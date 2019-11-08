package exit

import (
	"errors"
	"io"
	"io/ioutil"
	"log"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	. "github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/nkn/common"
	"github.com/nknorg/nkn/transaction"
	"github.com/patrickmn/go-cache"
	"github.com/rdegges/go-ipify"
	"github.com/trueinsider/smux"

	"github.com/nknorg/tuna"
)

type ServiceInfo struct {
	Address string `json:"address"`
	Price   string `json:"price"`
}

type Configuration struct {
	ListenTCP            int                    `json:"ListenTCP"`
	ListenUDP            int                    `json:"ListenUDP"`
	Reverse              bool                   `json:"Reverse"`
	ReverseRandomPorts   bool                   `json:"ReverseRandomPorts"`
	ReverseMaxPrice      string                 `json:"ReverseMaxPrice"`
	DialTimeout          uint16                 `json:"DialTimeout"`
	UDPTimeout           uint16                 `json:"UDPTimeout"`
	Seed                 string                 `json:"Seed"`
	SubscriptionPrefix   string                 `json:"SubscriptionPrefix"`
	SubscriptionDuration uint32                 `json:"SubscriptionDuration"`
	SubscriptionFee      string                 `json:"SubscriptionFee"`
	ClaimInterval        uint32                 `json:"ClaimInterval"`
	Services             map[string]ServiceInfo `json:"Services"`
}

type Service struct {
	Name string `json:"name"`
	TCP  []int  `json:"tcp"`
	UDP  []int  `json:"udp"`
}

type TunaExit struct {
	config           Configuration
	wallet           *WalletSDK
	services         []Service
	serviceConn      *cache.Cache
	common           *tuna.Common
	clientConn       *net.UDPConn
	reverseIp        net.IP
	reverseTcp       []int
	reverseUdp       []int
	onEntryConnected func()
}

func NewTunaExit(config Configuration, services []Service, wallet *WalletSDK) *TunaExit {
	return &TunaExit{
		config:      config,
		wallet:      wallet,
		services:    services,
		serviceConn: cache.New(time.Duration(config.UDPTimeout)*time.Second, time.Second),
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
	bytesOut := make([]uint64, 256)

	claimInterval := time.Duration(te.config.ClaimInterval) * time.Second
	errChan := make(chan error)
	npc := te.wallet.NewNanoPayClaimer(claimInterval, errChan)
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
						tuna.Close(session)
						tuna.Close(conn)
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
					tuna.Close(session)
					tuna.Close(conn)
					isClosed = true
					break
				}

				if common.Fixed64(float64(lastComputed)*0.9) > lastClaimed {
					log.Println("Nano pay amount covers less than 90% of total cost")
					tuna.Close(session)
					tuna.Close(conn)
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
				for i := range bytesOut {
					bytes := atomic.LoadUint64(&bytesOut[i])
					if bytes == 0 {
						continue
					}
					service, err := te.getService(byte(i))
					if err != nil {
						continue
					}
					serviceInfo := te.config.Services[service.Name]
					price, err := common.StringToFixed64(serviceInfo.Price)
					if err != nil {
						continue
					}
					totalCost += price * common.Fixed64(bytes) / 1048576
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

		serviceInfo := te.config.Services[service.Name]
		host := serviceInfo.Address + ":" + strconv.Itoa(port)

		conn, err := net.DialTimeout(string(protocol), host, time.Duration(te.config.DialTimeout)*time.Second)
		if err != nil {
			log.Println("Couldn't connect to host", host, "with error:", err)
			tuna.Close(stream)
			continue
		}

		go tuna.Pipe(conn, stream, nil)
		go tuna.Pipe(stream, conn, &bytesOut[serviceId])
	}

	tuna.Close(session)
	tuna.Close(conn)
	isClosed = true
}

func (te *TunaExit) listenTCP(port int) {
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
			go te.handleSession(session, conn)
		}
	}()
}

func (te *TunaExit) getService(serviceId byte) (*Service, error) {
	if int(serviceId) >= len(te.services) {
		return nil, errors.New("Wrong serviceId: " + strconv.Itoa(int(serviceId)))
	}
	return &te.services[serviceId], nil
}

