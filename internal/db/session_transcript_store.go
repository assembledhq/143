package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

const (
	DefaultTranscriptLimitTurns = 20
	MaxTranscriptLimitTurns     = 80
)

// SessionTranscriptWindowOptions controls which slice of the conversation is
// returned.
type SessionTranscriptWindowOptions struct {
	Position         models.TranscriptWindowPosition
	Before           *models.TranscriptCursor
	After            *models.TranscriptCursor
	AnchorEntryID    string
	AnchorMessageID  int64
	AnchorTurnNumber *int
	LimitTurns       int
	Include          TranscriptInclude
}

type TranscriptInclude struct {
	Messages    bool
	Tools       bool
	HumanInputs bool
	System      bool
}

func DefaultTranscriptInclude() TranscriptInclude {
	return TranscriptInclude{Messages: true, Tools: true, HumanInputs: true, System: true}
}

func ParseTranscriptInclude(raw string) (TranscriptInclude, error) {
	if strings.TrimSpace(raw) == "" {
		return DefaultTranscriptInclude(), nil
	}
	var include TranscriptInclude
	for _, part := range strings.Split(raw, ",") {
		switch strings.TrimSpace(part) {
		case "":
			continue
		case "messages":
			include.Messages = true
		case "tools":
			include.Tools = true
		case "human_inputs":
			include.HumanInputs = true
		case "system":
			include.System = true
		default:
			return TranscriptInclude{}, fmt.Errorf("unknown transcript include: %s", part)
		}
	}
	if !include.Messages && !include.Tools && !include.HumanInputs && !include.System {
		return DefaultTranscriptInclude(), nil
	}
	return include, nil
}

// SessionTranscriptRawRow is a single merged entry from one of the three
// underlying tables before the caller groups them into turns.
type SessionTranscriptRawRow struct {
	EntryKindHint models.TranscriptEntryKind
	TurnNumber    int
	EntryTime     time.Time
	SourceRank    int
	SourceID      int64

	Message    *models.SessionMessage
	Log        *models.SessionLog
	HumanInput *models.HumanInputRequest
	Inbox      *models.ThreadInboxEntry
}

// SessionTranscriptWindow is the result returned by ListThreadWindow.
type SessionTranscriptWindow struct {
	Rows                     []SessionTranscriptRawRow
	Phases                   map[int][]models.SessionTranscriptPhase
	ThreadStatus             models.ThreadStatus
	Position                 models.TranscriptWindowPosition
	HasOlder                 bool
	HasNewer                 bool
	OlderCursor              string
	NewerCursor              string
	AnchorEntryID            string
	AnchorFound              bool
	LatestAssistantEntryID   string
	LatestAssistantMessageID int64
	LiveEdgeEntryID          string
	LiveEdgeMessageID        int64
}

// SessionTranscriptStore fetches the raw rows needed to render a transcript
// window.
type SessionTranscriptStore struct {
	db DBTX
}

type transcriptSnapshotStarter interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

// NewSessionTranscriptStore constructs a SessionTranscriptStore.
func NewSessionTranscriptStore(db DBTX) *SessionTranscriptStore {
	return &SessionTranscriptStore{db: db}
}

// normalizeTranscriptLimitTurns clamps the requested limit to the allowed range.
func normalizeTranscriptLimitTurns(n int) int {
	if n <= 0 {
		return DefaultTranscriptLimitTurns
	}
	if n > MaxTranscriptLimitTurns {
		return MaxTranscriptLimitTurns
	}
	return n
}

// listDistinctTurns fetches distinct turn numbers from all three tables for a
// thread. The where clause and ordering direction are controlled by the caller.
func (s *SessionTranscriptStore) listDistinctTurns(
	ctx context.Context,
	orgID, threadID uuid.UUID,
	include TranscriptInclude,
	extraWhere string,
	orderDir string,
	limit int,
	args pgx.NamedArgs,
) ([]int, error) {
	args["org_id"] = orgID
	args["thread_id"] = threadID
	args["limit"] = limit

	whereClause := ""
	if extraWhere != "" {
		whereClause = "AND " + extraWhere
	}

	branches := transcriptTurnSelectBranches(include)
	if len(branches) == 0 {
		return nil, nil
	}

	query := `
		SELECT DISTINCT turn_number FROM (
			` + strings.Join(branches, "\nUNION\n") + `
		) t
		WHERE turn_number >= 0
		` + whereClause + `
		ORDER BY turn_number ` + orderDir + `
		LIMIT @limit`

	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query distinct transcript turns: %w", err)
	}
	defer rows.Close()

	var turns []int
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("scan transcript turn number: %w", err)
		}
		turns = append(turns, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate transcript turn numbers: %w", err)
	}
	return turns, nil
}

