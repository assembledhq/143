package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const DefaultThreadInboxMaxDeliveryAttempts = 5

type ThreadInboxStore struct {
	db DBTX
}

func NewThreadInboxStore(db DBTX) *ThreadInboxStore {
	return &ThreadInboxStore{db: db}
}

type AppendThreadInboxEntryParams struct {
	SessionID       uuid.UUID
	ThreadID        uuid.UUID
	MessageID       int64
	ClientMessageID string
	EntryType       models.ThreadInboxEntryType
	Payload         json.RawMessage
}

const threadInboxEntryColumns = `id, org_id, session_id, thread_id, sequence_no, message_id, client_message_id,
		entry_type, payload, delivery_state, delivery_attempts, last_error,
		owner_node_id, runtime_id, accepted_at, delivered_at, acked_at, created_at, updated_at`

const threadInboxEntryColumnsE = `e.id, e.org_id, e.session_id, e.thread_id, e.sequence_no, e.message_id, e.client_message_id,
		e.entry_type, e.payload, e.delivery_state, e.delivery_attempts, e.last_error,
		e.owner_node_id, e.runtime_id, e.accepted_at, e.delivered_at, e.acked_at, e.created_at, e.updated_at`

const threadInboxDeliverySummarySelect = `
		SELECT
			thread_id,
			(count(*) FILTER (WHERE delivery_state = 'pending'))::int AS pending_count,
			(count(*) FILTER (WHERE delivery_state = 'delivering'))::int AS delivering_count,
			(count(*) FILTER (WHERE delivery_state = 'delivered'))::int AS delivered_count,
			(count(*) FILTER (WHERE delivery_state = 'unknown_delivery'))::int AS unknown_delivery_count,
			(count(*) FILTER (WHERE delivery_state = 'acked'))::int AS acked_count,
			(count(*) FILTER (WHERE delivery_state = 'dead_letter'))::int AS dead_letter_count,
			COALESCE(max(sequence_no), 0) AS last_sequence_no,
			max(accepted_at) AS last_accepted_at,
			max(delivered_at) AS last_delivered_at,
			max(acked_at) AS last_acked_at,
			(array_agg(last_error ORDER BY updated_at DESC) FILTER (WHERE last_error IS NOT NULL))[1] AS last_error
		FROM thread_inbox_entries`

func (s *ThreadInboxStore) AppendForMessage(ctx context.Context, orgID uuid.UUID, params AppendThreadInboxEntryParams) (models.ThreadInboxEntry, error) {
	if err := params.EntryType.Validate(); err != nil {
		return models.ThreadInboxEntry{}, err
	}
	payload := params.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	clientMessageID := params.ClientMessageID
	if clientMessageID == "" && params.MessageID != 0 {
		clientMessageID = fmt.Sprintf("message:%d", params.MessageID)
	}

	query := `
		WITH locked_thread AS (
			SELECT id
			FROM session_threads
			WHERE org_id = @org_id
			  AND session_id = @session_id
			  AND id = @thread_id
			FOR UPDATE
		), next_sequence AS (
			SELECT COALESCE(MAX(sequence_no), 0) + 1 AS sequence_no
			FROM thread_inbox_entries
			WHERE org_id = @org_id
			  AND thread_id = @thread_id
		)
			INSERT INTO thread_inbox_entries (
				org_id, session_id, thread_id, sequence_no, message_id, client_message_id, entry_type, payload, delivery_state
			)
			SELECT
				@org_id, @session_id, @thread_id, next_sequence.sequence_no,
				NULLIF(@message_id, 0), NULLIF(@client_message_id, ''), @entry_type, @payload, 'pending'
			FROM locked_thread, next_sequence
			ON CONFLICT (org_id, thread_id, client_message_id) WHERE client_message_id IS NOT NULL DO UPDATE
			SET updated_at = thread_inbox_entries.updated_at
			RETURNING ` + threadInboxEntryColumns

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":            orgID,
		"session_id":        params.SessionID,
		"thread_id":         params.ThreadID,
		"message_id":        params.MessageID,
		"client_message_id": clientMessageID,
		"entry_type":        params.EntryType,
		"payload":           payload,
	})
	if err != nil {
		return models.ThreadInboxEntry{}, fmt.Errorf("append thread inbox entry: %w", err)
	}
	entry, err := pgx.CollectOneRow(rows, scanThreadInboxEntryRow)
	if err != nil {
		return models.ThreadInboxEntry{}, fmt.Errorf("append thread inbox entry: %w", err)
	}
	return entry, nil
}

