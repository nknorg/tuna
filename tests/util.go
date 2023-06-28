package tests

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/nkn/v2/vault"
	"github.com/nknorg/tuna"
	"github.com/nknorg/tuna/pb"
	"github.com/nknorg/tuna/types"
	"github.com/nknorg/tuna/util"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
)

type Server struct {
	host string
	port string
}

func NewServer(host, port string) *Server {
	return &Server{
		host: host,
		port: port,
	}
}

func (server *Server) RunTCPEchoServer() {
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%s", server.host, server.port))
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go func(conn net.Conn) {
			defer func() {
				conn.Close()
			}()
			io.Copy(conn, conn)
		}(conn)
	}
}

func (server *Server) RunUDPEchoServer() {
	ip := net.ParseIP(server.host)
	port, _ := strconv.Atoi(server.port)
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: ip, Port: port})
	if err != nil {
		log.Fatal(err)
	}
	buffer := make([]byte, 65536)
	for {
		n, addr, err := conn.ReadFromUDP(buffer)
		if err != nil {
			log.Fatal(err)
		}
		n, _, err = conn.WriteMsgUDP(buffer[:n], nil, addr)
		if err != nil {
			log.Fatal(err)
		}
	}
}

func runForwardEntry(seed, exitPubKey []byte, exitReady <-chan struct{}) error {
	seedRPCServerAddr := nkn.NewStringArray(nkn.DefaultSeedRPCServerAddr...)

	walletConfig := &nkn.WalletConfig{
		SeedRPCServerAddr: seedRPCServerAddr,
	}
	entryAccount, err := vault.NewAccountWithSeed(seed)
	if err != nil {
		return err
	}
	entryWallet, err := nkn.NewWallet(&nkn.Account{Account: entryAccount}, walletConfig)
	if err != nil {
		return err
	}
	entryConfig := new(tuna.EntryConfiguration)
	err = util.ReadJSON("config.forward.entry.json", entryConfig)
	if err != nil {
		return err
	}

	var entryServices []tuna.Service
	err = util.ReadJSON("services.entry.json", &entryServices)

	<-exitReady

	for serviceName, serviceInfo := range entryConfig.Services {
		for _, service := range entryServices {
			if service.Name == serviceName {
				go func(service tuna.Service, serviceInfo tuna.ServiceInfo) {
					if len(service.UDP) > 0 && service.UDPBufferSize == 0 {
						service.UDPBufferSize = tuna.DefaultUDPBufferSize
					}

					te, err := tuna.NewTunaEntry(service, serviceInfo, entryWallet, nil, entryConfig)
					if err != nil {
						log.Fatal(err)
					}
					node := types.Node{
						Delay:     0,
						Bandwidth: 0,
						Metadata: &pb.ServiceMetadata{
							Ip:              "127.0.0.1",
							TcpPort:         30010,
							UdpPort:         30011,
							ServiceId:       0,
							Price:           "0.0",
							BeneficiaryAddr: "",
						},
						Address:     hex.EncodeToString(exitPubKey),
						MetadataRaw: "Cg4xOTIuMTY4LjMxLjIwNhC66gEYu+oBOgUwLjAwMQ==",
					}
					te.SetRemoteNode(&node)
					err = te.Start(false)
					if err != nil {
						log.Fatal(err)
					}
				}(service, serviceInfo)
			}
		}
	}
	select {}
}

func runForwardExit(seed []byte, ready chan<- struct{}) error {
	exitAccount, err := vault.NewAccountWithSeed(seed)
	if err != nil {
		return err
	}
	seedRPCServerAddr := nkn.NewStringArray(nkn.DefaultSeedRPCServerAddr...)
	walletConfig := &nkn.WalletConfig{
		SeedRPCServerAddr: seedRPCServerAddr,
	}
	exitWallet, err := nkn.NewWallet(&nkn.Account{Account: exitAccount}, walletConfig)
	if err != nil {
		return err
	}
	exitConfig := &tuna.ExitConfiguration{}
	err = util.ReadJSON("config.forward.exit.json", exitConfig)
	if err != nil {
		log.Fatal("Load exitConfig file error:", err)
		return err
	}
	var exitServices []tuna.Service
	err = util.ReadJSON("services.reverse.exit.json", &exitServices)
	if err != nil {
		log.Fatal("Load service file error:", err)
		return err
	}
	te, err := tuna.NewTunaExit(exitServices, exitWallet, nil, exitConfig)
	if err != nil {
		log.Fatal(err)
		return err
	}
	err = te.Start()
	log.Println("exit server start...")
	if err != nil {
		log.Fatal(err)
		return err
	}
	defer te.Close()

	close(ready)

	select {}
}

