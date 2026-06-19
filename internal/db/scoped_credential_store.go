// Package db — scoped_credential_store.go
//
// ScopedCredentialStore is a scope-aware façade for the OAuth subscription
// flows (codexauth, claudecodeauth). Both org-scoped (Scope.UserID == nil)
// and personal-scoped operations target CodingCredentialStore — the unified
// coding_credentials table is the only credential store for coding agents.
//
// The adapter's remaining value over the raw CodingCredentialStore is scope
// routing plus the promote-or-rotate upsert logic the OAuth services rely on.
// Provider names and config types pass through untranslated: the services
// speak the unified vocabulary directly (openai_subscription /
// OpenAISubscriptionConfig, anthropic_subscription /
// AnthropicSubscriptionConfig).
package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
)

// ScopedCredentialStore implements the credential surface that the OAuth
// services depend on, routing every method to the unified store.
type ScopedCredentialStore struct {
	coding *CodingCredentialStore
}

// NewScopedCredentialStore wires the unified backing store. It is required
// in production — passing nil panics so a misconfigured boot fails fast at
// startup rather than surfacing as confusing errors at the first request.
func NewScopedCredentialStore(coding *CodingCredentialStore) *ScopedCredentialStore {
	if coding == nil {
		panic("db: NewScopedCredentialStore requires a non-nil CodingCredentialStore")
	}
	return &ScopedCredentialStore{coding: coding}
}

// Auth services define their own ErrCredentialNotFound sentinels and check
// pgx.ErrNoRows + their local sentinel via isNotFoundError. To avoid a cycle
// (the adapter can't import the auth packages), we surface unified-store
// not-found errors with ErrCodingCredentialNotFound in the chain — auth
// services errors.Is against this in addition to their local sentinel.

// InsertPendingAuth inserts a pending-auth credential row at the given scope.
func (s *ScopedCredentialStore) InsertPendingAuth(ctx context.Context, scope models.Scope, createdBy *uuid.UUID, label string, cfg models.ProviderConfig) (*uuid.UUID, error) {
	return s.coding.InsertPendingAuth(ctx, scope, label, cfg, createdBy)
}

// UpsertWithLabel writes an active credential at (scope, provider, label).
// Pending rows for the same label are promoted to active; otherwise a fresh
// row is inserted.
//
// Concurrency note: this is NOT atomic — it does a separate
// GetByProviderAndLabel + (PromotePending | UpdateConfigVerified | Create).
// In practice we get away with it because the auth services serialise
// concurrent Initiate/Complete calls per (scope, label) via a sync.Map mutex
// (see codexauth.Service.initMu / claudecodeauth.Service.initMu), so two
// racing CompleteOAuth calls for the same label cannot both reach this method
// at once. If a future caller drops that mutex, two racing Creates would both
// lose to the unique index and surface as ErrCodingCredentialLabelTaken to
// the user — the fix would be a transactional helper on CodingCredentialStore.
func (s *ScopedCredentialStore) UpsertWithLabel(ctx context.Context, scope models.Scope, createdBy *uuid.UUID, label string, cfg models.ProviderConfig) (*uuid.UUID, error) {
	// Look up an existing row at this (scope, provider, label) so we
	// promote-or-rotate instead of erroring on conflict.
	existing, lookupErr := s.coding.GetByProviderAndLabel(ctx, scope, cfg.Provider(), label)
	if lookupErr != nil && !errors.Is(lookupErr, ErrCodingCredentialNotFound) {
		return nil, lookupErr
	}
	if existing != nil {
		switch existing.Status {
		case models.CodingCredentialStatusPendingAuth:
			if err := s.coding.PromotePending(ctx, scope, existing.ID, cfg); err != nil {
				return nil, err
			}
			return &existing.ID, nil
		case models.CodingCredentialStatusActive,
			models.CodingCredentialStatusInvalid:
			if err := s.coding.UpdateConfigVerified(ctx, scope, existing.ID, cfg); err != nil {
				return nil, err
			}
			return &existing.ID, nil
		case models.CodingCredentialStatusDisabled:
			return s.coding.Create(ctx, scope, label, cfg, CreateOpts{CreatedBy: createdBy})
		}
	}
	return s.coding.Create(ctx, scope, label, cfg, CreateOpts{CreatedBy: createdBy})
}