func (s *SessionTranscriptStore) fetchPhasesForTurns(ctx context.Context, orgID, threadID uuid.UUID, turns []int) (map[int][]models.SessionTranscriptPhase, error) {
	result := make(map[int][]models.SessionTranscriptPhase, len(turns))
	if len(turns) == 0 {
		return result, nil
	}
	turnInts, err := int32TurnNumbers(turns)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `
		SELECT p.id, p.turn_number, p.phase_number, p.status, p.boundary_reason, p.trigger_kind,
		       p.started_at, p.completed_at, COUNT(l.id)::int AS tool_call_count
		FROM session_activity_phases p
		LEFT JOIN session_logs l
		  ON l.org_id = p.org_id AND l.activity_phase_id = p.id AND l.level = 'tool_use'
		WHERE p.org_id = @org_id AND p.thread_id = @thread_id
		  AND p.turn_number = ANY(@turns)
		GROUP BY p.id
		ORDER BY p.turn_number, p.phase_number`, pgx.NamedArgs{
		"org_id": orgID, "thread_id": threadID, "turns": turnInts,
	})
	if err != nil {
		return nil, fmt.Errorf("query transcript activity phases: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var phase models.SessionTranscriptPhase
		var turnNumber int
		if err := rows.Scan(&phase.ID, &turnNumber, &phase.PhaseNumber, &phase.Status, &phase.BoundaryReason, &phase.TriggerKind, &phase.StartedAt, &phase.CompletedAt, &phase.ToolCallCount); err != nil {
			return nil, fmt.Errorf("scan transcript activity phase: %w", err)
		}
		phase.AnchorID = "aph_" + phase.ID.String()
		result[turnNumber] = append(result[turnNumber], phase)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate transcript activity phases: %w", err)
	}
	return result, nil
}

// fetchEntriesForTurns fetches session_messages, session_logs, and
// session_human_input_requests for the given turn numbers and merges them.
func (s *SessionTranscriptStore) fetchEntriesForTurns(
	ctx context.Context,
	orgID, threadID uuid.UUID,
	include TranscriptInclude,
	turns []int,
) ([]SessionTranscriptRawRow, error) {
	if len(turns) == 0 {
		return nil, nil
	}

	turnInts, err := int32TurnNumbers(turns)
	if err != nil {
		return nil, err
	}

	var rows []SessionTranscriptRawRow

	if include.Messages {
		// --- session_messages ---
		msgQuery := `
		SELECT ` + sessionMessageSelectColumns + `
		FROM session_messages
		WHERE org_id = @org_id AND thread_id = @thread_id
		  AND turn_number = ANY(@turns)
		ORDER BY turn_number ASC, id ASC`

		msgRows, err := s.db.Query(ctx, msgQuery, pgx.NamedArgs{
			"org_id":    orgID,
			"thread_id": threadID,
			"turns":     turnInts,
		})
		if err != nil {
			return nil, fmt.Errorf("query transcript messages: %w", err)
		}
		messages, err := pgx.CollectRows(msgRows, pgx.RowToStructByNameLax[models.SessionMessage])
		if err != nil {
			return nil, fmt.Errorf("collect transcript messages: %w", err)
		}
		inboxByMessageID := make(map[int64]models.ThreadInboxEntry)
		if len(messages) > 0 {
			inboxRows, queryErr := s.db.Query(ctx, `
				SELECT `+threadInboxEntryColumnsE+`
				FROM thread_inbox_entries e
				JOIN session_messages m ON m.org_id = e.org_id AND m.id = e.message_id
				WHERE e.org_id = @org_id AND e.thread_id = @thread_id
				  AND m.turn_number = ANY(@turns)
				ORDER BY e.sequence_no`, pgx.NamedArgs{
				"org_id": orgID, "thread_id": threadID, "turns": turnInts,
			})
			if queryErr != nil {
				return nil, fmt.Errorf("query transcript inbox deliveries: %w", queryErr)
			}
			inboxEntries, queryErr := pgx.CollectRows(inboxRows, scanThreadInboxEntryRow)
			if queryErr != nil {
				return nil, fmt.Errorf("collect transcript inbox deliveries: %w", queryErr)
			}
			for _, entry := range inboxEntries {
				if entry.MessageID != 0 {
					inboxByMessageID[entry.MessageID] = entry
				}
			}
		}
		for i := range messages {
			var inbox *models.ThreadInboxEntry
			if entry, ok := inboxByMessageID[messages[i].ID]; ok {
				entryCopy := entry
				inbox = &entryCopy
			}
			rows = append(rows, SessionTranscriptRawRow{
				EntryKindHint: models.TranscriptEntryKindMessage,
				TurnNumber:    messages[i].TurnNumber,
				EntryTime:     messages[i].CreatedAt,
				SourceRank:    1,
				SourceID:      messages[i].ID,
				Message:       &messages[i],
				Inbox:         inbox,
			})
		}
	}

	if include.Tools || include.System {
		// --- session_logs ---
		logQuery := `
		SELECT id, session_id, org_id, thread_id, timestamp, level, message, metadata, turn_number, activity_phase_id
		FROM session_logs
		WHERE org_id = @org_id AND thread_id = @thread_id
		  AND turn_number = ANY(@turns)
		  AND turn_number > 0
		ORDER BY turn_number ASC, id ASC`

		logRows, err := s.db.Query(ctx, logQuery, pgx.NamedArgs{
			"org_id":    orgID,
			"thread_id": threadID,
			"turns":     turnInts,
		})
		if err != nil {
			return nil, fmt.Errorf("query transcript logs: %w", err)
		}
		logs, err := pgx.CollectRows(logRows, pgx.RowToStructByNameLax[models.SessionLog])
		if err != nil {
			return nil, fmt.Errorf("collect transcript logs: %w", err)
		}
		for i := range logs {
			kind := transcriptEntryKindForLog(logs[i])
			if (kind == models.TranscriptEntryKindToolUse || kind == models.TranscriptEntryKindToolResult) && !include.Tools {
				continue
			}
			if kind == models.TranscriptEntryKindLog && !include.System {
				continue
			}
			rows = append(rows, SessionTranscriptRawRow{
				EntryKindHint: kind,
				TurnNumber:    logs[i].TurnNumber,
				EntryTime:     logs[i].Timestamp,
				SourceRank:    2,
				SourceID:      logs[i].ID,
				Log:           &logs[i],
			})
		}
	}

	if include.HumanInputs {
		// --- session_human_input_requests ---
		hirQuery := `
		SELECT ` + humanInputRequestSelectColumns + `
		FROM session_human_input_requests
		WHERE org_id = @org_id AND thread_id = @thread_id
		  AND turn_number = ANY(@turns)
		  AND turn_number > 0
		ORDER BY turn_number ASC, created_at ASC, id ASC`

		hirRows, err := s.db.Query(ctx, hirQuery, pgx.NamedArgs{
			"org_id":    orgID,
			"thread_id": threadID,
			"turns":     turnInts,
		})
		if err != nil {
			return nil, fmt.Errorf("query transcript human input requests: %w", err)
		}
		hirs, err := pgx.CollectRows(hirRows, pgx.RowToStructByNameLax[models.HumanInputRequest])
		if err != nil {
			return nil, fmt.Errorf("collect transcript human input requests: %w", err)
		}
		for i := range hirs {
			rows = append(rows, SessionTranscriptRawRow{
				EntryKindHint: models.TranscriptEntryKindHumanInput,
				TurnNumber:    hirs[i].TurnNumber,
				EntryTime:     hirs[i].CreatedAt,
				SourceRank:    3,
				SourceID:      0,
				HumanInput:    &hirs[i],
			})
		}
	}

	// Sort by (TurnNumber ASC, EntryTime ASC, SourceRank ASC, SourceID ASC).
	sort.SliceStable(rows, func(a, b int) bool {
		ra, rb := rows[a], rows[b]
		if ra.TurnNumber != rb.TurnNumber {
			return ra.TurnNumber < rb.TurnNumber
		}
		if !ra.EntryTime.Equal(rb.EntryTime) {
			return ra.EntryTime.Before(rb.EntryTime)
		}
		if ra.SourceRank != rb.SourceRank {
			return ra.SourceRank < rb.SourceRank
		}
		return ra.SourceID < rb.SourceID
	})

	return rows, nil
}

