package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

const pullRequestCheckStateSelectColumns = `org_id, pull_request_id, head_sha, source, external_key,
	name, category, status, provider, details_url, summary, provider_event_id,
	provider_sequence, provider_updated_at, projection_version, created_at, updated_at`

func (s *PullRequestStore) UpsertCheckState(ctx context.Context, orgID uuid.UUID, state models.PullRequestCheckState) (bool, error) {
	if err := validatePullRequestCheckState(orgID, state.PullRequestID, state.HeadSHA, state); err != nil {
		return false, err
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return false, fmt.Errorf("begin pull request check state upsert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockPullRequestCheckProjection(ctx, tx, orgID, state.PullRequestID); err != nil {
		return false, err
	}

	var applied bool
	err = tx.QueryRow(ctx, `
		WITH applied AS (
			INSERT INTO pull_request_check_states (
				org_id, pull_request_id, head_sha, source, external_key, name,
				category, status, provider, details_url, summary, provider_event_id,
				provider_sequence, provider_updated_at
			) VALUES (
				@org_id, @pull_request_id, @head_sha, @source, @external_key, @name,
				@category, @status, @provider, @details_url, @summary, @provider_event_id,
				@provider_sequence, @provider_updated_at
			)
			ON CONFLICT (pull_request_id, head_sha, source, external_key) DO UPDATE
			SET name = EXCLUDED.name,
				category = EXCLUDED.category,
				status = EXCLUDED.status,
				provider = EXCLUDED.provider,
				details_url = EXCLUDED.details_url,
				summary = EXCLUDED.summary,
				provider_event_id = EXCLUDED.provider_event_id,
				provider_sequence = EXCLUDED.provider_sequence,
				provider_updated_at = EXCLUDED.provider_updated_at,
				projection_version = nextval('pull_request_check_state_projection_version_seq'),
				updated_at = now()
			WHERE pull_request_check_states.org_id = EXCLUDED.org_id
			  AND (
				pull_request_check_states.provider_sequence < EXCLUDED.provider_sequence
				OR (
					pull_request_check_states.provider_sequence = EXCLUDED.provider_sequence
					AND pull_request_check_states.provider_updated_at < EXCLUDED.provider_updated_at
				)
			  )
			RETURNING 1
		)
		SELECT EXISTS (SELECT 1 FROM applied)`, pullRequestCheckStateArgs(orgID, state)).Scan(&applied)
	if err != nil {
		return false, fmt.Errorf("upsert pull request check state: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit pull request check state upsert: %w", err)
	}
	return applied, nil
}

// ReserveCheckStateVersion creates an ordering barrier for an authoritative
// health sync. Webhook writes committed after this call receive a greater
// version and can therefore be overlaid without relying on wall-clock time.
func (s *PullRequestStore) ReserveCheckStateVersion(ctx context.Context, orgID, pullRequestID uuid.UUID) (int64, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin pull request check state version reservation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockPullRequestCheckProjection(ctx, tx, orgID, pullRequestID); err != nil {
		return 0, err
	}

	var version int64
	err = tx.QueryRow(ctx, `SELECT nextval('pull_request_check_state_projection_version_seq')`).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("reserve pull request check state version: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit pull request check state version reservation: %w", err)
	}
	return version, nil
}

func lockPullRequestCheckProjection(ctx context.Context, tx pgx.Tx, orgID, pullRequestID uuid.UUID) error {
	var lockedID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT id
		FROM pull_requests
		WHERE org_id = @org_id AND id = @pull_request_id
		FOR UPDATE`, pgx.NamedArgs{
		"org_id":          orgID,
		"pull_request_id": pullRequestID,
	}).Scan(&lockedID); err != nil {
		return fmt.Errorf("lock pull request check projection: %w", err)
	}
	return nil
}

func (s *PullRequestStore) ListCheckStatesAfter(ctx context.Context, orgID, pullRequestID uuid.UUID, headSHA string, afterVersion int64) ([]models.PullRequestCheckState, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+pullRequestCheckStateSelectColumns+`
		FROM pull_request_check_states
		WHERE org_id = @org_id
		  AND pull_request_id = @pull_request_id
		  AND head_sha = @head_sha
		  AND projection_version > @after_version
		ORDER BY lower(name), source, external_key`, pgx.NamedArgs{
		"org_id":          orgID,
		"pull_request_id": pullRequestID,
		"head_sha":        headSHA,
		"after_version":   afterVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("list pull request check states after version: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PullRequestCheckState])
}

func (s *PullRequestStore) HasCheckStatesAfter(ctx context.Context, orgID, pullRequestID uuid.UUID, headSHA string, afterVersion int64) (bool, error) {
	var pending bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pull_request_check_states
			WHERE org_id = @org_id
			  AND pull_request_id = @pull_request_id
			  AND head_sha = @head_sha
			  AND projection_version > @after_version
		)`, pgx.NamedArgs{
		"org_id":          orgID,
		"pull_request_id": pullRequestID,
		"head_sha":        headSHA,
		"after_version":   afterVersion,
	}).Scan(&pending)
	if err != nil {
		return false, fmt.Errorf("check for unapplied pull request check states: %w", err)
	}
	return pending, nil
}

