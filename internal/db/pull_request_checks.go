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
	provider_sequence, provider_updated_at, created_at, updated_at`

func (s *PullRequestStore) UpsertCheckState(ctx context.Context, orgID uuid.UUID, state models.PullRequestCheckState) (bool, error) {
	if err := validatePullRequestCheckState(orgID, state.PullRequestID, state.HeadSHA, state); err != nil {
		return false, err
	}

	var applied bool
	err := s.db.QueryRow(ctx, `
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
	return applied, nil
}

func (s *PullRequestStore) ListCheckStates(ctx context.Context, orgID, pullRequestID uuid.UUID, headSHA string) ([]models.PullRequestCheckState, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+pullRequestCheckStateSelectColumns+`
		FROM pull_request_check_states
		WHERE org_id = @org_id
		  AND pull_request_id = @pull_request_id
		  AND head_sha = @head_sha
		ORDER BY lower(name), source, external_key`, pgx.NamedArgs{
		"org_id":          orgID,
		"pull_request_id": pullRequestID,
		"head_sha":        headSHA,
	})
	if err != nil {
		return nil, fmt.Errorf("list pull request check states: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PullRequestCheckState])
}

func (s *PullRequestStore) ReconcileCheckStates(ctx context.Context, orgID, pullRequestID uuid.UUID, headSHA string, states []models.PullRequestCheckState) error {
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
		if _, err := tx.Exec(ctx, `
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
				updated_at = now()
			WHERE pull_request_check_states.org_id = EXCLUDED.org_id
			  AND (
				pull_request_check_states.provider_sequence < EXCLUDED.provider_sequence
				OR (
					pull_request_check_states.provider_sequence = EXCLUDED.provider_sequence
					AND pull_request_check_states.provider_updated_at < EXCLUDED.provider_updated_at
				)
			  )`, pullRequestCheckStateArgs(orgID, state)); err != nil {
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
