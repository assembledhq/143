package codexauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// mockCredentialStore is a simple in-memory credential store for testing.
//
// Rows track the scope they were inserted under via the credScope map; that
// lets personal-scope (UserID != nil) and org-scope (UserID == nil) rows
// coexist for the same (org, provider, label) without colliding the way the
// real CodingCredentialStore avoids it via its (org_id, user_id, provider,
// label) NULLS-NOT-DISTINCT unique index.
type mockCredentialStore struct {
	creds     map[string]*models.DecryptedCredential
	credScope map[uuid.UUID]models.Scope
	status    map[string]string
}

func newMockCredentialStore() *mockCredentialStore {
	return &mockCredentialStore{
		creds:     make(map[string]*models.DecryptedCredential),
		credScope: make(map[uuid.UUID]models.Scope),
		status:    make(map[string]string),
	}
}

func (m *mockCredentialStore) key(orgID uuid.UUID, provider models.ProviderName) string {
	return orgID.String() + ":" + string(provider)
}

// scopeKey collapses a Scope to a string suitable for keying internal maps
// alongside (provider, label). Mirrors how the real coding_credentials table
// keys rows on (org_id, user_id, provider, label).
func (m *mockCredentialStore) scopeKey(scope models.Scope) string {
	if scope.IsPersonal() {
		return scope.OrgID.String() + "|u|" + scope.UserID.String()
	}
	return scope.OrgID.String() + "|org"
}

// scopesMatch is true when a stored cred's scope matches the lookup scope.
// Used by every read path so a personal-scope query never returns an
// org-scope row (and vice versa).
func (m *mockCredentialStore) scopesMatch(credID uuid.UUID, scope models.Scope) bool {
	stored, ok := m.credScope[credID]
	if !ok {
		// Legacy seed paths that mutate m.creds directly without going
		// through Upsert/InsertPendingAuth treat the row as org-scope by
		// default. Keep that behaviour so existing tests don't need to
		// register scope explicitly.
		return scope.IsOrg()
	}
	if stored.IsPersonal() != scope.IsPersonal() {
		return false
	}
	if stored.IsPersonal() {
		return *stored.UserID == *scope.UserID && stored.OrgID == scope.OrgID
	}
	return stored.OrgID == scope.OrgID
}

func (m *mockCredentialStore) Upsert(_ context.Context, scope models.Scope, cfg models.ProviderConfig) error {
	k := m.scopedLabelKey(scope, cfg.Provider(), "")
	id := uuid.New()
	m.creds[k] = &models.DecryptedCredential{
		ID:       id,
		OrgID:    scope.OrgID,
		Provider: cfg.Provider(),
		Config:   cfg,
		Status:   "active",
	}
	m.credScope[id] = scope
	return nil
}

func (m *mockCredentialStore) Get(_ context.Context, scope models.Scope, provider models.ProviderName) (*models.DecryptedCredential, error) {
	// Return the first matching credential (prefers label="" for backward compat).
	for _, cred := range m.creds {
		if cred.OrgID == scope.OrgID && cred.Provider == provider {
			return cred, nil
		}
	}
	return nil, ErrCredentialNotFound
}

func (m *mockCredentialStore) UpdateStatus(_ context.Context, scope models.Scope, provider models.ProviderName, status string) error {
	for _, cred := range m.creds {
		if cred.OrgID == scope.OrgID && cred.Provider == provider {
			cred.Status = status
		}
	}
	m.status[m.key(scope.OrgID, provider)] = status
	return nil
}

func (m *mockCredentialStore) Disable(_ context.Context, scope models.Scope, provider models.ProviderName) error {
	for k, cred := range m.creds {
		if cred.OrgID == scope.OrgID && cred.Provider == provider {
			delete(m.creds, k)
		}
	}
	return nil
}

// labelKey is the per-row map key. We include the scope segment so personal
// and org rows at the same (org, provider, label) don't overwrite each
// other in the in-memory store. The legacy two-arg signature is preserved
// for tests that compose it from raw orgID values.
func (m *mockCredentialStore) labelKey(orgID uuid.UUID, provider models.ProviderName, label string) string {
	return orgID.String() + "|org:" + string(provider) + ":" + label
}

// scopedLabelKey is the scope-aware variant — preferred by the mock methods
// themselves so personal-scope writes land in their own slot.
func (m *mockCredentialStore) scopedLabelKey(scope models.Scope, provider models.ProviderName, label string) string {
	return m.scopeKey(scope) + ":" + string(provider) + ":" + label
}

func (m *mockCredentialStore) UpsertWithLabel(_ context.Context, scope models.Scope, createdBy *uuid.UUID, label string, cfg models.ProviderConfig) (*uuid.UUID, error) {
	k := m.scopedLabelKey(scope, cfg.Provider(), label)
	if existing, ok := m.creds[k]; ok {
		existing.Config = cfg
		existing.Status = "active"
		return &existing.ID, nil
	}
	id := uuid.New()
	m.creds[k] = &models.DecryptedCredential{
		ID:        id,
		OrgID:     scope.OrgID,
		Provider:  cfg.Provider(),
		Label:     label,
		Config:    cfg,
		Status:    "active",
		CreatedBy: createdBy,
	}
	m.credScope[id] = scope
	return &id, nil
}

func (m *mockCredentialStore) InsertPendingAuth(_ context.Context, scope models.Scope, createdBy *uuid.UUID, label string, cfg models.ProviderConfig) (*uuid.UUID, error) {
	k := m.scopedLabelKey(scope, cfg.Provider(), label)
	if existing, ok := m.creds[k]; ok {
		// Mirror the real store: pending_auth and disabled rows are reusable;
		// active and invalid rows reject with a typed error so callers can render
		// a status-specific message.
		if existing.Status != "pending_auth" && existing.Status != "disabled" {
			return nil, &db.ErrCredentialLabelTaken{Label: label, ExistingStatus: existing.Status}
		}
		existing.Config = cfg
		existing.Status = "pending_auth"
		return &existing.ID, nil
	}
	id := uuid.New()
	m.creds[k] = &models.DecryptedCredential{
		ID:        id,
		OrgID:     scope.OrgID,
		Provider:  cfg.Provider(),
		Label:     label,
		Config:    cfg,
		Status:    "pending_auth",
		CreatedBy: createdBy,
	}
	m.credScope[id] = scope
	return &id, nil
}

func (m *mockCredentialStore) GetByID(_ context.Context, scope models.Scope, id uuid.UUID) (*models.DecryptedCredential, error) {
	for _, cred := range m.creds {
		if cred.ID == id && m.scopesMatch(cred.ID, scope) {
			return cred, nil
		}
	}
	return nil, ErrCredentialNotFound
}

func (m *mockCredentialStore) ListByProvider(_ context.Context, scope models.Scope, provider models.ProviderName) ([]models.DecryptedCredential, error) {
	var result []models.DecryptedCredential
	for _, cred := range m.creds {
		if cred.OrgID == scope.OrgID && cred.Provider == provider {
			result = append(result, *cred)
		}
	}
	return result, nil
}

