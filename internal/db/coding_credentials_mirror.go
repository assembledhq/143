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
	MirrorOrgCredentialDelete(ctx context.Context, orgID, id uuid.UUID) error

	// MirrorOrgCredentialDisable flips status to 'disabled' on the unified row.
	MirrorOrgCredentialDisable(ctx context.Context, orgID, id uuid.UUID) error

	// MirrorUserCredential reflects a row from `user_credentials` into
	// `coding_credentials`. is_team_default = true → org-scoped row;
	// is_team_default = false → user-scoped row.
	MirrorUserCredential(ctx context.Context, row models.UserCredential, decryptedCfg models.ProviderConfig) error

	// MirrorUserCredentialDelete + MirrorUserCredentialDisable are the
	// counterparts for the user-credentials surface. Both also clean up any
	// team-default mirror row that may have been minted by the SQL migration
	// (which uses a fresh uuid, not the legacy id) — see § "Team-default
	// cascade" in the design doc. The orgID/userID/provider triple is enough
	// to compute the deterministic team-default label and reach that row.
	MirrorUserCredentialDelete(ctx context.Context, id, orgID, userID uuid.UUID, provider models.ProviderName) error
	MirrorUserCredentialDisable(ctx context.Context, id, orgID, userID uuid.UUID, provider models.ProviderName) error
}

// MirrorOrgCredential implements CodingCredentialMirror against the unified store.
//
// Failure counting: every public Mirror* method increments mirrorFailureTotal
// exactly once when the returned error is non-nil, via a deferred check on the
// named return value. This keeps `MirrorFailureCount` symmetric across the org
// and user surfaces — and it leaves the package-private helpers
// (mirrorDelete/mirrorDisable/upsertMirroredRow/deleteTeamDefaultMirror) free
// to return errors without each remembering to bump the counter.
func (s *CodingCredentialStore) MirrorOrgCredential(ctx context.Context, row models.OrgCredential, decryptedCfg models.ProviderConfig) (err error) {
	defer s.mirrorFailureOnError(&err)
	// Surface dual-set Anthropic rows so a malformed migration row doesn't
	// silently lose its API-key half. AnthropicConfig.Validate enforces
	// mutual exclusion at the write path, but rows present from earlier
	// schema generations may still be in the table.
	if row.Provider == models.ProviderAnthropic {
		if c, ok := decryptedCfg.(models.AnthropicConfig); ok && c.Subscription != nil && c.APIKey != "" {
			s.mirrorWarn("anthropic row id=%s has both APIKey and Subscription set; mirroring subscription only and dropping APIKey", row.ID)
			s.recordMirrorDrift()
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
		Status:         models.CodingCredentialRowStatus(row.Status),
		CreatedBy:      row.CreatedBy,
		LastVerifiedAt: row.LastVerifiedAt,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	})
}

// MirrorOrgCredentialDelete removes a mirrored org credential by legacy id.
//
// lint:allow-no-orgid reason="legacy id was already scope-checked by the calling OrgCredentialStore method; mirror loads scope back via RETURNING for cache invalidation"
func (s *CodingCredentialStore) MirrorOrgCredentialDelete(ctx context.Context, orgID, id uuid.UUID) (err error) {
	defer s.mirrorFailureOnError(&err)
	return s.mirrorDelete(ctx, orgID, id)
}

// MirrorOrgCredentialDisable disables a mirrored org credential by legacy id.
//
// lint:allow-no-orgid reason="legacy id was already scope-checked by the calling OrgCredentialStore method; mirror loads scope back via RETURNING for cache invalidation"
func (s *CodingCredentialStore) MirrorOrgCredentialDisable(ctx context.Context, orgID, id uuid.UUID) (err error) {
	defer s.mirrorFailureOnError(&err)
	return s.mirrorDisable(ctx, orgID, id)
}

