package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func sessionPublicationTestColumns() []string {
	return []string{
		"id", "org_id", "session_id", "changeset_id", "repository_id",
		"state", "source", "review_gate_state", "job_queue", "request_payload", "request_generation_at",
		"base_branch", "head_branch", "desired_head_sha",
		"published_head_sha", "github_pr_number", "github_pr_url", "attempt_count",
		"last_error_code", "last_error_message", "requested_at", "last_attempt_at",
		"branch_published_at", "pr_resolved_at", "completed_at", "created_at", "updated_at",
	}
}

func sessionPublicationTestRow(publication models.SessionPublication) []any {
	return []any{
		publication.ID, publication.OrgID, publication.SessionID, publication.ChangesetID, publication.RepositoryID,
		publication.State, publication.Source, publication.ReviewGateState, publication.JobQueue, publication.RequestPayload, publication.RequestGenerationAt,
		publication.BaseBranch, publication.HeadBranch, publication.DesiredHeadSHA,
		publication.PublishedHeadSHA, publication.GitHubPRNumber, publication.GitHubPRURL, publication.AttemptCount,
		publication.LastErrorCode, publication.LastErrorMessage, publication.RequestedAt, publication.LastAttemptAt,
		publication.BranchPublishedAt, publication.PRResolvedAt, publication.CompletedAt, publication.CreatedAt, publication.UpdatedAt,
	}
}

func TestSessionPublicationStoreEnsureRequestedPersistsReplayIntent(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create the database mock")
	t.Cleanup(mock.Close)
	orgID, sessionID, changesetID, repositoryID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC()
	payload := json.RawMessage(`{"org_id":"` + orgID.String() + `","session_id":"` + sessionID.String() + `","changeset_id":"` + changesetID.String() + `","draft":false}`)
	publication := models.SessionPublication{
		OrgID: orgID, SessionID: sessionID, ChangesetID: changesetID, RepositoryID: repositoryID,
		Source: models.SessionPublicationSourceUser, ReviewGateState: models.SessionPublicationReviewGateNotRequired,
		JobQueue: models.SessionPublicationJobQueueAgent, RequestPayload: payload,
		RequestGenerationAt: now,
		BaseBranch:          "main", HeadBranch: "143/session",
	}
	stored := publication
	stored.ID = uuid.New()
	stored.State = models.SessionPublicationStateRequested
	stored.RequestedAt = now
	stored.CreatedAt = now
	stored.UpdatedAt = now
	mock.ExpectQuery("INSERT INTO session_publications").WithArgs(pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID,
		"repository_id": repositoryID, "source": models.SessionPublicationSourceUser,
		"review_gate_state": models.SessionPublicationReviewGateNotRequired,
		"job_queue":         models.SessionPublicationJobQueueAgent, "request_payload": string(payload),
		"request_generation_at": now,
		"base_branch":           "main", "head_branch": "143/session", "desired_head_sha": (*string)(nil),
	}).WillReturnRows(pgxmock.NewRows(sessionPublicationTestColumns()).AddRow(sessionPublicationTestRow(stored)...))

	err = NewSessionPublicationStore(mock).EnsureRequested(context.Background(), orgID, &publication)
	require.NoError(t, err, "EnsureRequested should persist a durable publication replay intent")
	require.Equal(t, stored, publication, "EnsureRequested should return the exact stored publication including its replay payload and queue")
	require.NoError(t, mock.ExpectationsWereMet(), "publication intent persistence should remain tenant scoped")
}

