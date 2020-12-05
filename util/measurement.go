package util

import (
	"net"
	"time"
)

func DelayMeasurement(network, address string, timeout time.Duration) (time.Duration, error) {
	now := time.Now()
	d := net.Dialer{Timeout: timeout}
	_, err := d.Dial(network, address)
	delay := time.Since(now)
	if err != nil {
		return delay, err
	}

	return delay, nil
}
