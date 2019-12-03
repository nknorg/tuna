package main

import (
	"fmt"
	"log"

	. "github.com/nknorg/nkn-sdk-go"
	"github.com/nknorg/tuna"
	. "github.com/nknorg/tuna/exit"
)

func main() {
	config := Configuration{SubscriptionPrefix: tuna.DefaultSubscriptionPrefix}
	tuna.ReadJson("config.json", &config)

	Init()

	account, err := tuna.LoadOrCreateAccount("wallet.json", "wallet.pswd")
	if err != nil {
		log.Panicln("Load or create account error:", err)
	}

	wallet := NewWalletSDK(account)

	var services []Service
	tuna.ReadJson("services.json", &services)

	if config.Reverse {
		for serviceName := range config.Services {
			e := NewTunaExit(config, services, wallet)
			e.OnEntryConnected(func() {
				fmt.Printf("Service: %s, Address: %v:%v\n", serviceName, e.GetReverseIP(), e.GetReverseTCPPorts())
			})
			e.StartReverse(serviceName)
		}
	} else {
		NewTunaExit(config, services, wallet).Start()
	}

	select {}
}
