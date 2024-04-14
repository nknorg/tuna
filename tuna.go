package tuna

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/nkn/v2/common"
	"github.com/nknorg/nkn/v2/config"
	"github.com/nknorg/nkn/v2/crypto/ed25519"
	"github.com/nknorg/nkn/v2/transaction"
	"github.com/nknorg/nkn/v2/util"
	"github.com/nknorg/nkn/v2/util/address"
	"github.com/nknorg/nkn/v2/vault"
	"github.com/nknorg/tuna/filter"
	"github.com/nknorg/tuna/geo"
	"github.com/nknorg/tuna/pb"
	"github.com/nknorg/tuna/storage"
	"github.com/nknorg/tuna/types"
	tunaUtil "github.com/nknorg/tuna/util"
	"github.com/xtaci/smux"
	"golang.org/x/crypto/nacl/box"
	"google.golang.org/protobuf/proto"

	// blank import to prevent gomobile from being removed by go mod tidy and
	// causing gomobile compile error
	_ "golang.org/x/mobile/asset"
)

const (
	TrafficUnit = 1024 * 1024

	tcp4                          = "tcp"
	udp4                          = "udp"
	trafficPaymentThreshold       = 32
	maxTrafficUnpaid              = 1
	minTrafficCoverage            = 0.9
	trafficDelay                  = 10 * time.Second
	maxNanoPayDelay               = 30 * time.Second
	subscribeDurationRandomFactor = 0.1
	measureBandwidthTopCount      = 8
	measureDelayTopDelayCount     = 32
	pipeBufferSize                = 4096 // should be <= 4096 to be compatible with c++ smux
	maxConnMetadataSize           = 1024
	maxStreamMetadataSize         = 1024
	maxServiceMetadataSize        = 4096
	maxNanoPayTxnSize             = 4096
	numRPCClients                 = 4
	maxRPCRequests                = 8
)

var (
	// This lock makes sure that only one measurement can run at the same time if
	// measurement storage is set so that later measurement can take use of the
	// previous measurement results.
	measureStorageMutex sync.Mutex
)

type ServiceInfo struct {
	MaxPrice  string            `json:"maxPrice"`
	ListenIP  string            `json:"listenIP"`
	IPFilter  *geo.IPFilter     `json:"ipFilter"`
	NknFilter *filter.NknFilter `json:"nknFilter"`
}

type Service struct {
	Name          string   `json:"name"`
	TCP           []uint32 `json:"tcp"`
	UDP           []uint32 `json:"udp"`
	UDPBufferSize int      `json:"udpBufferSize"`
	Encryption    string   `json:"encryption"`
}

type Common struct {
	Service                        *Service
	ServiceInfo                    *ServiceInfo
	Wallet                         *nkn.Wallet
	Client                         *nkn.MultiClient
	DialTimeout                    int32
	SubscriptionPrefix             string
	Reverse                        bool
	ReverseMetadata                *pb.ServiceMetadata
	OnConnect                      *OnConnect
	IsServer                       bool
	GeoDBPath                      string
	DownloadGeoDB                  bool
	GetSubscribersBatchSize        int
	MeasureBandwidth               bool
	MeasureBandwidthTimeout        time.Duration
	MeasureBandwidthWorkersTimeout time.Duration
	MeasurementBytesDownLink       int32
	MeasureStoragePath             string
	MaxPoolSize                    int32
	TcpDialContext                 func(ctx context.Context, network, addr string) (net.Conn, error)
	HttpDialContext                func(ctx context.Context, network, addr string) (net.Conn, error)
	WsDialContext                  func(ctx context.Context, network, addr string) (net.Conn, error)

	udpReadChan                       chan []byte
	udpWriteChan                      chan []byte
	udpCloseChan                      chan struct{}
	tcpListener                       *net.TCPListener
	curveSecretKey                    *[sharedKeySize]byte
	encryptionAlgo                    pb.EncryptionAlgo
	closeChan                         chan struct{}
	measureStorage                    *storage.MeasureStorage
	sortMeasuredNodes                 func(types.Nodes)
	measureDelayConcurrentWorkers     int
	measureBandwidthConcurrentWorkers int
	sessionsWaitGroup                 *sync.WaitGroup

	sync.RWMutex
	udpReadWriteChanLock sync.RWMutex
	paymentReceiver      string
	entryToExitPrice     common.Fixed64
	exitToEntryPrice     common.Fixed64
	metadata             *pb.ServiceMetadata
	connected            bool
	tcpConn              net.Conn
	udpConn              *EncryptUDPConn
	isClosed             bool
	sharedKeys           map[string]*[sharedKeySize]byte
	encryptKeys          sync.Map
	remoteNknAddress     string
	activeSessions       int
	linger               time.Duration
	presetNode           *types.Node
	connReadyChan        sync.Map

	reverseBytesExitToEntry map[string][]uint64
	reverseBytesEntryToExit map[string][]uint64

	minBalance common.Fixed64 // minimum wallet balance requirement for connecting
}

