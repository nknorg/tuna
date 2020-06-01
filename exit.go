package tuna

import (
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/nkn/common"
	"github.com/nknorg/tuna/pb"
	"github.com/patrickmn/go-cache"
	"github.com/rdegges/go-ipify"
	"github.com/xtaci/smux"
)

type ExitServiceInfo struct {
	Address string `json:"address"`
	Price   string `json:"price"`
}

type ExitConfiguration struct {
	BeneficiaryAddr           string                     `json:"beneficiaryAddr"`
	ListenTCP                 int32                      `json:"listenTCP"`
	ListenUDP                 int32                      `json:"listenUDP"`
	DialTimeout               int32                      `json:"dialTimeout"`
	UDPTimeout                int32                      `json:"udpTimeout"`
	SubscriptionPrefix        string                     `json:"subscriptionPrefix"`
	SubscriptionDuration      int32                      `json:"subscriptionDuration"`
	SubscriptionFee           string                     `json:"subscriptionFee"`
	ClaimInterval             int32                      `json:"claimInterval"`
	Services                  map[string]ExitServiceInfo `json:"services"`
	Reverse                   bool                       `json:"reverse"`
	ReverseRandomPorts        bool                       `json:"reverseRandomPorts"`
	ReverseMaxPrice           string                     `json:"reverseMaxPrice"`
	ReverseNanoPayFee         string                     `json:"reverseNanopayfee"`
	ReverseServiceName        string                     `json:"reverseServiceName"`
	ReverseSubscriptionPrefix string                     `json:"reverseSubscriptionPrefix"`
	ReverseEncryption         string                     `json:"reverseEncryption"`
	ReverseIPFilter           IPFilter                   `json:"reverseIPFilter"`
}

type TunaExit struct {
	*Common
	config              *ExitConfiguration
	services            []Service
	serviceConn         *cache.Cache
	tcpListener         net.Listener
	udpConn             *net.UDPConn
	reverseIP           net.IP
	reverseTCP          []uint32
	reverseUDP          []uint32
	closeChan           chan struct{}
	reverseBytesIn      uint64
	reverseBytesOut     uint64
	reverseBytesInPaid  uint64
	reverseBytesOutPaid uint64
}

func NewTunaExit(config *ExitConfiguration, services []Service, wallet *nkn.Wallet) (*TunaExit, error) {
	var service *Service
	var serviceInfo *ServiceInfo
	var subscriptionPrefix string
	var reverseMetadata *pb.ServiceMetadata
	if config.Reverse {
		if len(services) != 1 {
			return nil, errors.New("services should have length 1")
		}

		reverseServiceName := config.ReverseServiceName
		if len(reverseServiceName) == 0 {
			reverseServiceName = DefaultReverseServiceName
		}

		service = &Service{
			Name:       reverseServiceName,
			Encryption: services[0].Encryption,
		}

		serviceInfo = &ServiceInfo{
			MaxPrice: config.ReverseMaxPrice,
			IPFilter: &config.ReverseIPFilter,
		}

		subscriptionPrefix = config.ReverseSubscriptionPrefix

		reverseMetadata = &pb.ServiceMetadata{}
		reverseMetadata.ServiceTcp = services[0].TCP
		reverseMetadata.ServiceUdp = services[0].UDP
	} else {
		subscriptionPrefix = config.SubscriptionPrefix
	}

	c, err := NewCommon(service, serviceInfo, wallet, config.DialTimeout, subscriptionPrefix, config.Reverse, !config.Reverse, reverseMetadata)
	if err != nil {
		return nil, err
	}

	te := &TunaExit{
		Common:      c,
		config:      config,
		services:    services,
		serviceConn: cache.New(time.Duration(config.UDPTimeout)*time.Second, time.Second),
		closeChan:   make(chan struct{}, 0),
	}

	return te, nil
}

func (te *TunaExit) getServiceID(serviceName string) (byte, error) {
	for i, service := range te.services {
		if service.Name == serviceName {
			return byte(i), nil
		}
	}

	return 0, errors.New("Service " + serviceName + " not found")
}

