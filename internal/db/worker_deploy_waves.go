package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type WorkerDeployWave struct {
	ID            string          `db:"id" json:"id"`
	Status        string          `db:"status" json:"status"`
	Mode          string          `db:"mode" json:"mode"`
	BuildSHA      string          `db:"build_sha" json:"build_sha"`
	Region        string          `db:"region" json:"region"`
	Bucket        string          `db:"bucket" json:"bucket"`
	RequestedBy   string          `db:"requested_by" json:"requested_by"`
	Reason        string          `db:"reason" json:"reason"`
	MaxConcurrent int             `db:"max_concurrent" json:"max_concurrent"`
	CanaryCount   int             `db:"canary_count" json:"canary_count"`
	PauseReason   string          `db:"pause_reason" json:"pause_reason"`
	Metadata      json.RawMessage `db:"metadata" json:"metadata"`
	CreatedAt     time.Time       `db:"created_at" json:"created_at"`
	StartedAt     *time.Time      `db:"started_at" json:"started_at,omitempty"`
	PausedAt      *time.Time      `db:"paused_at" json:"paused_at,omitempty"`
	CompletedAt   *time.Time      `db:"completed_at" json:"completed_at,omitempty"`
	UpdatedAt     time.Time       `db:"updated_at" json:"updated_at"`
}

type WorkerDeployWaveStore struct {
	db DBTX
}

func NewWorkerDeployWaveStore(db DBTX) *WorkerDeployWaveStore {
	return &WorkerDeployWaveStore{db: db}
}

type CreateWorkerDeployWaveParams struct {
	ID            string
	Mode          string
	BuildSHA      string
	Region        string
	Bucket        string
	RequestedBy   string
	Reason        string
	MaxConcurrent int
	CanaryCount   int
	Metadata      map[string]any
}

// Create inserts durable wave state for CI/controller handoff.
// lint:allow-no-orgid reason="worker deploy waves are cluster-scoped infrastructure state"
func (s *WorkerDeployWaveStore) Create(ctx context.Context, params CreateWorkerDeployWaveParams) (WorkerDeployWave, error) {
	if params.ID == "" {
		return WorkerDeployWave{}, fmt.Errorf("wave id is required")
	}
	if params.Mode == "" {
		params.Mode = "routine"
	}
	if params.MaxConcurrent <= 0 {
		params.MaxConcurrent = 1
	}
	if params.CanaryCount <= 0 {
		params.CanaryCount = 1
	}
	metadata := params.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	rawMetadata, err := json.Marshal(metadata)
	if err != nil {
		return WorkerDeployWave{}, fmt.Errorf("marshal worker deploy wave metadata: %w", err)
	}
	rows, err := s.db.Query(ctx, `
		INSERT INTO worker_deploy_waves (
			id, mode, build_sha, region, bucket, requested_by, reason,
			max_concurrent, canary_count, metadata
		)
		VALUES (
			@id, @mode, @build_sha, @region, @bucket, @requested_by, @reason,
			@max_concurrent, @canary_count, @metadata
		)
		RETURNING id, status, mode, build_sha, region, bucket, requested_by, reason,
			max_concurrent, canary_count, pause_reason, metadata,
			created_at, started_at, paused_at, completed_at, updated_at`,
		pgx.NamedArgs{
			"id":             params.ID,
			"mode":           params.Mode,
			"build_sha":      params.BuildSHA,
			"region":         params.Region,
			"bucket":         params.Bucket,
			"requested_by":   params.RequestedBy,
			"reason":         params.Reason,
			"max_concurrent": params.MaxConcurrent,
			"canary_count":   params.CanaryCount,
			"metadata":       rawMetadata,
		})
	if err != nil {
		return WorkerDeployWave{}, fmt.Errorf("create worker deploy wave: %w", err)
	}
	wave, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[WorkerDeployWave])
	if err != nil {
		return WorkerDeployWave{}, fmt.Errorf("scan worker deploy wave: %w", err)
	}
	return wave, nil
}

// Pause marks a wave paused so a controller or CI job can stop launching hosts.
// lint:allow-no-orgid reason="worker deploy waves are cluster-scoped infrastructure state"
func (s *WorkerDeployWaveStore) Pause(ctx context.Context, waveID, reason string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE worker_deploy_waves
		SET status = 'paused',
			pause_reason = @reason,
			paused_at = COALESCE(paused_at, now()),
			updated_at = now()
		WHERE id = @id
		  AND status IN ('pending', 'running')`,
		pgx.NamedArgs{"id": waveID, "reason": reason})
	if err != nil {
		return fmt.Errorf("pause worker deploy wave: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("pause worker deploy wave: wave %q not found or not pausable", waveID)
	}
	return nil
}
