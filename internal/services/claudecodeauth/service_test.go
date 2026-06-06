package claudecodeauth

import (
	"bytes"
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
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// mockCredentialStore is a minimal in-memory store used to drive the
// service's interactions. Only methods exercised by the tests below are
// fully implemented; unused ones return sensible defaults.
//
// Each cred carries the scope it was inserted at via credScope so personal
// and org rows for the same (org, label) pair don't collide in the
// in-memory map — mirroring the real CodingCredentialStore's per-scope
// partitioning.
type mockCredentialStore struct {
	creds     map[uuid.UUID]*models.DecryptedCredential
	credScope map[uuid.UUID]models.Scope

	getByIDErr            error
	upsertByIDErr         error
	claimErr              error
	listByProviderErr     error
	existsErr             error
	disableErr            error
	disableLabeledErr     error
	hasActiveLabeledErr   error
	getByProviderLabelErr error
}

func newMockCredentialStore() *mockCredentialStore {
	return &mockCredentialStore{
		creds:     make(map[uuid.UUID]*models.DecryptedCredential),
		credScope: make(map[uuid.UUID]models.Scope),
	}
}

// scopesMatch is true when the cred at id was inserted under a scope that
// matches the lookup scope. Tests that mutate creds directly (without going
// through Upsert/InsertPendingAuth) inherit org-scope semantics — keeps
// existing tests working without forcing them to pre-register scope.
func (m *mockCredentialStore) scopesMatch(id uuid.UUID, scope models.Scope) bool {
	stored, ok := m.credScope[id]
	if !ok {
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

func (m *mockCredentialStore) Get(_ context.Context, scope models.Scope, provider models.ProviderName) (*models.DecryptedCredential, error) {
	for _, cred := range m.creds {
		if cred.OrgID == scope.OrgID && cred.Provider == provider && cred.Label == "" {
			return cred, nil
		}
	}
	return nil, ErrCredentialNotFound
}

func (m *mockCredentialStore) UpsertWithLabel(_ context.Context, scope models.Scope, createdBy *uuid.UUID, label string, cfg models.ProviderConfig) (*uuid.UUID, error) {
	for _, cred := range m.creds {
		if m.scopesMatch(cred.ID, scope) && cred.Provider == cfg.Provider() && cred.Label == label {
			cred.Config = cfg
			cred.Status = "active"
			return &cred.ID, nil
		}
	}
	id := uuid.New()
	m.creds[id] = &models.DecryptedCredential{
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
	now := time.Now()
	for _, cred := range m.creds {
		if m.scopesMatch(cred.ID, scope) && cred.Provider == cfg.Provider() && cred.Label == label {
			if cred.Status != "pending_auth" && cred.Status != "disabled" {
				return nil, &db.ErrCredentialLabelTaken{Label: label, ExistingStatus: string(cred.Status)}
			}
			cred.Config = cfg
			cred.Status = models.CredentialStatusPendingAuth
			cred.UpdatedAt = now
			return &cred.ID, nil
		}
	}
	id := uuid.New()
	m.creds[id] = &models.DecryptedCredential{
		ID:        id,
		OrgID:     scope.OrgID,
		Provider:  cfg.Provider(),
		Label:     label,
		Config:    cfg,
		Status:    "pending_auth",
		CreatedBy: createdBy,
		CreatedAt: now,
		UpdatedAt: now,
	}
	m.credScope[id] = scope
	return &id, nil
}

func (m *mockCredentialStore) GetByID(_ context.Context, scope models.Scope, id uuid.UUID) (*models.DecryptedCredential, error) {
	if m.getByIDErr != nil {
		return nil, m.getByIDErr
	}
	if cred, ok := m.creds[id]; ok && m.scopesMatch(cred.ID, scope) {
		return cred, nil
	}
	return nil, ErrCredentialNotFound
}

func (m *mockCredentialStore) GetByProviderAndLabel(_ context.Context, scope models.Scope, provider models.ProviderName, label string) (*models.DecryptedCredential, error) {
	if m.getByProviderLabelErr != nil {
		return nil, m.getByProviderLabelErr
	}
	for _, cred := range m.creds {
		if m.scopesMatch(cred.ID, scope) && cred.Provider == provider && cred.Label == label {
			return cred, nil
		}
	}
	return nil, ErrCredentialNotFound
}

func (m *mockCredentialStore) ListByProvider(_ context.Context, scope models.Scope, provider models.ProviderName) ([]models.DecryptedCredential, error) {
	if m.listByProviderErr != nil {
		return nil, m.listByProviderErr
	}
	var out []models.DecryptedCredential
	for _, cred := range m.creds {
		if m.scopesMatch(cred.ID, scope) && cred.Provider == provider {
			out = append(out, *cred)
		}
	}
	return out, nil
}

func (m *mockCredentialStore) ClaimNextLabeledRoundRobin(_ context.Context, scope models.Scope, provider models.ProviderName) (*models.DecryptedCredential, error) {
	if m.claimErr != nil {
		return nil, m.claimErr
	}
	var oldest *models.DecryptedCredential
	for _, cred := range m.creds {
		if !m.scopesMatch(cred.ID, scope) || cred.Provider != provider || cred.Status != "active" || cred.Label == "" {
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

func (m *mockCredentialStore) DisableByID(_ context.Context, scope models.Scope, id uuid.UUID) error {
	if m.disableErr != nil {
		return m.disableErr
	}
	if cred, ok := m.creds[id]; ok && m.scopesMatch(cred.ID, scope) {
		cred.Status = models.CredentialStatusDisabled
	}
	return nil
}

func (m *mockCredentialStore) UpdateStatusByID(_ context.Context, scope models.Scope, id uuid.UUID, status models.CodingCredentialRowStatus) error {
	if cred, ok := m.creds[id]; ok && m.scopesMatch(cred.ID, scope) {
		cred.Status = models.CredentialStatus(status)
	}
	return nil
}

func (m *mockCredentialStore) UpsertByID(_ context.Context, scope models.Scope, id uuid.UUID, cfg models.ProviderConfig) error {
	if m.upsertByIDErr != nil {
		return m.upsertByIDErr
	}
	if cred, ok := m.creds[id]; ok && m.scopesMatch(cred.ID, scope) {
		if cred.Status == "disabled" {
			return nil
		}
		cred.Config = cfg
		cred.Status = "active"
	}
	return nil
}

func (m *mockCredentialStore) ExistsForProviderByID(_ context.Context, scope models.Scope, id uuid.UUID, provider models.ProviderName) (bool, error) {
	if m.existsErr != nil {
		return false, m.existsErr
	}
	if cred, ok := m.creds[id]; ok && m.scopesMatch(cred.ID, scope) && cred.Provider == provider {
		return true, nil
	}
	return false, nil
}

func (m *mockCredentialStore) DisableLabeled(_ context.Context, scope models.Scope, provider models.ProviderName) error {
	if m.disableLabeledErr != nil {
		return m.disableLabeledErr
	}
	for _, cred := range m.creds {
		if m.scopesMatch(cred.ID, scope) && cred.Provider == provider && cred.Label != "" {
			cred.Status = models.CredentialStatusDisabled
		}
	}
	return nil
}

func (m *mockCredentialStore) HasActiveLabeled(_ context.Context, scope models.Scope, provider models.ProviderName) (bool, error) {
	if m.hasActiveLabeledErr != nil {
		return false, m.hasActiveLabeledErr
	}
	for _, cred := range m.creds {
		if m.scopesMatch(cred.ID, scope) && cred.Provider == provider && cred.Label != "" && cred.Status == "active" {
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
	resp, err := svc.InitiateOAuth(context.Background(), models.Scope{OrgID: orgID}, nil, "team-a")
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

	cred, err := store.GetByProviderAndLabel(context.Background(), models.Scope{OrgID: orgID}, models.ProviderAnthropic, "team-a")
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
	_, err := store.UpsertWithLabel(context.Background(), models.Scope{OrgID: orgID}, nil, "team-a", models.AnthropicConfig{
		Subscription: &models.AnthropicSubscription{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour)},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err = svc.InitiateOAuth(context.Background(), models.Scope{OrgID: orgID}, nil, "team-a")
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
	_, err := store.UpsertWithLabel(context.Background(), models.Scope{OrgID: orgID}, nil, "", models.AnthropicConfig{APIKey: "sk-ant-fake"})
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
	subID, err := store.UpsertWithLabel(context.Background(), models.Scope{OrgID: orgID}, nil, "team-a", subCfg)
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
	_, err := store.UpsertWithLabel(context.Background(), models.Scope{OrgID: orgID}, nil, "", models.AnthropicConfig{APIKey: "sk-ant-fake"})
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
		id, err := store.UpsertWithLabel(context.Background(), models.Scope{OrgID: orgID}, nil, label, cfg)
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
	apiKeyID, _ := store.UpsertWithLabel(context.Background(), models.Scope{OrgID: orgID}, nil, "", models.AnthropicConfig{APIKey: "sk-ant"})
	subID, _ := store.UpsertWithLabel(context.Background(), models.Scope{OrgID: orgID}, nil, "team-a", models.AnthropicConfig{
		Subscription: &models.AnthropicSubscription{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour)},
	})

	if err := svc.DisconnectAll(context.Background(), models.Scope{OrgID: orgID}); err != nil {
		t.Fatalf("DisconnectAll: %v", err)
	}

	if store.creds[*apiKeyID].Status != "active" {
		t.Errorf("API-key row should remain active, got %q", store.creds[*apiKeyID].Status)
	}
	if store.creds[*subID].Status != "disabled" {
		t.Errorf("subscription row should be disabled, got %q", store.creds[*subID].Status)
	}
}

func TestDisconnectForOrg_RejectsUnlabeledAnthropicAPIKey(t *testing.T) {
	t.Parallel()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	orgID := uuid.New()
	apiKeyID := uuid.New()
	store.creds[apiKeyID] = &models.DecryptedCredential{
		ID:       apiKeyID,
		OrgID:    orgID,
		Provider: models.ProviderAnthropic,
		Label:    "",
		Config:   models.AnthropicConfig{APIKey: "sk-ant-test"},
		Status:   "active",
	}

	err := svc.DisconnectForOrg(context.Background(), models.Scope{OrgID: orgID}, apiKeyID)
	require.ErrorIs(t, err, ErrCredentialNotFound, "Claude subscription disconnect should reject an Anthropic API-key row")
	require.Equal(t, models.CredentialStatusActive, store.creds[apiKeyID].Status, "Anthropic API-key row should remain active")
}

func TestListSubscriptions_SkipsAPIKeyRow(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	_, _ = store.UpsertWithLabel(context.Background(), models.Scope{OrgID: orgID}, nil, "", models.AnthropicConfig{APIKey: "sk-ant"})
	_, _ = store.UpsertWithLabel(context.Background(), models.Scope{OrgID: orgID}, nil, "team-a", models.AnthropicConfig{
		Subscription: &models.AnthropicSubscription{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour), AccountType: "max"},
	})

	subs, err := svc.ListSubscriptions(context.Background(), models.Scope{OrgID: orgID})
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
	id, err := store.UpsertWithLabel(context.Background(), models.Scope{OrgID: orgID}, nil, label, models.AnthropicConfig{
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

	newSub, err := svc.RefreshTokenByID(context.Background(), models.Scope{OrgID: orgID}, credID)
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

func TestRefreshTokenByID_LogsSuccessfulRefreshAtInfo(t *testing.T) {
	t.Parallel()

	store := newMockCredentialStore()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`))
	}))
	defer ts.Close()

	var logs bytes.Buffer
	logger := zerolog.New(&logs).Level(zerolog.InfoLevel)
	svc := NewService(store, logger)
	svc.SetTokenURL(ts.URL)
	svc.SetProfileURL("")

	orgID := uuid.New()
	credID := seedActiveSub(t, store, orgID, "team-a", "old-access", "old-refresh", time.Now().Add(-time.Minute))

	_, err := svc.RefreshTokenByID(context.Background(), models.Scope{OrgID: orgID}, credID)
	require.NoError(t, err, "RefreshTokenByID should refresh the expired subscription")
	require.Contains(t, logs.String(), "Claude Code OAuth token refreshed", "successful refresh should be logged at info level")
	require.Contains(t, logs.String(), credID.String(), "successful refresh log should include credential id")
}

func TestStoreTokenByID_PersistsHarvestedSubscription(t *testing.T) {
	t.Parallel()

	store := newMockCredentialStore()
	profileTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer harvested-access", r.Header.Get("Authorization"), "StoreTokenByID should validate the harvested token")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"organization":{"organization_type":"claude_max","rate_limit_tier":"default_claude_max_20x"}}`))
	}))
	defer profileTS.Close()

	svc := NewService(store, zerolog.Nop())
	svc.SetProfileURL(profileTS.URL)
	orgID := uuid.New()
	userID := uuid.New()
	scope := models.Scope{OrgID: orgID, UserID: &userID}
	credID, err := store.UpsertWithLabel(context.Background(), scope, &userID, "personal-claude", models.AnthropicConfig{
		Subscription: &models.AnthropicSubscription{
			AccessToken:  "old-access",
			RefreshToken: "old-refresh",
			ExpiresAt:    time.Now().Add(time.Hour),
		},
	})
	require.NoError(t, err, "seed subscription should be stored")

	expiresAt := time.Now().Add(2 * time.Hour).Truncate(time.Millisecond)
	sub := models.AnthropicSubscription{
		AccessToken:   "harvested-access",
		RefreshToken:  "harvested-refresh",
		ExpiresAt:     expiresAt,
		AccountType:   "claude_max",
		RateLimitTier: "default_claude_max_20x",
		Scopes:        []string{"user:profile", "user:inference"},
	}

	stored, err := svc.StoreTokenByID(context.Background(), scope, *credID, sub)

	require.NoError(t, err, "StoreTokenByID should persist harvested Claude credentials")
	require.True(t, stored, "StoreTokenByID should report when harvested credentials were persisted")
	cred := store.creds[*credID]
	require.NotNil(t, cred, "stored credential should still exist")
	cfg, ok := cred.Config.(models.AnthropicConfig)
	require.True(t, ok, "stored credential config should remain AnthropicConfig")
	require.NotNil(t, cfg.Subscription, "stored credential should contain a subscription")
	require.Equal(t, sub, *cfg.Subscription, "stored credential should contain the harvested subscription")
}

func TestStoreTokenByID_RejectsInvalidHarvestedAccessToken(t *testing.T) {
	t.Parallel()

	store := newMockCredentialStore()
	profileTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer harvested-access", r.Header.Get("Authorization"), "StoreTokenByID should validate the harvested token")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer profileTS.Close()

	svc := NewService(store, zerolog.Nop())
	svc.SetProfileURL(profileTS.URL)
	orgID := uuid.New()
	credID := seedActiveSub(t, store, orgID, "team-a", "old-access", "old-refresh", time.Now().Add(time.Hour))

	stored, err := svc.StoreTokenByID(context.Background(), models.Scope{OrgID: orgID}, credID, models.AnthropicSubscription{
		AccessToken:  "harvested-access",
		RefreshToken: "harvested-refresh",
		ExpiresAt:    time.Now().Add(2 * time.Hour),
	})

	require.Error(t, err, "StoreTokenByID should reject a harvested token that Anthropic does not accept")
	require.False(t, stored, "StoreTokenByID should report no write for rejected harvested credentials")
	cfg := store.creds[credID].Config.(models.AnthropicConfig)
	require.Equal(t, "old-access", cfg.Subscription.AccessToken, "StoreTokenByID should not overwrite with an invalid harvested token")
	require.Equal(t, "old-refresh", cfg.Subscription.RefreshToken, "StoreTokenByID should preserve the existing refresh token after validation failure")
}

func TestStoreTokenByID_DoesNotOverwriteNewerStoredToken(t *testing.T) {
	t.Parallel()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	svc.SetProfileURL("")
	orgID := uuid.New()
	credID := seedActiveSub(t, store, orgID, "team-a", "newer-access", "newer-refresh", time.Now().Add(3*time.Hour))

	stored, err := svc.StoreTokenByID(context.Background(), models.Scope{OrgID: orgID}, credID, models.AnthropicSubscription{
		AccessToken:  "older-harvested-access",
		RefreshToken: "older-harvested-refresh",
		ExpiresAt:    time.Now().Add(2 * time.Hour),
	})

	require.NoError(t, err, "StoreTokenByID should no-op when the DB already has a newer token")
	require.False(t, stored, "StoreTokenByID should report no write for stale harvested credentials")
	cfg := store.creds[credID].Config.(models.AnthropicConfig)
	require.Equal(t, "newer-access", cfg.Subscription.AccessToken, "StoreTokenByID should preserve the newer stored access token")
	require.Equal(t, "newer-refresh", cfg.Subscription.RefreshToken, "StoreTokenByID should preserve the newer stored refresh token")
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

	newSub, err := svc.RefreshTokenByID(context.Background(), models.Scope{OrgID: orgID}, credID)
	if err != nil {
		t.Fatalf("RefreshTokenByID: %v", err)
	}
	if newSub.RefreshToken != "keep-this-refresh" {
		t.Errorf("want refresh preserved, got %q", newSub.RefreshToken)
	}
}

func TestRefreshTokenByID_RejectsEmptyAccessToken(t *testing.T) {
	t.Parallel()

	store := newMockCredentialStore()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"","refresh_token":"new-refresh","expires_in":3600,"scope":"user:profile"}`))
	}))
	defer ts.Close()

	svc := NewService(store, zerolog.Nop())
	svc.SetTokenURL(ts.URL)
	svc.SetProfileURL("")

	orgID := uuid.New()
	credID := seedActiveSub(t, store, orgID, "team-a", "old-access", "old-refresh", time.Now().Add(-time.Minute))

	newSub, err := svc.RefreshTokenByID(context.Background(), models.Scope{OrgID: orgID}, credID)
	require.Nil(t, newSub, "RefreshTokenByID should not return a subscription when the refresh response omits access_token")
	require.Error(t, err, "RefreshTokenByID should fail closed when the refresh response omits access_token")
	require.Contains(t, err.Error(), "empty access_token", "RefreshTokenByID should surface the empty access_token error")

	storedCred, getErr := store.GetByID(context.Background(), models.Scope{OrgID: orgID}, credID)
	require.NoError(t, getErr, "GetByID should return the seeded credential after a failed refresh")

	storedCfg, ok := storedCred.Config.(models.AnthropicConfig)
	require.True(t, ok, "stored credential should remain an AnthropicConfig after a failed refresh")
	require.NotNil(t, storedCfg.Subscription, "stored credential should still include its subscription after a failed refresh")
	require.Equal(t, "old-access", storedCfg.Subscription.AccessToken, "failed refresh should preserve the previously stored access token")
	require.Equal(t, "old-refresh", storedCfg.Subscription.RefreshToken, "failed refresh should preserve the previously stored refresh token")
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

	_, err := svc.RefreshTokenByID(context.Background(), models.Scope{OrgID: orgID}, credID)
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

	_, err := svc.RefreshTokenByID(context.Background(), models.Scope{OrgID: orgID}, credID)
	if err == nil {
		t.Fatal("want error on 401")
	}
	if store.creds[credID].Status != "invalid" {
		t.Errorf("want status=invalid after 401, got %q", store.creds[credID].Status)
	}
}

func TestRefreshTokenByID_InvalidGrantBadRequestMarksInvalid(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Refresh token not found or invalid"}`))
	}))
	defer ts.Close()

	svc := NewService(store, zerolog.Nop())
	svc.SetTokenURL(ts.URL)
	svc.SetProfileURL("")

	orgID := uuid.New()
	credID := seedActiveSub(t, store, orgID, "team-a", "old-access", "old-refresh", time.Now().Add(-time.Minute))

	_, err := svc.RefreshTokenByID(context.Background(), models.Scope{OrgID: orgID}, credID)
	require.Error(t, err, "RefreshTokenByID should fail when Anthropic rejects the refresh grant")
	require.Contains(t, err.Error(), "refresh token revoked", "RefreshTokenByID should classify invalid_grant as revoked")
	require.Equal(t, models.CredentialStatusInvalid, store.creds[credID].Status, "RefreshTokenByID should mark invalid_grant credentials invalid")
}

