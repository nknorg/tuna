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
	server2 := NewServer("0.0.0.0", "54322")
	go server.RunUDPEchoServer()
	go server.RunTCPEchoServer()
	go server2.RunUDPEchoServer()
	go server2.RunTCPEchoServer()
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

	tcpConn2, err := net.Dial("tcp", "127.0.0.1:12346")
	if err != nil {
		t.Fatal("dial err:", err)
	}
	err = testTCP(tcpConn2)
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

	udpConn2, err := net.DialUDP("udp", nil, &net.UDPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: 12346,
	})
	err = testUDP(udpConn2)
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
			tcpPort := make([]int, 2)
			udpPort := make([]int, 2)
			go runReverseExit(&tcpPort, &udpPort, exitSeed, entryPubKey)
			time.Sleep(10 * time.Second)
			for j := 0; j < len(tcpPort); j++ {
				tcpAddr := "127.0.0.1:" + strconv.Itoa(tcpPort[j])
				tcpConn, err := net.Dial("tcp", tcpAddr)
				if err != nil {
					t.Fatal("dial err:", err)
				}
				err = testTCP(tcpConn)
				if err != nil {
					t.Fatal(err)
				}
			}

			var wg2 sync.WaitGroup
			for i := 0; i < clientNum; i++ {
				wg2.Add(1)
				go func() {
					defer wg2.Done()
					for j := 0; j < len(udpPort); j++ {
						udpConn, err := net.DialUDP("udp", nil, &net.UDPAddr{
							IP:   net.ParseIP("127.0.0.1"),
							Port: udpPort[j],
						})
						if err != nil {
							t.Fatal(err)
						}
						udpConn.SetDeadline(time.Now().Add(10 * time.Second))
						err = testUDP(udpConn)
						if err != nil {
							t.Fatal(err)
						}
					}
				}()
			}
			wg2.Wait()
		}()
	}
	wg.Wait()
}
