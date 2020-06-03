package main

import (
	"log"
	"net"

	"github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/tuna"
)

type EntryCommand struct {
	ConfigFile string `short:"c" long:"config" description:"Config file path" default:"config.entry.json"`
}

var entryCommand EntryCommand

func (e *EntryCommand) Execute(args []string) error {
	config := &tuna.EntryConfiguration{
		SubscriptionPrefix:        tuna.DefaultSubscriptionPrefix,
		ReverseSubscriptionPrefix: tuna.DefaultSubscriptionPrefix,
	}
	err := tuna.ReadJSON(e.ConfigFile, config)
	if err != nil {
		log.Fatalln("Load config error:", err)
	}
	if len(opts.BeneficiaryAddr) > 0 {
		config.ReverseBeneficiaryAddr = opts.BeneficiaryAddr
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

	wallet, err := nkn.NewWallet(&nkn.Account{account}, nil)
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
		err = tuna.ReadJSON(opts.ServicesFile, &services)
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
					te, err := tuna.NewTunaEntry(&service, &serviceInfo, config, wallet)
					if err != nil {
						log.Fatalln(err)
					}
					go func() {
						defer te.Close()
						err := te.Start(true)
						if err != nil {
							log.Fatalln(err)
						}
					}()
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
