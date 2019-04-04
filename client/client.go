package main

import (
	"encoding/hex"
	"encoding/json"
	"log"
	"math/rand"
	"net"
	"strconv"
	"time"
	"unsafe"

	. "github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/nkn/vault"
	"github.com/nknorg/tuna"
	"github.com/patrickmn/go-cache"
	"github.com/trueinsider/smux"
)

type Configuration struct {
	DialTimeout uint16   `json:"DialTimeout"`
	UDPTimeout  uint16   `json:"UDPTimeout"`
	PrivateKey  string   `json:"PrivateKey"`
	Services    []string `json:"Services"`
}

type TunaClient struct {
	serviceName string
	config      Configuration
	serviceConn map[int]*net.UDPConn
	clientAddr  *cache.Cache
	wallet      *WalletSDK
	metadata    *tuna.Metadata
	tcpPortIds  map[int]byte
	udpPortIds  map[int]byte
	udpPorts    map[byte]int
	tcpConn     net.Conn
	udpConn     *net.UDPConn
	session     *smux.Session
}

func NewTunaClient(serviceName string, config Configuration) *TunaClient {
	Init()

	privateKey, _ := hex.DecodeString(config.PrivateKey)
	account, err := vault.NewAccountWithPrivatekey(privateKey)
	if err != nil {
		log.Panicln("Couldn't load account:", err)
	}

	wallet := NewWalletSDK(account)

	return &TunaClient{
		serviceName: serviceName,
		config:      config,
		serviceConn: make(map[int]*net.UDPConn),
		clientAddr:  cache.New(time.Duration(config.UDPTimeout)*time.Second, time.Second),
		wallet:      wallet,
	}
}

func (tc *TunaClient) Start() {
	for {
		err := tc.createServerConn(true)
		if err != nil {
			log.Println("Couldn't connect to node:", err)
			time.Sleep(1 * time.Second)
			continue
		}

		tc.listenTCP(tc.metadata.ServiceTCP)
		tc.listenUDP(len(tc.metadata.ServiceTCP), tc.metadata.ServiceUDP)
		break
	}
}

func (tc *TunaClient) getServerTCPConn(force bool) (net.Conn, error) {
	err := tc.createServerConn(force)
	if err != nil {
		return nil, err
	}
	return tc.tcpConn, nil
}

func (tc *TunaClient) getServerUDPConn(force bool) (*net.UDPConn, error) {
	err := tc.createServerConn(force)
	if err != nil {
		return nil, err
	}
	return tc.udpConn, nil
}

func (tc *TunaClient) createServerConn(force bool) error {
	if tc.tcpConn == nil || tc.udpConn == nil || force {
	RandomBucket:
		for {
			lastBucket, err := tc.wallet.GetTopicBucketsCount(tc.serviceName)
			if err != nil {
				return err
			}
			bucket := uint32(rand.Intn(int(lastBucket) + 1))
			subscribers, err := tc.wallet.GetSubscribers(tc.serviceName, bucket)
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
				for subscriber, metadataString := range subscribers {
					if i != subscriberIndex {
						i++
						continue
					}

					tc.metadata = &tuna.Metadata{}
					err := json.Unmarshal([]byte(metadataString), tc.metadata)
					if err != nil {
						log.Println("Couldn't unmarshal metadata:", err)
						continue RandomSubscriber
					}

					hasTCP := len(tc.metadata.ServiceTCP) > 0
					hasUDP := len(tc.metadata.ServiceUDP) > 0

					if hasTCP {
						tuna.Close(tc.tcpConn)

						address := tc.metadata.IP + ":" + strconv.Itoa(tc.metadata.TCPPort)
						tc.tcpConn, err = net.DialTimeout(
							string(tuna.TCP),
							address,
							time.Duration(tc.config.DialTimeout)*time.Second,
						)
						if err != nil {
							log.Println("Couldn't connect to TCP address", address, "from", subscriber, "because:", err)
							continue RandomSubscriber
						}
						log.Println("Connected to TCP at", address, "from", subscriber)

						tc.tcpPortIds = make(map[int]byte)
						for i, port := range tc.metadata.ServiceTCP {
							tc.tcpPortIds[port] = byte(i)
						}
					}
					if hasUDP {
						tuna.Close(tc.udpConn)

						address := net.UDPAddr{IP: net.ParseIP(tc.metadata.IP), Port: tc.metadata.UDPPort}
						tc.udpConn, err = net.DialUDP(
							string(tuna.UDP),
							nil,
							&address,
						)
						if err != nil {
							log.Println("Couldn't connect to UDP address", address, "from", subscriber, "because:", err)
							continue RandomSubscriber
						}
						log.Println("Connected to UDP at", address, "from", subscriber)

						tc.udpPortIds = make(map[int]byte)
						tc.udpPorts = make(map[byte]int)
						for i, port := range tc.metadata.ServiceUDP {
							portId := byte(i)
							tc.udpPortIds[port] = portId
							tc.udpPorts[portId] = port
						}
					}

					break RandomBucket
				}
			}
		}
	}

	return nil
}

