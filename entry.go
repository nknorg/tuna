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
	"unsafe"

	"github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/nkn/transaction"

	nknsdk "github.com/nknorg/nkn-sdk-go"

	"github.com/nknorg/nkn/common"
	cache "github.com/patrickmn/go-cache"
	"github.com/trueinsider/smux"
)

type EntryServiceInfo struct {
	MaxPrice string `json:"maxPrice"`
	ListenIP string `json:"ListenIP"`
	Address  string `json:"address"`
	Price    string `json:"price"`
}

type EntryConfiguration struct {
	BeneficiaryAddr             string                      `json:"BeneficiaryAddr"`
	Services                    map[string]EntryServiceInfo `json:"Services"`
	DialTimeout                 uint16                      `json:"DialTimeout"`
	UDPTimeout                  uint16                      `json:"UDPTimeout"`
	NanoPayFee                  string                      `json:"NanoPayFee"`
	SubscriptionPrefix          string                      `json:"SubscriptionPrefix"`
	Reverse                     bool                        `json:"Reverse"`
	ReverseBeneficiaryAddr      string                      `json:"ReverseBeneficiaryAddr"`
	ReverseTCP                  int                         `json:"ReverseTCP"`
	ReverseUDP                  int                         `json:"ReverseUDP"`
	ReverseServiceListenIP      string                      `json:"ReverseServiceListenIP"`
	ReversePrice                string                      `json:"ReversePrice"`
	ReverseClaimInterval        uint32                      `json:"ReverseClaimInterval"`
	ReverseSubscriptionPrefix   string                      `json:"ReverseSubscriptionPrefix"`
	ReverseSubscriptionDuration uint32                      `json:"ReverseSubscriptionDuration"`
	ReverseSubscriptionFee      string                      `json:"ReverseSubscriptionFee"`
	ClaimInterval               uint32                      `json:"ClaimInterval"`
}

type TunaEntry struct {
	*Common
	config             *EntryConfiguration
	tcpListeners       map[byte]*net.TCPListener
	services           []Service
	serviceConn        map[byte]*net.UDPConn
	clientAddr         *cache.Cache
	Session            *smux.Session
	closeChan          chan struct{}
	bytesIn            uint64
	bytesInPaid        uint64
	bytesOut           uint64
	bytesOutPaid       uint64
	reverseBeneficiary common.Uint160
	wallet             *nknsdk.Wallet
}

func NewTunaEntry(service *Service, listenIP net.IP, entryToExitMaxPrice, exitToEntryMaxPrice common.Fixed64, config *EntryConfiguration, wallet *nkn.Wallet) *TunaEntry {
	te := &TunaEntry{
		Common: &Common{
			Service:             service,
			ListenIP:            listenIP,
			EntryToExitMaxPrice: entryToExitMaxPrice,
			ExitToEntryMaxPrice: exitToEntryMaxPrice,
			Wallet:              wallet,
			DialTimeout:         config.DialTimeout,
			SubscriptionPrefix:  config.SubscriptionPrefix,
			Reverse:             config.Reverse,
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

		tcpPorts, err := te.listenTCP(te.ListenIP, te.Service.TCP)
		if err != nil {
			te.close()
			return
		}
		if len(tcpPorts) > 0 {
			log.Printf("Serving %s on localhost tcp port %v", te.Service.Name, tcpPorts)
		}

		udpPorts, err := te.listenUDP(te.ListenIP, te.Service.UDP)
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
			for {
				time.Sleep(time.Second * 2)
				bytesIn := atomic.LoadUint64(&te.bytesIn)
				bytesOut := atomic.LoadUint64(&te.bytesOut)
				entryToExitPrice, exitToEntryPrice := te.GetPrice()
				delta := exitToEntryPrice*common.Fixed64(bytesIn-te.bytesInPaid)/TrafficUnit + entryToExitPrice*common.Fixed64(bytesOut-te.bytesOutPaid)/TrafficUnit
				if delta == 0 {
					continue
				}
				paymentReceiver := te.GetPaymentReceiver()
				if np == nil || np.Address() != paymentReceiver {
					var err error
					np, err = te.Wallet.NewNanoPay(paymentReceiver, te.config.NanoPayFee, DefaultNanoPayDuration)
					if err != nil {
						continue
					}
				}
				tx, err := np.IncrementAmount(delta.String())
				if err != nil {
					continue
				}
				txData := tx.ToArray()
				session, err := te.getSession(false)
				if err != nil {
					continue
				}
				stream, err := session.OpenStream()
				if err != nil {
					continue
				}
				n, err := stream.Write(txData)
				if n == len(txData) && err == nil {
					te.bytesInPaid = bytesIn
					te.bytesOutPaid = bytesOut
				}
				stream.Close()
			}
		}()

		break
	}

	<-te.closeChan
}