func (s *CodingCredentialStore) MirrorUserCredential(ctx context.Context, row models.UserCredential, decryptedCfg models.ProviderConfig) (err error) {
	defer s.mirrorFailureOnError(&err)
	provider, cfg, ok := mirrorProviderForUser(row.Provider, decryptedCfg)
	if !ok {
		return nil // not a coding provider
	}
	encrypted, err := s.marshalAndEncrypt(cfg)
	if err != nil {
		return fmt.Errorf("mirror user credential: %w", err)
	}

	// If the legacy row is no longer team_default, clear any prior
	// team-default mirror row at the natural key. The SQL data-copy migration
	// mints fresh uuids for team-default rows, so an id-keyed disable/upsert
	// won't reach them — without this sweep, a user toggling team-default
	// on then off (or disabling the legacy row) leaves an orphan org-scoped
	// row in coding_credentials.
	if !row.IsTeamDefault {
		if err := s.deleteTeamDefaultMirror(ctx, row.OrgID, row.UserID, provider); err != nil {
			return fmt.Errorf("clear stale team default mirror: %w", err)
		}
	}

	// is_team_default → org-scoped row (user_id = NULL). The label
	// disambiguates the team-default row from a real org-scoped credential at
	// the same provider AND must match the migration SQL's label exactly so
	// the natural-key conflict path (org_id, user_id, provider, label) lands
	// on the same row instead of producing a duplicate. See migration step 3
	// in 000111_copy_coding_credentials.up.sql.
	//
	// `originUserID` carries the marker column value: non-nil for team-default
	// rows, nil for personal rows. Stamping it here keeps the mirror's row in
	// lockstep with what the migration writes so the down-migration's marker
	// check and the deleteTeamDefaultMirror cleanup both work without
	// label-string heuristics.
	var userID *uuid.UUID
	var originUserID *uuid.UUID
	label := ""
	if row.IsTeamDefault {
		userID = nil
		label = teamDefaultMirrorLabel(row.UserID)
		uid := row.UserID
		originUserID = &uid
	} else {
		uid := row.UserID
		userID = &uid
	}

	priority := 1 // personal user_credentials have no priority — anchor to 1.
	if row.IsTeamDefault {
		priority = teamDefaultMirrorPriority
	}

	if err := s.upsertMirroredRow(ctx, mirroredRow{
		ID:                      row.ID,
		OrgID:                   row.OrgID,
		UserID:                  userID,
		Provider:                provider,
		Label:                   label,
		EncryptedCfg:            encrypted,
		Priority:                priority,
		Status:                  models.CodingCredentialRowStatus(row.Status),
		CreatedBy:               &row.UserID,
		LastVerifiedAt:          row.LastVerifiedAt,
		CreatedAt:               row.CreatedAt,
		UpdatedAt:               row.UpdatedAt,
		TeamDefaultOriginUserID: originUserID,
	}); err != nil {
		return err
	}

	// On a personal→team-default flip, the mirror inserts a new config version
	// with user_id=NULL. upsertMirroredRow invalidates the new (org) scope, but the
	// originating user's personal-scope cache may still hold the row's old
	// representation as a personal entry. Wipe the personal scope too so the
	// next resolver call re-fetches and sees the row in the org tail instead
	// of stuck in the personal half.
	if row.IsTeamDefault {
		uid := row.UserID
		s.invalidate(models.Scope{OrgID: row.OrgID, UserID: &uid}, provider)
	}
	return nil
}

