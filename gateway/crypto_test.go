package gateway

import (
	"bytes"
	"testing"
)

func TestDeriveKeyDeterministicAndScoped(t *testing.T) {
	k1, err := DeriveKey("correct horse battery staple", "room1")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	if len(k1) != aesKeyLen {
		t.Fatalf("key length = %d, want %d", len(k1), aesKeyLen)
	}

	k2, err := DeriveKey("correct horse battery staple", "room1")
	if err != nil {
		t.Fatalf("DeriveKey (again): %v", err)
	}
	if !bytes.Equal(k1, k2) {
		t.Error("same passphrase+roomID produced different keys")
	}

	k3, err := DeriveKey("correct horse battery staple", "room2")
	if err != nil {
		t.Fatalf("DeriveKey (different room): %v", err)
	}
	if bytes.Equal(k1, k3) {
		t.Error("different roomID produced the same key — salt is not scoped to room")
	}

	k4, err := DeriveKey("different passphrase", "room1")
	if err != nil {
		t.Fatalf("DeriveKey (different passphrase): %v", err)
	}
	if bytes.Equal(k1, k4) {
		t.Error("different passphrase produced the same key")
	}
}

func TestDeriveKeyRejectsEmptyInputs(t *testing.T) {
	if _, err := DeriveKey("", "room1"); err == nil {
		t.Error("expected error for empty passphrase")
	}
	if _, err := DeriveKey("passphrase", ""); err == nil {
		t.Error("expected error for empty roomID")
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key, err := DeriveKey("passphrase", "room1")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}

	plaintext := []byte("the secret file contents, potentially spanning multiple lines\nand binary-ish bytes: \x00\x01\xff")

	blob, err := EncryptBytes(plaintext, key)
	if err != nil {
		t.Fatalf("EncryptBytes: %v", err)
	}

	got, err := DecryptPayload(blob.ToCryptoPayload(), key)
	if err != nil {
		t.Fatalf("DecryptPayload: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("round trip mismatch: got %q, want %q", got, plaintext)
	}
}

func TestDecryptWithWrongKeyFails(t *testing.T) {
	key, _ := DeriveKey("passphrase", "room1")
	wrongKey, _ := DeriveKey("wrong-passphrase", "room1")

	blob, err := EncryptBytes([]byte("secret"), key)
	if err != nil {
		t.Fatalf("EncryptBytes: %v", err)
	}

	if _, err := DecryptPayload(blob.ToCryptoPayload(), wrongKey); err == nil {
		t.Error("decryption with wrong key succeeded, want authentication failure")
	}
}

func TestDecryptWithTamperedCiphertextFails(t *testing.T) {
	key, _ := DeriveKey("passphrase", "room1")

	blob, err := EncryptBytes([]byte("secret"), key)
	if err != nil {
		t.Fatalf("EncryptBytes: %v", err)
	}

	cp := blob.ToCryptoPayload()
	cp.Ciphertext[0] ^= 0xFF // flip a bit

	if _, err := DecryptPayload(cp, key); err == nil {
		t.Error("decryption of tampered ciphertext succeeded, want authentication failure")
	}
}

func TestDecryptRejectsMalformedPayload(t *testing.T) {
	key, _ := DeriveKey("passphrase", "room1")

	if _, err := DecryptPayload(nil, key); err == nil {
		t.Error("expected error for nil payload")
	}

	cp := &CryptoPayload{Ciphertext: []byte("x"), IV: []byte("short"), AuthTag: make([]byte, gcmAuthTagLen)}
	if _, err := DecryptPayload(cp, key); err == nil {
		t.Error("expected error for wrong IV length")
	}

	cp2 := &CryptoPayload{Ciphertext: []byte("x"), IV: make([]byte, gcmIVLen), AuthTag: []byte("short")}
	if _, err := DecryptPayload(cp2, key); err == nil {
		t.Error("expected error for wrong auth tag length")
	}
}
