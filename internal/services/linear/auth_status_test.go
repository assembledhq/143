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
	mu             sync.Mutex
	row            models.Integration
	notFoundErr    error // when set, GetByOrgAndProvider returns this instead of a row
	statusCalls    []string
	configCalls    []json.RawMessage
	statusCfgCalls []statusAndConfigCall
}

type statusAndConfigCall struct {
	status string
	config json.RawMessage
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

func (f *fakeIntegrationStore) UpdateStatusAndConfig(_ context.Context, _, _ uuid.UUID, status string, cfg json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCfgCalls = append(f.statusCfgCalls, statusAndConfigCall{status: status, config: cfg})
	f.row.Status = models.IntegrationStatus(status)
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

	// Single atomic write, never the legacy two-step pair.
	require.Empty(t, store.statusCalls, "should not call UpdateStatus alone")
	require.Empty(t, store.configCalls, "should not call UpdateConfig alone")
	require.Len(t, store.statusCfgCalls, 1, "config+status should be patched in one atomic call")
	require.Equal(t, string(models.IntegrationStatusError), store.statusCfgCalls[0].status)

	var patched map[string]any
	require.NoError(t, json.Unmarshal(store.statusCfgCalls[0].config, &patched))
	require.Equal(t, "wks-1", patched["workspace_id"], "existing keys must be preserved")
	require.Contains(t, patched, models.IntegrationConfigAuthErrorKey)
	require.Contains(t, patched, models.IntegrationConfigAuthErrorAtKey)
	require.Equal(t, authErrorReasonUnauthorized, patched[models.IntegrationConfigAuthErrorKey])

	at, err := time.Parse(time.RFC3339, patched[models.IntegrationConfigAuthErrorAtKey].(string))
	require.NoError(t, err, "stamped timestamp must be RFC3339")
	require.WithinDuration(t, time.Now().UTC(), at, 5*time.Second)
}

func TestMarkIntegrationUnauthorized_SkipsWhenAlreadyErroredRecently(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Format(time.RFC3339)
	cfg, err := json.Marshal(map[string]any{
		models.IntegrationConfigAuthErrorKey:   "prior",
		models.IntegrationConfigAuthErrorAtKey: now,
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

	require.Empty(t, store.statusCalls, "no status write when already errored within window")
	require.Empty(t, store.configCalls, "no config write when stamp is fresh")
	require.Empty(t, store.statusCfgCalls, "no atomic write when nothing changed")
}

func TestMarkIntegrationUnauthorized_RestampWhenStaleStamp(t *testing.T) {
	t.Parallel()
	stale := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	cfg, err := json.Marshal(map[string]any{
		models.IntegrationConfigAuthErrorKey:   "older error",
		models.IntegrationConfigAuthErrorAtKey: stale,
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

	// Stale stamp falls past the recency window: a single atomic write
	// refreshes both fields (status idempotently stays at error; config
	// gets a refreshed timestamp surfaced in the UI).
	require.Empty(t, store.statusCalls)
	require.Empty(t, store.configCalls)
	require.Len(t, store.statusCfgCalls, 1, "stale timestamp should be refreshed via atomic write")
	require.Equal(t, string(models.IntegrationStatusError), store.statusCfgCalls[0].status)
	var refreshed map[string]any
	require.NoError(t, json.Unmarshal(store.statusCfgCalls[0].config, &refreshed))
	stampedAt, err := time.Parse(time.RFC3339, refreshed[models.IntegrationConfigAuthErrorAtKey].(string))
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
	require.Empty(t, storeNotFound.statusCfgCalls)

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
		"workspace_id":                         "wks-1",
		models.IntegrationConfigAuthErrorKey:   "prior",
		models.IntegrationConfigAuthErrorAtKey: time.Now().UTC().Format(time.RFC3339),
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

	// Both pieces of state changed → one atomic write.
	require.Empty(t, store.statusCalls)
	require.Empty(t, store.configCalls)
	require.Len(t, store.statusCfgCalls, 1)
	require.Equal(t, string(models.IntegrationStatusActive), store.statusCfgCalls[0].status)

	var cleared map[string]any
	require.NoError(t, json.Unmarshal(store.statusCfgCalls[0].config, &cleared))
	require.NotContains(t, cleared, models.IntegrationConfigAuthErrorKey, "auth error key should be stripped")
	require.NotContains(t, cleared, models.IntegrationConfigAuthErrorAtKey)
	require.Equal(t, "wks-1", cleared["workspace_id"], "non-auth keys preserved")
}

func TestClearIntegrationUnauthorized_OnlyConfigDirty(t *testing.T) {
	t.Parallel()
	// status=active but stale markers in config (e.g. crash mid-Mark).
	// Should clear config alone, no status write.
	cfg, err := json.Marshal(map[string]any{
		"workspace_id":                         "wks-1",
		models.IntegrationConfigAuthErrorKey:   "stale",
		models.IntegrationConfigAuthErrorAtKey: time.Now().UTC().Format(time.RFC3339),
	})
	require.NoError(t, err)
	store := &fakeIntegrationStore{
		row: models.Integration{
			ID:     uuid.New(),
			Status: models.IntegrationStatusActive,
			Config: cfg,
		},
	}
	svc := newAuthStatusService(store)

	svc.ClearIntegrationUnauthorized(context.Background(), uuid.New())

	require.Empty(t, store.statusCalls)
	require.Empty(t, store.statusCfgCalls)
	require.Len(t, store.configCalls, 1, "narrower update used when only config needed clearing")
}

func TestClearIntegrationUnauthorized_OnlyStatusDirty(t *testing.T) {
	t.Parallel()
	// status=error but no markers (e.g. config was hand-edited). Should
	// flip status alone via the narrower update.
	store := &fakeIntegrationStore{
		row: models.Integration{
			ID:     uuid.New(),
			Status: models.IntegrationStatusError,
			Config: json.RawMessage(`{"workspace_id":"wks-1"}`),
		},
	}
	svc := newAuthStatusService(store)

	svc.ClearIntegrationUnauthorized(context.Background(), uuid.New())

	require.Empty(t, store.configCalls)
	require.Empty(t, store.statusCfgCalls)
	require.Equal(t, []string{string(models.IntegrationStatusActive)}, store.statusCalls)
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
	require.Empty(t, store.statusCfgCalls)
}