// MirrorUserCredentialDelete removes a mirrored user credential by legacy id
// AND any team-default mirror row whose label encodes the same originating
// user. The migration SQL mints fresh uuids for team-default rows, so the
// id-keyed delete alone leaves them orphaned — see § "Team-default cascade"
// in the design doc.
//
// Both deletes run in the same transaction so a partial failure cannot leave
// the legacy id-keyed row gone but the team-default cascade row still
// present (or vice versa). Resolver-cache invalidation runs only after the
// transaction commits — a rolled-back tx must not poison the cache.
func (s *CodingCredentialStore) MirrorUserCredentialDelete(ctx context.Context, id, orgID, userID uuid.UUID, provider models.ProviderName) (err error) {
	defer s.mirrorFailureOnError(&err)
	return s.runMirrorCascadeTx(ctx, func(dbtx DBTX) (mirrorInvalidations, error) {
		idInval, err := s.mirrorDeleteTx(ctx, dbtx, orgID, id)
		if err != nil {
			return mirrorInvalidations{}, err
		}
		teamInval, err := s.deleteTeamDefaultMirrorTx(ctx, dbtx, orgID, userID, provider)
		if err != nil {
			return mirrorInvalidations{}, fmt.Errorf("delete team default mirror: %w", err)
		}
		return mirrorInvalidations{idInval, teamInval}, nil
	})
}

// MirrorUserCredentialDisable disables a mirrored user credential by legacy id
// AND removes any team-default mirror row that may have been minted by the SQL
// migration. The team-default cleanup is a hard delete (not a status flip)
// because a stale "Team default (migrated…)" row in 'disabled' state is just
// noise — the legacy row is gone, the row should not be visible at all.
//
// Both writes run in the same transaction; cache invalidation is deferred
// until commit. Same atomicity rationale as MirrorUserCredentialDelete.
func (s *CodingCredentialStore) MirrorUserCredentialDisable(ctx context.Context, id, orgID, userID uuid.UUID, provider models.ProviderName) (err error) {
	defer s.mirrorFailureOnError(&err)
	return s.runMirrorCascadeTx(ctx, func(dbtx DBTX) (mirrorInvalidations, error) {
		idInval, err := s.mirrorDisableTx(ctx, dbtx, orgID, id)
		if err != nil {
			return mirrorInvalidations{}, err
		}
		teamInval, err := s.deleteTeamDefaultMirrorTx(ctx, dbtx, orgID, userID, provider)
		if err != nil {
			return mirrorInvalidations{}, fmt.Errorf("disable cascade: clear team default mirror: %w", err)
		}
		return mirrorInvalidations{idInval, teamInval}, nil
	})
}

// mirrorInvalidation describes one cache key to wipe after a cascade tx
// commits. nil means "no row was touched"; the caller skips invalidation.
type mirrorInvalidation struct {
	orgID    uuid.UUID
	userID   *uuid.UUID
	provider models.ProviderName
}

type mirrorInvalidations [2]*mirrorInvalidation

// runMirrorCascadeTx runs `body` inside a tx and only triggers cache
// invalidations after a successful commit. If the store's DBTX backing does
// not support transactions (e.g. some test fixtures), falls back to running
// `body` against the raw DBTX and invalidating immediately — the test path
// has no rollback to worry about. pgx.Tx satisfies DBTX, so the body sees a
// consistent interface either way.
func (s *CodingCredentialStore) runMirrorCascadeTx(ctx context.Context, body func(dbtx DBTX) (mirrorInvalidations, error)) error {
	starter, ok := s.db.(TxStarter)
	if !ok {
		invals, err := body(s.db)
		if err != nil {
			return err
		}
		s.applyMirrorInvalidations(invals)
		return nil
	}
	tx, err := starter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin mirror cascade tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	invals, err := body(tx)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit mirror cascade tx: %w", err)
	}
	s.applyMirrorInvalidations(invals)
	return nil
}

func (s *CodingCredentialStore) applyMirrorInvalidations(invals mirrorInvalidations) {
	for _, inv := range invals {
		if inv == nil {
			continue
		}
		s.invalidate(models.Scope{OrgID: inv.orgID, UserID: inv.userID}, inv.provider)
	}
}

// mirrorFailureOnError increments the mirror-failure counter when the supplied
// error pointer references a non-nil error at function exit. Wired via defer
// at the top of every public Mirror* method so each call records exactly one
// failure on any error path — moving the count into a single named-return
// hook avoids the historical bug where one branch (e.g. marshalAndEncrypt
// failure inside MirrorOrgCredential) silently skipped the increment.
func (s *CodingCredentialStore) mirrorFailureOnError(err *error) {
	if err == nil || *err == nil {
		return
	}
	s.recordMirrorFailure()
}