func (s *SessionTranscriptStore) fetchWindowData(ctx context.Context, orgID, threadID uuid.UUID, include TranscriptInclude, turns []int) ([]SessionTranscriptRawRow, map[int][]models.SessionTranscriptPhase, error) {
	rows, err := s.fetchEntriesForTurns(ctx, orgID, threadID, include, turns)
	if err != nil {
		return nil, nil, err
	}
	phases, err := s.fetchPhasesForTurns(ctx, orgID, threadID, turns)
	if err != nil {
		return nil, nil, err
	}
	return rows, phases, nil
}

// ListThreadWindow implements turn-based cursor pagination for the transcript.
func (s *SessionTranscriptStore) ListThreadWindow(
	ctx context.Context,
	orgID, threadID uuid.UUID,
	opts SessionTranscriptWindowOptions,
) (SessionTranscriptWindow, error) {
	// A store built over a pgx.Tx does not expose BeginTx and already reads
	// inside its caller's transaction, so it takes the direct path.
	starter, ok := s.db.(transcriptSnapshotStarter)
	if !ok {
		return s.listThreadWindow(ctx, orgID, threadID, opts)
	}

	// Boundary transactions update the visible entry and phase lifecycle
	// together. The window is assembled by several queries, so keep them on
	// one snapshot to avoid returning opposite sides of a concurrent commit.
	tx, err := starter.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return SessionTranscriptWindow{}, fmt.Errorf("begin transcript snapshot: %w", err)
	}
	// Nothing is written, so the snapshot is released by rollback. Committing
	// instead would let a transient COMMIT error fail a request whose window
	// has already been read in full.
	defer func() { _ = tx.Rollback(ctx) }()

	snapshot := *s
	snapshot.db = tx
	return snapshot.listThreadWindow(ctx, orgID, threadID, opts)
}