// UpsertByID overwrites the config of a credential by ID. For pending_auth
// rows this promotes them to active (CompleteOAuth); for active rows it
// rotates the stored tokens (RefreshTokenByID).
func (s *ScopedCredentialStore) UpsertByID(ctx context.Context, scope models.Scope, id uuid.UUID, cfg models.ProviderConfig) error {
	// Look up the row's current status so we route promote-vs-rotate
	// the same way the legacy UpsertByID did (which set status='active'
	// in either case but kept disabled rows untouched).
	row, err := s.coding.Get(ctx, scope, id)
	if err != nil {
		return err
	}
	if row.Status == models.CodingCredentialStatusPendingAuth {
		return s.coding.PromotePending(ctx, scope, id, cfg)
	}
	// UpdateConfigVerified refuses to resurrect disabled rows — matches
	// the legacy UpsertByID's `WHERE status != 'disabled'` guard.
	return s.coding.UpdateConfigVerified(ctx, scope, id, cfg)
}

// GetByID returns the credential at (scope, id).
func (s *ScopedCredentialStore) GetByID(ctx context.Context, scope models.Scope, id uuid.UUID) (*models.DecryptedCredential, error) {
	row, err := s.coding.Get(ctx, scope, id)
	if err != nil {
		return nil, err
	}
	return decryptedCredentialFromCoding(row)
}

// GetByProviderAndLabel finds a row at (scope, provider, label).
func (s *ScopedCredentialStore) GetByProviderAndLabel(ctx context.Context, scope models.Scope, provider models.ProviderName, label string) (*models.DecryptedCredential, error) {
	row, err := s.coding.GetByProviderAndLabel(ctx, scope, provider, label)
	if err != nil {
		return nil, err
	}
	return decryptedCredentialFromCoding(row)
}

