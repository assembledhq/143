package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type ThreadInboxStore struct {
	db DBTX
}

func NewThreadInboxStore(db DBTX) *ThreadInboxStore {
	return &ThreadInboxStore{db: db}
}

const threadInboxSelectColumns = `id, org_id, session_id, thread_id, sequence_no, message_id,
	entry_type, delivery_state, accepted_at, delivered_at, acked_at, owner_node_id,
	delivery_attempts, last_error`

func (s *ThreadInboxStore) Create(ctx context.Context, entry *models.ThreadInboxEntry) error {
	query := `
		WITH thread_lock AS (
			SELECT pg_advisory_xact_lock(hashtextextended(@org_id::text || ':' || @thread_id::text, 0))
		),
		next_seq AS (
			SELECT COALESCE(MAX(sequence_no), 0) + 1 AS sequence_no
			FROM thread_inbox_entries
			WHERE org_id = @org_id AND thread_id = @thread_id
		)
		INSERT INTO thread_inbox_entries (
			org_id, session_id, thread_id, sequence_no, message_id, entry_type, delivery_state
		)
		SELECT @org_id, @session_id, @thread_id, next_seq.sequence_no, @message_id, @entry_type, 'pending'
		FROM next_seq
		CROSS JOIN thread_lock
		RETURNING id, sequence_no, delivery_state, accepted_at`

	return s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":     entry.OrgID,
		"session_id": entry.SessionID,
		"thread_id":  entry.ThreadID,
		"message_id": entry.MessageID,
		"entry_type": entry.EntryType,
	}).Scan(&entry.ID, &entry.SequenceNo, &entry.DeliveryState, &entry.AcceptedAt)
}

func (s *ThreadInboxStore) ListPendingAfter(ctx context.Context, orgID, threadID uuid.UUID, afterSequence int64, limit int) ([]models.ThreadInboxEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `
		SELECT `+threadInboxSelectColumns+`
		FROM thread_inbox_entries
		WHERE org_id = @org_id
		  AND thread_id = @thread_id
		  AND sequence_no > @after_sequence
		  AND delivery_state IN ('pending', 'delivered')
		ORDER BY sequence_no ASC
		LIMIT @limit
	`, pgx.NamedArgs{
		"org_id":         orgID,
		"thread_id":      threadID,
		"after_sequence": afterSequence,
		"limit":          limit,
	})
	if err != nil {
		return nil, fmt.Errorf("query thread inbox entries: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.ThreadInboxEntry])
}

func (s *ThreadInboxStore) MarkDelivered(ctx context.Context, orgID, threadID uuid.UUID, sequenceNo int64, ownerNodeID string) error {
	_, err := s.db.Exec(ctx, `
		UPDATE thread_inbox_entries
		SET delivery_state = 'delivered',
		    delivered_at = COALESCE(delivered_at, now()),
		    owner_node_id = @owner_node_id,
		    delivery_attempts = delivery_attempts + 1
		WHERE org_id = @org_id
		  AND thread_id = @thread_id
		  AND sequence_no = @sequence_no
		  AND delivery_state = 'pending'
	`, pgx.NamedArgs{
		"org_id":        orgID,
		"thread_id":     threadID,
		"sequence_no":   sequenceNo,
		"owner_node_id": ownerNodeID,
	})
	return err
}

func (s *ThreadInboxStore) MarkAckedThrough(ctx context.Context, orgID, threadID uuid.UUID, sequenceNo int64, ownerNodeID string) error {
	_, err := s.db.Exec(ctx, `
		UPDATE thread_inbox_entries
		SET delivery_state = 'acked',
		    delivered_at = COALESCE(delivered_at, now()),
		    acked_at = COALESCE(acked_at, now()),
		    owner_node_id = @owner_node_id
		WHERE org_id = @org_id
		  AND thread_id = @thread_id
		  AND sequence_no <= @sequence_no
		  AND delivery_state IN ('pending', 'delivered')
	`, pgx.NamedArgs{
		"org_id":        orgID,
		"thread_id":     threadID,
		"sequence_no":   sequenceNo,
		"owner_node_id": ownerNodeID,
	})
	return err
}

func (s *ThreadInboxStore) SequenceForMessage(ctx context.Context, orgID, threadID uuid.UUID, messageID int64) (int64, error) {
	var sequenceNo int64
	err := s.db.QueryRow(ctx, `
		SELECT sequence_no
		FROM thread_inbox_entries
		WHERE org_id = @org_id AND thread_id = @thread_id AND message_id = @message_id
	`, pgx.NamedArgs{
		"org_id":     orgID,
		"thread_id":  threadID,
		"message_id": messageID,
	}).Scan(&sequenceNo)
	if err != nil {
		return 0, fmt.Errorf("lookup thread inbox sequence: %w", err)
	}
	return sequenceNo, nil
}

func (s *ThreadInboxStore) CountUnacked(ctx context.Context, orgID, threadID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `
		SELECT count(*)
		FROM thread_inbox_entries
		WHERE org_id = @org_id AND thread_id = @thread_id AND delivery_state IN ('pending', 'delivered')
	`, pgx.NamedArgs{"org_id": orgID, "thread_id": threadID}).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count thread inbox entries: %w", err)
	}
	return count, nil
}

func (s *ThreadInboxStore) CountUnackedAfter(ctx context.Context, orgID, threadID uuid.UUID, afterSequence int64) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `
		SELECT count(*)
		FROM thread_inbox_entries
		WHERE org_id = @org_id
		  AND thread_id = @thread_id
		  AND sequence_no > @after_sequence
		  AND delivery_state IN ('pending', 'delivered')
	`, pgx.NamedArgs{
		"org_id":         orgID,
		"thread_id":      threadID,
		"after_sequence": afterSequence,
	}).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count thread inbox entries after sequence: %w", err)
	}
	return count, nil
}
