package db

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestEvalGateVersioningAndTenantIsolationPostgres(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	tests := []struct {
		name              string
		rollback, foreign bool
	}{{name: "replacement preserves version history"}, {name: "failed replacement preserves active version", rollback: true}, {name: "known IDs cannot cross tenants", foreign: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			org := models.Organization{Name: "eval-coverage-" + uuid.NewString(), Settings: json.RawMessage(`{}`)}
			require.NoError(t, NewOrganizationStore(pool).Create(ctx, &org), "gate fixture should own an isolated tenant")
			datasets := NewEvalDatasetStore(pool)
			dataset := models.EvalDataset{OrgID: org.ID, Name: "Golden API", Description: "Verified regression examples", SourceSummary: "Reviewed corpus"}
			require.NoError(t, datasets.Create(ctx, &dataset), "dataset defaults and count query should work on the complete schema")
			require.NotEqual(t, uuid.Nil, dataset.ID, "dataset creation should allocate a durable identity")
			expectedDataset := models.EvalDataset{ID: dataset.ID, OrgID: org.ID, Name: "Golden API", Description: dataset.Description, SourceSummary: dataset.SourceSummary, DatasetType: models.EvalDatasetTypeGolden, Status: models.EvalDatasetStatusActive, CreatedAt: dataset.CreatedAt, UpdatedAt: dataset.UpdatedAt}
			require.Equal(t, expectedDataset, dataset, "dataset should persist every default without inventing task memberships")
			loaded, err := datasets.GetByID(ctx, org.ID, dataset.ID)
			require.NoError(t, err, "owner should read the dataset")
			require.Equal(t, dataset, loaded, "dataset lookup should preserve the full persisted value")
			gates := NewEvalReleaseGateStore(pool)
			original := models.EvalReleaseGate{OrgID: org.ID, GateName: "API", DatasetID: &dataset.ID, Enabled: true, MinPassAt1: .8, MinPassAtK: .9, MaxRegressionDelta: .05}
			require.NoError(t, gates.Upsert(ctx, &original), "release gate should create an active version")
			require.JSONEq(t, `[10,30,100]`, string(original.CanaryStages), "gate should persist default bounded canary stages")
			require.JSONEq(t, `{}`, string(original.RollbackRules), "gate should persist empty default rollback rules")
			if tt.foreign {
				foreignOrg := uuid.New()
				_, err := datasets.GetByID(ctx, foreignOrg, dataset.ID)
				require.ErrorIs(t, err, pgx.ErrNoRows, "known dataset IDs must not bypass tenant ownership")
				listed, err := datasets.ListByOrg(ctx, foreignOrg, nil)
				require.NoError(t, err, "foreign tenant listing should complete without disclosing rows")
				require.Equal(t, []models.EvalDataset(nil), listed, "foreign tenant must not see another tenant's dataset")
				active, err := gates.ListActive(ctx, foreignOrg)
				require.NoError(t, err, "foreign gate listing should complete")
				require.Equal(t, []models.EvalReleaseGate(nil), active, "foreign tenant must not see another tenant's gates")
			} else {
				replacement := original
				replacement.Enabled = false
				replacement.MinPassAt1 = .95
				if tt.rollback {
					replacement.DatasetID = new(uuid.New())
					err = gates.Upsert(ctx, &replacement)
					require.ErrorContains(t, err, "violates foreign key constraint", "invalid replacement should fail after inactivating the old row inside the transaction")
				} else {
					require.NoError(t, gates.Upsert(ctx, &replacement), "valid update should insert another gate version")
					require.NotEqual(t, original.ID, replacement.ID, "gate updates should allocate a new version identity")
					retired := original
					retired.Active = false
					rows, err := pool.Query(ctx, `SELECT id,org_id,gate_name,enabled,dataset_id,min_pass_at_1,min_pass_at_k,max_policy_violations,max_regression_delta,canary_stages,rollback_rules,updated_by_user_id,active,created_at FROM eval_release_gates WHERE org_id=$1 AND gate_name=$2 ORDER BY active`, org.ID, "API")
					require.NoError(t, err, "read all owned historical gate versions")
					history, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.EvalReleaseGate])
					require.NoError(t, err, "scan complete version history")
					require.Equal(t, []models.EvalReleaseGate{retired, replacement}, history, "update should preserve the exact prior version and one active replacement")
					original = replacement
				}
			}
			active, err := gates.ListActive(ctx, org.ID)
			require.NoError(t, err, "owner should retain a readable active gate after each operation")
			require.Equal(t, []models.EvalReleaseGate{original}, active, "failed replacement and foreign reads must preserve the complete active gate")
		})
	}
}

