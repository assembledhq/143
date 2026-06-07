package db

import (
	"context"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

var codingCredentialTestColumns = []string{
	"id", "version_id", "org_id", "user_id", "provider", "label", "config", "priority", "status", "created_by", "last_verified_at", "rate_limited_until", "rate_limited_observed_at", "rate_limit_message", "team_default_origin_user_id", "active", "created_at", "updated_at",
}

var codingCredentialSnapshotColumns = []string{
	"id", "version_id", "org_id", "user_id", "provider", "label", "config", "priority", "config_status", "created_by", "team_default_origin_user_id", "created_at", "runtime_status", "last_verified_at", "rate_limited_until", "rate_limited_observed_at", "rate_limit_message",
}

func TestCodingCredentialsColumnsProjectsRuntimeUpdatedAt(t *testing.T) {
	t.Parallel()

	require.Contains(t, codingCredentialsColumns,
		"GREATEST(cc.updated_at, rt.created_at) AS updated_at",
		"credential reads should expose runtime-state changes through updated_at")
}

func encryptedCodingConfig(t *testing.T, store *CodingCredentialStore, cfg models.ProviderConfig) []byte {
	t.Helper()

	data, err := store.marshalAndEncrypt(cfg)
	require.NoError(t, err, "test config should marshal and encrypt")
	return data
}

func codingCredentialRow(t *testing.T, store *CodingCredentialStore, orgID uuid.UUID, userID *uuid.UUID, id uuid.UUID, provider models.ProviderName, cfg models.ProviderConfig, priority int, status models.CodingCredentialRowStatus) []any {
	t.Helper()

	now := time.Now().UTC()
	return []any{
		id,
		uuid.New(),
		orgID,
		userID,
		string(provider),
		"Test credential",
		encryptedCodingConfig(t, store, cfg),
		priority,
		status,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		true,
		now,
		now,
	}
}

func codingCredentialRowWithRateLimit(t *testing.T, store *CodingCredentialStore, orgID uuid.UUID, userID *uuid.UUID, id uuid.UUID, provider models.ProviderName, cfg models.ProviderConfig, priority int, rateLimitedUntil time.Time) []any {
	t.Helper()

	row := codingCredentialRow(t, store, orgID, userID, id, provider, cfg, priority, models.CodingCredentialStatusActive)
	observedAt := rateLimitedUntil.Add(-time.Minute)
	message := "try again later"
	row[11] = &rateLimitedUntil
	row[12] = &observedAt
	row[13] = &message
	return row
}

func addCodingCredentialRow(rows *pgxmock.Rows, values []any) *pgxmock.Rows {
	return rows.AddRow(values...)
}

func newMockCodingCredentialStore(t *testing.T) (*CodingCredentialStore, pgxmock.PgxPoolIface) {
	t.Helper()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	store := NewCodingCredentialStore(mock, nil)
	return store, mock
}

func codingAnyArgs(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = pgxmock.AnyArg()
	}
	return out
}

func codingCredentialSnapshotRow(t *testing.T, store *CodingCredentialStore, scope models.Scope, id uuid.UUID, provider models.ProviderName, status models.CodingCredentialRowStatus) []any {
	t.Helper()

	now := time.Now().UTC()
	return []any{
		id,
		uuid.New(),
		scope.OrgID,
		scope.UserID,
		string(provider),
		"Test credential",
		encryptedCodingConfig(t, store, models.OpenAIConfig{APIKey: "sk-openai-123456"}),
		1,
		status,
		nil,
		nil,
		now,
		status,
		nil,
		nil,
		nil,
		nil,
	}
}

// fakeClock returns a controllable time source used by the cache TTL tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestResolverCacheTTL(t *testing.T) {
	t.Parallel()

	clk := &fakeClock{now: time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC)}
	cache := newResolverCache(30 * time.Second)
	cache.clock = clk.Now

	orgID := uuid.New()
	userID := uuid.New()
	provider := models.ProviderAnthropicSubscription

	val := []models.DecryptedCodingCredential{{ID: uuid.New(), OrgID: orgID, Provider: provider}}
	cache.put(orgID, &userID, provider, val)

	got, ok := cache.get(orgID, &userID, provider)
	if !ok || len(got) != 1 {
		t.Fatalf("expected fresh hit, got ok=%v len=%d", ok, len(got))
	}

	// Within TTL — still hits.
	clk.Advance(15 * time.Second)
	if _, ok := cache.get(orgID, &userID, provider); !ok {
		t.Fatalf("expected hit at 15s, got miss")
	}

	// Past TTL — miss.
	clk.Advance(20 * time.Second) // total 35s
	if _, ok := cache.get(orgID, &userID, provider); ok {
		t.Fatalf("expected miss at 35s, got hit")
	}
}

func TestResolverCacheReturnsCopy(t *testing.T) {
	t.Parallel()

	cache := newResolverCache(30 * time.Second)
	orgID := uuid.New()
	provider := models.ProviderOpenAISubscription

	id1 := uuid.New()
	cache.put(orgID, nil, provider, []models.DecryptedCodingCredential{{ID: id1}})

	got, ok := cache.get(orgID, nil, provider)
	if !ok {
		t.Fatalf("expected hit")
	}
	// Mutating the returned slice must not poison the cache.
	got[0].ID = uuid.New()
	again, _ := cache.get(orgID, nil, provider)
	if again[0].ID != id1 {
		t.Fatalf("cache returned mutable reference; got %v want %v", again[0].ID, id1)
	}
}

func TestResolverCacheInvalidateScope(t *testing.T) {
	t.Parallel()

	cache := newResolverCache(30 * time.Second)
	orgID := uuid.New()
	userID := uuid.New()

	val := []models.DecryptedCodingCredential{{ID: uuid.New()}}
	cache.put(orgID, &userID, models.ProviderAnthropicSubscription, val)
	cache.put(orgID, nil, models.ProviderAnthropicSubscription, val)
	cache.put(orgID, &userID, models.ProviderOpenAISubscription, val)

	cache.invalidateOrg(orgID, models.ProviderAnthropicSubscription)

	if _, ok := cache.get(orgID, &userID, models.ProviderAnthropicSubscription); ok {
		t.Fatalf("expected personal anthropic_subscription invalidated")
	}
	if _, ok := cache.get(orgID, nil, models.ProviderAnthropicSubscription); ok {
		t.Fatalf("expected org anthropic_subscription invalidated")
	}
	if _, ok := cache.get(orgID, &userID, models.ProviderOpenAISubscription); !ok {
		t.Fatalf("expected openai_subscription untouched")
	}
}

