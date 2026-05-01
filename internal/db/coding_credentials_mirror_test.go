package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/crypto"
	"github.com/assembledhq/143/internal/models"
)

// devEncoded returns the dev-mode encoded form of a JSON-marshalled config.
// We use dev mode (cryptoSvc=nil → DevEncrypt prefix) so the mock pool can
// hand back a byte slice the store re-decrypts identically.
func devEncoded(t *testing.T, cfg models.ProviderConfig) []byte {
	t.Helper()
	plaintext, err := json.Marshal(cfg)
	require.NoError(t, err)
	return crypto.DevEncrypt(plaintext)
}

// TestOrgCredentialStore_MirrorsCodingProviderWrites is the headline mirror
// test from the design doc's review checklist. It exercises the full path
// through OrgCredentialStore + CodingCredentialStore for the three
// translations the mirror has to get right:
//
//   - openai_chatgpt → openai_subscription (provider rename)
//   - anthropic+Subscription → anthropic_subscription (split out)
//   - openai (plain API key) → openai (no rewrite)
//
// Both stores share the same pgxmock pool so a single ExpectQuery sequence
// covers the legacy write + the post-write SELECT + the mirror INSERT in
// the order they actually fire.
func TestOrgCredentialStore_MirrorsCodingProviderWrites(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                 string
		cfg                  models.ProviderConfig
		expectMirrorProvider string
		// expectedConfigCheck is run on the encrypted bytes that get written
		// to coding_credentials; it decrypts and asserts the resulting type.
		expectedConfigCheck func(t *testing.T, encrypted []byte)
	}{
		{
			name:                 "openai_chatgpt mirrors to openai_subscription",
			cfg:                  models.OpenAIChatGPTConfig{AccessToken: "tok", RefreshToken: "rt", AccountType: "pro", ExpiresAt: time.Now().Add(time.Hour)},
			expectMirrorProvider: string(models.ProviderOpenAISubscription),
			expectedConfigCheck: func(t *testing.T, encrypted []byte) {
				plain, err := crypto.DevDecrypt(encrypted)
				require.NoError(t, err)
				var cfg models.OpenAISubscriptionConfig
				require.NoError(t, json.Unmarshal(plain, &cfg))
				require.Equal(t, "tok", cfg.AccessToken)
				require.Equal(t, "pro", cfg.AccountType)
			},
		},
		{
			name: "anthropic+subscription mirrors to anthropic_subscription",
			cfg: models.AnthropicConfig{
				Subscription: &models.AnthropicSubscription{
					AccessToken:  "tok",
					RefreshToken: "rt",
					AccountType:  "claude_max",
					ExpiresAt:    time.Now().Add(time.Hour),
				},
			},
			expectMirrorProvider: string(models.ProviderAnthropicSubscription),
			expectedConfigCheck: func(t *testing.T, encrypted []byte) {
				plain, err := crypto.DevDecrypt(encrypted)
				require.NoError(t, err)
				var cfg models.AnthropicSubscriptionConfig
				require.NoError(t, json.Unmarshal(plain, &cfg))
				require.Equal(t, "tok", cfg.AccessToken)
				require.Equal(t, "claude_max", cfg.AccountType)
			},
		},
		{
			name:                 "openai api key mirrors unchanged",
			cfg:                  models.OpenAIConfig{APIKey: "sk-test", APIType: "responses"},
			expectMirrorProvider: string(models.ProviderOpenAI),
			expectedConfigCheck: func(t *testing.T, encrypted []byte) {
				plain, err := crypto.DevDecrypt(encrypted)
				require.NoError(t, err)
				var cfg models.OpenAIConfig
				require.NoError(t, json.Unmarshal(plain, &cfg))
				require.Equal(t, "sk-test", cfg.APIKey)
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			orgID := uuid.New()
			newID := uuid.New()
			provider := tc.cfg.Provider()

			legacy := NewOrgCredentialStore(mock, nil)
			coding := NewCodingCredentialStore(mock, nil)
			legacy.SetCodingMirror(coding)

			// 1. UpsertWithLabel begins a tx, takes the per-(scope) lock, reads MAX(priority),
			//    INSERTs the legacy row, and commits.
			mock.ExpectBegin()
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(pgxmock.AnyArg()).
				WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery(`SELECT COALESCE\(MAX\(priority\), 0\) \+ 1`).WithArgs(pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows([]string{"priority"}).AddRow(1))
			mock.ExpectQuery("INSERT INTO org_credentials").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(newID))
			mock.ExpectCommit()

			// 2. After the tx commits, reflectOrgCredentialByID does a SELECT to
			//    re-load the row + decrypt. We hand it back the same encrypted
			//    config the store would have written so the mirror sees a real
			//    payload to translate.
			encryptedLegacyCfg := devEncoded(t, tc.cfg)
			now := time.Now()
			mock.ExpectQuery("SELECT id, org_id, provider, label, config, status, priority, last_verified_at, last_used_at, created_by, created_at, updated_at\\s+FROM org_credentials WHERE id = @id").
				WithArgs(newID).
				WillReturnRows(pgxmock.NewRows(codingAuthColumns).
					AddRow(newID, orgID, string(provider), "", encryptedLegacyCfg, "active", 1, (*time.Time)(nil), (*time.Time)(nil), (*uuid.UUID)(nil), now, now))

			// 3. The mirror upserts into coding_credentials. We capture the
			//    config arg via a custom matcher so the encryption check below
			//    can verify the rewrap.
			var capturedEncrypted []byte

			mock.ExpectExec(`(?s)INSERT INTO coding_credentials.*ON CONFLICT \(id\) DO UPDATE`).
				WithArgs(
					newID,                   // id
					orgID,                   // org_id
					(*uuid.UUID)(nil),       // user_id (NULL)
					tc.expectMirrorProvider, // provider (translated)
					"",                      // label
					captureBytes(&capturedEncrypted),
					1,                 // priority
					"active",          // status
					(*uuid.UUID)(nil), // created_by
					(*time.Time)(nil), // last_verified_at
					pgxmock.AnyArg(),  // created_at
					pgxmock.AnyArg(),  // updated_at
				).
				WillReturnResult(pgxmock.NewResult("INSERT", 1))

			id, err := legacy.UpsertWithLabel(context.Background(), orgID, nil, "", tc.cfg)
			require.NoError(t, err)
			require.Equal(t, newID, *id)
			require.NoError(t, mock.ExpectationsWereMet())

			// Verify the bytes the mirror wrote decrypt back to the right
			// translated shape.
			require.NotEmpty(t, capturedEncrypted, "mirror should have written an encrypted config")
			tc.expectedConfigCheck(t, capturedEncrypted)
		})
	}
}

