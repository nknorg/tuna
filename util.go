package tuna

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"

	"github.com/golang/protobuf/proto"
	"github.com/nknorg/nkn/v2/common"
	"github.com/nknorg/tuna/pb"
	"github.com/xtaci/smux"
)

var encryptionAlgoMap = map[string]pb.EncryptionAlgo{
	"none":              pb.EncryptionAlgo_ENCRYPTION_NONE,
	"xsalsa20-poly1305": pb.EncryptionAlgo_ENCRYPTION_XSALSA20_POLY1305,
	"aes-gcm":           pb.EncryptionAlgo_ENCRYPTION_AES_GCM,
}

// OnConnectFunc is a wrapper type for gomobile compatibility.
type OnConnectFunc interface{ OnConnect() }

// OnConnect is a wrapper type for gomobile compatibility.
type OnConnect struct {
	C        chan struct{}
	Callback OnConnectFunc

	closeLock sync.RWMutex
	isClosed  bool
}

// NewOnConnect creates an OnConnect channel with a channel size and callback
// function.
func NewOnConnect(size int, cb OnConnectFunc) *OnConnect {
	return &OnConnect{
		C:        make(chan struct{}, size),
		Callback: cb,
	}
}

// Next waits and returns the next element from the channel.
func (c *OnConnect) Next() {
	<-c.C
	return
}

func (c *OnConnect) receive() {
	c.closeLock.RLock()
	if c.isClosed {
		c.closeLock.RUnlock()
		return
	}
	if c.Callback != nil {
		// RUnlock is called first to prevent OnConnect callback takeing long time
		c.closeLock.RUnlock()
		c.Callback.OnConnect()
	} else {
		select {
		case c.C <- struct{}{}:
		default:
		}
		c.closeLock.RUnlock()
	}
}

func (c *OnConnect) close() {
	c.closeLock.Lock()
	close(c.C)
	c.isClosed = true
	c.closeLock.Unlock()
}

func ParseEncryptionAlgo(encryptionAlgoStr string) (pb.EncryptionAlgo, error) {
	if encryptionAlgo, ok := encryptionAlgoMap[strings.ToLower(strings.TrimSpace(encryptionAlgoStr))]; ok {
		return encryptionAlgo, nil
	}
	return 0, fmt.Errorf("unknown encryption algo %v", encryptionAlgoStr)
}

func ParsePrice(priceStr string) (common.Fixed64, common.Fixed64, error) {
	price := strings.Split(priceStr, ",")
	entryToExitPrice, err := common.StringToFixed64(strings.Trim(price[0], " "))
	if err != nil {
		return 0, 0, err
	}
	var exitToEntryPrice common.Fixed64
	if len(price) > 1 {
		exitToEntryPrice, err = common.StringToFixed64(strings.Trim(price[1], " "))
		if err != nil {
			return 0, 0, err
		}
	} else {
		exitToEntryPrice = entryToExitPrice
	}
	return entryToExitPrice, exitToEntryPrice, nil
}

func ReadVarBytes(reader io.Reader, maxMsgSize uint32) ([]byte, error) {
	b := make([]byte, 4)
	_, err := io.ReadFull(reader, b)
	if err != nil {
		return nil, err
	}

	b = make([]byte, int(binary.LittleEndian.Uint32(b)))
	_, err = io.ReadFull(reader, b)
	if err != nil {
		return nil, err
	}

	return b, nil
}

func WriteVarBytes(writer io.Writer, b []byte) error {
	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, uint32(len(b)))

	_, err := writer.Write(lenBuf)
	if err != nil {
		return err
	}

	_, err = writer.Write(b)
	if err != nil {
		return err
	}

	return nil
}

func readConnMetadata(conn net.Conn) (*pb.ConnectionMetadata, error) {
	b, err := ReadVarBytes(conn, maxConnMetadataSize)
	if err != nil {
		return nil, err
	}

	connMetadata := &pb.ConnectionMetadata{}
	err = proto.Unmarshal(b, connMetadata)
	if err != nil {
		return nil, err
	}

	return connMetadata, nil
}

func writeConnMetadata(conn net.Conn, connMetadata *pb.ConnectionMetadata) error {
	b, err := proto.Marshal(connMetadata)
	if err != nil {
		return err
	}

	return WriteVarBytes(conn, b)
}

func readStreamMetadata(stream *smux.Stream) (*pb.StreamMetadata, error) {
	b, err := ReadVarBytes(stream, maxStreamMetadataSize)
	if err != nil {
		return nil, err
	}

	streamMetadata := &pb.StreamMetadata{}
	err = proto.Unmarshal(b, streamMetadata)
	if err != nil {
		return nil, err
	}

	return streamMetadata, nil
}

func writeStreamMetadata(stream *smux.Stream, streamMetadata *pb.StreamMetadata) error {
	b, err := proto.Marshal(streamMetadata)
	if err != nil {
		return err
	}

	err = WriteVarBytes(stream, b)
	if err != nil {
		return err
	}

	return nil
}
