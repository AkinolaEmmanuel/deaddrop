package gateway

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
)

var ErrAuthFailed = errors.New("decryption failed: wrong key or corrupted data")

func DecryptPayload(cp *CryptoPayload, key []byte) ([]byte, error) {
	if cp == nil {
		return nil, fmt.Errorf("decrypt payload cannot be nil")
	}

	if len(cp.IV) != gcmIVLen {
		return nil, fmt.Errorf("invalid IV length: expected %d, got %d", gcmIVLen, len(cp.IV))
	}

	if len(cp.AuthTag) != gcmAuthTagLen {
		return nil, fmt.Errorf("invalid auth tag length: expected %d, got %d", gcmAuthTagLen, len(cp.AuthTag))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	sealed := make([]byte, len(cp.Ciphertext)+gcmAuthTagLen)
	copy(sealed, cp.Ciphertext)
	copy(sealed[len(cp.Ciphertext):], cp.AuthTag)

	plaintext, err := gcm.Open(nil, cp.IV, sealed, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt: %w", err)
	}

	return plaintext, nil
}
