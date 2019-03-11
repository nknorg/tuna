package main

import (
	"encoding/hex"
	"log"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"time"
	"unsafe"

	. "github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/nkn/vault"
	"github.com/nknorg/tuna"
	"github.com/trueinsider/smux"
)

type Configuration struct {
	DialTimeout uint16   `json:"DialTimeout"`
	PrivateKey  string   `json:"PrivateKey"`
	Services    []string `json:"Services"`
}

type TunaClient struct {
	config      Configuration
	tcpConn     map[byte]net.Conn
	udpConn     map[byte]*net.UDPConn
	serviceConn map[connKey]*net.UDPConn
	clientAddr  map[uint16]*net.UDPAddr
	session     map[byte]*smux.Session
	wallet      *WalletSDK
}

type connKey struct {
	serviceId byte
	portId    byte
}

func NewTunaClient() *TunaClient {
	tuna.Init()
	Init()

	config := Configuration{}
	tuna.ReadJson("config.json", &config)

	privateKey, _ := hex.DecodeString(config.PrivateKey)
	account, err := vault.NewAccountWithPrivatekey(privateKey)
	if err != nil {
		log.Panicln("Couldn't load account:", err)
	}

	wallet := NewWalletSDK(account)

	return &TunaClient{
		config,
		make(map[byte]net.Conn),
		make(map[byte]*net.UDPConn),
		make(map[connKey]*net.UDPConn),
		make(map[uint16]*net.UDPAddr),
		make(map[byte]*smux.Session),
		wallet,
	}
}

func (tc *TunaClient) Start() {
	for _, serviceName := range tc.config.Services {
		serviceId, err := tuna.GetServiceId(serviceName)
		if err != nil {
			log.Panicln(err)
		}
		service := tuna.Services[serviceId]
		tc.listenTCP(serviceId, service.TCP)
		tc.listenUDP(serviceId, len(service.TCP), service.UDP)
	}
}

func (tc *TunaClient) getServerTCPConn(serviceId byte, force bool) (net.Conn, error) {
	err := tc.createServerConn(serviceId, force)
	if err != nil {
		return nil, err
	}
	return tc.tcpConn[serviceId], nil
}

func (tc *TunaClient) getServerUDPConn(serviceId byte, force bool) (*net.UDPConn, error) {
	err := tc.createServerConn(serviceId, force)
	if err != nil {
		return nil, err
	}
	return tc.udpConn[serviceId], nil
}

func (tc *TunaClient) createServerConn(serviceId byte, force bool) error {
	service := tuna.Services[serviceId]
	hasTCP := len(service.TCP) > 0
	hasUDP := len(service.UDP) > 0

	if hasTCP && tc.tcpConn[serviceId] == nil || hasUDP && tc.udpConn[serviceId] == nil || force {
		serviceName := tuna.Services[serviceId].Name

		RandomBucket:
		for {
			lastBucket, err := tc.wallet.GetTopicBucketsCount(serviceName)
			if err != nil {
				return err
			}
			bucket := uint32(rand.Intn(int(lastBucket) + 1))
			subscribers, err := tc.wallet.GetSubscribers(serviceName, bucket)
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
				for subscriber, metadata := range subscribers {
					if i != subscriberIndex {
						i++
						continue
					}

					addressParts := strings.Split(metadata, ",")

					if hasTCP {
						tuna.Close(tc.tcpConn[serviceId])
						address := addressParts[0] + ":" + addressParts[1]
						tc.tcpConn[serviceId], err = net.DialTimeout(
							string(tuna.TCP),
							address,
							time.Duration(tc.config.DialTimeout)*time.Second,
						)
						if err != nil {
							log.Println("Couldn't connect to TCP address", address, "from", subscriber, "because:", err)
							continue RandomSubscriber
						}
						log.Println("Connected to TCP at", address, "from", subscriber)
					}
					if hasUDP {
						tuna.Close(tc.udpConn[serviceId])
						port, _ := strconv.Atoi(addressParts[2])
						address := net.UDPAddr{IP: net.ParseIP(addressParts[0]), Port: port}
						tc.udpConn[serviceId], err = net.DialUDP(
							string(tuna.UDP),
							nil,
							&address,
						)
						if err != nil {
							log.Println("Couldn't connect to UDP address", address, "from", subscriber, "because:", err)
							continue RandomSubscriber
						}
						log.Println("Connected to UDP at", address, "from", subscriber)
					}

					break RandomBucket
				}
			}
		}
	}

	return nil
}

