package tuna

import (
	"encoding/json"
	"io"
	"io/ioutil"
	"log"
	"reflect"
	"strconv"
	"unsafe"
)

type Protocol string

const TCP Protocol = "tcp"
const UDP Protocol = "udp"

type Metadata struct {
	IP         string `json:"ip"`
	TCPPort    int    `json:"tcpPort"`
	UDPPort    int    `json:"udpPort"`
	ServiceId  byte   `json:"serviceId"`
	ServiceTCP []int  `json:"serviceTcp"`
	ServiceUDP []int  `json:"serviceUdp"`
}

func Pipe(dest io.WriteCloser, src io.ReadCloser) {
	defer dest.Close()
	defer src.Close()
	n, err := io.Copy(dest, src)
	if err != nil {
		log.Println("Pipe closed (written "+strconv.FormatInt(n, 10)+") with error:", err)
	}
	log.Println("Pipe closed (written " + strconv.FormatInt(n, 10) + ")")
}

func Close(conn io.Closer) {
	if conn == nil || reflect.ValueOf(conn).IsNil() {
		return
	}
	err := conn.Close()
	if err != nil {
		log.Println("Error while closing:", err)
	}
}

func ReadJson(fileName string, value interface{}) {
	file, err := ioutil.ReadFile(fileName)
	if err != nil {
		log.Panicln("Couldn't read file:", err)
	}

	err = json.Unmarshal(file, value)
	if err != nil {
		log.Panicln("Couldn't unmarshal json:", err)
	}
}

func GetConnIdString(data []byte) string {
	return strconv.Itoa(int(*(*uint16)(unsafe.Pointer(&data[0]))))
}