func TestRefreshTokenByID_NoRefreshToken(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	// Store a sub with empty refresh token + expired access.
	id, err := store.UpsertWithLabel(context.Background(), models.Scope{OrgID: orgID}, nil, "team-a", models.AnthropicConfig{
		Subscription: &models.AnthropicSubscription{
			AccessToken: "old-access",
			ExpiresAt:   time.Now().Add(-time.Minute),
		},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err = svc.RefreshTokenByID(context.Background(), models.Scope{OrgID: orgID}, *id)
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
	resp, err := svc.InitiateOAuth(context.Background(), models.Scope{OrgID: orgID}, nil, "team-a")
	if err != nil {
		t.Fatalf("InitiateOAuth: %v", err)
	}

	if _, err := svc.CompleteOAuth(context.Background(), models.Scope{OrgID: orgID}, "team-a", "mycode#"+resp.State); err != nil {
		t.Fatalf("CompleteOAuth: %v", err)
	}

	cred, err := store.GetByProviderAndLabel(context.Background(), models.Scope{OrgID: orgID}, models.ProviderAnthropic, "team-a")
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
	resp, err := svc.InitiateOAuth(context.Background(), models.Scope{OrgID: orgID}, nil, "team-a")
	if err != nil {
		t.Fatalf("InitiateOAuth: %v", err)
	}

	if _, err := svc.CompleteOAuth(context.Background(), models.Scope{OrgID: orgID}, "team-a", "mycode#"+resp.State); err != nil {
		t.Fatalf("want success despite profile failure, got %v", err)
	}
}

func TestDisconnectForOrg_WrongOrgNotFound(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	org1 := uuid.New()
	org2 := uuid.New()
	id, _ := store.UpsertWithLabel(context.Background(), models.Scope{OrgID: org1}, nil, "team-a", models.AnthropicConfig{
		Subscription: &models.AnthropicSubscription{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour)},
	})

	if err := svc.DisconnectForOrg(context.Background(), models.Scope{OrgID: org2}, *id); err != ErrCredentialNotFound {
		t.Errorf("want ErrCredentialNotFound, got %v", err)
	}
}

func TestSplitCodeAndState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		raw       string
		wantCode  string
		wantState string
		wantErr   bool
	}{
		{name: "well-formed", raw: "abc#def", wantCode: "abc", wantState: "def"},
		{name: "trims surrounding whitespace", raw: "  abc#def  ", wantCode: "abc", wantState: "def"},
		{name: "missing separator", raw: "abcdef", wantErr: true},
		{name: "empty code", raw: "#state", wantErr: true},
		{name: "empty state", raw: "code#", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			code, state, err := splitCodeAndState(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q/%q", code, state)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if code != tt.wantCode || state != tt.wantState {
				t.Errorf("got (%q,%q), want (%q,%q)", code, state, tt.wantCode, tt.wantState)
			}
		})
	}
}