func (s *SessionTranscriptStore) listThreadWindow(
	ctx context.Context,
	orgID, threadID uuid.UUID,
	opts SessionTranscriptWindowOptions,
) (SessionTranscriptWindow, error) {
	var threadStatus models.ThreadStatus
	if err := s.db.QueryRow(ctx, `
		SELECT status
		FROM session_threads
		WHERE org_id = @org_id AND id = @thread_id AND archived_at IS NULL`,
		pgx.NamedArgs{"org_id": orgID, "thread_id": threadID},
	).Scan(&threadStatus); err != nil {
		return SessionTranscriptWindow{}, fmt.Errorf("get transcript thread status: %w", err)
	}

	limit := normalizeTranscriptLimitTurns(opts.LimitTurns)
	opts.Include = normalizeTranscriptInclude(opts.Include)

	var window SessionTranscriptWindow
	var err error
	switch opts.Position {
	case models.TranscriptWindowPositionNewer:
		window, err = s.listNewerWindow(ctx, orgID, threadID, opts, limit)
	case models.TranscriptWindowPositionAround:
		window, err = s.listAroundWindow(ctx, orgID, threadID, opts, limit)
	default: // latest or older
		window, err = s.listLatestOrOlderWindow(ctx, orgID, threadID, opts, limit)
	}
	if err != nil {
		return SessionTranscriptWindow{}, err
	}
	window.ThreadStatus = threadStatus
	return window, nil
}

func (s *SessionTranscriptStore) listLatestOrOlderWindow(
	ctx context.Context,
	orgID, threadID uuid.UUID,
	opts SessionTranscriptWindowOptions,
	limit int,
) (SessionTranscriptWindow, error) {
	extraWhere := ""
	args := pgx.NamedArgs{}
	if opts.Before != nil {
		extraWhere = "turn_number < @before_turn"
		args["before_turn"] = opts.Before.TurnNumber
	}

	// Fetch DESC, limit+1 to detect hasOlder.
	descTurns, err := s.listDistinctTurns(ctx, orgID, threadID, opts.Include, extraWhere, "DESC", limit+1, args)
	if err != nil {
		return SessionTranscriptWindow{}, err
	}

	hasOlder := len(descTurns) > limit
	if hasOlder {
		descTurns = descTurns[:limit]
	}

	if len(descTurns) == 0 {
		return SessionTranscriptWindow{}, nil
	}

	// Reverse to ASC for fetching and presentation.
	turns := reversedInts(descTurns)

	rows, phases, err := s.fetchWindowData(ctx, orgID, threadID, opts.Include, turns)
	if err != nil {
		return SessionTranscriptWindow{}, err
	}

	window := SessionTranscriptWindow{
		Rows:     rows,
		Phases:   phases,
		Position: positionForLatestOrOlder(opts),
		HasOlder: hasOlder,
	}
	if hasOlder {
		cursor := models.TranscriptCursor{
			Version:    1,
			OrgID:      orgID,
			ThreadID:   threadID,
			TurnNumber: turns[0], // oldest turn in window
			EntryID:    firstTranscriptAnchorIDForTurn(rows, phases, turns[0]),
		}
		encoded, err := cursor.Encode()
		if err != nil {
			return SessionTranscriptWindow{}, fmt.Errorf("encode older cursor: %w", err)
		}
		window.OlderCursor = encoded
	}
	if err := s.populateAnchorMetadata(ctx, orgID, threadID, &window); err != nil {
		return SessionTranscriptWindow{}, err
	}
	return window, nil
}

func (s *SessionTranscriptStore) listNewerWindow(
	ctx context.Context,
	orgID, threadID uuid.UUID,
	opts SessionTranscriptWindowOptions,
	limit int,
) (SessionTranscriptWindow, error) {
	extraWhere := ""
	args := pgx.NamedArgs{}
	if opts.After != nil {
		extraWhere = "turn_number > @after_turn"
		args["after_turn"] = opts.After.TurnNumber
	}

	// Fetch ASC, limit+1 to detect hasNewer.
	ascTurns, err := s.listDistinctTurns(ctx, orgID, threadID, opts.Include, extraWhere, "ASC", limit+1, args)
	if err != nil {
		return SessionTranscriptWindow{}, err
	}

	hasNewer := len(ascTurns) > limit
	if hasNewer {
		ascTurns = ascTurns[:limit]
	}

	if len(ascTurns) == 0 {
		return SessionTranscriptWindow{}, nil
	}

	rows, phases, err := s.fetchWindowData(ctx, orgID, threadID, opts.Include, ascTurns)
	if err != nil {
		return SessionTranscriptWindow{}, err
	}

	window := SessionTranscriptWindow{
		Rows:     rows,
		Phases:   phases,
		Position: models.TranscriptWindowPositionNewer,
		HasNewer: hasNewer,
	}
	if hasNewer {
		cursor := models.TranscriptCursor{
			Version:    1,
			OrgID:      orgID,
			ThreadID:   threadID,
			TurnNumber: ascTurns[len(ascTurns)-1], // newest turn in window
			EntryID:    lastTranscriptAnchorIDForTurn(rows, phases, ascTurns[len(ascTurns)-1]),
		}
		encoded, err := cursor.Encode()
		if err != nil {
			return SessionTranscriptWindow{}, fmt.Errorf("encode newer cursor: %w", err)
		}
		window.NewerCursor = encoded
	}
	if err := s.populateAnchorMetadata(ctx, orgID, threadID, &window); err != nil {
		return SessionTranscriptWindow{}, err
	}
	return window, nil
}

