package db

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type ThreadRuntimeStore struct {
	db DBTX
}

func NewThreadRuntimeStore(db DBTX) *ThreadRuntimeStore {
	return &ThreadRuntimeStore{db: db}
}

const threadRuntimeSelectColumns = `thread_id, org_id, session_id, runtime_id, owner_node_id, lease_token,
	lease_expires_at, status, sandbox_id, agent_type, model, last_delivered_sequence,
	last_acked_sequence, last_heartbeat_at, started_at, closed_at`

func (s *ThreadRuntimeStore) Upsert(ctx context.Context, runtime *models.ThreadRuntime) error {
	rows, err := s.db.Query(ctx, `
		INSERT INTO thread_runtimes (
			thread_id, org_id, session_id, runtime_id, owner_node_id, lease_token,
			lease_expires_at, status, sandbox_id, agent_type, model, last_delivered_sequence,
			last_acked_sequence, last_heartbeat_at, started_at, closed_at
		)
		VALUES (
			@thread_id, @org_id, @session_id, @runtime_id, @owner_node_id, @lease_token,
			@lease_expires_at, @status, @sandbox_id, @agent_type, @model, @last_delivered_sequence,
			@last_acked_sequence, now(), COALESCE(@started_at, now()), @closed_at
		)
		ON CONFLICT (thread_id)
		DO UPDATE SET
			org_id = EXCLUDED.org_id,
			session_id = EXCLUDED.session_id,
			runtime_id = EXCLUDED.runtime_id,
			owner_node_id = EXCLUDED.owner_node_id,
			lease_token = EXCLUDED.lease_token,
			lease_expires_at = EXCLUDED.lease_expires_at,
			status = EXCLUDED.status,
			sandbox_id = EXCLUDED.sandbox_id,
			agent_type = EXCLUDED.agent_type,
			model = EXCLUDED.model,
			last_heartbeat_at = now(),
			closed_at = EXCLUDED.closed_at
		RETURNING `+threadRuntimeSelectColumns,
		pgx.NamedArgs{
			"thread_id":               runtime.ThreadID,
			"org_id":                  runtime.OrgID,
			"session_id":              runtime.SessionID,
			"runtime_id":              runtime.RuntimeID,
			"owner_node_id":           runtime.OwnerNodeID,
			"lease_token":             runtime.LeaseToken,
			"lease_expires_at":        runtime.LeaseExpiresAt,
			"status":                  runtime.Status,
			"sandbox_id":              runtime.SandboxID,
			"agent_type":              runtime.AgentType,
			"model":                   runtime.Model,
			"last_delivered_sequence": runtime.LastDeliveredSequence,
			"last_acked_sequence":     runtime.LastAckedSequence,
			"started_at":              zeroTimeNil(runtime.StartedAt),
			"closed_at":               runtime.ClosedAt,
		},
	)
	if err != nil {
		return fmt.Errorf("upsert thread runtime: %w", err)
	}
	updated, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.ThreadRuntime])
	if err != nil {
		return fmt.Errorf("collect thread runtime: %w", err)
	}
	*runtime = updated
	return nil
}

func zeroTimeNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func (s *ThreadRuntimeStore) GetByThread(ctx context.Context, orgID, threadID uuid.UUID) (models.ThreadRuntime, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+threadRuntimeSelectColumns+`
		FROM thread_runtimes
		WHERE org_id = @org_id AND thread_id = @thread_id
	`, pgx.NamedArgs{"org_id": orgID, "thread_id": threadID})
	if err != nil {
		return models.ThreadRuntime{}, fmt.Errorf("query thread runtime: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.ThreadRuntime])
}

func (s *ThreadRuntimeStore) AdvanceCursors(ctx context.Context, orgID, threadID uuid.UUID, ownerNodeID string, leaseToken uuid.UUID, deliveredThrough, ackedThrough int64) error {
	_, err := s.db.Exec(ctx, `
		UPDATE thread_runtimes
		SET last_delivered_sequence = GREATEST(last_delivered_sequence, @delivered_through),
		    last_acked_sequence = GREATEST(last_acked_sequence, @acked_through),
		    last_heartbeat_at = now(),
		    lease_expires_at = GREATEST(lease_expires_at, now())
		WHERE org_id = @org_id
		  AND thread_id = @thread_id
		  AND owner_node_id = @owner_node_id
		  AND lease_token = @lease_token
	`, pgx.NamedArgs{
		"org_id":            orgID,
		"thread_id":         threadID,
		"owner_node_id":     ownerNodeID,
		"lease_token":       leaseToken,
		"delivered_through": deliveredThrough,
		"acked_through":     ackedThrough,
	})
	return err
}

func (s *ThreadRuntimeStore) Heartbeat(ctx context.Context, orgID, threadID uuid.UUID, ownerNodeID string, leaseToken uuid.UUID, leaseExpiresAt time.Time) error {
	_, err := s.db.Exec(ctx, `
		UPDATE thread_runtimes
		SET lease_expires_at = @lease_expires_at,
		    last_heartbeat_at = now()
		WHERE org_id = @org_id
		  AND thread_id = @thread_id
		  AND owner_node_id = @owner_node_id
		  AND lease_token = @lease_token
	`, pgx.NamedArgs{
		"org_id":          orgID,
		"thread_id":       threadID,
		"owner_node_id":   ownerNodeID,
		"lease_token":     leaseToken,
		"lease_expires_at": leaseExpiresAt,
	})
	return err
}

func (s *ThreadRuntimeStore) Close(ctx context.Context, orgID, threadID uuid.UUID, ownerNodeID string, leaseToken uuid.UUID, status models.ThreadRuntimeStatus) error {
	_, err := s.db.Exec(ctx, `
		UPDATE thread_runtimes
		SET status = @status,
		    closed_at = now(),
		    last_heartbeat_at = now()
		WHERE org_id = @org_id
		  AND thread_id = @thread_id
		  AND owner_node_id = @owner_node_id
		  AND lease_token = @lease_token
	`, pgx.NamedArgs{
		"org_id":        orgID,
		"thread_id":     threadID,
		"owner_node_id": ownerNodeID,
		"lease_token":   leaseToken,
		"status":        status,
	})
	return err
}
