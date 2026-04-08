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
	svc.pending.Store(orgID.String(), &PendingAuth{
		DeviceAuthID: "dev_123",
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
	svc.pending.Store(orgID.String(), &PendingAuth{
		DeviceAuthID: "dev_123",
		UserCode:     "ABCD-1234",
		ExpiresAt:    time.Now().Add(15 * time.Minute),
		Interval:     5,
	})

	status, err := svc.PollForToken(context.Background(), orgID)
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
	svc.pending.Store(orgID.String(), &PendingAuth{
		DeviceAuthID: "dev_123",
		UserCode:     "ABCD-1234",
		ExpiresAt:    time.Now().Add(15 * time.Minute),
		Interval:     5,
	})

	status, err := svc.PollForToken(context.Background(), orgID)
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
	svc.pending.Store(orgID.String(), &PendingAuth{
		DeviceAuthID: "dev_123",
		UserCode:   "ABCD-1234",
		ExpiresAt:  time.Now().Add(15 * time.Minute),
		Interval:   5,
	})

	status, err := svc.PollForToken(context.Background(), orgID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Status != "completed" {
		t.Errorf("expected completed status, got %s (%s)", status.Status, status.Message)
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
	if cfg.RefreshToken != "chr_test_refresh_token_12345" {
		t.Errorf("unexpected refresh token: %s", cfg.RefreshToken)
	}
}

func TestPollForToken_Expired(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	svc.pending.Store(orgID.String(), &PendingAuth{
		DeviceAuthID: "dev_123",
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
	svc.pending.Store(orgID.String(), &PendingAuth{
		DeviceAuthID: "dev_123",
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
	t.Parallel()
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
	t.Parallel()
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
	svc.pending.Store(orgID.String(), &PendingAuth{
		DeviceAuthID: "dev_123",
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
	svc.pending.Store(orgID.String(), &PendingAuth{
		DeviceAuthID: "dev_123",
		ExpiresAt:  time.Now().Add(15 * time.Minute),
		Interval:   5,
	})

	status, err := svc.PollForToken(context.Background(), orgID)
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
	if _, ok := svc.pending.Load(orgID.String()); ok {
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
	svc.pending.Store(orgID.String(), &PendingAuth{
		DeviceAuthID: "dev_123",
		ExpiresAt:  time.Now().Add(15 * time.Minute),
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
	svc.pending.Store(orgID.String(), &PendingAuth{
		DeviceAuthID: "dev_123",
		ExpiresAt:  time.Now().Add(15 * time.Minute),
		Interval:   5,
	})

	status, err := svc.PollForToken(context.Background(), orgID)
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
	svc.pending.Store(orgID.String(), &PendingAuth{
		DeviceAuthID: "dev_123",
		ExpiresAt:    time.Now().Add(15 * time.Minute),
		Interval:     5,
	})

	status, err := svc.PollForToken(context.Background(), orgID)
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
	store.Upsert(context.Background(), orgID, models.OpenAIChatGPTConfig{
		AccessToken:  "cha_active_token",
		RefreshToken: "chr_refresh",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
		AccountType:  "plus",
	})

	// Poll with no in-memory state — should find it in DB.
	status, err := svc.PollForToken(context.Background(), orgID)
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

	status, err := svc.PollForToken(context.Background(), orgID)
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

	status, err := svc.PollForToken(context.Background(), orgID)
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
	store.Upsert(context.Background(), orgID, models.OpenAIChatGPTConfig{
		AccessToken:  "cha_tok",
		RefreshToken: "chr_tok",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	})
	// Mark as inactive.
	store.UpdateStatus(context.Background(), orgID, models.ProviderOpenAIChatGPT, "invalid")

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
	store.Upsert(context.Background(), orgID, models.OpenAIChatGPTConfig{
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
	store.Upsert(context.Background(), orgID, models.OpenAIChatGPTConfig{
		AccessToken:  "cha_expired",
		RefreshToken: "chr_refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Minute), // Already expired.
	})

	_, err := svc.GetValidToken(context.Background(), orgID)
	if err == nil {
		t.Fatal("expected error when token expired and refresh fails")
	}
}

func TestRefreshToken_NonAuthError(t *testing.T) {
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
	store.Upsert(context.Background(), orgID, models.OpenAIChatGPTConfig{
		AccessToken:  "cha_tok",
		RefreshToken: "chr_tok",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
	})

	_, err := svc.RefreshToken(context.Background(), orgID)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestRefreshToken_InvalidConfigType(t *testing.T) {
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

	_, err := svc.RefreshToken(context.Background(), orgID)
	if err == nil {
		t.Fatal("expected error for invalid config type")
	}
}

func TestRefreshToken_NotFound(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	_, err := svc.RefreshToken(context.Background(), uuid.New())
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

	_, err := svc.InitiateDeviceAuth(context.Background(), uuid.New())
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

	_, err := svc.InitiateDeviceAuth(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
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
		{"not found", fmt.Errorf("credential not found"), true},
		{"no rows", fmt.Errorf("no rows in result set"), true},
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

func TestRefreshToken_ConcurrentRefreshesAreSerialized(t *testing.T) {
	t.Parallel()

	refreshCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshCount++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":  fmt.Sprintf("cha_refreshed_%d", refreshCount),
			"refresh_token": fmt.Sprintf("chr_new_%d", refreshCount),
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetHTTPClient(server.Client())
	svc.SetIssuer(server.URL)

	orgID := uuid.New()
	store.Upsert(context.Background(), orgID, models.OpenAIChatGPTConfig{
		AccessToken:  "cha_old",
		RefreshToken: "chr_old",
		ExpiresAt:    time.Now().Add(-1 * time.Minute), // Expired.
	})

	// Fire two concurrent refreshes.
	done := make(chan *models.OpenAIChatGPTConfig, 2)
	for i := 0; i < 2; i++ {
		go func() {
			cfg, _ := svc.RefreshToken(context.Background(), orgID)
			done <- cfg
		}()
	}

	cfg1 := <-done
	cfg2 := <-done

	// The mutex should cause the second goroutine to see the already-refreshed
	// token (within the refresh window check) and skip the HTTP call.
	// Only 1 HTTP call should have been made.
	if refreshCount != 1 {
		t.Errorf("expected exactly 1 HTTP refresh call (serialized), got %d", refreshCount)
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
	store.Upsert(context.Background(), orgID, models.OpenAIChatGPTConfig{
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
	store.Upsert(context.Background(), orgID, models.OpenAIChatGPTConfig{
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
