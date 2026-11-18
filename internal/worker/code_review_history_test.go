package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type historyPromptRecords struct {
	guidanceRecords
	rounds    []db.CodeReviewHistoryRound
	listErr   error
	listCalls int
}

func (s *historyPromptRecords) ListPriorRounds(_ context.Context, _, _, _ uuid.UUID, _ *uuid.UUID, _ int) ([]db.CodeReviewHistoryRound, error) {
	s.listCalls++
	return s.rounds, s.listErr
}

func TestPrepareCodeReviewHistoryPrompt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                       string
		existing, race, wrongThread, wrongIdentity bool
		readFailure, listFailure, writeFailure     bool
		changedInputs                              bool
	}{
		{name: "first dispatch freezes references and binding"},
		{name: "retry reuses original prompt without a new lookup", existing: true},
		{name: "concurrent dispatch reuses the winning prompt", race: true},
		{name: "different thread cannot inherit a grant", existing: true, wrongThread: true},
		{name: "different session cannot inherit a grant", existing: true, wrongIdentity: true},
		{name: "read failure is returned", readFailure: true},
		{name: "history lookup failure is returned", listFailure: true},
		{name: "binding must persist before dispatch", writeFailure: true},
		{name: "a retry cannot mix a saved prompt with changed inputs", existing: true, changedInputs: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			job := runCodeReviewPayload{OrgID: uuid.New(), RepositoryID: uuid.New(), SessionID: uuid.New()}
			threadID := uuid.New()
			record := models.CodeReviewPromptRecord{OrgID: job.OrgID, SessionID: job.SessionID, RecordKey: "review/orchestrator", Role: "orchestrator", Metadata: json.RawMessage(`{"head_sha":"current"}`)}
			history := &prompts.CodeReviewHistoryPromptData{SessionID: job.SessionID.String()}
			boundThreadID := threadID
			if tt.wrongThread {
				boundThreadID = uuid.New()
			}
			metadata, err := json.Marshal(map[string]any{"review_history_thread_id": boundThreadID, "review_history": history, "head_sha": "current"})
			require.NoError(t, err, "encode the original prompt binding")
			saved := record
			saved.Content, saved.Metadata = "original prompt", metadata
			if tt.changedInputs {
				record.Metadata = json.RawMessage(`{"head_sha":"changed"}`)
			}
			if tt.wrongIdentity {
				saved.SessionID = uuid.New()
			}
			store := &historyPromptRecords{}
			if tt.existing {
				store.record = &saved
			}
			if tt.race {
				store.winner = &saved
			}
			failure := errors.New("storage unavailable")
			if tt.readFailure {
				store.readErr = failure
			}
			if tt.listFailure {
				store.listErr = failure
			}
			if tt.writeFailure {
				store.writeErr = failure
			}
			for range codeReviewHistoryIndexLimit + 1 {
				store.rounds = append(store.rounds, db.CodeReviewHistoryRound{SessionID: uuid.New(), BaseSHA: "base", HeadSHA: "head", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
			}
			var rendered *prompts.CodeReviewHistoryPromptData
			got, err := prepareCodeReviewHistoryPrompt(context.Background(), store, job, threadID, record, func(data *prompts.CodeReviewHistoryPromptData) string {
				rendered = data
				return "new prompt"
			})
			if tt.wrongThread || tt.wrongIdentity || tt.readFailure || tt.listFailure || tt.writeFailure || tt.changedInputs {
				require.Error(t, err, "dispatch must fail when history cannot be bound to the exact thread")
				return
			}
			require.NoError(t, err, "history prompt should be available before dispatch")
			expected := "new prompt"
			if tt.existing || tt.race {
				expected = "original prompt"
			}
			require.Equal(t, expected, got, "dispatch must use the immutable winning prompt")
			if tt.existing {
				require.Equal(t, 0, store.listCalls, "recovery must not replace the historical index")
				require.Nil(t, rendered, "recovery must not rerender the agent's prompt")
			} else {
				expectedRounds := make([]prompts.CodeReviewHistoryRoundReference, 0, codeReviewHistoryIndexLimit)
				for _, round := range store.rounds[:codeReviewHistoryIndexLimit] {
					expectedRounds = append(expectedRounds, prompts.CodeReviewHistoryRoundReference{SessionID: round.SessionID.String(), BaseSHA: "base", HeadSHA: "head", CreatedAt: "2026-01-01T00:00:00Z"})
				}
				require.Equal(t, &prompts.CodeReviewHistoryPromptData{SessionID: job.SessionID.String(), Rounds: expectedRounds, HasMore: true}, rendered, "compact references must disclose the omitted page without importing old conclusions")
			}
		})
	}
}