func (m *mockCredentialStore) GetByProviderAndLabel(_ context.Context, scope models.Scope, provider models.ProviderName, label string) (*models.DecryptedCredential, error) {
	for _, cred := range m.creds {
		if m.scopesMatch(cred.ID, scope) && cred.Provider == provider && cred.Label == label {
			return cred, nil
		}
	}
	return nil, ErrCredentialNotFound
}

func (m *mockCredentialStore) ClaimNextRoundRobin(_ context.Context, scope models.Scope, provider models.ProviderName) (*models.DecryptedCredential, error) {
	// Find the active credential with the oldest LastUsedAt (nil = never used, comes first).
	var oldest *models.DecryptedCredential
	for _, cred := range m.creds {
		if cred.OrgID != scope.OrgID || cred.Provider != provider || cred.Status != "active" {
			continue
		}
		if oldest == nil {
			oldest = cred
			continue
		}
		// Prefer nil LastUsedAt (never used), then earliest.
		if cred.LastUsedAt == nil && oldest.LastUsedAt != nil {
			oldest = cred
		} else if cred.LastUsedAt != nil && oldest.LastUsedAt != nil && cred.LastUsedAt.Before(*oldest.LastUsedAt) {
			oldest = cred
		}
	}
	if oldest == nil {
		return nil, ErrCredentialNotFound
	}
	now := time.Now()
	oldest.LastUsedAt = &now
	return oldest, nil
}

func (m *mockCredentialStore) DisableByID(_ context.Context, scope models.Scope, id uuid.UUID) error {
	for k, cred := range m.creds {
		if cred.ID == id && cred.OrgID == scope.OrgID {
			delete(m.creds, k)
			return nil
		}
	}
	return nil
}

func (m *mockCredentialStore) UpdateStatusByID(_ context.Context, scope models.Scope, id uuid.UUID, status string) error {
	for _, cred := range m.creds {
		if cred.ID == id && cred.OrgID == scope.OrgID {
			cred.Status = status
			k := m.key(cred.OrgID, cred.Provider)
			m.status[k] = status
			return nil
		}
	}
	return nil
}

func (m *mockCredentialStore) UpsertByID(_ context.Context, scope models.Scope, id uuid.UUID, cfg models.ProviderConfig) error {
	for k, cred := range m.creds {
		if cred.ID == id && cred.OrgID == scope.OrgID {
			// Mirror the real store: disabled rows aren't resurrected by a refresh.
			if cred.Status == "disabled" {
				return nil
			}
			m.creds[k] = &models.DecryptedCredential{
				ID:       id,
				OrgID:    cred.OrgID,
				Provider: cred.Provider,
				Label:    cred.Label,
				Config:   cfg,
				Status:   "active",
			}
			return nil
		}
	}
	return ErrCredentialNotFound
}

func (m *mockCredentialStore) ExistsForProviderByID(_ context.Context, scope models.Scope, id uuid.UUID, provider models.ProviderName) (bool, error) {
	for _, cred := range m.creds {
		if cred.ID == id && cred.OrgID == scope.OrgID && cred.Provider == provider {
			return true, nil
		}
	}
	return false, nil
}

func TestInitiateDeviceAuth(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/accounts/deviceauth/usercode" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"device_auth_id":   "dev_123",
			"user_code":        "ABCD-1234",
			"verification_uri": "https://auth.openai.com/codex/device",
			"expires_in":       900,
			"interval":         5,
		})
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()
	userID := uuid.New()
	resp, err := svc.InitiateDeviceAuth(context.Background(), models.Scope{OrgID: orgID}, &userID, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.UserCode != "ABCD-1234" {
		t.Errorf("expected user code ABCD-1234, got %s", resp.UserCode)
	}
	if resp.ExpiresIn != 900 {
		t.Errorf("expected expires_in 900, got %d", resp.ExpiresIn)
	}

	// The pending credential row should remember who started the flow.
	cred, err := store.GetByProviderAndLabel(context.Background(), models.Scope{OrgID: orgID}, models.ProviderOpenAIChatGPT, "")
	if err != nil {
		t.Fatalf("expected pending credential to be persisted: %v", err)
	}
	if cred.CreatedBy == nil || *cred.CreatedBy != userID {
		t.Errorf("expected created_by=%s, got %v", userID, cred.CreatedBy)
	}
}

func TestPollForToken_AuthorizationPending(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "authorization_pending",
		})
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()
	// Store a pending auth.
	svc.pending.Store(pendingKey(models.Scope{OrgID: orgID}, ""), &PendingAuth{
		DeviceAuthID: "dev_123",
		UserCode:     "ABCD-1234",
		ExpiresAt:    time.Now().Add(15 * time.Minute),
		Interval:     5,
	})

	status, err := svc.PollForToken(context.Background(), models.Scope{OrgID: orgID}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "pending" {
		t.Errorf("expected pending status, got %s", status.Status)
	}
}

func TestPollForToken_HTTP403Pending(t *testing.T) {
	t.Parallel()
	// OpenAI returns 403 while the user hasn't entered the code yet.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()
	svc.pending.Store(pendingKey(models.Scope{OrgID: orgID}, ""), &PendingAuth{
		DeviceAuthID: "dev_123",
		UserCode:     "ABCD-1234",
		ExpiresAt:    time.Now().Add(15 * time.Minute),
		Interval:     5,
	})

	status, err := svc.PollForToken(context.Background(), models.Scope{OrgID: orgID}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "pending" {
		t.Errorf("expected pending status for 403, got %s", status.Status)
	}
}

func TestPollForToken_HTTP404Pending(t *testing.T) {
	t.Parallel()
	// OpenAI may also return 404 while the user hasn't entered the code.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()
	svc.pending.Store(pendingKey(models.Scope{OrgID: orgID}, ""), &PendingAuth{
		DeviceAuthID: "dev_123",
		UserCode:     "ABCD-1234",
		ExpiresAt:    time.Now().Add(15 * time.Minute),
		Interval:     5,
	})

	status, err := svc.PollForToken(context.Background(), models.Scope{OrgID: orgID}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "pending" {
		t.Errorf("expected pending status for 404, got %s", status.Status)
	}
}

func TestPollForToken_Success(t *testing.T) {
	t.Parallel()
	// The test server handles two requests:
	// 1. Device code poll → returns authorization_code + code_verifier
	// 2. Token exchange at /oauth/token → returns access_token + refresh_token
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/oauth/token" {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token":  "cha_test_access_token_12345",
				"refresh_token": "chr_test_refresh_token_12345",
				"expires_in":    3600,
			})
		} else {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":             "success",
				"authorization_code": "ac_test_auth_code",
				"code_verifier":      "test_code_verifier",
			})
		}
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()
	svc.pending.Store(pendingKey(models.Scope{OrgID: orgID}, ""), &PendingAuth{
		DeviceAuthID: "dev_123",
		UserCode:     "ABCD-1234",
		ExpiresAt:    time.Now().Add(15 * time.Minute),
		Interval:     5,
	})

	status, err := svc.PollForToken(context.Background(), models.Scope{OrgID: orgID}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "completed" {
		t.Errorf("expected completed status, got %s (%s)", status.Status, status.Message)
	}

	// Verify credential was stored.
	cred, err := store.Get(context.Background(), models.Scope{OrgID: orgID}, models.ProviderOpenAIChatGPT)
	if err != nil {
		t.Fatalf("credential not stored: %v", err)
	}
	cfg := cred.Config.(models.OpenAIChatGPTConfig)
	if cfg.AccessToken != "cha_test_access_token_12345" {
		t.Errorf("unexpected access token: %s", cfg.AccessToken)
	}
	if cfg.RefreshToken != "chr_test_refresh_token_12345" {
		t.Errorf("unexpected refresh token: %s", cfg.RefreshToken)
	}
}

