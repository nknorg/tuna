package main

import (
	"encoding/hex"
	"log"
	"net"
	"strconv"
	"strings"
	"time"
	"unsafe"

	. "github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/nkn/vault"
	"github.com/rdegges/go-ipify"
	"github.com/trueinsider/smux"
	"github.com/trueinsider/tuna"
)

type Configuration struct {
	ListenTCP            int      `json:"ListenTCP"`
	ListenUDP            int      `json:"ListenUDP"`
	DialTimeout          uint32   `json:"DialTimeout"`
	PrivateKey           string   `json:"PrivateKey"`
	SubscriptionDuration uint32   `json:"SubscriptionDuration"`
	SubscriptionInterval uint32   `json:"SubscriptionInterval"`
	Services             []string `json:"Services"`
}

type TunnelServer struct {
	config      Configuration
	serviceConn map[connKey]*net.UDPConn
	clientConn  *net.UDPConn
}

type connKey struct {
	clientIp   string
	clientPort int
	connId     uint16
}

func NewTunnelServer() *TunnelServer {
	tuna.Init()
	Init()

	config := Configuration{}
	tuna.ReadJson("config.json", &config)

	return &TunnelServer{
		config: config,
		serviceConn: make(map[connKey]*net.UDPConn),
	}
}

func (ts *TunnelServer) handleSession(conn net.Conn, session *smux.Session) {
	for {
		stream, err := session.AcceptStream()
		if err != nil {
			log.Println("Couldn't accept stream:", err)
			break
		}

		metadata := stream.Metadata()
		serviceId := metadata[0]
		portId := int(metadata[1])

		service := tuna.Services[serviceId]
		tcpPortsCount := len(service.TCP)
		var protocol tuna.Protocol
		var port int
		if portId < tcpPortsCount {
			protocol = tuna.TCP
			port = service.TCP[portId]
		} else {
			protocol = tuna.UDP
			port = service.UDP[portId - tcpPortsCount]
		}
		host := "127.0.0.1:" + strconv.Itoa(port)

		conn, err := net.DialTimeout(string(protocol), host, time.Duration(ts.config.DialTimeout) * time.Second)
		if err != nil {
			log.Println("Couldn't connect to host", host, "with error:", err)
			tuna.Close(stream)
			continue
		}

		go tuna.Pipe(conn, stream)
		go tuna.Pipe(stream, conn)
	}

	tuna.Close(session)
	tuna.Close(conn)
}

func (ts *TunnelServer) listenTCP(port int) {
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{Port: port})
	if err != nil {
		log.Println("Couldn't bind listener:", err)
	}

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				log.Println("Couldn't accept client connection:", err)
				tuna.Close(conn)
				continue
			}

			session, _ := smux.Server(conn, nil)

			go ts.handleSession(conn, session)
		}
	}()
}

func (ts *TunnelServer) getServiceConn(addr *net.UDPAddr, connId []byte, serviceId byte, portId byte) (*net.UDPConn, error) {
	connKey := connKey{
		addr.IP.String(),
		addr.Port,
		*(*uint16)(unsafe.Pointer(&connId[0])),
	}
	var conn *net.UDPConn
	var ok bool
	if conn, ok = ts.serviceConn[connKey]; !ok {
		service := tuna.Services[serviceId]
		port := service.UDP[portId]
		var err error
		conn, err = net.DialUDP("udp", nil, &net.UDPAddr{Port: port})
		if err != nil {
			log.Println("Couldn't connect to local UDP port", port, "with error:", err)
			tuna.Close(conn)
			return conn, err
		}

		ts.serviceConn[connKey] = conn

		prefix := []byte{connId[0], connId[1], serviceId, portId}
		go func() {
			serviceBuffer := make([]byte, 2048)
			for {
				n, err := conn.Read(serviceBuffer)
				if err != nil {
					log.Println("Couldn't receive data from service:", err)
					tuna.Close(conn)
					break
				}
				_, err = ts.clientConn.WriteToUDP(append(prefix, serviceBuffer[:n]...), addr)
				if err != nil {
					log.Println("Couldn't send data to client:", err)
					tuna.Close(conn)
					break
				}
			}
		}()
	}

	return conn, nil
}

func (ts *TunnelServer) listenUDP(port int) {
	var err error
	ts.clientConn, err = net.ListenUDP("udp", &net.UDPAddr{Port: port})
	if err != nil {
		log.Println("Couldn't bind listener:", err)
	}

	go func() {
		clientBuffer := make([]byte, 2048)
		for {
			n, addr, err := ts.clientConn.ReadFromUDP(clientBuffer)
			if err != nil {
				log.Println("Couldn't receive data from client:", err)
				continue
			}
			serviceConn, err := ts.getServiceConn(addr, clientBuffer[0:2], clientBuffer[2], clientBuffer[3])
			if err != nil {
				continue
			}
			_, err = serviceConn.Write(clientBuffer[4:n])
			if err != nil {
				log.Println("Couldn't send data to service:", err)
			}
		}
	}()
}

func (ts *TunnelServer) Start() {
	ip, err := ipify.GetIp()
	if err != nil {
		log.Panicln("Couldn't get IP:", err)
	}

	privateKey, _ := hex.DecodeString(ts.config.PrivateKey)
	account, err := vault.NewAccountWithPrivatekey(privateKey)
	if err != nil {
		log.Panicln("Couldn't load account:", err)
	}

	w := NewWalletSDK(account)

	ts.listenTCP(ts.config.ListenTCP)
	ts.listenUDP(ts.config.ListenUDP)

	for _, _serviceName := range ts.config.Services {
		serviceName := _serviceName
		go func() {
			var waitTime time.Duration
			for {
				txid, err := w.SubscribeToFirstAvailableBucket(
					serviceName,
					serviceName,
					ts.config.SubscriptionDuration,
					strings.Join([]string{
						ip,
						strconv.Itoa(ts.config.ListenTCP),
						strconv.Itoa(ts.config.ListenUDP),
					}, ","),
				)
				if err != nil {
					waitTime = time.Duration(ts.config.SubscriptionInterval) * time.Second
					if err == AlreadySubscribed {
						log.Println("Already subscribed to topic", serviceName)
					} else {
						log.Println("Couldn't subscribe to topic", serviceName, "because:", err)
					}
				} else {
					waitTime = time.Duration(ts.config.SubscriptionDuration) * 20 * time.Second
					log.Println("Subscribed to topic", serviceName, "successfully:", txid)
				}

				time.Sleep(waitTime)
			}
		}()
	}
}

func main() {
	NewTunnelServer().Start()

	select{}
}