func (tc *TunaClient) getSession(serviceId byte, force bool) (*smux.Session, error) {
	if tc.session[serviceId] == nil || tc.session[serviceId].IsClosed() || force {
		conn, err := tc.getServerTCPConn(serviceId, force)
		if err != nil {
			return nil, err
		}
		tc.session[serviceId], _ = smux.Client(conn, nil)
		return tc.session[serviceId], nil
	}

	return tc.session[serviceId], nil
}

func (tc *TunaClient) openStream(serviceId byte, portId byte, force bool) (*smux.Stream, error) {
	session, err := tc.getSession(serviceId, force)
	if err != nil {
		return nil, err
	}
	stream, err := session.OpenStream(serviceId, portId)
	if err != nil {
		return tc.openStream(serviceId, portId, true)
	}
	return stream, err
}

func (tc *TunaClient) listenTCP(serviceId byte, ports []int) {
	for i, port := range ports {
		listener, err := net.ListenTCP(string(tuna.TCP), &net.TCPAddr{Port: port})
		if err != nil {
			log.Panicln("Couldn't bind listener:", err)
		}
		portId := byte(i)
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					log.Println("Couldn't accept connection:", err)
					tuna.Close(conn)
					continue
				}

				stream, err := tc.openStream(serviceId, portId, false)
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

func (tc *TunaClient) listenUDP(serviceId byte, portIdOffset int, ports []int) {
	service := tuna.Services[serviceId]
	hasUDP := len(service.UDP) > 0

	if !hasUDP {
		return
	}

	go func() {
		buffer := make([]byte, 2048)
		for {
			serverConn, err := tc.getServerUDPConn(serviceId, false)
			if err != nil {
				log.Println("Couldn't get server connection:", err)
				continue
			}

			n, err := serverConn.Read(buffer)
			if err != nil {
				log.Println("Couldn't receive data from server:", err)
				continue
			}

			connKey := connKey{buffer[2], buffer[3]}
			connId := *(*uint16)(unsafe.Pointer(&buffer[0]))

			var serviceConn *net.UDPConn
			var ok bool
			if serviceConn, ok = tc.serviceConn[connKey]; !ok {
				log.Println("Couldn't get service conn for:", connKey)
				continue
			}

			var clientAddr *net.UDPAddr
			if clientAddr, ok = tc.clientAddr[connId]; !ok {
				log.Println("Couldn't get client address for:", connId)
				continue
			}

			_, err = serviceConn.WriteToUDP(buffer[4:n], clientAddr)
			if err != nil {
				log.Println("Couldn't send data to client:", err)
			}
		}
	}()

	for i, port := range ports {
		localConn, err := net.ListenUDP(string(tuna.UDP), &net.UDPAddr{Port: port})
		if err != nil {
			log.Panicln("Couldn't bind listener:", err)
		}
		portId := byte(i)
		connKey := connKey{serviceId, portId}
		tc.serviceConn[connKey] = localConn

		go func() {
			localBuffer := make([]byte, 2048)
			for {
				n, addr, err := localConn.ReadFromUDP(localBuffer)
				if err != nil {
					log.Println("Couldn't receive data from local:", err)
					continue
				}

				connKey := uint16(addr.Port)
				if _, ok := tc.clientAddr[connKey]; !ok {
					tc.clientAddr[connKey] = addr
				}

				serverConn, err := tc.getServerUDPConn(serviceId, false)
				if err != nil {
					log.Println("Couldn't get remote connection:", err)
					continue
				}
				connId := *(*[2]byte)(unsafe.Pointer(&addr.Port))
				_, err = serverConn.Write(append([]byte{connId[0], connId[1], serviceId, portId}, localBuffer[:n]...))
				if err != nil {
					log.Println("Couldn't send data to server:", err)
				}
			}
		}()
	}
}

func main() {
	NewTunaClient().Start()

	select{}
}