// deleteTeamDefaultMirror drops any org-scoped mirror row that was minted by
// the SQL data-copy migration or by an earlier mirror call to back the legacy
// (orgID, originalUserID, provider) team-default credential. Keyed on the
// `team_default_origin_user_id` marker column so a renamed label can't shield
// the row from cleanup. Idempotent — no-op when no such row exists.
// Invalidates the resolver cache for the affected (org, provider) so a stale
// read does not survive the cleanup.
func (s *CodingCredentialStore) deleteTeamDefaultMirror(ctx context.Context, orgID, originalUserID uuid.UUID, provider models.ProviderName) error {
	inv, err := s.deleteTeamDefaultMirrorTx(ctx, s.db, orgID, originalUserID, provider)
	if err != nil {
		return err
	}
	if inv != nil {
		s.invalidate(models.Scope{OrgID: inv.orgID, UserID: inv.userID}, inv.provider)
	}
	return nil
}

// deleteTeamDefaultMirrorTx runs the cleanup against the provided DBTX (a tx
// or the pool) and returns the invalidation key the caller should apply on
// commit. Returns nil when no row matched.
func (s *CodingCredentialStore) deleteTeamDefaultMirrorTx(ctx context.Context, dbtx DBTX, orgID, originalUserID uuid.UUID, provider models.ProviderName) (*mirrorInvalidation, error) {
	var deletedOrgID uuid.UUID
	var userID *uuid.UUID
	var deletedProvider string
	err := dbtx.QueryRow(ctx,
		`WITH target AS (
			SELECT DISTINCT id, org_id, user_id, provider
			FROM coding_credentials
			WHERE org_id = @org_id
			  AND user_id IS NULL
			  AND provider = @provider
			  AND team_default_origin_user_id = @origin_user_id
			  AND active = true
		),
		deactivated_runtime AS (
			UPDATE coding_credential_runtime_state rt
			SET active = false
			FROM target
			WHERE rt.credential_id = target.id AND rt.active = true
			RETURNING rt.credential_id
		),
		deactivated_config AS (
			UPDATE coding_credentials cc
			SET active = false, updated_at = now()
			FROM target
			WHERE cc.id = target.id AND cc.active = true
			RETURNING cc.id
		)
		SELECT org_id, user_id, provider FROM target LIMIT 1`,
		pgx.NamedArgs{"org_id": orgID, "provider": string(provider), "origin_user_id": originalUserID},
	).Scan(&deletedOrgID, &userID, &deletedProvider)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("delete team default mirror: %w", err)
	}
	return &mirrorInvalidation{orgID: deletedOrgID, userID: userID, provider: models.ProviderName(deletedProvider)}, nil
}

// teamDefaultMirrorPriority parks team-default mirrors at the bottom of the
// org stack so explicit org rows still win the resolver. Tunable; mirrors a
// rare path so contention with normal CRUD is negligible.
const teamDefaultMirrorPriority = 9000

// teamDefaultMirrorLabelPrefix and the format produced by
// teamDefaultMirrorLabel must match the SQL data-copy migration in
// migrations/000111_copy_coding_credentials.up.sql exactly. The format is
// load-bearing for the natural-key conflict path
// `(org_id, user_id, provider, label)` — both writers must produce the same
// label so a row written by the migration and a row written by the mirror
// land on the same slot. The marker column `team_default_origin_user_id`
// guards row identity for cleanup; the label is for human readability and
// natural-key alignment with the migration.
const teamDefaultMirrorLabelPrefix = "Team default (migrated from "

func teamDefaultMirrorLabel(originUserID uuid.UUID) string {
	return teamDefaultMirrorLabelPrefix + originUserID.String() + ")"
}