func TestPollForToken_Expired(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	svc.pending.Store(pendingKey(models.Scope{OrgID: orgID}, ""), &PendingAuth{
		DeviceAuthID: "dev_123",
		ExpiresAt:    time.Now().Add(-1 * time.Minute), // Already expired.
		Interval:     5,
	})

	status, err := svc.PollForToken(context.Background(), models.Scope{OrgID: orgID}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "expired" {
		t.Errorf("expected expired status, got %s", status.Status)
	}
}

func TestPollForToken_SlowDown(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "slow_down",
		})
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()
	svc.pending.Store(pendingKey(models.Scope{OrgID: orgID}, ""), &PendingAuth{
		DeviceAuthID: "dev_123",
		ExpiresAt:    time.Now().Add(15 * time.Minute),
		Interval:     5,
	})

	status, err := svc.PollForToken(context.Background(), models.Scope{OrgID: orgID}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "pending" {
		t.Errorf("expected pending status, got %s", status.Status)
	}

	// Verify interval was doubled.
	val, _ := svc.pending.Load(pendingKey(models.Scope{OrgID: orgID}, ""))
	pending := val.(*PendingAuth)
	if pending.Interval != 10 {
		t.Errorf("expected interval 10 after slow_down, got %d", pending.Interval)
	}
}

func TestGetValidToken_NotConfigured(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	cfg, err := svc.GetValidToken(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Errorf("expected nil config for unconfigured org, got %+v", cfg)
	}
}

