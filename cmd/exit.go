package main

import (
	"log"
	"strings"

	"github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/tuna"
	"github.com/nknorg/tuna/util"
)

type ExitCommand struct {
	ConfigFile string `short:"c" long:"config" description:"Config file path" default:"config.exit.json"`
	Reverse    bool   `long:"reverse" description:"Reverse mode"`
}

var exitCommand ExitCommand

func (e *ExitCommand) Execute(args []string) error {
	config := &tuna.ExitConfiguration{}
	err := util.ReadJSON(e.ConfigFile, config)
	if err != nil {
		log.Fatalln("Load config file error:", err)
	}

	if len(opts.BeneficiaryAddr) > 0 {
		config.BeneficiaryAddr = opts.BeneficiaryAddr
	}

	if exitCommand.Reverse {
		config.Reverse = true
	}

	if len(config.BeneficiaryAddr) > 0 {
		err = nkn.VerifyWalletAddress(config.BeneficiaryAddr)
		if err != nil {
			log.Fatalln("Invalid beneficiary address:", err)
		}
	}

	account, err := tuna.LoadOrCreateAccount(opts.WalletFile, opts.PasswordFile)
	if err != nil {
		log.Fatalln("Load or create account error:", err)
	}

	var seedRPCServerAddr *nkn.StringArray
	if len(opts.SeedRPCServerAddr) > 0 {
		seedRPCServerAddr = nkn.NewStringArrayFromString(strings.ReplaceAll(opts.SeedRPCServerAddr, ",", " "))
	} else if len(config.SeedRPCServerAddr) > 0 {
		seedRPCServerAddr = nkn.NewStringArray(config.SeedRPCServerAddr...)
	} else if config.Reverse && len(config.MeasureStoragePath) > 0 {
		c, err := tuna.MergedExitConfig(config)
		if err == nil {
			rpcAddrs, err := tuna.GetFavoriteSeedRPCServer(config.MeasureStoragePath, c.SubscriptionPrefix+c.ReverseServiceName, 3000)
			if err == nil {
				seedRPCServerAddr = nkn.NewStringArray(append(rpcAddrs, nkn.DefaultSeedRPCServerAddr...)...)
			}
		}
	}

	config.SeedRPCServerAddr = seedRPCServerAddr.Elems()

	walletConfig := &nkn.WalletConfig{
		SeedRPCServerAddr: seedRPCServerAddr,
	}
	wallet, err := nkn.NewWallet(&nkn.Account{Account: account}, walletConfig)
	if err != nil {
		log.Fatalln("Create wallet error:", err)
	}

	log.Println("Your NKN wallet address is:", wallet.Address())

	var services []tuna.Service
	err = util.ReadJSON(opts.ServicesFile, &services)
	if err != nil {
		log.Fatalln("Load service file error:", err)
	}

	if config.Reverse {
		for _, service := range services {
			if _, ok := config.Services[service.Name]; ok {
				go func(service tuna.Service) {
					for {
						te, err := tuna.NewTunaExit([]tuna.Service{service}, wallet, config)
						if err != nil {
							log.Fatalln(err)
						}

						go func() {
							for range te.OnConnect.C {
								log.Printf("Service: %s, Address: %v:%v\n", service.Name, te.GetReverseIP(), te.GetReverseTCPPorts())
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
	} else {
		te, err := tuna.NewTunaExit(services, wallet, config)
		if err != nil {
			log.Fatalln(err)
		}

		err = te.Start()
		if err != nil {
			log.Fatalln(err)
		}

		defer te.Close()
	}

	select {}
}

func init() {
	parser.AddCommand("exit", "Tuna exit mode", "Start tuna in exit mode", &exitCommand)
}
