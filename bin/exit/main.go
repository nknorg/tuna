package main

import (
	"fmt"
	"log"
	"os"

	"github.com/jessevdk/go-flags"
	"github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/nkn/common"
	"github.com/nknorg/tuna"
)

var opts struct {
	BeneficiaryAddr string `short:"b" long:"beneficiary-addr" description:"Beneficiary address (NKN wallet address to receive rewards)"`
	ConfigFile      string `short:"c" long:"config" description:"Config file path" default:"config.exit.json"`
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

	config := &tuna.ExitConfiguration{SubscriptionPrefix: tuna.DefaultSubscriptionPrefix}
	err = tuna.ReadJson(opts.ConfigFile, config)
	if err != nil {
		log.Fatalln("Load config file error:", err)
	}
	if len(opts.BeneficiaryAddr) > 0 {
		config.BeneficiaryAddr = opts.BeneficiaryAddr
	}

	if len(config.BeneficiaryAddr) > 0 {
		_, err = common.ToScriptHash(config.BeneficiaryAddr)
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

	var services []tuna.Service
	err = tuna.ReadJson(opts.ServicesFile, &services)
	if err != nil {
		log.Fatalln("Load service file error:", err)
	}

	if config.Reverse {
		for serviceName, serviceInfo := range config.Services {
			reverseEntryToExitPrice, reverseExitToEntryPrice, err := tuna.ParsePrice(serviceInfo.Price)
			e := tuna.NewTunaExit(config, services, reverseEntryToExitPrice, reverseExitToEntryPrice, wallet)
			e.OnEntryConnected(func() {
				fmt.Printf("Service: %s, Address: %v:%v\n", serviceName, e.GetReverseIP(), e.GetReverseTCPPorts())
			})
			err = e.StartReverse(serviceName)
			if err != nil {
				log.Fatalln(err)
			}
		}
	} else {
		err = tuna.NewTunaExit(config, services, common.Fixed64(0), common.Fixed64(0), wallet).Start()
		if err != nil {
			log.Fatalln(err)
		}
	}

	select {}
}
