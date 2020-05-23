package tuna

import (
	"encoding/binary"
	"io"
	"strings"

	"github.com/nknorg/nkn/common"
)

func ParsePrice(priceStr string) (common.Fixed64, common.Fixed64, error) {
	price := strings.Split(priceStr, ",")
	entryToExitPrice, err := common.StringToFixed64(strings.Trim(price[0], " "))
	if err != nil {
		return 0, 0, err
	}
	var exitToEntryPrice common.Fixed64
	if len(price) > 1 {
		exitToEntryPrice, err = common.StringToFixed64(strings.Trim(price[1], " "))
		if err != nil {
			return 0, 0, err
		}
	} else {
		exitToEntryPrice = entryToExitPrice
	}
	return entryToExitPrice, exitToEntryPrice, nil
}

func ReadVarBytes(reader io.Reader) ([]byte, error) {
	b := make([]byte, 4)
	_, err := io.ReadFull(reader, b)
	if err != nil {
		return nil, err
	}

	b = make([]byte, int(binary.LittleEndian.Uint32(b)))
	_, err = io.ReadFull(reader, b)
	if err != nil {
		return nil, err
	}

	return b, nil
}

func WriteVarBytes(writer io.Writer, b []byte) error {
	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, uint32(len(b)))

	_, err := writer.Write(lenBuf)
	if err != nil {
		return err
	}

	_, err = writer.Write(b)
	if err != nil {
		return err
	}

	return nil
}
