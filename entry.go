package tuna

import (
	"errors"
	"fmt"
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
	"github.com/rdegges/go-ipify"
	"github.com/xtaci/smux"
)

type EntryConfiguration struct {
	Services                    map[string]ServiceInfo `json:"services"`
	DialTimeout                 int32                  `json:"dialTimeout"`
	UDPTimeout                  int32                  `json:"udpTimeout"`
	NanoPayFee                  string                 `json:"nanoPayFee"`
	SubscriptionPrefix          string                 `json:"subscriptionPrefix"`
	Reverse                     bool                   `json:"reverse"`
	ReverseBeneficiaryAddr      string                 `json:"reverseBeneficiaryAddr"`
	ReverseTCP                  int32                  `json:"reverseTCP"`
	ReverseUDP                  int32                  `json:"reverseUDP"`
	ReverseServiceListenIP      string                 `json:"reverseServiceListenIP"`
	ReversePrice                string                 `json:"reversePrice"`
	ReverseClaimInterval        int32                  `json:"reverseClaimInterval"`
	ReverseServiceName          string                 `json:"reverseServiceName"`
	ReverseSubscriptionPrefix   string                 `json:"reverseSubscriptionPrefix"`
	ReverseSubscriptionDuration int32                  `json:"reverseSubscriptionDuration"`
	ReverseSubscriptionFee      string                 `json:"reverseSubscriptionFee"`
	GeoDBPath                   string                 `json:"geoDBPath"`
	DownloadGeoDB               bool                   `json:"downloadGeoDB"`
	MeasureBandwidth            bool                   `json:"measureBandwidth"`
	MeasurementBytesDownLink    int32                  `json:"measurementBytesDownLink"`
}

type TunaEntry struct {
	// It's important to keep these uint64 field on top to avoid panic on arm32
	// architecture: https://github.com/golang/go/issues/23345
	bytesEntryToExit        uint64
	bytesEntryToExitPaid    uint64
	bytesExitToEntry        uint64
	bytesExitToEntryPaid    uint64
	reverseBytesEntryToExit uint64
	reverseBytesExitToEntry uint64

	*Common
	config             *EntryConfiguration
	tcpListeners       map[byte]*net.TCPListener
	serviceConn        map[byte]*net.UDPConn
	clientAddr         *cache.Cache
	session            *smux.Session
	paymentStream      *smux.Stream
	reverseBeneficiary common.Uint160
	sessionLock        sync.Mutex
}

func NewTunaEntry(service Service, serviceInfo ServiceInfo, wallet *nkn.Wallet, config *EntryConfiguration) (*TunaEntry, error) {
	c, err := NewCommon(&service, &serviceInfo, wallet, config.DialTimeout, config.SubscriptionPrefix, config.Reverse, config.Reverse, config.MeasureBandwidth, config.MeasurementBytesDownLink, nil)
	if err != nil {
		return nil, err
	}

	te := &TunaEntry{
		Common:       c,
		config:       config,
		tcpListeners: make(map[byte]*net.TCPListener),
		serviceConn:  make(map[byte]*net.UDPConn),
		clientAddr:   cache.New(time.Duration(config.UDPTimeout)*time.Second, time.Second),
	}

	te.SetServerUDPReadChan(make(chan []byte))
	te.SetServerUDPWriteChan(make(chan []byte))

	return te, nil
}

func (te *TunaEntry) Start(shouldReconnect bool) error {
	defer te.Close()

	listenIP := net.ParseIP(te.ServiceInfo.ListenIP)
	tcpPorts, err := te.listenTCP(listenIP, te.Service.TCP)
	if err != nil {
		return err
	}
	if len(tcpPorts) > 0 {
		log.Printf("Serving %s on localhost tcp port %v", te.Service.Name, tcpPorts)
	}

	udpPorts, err := te.listenUDP(listenIP, te.Service.UDP)
	if err != nil {
		return err
	}
	if len(udpPorts) > 0 {
		log.Printf("Serving %s on localhost udp port %v", te.Service.Name, udpPorts)
	}

	geoCloseChan := make(chan struct{})
	defer close(geoCloseChan)
	if !te.IsServer && te.ServiceInfo.IPFilter.NeedGeoInfo() {
		te.ServiceInfo.IPFilter.AddProvider(te.config.DownloadGeoDB, te.config.GeoDBPath)
		go te.ServiceInfo.IPFilter.UpdateDataFile(geoCloseChan)
	}

	for {
		if te.IsClosed() {
			return nil
		}

		err := te.CreateServerConn(true)
		if err != nil {
			log.Println("Couldn't connect to node:", err)
			time.Sleep(1 * time.Second)
			continue
		}

		go func() {
			for {
				session, err := te.getSession()
				if err != nil {
					return
				}

				_, err = session.AcceptStream()
				if err != nil {
					log.Println("Close connection:", err)
					session.Close()
					if !shouldReconnect {
						te.Close()
						return
					}
				}
			}
		}()

		go te.startPayment(
			&te.bytesEntryToExit, &te.bytesExitToEntry,
			&te.bytesEntryToExitPaid, &te.bytesExitToEntryPaid,
			te.config.NanoPayFee,
			te.getPaymentStream,
		)

		break
	}

	<-te.closeChan

	return nil
}

