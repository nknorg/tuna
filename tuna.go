package tuna

import (
	"bytes"
	"encoding/base64"
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
	"unsafe"

	"github.com/trueinsider/smux"

	"github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/nkn/common"
	"github.com/nknorg/nkn/crypto"
	"github.com/nknorg/nkn/crypto/util"
	"github.com/nknorg/nkn/program"
	"github.com/nknorg/nkn/transaction"
	"github.com/nknorg/nkn/util/address"
	"github.com/nknorg/nkn/util/config"
	"github.com/nknorg/nkn/vault"
)

type Protocol string

const (
	TCP                           Protocol = "tcp"
	UDP                           Protocol = "udp"
	DefaultNanoPayDuration                 = 4320 * 30
	DefaultNanoPayUpdateInterval           = time.Minute
	DefaultSubscriptionPrefix              = "tuna+1."
	DefaultReverseServiceName              = "reverse"
	DefaultServiceListenIP                 = "127.0.0.1"
	DefaultReverseServiceListenIP          = "0.0.0.0"
	TrafficUnit                            = 1024 * 1024
)

type Service struct {
	Name string `json:"name"`
	TCP  []int  `json:"tcp"`
	UDP  []int  `json:"udp"`
}

type Metadata struct {
	IP              string `json:"ip"`
	TCPPort         int    `json:"tcpPort"`
	UDPPort         int    `json:"udpPort"`
	ServiceId       byte   `json:"serviceId"`
	ServiceTCP      []int  `json:"serviceTcp,omitempty"`
	ServiceUDP      []int  `json:"serviceUdp,omitempty"`
	Price           string `json:"price,omitempty"`
	BeneficiaryAddr string `json:"beneficiaryAddr,omitempty"`
}