func TestSessionPublicationStoreEnsureRequestedReopensRetryableTerminalOutcomeForNewRequest(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create the database mock")
	t.Cleanup(mock.Close)
	orgID, sessionID, changesetID, repositoryID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC()
	headSHA := "0123456789abcdef0123456789abcdef01234567"
	payload := json.RawMessage(`{"org_id":"` + orgID.String() + `","session_id":"` + sessionID.String() + `","changeset_id":"` + changesetID.String() + `","publication_source":"user","publication_queue":"agent"}`)
	publication := models.SessionPublication{
		OrgID: orgID, SessionID: sessionID, ChangesetID: changesetID, RepositoryID: repositoryID,
		Source: models.SessionPublicationSourceUser, ReviewGateState: models.SessionPublicationReviewGateNotRequired,
		JobQueue: models.SessionPublicationJobQueueAgent, RequestPayload: payload,
		RequestGenerationAt: now,
		BaseBranch:          "main", HeadBranch: "143/session", DesiredHeadSHA: &headSHA,
	}
	stored := publication
	stored.ID = uuid.New()
	stored.State = models.SessionPublicationStateRequested
	stored.RequestedAt = now
	stored.CreatedAt = now
	stored.UpdatedAt = now
	mock.ExpectQuery(`INSERT INTO session_publications[\s\S]+ON CONFLICT[\s\S]+state = CASE[\s\S]+state IN \('completed_noop', 'terminal_failed'\)[\s\S]+EXCLUDED.request_generation_at > session_publications.request_generation_at`).
		WithArgs(pgx.NamedArgs{
			"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID,
			"repository_id": repositoryID, "source": models.SessionPublicationSourceUser,
			"review_gate_state": models.SessionPublicationReviewGateNotRequired,
			"job_queue":         models.SessionPublicationJobQueueAgent, "request_payload": string(payload),
			"request_generation_at": now,
			"base_branch":           "main", "head_branch": "143/session", "desired_head_sha": &headSHA,
		}).WillReturnRows(pgxmock.NewRows(sessionPublicationTestColumns()).AddRow(sessionPublicationTestRow(stored)...))

	err = NewSessionPublicationStore(mock).EnsureRequested(context.Background(), orgID, &publication)
	require.NoError(t, err, "a newer explicit request should reopen an earlier retryable terminal publication even when the desired head is unchanged")
	require.Equal(t, stored, publication, "the reopened publication should expose the new durable intent")
	require.NoError(t, mock.ExpectationsWereMet(), "publication generation reopening should remain tenant scoped")
}

func TestSessionPublicationStoreEnsureRequestedGuardsMutableIntentByGeneration(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create the database mock")
	t.Cleanup(mock.Close)
	orgID, sessionID, changesetID, repositoryID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	olderGeneration := time.Now().UTC().Add(-time.Minute)
	newerGeneration := olderGeneration.Add(30 * time.Second)
	oldPayload := json.RawMessage(`{"org_id":"` + orgID.String() + `","session_id":"` + sessionID.String() + `","changeset_id":"` + changesetID.String() + `","publication_source":"reconciler"}`)
	newPayload := json.RawMessage(`{"org_id":"` + orgID.String() + `","session_id":"` + sessionID.String() + `","changeset_id":"` + changesetID.String() + `","publication_source":"user"}`)
	publication := models.SessionPublication{
		OrgID: orgID, SessionID: sessionID, ChangesetID: changesetID, RepositoryID: repositoryID,
		Source: models.SessionPublicationSourceReconciler, ReviewGateState: models.SessionPublicationReviewGatePending,
		JobQueue: models.SessionPublicationJobQueueDefault, RequestPayload: oldPayload,
		RequestGenerationAt: olderGeneration,
		BaseBranch:          "stale-base", HeadBranch: "stale-head",
	}
	stored := publication
	stored.ID = uuid.New()
	stored.State = models.SessionPublicationStateRequested
	stored.Source = models.SessionPublicationSourceUser
	stored.ReviewGateState = models.SessionPublicationReviewGateNotRequired
	stored.JobQueue = models.SessionPublicationJobQueueAgent
	stored.RequestPayload = newPayload
	stored.RequestGenerationAt = newerGeneration
	stored.BaseBranch = "main"
	stored.HeadBranch = "current-head"
	stored.RequestedAt = newerGeneration
	stored.CreatedAt = olderGeneration
	stored.UpdatedAt = newerGeneration

	mock.ExpectQuery(`head_branch = CASE[\s\S]+EXCLUDED\.request_generation_at < session_publications\.request_generation_at[\s\S]+request_generation_at = CASE[\s\S]+GREATEST\(session_publications\.request_generation_at, EXCLUDED\.request_generation_at\)[\s\S]+source = CASE[\s\S]+EXCLUDED\.request_generation_at < session_publications\.request_generation_at[\s\S]+request_payload = CASE[\s\S]+EXCLUDED\.request_generation_at < session_publications\.request_generation_at[\s\S]+updated_at = CASE[\s\S]+EXCLUDED\.request_generation_at < session_publications\.request_generation_at`).
		WithArgs(pgx.NamedArgs{
			"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID,
			"repository_id": repositoryID, "source": models.SessionPublicationSourceReconciler,
			"review_gate_state": models.SessionPublicationReviewGatePending,
			"job_queue":         models.SessionPublicationJobQueueDefault, "request_payload": string(oldPayload),
			"request_generation_at": olderGeneration,
			"base_branch":           "stale-base", "head_branch": "stale-head", "desired_head_sha": (*string)(nil),
		}).WillReturnRows(pgxmock.NewRows(sessionPublicationTestColumns()).AddRow(sessionPublicationTestRow(stored)...))

	err = NewSessionPublicationStore(mock).EnsureRequested(context.Background(), orgID, &publication)
	require.NoError(t, err, "a stale retry should return the authoritative newer publication intent")
	require.Equal(t, stored, publication, "a stale retry must not replace newer branch, payload, source, queue, or review metadata")
	require.NoError(t, mock.ExpectationsWereMet(), "all mutable publication intent should be guarded by request generation")
}

