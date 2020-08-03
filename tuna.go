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
	"github.com/nknorg/nkn/v2/common"
	"github.com/nknorg/nkn/v2/crypto/ed25519"
	"github.com/nknorg/nkn/v2/transaction"
	"github.com/nknorg/nkn/v2/util"
	"github.com/nknorg/nkn/v2/util/address"
	"github.com/nknorg/nkn/v2/util/config"
	"github.com/nknorg/nkn/v2/vault"
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
	MaxTrafficOwned                        = 32
	MinTrafficCoverage                     = 0.9
	TrafficDelay                           = 10 * time.Second
	MaxNanoPayDelay                        = 20 * time.Second
	getSubscribersBatchSize                = 32
	DefaultEncryptionAlgo                  = pb.ENCRYPTION_NONE
	subscribeDurationRandomFactor          = 0.1
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
	closeChan      chan struct{}

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
		closeChan:          make(chan struct{}),
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

func (c *Common) encryptConn(conn net.Conn, remotePublicKey []byte) (net.Conn, error) {
	var connNonce []byte
	var encryptionAlgo pb.EncryptionAlgo

	if len(remotePublicKey) > 0 {
		encryptionAlgo = c.encryptionAlgo

		err := writeConnMetadata(conn, &pb.ConnectionMetadata{
			EncryptionAlgo: encryptionAlgo,
			PublicKey:      c.Wallet.PubKey(),
		})
		if err != nil {
			return nil, err
		}

		connMetadata, err := readConnMetadata(conn)
		if err != nil {
			return nil, err
		}

		connNonce = connMetadata.Nonce
	} else {
		connNonce = util.RandomBytes(connNonceSize)

		err := writeConnMetadata(conn, &pb.ConnectionMetadata{
			Nonce: connNonce,
		})
		if err != nil {
			return nil, err
		}

		connMetadata, err := readConnMetadata(conn)
		if err != nil {
			return nil, err
		}

		if len(connMetadata.PublicKey) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("invalid pubkey size %d", len(connMetadata.PublicKey))
		}

		encryptionAlgo = connMetadata.EncryptionAlgo
		remotePublicKey = connMetadata.PublicKey
	}

	if encryptionAlgo == pb.ENCRYPTION_NONE {
		return conn, nil
	}

	sharedKey, err := c.getOrComputeSharedKey(remotePublicKey)
	if err != nil {
		return nil, err
	}

	encryptKey := computeEncryptKey(connNonce, sharedKey[:])

	return encryptConn(conn, encryptKey, encryptionAlgo)
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

	c.OnConnect.receive()

	return nil
}

