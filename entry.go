package tuna

import (
	"errors"
	"log"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	nkn "github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/nkn/common"
	"github.com/patrickmn/go-cache"
	"github.com/trueinsider/smux"
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
	Session            *smux.Session
	closeChan          chan struct{}
	bytesIn            uint64
	bytesInPaid        uint64
	bytesOut           uint64
	bytesOutPaid       uint64
	reverseBytesIn     uint64
	reverseBytesOut    uint64
	reverseBeneficiary common.Uint160
}

func NewTunaEntry(service *Service, serviceInfo *ServiceInfo, config *EntryConfiguration, wallet *nkn.Wallet) *TunaEntry {
	te := &TunaEntry{
		Common: &Common{
			Service:            service,
			ServiceInfo:        serviceInfo,
			Wallet:             wallet,
			DialTimeout:        config.DialTimeout,
			SubscriptionPrefix: config.SubscriptionPrefix,
			Reverse:            config.Reverse,
		},
		config:       config,
		tcpListeners: make(map[byte]*net.TCPListener),
		serviceConn:  make(map[byte]*net.UDPConn),
		clientAddr:   cache.New(time.Duration(config.UDPTimeout)*time.Second, time.Second),
		closeChan:    make(chan struct{}),
	}
	te.SetServerUDPReadChan(make(chan []byte))
	te.SetServerUDPWriteChan(make(chan []byte))
	return te
}

func (te *TunaEntry) Start() {
	for {
		if err := te.CreateServerConn(true); err != nil {
			log.Println("Couldn't connect to node:", err)
			time.Sleep(1 * time.Second)
			continue
		}
		listenIP := net.ParseIP(te.ServiceInfo.ListenIP)
		tcpPorts, err := te.listenTCP(listenIP, te.Service.TCP)
		if err != nil {
			te.close()
			return
		}
		if len(tcpPorts) > 0 {
			log.Printf("Serving %s on localhost tcp port %v", te.Service.Name, tcpPorts)
		}

		udpPorts, err := te.listenUDP(listenIP, te.Service.UDP)
		if err != nil {
			te.close()
			return
		}
		if len(udpPorts) > 0 {
			log.Printf("Serving %s on localhost udp port %v", te.Service.Name, udpPorts)
		}

		go func() {
			session, err := te.getSession(false)
			if err != nil {
				return
			}
			stream, err := session.OpenStream()
			if err != nil {
				return
			}
			stream.Close()
			for {
				_, err = session.AcceptStream()
				if err != nil {
					log.Println("Close connection:", err)
					return
				}
			}
		}()

		go func() {
			var np *nkn.NanoPay
			lastCost := common.Fixed64(0)
			entryToExitPrice, exitToEntryPrice := te.GetPrice()
			for {
				select {
				case <-te.closeChan:
					return
				default:
				}

				bytesIn := atomic.LoadUint64(&te.bytesIn)
				bytesOut := atomic.LoadUint64(&te.bytesOut)
				cost := exitToEntryPrice*common.Fixed64(bytesIn-te.bytesInPaid)/TrafficUnit + entryToExitPrice*common.Fixed64(bytesOut-te.bytesOutPaid)/TrafficUnit
				if cost == lastCost {
					time.Sleep(DefaultNanoPayUpdateInterval / 10)
					continue
				}
				lastCost = cost
				session, err := te.getSession(false)
				if err != nil {
					log.Printf("get session err: %v", err)
					time.Sleep(time.Second)
					continue
				}
				err = sendNanoPay(np, session, te.Wallet, &cost, te.Common, te.config.NanoPayFee)
				if err != nil {
					log.Printf("send nano payment err: %v", err)
					time.Sleep(time.Second)
					continue
				}

				te.bytesInPaid = bytesIn
				te.bytesOutPaid = bytesOut

				time.Sleep(DefaultNanoPayUpdateInterval)
			}
		}()
		break
	}

	<-te.closeChan
}

func (te *TunaEntry) StartReverse(stream *smux.Stream) error {
	metadata := te.GetMetadata()
	listenIP := net.ParseIP(te.ServiceInfo.ListenIP)
	tcpPorts, err := te.listenTCP(listenIP, metadata.ServiceTcp)
	if err != nil {
		te.close()
		return err
	}
	udpPorts, err := te.listenUDP(listenIP, metadata.ServiceUdp)
	if err != nil {
		te.close()
		return err
	}

	serviceMetadata := CreateRawMetadata(0, tcpPorts, udpPorts, "", 0, 0, "", te.config.ReverseBeneficiaryAddr)
	_, err = stream.Write(serviceMetadata)
	if err != nil {
		te.close()
		return err
	}

	go func() {
		session, err := te.getSession(false)
		if err != nil {
			return
		}
		stream, err := session.OpenStream()
		if err != nil {
			return
		}
		stream.Close()

		lastClaimed := common.Fixed64(0)
		lastUpdate := time.Now()
		claimInterval := time.Duration(te.config.ReverseClaimInterval) * time.Second
		onErr := nkn.NewOnError(1, nil)
		isClosed := false
		entryToExitPrice, exitToEntryPrice, err := ParsePrice(te.config.ReversePrice)
		if err != nil {
			log.Println("parse reverse price error:", err)
			te.close()
			return
		}

		npc, err := te.Wallet.NewNanoPayClaimer(te.config.ReverseBeneficiaryAddr, int32(claimInterval/time.Millisecond), onErr)
		if err != nil {
			log.Println(err)
			te.close()
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
			stream, err = session.AcceptStream()
			if err != nil {
				log.Println("Close connection:", err)
				te.close()
				return
			}

			if len(stream.Metadata()) == 0 { // payment stream
				totalCost := getTotalCost()
				err = nanoPayClaim(stream, npc, &lastClaimed)
				if err != nil {
					log.Println("nanoPayClaim failed:", err)
					continue
				}

				lastUpdate = time.Now()

				if !checkTrafficCoverage(session, totalCost, lastClaimed, &isClosed) {
					continue
				}
			}
		}
	}()

	<-te.closeChan

	return nil
}

func (te *TunaEntry) close() {
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
	}
}

func (te *TunaEntry) getSession(force bool) (*smux.Session, error) {
	if te.Reverse && force {
		te.close()
		return nil, errors.New("reverse connection to service is dead")
	}
	if te.Session == nil || te.Session.IsClosed() || force {
		conn, err := te.GetServerTCPConn(force)
		if err != nil {
			return nil, err
		}
		te.Session, err = smux.Client(conn, nil)
		if err != nil {
			return nil, err
		}
	}

	return te.Session, nil
}

func (te *TunaEntry) openStream(portID byte, force bool) (*smux.Stream, error) {
	session, err := te.getSession(force)
	if err != nil {
		return nil, err
	}
	serviceID := te.GetMetadata().ServiceId
	stream, err := session.OpenStream(byte(serviceID), portID)
	if err != nil {
		return te.openStream(portID, true)
	}
	return stream, err
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
					Close(conn)
					if strings.Contains(err.Error(), "use of closed network connection") {
						te.close()
						return
					}
					time.Sleep(time.Second)
					continue
				}

				stream, err := te.openStream(portID, false)
				if err != nil {
					log.Println("Couldn't open stream:", err)
					Close(conn)
					time.Sleep(time.Second)
					continue
				}
				if te.config.Reverse {
					go Pipe(stream, conn, &te.reverseBytesIn)
					go Pipe(conn, stream, &te.reverseBytesOut)
				} else {
					go Pipe(stream, conn, &te.bytesOut)
					go Pipe(conn, stream, &te.bytesIn)
				}
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