func TestSessionPublicationStoreStartAttempt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		state         models.SessionPublicationState
		updatedRows   int64
		expectStarted bool
	}{
		{name: "starts active publication", state: models.SessionPublicationStateRequested, updatedRows: 1, expectStarted: true},
		{name: "skips completed publication", state: models.SessionPublicationStateCompleted, updatedRows: 0, expectStarted: false},
		{name: "skips terminal failure", state: models.SessionPublicationStateTerminalFailed, updatedRows: 0, expectStarted: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "test should create the database mock")
			t.Cleanup(mock.Close)
			orgID, sessionID, changesetID := uuid.New(), uuid.New(), uuid.New()
			args := pgx.NamedArgs{"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID}
			mock.ExpectExec("UPDATE session_publications").WithArgs(args).
				WillReturnResult(pgxmock.NewResult("UPDATE", tt.updatedRows))
			if tt.updatedRows == 0 {
				now := time.Now()
				publication := models.SessionPublication{
					ID: uuid.New(), OrgID: orgID, SessionID: sessionID, ChangesetID: changesetID, RepositoryID: uuid.New(),
					State: tt.state, Source: models.SessionPublicationSourceBackend, ReviewGateState: models.SessionPublicationReviewGateNotRequired,
					JobQueue: models.SessionPublicationJobQueueDefault, RequestPayload: json.RawMessage(`{}`),
					BaseBranch: "main", HeadBranch: "143/session", RequestedAt: now, CreatedAt: now, UpdatedAt: now,
				}
				mock.ExpectQuery("SELECT .*FROM session_publications").WithArgs(args).
					WillReturnRows(pgxmock.NewRows(sessionPublicationTestColumns()).AddRow(sessionPublicationTestRow(publication)...))
			}

			started, err := NewSessionPublicationStore(mock).StartAttempt(context.Background(), orgID, sessionID, changesetID)
			require.NoError(t, err, "StartAttempt should handle active and terminal publication states")
			require.Equal(t, tt.expectStarted, started, "StartAttempt should report whether GitHub side effects may proceed")
			require.NoError(t, mock.ExpectationsWereMet(), "all publication attempt queries should remain tenant scoped")
		})
	}
}

func TestSessionPublicationStoreSetReviewGate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		gate          models.SessionPublicationReviewGateState
		expectedState models.SessionPublicationState
	}{
		{name: "review pending", gate: models.SessionPublicationReviewGatePending, expectedState: models.SessionPublicationStateReviewPending},
		{name: "human decision remains resumable", gate: models.SessionPublicationReviewGateNeedsHuman, expectedState: models.SessionPublicationStateReviewPending},
		{name: "review passed", gate: models.SessionPublicationReviewGatePassed, expectedState: models.SessionPublicationStateReadyToPublish},
		{name: "review not required", gate: models.SessionPublicationReviewGateNotRequired, expectedState: models.SessionPublicationStateReadyToPublish},
		{name: "review failed terminally", gate: models.SessionPublicationReviewGateFailed, expectedState: models.SessionPublicationStateTerminalFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "test should create the database mock")
			t.Cleanup(mock.Close)
			orgID, sessionID, changesetID := uuid.New(), uuid.New(), uuid.New()
			mock.ExpectExec("UPDATE session_publications").WithArgs(pgx.NamedArgs{
				"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID,
				"review_gate_state": tt.gate, "state": tt.expectedState,
			}).WillReturnResult(pgxmock.NewResult("UPDATE", 1))

			err = NewSessionPublicationStore(mock).SetReviewGate(context.Background(), orgID, sessionID, changesetID, tt.gate)
			require.NoError(t, err, "SetReviewGate should persist the exact durable gate transition")
			require.NoError(t, mock.ExpectationsWereMet(), "review-gate transitions should remain tenant and changeset scoped")
		})
	}
}

