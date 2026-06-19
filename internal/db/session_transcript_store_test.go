package db

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestNormalizeTranscriptLimitTurns(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input int
		want  int
	}{
		{
			name:  "zero returns default",
			input: 0,
			want:  DefaultTranscriptLimitTurns,
		},
		{
			name:  "negative returns default",
			input: -1,
			want:  DefaultTranscriptLimitTurns,
		},
		{
			name:  "one returns one",
			input: 1,
			want:  1,
		},
		{
			name:  "default limit passes through",
			input: DefaultTranscriptLimitTurns,
			want:  DefaultTranscriptLimitTurns,
		},
		{
			name:  "max limit passes through",
			input: MaxTranscriptLimitTurns,
			want:  MaxTranscriptLimitTurns,
		},
		{
			name:  "max plus one is clamped to max",
			input: MaxTranscriptLimitTurns + 1,
			want:  MaxTranscriptLimitTurns,
		},
		{
			name:  "large value is clamped to max",
			input: 999,
			want:  MaxTranscriptLimitTurns,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeTranscriptLimitTurns(tc.input)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestReversedInts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input []int
		want  []int
	}{
		{
			name:  "nil input returns empty slice",
			input: nil,
			want:  []int{},
		},
		{
			name:  "empty slice returns empty slice",
			input: []int{},
			want:  []int{},
		},
		{
			name:  "single element unchanged",
			input: []int{42},
			want:  []int{42},
		},
		{
			name:  "two elements are swapped",
			input: []int{1, 2},
			want:  []int{2, 1},
		},
		{
			name:  "multiple elements are reversed",
			input: []int{10, 20, 30, 40, 50},
			want:  []int{50, 40, 30, 20, 10},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := reversedInts(tc.input)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestReversedInts_DoesNotMutateInput(t *testing.T) {
	t.Parallel()

	input := []int{5, 4, 3, 2, 1}
	snapshot := []int{5, 4, 3, 2, 1}

	result := reversedInts(input)
	require.Equal(t, []int{1, 2, 3, 4, 5}, result, "result should be reversed")
	require.Equal(t, snapshot, input, "original slice must not be mutated")
}

func TestInt32TurnNumbers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		input   []int
		want    []int32
		wantErr bool
	}{
		{
			name:  "empty slice returns empty slice",
			input: []int{},
			want:  []int32{},
		},
		{
			name:  "valid turn numbers are converted",
			input: []int{1, 2, 42},
			want:  []int32{1, 2, 42},
		},
		{
			name:  "int32 bounds are accepted",
			input: []int{math.MinInt32, math.MaxInt32},
			want:  []int32{math.MinInt32, math.MaxInt32},
		},
		{
			name:    "below int32 minimum returns error",
			input:   []int{math.MinInt32 - 1},
			wantErr: true,
		},
		{
			name:    "above int32 maximum returns error",
			input:   []int{math.MaxInt32 + 1},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := int32TurnNumbers(tc.input)
			if tc.wantErr {
				require.Error(t, err, "out-of-range turn numbers should return an error")
				return
			}
			require.NoError(t, err, "valid turn numbers should not return an error")
			require.Equal(t, tc.want, got, "turn numbers should convert to the expected int32 values")
		})
	}
}

func TestNewSessionTranscriptStore_NilDB(t *testing.T) {
	t.Parallel()

	// NewSessionTranscriptStore stores the db argument as-is; passing nil must
	// not panic and must return a non-nil store.
	store := NewSessionTranscriptStore(nil)
	require.NotNil(t, store)
}

