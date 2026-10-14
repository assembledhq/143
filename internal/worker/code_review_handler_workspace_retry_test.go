package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestContinueSessionRecheckWorkspaceWaitPreservesAssessment(t *testing.T) {
	t.Parallel()
	for _, sourceCleared := range []bool{false, true} {
		t.Run(map[bool]string{false: "temporary availability loss", true: "source recovery won CAS"}[sourceCleared], func(t *testing.T) {
			t.Parallel()
			stores, mock := newTestStores(t)
			defer mock.Close()
			stores.CodeReviewRechecks = db.NewCodeReviewRecheckStore(mock)
			stores.SessionThreads = db.NewSessionThreadStore(mock)
			org, session, thread, job, assessment := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			sessionRow := workerSessionRow(session, uuid.Nil, org, models.SessionStatusRunning, 0, nil, nil)
			setWorkerSessionColumn(sessionRow, "origin", models.SessionOriginCodeReview)
			mock.ExpectQuery("(?s)SELECT .*FROM sessions").WithArgs(pgx.NamedArgs{"id": session, "org_id": org}).
				WillReturnRows(pgxmock.NewRows(workerSessionColumns).AddRow(sessionRow...))
			now := time.Now()
			columns := strings.Split("assessment_id,org_id,repository_id,pull_request_id,session_id,thread_id,expected_turn,payload_digest,message_id,job_id,status,attempt_lock_token,result_message_id,provider_session_id,snapshot_key,native_context,failure_detail,attempt_usage,completed_at,created_at,updated_at", ",")
			mock.ExpectQuery("(?s)FROM code_review_recheck_dispatches").WithArgs(org, assessment).
				WillReturnRows(pgxmock.NewRows(columns).AddRow(assessment, org, uuid.New(), uuid.New(), session, thread, 1, "digest", int64(42), job, "running", nil, nil, nil, nil, nil, nil, json.RawMessage(`{}`), nil, now, now))
			threadRow := workerSessionThreadRow(thread, session, org, models.AgentTypeCodex, nil, models.ThreadStatusRunning)
			setWorkerSessionThreadColumn(threadRow, "current_turn", 0)
			mock.ExpectQuery("(?s)FROM session_threads").WithArgs(pgx.NamedArgs{"id": thread, "org_id": org}).
				WillReturnRows(pgxmock.NewRows(workerSessionThreadColumns).AddRow(threadRow...))
			waiting := agent.ErrSandboxWorkspaceNotReady
			if sourceCleared {
				waiting = agent.ErrStaleSandboxIDCleared
			}
			orchestrator := &orchestratorServiceStub{continueSessionFn: func(_ context.Context, _ *models.Session, options *agent.ContinueSessionOptions) error {
				require.NotNil(t, options.CodeReviewTurn, "workspace retry must retain its exact recheck turn")
				require.Equal(t, assessment, options.CodeReviewTurn.AssessmentID, "workspace retry must retain assessment ownership")
				return fmt.Errorf("shared input probe: %w", waiting)
			}}
			payload, err := json.Marshal(continueSessionJobInput{SessionID: session.String(), OrgID: org.String(), ThreadID: thread.String(), QueuedMessageID: "42", CodeReviewAssessmentID: assessment.String()})
			require.NoError(t, err, "recheck continuation should encode")
			ctx := jobctx.WithLockToken(jobctx.WithJobID(context.Background(), job), uuid.New())
			err = newContinueSessionHandler(stores, &Services{Orchestrator: orchestrator}, zerolog.Nop())(ctx, "continue_session", payload)
			var retry *RetryableError
			require.ErrorAs(t, err, &retry, "readiness availability loss must retry without failing the assessment")
			require.ErrorIs(t, retry, waiting, "recheck retry must preserve the waiting or source-recovery cause")
			require.False(t, retry.ConsumeAttempt, "workspace waiting must not consume provider attempt budget")
			require.NoError(t, mock.ExpectationsWereMet(), "waiting must not write a terminal recheck or thread result")
		})
	}
}
