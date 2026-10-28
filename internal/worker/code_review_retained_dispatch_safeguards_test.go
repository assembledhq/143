package worker

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func expectRetainedSafeguardStates(mock pgxmock.PgxPoolIface, f retainedDispatchCPUFixture) {
	mock.ExpectQuery(regexp.QuoteMeta(codeReviewCPUFixtureRetainedIDsSQL)).
		WithArgs(f.job.OrgID, f.job.SessionID, f.job.MetadataID, f.job.OutputKey).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(f.resultID)).RowsWillBeClosed()
	expectRetainedDispatchCPURead(mock, f)
}

func retainedSafeguardFixture(t *testing.T, state db.ReviewerDispatchState) retainedDispatchCPUFixture {
	t.Helper()
	f := newRetainedDispatchCPUFixture(t)
	f.state = state
	if state != db.ReviewerDispatchPending {
		f.attemptID, f.seq, f.writer = uuid.New(), 1, 1
	}
	return f
}

func TestRetainedReviewerSafeguardCancellation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		state db.ReviewerDispatchState
	}{
		{"pending", db.ReviewerDispatchPending},
		{"attempting", db.ReviewerDispatchAttempting},
		{"unknown", db.ReviewerDispatchUnknown},
		{"attention", db.ReviewerDispatchAttention},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := retainedSafeguardFixture(t, tt.state)
			stores, mock := newTestStores(t)
			defer mock.Close()
			stores.CodeReviews = db.NewCodeReviewStore(mock)
			stores.SessionThreads = db.NewSessionThreadStore(mock)
			wireCodeReviewControllerCPUFixture(t, stores, mock)
			expectRetainedSafeguardStates(mock, f)
			activeIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
			activeStatuses := []models.ThreadStatus{models.ThreadStatusRunning, models.ThreadStatusPending, models.ThreadStatusAwaitingInput}
			rows := newSessionThreadRows().AddRow(workerSessionThreadRow(f.threadID, f.job.SessionID, f.job.OrgID, models.AgentTypeCodex, nil, models.ThreadStatusRunning)...)
			for i, id := range activeIDs {
				rows.AddRow(workerSessionThreadRow(id, f.job.SessionID, f.job.OrgID, models.AgentTypeCodex, nil, activeStatuses[i])...)
			}
			for _, status := range []models.ThreadStatus{models.ThreadStatusIdle, models.ThreadStatusCompleted, models.ThreadStatusFailed, models.ThreadStatusCancelled} {
				rows.AddRow(workerSessionThreadRow(uuid.New(), f.job.SessionID, f.job.OrgID, models.AgentTypeCodex, nil, status)...)
			}
			mock.ExpectQuery(`(?s)SELECT .*FROM session_threads\s+WHERE org_id = @org_id AND session_id = @session_id AND archived_at IS NULL`).
				WithArgs(pgx.NamedArgs{"org_id": f.job.OrgID, "session_id": f.job.SessionID}).WillReturnRows(rows)
			for i, id := range activeIDs {
				row := workerSessionThreadRow(id, f.job.SessionID, f.job.OrgID, models.AgentTypeCodex, nil, activeStatuses[i])
				args := pgx.NamedArgs{"org_id": f.job.OrgID, "id": id}
				mock.ExpectQuery(`(?s)SELECT .*FROM session_threads\s+WHERE id = @id AND org_id = @org_id AND archived_at IS NULL`).WithArgs(args).
					WillReturnRows(newSessionThreadRows().AddRow(row...))
				mock.ExpectExec(`(?s)UPDATE session_threads\s+SET cancel_requested_at = COALESCE.*WHERE id = @id AND org_id = @org_id`).WithArgs(args).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectQuery(`(?s)SELECT .*FROM session_threads\s+WHERE id = @id AND org_id = @org_id AND archived_at IS NULL`).WithArgs(args).
					WillReturnRows(newSessionThreadRows().AddRow(row...))
			}
			orch := &orchestratorServiceStub{cancelThreadResult: true}
			err := cancelActiveCodeReviewThreads(context.Background(), stores, &Services{Orchestrator: orch}, zerolog.Nop(), f.job)
			require.NoError(t, err, "stale review cancellation must skip every unresolved lane without cancelling settled lanes")
			require.Equal(t, activeIDs, orch.cancelThreadIDs, "only the three independent active lanes may receive cancellation, never the original uncertain lane")
			require.NoError(t, mock.ExpectationsWereMet(), "protected cancellation must neither use the bulk path nor enqueue or send new work")
		})
	}
}