func NewCommon(
	service *Service,
	serviceInfo *ServiceInfo,
	wallet *nkn.Wallet,
	client *nkn.MultiClient,
	seedRPCServerAddr []string,
	dialTimeout int32,
	subscriptionPrefix string,
	reverse, isServer bool,
	geoDBPath string,
	downloadGeoDB bool,
	getSubscribersBatchSize int32,
	measureBandwidth bool,
	measureBandwidthTimeout int32,
	measureBandwidthWorkersTimeout int32,
	measurementBytes int32,
	measureStoragePath string,
	maxPoolSize int32,
	tcpDialContext func(ctx context.Context, network, addr string) (net.Conn, error),
	httpDialContext func(ctx context.Context, network, addr string) (net.Conn, error),
	wsDialContext func(ctx context.Context, network, addr string) (net.Conn, error),
	sortMeasuredNodes func(types.Nodes),
	reverseMetadata *pb.ServiceMetadata,
	minBalance string,
) (*Common, error) {
	encryptionAlgo := defaultEncryptionAlgo
	var err error
	if service != nil && len(service.Encryption) > 0 {
		encryptionAlgo, err = ParseEncryptionAlgo(service.Encryption)
		if err != nil {
			return nil, err
		}
	}

	if client == nil {
		clientConfig := &nkn.ClientConfig{
			HttpDialContext: httpDialContext,
			WsDialContext:   wsDialContext,
		}
		if len(seedRPCServerAddr) > 0 {
			clientConfig.SeedRPCServerAddr = nkn.NewStringArray(seedRPCServerAddr...)
		}
		client, err = nkn.NewMultiClient(wallet.Account(), randomIdentifier(), numRPCClients, false, clientConfig)
		if err != nil {
			return nil, err
		}
	}

	var sk [ed25519.PrivateKeySize]byte
	copy(sk[:], ed25519.GetPrivateKeyFromSeed(wallet.Seed()))
	curveSecretKey := ed25519.PrivateKeyToCurve25519PrivateKey(&sk)

	measureDelayConcurrentWorkers := defaultMeasureDelayConcurrentWorkers
	if measureDelayConcurrentWorkers > int(maxPoolSize) {
		measureDelayConcurrentWorkers = int(maxPoolSize)
	}

	measureBandwidthConcurrentWorkers := defaultMeasureBandwidthConcurrentWorkers
	if measureBandwidthConcurrentWorkers > int(maxPoolSize) {
		measureBandwidthConcurrentWorkers = int(maxPoolSize)
	}

	var wg sync.WaitGroup
	c := &Common{
		Service:                        service,
		ServiceInfo:                    serviceInfo,
		Wallet:                         wallet,
		Client:                         client,
		DialTimeout:                    dialTimeout,
		SubscriptionPrefix:             subscriptionPrefix,
		Reverse:                        reverse,
		ReverseMetadata:                reverseMetadata,
		OnConnect:                      NewOnConnect(1, nil),
		IsServer:                       isServer,
		GeoDBPath:                      geoDBPath,
		DownloadGeoDB:                  downloadGeoDB,
		GetSubscribersBatchSize:        int(getSubscribersBatchSize),
		MeasureBandwidth:               measureBandwidth,
		MeasureBandwidthTimeout:        time.Duration(measureBandwidthTimeout) * time.Second,
		MeasureBandwidthWorkersTimeout: time.Duration(measureBandwidthWorkersTimeout) * time.Second,
		MeasurementBytesDownLink:       measurementBytes,
		MeasureStoragePath:             measureStoragePath,
		MaxPoolSize:                    maxPoolSize,
		TcpDialContext:                 tcpDialContext,
		HttpDialContext:                httpDialContext,
		WsDialContext:                  wsDialContext,

		curveSecretKey:                    curveSecretKey,
		encryptionAlgo:                    encryptionAlgo,
		closeChan:                         make(chan struct{}),
		udpCloseChan:                      make(chan struct{}),
		sharedKeys:                        make(map[string]*[sharedKeySize]byte),
		measureDelayConcurrentWorkers:     measureDelayConcurrentWorkers,
		measureBandwidthConcurrentWorkers: measureBandwidthConcurrentWorkers,
		sortMeasuredNodes:                 sortMeasuredNodes,
		sessionsWaitGroup:                 &wg,

		reverseBytesEntryToExit: make(map[string][]uint64),
		reverseBytesExitToEntry: make(map[string][]uint64),

		udpReadChan:  make(chan []byte, 64),
		udpWriteChan: make(chan []byte, 64),
	}
	c.minBalance, err = common.StringToFixed64(minBalance)
	if err != nil {
		return nil, err
	}

	if !c.IsServer && c.ServiceInfo.IPFilter.NeedGeoInfo() {
		c.ServiceInfo.IPFilter.AddProvider(c.DownloadGeoDB, c.GeoDBPath)
	}

	if !c.IsServer && c.MeasureStoragePath != "" {
		c.measureStorage = storage.NewMeasureStorage(c.MeasureStoragePath, c.SubscriptionPrefix+c.Service.Name)
	}

	return c, nil
}

func (c *Common) GetTCPConn() net.Conn {
	c.RLock()
	defer c.RUnlock()
	return c.tcpConn
}

func (c *Common) SetServerTCPConn(conn net.Conn) {
	c.Lock()
	defer c.Unlock()
	c.tcpConn = conn
}

func (c *Common) GetUDPConn() *EncryptUDPConn {
	c.RLock()
	defer c.RUnlock()
	return c.udpConn
}

func (c *Common) SetServerUDPConn(conn *EncryptUDPConn) {
	c.Lock()
	defer c.Unlock()
	c.udpConn = conn
}

func (c *Common) GetConnected() bool {
	c.RLock()
	defer c.RUnlock()
	return c.connected
}

func (c *Common) SetConnected(connected bool) {
	c.Lock()
	defer c.Unlock()
	c.connected = connected
}

func (c *Common) GetServerTCPConn(force bool) (net.Conn, error) {
	err := c.CreateServerConn(force)
	if err != nil {
		return nil, err
	}
	conn := c.GetTCPConn()
	if conn == nil {
		return nil, errors.New("nil tcp connection")
	}
	return conn, nil
}

func (c *Common) GetServerUDPConn(force bool) (UDPConn, error) {
	err := c.CreateServerConn(force)
	if err != nil {
		return nil, err
	}
	return c.GetUDPConn(), nil
}

func (c *Common) SetServerUDPReadChan(udpReadChan chan []byte) {
	c.udpReadChan = udpReadChan
}

func (c *Common) SetServerUDPWriteChan(udpWriteChan chan []byte) {
	c.udpWriteChan = udpWriteChan
}

func (c *Common) GetServerUDPReadChan(force bool) (chan []byte, error) {
	c.udpReadWriteChanLock.Lock()
	defer c.udpReadWriteChanLock.Unlock()
	err := c.CreateServerConn(force)
	if err != nil {
		return nil, err
	}
	return c.udpReadChan, nil
}

func (c *Common) GetServerUDPWriteChan(force bool) (chan []byte, error) {
	c.udpReadWriteChanLock.Lock()
	defer c.udpReadWriteChanLock.Unlock()
	err := c.CreateServerConn(force)
	if err != nil {
		return nil, err
	}
	return c.udpWriteChan, nil
}

func (c *Common) GetMetadata() *pb.ServiceMetadata {
	c.RLock()
	defer c.RUnlock()
	return c.metadata
}

func (c *Common) SetMetadata(metadata *pb.ServiceMetadata) {
	c.Lock()
	defer c.Unlock()
	c.metadata = metadata
}

func (c *Common) GetRemoteNknAddress() string {
	c.RLock()
	defer c.RUnlock()
	return c.remoteNknAddress
}

func (c *Common) SetRemoteNknAddress(nknAddr string) {
	c.Lock()
	c.remoteNknAddress = nknAddr
	c.Unlock()
}

func (c *Common) GetPaymentReceiver() string {
	c.RLock()
	defer c.RUnlock()
	return c.paymentReceiver
}

func (c *Common) SetPaymentReceiver(paymentReceiver string) error {
	if len(paymentReceiver) > 0 {
		if err := nkn.VerifyWalletAddress(paymentReceiver); err != nil {
			return err
		}
	}
	c.Lock()
	defer c.Unlock()
	c.paymentReceiver = paymentReceiver
	return nil
}

func (c *Common) GetPrice() (common.Fixed64, common.Fixed64) {
	c.Lock()
	defer c.Unlock()
	return c.entryToExitPrice, c.exitToEntryPrice
}

