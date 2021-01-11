package tests

import (
	"github.com/nknorg/tuna/storage"
	"log"
	"testing"
)

func TestMeasureStorage(t *testing.T) {
	measureStorage := storage.NewMeasureStorage(".")
	err := measureStorage.Load()
	if err != nil {
		log.Println(err)
	}
}
