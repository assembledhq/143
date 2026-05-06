package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
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

// dualSetCipher returns a dev-encrypted AnthropicConfig blob with both
// APIKey and Subscription populated — the legacy malformed shape the
// anthropic-split must refuse to silently rewrite without operator ack.
func dualSetCipher(t *testing.T) []byte {
	t.Helper()
	plain, err := json.Marshal(models.AnthropicConfig{
		APIKey: "sk-ant-leftover-1234567890",
		Subscription: &models.AnthropicSubscription{
			AccessToken:  "tok-access",
			RefreshToken: "tok-refresh",
			ExpiresAt:    time.Now().Add(time.Hour),
			AccountType:  "claude_max",
		},
	})
	require.NoError(t, err)
	return crypto.DevEncrypt(plain)
}

// expectBatchHeader sets up the mock expectations for the per-batch
// preamble: Begin → SET LOCAL statement_timeout → SELECT batch.
// Returns nothing; the mock continues to track expectations on the next
// call. When `rowToReturn` is non-empty, the SELECT returns that single
// row in the column order runBatch scans.
func expectBatchHeader(t *testing.T, mock pgxmock.PgxPoolIface, rowID, orgID uuid.UUID, cipher []byte) {
	t.Helper()
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL statement_timeout").
		WillReturnResult(pgxmock.NewResult("SET", 0))
	now := time.Now()
	mock.ExpectQuery(`(?s)SELECT id, org_id, user_id, label, config.*FROM coding_credentials.*WHERE provider = 'anthropic'`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "user_id", "label", "config", "priority", "status",
			"created_by", "last_verified_at", "created_at", "updated_at",
		}).AddRow(
			rowID, orgID, (*uuid.UUID)(nil), "team", cipher, 100, "active",
			(*uuid.UUID)(nil), (*time.Time)(nil), now, now,
		))
}

// TestRunBatchDualSetGate locks the fail-closed contract on the
// --allow-dual-set flag. Without the flag, a dual-set row must abort the
// batch BEFORE any UPDATE is issued so the deferred Rollback reverts the
// in-flight tx and no API key is silently dropped. With the flag, the
// rewrite proceeds. Without this test the gate could regress to "warn and
// proceed" without anyone noticing.
func TestRunBatchDualSetGate(t *testing.T) {
	t.Parallel()

	t.Run("aborts before any UPDATE when --allow-dual-set is unset", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		rowID := uuid.New()
		orgID := uuid.New()
		expectBatchHeader(t, mock, rowID, orgID, dualSetCipher(t))
		// Critical: NO ExpectExec for the UPDATE here. The deferred
		// Rollback fires when runBatch returns the sentinel error.
		mock.ExpectRollback()

		_, _, dualSet, _, _, _, runErr := runBatch(
			context.Background(),
			mock,
			nil, /* dev-mode crypto */
			500, 5000,
			false, /* dryRun */
			false, /* allowDualSet */
			time.Time{}, uuid.Nil,
		)

		require.Error(t, runErr, "dual-set without ack must surface an error")
		require.True(t, errors.Is(runErr, errDualSetWithoutAck),
			"err must be errDualSetWithoutAck so main.go can map it to exit 3; got %v", runErr)
		require.Equal(t, 1, dualSet, "dual-set count must reflect the row we refused to rewrite")
		require.NoError(t, mock.ExpectationsWereMet(),
			"no UPDATE should fire when the gate trips; rollback must be the only post-query call")
	})

	t.Run("proceeds with rewrite when --allow-dual-set is set", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		rowID := uuid.New()
		orgID := uuid.New()
		expectBatchHeader(t, mock, rowID, orgID, dualSetCipher(t))
		mock.ExpectExec(`(?s)UPDATE coding_credentials.*SET provider = 'anthropic_subscription'`).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		mock.ExpectCommit()

		processed, splits, dualSet, skipped, _, _, runErr := runBatch(
			context.Background(),
			mock,
			nil,
			500, 5000,
			false, /* dryRun */
			true,  /* allowDualSet */
			time.Time{}, uuid.Nil,
		)

		require.NoError(t, runErr, "with --allow-dual-set the rewrite must proceed cleanly")
		require.Equal(t, 1, processed, "one row was processed")
		require.Equal(t, 1, splits, "the dual-set row must still be split when ack'd")
		require.Equal(t, 1, dualSet, "the dual-set count is bumped regardless of the gate")
		require.Equal(t, 0, skipped, "no decrypt failure here")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("dry-run also fails closed on dual-set without ack", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		rowID := uuid.New()
		orgID := uuid.New()
		expectBatchHeader(t, mock, rowID, orgID, dualSetCipher(t))
		mock.ExpectRollback()

		_, _, dualSet, _, _, _, runErr := runBatch(
			context.Background(),
			mock,
			nil,
			500, 5000,
			true,  /* dryRun */
			false, /* allowDualSet */
			time.Time{}, uuid.Nil,
		)

		require.Error(t, runErr, "dry-run must report the gate trip rather than reporting a clean would-split count")
		require.True(t, errors.Is(runErr, errDualSetWithoutAck))
		require.Equal(t, 1, dualSet)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}
