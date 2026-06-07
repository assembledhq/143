// Package db — scoped_credential_store.go
//
// ScopedCredentialStore is a scope-aware façade for the OAuth subscription
// flows (codexauth, claudecodeauth). It bridges two concrete stores during
// the unified-credentials migration:
//
//   - Org-scoped operations (Scope.UserID == nil) flow through
//     OrgCredentialStore, which persists to the legacy org_credentials table
//     and mirrors to coding_credentials.
//   - Personal-scoped operations (Scope.UserID != nil) target
//     CodingCredentialStore directly. The legacy user_credentials table does
//     not model multi-label rows that subscription credentials require, so
//     personal subscriptions live exclusively in the unified store.
//
// The adapter preserves the legacy provider/config types that the OAuth
// services emit (OpenAIChatGPTConfig, AnthropicConfig{Subscription:...}).
// On the personal path it transparently translates writes to the unified
// provider names + config types (openai_subscription / OpenAISubscriptionConfig,
// anthropic_subscription / AnthropicSubscriptionConfig). Reads are translated
// back so the OAuth services never see unified types.
package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
)

// ScopedCredentialStore implements the credential surface that the OAuth
// services depend on, routing per-method by Scope.
type ScopedCredentialStore struct {
	org    *OrgCredentialStore
	coding *CodingCredentialStore
}

// NewScopedCredentialStore wires the two backing stores. Both are required
// in production — passing nil panics so a misconfigured boot fails fast at
// startup rather than surfacing as confusing "personal scope requires
// CodingCredentialStore" errors at the first incoming request.
//
// Tests that need a partial store (e.g. an in-memory fake) should
// implement the auth services' CredentialStore interface directly instead
// of constructing a ScopedCredentialStore with one nil backend.
func NewScopedCredentialStore(org *OrgCredentialStore, coding *CodingCredentialStore) *ScopedCredentialStore {
	if org == nil {
		panic("db: NewScopedCredentialStore requires a non-nil OrgCredentialStore")
	}
	if coding == nil {
		panic("db: NewScopedCredentialStore requires a non-nil CodingCredentialStore")
	}
	return &ScopedCredentialStore{org: org, coding: coding}
}

// Auth services define their own ErrCredentialNotFound sentinels and check
// pgx.ErrNoRows + their local sentinel via isNotFoundError. To avoid a cycle
// (the adapter can't import the auth packages), we surface unified-store
// not-found errors with ErrCodingCredentialNotFound in the chain — auth
// services have been updated to errors.Is against this in addition to their
// local sentinel.

// InsertPendingAuth inserts a pending-auth credential row at the given scope.
func (s *ScopedCredentialStore) InsertPendingAuth(ctx context.Context, scope models.Scope, createdBy *uuid.UUID, label string, cfg models.ProviderConfig) (*uuid.UUID, error) {
	if scope.IsPersonal() {
		unified, err := translateConfigForUnifiedWrite(cfg)
		if err != nil {
			return nil, err
		}
		return s.coding.InsertPendingAuth(ctx, scope, label, unified, createdBy)
	}
	return s.org.InsertPendingAuth(ctx, scope.OrgID, createdBy, label, cfg)
}