func (te *TunaEntry) StartReverse(stream *smux.Stream, conn net.Conn) error {
	metadata := te.GetMetadata()
	tcpPorts, err := te.listenTCP(te.ListenIP, metadata.ServiceTCP)
	if err != nil {
		te.close()
		return err
	}
	udpPorts, err := te.listenUDP(te.ListenIP, metadata.ServiceUDP)
	if err != nil {
		te.close()
		return err
	}

	serviceMetadata := CreateRawMetadata(
		0,
		tcpPorts,
		udpPorts,
		"",
		-1,
		-1,
		"",
		te.config.ReverseBeneficiaryAddr,
	)
	_, err = stream.Write(serviceMetadata)
	if err != nil {
		te.close()
		return err
	}
	session, err := te.getSession(false)

	bytesIn := make([]uint64, 256)
	bytesOut := make([]uint64, 256)

	claimInterval := time.Duration(te.config.ClaimInterval) * time.Second
	onErr := nknsdk.NewOnError(1, nil)
	var npc *nknsdk.NanoPayClaimer
	lastComputed := common.Fixed64(0)
	lastClaimed := common.Fixed64(0)
	lastUpdate := time.Now()
	isClosed := false

	npc, err = te.wallet.NewNanoPayClaimer(int32(claimInterval/time.Millisecond), onErr, te.config.BeneficiaryAddr)
	if err != nil {
		log.Fatalln(err)
	}

	go func() {
		for {
			err := <-onErr.C
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
					fmt.Println("totalCost:", totalCost)
				}

				lastComputed = totalCost
				lastClaimed = amount.ToFixed64()
				lastUpdate = time.Now()
			}(stream)
			continue
		}

		serviceId := metadata[0]
		portId := int(metadata[1])

		service, err := te.getService(serviceId)
		if err != nil {
			log.Println(err)
			//Close(stream)
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
			//Close(stream)
			continue
		}

		serviceInfo := te.config.Services[service.Name]
		host := serviceInfo.Address + ":" + strconv.Itoa(port)

		conn, err := net.DialTimeout(string(protocol), host, time.Duration(te.config.DialTimeout)*time.Second)
		if err != nil {
			log.Println("Couldn't connect to host", host, "with error:", err)
			//Close(stream)
			continue
		}

		go Pipe(conn, stream, &bytesIn[serviceId])
		go Pipe(stream, conn, &bytesOut[serviceId])
	}

	Close(session)
	Close(conn)
	isClosed = true
	<-te.closeChan

	return nil
}

func (te *TunaEntry) close() {
	close(te.closeChan)
	for _, listener := range te.tcpListeners {
		Close(listener)
	}
	for _, conn := range te.serviceConn {
		Close(conn)
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

func (te *TunaEntry) openStream(portId byte, force bool) (*smux.Stream, error) {
	session, err := te.getSession(force)
	if err != nil {
		return nil, err
	}
	serviceId := te.GetMetadata().ServiceId
	stream, err := session.OpenStream(serviceId, portId)
	if err != nil {
		return te.openStream(portId, true)
	}
	return stream, err
}

func (te *TunaEntry) listenTCP(ip net.IP, ports []int) ([]int, error) {
	assignedPorts := make([]int, 0)
	for i, _port := range ports {
		listener, err := net.ListenTCP(string(TCP), &net.TCPAddr{IP: ip, Port: _port})
		if err != nil {
			log.Println("Couldn't bind listener:", err)
			return nil, err
		}
		port := listener.Addr().(*net.TCPAddr).Port
		portId := byte(i)
		assignedPorts = append(assignedPorts, port)

		te.tcpListeners[portId] = listener

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

				stream, err := te.openStream(portId, false)
				if err != nil {
					log.Println("Couldn't open stream:", err)
					Close(conn)
					time.Sleep(time.Second)
					continue
				}

				go Pipe(stream, conn, &te.bytesOut)
				go Pipe(conn, stream, &te.bytesIn)
			}
		}()
	}

	return assignedPorts, nil
}

func (te *TunaEntry) listenUDP(ip net.IP, ports []int) ([]int, error) {
	assignedPorts := make([]int, 0)
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

			portId := data[3]
			connId := GetConnIdString(data)

			var serviceConn *net.UDPConn
			var ok bool
			if serviceConn, ok = te.serviceConn[portId]; !ok {
				log.Println("Couldn't get service conn for portId:", portId)
				continue
			}

			var x interface{}
			if x, ok = te.clientAddr.Get(connId); !ok {
				log.Println("Couldn't get client address for:", connId)
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
		localConn, err := net.ListenUDP(string(UDP), &net.UDPAddr{IP: ip, Port: _port})
		if err != nil {
			log.Println("Couldn't bind listener:", err)
			return nil, err
		}
		port := localConn.LocalAddr().(*net.UDPAddr).Port
		portId := byte(i)
		assignedPorts = append(assignedPorts, port)

		te.serviceConn[portId] = localConn

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
				connId := GetConnIdData(addr.Port)
				serviceId := te.GetMetadata().ServiceId
				serverWriteChan <- append([]byte{connId[0], connId[1], serviceId, portId}, localBuffer[:n]...)
			}
		}()
	}

	return assignedPorts, nil
}

func GetConnIdData(port int) [2]byte {
	return *(*[2]byte)(unsafe.Pointer(&port))
}

func (te *TunaEntry) getService(serviceId byte) (*Service, error) {
	if int(serviceId) >= len(te.services) {
		return nil, errors.New("Wrong serviceId: " + strconv.Itoa(int(serviceId)))
	}
	return &te.services[serviceId], nil
}
