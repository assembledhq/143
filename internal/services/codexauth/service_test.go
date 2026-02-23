package codexauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

// mockCredentialStore is a simple in-memory credential store for testing.
type mockCredentialStore struct {
	creds  map[string]*models.DecryptedCredential
	status map[string]string
}

func newMockCredentialStore() *mockCredentialStore {
	return &mockCredentialStore{
		creds:  make(map[string]*models.DecryptedCredential),
		status: make(map[string]string),
	}
}

func (m *mockCredentialStore) key(orgID uuid.UUID, provider models.ProviderName) string {
	return orgID.String() + ":" + string(provider)
}

func (m *mockCredentialStore) Upsert(_ context.Context, orgID uuid.UUID, cfg models.ProviderConfig) error {
	k := m.key(orgID, cfg.Provider())
	m.creds[k] = &models.DecryptedCredential{
		OrgID:    orgID,
		Provider: cfg.Provider(),
		Config:   cfg,
		Status:   "active",
	}
	return nil
}

func (m *mockCredentialStore) Get(_ context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error) {
	k := m.key(orgID, provider)
	cred, ok := m.creds[k]
	if !ok {
		return nil, ErrCredentialNotFound
	}
	return cred, nil
}

func (m *mockCredentialStore) UpdateStatus(_ context.Context, orgID uuid.UUID, provider models.ProviderName, status string) error {
	k := m.key(orgID, provider)
	if cred, ok := m.creds[k]; ok {
		cred.Status = status
	}
	m.status[k] = status
	return nil
}

func (m *mockCredentialStore) Disable(_ context.Context, orgID uuid.UUID, provider models.ProviderName) error {
	k := m.key(orgID, provider)
	delete(m.creds, k)
	return nil
}

func TestInitiateDeviceAuth(t *testing.T) {
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
			"device_code":      "dev_123",
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
	resp, err := svc.InitiateDeviceAuth(context.Background(), orgID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.UserCode != "ABCD-1234" {
		t.Errorf("expected user code ABCD-1234, got %s", resp.UserCode)
	}
	if resp.ExpiresIn != 900 {
		t.Errorf("expected expires_in 900, got %d", resp.ExpiresIn)
	}
}

func TestPollForToken_AuthorizationPending(t *testing.T) {
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
	svc.pending.Store(orgID.String(), &PendingAuth{
		DeviceCode: "dev_123",
		UserCode:   "ABCD-1234",
		ExpiresAt:  time.Now().Add(15 * time.Minute),
		Interval:   5,
	})

	status, err := svc.PollForToken(context.Background(), orgID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "pending" {
		t.Errorf("expected pending status, got %s", status.Status)
	}
}

func TestPollForToken_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":  "cha_test_access_token_12345",
			"refresh_token": "chr_test_refresh_token_12345",
			"expires_in":    3600,
			"token_type":    "Bearer",
		})
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()
	svc.pending.Store(orgID.String(), &PendingAuth{
		DeviceCode: "dev_123",
		UserCode:   "ABCD-1234",
		ExpiresAt:  time.Now().Add(15 * time.Minute),
		Interval:   5,
	})

	status, err := svc.PollForToken(context.Background(), orgID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "completed" {
		t.Errorf("expected completed status, got %s", status.Status)
	}

	// Verify credential was stored.
	cred, err := store.Get(context.Background(), orgID, models.ProviderOpenAIChatGPT)
	if err != nil {
		t.Fatalf("credential not stored: %v", err)
	}
	cfg := cred.Config.(models.OpenAIChatGPTConfig)
	if cfg.AccessToken != "cha_test_access_token_12345" {
		t.Errorf("unexpected access token: %s", cfg.AccessToken)
	}
}

func TestPollForToken_Expired(t *testing.T) {
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	svc.pending.Store(orgID.String(), &PendingAuth{
		DeviceCode: "dev_123",
		ExpiresAt:  time.Now().Add(-1 * time.Minute), // Already expired.
		Interval:   5,
	})

	status, err := svc.PollForToken(context.Background(), orgID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "expired" {
		t.Errorf("expected expired status, got %s", status.Status)
	}
}

func TestPollForToken_SlowDown(t *testing.T) {
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
	svc.pending.Store(orgID.String(), &PendingAuth{
		DeviceCode: "dev_123",
		ExpiresAt:  time.Now().Add(15 * time.Minute),
		Interval:   5,
	})

	status, err := svc.PollForToken(context.Background(), orgID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "pending" {
		t.Errorf("expected pending status, got %s", status.Status)
	}

	// Verify interval was doubled.
	val, _ := svc.pending.Load(orgID.String())
	pending := val.(*PendingAuth)
	if pending.Interval != 10 {
		t.Errorf("expected interval 10 after slow_down, got %d", pending.Interval)
	}
}

func TestGetValidToken_NotConfigured(t *testing.T) {
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
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	// Pre-store a valid credential.
	store.Upsert(context.Background(), orgID, models.OpenAIChatGPTConfig{
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
	store.Upsert(context.Background(), orgID, models.OpenAIChatGPTConfig{
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

func TestRefreshToken_Revoked(t *testing.T) {
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
	store.Upsert(context.Background(), orgID, models.OpenAIChatGPTConfig{
		AccessToken:  "cha_expired",
		RefreshToken: "chr_revoked",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
	})

	_, err := svc.RefreshToken(context.Background(), orgID)
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
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	store.Upsert(context.Background(), orgID, models.OpenAIChatGPTConfig{
		AccessToken:  "cha_token",
		RefreshToken: "chr_refresh",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	})

	if err := svc.Disconnect(context.Background(), orgID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify credential was deleted.
	_, err := store.Get(context.Background(), orgID, models.ProviderOpenAIChatGPT)
	if err == nil {
		t.Error("expected credential to be deleted")
	}
}

func TestDisconnect_NilCredentials(t *testing.T) {
	svc := NewService(nil, zerolog.Nop())
	// Should not panic when credentials store is nil.
	err := svc.Disconnect(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}

// failingCredentialStore returns errors for all operations (simulates DB failure).
type failingCredentialStore struct{}

func (s *failingCredentialStore) Upsert(_ context.Context, _ uuid.UUID, _ models.ProviderConfig) error {
	return fmt.Errorf("db connection refused")
}
func (s *failingCredentialStore) Get(_ context.Context, _ uuid.UUID, _ models.ProviderName) (*models.DecryptedCredential, error) {
	return nil, fmt.Errorf("db connection refused")
}
func (s *failingCredentialStore) UpdateStatus(_ context.Context, _ uuid.UUID, _ models.ProviderName, _ string) error {
	return fmt.Errorf("db connection refused")
}
func (s *failingCredentialStore) Disable(_ context.Context, _ uuid.UUID, _ models.ProviderName) error {
	return fmt.Errorf("db connection refused")
}

func TestGetValidToken_DBError(t *testing.T) {
	svc := NewService(&failingCredentialStore{}, zerolog.Nop())

	_, err := svc.GetValidToken(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("expected error for DB failure, got nil")
	}
}

func TestPollForToken_RateLimited(t *testing.T) {
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
	svc.pending.Store(orgID.String(), &PendingAuth{
		DeviceCode: "dev_123",
		ExpiresAt:  time.Now().Add(15 * time.Minute),
		Interval:   5,
		LastPollAt: time.Now(), // Just polled.
	})

	// Second poll should be rate-limited (no HTTP call).
	status, err := svc.PollForToken(context.Background(), orgID)
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
