package db

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

// SessionThreadFileEventStore persists tab-level file write attribution.
// These rows are operational evidence — they power overlap badges in the tab
// strip and "Touched by tab" / "Overlap with another tab" filters in the
// Changes view. They are not security attribution and are best-effort: a
// missed event is recoverable by re-sampling git status on the next turn.
type SessionThreadFileEventStore struct {
	db DBTX
}

func NewSessionThreadFileEventStore(db DBTX) *SessionThreadFileEventStore {
	return &SessionThreadFileEventStore{db: db}
}

const sessionThreadFileEventColumns = `id, org_id, session_id, thread_id, turn, path, event_type, before_hash, after_hash, observed_at`

// appendBatchChunkSize bounds the per-statement INSERT row count so a turn
// with thousands of touched paths (rare, but possible for a refactor) does
// not push past Postgres's per-statement parameter limit (65535).
const appendBatchChunkSize = 500

// AppendBatch inserts a batch of file events in one round-trip. The orchestrator
// emits these after each turn by parsing the agent's diff; passing the whole
// batch through one statement keeps the per-turn DB cost flat regardless of
// how many files the tab touched. Splits into chunks at appendBatchChunkSize
// to stay under Postgres's per-statement parameter cap.
func (s *SessionThreadFileEventStore) AppendBatch(ctx context.Context, events []models.SessionThreadFileEvent) error {
	if len(events) == 0 {
		return nil
	}
	for start := 0; start < len(events); start += appendBatchChunkSize {
		end := start + appendBatchChunkSize
		if end > len(events) {
			end = len(events)
		}
		if err := s.appendChunk(ctx, events[start:end]); err != nil {
			return err
		}
	}
	return nil
}

// appendChunk is a single-statement multi-row INSERT. Built with explicit
// $N placeholders rather than unnest() so nullable columns (thread_id,
// before_hash, after_hash) round-trip via pgx's standard pointer codecs
// instead of relying on nullable-array encoding, which is fragile in pgx v5.
func (s *SessionThreadFileEventStore) appendChunk(ctx context.Context, events []models.SessionThreadFileEvent) error {
	if len(events) == 0 {
		return nil
	}
	const colsPerRow = 8
	var b strings.Builder
	b.WriteString("INSERT INTO session_thread_file_events (org_id, session_id, thread_id, turn, path, event_type, before_hash, after_hash) VALUES ")
	args := make([]any, 0, len(events)*colsPerRow)
	for i, e := range events {
		if i > 0 {
			b.WriteByte(',')
		}
		base := i * colsPerRow
		fmt.Fprintf(&b, "($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)", base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8)
		args = append(args, e.OrgID, e.SessionID, e.ThreadID, e.Turn, e.Path, e.EventType, e.BeforeHash, e.AfterHash)
	}
	if _, err := s.db.Exec(ctx, b.String(), args...); err != nil {
		return fmt.Errorf("append thread file events: %w", err)
	}
	return nil
}

// ListBySession returns every file event for a session, newest first. The
// frontend collapses this client-side into per-tab and per-path views; doing
// the rollup in the API layer would mean adding a parallel endpoint per
// shape, while the dataset is bounded by turns per session.
func (s *SessionThreadFileEventStore) ListBySession(ctx context.Context, orgID, sessionID uuid.UUID, since *time.Time) ([]models.SessionThreadFileEvent, error) {
	args := pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}
	query := `SELECT ` + sessionThreadFileEventColumns + `
		FROM session_thread_file_events
		WHERE org_id = @org_id AND session_id = @session_id`
	if since != nil {
		query += ` AND observed_at >= @since`
		args["since"] = *since
	}
	query += ` ORDER BY observed_at DESC, id DESC`
	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query thread file events: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.SessionThreadFileEvent])
}

// ListByThread returns the file events attributed to one tab, newest first.
// Used by the per-thread Changes filter.
func (s *SessionThreadFileEventStore) ListByThread(ctx context.Context, orgID, threadID uuid.UUID) ([]models.SessionThreadFileEvent, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+sessionThreadFileEventColumns+`
		FROM session_thread_file_events
		WHERE org_id = @org_id AND thread_id = @thread_id
		ORDER BY observed_at DESC, id DESC`,
		pgx.NamedArgs{"org_id": orgID, "thread_id": threadID},
	)
	if err != nil {
		return nil, fmt.Errorf("query thread file events by thread: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.SessionThreadFileEvent])
}
