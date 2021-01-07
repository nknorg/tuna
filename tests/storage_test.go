package tests

import (
	"github.com/nknorg/tuna/storage"
	"log"
	"testing"
	"time"
)

func TestMeasureStorage(t *testing.T) {
	measureStorage := storage.LoadMeasureStorage(".")
	err := measureStorage.Load()
	if err != nil {
		log.Println(err)
	}
	err = measureStorage.FavoriteNodes.Add(&storage.FavoriteNode{
		Index:        &storage.Index{Id: "192.168.1.1"},
		IP:           "192.168.1.1",
		Address:      "123123123abc",
		Delay:        10,
		MinBandwidth: 100,
		MaxBandwidth: 200,
		Expired:      time.Now().Add(time.Duration(storage.Expired)).Unix(),
	})
	if err != nil {
		log.Println(err)
		return
	}

	err = measureStorage.SaveFavoriteNodes()
	if err != nil {
		log.Println(err)
	}
	//
	//measureStorage.BadNodes = append(measureStorage.BadNodes, &storage.BadNode{
	//	IP:      "192.168.1.1",
	//	Subnet:  8,
	//	Address: "123aseqweqwe123",
	//	Expired: 0,
	//})
	//err = measureStorage.SaveBadNodes()
	//if err != nil {
	//	log.Println(err)
	//}
}
