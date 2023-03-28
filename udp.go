package tuna

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	stream "github.com/nknorg/encrypted-stream"
	"github.com/nknorg/tuna/pb"
)

const (
	PrefixLen            = 4
	DefaultUDPBufferSize = 8192
	MaxUDPBufferSize     = 65527
)

type UDPConn interface {
	WriteMsgUDP(b, oob []byte, addr *net.UDPAddr) (n, oobn int, err error)
	ReadFromUDP(b []byte) (n int, addr *net.UDPAddr, err error)

	Close() error

	LocalAddr() net.Addr
	RemoteAddr() net.Addr

	SetWriteBuffer(bytes int) error
	SetReadBuffer(bytes int) error
}

type EncryptUDPConn struct {
	conn UDPConn

	encoders sync.Map
	decoders sync.Map

	lock     sync.RWMutex
	isClosed bool

	readLock  sync.Mutex
	writeLock sync.Mutex

	readBuffer  []byte
	writeBuffer []byte
}

func NewEncryptUDPConn(conn *net.UDPConn) *EncryptUDPConn {
	ec := &EncryptUDPConn{
		conn:        conn,
		readBuffer:  make([]byte, MaxUDPBufferSize),
		writeBuffer: make([]byte, MaxUDPBufferSize),
	}
	conn.SetReadBuffer(MaxUDPBufferSize)
	conn.SetWriteBuffer(MaxUDPBufferSize)
	return ec
}

func (ec *EncryptUDPConn) AddCodec(addr *net.UDPAddr, encryptKey *[32]byte, encryptionAlgo pb.EncryptionAlgo, initiator bool) error {
	var cipher stream.Cipher
	var err error
	switch encryptionAlgo {
	case pb.EncryptionAlgo_ENCRYPTION_NONE:
		cipher = nil
	case pb.EncryptionAlgo_ENCRYPTION_XSALSA20_POLY1305:
		cipher = stream.NewXSalsa20Poly1305Cipher(encryptKey)
	case pb.EncryptionAlgo_ENCRYPTION_AES_GCM:
		cipher, err = stream.NewAESGCMCipher(encryptKey[:])
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported encryption algo %v", encryptionAlgo)
	}
	encoder, err := stream.NewEncoder(cipher, initiator, false)
	if err != nil {
		return err
	}
	decoder, err := stream.NewDecoder(cipher, initiator, false, true)
	if err != nil {
		return err
	}

	ec.encoders.Store(addr.String(), encoder)
	ec.decoders.Store(addr.String(), decoder)
	return nil
}

func (ec *EncryptUDPConn) ReadFromUDP(b []byte) (n int, addr *net.UDPAddr, err error) {
	n, addr, _, err = ec.ReadFromUDPEncrypted(b)
	return
}

func (ec *EncryptUDPConn) ReadFromUDPEncrypted(b []byte) (n int, addr *net.UDPAddr, encrypted bool, err error) {
	if ec == nil {
		return 0, nil, false, fmt.Errorf("unconnected udp conn")
	}

	if ec.IsClosed() {
		return 0, nil, false, io.ErrClosedPipe
	}

	ec.readLock.Lock()
	defer ec.readLock.Unlock()

	n, addr, err = ec.conn.ReadFromUDP(ec.readBuffer)
	if err != nil {
		return 0, addr, false, err
	}

	d, ok := ec.decoders.Load(addr.String())
	if !ok {
		copy(b, ec.readBuffer[:n])
		return n, addr, false, nil
	}
	decoder := d.(*stream.Decoder)

	plain, err := decoder.Decode(b, ec.readBuffer[:n])
	if err != nil {
		return 0, addr, false, err
	}
	return len(plain), addr, true, nil
}

func (ec *EncryptUDPConn) WriteMsgUDP(b, oob []byte, addr *net.UDPAddr) (n, oobn int, err error) {
	n, oobn, _, err = ec.WriteMsgUDPEncrypted(b, oob, addr)
	return
}

func (ec *EncryptUDPConn) WriteMsgUDPEncrypted(b, oob []byte, addr *net.UDPAddr) (n, oobn int, encrypted bool, err error) {
	if ec == nil {
		return 0, 0, false, fmt.Errorf("unconnected udp conn")
	}

	if ec.IsClosed() {
		return 0, 0, false, io.ErrClosedPipe
	}

	ec.writeLock.Lock()
	defer ec.writeLock.Unlock()

	var k string
	var ciphertext []byte
	encrypted = false

	if addr == nil {
		k = ec.RemoteUDPAddr().String()
	} else {
		k = addr.String()
	}
	e, ok := ec.encoders.Load(k)
	if !ok {
		ciphertext = b
	} else {
		encoder := e.(*stream.Encoder)
		ciphertext, err = encoder.Encode(ec.writeBuffer, b)
		if err != nil {
			return 0, 0, false, err
		}
		encrypted = true
	}

	n, oobn, err = ec.conn.WriteMsgUDP(ciphertext, oob, addr)
	if err != nil {
		return 0, 0, false, err
	}
	if n != len(ciphertext) {
		return 0, 0, false, io.ErrShortWrite
	}

	return len(b), oobn, encrypted, err
}

func (ec *EncryptUDPConn) SetWriteBuffer(size int) error {
	return ec.conn.SetWriteBuffer(size)
}

func (ec *EncryptUDPConn) SetReadBuffer(size int) error {
	return ec.conn.SetReadBuffer(size)
}

func (ec *EncryptUDPConn) LocalAddr() net.Addr {
	return ec.conn.LocalAddr()
}

func (ec *EncryptUDPConn) IsClosed() bool {
	ec.lock.RLock()
	defer ec.lock.RUnlock()
	return ec.isClosed
}

func (ec *EncryptUDPConn) Close() error {
	ec.lock.Lock()
	defer ec.lock.Unlock()
	ec.isClosed = true
	return ec.conn.Close()
}

func (ec *EncryptUDPConn) RemoteAddr() net.Addr {
	return ec.conn.RemoteAddr()
}

func (ec *EncryptUDPConn) RemoteUDPAddr() *net.UDPAddr {
	host, portStr, _ := net.SplitHostPort(ec.RemoteAddr().String())
	port, _ := strconv.Atoi(portStr)
	return &net.UDPAddr{IP: net.ParseIP(host), Port: port}
}

func (ec *EncryptUDPConn) SetDeadline(t time.Time) error {
	ec.lock.RLock()
	defer ec.lock.RUnlock()

	if ec.isClosed {
		return ErrClosed
	}

	c := ec.conn.(*net.UDPConn)
	return c.SetDeadline(t)
}

func (ec *EncryptUDPConn) SetReadDeadline(t time.Time) error {
	ec.lock.RLock()
	defer ec.lock.RUnlock()

	if ec.isClosed {
		return ErrClosed
	}

	c := ec.conn.(*net.UDPConn)
	return c.SetReadDeadline(t)
}

func (ec *EncryptUDPConn) SetWriteDeadline(t time.Time) error {
	ec.lock.RLock()
	defer ec.lock.RUnlock()

	if ec.isClosed {
		return ErrClosed
	}

	c := ec.conn.(*net.UDPConn)
	return c.SetWriteDeadline(t)
}