// Canonicalize only PostgreSQL's timezone representation; all persisted entry
// fields and window metadata remain part of the full equality assertion.
func transcriptCoverageUTCTimes(window SessionTranscriptWindow) SessionTranscriptWindow {
	rows := make([]SessionTranscriptRawRow, len(window.Rows))
	for i, row := range window.Rows {
		row.EntryTime = row.EntryTime.UTC()
		if row.Message != nil {
			value := *row.Message
			value.CreatedAt = value.CreatedAt.UTC()
			row.Message = &value
		}
		if row.Log != nil {
			value := *row.Log
			value.Timestamp = value.Timestamp.UTC()
			row.Log = &value
		}
		if row.HumanInput != nil {
			value := *row.HumanInput
			value.CreatedAt = value.CreatedAt.UTC()
			row.HumanInput = &value
		}
		rows[i] = row
	}
	if window.Rows != nil {
		window.Rows = rows
	}
	phases := make(map[int][]models.SessionTranscriptPhase, len(window.Phases))
	for turn, values := range window.Phases {
		for _, value := range values {
			value.StartedAt = value.StartedAt.UTC()
			if value.CompletedAt != nil {
				completed := value.CompletedAt.UTC()
				value.CompletedAt = &completed
			}
			phases[turn] = append(phases[turn], value)
		}
	}
	if window.Phases != nil {
		window.Phases = phases
	}
	return window
}