func TestRedactedBody_TruncatesOversized(t *testing.T) {
	t.Parallel()

	short := []byte("short body")
	if got := redactedBody(short); got != "short body" {
		t.Errorf("short body should pass through, got %q", got)
	}

	large := make([]byte, maxLoggedBodyBytes+50)
	for i := range large {
		large[i] = 'a'
	}
	got := redactedBody(large)
	if !strings.HasSuffix(got, "(truncated)") {
		t.Errorf("want (truncated) suffix, got %q", got[len(got)-20:])
	}
}

func TestParseScopes(t *testing.T) {
	t.Parallel()

	if got := parseScopes(""); got != nil {
		t.Errorf("empty input should return nil, got %v", got)
	}
	if got := parseScopes("   "); got != nil {
		t.Errorf("whitespace-only input should return nil, got %v", got)
	}
	got := parseScopes("user:profile user:inference")
	if len(got) != 2 || got[0] != "user:profile" || got[1] != "user:inference" {
		t.Errorf("split parse: got %v", got)
	}
}

func TestHasActiveSubscription(t *testing.T) {
	t.Parallel()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())
	orgID := uuid.New()

	has, err := svc.HasActiveSubscription(context.Background(), orgID)
	if err != nil || has {
		t.Errorf("empty store: got (%v, %v), want (false, nil)", has, err)
	}

	_, _ = store.UpsertWithLabel(context.Background(), models.Scope{OrgID: orgID}, nil, "team-a", models.AnthropicConfig{
		Subscription: &models.AnthropicSubscription{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour)},
	})
	has, err = svc.HasActiveSubscription(context.Background(), orgID)
	if err != nil || !has {
		t.Errorf("with labeled sub: got (%v, %v), want (true, nil)", has, err)
	}
}