func (c *Common) startUDPReaderWriter(conn *EncryptUDPConn, toAddr *net.UDPAddr, in *uint64, out *uint64) {
	from := new(net.UDPAddr)
	n := 0
	encrypted := false
	var err error
	addrToKey := new(sync.Map)

	go func() {
		buffer := make([]byte, MaxUDPBufferSize)
		for {
			if c.isClosed {
				return
			}
			n, from, encrypted, err = conn.ReadFromUDPEncrypted(buffer)
			if err != nil {
				log.Println("Couldn't receive data:", err)
				if errors.Is(err, io.ErrClosedPipe) {
					return
				}
			}
			if bytes.Equal(buffer[:PrefixLen], []byte{PrefixLen - 1: 0}) && c.IsServer && n > PrefixLen {
				connMetadata, err := parseUDPConnMetadata(buffer[PrefixLen:n])
				if err != nil {
					log.Println("Couldn't read udp metadata from client:", err)
					continue
				}
				if connMetadata.IsPing || encrypted {
					continue
				}
				connKey := string(append(connMetadata.PublicKey, connMetadata.Nonce...))

				readyChan, _ := c.connReadyChan.LoadOrStore(connKey, make(chan struct{}, 1))
				<-readyChan.(chan struct{})

				encryptKey, ok := c.encryptKeys.Load(connKey)
				if !ok {
					log.Println("no encrypt key found")
					continue
				}
				k := encryptKey.(*[encryptKeySize]byte)
				err = conn.AddCodec(from, k, connMetadata.EncryptionAlgo, false)
				if err != nil {
					log.Println(err)
					continue
				}

				if in == nil && out == nil {
					k := string(append(connMetadata.PublicKey, connMetadata.Nonce...))
					addrToKey.Store(from.String(), k)
				}
				continue
			}

			if !encrypted {
				log.Println("Unencrypted udp packet received")
				continue
			}

			if n > 0 {
				b := make([]byte, n)
				copy(b, buffer[:n])
				c.udpReadChan <- b

				if in != nil {
					atomic.AddUint64(in, uint64(n))
				} else {
					k, ok := addrToKey.Load(from.String())
					if ok {
						atomic.AddUint64(&c.reverseBytesEntryToExit[k.(string)][b[2]], uint64(n))
					}
				}
			}
		}
	}()

	go func() {
		for {
			if c.isClosed {
				return
			}
			to := toAddr
			select {
			case data := <-c.udpWriteChan:
				if conn.RemoteAddr() == nil && from != nil && toAddr == nil {
					to = from
				}
				n, _, err := conn.WriteMsgUDP(data, nil, to)
				if err != nil {
					log.Println("Couldn't send data to server:", err)
					continue
				}
				if out != nil {
					atomic.AddUint64(out, uint64(n))
				} else {
					k, ok := addrToKey.Load(from.String())
					if ok {
						atomic.AddUint64(&c.reverseBytesExitToEntry[k.(string)][data[2]], uint64(n))
					}
				}
			case <-c.udpCloseChan:
				return
			}
		}
	}()
}

func (c *Common) getOrComputeSharedKey(remotePublicKey []byte) (*[sharedKeySize]byte, error) {
	c.RLock()
	sharedKey, ok := c.sharedKeys[string(remotePublicKey)]
	c.RUnlock()
	if ok && sharedKey != nil {
		return sharedKey, nil
	}

	var pk [ed25519.PublicKeySize]byte
	copy(pk[:], remotePublicKey)
	curve25519PublicKey, ok := ed25519.PublicKeyToCurve25519PublicKey(&pk)
	if !ok {
		return nil, errors.New("invalid public key")
	}

	sharedKey = new([sharedKeySize]byte)
	box.Precompute(sharedKey, curve25519PublicKey, c.curveSecretKey)

	c.Lock()
	c.sharedKeys[string(remotePublicKey)] = sharedKey
	c.Unlock()

	return sharedKey, nil
}

func (c *Common) wrapConn(conn net.Conn, remotePublicKey []byte, localConnMetadata *pb.ConnectionMetadata) (net.Conn, *pb.ConnectionMetadata, error) {
	var connNonce []byte
	var encryptionAlgo pb.EncryptionAlgo
	var remoteConnMetadata *pb.ConnectionMetadata
	if localConnMetadata == nil {
		localConnMetadata = &pb.ConnectionMetadata{}
	} else {
		connMetadataCopy := *localConnMetadata
		localConnMetadata = &connMetadataCopy
	}

	err := conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err != nil {
		return nil, nil, err
	}

	defer conn.SetDeadline(time.Time{})

	if len(remotePublicKey) > 0 {
		encryptionAlgo = c.encryptionAlgo
		localConnMetadata.EncryptionAlgo = encryptionAlgo
		localConnMetadata.PublicKey = c.Wallet.PubKey()

		err := writeConnMetadata(conn, localConnMetadata)
		if err != nil {
			return nil, nil, err
		}

		remoteConnMetadata, err = readConnMetadata(conn)
		if err != nil {
			return nil, nil, err
		}
		if !bytes.Equal(remoteConnMetadata.PublicKey, remotePublicKey) {
			return nil, nil, fmt.Errorf("public key mismatch")
		}
		connNonce = remoteConnMetadata.Nonce
	} else {
		connNonce = util.RandomBytes(connNonceSize)
		localConnMetadata.Nonce = connNonce
		localConnMetadata.PublicKey = c.Wallet.PubKey()

		err := writeConnMetadata(conn, localConnMetadata)
		if err != nil {
			return nil, nil, err
		}

		remoteConnMetadata, err = readConnMetadata(conn)
		if err != nil {
			return nil, nil, err
		}
		remoteConnMetadata.Nonce = connNonce

		if len(remoteConnMetadata.PublicKey) != ed25519.PublicKeySize {
			return nil, nil, fmt.Errorf("invalid pubkey size %d", len(remoteConnMetadata.PublicKey))
		}

		encryptionAlgo = remoteConnMetadata.EncryptionAlgo
		remotePublicKey = remoteConnMetadata.PublicKey
	}

	k := string(append(remotePublicKey, connNonce...))
	encryptKey := new([encryptKeySize]byte)
	if encryptionAlgo != pb.EncryptionAlgo_ENCRYPTION_NONE {
		sharedKey, err := c.getOrComputeSharedKey(remotePublicKey)
		if err != nil {
			return nil, nil, err
		}

		encryptKey = computeEncryptKey(connNonce, sharedKey[:])
	}
	c.encryptKeys.Store(k, encryptKey)

	if c.IsServer {
		readyChan, _ := c.connReadyChan.LoadOrStore(k, make(chan struct{}, 1))
		select {
		case readyChan.(chan struct{}) <- struct{}{}:
		default:
		}
	}

	if encryptionAlgo == pb.EncryptionAlgo_ENCRYPTION_NONE {
		return conn, remoteConnMetadata, nil
	}

	encryptedConn, err := encryptConn(conn, encryptKey, encryptionAlgo, len(remotePublicKey) > 0)
	if err != nil {
		return nil, nil, err
	}

	return encryptedConn, remoteConnMetadata, nil
}