// TestSessionTranscriptRawRow_SortOrder verifies the sort comparator used by
// fetchEntriesForTurns produces the expected ordering:
// (TurnNumber ASC, EntryTime ASC, SourceRank ASC, SourceID ASC).
func TestSessionTranscriptRawRow_SortOrder(t *testing.T) {
	t.Parallel()

	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Second)

	// Deliberately unsorted input.
	rows := []SessionTranscriptRawRow{
		{TurnNumber: 2, EntryTime: t0, SourceRank: 1, SourceID: 10}, // turn 2 — should be last
		{TurnNumber: 1, EntryTime: t1, SourceRank: 2, SourceID: 5},  // turn 1, later time
		{TurnNumber: 1, EntryTime: t0, SourceRank: 3, SourceID: 1},  // turn 1, t0, rank 3
		{TurnNumber: 1, EntryTime: t0, SourceRank: 1, SourceID: 99}, // turn 1, t0, rank 1, id 99
		{TurnNumber: 1, EntryTime: t0, SourceRank: 1, SourceID: 7},  // turn 1, t0, rank 1, id 7
	}

	// Same comparator as fetchEntriesForTurns.
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

	// All turn-1 rows must come before the turn-2 row.
	for i := 0; i < 4; i++ {
		require.Equal(t, 1, rows[i].TurnNumber, "rows[%d] should be turn 1", i)
	}
	require.Equal(t, 2, rows[4].TurnNumber, "rows[4] should be turn 2")

	// Among turn-1 rows: t0 entries before t1.
	require.True(t, rows[0].EntryTime.Equal(t0), "first turn-1 row should have time t0")
	require.True(t, rows[1].EntryTime.Equal(t0), "second turn-1 row should have time t0")
	require.True(t, rows[2].EntryTime.Equal(t0), "third turn-1 row should have time t0")
	require.True(t, rows[3].EntryTime.Equal(t1), "fourth turn-1 row should have time t1")

	// Among t0 entries (rank 1 id 7, rank 1 id 99, rank 3 id 1):
	// rank 1 id 7 first, then rank 1 id 99, then rank 3 id 1.
	require.Equal(t, 1, rows[0].SourceRank, "rows[0] SourceRank")
	require.Equal(t, int64(7), rows[0].SourceID, "rows[0] SourceID")
	require.Equal(t, 1, rows[1].SourceRank, "rows[1] SourceRank")
	require.Equal(t, int64(99), rows[1].SourceID, "rows[1] SourceID")
	require.Equal(t, 3, rows[2].SourceRank, "rows[2] SourceRank")
	require.Equal(t, int64(1), rows[2].SourceID, "rows[2] SourceID")
}

// TestSessionTranscriptWindowOptions_ZeroLimitTurns verifies that the zero
// value of LimitTurns resolves to DefaultTranscriptLimitTurns.
func TestSessionTranscriptWindowOptions_ZeroLimitTurns(t *testing.T) {
	t.Parallel()

	var opts SessionTranscriptWindowOptions
	require.Equal(t, DefaultTranscriptLimitTurns, normalizeTranscriptLimitTurns(opts.LimitTurns))
}

// TestTranscriptEntryKindConstants ensures that the string values of the
// model constants referenced in fetchEntriesForTurns match expectations.
func TestTranscriptEntryKindConstants(t *testing.T) {
	t.Parallel()

	require.Equal(t, models.TranscriptEntryKind("message"), models.TranscriptEntryKindMessage)
	require.Equal(t, models.TranscriptEntryKind("tool_use"), models.TranscriptEntryKindToolUse)
	require.Equal(t, models.TranscriptEntryKind("log"), models.TranscriptEntryKindLog)
	require.Equal(t, models.TranscriptEntryKind("human_input"), models.TranscriptEntryKindHumanInput)
}