// UpsertWithLabel writes an active credential at (scope, provider, label).
// On the personal path: pending rows for the same label are promoted to
// active; otherwise a fresh row is inserted.
//
// Concurrency note: the personal path is NOT atomic — it does a separate
// GetByProviderAndLabel + (PromotePending | UpdateConfig | Create). The
// legacy OrgCredentialStore.UpsertWithLabel is single-statement so it
// avoids this race. In practice we get away with it because the auth
// services serialise concurrent Initiate/Complete calls per (scope, label)
// via a sync.Map mutex (see codexauth.Service.initMu /
// claudecodeauth.Service.initMu), so two racing CompleteOAuth calls for
// the same label cannot both reach this method at once. If a future caller
// drops that mutex, two racing Creates would both lose to the unique
// index and surface as ErrCodingCredentialLabelTaken to the user — the
// fix would be a transactional helper on CodingCredentialStore.
func (s *ScopedCredentialStore) UpsertWithLabel(ctx context.Context, scope models.Scope, createdBy *uuid.UUID, label string, cfg models.ProviderConfig) (*uuid.UUID, error) {
	if scope.IsPersonal() {
		unified, err := translateConfigForUnifiedWrite(cfg)
		if err != nil {
			return nil, err
		}
		// Look up an existing row at this (scope, provider, label) so we
		// promote-or-rotate instead of erroring on conflict.
		existing, lookupErr := s.coding.GetByProviderAndLabel(ctx, scope, unified.Provider(), label)
		if lookupErr != nil && !errors.Is(lookupErr, ErrCodingCredentialNotFound) {
			return nil, lookupErr
		}
		if existing != nil {
			switch existing.Status {
			case models.CodingCredentialStatusPendingAuth:
				if err := s.coding.PromotePending(ctx, scope, existing.ID, unified); err != nil {
					return nil, err
				}
				return &existing.ID, nil
			case models.CodingCredentialStatusActive,
				models.CodingCredentialStatusInvalid:
				if err := s.coding.UpdateConfigVerified(ctx, scope, existing.ID, unified); err != nil {
					return nil, err
				}
				return &existing.ID, nil
			case models.CodingCredentialStatusDisabled:
				return s.coding.Create(ctx, scope, label, unified, CreateOpts{CreatedBy: createdBy})
			}
		}
		return s.coding.Create(ctx, scope, label, unified, CreateOpts{CreatedBy: createdBy})
	}
	return s.org.UpsertWithLabel(ctx, scope.OrgID, createdBy, label, cfg)
}

// UpsertByID overwrites the config of a credential by ID. For pending_auth
// rows this promotes them to active (CompleteOAuth); for active rows it
// rotates the stored tokens (RefreshTokenByID).
func (s *ScopedCredentialStore) UpsertByID(ctx context.Context, scope models.Scope, id uuid.UUID, cfg models.ProviderConfig) error {
	if scope.IsPersonal() {
		unified, err := translateConfigForUnifiedWrite(cfg)
		if err != nil {
			return err
		}
		// Look up the row's current status so we route promote-vs-rotate
		// the same way the legacy UpsertByID does (which sets status='active'
		// in either case but keeps disabled rows untouched).
		row, err := s.coding.Get(ctx, scope, id)
		if err != nil {
			return err
		}
		if row.Status == models.CodingCredentialStatusPendingAuth {
			return s.coding.PromotePending(ctx, scope, id, unified)
		}
		// UpdateConfig refuses to resurrect disabled rows — matches the
		// legacy UpsertByID's `WHERE status != 'disabled'` guard.
		return s.coding.UpdateConfigVerified(ctx, scope, id, unified)
	}
	return s.org.UpsertByID(ctx, scope.OrgID, id, cfg)
}

// GetByID returns the credential at (scope, id). The returned row carries
// legacy types regardless of which store backed it.
func (s *ScopedCredentialStore) GetByID(ctx context.Context, scope models.Scope, id uuid.UUID) (*models.DecryptedCredential, error) {
	if scope.IsPersonal() {
		row, err := s.coding.Get(ctx, scope, id)
		if err != nil {
			return nil, err
		}
		legacy, err := decryptedCredentialFromCoding(row)
		if err != nil {
			return nil, err
		}
		return legacy, nil
	}
	return s.org.GetByID(ctx, scope.OrgID, id)
}

// GetByProviderAndLabel finds a row at (scope, provider, label). The provider
// argument uses the legacy name (anthropic / openai_chatgpt); the adapter
// translates to the unified provider for personal-scope lookups.
func (s *ScopedCredentialStore) GetByProviderAndLabel(ctx context.Context, scope models.Scope, provider models.ProviderName, label string) (*models.DecryptedCredential, error) {
	if scope.IsPersonal() {
		unifiedProvider := translateProviderForUnified(provider)
		row, err := s.coding.GetByProviderAndLabel(ctx, scope, unifiedProvider, label)
		if err != nil {
			return nil, err
		}
		legacy, err := decryptedCredentialFromCoding(row)
		if err != nil {
			return nil, err
		}
		return legacy, nil
	}
	return s.org.GetByProviderAndLabel(ctx, scope.OrgID, provider, label)
}

