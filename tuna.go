package tuna

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/nkn/common"
	"github.com/nknorg/nkn/crypto/ed25519"
	"github.com/nknorg/nkn/transaction"
	"github.com/nknorg/nkn/util"
	"github.com/nknorg/nkn/util/config"
	"github.com/nknorg/nkn/vault"
	"github.com/nknorg/tuna/pb"
	"github.com/xtaci/smux"
	"golang.org/x/crypto/nacl/box"
)

type Protocol string

const (
	TCP                           Protocol = "tcp"
	UDP                           Protocol = "udp"
	DefaultNanoPayDuration                 = 4320 * 30
	DefaultNanoPayUpdateInterval           = time.Minute
	DefaultSubscriptionPrefix              = "tuna_v1."
	DefaultReverseServiceName              = "reverse"
	DefaultServiceListenIP                 = "127.0.0.1"
	DefaultReverseServiceListenIP          = "0.0.0.0"
	TrafficUnit                            = 1024 * 1024
	MaxTrafficOwned                        = 32 * 1024 * 1024
	getSubscribersBatchSize                = 32
	DefaultEncryptionAlgo                  = pb.ENCRYPTION_NONE
	MaxNanoPayDelay                        = 30 * time.Second
)

type ServiceInfo struct {
	MaxPrice string    `json:"maxPrice"`
	ListenIP string    `json:"listenIP"`
	IPFilter *IPFilter `json:"ipFilter"`
}

type Service struct {
	Name       string   `json:"name"`
	TCP        []uint32 `json:"tcp"`
	UDP        []uint32 `json:"udp"`
	Encryption string   `json:"encryption"`
}

type Common struct {
	Service            *Service
	ServiceInfo        *ServiceInfo
	Wallet             *nkn.Wallet
	DialTimeout        int32
	SubscriptionPrefix string
	Reverse            bool
	ReverseMetadata    *pb.ServiceMetadata
	OnConnect          *OnConnect
	IsServer           bool

	udpReadChan    chan []byte
	udpWriteChan   chan []byte
	udpCloseChan   chan struct{}
	tcpListener    *net.TCPListener
	curveSecretKey *[sharedKeySize]byte
	encryptionAlgo pb.EncryptionAlgo

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
}

