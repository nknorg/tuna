package main

import (
	"encoding/hex"
	"log"
	"net"
	"strconv"
	"time"

	. "github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/nkn/vault"
	"github.com/nknorg/tuna"
	"github.com/patrickmn/go-cache"
	"github.com/pkg/errors"
	"github.com/rdegges/go-ipify"
	"github.com/trueinsider/smux"
)

type Configuration struct {
	ListenTCP            int      `json:"ListenTCP"`
	ListenUDP            int      `json:"ListenUDP"`
	Reverse              bool     `json:"Reverse"`
	DialTimeout          uint16   `json:"DialTimeout"`
	UDPTimeout           uint16   `json:"UDPTimeout"`
	PrivateKey           string   `json:"PrivateKey"`
	SubscriptionDuration uint32   `json:"SubscriptionDuration"`
	SubscriptionInterval uint32   `json:"SubscriptionInterval"`
	Services             []string `json:"Services"`
}

type Service struct {
	Name string `json:"name"`
	TCP  []int  `json:"tcp"`
	UDP  []int  `json:"udp"`
}

type TunaServer struct {
	config      Configuration
	services    []Service
	serviceConn *cache.Cache
	common      *tuna.Common
	clientConn  *net.UDPConn
}

func NewTunaServer() *TunaServer {
	Init()

	config := Configuration{}
	tuna.ReadJson("config.json", &config)

	var services []Service
	tuna.ReadJson("services.json", &services)

	return &TunaServer{
		config:      config,
		services:    services,
		serviceConn: cache.New(time.Duration(config.UDPTimeout)*time.Second, time.Second),
	}
}

func (ts *TunaServer) getServiceId(serviceName string) (byte, error) {
	for i, service := range ts.services {
		if service.Name == serviceName {
			return byte(i), nil
		}
	}

	return 0, errors.New("Service " + serviceName + " not found")
}

func (ts *TunaServer) handleSession(conn net.Conn) {
	session, _ := smux.Server(conn, nil)

	for {
		stream, err := session.AcceptStream()
		if err != nil {
			log.Println("Couldn't accept stream:", err)
			break
		}

		metadata := stream.Metadata()
		serviceId := metadata[0]
		portId := int(metadata[1])

		service, err := ts.getService(serviceId)
		if err != nil {
			log.Println(err)
			tuna.Close(stream)
			continue
		}
		tcpPortsCount := len(service.TCP)
		udpPortsCount := len(service.UDP)
		var protocol tuna.Protocol
		var port int
		if portId < tcpPortsCount {
			protocol = tuna.TCP
			port = service.TCP[portId]
		} else if portId-tcpPortsCount < udpPortsCount {
			protocol = tuna.UDP
			portId -= tcpPortsCount
			port = service.UDP[portId]
		} else {
			log.Println("Wrong portId received:", portId)
			tuna.Close(stream)
			continue
		}

		host := "127.0.0.1:" + strconv.Itoa(port)

		conn, err := net.DialTimeout(string(protocol), host, time.Duration(ts.config.DialTimeout)*time.Second)
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

func (ts *TunaServer) listenTCP(port int) {
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

			go ts.handleSession(conn)
		}
	}()
}

func (ts *TunaServer) getService(serviceId byte) (*Service, error) {
	if int(serviceId) >= len(ts.services) {
		return nil, errors.New("Wrong serviceId: " + strconv.Itoa(int(serviceId)))
	}
	return &ts.services[serviceId], nil
}

func (ts *TunaServer) getServiceConn(addr *net.UDPAddr, connId []byte, serviceId byte, portId byte) (*net.UDPConn, error) {
	connKey := addr.String() + ":" + tuna.GetConnIdString(connId)
	var conn *net.UDPConn
	var x interface{}
	var ok bool
	if x, ok = ts.serviceConn.Get(connKey); !ok {
		service, err := ts.getService(serviceId)
		if err != nil {
			log.Println(err)
			return nil, err
		}
		port := service.UDP[portId]
		conn, err = net.DialUDP("udp", nil, &net.UDPAddr{Port: port})
		if err != nil {
			log.Println("Couldn't connect to local UDP port", port, "with error:", err)
			tuna.Close(conn)
			return conn, err
		}

		ts.serviceConn.Set(connKey, conn, cache.DefaultExpiration)

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
	} else {
		conn = x.(*net.UDPConn)
	}

	return conn, nil
}

func (ts *TunaServer) listenUDP(port int) {
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

func (ts *TunaServer) updateAllMetadata(ip string, tcpPort int, udpPort int, wallet *WalletSDK) {
	for _, _serviceName := range ts.config.Services {
		serviceName := _serviceName
		serviceId, err := ts.getServiceId(serviceName)
		if err != nil {
			log.Panicln(err)
		}
		service, err := ts.getService(serviceId)
		if err != nil {
			log.Panicln(err)
		}
		tuna.UpdateMetadata(
			serviceName,
			serviceId,
			service.TCP,
			service.UDP,
			ip,
			tcpPort,
			udpPort,
			ts.config.SubscriptionDuration,
			ts.config.SubscriptionInterval,
			wallet,
		)
	}
}

func (ts *TunaServer) Start() {
	privateKey, _ := hex.DecodeString(ts.config.PrivateKey)
	account, err := vault.NewAccountWithPrivatekey(privateKey)
	if err != nil {
		log.Panicln("Couldn't load account:", err)
	}

	wallet := NewWalletSDK(account)

	if ts.config.Reverse {
		ts.startReverse(wallet)
		return
	}

	ip, err := ipify.GetIp()
	if err != nil {
		log.Panicln("Couldn't get IP:", err)
	}

	ts.listenTCP(ts.config.ListenTCP)
	ts.listenUDP(ts.config.ListenUDP)

	ts.updateAllMetadata(ip, ts.config.ListenTCP, ts.config.ListenUDP, wallet)
}

func (ts *TunaServer) startReverse(wallet *WalletSDK) {
	ts.common = &tuna.Common{
		ServiceName: "reverse",
		Wallet: wallet,
		DialTimeout: ts.config.DialTimeout,
	}

	for _, _serviceName := range ts.config.Services {
		serviceName := _serviceName
		serviceId, err := ts.getServiceId(serviceName)
		if err != nil {
			log.Panicln(err)
		}
		service, err := ts.getService(serviceId)
		if err != nil {
			log.Panicln(err)
		}

		go func() {
			var tcpConn net.Conn
			for {
				err := ts.common.CreateServerConn(true)
				if err != nil {
					log.Println("Couldn't connect to reverse entry:", err)
					time.Sleep(1 * time.Second)
					continue
				}

				udpConn, _ := ts.common.GetServerUDPConn(false)
				_, udpPortString, _ := net.SplitHostPort(udpConn.LocalAddr().String())
				udpPort, _ := strconv.Atoi(udpPortString)
				serviceMetadata := tuna.CreateRawMetadata(
					serviceId,
					service.TCP,
					service.UDP,
					"",
					-1,
					udpPort,
				)

				tcpConn, _ = ts.common.GetServerTCPConn(false)
				_, err = tcpConn.Write(serviceMetadata)
				if err != nil {
					log.Println("Couldn't send metadata to reverse entry:", err)
					time.Sleep(1 * time.Second)
					continue
				}

				break
			}

			go ts.handleSession(tcpConn)
		}()
	}
}

func main() {
	NewTunaServer().Start()

	select {}
}