func (s *SessionTranscriptStore) listAroundWindow(
	ctx context.Context,
	orgID, threadID uuid.UUID,
	opts SessionTranscriptWindowOptions,
	limit int,
) (SessionTranscriptWindow, error) {
	anchorTurn, found, err := s.resolveAnchorTurn(ctx, orgID, threadID, opts)
	if err != nil {
		return SessionTranscriptWindow{}, err
	}
	if !found {
		// Anchor not found — fall back to latest.
		window, err := s.listLatestOrOlderWindow(ctx, orgID, threadID, SessionTranscriptWindowOptions{
			LimitTurns: opts.LimitTurns,
			Include:    opts.Include,
		}, limit)
		window.AnchorEntryID = requestedTranscriptAnchorEntryID(opts)
		window.AnchorFound = false
		return window, err
	}

	olderLimit := limit / 2
	newerLimit := limit - olderLimit - 1
	if newerLimit < 0 {
		newerLimit = 0
	}

	// Older turns (turn < anchorTurn), DESC.
	olderTurnsDesc, err := s.listDistinctTurns(ctx, orgID, threadID, opts.Include,
		"turn_number < @anchor_turn", "DESC", olderLimit+1,
		pgx.NamedArgs{"anchor_turn": anchorTurn},
	)
	if err != nil {
		return SessionTranscriptWindow{}, err
	}
	hasOlder := len(olderTurnsDesc) > olderLimit
	if hasOlder {
		olderTurnsDesc = olderTurnsDesc[:olderLimit]
	}
	olderTurns := reversedInts(olderTurnsDesc) // now ASC

	// Newer turns (turn > anchorTurn), ASC.
	newerTurnsAsc, err := s.listDistinctTurns(ctx, orgID, threadID, opts.Include,
		"turn_number > @anchor_turn", "ASC", newerLimit+1,
		pgx.NamedArgs{"anchor_turn": anchorTurn},
	)
	if err != nil {
		return SessionTranscriptWindow{}, err
	}
	hasNewer := len(newerTurnsAsc) > newerLimit
	if hasNewer {
		newerTurnsAsc = newerTurnsAsc[:newerLimit]
	}

	// Combine: older + anchor + newer.
	var allTurns []int
	allTurns = append(allTurns, olderTurns...)
	allTurns = append(allTurns, anchorTurn)
	allTurns = append(allTurns, newerTurnsAsc...)

	rows, phases, err := s.fetchWindowData(ctx, orgID, threadID, opts.Include, allTurns)
	if err != nil {
		return SessionTranscriptWindow{}, err
	}

	window := SessionTranscriptWindow{
		Rows:          rows,
		Phases:        phases,
		Position:      models.TranscriptWindowPositionAround,
		HasOlder:      hasOlder,
		HasNewer:      hasNewer,
		AnchorEntryID: requestedTranscriptAnchorEntryID(opts),
		AnchorFound:   true,
	}
	if window.AnchorEntryID == "" {
		window.AnchorEntryID = firstTranscriptAnchorIDForTurn(rows, phases, anchorTurn)
	}
	if hasOlder && len(olderTurns) > 0 {
		cursor := models.TranscriptCursor{
			Version:    1,
			OrgID:      orgID,
			ThreadID:   threadID,
			TurnNumber: olderTurns[0],
			EntryID:    firstTranscriptAnchorIDForTurn(rows, phases, olderTurns[0]),
		}
		encoded, err := cursor.Encode()
		if err != nil {
			return SessionTranscriptWindow{}, fmt.Errorf("encode older cursor: %w", err)
		}
		window.OlderCursor = encoded
	}
	if hasNewer && len(newerTurnsAsc) > 0 {
		cursor := models.TranscriptCursor{
			Version:    1,
			OrgID:      orgID,
			ThreadID:   threadID,
			TurnNumber: newerTurnsAsc[len(newerTurnsAsc)-1],
			EntryID:    lastTranscriptAnchorIDForTurn(rows, phases, newerTurnsAsc[len(newerTurnsAsc)-1]),
		}
		encoded, err := cursor.Encode()
		if err != nil {
			return SessionTranscriptWindow{}, fmt.Errorf("encode newer cursor: %w", err)
		}
		window.NewerCursor = encoded
	}
	if err := s.populateAnchorMetadata(ctx, orgID, threadID, &window); err != nil {
		return SessionTranscriptWindow{}, err
	}
	return window, nil
}