func (tc *TunaClient) getSession(force bool) (*smux.Session, error) {
	if tc.session == nil || tc.session.IsClosed() || force {
		conn, err := tc.getServerTCPConn(force)
		if err != nil {
			return nil, err
		}
		tc.session, _ = smux.Client(conn, nil)
	}

	return tc.session, nil
}

func (tc *TunaClient) openStream(port int, force bool) (*smux.Stream, error) {
	session, err := tc.getSession(force)
	if err != nil {
		return nil, err
	}
	serviceId := tc.metadata.ServiceId
	portId := tc.tcpPortIds[port]
	stream, err := session.OpenStream(serviceId, portId)
	if err != nil {
		return tc.openStream(port, true)
	}
	return stream, err
}

func (tc *TunaClient) listenTCP(ports []int) {
	for _, _port := range ports {
		port := _port
		listener, err := net.ListenTCP(string(tuna.TCP), &net.TCPAddr{Port: port})
		if err != nil {
			log.Panicln("Couldn't bind listener:", err)
		}

		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					log.Println("Couldn't accept connection:", err)
					tuna.Close(conn)
					continue
				}

				stream, err := tc.openStream(port, false)
				if err != nil {
					log.Println("Couldn't open stream:", err)
					tuna.Close(conn)
					continue
				}

				go tuna.Pipe(stream, conn)
				go tuna.Pipe(conn, stream)
			}
		}()
	}
}

func (tc *TunaClient) listenUDP(portIdOffset int, ports []int) {
	if len(ports) == 0 {
		return
	}

	go func() {
		buffer := make([]byte, 2048)
		for {
			serverConn, err := tc.getServerUDPConn(false)
			if err != nil {
				log.Println("Couldn't get server connection:", err)
				continue
			}

			n, err := serverConn.Read(buffer)
			if err != nil {
				log.Println("Couldn't receive data from server:", err)
				continue
			}

			portId := buffer[3]
			port := tc.udpPorts[portId]
			connId := tuna.GetConnIdString(buffer)

			var serviceConn *net.UDPConn
			var ok bool
			if serviceConn, ok = tc.serviceConn[port]; !ok {
				log.Println("Couldn't get service conn for port:", port)
				continue
			}

			var x interface{}
			if x, ok = tc.clientAddr.Get(connId); !ok {
				log.Println("Couldn't get client address for:", connId)
				continue
			}
			clientAddr := x.(*net.UDPAddr)

			_, err = serviceConn.WriteToUDP(buffer[4:n], clientAddr)
			if err != nil {
				log.Println("Couldn't send data to client:", err)
			}
		}
	}()

	for _, _port := range ports {
		port := _port
		localConn, err := net.ListenUDP(string(tuna.UDP), &net.UDPAddr{Port: port})
		if err != nil {
			log.Panicln("Couldn't bind listener:", err)
		}

		tc.serviceConn[port] = localConn

		go func() {
			localBuffer := make([]byte, 2048)
			for {
				n, addr, err := localConn.ReadFromUDP(localBuffer)
				if err != nil {
					log.Println("Couldn't receive data from local:", err)
					continue
				}

				connKey := strconv.Itoa(addr.Port)
				tc.clientAddr.Set(connKey, addr, cache.DefaultExpiration)

				serverConn, err := tc.getServerUDPConn(false)
				if err != nil {
					log.Println("Couldn't get remote connection:", err)
					continue
				}
				connId := GetConnIdData(addr.Port)
				serviceId := tc.metadata.ServiceId
				portId := tc.udpPortIds[port]
				_, err = serverConn.Write(append([]byte{connId[0], connId[1], serviceId, portId}, localBuffer[:n]...))
				if err != nil {
					log.Println("Couldn't send data to server:", err)
				}
			}
		}()
	}
}

func GetConnIdData(port int) [2]byte {
	return *(*[2]byte)(unsafe.Pointer(&port))
}

func main() {
	config := Configuration{}
	tuna.ReadJson("config.json", &config)

	for _, serviceName := range config.Services {
		NewTunaClient(serviceName, config).Start()
	}

	select {}
}
