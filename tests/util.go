package tests

import (
	"bytes"
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
	"math/rand"
	"net"
	"strconv"
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

func runForwardEntry(seed, exitPubKey []byte) error {
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

	for serviceName, serviceInfo := range entryConfig.Services {
		for _, service := range entryServices {
			if service.Name == serviceName {
				go func(service tuna.Service, serviceInfo tuna.ServiceInfo) {
					if len(service.UDP) > 0 && service.UDPBufferSize == 0 {
						service.UDPBufferSize = tuna.DefaultUDPBufferSize
					}
					for {
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
								Price:           "0.001",
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
					}
				}(service, serviceInfo)
			}
		}
	}
	select {}
}

func runForwardExit(seed []byte) error {
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

	select {}
}

func runReverseEntry(seed []byte) error {
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

	select {}
}

func runReverseExit(tcpPort, udpPort *int, seed, entryPubKey []byte) error {
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
			Price:           "0.001",
			BeneficiaryAddr: "",
		},
		Address:     hex.EncodeToString(entryPubKey),
		MetadataRaw: "CgkxMjcuMC4wLjEQxOoBGMXqAToFMC4wMDE=",
	}
	for _, service := range exitServices {
		if _, ok := exitConfig.Services[service.Name]; ok {
			go func(service tuna.Service) {
				for {
					te, err := tuna.NewTunaExit([]tuna.Service{service}, exitWallet, nil, exitConfig)
					if err != nil {
						log.Fatalln(err)
					}
					te.SetRemoteNode(&node)

					go func() {
						for range te.OnConnect.C {
							log.Printf("Service: %s, Type: TCP, Address: %v:%v\n", service.Name, te.GetReverseIP(), te.GetReverseTCPPorts())
							*tcpPort = int(te.GetReverseTCPPorts()[0])
							if len(service.UDP) > 0 {
								log.Printf("Service: %s, Type: UDP, Address: %v:%v\n", service.Name, te.GetReverseIP(), te.GetReverseUDPPorts())
								*udpPort = int(te.GetReverseUDPPorts()[0])
							}
						}
					}()

					err = te.StartReverse(false)
					if err != nil {
						log.Println(err)
					}
				}
			}(service)
		}
	}

	select {}
}

func testTCP(conn net.Conn) error {
	send := make([]byte, 4096)
	receive := make([]byte, 4096)

	for i := 0; i < 10; i++ {
		rand.Read(send)
		conn.Write(send)
		conn.Read(receive)
		if !bytes.Equal(send, receive) {
			return errors.New("bytes not equal")
		}
	}
	return nil
}

func testUDP(conn *net.UDPConn) error {
	send := make([]byte, 4096)
	receive := make([]byte, 4096)
	for i := 0; i < 10; i++ {
		rand.Read(send)
		conn.Write(send)
		conn.Read(receive)
		if !bytes.Equal(send, receive) {
			return errors.New("bytes not equal")
		}
	}
	return nil
}