// ListByProvider lists every (scope, provider) row, translating personal
// rows back to legacy types.
func (s *ScopedCredentialStore) ListByProvider(ctx context.Context, scope models.Scope, provider models.ProviderName) ([]models.DecryptedCredential, error) {
	if scope.IsPersonal() {
		// On the personal path, "list anthropic" should surface subscription
		// rows the OAuth flow created — those live under
		// anthropic_subscription in the unified table. The legacy List, by
		// contrast, returns rows stored as `anthropic` with an embedded
		// Subscription. List both unified providers so the legacy callers
		// (which key off provider=anthropic) see exactly the rows they own.
		unifiedProvider := translateProviderForUnified(provider)
		rows, err := s.coding.ListByProvider(ctx, scope, unifiedProvider)
		if err != nil {
			return nil, err
		}
		out := make([]models.DecryptedCredential, 0, len(rows))
		for i := range rows {
			legacy, err := decryptedCredentialFromCoding(&rows[i])
			if err != nil {
				return nil, err
			}
			out = append(out, *legacy)
		}
		return out, nil
	}
	return s.org.ListByProvider(ctx, scope.OrgID, provider)
}

// DisableByID soft-deletes a credential. Idempotent across scopes.
func (s *ScopedCredentialStore) DisableByID(ctx context.Context, scope models.Scope, id uuid.UUID) error {
	if scope.IsPersonal() {
		err := s.coding.Disable(ctx, scope, id)
		if errors.Is(err, ErrCodingCredentialNotFound) {
			// Match legacy semantics: disabling a missing row is a no-op.
			return nil
		}
		return err
	}
	return s.org.DisableByID(ctx, scope.OrgID, id)
}

// UpdateStatusByID flips the row's status. Used to mark a credential invalid
// after a refresh fails authentication.
func (s *ScopedCredentialStore) UpdateStatusByID(ctx context.Context, scope models.Scope, id uuid.UUID, status models.CodingCredentialRowStatus) error {
	if scope.IsPersonal() {
		return s.coding.UpdateStatus(ctx, scope, id, status)
	}
	return s.org.UpdateStatusByID(ctx, scope.OrgID, id, models.CredentialStatus(status))
}

// ExistsForProviderByID is used by Disconnect to verify an id belongs to the
// caller's scope before disabling. Includes disabled rows to distinguish
// "not yours" from "already disconnected".
func (s *ScopedCredentialStore) ExistsForProviderByID(ctx context.Context, scope models.Scope, id uuid.UUID, provider models.ProviderName) (bool, error) {
	if scope.IsPersonal() {
		row, err := s.coding.Get(ctx, scope, id)
		if err != nil {
			if errors.Is(err, ErrCodingCredentialNotFound) {
				return false, nil
			}
			return false, err
		}
		// Any unified row whose provider maps back to the requested legacy
		// provider counts. For Anthropic this matches both the API-key row
		// (provider=anthropic) and subscription rows (anthropic_subscription).
		return translateProviderFromUnified(row.Provider) == provider, nil
	}
	return s.org.ExistsForProviderByID(ctx, scope.OrgID, id, provider)
}

// HasActiveLabeled reports whether at least one active labeled credential
// exists at (scope, provider). Backs HasActiveSubscription.
func (s *ScopedCredentialStore) HasActiveLabeled(ctx context.Context, scope models.Scope, provider models.ProviderName) (bool, error) {
	if scope.IsPersonal() {
		unifiedProvider := translateProviderForUnified(provider)
		rows, err := s.coding.ListByProvider(ctx, scope, unifiedProvider)
		if err != nil {
			return false, err
		}
		for _, row := range rows {
			if row.Label != "" && row.Status == models.CodingCredentialStatusActive {
				return true, nil
			}
		}
		return false, nil
	}
	return s.org.HasActiveLabeled(ctx, scope.OrgID, provider)
}

