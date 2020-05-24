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
	var cipher stream.Cipher
	var err error
	switch encryptionAlgo {
	case pb.ENCRYPTION_XSALSA20_POLY1305:
		cipher = stream.NewXSalsa20Poly1305Cipher(sharedKey)
	case pb.ENCRYPTION_AES_GCM:
		cipher, err = stream.NewAESGCMCipher(sharedKey[:])
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported encryption algo %v", encryptionAlgo)
	}
	config := &stream.Config{
		Cipher: cipher,
	}
	return stream.NewEncryptedStream(conn, config)
}