func runReverseEntry(seed []byte, ready chan<- struct{}) error {
	entryAccount, err := vault.NewAccountWithSeed(seed)
	if err != nil {
		return err
	}
	seedRPCServerAddr := nkn.NewStringArray(nkn.DefaultSeedRPCServerAddr...)

	walletConfig := &nkn.WalletConfig{
		SeedRPCServerAddr: seedRPCServerAddr,
	}
	entryWallet, err := nkn.NewWallet(&nkn.Account{Account: entryAccount}, walletConfig)
	if err != nil {
		return err
	}
	entryConfig := new(tuna.EntryConfiguration)
	err = util.ReadJSON("config.reverse.entry.json", entryConfig)
	if err != nil {
		return err
	}
	entryConfig.Reverse = true
	err = tuna.StartReverse(entryConfig, entryWallet)
	if err != nil {
		return err
	}
	ready <- struct{}{}

	select {}
}

func runReverseExit(tcpPort, udpPort *[]int, seed, entryPubKey []byte) error {
	exitAccount, err := vault.NewAccountWithSeed(seed)
	if err != nil {
		return err
	}
	seedRPCServerAddr := nkn.NewStringArray(nkn.DefaultSeedRPCServerAddr...)
	walletConfig := &nkn.WalletConfig{
		SeedRPCServerAddr: seedRPCServerAddr,
	}
	exitWallet, err := nkn.NewWallet(&nkn.Account{Account: exitAccount}, walletConfig)
	if err != nil {
		return err
	}
	exitConfig := &tuna.ExitConfiguration{}
	err = util.ReadJSON("config.reverse.exit.json", exitConfig)
	if err != nil {
		log.Fatal("Load exitConfig file error:", err)
		return err
	}
	exitConfig.Reverse = true
	var exitServices []tuna.Service
	err = util.ReadJSON("services.reverse.exit.json", &exitServices)
	if err != nil {
		log.Fatal("Load service file error:", err)
		return err
	}
	node := types.Node{
		Delay:     0,
		Bandwidth: 0,
		Metadata: &pb.ServiceMetadata{
			Ip:              "127.0.0.1",
			TcpPort:         30020,
			UdpPort:         30021,
			ServiceId:       0,
			Price:           "0.000",
			BeneficiaryAddr: "",
		},
		Address:     hex.EncodeToString(entryPubKey),
		MetadataRaw: "CgkxMjcuMC4wLjEQxOoBGMXqAToFMC4wMDE=",
	}
	var lock sync.Mutex
	var wg sync.WaitGroup
	for i, service := range exitServices {
		if _, ok := exitConfig.Services[service.Name]; ok {
			go func(service tuna.Service, i int) {
				for {
					wg.Add(1)
					te, err := tuna.NewTunaExit([]tuna.Service{service}, exitWallet, nil, exitConfig)
					if err != nil {
						log.Fatalln(err)
					}
					te.SetRemoteNode(&node)

					i := i
					go func(i int) {
						for range te.OnConnect.C {
							lock.Lock()
							log.Printf("Service: %s, Type: TCP, Address: %v:%v\n", service.Name, te.GetReverseIP(), te.GetReverseTCPPorts())
							port := int(te.GetReverseTCPPorts()[0])
							(*tcpPort)[i] = port
							if len(service.UDP) > 0 {
								log.Printf("Service: %s, Type: UDP, Address: %v:%v\n", service.Name, te.GetReverseIP(), te.GetReverseUDPPorts())
								port := int(te.GetReverseUDPPorts()[0])
								(*udpPort)[i] = port
							}
							wg.Done()
							lock.Unlock()
						}
					}(i)

					err = te.StartReverse(false)
					if err != nil {
						log.Println(err)
					}
				}
			}(service, i)
		}
	}
	wg.Wait()

	select {}
}

func testTCP(conn net.Conn) error {
	send := make([]byte, 4096)
	receive := make([]byte, 4096)

	for i := 0; i < 10; i++ {
		rand.Read(send)
		_, err := conn.Write(send)
		if err != nil {
			return err
		}
		_, err = conn.Read(receive)
		if err != nil {
			return err
		}
		if !bytes.Equal(send, receive) {
			log.Println("got:", hex.EncodeToString(receive))
			log.Println("want:", hex.EncodeToString(send))
			return errors.New("bytes not equal")
		}
	}
	return nil
}

func testUDP(conn *net.UDPConn) error {
	send := make([]byte, 4096)
	receive := make([]byte, 4096)
	for i := 0; i < 100; i++ {
		rand.Read(send)
		_, err := conn.Write(send)
		if err != nil {
			return err
		}
		_, err = conn.Read(receive)
		if err != nil {
			return err
		}
		if !bytes.Equal(send, receive) {
			log.Println("got:", hex.EncodeToString(receive))
			log.Println("want:", hex.EncodeToString(send))
			return errors.New("bytes not equal")
		}
	}
	return nil
}