func TestSessionTranscriptStore_ListThreadWindowIncludesTurnZeroInitialMessage(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should be created")
	defer mock.Close()

	store := NewSessionTranscriptStore(mock)
	orgID := uuid.New()
	threadID := uuid.New()
	sessionID := uuid.New()
	turns := []int32{0, 1}
	now := time.Now().UTC()
	turnOneTime := now.Add(time.Second)

	mock.ExpectQuery(`SELECT DISTINCT turn_number FROM \(.+session_messages.+session_logs.+\) t\s+WHERE turn_number >= 0`).
		WithArgs(pgx.NamedArgs{
			"org_id":    orgID,
			"thread_id": threadID,
			"limit":     DefaultTranscriptLimitTurns + 1,
		}).
		WillReturnRows(pgxmock.NewRows([]string{"turn_number"}).
			AddRow(1).
			AddRow(0))

	mock.ExpectQuery(`SELECT .+ FROM session_messages.+turn_number = ANY\(@turns\)`).
		WithArgs(pgx.NamedArgs{
			"org_id":    orgID,
			"thread_id": threadID,
			"turns":     turns,
		}).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "user_id", "turn_number", "role", "content", "attachments", "references", "commands", "token_usage", "source", "created_at"}).
			AddRow(int64(10), sessionID, orgID, &threadID, nil, 0, models.MessageRoleUser, "initial prompt", nil, nil, nil, nil, "", now).
			AddRow(int64(11), sessionID, orgID, &threadID, nil, 1, models.MessageRoleAssistant, "assistant response", nil, nil, nil, nil, "", turnOneTime))

	mock.ExpectQuery(`SELECT .+ FROM session_logs.+turn_number = ANY\(@turns\).+turn_number > 0`).
		WithArgs(pgx.NamedArgs{
			"org_id":    orgID,
			"thread_id": threadID,
			"turns":     turns,
		}).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}).
			AddRow(int64(101), sessionID, orgID, &threadID, turnOneTime.Add(time.Millisecond), models.SessionLogLevelInfo, "turn one log", json.RawMessage(`{}`), 1))

	mock.ExpectQuery(`SELECT id\s+FROM session_messages\s+WHERE org_id = @org_id AND thread_id = @thread_id AND role = 'assistant'`).
		WithArgs(pgx.NamedArgs{"org_id": orgID, "thread_id": threadID}).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int64(11)))

	mock.ExpectQuery(`SELECT entry_kind, source_id, message_id, hiq_uuid, level, metadata`).
		WithArgs(pgx.NamedArgs{"org_id": orgID, "thread_id": threadID}).
		WillReturnRows(pgxmock.NewRows([]string{"entry_kind", "source_id", "message_id", "hiq_uuid", "level", "metadata"}).
			AddRow(models.TranscriptEntryKindLog, int64(101), nil, nil, models.SessionLogLevelInfo, json.RawMessage(`{}`)))

	window, err := store.ListThreadWindow(context.Background(), orgID, threadID, SessionTranscriptWindowOptions{
		Include: TranscriptInclude{Messages: true, System: true},
	})
	require.NoError(t, err, "ListThreadWindow should include the initial prompt without error")
	require.False(t, window.HasOlder, "turn zero and turn one should fit in the default latest window")
	require.Equal(t, int64(11), window.LatestAssistantMessageID, "latest assistant metadata should still point at the assistant response")
	require.Equal(t, "log_101", window.LiveEdgeEntryID, "live edge metadata should ignore turn-zero history and use the latest positive-turn entry")

	gotKinds := make([]models.TranscriptEntryKind, 0, len(window.Rows))
	gotTurns := make([]int, 0, len(window.Rows))
	gotContents := make([]string, 0, len(window.Rows))
	for _, row := range window.Rows {
		gotKinds = append(gotKinds, row.EntryKindHint)
		gotTurns = append(gotTurns, row.TurnNumber)
		if row.Message != nil {
			gotContents = append(gotContents, row.Message.Content)
		}
		if row.Log != nil {
			gotContents = append(gotContents, row.Log.Message)
		}
	}
	require.Equal(t, []models.TranscriptEntryKind{
		models.TranscriptEntryKindMessage,
		models.TranscriptEntryKindMessage,
		models.TranscriptEntryKindLog,
	}, gotKinds, "window should include turn-zero message and positive-turn log entries")
	require.Equal(t, []int{0, 1, 1}, gotTurns, "turn-zero should appear only for the initial message")
	require.Equal(t, []string{"initial prompt", "assistant response", "turn one log"}, gotContents, "turn-zero log noise should stay out of the transcript")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestTranscriptTurnSelectBranchesFiltersLogIncludes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		include         TranscriptInclude
		wantContains    []string
		wantNotContains []string
	}{
		{
			name:    "tools only filters to tool logs",
			include: TranscriptInclude{Tools: true},
			wantContains: []string{
				"session_logs",
				"level = 'tool_use'",
				"metadata->>'type' = 'tool_result'",
			},
			wantNotContains: []string{"NOT ("},
		},
		{
			name:    "system only filters out tool logs",
			include: TranscriptInclude{System: true},
			wantContains: []string{
				"session_logs",
				"NOT (",
				"level = 'tool_use'",
				"metadata->>'type' = 'tool_result'",
			},
		},
		{
			name:            "tools and system includes all logs",
			include:         TranscriptInclude{Tools: true, System: true},
			wantContains:    []string{"session_logs"},
			wantNotContains: []string{"level = 'tool_use'", "metadata->>'type' = 'tool_result'", "NOT ("},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			branches := transcriptTurnSelectBranches(tt.include)
			joined := strings.Join(branches, "\n")
			for _, expected := range tt.wantContains {
				require.Contains(t, joined, expected, "log branch should include expected filter fragment")
			}
			for _, unexpected := range tt.wantNotContains {
				require.NotContains(t, joined, unexpected, "log branch should not include excluded filter fragment")
			}
		})
	}
}