func TestGetValidToken_ValidToken(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	// Pre-store a valid credential.
	store.Upsert(context.Background(), models.Scope{OrgID: orgID}, models.OpenAIChatGPTConfig{
		AccessToken:  "cha_valid_token",
		RefreshToken: "chr_valid_refresh",
		ExpiresAt:    time.Now().Add(1 * time.Hour), // Not expiring soon.
		AccountType:  "plus",
	})

	cfg, err := svc.GetValidToken(context.Background(), orgID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.AccessToken != "cha_valid_token" {
		t.Errorf("unexpected access token: %s", cfg.AccessToken)
	}
}

func TestGetValidToken_AutoRefresh(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":  "cha_refreshed_token",
			"refresh_token": "chr_new_refresh",
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()
	// Pre-store a credential expiring within the refresh window.
	store.Upsert(context.Background(), models.Scope{OrgID: orgID}, models.OpenAIChatGPTConfig{
		AccessToken:  "cha_old_token",
		RefreshToken: "chr_old_refresh",
		ExpiresAt:    time.Now().Add(2 * time.Minute), // Within 5-min refresh window.
		AccountType:  "pro",
	})

	cfg, err := svc.GetValidToken(context.Background(), orgID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AccessToken != "cha_refreshed_token" {
		t.Errorf("expected refreshed token, got %s", cfg.AccessToken)
	}
}

// TestGetValidToken_RoundRobinFailover verifies that when the first claimed
// credential has an expired cached token AND a broken refresh, the service
// marks it invalid and retries with the next credential in the rotation.
func TestGetValidToken_RoundRobinFailover(t *testing.T) {
	t.Parallel()

	// Refresh server refuses the first credential's refresh but succeeds for
	// the second. We distinguish by refresh_token value.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "chr_broken") {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":  "cha_good_refreshed",
			"refresh_token": "chr_good_new",
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()

	// First credential: expired access token + broken refresh token. Seeded
	// with an older LastUsedAt so round-robin claims it first.
	firstID, err := store.UpsertWithLabel(context.Background(), models.Scope{OrgID: orgID}, nil, "Team A", models.OpenAIChatGPTConfig{
		AccessToken:  "cha_broken_expired",
		RefreshToken: "chr_broken",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
	})
	if err != nil {
		t.Fatalf("seed first credential: %v", err)
	}
	older := time.Now().Add(-1 * time.Hour)
	firstKey := store.labelKey(orgID, models.ProviderOpenAIChatGPT, "Team A")
	store.creds[firstKey].LastUsedAt = &older

	// Second credential: also expired but with a working refresh token.
	// Seeded with a newer LastUsedAt so round-robin picks it second.
	if _, err := store.UpsertWithLabel(context.Background(), models.Scope{OrgID: orgID}, nil, "Team B", models.OpenAIChatGPTConfig{
		AccessToken:  "cha_good_expired",
		RefreshToken: "chr_good",
		ExpiresAt:    time.Now().Add(-30 * time.Minute),
	}); err != nil {
		t.Fatalf("seed second credential: %v", err)
	}
	newer := time.Now().Add(-1 * time.Minute)
	secondKey := store.labelKey(orgID, models.ProviderOpenAIChatGPT, "Team B")
	store.creds[secondKey].LastUsedAt = &newer

	cfg, err := svc.GetValidToken(context.Background(), orgID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config from failover")
	}
	if cfg.AccessToken != "cha_good_refreshed" {
		t.Errorf("expected failover to return refreshed token from Team B, got %q", cfg.AccessToken)
	}

	// The first credential should have been marked invalid.
	first, getErr := store.GetByID(context.Background(), models.Scope{OrgID: orgID}, *firstID)
	if getErr != nil {
		t.Fatalf("get first credential: %v", getErr)
	}
	if first.Status != "invalid" {
		t.Errorf("expected first credential to be marked invalid, got %q", first.Status)
	}
}

func TestRefreshTokenByID_Revoked(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "invalid_grant"}`))
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()
	store.Upsert(context.Background(), models.Scope{OrgID: orgID}, models.OpenAIChatGPTConfig{
		AccessToken:  "cha_expired",
		RefreshToken: "chr_revoked",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
	})
	cred, err := store.Get(context.Background(), models.Scope{OrgID: orgID}, models.ProviderOpenAIChatGPT)
	if err != nil {
		t.Fatalf("failed to get credential: %v", err)
	}

	_, err = svc.RefreshTokenByID(context.Background(), models.Scope{OrgID: orgID}, cred.ID)
	if err == nil {
		t.Fatal("expected error for revoked refresh token")
	}

	// Verify credential was marked invalid.
	k := store.key(orgID, models.ProviderOpenAIChatGPT)
	if store.status[k] != "invalid" {
		t.Errorf("expected credential status 'invalid', got %q", store.status[k])
	}
}

func TestDisconnect(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	store.Upsert(context.Background(), models.Scope{OrgID: orgID}, models.OpenAIChatGPTConfig{
		AccessToken:  "cha_token",
		RefreshToken: "chr_refresh",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	})

	// Get the credential ID assigned by Upsert.
	cred, err := store.Get(context.Background(), models.Scope{OrgID: orgID}, models.ProviderOpenAIChatGPT)
	if err != nil {
		t.Fatalf("failed to get credential: %v", err)
	}

	if err := svc.Disconnect(context.Background(), models.Scope{OrgID: orgID}, cred.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify credential was deleted.
	_, err = store.Get(context.Background(), models.Scope{OrgID: orgID}, models.ProviderOpenAIChatGPT)
	if err == nil {
		t.Error("expected credential to be deleted")
	}
}

func TestDisconnectForOrg_WrongOrg(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	otherOrgID := uuid.New()
	store.Upsert(context.Background(), models.Scope{OrgID: orgID}, models.OpenAIChatGPTConfig{
		AccessToken: "cha_token",
	})

	cred, _ := store.Get(context.Background(), models.Scope{OrgID: orgID}, models.ProviderOpenAIChatGPT)

	// Attempt to disconnect using a different org should fail.
	err := svc.DisconnectForOrg(context.Background(), models.Scope{OrgID: otherOrgID}, cred.ID)
	if err != ErrCredentialNotFound {
		t.Fatalf("expected ErrCredentialNotFound, got: %v", err)
	}

	// Credential should still exist.
	_, err = store.Get(context.Background(), models.Scope{OrgID: orgID}, models.ProviderOpenAIChatGPT)
	if err != nil {
		t.Error("credential should not have been deleted")
	}
}

func TestDisconnectForOrg_NotFound(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	err := svc.DisconnectForOrg(context.Background(), models.Scope{OrgID: uuid.New()}, uuid.New())
	if err != ErrCredentialNotFound {
		t.Fatalf("expected ErrCredentialNotFound, got: %v", err)
	}
}

func TestListSubscriptions(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()

	// Empty org should return empty list.
	subs, err := svc.ListSubscriptions(context.Background(), models.Scope{OrgID: orgID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(subs) != 0 {
		t.Errorf("expected 0 subscriptions, got %d", len(subs))
	}

	// Add a credential.
	store.Upsert(context.Background(), models.Scope{OrgID: orgID}, models.OpenAIChatGPTConfig{
		AccessToken: "cha_token",
		AccountType: "plus",
	})

	subs, err = svc.ListSubscriptions(context.Background(), models.Scope{OrgID: orgID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("expected 1 subscription, got %d", len(subs))
	}
	if subs[0].Status != "active" {
		t.Errorf("expected active status, got %s", subs[0].Status)
	}
	if subs[0].AccountType != "plus" {
		t.Errorf("expected account type 'plus', got %s", subs[0].AccountType)
	}
}

func TestListSubscriptions_NilCredentials(t *testing.T) {
	t.Parallel()
	svc := NewService(nil, zerolog.Nop())

	subs, err := svc.ListSubscriptions(context.Background(), models.Scope{OrgID: uuid.New()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if subs != nil {
		t.Errorf("expected nil for nil credentials store, got %+v", subs)
	}
}

func TestDisconnect_NilCredentials(t *testing.T) {
	t.Parallel()
	svc := NewService(nil, zerolog.Nop())
	// Should not panic when credentials store is nil.
	err := svc.Disconnect(context.Background(), models.Scope{OrgID: uuid.New()}, uuid.New())
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}

// failingCredentialStore returns errors for all operations (simulates DB failure).
type failingCredentialStore struct{}

func (s *failingCredentialStore) Disable(_ context.Context, _ models.Scope, _ models.ProviderName) error {
	return fmt.Errorf("db connection refused")
}
func (s *failingCredentialStore) UpsertWithLabel(_ context.Context, _ models.Scope, _ *uuid.UUID, _ string, _ models.ProviderConfig) (*uuid.UUID, error) {
	return nil, fmt.Errorf("db connection refused")
}
func (s *failingCredentialStore) InsertPendingAuth(_ context.Context, _ models.Scope, _ *uuid.UUID, _ string, _ models.ProviderConfig) (*uuid.UUID, error) {
	return nil, fmt.Errorf("db connection refused")
}
func (s *failingCredentialStore) GetByID(_ context.Context, _ models.Scope, _ uuid.UUID) (*models.DecryptedCredential, error) {
	return nil, fmt.Errorf("db connection refused")
}
func (s *failingCredentialStore) ListByProvider(_ context.Context, _ models.Scope, _ models.ProviderName) ([]models.DecryptedCredential, error) {
	return nil, fmt.Errorf("db connection refused")
}
func (s *failingCredentialStore) GetByProviderAndLabel(_ context.Context, _ models.Scope, _ models.ProviderName, _ string) (*models.DecryptedCredential, error) {
	return nil, fmt.Errorf("db connection refused")
}
func (s *failingCredentialStore) ClaimNextRoundRobin(_ context.Context, _ models.Scope, _ models.ProviderName) (*models.DecryptedCredential, error) {
	return nil, fmt.Errorf("db connection refused")
}
func (s *failingCredentialStore) DisableByID(_ context.Context, _ models.Scope, _ uuid.UUID) error {
	return fmt.Errorf("db connection refused")
}
func (s *failingCredentialStore) UpdateStatusByID(_ context.Context, _ models.Scope, _ uuid.UUID, _ string) error {
	return fmt.Errorf("db connection refused")
}
func (s *failingCredentialStore) UpsertByID(_ context.Context, _ models.Scope, _ uuid.UUID, _ models.ProviderConfig) error {
	return fmt.Errorf("db connection refused")
}
func (s *failingCredentialStore) ExistsForProviderByID(_ context.Context, _ models.Scope, _ uuid.UUID, _ models.ProviderName) (bool, error) {
	return false, fmt.Errorf("db connection refused")
}

func TestGetValidToken_DBError(t *testing.T) {
	t.Parallel()
	svc := NewService(&failingCredentialStore{}, zerolog.Nop())

	_, err := svc.GetValidToken(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("expected error for DB failure, got nil")
	}
}

func TestPollForToken_RateLimited(t *testing.T) {
	t.Parallel()
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "authorization_pending",
		})
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()
	svc.pending.Store(pendingKey(models.Scope{OrgID: orgID}, ""), &PendingAuth{
		DeviceAuthID: "dev_123",
		ExpiresAt:    time.Now().Add(15 * time.Minute),
		Interval:     5,
		LastPollAt:   time.Now(), // Just polled.
	})

	// Second poll should be rate-limited (no HTTP call).
	status, err := svc.PollForToken(context.Background(), models.Scope{OrgID: orgID}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "pending" {
		t.Errorf("expected pending status, got %s", status.Status)
	}
	if callCount != 0 {
		t.Errorf("expected 0 HTTP calls (rate-limited), got %d", callCount)
	}
}

func TestPollForToken_AccessDenied(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "access_denied",
		})
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()
	svc.pending.Store(pendingKey(models.Scope{OrgID: orgID}, ""), &PendingAuth{
		DeviceAuthID: "dev_123",
		ExpiresAt:    time.Now().Add(15 * time.Minute),
		Interval:     5,
	})

	status, err := svc.PollForToken(context.Background(), models.Scope{OrgID: orgID}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "error" {
		t.Errorf("expected error status, got %s", status.Status)
	}
	if status.Message != "authentication denied by user" {
		t.Errorf("unexpected message: %s", status.Message)
	}

	// Verify pending state was cleaned up.
	if _, ok := svc.pending.Load(pendingKey(models.Scope{OrgID: orgID}, "")); ok {
		t.Error("expected pending auth to be deleted after access_denied")
	}
}

func TestPollForToken_ExpiredToken(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "expired_token",
		})
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()
	svc.pending.Store(pendingKey(models.Scope{OrgID: orgID}, ""), &PendingAuth{
		DeviceAuthID: "dev_123",
		ExpiresAt:    time.Now().Add(15 * time.Minute),
		Interval:     5,
	})

	status, err := svc.PollForToken(context.Background(), models.Scope{OrgID: orgID}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "expired" {
		t.Errorf("expected expired status, got %s", status.Status)
	}
}

func TestPollForToken_UnknownError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "some_unknown_error",
		})
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()
	svc.pending.Store(pendingKey(models.Scope{OrgID: orgID}, ""), &PendingAuth{
		DeviceAuthID: "dev_123",
		ExpiresAt:    time.Now().Add(15 * time.Minute),
		Interval:     5,
	})

	status, err := svc.PollForToken(context.Background(), models.Scope{OrgID: orgID}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "error" {
		t.Errorf("expected error status, got %s", status.Status)
	}
	if status.Message != "auth error: some_unknown_error" {
		t.Errorf("unexpected message: %s", status.Message)
	}
}

func TestPollForToken_EmptyErrorField(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a response with no "error" field (e.g. unexpected format).
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"message": "internal error"}`))
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()
	svc.pending.Store(pendingKey(models.Scope{OrgID: orgID}, ""), &PendingAuth{
		DeviceAuthID: "dev_123",
		ExpiresAt:    time.Now().Add(15 * time.Minute),
		Interval:     5,
	})

	status, err := svc.PollForToken(context.Background(), models.Scope{OrgID: orgID}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "error" {
		t.Errorf("expected error status, got %s", status.Status)
	}
	// Should include HTTP status code instead of empty string.
	if status.Message != "auth error: unexpected response (HTTP 500)" {
		t.Errorf("unexpected message: %s", status.Message)
	}
}

