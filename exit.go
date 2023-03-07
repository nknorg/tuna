package tuna

import (
	"errors"
	"fmt"
	"github.com/rdegges/go-ipify"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/nkn/v2/common"
	"github.com/nknorg/tuna/pb"
	"github.com/nknorg/tuna/util"
	"github.com/patrickmn/go-cache"
	"github.com/xtaci/smux"
)

type ExitServiceInfo struct {
	Address string `json:"address"`
	Price   string `json:"price"`
}

type TunaExit struct {
	// It's important to keep these uint64 field on top to avoid panic on arm32
	// architecture: https://github.com/golang/go/issues/23345
	reverseBytesEntryToExit     uint64
	reverseBytesExitToEntry     uint64
	reverseBytesEntryToExitPaid uint64
	reverseBytesExitToEntryPaid uint64

	*Common
	OnConnect   *OnConnect // override Common.OnConnect
	config      *ExitConfiguration
	services    []Service
	serviceConn *cache.Cache
	tcpListener net.Listener
	reverseIP   net.IP
	reverseTCP  []uint32
	reverseUDP  []uint32
}

func NewTunaExit(services []Service, wallet *nkn.Wallet, client *nkn.MultiClient, config *ExitConfiguration) (*TunaExit, error) {
	config, err := MergedExitConfig(config)
	if err != nil {
		return nil, err
	}

	var service *Service
	var serviceInfo *ServiceInfo
	var subscriptionPrefix string
	var reverseMetadata *pb.ServiceMetadata
	if config.Reverse {
		if len(services) != 1 {
			return nil, errors.New("services should have length 1")
		}

		subscriptionPrefix = config.ReverseSubscriptionPrefix

		service = &Service{
			Name:          config.ReverseServiceName,
			Encryption:    services[0].Encryption,
			UDPBufferSize: services[0].UDPBufferSize,
		}
		if service.UDPBufferSize == 0 {
			service.UDPBufferSize = DefaultUDPBufferSize
		}

		serviceInfo = &ServiceInfo{
			MaxPrice:  config.ReverseMaxPrice,
			IPFilter:  &config.ReverseIPFilter,
			NknFilter: &config.ReverseNknFilter,
		}

		reverseMetadata = &pb.ServiceMetadata{}
		reverseMetadata.ServiceTcp = services[0].TCP
		reverseMetadata.ServiceUdp = services[0].UDP
		_, err = common.StringToFixed64(config.ReverseNanoPayFee)
		if err != nil {
			return nil, err
		}
		_, err = common.StringToFixed64(config.MinReverseNanoPayFee)
		if err != nil {
			return nil, err
		}
	} else {
		subscriptionPrefix = config.SubscriptionPrefix
	}

	c, err := NewCommon(
		service,
		serviceInfo,
		wallet,
		client,
		config.SeedRPCServerAddr,
		config.DialTimeout,
		subscriptionPrefix,
		config.Reverse,
		!config.Reverse,
		config.GeoDBPath,
		config.DownloadGeoDB,
		config.GetSubscribersBatchSize,
		config.MeasureBandwidth,
		config.MeasureBandwidthTimeout,
		config.MeasureBandwidthWorkersTimeout,
		config.MeasurementBytesDownLink,
		config.MeasureStoragePath,
		config.MaxMeasureWorkerPoolSize,
		config.TcpDialContext,
		config.HttpDialContext,
		config.WsDialContext,
		config.SortMeasuredNodes,
		reverseMetadata,
	)
	if err != nil {
		return nil, err
	}

	te := &TunaExit{
		Common:      c,
		OnConnect:   NewOnConnect(1, nil),
		config:      config,
		services:    services,
		serviceConn: cache.New(time.Duration(config.UDPTimeout)*time.Second, time.Second),
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

func (te *TunaExit) handleSession(session *smux.Session, connMetadata *pb.ConnectionMetadata) {
	bytesEntryToExit := make([]uint64, 256)
	bytesExitToEntry := make([]uint64, 256)
	var k string

	var npc *nkn.NanoPayClaimer
	var lastPaymentAmount, bytesPaid common.Fixed64
	var err error
	claimInterval := time.Duration(te.config.ClaimInterval) * time.Second
	onErr := nkn.NewOnError(1, nil)
	lastPaymentTime := time.Now()
	isClosed := false
	if connMetadata != nil {
		k = string(append(connMetadata.PublicKey, connMetadata.Nonce...))
		te.Common.reverseBytesEntryToExit[k] = bytesEntryToExit
		te.Common.reverseBytesExitToEntry[k] = bytesExitToEntry
	}

	getTotalCost := func() (common.Fixed64, common.Fixed64) {
		cost, totalBytes := common.Fixed64(0), common.Fixed64(0)
		for i := range bytesEntryToExit {
			entryToExit := common.Fixed64(atomic.LoadUint64(&bytesEntryToExit[i]))
			exitToEntry := common.Fixed64(atomic.LoadUint64(&bytesExitToEntry[i]))
			if entryToExit == 0 && exitToEntry == 0 {
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
			cost += entryToExitPrice*entryToExit/TrafficUnit + exitToEntryPrice*exitToEntry/TrafficUnit
			totalBytes += entryToExit + exitToEntry
		}
		return cost, totalBytes
	}

	if !te.config.Reverse {
		npc, err = te.Client.NewNanoPayClaimer(te.config.BeneficiaryAddr, int32(claimInterval/time.Millisecond), int32(nanoPayClaimerLinger/time.Millisecond), te.config.MinFlushAmount, onErr)
		if err != nil {
			log.Fatalln(err)
		}

		defer npc.Close()

		go checkNanoPayClaim(session, npc, onErr, &isClosed)

		go checkPayment(session, &lastPaymentTime, &lastPaymentAmount, &bytesPaid, &isClosed, getTotalCost)
	}

	for {
		stream, err := session.AcceptStream()
		if err != nil {
			log.Println("Couldn't accept stream:", err)
			session.Close()
			break
		}

		go func() {
			err := func() error {
				streamMetadata, err := readStreamMetadata(stream)
				if err != nil {
					return fmt.Errorf("read stream metadata error: %v", err)
				}

				if streamMetadata.IsPayment {
					return handlePaymentStream(stream, npc, &lastPaymentTime, &lastPaymentAmount, &bytesPaid, getTotalCost)
				}

				serviceID := byte(streamMetadata.ServiceId)
				portID := int(streamMetadata.PortId)

				service, err := te.getService(serviceID)
				if err != nil {
					return err
				}
				tcpPortsCount := len(service.TCP)
				udpPortsCount := len(service.UDP)
				var protocol string
				var port int
				if portID < tcpPortsCount {
					protocol = tcp4
					port = int(service.TCP[portID])
				} else if portID-tcpPortsCount < udpPortsCount {
					protocol = udp4
					portID -= tcpPortsCount
					port = int(service.UDP[portID])
				} else {
					return fmt.Errorf("invalid portId: %d", portID)
				}

				serviceInfo := te.config.Services[service.Name]
				host := serviceInfo.Address + ":" + strconv.Itoa(port)

				conn, err := net.DialTimeout(protocol, host, time.Duration(te.config.DialTimeout)*time.Second)
				if err != nil {
					return err
				}

				if te.config.Reverse {
					go te.pipe(conn, stream, &te.reverseBytesEntryToExit)
					go te.pipe(stream, conn, &te.reverseBytesExitToEntry)
				} else {
					go te.pipe(conn, stream, &te.Common.reverseBytesEntryToExit[k][serviceID])
					go te.pipe(stream, conn, &te.Common.reverseBytesExitToEntry[k][serviceID])
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
			if te.IsClosed() {
				return
			}
			if err != nil {
				if strings.Contains(err.Error(), "use of closed network connection") {
					te.Close()
					return
				}
				log.Println("Couldn't accept client connection:", err)
				time.Sleep(time.Second)
				continue
			}

			go func() {
				err := func() error {
					defer Close(conn)

					encryptedConn, connMetadata, err := te.wrapConn(conn, nil, nil)
					if err != nil {
						return fmt.Errorf("wrap conn error: %v", err)
					}

					defer Close(encryptedConn)

					if connMetadata.IsMeasurement {
						err = util.BandwidthMeasurementServer(encryptedConn, int(connMetadata.MeasurementBytesDownlink), maxMeasureBandwidthTimeout)
						if err != nil {
							return fmt.Errorf("bandwidth measurement server error: %v", err)
						}
						return nil
					}

					session, err := smux.Server(encryptedConn, nil)
					if err != nil {
						return fmt.Errorf("create session error: %v", err)
					}

					te.handleSession(session, connMetadata)

					return nil
				}()
				if err != nil {
					log.Println(err)
				}
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

func (te *TunaExit) getServiceConn(connID []byte, serviceID byte, portID byte) (*net.UDPConn, error) {
	connKey := strconv.Itoa(int(ConnIDToPort(connID)))
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
		conn, err = net.DialUDP(udp4, nil, &net.UDPAddr{Port: int(port)})
		if err != nil {
			log.Println("Couldn't connect to local UDP port", port, "with error:", err)
			return nil, err
		}

		if service.UDPBufferSize == 0 {
			service.UDPBufferSize = DefaultUDPBufferSize
		}
		conn.SetWriteBuffer(service.UDPBufferSize)
		conn.SetReadBuffer(service.UDPBufferSize)

		te.serviceConn.Set(connKey, conn, cache.DefaultExpiration)

		prefix := []byte{connID[0], connID[1], serviceID, portID}
		go func() {
			defer te.serviceConn.Delete(connKey)
			serviceBuffer := make([]byte, service.UDPBufferSize)

			for {
				n, _, err := conn.ReadFromUDP(serviceBuffer)
				if err != nil {
					log.Println("Couldn't receive data from service:", err)
					Close(conn)
					break
				}

				te.udpWriteChan <- append(prefix, serviceBuffer[:n]...)
			}
		}()
	} else {
		conn = x.(*net.UDPConn)
	}

	return conn, nil
}

func (te *TunaExit) listenUDP(port int) error {
	conn, err := net.ListenUDP(udp4, &net.UDPAddr{Port: port})
	if err != nil {
		log.Println("Couldn't bind listener:", err)
		return err
	}
	encConn := NewEncryptUDPConn(conn)
	udpConn, err := te.wrapUDPConn(encConn, nil, nil, nil)
	if err != nil {
		log.Println("wrap udp conn err:", err)
		return err
	}
	te.StartUDPReaderWriter(udpConn, nil, nil, nil)
	te.udpConn = udpConn
	te.readUDP()
	return nil
}

func (te *TunaExit) readUDP() {
	go func() {
		for {
			if te.IsClosed() {
				return
			}
			serverReadChan, err := te.GetServerUDPReadChan(false)
			if err != nil {
				log.Println("Couldn't get server connection:", err)
				continue
			}
			data := <-serverReadChan
			if len(data) < PrefixLen {
				log.Println("empty udp packet received")
				te.Close()
				return
			}

			serviceConn, err := te.getServiceConn(data[0:2], data[2], data[3])
			if err != nil {
				log.Println("get service conn error:", err)
				continue
			}
			_, _, err = serviceConn.WriteMsgUDP(data[PrefixLen:], nil, nil)
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
			te.config.SubscriptionReplaceTxPool,
			te.Client,
			te.closeChan,
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
	defer te.Close()

	geoCloseChan := make(chan struct{})
	defer close(geoCloseChan)
	if len(te.ServiceInfo.IPFilter.GetProviders()) > 0 {
		go te.ServiceInfo.IPFilter.StartUpdateDataFile(geoCloseChan)
	}

	serviceID := byte(0)
	service, err := te.getService(serviceID)
	if err != nil {
		return err
	}

	var paymentStream *smux.Stream
	var recipient string
	getPaymentStreamRecipient := func() (*smux.Stream, string, error) {
		return paymentStream, recipient, nil
	}

	var tcpConn net.Conn
	var payOnce sync.Once
	for {
		err = te.Common.CreateServerConn(true)
		if err != nil {
			if err == ErrClosed {
				return nil
			}
			log.Println("Couldn't connect to reverse entry:", err)
			time.Sleep(1 * time.Second)
			continue
		}
		if te.udpConn != nil {
			te.StartUDPReaderWriter(te.udpConn, nil, &te.reverseBytesEntryToExit, &te.reverseBytesExitToEntry)
		}

		udpPort := 0
		var udpConn Conn
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

		buf, err := ReadVarBytes(stream, maxServiceMetadataSize)
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

		reverseIP := tcpConn.RemoteAddr().(*net.TCPAddr).IP
		reverseTCP := reverseMetadata.ServiceTcp
		if len(reverseTCP) > 0 {
			go func() {
				conn, err := net.DialTimeout(tcp4, fmt.Sprintf("%s:%d", reverseIP.String(), reverseTCP[0]), defaultReverseTestTimeout)
				if err == nil {
					time.Sleep(defaultReverseTestTimeout)
					conn.Close()
				}
			}()

			session.SetDeadline(time.Now().Add(defaultReverseTestTimeout))

			stream, err := session.AcceptStream()
			if err != nil {
				log.Println("Couldn't accept stream test conn from reverse entry:", err)
				time.Sleep(1 * time.Second)
				continue
			}

			stream.Close()

			session.SetDeadline(time.Time{})
		}

		ps, err := openPaymentStream(session)
		if err != nil {
			log.Println("Couldn't open payment stream:", err)
			time.Sleep(1 * time.Second)
			continue
		}

		paymentStream, recipient = ps, te.GetPaymentReceiver()

		te.RLock()
		if te.isClosed {
			te.RUnlock()
			return nil
		}
		te.reverseIP = reverseIP
		te.reverseTCP = reverseTCP
		te.reverseUDP = reverseMetadata.ServiceUdp
		te.OnConnect.receive()
		te.RUnlock()

		if udpConn != nil {
			te.readUDP()
		}

		payOnce.Do(func() {
			go te.startPayment(
				&te.reverseBytesEntryToExit, &te.reverseBytesExitToEntry,
				&te.reverseBytesEntryToExitPaid, &te.reverseBytesExitToEntryPaid,
				te.config.ReverseNanoPayFee,
				te.config.MinReverseNanoPayFee,
				te.config.ReverseNanoPayFeeRatio,
				getPaymentStreamRecipient,
			)
		})

		te.handleSession(session, nil)

		Close(tcpConn)
		Close(udpConn)
		te.CloseUDPConn()

		if !shouldReconnect {
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
	te.WaitSessions()

	te.Lock()
	defer te.Unlock()

	if te.isClosed {
		return
	}

	te.isClosed = true
	close(te.closeChan)
	Close(te.tcpListener)
	Close(te.udpConn)
	Close(te.Common.tcpConn)
	Close(te.Common.udpConn)

	te.CloseUDPConn()
	te.OnConnect.close()
}

func (te *TunaExit) CloseUDPConn() {
	items := te.serviceConn.Items()
	for k, v := range items {
		conn := v.Object.(*net.UDPConn)
		te.serviceConn.Delete(k)
		Close(conn)
	}
}

func (te *TunaExit) IsClosed() bool {
	te.RLock()
	defer te.RUnlock()
	return te.isClosed
}