func TestTranscriptMixedSourceWindowsPostgres(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	tests := []struct {
		name, anchor                     string
		selected                         []int
		position                         models.TranscriptWindowPosition
		older, newer, foreign, toolsOnly bool
		before, after                    *int
	}{
		{name: "latest includes phase-only turn", selected: []int{4, 5, 6}, position: models.TranscriptWindowPositionLatest, older: true},
		{name: "older excludes cursor turn", selected: []int{1, 2, 3}, position: models.TranscriptWindowPositionOlder, older: true, before: new(4)},
		{name: "newer excludes cursor turn", selected: []int{2, 3, 4}, position: models.TranscriptWindowPositionNewer, newer: true, after: new(1)},
		{name: "around message", anchor: "message", selected: []int{3, 4, 5}, position: models.TranscriptWindowPositionAround, older: true, newer: true},
		{name: "around human input", anchor: "human", selected: []int{3, 4, 5}, position: models.TranscriptWindowPositionAround, older: true, newer: true},
		{name: "around tool result", anchor: "tool", selected: []int{2, 3, 4}, position: models.TranscriptWindowPositionAround, older: true, newer: true},
		{name: "around phase only", anchor: "phase", selected: []int{5, 6}, position: models.TranscriptWindowPositionAround, older: true},
		{name: "foreign anchor falls back without leaking", anchor: "foreign", selected: []int{4, 5, 6}, position: models.TranscriptWindowPositionLatest, older: true},
		{name: "tool filter excludes system and messages", selected: []int{3, 6}, position: models.TranscriptWindowPositionLatest, toolsOnly: true},
		{name: "known thread id cannot cross tenants", foreign: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			org := models.Organization{Name: "transcript-coverage-" + uuid.NewString(), Settings: json.RawMessage(`{}`)}
			require.NoError(t, NewOrganizationStore(pool).Create(ctx, &org), "transcript case should create an isolated tenant")
			session := models.Session{OrgID: org.ID, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusIdle, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
			require.NoError(t, NewSessionStore(pool).Create(ctx, &session), "create a session with its durable primary thread")
			require.NotNil(t, session.PrimaryThreadID, "session fixture should have a primary thread")
			thread := *session.PrimaryThreadID
			stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			messages := make([]models.SessionMessage, 6)
			entries := make(map[int][]SessionTranscriptRawRow)
			for turn := range messages {
				msg := models.SessionMessage{OrgID: org.ID, SessionID: session.ID, ThreadID: &thread, TurnNumber: turn, Role: models.MessageRoleAssistant, Content: fmt.Sprintf("answer %d", turn), Attachments: []string{}, References: models.SessionInputReferences{}, Commands: models.SessionInputCommands{}, CreatedAt: stamp.Add(time.Duration(turn) * time.Second)}
				if turn == 0 {
					msg.Role = models.MessageRoleUser
				}
				err := pool.QueryRow(ctx, `INSERT INTO session_messages(org_id,session_id,thread_id,turn_number,role,content,attachments,"references",commands,created_at) VALUES($1,$2,$3,$4,$5,$6,'{}','[]','[]',$7) RETURNING id`, org.ID, session.ID, thread, turn, msg.Role, msg.Content, msg.CreatedAt).Scan(&msg.ID)
				require.NoError(t, err, "seed complete messages with deterministic ordering")
				messages[turn] = msg
				entries[turn] = append(entries[turn], SessionTranscriptRawRow{EntryKindHint: models.TranscriptEntryKindMessage, TurnNumber: turn, EntryTime: msg.CreatedAt, SourceRank: 1, SourceID: msg.ID, Message: &msg})
			}
			logs := []models.SessionLog{
				{Level: models.SessionLogLevelInfo, Message: "system event", Metadata: json.RawMessage(`{}`)},
				{Level: models.SessionLogLevelToolUse, Message: "Read file", Metadata: json.RawMessage(`{}`)},
				{Level: models.SessionLogLevelInfo, Message: "File contents", Metadata: json.RawMessage(`{"type": "tool_result"}`)},
			}
			for i := range logs {
				log := &logs[i]
				log.OrgID, log.SessionID, log.ThreadID, log.TurnNumber, log.Timestamp = org.ID, session.ID, &thread, 3, stamp.Add(3*time.Second)
				err := pool.QueryRow(ctx, `INSERT INTO session_logs(org_id,session_id,thread_id,turn_number,level,message,metadata,timestamp) VALUES($1,$2,$3,3,$4,$5,$6,$7) RETURNING id`, org.ID, session.ID, thread, log.Level, log.Message, log.Metadata, log.Timestamp).Scan(&log.ID)
				require.NoError(t, err, "seed mixed log kinds with the same entry timestamp")
				kind := models.TranscriptEntryKindLog
				if i == 1 {
					kind = models.TranscriptEntryKindToolUse
				}
				if i == 2 {
					kind = models.TranscriptEntryKindToolResult
				}
				entries[3] = append(entries[3], SessionTranscriptRawRow{EntryKindHint: kind, TurnNumber: 3, EntryTime: log.Timestamp, SourceRank: 2, SourceID: log.ID, Log: log})
			}
			input := models.HumanInputRequest{OrgID: org.ID, SessionID: session.ID, ThreadID: &thread, TurnNumber: 4, AgentType: session.AgentType, Kind: models.HumanInputRequestKindFreeText, Title: "Approach", Body: "Which approach?"}
			require.NoError(t, NewSessionHumanInputRequestStore(pool).Create(ctx, &input), "seed a complete pending human-input entry")
			input.CreatedAt = stamp.Add(4 * time.Second)
			_, err := pool.Exec(ctx, `UPDATE session_human_input_requests SET created_at=$1 WHERE org_id=$2 AND id=$3`, input.CreatedAt, org.ID, input.ID)
			require.NoError(t, err, "give the input deterministic ordering relative to the message")
			entries[4] = append(entries[4], SessionTranscriptRawRow{EntryKindHint: models.TranscriptEntryKindHumanInput, TurnNumber: 4, EntryTime: input.CreatedAt, SourceRank: 3, HumanInput: &input})
			phaseID := uuid.New()
			started, completed := stamp.Add(6*time.Second), stamp.Add(7*time.Second)
			_, err = pool.Exec(ctx, `INSERT INTO session_activity_phases(id,org_id,session_id,thread_id,turn_number,phase_number,status,boundary_reason,trigger_kind,started_at,completed_at) VALUES($1,$2,$3,$4,6,1,'completed','final_response','initial',$5,$6)`, phaseID, org.ID, session.ID, thread, started, completed)
			require.NoError(t, err, "seed a phase-only turn so cursors cannot depend on messages")
			phase := models.SessionTranscriptPhase{ID: phaseID, AnchorID: "aph_" + phaseID.String(), PhaseNumber: 1, Status: models.ActivityPhaseStatusCompleted, BoundaryReason: models.ActivityPhaseBoundaryFinalResponse, TriggerKind: models.ActivityPhaseTriggerInitial, StartedAt: started, CompletedAt: &completed}
			opts := SessionTranscriptWindowOptions{Position: tt.position, LimitTurns: 3}
			if tt.before != nil {
				opts.Before = &models.TranscriptCursor{TurnNumber: *tt.before}
			}
			if tt.after != nil {
				opts.After = &models.TranscriptCursor{TurnNumber: *tt.after}
			}
			if tt.toolsOnly {
				opts.Include = TranscriptInclude{Tools: true}
			}
			anchor := ""
			switch tt.anchor {
			case "message":
				anchor = "msg_" + strconv.FormatInt(messages[4].ID, 10)
			case "human":
				anchor = "hiq_" + input.ID.String()
			case "tool":
				anchor = "tres_" + strconv.FormatInt(logs[2].ID, 10)
			case "phase":
				anchor = phase.AnchorID
			case "foreign":
				otherOrg := models.Organization{Name: "foreign-anchor-" + uuid.NewString(), Settings: json.RawMessage(`{}`)}
				require.NoError(t, NewOrganizationStore(pool).Create(ctx, &otherOrg), "foreign anchor should belong to a real different tenant")
				otherSession := session
				otherSession.ID, otherSession.OrgID, otherSession.PrimaryThreadID = uuid.Nil, otherOrg.ID, nil
				require.NoError(t, NewSessionStore(pool).Create(ctx, &otherSession), "foreign anchor should belong to another durable thread")
				otherInput := models.HumanInputRequest{OrgID: otherOrg.ID, SessionID: otherSession.ID, ThreadID: otherSession.PrimaryThreadID, TurnNumber: 2, AgentType: otherSession.AgentType, Kind: models.HumanInputRequestKindFreeText, Title: "Private", Body: "Foreign tenant question"}
				require.NoError(t, NewSessionHumanInputRequestStore(pool).Create(ctx, &otherInput), "seed an actual foreign input whose known ID must not resolve")
				anchor = "hiq_" + otherInput.ID.String()
			}
			if anchor != "" {
				opts.Position = models.TranscriptWindowPositionAround
				opts.AnchorEntryID = anchor
			}
			queryOrg := org.ID
			if tt.foreign {
				queryOrg = uuid.New()
			}
			actual, err := NewSessionTranscriptStore(pool).ListThreadWindow(ctx, queryOrg, thread, opts)
			require.NoError(t, err, "current-schema transcript query should support mixed sources and scoped anchors")
			expected := SessionTranscriptWindow{}
			if !tt.foreign {
				expected = SessionTranscriptWindow{Position: tt.position, HasOlder: tt.older, HasNewer: tt.newer, Phases: map[int][]models.SessionTranscriptPhase{}, LatestAssistantEntryID: "msg_" + strconv.FormatInt(messages[5].ID, 10), LatestAssistantMessageID: messages[5].ID, LiveEdgeEntryID: "msg_" + strconv.FormatInt(messages[5].ID, 10), LiveEdgeMessageID: messages[5].ID}
				for _, turn := range tt.selected {
					for _, row := range entries[turn] {
						if tt.toolsOnly && row.EntryKindHint != models.TranscriptEntryKindToolUse && row.EntryKindHint != models.TranscriptEntryKindToolResult {
							continue
						}
						expected.Rows = append(expected.Rows, row)
					}
					if turn == 6 {
						expected.Phases[6] = []models.SessionTranscriptPhase{phase}
					}
				}
				if tt.anchor != "" {
					expected.AnchorEntryID = anchor
					expected.AnchorFound = tt.anchor != "foreign"
				}
				cursorID := func(turn int, last bool) string {
					if turn == 6 {
						return phase.AnchorID
					}
					if last && turn == 4 {
						return "hiq_" + input.ID.String()
					}
					if last && turn == 3 {
						return "tres_" + strconv.FormatInt(logs[2].ID, 10)
					}
					return "msg_" + strconv.FormatInt(messages[turn].ID, 10)
				}
				if tt.older {
					turn := tt.selected[0]
					expected.OlderCursor, err = (models.TranscriptCursor{Version: 1, OrgID: org.ID, ThreadID: thread, TurnNumber: turn, EntryID: cursorID(turn, false)}).Encode()
					require.NoError(t, err, "expected older cursor should encode tenant and thread ownership")
				}
				if tt.newer {
					turn := tt.selected[len(tt.selected)-1]
					expected.NewerCursor, err = (models.TranscriptCursor{Version: 1, OrgID: org.ID, ThreadID: thread, TurnNumber: turn, EntryID: cursorID(turn, true)}).Encode()
					require.NoError(t, err, "expected newer cursor should encode tenant and thread ownership")
				}
			}
			require.Equal(t, transcriptCoverageUTCTimes(expected), transcriptCoverageUTCTimes(actual), "full transcript window should preserve source ordering, complete models, phase-only turns, exact cursors, and independent live-edge metadata")
		})
	}
}
