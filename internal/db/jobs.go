package db

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type JobStore struct {
	db DBTX
}

func NewJobStore(db DBTX) *JobStore {
	return &JobStore{db: db}
}

func (s *JobStore) Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
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

	err = s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":    orgID,
		"queue":     queue,
		"job_type":  jobType,
		"payload":   payloadJSON,
		"priority":  priority,
		"dedupe_key": dedupeKey,
	}).Scan(&id)
	return id, err
}