func TestHasActiveSubscription_NilStore(t *testing.T) {
	t.Parallel()
	svc := NewService(nil, zerolog.Nop())
	has, err := svc.HasActiveSubscription(context.Background(), uuid.New())
	if err != nil || has {
		t.Errorf("nil store: got (%v, %v), want (false, nil)", has, err)
	}
}

func TestCompleteOAuth_InvalidPasteFormat(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	if _, err := svc.InitiateOAuth(context.Background(), models.Scope{OrgID: orgID}, nil, "team-a"); err != nil {
		t.Fatalf("InitiateOAuth: %v", err)
	}
	if _, err := svc.CompleteOAuth(context.Background(), models.Scope{OrgID: orgID}, "team-a", "no-hash-at-all"); !errors.Is(err, ErrInvalidPaste) {
		t.Errorf("want ErrInvalidPaste, got %v", err)
	}
}

func TestCompleteOAuth_StateMismatch(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	if _, err := svc.InitiateOAuth(context.Background(), models.Scope{OrgID: orgID}, nil, "team-a"); err != nil {
		t.Fatalf("InitiateOAuth: %v", err)
	}
	if _, err := svc.CompleteOAuth(context.Background(), models.Scope{OrgID: orgID}, "team-a", "code#wrong-state"); !errors.Is(err, ErrInvalidPaste) {
		t.Errorf("want ErrInvalidPaste for state mismatch, got %v", err)
	}
}

func TestCompleteOAuth_NoPendingRow(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	if _, err := svc.CompleteOAuth(context.Background(), models.Scope{OrgID: orgID}, "team-a", "code#state"); !errors.Is(err, ErrPendingAuthNotFound) {
		t.Errorf("want ErrPendingAuthNotFound, got %v", err)
	}
}

