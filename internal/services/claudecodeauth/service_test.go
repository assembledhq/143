package claudecodeauth

import (
	"context"
	"errors"
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
	for _, cred := range m.creds {
		if cred.OrgID == orgID && cred.Provider == cfg.Provider() && cred.Label == label {
			if cred.Status != "pending_auth" && cred.Status != "disabled" {
				return nil, &db.ErrCredentialLabelTaken{Label: label, ExistingStatus: cred.Status}
			}
			cred.Config = cfg
			cred.Status = "pending_auth"
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
	if resp.Label != "team-a" {
		t.Errorf("want label team-a, got %q", resp.Label)
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
		t.Errorf("want label team-a, got %q", cred.Label)
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