// TestResolverCacheInvalidateUser locks the personal-scope Reorder/Move
// invalidation behavior: a personal stack mutation should drop only the
// requesting user's entries, leaving every other user's resolved view and
// the org-only entries intact.
func TestResolverCacheInvalidateUser(t *testing.T) {
	t.Parallel()

	cache := newResolverCache(30 * time.Second)
	orgID := uuid.New()
	userA := uuid.New()
	userB := uuid.New()

	val := []models.DecryptedCodingCredential{{ID: uuid.New()}}
	cache.put(orgID, &userA, models.ProviderAnthropicSubscription, val)
	cache.put(orgID, &userA, models.ProviderOpenAISubscription, val)
	cache.put(orgID, &userB, models.ProviderAnthropicSubscription, val)
	cache.put(orgID, nil, models.ProviderAnthropicSubscription, val)

	cache.invalidateUser(orgID, userA)

	if _, ok := cache.get(orgID, &userA, models.ProviderAnthropicSubscription); ok {
		t.Fatalf("expected userA anthropic_subscription invalidated")
	}
	if _, ok := cache.get(orgID, &userA, models.ProviderOpenAISubscription); ok {
		t.Fatalf("expected userA openai_subscription invalidated")
	}
	if _, ok := cache.get(orgID, &userB, models.ProviderAnthropicSubscription); !ok {
		t.Fatalf("expected userB entry untouched by userA invalidation")
	}
	if _, ok := cache.get(orgID, nil, models.ProviderAnthropicSubscription); !ok {
		t.Fatalf("expected org entry untouched by personal invalidation")
	}
}

func TestHealthCacheShedAndExpire(t *testing.T) {
	t.Parallel()

	clk := &fakeClock{now: time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC)}
	hc := newHealthCache(60 * time.Second)
	hc.clock = clk.Now

	id := uuid.New()
	if hc.isShed(id) {
		t.Fatalf("fresh id should not be shed")
	}

	hc.shed(id)
	if !hc.isShed(id) {
		t.Fatalf("expected shed after marker")
	}

	clk.Advance(30 * time.Second)
	if !hc.isShed(id) {
		t.Fatalf("expected shed at 30s")
	}

	clk.Advance(40 * time.Second) // total 70s
	if hc.isShed(id) {
		t.Fatalf("expected expiry past TTL")
	}
}

func TestHealthCacheFilter(t *testing.T) {
	t.Parallel()

	hc := newHealthCache(60 * time.Second)
	a := uuid.New()
	b := uuid.New()
	c := uuid.New()
	hc.shed(b)

	creds := []models.DecryptedCodingCredential{{ID: a}, {ID: b}, {ID: c}}
	got := hc.filter(creds)
	if len(got) != 2 {
		t.Fatalf("expected 2 eligible, got %d", len(got))
	}
	if got[0].ID == b || got[1].ID == b {
		t.Fatalf("shed credential leaked through filter")
	}
}

func TestGroupByPriorityAndScope(t *testing.T) {
	t.Parallel()

	uid := uuid.New()
	personal := func(p int) models.DecryptedCodingCredential {
		return models.DecryptedCodingCredential{ID: uuid.New(), UserID: &uid, Priority: p}
	}
	org := func(p int) models.DecryptedCodingCredential {
		return models.DecryptedCodingCredential{ID: uuid.New(), UserID: nil, Priority: p}
	}

	// Resolver order: personal (priority 1, 1, 2) then org (1, 1).
	creds := []models.DecryptedCodingCredential{
		personal(1), personal(1), personal(2),
		org(1), org(1),
	}
	tiers := groupByPriorityAndScope(creds)
	if len(tiers) != 3 {
		t.Fatalf("expected 3 tiers, got %d", len(tiers))
	}
	if len(tiers[0]) != 2 || tiers[0][0].UserID == nil {
		t.Fatalf("tier 0 should be 2 personal rows, got %+v", tiers[0])
	}
	if len(tiers[1]) != 1 || tiers[1][0].Priority != 2 || tiers[1][0].UserID == nil {
		t.Fatalf("tier 1 should be 1 personal priority-2 row, got %+v", tiers[1])
	}
	if len(tiers[2]) != 2 || tiers[2][0].UserID != nil {
		t.Fatalf("tier 2 should be 2 org priority-1 rows, got %+v", tiers[2])
	}
}

func TestGroupByPriorityAndScopeEmpty(t *testing.T) {
	t.Parallel()
	if got := groupByPriorityAndScope(nil); got != nil {
		t.Fatalf("expected nil for empty input, got %v", got)
	}
}

func TestSameUserPointer(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	other := uuid.New()
	cases := []struct {
		name string
		a, b *uuid.UUID
		want bool
	}{
		{"both nil", nil, nil, true},
		{"left nil", nil, &id, false},
		{"right nil", &id, nil, false},
		{"equal", &id, &id, true},
		{"distinct", &id, &other, false},
	}
	for _, tc := range cases {
		if got := sameUserPointer(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: sameUserPointer(%v,%v)=%v want %v", tc.name, tc.a, tc.b, got, tc.want)
		}
	}
}

