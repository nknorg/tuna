package main

import (
	"encoding/hex"
	"log"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"time"

	. "github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/nkn/vault"
	"github.com/trueinsider/smux"
	"github.com/trueinsider/tuna"
)

type Configuration struct {
	DialTimeout uint16   `json:"DialTimeout"`
	PrivateKey  string   `json:"PrivateKey"`
	Services    []string `json:"Services"`
}

type TunnelClient struct {
	config  Configuration
	tcpConn map[byte]net.Conn
	udpConn map[byte]net.Conn
	session map[byte]*smux.Session
	wallet  *WalletSDK
}

func NewTunnelClient() *TunnelClient {
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

	return &TunnelClient{
		config,
		make(map[byte]net.Conn),
		make(map[byte]net.Conn),
		make(map[byte]*smux.Session),
		wallet,
	}
}

func (tc *TunnelClient) Start() {
	for _, serviceName := range tc.config.Services {
		serviceId, err := tuna.GetServiceId(serviceName)
		if err != nil {
			log.Panicln(err)
		}
		service := tuna.Services[serviceId]
		tc.listenService(serviceId, 0, tuna.TCP, service.TCP)
		tc.listenService(serviceId, len(service.TCP), tuna.UDP, service.UDP)
	}
}

func (tc *TunnelClient) connectToNode(serviceId byte, protocol tuna.Protocol, force bool) (net.Conn, error) {
	var conn map[byte]net.Conn
	if protocol == tuna.TCP {
		conn = tc.tcpConn
	} else if protocol == tuna.UDP {
		conn = tc.udpConn
	}
	if conn[serviceId] == nil || force {
		serviceName := tuna.Services[serviceId].Name

	RandomBucket:
		for {
			lastBucket, err := tc.wallet.GetTopicBucketsCount(serviceName)
			if err != nil {
				return nil, err
			}
			bucket := uint32(rand.Intn(int(lastBucket) + 1))
			subscribers, err := tc.wallet.GetSubscribers(serviceName, bucket)
			if err != nil {
				return nil, err
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
					var address string
					if protocol == tuna.TCP {
						address = addressParts[0] + ":" + addressParts[1]
					} else if protocol == tuna.UDP {
						address = addressParts[0] + ":" + addressParts[2]
					}

					if conn[serviceId] != nil {
						tuna.Close(conn[serviceId])
					}
					conn[serviceId], err = net.DialTimeout(
						string(protocol),
						address,
						time.Duration(tc.config.DialTimeout)*time.Second,
					)
					if err != nil {
						log.Println("Couldn't connect to address", address, "from", subscriber, "because:", err)
						continue RandomSubscriber
					}

					log.Println("Connected to proxy provider at", address, "from", subscriber)
					break RandomBucket
				}
			}
		}
	}

	return conn[serviceId], nil
}

func (tc *TunnelClient) getSession(serviceId byte, protocol tuna.Protocol, forceSession bool, forceConn bool) (*smux.Session, error) {
	if tc.session[serviceId] == nil || tc.session[serviceId].IsClosed() || forceSession {
		conn, err := tc.connectToNode(serviceId, protocol, forceConn)
		if err != nil {
			return nil, err
		}
		tc.session[serviceId], err = smux.Client(conn, nil)
		if err != nil {
			return tc.getSession(serviceId, protocol, true, true)
		}
		return tc.session[serviceId], nil
	}

	return tc.session[serviceId], nil
}

func (tc *TunnelClient) openStream(serviceId byte, portId byte, protocol tuna.Protocol, force bool) (*smux.Stream, error) {
	session, err := tc.getSession(serviceId, protocol, force, false)
	if err != nil {
		return nil, err
	}
	stream, err := session.OpenStream(serviceId, portId)
	if err != nil {
		return tc.openStream(serviceId, portId, protocol, true)
	}
	return stream, err
}

func (tc *TunnelClient) listenService(serviceId byte, portIdOffset int, protocol tuna.Protocol, ports []int) {
	for i, port := range ports {
		listener, err := net.Listen(string(protocol), ":"+strconv.Itoa(port))
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

				stream, err := tc.openStream(serviceId, byte(portIdOffset+i), protocol, false)
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

func main() {
	NewTunnelClient().Start()

	select{}
}