func TestRetainedReviewerSafeguardHarvest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		state         db.ReviewerDispatchState
		afterDeadline bool
	}{
		{"pending before deadline", db.ReviewerDispatchPending, false},
		{"attempting before deadline", db.ReviewerDispatchAttempting, false},
		{"unknown before deadline", db.ReviewerDispatchUnknown, false},
		{"attention before deadline", db.ReviewerDispatchAttention, false},
		{"pending deadline", db.ReviewerDispatchPending, true},
		{"attempting deadline", db.ReviewerDispatchAttempting, true},
		{"unknown deadline", db.ReviewerDispatchUnknown, true},
		{"attention deadline", db.ReviewerDispatchAttention, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := retainedSafeguardFixture(t, tt.state)
			stores, mock := newTestStores(t)
			defer mock.Close()
			stores.CodeReviews = db.NewCodeReviewStore(mock)
			stores.SessionThreads = db.NewSessionThreadStore(mock)
			runner := wireCodeReviewControllerCPUFixture(t, stores, mock)
			result := retainedDispatchCPUResult(f)
			created := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
			if tt.afterDeadline {
				created = created.Add(-2 * time.Hour)
			}
			policy := models.CodeReviewPolicyRecord{AgentRoster: models.CodeReviewAgentRoster{TimeoutSeconds: 1800}}
			metadata := models.CodeReviewSessionMetadata{CreatedAt: created}
			deadline := created.Add(30 * time.Minute)
			mock.ExpectQuery(`(?s)SELECT .*FROM code_review_agent_results.*WHERE org_id = @org_id AND session_id = @session_id`).
				WithArgs(pgx.NamedArgs{"org_id": f.job.OrgID, "session_id": f.job.SessionID}).WillReturnRows(retainedDispatchCPUResultRows(result))
			expectRetainedSafeguardStates(mock, f)
			expectedUnits := 1
			if tt.afterDeadline {
				expectedUnits++
				// Both actual controller closure and aware outcome writer acquire
				// their existing ordered locks on this same supplied transaction.
				for range 2 {
					expectRetainedDispatchCPULocksOnly(mock, f)
					expectRetainedDispatchCPURead(mock, f)
				}
				mock.ExpectQuery(regexp.QuoteMeta(`SELECT statement_timestamp()>=$1::timestamptz`)).WithArgs(deadline).
					WillReturnRows(pgxmock.NewRows([]string{"due"}).AddRow(true))
				var fields map[string]any
				require.NoError(t, json.Unmarshal(result.StructuredResult, &fields), "the retained result has a complete original structured contribution")
				fields["completed_at"] = deadline.Format(time.RFC3339)
				raw := "Review contribution deadline reached; dispatch confirmation remains unresolved."
				if tt.state == db.ReviewerDispatchPending {
					expectRetainedDispatchCPULocks(mock, f)
					expectRetainedDispatchCPUUpdate(mock, f, "cancel", "deadline_before_attempt", 0, uuid.Nil)
					cancelled := f
					cancelled.state, cancelled.writer = db.ReviewerDispatchCancelled, f.writer+1
					expectRetainedDispatchCPUIntent(mock, cancelled)
					raw = "Reviewer did not produce a completed contribution before the review deadline."
				} else {
					fields["error"], fields["dispatch_unresolved"] = "dispatch_unresolved", true
				}
				structured, err := json.Marshal(fields)
				require.NoError(t, err, "expected deadline closure must preserve every existing structured field")
				closed := result
				closed.Status, closed.RawOutput, closed.StructuredResult = models.CodeReviewAgentResultStatusTimedOut, &raw, structured
				mock.ExpectQuery(`(?s)^UPDATE code_review_agent_results SET status=\$4,raw_output=\$5,structured_result=COALESCE\(\$6::jsonb,structured_result\),.*dispatch_writer_seq=dispatch_writer_seq\+1.*RETURNING .*`).
					WithArgs(f.job.OrgID, f.job.SessionID, f.resultID, models.CodeReviewAgentResultStatusTimedOut, &raw, retainedSafeguardJSON{expected: structured}).
					WillReturnRows(retainedDispatchCPUResultRows(closed))
			}
			err := harvestCodeReviewReviewerResults(context.Background(), stores, nil, zerolog.Nop(), f.job, policy, metadata, nil)
			require.NoError(t, err, "harvest must preserve uncertainty and close only the contribution at its original deadline")
			require.Equal(t, expectedUnits, runner.calls, "before the deadline harvest is read-only; deadline closure uses one separate owner unit")
			require.NoError(t, mock.ExpectationsWereMet(), "unresolved harvest must not read or cancel the thread, send, or run the generic outcome writer")
		})
	}
}