// ListByProvider lists every (scope, provider) row.
func (s *ScopedCredentialStore) ListByProvider(ctx context.Context, scope models.Scope, provider models.ProviderName) ([]models.DecryptedCredential, error) {
	rows, err := s.coding.ListByProvider(ctx, scope, provider)
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

// DisableByID soft-deletes a credential. Idempotent across scopes.
func (s *ScopedCredentialStore) DisableByID(ctx context.Context, scope models.Scope, id uuid.UUID) error {
	err := s.coding.Disable(ctx, scope, id)
	if errors.Is(err, ErrCodingCredentialNotFound) {
		// Match legacy semantics: disabling a missing row is a no-op.
		return nil
	}
	return err
}

// UpdateStatusByID flips the row's status. Used to mark a credential invalid
// after a refresh fails authentication.
func (s *ScopedCredentialStore) UpdateStatusByID(ctx context.Context, scope models.Scope, id uuid.UUID, status models.CodingCredentialRowStatus) error {
	return s.coding.UpdateStatus(ctx, scope, id, status)
}

// WithRefreshLock serializes OAuth token refreshes for one credential across
// processes via the unified store's Postgres advisory lock. Satisfies the
// auth services' optional RefreshLocker interface.
//
// lint:allow-no-orgid reason="advisory lock keyed by credential ID only; it reads/writes no rows — scope is enforced by every store call fn makes while holding it"
func (s *ScopedCredentialStore) WithRefreshLock(ctx context.Context, credID uuid.UUID, fn func(ctx context.Context) error) error {
	return s.coding.WithRefreshLock(ctx, credID, fn)
}

// ExistsForProviderByID is used by Disconnect to verify an id belongs to the
// caller's scope before disabling. Includes disabled rows to distinguish
// "not yours" from "already disconnected".
func (s *ScopedCredentialStore) ExistsForProviderByID(ctx context.Context, scope models.Scope, id uuid.UUID, provider models.ProviderName) (bool, error) {
	row, err := s.coding.Get(ctx, scope, id)
	if err != nil {
		if errors.Is(err, ErrCodingCredentialNotFound) {
			return false, nil
		}
		return false, err
	}
	return row.Provider == provider, nil
}

// HasActiveLabeled reports whether at least one active labeled credential
// exists at (scope, provider). Backs HasActiveSubscription.
func (s *ScopedCredentialStore) HasActiveLabeled(ctx context.Context, scope models.Scope, provider models.ProviderName) (bool, error) {
	rows, err := s.coding.ListByProvider(ctx, scope, provider)
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

// DisableLabeled disables every labeled subscription row at (scope, provider)
// while leaving the API-key row (label="") intact. Used by DisconnectAll so
// the caller doesn't lose their fallback API key.
func (s *ScopedCredentialStore) DisableLabeled(ctx context.Context, scope models.Scope, provider models.ProviderName) error {
	// The API-key row lives under provider=anthropic while subscription rows
	// live under anthropic_subscription. Listing only the subscription
	// provider is therefore safe — the API-key row is in a separate provider
	// partition and untouched.
	rows, err := s.coding.ListByProvider(ctx, scope, provider)
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

// Disable removes every credential at (scope, provider). Legacy
// single-credential path (codexauth.DisconnectAll).
func (s *ScopedCredentialStore) Disable(ctx context.Context, scope models.Scope, provider models.ProviderName) error {
	rows, err := s.coding.ListByProvider(ctx, scope, provider)
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

// ClaimNextRoundRobin / ClaimNextLabeledRoundRobin back the legacy
// GetValidToken runtime path. Selection is delegated to the unified store's
// PickRunnable (priority tiers, random within a tier, rate-limit shedding via
// the in-process health cache) — the legacy last_used_at round-robin walked
// org_credentials, which no longer holds coding rows.
func (s *ScopedCredentialStore) ClaimNextRoundRobin(ctx context.Context, scope models.Scope, provider models.ProviderName) (*models.DecryptedCredential, error) {
	return s.pickRunnable(ctx, scope, provider)
}

// ClaimNextLabeledRoundRobin is the labeled-row variant for providers like
// Anthropic that mix API-key (label="") and subscription (label!="") rows.
// Subscriptions live in their own provider partition (anthropic_subscription),
// so the unified pick is already scoped to the subscription set.
func (s *ScopedCredentialStore) ClaimNextLabeledRoundRobin(ctx context.Context, scope models.Scope, provider models.ProviderName) (*models.DecryptedCredential, error) {
	return s.pickRunnable(ctx, scope, provider)
}

func (s *ScopedCredentialStore) pickRunnable(ctx context.Context, scope models.Scope, provider models.ProviderName) (*models.DecryptedCredential, error) {
	row, err := s.coding.PickRunnable(ctx, scope, provider)
	if err != nil {
		return nil, err
	}
	return decryptedCredentialFromCoding(row)
}

// decryptedCredentialFromCoding maps a unified row to the flat decrypted
// shape the OAuth services consume. UserID is dropped because the flat struct
// has no slot for it (callers already hold scope context).
func decryptedCredentialFromCoding(row *models.DecryptedCodingCredential) (*models.DecryptedCredential, error) {
	if row == nil {
		return nil, fmt.Errorf("nil coding credential")
	}
	return &models.DecryptedCredential{
		ID:             row.ID,
		OrgID:          row.OrgID,
		Provider:       row.Provider,
		Label:          row.Label,
		Config:         row.Config,
		Status:         models.CredentialStatus(row.Status),
		Priority:       row.Priority,
		LastVerifiedAt: row.LastVerifiedAt,
		CreatedBy:      row.CreatedBy,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}, nil
}
