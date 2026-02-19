package crypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// devPrefix marks plaintext-stored credentials in dev mode (no ENCRYPTION_MASTER_KEY).
var devPrefix = []byte("v0:")

// Service provides AES-256-GCM envelope encryption.
// Each Encrypt call generates a random per-record DEK, encrypts plaintext with
// the DEK, then wraps the DEK with the master-derived KEK.
type Service struct {
	kek []byte // 32-byte key encryption key derived from master key via HKDF
}

// NewService creates an encryption service from a master key string.
// Returns (nil, nil) if masterKey is empty (dev mode — use DevEncrypt/DevDecrypt).
// Returns an error if masterKey is non-empty but shorter than 32 characters.
func NewService(masterKey string) (*Service, error) {
	if masterKey == "" {
		return nil, nil
	}
	if len(masterKey) < 32 {
		return nil, fmt.Errorf("encryption master key must be at least 32 characters, got %d", len(masterKey))
	}

	// Derive a 32-byte KEK from the master key via HKDF-SHA256.
	hkdfReader := hkdf.New(sha256.New, []byte(masterKey), nil, []byte("143-credential-encryption"))
	kek := make([]byte, 32)
	if _, err := io.ReadFull(hkdfReader, kek); err != nil {
		return nil, fmt.Errorf("derive KEK: %w", err)
	}

	return &Service{kek: kek}, nil
}

// Encrypt encrypts plaintext using envelope encryption.
// Layout: [4-byte wrappedDEK length][wrappedDEK][encrypted plaintext]
func (s *Service) Encrypt(plaintext []byte) ([]byte, error) {
	// Generate random DEK.
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return nil, fmt.Errorf("generate DEK: %w", err)
	}

	// Encrypt plaintext with DEK.
	ciphertext, err := aesGCMEncrypt(dek, plaintext)
	if err != nil {
		return nil, fmt.Errorf("encrypt plaintext: %w", err)
	}

	// Wrap DEK with KEK.
	wrappedDEK, err := aesGCMEncrypt(s.kek, dek)
	if err != nil {
		return nil, fmt.Errorf("wrap DEK: %w", err)
	}

	// Encode: [4-byte len(wrappedDEK)][wrappedDEK][ciphertext]
	buf := make([]byte, 4+len(wrappedDEK)+len(ciphertext))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(wrappedDEK)))
	copy(buf[4:4+len(wrappedDEK)], wrappedDEK)
	copy(buf[4+len(wrappedDEK):], ciphertext)

	return buf, nil
}

// Decrypt reverses Encrypt.
func (s *Service) Decrypt(data []byte) ([]byte, error) {
	if len(data) < 4 {
		return nil, errors.New("encrypted data too short")
	}

	wdekLen := int(binary.BigEndian.Uint32(data[:4]))
	if len(data) < 4+wdekLen {
		return nil, errors.New("encrypted data truncated: wrapped DEK incomplete")
	}

	wrappedDEK := data[4 : 4+wdekLen]
	ciphertext := data[4+wdekLen:]

	// Unwrap DEK.
	dek, err := aesGCMDecrypt(s.kek, wrappedDEK)
	if err != nil {
		return nil, fmt.Errorf("unwrap DEK: %w", err)
	}

	// Decrypt plaintext.
	plaintext, err := aesGCMDecrypt(dek, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decrypt plaintext: %w", err)
	}

	return plaintext, nil
}

// DevEncrypt stores plaintext with a "v0:" prefix for dev mode (no encryption).
func DevEncrypt(plaintext []byte) []byte {
	out := make([]byte, len(devPrefix)+len(plaintext))
	copy(out, devPrefix)
	copy(out[len(devPrefix):], plaintext)
	return out
}

// DevDecrypt reads plaintext stored with DevEncrypt.
func DevDecrypt(data []byte) ([]byte, error) {
	if !bytes.HasPrefix(data, devPrefix) {
		return nil, errors.New("data does not have dev prefix (v0:)")
	}
	return data[len(devPrefix):], nil
}

// aesGCMEncrypt encrypts plaintext with a 256-bit key using AES-256-GCM.
// Returns [nonce || ciphertext].
func aesGCMEncrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// aesGCMDecrypt decrypts data produced by aesGCMEncrypt.
func aesGCMDecrypt(key, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}
	return gcm.Open(nil, data[:nonceSize], data[nonceSize:], nil)
}