func TestCodingCredentialStoreLookupMethods(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		argCount  int
		call      func(ctx context.Context, store *CodingCredentialStore, scope models.Scope, provider models.ProviderName, id uuid.UUID) ([]models.DecryptedCodingCredential, error)
		setupRows func(t *testing.T, store *CodingCredentialStore, orgID uuid.UUID, userID *uuid.UUID, id uuid.UUID) *pgxmock.Rows
	}{
		{
			name:     "get",
			argCount: 3,
			call: func(ctx context.Context, store *CodingCredentialStore, scope models.Scope, provider models.ProviderName, id uuid.UUID) ([]models.DecryptedCodingCredential, error) {
				got, err := store.Get(ctx, scope, id)
				if err != nil {
					return nil, err
				}
				return []models.DecryptedCodingCredential{*got}, nil
			},
			setupRows: func(t *testing.T, store *CodingCredentialStore, orgID uuid.UUID, userID *uuid.UUID, id uuid.UUID) *pgxmock.Rows {
				return addCodingCredentialRow(
					pgxmock.NewRows(codingCredentialTestColumns),
					codingCredentialRow(t, store, orgID, userID, id, models.ProviderOpenAI, models.OpenAIConfig{APIKey: "sk-openai-123456"}, 1, models.CodingCredentialStatusActive),
				)
			},
		},
		{
			name:     "get by provider and label",
			argCount: 4,
			call: func(ctx context.Context, store *CodingCredentialStore, scope models.Scope, provider models.ProviderName, id uuid.UUID) ([]models.DecryptedCodingCredential, error) {
				got, err := store.GetByProviderAndLabel(ctx, scope, provider, "Test credential")
				if err != nil {
					return nil, err
				}
				return []models.DecryptedCodingCredential{*got}, nil
			},
			setupRows: func(t *testing.T, store *CodingCredentialStore, orgID uuid.UUID, userID *uuid.UUID, id uuid.UUID) *pgxmock.Rows {
				return addCodingCredentialRow(
					pgxmock.NewRows(codingCredentialTestColumns),
					codingCredentialRow(t, store, orgID, userID, id, models.ProviderOpenAI, models.OpenAIConfig{APIKey: "sk-openai-123456"}, 1, models.CodingCredentialStatusActive),
				)
			},
		},
		{
			name:     "list by scope",
			argCount: 2,
			call: func(ctx context.Context, store *CodingCredentialStore, scope models.Scope, _ models.ProviderName, _ uuid.UUID) ([]models.DecryptedCodingCredential, error) {
				return store.ListByScope(ctx, scope)
			},
			setupRows: func(t *testing.T, store *CodingCredentialStore, orgID uuid.UUID, userID *uuid.UUID, id uuid.UUID) *pgxmock.Rows {
				return addCodingCredentialRow(
					pgxmock.NewRows(codingCredentialTestColumns),
					codingCredentialRow(t, store, orgID, userID, id, models.ProviderOpenAI, models.OpenAIConfig{APIKey: "sk-openai-123456"}, 1, models.CodingCredentialStatusActive),
				)
			},
		},
		{
			name:     "list by provider",
			argCount: 3,
			call: func(ctx context.Context, store *CodingCredentialStore, scope models.Scope, provider models.ProviderName, _ uuid.UUID) ([]models.DecryptedCodingCredential, error) {
				return store.ListByProvider(ctx, scope, provider)
			},
			setupRows: func(t *testing.T, store *CodingCredentialStore, orgID uuid.UUID, userID *uuid.UUID, id uuid.UUID) *pgxmock.Rows {
				return addCodingCredentialRow(
					pgxmock.NewRows(codingCredentialTestColumns),
					codingCredentialRow(t, store, orgID, userID, id, models.ProviderOpenAI, models.OpenAIConfig{APIKey: "sk-openai-123456"}, 1, models.CodingCredentialStatusActive),
				)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, mock := newMockCodingCredentialStore(t)
			defer mock.Close()

			orgID := uuid.New()
			userID := uuid.New()
			id := uuid.New()
			provider := models.ProviderOpenAI
			scope := models.Scope{OrgID: orgID, UserID: &userID}
			mock.ExpectQuery("FROM coding_credentials").
				WithArgs(codingAnyArgs(tt.argCount)...).
				WillReturnRows(tt.setupRows(t, store, orgID, &userID, id))

			got, err := tt.call(context.Background(), store, scope, provider, id)

			require.NoError(t, err, "lookup method should not return an error")
			require.Len(t, got, 1, "lookup method should return one credential")
			require.Equal(t, id, got[0].ID, "lookup method should return the expected credential id")
			require.Equal(t, orgID, got[0].OrgID, "lookup method should preserve the org id")
			require.Equal(t, userID, *got[0].UserID, "lookup method should preserve the user id")
			require.Equal(t, provider, got[0].Provider, "lookup method should preserve the provider")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestCodingCredentialStoreListResolvableAndPickRunnable(t *testing.T) {
	t.Parallel()

	store, mock := newMockCodingCredentialStore(t)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	personalID := uuid.New()
	orgCredID := uuid.New()
	provider := models.ProviderAnthropicSubscription
	scope := models.Scope{OrgID: orgID, UserID: &userID}

	mock.ExpectQuery("FROM coding_credentials").
		WithArgs(codingAnyArgs(3)...).
		WillReturnRows(addCodingCredentialRow(
			pgxmock.NewRows(codingCredentialTestColumns),
			codingCredentialRow(t, store, orgID, &userID, personalID, provider, models.AnthropicSubscriptionConfig{AccessToken: "tok", RefreshToken: "refresh"}, 1, models.CodingCredentialStatusActive),
		))
	mock.ExpectQuery("FROM coding_credentials").
		WithArgs(codingAnyArgs(2)...).
		WillReturnRows(addCodingCredentialRow(
			pgxmock.NewRows(codingCredentialTestColumns),
			codingCredentialRow(t, store, orgID, nil, orgCredID, provider, models.AnthropicSubscriptionConfig{AccessToken: "tok2", RefreshToken: "refresh2"}, 1, models.CodingCredentialStatusActive),
		))

	resolved, err := store.ListResolvable(context.Background(), orgID, &userID, provider)
	require.NoError(t, err, "ListResolvable should return resolver rows")
	require.Equal(t, []uuid.UUID{personalID, orgCredID}, []uuid.UUID{resolved[0].ID, resolved[1].ID}, "ListResolvable should order personal rows before org fallback")

	store.MarkRateLimited(personalID)
	picked, err := store.PickRunnable(context.Background(), scope, provider)
	require.NoError(t, err, "PickRunnable should fall back to the org row when the personal row is shed")
	require.Equal(t, orgCredID, picked.ID, "PickRunnable should skip rate-limited rows")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// TestListResolvableMulti_BulkFetchAndCacheReuse asserts that the bulk
// resolver:
//   - issues exactly one query per scope half (personal + org) regardless of
//     how many providers are requested,
//   - returns rows bucketed by provider with the personal half ordered
//     before the org half within each bucket,
//   - serves a subsequent multi-call entirely from the resolver cache.
//
// Together this is the regression guard that listResolved on the account
// settings page does not regress to N round trips on a cold cache.
func TestListResolvableMulti_BulkFetchAndCacheReuse(t *testing.T) {
	t.Parallel()

	store, mock := newMockCodingCredentialStore(t)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	personalAnthropicID := uuid.New()
	personalOpenAIID := uuid.New()
	orgAnthropicID := uuid.New()
	providers := []models.ProviderName{models.ProviderAnthropic, models.ProviderOpenAI}

	personalRows := pgxmock.NewRows(codingCredentialTestColumns)
	personalRows = addCodingCredentialRow(personalRows,
		codingCredentialRow(t, store, orgID, &userID, personalAnthropicID, models.ProviderAnthropic, models.AnthropicConfig{APIKey: "sk-ant-personal-1234"}, 1, models.CodingCredentialStatusActive),
	)
	personalRows = addCodingCredentialRow(personalRows,
		codingCredentialRow(t, store, orgID, &userID, personalOpenAIID, models.ProviderOpenAI, models.OpenAIConfig{APIKey: "sk-openai-personal"}, 1, models.CodingCredentialStatusActive),
	)
	orgRows := pgxmock.NewRows(codingCredentialTestColumns)
	orgRows = addCodingCredentialRow(orgRows,
		codingCredentialRow(t, store, orgID, nil, orgAnthropicID, models.ProviderAnthropic, models.AnthropicConfig{APIKey: "sk-ant-org-1234"}, 1, models.CodingCredentialStatusActive),
	)

	mock.ExpectQuery(`FROM coding_credentials`).
		WithArgs(codingAnyArgs(3)...).
		WillReturnRows(personalRows)
	mock.ExpectQuery(`FROM coding_credentials`).
		WithArgs(codingAnyArgs(2)...).
		WillReturnRows(orgRows)

	got, err := store.ListResolvableMulti(context.Background(), orgID, &userID, providers)
	require.NoError(t, err, "ListResolvableMulti should fold per-scope queries into one each")
	require.Len(t, got, 2, "ListResolvableMulti should return one bucket per requested provider")
	require.Equal(t, []uuid.UUID{personalAnthropicID, orgAnthropicID},
		[]uuid.UUID{got[models.ProviderAnthropic][0].ID, got[models.ProviderAnthropic][1].ID},
		"anthropic bucket should be personal-then-org ordered")
	require.Len(t, got[models.ProviderOpenAI], 1, "openai bucket should contain only the personal row")
	require.Equal(t, personalOpenAIID, got[models.ProviderOpenAI][0].ID, "openai bucket should reflect the personal pick")

	// Second call with overlapping providers must hit the resolver cache and
	// issue zero additional queries — pgxmock would fail this if it did.
	got2, err := store.ListResolvableMulti(context.Background(), orgID, &userID, providers)
	require.NoError(t, err, "ListResolvableMulti second call should hit cache")
	require.Len(t, got2[models.ProviderAnthropic], 2, "cache should preserve anthropic bucket length")
	require.NoError(t, mock.ExpectationsWereMet(), "ListResolvableMulti must use a single query per scope half")
}

// TestListResolvableMulti_PerBucketPriorityOrder locks the contract that
// rows within a single provider bucket are returned in (priority, created_at)
// order. The SQL ORDER BY emits rows already sorted; the per-row append into
// `bucketed[row.Provider]` preserves that order. This test guards against a
// future refactor that batches/buckets rows out of SQL order (e.g. parallel
// scope queries that interleave per-provider).
func TestListResolvableMulti_PerBucketPriorityOrder(t *testing.T) {
	t.Parallel()

	store, mock := newMockCodingCredentialStore(t)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	personalP1 := uuid.New()
	personalP2 := uuid.New()
	personalP3 := uuid.New()
	providers := []models.ProviderName{models.ProviderAnthropic}

	personalRows := pgxmock.NewRows(codingCredentialTestColumns)
	// Add rows in the same order the SQL would, after ORDER BY priority,
	// created_at. The bucketing must preserve this order in the returned
	// slice — callers downstream (e.g. PickRunnableMulti's tier walker)
	// depend on it to enforce priority semantics.
	personalRows = addCodingCredentialRow(personalRows,
		codingCredentialRow(t, store, orgID, &userID, personalP1, models.ProviderAnthropic, models.AnthropicConfig{APIKey: "sk-ant-p1"}, 1, models.CodingCredentialStatusActive),
	)
	personalRows = addCodingCredentialRow(personalRows,
		codingCredentialRow(t, store, orgID, &userID, personalP2, models.ProviderAnthropic, models.AnthropicConfig{APIKey: "sk-ant-p2"}, 2, models.CodingCredentialStatusActive),
	)
	personalRows = addCodingCredentialRow(personalRows,
		codingCredentialRow(t, store, orgID, &userID, personalP3, models.ProviderAnthropic, models.AnthropicConfig{APIKey: "sk-ant-p3"}, 3, models.CodingCredentialStatusActive),
	)
	orgRows := pgxmock.NewRows(codingCredentialTestColumns)

	mock.ExpectQuery(`FROM coding_credentials`).
		WithArgs(codingAnyArgs(3)...).
		WillReturnRows(personalRows)
	mock.ExpectQuery(`FROM coding_credentials`).
		WithArgs(codingAnyArgs(2)...).
		WillReturnRows(orgRows)

	got, err := store.ListResolvableMulti(context.Background(), orgID, &userID, providers)
	require.NoError(t, err)
	require.Len(t, got[models.ProviderAnthropic], 3)
	require.Equal(t,
		[]uuid.UUID{personalP1, personalP2, personalP3},
		[]uuid.UUID{got[models.ProviderAnthropic][0].ID, got[models.ProviderAnthropic][1].ID, got[models.ProviderAnthropic][2].ID},
		"bucket must preserve SQL ORDER BY priority within a single provider")
}

func TestCodingCredentialStorePickRunnableMulti_MergesProvidersByScopeBeforeOrgFallback(t *testing.T) {
	t.Parallel()

	store, mock := newMockCodingCredentialStore(t)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	personalSubID := uuid.New()
	orgAPIKeyID := uuid.New()
	scope := models.Scope{OrgID: orgID, UserID: &userID}
	providers := []models.ProviderName{models.ProviderAnthropic, models.ProviderAnthropicSubscription}

	personalRows := pgxmock.NewRows(codingCredentialTestColumns)
	personalRows = addCodingCredentialRow(personalRows,
		codingCredentialRow(t, store, orgID, &userID, personalSubID, models.ProviderAnthropicSubscription, models.AnthropicSubscriptionConfig{AccessToken: "personal-token", RefreshToken: "personal-refresh"}, 10, models.CodingCredentialStatusActive),
	)
	orgRows := pgxmock.NewRows(codingCredentialTestColumns)
	orgRows = addCodingCredentialRow(orgRows,
		codingCredentialRow(t, store, orgID, nil, orgAPIKeyID, models.ProviderAnthropic, models.AnthropicConfig{APIKey: "org-api-key"}, 1, models.CodingCredentialStatusActive),
	)

	mock.ExpectQuery(`FROM coding_credentials`).
		WithArgs(codingAnyArgs(3)...).
		WillReturnRows(personalRows)
	mock.ExpectQuery(`FROM coding_credentials`).
		WithArgs(codingAnyArgs(2)...).
		WillReturnRows(orgRows)

	picked, err := store.PickRunnableMulti(context.Background(), scope, providers)
	require.NoError(t, err, "PickRunnableMulti should pick a credential from the merged provider set")
	require.Equal(t, personalSubID, picked.ID, "PickRunnableMulti should choose a personal subscription before an org API-key fallback")

	store.MarkRateLimited(personalSubID)
	picked, err = store.PickRunnableMulti(context.Background(), scope, providers)
	require.NoError(t, err, "PickRunnableMulti should continue to org fallback after the personal candidate is shed")
	require.Equal(t, orgAPIKeyID, picked.ID, "PickRunnableMulti should fall back to the org API key only after personal rows are ineligible")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodingCredentialStorePickRunnableMulti_SkipsPersistedRateLimitedProviderTwin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		providers        []models.ProviderName
		personalProvider models.ProviderName
		personalConfig   models.ProviderConfig
		orgProvider      models.ProviderName
		orgConfig        models.ProviderConfig
	}{
		{
			name:             "codex subscription falls back to org openai api key",
			providers:        []models.ProviderName{models.ProviderOpenAI, models.ProviderOpenAISubscription},
			personalProvider: models.ProviderOpenAISubscription,
			personalConfig:   models.OpenAISubscriptionConfig{AccessToken: "personal-token", RefreshToken: "personal-refresh", ExpiresAt: time.Now().Add(time.Hour)},
			orgProvider:      models.ProviderOpenAI,
			orgConfig:        models.OpenAIConfig{APIKey: "sk-openai-org"},
		},
		{
			name:             "claude subscription falls back to org anthropic api key",
			providers:        []models.ProviderName{models.ProviderAnthropic, models.ProviderAnthropicSubscription},
			personalProvider: models.ProviderAnthropicSubscription,
			personalConfig:   models.AnthropicSubscriptionConfig{AccessToken: "personal-token", RefreshToken: "personal-refresh", ExpiresAt: time.Now().Add(time.Hour)},
			orgProvider:      models.ProviderAnthropic,
			orgConfig:        models.AnthropicConfig{APIKey: "sk-ant-org"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, mock := newMockCodingCredentialStore(t)
			defer mock.Close()

			now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
			store.SetClock(func() time.Time { return now })
			orgID := uuid.New()
			userID := uuid.New()
			personalID := uuid.New()
			orgIDCred := uuid.New()
			scope := models.Scope{OrgID: orgID, UserID: &userID}

			personalRows := pgxmock.NewRows(codingCredentialTestColumns)
			personalRows = addCodingCredentialRow(personalRows,
				codingCredentialRowWithRateLimit(t, store, orgID, &userID, personalID, tt.personalProvider, tt.personalConfig, 1, now.Add(time.Hour)),
			)
			orgRows := pgxmock.NewRows(codingCredentialTestColumns)
			orgRows = addCodingCredentialRow(orgRows,
				codingCredentialRow(t, store, orgID, nil, orgIDCred, tt.orgProvider, tt.orgConfig, 1, models.CodingCredentialStatusActive),
			)

			mock.ExpectQuery(`FROM coding_credentials`).
				WithArgs(codingAnyArgs(3)...).
				WillReturnRows(personalRows)
			mock.ExpectQuery(`FROM coding_credentials`).
				WithArgs(codingAnyArgs(2)...).
				WillReturnRows(orgRows)

			resolved, err := store.ListResolvableMulti(context.Background(), orgID, &userID, tt.providers)
			require.NoError(t, err, "ListResolvableMulti should return rate-limited rows for visibility")
			require.Equal(t, personalID, resolved[tt.personalProvider][0].ID, "resolver should keep the rate-limited personal row visible")

			picked, err := store.PickRunnableMulti(context.Background(), scope, tt.providers)
			require.NoError(t, err, "PickRunnableMulti should skip persisted rate-limited credentials")
			require.Equal(t, orgIDCred, picked.ID, "PickRunnableMulti should fall back to the org credential")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestCodingCredentialStorePickRunnable_PersistedRateLimitExpiry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		rateLimitedUntil  time.Time
		expectedPickedID  string
		expectedErrIsShed bool
	}{
		{
			name:             "expired rate limit is runnable",
			rateLimitedUntil: time.Date(2026, 5, 13, 11, 59, 0, 0, time.UTC),
			expectedPickedID: "first",
		},
		{
			name:              "future rate limit skips to next priority",
			rateLimitedUntil:  time.Date(2026, 5, 13, 12, 30, 0, 0, time.UTC),
			expectedPickedID:  "second",
			expectedErrIsShed: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, mock := newMockCodingCredentialStore(t)
			defer mock.Close()

			now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
			store.SetClock(func() time.Time { return now })
			orgID := uuid.New()
			firstID := uuid.New()
			secondID := uuid.New()
			scope := models.Scope{OrgID: orgID}

			rows := pgxmock.NewRows(codingCredentialTestColumns)
			rows = addCodingCredentialRow(rows,
				codingCredentialRowWithRateLimit(t, store, orgID, nil, firstID, models.ProviderGemini, models.GeminiConfig{APIKey: "gemini-first"}, 1, tt.rateLimitedUntil),
			)
			rows = addCodingCredentialRow(rows,
				codingCredentialRow(t, store, orgID, nil, secondID, models.ProviderGemini, models.GeminiConfig{APIKey: "gemini-second"}, 2, models.CodingCredentialStatusActive),
			)

			mock.ExpectQuery(`FROM coding_credentials`).
				WithArgs(codingAnyArgs(2)...).
				WillReturnRows(rows)

			picked, err := store.PickRunnable(context.Background(), scope, models.ProviderGemini)
			require.NoError(t, err, "PickRunnable should return an available credential")
			expected := map[string]uuid.UUID{"first": firstID, "second": secondID}[tt.expectedPickedID]
			require.Equal(t, expected, picked.ID, "PickRunnable should account for persisted rate-limit expiry")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestCodingCredentialStoreOrgScopeBranches(t *testing.T) {
	t.Parallel()

	store, mock := newMockCodingCredentialStore(t)
	defer mock.Close()

	orgID := uuid.New()
	id := uuid.New()
	scope := models.Scope{OrgID: orgID}
	provider := models.ProviderOpenAI
	row := codingCredentialRow(t, store, orgID, nil, id, provider, models.OpenAIConfig{APIKey: "sk-openai-123456"}, 1, models.CodingCredentialStatusActive)

	mock.ExpectQuery("FROM coding_credentials").
		WithArgs(codingAnyArgs(1)...).
		WillReturnRows(pgxmock.NewRows(codingCredentialTestColumns).AddRow(row...))
	gotScope, err := store.ListByScope(context.Background(), scope)
	require.NoError(t, err, "ListByScope should support org scope")
	require.Equal(t, id, gotScope[0].ID, "ListByScope should return the org credential")

	mock.ExpectQuery("FROM coding_credentials").
		WithArgs(codingAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(codingCredentialTestColumns).AddRow(row...))
	gotProvider, err := store.ListByProvider(context.Background(), scope, provider)
	require.NoError(t, err, "ListByProvider should support org scope")
	require.Equal(t, id, gotProvider[0].ID, "ListByProvider should return the org credential")

	mock.ExpectQuery("FROM coding_credentials").
		WithArgs(codingAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(codingCredentialTestColumns).AddRow(row...))
	resolved, err := store.ListResolvable(context.Background(), orgID, nil, provider)
	require.NoError(t, err, "ListResolvable should support org-only resolution")
	require.Equal(t, id, resolved[0].ID, "ListResolvable should return org rows when no user id is supplied")

	store.MarkAuthRejected(id)
	_, err = store.PickRunnable(context.Background(), scope, provider)
	require.ErrorIs(t, err, ErrAllCredentialsShed, "PickRunnable should distinguish all-shed from not-found")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodingCredentialStoreConfigurationHelpers(t *testing.T) {
	t.Parallel()

	store, mock := newMockCodingCredentialStore(t)
	defer mock.Close()

	called := false
	store.SetMirrorLogger(func(format string, args ...any) {
		called = true
		require.Equal(t, "hello %s", format, "mirror logger should receive the format string")
		require.Equal(t, []any{"world"}, args, "mirror logger should receive arguments")
	})
	store.mirrorWarn("hello %s", "world")
	require.True(t, called, "mirrorWarn should call the configured logger")

	store.SetRNG(rand.New(rand.NewPCG(1, 2)))
	store.SetClock(func() time.Time { return time.Unix(10, 0) })
	id := uuid.New()
	store.MarkAuthRejected(id)
	require.True(t, store.health.isShed(id), "MarkAuthRejected should shed the credential")

	require.Contains(t, (&ErrCodingCredentialLabelTaken{Label: "Codex", ExistingStatus: models.CodingCredentialStatusActive}).Error(), "already connected", "active label conflicts should render connected copy")
	require.Contains(t, (&ErrCodingCredentialLabelTaken{Label: "Codex", ExistingStatus: models.CodingCredentialStatusInvalid}).Error(), "invalid", "invalid label conflicts should render invalid copy")
	require.Contains(t, (&ErrCodingCredentialLabelTaken{Label: "Codex"}).Error(), "already exists", "unknown label conflicts should render generic copy")
	require.Equal(t, "org", scopePtrKey(nil), "nil scope pointer should use org key")
}

func TestCodingCredentialStoreMutations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(t *testing.T, mock pgxmock.PgxPoolIface, store *CodingCredentialStore, scope models.Scope, id uuid.UUID)
		call  func(ctx context.Context, store *CodingCredentialStore, scope models.Scope, id uuid.UUID) error
	}{
		{
			name: "create",
			setup: func(t *testing.T, mock pgxmock.PgxPoolIface, store *CodingCredentialStore, scope models.Scope, id uuid.UUID) {
				mock.ExpectBegin()
				mock.ExpectExec("pg_advisory_xact_lock").
					WithArgs(codingAnyArgs(1)...).
					WillReturnResult(pgxmock.NewResult("SELECT", 1))
				mock.ExpectQuery("SELECT COALESCE").
					WithArgs(codingAnyArgs(2)...).
					WillReturnRows(pgxmock.NewRows([]string{"next_priority"}).AddRow(1))
				mock.ExpectQuery(`FROM coding_credentials cc\s+JOIN coding_credential_runtime_state`).
					WithArgs(codingAnyArgs(4)...).
					WillReturnRows(pgxmock.NewRows(codingCredentialSnapshotColumns))
				mock.ExpectExec(`INSERT INTO coding_credentials`).
					WithArgs(codingAnyArgs(11)...).
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
				mock.ExpectExec(`INSERT INTO coding_credential_runtime_state`).
					WithArgs(codingAnyArgs(8)...).
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
				mock.ExpectCommit()
			},
			call: func(ctx context.Context, store *CodingCredentialStore, scope models.Scope, _ uuid.UUID) error {
				_, err := store.Create(ctx, scope, "Codex", models.OpenAIConfig{APIKey: "sk-openai-123456"}, CreateOpts{})
				return err
			},
		},
		{
			name: "promote pending",
			setup: func(t *testing.T, mock pgxmock.PgxPoolIface, store *CodingCredentialStore, scope models.Scope, id uuid.UUID) {
				expectScopedMutation(t, mock, scope, id, models.ProviderOpenAI)
				mock.ExpectExec(`UPDATE coding_credentials\s+SET active = false`).
					WithArgs(codingAnyArgs(2)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectExec(`INSERT INTO coding_credentials`).
					WithArgs(codingAnyArgs(11)...).
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
				mock.ExpectExec(`UPDATE coding_credential_runtime_state\s+SET active = false`).
					WithArgs(codingAnyArgs(1)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectExec(`INSERT INTO coding_credential_runtime_state`).
					WithArgs(codingAnyArgs(8)...).
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
				mock.ExpectCommit()
			},
			call: func(ctx context.Context, store *CodingCredentialStore, scope models.Scope, id uuid.UUID) error {
				return store.PromotePending(ctx, scope, id, models.OpenAIConfig{APIKey: "sk-openai-123456"})
			},
		},
		{
			name: "update config",
			setup: func(t *testing.T, mock pgxmock.PgxPoolIface, store *CodingCredentialStore, scope models.Scope, id uuid.UUID) {
				expectScopedMutation(t, mock, scope, id, models.ProviderOpenAI)
				mock.ExpectExec(`UPDATE coding_credentials\s+SET active = false`).
					WithArgs(codingAnyArgs(2)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectExec(`INSERT INTO coding_credentials`).
					WithArgs(codingAnyArgs(11)...).
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
				mock.ExpectCommit()
			},
			call: func(ctx context.Context, store *CodingCredentialStore, scope models.Scope, id uuid.UUID) error {
				return store.UpdateConfig(ctx, scope, id, models.OpenAIConfig{APIKey: "sk-openai-abcdef"})
			},
		},
		{
			name: "update config verified writes runtime verification",
			setup: func(t *testing.T, mock pgxmock.PgxPoolIface, store *CodingCredentialStore, scope models.Scope, id uuid.UUID) {
				expectScopedMutation(t, mock, scope, id, models.ProviderOpenAI)
				mock.ExpectExec(`UPDATE coding_credentials\s+SET active = false`).
					WithArgs(codingAnyArgs(2)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectExec(`INSERT INTO coding_credentials`).
					WithArgs(codingAnyArgs(11)...).
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
				mock.ExpectExec(`UPDATE coding_credential_runtime_state\s+SET active = false`).
					WithArgs(codingAnyArgs(1)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectExec(`INSERT INTO coding_credential_runtime_state`).
					WithArgs(codingAnyArgs(8)...).
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
				mock.ExpectCommit()
			},
			call: func(ctx context.Context, store *CodingCredentialStore, scope models.Scope, id uuid.UUID) error {
				return store.UpdateConfigVerified(ctx, scope, id, models.OpenAIConfig{APIKey: "sk-openai-verified"})
			},
		},
		{
			name: "rename",
			setup: func(t *testing.T, mock pgxmock.PgxPoolIface, store *CodingCredentialStore, scope models.Scope, id uuid.UUID) {
				expectScopedMutation(t, mock, scope, id, models.ProviderOpenAI)
				mock.ExpectExec(`UPDATE coding_credentials\s+SET active = false`).
					WithArgs(codingAnyArgs(2)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectExec(`INSERT INTO coding_credentials`).
					WithArgs(codingAnyArgs(11)...).
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
				mock.ExpectCommit()
			},
			call: func(ctx context.Context, store *CodingCredentialStore, scope models.Scope, id uuid.UUID) error {
				return store.Rename(ctx, scope, id, "Renamed")
			},
		},
		{
			name: "update status",
			setup: func(t *testing.T, mock pgxmock.PgxPoolIface, store *CodingCredentialStore, scope models.Scope, id uuid.UUID) {
				expectScopedMutation(t, mock, scope, id, models.ProviderOpenAI)
				mock.ExpectExec(`UPDATE coding_credential_runtime_state\s+SET active = false`).
					WithArgs(codingAnyArgs(1)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectExec(`INSERT INTO coding_credential_runtime_state`).
					WithArgs(codingAnyArgs(8)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectCommit()
			},
			call: func(ctx context.Context, store *CodingCredentialStore, scope models.Scope, id uuid.UUID) error {
				return store.UpdateStatus(ctx, scope, id, models.CodingCredentialStatusInvalid)
			},
		},
		{
			name: "mark rate limited",
			setup: func(t *testing.T, mock pgxmock.PgxPoolIface, store *CodingCredentialStore, scope models.Scope, id uuid.UUID) {
				expectScopedMutation(t, mock, scope, id, models.ProviderOpenAI)
				mock.ExpectExec(`UPDATE coding_credential_runtime_state\s+SET active = false`).
					WithArgs(codingAnyArgs(1)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectExec(`INSERT INTO coding_credential_runtime_state`).
					WithArgs(codingAnyArgs(8)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectCommit()
			},
			call: func(ctx context.Context, store *CodingCredentialStore, scope models.Scope, id uuid.UUID) error {
				return store.MarkRateLimitedForScope(ctx, scope, id, models.CodingCredentialRateLimit{
					Until:   time.Now().Add(time.Hour),
					Message: "try again later",
				})
			},
		},
		{
			name: "clear rate limited",
			setup: func(t *testing.T, mock pgxmock.PgxPoolIface, store *CodingCredentialStore, scope models.Scope, id uuid.UUID) {
				expectScopedMutation(t, mock, scope, id, models.ProviderOpenAI)
				mock.ExpectExec(`UPDATE coding_credential_runtime_state\s+SET active = false`).
					WithArgs(codingAnyArgs(1)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectExec(`INSERT INTO coding_credential_runtime_state`).
					WithArgs(codingAnyArgs(8)...).
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
				mock.ExpectCommit()
			},
			call: func(ctx context.Context, store *CodingCredentialStore, scope models.Scope, id uuid.UUID) error {
				return store.ClearRateLimitedForScope(ctx, scope, id)
			},
		},
		{
			name: "mark verified",
			setup: func(t *testing.T, mock pgxmock.PgxPoolIface, store *CodingCredentialStore, scope models.Scope, id uuid.UUID) {
				expectScopedMutation(t, mock, scope, id, models.ProviderOpenAI)
				mock.ExpectExec(`UPDATE coding_credential_runtime_state\s+SET active = false`).
					WithArgs(codingAnyArgs(1)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectExec(`INSERT INTO coding_credential_runtime_state`).
					WithArgs(codingAnyArgs(8)...).
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
				mock.ExpectCommit()
			},
			call: func(ctx context.Context, store *CodingCredentialStore, scope models.Scope, id uuid.UUID) error {
				return store.MarkVerifiedForScope(ctx, scope, id)
			},
		},
		{
			name: "mark auth rejected",
			setup: func(t *testing.T, mock pgxmock.PgxPoolIface, store *CodingCredentialStore, scope models.Scope, id uuid.UUID) {
				expectScopedMutation(t, mock, scope, id, models.ProviderOpenAI)
				mock.ExpectExec(`UPDATE coding_credential_runtime_state\s+SET active = false`).
					WithArgs(codingAnyArgs(1)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectExec(`INSERT INTO coding_credential_runtime_state`).
					WithArgs(codingAnyArgs(8)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectCommit()
			},
			call: func(ctx context.Context, store *CodingCredentialStore, scope models.Scope, id uuid.UUID) error {
				return store.MarkAuthRejectedForScope(ctx, scope, id)
			},
		},
		{
			name: "disable",
			setup: func(t *testing.T, mock pgxmock.PgxPoolIface, store *CodingCredentialStore, scope models.Scope, id uuid.UUID) {
				expectScopedMutation(t, mock, scope, id, models.ProviderOpenAI)
				mock.ExpectExec(`UPDATE coding_credential_runtime_state\s+SET active = false`).
					WithArgs(codingAnyArgs(1)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectExec(`INSERT INTO coding_credential_runtime_state`).
					WithArgs(codingAnyArgs(8)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectCommit()
			},
			call: func(ctx context.Context, store *CodingCredentialStore, scope models.Scope, id uuid.UUID) error {
				return store.Disable(ctx, scope, id)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, mock := newMockCodingCredentialStore(t)
			defer mock.Close()

			orgID := uuid.New()
			userID := uuid.New()
			id := uuid.New()
			scope := models.Scope{OrgID: orgID, UserID: &userID}
			tt.setup(t, mock, store, scope, id)

			err := tt.call(context.Background(), store, scope, id)

			require.NoError(t, err, "mutation should not return an error")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestCodingCredentialStoreCreateLabelTaken(t *testing.T) {
	t.Parallel()

	store, mock := newMockCodingCredentialStore(t)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	scope := models.Scope{OrgID: orgID, UserID: &userID}

	mock.ExpectBegin()
	mock.ExpectExec("pg_advisory_xact_lock").
		WithArgs(codingAnyArgs(1)...).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT COALESCE").
		WithArgs(codingAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"next_priority"}).AddRow(1))
	mock.ExpectQuery(`FROM coding_credentials cc\s+JOIN coding_credential_runtime_state`).
		WithArgs(codingAnyArgs(4)...).
		WillReturnRows(pgxmock.NewRows(codingCredentialSnapshotColumns).
			AddRow(codingCredentialSnapshotRow(t, store, scope, uuid.New(), models.ProviderOpenAI, models.CodingCredentialStatusActive)...))
	mock.ExpectRollback()

	_, err := store.Create(context.Background(), scope, "Codex", models.OpenAIConfig{APIKey: "sk-openai-123456"}, CreateOpts{})

	var taken *ErrCodingCredentialLabelTaken
	require.ErrorAs(t, err, &taken, "Create should return a typed label conflict")
	require.Equal(t, models.CodingCredentialStatusActive, taken.ExistingStatus, "label conflict should expose the existing status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodingCredentialStoreRenameLabelTaken(t *testing.T) {
	t.Parallel()

	store, mock := newMockCodingCredentialStore(t)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	id := uuid.New()
	scope := models.Scope{OrgID: orgID, UserID: &userID}

	expectScopedMutation(t, mock, scope, id, models.ProviderOpenAI)
	mock.ExpectExec(`UPDATE coding_credentials\s+SET active = false`).
		WithArgs(codingAnyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`INSERT INTO coding_credentials`).
		WithArgs(codingAnyArgs(11)...).
		WillReturnError(&pgconn.PgError{Code: "23505"})
	mock.ExpectRollback()

	err := store.Rename(context.Background(), scope, id, "Taken")

	var taken *ErrCodingCredentialLabelTaken
	require.ErrorAs(t, err, &taken, "Rename should return a typed label conflict on unique violations")
	require.Equal(t, "Taken", taken.Label, "Rename conflict should expose the requested label")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodingCredentialStoreReorderMoveAndJanitor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(t *testing.T, mock pgxmock.PgxPoolIface, scope models.Scope, ids []uuid.UUID)
		call  func(ctx context.Context, store *CodingCredentialStore, scope models.Scope, ids []uuid.UUID) error
	}{
		{
			name: "reorder updates each row",
			setup: func(t *testing.T, mock pgxmock.PgxPoolIface, scope models.Scope, ids []uuid.UUID) {
				mock.ExpectBegin()
				mock.ExpectExec("pg_advisory_xact_lock").
					WithArgs(codingAnyArgs(1)...).
					WillReturnResult(pgxmock.NewResult("SELECT", 1))
				mock.ExpectQuery(`SELECT cc.id\s+FROM coding_credentials cc\s+JOIN coding_credential_runtime_state`).
					WithArgs(codingAnyArgs(2)...).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(ids[0]).AddRow(ids[1]).AddRow(ids[2]))
				for range ids {
					mock.ExpectQuery(`FROM coding_credentials cc\s+JOIN coding_credential_runtime_state`).
						WithArgs(codingAnyArgs(3)...).
						WillReturnRows(pgxmock.NewRows(codingCredentialSnapshotColumns).
							AddRow(codingCredentialSnapshotRow(t, NewCodingCredentialStore(mock, nil), scope, uuid.New(), models.ProviderOpenAI, models.CodingCredentialStatusActive)...))
					mock.ExpectExec(`UPDATE coding_credentials\s+SET active = false`).
						WithArgs(codingAnyArgs(2)...).
						WillReturnResult(pgxmock.NewResult("UPDATE", 1))
					mock.ExpectExec(`INSERT INTO coding_credentials`).
						WithArgs(codingAnyArgs(11)...).
						WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				}
				mock.ExpectCommit()
			},
			call: func(ctx context.Context, store *CodingCredentialStore, scope models.Scope, ids []uuid.UUID) error {
				return store.Reorder(ctx, scope, ids)
			},
		},
		{
			name: "move to top rewrites changed priorities",
			setup: func(t *testing.T, mock pgxmock.PgxPoolIface, scope models.Scope, ids []uuid.UUID) {
				mock.ExpectBegin()
				mock.ExpectExec("pg_advisory_xact_lock").
					WithArgs(codingAnyArgs(1)...).
					WillReturnResult(pgxmock.NewResult("SELECT", 1))
				expectScopedSnapshot(t, mock, scope, ids[2], models.ProviderOpenAI)
				mock.ExpectQuery(`SELECT cc.id\s+FROM coding_credentials cc\s+JOIN coding_credential_runtime_state`).
					WithArgs(codingAnyArgs(2)...).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(ids[0]).AddRow(ids[1]).AddRow(ids[2]))
				mock.ExpectQuery(`SELECT cc.id, cc.priority\s+FROM coding_credentials cc\s+JOIN coding_credential_runtime_state`).
					WithArgs(codingAnyArgs(3)...).
					WillReturnRows(pgxmock.NewRows([]string{"id", "priority"}).AddRow(ids[0], 1).AddRow(ids[1], 2).AddRow(ids[2], 3))
				for range ids {
					mock.ExpectQuery(`FROM coding_credentials cc\s+JOIN coding_credential_runtime_state`).
						WithArgs(codingAnyArgs(3)...).
						WillReturnRows(pgxmock.NewRows(codingCredentialSnapshotColumns).
							AddRow(codingCredentialSnapshotRow(t, NewCodingCredentialStore(mock, nil), scope, uuid.New(), models.ProviderOpenAI, models.CodingCredentialStatusActive)...))
					mock.ExpectExec(`UPDATE coding_credentials\s+SET active = false`).
						WithArgs(codingAnyArgs(2)...).
						WillReturnResult(pgxmock.NewResult("UPDATE", 1))
					mock.ExpectExec(`INSERT INTO coding_credentials`).
						WithArgs(codingAnyArgs(11)...).
						WillReturnResult(pgxmock.NewResult("INSERT", 1))
				}
				mock.ExpectCommit()
			},
			call: func(ctx context.Context, store *CodingCredentialStore, scope models.Scope, ids []uuid.UUID) error {
				return store.Move(ctx, scope, ids[2], models.MoveCodingCredentialInput{ToTop: true})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, mock := newMockCodingCredentialStore(t)
			defer mock.Close()

			orgID := uuid.New()
			userID := uuid.New()
			scope := models.Scope{OrgID: orgID, UserID: &userID}
			ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
			tt.setup(t, mock, scope, ids)

			err := tt.call(context.Background(), store, scope, ids)

			require.NoError(t, err, "reorder/move operation should not return an error")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}

	t.Run("move rejects before_id from a different scope", func(t *testing.T) {
		t.Parallel()

		store, mock := newMockCodingCredentialStore(t)
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		scope := models.Scope{OrgID: orgID, UserID: &userID}
		movingID := uuid.New()
		stackID := uuid.New()
		// foreignID is the supposed before_id — it belongs to a different
		// scope (org-scoped, or a different user). It must NOT appear in
		// fetchStackTx's scope-bounded list.
		foreignID := uuid.New()

		// 1. Begin + lockAndAssertScope on the moving id.
		mock.ExpectBegin()
		mock.ExpectExec("pg_advisory_xact_lock").
			WithArgs(codingAnyArgs(1)...).
			WillReturnResult(pgxmock.NewResult("SELECT", 1))
		expectScopedSnapshot(t, mock, scope, movingID, models.ProviderOpenAI)
		// 2. fetchStackTx returns only ids that belong to scope. foreignID is
		//    deliberately absent.
		mock.ExpectQuery(`SELECT cc.id\s+FROM coding_credentials cc\s+JOIN coding_credential_runtime_state`).
			WithArgs(codingAnyArgs(2)...).
			WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(movingID).AddRow(stackID))
		// 3. The store sees foreignID is not in `without`, returns an error,
		//    and rolls back. We do NOT expect any UPDATEs.
		mock.ExpectRollback()

		err := store.Move(context.Background(), scope, movingID, models.MoveCodingCredentialInput{BeforeID: &foreignID})
		require.Error(t, err, "Move should reject a before_id that does not belong to the requested scope")
		require.Contains(t, err.Error(), "before_id not found in scope", "error should explain the cross-scope rejection")
		require.NoError(t, mock.ExpectationsWereMet(), "no priority writes should fire when the move is rejected")
	})

	t.Run("janitor deletes old pending auth rows", func(t *testing.T) {
		t.Parallel()

		store, mock := newMockCodingCredentialStore(t)
		defer mock.Close()

		mock.ExpectQuery("WITH expired").
			WithArgs(codingAnyArgs(1)...).
			WillReturnRows(pgxmock.NewRows([]string{"deactivated_count"}).AddRow(int64(2)))

		n, err := store.JanitorDeletePendingAuthOlderThan(context.Background(), time.Hour)

		require.NoError(t, err, "janitor sweep should not return an error")
		require.Equal(t, int64(2), n, "janitor sweep should return deactivated logical credential count")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})
}

func TestCodingCredentialStoreReorderRejectsPartialStack(t *testing.T) {
	t.Parallel()

	store, mock := newMockCodingCredentialStore(t)
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	scope := models.Scope{OrgID: orgID, UserID: &userID}
	firstID := uuid.New()
	secondID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectExec("pg_advisory_xact_lock").
		WithArgs(codingAnyArgs(1)...).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`SELECT cc.id\s+FROM coding_credentials cc\s+JOIN coding_credential_runtime_state`).
		WithArgs(codingAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(firstID).AddRow(secondID))
	mock.ExpectRollback()

	err := store.Reorder(context.Background(), scope, []uuid.UUID{secondID})

	require.Error(t, err, "Reorder should reject a partial ordered_ids list")
	require.Contains(t, err.Error(), "exactly match", "Reorder should explain that every active row must be included")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func expectScopedMutation(t *testing.T, mock pgxmock.PgxPoolIface, scope models.Scope, id uuid.UUID, provider models.ProviderName) {
	t.Helper()

	mock.ExpectBegin()
	expectScopedSnapshot(t, mock, scope, id, provider)
}

func expectScopedSnapshot(t *testing.T, mock pgxmock.PgxPoolIface, scope models.Scope, id uuid.UUID, provider models.ProviderName) {
	t.Helper()

	mock.ExpectQuery(`FROM coding_credentials cc\s+JOIN coding_credential_runtime_state`).
		WithArgs(codingAnyArgs(3)...).
		WillReturnRows(pgxmock.NewRows(codingCredentialSnapshotColumns).
			AddRow(codingCredentialSnapshotRow(t, NewCodingCredentialStore(mock, nil), scope, id, provider, models.CodingCredentialStatusActive)...))
}

func TestMoveCodingCredentialInputValidate(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	cases := []struct {
		name string
		in   models.MoveCodingCredentialInput
		ok   bool
	}{
		{"to_top", models.MoveCodingCredentialInput{ToTop: true}, true},
		{"to_bottom", models.MoveCodingCredentialInput{ToBottom: true}, true},
		{"before_id", models.MoveCodingCredentialInput{BeforeID: &id}, true},
		{"after_id", models.MoveCodingCredentialInput{AfterID: &id}, true},
		{"none", models.MoveCodingCredentialInput{}, false},
		{"two", models.MoveCodingCredentialInput{ToTop: true, ToBottom: true}, false},
		{"top_and_id", models.MoveCodingCredentialInput{ToTop: true, BeforeID: &id}, false},
	}
	for _, tc := range cases {
		err := tc.in.Validate()
		if tc.ok && err != nil {
			t.Errorf("%s: expected ok, got %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
		}
	}
}