func (c *Common) wrapUDPConn(conn UDPConn, addr *net.UDPAddr, remotePublicKey []byte, connNonce []byte) (*EncryptUDPConn, error) {
	localConnMetadata := new(pb.ConnectionMetadata)
	var err error
	var encryptionAlgo pb.EncryptionAlgo
	encConn := new(EncryptUDPConn)
	encryptionAlgo = c.encryptionAlgo

	conn.SetWriteBuffer(MaxUDPBufferSize)
	conn.SetReadBuffer(MaxUDPBufferSize)

	if c.IsServer {
		encConn = conn.(*EncryptUDPConn)
	} else {
		encConn = NewEncryptUDPConn(conn.(*net.UDPConn))
	}

	if len(remotePublicKey) > 0 {
		localConnMetadata.EncryptionAlgo = c.encryptionAlgo
		localConnMetadata.PublicKey = c.Wallet.PubKey()
		localConnMetadata.Nonce = connNonce
		for i := 0; i < 3; i++ {
			err = writeUDPConnMetadata(conn, nil, localConnMetadata)
			if err != nil {
				return nil, err
			}
		}

		encryptKey, ok := c.encryptKeys.Load(string(append(remotePublicKey, connNonce...)))
		if !ok || encryptKey == nil {
			return nil, fmt.Errorf("encrypted key for UDP conn not found")
		}
		k := encryptKey.(*[encryptKeySize]byte)
		err = encConn.AddCodec(addr, k, encryptionAlgo, true)
		if err != nil {
			return nil, err
		}
	}

	return encConn, nil
}

func (c *Common) UpdateServerConn(remotePublicKey []byte) error {
	hasUDP := len(c.Service.UDP) > 0 || (c.ReverseMetadata != nil && len(c.ReverseMetadata.ServiceUdp) > 0)
	metadata := c.GetMetadata()

	Close(c.GetTCPConn())

	addr := metadata.Ip + ":" + strconv.Itoa(int(metadata.TcpPort))
	var tcpConn net.Conn
	var err error
	if c.TcpDialContext != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(c.DialTimeout)*time.Second)
		defer cancel()
		tcpConn, err = c.TcpDialContext(ctx, tcp4, addr)
	} else {
		tcpConn, err = net.DialTimeout(
			tcp4,
			addr,
			time.Duration(c.DialTimeout)*time.Second,
		)
	}
	if err != nil {
		return err
	}

	encryptedConn, remoteMetadata, err := c.wrapConn(tcpConn, remotePublicKey, nil)
	if err != nil {
		Close(tcpConn)
		return err
	}

	c.SetServerTCPConn(encryptedConn)

	log.Println("Connected to TCP at", addr)

	if hasUDP {
		oldConn := c.GetUDPConn()
		Close(oldConn)

		addr := &net.UDPAddr{IP: net.ParseIP(metadata.Ip), Port: int(metadata.UdpPort)}
		udpConn, err := net.DialUDP(
			udp4,
			nil,
			addr,
		)
		if err != nil {
			return err
		}
		uConn, err := c.wrapUDPConn(udpConn, addr, remotePublicKey, remoteMetadata.Nonce)
		if err != nil {
			return err
		}
		c.SetServerUDPConn(uConn)
		log.Println("Connected to UDP at", addr.String())
	}

	c.SetConnected(true)

	c.OnConnect.receive()

	return nil
}

func (c *Common) CreateServerConn(force bool) error {
	if !c.IsServer && (!c.GetConnected() || force) {
		for {
			if c.isClosed {
				return ErrClosed
			}

			err := c.SetPaymentReceiver("")
			if err != nil {
				return err
			}

			if c.minBalance > 0 {
				entryToExitMaxPrice, exitToEntryMaxPrice, err := ParsePrice(c.ServiceInfo.MaxPrice)
				if err != nil {
					return err
				}
				if entryToExitMaxPrice > 0 || exitToEntryMaxPrice > 0 {
					balance, err := c.Client.BalanceByAddress(c.Wallet.Address())
					if err != nil {
						log.Println("tuna.CreateServerConn BalanceByAddress error:", err)
					} else {
						if balance.ToFixed64() < c.minBalance {
							return nkn.ErrInsufficientBalance
						}
					}
				}
			}

			candidateSubs, err := c.GetTopPerformanceNodes(c.MeasureBandwidth, measureBandwidthTopCount)
			if err != nil {
				log.Println(err)
				time.Sleep(time.Second)
				continue
			}

			for _, subscriber := range candidateSubs {
				metadata := subscriber.Metadata
				if c.presetNode == nil {
					subscription, err := c.Client.GetSubscription(c.SubscriptionPrefix+c.Service.Name, subscriber.Address)
					if err == nil {
						latestMeta, err := ReadMetadata(subscription.Meta)
						if err == nil {
							metadata = latestMeta
						} else {
							log.Println(err)
						}
					} else {
						log.Println(err)
					}
				}

				c.SetMetadata(metadata)

				log.Printf("IP: %s, address: %s, delay: %.3f ms, bandwidth: %f KB/s", metadata.Ip, subscriber.Address, subscriber.Delay, subscriber.Bandwidth/1024)

				entryToExitPrice, exitToEntryPrice, err := ParsePrice(metadata.Price)
				if err != nil {
					log.Println(err)
					continue
				}

				if len(metadata.BeneficiaryAddr) > 0 {
					err = c.SetPaymentReceiver(metadata.BeneficiaryAddr)
					if err != nil {
						log.Println(err)
						continue
					}
				} else {
					addr, err := nkn.ClientAddrToWalletAddr(subscriber.Address)
					if err != nil {
						log.Println(err)
						continue
					}

					err = c.SetPaymentReceiver(addr)
					if err != nil {
						log.Println(err)
						continue
					}
				}
				c.Lock()
				c.remoteNknAddress = subscriber.Address
				c.entryToExitPrice = entryToExitPrice
				c.exitToEntryPrice = exitToEntryPrice
				if c.ReverseMetadata != nil {
					c.metadata.ServiceTcp = c.ReverseMetadata.ServiceTcp
					c.metadata.ServiceUdp = c.ReverseMetadata.ServiceUdp
				}
				c.Unlock()
				remotePublicKey, err := nkn.ClientAddrToPubKey(subscriber.Address)
				if err != nil {
					log.Println(err)
					continue
				}

				err = c.UpdateServerConn(remotePublicKey)
				if err != nil {
					log.Println(err)
					time.Sleep(time.Second)
					continue
				}

				return nil
			}
		}
	}

	return nil
}

func (c *Common) GetTopPerformanceNodes(measureBandwidth bool, n int) (types.Nodes, error) {
	if c.presetNode != nil {
		return types.Nodes{c.presetNode}, nil
	}
	return c.GetTopPerformanceNodesContext(context.Background(), measureBandwidth, n)
}

func (c *Common) GetTopPerformanceNodesContext(ctx context.Context, measureBandwidth bool, n int) (types.Nodes, error) {
	if c.ServiceInfo.IPFilter != nil && len(c.ServiceInfo.IPFilter.GetProviders()) > 0 {
		c.ServiceInfo.IPFilter.UpdateDataFileContext(ctx)
	}

	if c.measureStorage != nil {
		measureStorageMutex.Lock()
		defer measureStorageMutex.Unlock()

		err := c.measureStorage.Load()
		if err != nil {
			return nil, err
		}
	}

	var filterSubs types.Nodes
	allSubscribers, subscriberRaw, err := c.nknFilterContext(ctx)
	if err != nil {
		return nil, err
	}

	filterSubs = c.filterSubscribers(allSubscribers, subscriberRaw)

	var candidateSubs types.Nodes
	if len(filterSubs) == 0 {
		return nil, nil
	} else if len(filterSubs) == 1 {
		candidateSubs = filterSubs
	} else {
		delayMeasuredSubs := measureDelay(ctx, filterSubs, c.measureDelayConcurrentWorkers, measureDelayTopDelayCount, defaultMeasureDelayTimeout, c.TcpDialContext)
		if measureBandwidth {
			candidateSubs = c.measureBandwidth(ctx, delayMeasuredSubs, n, c.MeasureBandwidthWorkersTimeout)
		} else {
			length := n
			if length > len(delayMeasuredSubs) {
				length = len(delayMeasuredSubs)
			}
			candidateSubs = delayMeasuredSubs[:length]
		}
	}

	if c.sortMeasuredNodes != nil {
		c.sortMeasuredNodes(candidateSubs)
	}

	return candidateSubs, nil
}