func TestCompleteOAuth_Expired(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	resp, err := svc.InitiateOAuth(context.Background(), models.Scope{OrgID: orgID}, nil, "team-a")
	if err != nil {
		t.Fatalf("InitiateOAuth: %v", err)
	}

	// Backdate the pending row past the TTL window.
	cred, err := store.GetByProviderAndLabel(context.Background(), models.Scope{OrgID: orgID}, models.ProviderAnthropic, "team-a")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	cred.UpdatedAt = time.Now().Add(-2 * pendingAuthTTL)

	if _, err := svc.CompleteOAuth(context.Background(), models.Scope{OrgID: orgID}, "team-a", "code#"+resp.State); !errors.Is(err, ErrPendingAuthExpired) {
		t.Errorf("want ErrPendingAuthExpired, got %v", err)
	}
}

func TestCompleteOAuth_AlreadyActive(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	// Seed an active (not pending) row — replay attempts must not overwrite it.
	_, _ = store.UpsertWithLabel(context.Background(), models.Scope{OrgID: orgID}, nil, "team-a", models.AnthropicConfig{
		Subscription: &models.AnthropicSubscription{
			AccessToken: "live", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour),
			State: "s", CodeVerifier: "v",
		},
	})

	if _, err := svc.CompleteOAuth(context.Background(), models.Scope{OrgID: orgID}, "team-a", "code#s"); !errors.Is(err, ErrPendingAuthNotFound) {
		t.Errorf("active row should surface as ErrPendingAuthNotFound, got %v", err)
	}
}

func TestCompleteOAuth_TokenExchangeFailure(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer ts.Close()

	svc := NewService(store, zerolog.Nop())
	svc.SetTokenURL(ts.URL)
	svc.SetProfileURL("")

	orgID := uuid.New()
	resp, err := svc.InitiateOAuth(context.Background(), models.Scope{OrgID: orgID}, nil, "team-a")
	if err != nil {
		t.Fatalf("InitiateOAuth: %v", err)
	}

	if _, err := svc.CompleteOAuth(context.Background(), models.Scope{OrgID: orgID}, "team-a", "code#"+resp.State); err == nil {
		t.Fatal("want error from token exchange failure")
	}
}

func TestGetValidToken_UsesCachedAccessTokenOnRefreshFailure(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"server_error"}`))
	}))
	defer ts.Close()

	svc := NewService(store, zerolog.Nop())
	svc.SetTokenURL(ts.URL)
	svc.SetProfileURL("")

	orgID := uuid.New()
	// Token that's near expiry but not yet expired — refresh attempt fires,
	// fails, and service falls back to the cached token.
	credID := seedActiveSub(t, store, orgID, "team-a", "cached", "refresh", time.Now().Add(time.Minute))

	sub, gotID, err := svc.GetValidToken(context.Background(), orgID)
	if err != nil {
		t.Fatalf("GetValidToken: %v", err)
	}
	if sub == nil || sub.AccessToken != "cached" {
		t.Errorf("want cached access token, got %v", sub)
	}
	if gotID == nil || *gotID != credID {
		t.Errorf("want cred id %v, got %v", credID, gotID)
	}
}

func TestGetValidToken_MarksInvalidWhenExpiredAndRefreshFails(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	svc := NewService(store, zerolog.Nop())
	svc.SetTokenURL(ts.URL)
	svc.SetProfileURL("")

	orgID := uuid.New()
	// Already expired — refresh failure must mark the cred invalid.
	credID := seedActiveSub(t, store, orgID, "team-a", "dead", "refresh", time.Now().Add(-time.Hour))

	sub, _, err := svc.GetValidToken(context.Background(), orgID)
	if err == nil || sub != nil {
		t.Errorf("want error and nil sub, got (%v, %v)", sub, err)
	}
	if store.creds[credID].Status != "invalid" {
		t.Errorf("want status invalid after expired+refresh-failure, got %q", store.creds[credID].Status)
	}
}

func TestDisconnect_ClearsRefreshMutex(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	id, _ := store.UpsertWithLabel(context.Background(), models.Scope{OrgID: orgID}, nil, "team-a", models.AnthropicConfig{
		Subscription: &models.AnthropicSubscription{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour)},
	})

	// Prime the refresh mutex so Disconnect has something to drop.
	_ = svc.credRefreshMu(*id)

	if err := svc.Disconnect(context.Background(), models.Scope{OrgID: orgID}, *id); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if store.creds[*id].Status != "disabled" {
		t.Errorf("want disabled, got %q", store.creds[*id].Status)
	}
}

func TestDisconnectForOrg_Success(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	id, _ := store.UpsertWithLabel(context.Background(), models.Scope{OrgID: orgID}, nil, "team-a", models.AnthropicConfig{
		Subscription: &models.AnthropicSubscription{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour)},
	})

	if err := svc.DisconnectForOrg(context.Background(), models.Scope{OrgID: orgID}, *id); err != nil {
		t.Fatalf("DisconnectForOrg: %v", err)
	}
	if store.creds[*id].Status != "disabled" {
		t.Errorf("want disabled, got %q", store.creds[*id].Status)
	}
}

func TestDisconnectAll_NilStore(t *testing.T) {
	t.Parallel()
	svc := NewService(nil, zerolog.Nop())
	// Populates an init mutex via InitiateOAuth before DisconnectAll sweeps it.
	orgID := uuid.New()
	if _, err := svc.InitiateOAuth(context.Background(), models.Scope{OrgID: orgID}, nil, "team-a"); err != nil {
		t.Fatalf("InitiateOAuth: %v", err)
	}
	if err := svc.DisconnectAll(context.Background(), models.Scope{OrgID: orgID}); err != nil {
		t.Errorf("DisconnectAll with nil store: %v", err)
	}
}

func TestListSubscriptions_NilStore(t *testing.T) {
	t.Parallel()
	svc := NewService(nil, zerolog.Nop())
	subs, err := svc.ListSubscriptions(context.Background(), models.Scope{OrgID: uuid.New()})
	if err != nil || subs != nil {
		t.Errorf("want (nil, nil); got (%v, %v)", subs, err)
	}
}

func TestSetters(t *testing.T) {
	t.Parallel()
	svc := NewService(nil, zerolog.Nop())
	custom := &http.Client{Timeout: time.Second}
	svc.SetHTTPClient(custom)
	svc.SetAuthorizeURL("https://authorize.example")
	if svc.httpClient != custom {
		t.Error("SetHTTPClient did not apply")
	}
	if svc.authorizeURL != "https://authorize.example" {
		t.Error("SetAuthorizeURL did not apply")
	}
}

func TestCompleteOAuth_NilStore(t *testing.T) {
	t.Parallel()
	svc := NewService(nil, zerolog.Nop())
	_, err := svc.CompleteOAuth(context.Background(), models.Scope{OrgID: uuid.New()}, "team-a", "code#state")
	if err == nil || !strings.Contains(err.Error(), "credential store") {
		t.Errorf("want credential-store error, got %v", err)
	}
}

func TestCompleteOAuth_DBError(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	store.getByProviderLabelErr = errors.New("db is down")
	svc := NewService(store, zerolog.Nop())
	_, err := svc.CompleteOAuth(context.Background(), models.Scope{OrgID: uuid.New()}, "team-a", "code#state")
	if err == nil || errors.Is(err, ErrPendingAuthNotFound) {
		t.Errorf("want wrapped DB error, got %v", err)
	}
}

func TestCompleteOAuth_UpsertError(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"a","refresh_token":"r","expires_in":3600,"scope":""}`))
	}))
	defer ts.Close()

	svc := NewService(store, zerolog.Nop())
	svc.SetTokenURL(ts.URL)
	svc.SetProfileURL("")

	orgID := uuid.New()
	resp, err := svc.InitiateOAuth(context.Background(), models.Scope{OrgID: orgID}, nil, "team-a")
	if err != nil {
		t.Fatalf("InitiateOAuth: %v", err)
	}
	store.upsertByIDErr = errors.New("db write failed")

	if _, err := svc.CompleteOAuth(context.Background(), models.Scope{OrgID: orgID}, "team-a", "code#"+resp.State); err == nil {
		t.Fatal("want error when UpsertByID fails")
	}
}