// resolveAnchorTurn determines the turn number for an around request.
// Returns (turn, found, error).
func (s *SessionTranscriptStore) resolveAnchorTurn(
	ctx context.Context,
	orgID, threadID uuid.UUID,
	opts SessionTranscriptWindowOptions,
) (int, bool, error) {
	// Priority 1: anchor_entry_id.
	if opts.AnchorEntryID != "" {
		return s.resolveAnchorEntryID(ctx, orgID, threadID, opts.AnchorEntryID)
	}
	// Priority 2: anchor_message_id.
	if opts.AnchorMessageID > 0 {
		return s.resolveAnchorMessageID(ctx, orgID, threadID, opts.AnchorMessageID)
	}
	// Priority 3: anchor_turn_number.
	if opts.AnchorTurnNumber != nil {
		return *opts.AnchorTurnNumber, true, nil
	}
	return 0, false, nil
}

// resolveAnchorEntryID parses an entry ID like "msg_N", "log_N", "tuse_N",
// "tres_N", or "hiq_<uuid>" and looks up the corresponding turn number.
func (s *SessionTranscriptStore) resolveAnchorEntryID(
	ctx context.Context,
	orgID, threadID uuid.UUID,
	entryID string,
) (int, bool, error) {
	switch {
	case strings.HasPrefix(entryID, "aph_"):
		id, err := uuid.Parse(strings.TrimPrefix(entryID, "aph_"))
		if err != nil {
			return 0, false, nil
		}
		var turnNumber int
		if err := s.db.QueryRow(ctx,
			`SELECT turn_number FROM session_activity_phases WHERE org_id = @org_id AND thread_id = @thread_id AND id = @id`,
			pgx.NamedArgs{"org_id": orgID, "thread_id": threadID, "id": id},
		).Scan(&turnNumber); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, false, nil
			}
			return 0, false, fmt.Errorf("resolve activity phase anchor %q: %w", entryID, err)
		}
		return turnNumber, true, nil

	case strings.HasPrefix(entryID, "msg_"):
		rawID := strings.TrimPrefix(entryID, "msg_")
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil {
			return 0, false, nil
		}
		var turnNumber int
		if err := s.db.QueryRow(ctx,
			`SELECT turn_number FROM session_messages WHERE org_id = @org_id AND thread_id = @thread_id AND id = @id`,
			pgx.NamedArgs{"org_id": orgID, "thread_id": threadID, "id": id},
		).Scan(&turnNumber); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, false, nil
			}
			return 0, false, fmt.Errorf("resolve anchor message entry %q: %w", entryID, err)
		}
		return turnNumber, true, nil

	case strings.HasPrefix(entryID, "log_") ||
		strings.HasPrefix(entryID, "tuse_") ||
		strings.HasPrefix(entryID, "tres_"):
		var rawID string
		switch {
		case strings.HasPrefix(entryID, "log_"):
			rawID = strings.TrimPrefix(entryID, "log_")
		case strings.HasPrefix(entryID, "tuse_"):
			rawID = strings.TrimPrefix(entryID, "tuse_")
		default:
			rawID = strings.TrimPrefix(entryID, "tres_")
		}
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil {
			return 0, false, nil
		}
		var turnNumber int
		if err := s.db.QueryRow(ctx,
			`SELECT turn_number FROM session_logs WHERE org_id = @org_id AND thread_id = @thread_id AND id = @id`,
			pgx.NamedArgs{"org_id": orgID, "thread_id": threadID, "id": id},
		).Scan(&turnNumber); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, false, nil
			}
			return 0, false, fmt.Errorf("resolve anchor log entry %q: %w", entryID, err)
		}
		return turnNumber, true, nil

	case strings.HasPrefix(entryID, "hiq_"):
		rawUUID := strings.TrimPrefix(entryID, "hiq_")
		id, err := uuid.Parse(rawUUID)
		if err != nil {
			return 0, false, nil
		}
		var turnNumber int
		if err := s.db.QueryRow(ctx,
			`SELECT turn_number FROM session_human_input_requests WHERE org_id = @org_id AND thread_id = @thread_id AND id = @id`,
			pgx.NamedArgs{"org_id": orgID, "thread_id": threadID, "id": id},
		).Scan(&turnNumber); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, false, nil
			}
			return 0, false, fmt.Errorf("resolve anchor human input entry %q: %w", entryID, err)
		}
		return turnNumber, true, nil

	default:
		return 0, false, nil
	}
}

// resolveAnchorMessageID looks up the turn number for a given message ID.
func (s *SessionTranscriptStore) resolveAnchorMessageID(
	ctx context.Context,
	orgID, threadID uuid.UUID,
	messageID int64,
) (int, bool, error) {
	var turnNumber int
	if err := s.db.QueryRow(ctx,
		`SELECT turn_number FROM session_messages WHERE org_id = @org_id AND thread_id = @thread_id AND id = @id`,
		pgx.NamedArgs{"org_id": orgID, "thread_id": threadID, "id": messageID},
	).Scan(&turnNumber); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("resolve anchor message id %d: %w", messageID, err)
	}
	return turnNumber, true, nil
}

