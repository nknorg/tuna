package tests

import (
	"log"
	"sync"
	"testing"
	"time"

	"github.com/nknorg/tuna/util"
)

var ips = []string{
	"example.com:80",
}

func TestDelayMeasurement(t *testing.T) {
	var wg sync.WaitGroup
	for _, ip := range ips {
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			delay, err := util.DelayMeasurement("tcp", ip, time.Second*2, nil)
			if err != nil {
				log.Println("timeout, ip:", ip)
				return
			}
			log.Println("succeeded, ip:", ip, ", delay: ", delay)
		}(ip)
	}
	wg.Wait()
	return
}
