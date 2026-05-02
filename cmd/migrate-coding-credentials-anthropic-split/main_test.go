package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/crypto"
	"github.com/assembledhq/143/internal/models"
)

// TestEvaluateRowForSplit covers the three classes of legacy anthropic row
// the post-step has to handle. Uses dev-mode crypto (cryptoSvc=nil) so the
// test does not need a real ENCRYPTION_MASTER_KEY.
func TestEvaluateRowForSplit(t *testing.T) {
	t.Parallel()

	mustEncrypt := func(t *testing.T, cfg models.AnthropicConfig) []byte {
		t.Helper()
		plain, err := json.Marshal(cfg)
		require.NoError(t, err)
		return crypto.DevEncrypt(plain)
	}

	t.Run("api-key-only row is skipped", func(t *testing.T) {
		t.Parallel()
		cipher := mustEncrypt(t, models.AnthropicConfig{APIKey: "sk-ant-abc-1234567890"})

		outcome, err := evaluateRowForSplit(nil, cipher)
		require.NoError(t, err)
		require.True(t, outcome.Skip, "pure api-key row should be skipped")
		require.False(t, outcome.HadDualSet, "no warning expected for api-key-only row")
		require.Empty(t, outcome.NewCipher, "skipped rows must not emit a rewrite payload")
	})

	t.Run("subscription-only row is split into AnthropicSubscriptionConfig", func(t *testing.T) {
		t.Parallel()
		expires := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
		sub := &models.AnthropicSubscription{
			AccessToken:  "tok-access",
			RefreshToken: "tok-refresh",
			ExpiresAt:    expires,
			AccountType:  "claude_max",
		}
		cipher := mustEncrypt(t, models.AnthropicConfig{Subscription: sub})

		outcome, err := evaluateRowForSplit(nil, cipher)
		require.NoError(t, err)
		require.False(t, outcome.Skip, "subscription row should split, not skip")
		require.False(t, outcome.HadDualSet, "subscription-only row should not flag dual-set")
		require.NotEmpty(t, outcome.NewCipher, "subscription rewrite must produce a new cipher")

		// Round-trip: the rewritten cipher must decrypt as
		// AnthropicSubscriptionConfig with all subscription fields preserved.
		newPlain, err := crypto.DevDecrypt(outcome.NewCipher)
		require.NoError(t, err)
		var got models.AnthropicSubscriptionConfig
		require.NoError(t, json.Unmarshal(newPlain, &got))
		require.Equal(t, "tok-access", got.AccessToken, "access_token must round-trip")
		require.Equal(t, "tok-refresh", got.RefreshToken, "refresh_token must round-trip")
		require.Equal(t, "claude_max", got.AccountType, "account_type must round-trip")
		require.True(t, got.ExpiresAt.Equal(expires), "expires_at must round-trip")
	})

	t.Run("dual-set row preserves subscription and flags warning", func(t *testing.T) {
		t.Parallel()
		// Pre-validator hand-edited / legacy state: APIKey AND Subscription
		// both populated. The post-step must keep the subscription half and
		// drop the API key, with HadDualSet=true so runBatch logs the drop.
		sub := &models.AnthropicSubscription{
			AccessToken:  "tok-access",
			RefreshToken: "tok-refresh",
			ExpiresAt:    time.Now().Add(time.Hour),
		}
		cipher := mustEncrypt(t, models.AnthropicConfig{
			APIKey:       "sk-ant-leftover-1234567890",
			Subscription: sub,
		})

		outcome, err := evaluateRowForSplit(nil, cipher)
		require.NoError(t, err)
		require.False(t, outcome.Skip, "dual-set row should still split")
		require.True(t, outcome.HadDualSet, "dual-set row must flag for the warning emission")
		require.NotEmpty(t, outcome.NewCipher)

		// The rewritten cipher must contain ONLY subscription fields — no
		// stray api_key field carried over.
		newPlain, err := crypto.DevDecrypt(outcome.NewCipher)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(newPlain, &raw))
		_, hasAPIKey := raw["api_key"]
		require.False(t, hasAPIKey, "rewritten subscription cipher must not carry api_key")
		require.Equal(t, "tok-access", raw["access_token"], "subscription access_token must round-trip")
	})

	t.Run("garbage cipher surfaces a wrapped decrypt error", func(t *testing.T) {
		t.Parallel()
		_, err := evaluateRowForSplit(nil, []byte("not-a-valid-cipher"))
		require.Error(t, err, "decrypt failure must surface, not panic")
		require.Contains(t, err.Error(), "decrypt", "error must wrap the decrypt step")
	})

	t.Run("malformed plaintext surfaces a wrapped unmarshal error", func(t *testing.T) {
		t.Parallel()
		// Valid ciphertext envelope around invalid JSON.
		bad := crypto.DevEncrypt([]byte("{not valid json"))
		_, err := evaluateRowForSplit(nil, bad)
		require.Error(t, err)
		require.Contains(t, err.Error(), "unmarshal", "error must wrap the unmarshal step")
	})
}
