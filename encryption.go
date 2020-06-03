package tuna

import (
	"crypto/sha256"
	"fmt"
	"net"

	stream "github.com/nknorg/encrypted-stream"
	"github.com/nknorg/tuna/pb"
)

const (
	connNonceSize  = 32
	sharedKeySize  = 32
	encryptKeySize = 32
)

func computeEncryptKey(connNonce []byte, sharedKey []byte) *[encryptKeySize]byte {
	encryptKey := sha256.Sum256(append(connNonce, sharedKey...))
	return &encryptKey
}

func encryptConn(conn net.Conn, encryptKey *[encryptKeySize]byte, encryptionAlgo pb.EncryptionAlgo) (net.Conn, error) {
	var cipher stream.Cipher
	var err error
	switch encryptionAlgo {
	case pb.ENCRYPTION_NONE:
		return conn, nil
	case pb.ENCRYPTION_XSALSA20_POLY1305:
		cipher = stream.NewXSalsa20Poly1305Cipher(encryptKey)
	case pb.ENCRYPTION_AES_GCM:
		cipher, err = stream.NewAESGCMCipher(encryptKey[:])
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