func TestPollForToken_RestoreFromDB_Active(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	// Pre-store an active credential in the DB.
	store.Upsert(context.Background(), models.Scope{OrgID: orgID}, models.OpenAIChatGPTConfig{
		AccessToken:  "cha_active_token",
		RefreshToken: "chr_refresh",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
		AccountType:  "plus",
	})

	// Poll with no in-memory state — should find it in DB.
	status, err := svc.PollForToken(context.Background(), models.Scope{OrgID: orgID}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "completed" {
		t.Errorf("expected completed status, got %s", status.Status)
	}
	if status.AccountType != "plus" {
		t.Errorf("expected account type 'plus', got %s", status.AccountType)
	}
}

func TestPollForToken_EmptyLabelDetectsActiveLabeledPersonalSubscription(t *testing.T) {
	t.Parallel()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	userID := uuid.New()
	scope := models.Scope{OrgID: orgID, UserID: &userID}
	_, err := store.UpsertWithLabel(context.Background(), scope, &userID, "Codex subscription", models.OpenAIChatGPTConfig{
		AccessToken:  "cha_personal",
		RefreshToken: "chr_personal",
		ExpiresAt:    time.Now().Add(time.Hour),
		AccountType:  "plus",
	})
	if err != nil {
		t.Fatalf("seed personal subscription: %v", err)
	}

	status, err := svc.PollForToken(context.Background(), scope, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "completed" {
		t.Fatalf("expected completed status for active labeled personal subscription, got %q", status.Status)
	}
	if status.AccountType != "plus" {
		t.Fatalf("expected account type 'plus', got %q", status.AccountType)
	}
}

func TestPollForToken_RestoreFromDB_PendingAuth(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "authorization_pending",
		})
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()
	// Pre-store a pending_auth credential in the DB.
	k := store.key(orgID, models.ProviderOpenAIChatGPT)
	store.creds[k] = &models.DecryptedCredential{
		OrgID:    orgID,
		Provider: models.ProviderOpenAIChatGPT,
		Config: models.OpenAIChatGPTConfig{
			DeviceAuthID:    "dev_restored",
			UserCode:        "REST-CODE",
			VerificationURI: "https://auth.openai.com/codex/device",
			ExpiresAt:       time.Now().Add(10 * time.Minute),
			PollInterval:    5,
		},
		Status: "pending_auth",
	}

	status, err := svc.PollForToken(context.Background(), models.Scope{OrgID: orgID}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "pending" {
		t.Errorf("expected pending status from restored auth, got %s", status.Status)
	}
}

func TestPollForToken_InvalidConfigType(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	// Store an active credential with the wrong config type.
	k := store.key(orgID, models.ProviderOpenAIChatGPT)
	store.creds[k] = &models.DecryptedCredential{
		OrgID:    orgID,
		Provider: models.ProviderOpenAIChatGPT,
		Config:   models.AnthropicConfig{APIKey: "wrong-type"},
		Status:   "active",
	}

	status, err := svc.PollForToken(context.Background(), models.Scope{OrgID: orgID}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "error" {
		t.Errorf("expected error status for invalid config, got %s", status.Status)
	}
}

func TestGetValidToken_NilCredentials(t *testing.T) {
	t.Parallel()
	svc := NewService(nil, zerolog.Nop())

	cfg, err := svc.GetValidToken(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if cfg != nil {
		t.Errorf("expected nil config, got %+v", cfg)
	}
}

func TestGetValidToken_InactiveStatus(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	store.Upsert(context.Background(), models.Scope{OrgID: orgID}, models.OpenAIChatGPTConfig{
		AccessToken:  "cha_tok",
		RefreshToken: "chr_tok",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	})
	// Mark as inactive.
	store.UpdateStatus(context.Background(), models.Scope{OrgID: orgID}, models.ProviderOpenAIChatGPT, "invalid")

	cfg, err := svc.GetValidToken(context.Background(), orgID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Errorf("expected nil config for inactive credential, got %+v", cfg)
	}
}

func TestGetValidToken_InvalidConfigType(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	k := store.key(orgID, models.ProviderOpenAIChatGPT)
	store.creds[k] = &models.DecryptedCredential{
		OrgID:    orgID,
		Provider: models.ProviderOpenAIChatGPT,
		Config:   models.AnthropicConfig{APIKey: "wrong"},
		Status:   "active",
	}

	_, err := svc.GetValidToken(context.Background(), orgID)
	if err == nil {
		t.Fatal("expected error for invalid config type")
	}
}

func TestGetValidToken_RefreshFailsButTokenValid(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Refresh endpoint returns 500 error.
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "server_error"}`))
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()
	// Token expiring within refresh window but not yet expired.
	store.Upsert(context.Background(), models.Scope{OrgID: orgID}, models.OpenAIChatGPTConfig{
		AccessToken:  "cha_still_valid",
		RefreshToken: "chr_refresh",
		ExpiresAt:    time.Now().Add(3 * time.Minute), // Within 5-min window but still valid.
		AccountType:  "plus",
	})

	cfg, err := svc.GetValidToken(context.Background(), orgID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config when token still valid despite refresh failure")
	}
	if cfg.AccessToken != "cha_still_valid" {
		t.Errorf("expected original token, got %s", cfg.AccessToken)
	}
}

func TestGetValidToken_RefreshFailsTokenExpired(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "server_error"}`))
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()
	// Token already expired and refresh fails.
	store.Upsert(context.Background(), models.Scope{OrgID: orgID}, models.OpenAIChatGPTConfig{
		AccessToken:  "cha_expired",
		RefreshToken: "chr_refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Minute), // Already expired.
	})

	_, err := svc.GetValidToken(context.Background(), orgID)
	if err == nil {
		t.Fatal("expected error when token expired and refresh fails")
	}
}

