package claudecodeauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// mockCredentialStore is a minimal in-memory store used to drive the
// service's interactions. Only methods exercised by the tests below are
// fully implemented; unused ones return sensible defaults.
type mockCredentialStore struct {
	creds map[uuid.UUID]*models.DecryptedCredential
}

func newMockCredentialStore() *mockCredentialStore {
	return &mockCredentialStore{creds: make(map[uuid.UUID]*models.DecryptedCredential)}
}

func (m *mockCredentialStore) Get(_ context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error) {
	for _, cred := range m.creds {
		if cred.OrgID == orgID && cred.Provider == provider && cred.Label == "" {
			return cred, nil
		}
	}
	return nil, ErrCredentialNotFound
}

func (m *mockCredentialStore) UpsertWithLabel(_ context.Context, orgID uuid.UUID, createdBy *uuid.UUID, label string, cfg models.ProviderConfig) (*uuid.UUID, error) {
	for _, cred := range m.creds {
		if cred.OrgID == orgID && cred.Provider == cfg.Provider() && cred.Label == label {
			cred.Config = cfg
			cred.Status = "active"
			return &cred.ID, nil
		}
	}
	id := uuid.New()
	m.creds[id] = &models.DecryptedCredential{
		ID:        id,
		OrgID:     orgID,
		Provider:  cfg.Provider(),
		Label:     label,
		Config:    cfg,
		Status:    "active",
		CreatedBy: createdBy,
	}
	return &id, nil
}

func (m *mockCredentialStore) InsertPendingAuth(_ context.Context, orgID uuid.UUID, createdBy *uuid.UUID, label string, cfg models.ProviderConfig) (*uuid.UUID, error) {
	now := time.Now()
	for _, cred := range m.creds {
		if cred.OrgID == orgID && cred.Provider == cfg.Provider() && cred.Label == label {
			if cred.Status != "pending_auth" && cred.Status != "disabled" {
				return nil, &db.ErrCredentialLabelTaken{Label: label, ExistingStatus: cred.Status}
			}
			cred.Config = cfg
			cred.Status = "pending_auth"
			cred.UpdatedAt = now
			return &cred.ID, nil
		}
	}
	id := uuid.New()
	m.creds[id] = &models.DecryptedCredential{
		ID:        id,
		OrgID:     orgID,
		Provider:  cfg.Provider(),
		Label:     label,
		Config:    cfg,
		Status:    "pending_auth",
		CreatedBy: createdBy,
		CreatedAt: now,
		UpdatedAt: now,
	}
	return &id, nil
}

func (m *mockCredentialStore) GetByID(_ context.Context, orgID uuid.UUID, id uuid.UUID) (*models.DecryptedCredential, error) {
	if cred, ok := m.creds[id]; ok && cred.OrgID == orgID {
		return cred, nil
	}
	return nil, ErrCredentialNotFound
}

func (m *mockCredentialStore) GetByProviderAndLabel(_ context.Context, orgID uuid.UUID, provider models.ProviderName, label string) (*models.DecryptedCredential, error) {
	for _, cred := range m.creds {
		if cred.OrgID == orgID && cred.Provider == provider && cred.Label == label {
			return cred, nil
		}
	}
	return nil, ErrCredentialNotFound
}

func (m *mockCredentialStore) ListByProvider(_ context.Context, orgID uuid.UUID, provider models.ProviderName) ([]models.DecryptedCredential, error) {
	var out []models.DecryptedCredential
	for _, cred := range m.creds {
		if cred.OrgID == orgID && cred.Provider == provider {
			out = append(out, *cred)
		}
	}
	return out, nil
}

