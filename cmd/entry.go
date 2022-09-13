package main

import (
	"log"
	"strings"

	"github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/tuna"
	"github.com/nknorg/tuna/util"
)

type EntryCommand struct {
	ConfigFile string `short:"c" long:"config" description:"Config file path" default:"config.entry.json"`
	Reverse    bool   `long:"reverse" description:"Reverse mode"`
}

var entryCommand EntryCommand

func (e *EntryCommand) Execute(args []string) error {
	config := &tuna.EntryConfiguration{}
	err := util.ReadJSON(e.ConfigFile, config)
	if err != nil {
		log.Fatalln("Load config error:", err)
	}

	if len(opts.BeneficiaryAddr) > 0 {
		config.ReverseBeneficiaryAddr = opts.BeneficiaryAddr
	}

	if entryCommand.Reverse {
		config.Reverse = true
	}

	if len(config.ReverseBeneficiaryAddr) > 0 {
		err = nkn.VerifyWalletAddress(config.ReverseBeneficiaryAddr)
		if err != nil {
			log.Fatalln("Invalid beneficiary address:", err)
		}
	}

	account, err := tuna.LoadOrCreateAccount(opts.WalletFile, opts.PasswordFile)
	if err != nil {
		log.Fatalln("Load or create account error:", err)
	}

	seedRPCServerAddr := nkn.NewStringArray(nkn.DefaultSeedRPCServerAddr...)
	if len(opts.SeedRPCServerAddr) > 0 {
		seedRPCServerAddr = nkn.NewStringArrayFromString(strings.ReplaceAll(opts.SeedRPCServerAddr, ",", " "))
	} else if len(config.SeedRPCServerAddr) > 0 {
		seedRPCServerAddr = nkn.NewStringArray(config.SeedRPCServerAddr...)
	} else if !config.Reverse && len(config.MeasureStoragePath) > 0 {
		c, err := tuna.MergedEntryConfig(config)
		if err == nil {
			for serviceName := range c.Services {
				rpcAddrs, err := tuna.GetFavoriteSeedRPCServer(c.MeasureStoragePath, c.SubscriptionPrefix+serviceName, 3000, c.HttpDialContext)
				if err == nil {
					seedRPCServerAddr = nkn.NewStringArray(append(rpcAddrs, nkn.DefaultSeedRPCServerAddr...)...)
					break
				}
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

	if config.Reverse {
		err = tuna.StartReverse(config, wallet)
		if err != nil {
			log.Fatalln(err)
		}
	} else {
		var services []tuna.Service
		err = util.ReadJSON(opts.ServicesFile, &services)
		if err != nil {
			log.Fatalln("Load service file error:", err)
		}

	service:
		for serviceName, serviceInfo := range config.Services {
			for _, service := range services {
				if service.Name == serviceName {
					go func(service tuna.Service, serviceInfo tuna.ServiceInfo) {
						for {
							te, err := tuna.NewTunaEntry(service, serviceInfo, wallet, nil, config)
							if err != nil {
								log.Fatalln(err)
							}

							err = te.Start(false)
							if err != nil {
								log.Println(err)
							}
						}
					}(service, serviceInfo)
					continue service
				}
			}
			log.Fatalln("Service", serviceName, "not found in service file")
		}
	}

	select {}
}

func init() {
	parser.AddCommand("entry", "Tuna entry mode", "Start tuna in entry mode", &entryCommand)
}
