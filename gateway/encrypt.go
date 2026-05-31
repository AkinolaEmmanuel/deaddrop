package gateway

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
	"os"
)

const (
	gcmIVLen      = 12
	gcmAuthTagLen = 16
)

type EncryptBlob struct {
	Ciphertext []byte `json:"ciphertext"`
	IV         []byte `json:"iv"`
	AuthTag    []byte `json:"authTag"`
}

func EncryptFile(path string, key []byte) (*EncryptBlob, error) {
	plaintext, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}
	return EncryptBytes(plaintext, key)
}

func EncryptBytes(plaintext, key []byte) (*EncryptBlob, error) {

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	iv := make([]byte, gcmIVLen)
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return nil, fmt.Errorf("failed to generate IV: %w", err)
	}

	sealed := gcm.Seal(nil, iv, plaintext, nil)

	splitAt := len(sealed) - gcmAuthTagLen
	return &EncryptBlob{
		Ciphertext: sealed[:splitAt],
		IV:         iv,
		AuthTag:    sealed[splitAt:],
	}, nil
}

func (b *EncryptBlob) ToCryptoPayload() *CryptoPayload {
	return &CryptoPayload{
		Ciphertext: b.Ciphertext,
		IV:         b.IV,
		AuthTag:    b.AuthTag,
	}
}