func (m *mockCredentialStore) ClaimNextLabeledRoundRobin(_ context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error) {
	var oldest *models.DecryptedCredential
	for _, cred := range m.creds {
		if cred.OrgID != orgID || cred.Provider != provider || cred.Status != "active" || cred.Label == "" {
			continue
		}
		if oldest == nil {
			oldest = cred
			continue
		}
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

func (m *mockCredentialStore) DisableByID(_ context.Context, orgID uuid.UUID, id uuid.UUID) error {
	if cred, ok := m.creds[id]; ok && cred.OrgID == orgID {
		cred.Status = "disabled"
	}
	return nil
}

func (m *mockCredentialStore) UpdateStatusByID(_ context.Context, orgID uuid.UUID, id uuid.UUID, status string) error {
	if cred, ok := m.creds[id]; ok && cred.OrgID == orgID {
		cred.Status = status
	}
	return nil
}

func (m *mockCredentialStore) UpsertByID(_ context.Context, orgID uuid.UUID, id uuid.UUID, cfg models.ProviderConfig) error {
	if cred, ok := m.creds[id]; ok && cred.OrgID == orgID {
		if cred.Status == "disabled" {
			return nil
		}
		cred.Config = cfg
		cred.Status = "active"
	}
	return nil
}

func (m *mockCredentialStore) ExistsForProviderByID(_ context.Context, orgID uuid.UUID, id uuid.UUID, provider models.ProviderName) (bool, error) {
	if cred, ok := m.creds[id]; ok && cred.OrgID == orgID && cred.Provider == provider {
		return true, nil
	}
	return false, nil
}

func (m *mockCredentialStore) DisableLabeled(_ context.Context, orgID uuid.UUID, provider models.ProviderName) error {
	for _, cred := range m.creds {
		if cred.OrgID == orgID && cred.Provider == provider && cred.Label != "" {
			cred.Status = "disabled"
		}
	}
	return nil
}

func (m *mockCredentialStore) HasActiveLabeled(_ context.Context, orgID uuid.UUID, provider models.ProviderName) (bool, error) {
	for _, cred := range m.creds {
		if cred.OrgID == orgID && cred.Provider == provider && cred.Label != "" && cred.Status == "active" {
			return true, nil
		}
	}
	return false, nil
}

func TestInitiateOAuth_PersistsPendingSubscriptionRow(t *testing.T) {
	t.Parallel()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	resp, err := svc.InitiateOAuth(context.Background(), orgID, nil, "team-a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.State == "" {
		t.Error("want non-empty state")
	}
	if !strings.Contains(resp.AuthorizeURL, "code_challenge=") {
		t.Errorf("authorize URL missing code_challenge: %s", resp.AuthorizeURL)
	}
	if !strings.Contains(resp.AuthorizeURL, "code_challenge_method=S256") {
		t.Errorf("authorize URL missing S256 method: %s", resp.AuthorizeURL)
	}
	if !strings.Contains(resp.AuthorizeURL, "state="+resp.State) {
		t.Errorf("authorize URL state mismatch: %s", resp.AuthorizeURL)
	}

	cred, err := store.GetByProviderAndLabel(context.Background(), orgID, models.ProviderAnthropic, "team-a")
	if err != nil {
		t.Fatalf("expected persisted pending row: %v", err)
	}
	cfg, ok := cred.Config.(models.AnthropicConfig)
	if !ok || cfg.Subscription == nil {
		t.Fatalf("expected AnthropicConfig with Subscription, got %T", cred.Config)
	}
	if cfg.Subscription.State != resp.State {
		t.Errorf("stored state %q != returned state %q", cfg.Subscription.State, resp.State)
	}
	if cfg.Subscription.CodeVerifier == "" {
		t.Error("want non-empty code_verifier stored on pending row")
	}
	if cred.Label != "team-a" {
		t.Errorf("want persisted row label team-a, got %q", cred.Label)
	}
	if cred.Status != "pending_auth" {
		t.Errorf("want status pending_auth, got %q", cred.Status)
	}
}

func TestInitiateOAuth_LabelTakenByActiveRow(t *testing.T) {
	t.Parallel()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	_, err := store.UpsertWithLabel(context.Background(), orgID, nil, "team-a", models.AnthropicConfig{
		Subscription: &models.AnthropicSubscription{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour)},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err = svc.InitiateOAuth(context.Background(), orgID, nil, "team-a")
	if err == nil {
		t.Fatal("expected ErrCredentialLabelTaken, got nil")
	}
	var labelErr *db.ErrCredentialLabelTaken
	if !errors.As(err, &labelErr) {
		t.Errorf("expected wrapped ErrCredentialLabelTaken, got %v", err)
	}
}

func TestGetValidToken_SkipsAPIKeyRow(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()

	// Seed the API-key row (label=""), which must never be claimed for subs.
	_, err := store.UpsertWithLabel(context.Background(), orgID, nil, "", models.AnthropicConfig{APIKey: "sk-ant-fake"})
	if err != nil {
		t.Fatalf("seed api key: %v", err)
	}

	// Seed a labeled subscription row.
	now := time.Now()
	subCfg := models.AnthropicConfig{
		Subscription: &models.AnthropicSubscription{
			AccessToken:  "access-1",
			RefreshToken: "refresh-1",
			ExpiresAt:    now.Add(time.Hour),
			AccountType:  "pro",
		},
	}
	subID, err := store.UpsertWithLabel(context.Background(), orgID, nil, "team-a", subCfg)
	if err != nil {
		t.Fatalf("seed subscription: %v", err)
	}

	got, gotID, err := svc.GetValidToken(context.Background(), orgID)
	if err != nil {
		t.Fatalf("GetValidToken: %v", err)
	}
	if got == nil {
		t.Fatal("expected subscription, got nil")
	}
	if got.AccessToken != "access-1" {
		t.Errorf("want access_token access-1, got %s", got.AccessToken)
	}
	if gotID == nil || *gotID != *subID {
		t.Errorf("want cred id %v, got %v", subID, gotID)
	}
}

func TestGetValidToken_ReturnsNilWhenOnlyAPIKeyPresent(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	_, err := store.UpsertWithLabel(context.Background(), orgID, nil, "", models.AnthropicConfig{APIKey: "sk-ant-fake"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	sub, credID, err := svc.GetValidToken(context.Background(), orgID)
	if err != nil {
		t.Fatalf("GetValidToken: %v", err)
	}
	if sub != nil || credID != nil {
		t.Errorf("want (nil, nil); got (%v, %v)", sub, credID)
	}
}

func TestGetValidToken_RotatesLRU(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()

	mk := func(label, access string) *uuid.UUID {
		cfg := models.AnthropicConfig{Subscription: &models.AnthropicSubscription{
			AccessToken:  access,
			RefreshToken: "refresh",
			ExpiresAt:    time.Now().Add(time.Hour),
			AccountType:  "max",
		}}
		id, err := store.UpsertWithLabel(context.Background(), orgID, nil, label, cfg)
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
		return id
	}
	aID := mk("team-a", "access-a")
	bID := mk("team-b", "access-b")

	seen := map[uuid.UUID]int{}
	for i := 0; i < 4; i++ {
		_, id, err := svc.GetValidToken(context.Background(), orgID)
		if err != nil || id == nil {
			t.Fatalf("iter %d: unexpected: %v id=%v", i, err, id)
		}
		seen[*id]++
	}
	if seen[*aID] != 2 || seen[*bID] != 2 {
		t.Errorf("want 2/2 rotation, got a=%d b=%d", seen[*aID], seen[*bID])
	}
}

func TestDisconnectAll_PreservesAPIKeyRow(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	apiKeyID, _ := store.UpsertWithLabel(context.Background(), orgID, nil, "", models.AnthropicConfig{APIKey: "sk-ant"})
	subID, _ := store.UpsertWithLabel(context.Background(), orgID, nil, "team-a", models.AnthropicConfig{
		Subscription: &models.AnthropicSubscription{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour)},
	})

	if err := svc.DisconnectAll(context.Background(), orgID); err != nil {
		t.Fatalf("DisconnectAll: %v", err)
	}

	if store.creds[*apiKeyID].Status != "active" {
		t.Errorf("API-key row should remain active, got %q", store.creds[*apiKeyID].Status)
	}
	if store.creds[*subID].Status != "disabled" {
		t.Errorf("subscription row should be disabled, got %q", store.creds[*subID].Status)
	}
}

func TestListSubscriptions_SkipsAPIKeyRow(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	_, _ = store.UpsertWithLabel(context.Background(), orgID, nil, "", models.AnthropicConfig{APIKey: "sk-ant"})
	_, _ = store.UpsertWithLabel(context.Background(), orgID, nil, "team-a", models.AnthropicConfig{
		Subscription: &models.AnthropicSubscription{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour), AccountType: "max"},
	})

	subs, err := svc.ListSubscriptions(context.Background(), orgID)
	if err != nil {
		t.Fatalf("ListSubscriptions: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("expected 1 subscription, got %d", len(subs))
	}
	if subs[0].Label != "team-a" {
		t.Errorf("want label team-a, got %q", subs[0].Label)
	}
	if subs[0].AccountType != "max" {
		t.Errorf("want account_type max, got %q", subs[0].AccountType)
	}
}

// seedActiveSub is a small test helper that inserts an active subscription
// row and returns its ID so tests can exercise the refresh path.
func seedActiveSub(t *testing.T, store *mockCredentialStore, orgID uuid.UUID, label, access, refresh string, expiresAt time.Time) uuid.UUID {
	t.Helper()
	id, err := store.UpsertWithLabel(context.Background(), orgID, nil, label, models.AnthropicConfig{
		Subscription: &models.AnthropicSubscription{
			AccessToken:  access,
			RefreshToken: refresh,
			ExpiresAt:    expiresAt,
			AccountType:  "claude_max",
		},
	})
	if err != nil {
		t.Fatalf("seed sub: %v", err)
	}
	return *id
}

func TestRefreshTokenByID_HappyPath(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]string
		_ = json.Unmarshal(body, &req)
		if req["grant_type"] != "refresh_token" {
			t.Errorf("want grant_type=refresh_token, got %q", req["grant_type"])
		}
		if req["refresh_token"] != "old-refresh" {
			t.Errorf("want refresh_token=old-refresh, got %q", req["refresh_token"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600,"scope":"user:profile user:inference"}`))
	}))
	defer ts.Close()

	svc := NewService(store, zerolog.Nop())
	svc.SetTokenURL(ts.URL)
	svc.SetProfileURL("") // skip profile

	orgID := uuid.New()
	// Expired so the refresh actually runs.
	credID := seedActiveSub(t, store, orgID, "team-a", "old-access", "old-refresh", time.Now().Add(-time.Minute))

	newSub, err := svc.RefreshTokenByID(context.Background(), orgID, credID)
	if err != nil {
		t.Fatalf("RefreshTokenByID: %v", err)
	}
	if newSub.AccessToken != "new-access" {
		t.Errorf("want access=new-access, got %q", newSub.AccessToken)
	}
	if newSub.RefreshToken != "new-refresh" {
		t.Errorf("want refresh=new-refresh, got %q", newSub.RefreshToken)
	}
	if got := strings.Join(newSub.Scopes, " "); got != "user:profile user:inference" {
		t.Errorf("want scopes from refresh, got %v", newSub.Scopes)
	}
	if newSub.AccountType != "claude_max" {
		t.Errorf("want account_type preserved, got %q", newSub.AccountType)
	}
}

func TestRefreshTokenByID_PreservesRefreshTokenWhenEmpty(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Anthropic's refresh endpoint sometimes omits refresh_token — the
		// service must fall back to the stored value instead of blanking it.
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"","expires_in":3600,"scope":""}`))
	}))
	defer ts.Close()

	svc := NewService(store, zerolog.Nop())
	svc.SetTokenURL(ts.URL)
	svc.SetProfileURL("")

	orgID := uuid.New()
	credID := seedActiveSub(t, store, orgID, "team-a", "old-access", "keep-this-refresh", time.Now().Add(-time.Minute))

	newSub, err := svc.RefreshTokenByID(context.Background(), orgID, credID)
	if err != nil {
		t.Fatalf("RefreshTokenByID: %v", err)
	}
	if newSub.RefreshToken != "keep-this-refresh" {
		t.Errorf("want refresh preserved, got %q", newSub.RefreshToken)
	}
}