func TestSessionPublicationStoreListBySessionScopesTenant(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create the database mock")
	t.Cleanup(mock.Close)
	orgID, sessionID := uuid.New(), uuid.New()
	now := time.Now()
	expected := []models.SessionPublication{
		{
			ID: uuid.New(), OrgID: orgID, SessionID: sessionID, ChangesetID: uuid.New(), RepositoryID: uuid.New(),
			State: models.SessionPublicationStateBranchPublished, Source: models.SessionPublicationSourceAutomation,
			ReviewGateState: models.SessionPublicationReviewGatePassed, BaseBranch: "main", HeadBranch: "143/one",
			JobQueue: models.SessionPublicationJobQueueDefault, RequestPayload: json.RawMessage(`{"session_id":"one"}`),
			AttemptCount: 1, RequestedAt: now, CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: uuid.New(), OrgID: orgID, SessionID: sessionID, ChangesetID: uuid.New(), RepositoryID: uuid.New(),
			State: models.SessionPublicationStateCompleted, Source: models.SessionPublicationSourceUser,
			ReviewGateState: models.SessionPublicationReviewGateNotRequired, BaseBranch: "main", HeadBranch: "143/two",
			JobQueue: models.SessionPublicationJobQueueAgent, RequestPayload: json.RawMessage(`{"session_id":"two"}`),
			AttemptCount: 1, RequestedAt: now, CreatedAt: now, UpdatedAt: now,
		},
	}
	rows := pgxmock.NewRows(sessionPublicationTestColumns())
	for _, publication := range expected {
		rows.AddRow(sessionPublicationTestRow(publication)...)
	}
	mock.ExpectQuery("SELECT .*FROM session_publications").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).
		WillReturnRows(rows)

	actual, err := NewSessionPublicationStore(mock).ListBySession(context.Background(), orgID, sessionID)
	require.NoError(t, err, "ListBySession should return publication checkpoints")
	require.Equal(t, expected, actual, "ListBySession should return the exact tenant-scoped publications")
	require.NoError(t, mock.ExpectationsWereMet(), "publication list query should include org and session filters")
}

func TestSessionPublicationStoreListReconcileCandidatesSkipsBlockedGatesWithoutPR(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create the database mock")
	t.Cleanup(mock.Close)
	orgID, sessionID := uuid.New(), uuid.New()
	now := time.Now()
	expected := models.SessionPublication{
		ID: uuid.New(), OrgID: orgID, SessionID: sessionID, ChangesetID: uuid.New(), RepositoryID: uuid.New(),
		State: models.SessionPublicationStateReadyToPublish, Source: models.SessionPublicationSourceUser,
		ReviewGateState: models.SessionPublicationReviewGateNotRequired, BaseBranch: "main", HeadBranch: "143/recover",
		JobQueue: models.SessionPublicationJobQueueDefault, RequestPayload: json.RawMessage(`{"session_id":"recover"}`),
		AttemptCount: 1, RequestedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	updatedBefore := now.Add(time.Minute)
	mock.ExpectQuery(`SELECT .+FROM session_publications.+state IN \(.+'requested', 'review_pending', 'ready_to_publish'.+review_gate_state IN \('not_required', 'passed'\).+OR EXISTS \(.+FROM pull_requests.+updated_at <`).
		WithArgs(pgx.NamedArgs{"org_id": orgID, "updated_before": updatedBefore, "limit": 25}).
		WillReturnRows(pgxmock.NewRows(sessionPublicationTestColumns()).AddRow(sessionPublicationTestRow(expected)...))

	actual, err := NewSessionPublicationStore(mock).ListReconcileCandidates(context.Background(), orgID, updatedBefore, 25)
	require.NoError(t, err, "ListReconcileCandidates should include publishable work lost before the branch checkpoint")
	require.Equal(t, []models.SessionPublication{expected}, actual, "reconciliation should return the exact tenant-scoped pre-push publication")
	require.NoError(t, mock.ExpectationsWereMet(), "reconciliation candidate lookup should enforce state and tenant scope")
}

func TestSessionPublicationStoreRecordRequeuedClearsRetryFailure(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create the database mock")
	t.Cleanup(mock.Close)
	orgID, sessionID, changesetID := uuid.New(), uuid.New(), uuid.New()
	mock.ExpectExec("UPDATE session_publications").WithArgs(pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID,
	}).WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = NewSessionPublicationStore(mock).RecordRequeued(context.Background(), orgID, sessionID, changesetID)
	require.NoError(t, err, "RecordRequeued should move retryable work back into the active reconciliation lifecycle")
	require.NoError(t, mock.ExpectationsWereMet(), "requeue checkpoint should remain tenant and changeset scoped")
}
