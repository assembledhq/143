// Package db — coding-credentials mirror.
//
// During the unified-coding-credentials migration window we keep legacy writes
// against `org_credentials` and `user_credentials` working unchanged for any
// caller we have not yet ported. To prevent the unified table from drifting
// we install a *mirror*: every write through the legacy store opportunistically
// writes through to `coding_credentials` as well, using the legacy row's `id`
// as the unified row's `id` so future updates stay in lockstep.
//
// The mirror is best-effort. A failure to mirror logs a warning and does not
// fail the legacy write — the next mutation re-syncs. After the cleanup PR
// removes the legacy code paths, this whole file goes away.
//
// See docs/design/future/65-unified-coding-credentials.md.
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

// CodingCredentialMirror is the narrow surface the legacy stores call against.
// Defined as an interface so the legacy stores don't need to import the full
// CodingCredentialStore type — and so tests can wire a no-op mirror.
type CodingCredentialMirror interface {
	// MirrorOrgCredential reflects a row from `org_credentials` into
	// `coding_credentials` at the given id with user_id = NULL. Provider name
	// rename (`openai_chatgpt` → `openai_subscription`) is applied here.
	// AnthropicConfig.Subscription rows are mirrored as
	// `anthropic_subscription` so the new resolver finds them; the legacy row
	// stays at provider='anthropic' until the cleanup PR.
	MirrorOrgCredential(ctx context.Context, row models.OrgCredential, decryptedCfg models.ProviderConfig) error

	// MirrorOrgCredentialDelete removes the unified row matching the legacy id.
	MirrorOrgCredentialDelete(ctx context.Context, id uuid.UUID) error

	// MirrorOrgCredentialDisable flips status to 'disabled' on the unified row.
	MirrorOrgCredentialDisable(ctx context.Context, id uuid.UUID) error

	// MirrorUserCredential reflects a row from `user_credentials` into
	// `coding_credentials`. is_team_default = true → org-scoped row;
	// is_team_default = false → user-scoped row.
	MirrorUserCredential(ctx context.Context, row models.UserCredential, decryptedCfg models.ProviderConfig) error

	// MirrorUserCredentialDelete + MirrorUserCredentialDisable are the
	// counterparts for the user-credentials surface.
	MirrorUserCredentialDelete(ctx context.Context, id uuid.UUID) error
	MirrorUserCredentialDisable(ctx context.Context, id uuid.UUID) error
}

// MirrorOrgCredential implements CodingCredentialMirror against the unified store.
func (s *CodingCredentialStore) MirrorOrgCredential(ctx context.Context, row models.OrgCredential, decryptedCfg models.ProviderConfig) error {
	// Surface dual-set Anthropic rows so a malformed migration row doesn't
	// silently lose its API-key half. AnthropicConfig.Validate enforces
	// mutual exclusion at the write path, but rows present from earlier
	// schema generations may still be in the table.
	if row.Provider == models.ProviderAnthropic {
		if c, ok := decryptedCfg.(models.AnthropicConfig); ok && c.Subscription != nil && c.APIKey != "" {
			s.mirrorWarn("anthropic row id=%s has both APIKey and Subscription set; mirroring subscription only and dropping APIKey", row.ID)
		}
	}
	provider, cfg, ok := mirrorProviderForOrg(row.Provider, decryptedCfg)
	if !ok {
		return nil // non-coding provider; nothing to mirror
	}
	encrypted, err := s.marshalAndEncrypt(cfg)
	if err != nil {
		return fmt.Errorf("mirror org credential: %w", err)
	}
	return s.upsertMirroredRow(ctx, mirroredRow{
		ID:             row.ID,
		OrgID:          row.OrgID,
		UserID:         nil,
		Provider:       provider,
		Label:          row.Label,
		EncryptedCfg:   encrypted,
		Priority:       row.Priority,
		Status:         row.Status,
		CreatedBy:      row.CreatedBy,
		LastVerifiedAt: row.LastVerifiedAt,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	})
}

// MirrorOrgCredentialDelete removes a mirrored org credential by legacy id.
//
// lint:allow-no-orgid reason="legacy id was already scope-checked by the calling OrgCredentialStore method; mirror loads scope back via RETURNING for cache invalidation"
func (s *CodingCredentialStore) MirrorOrgCredentialDelete(ctx context.Context, id uuid.UUID) error {
	return s.mirrorDelete(ctx, id)
}