func (s *PullRequestStore) ReconcileCheckStates(ctx context.Context, orgID, pullRequestID uuid.UUID, headSHA string, checkStateVersion int64, states []models.PullRequestCheckState) error {
	for _, state := range states {
		if err := validatePullRequestCheckState(orgID, pullRequestID, headSHA, state); err != nil {
			return err
		}
	}

	tx, err := s.beginTx(ctx)
	if err != nil {
		return fmt.Errorf("begin pull request check state reconciliation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Old heads can never contribute to the live aggregate. Rows on the current
	// head are retained and ordered upserts are used below so a webhook that
	// races the GitHub read cannot be deleted or regressed by reconciliation.
	if _, err := tx.Exec(ctx, `
		DELETE FROM pull_request_check_states
		WHERE org_id = @org_id
		  AND pull_request_id = @pull_request_id
		  AND head_sha <> @head_sha`, pgx.NamedArgs{
		"org_id":          orgID,
		"pull_request_id": pullRequestID,
		"head_sha":        headSHA,
	}); err != nil {
		return fmt.Errorf("delete stale-head pull request check states: %w", err)
	}

	for _, state := range states {
		args := pullRequestCheckStateArgs(orgID, state)
		args["projection_version"] = checkStateVersion
		if _, err := tx.Exec(ctx, `
			INSERT INTO pull_request_check_states (
				org_id, pull_request_id, head_sha, source, external_key, name,
				category, status, provider, details_url, summary, provider_event_id,
				provider_sequence, provider_updated_at, projection_version
			) VALUES (
				@org_id, @pull_request_id, @head_sha, @source, @external_key, @name,
				@category, @status, @provider, @details_url, @summary, @provider_event_id,
				@provider_sequence, @provider_updated_at, @projection_version
			)
			ON CONFLICT (pull_request_id, head_sha, source, external_key) DO UPDATE
			SET name = EXCLUDED.name,
				category = EXCLUDED.category,
				status = EXCLUDED.status,
				provider = EXCLUDED.provider,
				details_url = EXCLUDED.details_url,
				summary = EXCLUDED.summary,
				provider_event_id = EXCLUDED.provider_event_id,
				provider_sequence = EXCLUDED.provider_sequence,
				provider_updated_at = EXCLUDED.provider_updated_at,
				projection_version = EXCLUDED.projection_version,
				updated_at = now()
			WHERE pull_request_check_states.org_id = EXCLUDED.org_id
			  AND (
				pull_request_check_states.provider_sequence < EXCLUDED.provider_sequence
				OR (
					pull_request_check_states.provider_sequence = EXCLUDED.provider_sequence
					AND pull_request_check_states.provider_updated_at < EXCLUDED.provider_updated_at
				)
			  )`, args); err != nil {
			return fmt.Errorf("insert reconciled pull request check state: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit pull request check state reconciliation: %w", err)
	}
	return nil
}

func validatePullRequestCheckState(orgID, pullRequestID uuid.UUID, headSHA string, state models.PullRequestCheckState) error {
	if orgID == uuid.Nil || state.OrgID != orgID {
		return fmt.Errorf("pull request check state org does not match orgID")
	}
	if pullRequestID == uuid.Nil || state.PullRequestID != pullRequestID {
		return fmt.Errorf("pull request check state pull request does not match pullRequestID")
	}
	if strings.TrimSpace(headSHA) == "" || state.HeadSHA != headSHA {
		return fmt.Errorf("pull request check state head SHA does not match headSHA")
	}
	if err := state.Source.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(state.ExternalKey) == "" || strings.TrimSpace(state.Name) == "" {
		return fmt.Errorf("pull request check state requires an external key and name")
	}
	if err := state.Category.Validate(); err != nil {
		return err
	}
	if err := state.Status.Validate(); err != nil {
		return err
	}
	if state.ProviderUpdatedAt.IsZero() {
		return fmt.Errorf("pull request check state requires provider updated time")
	}
	return nil
}

func pullRequestCheckStateArgs(orgID uuid.UUID, state models.PullRequestCheckState) pgx.NamedArgs {
	return pgx.NamedArgs{
		"org_id":              orgID,
		"pull_request_id":     state.PullRequestID,
		"head_sha":            state.HeadSHA,
		"source":              state.Source,
		"external_key":        state.ExternalKey,
		"name":                state.Name,
		"category":            state.Category,
		"status":              state.Status,
		"provider":            state.Provider,
		"details_url":         state.DetailsURL,
		"summary":             state.Summary,
		"provider_event_id":   state.ProviderEventID,
		"provider_sequence":   state.ProviderSequence,
		"provider_updated_at": state.ProviderUpdatedAt,
	}
}
