package tuna

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	. "github.com/nknorg/nkn-sdk-go"
	nknsdk "github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/nkn/common"
	"github.com/nknorg/nkn/transaction"
	cache "github.com/patrickmn/go-cache"
	ipify "github.com/rdegges/go-ipify"
	"github.com/trueinsider/smux"
)

type ExitServiceInfo struct {
	Address string `json:"address"`
	Price   string `json:"price"`
}

type ExitConfiguration struct {
	BeneficiaryAddr      string                     `json:"BeneficiaryAddr"`
	ListenTCP            int                        `json:"ListenTCP"`
	ListenUDP            int                        `json:"ListenUDP"`
	Reverse              bool                       `json:"Reverse"`
	ReverseRandomPorts   bool                       `json:"ReverseRandomPorts"`
	ReverseMaxPrice      string                     `json:"ReverseMaxPrice"`
	DialTimeout          uint16                     `json:"DialTimeout"`
	UDPTimeout           uint16                     `json:"UDPTimeout"`
	SubscriptionPrefix   string                     `json:"SubscriptionPrefix"`
	SubscriptionDuration uint32                     `json:"SubscriptionDuration"`
	SubscriptionFee      string                     `json:"SubscriptionFee"`
	ClaimInterval        uint32                     `json:"ClaimInterval"`
	Services             map[string]ExitServiceInfo `json:"Services"`
}

type TunaExit struct {
	config           *ExitConfiguration
	wallet           *nknsdk.Wallet
	services         []Service
	serviceConn      *cache.Cache
	common           *Common
	tcpListener      net.Listener
	udpConn          *net.UDPConn
	reverseIp        net.IP
	reverseTcp       []int
	reverseUdp       []int
	onEntryConnected func()
	closeChan        chan struct{}
}

func NewTunaExit(config *ExitConfiguration, services []Service, wallet *nknsdk.Wallet) *TunaExit {
	return &TunaExit{
		config:      config,
		wallet:      wallet,
		services:    services,
		serviceConn: cache.New(time.Duration(config.UDPTimeout)*time.Second, time.Second),
		closeChan:   make(chan struct{}, 0),
	}
}

func (te *TunaExit) getServiceId(serviceName string) (byte, error) {
	for i, service := range te.services {
		if service.Name == serviceName {
			return byte(i), nil
		}
	}

	return 0, errors.New("Service " + serviceName + " not found")
}

