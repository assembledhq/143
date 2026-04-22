package api

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/services/claudecodeauth"
	"github.com/assembledhq/143/internal/services/codexauth"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestNewRouter_EncryptionKeyValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		masterKey string
		expectErr bool
	}{
		{
			name:      "returns error when encryption key is too short",
			masterKey: "short",
			expectErr: true,
		},
		{
			name:      "allows startup when encryption key is unset",
			masterKey: "",
			expectErr: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{EncryptionMasterKey: tt.masterKey}
			codexSvc := codexauth.NewService(nil, zerolog.Nop())
			claudeSvc := claudecodeauth.NewService(nil, zerolog.Nop())
			router, _, _, _, _, err := NewRouter(cfg, nil, zerolog.Nop(), nil, codexSvc, claudeSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			if tt.expectErr {
				require.Error(t, err, "NewRouter should return an error when encryption key is invalid")
				require.Nil(t, router, "NewRouter should not construct a router with an invalid encryption key")
				return
			}

			require.NoError(t, err, "NewRouter should not return an error when encryption key is unset")
			require.NotNil(t, router, "NewRouter should construct a router when encryption key is unset")
		})
	}
}

func testRouterPrivateKeyPEM(t *testing.T) string {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err, "rsa key generation should not return an error")

	block := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}
	return string(pem.EncodeToMemory(block))
}

func TestNewRouter_GitHubAppConfigBuildsRouter(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		GitHubAppID:         143,
		GitHubAppPrivateKey: testRouterPrivateKeyPEM(t),
	}
	codexSvc := codexauth.NewService(nil, zerolog.Nop())
	claudeSvc := claudecodeauth.NewService(nil, zerolog.Nop())

	router, _, _, _, _, err := NewRouter(cfg, nil, zerolog.Nop(), nil, codexSvc, claudeSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err, "NewRouter should build successfully when GitHub App credentials are valid")
	require.NotNil(t, router, "NewRouter should construct a router when GitHub App credentials are valid")
}
