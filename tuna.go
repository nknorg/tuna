package tuna

import (
	"encoding/json"
	"errors"
	"io"
	"io/ioutil"
	"log"
)

type Protocol string

const TCP Protocol = "tcp"
const UDP Protocol = "udp"

var Services []Service

type Service struct {
	Name string `json:"name"`
	TCP  []int  `json:"tcp"`
	UDP  []int  `json:"udp"`
}

func Pipe(dest io.WriteCloser, src io.ReadCloser) {
	defer dest.Close()
	defer src.Close()
	io.Copy(dest, src)
}

func Close(conn io.Closer) {
	if conn == nil {
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

func GetServiceId(serviceName string) (byte, error) {
	for i, service := range Services {
		if service.Name == serviceName {
			return byte(i), nil
		}
	}

	return 0, errors.New("Service " + serviceName + " not found")
}

func Init() {
	ReadJson("services.json", &Services)
}