package worker

import (
	"context"
	"errors"
	"os"
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

func TestCodeReviewWorkspaceFailurePresentation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		stage    string
		cause    error
		expected string
	}{
		{name: "clone", stage: agent.CodeReviewPreparationStageClone, cause: errors.New("token=synthetic-secret"), expected: "Review workspace preparation failed during repository clone. Review did not run. This is an operational review failure. Check repository access and sandbox network connectivity, then request a new review."},
		{name: "hydration timeout", stage: agent.CodeReviewPreparationStageHydration, cause: context.DeadlineExceeded, expected: "Review workspace preparation exceeded its total eight-minute budget during pinned source hydration. Review did not run. This is an operational review failure. Restore access to the pinned base and head Git objects, then request a new review."},
		{name: "source integrity validation", stage: agent.CodeReviewPreparationStageHydration, cause: agent.ErrCodeReviewSourceIntegrity, expected: "Review workspace preparation could not verify pinned Git source object integrity. Recreate the review workspace and retry. Review did not run. This is an operational review failure."},
		{name: "checkout", stage: agent.CodeReviewPreparationStageCheckout, cause: errors.New("raw command stderr"), expected: "Review workspace preparation failed during pinned pull request checkout. Review did not run. This is an operational review failure. Check that the pinned pull request commit remains fetchable, then request a new review."},
		{name: "dependency setup", stage: agent.CodeReviewPreparationStageDependencySetup, cause: errors.New("raw command stderr"), expected: "Review workspace preparation failed during declared dependency and bootstrap setup. Review did not run. This is an operational review failure. Check the declared dependency setup and bootstrap commands, then request a new review."},
		{name: "readiness", stage: agent.CodeReviewPreparationStageReadiness, cause: errors.New("raw command stderr"), expected: "Review workspace preparation failed during workspace readiness verification. Review did not run. This is an operational review failure. Check workspace capacity and sandbox availability, then request a new review."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			preparation := agent.NewCodeReviewWorkspacePreparationError(tt.stage, tt.cause)
			decoded, known := knownCodeReviewWorkspacePreparationError(preparation.Error())
			require.True(t, known, "persisted finite setup stages should remain recognizable")
			if errors.Is(tt.cause, agent.ErrCodeReviewSourceIntegrity) {
				require.ErrorIs(t, decoded, agent.ErrCodeReviewSourceIntegrity, "persisted source validation failure must preserve its deterministic classification")
				var typed *agent.CodeReviewWorkspacePreparationError
				require.ErrorAs(t, decoded, &typed, "source validation should remain a typed preparation error")
				require.Equal(t, agent.CodeReviewPreparationStageHydration, typed.Stage, "finite source validation text must reconstruct the existing pinned source stage")
			}
			require.Equal(t, tt.expected, codeReviewWorkspacePreparationFailureMessage(decoded), "setup failure must explain the safe stage and recovery action")
			code, message, retryable := codeReviewTerminalFailureStatus(&FatalError{Err: preparation})
			require.Equal(t, models.CodeReviewStatusCodeWorkerFailed, code, "setup failures use the existing operational status contract")
			require.Equal(t, tt.expected, message, "dead-letter fallback should preserve the same safe author-facing explanation")
			require.True(t, retryable, "authors may request a fresh attempt after correcting the setup issue")
			body := codeReviewStatusCommentBody(models.CodeReviewSessionMetadata{Status: models.CodeReviewSessionStatusFailed, StatusMessage: &message, FinalReviewBody: stringPtr("Stale approval narrative")}, nil, "https://143.test/code-reviews", "https://143.test/code-reviews?review_now=example")
			require.Contains(t, body, "**143 review issues:**\n- "+tt.expected, "rolling comment should frame setup failure as an operational review issue")
			require.NotContains(t, body, "Stale approval narrative", "a failed setup cannot publish a stored approval narrative")
			require.NotContains(t, body, "Blocking findings", "setup failures must not become code findings")
			require.NotContains(t, body, "Important uncertainty remained", "setup failures must not become material code uncertainty")
			require.NotContains(t, body, "synthetic-secret", "command diagnostics must not reach author-visible review text")
		})
	}
}

