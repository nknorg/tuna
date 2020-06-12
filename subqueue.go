package tuna

import (
	"log"
	"time"

	"github.com/nknorg/nkn-sdk-go"
)

const (
	subQueueLen = 1024
	maxRetry    = 3
)

type subscribeData struct {
	wallet     *nkn.Wallet
	identifier string
	topic      string
	duration   int
	meta       string
	config     *nkn.TransactionConfig
}

var subQueue chan *subscribeData

func init() {
	subQueue = make(chan *subscribeData, subQueueLen)
	go func() {
		for subData := range subQueue {
			for i := 0; i < maxRetry; i++ {
				txnHash, err := subData.wallet.Subscribe(subData.identifier, subData.topic, subData.duration, subData.meta, subData.config)
				if err != nil {
					log.Println("subscribe to topic", subData.topic, "error:", err)
					time.Sleep(time.Second)
					continue
				}
				log.Println("Subscribed to topic", subData.topic, "success:", txnHash)
				break
			}
			time.Sleep(time.Second)
		}
	}()
}

func addToSubscribeQueue(wallet *nkn.Wallet, identifier string, topic string, duration int, meta string, config *nkn.TransactionConfig) {
	subData := &subscribeData{
		wallet:     wallet,
		identifier: identifier,
		topic:      topic,
		duration:   duration,
		meta:       meta,
		config:     config,
	}
	select {
	case subQueue <- subData:
	default:
		log.Println("Subscribe queue full, discard request.")
	}
}