func TestRefreshTokenByID_NonAuthError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`internal server error`))
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()
	store.Upsert(context.Background(), models.Scope{OrgID: orgID}, models.OpenAIChatGPTConfig{
		AccessToken:  "cha_tok",
		RefreshToken: "chr_tok",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
	})
	cred, err := store.Get(context.Background(), models.Scope{OrgID: orgID}, models.ProviderOpenAIChatGPT)
	if err != nil {
		t.Fatalf("failed to get credential: %v", err)
	}

	_, err = svc.RefreshTokenByID(context.Background(), models.Scope{OrgID: orgID}, cred.ID)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestRefreshTokenByID_InvalidConfigType(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	credID := uuid.New()
	k := store.key(orgID, models.ProviderOpenAIChatGPT)
	store.creds[k] = &models.DecryptedCredential{
		ID:       credID,
		OrgID:    orgID,
		Provider: models.ProviderOpenAIChatGPT,
		Config:   models.AnthropicConfig{APIKey: "wrong"},
		Status:   "active",
	}

	_, err := svc.RefreshTokenByID(context.Background(), models.Scope{OrgID: orgID}, credID)
	if err == nil {
		t.Fatal("expected error for invalid config type")
	}
}

func TestRefreshTokenByID_NotFound(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	_, err := svc.RefreshTokenByID(context.Background(), models.Scope{OrgID: uuid.New()}, uuid.New())
	if err == nil {
		t.Fatal("expected error when credential not found")
	}
}

func TestInitiateDeviceAuth_ServerError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`server error`))
	}))
	defer server.Close()

	svc := NewService(nil, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	_, err := svc.InitiateDeviceAuth(context.Background(), models.Scope{OrgID: uuid.New()}, nil, "")
	if err == nil {
		t.Fatal("expected error for server error response")
	}
}

func TestInitiateDeviceAuth_InvalidJSON(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not json`))
	}))
	defer server.Close()

	svc := NewService(nil, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	_, err := svc.InitiateDeviceAuth(context.Background(), models.Scope{OrgID: uuid.New()}, nil, "")
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

// TestInitiateDeviceAuth_LabelConflict verifies that initiating a device auth
// flow with a label that already names an active credential surfaces a typed
// ErrCredentialLabelTaken error and does not leave stale in-memory pending state.
func TestInitiateDeviceAuth_LabelConflict(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"device_auth_id":"dev_x","user_code":"AAAA-BBBB","verification_uri":"https://x","expires_in":900,"interval":5}`))
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()
	const label = "Team A"

	// Seed an active credential under this label so the next initiate must conflict.
	if _, err := store.UpsertWithLabel(context.Background(), models.Scope{OrgID: orgID}, nil, label, models.OpenAIChatGPTConfig{
		AccessToken:  "cha_existing",
		RefreshToken: "chr_existing",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	}); err != nil {
		t.Fatalf("seed active credential: %v", err)
	}

	_, err := svc.InitiateDeviceAuth(context.Background(), models.Scope{OrgID: orgID}, nil, label)
	if err == nil {
		t.Fatal("expected error when initiating against an existing active label")
	}

	var taken *db.ErrCredentialLabelTaken
	if !errors.As(err, &taken) {
		t.Fatalf("expected wrapped *db.ErrCredentialLabelTaken, got %T: %v", err, err)
	}
	if taken.Label != label {
		t.Errorf("expected ErrCredentialLabelTaken.Label=%q, got %q", label, taken.Label)
	}
	if taken.ExistingStatus != "active" {
		t.Errorf("expected ExistingStatus=active, got %q", taken.ExistingStatus)
	}

	// In-memory pending state must be cleaned up so the next attempt is not
	// shadowed by a stale entry.
	if _, ok := svc.pending.Load(pendingKey(models.Scope{OrgID: orgID}, label)); ok {
		t.Error("expected pending state for label to be cleared after conflict")
	}
}