func TestCodeReviewWorkspaceFailureRejectsRawDiagnostics(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, detail string }{
		{name: "unknown command output", detail: "bootstrap failed with token=synthetic-secret"},
		{name: "safe text with appended diagnostics", detail: agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageClone, errors.New("failed")).Error() + " token=synthetic-secret"},
		{name: "source integrity text with appended diagnostics", detail: agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageHydration, agent.ErrCodeReviewSourceIntegrity).Error() + " token=synthetic-secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, known := knownCodeReviewWorkspacePreparationError(tt.detail)
			require.False(t, known, "only exact finite preparation messages may cross the diagnostic boundary")
			actual := codeReviewWorkspacePreparationFailureMessage(safeCodeReviewWorkspacePreparationError(tt.detail))
			require.Equal(t, "Review workspace preparation failed during workspace readiness verification. Review did not run. This is an operational review failure. Check workspace capacity and sandbox availability, then request a new review.", actual, "unknown diagnostics should produce a safe actionable operational fallback")
		})
	}
}

func TestInitialCodeReviewPreparationDeadline(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		role         models.CodeReviewAgentRole
		known        bool
		roleError    bool
		label        string
		turn         int
		recheck      bool
		checkpoint   bool
		wantDeadline bool
	}{
		{name: "renamed reviewer inherits first checkpoint", role: models.CodeReviewAgentRoleReviewer, known: true, label: "Custom reviewer tab", checkpoint: true, wantDeadline: true},
		{name: "misleading orchestrator title keeps later job budget", role: models.CodeReviewAgentRoleOrchestrator, known: true, label: "Code review: codex", checkpoint: true},
		{name: "unknown role retains conservative first deadline", label: "Unknown tab", checkpoint: true, wantDeadline: true},
		{name: "role lookup failure retains conservative first deadline", roleError: true, checkpoint: true, wantDeadline: true},
		{name: "legacy reviewer uses persisted continuation job", role: models.CodeReviewAgentRoleReviewer, known: true, wantDeadline: true},
		{name: "later reviewer turn keeps current job budget", turn: 1, role: models.CodeReviewAgentRoleReviewer, known: true, checkpoint: true},
		{name: "evidence recheck keeps current job budget", recheck: true, checkpoint: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "deadline routing mock should initialize")
			defer mock.Close()
			orgID, sessionID, threadID, reviewID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			created := time.Now().UTC().Add(-time.Minute)
			first := created.Add(-6 * time.Minute)
			thread := &models.SessionThread{ID: threadID, SessionID: sessionID, CurrentTurn: tt.turn, Label: tt.label, ExecutionMode: models.ThreadExecutionModeReview, FilesystemMode: models.ThreadFilesystemModeReadOnly}
			if tt.turn == 0 && !tt.recheck {
				query := mock.ExpectQuery("SELECT DISTINCT r.role").WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID, "thread_id": threadID.String()})
				if tt.roleError {
					query.WillReturnError(errors.New("temporary role lookup error"))
				} else {
					rows := pgxmock.NewRows([]string{"role"})
					if tt.known {
						rows.AddRow(tt.role)
					}
					query.WillReturnRows(rows)
				}
				if !tt.known || tt.role != models.CodeReviewAgentRoleOrchestrator || tt.roleError {
					mock.ExpectQuery("FROM code_review_session_metadata").WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).WillReturnRows(newCodeReviewMetadataRows().AddRow(reviewID, orgID, sessionID, uuid.New(), uuid.New(), uuid.New(), "base", "head", false, models.CodeReviewTriggerSourceAppReviewer, models.CodeReviewSessionStatusRunning, nil, nil, nil, nil, nil, false, nil, nil, false, nil, "output", nil, nil, nil, nil, nil, nil, created))
					rows := pgxmock.NewRows([]string{"created_at"})
					if tt.checkpoint {
						rows.AddRow(first)
					}
					mock.ExpectQuery("SELECT created_at[\\s\\S]*FROM jobs").WithArgs(orgID, reviewID.String(), sessionID.String()).WillReturnRows(rows)
				}
			}
			deadline, err := initialCodeReviewPreparationDeadline(jobctx.WithJobCreatedAt(context.Background(), created), &Stores{CodeReviews: db.NewCodeReviewStore(mock), Jobs: db.NewJobStore(mock)}, zerolog.Nop(), models.Session{ID: sessionID, OrgID: orgID, Origin: models.SessionOriginCodeReview}, thread, tt.recheck)
			require.NoError(t, err, "role routing must preserve the correct durable preparation window")
			if tt.wantDeadline {
				start := created
				if tt.checkpoint {
					start = first
				}
				expected := start.Add(agent.CodeReviewWorkspacePreparationTimeout)
				require.Equal(t, &expected, deadline, "initial setup may spend only the remaining original budget")
			} else {
				require.Nil(t, deadline, "later synthesis or evidence turns must not inherit a forever-expired initial deadline")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "deadline ownership must stay scoped to exact tenant and thread")
		})
	}
}