type mirroredRow struct {
	ID             uuid.UUID
	OrgID          uuid.UUID
	UserID         *uuid.UUID
	Provider       models.ProviderName
	Label          string
	EncryptedCfg   []byte
	Priority       int
	Status         models.CodingCredentialRowStatus
	CreatedBy      *uuid.UUID
	LastVerifiedAt *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	// TeamDefaultOriginUserID stamps the marker column for org-scoped rows
	// minted by a personal team-default credential. Non-nil only when the
	// caller is mirroring a `is_team_default = true` user_credentials row.
	TeamDefaultOriginUserID *uuid.UUID
}

// upsertMirroredRow writes a new active config/runtime version while preserving
// the legacy id as the logical credential id. The unique (org_id, user_id,
// provider, label) index might already have a separate logical credential at
// the natural key — when that happens we update that natural-key row instead of
// the legacy-id row, leaving at most one row per (scope, provider, label).
//
// Provider IS included in the SET list. AnthropicConfig.Validate enforces
// APIKey/Subscription mutual exclusion, but a row that flips between the two
// (e.g. user removes a subscription and saves an API key under the same id)
// must rewrite the unified row's provider too — otherwise the config blob
// (now an AnthropicConfig) would be stored under provider='anthropic_subscription'
// and ParseCodingProviderConfig would fail at decrypt time.
func (s *CodingCredentialStore) upsertMirroredRow(ctx context.Context, row mirroredRow) error {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return fmt.Errorf("mirror upsert: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	newScope := models.Scope{OrgID: row.OrgID, UserID: row.UserID}
	var invalidations mirrorInvalidations

	current, _, err := s.fetchActiveConfigByIDAnyScopeTx(ctx, tx, row.OrgID, row.ID, true)
	if err != nil && !errors.Is(err, ErrCodingCredentialNotFound) {
		return fmt.Errorf("mirror fetch by id: %w", err)
	}
	if current != nil {
		invalidations[0] = &mirrorInvalidation{orgID: current.OrgID, userID: current.UserID, provider: current.Provider}
		if conflict, _, err := s.fetchActiveConfigByProviderLabelTx(ctx, tx, newScope, row.Provider, row.Label, true); err != nil && !errors.Is(err, ErrCodingCredentialNotFound) {
			return fmt.Errorf("mirror fetch natural key: %w", err)
		} else if conflict != nil && conflict.ID != current.ID {
			s.recordMirrorNaturalKeyFallback()
			inv, err := s.updateMirroredRowByNaturalKeyTx(ctx, tx, row)
			if err != nil {
				return err
			}
			invalidations[1] = inv
			if _, err := s.mirrorDeleteTx(ctx, tx, current.OrgID, current.ID); err != nil {
				return fmt.Errorf("mirror deactivate stale logical id: %w", err)
			}
		} else {
			next := row.configSnapshot(current.ID, current.VersionID, current.CreatedAt)
			if err := s.insertConfigVersionTx(ctx, tx, next); err != nil {
				return fmt.Errorf("mirror config version: %w", err)
			}
			if err := s.insertRuntimeVersionTx(ctx, tx, newScope, current.ID, row.runtimeSnapshot()); err != nil {
				return fmt.Errorf("mirror runtime version: %w", err)
			}
			invalidations[1] = &mirrorInvalidation{orgID: row.OrgID, userID: row.UserID, provider: row.Provider}
		}
	} else if inv, err := s.updateMirroredRowByNaturalKeyTx(ctx, tx, row); err == nil && inv != nil {
		s.recordMirrorNaturalKeyFallback()
		invalidations[0] = inv
	} else if err != nil && !errors.Is(err, ErrCodingCredentialNotFound) {
		return err
	} else {
		if err := s.insertInitialConfigVersionTx(ctx, tx, row.configSnapshot(row.ID, uuid.Nil, row.createdAtOrNow(s.clock))); err != nil {
			return fmt.Errorf("mirror insert config: %w", err)
		}
		if err := s.insertInitialRuntimeVersionTx(ctx, tx, newScope, row.ID, row.runtimeSnapshot()); err != nil {
			return fmt.Errorf("mirror insert runtime: %w", err)
		}
		invalidations[0] = &mirrorInvalidation{orgID: row.OrgID, userID: row.UserID, provider: row.Provider}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit mirror upsert: %w", err)
	}
	s.applyMirrorInvalidations(invalidations)
	return nil
}

func (row mirroredRow) createdAtOrNow(now func() time.Time) time.Time {
	if row.CreatedAt.IsZero() {
		return now()
	}
	return row.CreatedAt
}

func (row mirroredRow) configSnapshot(id, versionID uuid.UUID, createdAt time.Time) codingCredentialConfigSnapshot {
	return codingCredentialConfigSnapshot{
		ID:                      id,
		VersionID:               versionID,
		OrgID:                   row.OrgID,
		UserID:                  row.UserID,
		Provider:                row.Provider,
		Label:                   row.Label,
		Config:                  row.EncryptedCfg,
		Priority:                row.Priority,
		Status:                  row.Status,
		CreatedBy:               row.CreatedBy,
		TeamDefaultOriginUserID: row.TeamDefaultOriginUserID,
		CreatedAt:               createdAt,
	}
}

func (row mirroredRow) runtimeSnapshot() codingCredentialRuntimeSnapshot {
	return codingCredentialRuntimeSnapshot{
		Status:         row.Status,
		LastVerifiedAt: row.LastVerifiedAt,
	}
}

func (s *CodingCredentialStore) updateMirroredRowByNaturalKeyTx(ctx context.Context, tx pgx.Tx, row mirroredRow) (*mirrorInvalidation, error) {
	scope := models.Scope{OrgID: row.OrgID, UserID: row.UserID}
	current, _, err := s.fetchActiveConfigByProviderLabelTx(ctx, tx, scope, row.Provider, row.Label, true)
	if err != nil {
		return nil, fmt.Errorf("mirror update by natural key: %w", err)
	}
	if err := s.insertConfigVersionTx(ctx, tx, row.configSnapshot(current.ID, current.VersionID, current.CreatedAt)); err != nil {
		return nil, fmt.Errorf("mirror natural-key config version: %w", err)
	}
	if err := s.insertRuntimeVersionTx(ctx, tx, scope, current.ID, row.runtimeSnapshot()); err != nil {
		return nil, fmt.Errorf("mirror natural-key runtime version: %w", err)
	}
	return &mirrorInvalidation{orgID: row.OrgID, userID: row.UserID, provider: row.Provider}, nil
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
func (s *CodingCredentialStore) mirrorDelete(ctx context.Context, scopedOrgID, id uuid.UUID) error {
	inv, err := s.mirrorDeleteTx(ctx, s.db, scopedOrgID, id)
	if err != nil {
		return err
	}
	if inv != nil {
		s.invalidate(models.Scope{OrgID: inv.orgID, UserID: inv.userID}, inv.provider)
	}
	return nil
}

// mirrorDeleteTx runs the delete against an explicit DBTX (tx or pool) and
// returns the invalidation key the caller should apply on commit.
func (s *CodingCredentialStore) mirrorDeleteTx(ctx context.Context, dbtx DBTX, scopedOrgID, id uuid.UUID) (*mirrorInvalidation, error) {
	var orgID uuid.UUID
	var userID *uuid.UUID
	var provider string
	err := dbtx.QueryRow(ctx,
		`WITH target AS (
			SELECT DISTINCT id, org_id, user_id, provider
			FROM coding_credentials
			WHERE id = @id AND org_id = @org_id AND active = true
		),
		deactivated_runtime AS (
			UPDATE coding_credential_runtime_state rt
			SET active = false
			FROM target
			WHERE rt.credential_id = target.id AND rt.active = true
			RETURNING rt.credential_id
		),
		deactivated_config AS (
			UPDATE coding_credentials cc
			SET active = false, updated_at = now()
			FROM target
			WHERE cc.id = target.id AND cc.active = true
			RETURNING cc.id
		)
		SELECT org_id, user_id, provider FROM target LIMIT 1`,
		pgx.NamedArgs{"id": id, "org_id": scopedOrgID},
	).Scan(&orgID, &userID, &provider)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("mirror delete: %w", err)
	}
	return &mirrorInvalidation{orgID: orgID, userID: userID, provider: models.ProviderName(provider)}, nil
}

// mirrorDisable flips status to 'disabled' and invalidates the resolver
// cache for the affected (scope, provider) key. Same RETURNING trick as
// mirrorDelete keeps invalidation precise.
func (s *CodingCredentialStore) mirrorDisable(ctx context.Context, scopedOrgID, id uuid.UUID) error {
	inv, err := s.mirrorDisableTx(ctx, s.db, scopedOrgID, id)
	if err != nil {
		return err
	}
	if inv != nil {
		s.invalidate(models.Scope{OrgID: inv.orgID, UserID: inv.userID}, inv.provider)
	}
	return nil
}

// mirrorDisableTx is the tx-aware variant of mirrorDisable.
func (s *CodingCredentialStore) mirrorDisableTx(ctx context.Context, dbtx DBTX, scopedOrgID, id uuid.UUID) (*mirrorInvalidation, error) {
	var orgID uuid.UUID
	var userID *uuid.UUID
	var provider string
	err := dbtx.QueryRow(ctx,
		`WITH current AS (
			SELECT cc.id, cc.org_id, cc.user_id, cc.provider,
			       rt.last_verified_at, rt.rate_limited_until,
			       rt.rate_limited_observed_at, rt.rate_limit_message
			FROM coding_credentials cc
			JOIN coding_credential_runtime_state rt ON rt.credential_id = cc.id AND rt.active = true
			WHERE cc.id = @id AND cc.org_id = @org_id AND cc.active = true
		),
		deactivated AS (
			UPDATE coding_credential_runtime_state rt
			SET active = false
			FROM current
			WHERE rt.credential_id = current.id AND rt.active = true
			RETURNING current.id, current.org_id, current.user_id, current.provider,
			          current.last_verified_at, current.rate_limited_until,
			          current.rate_limited_observed_at, current.rate_limit_message
		),
		inserted AS (
			INSERT INTO coding_credential_runtime_state (
				credential_id, org_id, user_id, status, last_verified_at,
				rate_limited_until, rate_limited_observed_at, rate_limit_message, active
			)
			SELECT id, org_id, user_id, 'disabled', last_verified_at,
			       rate_limited_until, rate_limited_observed_at, rate_limit_message, true
			FROM deactivated
			RETURNING credential_id
		)
		SELECT org_id, user_id, provider FROM deactivated LIMIT 1`,
		pgx.NamedArgs{"id": id, "org_id": scopedOrgID},
	).Scan(&orgID, &userID, &provider)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("mirror disable: %w", err)
	}
	return &mirrorInvalidation{orgID: orgID, userID: userID, provider: models.ProviderName(provider)}, nil
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
func (noopMirror) MirrorOrgCredentialDelete(context.Context, uuid.UUID, uuid.UUID) error  { return nil }
func (noopMirror) MirrorOrgCredentialDisable(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (noopMirror) MirrorUserCredential(context.Context, models.UserCredential, models.ProviderConfig) error {
	return nil
}
func (noopMirror) MirrorUserCredentialDelete(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, models.ProviderName) error {
	return nil
}
func (noopMirror) MirrorUserCredentialDisable(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, models.ProviderName) error {
	return nil
}

// NoopMirror returns the no-op mirror.
func NoopMirror() CodingCredentialMirror { return noopMirror{} }

func isNoopCodingCredentialMirror(m CodingCredentialMirror) bool {
	if m == nil {
		return true
	}
	_, ok := m.(noopMirror)
	return ok
}

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
