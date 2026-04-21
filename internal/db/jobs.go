package db

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type JobStore struct {
	db DBTX
}

func NewJobStore(db DBTX) *JobStore {
	return &JobStore{db: db}
}

// GetLatestFailedByType returns the most recent failed or dead_letter job for the given org and job type.
// Returns nil, nil if no failed job exists.
func (s *JobStore) GetLatestFailedByType(ctx context.Context, orgID uuid.UUID, jobType string) (*models.LatestJobError, error) {
	query := `
		SELECT id, last_error, updated_at
		FROM jobs
		WHERE org_id = @org_id AND job_type = @job_type AND status IN ('failed', 'dead_letter')
		ORDER BY updated_at DESC
		LIMIT 1`

	var result models.LatestJobError
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"job_type": jobType,
	}).Scan(&result.JobID, &result.LastError, &result.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &result, nil
}

func (s *JobStore) Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	return enqueueOn(ctx, s.db, orgID, queue, jobType, payload, priority, dedupeKey)
}

// EnqueueInTx inserts a job inside an existing transaction so callers that
// must create a row and a job atomically (e.g. automation RunNow) don't leave
// orphaned state when one side fails.
func (s *JobStore) EnqueueInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	return enqueueOn(ctx, tx, orgID, queue, jobType, payload, priority, dedupeKey)
}

type jobQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func enqueueOn(ctx context.Context, q jobQuerier, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return uuid.Nil, err
	}

	var id uuid.UUID
	query := `
		INSERT INTO jobs (org_id, queue, job_type, payload, priority, dedupe_key)
		VALUES (@org_id, @queue, @job_type, @payload, @priority, @dedupe_key)
		ON CONFLICT DO NOTHING
		RETURNING id`

	err = q.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":     orgID,
		"queue":      queue,
		"job_type":   jobType,
		"payload":    payloadJSON,
		"priority":   priority,
		"dedupe_key": dedupeKey,
	}).Scan(&id)
	// ON CONFLICT DO NOTHING returns no row when a pending/running job with the
	// same (queue, dedupe_key) already exists. Treat that as a successful no-op:
	// the existing job will satisfy the caller's intent.
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil
	}
	return id, err
}

// DeleteExpiredCompleted removes completed/failed jobs older than the given number of days.
// lint:allow-no-orgid reason="system-wide retention cleanup across all orgs"
func (s *JobStore) DeleteExpiredCompleted(ctx context.Context, retentionDays int) (int64, error) {
	var deleted int64
	err := s.db.QueryRow(ctx,
		"SELECT delete_expired_completed_jobs($1)", retentionDays,
	).Scan(&deleted)
	return deleted, err
}