func TestCodeReviewWorkspaceFailureCannotReplaceNewerGeneration(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "failure projection mock should initialize")
	defer mock.Close()
	orgID, sessionID, reviewID, prID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	failure := &codeReviewWorkspaceGateFailure{Generation: 1, Err: agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageDependencySetup, errors.New("failed"))}
	reason := codeReviewWorkspacePreparationFailureMessage(failure.Err)
	mock.ExpectBegin()
	for _, setting := range []string{"lock_timeout", "statement_timeout", "idle_in_transaction_session_timeout"} {
		mock.ExpectExec("SET LOCAL " + setting).WillReturnResult(pgxmock.NewResult("SET", 0))
	}
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(pgx.NamedArgs{"lock_key": "code_review_status_comment:" + orgID.String() + ":" + prID.String()}).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("(?s)WITH candidate AS.*s.workspace_generation = @generation.*FOR UPDATE OF s, m.*UPDATE code_review_session_metadata").WithArgs(pgx.NamedArgs{"org_id": orgID, "review_id": reviewID, "session_id": sessionID, "pull_request_id": prID, "head_sha": "head", "generation": int64(1), "reason": reason}).WillReturnRows(newCodeReviewMetadataRows())
	mock.ExpectRollback()
	err = failCodeReviewWorkspacePreparation(context.Background(), &Stores{CodeReviews: db.NewCodeReviewStore(mock)}, nil, zerolog.Nop(), runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, MetadataID: reviewID, PullRequestID: prID, HeadSHA: "head"}, failure)
	var retry *RetryableError
	require.ErrorAs(t, err, &retry, "a newer generation must cause a fresh controller read instead of terminal projection")
	require.ErrorIs(t, err, errCodeReviewEndedParentRecovery, "an exhausted ownership retry cannot overwrite the newer assessment")
	require.NoError(t, mock.ExpectationsWereMet(), "a lost fence must not mutate session, assessment, or publication")
}

func TestCodeReviewWorkspaceFailureProjectionPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for workspace failure projection proof")
	}
	pool := fullRecoveryPostgresPool(t, context.Background())
	f := seedStrandedReview(t, pool, "running")
	setup := agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageDependencySetup, errors.New("raw token=synthetic-secret"))
	expected := "Review workspace preparation failed during declared dependency and bootstrap setup. Review did not run. This is an operational review failure. Check the declared dependency setup and bootstrap commands, then request a new review."
	err := failCodeReviewWorkspacePreparation(context.Background(), f.stores, nil, zerolog.Nop(), f.job, &codeReviewWorkspaceGateFailure{Generation: 0, Err: setup})
	require.NoError(t, err, "controller should reconcile the entire operational failure under its current workspace fence")
	metadata, err := f.stores.CodeReviews.GetBySessionID(context.Background(), f.job.OrgID, f.job.SessionID)
	require.NoError(t, err, "read failed review metadata from the disposable fixture")
	require.Equal(t, models.CodeReviewSessionStatusFailed, metadata.Status, "setup failure must end the review without approval")
	require.Equal(t, models.CodeReviewDecisionBlocked, *metadata.Decision, "setup failure must not synthesize an approval decision")
	require.Equal(t, &expected, metadata.StatusMessage, "dashboard and rolling comment must receive exact safe recovery guidance")
	require.Equal(t, &expected, metadata.FailureReason, "durable review failure should preserve operational provenance")
	assessment, err := f.stores.CodeReviewAssessments.GetBySessionID(context.Background(), f.job.OrgID, f.job.SessionID)
	require.NoError(t, err, "read the failed full assessment")
	require.Equal(t, models.CodeReviewAssessmentFailed, assessment.Status, "workspace failure cannot leave an active full assessment or reusable coverage")
	session, err := f.stores.Sessions.GetByID(context.Background(), f.job.OrgID, f.job.SessionID)
	require.NoError(t, err, "read the reconciled parent session")
	require.Equal(t, models.SessionStatusFailed, session.Status, "the parent must not remain running after operational review failure")
	require.Equal(t, "code_review_workspace_preparation", stringPtrValue(session.FailureCategory), "parent failure should retain the setup category")
	body := codeReviewStatusCommentBody(metadata, nil, "https://143.test/code-reviews", "https://143.test/code-reviews?review_now=test")
	require.Contains(t, body, "**143 review issues:**\n- "+expected, "safe operational failure should be author visible through the existing rolling-comment path")
	require.NotContains(t, body, "synthetic-secret", "raw setup stderr must not cross the publication boundary")
}
