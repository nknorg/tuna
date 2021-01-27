package tests

import (
	"log"
	"testing"

	"github.com/nknorg/tuna/storage"
)

func TestMeasureStorage(t *testing.T) {
	measureStorage := storage.NewMeasureStorage(".", "test")
	err := measureStorage.Load()
	if err != nil {
		log.Println(err)
	}
}