func (te *TunaEntry) StartReverse(stream *smux.Stream) error {
	defer te.Close()

	metadata := te.GetMetadata()
	listenIP := net.ParseIP(te.ServiceInfo.ListenIP)
	tcpPorts, err := te.listenTCP(listenIP, metadata.ServiceTcp)
	if err != nil {
		return err
	}
	udpPorts, err := te.listenUDP(listenIP, metadata.ServiceUdp)
	if err != nil {
		return err
	}

	serviceMetadata := CreateRawMetadata(0, tcpPorts, udpPorts, "", 0, 0, "", te.config.ReverseBeneficiaryAddr)
	err = WriteVarBytes(stream, serviceMetadata)
	if err != nil {
		return err
	}

	session, err := te.getSession()
	if err != nil {
		return err
	}

	var bytesPaid, lastPaymentAmount common.Fixed64
	lastPaymentTime := time.Now()
	claimInterval := time.Duration(te.config.ReverseClaimInterval) * time.Second
	onErr := nkn.NewOnError(1, nil)
	isClosed := false

	entryToExitPrice, exitToEntryPrice, err := ParsePrice(te.config.ReversePrice)
	if err != nil {
		return err
	}

	npc, err := te.Wallet.NewNanoPayClaimer(te.config.ReverseBeneficiaryAddr, int32(claimInterval/time.Millisecond), onErr)
	if err != nil {
		return err
	}

	defer npc.Close()

	getTotalCost := func() (common.Fixed64, common.Fixed64) {
		bytesEntryToExit := common.Fixed64(atomic.LoadUint64(&te.reverseBytesEntryToExit))
		bytesExitToEntry := common.Fixed64(atomic.LoadUint64(&te.reverseBytesExitToEntry))
		cost := entryToExitPrice*bytesEntryToExit/TrafficUnit + exitToEntryPrice*bytesExitToEntry/TrafficUnit
		totalBytes := bytesEntryToExit + bytesExitToEntry
		return cost, totalBytes
	}

	go checkNanoPayClaim(session, npc, onErr, &isClosed)

	go checkPayment(session, &lastPaymentTime, &lastPaymentAmount, &bytesPaid, &isClosed, getTotalCost)

	for {
		if te.IsClosed() {
			return nil
		}
		stream, err := session.AcceptStream()
		if err != nil {
			log.Println("Couldn't accept stream:", err)
			session.Close()
			break
		}

		go func(stream *smux.Stream) {
			err := func() error {
				streamMetadata, err := readStreamMetadata(stream)
				if err != nil {
					return fmt.Errorf("read stream metadata error: %v", err)
				}

				if streamMetadata.IsPayment {
					return handlePaymentStream(stream, npc, &lastPaymentTime, &lastPaymentAmount, &bytesPaid, getTotalCost)
				}
				return nil
			}()
			if err != nil {
				log.Println(err)
				Close(stream)
			}
		}(stream)
	}

	return nil
}

func (te *TunaEntry) Close() {
	te.Lock()
	defer te.Unlock()
	if !te.isClosed {
		te.isClosed = true
		close(te.closeChan)
		for _, listener := range te.tcpListeners {
			Close(listener)
		}
		for _, conn := range te.serviceConn {
			Close(conn)
		}
		te.OnConnect.close()
	}
}

func (te *TunaEntry) IsClosed() bool {
	te.RLock()
	defer te.RUnlock()
	return te.isClosed
}

