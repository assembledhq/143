package db

import (
	"context"
	"fmt"
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

// AppendBatch inserts a batch of file events in one round-trip. The orchestrator
// emits these after each turn by diffing git status against the pre-turn
// snapshot; passing the whole batch through one INSERT keeps the per-turn DB
// cost flat regardless of how many files the tab touched.
func (s *SessionThreadFileEventStore) AppendBatch(ctx context.Context, events []models.SessionThreadFileEvent) error {
	if len(events) == 0 {
		return nil
	}
	orgIDs := make([]uuid.UUID, len(events))
	sessionIDs := make([]uuid.UUID, len(events))
	threadIDs := make([]*uuid.UUID, len(events))
	turns := make([]int, len(events))
	paths := make([]string, len(events))
	eventTypes := make([]string, len(events))
	beforeHashes := make([]*string, len(events))
	afterHashes := make([]*string, len(events))
	for i, e := range events {
		orgIDs[i] = e.OrgID
		sessionIDs[i] = e.SessionID
		threadIDs[i] = e.ThreadID
		turns[i] = e.Turn
		paths[i] = e.Path
		eventTypes[i] = e.EventType
		beforeHashes[i] = e.BeforeHash
		afterHashes[i] = e.AfterHash
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO session_thread_file_events
		    (org_id, session_id, thread_id, turn, path, event_type, before_hash, after_hash)
		SELECT * FROM unnest(
		    @org_ids::uuid[],
		    @session_ids::uuid[],
		    @thread_ids::uuid[],
		    @turns::int[],
		    @paths::text[],
		    @event_types::text[],
		    @before_hashes::text[],
		    @after_hashes::text[]
		)
	`, pgx.NamedArgs{
		"org_ids":       orgIDs,
		"session_ids":   sessionIDs,
		"thread_ids":    threadIDs,
		"turns":         turns,
		"paths":         paths,
		"event_types":   eventTypes,
		"before_hashes": beforeHashes,
		"after_hashes":  afterHashes,
	})
	if err != nil {
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
