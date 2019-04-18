package tuna

import (
	"encoding/json"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unsafe"

	. "github.com/nknorg/nkn-sdk-go"
)

type Protocol string

const TCP Protocol = "tcp"
const UDP Protocol = "udp"

const DefaultSubscriptionPrefix string = "tuna%1."

type Metadata struct {
	IP         string `json:"ip"`
	TCPPort    int    `json:"tcpPort"`
	UDPPort    int    `json:"udpPort"`
	ServiceId  byte   `json:"serviceId"`
	ServiceTCP []int  `json:"serviceTcp"`
	ServiceUDP []int  `json:"serviceUdp"`
}

type Common struct {
	ServiceName        string
	Wallet             *WalletSDK
	DialTimeout        uint16
	SubscriptionPrefix string
	Reverse            bool

	Metadata   *Metadata
	TCPPortIds map[int]byte
	UDPPortIds map[int]byte
	UDPPorts   map[byte]int

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
	if hasTCP {
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
	if hasUDP {
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
		RandomBucket:
			for {
				lastBucket, err := c.Wallet.GetTopicBucketsCount(topic)
				if err != nil {
					return err
				}
				bucket := uint32(rand.Intn(int(lastBucket) + 1))
				subscribers, err := c.Wallet.GetSubscribers(topic, bucket)
				if err != nil {
					return err
				}
				subscribersCount := len(subscribers)

				subscribersIndexes := rand.Perm(subscribersCount)

			RandomSubscriber:
				for {
					if len(subscribersIndexes) == 0 {
						continue RandomBucket
					}
					var subscriberIndex int
					subscriberIndex, subscribersIndexes = subscribersIndexes[0], subscribersIndexes[1:]
					i := 0
					for _, metadataString := range subscribers {
						if i != subscriberIndex {
							i++
							continue
						}

						if !c.SetMetadata(metadataString) {
							continue RandomSubscriber
						}

						if !c.UpdateServerConn() {
							continue RandomSubscriber
						}

						break RandomBucket
					}
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
) []byte {
	metadata := Metadata{
		IP:         ip,
		TCPPort:    tcpPort,
		UDPPort:    udpPort,
		ServiceId:  serviceId,
		ServiceTCP: serviceTCP,
		ServiceUDP: serviceUDP,
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
	subscriptionPrefix string,
	subscriptionDuration uint32,
	subscriptionInterval uint32,
	wallet *WalletSDK,
) {
	metadataRaw := CreateRawMetadata(
		serviceId,
		serviceTCP,
		serviceUDP,
		ip,
		tcpPort,
		udpPort,
	)
	topic := subscriptionPrefix + serviceName
	go func() {
		var waitTime time.Duration
		for {
			txid, err := wallet.SubscribeToFirstAvailableBucket(
				"",
				topic,
				subscriptionDuration,
				string(metadataRaw),
			)
			if err != nil {
				waitTime = time.Duration(subscriptionInterval) * time.Second
				if err == AlreadySubscribed {
					log.Println("Already subscribed to topic", topic)
				} else {
					log.Println("Couldn't subscribe to topic", topic, "because:", err)
				}
			} else {
				waitTime = time.Duration(subscriptionDuration) * 20 * time.Second
				log.Println("Subscribed to topic", topic, "successfully:", txid)
			}

			time.Sleep(waitTime)
		}
	}()
}

func Pipe(dest io.WriteCloser, src io.ReadCloser) {
	defer dest.Close()
	defer src.Close()
	n, err := io.Copy(dest, src)
	if err != nil {
		log.Println("Pipe closed (written "+strconv.FormatInt(n, 10)+") with error:", err)
	}
	log.Println("Pipe closed (written " + strconv.FormatInt(n, 10) + ")")
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