func (c *Common) nknFilter() ([]string, map[string]string, error) {
	return c.nknFilterContext(context.Background())
}

func (c *Common) nknFilterContext(ctx context.Context) ([]string, map[string]string, error) {
	topic := c.SubscriptionPrefix + c.Service.Name
	var allSubscribers []string
	var subscriberRaw map[string]string

	if c.ServiceInfo.NknFilter != nil && len(c.ServiceInfo.NknFilter.Allow) > 0 {
		nknFilterLength := len(c.ServiceInfo.NknFilter.Allow)
		subscriberRaw = make(map[string]string, nknFilterLength)
		allSubscribers = make([]string, 0, nknFilterLength)
		for _, f := range c.ServiceInfo.NknFilter.Allow {
			if len(f.Metadata) > 0 {
				subscriberRaw[f.Address] = f.Metadata
			} else {
				subscription, err := c.Client.GetSubscriptionContext(ctx, topic, f.Address)
				if err != nil {
					log.Println(err)
					continue
				}
				subscriberRaw[f.Address] = subscription.Meta
			}
			allSubscribers = append(allSubscribers, f.Address)
		}
		if len(allSubscribers) == 0 {
			return nil, nil, errors.New("none of the NKN address whitelist can provide service")
		}
	} else {
		// check if there is at least one service provider with low cost
		subscribers, err := c.Client.GetSubscribersContext(ctx, topic, 0, c.GetSubscribersBatchSize, false, false, nil)
		if err != nil {
			return nil, nil, err
		}
		if subscribers.Subscribers.Len() == 0 {
			return nil, nil, errors.New("there is no service providers for " + c.Service.Name)
		}

		var allPrefix [][]byte
		if subscribers.Subscribers.Len() < c.GetSubscribersBatchSize {
			allPrefix = make([][]byte, 1)
		} else {
			allPrefix = make([][]byte, 256)
			for i := 0; i < 256; i++ {
				allPrefix[i] = []byte{byte(i)}
			}
		}

		rand.Shuffle(len(allPrefix), func(i, j int) {
			allPrefix[i], allPrefix[j] = allPrefix[j], allPrefix[i]
		})

		subscriberRaw = make(map[string]string)
		subscriberCount := 0
		for i := 0; i < len(allPrefix); i++ {
			count, err := c.Client.GetSubscribersCountContext(ctx, topic, allPrefix[i])
			if err != nil {
				return nil, nil, err
			}

			if count > 0 {
				offset := rand.Intn((count-1)/c.GetSubscribersBatchSize + 1)
				subscribers, err := c.Client.GetSubscribersContext(ctx, topic, offset*c.GetSubscribersBatchSize, c.GetSubscribersBatchSize, true, false, allPrefix[i])
				if err != nil {
					return nil, nil, err
				}

				for subscriber, meta := range subscribers.Subscribers.Map() {
					if _, ok := subscriberRaw[subscriber]; !ok {
						subscriberRaw[subscriber] = meta
						subscriberCount++
					}
				}
				if subscriberCount >= c.GetSubscribersBatchSize {
					break
				}
			}

			if i+maxRPCRequests < len(allPrefix) {
				estimatedRemainingRequests := float64(c.GetSubscribersBatchSize-subscriberCount) / (float64(subscriberCount+1) / float64(i+1))
				if estimatedRemainingRequests > maxRPCRequests {
					i = len(allPrefix) - 1
					allPrefix = append(allPrefix, nil)
				}
			}
		}

		if c.measureStorage != nil {
			nodes := c.measureStorage.FavoriteNodes.GetData()
			for _, v := range nodes {
				item := v.(*storage.FavoriteNode)
				subscriberRaw[item.Address] = item.Metadata
				log.Printf("Use favorite node: %s", item.IP)
			}
		}

		allSubscribers = make([]string, 0, len(subscriberRaw))
		for subscriber := range subscriberRaw {
			allSubscribers = append(allSubscribers, subscriber)
		}
	}

	return allSubscribers, subscriberRaw, nil
}

func (c *Common) filterSubscribers(allSubscribers []string, subscriberRaw map[string]string) types.Nodes {
	entryToExitMaxPrice, exitToEntryMaxPrice, err := ParsePrice(c.ServiceInfo.MaxPrice)
	if err != nil {
		log.Fatalf("Parse price of service error: %v", err)
	}
	filterSubs := make(types.Nodes, 0, len(allSubscribers))

	var nodes []*net.IPNet
	if c.measureStorage != nil {
		nodes = c.measureStorage.GetAvoidCIDR()
	}

	for _, subscriber := range allSubscribers {
		metadataString := subscriberRaw[subscriber]
		metadata, err := ReadMetadata(metadataString)
		if err != nil {
			log.Println("Couldn't unmarshal metadata:", err)
			continue
		}
		entryToExitPrice, exitToEntryPrice, err := ParsePrice(metadata.Price)
		if err != nil {
			log.Println(err)
			continue
		}
		if entryToExitPrice > entryToExitMaxPrice || exitToEntryPrice > exitToEntryMaxPrice {
			continue
		}

		if !c.ServiceInfo.NknFilter.IsAllow(&filter.NknClient{Address: subscriber}) {
			continue
		}

		res, err := c.ServiceInfo.IPFilter.AllowIP(metadata.Ip)
		if err != nil {
			log.Println(err)
		}
		if !res {
			continue
		}

		if c.measureStorage != nil { // disallow avoid nodes
			for _, ip := range nodes {
				if ip.Contains(net.ParseIP(metadata.Ip)) {
					log.Printf("disallow avoid subnet: %s, ip: %s", ip.String(), metadata.Ip)
					continue
				}
			}
		}

		filterSubs = append(filterSubs, &types.Node{
			Address:     subscriber,
			Metadata:    metadata,
			MetadataRaw: metadataString,
		})
	}

	return filterSubs
}