func (te *TunaEntry) createSession(force bool) (*smux.Session, *smux.Stream, error) {
	conn, err := te.GetServerTCPConn(force)
	if err != nil {
		return nil, nil, err
	}

	session, err := smux.Client(conn, nil)
	if err != nil {
		return nil, nil, err
	}

	paymentStream, err := openPaymentStream(session)
	if err != nil {
		return nil, nil, err
	}

	return session, paymentStream, nil
}

func (te *TunaEntry) getSession() (*smux.Session, error) {
	te.sessionLock.Lock()
	defer te.sessionLock.Unlock()

	if te.session == nil || te.session.IsClosed() {
		if te.Reverse {
			return nil, errors.New("reverse connection to exit is dead")
		}

		session, paymentStream, err := te.createSession(false)
		if err != nil {
			session, paymentStream, err = te.createSession(true)
			if err != nil {
				return nil, err
			}
		}

		te.session = session
		te.paymentStream = paymentStream
	}

	return te.session, nil
}

func (te *TunaEntry) getPaymentStream() (*smux.Stream, error) {
	_, err := te.getSession()
	if err != nil {
		return nil, err
	}
	paymentStream := te.paymentStream
	if paymentStream == nil {
		return nil, errors.New("nil payment stream")
	}
	return paymentStream, nil
}

func (te *TunaEntry) openServiceStream(portID byte) (*smux.Stream, error) {
	session, err := te.getSession()
	if err != nil {
		return nil, err
	}

	stream, err := session.OpenStream()
	if err != nil {
		session.Close()
		return nil, err
	}

	streamMetadata := &pb.StreamMetadata{
		ServiceId: te.GetMetadata().ServiceId,
		PortId:    uint32(portID),
		IsPayment: false,
	}

	err = writeStreamMetadata(stream, streamMetadata)
	if err != nil {
		stream.Close()
		return nil, err
	}

	return stream, nil
}

func (te *TunaEntry) listenTCP(ip net.IP, ports []uint32) ([]uint32, error) {
	assignedPorts := make([]uint32, 0, len(ports))
	for i, _port := range ports {
		listener, err := net.ListenTCP(string(TCP), &net.TCPAddr{IP: ip, Port: int(_port)})
		if err != nil {
			log.Println("Couldn't bind listener:", err)
			return nil, err
		}
		port := listener.Addr().(*net.TCPAddr).Port
		portID := byte(i)
		assignedPorts = append(assignedPorts, uint32(port))

		te.tcpListeners[portID] = listener

		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					if te.IsClosed() {
						return
					}
					if strings.Contains(err.Error(), "use of closed network connection") {
						te.Close()
						return
					}
					log.Println("Couldn't accept connection:", err)
					time.Sleep(time.Second)
					continue
				}

				go func() {
					if te.IsClosed() {
						return
					}
					stream, err := te.openServiceStream(portID)
					if err != nil {
						log.Println("Couldn't open stream:", err)
						Close(conn)
						return
					}

					if te.config.Reverse {
						go Pipe(stream, conn, &te.reverseBytesEntryToExit)
						go Pipe(conn, stream, &te.reverseBytesExitToEntry)
					} else {
						go Pipe(stream, conn, &te.bytesEntryToExit)
						go Pipe(conn, stream, &te.bytesExitToEntry)
					}
				}()
			}
		}()
	}

	return assignedPorts, nil
}

