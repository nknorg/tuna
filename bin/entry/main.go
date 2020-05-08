package main

import (
	"log"
	"net"
	"os"
	"strings"

	"github.com/jessevdk/go-flags"
	nkn "github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/nkn/common"
	"github.com/nknorg/tuna"
	"github.com/rdegges/go-ipify"
	"github.com/trueinsider/smux"
)

var opts struct {
	BeneficiaryAddr string `short:"b" long:"beneficiary-addr" description:"Beneficiary address (NKN wallet address to receive rewards)"`
	ConfigFile      string `short:"c" long:"config" description:"Config file path" default:"config.entry.json"`
	ServicesFile    string `short:"s" long:"services" description:"Services file path" default:"services.json"`
	WalletFile      string `short:"w" long:"wallet" description:"Wallet file path" default:"wallet.json"`
	PasswordFile    string `short:"p" long:"password-file" description:"Wallet password file path" default:"wallet.pswd"`
}

func main() {
	_, err := flags.Parse(&opts)
	if err != nil {
		if flagsErr, ok := err.(*flags.Error); ok && flagsErr.Type == flags.ErrHelp {
			os.Exit(0)
		}
		log.Fatalln(err)
	}

	config := &tuna.EntryConfiguration{
		SubscriptionPrefix:        tuna.DefaultSubscriptionPrefix,
		ReverseSubscriptionPrefix: tuna.DefaultSubscriptionPrefix,
	}
	err = tuna.ReadJson(opts.ConfigFile, config)
	if err != nil {
		log.Fatalln("Load config error:", err)
	}
	if len(opts.BeneficiaryAddr) > 0 {
		config.ReverseBeneficiaryAddr = opts.BeneficiaryAddr
	}

	if len(config.ReverseBeneficiaryAddr) > 0 {
		_, err = common.ToScriptHash(config.ReverseBeneficiaryAddr)
		if err != nil {
			log.Fatalln("Invalid beneficiary address:", err)
		}
	}

	account, err := tuna.LoadOrCreateAccount(opts.WalletFile, opts.PasswordFile)
	if err != nil {
		log.Fatalln("Load or create account error:", err)
	}

	wallet, err := nkn.NewWallet(&nkn.Account{account}, nil)
	if err != nil {
		log.Fatalln("Create wallet error:", err)
	}

	log.Println("Your NKN wallet address is:", wallet.Address())

	if config.Reverse {
		var serviceListenIP string
		if net.ParseIP(config.ReverseServiceListenIP) == nil {
			serviceListenIP = tuna.DefaultReverseServiceListenIP
		} else {
			serviceListenIP = config.ReverseServiceListenIP
		}

		ip, err := ipify.GetIp()
		if err != nil {
			log.Fatalln("Couldn't get IP:", err)
		}

		listener, err := net.ListenTCP(string(tuna.TCP), &net.TCPAddr{Port: config.ReverseTCP})
		if err != nil {
			log.Fatalln("Couldn't bind listener:", err)
		}

		udpConn, err := net.ListenUDP(string(tuna.UDP), &net.UDPAddr{Port: config.ReverseUDP})
		if err != nil {
			log.Fatalln("Couldn't bind listener:", err)
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
					tuna.Close(tcpConn)
					continue
				}

				te := tuna.NewTunaEntry(&tuna.Service{}, &tuna.ServiceInfo{ListenIP: serviceListenIP}, config, wallet)
				te.Session, _ = smux.Client(tcpConn, nil)
				stream, err := te.Session.OpenStream()
				if err != nil {
					log.Println("Couldn't open stream:", err)
					tuna.Close(tcpConn)
					continue
				}

				buf := make([]byte, 2048)
				n, err := stream.Read(buf)
				if err != nil {
					log.Println("Couldn't read service metadata:", err)
					tuna.Close(tcpConn)
					continue
				}
				metadataRaw := make([]byte, n)
				copy(metadataRaw, buf)

				te.SetMetadata(string(metadataRaw))

				te.SetServerTCPConn(tcpConn)

				metadata := te.GetMetadata()
				if metadata.UDPPort > 0 {
					ip, _, _ := net.SplitHostPort(tcpConn.RemoteAddr().String())
					udpAddr := net.UDPAddr{IP: net.ParseIP(ip), Port: metadata.UDPPort}

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
				go func() {
					te.StartReverse(stream)
					tuna.Close(tcpConn)
					te = nil
				}()
			}
		}()

		tuna.UpdateMetadata(
			tuna.DefaultReverseServiceName,
			255,
			[]int{},
			[]int{},
			ip,
			config.ReverseTCP,
			config.ReverseUDP,
			config.ReversePrice,
			config.ReverseBeneficiaryAddr,
			config.ReverseSubscriptionPrefix,
			config.ReverseSubscriptionDuration,
			config.ReverseSubscriptionFee,
			wallet,
		)
	} else {
		var services []tuna.Service
		err = tuna.ReadJson(opts.ServicesFile, &services)
		if err != nil {
			log.Fatalln("Load service file error:", err)
		}

	service:
		for serviceName, serviceInfo := range config.Services {
			serviceListenIP := net.ParseIP(serviceInfo.ListenIP)
			if serviceListenIP == nil {
				serviceInfo.ListenIP = tuna.DefaultServiceListenIP
			}

			for _, service := range services {
				if service.Name == serviceName {
					go tuna.NewTunaEntry(&service, &serviceInfo, config, wallet).Start()
					continue service
				}
			}
			log.Fatalln("Service", serviceName, "not found in service file")
		}
	}

	select {}
}
