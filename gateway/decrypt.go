package gateway

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"os"
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

func DecryptToFile(cp *CryptoPayload, key []byte, outputPath string) error {
	plaintext, err := DecryptPayload(cp, key)
	if err != nil {
		return err
	}

	temp, err := os.CreateTemp("", "decrypted-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	tempName := temp.Name()

	var writeErr error
	defer func() {
		if writeErr != nil {
			os.Remove(tempName)
		}
	}()

	if _, writeErr = temp.Write(plaintext); writeErr != nil {
		return fmt.Errorf("failed to write decrypted data: %w", writeErr)
	}

	if writeErr = temp.Close(); writeErr != nil {
		return fmt.Errorf("failed to close temp file: %w", writeErr)
	}

	if writeErr = os.Rename(tempName, outputPath); writeErr != nil {
		return fmt.Errorf("failed to move decrypted file to destination: %w", writeErr)
	}

	return nil
}