func scanThreadInboxEntryRow(row pgx.CollectableRow) (models.ThreadInboxEntry, error) {
	var entry models.ThreadInboxEntry
	var entryType string
	var deliveryState string
	var payload []byte
	var messageID sql.NullInt64
	var clientMessageID sql.NullString
	var lastError sql.NullString
	var ownerNodeID sql.NullString
	var runtimeID pgtype.UUID
	var deliveredAt pgtype.Timestamptz
	var ackedAt pgtype.Timestamptz
	if err := row.Scan(
		&entry.ID,
		&entry.OrgID,
		&entry.SessionID,
		&entry.ThreadID,
		&entry.SequenceNo,
		&messageID,
		&clientMessageID,
		&entryType,
		&payload,
		&deliveryState,
		&entry.DeliveryAttempts,
		&lastError,
		&ownerNodeID,
		&runtimeID,
		&entry.AcceptedAt,
		&deliveredAt,
		&ackedAt,
		&entry.CreatedAt,
		&entry.UpdatedAt,
	); err != nil {
		return models.ThreadInboxEntry{}, err
	}
	entry.EntryType = models.ThreadInboxEntryType(entryType)
	if messageID.Valid {
		entry.MessageID = messageID.Int64
	}
	if clientMessageID.Valid {
		entry.ClientMessageID = &clientMessageID.String
	}
	entry.Payload = json.RawMessage(payload)
	entry.DeliveryState = models.ThreadInboxDeliveryState(deliveryState)
	if lastError.Valid {
		entry.LastError = &lastError.String
	}
	if ownerNodeID.Valid {
		entry.OwnerNodeID = &ownerNodeID.String
	}
	if runtimeID.Valid {
		id := uuid.UUID(runtimeID.Bytes)
		entry.RuntimeID = &id
	}
	if deliveredAt.Valid {
		t := deliveredAt.Time.UTC()
		entry.DeliveredAt = &t
	}
	if ackedAt.Valid {
		t := ackedAt.Time.UTC()
		entry.AckedAt = &t
	}
	entry.AcceptedAt = entry.AcceptedAt.In(time.UTC)
	entry.CreatedAt = entry.CreatedAt.In(time.UTC)
	entry.UpdatedAt = entry.UpdatedAt.In(time.UTC)
	return entry, nil
}

func scanThreadInboxDeliverySummaryRow(row pgx.CollectableRow) (models.ThreadInboxDeliverySummary, error) {
	var summary models.ThreadInboxDeliverySummary
	var lastAcceptedAt pgtype.Timestamptz
	var lastDeliveredAt pgtype.Timestamptz
	var lastAckedAt pgtype.Timestamptz
	var lastError sql.NullString
	if err := row.Scan(
		&summary.ThreadID,
		&summary.PendingCount,
		&summary.DeliveringCount,
		&summary.DeliveredCount,
		&summary.UnknownDeliveryCount,
		&summary.AckedCount,
		&summary.DeadLetterCount,
		&summary.LastSequenceNo,
		&lastAcceptedAt,
		&lastDeliveredAt,
		&lastAckedAt,
		&lastError,
	); err != nil {
		return models.ThreadInboxDeliverySummary{}, err
	}
	if lastAcceptedAt.Valid {
		t := lastAcceptedAt.Time.UTC()
		summary.LastAcceptedAt = &t
	}
	if lastDeliveredAt.Valid {
		t := lastDeliveredAt.Time.UTC()
		summary.LastDeliveredAt = &t
	}
	if lastAckedAt.Valid {
		t := lastAckedAt.Time.UTC()
		summary.LastAckedAt = &t
	}
	if lastError.Valid {
		summary.LastError = &lastError.String
	}
	summary.Normalize()
	return summary, nil
}

