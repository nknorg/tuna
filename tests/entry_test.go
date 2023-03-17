package tests

import (
	"github.com/nknorg/nkn/v2/crypto"
	"net"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	server := NewServer("0.0.0.0", "54321")
	go server.RunUDPEchoServer()
	go server.RunTCPEchoServer()
	os.Exit(m.Run())
}

func TestForwardProxy(t *testing.T) {
	exitPubKey, exitPrivKey, _ := crypto.GenKeyPair()
	exitSeed := crypto.GetSeedFromPrivateKey(exitPrivKey)

	_, entryPrivKey, _ := crypto.GenKeyPair()
	entrySeed := crypto.GetSeedFromPrivateKey(entryPrivKey)

	go runForwardExit(exitSeed)
	go runForwardEntry(entrySeed, exitPubKey)
	time.Sleep(10 * time.Second)
	tcpConn, err := net.Dial("tcp", "127.0.0.1:12345")
	if err != nil {
		t.Fatal("dial err:", err)
	}
	err = testTCP(tcpConn)
	if err != nil {
		t.Fatal(err)
	}
	udpConn, err := net.DialUDP("udp", nil, &net.UDPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: 12345,
	})
	err = testUDP(udpConn)
	if err != nil {
		t.Fatal(err)
	}
}

func TestMultipleClientReverseProxy(t *testing.T) {
	entryPubKey, entryPrivKey, _ := crypto.GenKeyPair()
	entrySeed := crypto.GetSeedFromPrivateKey(entryPrivKey)

	go runReverseEntry(entrySeed)
	time.Sleep(10 * time.Second)
	exitNum := 4
	clientNum := 4
	var wg sync.WaitGroup
	for i := 0; i < exitNum; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, exitPrivKey, _ := crypto.GenKeyPair()
			exitSeed := crypto.GetSeedFromPrivateKey(exitPrivKey)
			tcpPort := 0
			udpPort := 0
			go runReverseExit(&tcpPort, &udpPort, exitSeed, entryPubKey)
			time.Sleep(10 * time.Second)
			tcpAddr := "127.0.0.1:" + strconv.Itoa(tcpPort)
			tcpConn, err := net.Dial("tcp", tcpAddr)
			if err != nil {
				t.Fatal("dial err:", err)
			}
			err = testTCP(tcpConn)
			if err != nil {
				t.Fatal(err)
			}

			var wg2 sync.WaitGroup
			for i := 0; i < clientNum; i++ {
				wg2.Add(1)
				go func() {
					defer wg2.Done()
					udpConn, err := net.DialUDP("udp", nil, &net.UDPAddr{
						IP:   net.ParseIP("127.0.0.1"),
						Port: udpPort,
					})
					if err != nil {
						t.Fatal(err)
					}
					err = testUDP(udpConn)
					if err != nil {
						t.Fatal(err)
					}
				}()
			}
			wg2.Wait()
		}()
	}
	wg.Wait()
}