func (te *TunaExit) handleSession(session *smux.Session, conn net.Conn) {
	bytesIn := make([]uint64, 256)
	bytesOut := make([]uint64, 256)

	claimInterval := time.Duration(te.config.ClaimInterval) * time.Second
	errChan := make(chan error)
	npc, err := te.wallet.NewNanoPayClaimer(claimInterval, errChan, te.config.BeneficiaryAddr)
	if err != nil {
		log.Fatalln(err)
	}
	lastComputed := common.Fixed64(0)
	lastClaimed := common.Fixed64(0)
	lastUpdate := time.Now()
	isClosed := false

	if !te.config.Reverse {
		go func() {
			for {
				err := <-errChan
				if err != nil {
					log.Println("Couldn't claim nano pay:", err)
					if npc.IsClosed() {
						Close(session)
						Close(conn)
						isClosed = true
						break
					}
				}
			}
		}()

		go func() {
			for {
				time.Sleep(claimInterval)

				if isClosed {
					break
				}

				if time.Since(lastUpdate) > claimInterval {
					log.Println("Didn't update nano pay for more than", claimInterval.String())
					Close(session)
					Close(conn)
					isClosed = true
					break
				}

				if common.Fixed64(float64(lastComputed)*0.9) > lastClaimed {
					log.Println("Nano pay amount covers less than 90% of total cost")
					Close(session)
					Close(conn)
					isClosed = true
					break
				}
			}
		}()
	}

	for {
		stream, err := session.AcceptStream()
		if err != nil {
			log.Println("Couldn't accept stream:", err)
			break
		}

		metadata := stream.Metadata()
		if len(metadata) == 0 { // payment stream
			if te.config.Reverse {
				continue
			}
			go func(stream *smux.Stream) {
				txData, err := ioutil.ReadAll(stream)
				if err != nil && err.Error() != io.EOF.Error() {
					log.Println("Couldn't read payment stream:", err)
					return
				}
				if len(txData) == 0 {
					return
				}
				tx := new(transaction.Transaction)
				if err := tx.Unmarshal(txData); err != nil {
					log.Println("Couldn't unmarshal payment stream data:", err)
					return
				}
				amount, err := npc.Claim(tx)
				if err != nil {
					log.Println("Couldn't accept nano pay update:", err)
					return
				}

				totalCost := common.Fixed64(0)
				for i := range bytesIn {
					in := atomic.LoadUint64(&bytesIn[i])
					out := atomic.LoadUint64(&bytesOut[i])
					if in == 0 && out == 0 {
						continue
					}
					service, err := te.getService(byte(i))
					if err != nil {
						continue
					}
					serviceInfo := te.config.Services[service.Name]
					entryToExitPrice, exitToEntryPrice, err := ParsePrice(serviceInfo.Price)
					if err != nil {
						continue
					}
					totalCost += entryToExitPrice*common.Fixed64(in)/TrafficUnit + exitToEntryPrice*common.Fixed64(out)/TrafficUnit
				}

				lastComputed = totalCost
				lastClaimed = amount
				lastUpdate = time.Now()
			}(stream)
			continue
		}

		serviceId := metadata[0]
		portId := int(metadata[1])

		service, err := te.getService(serviceId)
		if err != nil {
			log.Println(err)
			Close(stream)
			continue
		}
		tcpPortsCount := len(service.TCP)
		udpPortsCount := len(service.UDP)
		var protocol Protocol
		var port int
		if portId < tcpPortsCount {
			protocol = TCP
			port = service.TCP[portId]
		} else if portId-tcpPortsCount < udpPortsCount {
			protocol = UDP
			portId -= tcpPortsCount
			port = service.UDP[portId]
		} else {
			log.Println("Wrong portId received:", portId)
			Close(stream)
			continue
		}

		serviceInfo := te.config.Services[service.Name]
		host := serviceInfo.Address + ":" + strconv.Itoa(port)

		conn, err := net.DialTimeout(string(protocol), host, time.Duration(te.config.DialTimeout)*time.Second)
		if err != nil {
			log.Println("Couldn't connect to host", host, "with error:", err)
			Close(stream)
			continue
		}

		go Pipe(conn, stream, &bytesIn[serviceId])
		go Pipe(stream, conn, &bytesOut[serviceId])
	}

	Close(session)
	Close(conn)
	isClosed = true
}

func (te *TunaExit) listenTCP(port int) error {
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{Port: port})
	if err != nil {
		log.Println("Couldn't bind listener:", err)
		return err
	}
	te.tcpListener = listener

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				log.Println("Couldn't accept client connection:", err)
				Close(conn)
				continue
			}

			session, err := smux.Server(conn, nil)
			if err != nil {
				log.Println(err)
				Close(conn)
				continue
			}

			go te.handleSession(session, conn)
		}
	}()

	return nil
}

func (te *TunaExit) getService(serviceId byte) (*Service, error) {
	if int(serviceId) >= len(te.services) {
		return nil, errors.New("Wrong serviceId: " + strconv.Itoa(int(serviceId)))
	}
	return &te.services[serviceId], nil
}