func TestCompleteOAuth_PendingRowMissingState(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	// Seed a pending_auth row with empty state/verifier — CompleteOAuth must
	// treat it as "no pending row".
	id := uuid.New()
	store.creds[id] = &models.DecryptedCredential{
		ID:       id,
		OrgID:    orgID,
		Provider: models.ProviderAnthropic,
		Label:    "team-a",
		Config: models.AnthropicConfig{
			Subscription: &models.AnthropicSubscription{},
		},
		Status:    "pending_auth",
		UpdatedAt: time.Now(),
	}
	if _, err := svc.CompleteOAuth(context.Background(), models.Scope{OrgID: orgID}, "team-a", "code#state"); !errors.Is(err, ErrPendingAuthNotFound) {
		t.Errorf("want ErrPendingAuthNotFound, got %v", err)
	}
}

func TestCompleteOAuth_UnexpectedConfig(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	id := uuid.New()
	store.creds[id] = &models.DecryptedCredential{
		ID:        id,
		OrgID:     orgID,
		Provider:  models.ProviderAnthropic,
		Label:     "team-a",
		Config:    models.AnthropicConfig{APIKey: "sk-ant"},
		Status:    "pending_auth",
		UpdatedAt: time.Now(),
	}
	if _, err := svc.CompleteOAuth(context.Background(), models.Scope{OrgID: orgID}, "team-a", "code#state"); err == nil {
		t.Fatal("want error for unexpected config")
	}
}

func TestRefreshTokenByID_GetByIDError(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	store.getByIDErr = errors.New("boom")
	svc := NewService(store, zerolog.Nop())
	_, err := svc.RefreshTokenByID(context.Background(), models.Scope{OrgID: uuid.New()}, uuid.New())
	if err == nil {
		t.Fatal("want error when GetByID fails")
	}
}

func TestRefreshTokenByID_NotAnthropicConfig(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	orgID := uuid.New()
	id := uuid.New()
	store.creds[id] = &models.DecryptedCredential{
		ID:       id,
		OrgID:    orgID,
		Provider: models.ProviderAnthropic,
		Label:    "team-a",
		Config:   models.AnthropicConfig{APIKey: "sk-ant"},
		Status:   "active",
	}
	svc := NewService(store, zerolog.Nop())
	_, err := svc.RefreshTokenByID(context.Background(), models.Scope{OrgID: orgID}, id)
	if err == nil {
		t.Fatal("want error when config is not subscription")
	}
}

func TestRefreshTokenByID_StillFreshAfterLock(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	// Token is not near expiry — Refresh should short-circuit and return it.
	credID := seedActiveSub(t, store, orgID, "team-a", "fresh", "refresh", time.Now().Add(time.Hour))

	sub, err := svc.RefreshTokenByID(context.Background(), models.Scope{OrgID: orgID}, credID)
	if err != nil || sub == nil {
		t.Fatalf("want cached fresh token, got (%v, %v)", sub, err)
	}
	if sub.AccessToken != "fresh" {
		t.Errorf("want fresh token, got %q", sub.AccessToken)
	}
}

func TestRefreshTokenByID_Non200Status(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"temporary"}`))
	}))
	defer ts.Close()

	svc := NewService(store, zerolog.Nop())
	svc.SetTokenURL(ts.URL)

	orgID := uuid.New()
	credID := seedActiveSub(t, store, orgID, "team-a", "old", "refresh", time.Now().Add(-time.Minute))

	_, err := svc.RefreshTokenByID(context.Background(), models.Scope{OrgID: orgID}, credID)
	if err == nil {
		t.Fatal("want error on 500")
	}
	if store.creds[credID].Status == "invalid" {
		t.Errorf("500 should not mark credential invalid")
	}
}

func TestRefreshTokenByID_MalformedResponse(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer ts.Close()

	svc := NewService(store, zerolog.Nop())
	svc.SetTokenURL(ts.URL)

	orgID := uuid.New()
	credID := seedActiveSub(t, store, orgID, "team-a", "old", "refresh", time.Now().Add(-time.Minute))

	_, err := svc.RefreshTokenByID(context.Background(), models.Scope{OrgID: orgID}, credID)
	if err == nil {
		t.Fatal("want parse error on malformed JSON")
	}
}

func TestRefreshTokenByID_NetworkError(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()

	// Close immediately so the client can't reach the server.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	url := ts.URL
	ts.Close()

	svc := NewService(store, zerolog.Nop())
	svc.SetTokenURL(url)

	orgID := uuid.New()
	credID := seedActiveSub(t, store, orgID, "team-a", "old", "refresh", time.Now().Add(-time.Minute))

	_, err := svc.RefreshTokenByID(context.Background(), models.Scope{OrgID: orgID}, credID)
	if err == nil {
		t.Fatal("want network error")
	}
}

func TestRefreshTokenByID_UpsertError(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new","refresh_token":"r","expires_in":3600,"scope":""}`))
	}))
	defer ts.Close()

	svc := NewService(store, zerolog.Nop())
	svc.SetTokenURL(ts.URL)

	orgID := uuid.New()
	credID := seedActiveSub(t, store, orgID, "team-a", "old", "refresh", time.Now().Add(-time.Minute))
	store.upsertByIDErr = errors.New("db down")

	_, err := svc.RefreshTokenByID(context.Background(), models.Scope{OrgID: orgID}, credID)
	if err == nil {
		t.Fatal("want error from Upsert failure")
	}
}