// MirrorOrgCredentialDisable disables a mirrored org credential by legacy id.
//
// lint:allow-no-orgid reason="legacy id was already scope-checked by the calling OrgCredentialStore method; mirror loads scope back via RETURNING for cache invalidation"
func (s *CodingCredentialStore) MirrorOrgCredentialDisable(ctx context.Context, id uuid.UUID) error {
	return s.mirrorDisable(ctx, id)
}

func (s *CodingCredentialStore) MirrorUserCredential(ctx context.Context, row models.UserCredential, decryptedCfg models.ProviderConfig) error {
	provider, cfg, ok := mirrorProviderForUser(row.Provider, decryptedCfg)
	if !ok {
		return nil // not a coding provider
	}
	encrypted, err := s.marshalAndEncrypt(cfg)
	if err != nil {
		return fmt.Errorf("mirror user credential: %w", err)
	}

	// is_team_default → org-scoped row (user_id = NULL). The label disambiguates
	// the team-default row from a real org-scoped credential at the same provider.
	var userID *uuid.UUID
	label := ""
	if row.IsTeamDefault {
		userID = nil
		label = "Team default (mirrored from " + row.UserID.String() + ")"
	} else {
		uid := row.UserID
		userID = &uid
	}

	priority := 1 // personal user_credentials have no priority — anchor to 1.
	if row.IsTeamDefault {
		priority = teamDefaultMirrorPriority
	}

	return s.upsertMirroredRow(ctx, mirroredRow{
		ID:             row.ID,
		OrgID:          row.OrgID,
		UserID:         userID,
		Provider:       provider,
		Label:          label,
		EncryptedCfg:   encrypted,
		Priority:       priority,
		Status:         row.Status,
		CreatedBy:      &row.UserID,
		LastVerifiedAt: row.LastVerifiedAt,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	})
}

// MirrorUserCredentialDelete removes a mirrored user credential by legacy id.
//
// lint:allow-no-orgid reason="legacy id was already scope-checked by the calling UserCredentialStore method; mirror loads scope back via RETURNING for cache invalidation"
func (s *CodingCredentialStore) MirrorUserCredentialDelete(ctx context.Context, id uuid.UUID) error {
	return s.mirrorDelete(ctx, id)
}

// MirrorUserCredentialDisable disables a mirrored user credential by legacy id.
//
// lint:allow-no-orgid reason="legacy id was already scope-checked by the calling UserCredentialStore method; mirror loads scope back via RETURNING for cache invalidation"
func (s *CodingCredentialStore) MirrorUserCredentialDisable(ctx context.Context, id uuid.UUID) error {
	return s.mirrorDisable(ctx, id)
}

// teamDefaultMirrorPriority parks team-default mirrors at the bottom of the
// org stack so explicit org rows still win the resolver. Tunable; mirrors a
// rare path so contention with normal CRUD is negligible.
const teamDefaultMirrorPriority = 9000