func (te *TunaExit) getServiceConn(addr *net.UDPAddr, connId []byte, serviceId byte, portId byte) (*net.UDPConn, error) {
	connKey := addr.String() + ":" + GetConnIdString(connId)
	var conn *net.UDPConn
	var x interface{}
	var ok bool
	if x, ok = te.serviceConn.Get(connKey); !ok {
		service, err := te.getService(serviceId)
		if err != nil {
			log.Println(err)
			return nil, err
		}
		port := service.UDP[portId]
		conn, err = net.DialUDP("udp", nil, &net.UDPAddr{Port: port})
		if err != nil {
			log.Println("Couldn't connect to local UDP port", port, "with error:", err)
			Close(conn)
			return conn, err
		}

		te.serviceConn.Set(connKey, conn, cache.DefaultExpiration)

		prefix := []byte{connId[0], connId[1], serviceId, portId}
		go func() {
			serviceBuffer := make([]byte, 2048)
			for {
				n, err := conn.Read(serviceBuffer)
				if err != nil {
					log.Println("Couldn't receive data from service:", err)
					Close(conn)
					break
				}
				_, err = te.udpConn.WriteToUDP(append(prefix, serviceBuffer[:n]...), addr)
				if err != nil {
					log.Println("Couldn't send data to client:", err)
					Close(conn)
					break
				}
			}
		}()
	} else {
		conn = x.(*net.UDPConn)
	}

	return conn, nil
}

func (te *TunaExit) listenUDP(port int) error {
	var err error
	te.udpConn, err = net.ListenUDP("udp", &net.UDPAddr{Port: port})
	if err != nil {
		log.Println("Couldn't bind listener:", err)
		return err
	}
	te.readUDP()
	return nil
}