func measureDelay(ctx context.Context, nodes types.Nodes, concurrentWorkers, numResults int, timeout time.Duration, dialContext func(ctx context.Context, network, addr string) (net.Conn, error)) types.Nodes {
	timeStart := time.Now()
	var lock sync.Mutex
	delayMeasuredSubs := make(types.Nodes, 0, len(nodes))
	wg := &sync.WaitGroup{}
	var measurementDelayJobChan = make(chan tunaUtil.Job, 1)
	go tunaUtil.WorkPool(concurrentWorkers, measurementDelayJobChan, wg)
	for index := range nodes {
		func(node *types.Node) {
			wg.Add(1)
			tunaUtil.Enqueue(measurementDelayJobChan, func() {
				addr := node.Metadata.Ip + ":" + strconv.Itoa(int(node.Metadata.TcpPort))
				delay, err := tunaUtil.DelayMeasurementContext(ctx, tcp4, addr, timeout, dialContext)
				if err != nil {
					var e net.Error
					if !errors.As(err, &e) {
						log.Println(err)
					}
					return
				}
				node.Delay = float32(delay) / float32(time.Millisecond)
				lock.Lock()
				delayMeasuredSubs = append(delayMeasuredSubs, node)
				lock.Unlock()
			})
		}(nodes[index])
	}
	wg.Wait()
	measureDelayTime := time.Since(timeStart)
	log.Printf("Measure delay: total use %s\n", measureDelayTime)

	close(measurementDelayJobChan)

	sort.Sort(types.SortByDelay{Nodes: delayMeasuredSubs})

	if len(delayMeasuredSubs) > numResults {
		delayMeasuredSubs = delayMeasuredSubs[:numResults]
	}

	return delayMeasuredSubs
}

func (c *Common) measureBandwidth(ctx context.Context, nodes types.Nodes, n int, timeout time.Duration) types.Nodes {
	timeStart := time.Now()

	var resLock sync.Mutex
	bandwidthMeasuredSubs := make(types.Nodes, 0, len(nodes))

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	wg := &sync.WaitGroup{}
	var measurementBandwidthJobChan = make(chan tunaUtil.Job, 1)

	go tunaUtil.WorkPool(c.measureBandwidthConcurrentWorkers, measurementBandwidthJobChan, wg)

	for i := range nodes {
		wg.Add(1)
		sub := nodes[i]
		tunaUtil.Enqueue(measurementBandwidthJobChan, func() {
			remotePublicKey, err := nkn.ClientAddrToPubKey(sub.Address)
			if err != nil {
				log.Println(err)
				return
			}

			d := net.Dialer{Timeout: defaultMeasureDelayTimeout}
			addr := sub.Metadata.Ip + ":" + strconv.Itoa(int(sub.Metadata.TcpPort))
			var dialContext = d.DialContext
			if c.TcpDialContext != nil {
				dialContext = c.TcpDialContext
			}
			conn, err := dialContext(ctx, tcp4, addr)
			if err != nil {
				var e net.Error
				if !errors.As(err, &e) {
					log.Println(err)
				}
				return
			}

			go func() {
				<-ctx.Done()
				conn.SetDeadline(time.Now())
			}()

			encryptedConn, _, err := c.wrapConn(conn, remotePublicKey, &pb.ConnectionMetadata{
				IsMeasurement:            true,
				MeasurementBytesDownlink: uint32(c.MeasurementBytesDownLink),
			})
			if err != nil {
				select {
				case <-ctx.Done():
				default:
					log.Println(err)
				}
				conn.Close()
				return
			}
			defer encryptedConn.Close()

			timeStart := time.Now()
			min, max, err := tunaUtil.BandwidthMeasurementClientContext(ctx, encryptedConn, int(c.MeasurementBytesDownLink), c.MeasureBandwidthTimeout)
			dur := time.Since(timeStart)
			if err != nil {
				select {
				case <-ctx.Done():
				default:
					if c.measureStorage != nil {
						c.measureStorage.AddAvoidNode(sub.Metadata.Ip, &storage.AvoidNode{
							IP:      sub.Metadata.Ip,
							Address: sub.Address,
						})
						err = c.measureStorage.SaveAvoidNodes()
						if err != nil {
							log.Println(err)
						}
						log.Printf("Add avoid node: %s", sub.Metadata.Ip)
					}
				}
				return
			}

			log.Printf("Address: %s, bandwidth: %f - %f KB/s, time: %s", addr, min/1024, max/1024, dur)

			if c.measureStorage != nil {
				metadata, err := proto.Marshal(sub.Metadata)
				if err != nil {
					log.Println(err)
				} else {
					metadataString := base64.StdEncoding.EncodeToString(metadata)
					updated := c.measureStorage.AddFavoriteNode(sub.Metadata.Ip, &storage.FavoriteNode{
						IP:           sub.Metadata.Ip,
						Address:      sub.Address,
						Metadata:     metadataString,
						Delay:        sub.Delay,
						MinBandwidth: min / 1024,
						MaxBandwidth: max / 1024,
					})
					if updated {
						err = c.measureStorage.SaveFavoriteNodes()
						if err != nil {
							log.Println(err)
						}
						log.Printf("Add favorite node: %s", sub.Metadata.Ip)
					}
				}
			}

			sub.Bandwidth = min
			resLock.Lock()
			bandwidthMeasuredSubs = append(bandwidthMeasuredSubs, sub)
			if len(bandwidthMeasuredSubs) >= n {
				log.Println("Collected enough results, cancel bandwidth measurement.")
				cancel()
			}
			resLock.Unlock()
		})
	}
	wg.Wait()

	measureBandwidthTime := time.Since(timeStart)
	log.Printf("Measure bandwidth: total use %s\n", measureBandwidthTime)

	close(measurementBandwidthJobChan)

	sort.Sort(types.SortByBandwidth{Nodes: bandwidthMeasuredSubs})

	return bandwidthMeasuredSubs
}

func (c *Common) startPayment(
	bytesEntryToExitUsed, bytesExitToEntryUsed *uint64,
	bytesEntryToExitPaid, bytesExitToEntryPaid *uint64,
	nanoPayFee string,
	minNanoPayFee string,
	nanoPayFeePercentage float64,
	getPaymentStreamRecipient func() (*smux.Stream, string, error),
) {
	var np *nkn.NanoPay
	var bytesEntryToExit, bytesExitToEntry uint64
	var cost, lastCost common.Fixed64
	entryToExitPrice, exitToEntryPrice := c.GetPrice()
	lastPaymentTime := time.Now()

	for {
		for {
			time.Sleep(100 * time.Millisecond)
			if c.isClosed {
				return
			}
			bytesEntryToExit = atomic.LoadUint64(bytesEntryToExitUsed)
			bytesExitToEntry = atomic.LoadUint64(bytesExitToEntryUsed)
			if (bytesEntryToExit+bytesExitToEntry)-(*bytesEntryToExitPaid+*bytesExitToEntryPaid) > trafficPaymentThreshold*TrafficUnit {
				break
			}
			if time.Since(lastPaymentTime) > defaultNanoPayUpdateInterval {
				break
			}
		}

		bytesEntryToExit = atomic.LoadUint64(bytesEntryToExitUsed)
		bytesExitToEntry = atomic.LoadUint64(bytesExitToEntryUsed)
		cost = entryToExitPrice*common.Fixed64(bytesEntryToExit-*bytesEntryToExitPaid)/TrafficUnit + exitToEntryPrice*common.Fixed64(bytesExitToEntry-*bytesExitToEntryPaid)/TrafficUnit
		if cost == lastCost || cost <= common.Fixed64(0) {
			continue
		}
		costTimeStamp := time.Now()

		paymentStream, paymentReceiver, err := getPaymentStreamRecipient()
		if err != nil {
			log.Printf("Get payment stream err: %v", err)
			continue
		}

		if len(paymentReceiver) == 0 {
			continue
		}

		if np == nil || np.Recipient() != paymentReceiver {
			np, err = c.Client.NewNanoPay(paymentReceiver, nanoPayFee, defaultNanoPayDuration)
			if err != nil {
				log.Printf("Create nanopay err: %v", err)
				continue
			}
		}

		if nanoPayFee == "" {
			fee := common.Fixed64(float64(cost.GetData()) * nanoPayFeePercentage)

			minTxFee, err := common.StringToFixed64(minNanoPayFee)
			if err != nil {
				log.Printf("minNanoPayFee to Fixed64 err: %v", err)
				return
			}
			if fee < minTxFee {
				fee = minTxFee
			}
			nanoPayFee = fee.String()
		}

		err = sendNanoPay(np, paymentStream, cost, nanoPayFee)
		if err != nil {
			log.Printf("Send nanopay err: %v", err)
			return
		}
		log.Printf("send nanopay success: %s", cost.String())

		*bytesEntryToExitPaid = bytesEntryToExit
		*bytesExitToEntryPaid = bytesExitToEntry
		lastCost = cost
		lastPaymentTime = costTimeStamp
	}
}