type mirroredRow struct {
	ID             uuid.UUID
	OrgID          uuid.UUID
	UserID         *uuid.UUID
	Provider       models.ProviderName
	Label          string
	EncryptedCfg   []byte
	Priority       int
	Status         string
	CreatedBy      *uuid.UUID
	LastVerifiedAt *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// upsertMirroredRow inserts or updates a coding_credentials row preserving the
// legacy id. It uses ON CONFLICT (id) so retries are idempotent. The unique
// (org_id, user_id, provider, label) index might still trip if a separate
// mirror has already inserted at that natural key — when that happens we treat
// the conflict as an update keyed by the natural key instead of by id, leaving
// at most one row per (scope, provider, label).
func (s *CodingCredentialStore) upsertMirroredRow(ctx context.Context, row mirroredRow) error {
	args := pgx.NamedArgs{
		"id":               row.ID,
		"org_id":           row.OrgID,
		"user_id":          row.UserID,
		"provider":         string(row.Provider),
		"label":            row.Label,
		"config":           row.EncryptedCfg,
		"priority":         row.Priority,
		"status":           row.Status,
		"created_by":       row.CreatedBy,
		"last_verified_at": row.LastVerifiedAt,
		"created_at":       row.CreatedAt,
		"updated_at":       row.UpdatedAt,
	}

	// Insert by id; on conflict, update the row in place. Provider and scope
	// are immutable; we only refresh mutable fields.
	_, err := s.db.Exec(ctx, `
		INSERT INTO coding_credentials (
			id, org_id, user_id, provider, label, config, priority, status,
			created_by, last_verified_at, created_at, updated_at
		) VALUES (
			@id, @org_id, @user_id, @provider, @label, @config, @priority, @status,
			@created_by, @last_verified_at, @created_at, @updated_at
		)
		ON CONFLICT (id) DO UPDATE SET
			label            = EXCLUDED.label,
			config           = EXCLUDED.config,
			priority         = EXCLUDED.priority,
			status           = EXCLUDED.status,
			last_verified_at = EXCLUDED.last_verified_at,
			updated_at       = EXCLUDED.updated_at`,
		args,
	)
	if err == nil {
		s.invalidate(models.Scope{OrgID: row.OrgID, UserID: row.UserID}, row.Provider)
		return nil
	}
	// If the natural-key index conflicts (rare — would require an out-of-band
	// row already present at the same (scope, provider, label)), fall back to
	// updating that row by natural key. The id divergence is acceptable
	// during the migration window; the cleanup PR retires this fallback.
	if isUniqueViolation(err) {
		return s.updateMirroredRowByNaturalKey(ctx, row)
	}
	return fmt.Errorf("mirror upsert: %w", err)
}

func (s *CodingCredentialStore) updateMirroredRowByNaturalKey(ctx context.Context, row mirroredRow) error {
	args := pgx.NamedArgs{
		"org_id":           row.OrgID,
		"user_id":          row.UserID,
		"provider":         string(row.Provider),
		"label":            row.Label,
		"config":           row.EncryptedCfg,
		"priority":         row.Priority,
		"status":           row.Status,
		"last_verified_at": row.LastVerifiedAt,
		"updated_at":       row.UpdatedAt,
	}
	var query string
	if row.UserID != nil {
		query = `
			UPDATE coding_credentials SET
				config           = @config,
				priority         = @priority,
				status           = @status,
				last_verified_at = @last_verified_at,
				updated_at       = @updated_at
			WHERE org_id = @org_id AND user_id = @user_id AND provider = @provider AND label = @label`
	} else {
		query = `
			UPDATE coding_credentials SET
				config           = @config,
				priority         = @priority,
				status           = @status,
				last_verified_at = @last_verified_at,
				updated_at       = @updated_at
			WHERE org_id = @org_id AND user_id IS NULL AND provider = @provider AND label = @label`
	}
	if _, err := s.db.Exec(ctx, query, args); err != nil {
		return fmt.Errorf("mirror update by natural key: %w", err)
	}
	s.invalidate(models.Scope{OrgID: row.OrgID, UserID: row.UserID}, row.Provider)
	return nil
}

// isUniqueViolation reports a postgres unique_violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	type sqlStateErr interface{ SQLState() string }
	var s sqlStateErr
	if errors.As(err, &s) {
		return s.SQLState() == "23505"
	}
	return false
}

// mirrorDelete removes the unified row by id. RETURNING gives us the scope
// + provider so we can invalidate the resolver cache for the exact key the
// row participated in. If the row didn't exist (already deleted, or never
// mirrored because it was a non-coding provider) we no-op silently — there
// is nothing to invalidate either.
func (s *CodingCredentialStore) mirrorDelete(ctx context.Context, id uuid.UUID) error {
	var orgID uuid.UUID
	var userID *uuid.UUID
	var provider string
	err := s.db.QueryRow(ctx,
		`DELETE FROM coding_credentials WHERE id = @id RETURNING org_id, user_id, provider`,
		pgx.NamedArgs{"id": id},
	).Scan(&orgID, &userID, &provider)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("mirror delete: %w", err)
	}
	s.invalidate(models.Scope{OrgID: orgID, UserID: userID}, models.ProviderName(provider))
	return nil
}

// mirrorDisable flips status to 'disabled' and invalidates the resolver
// cache for the affected (scope, provider) key. Same RETURNING trick as
// mirrorDelete keeps invalidation precise.
func (s *CodingCredentialStore) mirrorDisable(ctx context.Context, id uuid.UUID) error {
	var orgID uuid.UUID
	var userID *uuid.UUID
	var provider string
	err := s.db.QueryRow(ctx,
		`UPDATE coding_credentials SET status = 'disabled', updated_at = now()
		 WHERE id = @id RETURNING org_id, user_id, provider`,
		pgx.NamedArgs{"id": id},
	).Scan(&orgID, &userID, &provider)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("mirror disable: %w", err)
	}
	s.invalidate(models.Scope{OrgID: orgID, UserID: userID}, models.ProviderName(provider))
	return nil
}

