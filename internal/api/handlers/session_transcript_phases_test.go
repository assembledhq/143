package handlers

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/thread"
)

func TestBuildTranscriptWindowResponseIncludesPhaseOnlyTurn(t *testing.T) {
	t.Parallel()

	phaseID := uuid.New()
	startedAt := time.Date(2026, 8, 3, 1, 2, 3, 0, time.UTC)
	result := thread.TranscriptWindowResult{
		Window: db.SessionTranscriptWindow{
			Position: models.TranscriptWindowPositionLatest,
			Phases: map[int][]models.SessionTranscriptPhase{
				4: {{
					ID: phaseID, AnchorID: "aph_" + phaseID.String(), PhaseNumber: 1,
					Status: models.ActivityPhaseStatusInterrupted, BoundaryReason: models.ActivityPhaseBoundaryRuntimeLost,
					TriggerKind: models.ActivityPhaseTriggerInitial,
					StartedAt:   startedAt, CompletedAt: transcriptTimePtr(startedAt.Add(time.Second)), ToolCallCount: 0,
				}},
			},
		},
		ThreadStatus: models.ThreadStatusIdle,
	}

	response := buildTranscriptWindowResponse(result, uuid.New(), uuid.New())

	require.Equal(t, []models.SessionTranscriptTurn{{
		TurnNumber: 4,
		StartedAt:  startedAt,
		Phases:     result.Window.Phases[4],
		Entries:    []models.SessionTranscriptEntry{},
	}}, response.Data, "a durable phase without transcript output should remain visible as an empty turn")
}

func TestBuildTranscriptEntryIncludesActivityPhaseAssociation(t *testing.T) {
	t.Parallel()

	phaseID := uuid.New()
	createdAt := time.Date(2026, 8, 3, 1, 2, 3, 0, time.UTC)
	messageID := int64(42)
	entry := buildTranscriptEntry(db.SessionTranscriptRawRow{
		EntryKindHint: models.TranscriptEntryKindMessage,
		EntryTime:     createdAt,
		Message: &models.SessionMessage{
			ID: messageID, Role: models.MessageRoleAssistant, Content: "done", CreatedAt: createdAt,
			ActivityPhaseID: &phaseID,
		},
	})

	require.Equal(t, &phaseID, entry.ActivityPhaseID, "the transcript entry should expose its explicit phase association")
}

func TestMissingTranscriptPhaseAssociationsCountsExpectedBoundaryAndToolEntries(t *testing.T) {
	t.Parallel()

	phaseID := uuid.New()
	turn := 2
	startedAt := time.Date(2026, 8, 4, 1, 0, 0, 0, time.UTC)
	completedAt := startedAt.Add(time.Minute)
	result := thread.TranscriptWindowResult{Window: db.SessionTranscriptWindow{
		Phases: map[int][]models.SessionTranscriptPhase{turn: {{ID: phaseID, StartedAt: startedAt, CompletedAt: &completedAt}}},
		Rows: []db.SessionTranscriptRawRow{
			{TurnNumber: turn, EntryTime: startedAt.Add(time.Second), EntryKindHint: models.TranscriptEntryKindToolUse, Log: &models.SessionLog{}},
			{TurnNumber: turn, EntryTime: startedAt.Add(2 * time.Second), EntryKindHint: models.TranscriptEntryKindToolResult, Log: &models.SessionLog{ActivityPhaseID: &phaseID}},
			{TurnNumber: turn, EntryTime: startedAt.Add(3 * time.Second), Message: &models.SessionMessage{Role: models.MessageRoleAssistant}},
			{TurnNumber: turn, EntryTime: startedAt.Add(4 * time.Second), Message: &models.SessionMessage{Role: models.MessageRoleUser}},
			{TurnNumber: turn, EntryTime: completedAt, HumanInput: &models.HumanInputRequest{}},
			{TurnNumber: turn, EntryTime: startedAt.Add(-time.Second), EntryKindHint: models.TranscriptEntryKindToolUse, Log: &models.SessionLog{}},
			{TurnNumber: turn + 1, EntryTime: startedAt, EntryKindHint: models.TranscriptEntryKindToolUse, Log: &models.SessionLog{}},
		},
	}}

	require.Equal(t, map[models.TranscriptEntryKind]int64{
		models.TranscriptEntryKindToolUse:    1,
		models.TranscriptEntryKindMessage:    1,
		models.TranscriptEntryKindHumanInput: 1,
	}, missingTranscriptPhaseAssociations(result), "only entries expected to belong to an authoritative phase should count as missing")
}

func transcriptTimePtr(value time.Time) *time.Time {
	return &value
}
