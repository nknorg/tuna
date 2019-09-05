package tuna

import (
	"encoding/json"
	"errors"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	. "github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/nkn/common"
	"github.com/nknorg/nkn/crypto"
	"github.com/nknorg/nkn/program"
	"github.com/nknorg/nkn/util/address"
	"github.com/nknorg/nkn/util/config"
)

type Protocol string

const TCP Protocol = "tcp"
const UDP Protocol = "udp"

const DefaultSubscriptionPrefix string = "tuna+1."

type Metadata struct {
	IP         string `json:"ip"`
	TCPPort    int    `json:"tcpPort"`
	UDPPort    int    `json:"udpPort"`
	ServiceId  byte   `json:"serviceId"`
	ServiceTCP []int  `json:"serviceTcp"`
	ServiceUDP []int  `json:"serviceUdp"`
	Price      string `json:"price"`
}

type Common struct {
	ServiceName        string
	MaxPrice           common.Fixed64
	Wallet             *WalletSDK
	DialTimeout        uint16
	SubscriptionPrefix string
	Reverse            bool
	ReverseMetadata    *Metadata

	Price           common.Fixed64
	PaymentReceiver string
	Metadata        *Metadata
	TCPPortIds      map[int]byte
	UDPPortIds      map[int]byte
	UDPPorts        map[byte]int

	connected    bool
	tcpConn      net.Conn
	udpConn      *net.UDPConn
	udpReadChan  chan []byte
	udpWriteChan chan []byte
	udpCloseChan chan struct{}
	tcpListener  *net.TCPListener
}

func (c *Common) SetServerTCPConn(conn net.Conn) {
	c.tcpConn = conn
}

func (c *Common) GetServerTCPConn(force bool) (net.Conn, error) {
	err := c.CreateServerConn(force)
	if err != nil {
		return nil, err
	}
	return c.tcpConn, nil
}

func (c *Common) GetServerUDPConn(force bool) (*net.UDPConn, error) {
	err := c.CreateServerConn(force)
	if err != nil {
		return nil, err
	}
	return c.udpConn, nil
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

func (c *Common) SetMetadata(metadataString string) bool {
	var err error
	c.Metadata, err = ReadMetadata(metadataString)
	if err != nil {
		log.Println("Couldn't unmarshal metadata:", err)
		return false
	}
	return true
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
	hasTCP := len(c.Metadata.ServiceTCP) > 0
	hasUDP := len(c.Metadata.ServiceUDP) > 0

	var err error
	if hasTCP || c.ReverseMetadata != nil {
		if !c.Reverse {
			Close(c.tcpConn)

			address := c.Metadata.IP + ":" + strconv.Itoa(c.Metadata.TCPPort)
			c.tcpConn, err = net.DialTimeout(
				string(TCP),
				address,
				time.Duration(c.DialTimeout)*time.Second,
			)
			if err != nil {
				log.Println("Couldn't connect to TCP address", address, "because:", err)
				return false
			}
			log.Println("Connected to TCP at", address)
		}

		c.TCPPortIds = make(map[int]byte)
		for i, port := range c.Metadata.ServiceTCP {
			c.TCPPortIds[port] = byte(i)
		}
	}
	if hasUDP || c.ReverseMetadata != nil {
		if !c.Reverse {
			Close(c.udpConn)

			address := net.UDPAddr{IP: net.ParseIP(c.Metadata.IP), Port: c.Metadata.UDPPort}
			c.udpConn, err = net.DialUDP(
				string(UDP),
				nil,
				&address,
			)
			if err != nil {
				log.Println("Couldn't connect to UDP address", address, "because:", err)
				return false
			}
			log.Println("Connected to UDP at", address)

			c.StartUDPReaderWriter(c.udpConn)
		}

		c.UDPPortIds = make(map[int]byte)
		c.UDPPorts = make(map[byte]int)
		for i, port := range c.Metadata.ServiceUDP {
			portId := byte(i)
			c.UDPPortIds[port] = portId
			c.UDPPorts[portId] = port
		}
	}
	c.connected = true

	return true
}

func (c *Common) CreateServerConn(force bool) error {
	if c.connected == false || force {
		if c.Reverse {
			c.UpdateServerConn()
		} else {
			topic := c.SubscriptionPrefix + c.ServiceName
		RandomSubscriber:
			for {
				c.PaymentReceiver = ""
				subscribersCount, err := c.Wallet.GetSubscribersCount(topic)
				if err != nil {
					return err
				}
				if subscribersCount == 0 {
					return errors.New("there is no service providers for " + c.ServiceName)
				}
				offset := uint32(rand.Intn(int(subscribersCount)))
				subscribers, _, err := c.Wallet.GetSubscribers(topic, offset, 1, true, false)
				if err != nil {
					return err
				}

				for subscriber, metadataString := range subscribers {
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

					paymentReceiver, err := programHash.ToAddress()
					if err != nil {
						log.Println(err)
						continue RandomSubscriber
					}

					c.PaymentReceiver = paymentReceiver
					if !c.SetMetadata(metadataString) {
						continue RandomSubscriber
					}

					price, err := common.StringToFixed64(c.Metadata.Price)
					if err != nil {
						log.Println(err)
						continue RandomSubscriber
					}
					if price > c.MaxPrice {
						log.Printf("Price %s is bigger than max allowed price %s\n", price.String(), c.MaxPrice.String())
						continue RandomSubscriber
					}
					c.Price = price

					if c.ReverseMetadata != nil {
						c.Metadata.ServiceTCP = c.ReverseMetadata.ServiceTCP
						c.Metadata.ServiceUDP = c.ReverseMetadata.ServiceUDP
					}

					if !c.UpdateServerConn() {
						continue RandomSubscriber
					}

					break RandomSubscriber
				}
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
) []byte {
	metadata := Metadata{
		IP:         ip,
		TCPPort:    tcpPort,
		UDPPort:    udpPort,
		ServiceId:  serviceId,
		ServiceTCP: serviceTCP,
		ServiceUDP: serviceUDP,
		Price:      price,
	}
	metadataRaw, err := json.Marshal(metadata)
	if err != nil {
		log.Panicln(err)
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
	subscriptionPrefix string,
	subscriptionDuration uint32,
	subscriptionFee string,
	wallet *WalletSDK,
) {
	metadataRaw := CreateRawMetadata(
		serviceId,
		serviceTCP,
		serviceUDP,
		ip,
		tcpPort,
		udpPort,
		price,
	)
	topic := subscriptionPrefix + serviceName
	go func() {
		var waitTime time.Duration
		for {
			txid, err := wallet.Subscribe(
				"",
				topic,
				subscriptionDuration,
				string(metadataRaw),
				subscriptionFee,
			)
			if err != nil {
				waitTime = time.Second
				log.Println("Couldn't subscribe to topic", topic, "because:", err)
			} else {
				waitTime = time.Duration(subscriptionDuration) * config.ConsensusDuration
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
	if err := copyBuffer(dest, src, written); err != nil {
		//log.Println("Pipe closed with error:", err)
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

func ReadJson(fileName string, value interface{}) {
	file, err := ioutil.ReadFile(fileName)
	if err != nil {
		log.Panicln("Couldn't read file:", err)
	}

	err = json.Unmarshal(file, value)
	if err != nil {
		log.Panicln("Couldn't unmarshal json:", err)
	}
}

func GetConnIdString(data []byte) string {
	return strconv.Itoa(int(*(*uint16)(unsafe.Pointer(&data[0]))))
}
