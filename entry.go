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

	nkn "github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/nkn/common"
	"github.com/nknorg/tuna/pb"
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
}

type TunaEntry struct {
	*Common
	config             *EntryConfiguration
	tcpListeners       map[byte]*net.TCPListener
	serviceConn        map[byte]*net.UDPConn
	clientAddr         *cache.Cache
	session            *smux.Session
	paymentStream      *smux.Stream
	closeChan          chan struct{}
	bytesIn            uint64
	bytesInPaid        uint64
	bytesOut           uint64
	bytesOutPaid       uint64
	reverseBytesIn     uint64
	reverseBytesOut    uint64
	reverseBeneficiary common.Uint160
}

func NewTunaEntry(service *Service, serviceInfo *ServiceInfo, config *EntryConfiguration, wallet *nkn.Wallet) (*TunaEntry, error) {
	common, err := NewCommon(service, serviceInfo, wallet, config.DialTimeout, config.SubscriptionPrefix, config.Reverse, nil)
	if err != nil {
		return nil, err
	}

	te := &TunaEntry{
		Common:       common,
		config:       config,
		tcpListeners: make(map[byte]*net.TCPListener),
		serviceConn:  make(map[byte]*net.UDPConn),
		clientAddr:   cache.New(time.Duration(config.UDPTimeout)*time.Second, time.Second),
		closeChan:    make(chan struct{}),
	}

	te.SetServerUDPReadChan(make(chan []byte))
	te.SetServerUDPWriteChan(make(chan []byte))

	return te, nil
}

func (te *TunaEntry) Start() {
	for {
		err := te.CreateServerConn(true)
		if err != nil {
			log.Println("Couldn't connect to node:", err)
			time.Sleep(1 * time.Second)
			continue
		}

		listenIP := net.ParseIP(te.ServiceInfo.ListenIP)
		tcpPorts, err := te.listenTCP(listenIP, te.Service.TCP)
		if err != nil {
			te.Close()
			return
		}
		if len(tcpPorts) > 0 {
			log.Printf("Serving %s on localhost tcp port %v", te.Service.Name, tcpPorts)
		}

		udpPorts, err := te.listenUDP(listenIP, te.Service.UDP)
		if err != nil {
			te.Close()
			return
		}
		if len(udpPorts) > 0 {
			log.Printf("Serving %s on localhost udp port %v", te.Service.Name, udpPorts)
		}

		te.OnConnect.receive()

		go func() {
			session, err := te.getSession(false)
			if err != nil {
				return
			}

			for {
				_, err = session.AcceptStream()
				if err != nil {
					log.Println("Close connection:", err)
					return
				}
			}
		}()

		go te.Common.startPayment(
			&te.bytesIn, &te.bytesOut, &te.bytesInPaid, &te.bytesOutPaid,
			te.config.NanoPayFee,
			te.getPaymentStream,
			te.closeChan,
		)

		break
	}

	<-te.closeChan
}

