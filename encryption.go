package tuna

import (
	"fmt"
	"net"

	stream "github.com/nknorg/encrypted-stream"
	"github.com/nknorg/tuna/pb"
)

const (
	sharedKeySize = 32
)

func encryptConn(conn net.Conn, sharedKey *[sharedKeySize]byte, encryptionAlgo pb.EncryptionAlgo) (net.Conn, error) {
	var config *stream.Config
	switch encryptionAlgo {
	case pb.ENCRYPTION_XSALSA20_POLY1305:
		config = &stream.Config{
			Cipher: stream.NewXSalsa20Poly1305Cipher(sharedKey),
		}
	default:
		return nil, fmt.Errorf("unsupported encryption algo %v", encryptionAlgo)
	}
	return stream.NewEncryptedStream(conn, config)
}