// DisableLabeled disables every labeled subscription row at (scope, provider)
// while leaving the API-key row (label="") intact. Used by DisconnectAll so
// the caller doesn't lose their fallback API key.
func (s *ScopedCredentialStore) DisableLabeled(ctx context.Context, scope models.Scope, provider models.ProviderName) error {
	if scope.IsPersonal() {
		// On the personal path the API-key row lives under provider=anthropic
		// while subscription rows live under anthropic_subscription. Listing
		// only the subscription provider is therefore safe — the API-key row
		// is in a separate provider partition and untouched.
		unifiedProvider := translateProviderForUnified(provider)
		rows, err := s.coding.ListByProvider(ctx, scope, unifiedProvider)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Label == "" || row.Status == models.CodingCredentialStatusDisabled {
				continue
			}
			if err := s.coding.Disable(ctx, scope, row.ID); err != nil && !errors.Is(err, ErrCodingCredentialNotFound) {
				return err
			}
		}
		return nil
	}
	return s.org.DisableLabeled(ctx, scope.OrgID, provider)
}

// Disable removes the unlabeled credential at (scope, provider). Legacy
// single-credential path (codexauth.DisconnectAll) — personal scope is a
// no-op since personal subscriptions always carry a label.
func (s *ScopedCredentialStore) Disable(ctx context.Context, scope models.Scope, provider models.ProviderName) error {
	if scope.IsPersonal() {
		// "Disable everything for this provider" on the personal path:
		// disable every active row at this provider regardless of label.
		unifiedProvider := translateProviderForUnified(provider)
		rows, err := s.coding.ListByProvider(ctx, scope, unifiedProvider)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Status == models.CodingCredentialStatusDisabled {
				continue
			}
			if err := s.coding.Disable(ctx, scope, row.ID); err != nil && !errors.Is(err, ErrCodingCredentialNotFound) {
				return err
			}
		}
		return nil
	}
	return s.org.Disable(ctx, scope.OrgID, provider)
}

// ClaimNextRoundRobin / ClaimNextLabeledRoundRobin back the legacy
// GetValidToken runtime path that walks an org's subscription stack in
// least-recently-used order. Personal scope routes through the unified
// resolver (env.go's pickFromCodingProviderSet), so these methods return
// ErrCodingCredentialNotFound on personal scope to surface the
// misconfiguration loudly rather than silently never picking a credential.
func (s *ScopedCredentialStore) ClaimNextRoundRobin(ctx context.Context, scope models.Scope, provider models.ProviderName) (*models.DecryptedCredential, error) {
	if scope.IsPersonal() {
		return nil, ErrCodingCredentialNotFound
	}
	return s.org.ClaimNextRoundRobin(ctx, scope.OrgID, provider)
}

// ClaimNextLabeledRoundRobin is the labeled-row variant for providers like
// Anthropic that mix API-key (label="") and subscription (label!="") rows.
func (s *ScopedCredentialStore) ClaimNextLabeledRoundRobin(ctx context.Context, scope models.Scope, provider models.ProviderName) (*models.DecryptedCredential, error) {
	if scope.IsPersonal() {
		return nil, ErrCodingCredentialNotFound
	}
	return s.org.ClaimNextLabeledRoundRobin(ctx, scope.OrgID, provider)
}

// translateProviderForUnified maps a legacy provider name (the value the
// OAuth services pass in) to the unified provider where personal-scope
// subscription rows actually live.
func translateProviderForUnified(p models.ProviderName) models.ProviderName {
	switch p {
	case models.ProviderOpenAIChatGPT:
		return models.ProviderOpenAISubscription
	case models.ProviderAnthropic:
		// Subscriptions live under anthropic_subscription. The bare
		// anthropic provider in the unified table is API-key-only, which
		// the OAuth services never read.
		return models.ProviderAnthropicSubscription
	}
	return p
}

