package db

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestTranscriptAnchorResolutionOwnershipAndFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, prefix, table string
		uuidID, legacy      bool
	}{
		{name: "message", prefix: "msg_", table: "session_messages"},
		{name: "system log", prefix: "log_", table: "session_logs"},
		{name: "tool use", prefix: "tuse_", table: "session_logs"},
		{name: "tool result", prefix: "tres_", table: "session_logs"},
		{name: "human input", prefix: "hiq_", table: "session_human_input_requests", uuidID: true},
		{name: "activity phase", prefix: "aph_", table: "session_activity_phases", uuidID: true},
		{name: "legacy message", table: "session_messages", legacy: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			outcomes := []struct {
				name             string
				missing, failure bool
			}{{name: "found"}, {name: "missing or foreign tenant", missing: true}, {name: "database failure", failure: true}}
			for _, outcome := range outcomes {
				t.Run(outcome.name, func(t *testing.T) {
					t.Parallel()
					mock, err := pgxmock.NewPool()
					require.NoError(t, err, "anchor test should have an isolated database mock")
					t.Cleanup(mock.Close)
					orgID, threadID := uuid.New(), uuid.New()
					var id any = int64(42)
					entryID := tt.prefix + "42"
					if tt.uuidID {
						value := uuid.New()
						id = value
						entryID = tt.prefix + value.String()
					}
					q := mock.ExpectQuery(`SELECT turn_number FROM ` + tt.table + ` WHERE org_id = @org_id AND thread_id = @thread_id AND id = @id`).WithArgs(pgx.NamedArgs{"org_id": orgID, "thread_id": threadID, "id": id})
					fault := errors.New("anchor database unavailable")
					if outcome.failure {
						q.WillReturnError(fault)
					} else {
						rows := pgxmock.NewRows([]string{"turn_number"})
						if !outcome.missing {
							rows.AddRow(7)
						}
						q.WillReturnRows(rows)
					}
					opts := SessionTranscriptWindowOptions{AnchorEntryID: entryID, AnchorMessageID: 999, AnchorTurnNumber: new(99)}
					if tt.legacy {
						opts.AnchorEntryID = ""
						opts.AnchorMessageID = 42
					}
					turn, found, err := NewSessionTranscriptStore(mock).resolveAnchorTurn(t.Context(), orgID, threadID, opts)
					if outcome.failure {
						require.ErrorIs(t, err, fault, "anchor resolution should preserve dependency failures")
					} else {
						require.NoError(t, err, "absent anchors should allow a safe latest-window fallback")
					}
					expectedTurn := 7
					if outcome.missing || outcome.failure {
						expectedTurn = 0
					}
					require.Equal(t, expectedTurn, turn, "entry lookup should take precedence over legacy message and numeric turn anchors")
					require.Equal(t, !outcome.missing && !outcome.failure, found, "only an owned persisted anchor should be found")
					require.NoError(t, mock.ExpectationsWereMet(), "anchor lookup should require both tenant and thread ownership")
				})
			}
		})
	}
}

func TestTranscriptInvalidAnchorsAvoidDatabase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, entry string
		turn        *int
		expected    int
		found       bool
	}{
		{name: "invalid message id", entry: "msg_bad"}, {name: "invalid tool id", entry: "tuse_bad"},
		{name: "invalid human input id", entry: "hiq_bad"}, {name: "invalid activity phase id", entry: "aph_bad"},
		{name: "unknown prefix", entry: "unknown_12"}, {name: "no anchor"},
		{name: "explicit turn zero", turn: new(0), found: true}, {name: "explicit positive turn", turn: new(7), expected: 7, found: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "invalid anchor test should have its own mock")
			t.Cleanup(mock.Close)
			turn, found, err := NewSessionTranscriptStore(mock).resolveAnchorTurn(t.Context(), uuid.New(), uuid.New(), SessionTranscriptWindowOptions{AnchorEntryID: tt.entry, AnchorTurnNumber: tt.turn})
			require.NoError(t, err, "malformed anchor IDs should be treated as absent")
			require.Equal(t, tt.expected, turn, "numeric anchors should be preserved exactly")
			require.Equal(t, tt.found, found, "only a supplied numeric anchor should resolve without a query")
			require.NoError(t, mock.ExpectationsWereMet(), "invalid anchors should not issue an unscoped database lookup")
		})
	}
}