func (te *TunaExit) getServiceConn(addr *net.UDPAddr, connId []byte, serviceId byte, portId byte) (*net.UDPConn, error) {
	connKey := addr.String() + ":" + tuna.GetConnIdString(connId)
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
			tuna.Close(conn)
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
					tuna.Close(conn)
					break
				}
				_, err = te.clientConn.WriteToUDP(append(prefix, serviceBuffer[:n]...), addr)
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

func (te *TunaExit) listenUDP(port int) {
	var err error
	te.clientConn, err = net.ListenUDP("udp", &net.UDPAddr{Port: port})
	if err != nil {
		log.Println("Couldn't bind listener:", err)
	}
	te.readUDP()
}

func (te *TunaExit) readUDP() {
	go func() {
		clientBuffer := make([]byte, 2048)
		for {
			n, addr, err := te.clientConn.ReadFromUDP(clientBuffer)
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

func (te *TunaExit) updateAllMetadata(ip string, tcpPort int, udpPort int) {
	for serviceName, serviceInfo := range te.config.Services {
		serviceId, err := te.getServiceId(serviceName)
		if err != nil {
			log.Panicln(err)
		}
		service, err := te.getService(serviceId)
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
			serviceInfo.Price,
			te.config.SubscriptionPrefix,
			te.config.SubscriptionDuration,
			te.config.SubscriptionFee,
			te.wallet,
		)
	}
}

func (te *TunaExit) Start() {
	ip, err := ipify.GetIp()
	if err != nil {
		log.Panicln("Couldn't get IP:", err)
	}
	te.listenTCP(te.config.ListenTCP)
	te.listenUDP(te.config.ListenUDP)

	te.updateAllMetadata(ip, te.config.ListenTCP, te.config.ListenUDP)
}

func (te *TunaExit) StartReverse(serviceName string) {
	serviceId, err := te.getServiceId(serviceName)
	if err != nil {
		log.Panicln(err)
	}
	service, err := te.getService(serviceId)
	if err != nil {
		log.Panicln(err)
	}

	reverseMetadata := &tuna.Metadata{}
	reverseMetadata.ServiceTCP = service.TCP
	reverseMetadata.ServiceUDP = service.UDP

	maxPrice, err := common.StringToFixed64(te.config.ReverseMaxPrice)
	if err != nil {
		log.Panicln(err)
	}

	te.common = &tuna.Common{
		ServiceName:        "reverse",
		MaxPrice:           maxPrice,
		Wallet:             te.wallet,
		DialTimeout:        te.config.DialTimeout,
		ReverseMetadata:    reverseMetadata,
		SubscriptionPrefix: te.config.SubscriptionPrefix,
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
			udpConn, _ := te.common.GetServerUDPConn(false)
			if udpConn != nil {
				_, udpPortString, _ := net.SplitHostPort(udpConn.LocalAddr().String())
				udpPort, _ = strconv.Atoi(udpPortString)
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

			serviceMetadata := tuna.CreateRawMetadata(
				serviceId,
				tcpPorts,
				udpPorts,
				"",
				-1,
				udpPort,
				"",
			)

			tcpConn, _ = te.common.GetServerTCPConn(false)
			session, _ := smux.Server(tcpConn, nil)
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
				tuna.Close(tcpConn)
				break
			}
			reverseMetadataRaw := make([]byte, n)
			copy(reverseMetadataRaw, buf)
			reverseMetadata, err := tuna.ReadMetadata(string(reverseMetadataRaw))
			if err != nil {
				log.Println("Couldn't unmarshal metadata:", err)
				tuna.Close(tcpConn)
				break
			}
			te.reverseIp = tcpConn.RemoteAddr().(*net.TCPAddr).IP
			te.reverseTcp = reverseMetadata.ServiceTCP
			te.reverseUdp = reverseMetadata.ServiceUDP
			if te.onEntryConnected != nil {
				te.onEntryConnected()
			}

			te.handleSession(session, tcpConn)

			if udpConn != nil {
				te.clientConn = udpConn
				te.readUDP()
			}
		}
	}()
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