func (te *TunaExit) handleSession(session *smux.Session) {
	bytesIn := make([]uint64, 256)
	bytesOut := make([]uint64, 256)

	claimInterval := time.Duration(te.config.ClaimInterval) * time.Second
	onErr := nkn.NewOnError(1, nil)
	var npc *nkn.NanoPayClaimer
	var err error
	lastClaimed := common.Fixed64(0)
	bytesPaid := common.Fixed64(0)
	lastUpdate := time.Now()
	isClosed := false

	getTotalCost := func() (common.Fixed64, common.Fixed64) {
		cost := common.Fixed64(0)
		totalBytes := common.Fixed64(0)
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
			cost += entryToExitPrice*common.Fixed64(in)/TrafficUnit + exitToEntryPrice*common.Fixed64(out)/TrafficUnit
			totalBytes += common.Fixed64(in) + common.Fixed64(out)
		}
		return cost, totalBytes
	}

	if !te.config.Reverse {
		npc, err = te.Wallet.NewNanoPayClaimer(te.config.BeneficiaryAddr, int32(claimInterval/time.Millisecond), onErr)
		if err != nil {
			log.Fatalln(err)
		}

		go checkNanoPayClaim(session, npc, onErr, &isClosed)

		go checkPayment(session, &lastUpdate, &bytesPaid, &lastClaimed, &isClosed, getTotalCost)
	}

	for {
		stream, err := session.AcceptStream()
		if err != nil {
			log.Println("Couldn't accept stream:", err)
			break
		}

		go func() {
			err := func() error {
				streamMetadata, err := readStreamMetadata(stream)
				if err != nil {
					return fmt.Errorf("read stream metadata error: %v", err)
				}

				if streamMetadata.IsPayment {
					return handlePaymentStream(stream, npc, &lastClaimed, &lastUpdate, &bytesPaid, getTotalCost)
				}

				serviceID := byte(streamMetadata.ServiceId)
				portID := int(streamMetadata.PortId)

				service, err := te.getService(serviceID)
				if err != nil {
					return err
				}
				tcpPortsCount := len(service.TCP)
				udpPortsCount := len(service.UDP)
				var protocol Protocol
				var port int
				if portID < tcpPortsCount {
					protocol = TCP
					port = int(service.TCP[portID])
				} else if portID-tcpPortsCount < udpPortsCount {
					protocol = UDP
					portID -= tcpPortsCount
					port = int(service.UDP[portID])
				} else {
					return fmt.Errorf("invalid portId: %d", portID)
				}

				serviceInfo := te.config.Services[service.Name]
				host := serviceInfo.Address + ":" + strconv.Itoa(port)

				conn, err := net.DialTimeout(string(protocol), host, time.Duration(te.config.DialTimeout)*time.Second)
				if err != nil {
					return err
				}

				if te.config.Reverse {
					go Pipe(conn, stream, &te.reverseBytesIn)
					go Pipe(stream, conn, &te.reverseBytesOut)
				} else {
					go Pipe(conn, stream, &bytesIn[serviceID])
					go Pipe(stream, conn, &bytesOut[serviceID])
				}

				return nil
			}()
			if err != nil {
				log.Println(err)
				Close(stream)
			}
		}()
	}

	Close(session)
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
				continue
			}

			go func() {
				defer Close(conn)

				encryptedConn, err := te.Common.encryptConn(conn, nil)
				if err != nil {
					log.Println(err)
					return
				}

				defer Close(encryptedConn)

				session, err := smux.Server(encryptedConn, nil)
				if err != nil {
					log.Println(err)
					return
				}

				te.handleSession(session)
			}()
		}
	}()

	return nil
}

func (te *TunaExit) getService(serviceID byte) (*Service, error) {
	if int(serviceID) >= len(te.services) {
		return nil, errors.New("Wrong serviceId: " + strconv.Itoa(int(serviceID)))
	}
	return &te.services[serviceID], nil
}

