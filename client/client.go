package main

import (
	"encoding/hex"
	"errors"
	"log"
	"net"
	"strconv"
	"strings"
	"time"
	"unsafe"

	. "github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/nkn/vault"
	"github.com/nknorg/tuna"
	"github.com/patrickmn/go-cache"
	"github.com/rdegges/go-ipify"
	"github.com/trueinsider/smux"
)

type Configuration struct {
	DialTimeout          uint16   `json:"DialTimeout"`
	UDPTimeout           uint16   `json:"UDPTimeout"`
	PrivateKey           string   `json:"PrivateKey"`
	Services             []string `json:"Services"`

	Reverse              bool     `json:"Reverse"`
	ReverseTCP           int      `json:"ReverseTCP"`
	ReverseUDP           int      `json:"ReverseUDP"`
	SubscriptionDuration uint32   `json:"SubscriptionDuration"`
	SubscriptionInterval uint32   `json:"SubscriptionInterval"`
}

type TunaClient struct {
	*tuna.Common
	config       Configuration
	tcpListeners map[int]*net.TCPListener
	serviceConn  map[int]*net.UDPConn
	clientAddr   *cache.Cache
	session      *smux.Session
	closeChan    chan struct{}
}

func NewTunaClient(serviceName string, reverse bool, config Configuration, wallet *WalletSDK) *TunaClient {
	tc := &TunaClient{
		Common: &tuna.Common{
			ServiceName: serviceName,
			Wallet: wallet,
			Reverse: reverse,
			DialTimeout: config.DialTimeout,
		},
		config:       config,
		tcpListeners: make(map[int]*net.TCPListener),
		serviceConn:  make(map[int]*net.UDPConn),
		clientAddr:   cache.New(time.Duration(config.UDPTimeout)*time.Second, time.Second),
		closeChan:    make(chan struct{}),
	}
	tc.SetServerUDPReadChan(make(chan []byte))
	tc.SetServerUDPWriteChan(make(chan []byte))
	return tc
}

func (tc *TunaClient) Start() {
	for {
		err := tc.CreateServerConn(true)
		if err != nil {
			log.Println("Couldn't connect to node:", err)
			time.Sleep(1 * time.Second)
			continue
		}

		tc.listenTCP(tc.Metadata.ServiceTCP)
		tc.listenUDP(len(tc.Metadata.ServiceTCP), tc.Metadata.ServiceUDP)
		break
	}

	<- tc.closeChan
}

func (tc *TunaClient) close() {
	for _, listener := range tc.tcpListeners {
		tuna.Close(listener)
	}
	for _, conn := range tc.serviceConn {
		tuna.Close(conn)
	}
	tc.closeChan <- struct{}{}
}

func (tc *TunaClient) getSession(force bool) (*smux.Session, error) {
	if tc.Reverse && force {
		tc.close()
		return nil, errors.New("reverse connection to service is dead")
	}
	if tc.session == nil || tc.session.IsClosed() || force {
		conn, err := tc.GetServerTCPConn(force)
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
	serviceId := tc.Metadata.ServiceId
	portId := tc.TCPPortIds[port]
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

		tc.tcpListeners[port] = listener

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
		for {
			serverReadChan, err := tc.GetServerUDPReadChan(false)
			if err != nil {
				log.Println("Couldn't get server connection:", err)
				continue
			}

			data := <- serverReadChan

			portId := data[3]
			port := tc.UDPPorts[portId]
			connId := tuna.GetConnIdString(data)

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

			_, err = serviceConn.WriteToUDP(data, clientAddr)
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

				serverWriteChan, err := tc.GetServerUDPWriteChan(false)
				if err != nil {
					log.Println("Couldn't get remote connection:", err)
					continue
				}
				connId := GetConnIdData(addr.Port)
				serviceId := tc.Metadata.ServiceId
				portId := tc.UDPPortIds[port]
				serverWriteChan <- append([]byte{connId[0], connId[1], serviceId, portId}, localBuffer[:n]...)
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

	privateKey, _ := hex.DecodeString(config.PrivateKey)
	account, err := vault.NewAccountWithPrivatekey(privateKey)
	if err != nil {
		log.Panicln("Couldn't load account:", err)
	}

	Init()
	wallet := NewWalletSDK(account)

	if config.Reverse {
		ip, err := ipify.GetIp()
		if err != nil {
			log.Panicln("Couldn't get IP:", err)
		}

		listener, err := net.ListenTCP(string(tuna.TCP), &net.TCPAddr{Port: config.ReverseTCP})
		if err != nil {
			log.Panicln("Couldn't bind listener:", err)
		}

		udpConn, err := net.ListenUDP(string(tuna.UDP), &net.UDPAddr{Port: config.ReverseUDP})
		if err != nil {
			log.Panicln("Couldn't bind listener:", err)
		}

		udpReadChans := make(map[string]chan []byte)
		udpCloseChan := make(chan struct{})

		go func() {
			for {
				buffer := make([]byte, 2048)
				n, addr, err := udpConn.ReadFromUDP(buffer)
				if err != nil {
					log.Println("Couldn't receive data from server:", err)
					if strings.Contains(err.Error(), "use of closed network connection") {
						udpCloseChan <- struct {}{}
						return
					}
					continue
				}

				data := make([]byte, n)
				copy(data, buffer)

				if udpReadChan, ok := udpReadChans[addr.String()]; ok {
					udpReadChan <- data
				}
			}
		}()

		go func() {
			for {
				tcpConn, err := listener.Accept()
				if err != nil {
					log.Println("Couldn't accept client connection:", err)
					tuna.Close(tcpConn)
					continue
				}

				buf := make([]byte, 2048)
				n, err := tcpConn.Read(buf)
				if err != nil {
					log.Println("Couldn't read service metadata:", err)
					tuna.Close(tcpConn)
					break
				}
				metadataRaw := make([]byte, n)
				copy(metadataRaw, buf)

				_, _, err = udpConn.ReadFromUDP(buf)
				if err != nil {
					log.Println("Couldn't receive data from server:", err)
					continue
				}

				tc := NewTunaClient("", true, config, wallet)
				tc.SetMetadata(string(metadataRaw))

				ip, _, _ := net.SplitHostPort(tcpConn.RemoteAddr().String())
				udpAddr := net.UDPAddr{IP: net.ParseIP(ip), Port: tc.Metadata.UDPPort}

				udpReadChan := make(chan []byte)
				udpWriteChan := make(chan []byte)

				go func() {
					for {
						select {
						case data := <- udpWriteChan:
							_, err := udpConn.WriteToUDP(data, &udpAddr)
							if err != nil {
								log.Println("Couldn't send data to server:", err)
							}
						case <- udpCloseChan:
							return
						}
					}
				}()

				udpReadChans[udpAddr.String()] = udpReadChan

				tc.SetServerTCPConn(tcpConn)
				tc.SetServerUDPReadChan(udpReadChan)
				tc.SetServerUDPWriteChan(udpWriteChan)
				go func() {
					tc.Start()
					tc = nil
				}()
			}
		}()

		tuna.UpdateMetadata(
			"reverse",
			255,
			[]int{},
			[]int{},
			ip,
			config.ReverseTCP,
			config.ReverseUDP,
			config.SubscriptionDuration,
			config.SubscriptionInterval,
			wallet,
		)
	}

	for _, serviceName := range config.Services {
		go NewTunaClient(serviceName, false, config, wallet).Start()
	}

	select {}
}
