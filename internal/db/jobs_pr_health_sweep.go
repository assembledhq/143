package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const PullRequestHealthSweepCheckpointVersion = 1

// ErrPullRequestHealthSweepLeaseLost means the caller no longer owns the
// running periodic job. A missing checkpoint is reported separately as nil.
var ErrPullRequestHealthSweepLeaseLost = errors.New("pull request health sweep job lease lost")

// PullRequestHealthSweepCheckpoint freezes one periodic admission's health
// selection across retries. An explicit empty remainder records completion.
// Callers must not mutate a checkpoint concurrently with a store operation.
type PullRequestHealthSweepCheckpoint struct {
	Version                 int         `json:"version"`
	RemainingPullRequestIDs []uuid.UUID `json:"remaining_pull_request_ids"`
	Population              int         `json:"population"`
	PopulationAvailable     bool        `json:"population_available"`
	CandidateLimit          int         `json:"candidate_limit"`
	SelectedCount           int         `json:"selected_count"`
}

// Validate checks the bounded, versioned selection before it can influence
// provider reads. A nil remainder must never stand in for completed work.
func (c PullRequestHealthSweepCheckpoint) Validate() error {
	if c.Version != PullRequestHealthSweepCheckpointVersion {
		return fmt.Errorf("unsupported health sweep checkpoint version")
	}
	if c.Population < 0 || c.Population > 1000 {
		return fmt.Errorf("health sweep checkpoint population out of bounds")
	}
	if c.CandidateLimit < 1 || c.CandidateLimit > 200 {
		return fmt.Errorf("health sweep checkpoint candidate limit out of bounds")
	}
	if c.SelectedCount < 0 || c.SelectedCount > c.CandidateLimit {
		return fmt.Errorf("health sweep checkpoint selection out of bounds")
	}
	if c.RemainingPullRequestIDs == nil || len(c.RemainingPullRequestIDs) > c.SelectedCount {
		return fmt.Errorf("health sweep checkpoint remainder out of bounds")
	}
	seen := make(map[uuid.UUID]struct{}, len(c.RemainingPullRequestIDs))
	for _, id := range c.RemainingPullRequestIDs {
		if id == uuid.Nil {
			return fmt.Errorf("health sweep checkpoint has an empty pull request identity")
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("health sweep checkpoint has duplicate pull request identities")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func decodePullRequestHealthSweepCheckpoint(raw []byte) (*PullRequestHealthSweepCheckpoint, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("decode health sweep checkpoint: %w", err)
	}
	for _, field := range []string{"version", "remaining_pull_request_ids", "population", "population_available", "candidate_limit", "selected_count"} {
		value, ok := fields[field]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, fmt.Errorf("health sweep checkpoint is missing a required field")
		}
	}
	var checkpoint PullRequestHealthSweepCheckpoint
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&checkpoint); err != nil {
		return nil, fmt.Errorf("decode health sweep checkpoint: %w", err)
	}
	if err := checkpoint.Validate(); err != nil {
		return nil, err
	}
	return &checkpoint, nil
}

// GetPullRequestHealthSweepCheckpointWithLease reads only the current tenant's
// running periodic job. nil means the payload has no health_sweep key; invalid
// or unsupported saved selections always fail closed. Fencing matches other
// queue writes: a replacement lock token, rather than elapsed wall time,
// revokes ownership.
func (s *JobStore) GetPullRequestHealthSweepCheckpointWithLease(ctx context.Context, orgID, jobID, lockToken uuid.UUID) (*PullRequestHealthSweepCheckpoint, error) {
	var payloadType string
	var present bool
	var raw []byte
	err := s.db.QueryRow(ctx, `
		SELECT jsonb_typeof(payload), payload ? 'health_sweep', payload->'health_sweep'
		FROM jobs
		WHERE org_id = @org_id AND id = @job_id
		  AND queue = 'default' AND job_type = 'reconcile_pull_request_state'
		  AND status = 'running' AND lock_token = @lock_token`, pgx.NamedArgs{
		"org_id": orgID, "job_id": jobID, "lock_token": lockToken,
	}).Scan(&payloadType, &present, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPullRequestHealthSweepLeaseLost
	}
	if err != nil {
		return nil, fmt.Errorf("get pull request health sweep checkpoint with lease: %w", err)
	}
	if payloadType != "object" {
		return nil, fmt.Errorf("health sweep job payload must be an object")
	}
	if !present {
		return nil, nil
	}
	return decodePullRequestHealthSweepCheckpoint(raw)
}

// SavePullRequestHealthSweepCheckpointWithLease changes only the checkpoint of
// the current tenant's running periodic job, preserving unrelated payload and
// queue state. The handler serializes writes under its current fencing token.
func (s *JobStore) SavePullRequestHealthSweepCheckpointWithLease(ctx context.Context, orgID, jobID, lockToken uuid.UUID, checkpoint PullRequestHealthSweepCheckpoint) error {
	if err := checkpoint.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("encode health sweep checkpoint: %w", err)
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE jobs
		SET payload = jsonb_set(payload, '{health_sweep}', @checkpoint::jsonb),
		    updated_at = now()
		WHERE org_id = @org_id AND id = @job_id
		  AND queue = 'default' AND job_type = 'reconcile_pull_request_state'
		  AND status = 'running' AND lock_token = @lock_token
		  AND jsonb_typeof(payload) = 'object'`, pgx.NamedArgs{
		"org_id": orgID, "job_id": jobID, "lock_token": lockToken, "checkpoint": raw,
	})
	if err != nil {
		return fmt.Errorf("save pull request health sweep checkpoint with lease: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrPullRequestHealthSweepLeaseLost
	}
	return nil
}
