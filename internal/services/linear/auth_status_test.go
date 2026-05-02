package linear

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

// fakeIntegrationStore is a tiny in-memory pair (reader + writer) backing
// the auth-status tests. Mirrors the *db.IntegrationStore surface the
// production wiring passes — no Linear DB shape, just enough to verify
// MarkIntegrationUnauthorized / ClearIntegrationUnauthorized do the right
// writes (and skip writes when nothing changed).
type fakeIntegrationStore struct {
	mu          sync.Mutex
	row         models.Integration
	notFoundErr error // when set, GetByOrgAndProvider returns this instead of a row
	statusCalls []string
	configCalls []json.RawMessage
}

func (f *fakeIntegrationStore) GetByOrgAndProvider(_ context.Context, _ uuid.UUID, _ string) (models.Integration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.notFoundErr != nil {
		return models.Integration{}, f.notFoundErr
	}
	return f.row, nil
}

func (f *fakeIntegrationStore) UpdateStatus(_ context.Context, _, _ uuid.UUID, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCalls = append(f.statusCalls, status)
	f.row.Status = models.IntegrationStatus(status)
	return nil
}

func (f *fakeIntegrationStore) UpdateConfig(_ context.Context, _, _ uuid.UUID, cfg json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configCalls = append(f.configCalls, cfg)
	f.row.Config = cfg
	return nil
}

func newAuthStatusService(store *fakeIntegrationStore) *Service {
	return &Service{
		logger:             zerolog.Nop(),
		integrations:       store,
		integrationsWriter: store,
	}
}

func TestMarkIntegrationUnauthorized_FlipsStatusAndStampsConfig(t *testing.T) {
	t.Parallel()
	store := &fakeIntegrationStore{
		row: models.Integration{
			ID:     uuid.New(),
			Status: models.IntegrationStatusActive,
			Config: json.RawMessage(`{"workspace_id":"wks-1"}`),
		},
	}
	svc := newAuthStatusService(store)

	svc.MarkIntegrationUnauthorized(context.Background(), uuid.New())

	require.Equal(t, []string{string(models.IntegrationStatusError)}, store.statusCalls)
	require.Len(t, store.configCalls, 1, "config should be patched once")

	var patched map[string]any
	require.NoError(t, json.Unmarshal(store.configCalls[0], &patched))
	require.Equal(t, "wks-1", patched["workspace_id"], "existing keys must be preserved")
	require.Contains(t, patched, configAuthErrorKey)
	require.Contains(t, patched, configAuthErrorAtKey)
	require.Equal(t, authErrorReasonUnauthorized, patched[configAuthErrorKey])

	at, err := time.Parse(time.RFC3339, patched[configAuthErrorAtKey].(string))
	require.NoError(t, err, "stamped timestamp must be RFC3339")
	require.WithinDuration(t, time.Now().UTC(), at, 5*time.Second)
}

func TestMarkIntegrationUnauthorized_SkipsWhenAlreadyErroredRecently(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Format(time.RFC3339)
	cfg, err := json.Marshal(map[string]any{
		configAuthErrorKey:   "prior",
		configAuthErrorAtKey: now,
	})
	require.NoError(t, err)
	store := &fakeIntegrationStore{
		row: models.Integration{
			ID:     uuid.New(),
			Status: models.IntegrationStatusError,
			Config: cfg,
		},
	}
	svc := newAuthStatusService(store)

	svc.MarkIntegrationUnauthorized(context.Background(), uuid.New())

	require.Empty(t, store.statusCalls, "no status flip when already errored within window")
	require.Empty(t, store.configCalls, "no config rewrite when stamp is fresh")
}

func TestMarkIntegrationUnauthorized_RestampWhenStaleStamp(t *testing.T) {
	t.Parallel()
	stale := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	cfg, err := json.Marshal(map[string]any{
		configAuthErrorKey:   "older error",
		configAuthErrorAtKey: stale,
	})
	require.NoError(t, err)
	store := &fakeIntegrationStore{
		row: models.Integration{
			ID:     uuid.New(),
			Status: models.IntegrationStatusError,
			Config: cfg,
		},
	}
	svc := newAuthStatusService(store)

	svc.MarkIntegrationUnauthorized(context.Background(), uuid.New())

	// Stale stamp falls past the recency window: both writes proceed (the
	// status write is idempotent — error→error — but the config write
	// refreshes the timestamp surfaced in the UI).
	require.Len(t, store.configCalls, 1, "stale timestamp should be refreshed")
	require.Equal(t, []string{string(models.IntegrationStatusError)}, store.statusCalls)
	var refreshed map[string]any
	require.NoError(t, json.Unmarshal(store.configCalls[0], &refreshed))
	stampedAt, err := time.Parse(time.RFC3339, refreshed[configAuthErrorAtKey].(string))
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().UTC(), stampedAt, 5*time.Second, "stamp must be refreshed to ~now")
}

func TestMarkIntegrationUnauthorized_NilSafe(t *testing.T) {
	t.Parallel()
	// Nil receiver / nil writer / lookup error all fall through silently.
	(*Service)(nil).MarkIntegrationUnauthorized(context.Background(), uuid.New())

	storeNotFound := &fakeIntegrationStore{notFoundErr: errors.New("no rows")}
	svc := newAuthStatusService(storeNotFound)
	svc.MarkIntegrationUnauthorized(context.Background(), uuid.New())
	require.Empty(t, storeNotFound.statusCalls)
	require.Empty(t, storeNotFound.configCalls)

	// Nil writer (only reader wired) — must not panic.
	svcReadOnly := &Service{
		logger:       zerolog.Nop(),
		integrations: &fakeIntegrationStore{row: models.Integration{Status: models.IntegrationStatusActive}},
	}
	svcReadOnly.MarkIntegrationUnauthorized(context.Background(), uuid.New())
}

func TestClearIntegrationUnauthorized_RestoresStatusAndStripsMarkers(t *testing.T) {
	t.Parallel()
	cfg, err := json.Marshal(map[string]any{
		"workspace_id":       "wks-1",
		configAuthErrorKey:   "prior",
		configAuthErrorAtKey: time.Now().UTC().Format(time.RFC3339),
	})
	require.NoError(t, err)
	store := &fakeIntegrationStore{
		row: models.Integration{
			ID:     uuid.New(),
			Status: models.IntegrationStatusError,
			Config: cfg,
		},
	}
	svc := newAuthStatusService(store)

	svc.ClearIntegrationUnauthorized(context.Background(), uuid.New())

	require.Equal(t, []string{string(models.IntegrationStatusActive)}, store.statusCalls)
	require.Len(t, store.configCalls, 1)

	var cleared map[string]any
	require.NoError(t, json.Unmarshal(store.configCalls[0], &cleared))
	require.NotContains(t, cleared, configAuthErrorKey, "auth error key should be stripped")
	require.NotContains(t, cleared, configAuthErrorAtKey)
	require.Equal(t, "wks-1", cleared["workspace_id"], "non-auth keys preserved")
}

func TestClearIntegrationUnauthorized_NoOpWhenAlreadyHealthy(t *testing.T) {
	t.Parallel()
	store := &fakeIntegrationStore{
		row: models.Integration{
			ID:     uuid.New(),
			Status: models.IntegrationStatusActive,
			Config: json.RawMessage(`{"workspace_id":"wks-1"}`),
		},
	}
	svc := newAuthStatusService(store)

	svc.ClearIntegrationUnauthorized(context.Background(), uuid.New())

	require.Empty(t, store.statusCalls)
	require.Empty(t, store.configCalls)
}
