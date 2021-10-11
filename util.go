package tuna

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"strings"
	"sync"

	"github.com/golang/protobuf/proto"
	"github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/nkn/v2/common"
	nknPb "github.com/nknorg/nkn/v2/pb"
	"github.com/nknorg/tuna/pb"
	"github.com/nknorg/tuna/storage"
	"github.com/xtaci/smux"
)

const (
	nodeRPCPort            = 30003
	randomIdentifierChars  = "abcdefghijklmnopqrstuvwxyz0123456789"
	randomIdentifierLength = 8
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

	msgSize := binary.LittleEndian.Uint32(b)
	if msgSize > maxMsgSize {
		return nil, fmt.Errorf("msg size %d is larger than %d bytes", msgSize, maxMsgSize)
	}

	b = make([]byte, msgSize)
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

// GetFavoriteSeedRPCServer wraps GetFavoriteSeedRPCServerContext with
// background context.
func GetFavoriteSeedRPCServer(path, filenamePrefix string, timeout int32) ([]string, error) {
	return GetFavoriteSeedRPCServerContext(context.Background(), path, filenamePrefix, timeout)
}

// GetFavoriteSeedRPCServerContext returns an array of node rpc address from
// favorite node file. Timeout is in unit of millisecond.
func GetFavoriteSeedRPCServerContext(ctx context.Context, path, filenamePrefix string, timeout int32) ([]string, error) {
	measureStorage := storage.NewMeasureStorage(path, filenamePrefix)
	err := measureStorage.Load()
	if err != nil {
		return nil, err
	}

	if measureStorage.FavoriteNodes.Len() == 0 {
		return nil, nil
	}

	var wg sync.WaitGroup
	var lock sync.Mutex
	rpcAddrs := make([]string, 0, measureStorage.FavoriteNodes.Len())

	for _, node := range measureStorage.FavoriteNodes.GetData() {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			nodeState, err := nkn.GetNodeStateContext(ctx, &nkn.RPCConfig{
				SeedRPCServerAddr: nkn.NewStringArray(addr),
				RPCTimeout:        timeout,
			})
			if err != nil {
				return
			}
			if nodeState.SyncState != nknPb.SyncState_name[int32(nknPb.SyncState_PERSIST_FINISHED)] {
				log.Printf("Skip rpc node %s in state %s\n", addr, nodeState.SyncState)
				return
			}
			lock.Lock()
			rpcAddrs = append(rpcAddrs, addr)
			lock.Unlock()
		}(fmt.Sprintf("http://%s:%d", node.(*storage.FavoriteNode).IP, nodeRPCPort))
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-done:
	}

	return rpcAddrs, nil
}

func randomIdentifier() string {
	b := make([]byte, randomIdentifierLength)
	for i := range b {
		b[i] = randomIdentifierChars[rand.Intn(len(randomIdentifierChars))]
	}
	return string(b)
}
