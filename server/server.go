package main

import (
	"encoding/hex"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

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
	ListenerTCP string
	ListenerUDP string
	timeout     time.Duration
}

func NewTunnelServer() *TunnelServer {
	tuna.Init()
	Init()

	config := Configuration{}
	tuna.ReadJson("config.json", &config)

	return &TunnelServer{
		config,
		":" + strconv.Itoa(config.ListenTCP),
		":" + strconv.Itoa(config.ListenUDP),
		time.Duration(config.DialTimeout) * time.Second,
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

		conn, err := net.DialTimeout(string(protocol), host, ts.timeout)
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

func (ts *TunnelServer) listenService(protocol tuna.Protocol, port string) {
	listener, err := net.Listen(string(protocol), port)
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

			session, err := smux.Server(conn, nil)
			if err != nil {
				log.Println("Couldn't create smux session:", err)
				tuna.Close(conn)
				continue
			}

			go ts.handleSession(conn, session)
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

	ts.listenService(tuna.TCP, ts.ListenerTCP)
	ts.listenService(tuna.UDP, ts.ListenerUDP)

	for _, serviceName := range ts.config.Services {
		// retry subscription once a minute (regardless of result)
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
					if err == AlreadySubscribed {
						waitTime = time.Duration(ts.config.SubscriptionInterval) * time.Second
						log.Println(err)
					} else {
						log.Panicln("Couldn't subscribe:", err)
					}
				} else {
					waitTime = time.Duration(ts.config.SubscriptionDuration) * 20 * time.Second
					log.Println("Subscribed to topic successfully:", txid)
				}

				time.Sleep(waitTime)
			}
		}()
	}
}

func main() {
	NewTunnelServer().Start()
}