func TestTranscriptPaginationWindowContracts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                               string
		position                           models.TranscriptWindowPosition
		turns                              []int
		selected                           []int32
		older, newer, empty, missingAnchor bool
	}{
		{name: "latest bounded", turns: []int{5, 4, 3}, selected: []int32{4, 5}, older: true},
		{name: "older bounded", position: models.TranscriptWindowPositionOlder, turns: []int{3, 2, 1}, selected: []int32{2, 3}, older: true},
		{name: "newer bounded", position: models.TranscriptWindowPositionNewer, turns: []int{3, 4, 5}, selected: []int32{3, 4}, newer: true},
		{name: "newer end reached", position: models.TranscriptWindowPositionNewer, turns: []int{3}, selected: []int32{3}},
		{name: "latest empty", empty: true}, {name: "newer empty", position: models.TranscriptWindowPositionNewer, empty: true},
		{name: "around both edges", position: models.TranscriptWindowPositionAround, selected: []int32{2, 3, 4}, older: true, newer: true},
		{name: "unknown anchor falls back to latest", position: models.TranscriptWindowPositionAround, turns: []int{5, 4, 3}, selected: []int32{4, 5}, older: true, missingAnchor: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "window test should have its own mock")
			t.Cleanup(mock.Close)
			orgID, threadID, sessionID := uuid.New(), uuid.New(), uuid.New()
			args := pgx.NamedArgs{"org_id": orgID, "thread_id": threadID, "limit": 3}
			opts := SessionTranscriptWindowOptions{Position: tt.position, LimitTurns: 2, Include: TranscriptInclude{System: true}}
			pattern := `SELECT DISTINCT turn_number.*ORDER BY turn_number DESC LIMIT @limit`
			if tt.position == models.TranscriptWindowPositionOlder {
				opts.Before = &models.TranscriptCursor{TurnNumber: 4}
				args["before_turn"] = 4
				pattern = `SELECT DISTINCT turn_number.*turn_number < @before_turn ORDER BY turn_number DESC LIMIT @limit`
			}
			if tt.position == models.TranscriptWindowPositionNewer {
				opts.After = &models.TranscriptCursor{TurnNumber: 2}
				args["after_turn"] = 2
				pattern = `SELECT DISTINCT turn_number.*turn_number > @after_turn ORDER BY turn_number ASC LIMIT @limit`
			}
			expected := SessionTranscriptWindow{}
			if tt.position == models.TranscriptWindowPositionAround && !tt.missingAnchor {
				opts.LimitTurns = 3
				opts.AnchorTurnNumber = new(3)
				mock.ExpectQuery(`SELECT DISTINCT turn_number.*turn_number < @anchor_turn ORDER BY turn_number DESC LIMIT @limit`).WithArgs(pgx.NamedArgs{"org_id": orgID, "thread_id": threadID, "limit": 2, "anchor_turn": 3}).WillReturnRows(pgxmock.NewRows([]string{"turn_number"}).AddRow(2).AddRow(1))
				mock.ExpectQuery(`SELECT DISTINCT turn_number.*turn_number > @anchor_turn ORDER BY turn_number ASC LIMIT @limit`).WithArgs(pgx.NamedArgs{"org_id": orgID, "thread_id": threadID, "limit": 2, "anchor_turn": 3}).WillReturnRows(pgxmock.NewRows([]string{"turn_number"}).AddRow(4).AddRow(5))
			} else {
				if tt.missingAnchor {
					opts.AnchorEntryID = "msg_bad"
				}
				rows := pgxmock.NewRows([]string{"turn_number"})
				for _, turn := range tt.turns {
					rows.AddRow(turn)
				}
				mock.ExpectQuery(pattern).WithArgs(args).WillReturnRows(rows)
			}
			if !tt.empty {
				stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
				logs := pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number", "activity_phase_id"})
				var entries []SessionTranscriptRawRow
				for _, turn := range tt.selected {
					log := models.SessionLog{ID: int64(turn) * 10, SessionID: sessionID, OrgID: orgID, ThreadID: &threadID, Timestamp: stamp, Level: models.SessionLogLevelInfo, Message: "turn " + strconv.Itoa(int(turn)), Metadata: json.RawMessage(`{}`), TurnNumber: int(turn)}
					logs.AddRow(log.ID, sessionID, orgID, &threadID, stamp, log.Level, log.Message, log.Metadata, log.TurnNumber, nil)
					entries = append(entries, SessionTranscriptRawRow{EntryKindHint: models.TranscriptEntryKindLog, TurnNumber: int(turn), EntryTime: stamp, SourceRank: 2, SourceID: log.ID, Log: &log})
				}
				windowArgs := pgx.NamedArgs{"org_id": orgID, "thread_id": threadID, "turns": tt.selected}
				mock.ExpectQuery(`FROM session_logs WHERE org_id = @org_id AND thread_id = @thread_id.*turn_number = ANY\(@turns\)`).WithArgs(windowArgs).WillReturnRows(logs)
				mock.ExpectQuery(`FROM session_activity_phases p.*WHERE p.org_id = @org_id AND p.thread_id = @thread_id`).WithArgs(windowArgs).WillReturnRows(pgxmock.NewRows([]string{"id", "turn_number", "phase_number", "status", "boundary_reason", "trigger_kind", "started_at", "completed_at", "tool_call_count"}))
				mock.ExpectQuery(`FROM session_messages WHERE org_id = @org_id AND thread_id = @thread_id AND role = 'assistant'`).WithArgs(pgx.NamedArgs{"org_id": orgID, "thread_id": threadID}).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int64(91)))
				mock.ExpectQuery(`SELECT entry_kind, source_id, message_id, hiq_uuid, level, metadata`).WithArgs(pgx.NamedArgs{"org_id": orgID, "thread_id": threadID}).WillReturnRows(pgxmock.NewRows([]string{"entry_kind", "source_id", "message_id", "hiq_uuid", "level", "metadata"}).AddRow(models.TranscriptEntryKindLog, int64(99), nil, nil, models.SessionLogLevelInfo, json.RawMessage(`{}`)))
				position := tt.position
				if position == "" || tt.missingAnchor {
					position = models.TranscriptWindowPositionLatest
				}
				expected = SessionTranscriptWindow{Rows: entries, Phases: map[int][]models.SessionTranscriptPhase{}, Position: position, HasOlder: tt.older, HasNewer: tt.newer, LatestAssistantEntryID: "msg_91", LatestAssistantMessageID: 91, LiveEdgeEntryID: "log_99"}
				if tt.older {
					turn := int(tt.selected[0])
					expected.OlderCursor, err = (models.TranscriptCursor{Version: 1, OrgID: orgID, ThreadID: threadID, TurnNumber: turn, EntryID: "log_" + strconv.Itoa(turn*10)}).Encode()
					require.NoError(t, err, "expected older cursor should serialize")
				}
				if tt.newer {
					turn := int(tt.selected[len(tt.selected)-1])
					expected.NewerCursor, err = (models.TranscriptCursor{Version: 1, OrgID: orgID, ThreadID: threadID, TurnNumber: turn, EntryID: "log_" + strconv.Itoa(turn*10)}).Encode()
					require.NoError(t, err, "expected newer cursor should serialize")
				}
				if tt.position == models.TranscriptWindowPositionAround && !tt.missingAnchor {
					expected.AnchorFound = true
					expected.AnchorEntryID = "log_30"
				}
				if tt.missingAnchor {
					expected.AnchorEntryID = "msg_bad"
				}
			}
			actual, err := NewSessionTranscriptStore(mock).ListThreadWindow(t.Context(), orgID, threadID, opts)
			require.NoError(t, err, "pagination should return a bounded window or a safe latest fallback")
			require.Equal(t, expected, actual, "window should preserve exact entries, edge flags, scoped cursors, and independent live metadata")
			require.NoError(t, mock.ExpectationsWereMet(), "pagination should scope every query to the same tenant and thread")
		})
	}
}

func TestTranscriptWindowDependencyFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, stage string
		newer       bool
	}{
		{name: "turn query", stage: "turns"}, {name: "turn scan", stage: "scan"}, {name: "turn iteration", stage: "iteration"},
		{name: "entry query", stage: "entries"}, {name: "phase query", stage: "phases"},
		{name: "assistant metadata", stage: "assistant"}, {name: "live metadata", stage: "live"},
		{name: "newer turn query", stage: "turns", newer: true}, {name: "newer phase query", stage: "phases", newer: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "failure test should create an isolated mock")
			t.Cleanup(mock.Close)
			orgID, threadID := uuid.New(), uuid.New()
			fault := errors.New("transcript dependency failed")
			q := mock.ExpectQuery(`SELECT DISTINCT turn_number`).WithArgs(pgx.NamedArgs{"org_id": orgID, "thread_id": threadID, "limit": 3})
			switch tt.stage {
			case "turns":
				q.WillReturnError(fault)
			case "scan":
				q.WillReturnRows(pgxmock.NewRows([]string{"turn_number"}).AddRow("invalid"))
			case "iteration":
				q.WillReturnRows(pgxmock.NewRows([]string{"turn_number"}).AddRow(1).RowError(0, fault))
			default:
				q.WillReturnRows(pgxmock.NewRows([]string{"turn_number"}).AddRow(1))
				args := pgx.NamedArgs{"org_id": orgID, "thread_id": threadID, "turns": []int32{1}}
				entries := mock.ExpectQuery(`FROM session_logs WHERE org_id = @org_id AND thread_id = @thread_id`).WithArgs(args)
				if tt.stage == "entries" {
					entries.WillReturnError(fault)
				} else {
					entries.WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))
					phases := mock.ExpectQuery(`FROM session_activity_phases p`).WithArgs(args)
					if tt.stage == "phases" {
						phases.WillReturnError(fault)
					} else {
						phases.WillReturnRows(pgxmock.NewRows([]string{"id", "turn_number", "phase_number", "status", "boundary_reason", "trigger_kind", "started_at", "completed_at", "tool_call_count"}))
						assistant := mock.ExpectQuery(`FROM session_messages.*role = 'assistant'`).WithArgs(pgx.NamedArgs{"org_id": orgID, "thread_id": threadID})
						if tt.stage == "assistant" {
							assistant.WillReturnError(fault)
						} else {
							assistant.WillReturnRows(pgxmock.NewRows([]string{"id"}))
							mock.ExpectQuery(`SELECT entry_kind, source_id, message_id, hiq_uuid, level, metadata`).WithArgs(pgx.NamedArgs{"org_id": orgID, "thread_id": threadID}).WillReturnError(fault)
						}
					}
				}
			}
			opts := SessionTranscriptWindowOptions{Include: TranscriptInclude{System: true}, LimitTurns: 2}
			if tt.newer {
				opts.Position = models.TranscriptWindowPositionNewer
			}
			actual, err := NewSessionTranscriptStore(mock).ListThreadWindow(t.Context(), orgID, threadID, opts)
			if tt.stage == "scan" {
				require.ErrorContains(t, err, "scan transcript turn number", "invalid turn values should fail the window")
			} else {
				require.ErrorIs(t, err, fault, "window should preserve the failed dependency's cause")
			}
			require.Equal(t, SessionTranscriptWindow{}, actual, "failed pagination should not expose a partially built window")
			require.NoError(t, mock.ExpectationsWereMet(), "failed window should stop before later queries")
		})
	}
}