func (te *TunaEntry) StartReverse(stream *smux.Stream) error {
	metadata := te.GetMetadata()
	listenIP := net.ParseIP(te.ServiceInfo.ListenIP)
	tcpPorts, err := te.listenTCP(listenIP, metadata.ServiceTcp)
	if err != nil {
		te.Close()
		return err
	}
	udpPorts, err := te.listenUDP(listenIP, metadata.ServiceUdp)
	if err != nil {
		te.Close()
		return err
	}

	serviceMetadata := CreateRawMetadata(0, tcpPorts, udpPorts, "", 0, 0, "", te.config.ReverseBeneficiaryAddr)
	err = WriteVarBytes(stream, serviceMetadata)
	if err != nil {
		te.Close()
		return err
	}

	go func() {
		session, err := te.getSession(false)
		if err != nil {
			return
		}

		lastClaimed := common.Fixed64(0)
		lastUpdate := time.Now()
		claimInterval := time.Duration(te.config.ReverseClaimInterval) * time.Second
		onErr := nkn.NewOnError(1, nil)
		isClosed := false
		entryToExitPrice, exitToEntryPrice, err := ParsePrice(te.config.ReversePrice)
		if err != nil {
			log.Println("parse reverse price error:", err)
			te.Close()
			return
		}

		npc, err := te.Wallet.NewNanoPayClaimer(te.config.ReverseBeneficiaryAddr, int32(claimInterval/time.Millisecond), onErr)
		if err != nil {
			log.Println(err)
			te.Close()
			return
		}

		getTotalCost := func() common.Fixed64 {
			in := atomic.LoadUint64(&te.reverseBytesIn)
			out := atomic.LoadUint64(&te.reverseBytesOut)
			return entryToExitPrice*common.Fixed64(out)/TrafficUnit + exitToEntryPrice*common.Fixed64(in)/TrafficUnit
		}

		go checkNanoPayClaim(session, npc, onErr, &isClosed)

		go checkPaymentTimeout(session, 2*DefaultNanoPayUpdateInterval, &lastUpdate, &isClosed, getTotalCost)

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
						return fmt.Errorf("Read stream metadata error: %v", err)
					}

					if streamMetadata.IsPayment {
						return handlePaymentStream(session, stream, npc, &lastClaimed, getTotalCost, &lastUpdate, &isClosed)
					}

					return nil
				}()
				if err != nil {
					log.Println(err)
					Close(stream)
				}
			}()
		}
	}()

	<-te.closeChan

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

func (te *TunaEntry) getSession(force bool) (*smux.Session, error) {
	if te.Reverse && force {
		te.Close()
		return nil, errors.New("reverse connection to service is dead")
	}

	if te.session == nil || te.session.IsClosed() || force {
		conn, err := te.GetServerTCPConn(force)
		if err != nil {
			return nil, err
		}

		te.session, err = smux.Client(conn, nil)
		if err != nil {
			return nil, err
		}

		te.paymentStream, err = openPaymentStream(te.session)
		if err != nil {
			return nil, err
		}
	}

	return te.session, nil
}

func (te *TunaEntry) getPaymentStream() (*smux.Stream, error) {
	_, err := te.getSession(false)
	if err != nil {
		return nil, err
	}
	paymentStream := te.paymentStream
	if paymentStream == nil {
		return nil, errors.New("nil payment stream")
	}
	return paymentStream, nil
}

func (te *TunaEntry) openServiceStream(portID byte, force bool) (*smux.Stream, error) {
	session, err := te.getSession(force)
	if err != nil {
		return nil, err
	}

	stream, err := session.OpenStream()
	if err != nil {
		if !force {
			return te.openServiceStream(portID, true)
		}
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
					log.Println("Couldn't accept connection:", err)
					if strings.Contains(err.Error(), "use of closed network connection") {
						te.Close()
						return
					}
					time.Sleep(time.Second)
					continue
				}

				go func() {
					stream, err := te.openServiceStream(portID, false)
					if err != nil {
						log.Println("Couldn't open stream:", err)
						Close(conn)
						return
					}

					if te.config.Reverse {
						go Pipe(stream, conn, &te.reverseBytesIn)
						go Pipe(conn, stream, &te.reverseBytesOut)
					} else {
						go Pipe(stream, conn, &te.bytesOut)
						go Pipe(conn, stream, &te.bytesIn)
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
				log.Println("Couldn't accept client connection:", err)
				continue
			}

			go func() {
				err := func() error {
					defer Close(tcpConn)

					te, err := NewTunaEntry(&Service{}, &ServiceInfo{ListenIP: serviceListenIP}, config, wallet)
					if err != nil {
						return err
					}

					encryptedConn, err := te.Common.encryptConn(tcpConn, nil)
					if err != nil {
						return err
					}

					defer Close(encryptedConn)

					te.session, err = smux.Server(encryptedConn, nil)
					if err != nil {
						return fmt.Errorf("create session error: %v", err)
					}

					stream, err := te.session.AcceptStream()
					if err != nil {
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

					te.StartReverse(stream)

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
	)

	return nil
}