func NewCommon(service *Service, serviceInfo *ServiceInfo, wallet *nkn.Wallet, dialTimeout int32, subscriptionPrefix string, reverse, isServer bool, reverseMetadata *pb.ServiceMetadata) (*Common, error) {
	encryptionAlgo := DefaultEncryptionAlgo
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

	common := &Common{
		Service:            service,
		ServiceInfo:        serviceInfo,
		Wallet:             wallet,
		DialTimeout:        dialTimeout,
		SubscriptionPrefix: subscriptionPrefix,
		Reverse:            reverse,
		ReverseMetadata:    reverseMetadata,
		OnConnect:          NewOnConnect(1, nil),
		IsServer:           isServer,
		sharedKeys:         make(map[string]*[sharedKeySize]byte),
		curveSecretKey:     curveSecretKey,
		encryptionAlgo:     encryptionAlgo,
	}

	return common, nil
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

func (c *Common) SetMetadata(metadataString string) bool {
	var err error
	c.Lock()
	c.metadata, err = ReadMetadata(metadataString)
	c.Unlock()
	if err != nil {
		log.Println("Couldn't unmarshal metadata:", err)
		return false
	}
	return true
}

func (c *Common) GetPaymentReceiver() string {
	c.RLock()
	defer c.RUnlock()
	return c.paymentReceiver
}

func (c *Common) SetPaymentReceiver(paymentReceiver string) {
	c.Lock()
	defer c.Unlock()
	c.paymentReceiver = paymentReceiver
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

func (c *Common) encryptConn(conn net.Conn, remotePublicKey []byte) (net.Conn, error) {
	var connMetadata *pb.ConnectionMetadata
	if len(remotePublicKey) > 0 {
		connMetadata = &pb.ConnectionMetadata{
			EncryptionAlgo: c.encryptionAlgo,
			PublicKey:      c.Wallet.PubKey(),
		}

		b, err := proto.Marshal(connMetadata)
		if err != nil {
			return nil, err
		}

		err = WriteVarBytes(conn, b)
		if err != nil {
			return nil, err
		}
	} else {
		b, err := ReadVarBytes(conn)
		if err != nil {
			return nil, err
		}

		connMetadata = &pb.ConnectionMetadata{}
		err = proto.Unmarshal(b, connMetadata)
		if err != nil {
			return nil, err
		}

		if len(connMetadata.PublicKey) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("invalid pubkey size %d", len(connMetadata.PublicKey))
		}

		remotePublicKey = connMetadata.PublicKey
	}

	if connMetadata.EncryptionAlgo == pb.ENCRYPTION_NONE {
		return conn, nil
	}

	sharedKey, err := c.getOrComputeSharedKey(remotePublicKey)
	if err != nil {
		return nil, err
	}

	return encryptConn(conn, sharedKey, connMetadata.EncryptionAlgo)
}

func (c *Common) UpdateServerConn(remotePublicKey []byte) error {
	hasTCP := len(c.Service.TCP) > 0 || (c.ReverseMetadata != nil && len(c.ReverseMetadata.ServiceTcp) > 0)
	hasUDP := len(c.Service.UDP) > 0 || (c.ReverseMetadata != nil && len(c.ReverseMetadata.ServiceUdp) > 0)
	metadata := c.GetMetadata()

	if hasTCP {
		Close(c.GetTCPConn())

		address := metadata.Ip + ":" + strconv.Itoa(int(metadata.TcpPort))
		tcpConn, err := net.DialTimeout(
			string(TCP),
			address,
			time.Duration(c.DialTimeout)*time.Second,
		)
		if err != nil {
			return err
		}

		encryptedConn, err := c.encryptConn(tcpConn, remotePublicKey)
		if err != nil {
			Close(tcpConn)
			return err
		}

		c.SetServerTCPConn(encryptedConn)

		log.Println("Connected to TCP at", address)
	}
	if hasUDP {
		udpConn := c.GetUDPConn()
		Close(udpConn)

		address := net.UDPAddr{IP: net.ParseIP(metadata.Ip), Port: int(metadata.UdpPort)}
		udpConn, err := net.DialUDP(
			string(UDP),
			nil,
			&address,
		)
		if err != nil {
			return err
		}
		c.SetServerUDPConn(udpConn)
		log.Println("Connected to UDP at", address.String())

		c.StartUDPReaderWriter(udpConn)
	}

	c.SetConnected(true)

	return nil
}

func (c *Common) CreateServerConn(force bool) error {
	entryToExitMaxPrice, exitToEntryMaxPrice, err := ParsePrice(c.ServiceInfo.MaxPrice)
	if err != nil {
		log.Fatalf("Parse price of service error: %v", err)
	}
	if !c.IsServer && (c.GetConnected() == false || force) {
		topic := c.SubscriptionPrefix + c.Service.Name
	RandomSubscriber:
		for {
			c.SetPaymentReceiver("")
			subscribersCount, err := c.Wallet.GetSubscribersCount(topic)
			if err != nil {
				return err
			}
			if subscribersCount == 0 {
				return errors.New("there is no service providers for " + c.Service.Name)
			}

			offset := rand.Intn(subscribersCount/getSubscribersBatchSize + 1)
			subscribers, err := c.Wallet.GetSubscribers(topic, offset*getSubscribersBatchSize, getSubscribersBatchSize, true, false)
			if err != nil {
				return err
			}

			for subscriber, metadataString := range subscribers.Subscribers.Map {
				if !c.SetMetadata(metadataString) {
					continue RandomSubscriber
				}

				metadata := c.GetMetadata()

				res, err := c.ServiceInfo.IPFilter.GeoCheck(metadata.Ip)
				if err != nil {
					log.Println(err)
				}
				if !res {
					continue
				}

				if len(metadata.BeneficiaryAddr) > 0 {
					c.SetPaymentReceiver(metadata.BeneficiaryAddr)
				} else {
					address, err := nkn.ClientAddrToWalletAddr(subscriber)
					if err != nil {
						log.Println(err)
						continue RandomSubscriber
					}

					c.SetPaymentReceiver(address)
				}

				entryToExitPrice, exitToEntryPrice, err := ParsePrice(metadata.Price)
				if err != nil {
					log.Println(err)
					continue RandomSubscriber
				}

				if entryToExitPrice > entryToExitMaxPrice {
					continue RandomSubscriber
				}
				if exitToEntryPrice > exitToEntryMaxPrice {
					continue RandomSubscriber
				}

				c.Lock()
				c.entryToExitPrice = entryToExitPrice
				c.exitToEntryPrice = exitToEntryPrice
				if c.ReverseMetadata != nil {
					c.metadata.ServiceTcp = c.ReverseMetadata.ServiceTcp
					c.metadata.ServiceUdp = c.ReverseMetadata.ServiceUdp
				}
				c.Unlock()

				remotePublicKey, err := nkn.ClientAddrToPubKey(subscriber)
				if err != nil {
					log.Println(err)
					continue RandomSubscriber
				}

				err = c.UpdateServerConn(remotePublicKey)
				if err != nil {
					log.Println(err)
					continue RandomSubscriber
				}

				break RandomSubscriber
			}
		}
	}

	return nil
}

func (c *Common) startPayment(
	bytesInUsed, bytesOutUsed, bytesInPaid, bytesOutPaid *uint64,
	nanoPayFee string,
	getPaymentStream func() (*smux.Stream, error),
	closeChan chan struct{},
) {
	var np *nkn.NanoPay
	lastCost := common.Fixed64(0)
	var bytesIn, bytesOut uint64
	entryToExitPrice, exitToEntryPrice := c.GetPrice()
	cost := common.Fixed64(0)

	paymentTimer := time.After(DefaultNanoPayUpdateInterval)
	for {
		select {
		case <-closeChan:
			return
		case <-time.After(100 * time.Millisecond):
			bytesIn = atomic.LoadUint64(bytesInUsed)
			bytesOut = atomic.LoadUint64(bytesOutUsed)
			if (bytesIn+bytesOut)-(*bytesInPaid+*bytesOutPaid) <= uint64(MaxTrafficOwned) {
				continue
			}
		case <-paymentTimer:
			paymentTimer = time.After(2 * time.Second)
		}

		bytesIn = atomic.LoadUint64(bytesInUsed)
		bytesOut = atomic.LoadUint64(bytesOutUsed)
		cost = exitToEntryPrice*common.Fixed64(bytesIn-*bytesInPaid)/TrafficUnit + entryToExitPrice*common.Fixed64(bytesOut-*bytesOutPaid)/TrafficUnit
		if cost == lastCost {
			time.Sleep(DefaultNanoPayUpdateInterval / 10)
			continue
		}
		lastCost = cost

		paymentStream, err := getPaymentStream()
		if err != nil {
			log.Printf("get payment stream err: %v", err)
			time.Sleep(time.Second)
			continue
		}

		paymentReceiver := c.GetPaymentReceiver()
		if np == nil || np.Recipient() != paymentReceiver {
			np, err = c.Wallet.NewNanoPay(paymentReceiver, nanoPayFee, DefaultNanoPayDuration)
			if err != nil {
				log.Printf("create nano payment err: %v", err)
				time.Sleep(time.Second)
				continue
			}
		}

		err = sendNanoPay(np, paymentStream, cost)
		if err != nil {
			log.Printf("send nano payment err: %v", err)
			time.Sleep(time.Second)
			continue
		}
		*bytesInPaid = bytesIn
		*bytesOutPaid = bytesOut
		paymentTimer = time.After(DefaultNanoPayUpdateInterval)
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
) {
	metadataRaw := CreateRawMetadata(
		serviceID,
		serviceTCP,
		serviceUDP,
		ip,
		tcpPort,
		udpPort,
		price,
		beneficiaryAddr,
	)
	topic := subscriptionPrefix + serviceName
	go func() {
		var waitTime time.Duration
		for {
			txid, err := wallet.Subscribe(
				"",
				topic,
				int(subscriptionDuration),
				string(metadataRaw),
				&nkn.TransactionConfig{Fee: subscriptionFee},
			)
			if err != nil {
				waitTime = time.Second
				log.Println("Couldn't subscribe to topic", topic, "because:", err)
			} else {
				if subscriptionDuration > 3 {
					waitTime = time.Duration(subscriptionDuration-3) * config.ConsensusDuration
				} else {
					waitTime = config.ConsensusDuration
				}
				log.Println("Subscribed to topic", topic, "successfully:", txid)
			}

			time.Sleep(waitTime)
		}
	}()
}

func copyBuffer(dest io.Writer, src io.Reader, written *uint64) error {
	buf := make([]byte, 32768)
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

func Pipe(dest io.WriteCloser, src io.ReadCloser, written *uint64) {
	defer dest.Close()
	defer src.Close()
	copyBuffer(dest, src, written)
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

func ReadJSON(fileName string, value interface{}) error {
	file, err := ioutil.ReadFile(fileName)
	if err != nil {
		return fmt.Errorf("read file error: %v", err)
	}

	err = json.Unmarshal(file, value)
	if err != nil {
		return fmt.Errorf("parse json error: %v", err)
	}

	return nil
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
	tx, err := np.IncrementAmount(cost.String())
	if err != nil {
		return err
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

func nanoPayClaim(txBytes []byte, npc *nkn.NanoPayClaimer, lastClaimed *common.Fixed64) error {
	tx := &transaction.Transaction{}
	if err := tx.Unmarshal(txBytes); err != nil {
		return fmt.Errorf("couldn't unmarshal payment stream data: %v", err)
	}

	amount, err := npc.Claim(tx)
	if err != nil {
		return fmt.Errorf("couldn't accept nano pay update: %v", err)
	}

	*lastClaimed = amount.ToFixed64()
	return nil
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

func checkPayment(session *smux.Session, lastUpdate *time.Time, bytesPaid *common.Fixed64, lastClaimed *common.Fixed64, isClosed *bool, getTotalCost func() (common.Fixed64, common.Fixed64)) {
	lastCost := common.Fixed64(0)
	for {
		for {
			time.Sleep(time.Second)
			if *isClosed {
				return
			}
			totalCost, totalBytes := getTotalCost()
			if totalCost.GetData() == lastCost.GetData() {
				continue
			}
			lastCost = totalCost
			if time.Since(*lastUpdate) > DefaultNanoPayUpdateInterval {
				break
			}
			unpaidTraffic := totalBytes - *bytesPaid
			if unpaidTraffic > MaxTrafficOwned {
				break
			}
		}
		time.Sleep(MaxNanoPayDelay)
		if time.Since(*lastUpdate) > DefaultNanoPayUpdateInterval || *lastClaimed < lastCost {
			Close(session)
			*isClosed = true
			log.Printf("Didn't update nano pay for more than %s or %d cost", (DefaultNanoPayUpdateInterval).String(), lastCost.GetData())
			return
		}
	}
}

func readStreamMetadata(stream *smux.Stream) (*pb.StreamMetadata, error) {
	b, err := ReadVarBytes(stream)
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

func handlePaymentStream(
	stream *smux.Stream,
	npc *nkn.NanoPayClaimer,
	lastClaimed *common.Fixed64,
	lastUpdate *time.Time,
	bytesPaid *common.Fixed64,
	getTotalCost func() (common.Fixed64, common.Fixed64),
) error {
	for {
		tx, err := ReadVarBytes(stream)
		if err != nil {
			return fmt.Errorf("couldn't read payment stream: %v", err)
		}

		_, totalBytes := getTotalCost()

		err = nanoPayClaim(tx, npc, lastClaimed)
		if err != nil {
			log.Println("Couldn't claim nanoPay:", err)
			continue
		}
		*bytesPaid = totalBytes
		*lastUpdate = time.Now()
	}
}
