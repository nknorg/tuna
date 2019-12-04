package main

import (
	"fmt"
	"log"
	"os"

	nknSdk "github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/tuna"
)

func main() {
	nknSdk.Init()

	config := &tuna.ExitConfiguration{SubscriptionPrefix: tuna.DefaultSubscriptionPrefix}
	configFile := "config.json"
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		configFile = "config.exit.json"
	}
	err := tuna.ReadJson(configFile, config)
	if err != nil {
		log.Panicln("Load config file error:", err)
	}

	account, err := tuna.LoadOrCreateAccount("wallet.json", "wallet.pswd")
	if err != nil {
		log.Panicln("Load or create account error:", err)
	}

	wallet := nknSdk.NewWalletSDK(account)

	var services []tuna.Service
	err = tuna.ReadJson("services.json", &services)
	if err != nil {
		log.Panicln("Load service file error:", err)
	}

	if config.Reverse {
		for serviceName := range config.Services {
			e := tuna.NewTunaExit(config, services, wallet)
			e.OnEntryConnected(func() {
				fmt.Printf("Service: %s, Address: %v:%v\n", serviceName, e.GetReverseIP(), e.GetReverseTCPPorts())
			})
			e.StartReverse(serviceName)
		}
	} else {
		tuna.NewTunaExit(config, services, wallet).Start()
	}

	select {}
}
