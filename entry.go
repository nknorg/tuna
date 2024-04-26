package tuna

import (
	"bytes"
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
	tcpPorts           []uint32
	udpPorts           []uint32
}

func NewTunaEntry(service Service, serviceInfo ServiceInfo, wallet *nkn.Wallet, client *nkn.MultiClient, config *EntryConfiguration) (*TunaEntry, error) {
	config, err := MergedEntryConfig(config)
	if err != nil {
		return nil, err
	}
	if !config.Reverse {
		_, err = common.StringToFixed64(config.NanoPayFee)
		if err != nil {
			log.Fatalln("Parse NanoPayFee error:", err)
		}
		_, err = common.StringToFixed64(config.MinNanoPayFee)
		if err != nil {
			log.Fatalln("Parse MinNanoPayFee error:", err)
		}
	}

	c, err := NewCommon(
		&service,
		&serviceInfo,
		wallet,
		client,
		config.SeedRPCServerAddr,
		config.DialTimeout,
		config.SubscriptionPrefix,
		config.Reverse,
		config.Reverse,
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
		nil,
		config.MinBalance,
	)
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
	return te, nil
}

func (te *TunaEntry) Start(shouldReconnect bool) error {
	defer te.Close()

	listenIP := net.ParseIP(te.ServiceInfo.ListenIP)
	if listenIP == nil {
		listenIP = net.ParseIP(defaultServiceListenIP)
	}

	tcpPorts, err := te.listenTCP(listenIP, te.Service.TCP)
	if err != nil {
		return err
	}
	if len(tcpPorts) > 0 {
		log.Printf("Serving %s on localhost tcp port %v", te.Service.Name, tcpPorts)
		te.tcpPorts = tcpPorts
	}

	udpPorts, err := te.listenUDP(listenIP, te.Service.UDP)
	if err != nil {
		return err
	}
	if len(udpPorts) > 0 {
		log.Printf("Serving %s on localhost udp port %v", te.Service.Name, udpPorts)
		te.udpPorts = udpPorts
	}

	for {
		if te.IsClosed() {
			return nil
		}

		err := te.CreateServerConn(true)
		if err != nil {
			log.Println("Couldn't connect to node:", err)
			if errors.Is(err, nkn.ErrInsufficientBalance) {
				return err
			}
			time.Sleep(1 * time.Second)
			continue
		}
		if te.udpConn != nil {
			te.startUDPReaderWriter(te.udpConn, nil, &te.bytesExitToEntry, &te.bytesEntryToExit)
			go sendPingMsg(te.udpConn, te.udpCloseChan)
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

		getPaymentStreamRecipient := func() (*smux.Stream, string, error) {
			ps, err := te.getPaymentStream()
			return ps, te.GetPaymentReceiver(), err
		}

		go te.startPayment(
			&te.bytesEntryToExit, &te.bytesExitToEntry,
			&te.bytesEntryToExitPaid, &te.bytesExitToEntryPaid,
			te.config.NanoPayFee,
			te.config.MinNanoPayFee,
			te.config.NanoPayFeeRatio,
			getPaymentStreamRecipient,
		)

		break
	}

	geoCloseChan := make(chan struct{})
	defer close(geoCloseChan)
	if te.ServiceInfo.IPFilter != nil && len(te.ServiceInfo.IPFilter.GetProviders()) > 0 {
		go te.ServiceInfo.IPFilter.StartUpdateDataFile(geoCloseChan)
	}

	<-te.closeChan

	return nil
}

func (te *TunaEntry) StartReverse(stream *smux.Stream, connMetadata *pb.ConnectionMetadata) error {
	defer te.Close()

	metadata := te.GetMetadata()
	listenIP := net.ParseIP(te.ServiceInfo.ListenIP)
	if listenIP == nil {
		listenIP = net.ParseIP(defaultServiceListenIP)
	}
	tcpPorts, err := te.listenTCP(listenIP, metadata.ServiceTcp)
	if err != nil {
		return err
	}

	var udpPorts []uint32
	if len(metadata.ServiceUdp) > 0 {
		if len(te.Service.UDP) > 0 || metadata.ServiceUdp[0] == 0 {
			metadata.ServiceUdp = tcpPorts // same ports with tcp if udp ports not specific
		}
		udpPorts, err = te.listenUDP(listenIP, metadata.ServiceUdp)
		if err != nil {
			return err
		}
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

	npc, err := te.Client.NewNanoPayClaimer(te.config.ReverseBeneficiaryAddr, int32(claimInterval/time.Millisecond), int32(nanoPayClaimerLinger/time.Millisecond), te.config.ReverseMinFlushAmount, onErr)
	if err != nil {
		return err
	}

	defer npc.Close()
	k := string(append(connMetadata.PublicKey, connMetadata.Nonce...))

	getTotalCost := func() (common.Fixed64, common.Fixed64) {
		cost, totalBytes := common.Fixed64(0), common.Fixed64(0)
		for i := range te.Common.reverseBytesEntryToExit[k] {
			entryToExit := common.Fixed64(atomic.LoadUint64(&te.Common.reverseBytesEntryToExit[k][i]))
			exitToEntry := common.Fixed64(atomic.LoadUint64(&te.Common.reverseBytesExitToEntry[k][i]))
			if entryToExit == 0 && exitToEntry == 0 {
				continue
			}
			cost += entryToExitPrice*entryToExit/TrafficUnit + exitToEntryPrice*exitToEntry/TrafficUnit
			totalBytes += entryToExit + exitToEntry
		}
		tcpBytesEntryToExit := common.Fixed64(atomic.LoadUint64(&te.reverseBytesEntryToExit))
		tcpBytesExitToEntry := common.Fixed64(atomic.LoadUint64(&te.reverseBytesExitToEntry))
		totalBytes += tcpBytesEntryToExit + tcpBytesExitToEntry
		cost += entryToExitPrice*tcpBytesEntryToExit/TrafficUnit + exitToEntryPrice*tcpBytesExitToEntry/TrafficUnit
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

func (te *TunaEntry) GetTCPPorts() []uint32 {
	return te.tcpPorts
}

func (te *TunaEntry) GetUDPPorts() []uint32 {
	return te.udpPorts
}

func (te *TunaEntry) Close() {
	te.WaitSessions()

	te.Lock()
	defer te.Unlock()

	if te.isClosed {
		return
	}

	te.isClosed = true
	close(te.closeChan)
	close(te.udpCloseChan)
	for _, listener := range te.tcpListeners {
		Close(listener)
	}
	for _, conn := range te.serviceConn {
		Close(conn)
	}
	if te.session != nil {
		te.session.Close()
	}
	te.OnConnect.close()
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
		listener, err := net.ListenTCP(tcp4, &net.TCPAddr{IP: ip, Port: int(_port)})
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
				if c, ok := conn.(*net.TCPConn); ok {
					err := c.SetLinger(5)
					if err != nil {
						log.Println("Couldn't set linger:", err)
						continue
					}
				}
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
						go te.pipe(stream, conn, &te.reverseBytesEntryToExit)
						go te.pipe(conn, stream, &te.reverseBytesExitToEntry)
					} else {
						go te.pipe(stream, conn, &te.bytesEntryToExit)
						go te.pipe(conn, stream, &te.bytesExitToEntry)
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

			if len(data) < PrefixLen {
				log.Println("empty udp packet received")
				te.Close()
				return
			}
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

			_, _, err = serviceConn.WriteMsgUDP(data[PrefixLen:], nil, clientAddr)
			if err != nil {
				log.Println("Couldn't send data to client:", err)
			}
		}
	}()

	for i, _port := range ports {
		localConn, err := net.ListenUDP(udp4, &net.UDPAddr{IP: ip, Port: int(_port)})
		if err != nil {
			log.Println("Couldn't bind listener:", err)
			return nil, err
		}

		bs := te.Service.UDPBufferSize
		if te.Reverse {
			bs = MaxUDPBufferSize
		}
		localConn.SetWriteBuffer(bs)
		localConn.SetReadBuffer(bs)

		port := localConn.LocalAddr().(*net.UDPAddr).Port
		portID := byte(i)
		assignedPorts = append(assignedPorts, uint32(port))

		te.serviceConn[portID] = localConn

		go func() {
			localBuffer := make([]byte, bs)
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
	config, err := MergedEntryConfig(config)
	if err != nil {
		return err
	}

	var serviceListenIP string
	if net.ParseIP(config.ReverseServiceListenIP) == nil {
		serviceListenIP = defaultReverseServiceListenIP
	} else {
		serviceListenIP = config.ReverseServiceListenIP
	}

	ip, err := ipify.GetIp()
	if err != nil {
		return fmt.Errorf("couldn't get IP: %v", err)
	}

	listener, err := net.ListenTCP(tcp4, &net.TCPAddr{Port: int(config.ReverseTCP)})
	if err != nil {
		return err
	}

	uConn, err := net.ListenUDP(udp4, &net.UDPAddr{Port: int(config.ReverseUDP)})
	if err != nil {
		return err
	}
	encConn := NewEncryptUDPConn(uConn)
	var encKeys, udpEntrys, tcpEntrys, tcpReady, udpReady, addrToKey, keyToAddr sync.Map
	go func() {
		if encConn.IsClosed() {
			return
		}
		buffer := make([]byte, MaxUDPBufferSize)
		for {
			n, from, encrypted, err := encConn.ReadFromUDPEncrypted(buffer)
			if err != nil {
				log.Println("Couldn't receive exit's data:", err)
				continue
			}
			if bytes.Equal(buffer[:PrefixLen], []byte{PrefixLen - 1: 0}) && n > PrefixLen {
				connMetadata, err := parseUDPConnMetadata(buffer[PrefixLen:n])
				if err != nil {
					log.Println("Couldn't read udp metadata from client:", err)
					continue
				}
				if connMetadata.IsPing || encrypted {
					continue
				}
				k := string(append(connMetadata.PublicKey, connMetadata.Nonce...))
				readyChan, _ := tcpReady.LoadOrStore(k, make(chan struct{}))
				<-readyChan.(chan struct{})

				encryptKey, ok := encKeys.Load(k)
				if !ok {
					log.Println("no encrypt key found")
					continue
				}
				key := encryptKey.(*[encryptKeySize]byte)
				err = encConn.AddCodec(from, key, connMetadata.EncryptionAlgo, false)
				if err != nil {
					log.Println(err)
					return
				}

				te, ok := tcpEntrys.Load(k)
				if !ok {
					log.Println("no encrypt key found from tcp conn")
					continue
				}
				t := te.(*TunaEntry)
				t.Common.reverseBytesEntryToExit[k] = make([]uint64, 256)
				t.Common.reverseBytesExitToEntry[k] = make([]uint64, 256)
				udpEntrys.Store(from.String(), te)
				addrToKey.Store(from.String(), k)
				keyToAddr.Store(k, from.String())

				if c, ok := udpReady.Load(k); ok {
					closeChan(c.(chan struct{}))
				}

				continue
			}
			if !encrypted {
				log.Println("Unencrypted udp packet received")
				continue
			}
			entry, ok := udpEntrys.Load(from.String())
			if !ok {
				log.Println("no entry found for udp data")
				continue
			}
			te := entry.(*TunaEntry)
			udpReadchan, err := te.GetServerUDPReadChan(false)
			if err != nil {
				log.Println("Couldn't get udp read chan:", err)
				continue
			}
			if n > 0 {
				k, ok := addrToKey.Load(from.String())
				if !ok {
					log.Println("no key found for udp data")
					continue
				}
				b := make([]byte, n)
				copy(b, buffer[:n])
				udpReadchan <- b
				atomic.AddUint64(&te.Common.reverseBytesEntryToExit[k.(string)][b[2]], uint64(n))
			}
		}
	}()

	clientConfig := &nkn.ClientConfig{
		HttpDialContext: config.HttpDialContext,
		WsDialContext:   config.WsDialContext,
	}
	if len(config.SeedRPCServerAddr) > 0 {
		clientConfig.SeedRPCServerAddr = nkn.NewStringArray(config.SeedRPCServerAddr...)
	}
	client, err := nkn.NewMultiClient(wallet.Account(), randomIdentifier(), numRPCClients, false, clientConfig)
	if err != nil {
		return err
	}

	go func() {
		for {
			tcpConn, err := listener.Accept()
			if c, ok := tcpConn.(*net.TCPConn); ok {
				err := c.SetLinger(5)
				if err != nil {
					log.Println("Couldn't set linger:", err)
					continue
				}
			}
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
					te, err := NewTunaEntry(Service{}, ServiceInfo{ListenIP: serviceListenIP}, wallet, client, config)
					if err != nil {
						return fmt.Errorf("create tuna entry error: %v", err)
					}
					encryptedConn, connMetadata, err := te.wrapConn(tcpConn, nil, nil)
					if err != nil {
						te.Close()
						return fmt.Errorf("wrap conn error: %v", err)
					}

					connKey := string(append(connMetadata.PublicKey, connMetadata.Nonce...))
					tcpEntrys.Store(connKey, te)
					k, _ := te.encryptKeys.Load(connKey)
					encKeys.Store(connKey, k)

					rc, _ := tcpReady.LoadOrStore(connKey, make(chan struct{}))
					readyChan := rc.(chan struct{})
					closeChan(readyChan)

					udpReady.Store(connKey, make(chan struct{}))

					defer func() {
						Close(encryptedConn)
						te, ok := tcpEntrys.Load(k)
						if ok {
							t := te.(*TunaEntry)
							t.Close()
						}
						tcpEntrys.Delete(connKey)
						encKeys.Delete(connKey)
					}()

					if connMetadata.IsMeasurement {
						err = util.BandwidthMeasurementServer(encryptedConn, int(connMetadata.MeasurementBytesDownlink), maxMeasureBandwidthTimeout)
						if err != nil {
							return fmt.Errorf("bandwidth measurement server error: %v", err)
						}
						return nil
					}

					te.session, err = smux.Server(encryptedConn, nil)
					if err != nil {
						return fmt.Errorf("create session error: %v", err)
					}

					stream, err := te.session.AcceptStream()
					if err != nil {
						te.Close()
						return fmt.Errorf("couldn't accept stream: %v", err)
					}

					buf, err := ReadVarBytes(stream, maxServiceMetadataSize)
					if err != nil {
						return fmt.Errorf("couldn't read service metadata: %v", err)
					}

					metadata, err := ReadMetadata(string(buf))
					if err != nil {
						return fmt.Errorf("couldn't decode service metadata: %v", err)
					}

					te.SetMetadata(metadata)

					te.SetServerTCPConn(encryptedConn)

					if len(metadata.ServiceUdp) > 0 {
						go func() {
							tr, ok := tcpReady.Load(connKey)
							if !ok {
								return
							}
							<-tr.(chan struct{})

							ur, ok := udpReady.Load(connKey)
							if !ok {
								return
							}
							<-ur.(chan struct{})

							addr, ok := keyToAddr.Load(connKey)
							if !ok {
								return
							}
							clientAddr := addr.(string)

							ip, portStr, err := net.SplitHostPort(clientAddr)
							if err != nil {
								log.Printf("parse host error: %v\n", err)
								return
							}
							port, err := strconv.Atoi(portStr)
							if err != nil {
								log.Printf("parse port error: %v\n", err)
								return
							}

							udpAddr := net.UDPAddr{IP: net.ParseIP(ip), Port: port}
							for {
								if te.isClosed {
									return
								}
								select {
								case data := <-te.udpWriteChan:
									n, _, err := encConn.WriteMsgUDP(data, nil, &udpAddr)
									if err != nil {
										log.Println("couldn't send udp data to server:", err)
										continue
									}
									key, ok := addrToKey.Load(udpAddr.String())
									if !ok || len(data) < 2 {
										log.Println("no key found from this udp addr:", udpAddr.String())
										continue
									}
									atomic.AddUint64(&te.Common.reverseBytesExitToEntry[key.(string)][data[2]], uint64(n))
								case <-te.udpCloseChan:
									return
								}
							}
						}()
					}

					err = te.StartReverse(stream, connMetadata)
					if err != nil {
						te.Close()
						return fmt.Errorf("start reverse error: %v", err)
					}

					return nil
				}()
				if err != nil {
					tcpConn.Close()
					log.Println(err)
				}
			}()
		}
	}()

	for _, rsn := range strings.Split(config.ReverseServiceName, ",") {
		UpdateMetadata(
			strings.Trim(rsn, " "),
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
			config.ReverseSubscriptionReplaceTxPool,
			client,
			make(chan struct{}),
		)
	}

	return nil
}
