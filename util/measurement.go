package util

import (
	"net"
	"time"
)

func DelayMeasurement(network, address string, timeout time.Duration) (time.Duration, error) {
	now := time.Now()
	d := net.Dialer{Timeout: timeout}
	conn, err := d.Dial(network, address)
	delay := time.Since(now)
	if err != nil {
		return delay, err
	}

	conn.Close()

	return delay, nil
}