// translateProviderFromUnified is the inverse of translateProviderForUnified.
func translateProviderFromUnified(p models.ProviderName) models.ProviderName {
	switch p {
	case models.ProviderOpenAISubscription:
		return models.ProviderOpenAIChatGPT
	case models.ProviderAnthropicSubscription:
		return models.ProviderAnthropic
	}
	return p
}

// translateConfigForUnifiedWrite converts the legacy OAuth config types the
// services emit into their unified counterparts before writing.
func translateConfigForUnifiedWrite(cfg models.ProviderConfig) (models.ProviderConfig, error) {
	switch v := cfg.(type) {
	case models.OpenAIChatGPTConfig:
		// OpenAIChatGPTConfig and OpenAISubscriptionConfig have identical
		// field layouts; the conversion is a zero-cost rename that shifts
		// the row to provider=openai_subscription on the unified table.
		return models.OpenAISubscriptionConfig(v), nil
	case models.AnthropicConfig:
		// API-key-only AnthropicConfig is left as-is — personal anthropic
		// API keys live under provider=anthropic in the unified table. Only
		// subscription configs need the rewrite.
		if v.Subscription == nil {
			return v, nil
		}
		sub := v.Subscription
		return models.AnthropicSubscriptionConfig{
			AccessToken:   sub.AccessToken,
			RefreshToken:  sub.RefreshToken,
			ExpiresAt:     sub.ExpiresAt,
			AccountType:   sub.AccountType,
			RateLimitTier: sub.RateLimitTier,
			Scopes:        sub.Scopes,
			State:         sub.State,
			CodeVerifier:  sub.CodeVerifier,
			AuthorizeURL:  sub.AuthorizeURL,
		}, nil
	}
	return cfg, nil
}

// translateConfigFromUnifiedRead is the inverse — invoked when surfacing a
// unified row to a legacy-typed caller. The OAuth services pattern-match on
// AnthropicConfig / OpenAIChatGPTConfig, so we hand them exactly that.
func translateConfigFromUnifiedRead(cfg models.ProviderConfig) models.ProviderConfig {
	switch v := cfg.(type) {
	case models.OpenAISubscriptionConfig:
		// Reverse of the write-side conversion — same identical-field
		// rename, just back to the legacy type the OAuth services expect.
		return models.OpenAIChatGPTConfig(v)
	case models.AnthropicSubscriptionConfig:
		return models.AnthropicConfig{
			Subscription: &models.AnthropicSubscription{
				AccessToken:   v.AccessToken,
				RefreshToken:  v.RefreshToken,
				ExpiresAt:     v.ExpiresAt,
				AccountType:   v.AccountType,
				RateLimitTier: v.RateLimitTier,
				Scopes:        v.Scopes,
				State:         v.State,
				CodeVerifier:  v.CodeVerifier,
				AuthorizeURL:  v.AuthorizeURL,
			},
		}
	}
	return cfg
}

// decryptedCredentialFromCoding maps a unified row to the legacy decrypted
// shape. Provider and config are translated; UserID is dropped because the
// legacy struct has no slot for it (callers already hold scope context).
func decryptedCredentialFromCoding(row *models.DecryptedCodingCredential) (*models.DecryptedCredential, error) {
	if row == nil {
		return nil, fmt.Errorf("nil coding credential")
	}
	return &models.DecryptedCredential{
		ID:             row.ID,
		OrgID:          row.OrgID,
		Provider:       translateProviderFromUnified(row.Provider),
		Label:          row.Label,
		Config:         translateConfigFromUnifiedRead(row.Config),
		Status:         models.CredentialStatus(row.Status),
		Priority:       row.Priority,
		LastVerifiedAt: row.LastVerifiedAt,
		CreatedBy:      row.CreatedBy,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}, nil
}