type retainedSafeguardJSON struct{ expected []byte }

func (m retainedSafeguardJSON) Match(value any) bool {
	actual, ok := value.([]byte)
	if !ok {
		return false
	}
	var left, right any
	if json.Unmarshal(m.expected, &left) != nil || json.Unmarshal(actual, &right) != nil {
		return false
	}
	l, err := json.Marshal(left)
	if err != nil {
		return false
	}
	r, err := json.Marshal(right)
	return err == nil && string(l) == string(r)
}

func TestRetainedReviewerSafeguardZeroOutput(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		state db.ReviewerDispatchState
	}{
		{"pending", db.ReviewerDispatchPending}, {"attempting", db.ReviewerDispatchAttempting},
		{"unknown", db.ReviewerDispatchUnknown}, {"attention", db.ReviewerDispatchAttention},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := retainedSafeguardFixture(t, tt.state)
			stores, mock := newTestStores(t)
			defer mock.Close()
			stores.CodeReviews = db.NewCodeReviewStore(mock)
			stores.SessionThreads = db.NewSessionThreadStore(mock)
			wireCodeReviewControllerCPUFixture(t, stores, mock)
			expectRetainedSafeguardStates(mock, f)
			err := failCodeReviewWithoutReviewerOutput(context.Background(), stores, nil, zerolog.Nop(), f.job, models.PullRequest{}, []models.CodeReviewAgentResult{retainedDispatchCPUResult(f)})
			require.EqualError(t, err, "zero reviewer output with unresolved dispatch requires an approved outcome policy", "both zero-output caller paths must stop at the unanswered policy guard")
			require.NoError(t, mock.ExpectationsWereMet(), "the guard must not fail metadata or assessment, recommend a retry, or enqueue replacement work")
		})
	}
}

func TestRetainedReviewerSafeguardPresentationAndArchival(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name       string
		unresolved bool
		expected   string
		archive    bool
	}{
		{"uncertain closed contribution", true, "Codex contribution deadline reached; dispatch confirmation remains unresolved", false},
		{"ordinary timeout", false, "Codex timed out", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newRetainedDispatchCPUFixture(t)
			result := retainedDispatchCPUResult(f)
			result.Status = models.CodeReviewAgentResultStatusTimedOut
			state, ok := parseCodeReviewReviewerStructuredResult(result.StructuredResult)
			require.True(t, ok, "fixture must retain its original reviewer and lane identity")
			state.DispatchUnresolved = tt.unresolved
			result.StructuredResult = marshalCodeReviewReviewerStructuredResult(state)
			lane := models.SessionThread{ID: f.threadID, OrgID: f.job.OrgID, SessionID: f.job.SessionID, Status: models.ThreadStatusIdle, CreatedBySource: models.ThreadCreatedBySourceSystem}
			var expected *models.SessionThread
			if tt.archive {
				expected = &lane
			}
			require.Equal(t, []string{tt.expected}, codeReviewAgentSummaries([]models.CodeReviewAgentResult{result}, nil), "public copy must distinguish contribution deadline from known execution timeout")
			require.Equal(t, expected, codeReviewFallbackArchivalCandidate([]models.SessionThread{lane}, []models.CodeReviewAgentResult{result}, uuid.New()), "closed uncertain work must retain its lane while ordinary terminal work can release capacity")
		})
	}
}
