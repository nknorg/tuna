package tuna

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
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

	"github.com/nknorg/tuna/storage"
	"github.com/nknorg/tuna/types"

	"github.com/golang/protobuf/proto"
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
	tunaUtil "github.com/nknorg/tuna/util"
	"github.com/xtaci/smux"
	"golang.org/x/crypto/nacl/box"
)

const (
	TrafficUnit = 1024 * 1024

	tcp                           = "tcp"
	udp                           = "udp"
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
	Name       string   `json:"name"`
	TCP        []uint32 `json:"tcp"`
	UDP        []uint32 `json:"udp"`
	Encryption string   `json:"encryption"`
}

type Common struct {
	Service                        *Service
	ServiceInfo                    *ServiceInfo
	Wallet                         *nkn.Wallet
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
	paymentReceiver  string
	entryToExitPrice common.Fixed64
	exitToEntryPrice common.Fixed64
	metadata         *pb.ServiceMetadata
	connected        bool
	tcpConn          net.Conn
	udpConn          *net.UDPConn
	isClosed         bool
	sharedKeys       map[string]*[sharedKeySize]byte
	remoteNknAddress string
	activeSessions   int
	linger           time.Duration
}

func NewCommon(
	service *Service,
	serviceInfo *ServiceInfo,
	wallet *nkn.Wallet,
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
	sortMeasuredNodes func(types.Nodes),
	reverseMetadata *pb.ServiceMetadata,
) (*Common, error) {
	encryptionAlgo := defaultEncryptionAlgo
	var err error
	if service != nil && len(service.Encryption) > 0 {
		encryptionAlgo, err = ParseEncryptionAlgo(service.Encryption)
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

		curveSecretKey:                    curveSecretKey,
		encryptionAlgo:                    encryptionAlgo,
		closeChan:                         make(chan struct{}),
		sharedKeys:                        make(map[string]*[sharedKeySize]byte),
		measureDelayConcurrentWorkers:     measureDelayConcurrentWorkers,
		measureBandwidthConcurrentWorkers: measureBandwidthConcurrentWorkers,
		sortMeasuredNodes:                 sortMeasuredNodes,
		sessionsWaitGroup:                 &wg,
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

func (c *Common) GetUDPConn() *net.UDPConn {
	c.RLock()
	defer c.RUnlock()
	return c.udpConn
}

func (c *Common) SetServerUDPConn(conn *net.UDPConn) {
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

func (c *Common) GetServerUDPConn(force bool) (*net.UDPConn, error) {
	err := c.CreateServerConn(force)
	if err != nil {
		return nil, err
	}
	c.RLock()
	defer c.RUnlock()
	return c.GetUDPConn(), nil
}

func (c *Common) SetServerUDPReadChan(udpReadChan chan []byte) {
	c.udpReadChan = udpReadChan
}

func (c *Common) SetServerUDPWriteChan(udpWriteChan chan []byte) {
	c.udpWriteChan = udpWriteChan
}

func (c *Common) GetServerUDPReadChan(force bool) (chan []byte, error) {
	err := c.CreateServerConn(force)
	if err != nil {
		return nil, err
	}
	return c.udpReadChan, nil
}

func (c *Common) GetServerUDPWriteChan(force bool) (chan []byte, error) {
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

func (c *Common) StartUDPReaderWriter(conn *net.UDPConn) {
	go func() {
		for {
			buffer := make([]byte, 2048)
			n, err := conn.Read(buffer)
			if err != nil {
				log.Println("Couldn't receive data from server:", err)
				if strings.Contains(err.Error(), "use of closed network connection") {
					c.udpCloseChan <- struct{}{}
					return
				}
				continue
			}

			data := make([]byte, n)
			copy(data, buffer)
			c.udpReadChan <- data
		}
	}()
	go func() {
		for {
			select {
			case data := <-c.udpWriteChan:
				_, err := conn.Write(data)
				if err != nil {
					log.Println("Couldn't send data to server:", err)
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

		connNonce = remoteConnMetadata.Nonce
	} else {
		connNonce = util.RandomBytes(connNonceSize)
		localConnMetadata.Nonce = connNonce

		err := writeConnMetadata(conn, localConnMetadata)
		if err != nil {
			return nil, nil, err
		}

		remoteConnMetadata, err = readConnMetadata(conn)
		if err != nil {
			return nil, nil, err
		}

		if len(remoteConnMetadata.PublicKey) != ed25519.PublicKeySize {
			return nil, nil, fmt.Errorf("invalid pubkey size %d", len(remoteConnMetadata.PublicKey))
		}

		encryptionAlgo = remoteConnMetadata.EncryptionAlgo
		remotePublicKey = remoteConnMetadata.PublicKey
	}

	if encryptionAlgo == pb.EncryptionAlgo_ENCRYPTION_NONE {
		return conn, remoteConnMetadata, nil
	}

	sharedKey, err := c.getOrComputeSharedKey(remotePublicKey)
	if err != nil {
		return nil, nil, err
	}

	encryptKey := computeEncryptKey(connNonce, sharedKey[:])

	encryptedConn, err := encryptConn(conn, encryptKey, encryptionAlgo)
	if err != nil {
		return nil, nil, err
	}

	return encryptedConn, remoteConnMetadata, nil
}

func (c *Common) UpdateServerConn(remotePublicKey []byte) error {
	hasTCP := len(c.Service.TCP) > 0 || (c.ReverseMetadata != nil && len(c.ReverseMetadata.ServiceTcp) > 0)
	hasUDP := len(c.Service.UDP) > 0 || (c.ReverseMetadata != nil && len(c.ReverseMetadata.ServiceUdp) > 0)
	metadata := c.GetMetadata()

	if hasTCP {
		Close(c.GetTCPConn())

		addr := metadata.Ip + ":" + strconv.Itoa(int(metadata.TcpPort))
		tcpConn, err := net.DialTimeout(
			tcp,
			addr,
			time.Duration(c.DialTimeout)*time.Second,
		)
		if err != nil {
			return err
		}

		encryptedConn, _, err := c.wrapConn(tcpConn, remotePublicKey, nil)
		if err != nil {
			Close(tcpConn)
			return err
		}

		c.SetServerTCPConn(encryptedConn)

		log.Println("Connected to TCP at", addr)
	}
	if hasUDP {
		udpConn := c.GetUDPConn()
		Close(udpConn)

		addr := net.UDPAddr{IP: net.ParseIP(metadata.Ip), Port: int(metadata.UdpPort)}
		udpConn, err := net.DialUDP(
			udp,
			nil,
			&addr,
		)
		if err != nil {
			return err
		}
		c.SetServerUDPConn(udpConn)
		log.Println("Connected to UDP at", addr.String())

		c.StartUDPReaderWriter(udpConn)
	}

	c.SetConnected(true)

	c.OnConnect.receive()

	return nil
}

func (c *Common) CreateServerConn(force bool) error {
	if !c.IsServer && (!c.GetConnected() || force) {
		for {
			err := c.SetPaymentReceiver("")
			if err != nil {
				return err
			}

			candidateSubs, err := c.GetTopPerformanceNodes(c.MeasureBandwidth, measureBandwidthTopCount)
			if err != nil {
				log.Println(err)
				time.Sleep(time.Second)
				continue
			}

			for _, subscriber := range candidateSubs {
				metadata := subscriber.Metadata
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
					continue
				}

				return nil
			}
		}
	}

	return nil
}

func (c *Common) GetTopPerformanceNodes(measureBandwidth bool, n int) (types.Nodes, error) {
	if len(c.ServiceInfo.IPFilter.GetProviders()) > 0 {
		c.ServiceInfo.IPFilter.UpdateDataFile()
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
	allSubscribers, subscriberRaw, err := c.nknFilter()
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
		delayMeasuredSubs := measureDelay(filterSubs, c.measureDelayConcurrentWorkers, measureDelayTopDelayCount, defaultMeasureDelayTimeout)
		if measureBandwidth {
			candidateSubs = c.measureBandwidth(delayMeasuredSubs, n, c.MeasureBandwidthWorkersTimeout)
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
				subscription, err := c.Wallet.GetSubscription(topic, f.Address)
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
		subscribersCount, err := c.Wallet.GetSubscribersCount(topic)
		if err != nil {
			return nil, nil, err
		}
		if subscribersCount == 0 {
			return nil, nil, errors.New("there is no service providers for " + c.Service.Name)
		}

		offset := rand.Intn((subscribersCount-1)/c.GetSubscribersBatchSize + 1)
		subscribers, err := c.Wallet.GetSubscribers(topic, offset*c.GetSubscribersBatchSize, c.GetSubscribersBatchSize, true, false)
		if err != nil {
			return nil, nil, err
		}

		subscriberRaw = subscribers.Subscribers.Map

		allSubscribers = make([]string, 0, len(subscriberRaw))
		if c.measureStorage != nil {
			nodes := c.measureStorage.FavoriteNodes.GetData()
			for _, v := range nodes {
				item := v.(*storage.FavoriteNode)
				subscriberRaw[item.Address] = item.Metadata
				log.Printf("Use favorite node: %s", item.IP)
			}
		}
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
			Address:  subscriber,
			Metadata: metadata,
		})
	}

	return filterSubs
}

func measureDelay(nodes types.Nodes, concurrentWorkers, numResults int, timeout time.Duration) types.Nodes {
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
				delay, err := tunaUtil.DelayMeasurement(tcp, addr, timeout)
				if err != nil {
					if _, ok := err.(net.Error); !ok {
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

func (c *Common) measureBandwidth(nodes types.Nodes, n int, timeout time.Duration) types.Nodes {
	timeStart := time.Now()
	var resLock sync.Mutex
	bandwidthMeasuredSubs := make(types.Nodes, 0, len(nodes))
	wg := &sync.WaitGroup{}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	var measurementBandwidthJobChan = make(chan tunaUtil.Job, 1)
	go tunaUtil.WorkPool(c.measureBandwidthConcurrentWorkers, measurementBandwidthJobChan, wg)
	for index := range nodes {
		func(sub *types.Node) {
			wg.Add(1)
			tunaUtil.Enqueue(measurementBandwidthJobChan, func() {
				select {
				case <-ctx.Done():
					return
				default:
				}
				remotePublicKey, err := nkn.ClientAddrToPubKey(sub.Address)
				if err != nil {
					log.Println(err)
					return
				}

				addr := sub.Metadata.Ip + ":" + strconv.Itoa(int(sub.Metadata.TcpPort))
				conn, err := net.DialTimeout(
					tcp,
					addr,
					defaultMeasureDelayTimeout,
				)
				if err != nil {
					if _, ok := err.(net.Error); !ok {
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
				min, max, err := tunaUtil.BandwidthMeasurementClient(encryptedConn, int(c.MeasurementBytesDownLink), c.MeasureBandwidthTimeout)
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
		}(nodes[index])
	}
	wg.Wait()
	cancel()
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
	getPaymentStream func() (*smux.Stream, error),
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

		paymentStream, err := getPaymentStream()
		if err != nil {
			log.Printf("Get payment stream err: %v", err)
			continue
		}

		paymentReceiver := c.GetPaymentReceiver()
		if np == nil || np.Recipient() != paymentReceiver {
			np, err = c.Wallet.NewNanoPay(paymentReceiver, nanoPayFee, defaultNanoPayDuration)
			if err != nil {
				log.Printf("Create nanopay err: %v", err)
				continue
			}
		}

		err = sendNanoPay(np, paymentStream, cost)
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
	wallet *nkn.Wallet,
	closeChan chan struct{},
) {
	metadataRaw := CreateRawMetadata(serviceID, serviceTCP, serviceUDP, ip, tcpPort, udpPort, price, beneficiaryAddr)
	topic := subscriptionPrefix + serviceName
	identifier := ""
	subInterval := config.ConsensusDuration
	if subscriptionDuration > 3 {
		subInterval = time.Duration(subscriptionDuration-3) * config.ConsensusDuration
	}
	nextSub := time.After(0)

	go func() {
		func() {
			sub, err := wallet.GetSubscription(topic, address.MakeAddressString(wallet.PubKey(), identifier))
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

			height, err := wallet.GetHeight()
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

		for {
			select {
			case <-nextSub:
			case <-closeChan:
				return
			}
			addToSubscribeQueue(wallet, identifier, topic, int(subscriptionDuration), string(metadataRaw), &nkn.TransactionConfig{Fee: subscriptionFee})
			nextSub = time.After(time.Duration((1 - rand.Float64()*subscribeDurationRandomFactor) * float64(subInterval)))
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
	content, err := ioutil.ReadFile(path)
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
			err = ioutil.WriteFile(passwordFile, []byte(pswd), 0644)
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

func sendNanoPay(np *nkn.NanoPay, paymentStream *smux.Stream, cost common.Fixed64) error {
	var tx *transaction.Transaction
	var err error
	for i := 0; i < 3; i++ {
		if i > 0 {
			time.Sleep(1 * time.Second)
		}
		tx, err = np.IncrementAmount(cost.String())
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
		err := <-onErr.C
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

		_, totalBytes := getTotalCost()

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
		*bytesPaid = totalBytes
	}
}