func (te *TunaExit) readUDP() {
	go func() {
		clientBuffer := make([]byte, 2048)
		for {
			n, addr, err := te.udpConn.ReadFromUDP(clientBuffer)
			if err != nil {
				log.Println("Couldn't receive data from client:", err)
				if strings.Contains(err.Error(), "use of closed network connection") {
					return
				}
				continue
			}
			serviceConn, err := te.getServiceConn(addr, clientBuffer[0:2], clientBuffer[2], clientBuffer[3])
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

func (te *TunaExit) updateAllMetadata(ip string, tcpPort int, udpPort int) error {
	for serviceName, serviceInfo := range te.config.Services {
		serviceId, err := te.getServiceId(serviceName)
		if err != nil {
			return err
		}
		UpdateMetadata(
			serviceName,
			serviceId,
			nil,
			nil,
			ip,
			tcpPort,
			udpPort,
			serviceInfo.Price,
			te.config.BeneficiaryAddr,
			te.config.SubscriptionPrefix,
			te.config.SubscriptionDuration,
			te.config.SubscriptionFee,
			te.wallet,
		)
	}
	return nil
}

func (te *TunaExit) Start() error {
	ip, err := ipify.GetIp()
	if err != nil {
		return fmt.Errorf("Couldn't get IP:", err)
	}

	err = te.listenTCP(te.config.ListenTCP)
	if err != nil {
		return err
	}

	err = te.listenUDP(te.config.ListenUDP)
	if err != nil {
		return err
	}

	return te.updateAllMetadata(ip, te.config.ListenTCP, te.config.ListenUDP)
}

func (te *TunaExit) StartReverse(serviceName string) error {
	serviceId, err := te.getServiceId(serviceName)
	if err != nil {
		return err
	}
	service, err := te.getService(serviceId)
	if err != nil {
		return err
	}

	reverseMetadata := &Metadata{}
	reverseMetadata.ServiceTCP = service.TCP
	reverseMetadata.ServiceUDP = service.UDP

	maxPrice, err := common.StringToFixed64(te.config.ReverseMaxPrice)
	if err != nil {
		return err
	}

	te.common = &Common{
		Service:             &Service{Name: DefaultReverseServiceName},
		EntryToExitMaxPrice: maxPrice,
		ExitToEntryMaxPrice: maxPrice,
		Wallet:              te.wallet,
		DialTimeout:         te.config.DialTimeout,
		ReverseMetadata:     reverseMetadata,
		SubscriptionPrefix:  te.config.SubscriptionPrefix,
	}

	go func() {
		var tcpConn net.Conn
		for {
			err := te.common.CreateServerConn(true)
			if err != nil {
				log.Println("Couldn't connect to reverse entry:", err)
				time.Sleep(1 * time.Second)
				continue
			}

			udpPort := -1
			var udpConn *net.UDPConn
			if len(service.UDP) > 0 {
				udpConn, err = te.common.GetServerUDPConn(false)
				if err != nil {
					log.Println(err)
					time.Sleep(1 * time.Second)
					continue
				}
				if udpConn != nil {
					_, udpPortString, err := net.SplitHostPort(udpConn.LocalAddr().String())
					if err != nil {
						log.Println(err)
						time.Sleep(1 * time.Second)
						continue
					}
					udpPort, err = strconv.Atoi(udpPortString)
					if err != nil {
						log.Println(err)
						time.Sleep(1 * time.Second)
						continue
					}
				}
			}

			var tcpPorts []int
			var udpPorts []int
			if te.config.ReverseRandomPorts {
				tcpPorts = make([]int, len(service.TCP))
				udpPorts = make([]int, len(service.UDP))
			} else {
				tcpPorts = service.TCP
				udpPorts = service.UDP
			}

			serviceMetadata := CreateRawMetadata(
				serviceId,
				tcpPorts,
				udpPorts,
				"",
				-1,
				udpPort,
				"",
				te.config.BeneficiaryAddr,
			)

			tcpConn, err = te.common.GetServerTCPConn(false)
			if err != nil {
				log.Println(err)
				time.Sleep(1 * time.Second)
				continue
			}

			session, err := smux.Server(tcpConn, nil)
			if err != nil {
				log.Println(err)
				time.Sleep(1 * time.Second)
				continue
			}

			stream, err := session.AcceptStream()
			if err != nil {
				log.Println("Couldn't open stream to reverse entry:", err)
				time.Sleep(1 * time.Second)
				continue
			}

			_, err = stream.Write(serviceMetadata)
			if err != nil {
				log.Println("Couldn't send metadata to reverse entry:", err)
				time.Sleep(1 * time.Second)
				continue
			}

			buf := make([]byte, 2048)
			n, err := stream.Read(buf)
			if err != nil {
				log.Println("Couldn't read reverse metadata:", err)
				time.Sleep(1 * time.Second)
				continue
			}

			reverseMetadataRaw := make([]byte, n)
			copy(reverseMetadataRaw, buf)
			reverseMetadata, err := ReadMetadata(string(reverseMetadataRaw))
			if err != nil {
				log.Println("Couldn't unmarshal metadata:", err)
				time.Sleep(1 * time.Second)
				continue
			}

			te.reverseIp = tcpConn.RemoteAddr().(*net.TCPAddr).IP
			te.reverseTcp = reverseMetadata.ServiceTCP
			te.reverseUdp = reverseMetadata.ServiceUDP
			if te.onEntryConnected != nil {
				te.onEntryConnected()
			}

			if udpConn != nil {
				te.udpConn = udpConn
				te.readUDP()
			}

			te.handleSession(session, tcpConn)

			if _, ok := <-te.closeChan; !ok {
				return
			}
		}
	}()

	return nil
}

func (te *TunaExit) OnEntryConnected(callback func()) {
	te.onEntryConnected = callback
}

func (te *TunaExit) GetReverseIP() net.IP {
	return te.reverseIp
}

func (te *TunaExit) GetReverseTCPPorts() []int {
	return te.reverseTcp
}

func (te *TunaExit) GetReverseUDPPorts() []int {
	return te.reverseUdp
}

func (te *TunaExit) Close() {
	close(te.closeChan)
	Close(te.tcpListener)
	Close(te.udpConn)
	Close(te.common.tcpConn)
	Close(te.common.udpConn)
}
