package util

import (
	"context"
	"math/rand"
	"net"
	"time"
)

const (
	readBufferSize  = 1024
	writeBufferSize = 1024
)

func DelayMeasurement(network, address string, timeout time.Duration, dialContext func(ctx context.Context, network, addr string) (net.Conn, error)) (time.Duration, error) {
	return DelayMeasurementContext(context.Background(), network, address, timeout, dialContext)
}

func DelayMeasurementContext(ctx context.Context, network, address string, timeout time.Duration, dialContext func(ctx context.Context, network, addr string) (net.Conn, error)) (time.Duration, error) {
	now := time.Now()

	var conn net.Conn
	var err error
	if dialContext != nil {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		conn, err = dialContext(ctx, network, address)
	} else {
		conn, err = net.DialTimeout(network, address, timeout)
	}
	delay := time.Since(now)
	if err != nil {
		return delay, err
	}

	conn.Close()

	return delay, nil
}

func BandwidthMeasurementClient(conn net.Conn, bytesDownlink int, timeout time.Duration) (float32, float32, error) {
	return BandwidthMeasurementClientContext(context.Background(), conn, bytesDownlink, timeout)
}

func BandwidthMeasurementClientContext(ctx context.Context, conn net.Conn, bytesDownlink int, timeout time.Duration) (float32, float32, error) {
	timeStart := time.Now()
	var timeToFirstByte time.Duration

	if timeout > 0 {
		err := conn.SetReadDeadline(timeStart.Add(timeout))
		if err != nil {
			return 0, 0, err
		}
	}

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			conn.SetDeadline(time.Now())
		case <-done:
		}
	}()

	b := make([]byte, readBufferSize)
	for bytesRead := 0; bytesRead < bytesDownlink; {
		n := bytesDownlink - bytesRead
		if n > len(b) {
			n = len(b)
		}
		m, err := conn.Read(b[:n])
		if err != nil {
			return 0, 0, err
		}
		if bytesRead == 0 {
			timeToFirstByte = time.Since(timeStart)
		}
		bytesRead += m
	}

	close(done)

	timeToLastByte := time.Since(timeStart)
	bps := float32(bytesDownlink) / float32(timeToLastByte) * float32(time.Second)
	bpsRead := float32(bytesDownlink) / float32(timeToLastByte-timeToFirstByte) * float32(time.Second)

	return bps, bpsRead, nil
}

func BandwidthMeasurementServer(conn net.Conn, bytesDownlink int, timeout time.Duration) error {
	return BandwidthMeasurementServerContext(context.Background(), conn, bytesDownlink, timeout)
}

func BandwidthMeasurementServerContext(ctx context.Context, conn net.Conn, bytesDownlink int, timeout time.Duration) error {
	if timeout > 0 {
		err := conn.SetWriteDeadline(time.Now().Add(timeout))
		if err != nil {
			return err
		}
	}

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			conn.SetDeadline(time.Now())
		case <-done:
		}
	}()

	b := make([]byte, writeBufferSize)
	for bytesWritten := 0; bytesWritten < bytesDownlink; {
		n := bytesDownlink - bytesWritten
		if n > len(b) {
			n = len(b)
		}
		_, err := rand.Read(b[:n])
		if err != nil {
			return err
		}
		m, err := conn.Write(b[:n])
		if err != nil {
			return err
		}
		bytesWritten += m
	}

	close(done)

	return nil
}
