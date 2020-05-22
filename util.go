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

func ReadFullBytes(reader io.Reader, numBytes int) ([]byte, error) {
	b := make([]byte, numBytes)
	bytesRead := 0
	for bytesRead < numBytes {
		n, err := reader.Read(b[bytesRead:])
		if err != nil {
			return nil, err
		}
		bytesRead += n
	}
	return b, nil
}

func WriteFullBytes(writer io.Writer, b []byte) error {
	n, err := writer.Write(b)
	if err != nil {
		return err
	}
	if n != len(b) {
		return io.ErrShortWrite
	}
	return nil
}

func ReadVarBytes(reader io.Reader) ([]byte, error) {
	b, err := ReadFullBytes(reader, 4)
	if err != nil {
		return nil, err
	}

	return ReadFullBytes(reader, int(binary.LittleEndian.Uint32(b)))
}

func WriteVarBytes(writer io.Writer, b []byte) error {
	lenBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lenBuf, uint32(len(b)))
	err := WriteFullBytes(writer, lenBuf)
	if err != nil {
		return err
	}

	err = WriteFullBytes(writer, b)
	if err != nil {
		return err
	}

	return nil
}