func (s *SessionTranscriptStore) populateAnchorMetadata(ctx context.Context, orgID, threadID uuid.UUID, window *SessionTranscriptWindow) error {
	latestEntryID, latestMsgID, liveEntryID, liveMsgID, err := s.getTranscriptAnchorMetadata(ctx, orgID, threadID)
	if err != nil {
		return err
	}
	window.LatestAssistantEntryID = latestEntryID
	window.LatestAssistantMessageID = latestMsgID
	window.LiveEdgeEntryID = liveEntryID
	window.LiveEdgeMessageID = liveMsgID
	return nil
}

func (s *SessionTranscriptStore) getTranscriptAnchorMetadata(ctx context.Context, orgID, threadID uuid.UUID) (string, int64, string, int64, error) {
	var latestAssistant sql.NullInt64
	err := s.db.QueryRow(ctx, `
		SELECT id
		FROM session_messages
		WHERE org_id = @org_id AND thread_id = @thread_id AND role = 'assistant'
		ORDER BY turn_number DESC, created_at DESC, id DESC
		LIMIT 1`,
		pgx.NamedArgs{"org_id": orgID, "thread_id": threadID},
	).Scan(&latestAssistant)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", 0, "", 0, fmt.Errorf("query transcript latest assistant metadata: %w", err)
	}

	var latestAssistantEntryID string
	var latestAssistantMessageID int64
	if latestAssistant.Valid {
		latestAssistantMessageID = latestAssistant.Int64
		latestAssistantEntryID = "msg_" + strconv.FormatInt(latestAssistant.Int64, 10)
	}

	type liveRow struct {
		kind      models.TranscriptEntryKind
		sourceID  int64
		messageID sql.NullInt64
		hiqUUID   sql.NullString
		level     models.SessionLogLevel
		metadata  json.RawMessage
	}
	var live liveRow
	err = s.db.QueryRow(ctx, `
		SELECT entry_kind, source_id, message_id, hiq_uuid, level, metadata
		FROM (
			SELECT 'message'::text AS entry_kind, id AS source_id, id AS message_id, NULL::text AS hiq_uuid, ''::text AS level, NULL::jsonb AS metadata,
			       turn_number, created_at AS entry_time, 1 AS source_rank
			FROM session_messages
			WHERE org_id = @org_id AND thread_id = @thread_id
			UNION ALL
			SELECT 'log'::text AS entry_kind, id AS source_id, NULL::bigint AS message_id, NULL::text AS hiq_uuid, level::text AS level, metadata,
			       turn_number, timestamp AS entry_time, 2 AS source_rank
			FROM session_logs
			WHERE org_id = @org_id AND thread_id = @thread_id
			UNION ALL
			SELECT 'human_input'::text AS entry_kind, 0 AS source_id, NULL::bigint AS message_id, id::text AS hiq_uuid, ''::text AS level, NULL::jsonb AS metadata,
			       turn_number, created_at AS entry_time, 3 AS source_rank
			FROM session_human_input_requests
			WHERE org_id = @org_id AND thread_id = @thread_id
		) entries
		WHERE turn_number > 0
		ORDER BY turn_number DESC, entry_time DESC, source_rank DESC, source_id DESC
		LIMIT 1`,
		pgx.NamedArgs{"org_id": orgID, "thread_id": threadID},
	).Scan(&live.kind, &live.sourceID, &live.messageID, &live.hiqUUID, &live.level, &live.metadata)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return latestAssistantEntryID, latestAssistantMessageID, "", 0, nil
		}
		return "", 0, "", 0, fmt.Errorf("query transcript live edge metadata: %w", err)
	}

	var liveEntryID string
	switch live.kind {
	case models.TranscriptEntryKindLog:
		live.kind = transcriptEntryKindForLog(models.SessionLog{ID: live.sourceID, Level: live.level, Metadata: live.metadata})
		liveEntryID = transcriptEntryID(live.kind, live.sourceID)
	case models.TranscriptEntryKindHumanInput:
		if live.hiqUUID.Valid {
			liveEntryID = "hiq_" + live.hiqUUID.String
		}
	default:
		liveEntryID = transcriptEntryID(live.kind, live.sourceID)
	}

	var liveMessageID int64
	if live.messageID.Valid {
		liveMessageID = live.messageID.Int64
	}
	return latestAssistantEntryID, latestAssistantMessageID, liveEntryID, liveMessageID, nil
}

func positionForLatestOrOlder(opts SessionTranscriptWindowOptions) models.TranscriptWindowPosition {
	if opts.Before != nil {
		return models.TranscriptWindowPositionOlder
	}
	return models.TranscriptWindowPositionLatest
}

func requestedTranscriptAnchorEntryID(opts SessionTranscriptWindowOptions) string {
	if strings.TrimSpace(opts.AnchorEntryID) != "" {
		return strings.TrimSpace(opts.AnchorEntryID)
	}
	if opts.AnchorMessageID > 0 {
		return "msg_" + strconv.FormatInt(opts.AnchorMessageID, 10)
	}
	return ""
}