func TestValidateCodeReviewHistoryReads(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, kind                                 string
		noHistory, noPrompt, wrongThread, wrongOrg bool
		wrongSource, empty                         bool
		wantErr                                    bool
	}{
		{name: "empty history is valid", noHistory: true},
		{name: "listing alone cannot satisfy consultation", kind: "prior_rounds", wantErr: true},
		{name: "a round read satisfies consultation", kind: "prior_round"},
		{name: "session excerpts do not replace the latest report", kind: "prior_messages", wantErr: true},
		{name: "empty report response does not count", kind: "prior_round", empty: true, wantErr: true},
		{name: "another earlier round does not replace the latest report", kind: "prior_round", wrongSource: true, wantErr: true},
		{name: "fallback needs its own read", kind: "prior_round", wrongThread: true, wantErr: true},
		{name: "another org cannot provide a receipt", kind: "prior_round", wrongOrg: true, wantErr: true},
		{name: "missing prompt cannot establish empty history", noPrompt: true, wantErr: true},
		{name: "no read leaves synthesis incomplete", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			job := runCodeReviewPayload{OrgID: uuid.New(), SessionID: uuid.New()}
			threadID, sourceID := uuid.New(), uuid.New()
			history := &prompts.CodeReviewHistoryPromptData{SessionID: job.SessionID.String()}
			if !tt.noHistory {
				history.Rounds = []prompts.CodeReviewHistoryRoundReference{{SessionID: sourceID.String()}}
			}
			metadata, err := json.Marshal(codeReviewHistoryPromptMetadata{ThreadID: threadID, History: history})
			require.NoError(t, err, "encode history prompt metadata")
			var records []models.CodeReviewPromptRecord
			if !tt.noPrompt {
				records = append(records, models.CodeReviewPromptRecord{OrgID: job.OrgID, SessionID: job.SessionID, RecordKey: "prompt", Role: "orchestrator", Content: "prompt", Metadata: metadata})
			}
			if tt.kind != "" {
				receiptThreadID, receiptOrgID := threadID, job.OrgID
				if tt.wrongThread {
					receiptThreadID = uuid.New()
				}
				if tt.wrongOrg {
					receiptOrgID = uuid.New()
				}
				if tt.wrongSource {
					sourceID = uuid.New()
				}
				metadata, err = json.Marshal(map[string]any{"review_history_thread_id": receiptThreadID, "source_session_id": sourceID, "kind": tt.kind})
				require.NoError(t, err, "encode successful read receipt")
				content := `{"data":{"session_id":"` + sourceID.String() + `"}}`
				if tt.empty {
					content = `{"data":{}}`
				}
				records = append(records, models.CodeReviewPromptRecord{OrgID: receiptOrgID, SessionID: job.SessionID, Role: "review_history", Content: content, Metadata: metadata})
			}
			err = validateCodeReviewHistoryReads(records, job, "prompt", threadID)
			if tt.wantErr {
				require.Error(t, err, "missing authorized history access must prevent a history-aware synthesis")
			} else {
				require.NoError(t, err, "empty history or a substantive authorized read permits synthesis")
			}
		})
	}
}

func TestCodeReviewHistoryProvenance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, previous, expected string
		optedIn                  bool
	}{
		{name: "ordinary review unchanged"},
		{name: "existing provenance unchanged", previous: "Evidence rechecked.", expected: "Evidence rechecked."},
		{name: "opt-in disclosed", optedIn: true, expected: "Earlier review history retrieval was enabled for this review (experimental)."},
		{name: "recheck provenance preserved", optedIn: true, previous: "Evidence rechecked.", expected: "Evidence rechecked.\nEarlier review history retrieval was enabled for this review (experimental)."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := liveCodeReviewOutcomeInput{ReviewProvenance: tt.previous, Job: runCodeReviewPayload{RequestContext: &codereviewsvc.ReviewRequestContext{UseReviewHistory: tt.optedIn}}}
			require.Equal(t, tt.expected, codeReviewHistoryProvenance(input), "history enrollment should be disclosed without overwriting other provenance")
		})
	}
}
