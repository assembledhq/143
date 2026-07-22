package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	codeReviewGitHubRateReservation = 100
	githubRateRecoveryPercent       = 10
	githubRateMinimumRecovery       = 500
	githubRateBootstrapRetry        = 15 * time.Second
	githubRateBootstrapLease        = 2 * time.Minute
	githubRateObservationMaxAge     = 2 * time.Minute
)

type GitHubRateLimitStore struct {
	db DBTX
}

func NewGitHubRateLimitStore(db DBTX) *GitHubRateLimitStore {
	return &GitHubRateLimitStore{db: db}
}

// Observe records a quota shared by every 143 org linked to the installation.
// lint:allow-no-orgid reason="GitHub API quotas are global per App installation, not per linked 143 organization"
func (s *GitHubRateLimitStore) Observe(ctx context.Context, observation models.GitHubRateLimitObservation) error {
	if observation.InstallationID <= 0 {
		return fmt.Errorf("GitHub installation ID must be positive")
	}
	if err := observation.Resource.Validate(); err != nil {
		return err
	}
	if observation.ObservedAt.IsZero() {
		observation.ObservedAt = time.Now().UTC()
	}
	if err := validateGitHubRateLimitObservation(observation); err != nil {
		return err
	}
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return fmt.Errorf("GitHub rate-limit observation requires transaction support")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin GitHub rate-limit observation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, githubRateLimitLockKey(observation.InstallationID)); err != nil {
		return fmt.Errorf("lock GitHub installation rate limit: %w", err)
	}
	if err := ensureGitHubInstallationForRateLimit(ctx, tx, observation.InstallationID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO github_installation_rate_limits (
			installation_id, resource, limit_count, remaining_count, reset_at, blocked_until, observed_at
		) VALUES (
			@installation_id, @resource, @limit_count, @remaining_count, @reset_at, @blocked_until, @observed_at
		)
		ON CONFLICT (installation_id, resource) DO UPDATE
		SET limit_count = CASE
				WHEN EXCLUDED.reset_at IS NULL THEN github_installation_rate_limits.limit_count
				WHEN github_installation_rate_limits.reset_at IS NULL OR EXCLUDED.reset_at > github_installation_rate_limits.reset_at THEN EXCLUDED.limit_count
				WHEN EXCLUDED.reset_at = github_installation_rate_limits.reset_at THEN GREATEST(github_installation_rate_limits.limit_count, EXCLUDED.limit_count)
				ELSE github_installation_rate_limits.limit_count
			END,
			remaining_count = CASE
				WHEN EXCLUDED.reset_at IS NULL THEN github_installation_rate_limits.remaining_count
				WHEN github_installation_rate_limits.reset_at IS NULL OR EXCLUDED.reset_at > github_installation_rate_limits.reset_at THEN EXCLUDED.remaining_count
				WHEN EXCLUDED.reset_at = github_installation_rate_limits.reset_at THEN LEAST(github_installation_rate_limits.remaining_count, EXCLUDED.remaining_count)
				ELSE github_installation_rate_limits.remaining_count
			END,
			reset_at = CASE
				WHEN EXCLUDED.reset_at IS NULL THEN github_installation_rate_limits.reset_at
				WHEN github_installation_rate_limits.reset_at IS NULL OR EXCLUDED.reset_at > github_installation_rate_limits.reset_at THEN EXCLUDED.reset_at
				ELSE github_installation_rate_limits.reset_at
			END,
			blocked_until = CASE
				WHEN EXCLUDED.blocked_until IS NULL THEN github_installation_rate_limits.blocked_until
				WHEN github_installation_rate_limits.blocked_until IS NULL THEN EXCLUDED.blocked_until
				ELSE GREATEST(github_installation_rate_limits.blocked_until, EXCLUDED.blocked_until)
			END,
			observed_at = CASE
				WHEN EXCLUDED.reset_at IS NULL THEN github_installation_rate_limits.observed_at
				WHEN github_installation_rate_limits.reset_at IS NULL OR EXCLUDED.reset_at >= github_installation_rate_limits.reset_at THEN GREATEST(github_installation_rate_limits.observed_at, EXCLUDED.observed_at)
				ELSE github_installation_rate_limits.observed_at
			END,
			bootstrap_metadata_id = CASE
				WHEN EXCLUDED.reset_at IS NOT NULL
				 AND (github_installation_rate_limits.reset_at IS NULL OR EXCLUDED.reset_at >= github_installation_rate_limits.reset_at) THEN NULL
				ELSE github_installation_rate_limits.bootstrap_metadata_id
			END,
			bootstrap_reserved_at = CASE
				WHEN EXCLUDED.reset_at IS NOT NULL
				 AND (github_installation_rate_limits.reset_at IS NULL OR EXCLUDED.reset_at >= github_installation_rate_limits.reset_at) THEN NULL
				ELSE github_installation_rate_limits.bootstrap_reserved_at
			END`, pgx.NamedArgs{
		"installation_id": observation.InstallationID,
		"resource":        observation.Resource,
		"limit_count":     observation.Limit,
		"remaining_count": observation.Remaining,
		"reset_at":        observation.ResetAt,
		"blocked_until":   observation.BlockedUntil,
		"observed_at":     observation.ObservedAt,
	})
	if err != nil {
		return fmt.Errorf("observe GitHub installation rate limit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit GitHub rate-limit observation: %w", err)
	}
	return nil
}

func validateGitHubRateLimitObservation(observation models.GitHubRateLimitObservation) error {
	quotaFields := 0
	if observation.Limit != nil {
		quotaFields++
	}
	if observation.Remaining != nil {
		quotaFields++
	}
	if observation.ResetAt != nil {
		quotaFields++
	}
	if quotaFields != 0 && quotaFields != 3 {
		return fmt.Errorf("GitHub rate-limit quota requires limit, remaining, and reset together")
	}
	if quotaFields == 3 && (*observation.Limit <= 0 || *observation.Remaining < 0 || *observation.Remaining > *observation.Limit) {
		return fmt.Errorf("invalid GitHub rate-limit counts: remaining=%d limit=%d", *observation.Remaining, *observation.Limit)
	}
	if quotaFields == 0 && observation.BlockedUntil == nil {
		return fmt.Errorf("GitHub rate-limit observation has no quota or block window")
	}
	return nil
}

type githubRateLimitState struct {
	Limit        *int
	Remaining    *int
	ResetAt      *time.Time
	BlockedUntil *time.Time
	ObservedAt   *time.Time
	BootstrapAt  *time.Time
}

// ReserveCodeReview serializes installation-wide admission and creates an
// idempotent reservation for a queued review. Existing reservations retain
// their units but must still honor a newly observed installation-wide block.
func (s *GitHubRateLimitStore) ReserveCodeReview(ctx context.Context, orgID uuid.UUID, installationID int64, metadataID uuid.UUID, now time.Time) (models.GitHubRateLimitDecision, error) {
	if orgID == uuid.Nil || installationID <= 0 || metadataID == uuid.Nil {
		return models.GitHubRateLimitDecision{}, fmt.Errorf("org ID, installation ID, and code review metadata ID are required")
	}
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return models.GitHubRateLimitDecision{}, fmt.Errorf("GitHub rate-limit admission requires transaction support")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return models.GitHubRateLimitDecision{}, fmt.Errorf("begin GitHub rate-limit admission: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, githubRateLimitLockKey(installationID)); err != nil {
		return models.GitHubRateLimitDecision{}, fmt.Errorf("lock GitHub installation rate limit: %w", err)
	}
	if err := ensureGitHubInstallationForRateLimit(ctx, tx, installationID); err != nil {
		return models.GitHubRateLimitDecision{}, err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE github_installation_rate_reservations reservation
		SET released_at = @released_at
		FROM code_review_session_metadata metadata
		WHERE reservation.installation_id = @installation_id
		  AND reservation.released_at IS NULL
		  AND metadata.org_id = reservation.org_id
		  AND metadata.id = reservation.code_review_metadata_id
		  AND metadata.status IN ('completed', 'failed', 'stale', 'cancelled')`, pgx.NamedArgs{
		"installation_id": installationID,
		"released_at":     now,
	}); err != nil {
		return models.GitHubRateLimitDecision{}, fmt.Errorf("release terminal GitHub rate-limit reservations: %w", err)
	}

	var existingUnits int
	err = tx.QueryRow(ctx, `
		SELECT reserved_units
		FROM github_installation_rate_reservations
		WHERE org_id = @org_id
		  AND installation_id = @installation_id
		  AND code_review_metadata_id = @metadata_id
		  AND resource = @resource
		  AND released_at IS NULL`, pgx.NamedArgs{
		"org_id":          orgID,
		"installation_id": installationID,
		"metadata_id":     metadataID,
		"resource":        models.GitHubRateLimitResourceCore,
	}).Scan(&existingUnits)
	existingReservation := err == nil
	if !errors.Is(err, pgx.ErrNoRows) {
		if !existingReservation {
			return models.GitHubRateLimitDecision{}, fmt.Errorf("query existing GitHub rate-limit reservation: %w", err)
		}
	}

	var activeReserved int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(reserved_units), 0)
		FROM github_installation_rate_reservations
		WHERE installation_id = @installation_id
		  AND resource = @resource
		  AND released_at IS NULL`, pgx.NamedArgs{
		"installation_id": installationID,
		"resource":        models.GitHubRateLimitResourceCore,
	}).Scan(&activeReserved); err != nil {
		return models.GitHubRateLimitDecision{}, fmt.Errorf("sum active GitHub rate-limit reservations: %w", err)
	}

	state, err := loadGitHubRateLimitState(ctx, tx, installationID)
	if err != nil {
		return models.GitHubRateLimitDecision{}, err
	}

	decision := decideGitHubCodeReviewAdmission(state, activeReserved, existingReservation, metadataID, now)
	if decision.RefreshRequired {
		if _, err := tx.Exec(ctx, `
			INSERT INTO github_installation_rate_limits (
				installation_id, resource, observed_at, bootstrap_metadata_id, bootstrap_reserved_at
			) VALUES (
				@installation_id, @resource, @observed_at, @metadata_id, @bootstrap_reserved_at
			)
			ON CONFLICT (installation_id, resource) DO UPDATE
			SET bootstrap_metadata_id = EXCLUDED.bootstrap_metadata_id,
				bootstrap_reserved_at = EXCLUDED.bootstrap_reserved_at`, pgx.NamedArgs{
			"installation_id":       installationID,
			"resource":              models.GitHubRateLimitResourceCore,
			"observed_at":           now,
			"metadata_id":           metadataID,
			"bootstrap_reserved_at": now,
		}); err != nil {
			return models.GitHubRateLimitDecision{}, fmt.Errorf("claim GitHub rate-limit bootstrap lease: %w", err)
		}
	}
	if decision.Allowed && !existingReservation {
		if _, err := tx.Exec(ctx, `
			INSERT INTO github_installation_rate_reservations (
				org_id, installation_id, code_review_metadata_id, resource, reserved_units
			) VALUES (
				@org_id, @installation_id, @metadata_id, @resource, @reserved_units
			)`, pgx.NamedArgs{
			"org_id":          orgID,
			"installation_id": installationID,
			"metadata_id":     metadataID,
			"resource":        models.GitHubRateLimitResourceCore,
			"reserved_units":  codeReviewGitHubRateReservation,
		}); err != nil {
			return models.GitHubRateLimitDecision{}, fmt.Errorf("insert GitHub rate-limit reservation: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return models.GitHubRateLimitDecision{}, fmt.Errorf("commit GitHub rate-limit admission: %w", err)
	}
	return decision, nil
}

// CheckCodeReviewBlock checks only installation-wide secondary-limit state.
// Admitted reviews use this on retries so they can consume recovery capacity
// without making requests while GitHub has told the installation to stop.
// lint:allow-no-orgid reason="GitHub API blocks are global per App installation, not per linked 143 organization"
func (s *GitHubRateLimitStore) CheckCodeReviewBlock(ctx context.Context, installationID int64, now time.Time) (models.GitHubRateLimitDecision, error) {
	if installationID <= 0 {
		return models.GitHubRateLimitDecision{}, fmt.Errorf("GitHub installation ID must be positive")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	var blockedUntil sql.NullTime
	if err := s.db.QueryRow(ctx, `
		SELECT MAX(blocked_until)
		FROM github_installation_rate_limits
		WHERE installation_id = @installation_id`, pgx.NamedArgs{
		"installation_id": installationID,
	}).Scan(&blockedUntil); err != nil {
		return models.GitHubRateLimitDecision{}, fmt.Errorf("check GitHub installation rate-limit block: %w", err)
	}

	decision := models.GitHubRateLimitDecision{Allowed: true}
	if blockedUntil.Valid && blockedUntil.Time.After(now) {
		decision.Allowed = false
		decision.BlockedUntil = blockedUntil.Time
		decision.RetryAfter = blockedUntil.Time.Sub(now)
	}
	return decision, nil
}

func loadGitHubRateLimitState(ctx context.Context, tx pgx.Tx, installationID int64) (githubRateLimitState, error) {
	rows, err := tx.Query(ctx, `
		SELECT resource, limit_count, remaining_count, reset_at, blocked_until,
		       observed_at, bootstrap_reserved_at
		FROM github_installation_rate_limits
		WHERE installation_id = @installation_id
		FOR UPDATE`, pgx.NamedArgs{"installation_id": installationID})
	if err != nil {
		return githubRateLimitState{}, fmt.Errorf("load GitHub installation rate limits: %w", err)
	}
	defer rows.Close()
	state := githubRateLimitState{}
	for rows.Next() {
		var resource models.GitHubRateLimitResource
		var limitCount, remainingCount sql.NullInt64
		var resetAt, blockedUntil, observedAt, bootstrapAt sql.NullTime
		if err := rows.Scan(&resource, &limitCount, &remainingCount, &resetAt, &blockedUntil, &observedAt, &bootstrapAt); err != nil {
			return githubRateLimitState{}, fmt.Errorf("scan GitHub installation rate limit: %w", err)
		}
		if blockedUntil.Valid && (state.BlockedUntil == nil || blockedUntil.Time.After(*state.BlockedUntil)) {
			value := blockedUntil.Time
			state.BlockedUntil = &value
		}
		if resource != models.GitHubRateLimitResourceCore {
			continue
		}
		if limitCount.Valid {
			value := int(limitCount.Int64)
			state.Limit = &value
		}
		if remainingCount.Valid {
			value := int(remainingCount.Int64)
			state.Remaining = &value
		}
		if resetAt.Valid {
			value := resetAt.Time
			state.ResetAt = &value
		}
		if observedAt.Valid {
			value := observedAt.Time
			state.ObservedAt = &value
		}
		if bootstrapAt.Valid {
			value := bootstrapAt.Time
			state.BootstrapAt = &value
		}
	}
	if err := rows.Err(); err != nil {
		return githubRateLimitState{}, fmt.Errorf("iterate GitHub installation rate limits: %w", err)
	}
	return state, nil
}

func decideGitHubCodeReviewAdmission(state githubRateLimitState, activeReserved int, existingReservation bool, metadataID uuid.UUID, now time.Time) models.GitHubRateLimitDecision {
	decision := models.GitHubRateLimitDecision{ActiveReserved: activeReserved, ExistingReservation: existingReservation, MetadataID: metadataID}
	if state.BlockedUntil != nil && state.BlockedUntil.After(now) {
		decision.BlockedUntil = *state.BlockedUntil
		decision.RetryAfter = state.BlockedUntil.Sub(now)
		if state.Limit != nil && state.Remaining != nil && state.ResetAt != nil && state.ResetAt.After(now) {
			decision.Known = true
			decision.Limit = *state.Limit
			decision.Remaining = *state.Remaining
			decision.ResetAt = *state.ResetAt
			decision.RecoveryReserve = githubRateRecoveryReserve(*state.Limit)
		}
		return decision
	}
	quotaFresh := state.Limit != nil && state.Remaining != nil && state.ResetAt != nil && state.ResetAt.After(now) &&
		state.ObservedAt != nil && state.ObservedAt.After(now.Add(-githubRateObservationMaxAge))
	if existingReservation {
		decision.Allowed = true
		if quotaFresh {
			populateKnownGitHubRateLimitDecision(&decision, state)
		}
		return decision
	}
	if !quotaFresh {
		decision.Bootstrap = true
		decision.RefreshRequired = state.BootstrapAt == nil || !state.BootstrapAt.Add(githubRateBootstrapLease).After(now)
		if !decision.RefreshRequired {
			leaseRemaining := state.BootstrapAt.Add(githubRateBootstrapLease).Sub(now)
			decision.RetryAfter = min(leaseRemaining, githubRateBootstrapRetry)
		}
		return decision
	}

	populateKnownGitHubRateLimitDecision(&decision, state)
	decision.Allowed = decision.Remaining-activeReserved-codeReviewGitHubRateReservation >= decision.RecoveryReserve
	if !decision.Allowed {
		decision.RetryAfter = decision.ResetAt.Sub(now)
	}
	return decision
}

func populateKnownGitHubRateLimitDecision(decision *models.GitHubRateLimitDecision, state githubRateLimitState) {
	decision.Known = true
	decision.Limit = *state.Limit
	decision.Remaining = *state.Remaining
	decision.ResetAt = *state.ResetAt
	decision.RecoveryReserve = githubRateRecoveryReserve(*state.Limit)
}

func githubRateLimitLockKey(installationID int64) string {
	return fmt.Sprintf("github_rate_limit:%d", installationID)
}

func ensureGitHubInstallationForRateLimit(ctx context.Context, tx pgx.Tx, installationID int64) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO github_installations (installation_id, account_id, account_login, status)
		VALUES (@installation_id, 0, @account_login, 'active')
		ON CONFLICT (installation_id) DO NOTHING`, pgx.NamedArgs{
		"installation_id": installationID,
		"account_login":   fmt.Sprintf("installation-%d", installationID),
	}); err != nil {
		return fmt.Errorf("ensure GitHub installation for rate limiting: %w", err)
	}
	return nil
}

func githubRateRecoveryReserve(limit int) int {
	reserve := (limit*githubRateRecoveryPercent + 99) / 100
	if reserve < githubRateMinimumRecovery {
		return githubRateMinimumRecovery
	}
	return reserve
}