type Common struct {
	Service             *Service
	ListenIP            net.IP
	EntryToExitMaxPrice common.Fixed64
	ExitToEntryMaxPrice common.Fixed64
	Wallet              *nkn.Wallet
	DialTimeout         uint16
	SubscriptionPrefix  string
	Reverse             bool
	ReverseMetadata     *Metadata

	udpReadChan  chan []byte
	udpWriteChan chan []byte
	udpCloseChan chan struct{}
	tcpListener  *net.TCPListener

	sync.RWMutex
	paymentReceiver  string
	entryToExitPrice common.Fixed64
	exitToEntryPrice common.Fixed64
	metadata         *Metadata
	connected        bool
	tcpConn          net.Conn
	udpConn          *net.UDPConn
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
	c.RLock()
	defer c.RUnlock()
	return c.GetTCPConn(), nil
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

func (c *Common) GetMetadata() *Metadata {
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
	hasTCP := len(c.Service.TCP) > 0 || (c.ReverseMetadata != nil && len(c.ReverseMetadata.ServiceTCP) > 0)
	hasUDP := len(c.Service.UDP) > 0 || (c.ReverseMetadata != nil && len(c.ReverseMetadata.ServiceUDP) > 0)
	metadata := c.GetMetadata()

	if hasTCP {
		Close(c.GetTCPConn())

		address := metadata.IP + ":" + strconv.Itoa(metadata.TCPPort)
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

		address := net.UDPAddr{IP: net.ParseIP(metadata.IP), Port: metadata.UDPPort}
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
			offset := rand.Intn(int(subscribersCount))
			subscribers, err := c.Wallet.GetSubscribers(topic, offset, 1, true, false)
			if err != nil {
				return err
			}

			for subscriber, metadataString := range subscribers.Subscribers.Map {
				if !c.SetMetadata(metadataString) {
					continue RandomSubscriber
				}

				metadata := c.GetMetadata()

				if len(metadata.BeneficiaryAddr) > 0 {
					c.SetPaymentReceiver(metadata.BeneficiaryAddr)
				} else {
					_, publicKey, _, err := address.ParseClientAddress(subscriber)
					if err != nil {
						log.Println(err)
						continue RandomSubscriber
					}

					pubKey, err := crypto.NewPubKeyFromBytes(publicKey)
					if err != nil {
						log.Println(err)
						continue RandomSubscriber
					}

					programHash, err := program.CreateProgramHash(pubKey)
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

				if entryToExitPrice > c.EntryToExitMaxPrice {
					log.Printf("Entry to exit price %s is bigger than max allowed price %s\n", entryToExitPrice.String(), c.EntryToExitMaxPrice.String())
					continue RandomSubscriber
				}
				if exitToEntryPrice > c.ExitToEntryMaxPrice {
					log.Printf("Exit to entry price %s is bigger than max allowed price %s\n", exitToEntryPrice.String(), c.ExitToEntryMaxPrice.String())
					continue RandomSubscriber
				}

				c.Lock()
				c.entryToExitPrice = entryToExitPrice
				c.exitToEntryPrice = exitToEntryPrice
				if c.ReverseMetadata != nil {
					c.metadata.ServiceTCP = c.ReverseMetadata.ServiceTCP
					c.metadata.ServiceUDP = c.ReverseMetadata.ServiceUDP
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

func ReadMetadata(metadataString string) (*Metadata, error) {
	metadata := &Metadata{}
	err := json.Unmarshal([]byte(metadataString), metadata)
	return metadata, err
}

func CreateRawMetadata(
	serviceId byte,
	serviceTCP []int,
	serviceUDP []int,
	ip string,
	tcpPort int,
	udpPort int,
	price string,
	beneficiaryAddr string,
) []byte {
	metadata := Metadata{
		IP:              ip,
		TCPPort:         tcpPort,
		UDPPort:         udpPort,
		ServiceId:       serviceId,
		ServiceTCP:      serviceTCP,
		ServiceUDP:      serviceUDP,
		Price:           price,
		BeneficiaryAddr: beneficiaryAddr,
	}
	metadataRaw, err := json.Marshal(metadata)
	if err != nil {
		log.Fatalln(err)
	}
	return metadataRaw
}

func UpdateMetadata(
	serviceName string,
	serviceId byte,
	serviceTCP []int,
	serviceUDP []int,
	ip string,
	tcpPort int,
	udpPort int,
	price string,
	beneficiaryAddr string,
	subscriptionPrefix string,
	subscriptionDuration uint32,
	subscriptionFee string,
	wallet *nkn.Wallet,
) {
	metadataRaw := CreateRawMetadata(
		serviceId,
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
				subscriptionFee,
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

func ReadJson(fileName string, value interface{}) error {
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

func GetConnIdString(data []byte) string {
	return strconv.Itoa(int(*(*uint16)(unsafe.Pointer(&data[0]))))
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
	var wallet *vault.WalletImpl
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
		wallet, err = vault.NewWallet(walletFile, []byte(pswd), true)
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

func sendNanoPay(np *nkn.NanoPay, session *smux.Session, w *nkn.Wallet, cost *common.Fixed64, c *Common, nanoPayFee string) error {
	var err error
	paymentReceiver := c.GetPaymentReceiver()
	if np == nil || np.Address() != paymentReceiver {
		np, err = w.NewNanoPay(paymentReceiver, nanoPayFee, DefaultNanoPayDuration)
		if err != nil {
			return err
		}
	}
	tx, err := np.IncrementAmount(cost.String())
	if err != nil {
		return err
	}
	txData := tx.ToArray()
	stream, err := session.OpenStream()
	if err != nil {
		return err
	}
	n, err := stream.Write(txData)
	if err != nil {
		return err
	}
	stream.Close()
	if n != len(txData) {
		return fmt.Errorf("send txData err: should be %d, have %d", len(txData), n)
	}
	return nil
}

func nanoPayClaim(stream *smux.Stream, npc *nkn.NanoPayClaimer, lastComputed, lastClaimed, totalCost *common.Fixed64, lastUpdate *time.Time) error {
	txData, err := ioutil.ReadAll(stream)
	if err != nil && err.Error() != io.EOF.Error() {
		return fmt.Errorf("couldn't read payment stream: %v", err)
	}
	if len(txData) == 0 {
		return fmt.Errorf("no data received")
	}
	tx := new(transaction.Transaction)
	if err := tx.Unmarshal(txData); err != nil {
		return fmt.Errorf("couldn't unmarshal payment stream data: %v", err)
	}

	amount, err := npc.Claim(tx)
	if err != nil {
		return fmt.Errorf("couldn't accept nano pay update: %v", err)
	}

	*lastComputed = *totalCost
	lc := amount.ToFixed64()
	*lastClaimed = lc
	lu := time.Now()
	*lastUpdate = lu
	return nil
}

func claimCheck(session *smux.Session, npc *nkn.NanoPayClaimer, onErr *nkn.OnError, isClosed *bool) {
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
func paymentCheck(session *smux.Session, claimInterval time.Duration, lastComputed, lastClaimed *common.Fixed64, lastUpdate *time.Time, isClosed *bool) {
	for {
		time.Sleep(claimInterval)

		if *isClosed {
			break
		}

		if time.Since(*lastUpdate) > claimInterval {
			log.Println("Didn't update nano pay for more than", claimInterval.String())
			Close(session)
			*isClosed = true
			break
		}

		if common.Fixed64(float64(*lastComputed)*0.9) > *lastClaimed {
			log.Println("Nano pay amount covers less than 90% of total cost")
			Close(session)
			*isClosed = true
			break
		}
	}
}
