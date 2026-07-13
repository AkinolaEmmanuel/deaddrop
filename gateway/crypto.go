package gateway

import (
	"crypto/sha256"
	"fmt"

	"golang.org/x/crypto/scrypt"
)

const (
	aesKeyLen = 32 // 256 bits
	// scryptN=2^17 costs ~1s per derivation on modern hardware. Security here
	// rests entirely on the passphrase, so we pay that cost to slow down
	// offline brute force; 2^14 (the old interactive-login default) is too
	// cheap against today's GPU/ASIC cracking rigs.
	scryptN = 1 << 17
	scryptR = 8
	scryptP = 1

	systemSaltPrefix = "deaddrop_v1:"
)

func DeriveKey(passphrase, roomID string) ([]byte, error) {
	if passphrase == "" {
		return nil, fmt.Errorf("passphrase cannot be empty")
	}

	if roomID == "" {
		return nil, fmt.Errorf("roomID cannot be empty")
	}

	salt := roomSalt(roomID)

	key, err := scrypt.Key([]byte(passphrase), salt, scryptN, scryptR, scryptP, aesKeyLen)
	if err != nil {
		return nil, fmt.Errorf("scrypt key derivation failed: %w", err)
	}

	return key, nil

}

func roomSalt(roomID string) []byte {
	hash := sha256.Sum256([]byte(systemSaltPrefix + roomID))
	return hash[:]
}