// TestInitiateDeviceAuth_DisabledLabelResurrects verifies that re-adding a
// label whose previous credential was disconnected (status=disabled) succeeds
// — the row is reused and reset to pending_auth.
func TestInitiateDeviceAuth_DisabledLabelResurrects(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"device_auth_id":"dev_x","user_code":"AAAA-BBBB","verification_uri":"https://x","expires_in":900,"interval":5}`))
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()
	const label = "Team A"

	// Seed a disabled credential so the next initiate should resurrect it.
	k := store.labelKey(orgID, models.ProviderOpenAIChatGPT, label)
	store.creds[k] = &models.DecryptedCredential{
		ID:       uuid.New(),
		OrgID:    orgID,
		Provider: models.ProviderOpenAIChatGPT,
		Label:    label,
		Status:   "disabled",
		Config:   models.OpenAIChatGPTConfig{},
	}

	if _, err := svc.InitiateDeviceAuth(context.Background(), models.Scope{OrgID: orgID}, nil, label); err != nil {
		t.Fatalf("expected re-initiate against disabled label to succeed, got: %v", err)
	}

	cred, err := store.GetByProviderAndLabel(context.Background(), models.Scope{OrgID: orgID}, models.ProviderOpenAIChatGPT, label)
	if err != nil {
		t.Fatalf("expected to find resurrected credential: %v", err)
	}
	if cred.Status != "pending_auth" {
		t.Errorf("expected resurrected status=pending_auth, got %q", cred.Status)
	}
}

func TestIsNotFoundError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error", nil, false},
		{"sentinel ErrCredentialNotFound", ErrCredentialNotFound, true},
		{"wrapped ErrCredentialNotFound", fmt.Errorf("get cred: %w", ErrCredentialNotFound), true},
		{"sentinel pgx.ErrNoRows", pgx.ErrNoRows, true},
		{"wrapped pgx.ErrNoRows", fmt.Errorf("query: %w", pgx.ErrNoRows), true},
		{"unrelated string match", fmt.Errorf("not found"), false},
		{"other error", fmt.Errorf("db connection refused"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isNotFoundError(tt.err); got != tt.expected {
				t.Errorf("isNotFoundError(%v) = %v, want %v", tt.err, got, tt.expected)
			}
		})
	}
}

func TestRefreshTokenByID_ConcurrentRefreshesAreSerialized(t *testing.T) {
	t.Parallel()

	var refreshCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := refreshCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":  fmt.Sprintf("cha_refreshed_%d", n),
			"refresh_token": fmt.Sprintf("chr_new_%d", n),
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()
	store.Upsert(context.Background(), models.Scope{OrgID: orgID}, models.OpenAIChatGPTConfig{
		AccessToken:  "cha_old",
		RefreshToken: "chr_old",
		ExpiresAt:    time.Now().Add(-1 * time.Minute), // Expired.
	})
	cred, err := store.Get(context.Background(), models.Scope{OrgID: orgID}, models.ProviderOpenAIChatGPT)
	if err != nil {
		t.Fatalf("failed to get credential: %v", err)
	}

	// Fire two concurrent refreshes.
	done := make(chan *models.OpenAIChatGPTConfig, 2)
	for i := 0; i < 2; i++ {
		go func() {
			cfg, _ := svc.RefreshTokenByID(context.Background(), models.Scope{OrgID: orgID}, cred.ID)
			done <- cfg
		}()
	}

	cfg1 := <-done
	cfg2 := <-done

	// The mutex should cause the second goroutine to see the already-refreshed
	// token (within the refresh window check) and skip the HTTP call.
	// Only 1 HTTP call should have been made.
	if got := refreshCount.Load(); got != 1 {
		t.Errorf("expected exactly 1 HTTP refresh call (serialized), got %d", got)
	}

	// Both should return a valid token.
	if cfg1 == nil || cfg2 == nil {
		t.Fatal("expected both goroutines to return a valid config")
	}
}

func TestGetValidToken_RefreshTokenReused_ExpiredToken(t *testing.T) {
	t.Parallel()

	// Simulate refresh_token_reused error from OpenAI.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "refresh_token_reused"}`))
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()
	// Store a credential that is ALREADY EXPIRED and needs refresh.
	store.Upsert(context.Background(), models.Scope{OrgID: orgID}, models.OpenAIChatGPTConfig{
		AccessToken:  "cha_expired",
		RefreshToken: "chr_reused",
		ExpiresAt:    time.Now().Add(-10 * time.Minute), // Already expired.
		AccountType:  "plus",
	})

	// GetValidToken should NOT return the expired token.
	// Previously this was a bug: it returned the expired token on
	// refresh_token_reused errors regardless of expiry.
	cfg, err := svc.GetValidToken(context.Background(), orgID)
	if err == nil {
		t.Fatal("expected error when token is expired and refresh fails with refresh_token_reused")
	}
	if cfg != nil {
		t.Errorf("expected nil config for expired token, got access_token=%s", cfg.AccessToken)
	}
}

func TestGetValidToken_RefreshTokenReused_ValidToken(t *testing.T) {
	t.Parallel()

	// Simulate refresh_token_reused error from OpenAI.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "refresh_token_reused"}`))
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()
	// Store a credential within the refresh window but NOT yet expired.
	store.Upsert(context.Background(), models.Scope{OrgID: orgID}, models.OpenAIChatGPTConfig{
		AccessToken:  "cha_still_valid",
		RefreshToken: "chr_reused",
		ExpiresAt:    time.Now().Add(3 * time.Minute), // Within 5-min window but still valid.
		AccountType:  "plus",
	})

	// GetValidToken should return the still-valid token even though refresh failed.
	cfg, err := svc.GetValidToken(context.Background(), orgID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config when token still valid despite refresh_token_reused")
	}
	if cfg.AccessToken != "cha_still_valid" {
		t.Errorf("expected original token, got %s", cfg.AccessToken)
	}
}

// TestDisconnectForOrg_AlreadyDisabled verifies DisconnectForOrg is idempotent
// for rows whose status is already "disabled": the ExistsByID check still sees
// the row (disabled rows aren't filtered out), so a second disconnect call
// returns nil rather than ErrCredentialNotFound. This matches the user mental
// model where clicking "Remove" twice shouldn't surface an error.
func TestDisconnectForOrg_AlreadyDisabled(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	credID, err := store.UpsertWithLabel(context.Background(), models.Scope{OrgID: orgID}, nil, "Team A", models.OpenAIChatGPTConfig{
		AccessToken:  "cha_token",
		RefreshToken: "chr_refresh",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	})
	if err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	// Flip the row's status to "disabled" directly (simulating a row the real
	// DB would leave in status='disabled' after a prior DisableByID).
	k := store.labelKey(orgID, models.ProviderOpenAIChatGPT, "Team A")
	store.creds[k].Status = "disabled"

	// Disconnecting an already-disabled row should return nil, not ErrCredentialNotFound.
	if err := svc.DisconnectForOrg(context.Background(), models.Scope{OrgID: orgID}, *credID); err != nil {
		t.Fatalf("expected idempotent disconnect to succeed, got: %v", err)
	}
}

// alwaysSameCredStore wraps mockCredentialStore but always hands back the same
// credential from ClaimNextRoundRobin and swallows UpdateStatusByID. This lets
// us exercise the dedupe break-out path in GetValidToken: even if a broken
// credential isn't marked invalid (simulating a failed status update), the
// loop must terminate instead of spinning forever.
type alwaysSameCredStore struct {
	*mockCredentialStore
	fixed *models.DecryptedCredential
}

func (s *alwaysSameCredStore) ClaimNextRoundRobin(_ context.Context, _ models.Scope, _ models.ProviderName) (*models.DecryptedCredential, error) {
	return s.fixed, nil
}

func (s *alwaysSameCredStore) UpdateStatusByID(_ context.Context, _ models.Scope, _ uuid.UUID, _ string) error {
	return nil // Simulate a silent status-update failure (no row actually marked invalid).
}