func TestGetValidToken_NilStore(t *testing.T) {
	t.Parallel()
	svc := NewService(nil, zerolog.Nop())
	sub, credID, err := svc.GetValidToken(context.Background(), uuid.New())
	if err != nil || sub != nil || credID != nil {
		t.Errorf("want (nil, nil, nil); got (%v, %v, %v)", sub, credID, err)
	}
}

func TestGetValidToken_ClaimError(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	store.claimErr = errors.New("db timeout")
	svc := NewService(store, zerolog.Nop())
	_, _, err := svc.GetValidToken(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("want wrapped claim error")
	}
}

func TestGetValidToken_SkipsInvalidAndEmptyTokenRows(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	// Row with empty access token — service should skip and eventually return
	// a "no usable subscription" error because the claim loop wraps.
	id := uuid.New()
	store.creds[id] = &models.DecryptedCredential{
		ID:       id,
		OrgID:    orgID,
		Provider: models.ProviderAnthropic,
		Label:    "team-a",
		Config: models.AnthropicConfig{
			Subscription: &models.AnthropicSubscription{
				AccessToken: "",
				ExpiresAt:   time.Now().Add(time.Hour),
			},
		},
		Status: "active",
	}

	sub, _, err := svc.GetValidToken(context.Background(), orgID)
	if err == nil || sub != nil {
		t.Errorf("want 'no usable' error; got (%v, %v)", sub, err)
	}
}

func TestGetValidToken_WrongConfigType(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	id := uuid.New()
	// Mock store's ClaimNextLabeledRoundRobin filters by Label != "" but picks
	// by provider — give the row a label and ProviderAnthropic but a non-sub
	// config so the "not an Anthropic subscription" branch fires.
	store.creds[id] = &models.DecryptedCredential{
		ID:       id,
		OrgID:    orgID,
		Provider: models.ProviderAnthropic,
		Label:    "team-a",
		Config:   models.AnthropicConfig{APIKey: "sk-ant"},
		Status:   "active",
	}

	sub, _, err := svc.GetValidToken(context.Background(), orgID)
	if err == nil || sub != nil {
		t.Errorf("want error for wrong config type; got (%v, %v)", sub, err)
	}
}

func TestDisconnect_StoreError(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	store.disableErr = errors.New("db down")
	svc := NewService(store, zerolog.Nop())
	err := svc.Disconnect(context.Background(), models.Scope{OrgID: uuid.New()}, uuid.New())
	if err == nil {
		t.Fatal("want error from DisableByID")
	}
}

func TestDisconnect_NilStore(t *testing.T) {
	t.Parallel()
	svc := NewService(nil, zerolog.Nop())
	if err := svc.Disconnect(context.Background(), models.Scope{OrgID: uuid.New()}, uuid.New()); err != nil {
		t.Errorf("want nil with nil store, got %v", err)
	}
}

func TestDisconnectForOrg_NilStore(t *testing.T) {
	t.Parallel()
	svc := NewService(nil, zerolog.Nop())
	if err := svc.DisconnectForOrg(context.Background(), models.Scope{OrgID: uuid.New()}, uuid.New()); err != nil {
		t.Errorf("want nil with nil store, got %v", err)
	}
}

func TestDisconnectForOrg_ExistsError(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	store.existsErr = errors.New("db down")
	svc := NewService(store, zerolog.Nop())
	err := svc.DisconnectForOrg(context.Background(), models.Scope{OrgID: uuid.New()}, uuid.New())
	if err == nil {
		t.Fatal("want wrapped exists error")
	}
}

func TestDisconnectAll_DisableLabeledError(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	store.disableLabeledErr = errors.New("db down")
	svc := NewService(store, zerolog.Nop())
	if err := svc.DisconnectAll(context.Background(), models.Scope{OrgID: uuid.New()}); err == nil {
		t.Fatal("want wrapped DisableLabeled error")
	}
}

func TestDisconnectAll_LogsListByProviderError(t *testing.T) {
	t.Parallel()

	store := newMockCredentialStore()
	store.listByProviderErr = errors.New("list failed")
	var logBuf bytes.Buffer
	svc := NewService(store, zerolog.New(&logBuf))

	err := svc.DisconnectAll(context.Background(), models.Scope{OrgID: uuid.New()})

	require.NoError(t, err, "DisconnectAll should continue when listing subscription IDs fails during best-effort mutex cleanup")
	require.Contains(t, logBuf.String(), "failed to list claude subscriptions before disconnect cleanup", "DisconnectAll should log the list failure so leaked refresh mutexes are visible to operators")
}

func TestHasActiveSubscription_StoreError(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	store.hasActiveLabeledErr = errors.New("db down")
	svc := NewService(store, zerolog.Nop())
	has, err := svc.HasActiveSubscription(context.Background(), uuid.New())
	if err == nil || has {
		t.Errorf("want wrapped error + false, got (%v, %v)", has, err)
	}
}

func TestListSubscriptions_StoreError(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	store.listByProviderErr = errors.New("db down")
	svc := NewService(store, zerolog.Nop())
	_, err := svc.ListSubscriptions(context.Background(), models.Scope{OrgID: uuid.New()})
	if err == nil {
		t.Fatal("want wrapped list error")
	}
}

func TestListSubscriptions_SkipsNonSubscriptionConfig(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	// Seed a labeled cred with APIKey-only (not a subscription) — it must be
	// skipped so the returned list has zero entries.
	id := uuid.New()
	store.creds[id] = &models.DecryptedCredential{
		ID:       id,
		OrgID:    orgID,
		Provider: models.ProviderAnthropic,
		Label:    "labelled-but-apikey",
		Config:   models.AnthropicConfig{APIKey: "sk-ant"},
		Status:   "active",
	}

	subs, err := svc.ListSubscriptions(context.Background(), models.Scope{OrgID: orgID})
	if err != nil {
		t.Fatalf("ListSubscriptions: %v", err)
	}
	if len(subs) != 0 {
		t.Errorf("want 0 subs, got %v", subs)
	}
}

func TestFetchProfile_ShortCircuitsWhenNotConfigured(t *testing.T) {
	t.Parallel()
	svc := NewService(newMockCredentialStore(), zerolog.Nop())
	svc.SetProfileURL("")

	profile, err := svc.fetchProfile(context.Background(), "access")
	if err != nil || profile != nil {
		t.Errorf("want (nil, nil) when profileURL empty, got (%v, %v)", profile, err)
	}

	svc.SetProfileURL("https://anthropic.example/profile")
	profile, err = svc.fetchProfile(context.Background(), "")
	if err != nil || profile != nil {
		t.Errorf("want (nil, nil) when access token empty, got (%v, %v)", profile, err)
	}
}