func (c *Common) CreateServerConn(force bool) error {
	entryToExitMaxPrice, exitToEntryMaxPrice, err := ParsePrice(c.ServiceInfo.MaxPrice)
	if err != nil {
		log.Fatalf("Parse price of service error: %v", err)
	}

	if !c.IsServer && (!c.GetConnected() || force) {
		topic := c.SubscriptionPrefix + c.Service.Name
		for {
			err = c.SetPaymentReceiver("")
			if err != nil {
				return err
			}

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

			allSubscribers := make([]string, 0, len(subscribers.Subscribers.Map))
			for subscriber := range subscribers.Subscribers.Map {
				allSubscribers = append(allSubscribers, subscriber)
			}

			rand.Shuffle(len(allSubscribers), func(i, j int) {
				allSubscribers[i], allSubscribers[j] = allSubscribers[j], allSubscribers[i]
			})

			for _, subscriber := range allSubscribers {
				metadataString := subscribers.Subscribers.Map[subscriber]
				if !c.SetMetadata(metadataString) {
					continue
				}

				metadata := c.GetMetadata()

				entryToExitPrice, exitToEntryPrice, err := ParsePrice(metadata.Price)
				if err != nil {
					log.Println(err)
					continue
				}

				if entryToExitPrice > entryToExitMaxPrice || exitToEntryPrice > exitToEntryMaxPrice {
					continue
				}

				res, err := c.ServiceInfo.IPFilter.AllowIP(metadata.Ip)
				if err != nil {
					log.Println(err)
				}
				if !res {
					continue
				}

				if len(metadata.BeneficiaryAddr) > 0 {
					err = c.SetPaymentReceiver(metadata.BeneficiaryAddr)
					if err != nil {
						log.Println(err)
						continue
					}
				} else {
					address, err := nkn.ClientAddrToWalletAddr(subscriber)
					if err != nil {
						log.Println(err)
						continue
					}

					err = c.SetPaymentReceiver(address)
					if err != nil {
						log.Println(err)
						continue
					}
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

	paymentTimer := time.After(DefaultNanoPayUpdateInterval)
	for {
		select {
		case <-c.closeChan:
			return
		case <-time.After(100 * time.Millisecond):
			bytesEntryToExit = atomic.LoadUint64(bytesEntryToExitUsed)
			bytesExitToEntry = atomic.LoadUint64(bytesExitToEntryUsed)
			if (bytesEntryToExit+bytesExitToEntry)-(*bytesEntryToExitPaid+*bytesExitToEntryPaid) <= MaxTrafficOwned*TrafficUnit {
				continue
			}
		case <-paymentTimer:
			paymentTimer = time.After(time.Second)
		}

		bytesEntryToExit = atomic.LoadUint64(bytesEntryToExitUsed)
		bytesExitToEntry = atomic.LoadUint64(bytesExitToEntryUsed)
		cost = entryToExitPrice*common.Fixed64(bytesEntryToExit-*bytesEntryToExitPaid)/TrafficUnit + exitToEntryPrice*common.Fixed64(bytesExitToEntry-*bytesExitToEntryPaid)/TrafficUnit
		if cost == lastCost {
			continue
		}

		paymentStream, err := getPaymentStream()
		if err != nil {
			log.Printf("Get payment stream err: %v", err)
			continue
		}

		paymentReceiver := c.GetPaymentReceiver()
		if np == nil || np.Recipient() != paymentReceiver {
			np, err = c.Wallet.NewNanoPay(paymentReceiver, nanoPayFee, DefaultNanoPayDuration)
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

		*bytesEntryToExitPaid = bytesEntryToExit
		*bytesExitToEntryPaid = bytesExitToEntry
		lastCost = cost
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
			time.AfterFunc(TrafficDelay, func() {
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

			if time.Since(*lastPaymentTime) > DefaultNanoPayUpdateInterval {
				break
			}

			if totalBytes-*bytesPaid > MaxTrafficOwned*TrafficUnit {
				break
			}
		}

		time.Sleep(MaxNanoPayDelay)

		if time.Since(*lastPaymentTime) > DefaultNanoPayUpdateInterval || *lastPaymentAmount < common.Fixed64(MinTrafficCoverage*float64(totalCost)) {
			Close(session)
			*isClosed = true
			log.Printf("Not enough payment. Since last payment: %s. Last claimed: %v, expected: %v", time.Since(*lastPaymentTime).String(), *lastPaymentAmount, totalCost)
			return
		}
	}
}

func handlePaymentStream(stream *smux.Stream, npc *nkn.NanoPayClaimer, lastPaymentTime *time.Time, lastPaymentAmount, bytesPaid *common.Fixed64, getTotalCost func() (common.Fixed64, common.Fixed64)) error {
	for {
		tx, err := ReadVarBytes(stream)
		if err != nil {
			return fmt.Errorf("couldn't read payment stream: %v", err)
		}

		_, totalBytes := getTotalCost()

		amount, err := nanoPayClaim(tx, npc)
		if err != nil {
			log.Println("Couldn't claim nanoPay:", err)
			if npc.IsClosed() {
				return fmt.Errorf("nanopayclaimer closed: %v", err)
			}
			continue
		}

		*lastPaymentAmount = amount.ToFixed64()
		*lastPaymentTime = time.Now()
		*bytesPaid = totalBytes
	}
}
