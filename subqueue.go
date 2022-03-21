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
	client        *nkn.MultiClient
	identifier    string
	topic         string
	duration      int
	meta          string
	config        *nkn.TransactionConfig
	replaceTxPool bool
}

var subQueue chan *subscribeData

func init() {
	subQueue = make(chan *subscribeData, subQueueLen)
	go func() {
		for subData := range subQueue {
			for i := 0; i < maxRetry; i++ {
				if subData.replaceTxPool {
					nonce, err := subData.client.GetNonce(false)
					if err != nil {
						log.Println("get nonce error:", err)
						time.Sleep(time.Second)
						continue
					}
					subData.config.Nonce = nonce
				}

				txnHash, err := subData.client.Subscribe(subData.identifier, subData.topic, subData.duration, subData.meta, subData.config)
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

func addToSubscribeQueue(client *nkn.MultiClient, identifier string, topic string, duration int, meta string, config *nkn.TransactionConfig, replaceTxPool bool) {
	subData := &subscribeData{
		client:        client,
		identifier:    identifier,
		topic:         topic,
		duration:      duration,
		meta:          meta,
		config:        config,
		replaceTxPool: replaceTxPool,
	}
	select {
	case subQueue <- subData:
	default:
		log.Println("Subscribe queue full, discard request.")
	}
}
