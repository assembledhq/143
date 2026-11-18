package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

const pullRequestCheckStateFields = `org_id, pull_request_id, head_sha, source, external_key,
	name, category, status, provider, details_url, summary, provider_event_id,
	provider_sequence, provider_updated_at`

// A qualified check run can continue updating its same-provider legacy row.
// Compare both aliases during rolling deployments because older writers still
// use the unqualified name. A legacy caller keeps its original single key.
const pullRequestCheckStateKeyCTE = `selected_key AS (
	SELECT COALESCE((
		SELECT external_key
		FROM pull_request_check_states
		WHERE org_id = @org_id
		  AND pull_request_id = @pull_request_id
		  AND head_sha = @head_sha
		  AND source = @source
		  AND (external_key = @external_key OR (
			@source = 'check_run'
			AND @external_key LIKE ' check-run-v2:%'
			AND external_key = @legacy_external_key
			AND lower(btrim(provider)) = lower(btrim(@provider))
		  ))
		ORDER BY provider_sequence DESC, provider_updated_at DESC, projection_version DESC
		LIMIT 1
	), @external_key) AS external_key
)`

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
		WITH `+pullRequestCheckStateKeyCTE+`, applied AS (
			INSERT INTO pull_request_check_states (
				org_id, pull_request_id, head_sha, source, external_key, name,
				category, status, provider, details_url, summary, provider_event_id,
				provider_sequence, provider_updated_at
			)
			SELECT
				@org_id, @pull_request_id, @head_sha, @source, external_key, @name,
				@category, @status, @provider, @details_url, @summary, @provider_event_id,
				@provider_sequence, @provider_updated_at
			FROM selected_key
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
		SELECT EXISTS (SELECT 1 FROM applied) OR EXISTS (
			SELECT 1 FROM pull_request_check_states
			WHERE org_id=@org_id AND pull_request_id=@pull_request_id AND head_sha=@head_sha
			  AND source=@source AND external_key=(SELECT external_key FROM selected_key)
			  AND provider_sequence=@provider_sequence AND provider_updated_at=@provider_updated_at
			  AND name=@name AND category=@category AND status=@status AND provider=@provider
			  AND details_url IS NOT DISTINCT FROM @details_url AND summary=@summary
			  AND provider_event_id=@provider_event_id
		)`, pullRequestCheckStateArgs(orgID, state)).Scan(&applied)
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
		WITH aliases AS (
			SELECT `+pullRequestCheckStateFields+`, created_at, updated_at,
				MAX(projection_version) OVER logical_check AS alias_projection_version,
				ROW_NUMBER() OVER (logical_check
					ORDER BY provider_sequence DESC, provider_updated_at DESC, projection_version DESC
				) AS alias_rank
			FROM pull_request_check_states
			WHERE org_id = @org_id
			  AND pull_request_id = @pull_request_id
			  AND head_sha = @head_sha
			WINDOW logical_check AS (PARTITION BY source, lower(btrim(provider)),
				CASE WHEN source = 'check_run' THEN regexp_replace(external_key, '^ check-run-v2:[^:]+:', '')
					ELSE external_key END)
		)
		SELECT `+pullRequestCheckStateFields+`, alias_projection_version AS projection_version, created_at, updated_at
		FROM aliases
		WHERE alias_rank = 1 AND alias_projection_version > @after_version
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

// ReconcileCheckStates is called under ApplyHealthSync's pull request lock,
// which serializes alias selection against concurrent webhook upserts.
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
			WITH `+pullRequestCheckStateKeyCTE+`
			INSERT INTO pull_request_check_states (
				org_id, pull_request_id, head_sha, source, external_key, name,
				category, status, provider, details_url, summary, provider_event_id,
				provider_sequence, provider_updated_at, projection_version
			)
			SELECT
				@org_id, @pull_request_id, @head_sha, @source, external_key, @name,
				@category, @status, @provider, @details_url, @summary, @provider_event_id,
				@provider_sequence, @provider_updated_at, @projection_version
			FROM selected_key
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
		"legacy_external_key": strings.ToLower(strings.TrimSpace(state.Name)),
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
