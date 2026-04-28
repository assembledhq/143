package db

import (
	"context"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

var codingCredentialTestColumns = []string{
	"id", "org_id", "user_id", "provider", "label", "config", "priority", "status", "created_by", "last_verified_at", "created_at", "updated_at",
}

func encryptedCodingConfig(t *testing.T, store *CodingCredentialStore, cfg models.ProviderConfig) []byte {
	t.Helper()

	data, err := store.marshalAndEncrypt(cfg)
	require.NoError(t, err, "test config should marshal and encrypt")
	return data
}

func codingCredentialRow(t *testing.T, store *CodingCredentialStore, orgID uuid.UUID, userID *uuid.UUID, id uuid.UUID, provider models.ProviderName, cfg models.ProviderConfig, priority int, status string) []any {
	t.Helper()

	now := time.Now().UTC()
	return []any{
		id,
		orgID,
		userID,
		string(provider),
		"Test credential",
		encryptedCodingConfig(t, store, cfg),
		priority,
		status,
		nil,
		nil,
		now,
		now,
	}
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
			argCount: 1,
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
	require.ErrorIs(t, err, ErrCodingCredentialNotFound, "PickRunnable should return not found when every cached candidate is shed")
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
				mock.ExpectQuery("INSERT INTO coding_credentials").
					WithArgs(codingAnyArgs(8)...).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(id))
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
				mock.ExpectExec("UPDATE coding_credentials").
					WithArgs(codingAnyArgs(2)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
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
				mock.ExpectExec("UPDATE coding_credentials").
					WithArgs(codingAnyArgs(2)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectCommit()
			},
			call: func(ctx context.Context, store *CodingCredentialStore, scope models.Scope, id uuid.UUID) error {
				return store.UpdateConfig(ctx, scope, id, models.OpenAIConfig{APIKey: "sk-openai-abcdef"})
			},
		},
		{
			name: "rename",
			setup: func(t *testing.T, mock pgxmock.PgxPoolIface, store *CodingCredentialStore, scope models.Scope, id uuid.UUID) {
				expectScopedMutation(t, mock, scope, id, models.ProviderOpenAI)
				mock.ExpectExec("UPDATE coding_credentials").
					WithArgs(codingAnyArgs(2)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
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
				mock.ExpectExec("UPDATE coding_credentials").
					WithArgs(codingAnyArgs(2)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectCommit()
			},
			call: func(ctx context.Context, store *CodingCredentialStore, scope models.Scope, id uuid.UUID) error {
				return store.UpdateStatus(ctx, scope, id, models.CodingCredentialStatusInvalid)
			},
		},
		{
			name: "disable",
			setup: func(t *testing.T, mock pgxmock.PgxPoolIface, store *CodingCredentialStore, scope models.Scope, id uuid.UUID) {
				expectScopedMutation(t, mock, scope, id, models.ProviderOpenAI)
				mock.ExpectExec("UPDATE coding_credentials").
					WithArgs(codingAnyArgs(2)...).
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
	mock.ExpectQuery("INSERT INTO coding_credentials").
		WithArgs(codingAnyArgs(8)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}))
	mock.ExpectQuery("SELECT status FROM coding_credentials").
		WithArgs(codingAnyArgs(4)...).
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow(models.CodingCredentialStatusActive))
	mock.ExpectRollback()

	_, err := store.Create(context.Background(), scope, "Codex", models.OpenAIConfig{APIKey: "sk-openai-123456"}, CreateOpts{})

	var taken *ErrCodingCredentialLabelTaken
	require.ErrorAs(t, err, &taken, "Create should return a typed label conflict")
	require.Equal(t, models.CodingCredentialStatusActive, taken.ExistingStatus, "label conflict should expose the existing status")
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
				for range ids {
					mock.ExpectQuery("SELECT org_id, user_id, provider").
						WithArgs(codingAnyArgs(1)...).
						WillReturnRows(pgxmock.NewRows([]string{"org_id", "user_id", "provider"}).AddRow(scope.OrgID, scope.UserID, string(models.ProviderOpenAI)))
					mock.ExpectExec("UPDATE coding_credentials SET priority").
						WithArgs(codingAnyArgs(2)...).
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
				mock.ExpectQuery("SELECT org_id, user_id, provider").
					WithArgs(codingAnyArgs(1)...).
					WillReturnRows(pgxmock.NewRows([]string{"org_id", "user_id", "provider"}).AddRow(scope.OrgID, scope.UserID, string(models.ProviderOpenAI)))
				mock.ExpectQuery("SELECT id FROM coding_credentials").
					WithArgs(codingAnyArgs(2)...).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(ids[0]).AddRow(ids[1]).AddRow(ids[2]))
				mock.ExpectQuery("SELECT id, priority FROM coding_credentials").
					WithArgs(codingAnyArgs(1)...).
					WillReturnRows(pgxmock.NewRows([]string{"id", "priority"}).AddRow(ids[0], 1).AddRow(ids[1], 2).AddRow(ids[2], 3))
				mock.ExpectExec("UPDATE coding_credentials SET priority").
					WithArgs(codingAnyArgs(2)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectExec("UPDATE coding_credentials SET priority").
					WithArgs(codingAnyArgs(2)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectExec("UPDATE coding_credentials SET priority").
					WithArgs(codingAnyArgs(2)...).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
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

	t.Run("janitor deletes old pending auth rows", func(t *testing.T) {
		t.Parallel()

		store, mock := newMockCodingCredentialStore(t)
		defer mock.Close()

		mock.ExpectExec("DELETE FROM coding_credentials").
			WithArgs(codingAnyArgs(1)...).
			WillReturnResult(pgxmock.NewResult("DELETE", 2))

		n, err := store.JanitorDeletePendingAuthOlderThan(context.Background(), time.Hour)

		require.NoError(t, err, "janitor sweep should not return an error")
		require.Equal(t, int64(2), n, "janitor sweep should return rows affected")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})
}

func expectScopedMutation(t *testing.T, mock pgxmock.PgxPoolIface, scope models.Scope, id uuid.UUID, provider models.ProviderName) {
	t.Helper()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT org_id, user_id, provider").
		WithArgs(codingAnyArgs(1)...).
		WillReturnRows(pgxmock.NewRows([]string{"org_id", "user_id", "provider"}).AddRow(scope.OrgID, scope.UserID, string(provider)))
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