func (te *TunaEntry) listenUDP(ip net.IP, ports []uint32) ([]uint32, error) {
	assignedPorts := make([]uint32, 0, len(ports))
	if len(ports) == 0 {
		return assignedPorts, nil
	}

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

			portID := data[3]
			port := ConnIDToPort(data)
			connID := strconv.Itoa(int(port))

			var serviceConn *net.UDPConn
			var ok bool
			if serviceConn, ok = te.serviceConn[portID]; !ok {
				log.Println("Couldn't get service conn for portId:", portID)
				continue
			}

			var x interface{}
			if x, ok = te.clientAddr.Get(connID); !ok {
				log.Println("Couldn't get client address for:", connID)
				continue
			}
			clientAddr := x.(*net.UDPAddr)

			_, err = serviceConn.WriteToUDP(data, clientAddr)
			if err != nil {
				log.Println("Couldn't send data to client:", err)
			}
		}
	}()

	for i, _port := range ports {
		localConn, err := net.ListenUDP(string(UDP), &net.UDPAddr{IP: ip, Port: int(_port)})
		if err != nil {
			log.Println("Couldn't bind listener:", err)
			return nil, err
		}
		port := localConn.LocalAddr().(*net.UDPAddr).Port
		portID := byte(i)
		assignedPorts = append(assignedPorts, uint32(port))

		te.serviceConn[portID] = localConn

		go func() {
			localBuffer := make([]byte, 2048)
			for {
				if te.IsClosed() {
					return
				}

				n, addr, err := localConn.ReadFromUDP(localBuffer)
				if err != nil {
					log.Println("Couldn't receive data from local:", err)
					te.Close()
					return
				}

				connKey := strconv.Itoa(addr.Port)
				te.clientAddr.Set(connKey, addr, cache.DefaultExpiration)

				serverWriteChan, err := te.GetServerUDPWriteChan(false)
				if err != nil {
					log.Println("Couldn't get remote connection:", err)
					continue
				}
				connID := PortToConnID(uint16(addr.Port))
				serviceID := te.GetMetadata().ServiceId
				serverWriteChan <- append([]byte{connID[0], connID[1], byte(serviceID), portID}, localBuffer[:n]...)
			}
		}()
	}

	return assignedPorts, nil
}

func StartReverse(config *EntryConfiguration, wallet *nkn.Wallet) error {
	var serviceListenIP string
	if net.ParseIP(config.ReverseServiceListenIP) == nil {
		serviceListenIP = DefaultReverseServiceListenIP
	} else {
		serviceListenIP = config.ReverseServiceListenIP
	}

	ip, err := ipify.GetIp()
	if err != nil {
		return fmt.Errorf("Couldn't get IP: %v", err)
	}

	listener, err := net.ListenTCP(string(TCP), &net.TCPAddr{Port: int(config.ReverseTCP)})
	if err != nil {
		return err
	}

	udpConn, err := net.ListenUDP(string(UDP), &net.UDPAddr{Port: int(config.ReverseUDP)})
	if err != nil {
		return err
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
				if strings.Contains(err.Error(), "use of closed network connection") {
					return
				}
				log.Println("Couldn't accept client connection:", err)
				time.Sleep(time.Second)
				continue
			}

			go func() {
				err := func() error {
					defer Close(tcpConn)

					te, err := NewTunaEntry(Service{}, ServiceInfo{ListenIP: serviceListenIP}, wallet, config)
					if err != nil {
						return err
					}

					encryptedConn, connMetadata, err := te.wrapConn(tcpConn, nil, nil)
					if err != nil {
						return err
					}

					defer Close(encryptedConn)

					if connMetadata.IsMeasurement {
						return util.BandwidthMeasurementServer(encryptedConn, int(connMetadata.MeasurementBytesDownlink), 0)
					}

					te.session, err = smux.Server(encryptedConn, nil)
					if err != nil {
						return fmt.Errorf("create session error: %v", err)
					}

					stream, err := te.session.AcceptStream()
					if err != nil {
						te.session.Close()
						return fmt.Errorf("couldn't accept stream: %v", err)
					}

					buf, err := ReadVarBytes(stream)
					if err != nil {
						return fmt.Errorf("couldn't read service metadata: %v", err)
					}

					te.SetMetadata(string(buf))

					te.SetServerTCPConn(encryptedConn)

					metadata := te.GetMetadata()
					if metadata.UdpPort > 0 {
						ip, _, err := net.SplitHostPort(encryptedConn.RemoteAddr().String())
						if err != nil {
							return fmt.Errorf("Parse host error: %v", err)
						}

						udpAddr := net.UDPAddr{IP: net.ParseIP(ip), Port: int(metadata.UdpPort)}
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

					err = te.StartReverse(stream)
					if err != nil {
						log.Println(err)
					}

					return nil
				}()
				if err != nil {
					log.Println(err)
				}
			}()
		}
	}()

	reverseServiceName := config.ReverseServiceName
	if len(reverseServiceName) == 0 {
		reverseServiceName = DefaultReverseServiceName
	}

	UpdateMetadata(
		reverseServiceName,
		0,
		nil,
		nil,
		ip,
		uint32(config.ReverseTCP),
		uint32(config.ReverseUDP),
		config.ReversePrice,
		config.ReverseBeneficiaryAddr,
		config.ReverseSubscriptionPrefix,
		uint32(config.ReverseSubscriptionDuration),
		config.ReverseSubscriptionFee,
		wallet,
		make(chan struct{}),
	)

	return nil
}