func (s *ThreadInboxStore) MarkDeliveredThrough(ctx context.Context, orgID, threadID, runtimeID uuid.UUID, ownerNodeID string, sequenceNo int64) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE thread_inbox_entries
		SET delivery_state = 'delivered',
			runtime_id = $3,
			owner_node_id = $4,
			delivered_at = COALESCE(delivered_at, now()),
			updated_at = now()
		WHERE org_id = $1
		  AND thread_id = $2
		  AND sequence_no <= $5
		  AND delivery_state IN ('pending', 'delivering')`, orgID, threadID, runtimeID, ownerNodeID, sequenceNo)
	if err != nil {
		return 0, fmt.Errorf("mark thread inbox delivered: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *ThreadInboxStore) MarkDeliveredForEntry(ctx context.Context, orgID, threadID, runtimeID uuid.UUID, ownerNodeID string, entryID uuid.UUID, sequenceNo int64) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE thread_inbox_entries
		SET delivery_state = 'delivered',
			runtime_id = $3,
			owner_node_id = NULLIF($4, ''),
			delivered_at = COALESCE(delivered_at, now()),
			updated_at = now()
		WHERE org_id = $1
		  AND thread_id = $2
		  AND runtime_id = $3
		  AND id = $5
		  AND sequence_no = $6
		  AND delivery_state = 'delivering'`, orgID, threadID, runtimeID, ownerNodeID, entryID, sequenceNo)
	if err != nil {
		return 0, fmt.Errorf("mark thread inbox entry delivered: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *ThreadInboxStore) MarkAckedThrough(ctx context.Context, orgID, threadID, runtimeID uuid.UUID, sequenceNo int64) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE thread_inbox_entries
		SET delivery_state = 'acked',
			runtime_id = $3,
			acked_at = COALESCE(acked_at, now()),
			updated_at = now()
		WHERE org_id = $1
		  AND thread_id = $2
		  AND sequence_no <= $4
		  AND delivery_state IN ('pending', 'delivering', 'delivered')`, orgID, threadID, runtimeID, sequenceNo)
	if err != nil {
		return 0, fmt.Errorf("mark thread inbox acked: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *ThreadInboxStore) MarkAckedForMessages(ctx context.Context, orgID, threadID, runtimeID uuid.UUID, messageIDs []int64) (int64, error) {
	if len(messageIDs) == 0 {
		return 0, nil
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE thread_inbox_entries
		SET delivery_state = 'acked',
			runtime_id = $3,
			delivered_at = COALESCE(delivered_at, now()),
			acked_at = COALESCE(acked_at, now()),
			updated_at = now()
		WHERE org_id = $1
		  AND thread_id = $2
		  AND message_id = ANY($4)
		  AND delivery_state IN ('pending', 'delivering', 'delivered')`, orgID, threadID, runtimeID, messageIDs)
	if err != nil {
		return 0, fmt.Errorf("mark thread inbox acked for messages: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *ThreadInboxStore) MarkDeliveringForMessages(ctx context.Context, orgID, threadID, runtimeID uuid.UUID, ownerNodeID string, messageIDs []int64) (int64, error) {
	if len(messageIDs) == 0 {
		return 0, nil
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE thread_inbox_entries
		SET delivery_state = 'delivering',
			runtime_id = $3,
			owner_node_id = NULLIF($4, ''),
			updated_at = now()
		WHERE org_id = $1
		  AND thread_id = $2
		  AND message_id = ANY($5)
		  AND delivery_state = 'pending'`, orgID, threadID, runtimeID, ownerNodeID, messageIDs)
	if err != nil {
		return 0, fmt.Errorf("mark thread inbox delivering for messages: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *ThreadInboxStore) MarkDeadLetter(ctx context.Context, orgID, threadID, entryID uuid.UUID, reason string) (models.ThreadInboxEntry, error) {
	rows, err := s.db.Query(ctx, `
		UPDATE thread_inbox_entries
		SET delivery_state = 'dead_letter',
			delivery_attempts = delivery_attempts + 1,
			last_error = NULLIF($4, ''),
			owner_node_id = NULL,
			updated_at = now()
		WHERE org_id = $1
		  AND thread_id = $2
		  AND id = $3
		  AND delivery_state IN ('pending', 'delivering', 'delivered', 'unknown_delivery')
		RETURNING `+threadInboxEntryColumns, orgID, threadID, entryID, reason)
	if err != nil {
		return models.ThreadInboxEntry{}, fmt.Errorf("mark thread inbox dead-letter: %w", err)
	}
	entry, err := pgx.CollectOneRow(rows, scanThreadInboxEntryRow)
	if err != nil {
		return models.ThreadInboxEntry{}, fmt.Errorf("mark thread inbox dead-letter: %w", err)
	}
	return entry, nil
}

func (s *ThreadInboxStore) MarkDeliveryFailed(ctx context.Context, orgID, threadID, runtimeID, entryID uuid.UUID, reason string, maxAttempts int) (models.ThreadInboxEntry, error) {
	if maxAttempts <= 0 {
		maxAttempts = DefaultThreadInboxMaxDeliveryAttempts
	}
	rows, err := s.db.Query(ctx, `
		UPDATE thread_inbox_entries
		SET delivery_state = CASE
				WHEN delivery_attempts + 1 >= $6 THEN 'dead_letter'
				ELSE 'pending'
			END,
			delivery_attempts = delivery_attempts + 1,
			last_error = NULLIF($5, ''),
			owner_node_id = NULL,
			runtime_id = NULL,
			updated_at = now()
		WHERE org_id = $1
		  AND thread_id = $2
		  AND runtime_id = $3
		  AND id = $4
		  AND delivery_state = 'delivering'
		RETURNING `+threadInboxEntryColumns, orgID, threadID, runtimeID, entryID, reason, maxAttempts)
	if err != nil {
		return models.ThreadInboxEntry{}, fmt.Errorf("mark thread inbox delivery failed: %w", err)
	}
	entry, err := pgx.CollectOneRow(rows, scanThreadInboxEntryRow)
	if err != nil {
		return models.ThreadInboxEntry{}, fmt.Errorf("mark thread inbox delivery failed: %w", err)
	}
	return entry, nil
}

func (s *ThreadInboxStore) ListDeliverableAfter(ctx context.Context, orgID, threadID uuid.UUID, afterSequence int64, limit int) ([]models.ThreadInboxEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `
		SELECT `+threadInboxEntryColumns+`
		FROM thread_inbox_entries
		WHERE org_id = $1
		  AND thread_id = $2
		  AND (sequence_no > $3 OR delivery_state = 'pending')
		  AND delivery_state IN ('pending', 'delivering')
		ORDER BY sequence_no ASC
		LIMIT $4`, orgID, threadID, afterSequence, limit)
	if err != nil {
		return nil, fmt.Errorf("list deliverable thread inbox entries: %w", err)
	}
	entries, err := pgx.CollectRows(rows, scanThreadInboxEntryRow)
	if err != nil {
		return nil, fmt.Errorf("list deliverable thread inbox entries: %w", err)
	}
	return entries, nil
}

func (s *ThreadInboxStore) ClaimDeliverableAfter(ctx context.Context, orgID, threadID, runtimeID uuid.UUID, ownerNodeID string, afterSequence int64, limit int) ([]models.ThreadInboxEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `
		WITH candidates AS (
			SELECT id
			FROM thread_inbox_entries
			WHERE org_id = $1
			  AND thread_id = $2
			  AND (sequence_no > $5 OR delivery_state = 'pending')
			  AND delivery_state IN ('pending', 'delivering')
			ORDER BY sequence_no ASC
			LIMIT $6
			FOR UPDATE SKIP LOCKED
		), claimed AS (
			UPDATE thread_inbox_entries e
			SET delivery_state = 'delivering',
				runtime_id = $3,
				owner_node_id = NULLIF($4, ''),
				updated_at = now()
			FROM candidates c
			WHERE e.id = c.id
			  AND e.delivery_state = 'pending'
			RETURNING `+threadInboxEntryColumnsE+`
		)
		SELECT `+threadInboxEntryColumns+`
		FROM claimed
		UNION ALL
		SELECT `+threadInboxEntryColumns+`
		FROM thread_inbox_entries e
		WHERE e.id IN (SELECT id FROM candidates)
		  AND e.delivery_state = 'delivering'
		  AND e.runtime_id = $3
		ORDER BY sequence_no ASC`, orgID, threadID, runtimeID, ownerNodeID, afterSequence, limit)
	if err != nil {
		return nil, fmt.Errorf("claim deliverable thread inbox entries: %w", err)
	}
	entries, err := pgx.CollectRows(rows, scanThreadInboxEntryRow)
	if err != nil {
		return nil, fmt.Errorf("claim deliverable thread inbox entries: %w", err)
	}
	return entries, nil
}

func (s *ThreadInboxStore) ListRecoverableByThread(ctx context.Context, orgID, threadID uuid.UUID, limit int) ([]models.ThreadInboxEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `
		SELECT `+threadInboxEntryColumns+`
		FROM thread_inbox_entries
		WHERE org_id = $1
		  AND thread_id = $2
		  AND delivery_state IN ('dead_letter', 'unknown_delivery')
		ORDER BY sequence_no ASC
		LIMIT $3`, orgID, threadID, limit)
	if err != nil {
		return nil, fmt.Errorf("list recoverable thread inbox entries: %w", err)
	}
	entries, err := pgx.CollectRows(rows, scanThreadInboxEntryRow)
	if err != nil {
		return nil, fmt.Errorf("list recoverable thread inbox entries: %w", err)
	}
	return entries, nil
}

func (s *ThreadInboxStore) RetryRecoverable(ctx context.Context, orgID, threadID, entryID uuid.UUID, allowUnknownDelivery bool) (models.ThreadInboxEntry, error) {
	rows, err := s.db.Query(ctx, `
		UPDATE thread_inbox_entries
		SET delivery_state = 'pending',
			delivery_attempts = 0,
			last_error = NULL,
			owner_node_id = NULL,
			runtime_id = NULL,
			delivered_at = CASE WHEN delivery_state = 'unknown_delivery' THEN delivered_at ELSE NULL END,
			acked_at = NULL,
			updated_at = now()
		WHERE org_id = $1
		  AND thread_id = $2
		  AND id = $3
		  AND (
			  delivery_state = 'dead_letter'
			  OR ($4::boolean AND delivery_state = 'unknown_delivery')
		  )
		RETURNING `+threadInboxEntryColumns, orgID, threadID, entryID, allowUnknownDelivery)
	if err != nil {
		return models.ThreadInboxEntry{}, fmt.Errorf("retry recoverable thread inbox entry: %w", err)
	}
	entry, err := pgx.CollectOneRow(rows, scanThreadInboxEntryRow)
	if err != nil {
		return models.ThreadInboxEntry{}, fmt.Errorf("retry recoverable thread inbox entry: %w", err)
	}
	return entry, nil
}

func (s *ThreadInboxStore) CountPendingByThread(ctx context.Context, orgID, threadID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `
		SELECT count(*)
		FROM thread_inbox_entries
		WHERE org_id = $1
		  AND thread_id = $2
		  AND delivery_state IN ('pending', 'delivering', 'delivered', 'unknown_delivery')`, orgID, threadID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count pending thread inbox entries: %w", err)
	}
	return count, nil
}

func (s *ThreadInboxStore) CountPendingBySession(ctx context.Context, orgID, sessionID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `
		SELECT count(*)
		FROM thread_inbox_entries
		WHERE org_id = $1
		  AND session_id = $2
		  AND delivery_state IN ('pending', 'delivering', 'delivered', 'unknown_delivery')`, orgID, sessionID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count pending session inbox entries: %w", err)
	}
	return count, nil
}

func (s *ThreadInboxStore) GetByClientMessageID(ctx context.Context, orgID, threadID uuid.UUID, clientMessageID string) (models.ThreadInboxEntry, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+threadInboxEntryColumns+`
		FROM thread_inbox_entries
		WHERE org_id = $1
		  AND thread_id = $2
		  AND client_message_id = $3
		LIMIT 1`, orgID, threadID, clientMessageID)
	if err != nil {
		return models.ThreadInboxEntry{}, fmt.Errorf("get thread inbox by client message id: %w", err)
	}
	entry, err := pgx.CollectOneRow(rows, scanThreadInboxEntryRow)
	if err != nil {
		return models.ThreadInboxEntry{}, fmt.Errorf("get thread inbox by client message id: %w", err)
	}
	return entry, nil
}

func (s *ThreadInboxStore) IsMessageAcked(ctx context.Context, orgID, threadID uuid.UUID, messageID int64) (bool, error) {
	var acked bool
	err := s.db.QueryRow(ctx, `
		SELECT delivery_state = 'acked'
		FROM thread_inbox_entries
		WHERE org_id = $1
		  AND thread_id = $2
		  AND message_id = $3
		LIMIT 1`, orgID, threadID, messageID).Scan(&acked)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check thread inbox message acked: %w", err)
	}
	return acked, nil
}

func (s *ThreadInboxStore) ListDeliverySummariesBySession(ctx context.Context, orgID, sessionID uuid.UUID) (map[uuid.UUID]models.ThreadInboxDeliverySummary, error) {
	rows, err := s.db.Query(ctx, threadInboxDeliverySummarySelect+`
		WHERE org_id = $1
		  AND session_id = $2
		  AND delivery_state <> 'acked'
		GROUP BY thread_id`, orgID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list thread inbox delivery summaries: %w", err)
	}
	summaries, err := pgx.CollectRows(rows, scanThreadInboxDeliverySummaryRow)
	if err != nil {
		return nil, fmt.Errorf("list thread inbox delivery summaries: %w", err)
	}
	byThread := make(map[uuid.UUID]models.ThreadInboxDeliverySummary, len(summaries))
	for _, summary := range summaries {
		byThread[summary.ThreadID] = summary
	}
	return byThread, nil
}

func (s *ThreadInboxStore) GetDeliverySummaryByThread(ctx context.Context, orgID, threadID uuid.UUID) (models.ThreadInboxDeliverySummary, error) {
	rows, err := s.db.Query(ctx, threadInboxDeliverySummarySelect+`
		WHERE org_id = $1
		  AND thread_id = $2
		  AND delivery_state <> 'acked'
		GROUP BY thread_id`, orgID, threadID)
	if err != nil {
		return models.ThreadInboxDeliverySummary{}, fmt.Errorf("get thread inbox delivery summary: %w", err)
	}
	summary, err := pgx.CollectOneRow(rows, scanThreadInboxDeliverySummaryRow)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			summary := models.ThreadInboxDeliverySummary{ThreadID: threadID}
			summary.Normalize()
			return summary, nil
		}
		return models.ThreadInboxDeliverySummary{}, fmt.Errorf("get thread inbox delivery summary: %w", err)
	}
	return summary, nil
}
