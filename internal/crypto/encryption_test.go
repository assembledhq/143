package crypto

import (
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewService_ValidKey(t *testing.T) {
	t.Parallel()

	key := generateTestKey(t)
	svc, err := NewService(key)
	require.NoError(t, err, "NewService should not return an error with a valid key")
	require.NotNil(t, svc, "NewService should return a non-nil service")
}

func TestNewService_ShortKey(t *testing.T) {
	t.Parallel()

	_, err := NewService("short")
	require.Error(t, err, "NewService should return an error with a short key")
	require.Contains(t, err.Error(), "at least 32", "error should mention minimum length")
}

func TestNewService_EmptyKey(t *testing.T) {
	t.Parallel()

	svc, err := NewService("")
	require.NoError(t, err, "NewService should not error with empty key (dev mode)")
	require.Nil(t, svc, "NewService should return nil for empty key (dev mode)")
}

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	t.Parallel()

	svc, err := NewService(generateTestKey(t))
	require.NoError(t, err, "NewService should not error")

	plaintext := []byte(`{"api_key":"sk-ant-test-key-12345","base_url":""}`)
	encrypted, err := svc.Encrypt(plaintext)
	require.NoError(t, err, "Encrypt should not error")
	require.NotEqual(t, plaintext, encrypted, "encrypted data should differ from plaintext")

	decrypted, err := svc.Decrypt(encrypted)
	require.NoError(t, err, "Decrypt should not error")
	require.Equal(t, plaintext, decrypted, "decrypted data should match original plaintext")
}

func TestEncryptDecrypt_DifferentCiphertexts(t *testing.T) {
	t.Parallel()

	svc, err := NewService(generateTestKey(t))
	require.NoError(t, err, "NewService should not error")

	plaintext := []byte("same input")
	enc1, err := svc.Encrypt(plaintext)
	require.NoError(t, err, "first Encrypt should not error")

	enc2, err := svc.Encrypt(plaintext)
	require.NoError(t, err, "second Encrypt should not error")

	require.NotEqual(t, enc1, enc2, "two encryptions of same plaintext should produce different ciphertexts due to random DEK")

	dec1, err := svc.Decrypt(enc1)
	require.NoError(t, err, "first Decrypt should not error")
	dec2, err := svc.Decrypt(enc2)
	require.NoError(t, err, "second Decrypt should not error")
	require.Equal(t, dec1, dec2, "both decryptions should yield original plaintext")
}

func TestDecrypt_WrongKey(t *testing.T) {
	t.Parallel()

	svc1, err := NewService(generateTestKey(t))
	require.NoError(t, err, "first NewService should not error")

	svc2, err := NewService(generateTestKey(t))
	require.NoError(t, err, "second NewService should not error")

	plaintext := []byte("secret data")
	encrypted, err := svc1.Encrypt(plaintext)
	require.NoError(t, err, "Encrypt should not error")

	_, err = svc2.Decrypt(encrypted)
	require.Error(t, err, "Decrypt with wrong key should return an error")
}

func TestDecrypt_CorruptedData(t *testing.T) {
	t.Parallel()

	svc, err := NewService(generateTestKey(t))
	require.NoError(t, err, "NewService should not error")

	_, err = svc.Decrypt([]byte("not valid encrypted data"))
	require.Error(t, err, "Decrypt should error on corrupted data")
}

func TestEncrypt_EmptyPlaintext(t *testing.T) {
	t.Parallel()

	svc, err := NewService(generateTestKey(t))
	require.NoError(t, err, "NewService should not error")

	encrypted, err := svc.Encrypt([]byte{})
	require.NoError(t, err, "Encrypt should handle empty plaintext")

	decrypted, err := svc.Decrypt(encrypted)
	require.NoError(t, err, "Decrypt should handle empty plaintext round-trip")
	require.Empty(t, decrypted, "decrypted empty plaintext should be empty")
}

func TestDevMode_NilService(t *testing.T) {
	t.Parallel()

	plaintext := []byte(`{"api_key":"sk-test"}`)

	encrypted := DevEncrypt(plaintext)
	require.True(t, len(encrypted) > 0, "DevEncrypt should return non-empty data")

	decrypted, err := DevDecrypt(encrypted)
	require.NoError(t, err, "DevDecrypt should not error on valid data")
	require.Equal(t, plaintext, decrypted, "DevDecrypt should return original plaintext")
}

func TestDevDecrypt_InvalidPrefix(t *testing.T) {
	t.Parallel()

	_, err := DevDecrypt([]byte("not-dev-encrypted"))
	require.Error(t, err, "DevDecrypt should error on data without dev prefix")
}

func generateTestKey(t *testing.T) string {
	t.Helper()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err, "generating random key should not error")
	return hex.EncodeToString(key)
}