func TestFetchProfile_Non200(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	}))
	defer ts.Close()

	svc := NewService(newMockCredentialStore(), zerolog.Nop())
	svc.SetProfileURL(ts.URL)

	_, err := svc.fetchProfile(context.Background(), "access")
	if err == nil {
		t.Fatal("want error on non-200")
	}
}

func TestFetchProfile_MalformedJSON(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer ts.Close()

	svc := NewService(newMockCredentialStore(), zerolog.Nop())
	svc.SetProfileURL(ts.URL)

	_, err := svc.fetchProfile(context.Background(), "access")
	if err == nil {
		t.Fatal("want parse error")
	}
}

func TestFetchProfile_NetworkError(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	url := ts.URL
	ts.Close()

	svc := NewService(newMockCredentialStore(), zerolog.Nop())
	svc.SetProfileURL(url)

	_, err := svc.fetchProfile(context.Background(), "access")
	if err == nil {
		t.Fatal("want network error")
	}
}

func TestExchangeAuthCode_EmptyAccessToken(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"","refresh_token":"r","expires_in":3600}`))
	}))
	defer ts.Close()

	svc := NewService(store, zerolog.Nop())
	svc.SetTokenURL(ts.URL)
	svc.SetProfileURL("")

	orgID := uuid.New()
	resp, err := svc.InitiateOAuth(context.Background(), models.Scope{OrgID: orgID}, nil, "team-a")
	if err != nil {
		t.Fatalf("InitiateOAuth: %v", err)
	}
	if _, err := svc.CompleteOAuth(context.Background(), models.Scope{OrgID: orgID}, "team-a", "code#"+resp.State); err == nil || !strings.Contains(err.Error(), "empty access_token") {
		t.Errorf("want empty access_token error, got %v", err)
	}
}

func TestExchangeAuthCode_NetworkError(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	url := ts.URL
	ts.Close()

	svc := NewService(store, zerolog.Nop())
	svc.SetTokenURL(url)
	svc.SetProfileURL("")

	orgID := uuid.New()
	resp, err := svc.InitiateOAuth(context.Background(), models.Scope{OrgID: orgID}, nil, "team-a")
	if err != nil {
		t.Fatalf("InitiateOAuth: %v", err)
	}
	if _, err := svc.CompleteOAuth(context.Background(), models.Scope{OrgID: orgID}, "team-a", "code#"+resp.State); err == nil {
		t.Fatal("want network error")
	}
}

func TestExchangeAuthCode_MalformedResponse(t *testing.T) {
	t.Parallel()
	store := newMockCredentialStore()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer ts.Close()

	svc := NewService(store, zerolog.Nop())
	svc.SetTokenURL(ts.URL)
	svc.SetProfileURL("")

	orgID := uuid.New()
	resp, err := svc.InitiateOAuth(context.Background(), models.Scope{OrgID: orgID}, nil, "team-a")
	if err != nil {
		t.Fatalf("InitiateOAuth: %v", err)
	}
	if _, err := svc.CompleteOAuth(context.Background(), models.Scope{OrgID: orgID}, "team-a", "code#"+resp.State); err == nil {
		t.Fatal("want parse error")
	}
}

// --- Personal-scope coverage ---
//
// The OAuth flow used to be exclusively org-scoped; this PR added personal
// scope so individual users can connect their own Claude subscription as a
// personal-stack credential. The tests below lock the new contract: scope
// flows through Initiate, GetByProviderAndLabel sees the personal row, and
// the personal pendingKey shape is distinct from the org one so the same
// label can coexist across scopes.

func TestPendingKey_ScopesAreDistinct(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	userID := uuid.New()
	orgKey := pendingKey(models.Scope{OrgID: orgID}, "team-a")
	personalKey := pendingKey(models.Scope{OrgID: orgID, UserID: &userID}, "team-a")
	require.NotEqual(t, orgKey, personalKey, "personal scope must produce a distinct pending key")
	require.Contains(t, personalKey, ":u:", "personal pendingKey expected to contain :u: segment")

	collidingOrgLabel := "u:" + userID.String() + ":team-a"
	collidingOrgKey := pendingKey(models.Scope{OrgID: orgID}, collidingOrgLabel)
	require.NotEqual(t, collidingOrgKey, personalKey, "org labels must not be able to collide with personal pending keys")
}

func TestInitiateOAuth_PersonalScope(t *testing.T) {
	t.Parallel()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	userID := uuid.New()
	personalScope := models.Scope{OrgID: orgID, UserID: &userID}

	resp, err := svc.InitiateOAuth(context.Background(), personalScope, &userID, "personal-claude")
	require.NoError(t, err)
	require.NotEmpty(t, resp.State, "personal OAuth must produce a CSRF state")
	require.Contains(t, resp.AuthorizeURL, "code_challenge=", "authorize URL missing PKCE challenge")

	cred, err := store.GetByProviderAndLabel(context.Background(), personalScope, models.ProviderAnthropic, "personal-claude")
	require.NoError(t, err, "personal pending row must be persisted")
	require.Equal(t, models.CredentialStatusPendingAuth, cred.Status, "personal row must start in pending_auth")
	require.NotNil(t, cred.CreatedBy)
	require.Equal(t, userID, *cred.CreatedBy)

	_, err = store.GetByProviderAndLabel(context.Background(), models.Scope{OrgID: orgID}, models.ProviderAnthropic, "personal-claude")
	require.Error(t, err, "personal-scope row must not be visible to an org-scope read")
}

func TestInitiateOAuth_SameLabelAcrossScopes(t *testing.T) {
	// A user adding a personal "claude-pro" subscription must not collide
	// with an org-scope "claude-pro" subscription that already exists.
	t.Parallel()

	store := newMockCredentialStore()
	svc := NewService(store, zerolog.Nop())

	orgID := uuid.New()
	userID := uuid.New()
	label := "claude-pro"

	_, err := svc.InitiateOAuth(context.Background(), models.Scope{OrgID: orgID}, nil, label)
	require.NoError(t, err, "org initiate should succeed")
	_, err = svc.InitiateOAuth(context.Background(), models.Scope{OrgID: orgID, UserID: &userID}, &userID, label)
	require.NoError(t, err, "personal initiate with the same label must succeed")

	orgRow, err := store.GetByProviderAndLabel(context.Background(), models.Scope{OrgID: orgID}, models.ProviderAnthropic, label)
	require.NoError(t, err)
	personalRow, err := store.GetByProviderAndLabel(context.Background(), models.Scope{OrgID: orgID, UserID: &userID}, models.ProviderAnthropic, label)
	require.NoError(t, err)
	require.NotEqual(t, orgRow.ID, personalRow.ID, "org and personal rows must be distinct credentials despite sharing the label")
}
