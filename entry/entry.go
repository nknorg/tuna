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
	DialTimeout uint16   `json:"DialTimeout"`
	UDPTimeout  uint16   `json:"UDPTimeout"`
	PrivateKey  string   `json:"PrivateKey"`
	Services    []string `json:"Services"`

	Reverse              bool   `json:"Reverse"`
	ReverseTCP           int    `json:"ReverseTCP"`
	ReverseUDP           int    `json:"ReverseUDP"`
	SubscriptionPrefix   string `json:"SubscriptionPrefix"`
	SubscriptionDuration uint32 `json:"SubscriptionDuration"`
	SubscriptionInterval uint32 `json:"SubscriptionInterval"`
}

type TunaEntry struct {
	*tuna.Common
	config       Configuration
	tcpListeners map[int]*net.TCPListener
	serviceConn  map[int]*net.UDPConn
	clientAddr   *cache.Cache
	session      *smux.Session
	closeChan    chan struct{}
}

func NewTunaEntry(serviceName string, reverse bool, config Configuration, wallet *WalletSDK) *TunaEntry {
	te := &TunaEntry{
		Common: &tuna.Common{
			ServiceName:        serviceName,
			Wallet:             wallet,
			DialTimeout:        config.DialTimeout,
			SubscriptionPrefix: config.SubscriptionPrefix,
			Reverse:            reverse,
		},
		config:       config,
		tcpListeners: make(map[int]*net.TCPListener),
		serviceConn:  make(map[int]*net.UDPConn),
		clientAddr:   cache.New(time.Duration(config.UDPTimeout)*time.Second, time.Second),
		closeChan:    make(chan struct{}),
	}
	te.SetServerUDPReadChan(make(chan []byte))
	te.SetServerUDPWriteChan(make(chan []byte))
	return te
}

func (te *TunaEntry) Start() {
	for {
		err := te.CreateServerConn(true)
		if err != nil {
			log.Println("Couldn't connect to node:", err)
			time.Sleep(1 * time.Second)
			continue
		}

		if !te.listenTCP(te.Metadata.ServiceTCP) {
			te.close()
			return
		}
		if !te.listenUDP(len(te.Metadata.ServiceTCP), te.Metadata.ServiceUDP) {
			te.close()
			return
		}
		break
	}

	<-te.closeChan
}

func (te *TunaEntry) close() {
	for _, listener := range te.tcpListeners {
		tuna.Close(listener)
	}
	for _, conn := range te.serviceConn {
		tuna.Close(conn)
	}
	te.closeChan <- struct{}{}
}

func (te *TunaEntry) getSession(force bool) (*smux.Session, error) {
	if te.Reverse && force {
		te.close()
		return nil, errors.New("reverse connection to service is dead")
	}
	if te.session == nil || te.session.IsClosed() || force {
		conn, err := te.GetServerTCPConn(force)
		if err != nil {
			return nil, err
		}
		te.session, _ = smux.Client(conn, nil)
	}

	return te.session, nil
}

func (te *TunaEntry) openStream(port int, force bool) (*smux.Stream, error) {
	session, err := te.getSession(force)
	if err != nil {
		return nil, err
	}
	serviceId := te.Metadata.ServiceId
	portId := te.TCPPortIds[port]
	stream, err := session.OpenStream(serviceId, portId)
	if err != nil {
		return te.openStream(port, true)
	}
	return stream, err
}

func (te *TunaEntry) listenTCP(ports []int) bool {
	for _, _port := range ports {
		port := _port
		listener, err := net.ListenTCP(string(tuna.TCP), &net.TCPAddr{Port: port})
		if err != nil {
			log.Println("Couldn't bind listener:", err)
			return false
		}

		te.tcpListeners[port] = listener

		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					log.Println("Couldn't accept connection:", err)
					tuna.Close(conn)
					if strings.Contains(err.Error(), "use of closed network connection") {
						te.close()
						return
					}
					continue
				}

				stream, err := te.openStream(port, false)
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

	return true
}

func (te *TunaEntry) listenUDP(portIdOffset int, ports []int) bool {
	if len(ports) == 0 {
		return true
	}

	go func() {
		for {
			serverReadChan, err := te.GetServerUDPReadChan(false)
			if err != nil {
				log.Println("Couldn't get server connection:", err)
				continue
			}

			data := <-serverReadChan

			portId := data[3]
			port := te.UDPPorts[portId]
			connId := tuna.GetConnIdString(data)

			var serviceConn *net.UDPConn
			var ok bool
			if serviceConn, ok = te.serviceConn[port]; !ok {
				log.Println("Couldn't get service conn for port:", port)
				continue
			}

			var x interface{}
			if x, ok = te.clientAddr.Get(connId); !ok {
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
			log.Println("Couldn't bind listener:", err)
			return false
		}

		te.serviceConn[port] = localConn

		go func() {
			localBuffer := make([]byte, 2048)
			for {
				n, addr, err := localConn.ReadFromUDP(localBuffer)
				if err != nil {
					log.Println("Couldn't receive data from local:", err)
					continue
				}

				connKey := strconv.Itoa(addr.Port)
				te.clientAddr.Set(connKey, addr, cache.DefaultExpiration)

				serverWriteChan, err := te.GetServerUDPWriteChan(false)
				if err != nil {
					log.Println("Couldn't get remote connection:", err)
					continue
				}
				connId := GetConnIdData(addr.Port)
				serviceId := te.Metadata.ServiceId
				portId := te.UDPPortIds[port]
				serverWriteChan <- append([]byte{connId[0], connId[1], serviceId, portId}, localBuffer[:n]...)
			}
		}()
	}

	return true
}

func GetConnIdData(port int) [2]byte {
	return *(*[2]byte)(unsafe.Pointer(&port))
}

func main() {
	Init()

	config := Configuration{SubscriptionPrefix: tuna.DefaultSubscriptionPrefix}
	tuna.ReadJson("config.json", &config)

	privateKey, _ := hex.DecodeString(config.PrivateKey)
	account, err := vault.NewAccountWithPrivatekey(privateKey)
	if err != nil {
		log.Panicln("Couldn't load account:", err)
	}

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
						udpCloseChan <- struct{}{}
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

				te := NewTunaEntry("", true, config, wallet)
				te.SetMetadata(string(metadataRaw))

				te.SetServerTCPConn(tcpConn)

				if te.Metadata.UDPPort > 0 {
					ip, _, _ := net.SplitHostPort(tcpConn.RemoteAddr().String())
					udpAddr := net.UDPAddr{IP: net.ParseIP(ip), Port: te.Metadata.UDPPort}

					udpReadChan := make(chan []byte)
					udpWriteChan := make(chan []byte)

					go func() {
						for {
							select {
							case data := <-udpWriteChan:
								_, err := udpConn.WriteToUDP(data, &udpAddr)
								if err != nil {
									log.Println("Couldn't send data to server:", err)
								}
							case <-udpCloseChan:
								return
							}
						}
					}()

					udpReadChans[udpAddr.String()] = udpReadChan

					te.SetServerUDPReadChan(udpReadChan)
					te.SetServerUDPWriteChan(udpWriteChan)
				}
				go func() {
					te.Start()
					tuna.Close(tcpConn)
					te = nil
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
			config.SubscriptionPrefix,
			config.SubscriptionDuration,
			config.SubscriptionInterval,
			wallet,
		)
	} else {
		for _, serviceName := range config.Services {
			go NewTunaEntry(serviceName, false, config, wallet).Start()
		}
	}

	select {}
}