func TestRefreshTokenByID_RefreshTokenReused(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"refresh_token_reused"}`))
	}))
	defer ts.Close()

	svc := NewService(store, zerolog.Nop())
	svc.SetTokenURL(ts.URL)
	svc.SetProfileURL("")

	orgID := uuid.New()
	credID := seedActiveSub(t, store, orgID, "team-a", "old-access", "old-refresh", time.Now().Add(-time.Minute))

	_, err := svc.RefreshTokenByID(context.Background(), orgID, credID)
	if err == nil {
		t.Fatal("want error on refresh_token_reused")
	}
	if !strings.Contains(err.Error(), "already used") {
		t.Errorf("want 'already used' error, got %v", err)
	}
	// The credential must NOT be marked invalid on refresh_token_reused —
	// another client already consumed the token, but the access token may
	// still be valid for this one.
	if store.creds[credID].Status == "invalid" {
		t.Errorf("want status preserved on refresh_token_reused, got invalid")
	}
}

func TestRefreshTokenByID_UnauthorizedMarksInvalid(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer ts.Close()

	svc := NewService(store, zerolog.Nop())
	svc.SetTokenURL(ts.URL)
	svc.SetProfileURL("")

	orgID := uuid.New()
	credID := seedActiveSub(t, store, orgID, "team-a", "old-access", "old-refresh", time.Now().Add(-time.Minute))

	_, err := svc.RefreshTokenByID(context.Background(), orgID, credID)
	if err == nil {
		t.Fatal("want error on 401")
	}
	if store.creds[credID].Status != "invalid" {
		t.Errorf("want status=invalid after 401, got %q", store.creds[credID].Status)
	}
}

func TestRefreshTokenByID_NoRefreshToken(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	// Store a sub with empty refresh token + expired access.
	id, err := store.UpsertWithLabel(context.Background(), orgID, nil, "team-a", models.AnthropicConfig{
		Subscription: &models.AnthropicSubscription{
			AccessToken: "old-access",
			ExpiresAt:   time.Now().Add(-time.Minute),
		},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err = svc.RefreshTokenByID(context.Background(), orgID, *id)
	if err == nil {
		t.Fatal("want error when refresh_token is empty")
	}
	if !strings.Contains(err.Error(), "no refresh token") {
		t.Errorf("want 'no refresh token' error, got %v", err)
	}
}

func TestCompleteOAuth_ParsesScopesAndFetchesProfile(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()

	tokenTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"access-1","refresh_token":"refresh-1","expires_in":3600,"scope":"user:profile user:inference user:sessions:claude_code"}`))
	}))
	defer tokenTS.Close()

	profileTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access-1" {
			t.Errorf("want bearer auth on profile fetch, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"organization":{"organization_type":"claude_max","rate_limit_tier":"default_claude_max_20x"}}`))
	}))
	defer profileTS.Close()

	svc := NewService(store, zerolog.Nop())
	svc.SetTokenURL(tokenTS.URL)
	svc.SetProfileURL(profileTS.URL)

	orgID := uuid.New()
	resp, err := svc.InitiateOAuth(context.Background(), orgID, nil, "team-a")
	if err != nil {
		t.Fatalf("InitiateOAuth: %v", err)
	}

	if _, err := svc.CompleteOAuth(context.Background(), orgID, "team-a", "mycode#"+resp.State); err != nil {
		t.Fatalf("CompleteOAuth: %v", err)
	}

	cred, err := store.GetByProviderAndLabel(context.Background(), orgID, models.ProviderAnthropic, "team-a")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	cfg := cred.Config.(models.AnthropicConfig)
	sub := cfg.Subscription
	if sub.AccessToken != "access-1" {
		t.Errorf("access_token: %q", sub.AccessToken)
	}
	if sub.AccountType != "claude_max" {
		t.Errorf("account_type: %q", sub.AccountType)
	}
	if sub.RateLimitTier != "default_claude_max_20x" {
		t.Errorf("rate_limit_tier: %q", sub.RateLimitTier)
	}
	if len(sub.Scopes) != 3 {
		t.Errorf("want 3 scopes, got %v", sub.Scopes)
	}
}