func firstTranscriptEntryIDForTurn(rows []SessionTranscriptRawRow, turn int) string {
	for _, row := range rows {
		if row.TurnNumber == turn {
			return transcriptRawRowEntryID(row)
		}
	}
	return ""
}

func lastTranscriptEntryIDForTurn(rows []SessionTranscriptRawRow, turn int) string {
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].TurnNumber == turn {
			return transcriptRawRowEntryID(rows[i])
		}
	}
	return ""
}

func firstTranscriptAnchorIDForTurn(rows []SessionTranscriptRawRow, phases map[int][]models.SessionTranscriptPhase, turn int) string {
	if entryID := firstTranscriptEntryIDForTurn(rows, turn); entryID != "" {
		return entryID
	}
	if turnPhases := phases[turn]; len(turnPhases) > 0 {
		return turnPhases[0].AnchorID
	}
	return ""
}

func lastTranscriptAnchorIDForTurn(rows []SessionTranscriptRawRow, phases map[int][]models.SessionTranscriptPhase, turn int) string {
	if entryID := lastTranscriptEntryIDForTurn(rows, turn); entryID != "" {
		return entryID
	}
	if turnPhases := phases[turn]; len(turnPhases) > 0 {
		return turnPhases[len(turnPhases)-1].AnchorID
	}
	return ""
}

func transcriptRawRowEntryID(row SessionTranscriptRawRow) string {
	if row.Message != nil {
		return transcriptEntryID(models.TranscriptEntryKindMessage, row.Message.ID)
	}
	if row.Log != nil {
		return transcriptEntryID(row.EntryKindHint, row.Log.ID)
	}
	if row.HumanInput != nil {
		return "hiq_" + row.HumanInput.ID.String()
	}
	return ""
}

func transcriptEntryID(kind models.TranscriptEntryKind, sourceID int64) string {
	switch kind {
	case models.TranscriptEntryKindMessage:
		return "msg_" + strconv.FormatInt(sourceID, 10)
	case models.TranscriptEntryKindToolUse:
		return "tuse_" + strconv.FormatInt(sourceID, 10)
	case models.TranscriptEntryKindToolResult:
		return "tres_" + strconv.FormatInt(sourceID, 10)
	case models.TranscriptEntryKindLog:
		return "log_" + strconv.FormatInt(sourceID, 10)
	default:
		return ""
	}
}

func transcriptEntryKindForLog(log models.SessionLog) models.TranscriptEntryKind {
	if log.Level == models.SessionLogLevelToolUse {
		return models.TranscriptEntryKindToolUse
	}
	var meta struct {
		Type string `json:"type"`
	}
	if len(log.Metadata) > 0 && json.Unmarshal(log.Metadata, &meta) == nil && meta.Type == "tool_result" {
		return models.TranscriptEntryKindToolResult
	}
	return models.TranscriptEntryKindLog
}

// reversedInts returns a new slice with the elements in reverse order.
func reversedInts(in []int) []int {
	out := make([]int, len(in))
	for i, v := range in {
		out[len(in)-1-i] = v
	}
	return out
}

func int32TurnNumbers(turns []int) ([]int32, error) {
	turnInts := make([]int32, len(turns))
	for i, turn := range turns {
		if turn < math.MinInt32 || turn > math.MaxInt32 {
			return nil, fmt.Errorf("transcript turn number %d is outside int32 range", turn)
		}
		turnInts[i] = int32(turn)
	}
	return turnInts, nil
}

func normalizeTranscriptInclude(include TranscriptInclude) TranscriptInclude {
	if !include.Messages && !include.Tools && !include.HumanInputs && !include.System {
		return DefaultTranscriptInclude()
	}
	return include
}

func transcriptTurnSelectBranches(include TranscriptInclude) []string {
	branches := []string{`
		SELECT turn_number FROM session_activity_phases
			WHERE org_id = @org_id AND thread_id = @thread_id`}
	if include.Messages {
		branches = append(branches, `
			SELECT turn_number FROM session_messages
				WHERE org_id = @org_id AND thread_id = @thread_id`)
	}
	if include.Tools || include.System {
		logFilter := " AND turn_number > 0"
		toolLogPredicate := "(level = 'tool_use' OR metadata->>'type' = 'tool_result')"
		switch {
		case include.Tools && !include.System:
			logFilter += " AND " + toolLogPredicate
		case include.System && !include.Tools:
			logFilter += " AND NOT " + toolLogPredicate
		}
		branches = append(branches, `
			SELECT turn_number FROM session_logs
				WHERE org_id = @org_id AND thread_id = @thread_id`+logFilter)
	}
	if include.HumanInputs {
		branches = append(branches, `
			SELECT turn_number FROM session_human_input_requests
				WHERE org_id = @org_id AND thread_id = @thread_id AND turn_number > 0`)
	}
	return branches
}
