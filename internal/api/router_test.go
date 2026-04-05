package api

import (
	"testing"

	"github.com/assembledhq/143/internal/config"
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
			router, err := NewRouter(cfg, nil, zerolog.Nop(), codexSvc, nil, nil)
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