func (te *TunaExit) getServiceConn(addr *net.UDPAddr, connID []byte, serviceID byte, portID byte) (*net.UDPConn, error) {
	connKey := addr.String() + ":" + strconv.Itoa(int(ConnIDToPort(connID)))
	var conn *net.UDPConn
	var x interface{}
	var ok bool
	if x, ok = te.serviceConn.Get(connKey); !ok {
		service, err := te.getService(serviceID)
		if err != nil {
			log.Println(err)
			return nil, err
		}
		if int(portID) >= len(service.UDP) {
			return nil, fmt.Errorf("UDP portID %v out of range", portID)
		}
		port := service.UDP[portID]
		conn, err = net.DialUDP("udp", nil, &net.UDPAddr{Port: int(port)})
		if err != nil {
			log.Println("Couldn't connect to local UDP port", port, "with error:", err)
			Close(conn)
			return conn, err
		}

		te.serviceConn.Set(connKey, conn, cache.DefaultExpiration)

		prefix := []byte{connID[0], connID[1], serviceID, portID}
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

func (te *TunaExit) updateAllMetadata(ip string, tcpPort, udpPort uint32) error {
	for serviceName, serviceInfo := range te.config.Services {
		serviceID, err := te.getServiceID(serviceName)
		if err != nil {
			return err
		}
		UpdateMetadata(
			serviceName,
			serviceID,
			nil,
			nil,
			ip,
			tcpPort,
			udpPort,
			serviceInfo.Price,
			te.config.BeneficiaryAddr,
			te.config.SubscriptionPrefix,
			uint32(te.config.SubscriptionDuration),
			te.config.SubscriptionFee,
			te.Wallet,
		)
	}
	return nil
}

func (te *TunaExit) Start() error {
	ip, err := ipify.GetIp()
	if err != nil {
		return fmt.Errorf("couldn't get IP: %v", err)
	}

	err = te.listenTCP(int(te.config.ListenTCP))
	if err != nil {
		return err
	}

	err = te.listenUDP(int(te.config.ListenUDP))
	if err != nil {
		return err
	}

	return te.updateAllMetadata(ip, uint32(te.config.ListenTCP), uint32(te.config.ListenUDP))
}

func (te *TunaExit) StartReverse(shouldReconnect bool) error {
	serviceID := byte(0)
	service, err := te.getService(serviceID)
	if err != nil {
		return err
	}

	var tcpConn net.Conn
	for {
		err := te.Common.CreateServerConn(true)
		if err != nil {
			log.Println("Couldn't connect to reverse entry:", err)
			time.Sleep(1 * time.Second)
			continue
		}

		udpPort := 0
		var udpConn *net.UDPConn
		if len(service.UDP) > 0 {
			udpConn, err = te.Common.GetServerUDPConn(false)
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

		var tcpPorts []uint32
		var udpPorts []uint32
		if te.config.ReverseRandomPorts {
			tcpPorts = make([]uint32, len(service.TCP))
			udpPorts = make([]uint32, len(service.UDP))
		} else {
			tcpPorts = service.TCP
			udpPorts = service.UDP
		}

		serviceMetadata := CreateRawMetadata(
			serviceID,
			tcpPorts,
			udpPorts,
			"",
			0,
			uint32(udpPort),
			"",
			te.config.BeneficiaryAddr,
		)

		tcpConn, err = te.Common.GetServerTCPConn(false)
		if err != nil {
			log.Println(err)
			time.Sleep(1 * time.Second)
			continue
		}

		session, err := smux.Client(tcpConn, nil)
		if err != nil {
			log.Println(err)
			time.Sleep(1 * time.Second)
			continue
		}

		stream, err := session.OpenStream()
		if err != nil {
			log.Println("Couldn't open stream to reverse entry:", err)
			time.Sleep(1 * time.Second)
			continue
		}

		err = WriteVarBytes(stream, serviceMetadata)
		if err != nil {
			log.Println("Couldn't send metadata to reverse entry:", err)
			time.Sleep(1 * time.Second)
			continue
		}

		buf, err := ReadVarBytes(stream)
		if err != nil {
			log.Println("Couldn't read reverse metadata:", err)
			time.Sleep(1 * time.Second)
			continue
		}

		reverseMetadata, err := ReadMetadata(string(buf))
		if err != nil {
			log.Println("Couldn't unmarshal metadata:", err)
			time.Sleep(1 * time.Second)
			continue
		}

		paymentStream, err := openPaymentStream(session)
		if err != nil {
			log.Println("Couldn't open payment stream:", err)
			time.Sleep(1 * time.Second)
			continue
		}

		te.reverseIP = tcpConn.RemoteAddr().(*net.TCPAddr).IP
		te.reverseTCP = reverseMetadata.ServiceTcp
		te.reverseUDP = reverseMetadata.ServiceUdp
		te.OnConnect.receive()

		if udpConn != nil {
			te.udpConn = udpConn
			te.readUDP()
		}

		getPaymentStream := func() (*smux.Stream, error) {
			return paymentStream, nil
		}

		go te.Common.startPayment(
			&te.reverseBytesIn, &te.reverseBytesOut, &te.reverseBytesInPaid, &te.reverseBytesOutPaid,
			te.config.ReverseNanoPayFee,
			getPaymentStream,
			te.closeChan,
		)

		te.handleSession(session)

		Close(tcpConn)

		if !shouldReconnect {
			te.Close()
			break
		}

		select {
		case _, ok := <-te.closeChan:
			if !ok {
				return nil
			}
		default:
		}
	}

	return nil
}

func (te *TunaExit) GetReverseIP() net.IP {
	return te.reverseIP
}

func (te *TunaExit) GetReverseTCPPorts() []uint32 {
	return te.reverseTCP
}

func (te *TunaExit) GetReverseUDPPorts() []uint32 {
	return te.reverseUDP
}

func (te *TunaExit) Close() {
	te.Lock()
	defer te.Unlock()
	if !te.isClosed {
		te.isClosed = true
		close(te.closeChan)
		Close(te.tcpListener)
		Close(te.udpConn)
		Close(te.Common.tcpConn)
		Close(te.Common.udpConn)
		te.OnConnect.close()
	}
}
