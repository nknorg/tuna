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
	nkn "github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/nkn/common"
	"github.com/nknorg/nkn/program"
	"github.com/nknorg/nkn/transaction"
	"github.com/nknorg/nkn/util"
	"github.com/nknorg/nkn/util/address"
	"github.com/nknorg/nkn/util/config"
	"github.com/nknorg/nkn/vault"
	"github.com/nknorg/tuna/pb"
	"github.com/xtaci/smux"
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
	MinTrafficCoverage                     = 0.9
	getSubscribersBatchSize                = 32
)

type ServiceInfo struct {
	MaxPrice string    `json:"maxPrice"`
	ListenIP string    `json:"listenIP"`
	IPFilter *IPFilter `json:"ipFilter"`
}

type Service struct {
	Name string   `json:"name"`
	TCP  []uint32 `json:"tcp"`
	UDP  []uint32 `json:"udp"`
}

type Common struct {
	Service            *Service
	Wallet             *nkn.Wallet
	DialTimeout        int32
	SubscriptionPrefix string
	Reverse            bool
	ReverseMetadata    *pb.ServiceMetadata
	ServiceInfo        *ServiceInfo

	udpReadChan  chan []byte
	udpWriteChan chan []byte
	udpCloseChan chan struct{}
	tcpListener  *net.TCPListener

	sync.RWMutex
	paymentReceiver  string
	entryToExitPrice common.Fixed64
	exitToEntryPrice common.Fixed64
	metadata         *pb.ServiceMetadata
	connected        bool
	tcpConn          net.Conn
	udpConn          *net.UDPConn
	isClosed         bool
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

func (c *Common) UpdateServerConn() bool {
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
			log.Println("Couldn't connect to TCP address", address, "because:", err)
			return false
		}
		c.SetServerTCPConn(tcpConn)
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
			log.Println("Couldn't connect to UDP address", address, "because:", err)
			return false
		}
		c.SetServerUDPConn(udpConn)
		log.Println("Connected to UDP at", address.String())

		c.StartUDPReaderWriter(udpConn)
	}
	c.SetConnected(true)

	return true
}

func (c *Common) CreateServerConn(force bool) error {
	entryToExitMaxPrice, exitToEntryMaxPrice, err := ParsePrice(c.ServiceInfo.MaxPrice)
	if err != nil {
		log.Fatalf("Parse price of service error: %v", err)
	}
	if !c.Reverse && (c.GetConnected() == false || force) {
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
					_, publicKey, _, err := address.ParseClientAddress(subscriber)
					if err != nil {
						log.Println(err)
						continue RandomSubscriber
					}

					programHash, err := program.CreateProgramHash(publicKey)
					if err != nil {
						log.Println(err)
						continue RandomSubscriber
					}

					address, err := programHash.ToAddress()
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

				if !c.UpdateServerConn() {
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
	entryToExitPrice, exitToEntryPrice := c.GetPrice()

	time.Sleep(DefaultNanoPayUpdateInterval)

	for {
		select {
		case <-closeChan:
			return
		default:
		}

		bytesIn := atomic.LoadUint64(bytesInUsed)
		bytesOut := atomic.LoadUint64(bytesOutUsed)
		cost := exitToEntryPrice*common.Fixed64(bytesIn-*bytesInPaid)/TrafficUnit + entryToExitPrice*common.Fixed64(bytesOut-*bytesOutPaid)/TrafficUnit
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

		time.Sleep(DefaultNanoPayUpdateInterval)
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

func checkPaymentTimeout(session *smux.Session, updateTimeout time.Duration, lastUpdate *time.Time, isClosed *bool, getTotalCost func() common.Fixed64) {
	totalCost := getTotalCost()
	lastCost := common.Fixed64(0)
	for {
		time.Sleep(5 * time.Second)

		if *isClosed {
			break
		}

		totalCost = getTotalCost()
		if totalCost.GetData() == lastCost.GetData() {
			continue
		}
		lastCost = totalCost

		if time.Since(*lastUpdate) > updateTimeout {
			log.Println("Didn't update nano pay for more than", updateTimeout.String())
			Close(session)
			*isClosed = true
			break
		}
	}
}

func checkTrafficCoverage(session *smux.Session, lastComputed, lastClaimed common.Fixed64, isClosed *bool) bool {
	if float64(lastClaimed) >= float64(lastComputed)*MinTrafficCoverage {
		return true
	}
	log.Printf("Nano pay amount covers less than %f%% of total cost (%d/%d)", MinTrafficCoverage*100, lastClaimed, lastComputed)
	Close(session)
	*isClosed = true
	return false
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

func acceptStream(
	session *smux.Session,
	npc *nkn.NanoPayClaimer,
	lastClaimed *common.Fixed64,
	getTotalCost func() common.Fixed64,
	lastUpdate *time.Time,
	isClosed *bool,
) (*smux.Stream, *pb.StreamMetadata, error) {
	stream, err := session.AcceptStream()
	if err != nil {
		return nil, nil, fmt.Errorf("Couldn't accept stream: %v", err)
	}

	streamMetadata, err := readStreamMetadata(stream)
	if err != nil {
		return nil, nil, fmt.Errorf("Read stream metadata error: %v", err)
	}

	if streamMetadata.IsPayment {
		go func() {
			for {
				tx, err := ReadVarBytes(stream)
				if err != nil {
					log.Println("couldn't read payment stream:", err)
					return
				}

				err = nanoPayClaim(tx, npc, lastClaimed)
				if err != nil {
					log.Println("Couldn't claim nanoPay:", err)
					continue
				}

				totalCost := getTotalCost()
				*lastUpdate = time.Now()

				if !checkTrafficCoverage(session, totalCost, *lastClaimed, isClosed) {
					return
				}
			}
		}()
	}

	return stream, streamMetadata, nil
}