func (c *Common) pipe(dest io.WriteCloser, src io.ReadCloser, written *uint64) {
	c.sessionsWaitGroup.Add(1)

	c.Lock()
	c.activeSessions++
	c.Unlock()

	defer func() {
		dest.Close()
		src.Close()

		c.Lock()
		c.activeSessions--
		c.Unlock()

		c.sessionsWaitGroup.Done()
	}()

	copyBuffer(dest, src, written)
}

func (c *Common) GetNumActiveSessions() int {
	c.RLock()
	defer c.RUnlock()
	return c.activeSessions
}

func (c *Common) GetSessionsWaitGroup() *sync.WaitGroup {
	return c.sessionsWaitGroup
}

// SetLinger sets the behavior of Close when there is at least one active session.
// t = 0 (default): close all conn when tuna close.
// t < 0: tuna Close() call will block and wait for all sessions to close before closing tuna.
// t > 0: tuna Close() call will block and wait for up to timeout all sessions to close before closing tuna.
func (c *Common) SetLinger(t time.Duration) {
	c.Lock()
	c.linger = t
	c.Unlock()
}

// WaitSessions waits for sessions wait group, or until linger times out.
func (c *Common) WaitSessions() {
	c.RLock()
	linger := c.linger
	c.RUnlock()

	if linger == 0 {
		return
	}

	waitChan := make(chan struct{})
	go func() {
		c.sessionsWaitGroup.Wait()
		close(waitChan)
	}()

	var timeoutChan <-chan time.Time
	if linger > 0 {
		timeoutChan = time.After(linger)
	}

	select {
	case <-waitChan:
	case <-timeoutChan:
	}
}

func (c *Common) SetRemoteNode(node *types.Node) {
	c.presetNode = node
}

func ReadMetadata(metadataString string) (*pb.ServiceMetadata, error) {
	metadataRaw, err := base64.StdEncoding.DecodeString(metadataString)
	if err != nil {
		return nil, err
	}
	metadata := &pb.ServiceMetadata{}
	err = proto.Unmarshal(metadataRaw, metadata)
	if err != nil {
		return nil, err
	}
	return metadata, nil
}

func CreateRawMetadata(
	serviceID byte,
	serviceTCP []uint32,
	serviceUDP []uint32,
	ip string,
	tcpPort uint32,
	udpPort uint32,
	price string,
	beneficiaryAddr string,
) []byte {
	metadata := &pb.ServiceMetadata{
		Ip:              ip,
		TcpPort:         tcpPort,
		UdpPort:         udpPort,
		ServiceId:       uint32(serviceID),
		ServiceTcp:      serviceTCP,
		ServiceUdp:      serviceUDP,
		Price:           price,
		BeneficiaryAddr: beneficiaryAddr,
	}
	metadataRaw, err := proto.Marshal(metadata)
	if err != nil {
		log.Fatalln(err)
	}
	return []byte(base64.StdEncoding.EncodeToString(metadataRaw))
}

func UpdateMetadata(
	serviceName string,
	serviceID byte,
	serviceTCP []uint32,
	serviceUDP []uint32,
	ip string,
	tcpPort uint32,
	udpPort uint32,
	price string,
	beneficiaryAddr string,
	subscriptionPrefix string,
	subscriptionDuration uint32,
	subscriptionFee string,
	subscriptionReplaceTxPool bool,
	client *nkn.MultiClient,
	closeChan chan struct{},
) {
	metadataRaw := CreateRawMetadata(serviceID, serviceTCP, serviceUDP, ip, tcpPort, udpPort, price, beneficiaryAddr)
	topic := subscriptionPrefix + serviceName
	identifier := ""
	subInterval := config.ConsensusDuration
	if subscriptionDuration > 3 {
		subInterval = time.Duration(subscriptionDuration-3) * config.ConsensusDuration
	}
	var nextSub <-chan time.Time

	go func() {
		for {
			nextSub = time.After(0)

			func() {
				sub, err := client.GetSubscription(topic, address.MakeAddressString(client.PubKey(), identifier))
				if err != nil {
					log.Println("Get existing subscription error:", err)
					return
				}

				if len(sub.Meta) == 0 && sub.ExpiresAt == 0 {
					return
				}

				if sub.Meta != string(metadataRaw) {
					log.Println("Existing subscription meta need update.")
					return
				}

				height, err := client.GetHeight()
				if err != nil {
					log.Println("Get current height error:", err)
					return
				}

				if sub.ExpiresAt-height < 3 {
					log.Println("Existing subscription is expiring")
					return
				}

				log.Println("Existing subscription expires after", sub.ExpiresAt-height, "blocks")

				maxSubDuration := float64(sub.ExpiresAt-height) * float64(config.ConsensusDuration)
				nextSub = time.After(time.Duration((1 - rand.Float64()*subscribeDurationRandomFactor) * maxSubDuration))
			}()

			select {
			case <-nextSub:
			case <-closeChan:
				return
			}

			subFee, err := common.StringToFixed64(subscriptionFee)
			if err != nil {
				log.Println("Parse subscription fee error:", err)
			}

			if subFee > 0 {
				balance, err := client.Balance()
				if err != nil {
					log.Println("Get balance error:", err)
				} else {
					if subFee > balance.ToFixed64() {
						subFee = balance.ToFixed64()
					}
				}
			}

			addToSubscribeQueue(client, identifier, topic, int(subscriptionDuration), string(metadataRaw), &nkn.TransactionConfig{Fee: subFee.String()}, subscriptionReplaceTxPool)

			nextSub = time.After(time.Duration((1 - rand.Float64()*subscribeDurationRandomFactor) * float64(subInterval)))

			select {
			case <-nextSub:
			case <-time.After(maxCheckSubscribeInterval):
			case <-closeChan:
				return
			}
		}
	}()
}