// TestOrgCredentialStore_MirrorDisableInvalidatesCache is the regression test
// for the bug the review caught: mirrorDisable used to call
// invalidateAll(uuid.Nil) which never matches, leaving disabled rows live in
// the resolver cache for up to 30s. The fix flips the row + invalidates the
// real (scope, provider) key in one RETURNING-driven statement.
func TestOrgCredentialStore_MirrorDisableInvalidatesCache(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	rowID := uuid.New()

	legacy := NewOrgCredentialStore(mock, nil)
	coding := NewCodingCredentialStore(mock, nil)
	legacy.SetCodingMirror(coding)

	// Seed the resolver cache so we can verify it gets cleared.
	cachedRow := models.DecryptedCodingCredential{
		ID:       rowID,
		OrgID:    orgID,
		Provider: models.ProviderAnthropic,
		Status:   "active",
	}
	coding.resolverCache.put(orgID, nil, models.ProviderAnthropic, []models.DecryptedCodingCredential{cachedRow})
	if _, hit := coding.resolverCache.get(orgID, nil, models.ProviderAnthropic); !hit {
		t.Fatalf("expected cached entry to be present before disable")
	}

	// DisableByID issues the legacy UPDATE then asks the mirror to flip the
	// unified row's status. The mirror's UPDATE...RETURNING gives us the
	// scope+provider so the cache invalidation hits the right key.
	mock.ExpectExec(`UPDATE org_credentials SET status = 'disabled'.*WHERE id = @id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery(`UPDATE coding_credentials SET status = 'disabled'.*WHERE id = @id RETURNING org_id, user_id, provider`).
		WithArgs(rowID).
		WillReturnRows(pgxmock.NewRows([]string{"org_id", "user_id", "provider"}).
			AddRow(orgID, (*uuid.UUID)(nil), string(models.ProviderAnthropic)))

	require.NoError(t, legacy.DisableByID(context.Background(), orgID, rowID))
	require.NoError(t, mock.ExpectationsWereMet())

	if _, hit := coding.resolverCache.get(orgID, nil, models.ProviderAnthropic); hit {
		t.Fatalf("cache should be invalidated after mirror disable")
	}
}

// TestOrgCredentialStore_MirrorDeleteInvalidatesCache exercises the same fix
// for DeleteCodingAuth → mirrorDelete.
func TestOrgCredentialStore_MirrorDeleteInvalidatesCache(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	rowID := uuid.New()

	legacy := NewOrgCredentialStore(mock, nil)
	coding := NewCodingCredentialStore(mock, nil)
	legacy.SetCodingMirror(coding)

	coding.resolverCache.put(orgID, &userID, models.ProviderAnthropic, []models.DecryptedCodingCredential{{ID: rowID}})
	if _, hit := coding.resolverCache.get(orgID, &userID, models.ProviderAnthropic); !hit {
		t.Fatalf("expected cached entry before delete")
	}

	mock.ExpectExec(`DELETE FROM org_credentials`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectQuery(`DELETE FROM coding_credentials WHERE id = @id RETURNING org_id, user_id, provider`).
		WithArgs(rowID).
		WillReturnRows(pgxmock.NewRows([]string{"org_id", "user_id", "provider"}).
			AddRow(orgID, &userID, string(models.ProviderAnthropic)))

	require.NoError(t, legacy.DeleteCodingAuth(context.Background(), orgID, rowID))
	require.NoError(t, mock.ExpectationsWereMet())

	if _, hit := coding.resolverCache.get(orgID, &userID, models.ProviderAnthropic); hit {
		t.Fatalf("cache should be invalidated after mirror delete")
	}
}

// TestOrgCredentialStore_MirrorDisableMissingRowIsNoop confirms the silent
// no-op path when the unified row doesn't exist (e.g. a non-coding provider
// that never got mirrored). Cache should be untouched and no error surfaced.
func TestOrgCredentialStore_MirrorDisableMissingRowIsNoop(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	rowID := uuid.New()

	legacy := NewOrgCredentialStore(mock, nil)
	coding := NewCodingCredentialStore(mock, nil)
	legacy.SetCodingMirror(coding)

	// Cache another entry that should NOT get invalidated.
	otherID := uuid.New()
	coding.resolverCache.put(orgID, nil, models.ProviderAnthropic, []models.DecryptedCodingCredential{{ID: otherID}})

	mock.ExpectExec(`UPDATE org_credentials SET status = 'disabled'`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery(`UPDATE coding_credentials SET status = 'disabled'.*WHERE id = @id RETURNING`).
		WithArgs(rowID).
		WillReturnError(pgx.ErrNoRows)

	require.NoError(t, legacy.DisableByID(context.Background(), orgID, rowID))
	require.NoError(t, mock.ExpectationsWereMet())

	// Untouched cache still has the unrelated entry.
	got, hit := coding.resolverCache.get(orgID, nil, models.ProviderAnthropic)
	require.True(t, hit)
	require.Equal(t, otherID, got[0].ID)
}

func TestCodingCredentialStore_MirrorUserCredential(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		row            models.UserCredential
		cfg            models.ProviderConfig
		expectUserID   *uuid.UUID
		expectLabel    string
		expectPriority int
		expectNoop     bool
	}{
		{
			name: "personal api key",
			row: models.UserCredential{
				ID: uuid.New(), UserID: uuid.New(), OrgID: uuid.New(),
				Provider: models.ProviderOpenAI, Status: "active", CreatedAt: time.Now(), UpdatedAt: time.Now(),
			},
			cfg:            models.OpenAIConfig{APIKey: "sk-openai-123456"},
			expectPriority: 1,
		},
		{
			name: "team default becomes org scoped",
			row: models.UserCredential{
				ID: uuid.New(), UserID: uuid.New(), OrgID: uuid.New(),
				Provider: models.ProviderAnthropic, IsTeamDefault: true, Status: "active", CreatedAt: time.Now(), UpdatedAt: time.Now(),
			},
			cfg:            models.AnthropicConfig{APIKey: "sk-ant-123456789"},
			expectLabel:    "team",
			expectPriority: teamDefaultMirrorPriority,
		},
		{
			name: "non coding provider is ignored",
			row: models.UserCredential{
				ID: uuid.New(), UserID: uuid.New(), OrgID: uuid.New(),
				Provider: models.ProviderGitHubApp, Status: "active", CreatedAt: time.Now(), UpdatedAt: time.Now(),
			},
			cfg:        models.GitHubAppConfig{AppID: 1, PrivateKey: "key"},
			expectNoop: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			store := NewCodingCredentialStore(mock, nil)
			if !tt.expectNoop {
				if tt.row.IsTeamDefault {
					mock.ExpectExec(`(?s)INSERT INTO coding_credentials.*ON CONFLICT \(id\) DO UPDATE`).
						WithArgs(codingAnyArgs(12)...).
						WillReturnResult(pgxmock.NewResult("INSERT", 1))
				} else {
					uid := tt.row.UserID
					tt.expectUserID = &uid
					mock.ExpectExec(`(?s)INSERT INTO coding_credentials.*ON CONFLICT \(id\) DO UPDATE`).
						WithArgs(codingAnyArgs(12)...).
						WillReturnResult(pgxmock.NewResult("INSERT", 1))
				}
			}

			err = store.MirrorUserCredential(context.Background(), tt.row, tt.cfg)

			require.NoError(t, err, "MirrorUserCredential should not return an error")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

// TestMirrorUserCredential_TeamDefaultLabelMatchesMigration locks the
// team-default label format the mirror writes to byte-for-byte equality with
// the format used by `migrations/000107_copy_coding_credentials.up.sql`
// step 3. If this assertion drifts, the SQL data-copy will create one row
// for a logical team-default credential and the dual-write mirror will
// create a second one at the same (org, provider) — instead of upserting via
// the unique (org_id, user_id, provider, label) natural key. The duplicate
// would surface as a duplicate row in /settings/account "Org fallback" and
// quietly mis-bias the resolver toward whichever lands first.
//
// The migration writes:
//
//	'Team default (migrated from ' || uc.user_id::text || ')'
//
// The mirror writes the exact same shape via Go string concatenation. This
// test captures the second positional argument to the mirror UPSERT (label)
// and asserts byte-equality.
func TestMirrorUserCredential_TeamDefaultLabelMatchesMigration(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewCodingCredentialStore(mock, nil)

	userID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	orgID := uuid.New()
	row := models.UserCredential{
		ID:            uuid.New(),
		UserID:        userID,
		OrgID:         orgID,
		Provider:      models.ProviderAnthropic,
		IsTeamDefault: true,
		Status:        "active",
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	cfg := models.AnthropicConfig{APIKey: "sk-ant-test-1234567890"}

	// Positional arg order on the mirror UPSERT (see upsertMirroredRow):
	//   id, org_id, user_id, provider, label, config, priority, status,
	//   created_by, last_verified_at, created_at, updated_at
	// We only constrain `label` (index 4); everything else is AnyArg.
	wantLabel := "Team default (migrated from " + userID.String() + ")"
	mock.ExpectExec(`(?s)INSERT INTO coding_credentials.*ON CONFLICT \(id\) DO UPDATE`).
		WithArgs(
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			wantLabel,
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	require.NoError(t, store.MirrorUserCredential(context.Background(), row, cfg),
		"MirrorUserCredential should accept the team-default row")
	require.NoError(t, mock.ExpectationsWereMet(),
		"team-default mirror must write the migration-aligned label format")
}

func TestMirrorProviderHelpers(t *testing.T) {
	t.Parallel()

	orgTests := []struct {
		name         string
		provider     models.ProviderName
		cfg          models.ProviderConfig
		wantProvider models.ProviderName
		wantOK       bool
	}{
		{name: "chatgpt renamed", provider: models.ProviderOpenAIChatGPT, cfg: models.OpenAIChatGPTConfig{AccessToken: "tok", RefreshToken: "refresh"}, wantProvider: models.ProviderOpenAISubscription, wantOK: true},
		{name: "chatgpt wrong type", provider: models.ProviderOpenAIChatGPT, cfg: models.OpenAIConfig{APIKey: "sk"}, wantOK: false},
		{name: "anthropic api key cleaned", provider: models.ProviderAnthropic, cfg: models.AnthropicConfig{APIKey: "sk-ant", Subscription: nil}, wantProvider: models.ProviderAnthropic, wantOK: true},
		{name: "anthropic wrong type", provider: models.ProviderAnthropic, cfg: models.OpenAIConfig{APIKey: "sk"}, wantOK: false},
		{name: "openrouter unchanged", provider: models.ProviderOpenRouter, cfg: models.OpenRouterConfig{APIKey: "sk-or"}, wantProvider: models.ProviderOpenRouter, wantOK: true},
		{name: "non coding skipped", provider: models.ProviderSlack, cfg: models.SlackConfig{AccessToken: "xoxb"}, wantOK: false},
	}
	for _, tt := range orgTests {
		t.Run("org "+tt.name, func(t *testing.T) {
			t.Parallel()

			gotProvider, _, ok := mirrorProviderForOrg(tt.provider, tt.cfg)

			require.Equal(t, tt.wantOK, ok, "mirrorProviderForOrg ok should match expected")
			require.Equal(t, tt.wantProvider, gotProvider, "mirrorProviderForOrg provider should match expected")
		})
	}

	userProvider, _, ok := mirrorProviderForUser(models.ProviderGemini, models.GeminiConfig{APIKey: "gemini"})
	require.True(t, ok, "mirrorProviderForUser should keep coding providers")
	require.Equal(t, models.ProviderGemini, userProvider, "mirrorProviderForUser should preserve provider")
	_, _, ok = mirrorProviderForUser(models.ProviderSlack, models.SlackConfig{AccessToken: "xoxb"})
	require.False(t, ok, "mirrorProviderForUser should skip non-coding providers")

	require.NoError(t, NoopMirror().MirrorOrgCredential(context.Background(), models.OrgCredential{}, models.OpenAIConfig{}), "noop mirror should ignore org upsert")
	require.NoError(t, NoopMirror().MirrorOrgCredentialDelete(context.Background(), uuid.New()), "noop mirror should ignore org delete")
	require.NoError(t, NoopMirror().MirrorOrgCredentialDisable(context.Background(), uuid.New()), "noop mirror should ignore org disable")
	require.NoError(t, NoopMirror().MirrorUserCredential(context.Background(), models.UserCredential{}, models.OpenAIConfig{}), "noop mirror should ignore user upsert")
	require.NoError(t, NoopMirror().MirrorUserCredentialDelete(context.Background(), uuid.New()), "noop mirror should ignore user delete")
	require.NoError(t, NoopMirror().MirrorUserCredentialDisable(context.Background(), uuid.New()), "noop mirror should ignore user disable")
}

// captureBytes returns a pgxmock argument matcher that records the bytes
// passed in this slot into out. Used by the mirror tests to inspect the
// re-encrypted config without needing access to a fixed key.
func captureBytes(out *[]byte) pgxmock.Argument {
	return &bytesCaptor{out: out}
}

type bytesCaptor struct {
	out *[]byte
}

func (c *bytesCaptor) Match(arg any) bool {
	b, ok := arg.([]byte)
	if !ok {
		return false
	}
	*c.out = b
	return true
}
