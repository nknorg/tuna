package tuna

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/gogo/protobuf/proto"
	"github.com/nknorg/nkn/common"
	"github.com/nknorg/tuna/pb"
)

var encryptionAlgoMap = map[string]pb.EncryptionAlgo{
	"none":              pb.ENCRYPTION_NONE,
	"xsalsa20-poly1305": pb.ENCRYPTION_XSALSA20_POLY1305,
	"aes-gcm":           pb.ENCRYPTION_AES_GCM,
}

// OnConnectFunc is a wrapper type for gomobile compatibility.
type OnConnectFunc interface{ OnConnect() }

// OnConnect is a wrapper type for gomobile compatibility.
type OnConnect struct {
	C        chan struct{}
	Callback OnConnectFunc
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
	if c.Callback != nil {
		c.Callback.OnConnect()
	} else {
		select {
		case c.C <- struct{}{}:
		default:
		}
	}
}

func (c *OnConnect) close() {
	close(c.C)
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

func ReadVarBytes(reader io.Reader) ([]byte, error) {
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
	b, err := ReadVarBytes(conn)
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