func copyBuffer(dest io.Writer, src io.Reader, written *uint64) error {
	buf := make([]byte, pipeBufferSize)
	for {
		nr, err := src.Read(buf)
		if nr > 0 {
			nw, err := dest.Write(buf[0:nr])
			if nw > 0 {
				if written != nil {
					atomic.AddUint64(written, uint64(nw))
				}
			}
			if err != nil {
				return err
			}
			if nr != nw {
				return io.ErrShortWrite
			}
		}
		if err != nil {
			if err != io.EOF {
				return err
			}
			return nil
		}
	}
}

func Close(conn io.Closer) {
	if conn == nil || reflect.ValueOf(conn).IsNil() {
		return
	}
	err := conn.Close()
	if err != nil {
		log.Println("Error while closing:", err)
	}
}

func PortToConnID(port uint16) []byte {
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, port)
	return b
}

func ConnIDToPort(data []byte) uint16 {
	return binary.LittleEndian.Uint16(data)
}

func LoadPassword(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	// Remove the UTF-8 Byte Order Mark
	content = bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))
	return strings.Trim(string(content), "\r\n"), nil
}

func LoadOrCreateAccount(walletFile, passwordFile string) (*vault.Account, error) {
	var wallet *vault.Wallet
	var pswd string
	if _, err := os.Stat(walletFile); os.IsNotExist(err) {
		if _, err = os.Stat(passwordFile); os.IsNotExist(err) {
			pswd = base64.StdEncoding.EncodeToString(util.RandomBytes(24))
			log.Println("Creating wallet.pswd")
			err = os.WriteFile(passwordFile, []byte(pswd), 0644)
			if err != nil {
				return nil, fmt.Errorf("save password to file error: %v", err)
			}
		}
		log.Println("Creating wallet.json")
		wallet, err = vault.NewWallet(walletFile, []byte(pswd))
		if err != nil {
			return nil, fmt.Errorf("create wallet error: %v", err)
		}
	} else {
		pswd, err = LoadPassword(passwordFile)
		if err != nil {
			return nil, err
		}
		wallet, err = vault.OpenWallet(walletFile, []byte(pswd))
		if err != nil {
			return nil, fmt.Errorf("open wallet error: %v", err)
		}
	}
	return wallet.GetDefaultAccount()
}

func openPaymentStream(session *smux.Session) (*smux.Stream, error) {
	stream, err := session.OpenStream()
	if err != nil {
		session.Close()
		return nil, err
	}

	streamMetadata := &pb.StreamMetadata{
		IsPayment: true,
	}

	err = writeStreamMetadata(stream, streamMetadata)
	if err != nil {
		return nil, err
	}

	return stream, nil
}

func sendNanoPay(np *nkn.NanoPay, paymentStream *smux.Stream, cost common.Fixed64, nanoPayFee string) error {
	var tx *transaction.Transaction
	var err error
	for i := 0; i < 3; i++ {
		if i > 0 {
			time.Sleep(1 * time.Second)
		}
		tx, err = np.IncrementAmount(cost.String(), nanoPayFee)
		if err == nil {
			break
		}
	}
	if err != nil || tx == nil || tx.GetSize() == 0 {
		return fmt.Errorf("send nanopay tx failed: %v", err)
	}

	txBytes, err := tx.Marshal()
	if err != nil {
		return err
	}

	err = WriteVarBytes(paymentStream, txBytes)
	if err != nil {
		return err
	}

	return nil
}

func nanoPayClaim(txBytes []byte, npc *nkn.NanoPayClaimer) (*nkn.Amount, error) {
	if len(txBytes) == 0 {
		return nil, errors.New("empty txn bytes")
	}

	tx := &transaction.Transaction{}
	if err := tx.Unmarshal(txBytes); err != nil {
		return nil, fmt.Errorf("couldn't unmarshal payment stream data: %v", err)
	}

	if tx.UnsignedTx == nil {
		return nil, errors.New("nil txn body")
	}

	return npc.Claim(tx)
}

func checkNanoPayClaim(session *smux.Session, npc *nkn.NanoPayClaimer, onErr *nkn.OnError, isClosed *bool) {
	for {
		err, ok := <-onErr.C
		if !ok {
			break
		}
		if err != nil {
			log.Println("Couldn't claim nano pay:", err)
			if npc.IsClosed() {
				Close(session)
				*isClosed = true
				break
			}
		}
	}
}

func checkPayment(session *smux.Session, lastPaymentTime *time.Time, lastPaymentAmount, bytesPaid *common.Fixed64, isClosed *bool, getTotalCost func() (common.Fixed64, common.Fixed64)) {
	var totalCost, totalBytes, totalCostDelayed, totalBytesDelayed common.Fixed64

	go func() {
		for {
			time.Sleep(time.Second)
			if *isClosed {
				return
			}
			totalCostNow, totalBytesNow := getTotalCost()
			time.AfterFunc(trafficDelay, func() {
				totalCostDelayed, totalBytesDelayed = totalCostNow, totalBytesNow
			})
		}
	}()

	for {
		for {
			time.Sleep(100 * time.Millisecond)

			if *isClosed {
				return
			}

			totalCost, totalBytes = totalCostDelayed, totalBytesDelayed
			if totalCost <= *lastPaymentAmount {
				continue
			}

			if time.Since(*lastPaymentTime) > defaultNanoPayUpdateInterval {
				break
			}

			if totalBytes-*bytesPaid > trafficPaymentThreshold*TrafficUnit {
				break
			}
		}

		time.Sleep(maxNanoPayDelay)

		if *lastPaymentAmount < common.Fixed64(minTrafficCoverage*float64(totalCost)) && totalCost-*lastPaymentAmount > common.Fixed64(maxTrafficUnpaid*TrafficUnit*float64(totalCost)/float64(totalBytes)) {
			Close(session)
			*isClosed = true
			log.Printf("Not enough payment. Since last payment: %s. Last claimed: %v, expected: %v", time.Since(*lastPaymentTime).String(), *lastPaymentAmount, totalCost)
			return
		}
	}
}

func handlePaymentStream(stream *smux.Stream, npc *nkn.NanoPayClaimer, lastPaymentTime *time.Time, lastPaymentAmount, bytesPaid *common.Fixed64, getTotalCost func() (common.Fixed64, common.Fixed64)) error {
	for {
		tx, err := ReadVarBytes(stream, maxNanoPayTxnSize)
		if err != nil {
			return fmt.Errorf("couldn't read payment stream: %v", err)
		}

		totalCost, totalBytes := getTotalCost()
		if totalCost == 0 {
			continue
		}

		var amount *nkn.Amount
		for i := 0; i < 3; i++ {
			if i > 0 {
				time.Sleep(3 * time.Second)
			}
			amount, err = nanoPayClaim(tx, npc)
			if err == nil {
				break
			} else {
				log.Printf("could't claim nanoPay: %v", err)
			}
		}
		if err != nil || amount == nil {
			if npc.IsClosed() {
				log.Printf("nanopayclaimer closed: %v", err)
				return nil
			}
			continue
		}

		*lastPaymentAmount = amount.ToFixed64()
		*lastPaymentTime = time.Now()
		*bytesPaid = totalBytes * (npc.Amount().Fixed64 / totalCost)
	}
}
