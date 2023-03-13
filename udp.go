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

	encoders map[string]*stream.Encoder
	decoders map[string]*stream.Decoder

	lock     sync.RWMutex
	isClosed bool

	readLock  sync.Mutex
	writeLock sync.Mutex

	readBuffer []byte
}

func NewEncryptUDPConn(conn *net.UDPConn) *EncryptUDPConn {
	ec := &EncryptUDPConn{
		conn:       conn,
		encoders:   make(map[string]*stream.Encoder),
		decoders:   make(map[string]*stream.Decoder),
		readBuffer: make([]byte, MaxUDPBufferSize),
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
		return nil
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
	ec.decoders[addr.String()] = decoder
	ec.encoders[addr.String()] = encoder
	return nil
}

func (ec *EncryptUDPConn) ReadFromUDP(b []byte) (n int, addr *net.UDPAddr, err error) {
	if ec == nil {
		return 0, nil, fmt.Errorf("unconnected udp conn")
	}

	if ec.IsClosed() {
		return 0, nil, io.ErrClosedPipe
	}

	ec.readLock.Lock()
	defer ec.readLock.Unlock()

	n, addr, err = ec.conn.ReadFromUDP(ec.readBuffer)
	if err != nil {
		return 0, nil, err
	}

	decoder := ec.decoders[addr.String()]
	if decoder == nil {
		copy(b, ec.readBuffer[:n])
		return n, addr, err
	}

	plain, err := decoder.Decode(b, ec.readBuffer[:n])
	if err != nil {
		return 0, nil, err
	}
	return len(plain), addr, nil
}

func (ec *EncryptUDPConn) WriteMsgUDP(b, oob []byte, addr *net.UDPAddr) (n, oobn int, err error) {
	if ec == nil {
		return 0, 0, fmt.Errorf("unconnected udp conn")
	}

	if ec.IsClosed() {
		return 0, 0, io.ErrClosedPipe
	}

	ec.writeLock.Lock()
	defer ec.writeLock.Unlock()

	k := addr.String()
	if addr == nil {
		k = ec.RemoteUDPAddr().String()
	}
	encoder, ok := ec.encoders[k]
	if !ok {
		remoteAddr := ec.conn.RemoteAddr()
		if k, ok := remoteAddr.(*net.UDPAddr); ok {
			encoder = ec.encoders[k.String()]
		}
	}

	var msgLen int
	ciphertext := make([]byte, MaxUDPBufferSize)
	if encoder != nil {
		tmp, err := encoder.Encode(ciphertext, b)
		if err != nil {
			return 0, 0, err
		}
		msgLen = len(tmp)
	} else {
		copy(ciphertext, b)
		msgLen = len(b)
	}

	n, oobn, err = ec.conn.WriteMsgUDP(ciphertext[:msgLen], oob, addr)

	if err != nil {
		return 0, 0, err
	}
	return len(b), oobn, err
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