// mirrorProviderForOrg decides which (provider, config) pair to write into the
// unified table for a legacy org_credentials row.
//
//   - openai_chatgpt → openai_subscription, config rewrapped as OpenAISubscriptionConfig
//   - anthropic + Subscription set → anthropic_subscription, config rewrapped
//     as AnthropicSubscriptionConfig (the APIKey on a dual-set row is dropped on
//     purpose: AnthropicConfig.Validate enforces APIKey and Subscription as
//     mutually exclusive, so a dual-set row is malformed; we mirror only the
//     subscription half because that's what the legacy claudecodeauth.Service
//     path was producing — the new shape is one method per row.)
//   - anthropic + APIKey set → anthropic, AnthropicConfig with Subscription field cleared
//   - other coding providers (openai, gemini, amp, pi, openrouter) → unchanged
//   - non-coding providers (github_app, sentry, linear, slack, notion, …) →
//     ok=false, mirror skipped
func mirrorProviderForOrg(provider models.ProviderName, cfg models.ProviderConfig) (models.ProviderName, models.ProviderConfig, bool) {
	switch provider {
	case models.ProviderOpenAIChatGPT:
		if c, ok := cfg.(models.OpenAIChatGPTConfig); ok {
			return models.ProviderOpenAISubscription, models.FromOpenAIChatGPTConfig(c), true
		}
		return "", nil, false
	case models.ProviderAnthropic:
		if c, ok := cfg.(models.AnthropicConfig); ok {
			if c.Subscription != nil {
				return models.ProviderAnthropicSubscription, models.FromAnthropicSubscription(*c.Subscription), true
			}
			// API-key row — strip Subscription pointer just to be safe and
			// emit an AnthropicConfig with only APIKey/BaseURL set.
			clean := models.AnthropicConfig{APIKey: c.APIKey, BaseURL: c.BaseURL}
			return models.ProviderAnthropic, clean, true
		}
		return "", nil, false
	case models.ProviderOpenAI, models.ProviderGemini, models.ProviderAmp, models.ProviderPi, models.ProviderOpenRouter:
		return provider, cfg, true
	default:
		return "", nil, false
	}
}

// mirrorProviderForUser is the user_credentials variant. It does not handle
// the OpenAI ChatGPT / Anthropic subscription split because the legacy
// user_credentials table never carried subscription rows — only API keys.
func mirrorProviderForUser(provider models.ProviderName, cfg models.ProviderConfig) (models.ProviderName, models.ProviderConfig, bool) {
	switch provider {
	case models.ProviderAnthropic, models.ProviderOpenAI, models.ProviderGemini,
		models.ProviderAmp, models.ProviderPi, models.ProviderOpenRouter:
		return provider, cfg, true
	default:
		return "", nil, false
	}
}

// noopMirror satisfies CodingCredentialMirror without writing anything. Used
// by tests that don't exercise the dual-write path and by main.go before
// SetCodingMirror is called.
type noopMirror struct{}

func (noopMirror) MirrorOrgCredential(context.Context, models.OrgCredential, models.ProviderConfig) error {
	return nil
}
func (noopMirror) MirrorOrgCredentialDelete(context.Context, uuid.UUID) error  { return nil }
func (noopMirror) MirrorOrgCredentialDisable(context.Context, uuid.UUID) error { return nil }
func (noopMirror) MirrorUserCredential(context.Context, models.UserCredential, models.ProviderConfig) error {
	return nil
}
func (noopMirror) MirrorUserCredentialDelete(context.Context, uuid.UUID) error  { return nil }
func (noopMirror) MirrorUserCredentialDisable(context.Context, uuid.UUID) error { return nil }

// NoopMirror returns the no-op mirror.
func NoopMirror() CodingCredentialMirror { return noopMirror{} }

// jsonDecodeProvider is the helper the legacy stores call to materialise a
// typed ProviderConfig from already-decrypted JSON. Kept here so the legacy
// stores don't have to reach into models.ParseProviderConfig directly when
// preparing a mirror write.
func jsonDecodeProvider(provider models.ProviderName, plaintext []byte) (models.ProviderConfig, error) {
	cfg, err := models.ParseProviderConfig(provider, plaintext)
	if err != nil {
		return nil, fmt.Errorf("decode %s mirror: %w", provider, err)
	}
	return cfg, nil
}
