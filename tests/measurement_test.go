package tests

import (
	"context"
	"github.com/nknorg/tuna"
	"github.com/nknorg/tuna/util"
	"log"
	"testing"
	"time"
)

var ips = []string{
	"34.201.72.169:30010",
	"167.71.88.124:30010",
	"3.95.191.171:30010",
	"54.202.225.44:30010",
}

func TestDelayMeasurement(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	for _, ip := range ips {
		go func(ip string) {
			delay, err := util.DelayMeasurement(ctx, string(tuna.TCP), ip, time.Second*2)
			if err != nil {
				log.Println("timeout, ip:", ip)
				return
			}
			log.Println("succeeded, ip:", ip, ", delay: ", delay)
			cancel()
		}(ip)
	}

	select {
	case <-ctx.Done():
		return
	case <-time.After(10 * time.Second):
		return
	}
}