func TestCompleteOAuth_ProfileFailureDoesNotBlock(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()

	tokenTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"access-1","refresh_token":"refresh-1","expires_in":3600,"scope":"user:profile"}`))
	}))
	defer tokenTS.Close()

	profileTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer profileTS.Close()

	svc := NewService(store, zerolog.Nop())
	svc.SetTokenURL(tokenTS.URL)
	svc.SetProfileURL(profileTS.URL)

	orgID := uuid.New()
	resp, err := svc.InitiateOAuth(context.Background(), orgID, nil, "team-a")
	if err != nil {
		t.Fatalf("InitiateOAuth: %v", err)
	}

	if _, err := svc.CompleteOAuth(context.Background(), orgID, "team-a", "mycode#"+resp.State); err != nil {
		t.Fatalf("want success despite profile failure, got %v", err)
	}
}

func TestDisconnectForOrg_WrongOrgNotFound(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	org1 := uuid.New()
	org2 := uuid.New()
	id, _ := store.UpsertWithLabel(context.Background(), org1, nil, "team-a", models.AnthropicConfig{
		Subscription: &models.AnthropicSubscription{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour)},
	})

	if err := svc.DisconnectForOrg(context.Background(), org2, *id); err != ErrCredentialNotFound {
		t.Errorf("want ErrCredentialNotFound, got %v", err)
	}
}
