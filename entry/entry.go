package entry

import (
	"errors"
	"log"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	. "github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/nkn/common"
	"github.com/nknorg/tuna"
	cache "github.com/patrickmn/go-cache"
	"github.com/trueinsider/smux"
)

const nanoPayUpdateInterval = time.Minute

type ServiceInfo struct {
	MaxPrice string `json:"maxPrice"`
}

type Configuration struct {
	DialTimeout          uint16                 `json:"DialTimeout"`
	UDPTimeout           uint16                 `json:"UDPTimeout"`
	Services             map[string]ServiceInfo `json:"Services"`
	NanoPayFee           string                 `json:"NanoPayFee"`
	Reverse              bool                   `json:"Reverse"`
	ReverseTCP           int                    `json:"ReverseTCP"`
	ReverseUDP           int                    `json:"ReverseUDP"`
	ReversePrice         string                 `json:"ReversePrice"`
	ReverseClaimInterval uint32                 `json:"ReverseClaimInterval"`
	SubscriptionPrefix   string                 `json:"SubscriptionPrefix"`
	SubscriptionDuration uint32                 `json:"SubscriptionDuration"`
	SubscriptionFee      string                 `json:"SubscriptionFee"`
}

type TunaEntry struct {
	*tuna.Common
	config       Configuration
	tcpListeners map[byte]*net.TCPListener
	serviceConn  map[byte]*net.UDPConn
	clientAddr   *cache.Cache
	Session      *smux.Session
	closeChan    chan struct{}
	bytesIn      uint64
	bytesPaid    uint64
}

func NewTunaEntry(serviceName string, maxPrice common.Fixed64, reverse bool, config Configuration, wallet *WalletSDK) *TunaEntry {
	te := &TunaEntry{
		Common: &tuna.Common{
			ServiceName:        serviceName,
			MaxPrice:           maxPrice,
			Wallet:             wallet,
			DialTimeout:        config.DialTimeout,
			SubscriptionPrefix: config.SubscriptionPrefix,
			Reverse:            reverse,
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

		_, err := te.listenTCP(te.Metadata.ServiceTCP)
		if err != nil {
			te.close()
			return
		}
		_, err = te.listenUDP(te.Metadata.ServiceUDP)
		if err != nil {
			te.close()
			return
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
					te.close()
					return
				}
			}
		}()

		go func() {
			var np *NanoPay
			for {
				time.Sleep(nanoPayUpdateInterval)
				bytesIn := atomic.LoadUint64(&te.bytesIn)
				if bytesIn == te.bytesPaid {
					continue
				}
				if np == nil || np.Address() != te.PaymentReceiver {
					var err error
					np, err = te.Wallet.NewNanoPay(te.PaymentReceiver, te.config.NanoPayFee)
					if err != nil {
						continue
					}
				}
				delta := te.Price * common.Fixed64(bytesIn-te.bytesPaid) / 1048576
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
					te.bytesPaid = bytesIn
				}
				stream.Close()
			}
		}()

		break
	}

	<-te.closeChan
}

func (te *TunaEntry) StartReverse(stream *smux.Stream) {
	tcpPorts, err := te.listenTCP(te.Metadata.ServiceTCP)
	if err != nil {
		te.close()
		return
	}
	udpPorts, err := te.listenUDP(te.Metadata.ServiceUDP)
	if err != nil {
		te.close()
		return
	}

	serviceMetadata := tuna.CreateRawMetadata(
		0,
		tcpPorts,
		udpPorts,
		"",
		-1,
		-1,
		"",
	)
	_, err = stream.Write(serviceMetadata)
	if err != nil {
		log.Println("Couldn't send metadata to reverse exit:", err)
		te.close()
		return
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
				te.close()
				return
			}
		}
	}()

	<-te.closeChan
}

func (te *TunaEntry) close() {
	for _, listener := range te.tcpListeners {
		tuna.Close(listener)
	}
	for _, conn := range te.serviceConn {
		tuna.Close(conn)
	}
	te.closeChan <- struct{}{}
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
		te.Session, _ = smux.Client(conn, nil)
	}

	return te.Session, nil
}

func (te *TunaEntry) openStream(portId byte, force bool) (*smux.Stream, error) {
	session, err := te.getSession(force)
	if err != nil {
		return nil, err
	}
	serviceId := te.Metadata.ServiceId
	stream, err := session.OpenStream(serviceId, portId)
	if err != nil {
		return te.openStream(portId, true)
	}
	return stream, err
}

func (te *TunaEntry) listenTCP(ports []int) ([]int, error) {
	assignedPorts := make([]int, 0)
	for i, _port := range ports {
		listener, err := net.ListenTCP(string(tuna.TCP), &net.TCPAddr{Port: _port})
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
					tuna.Close(conn)
					if strings.Contains(err.Error(), "use of closed network connection") {
						te.close()
						return
					}
					continue
				}

				stream, err := te.openStream(portId, false)
				if err != nil {
					log.Println("Couldn't open stream:", err)
					tuna.Close(conn)
					continue
				}

				go tuna.Pipe(stream, conn, nil)
				go tuna.Pipe(conn, stream, &te.bytesIn)
			}
		}()
	}

	return assignedPorts, nil
}

func (te *TunaEntry) listenUDP(ports []int) ([]int, error) {
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
			connId := tuna.GetConnIdString(data)

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
		localConn, err := net.ListenUDP(string(tuna.UDP), &net.UDPAddr{Port: _port})
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
				serviceId := te.Metadata.ServiceId
				serverWriteChan <- append([]byte{connId[0], connId[1], serviceId, portId}, localBuffer[:n]...)
			}
		}()
	}

	return assignedPorts, nil
}

func GetConnIdData(port int) [2]byte {
	return *(*[2]byte)(unsafe.Pointer(&port))
}