// TestGetValidToken_TriedDedupeBreaksLoop covers the safety net in GetValidToken
// that breaks out when ClaimNextRoundRobin re-serves a credential we already
// tried this call. Without the break, a store that fails to filter invalid rows
// would cause the loop to spin until maxRoundRobinAttempts with the same
// credential, wasting HTTP calls.
func TestGetValidToken_TriedDedupeBreaksLoop(t *testing.T) {
	t.Parallel()

	var refreshCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshCalls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "invalid_grant"}`))
	}))
	defer server.Close()

	base := newMockCredentialStore()
	orgID := uuid.New()
	if _, err := base.UpsertWithLabel(context.Background(), models.Scope{OrgID: orgID}, nil, "Solo", models.OpenAIChatGPTConfig{
		AccessToken:  "cha_expired",
		RefreshToken: "chr_broken",
		ExpiresAt:    time.Now().Add(-1 * time.Hour), // Already expired.
	}); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	k := base.labelKey(orgID, models.ProviderOpenAIChatGPT, "Solo")
	fixed := base.creds[k]

	store := &alwaysSameCredStore{mockCredentialStore: base, fixed: fixed}
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	cfg, err := svc.GetValidToken(context.Background(), orgID)
	if err == nil {
		t.Fatal("expected error when only credential has broken refresh and expired token")
	}
	if cfg != nil {
		t.Errorf("expected nil config, got %+v", cfg)
	}

	// The break-out should kick in on the second claim, so at most one refresh
	// HTTP call should have fired (for the first attempt). Without dedupe we
	// would see maxRoundRobinAttempts (5) refresh calls.
	if got := refreshCalls.Load(); got > 1 {
		t.Errorf("expected at most 1 refresh call before dedupe break, got %d", got)
	}
}

// TestDisconnectAll_CleansRefreshMutexes verifies that DisconnectAll removes
// per-credential refresh mutex entries for every credential belonging to the
// org. Without this cleanup the sync.Map would leak entries across the
// lifetime of the process for any org that reconnects with new credential IDs.
func TestDisconnectAll_CleansRefreshMutexes(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	firstID, err := store.UpsertWithLabel(context.Background(), models.Scope{OrgID: orgID}, nil, "Team A", models.OpenAIChatGPTConfig{
		AccessToken:  "cha_a",
		RefreshToken: "chr_a",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	})
	if err != nil {
		t.Fatalf("seed first credential: %v", err)
	}
	secondID, err := store.UpsertWithLabel(context.Background(), models.Scope{OrgID: orgID}, nil, "Team B", models.OpenAIChatGPTConfig{
		AccessToken:  "cha_b",
		RefreshToken: "chr_b",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	})
	if err != nil {
		t.Fatalf("seed second credential: %v", err)
	}

	// Populate the refresh-mutex map as real traffic would.
	_ = svc.credRefreshMu(*firstID)
	_ = svc.credRefreshMu(*secondID)
	if _, ok := svc.refreshMu.Load(firstID.String()); !ok {
		t.Fatal("expected refresh mutex to be populated for first credential")
	}
	if _, ok := svc.refreshMu.Load(secondID.String()); !ok {
		t.Fatal("expected refresh mutex to be populated for second credential")
	}

	if err := svc.DisconnectAll(context.Background(), models.Scope{OrgID: orgID}); err != nil {
		t.Fatalf("DisconnectAll: %v", err)
	}

	if _, ok := svc.refreshMu.Load(firstID.String()); ok {
		t.Error("expected refresh mutex for first credential to be cleared after DisconnectAll")
	}
	if _, ok := svc.refreshMu.Load(secondID.String()); ok {
		t.Error("expected refresh mutex for second credential to be cleared after DisconnectAll")
	}
}

// --- Personal-scope coverage ---
//
// The OAuth flow used to be exclusively org-scoped; PR #729-equivalent
// added personal scope so individual users can connect their own ChatGPT
// subscription as a personal-stack credential. The tests below lock the
// new contract: scope flows through Initiate, GetByProviderAndLabel sees
// the personal row, and the personal pendingKey shape is distinct from
// the org one so the same label can coexist across scopes.

func TestPendingKey_ScopesAreDistinct(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	userID := uuid.New()
	orgKey := pendingKey(models.Scope{OrgID: orgID}, "Team A")
	personalKey := pendingKey(models.Scope{OrgID: orgID, UserID: &userID}, "Team A")
	if orgKey == personalKey {
		t.Fatal("pendingKey must namespace personal scope distinctly from org scope")
	}
	if !strings.Contains(personalKey, ":u:") {
		t.Errorf("personal pendingKey expected to contain :u: segment, got %q", personalKey)
	}

	collidingOrgLabel := "u:" + userID.String() + ":Team A"
	collidingOrgKey := pendingKey(models.Scope{OrgID: orgID}, collidingOrgLabel)
	if collidingOrgKey == personalKey {
		t.Fatalf("org label %q must not collide with personal pending key %q", collidingOrgLabel, personalKey)
	}
}

func TestInitiateDeviceAuth_PersonalScope(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"device_auth_id":   "dev_personal_1",
			"user_code":        "PERS-1234",
			"verification_uri": "https://auth.openai.com/codex/device",
			"expires_in":       900,
			"interval":         5,
		})
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()
	userID := uuid.New()
	personalScope := models.Scope{OrgID: orgID, UserID: &userID}

	resp, err := svc.InitiateDeviceAuth(context.Background(), personalScope, &userID, "Personal A")
	if err != nil {
		t.Fatalf("personal initiate: %v", err)
	}
	if resp.UserCode != "PERS-1234" {
		t.Errorf("expected user code PERS-1234, got %q", resp.UserCode)
	}

	// The personal pending row must be reachable from personal scope and
	// invisible to org scope — that is the whole point of the refactor.
	cred, err := store.GetByProviderAndLabel(context.Background(), personalScope, models.ProviderOpenAIChatGPT, "Personal A")
	if err != nil {
		t.Fatalf("expected personal pending row to be persisted: %v", err)
	}
	if cred.Status != "pending_auth" {
		t.Errorf("expected pending_auth status, got %q", cred.Status)
	}
	if cred.CreatedBy == nil || *cred.CreatedBy != userID {
		t.Errorf("expected created_by=%s, got %v", userID, cred.CreatedBy)
	}

	if _, err := store.GetByProviderAndLabel(context.Background(), models.Scope{OrgID: orgID}, models.ProviderOpenAIChatGPT, "Personal A"); err == nil {
		t.Error("personal-scope row must not be visible to an org-scope read")
	}
}

func TestInitiateDeviceAuth_SameLabelAcrossScopes(t *testing.T) {
	// A user adding a personal "Codex backup" subscription must not collide
	// with an org-scope "Codex backup" subscription that already exists. The
	// pending_auth keys live in distinct partitions; this test guards
	// against a regression where they share a row.
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"device_auth_id":   "dev_shared_label",
			"user_code":        "SHRD-1234",
			"verification_uri": "https://auth.openai.com/codex/device",
			"expires_in":       900,
			"interval":         5,
		})
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()
	userID := uuid.New()
	label := "Codex backup"

	if _, err := svc.InitiateDeviceAuth(context.Background(), models.Scope{OrgID: orgID}, nil, label); err != nil {
		t.Fatalf("org initiate: %v", err)
	}
	if _, err := svc.InitiateDeviceAuth(context.Background(), models.Scope{OrgID: orgID, UserID: &userID}, &userID, label); err != nil {
		t.Fatalf("personal initiate with same label must succeed: %v", err)
	}

	orgRow, err := store.GetByProviderAndLabel(context.Background(), models.Scope{OrgID: orgID}, models.ProviderOpenAIChatGPT, label)
	if err != nil {
		t.Fatalf("expected org pending row: %v", err)
	}
	personalRow, err := store.GetByProviderAndLabel(context.Background(), models.Scope{OrgID: orgID, UserID: &userID}, models.ProviderOpenAIChatGPT, label)
	if err != nil {
		t.Fatalf("expected personal pending row: %v", err)
	}
	if orgRow.ID == personalRow.ID {
		t.Error("org and personal rows must be distinct credentials despite sharing the label")
	}
}
