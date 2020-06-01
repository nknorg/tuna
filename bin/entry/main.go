package main

import (
	"log"
	"net"
	"os"

	"github.com/jessevdk/go-flags"
	"github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/nkn/common"
	"github.com/nknorg/tuna"
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
	err = tuna.ReadJSON(opts.ConfigFile, config)
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
