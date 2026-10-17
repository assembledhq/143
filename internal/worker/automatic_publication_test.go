package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/publicationintent"
)

type automaticPublicationCoordinator struct {
	request func(context.Context, uuid.UUID, uuid.UUID, publicationintent.RequestPullRequest) (*publicationintent.PublicationIntentResult, error)
}

func (c automaticPublicationCoordinator) RequestPullRequest(ctx context.Context, orgID, sessionID uuid.UUID, request publicationintent.RequestPullRequest) (*publicationintent.PublicationIntentResult, error) {
	return c.request(ctx, orgID, sessionID, request)
}

func (automaticPublicationCoordinator) PublicationExecutionEnabled(models.SessionPublicationSource) bool {
	return true
}
func (automaticPublicationCoordinator) ReviewExecutionEnabled(models.SessionPublicationSource) bool {
	return true
}

type automaticPublicationFixture struct {
	session                                          models.Session
	run                                              models.AutomationRun
	changesetID, snapshotID                          uuid.UUID
	policy                                           models.AutomationPublishPolicy
	publicationErr, prErr, policyErr, coordinatorErr error
	requests                                         []publicationintent.RequestPullRequest
	policyCalls                                      int
	holders                                          *db.SessionSandboxHolderStore
}

func newAutomaticPublicationFixture() *automaticPublicationFixture {
	orgID, sessionID, runID := uuid.New(), uuid.New(), uuid.New()
	diff := "diff --git a/example.go b/example.go\n+changed"
	return &automaticPublicationFixture{
		session: models.Session{ID: sessionID, OrgID: orgID, AutomationRunID: &runID,
			Origin: models.SessionOriginAutomation, Status: models.SessionStatusCompleted,
			InteractionMode: models.SessionInteractionModeSingleRun, Diff: &diff},
		run:         models.AutomationRun{ID: runID, OrgID: orgID, ConfigSnapshot: json.RawMessage(`{"session_continuity":"per_run","publish_policy":"pull_request"}`)},
		changesetID: uuid.New(), snapshotID: uuid.New(), policy: models.AutomationPublishPolicyPullRequest,
		publicationErr: pgx.ErrNoRows, prErr: pgx.ErrNoRows,
	}
}

func (f *automaticPublicationFixture) handler(t *testing.T) JobHandler {
	t.Helper()
	checkScope := func(orgID, sessionID uuid.UUID) {
		t.Helper()
		require.Equal(t, f.session.OrgID, orgID, "every lookup must preserve the job tenant")
		require.Equal(t, f.session.ID, sessionID, "every lookup must preserve the completed session")
	}
	return newAutomaticPublicationHandlerWithDependencies(automaticPublicationDependencies{
		session: func(_ context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
			checkScope(orgID, sessionID)
			return f.session, nil
		},
		primary: func(_ context.Context, orgID, sessionID uuid.UUID) (models.SessionChangeset, error) {
			checkScope(orgID, sessionID)
			return models.SessionChangeset{ID: f.changesetID, OrgID: orgID, SessionID: sessionID, IsPrimary: true}, nil
		},
		publication: func(_ context.Context, orgID, sessionID, changesetID uuid.UUID) (models.SessionPublication, error) {
			checkScope(orgID, sessionID)
			require.Equal(t, f.changesetID, changesetID, "admission recovery must inspect the primary changeset intent")
			return models.SessionPublication{State: models.SessionPublicationStateTerminalFailed}, f.publicationErr
		},
		pullRequest: func(_ context.Context, orgID, sessionID uuid.UUID) (models.PullRequest, error) {
			checkScope(orgID, sessionID)
			return models.PullRequest{}, f.prErr
		},
		run: func(_ context.Context, orgID, runID uuid.UUID) (models.AutomationRun, error) {
			require.Equal(t, f.run.OrgID, orgID, "run snapshot must use the job tenant")
			require.Equal(t, f.run.ID, runID, "run snapshot must be the session's immutable run")
			return f.run, nil
		},
		policy: func(_ context.Context, orgID, runID uuid.UUID) (models.AutomationPublishPolicy, error) {
			require.Equal(t, f.run.OrgID, orgID, "policy resolution must use the job tenant")
			require.Equal(t, f.run.ID, runID, "policy resolution must target the immutable run")
			f.policyCalls++
			return f.policy, f.policyErr
		},
		quiescent: func(ctx context.Context, session models.Session) error {
			return ensureSessionSnapshotQuiescent(ctx, &Stores{SandboxHolders: f.holders}, session)
		},
		snapshot: func(_ context.Context, orgID, sessionID uuid.UUID, turn int) (models.SessionTurnIssueSnapshot, error) {
			checkScope(orgID, sessionID)
			require.Equal(t, max(f.session.CurrentTurn, 1), turn, "initial completion must retain the first turn issue snapshot")
			return models.SessionTurnIssueSnapshot{ID: f.snapshotID}, nil
		},
		coordinator: automaticPublicationCoordinator{request: func(_ context.Context, orgID, sessionID uuid.UUID, request publicationintent.RequestPullRequest) (*publicationintent.PublicationIntentResult, error) {
			checkScope(orgID, sessionID)
			f.requests = append(f.requests, request)
			return &publicationintent.PublicationIntentResult{Status: publicationintent.ResultPRQueued, SessionID: sessionID}, f.coordinatorErr
		}},
	}, zerolog.Nop())
}

func (f *automaticPublicationFixture) payload(t *testing.T) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(map[string]uuid.UUID{"org_id": f.session.OrgID, "session_id": f.session.ID})
	require.NoError(t, err, "encode the durable publication coordination scope")
	return payload
}

func TestAutomaticPublicationHandlerEligibilityAndExistingOwnership(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                          string
		change                        func(*automaticPublicationFixture)
		wantRequests, wantPolicyCalls int
		wantErr                       string
	}{
		{name: "completed policy-approved automation coordinates once", wantRequests: 1, wantPolicyCalls: 1},
		{name: "failed session cannot publish", change: func(f *automaticPublicationFixture) { f.session.Status = models.SessionStatusFailed }},
		{name: "interactive automation cannot publish", change: func(f *automaticPublicationFixture) {
			f.session.InteractionMode = models.SessionInteractionModeInteractive
		}},
		{name: "goal improvement cannot publish", change: func(f *automaticPublicationFixture) { f.session.Origin = models.SessionOriginAutomationGoalImprovement }},
		{name: "ordinary session cannot publish", change: func(f *automaticPublicationFixture) { f.session.AutomationRunID = nil }},
		{name: "missing diff cannot publish", change: func(f *automaticPublicationFixture) { f.session.Diff = nil }},
		{name: "existing terminal intent is not reopened", change: func(f *automaticPublicationFixture) { f.publicationErr = nil }},
		{name: "existing pull request is not recreated", change: func(f *automaticPublicationFixture) { f.prErr = nil }},
		{name: "unknown intent lookup retries", change: func(f *automaticPublicationFixture) { f.publicationErr = errors.New("database unavailable") }, wantErr: "check automatic publication intent"},
		{name: "unknown PR lookup retries", change: func(f *automaticPublicationFixture) { f.prErr = errors.New("database unavailable") }, wantErr: "check existing automatic pull request"},
		{name: "per target run cannot publish", change: func(f *automaticPublicationFixture) { id := uuid.New(); f.run.TargetID = &id }},
		{name: "frozen per target policy cannot publish", change: func(f *automaticPublicationFixture) {
			f.run.ConfigSnapshot = json.RawMessage(`{"session_continuity":"per_target"}`)
		}},
		{name: "none policy cannot publish", change: func(f *automaticPublicationFixture) { f.policy = models.AutomationPublishPolicyNone }, wantPolicyCalls: 1},
		{name: "policy lookup failure retries", change: func(f *automaticPublicationFixture) { f.policyErr = errors.New("policy unavailable") }, wantErr: "resolve automatic publication policy", wantPolicyCalls: 1},
		{name: "invalid frozen policy fails closed", change: func(f *automaticPublicationFixture) { f.policy = "invalid" }, wantErr: "invalid automatic publication policy", wantPolicyCalls: 1},
		{name: "coordinator admission failure retries", change: func(f *automaticPublicationFixture) { f.coordinatorErr = errors.New("freeze unavailable") }, wantRequests: 1, wantPolicyCalls: 1, wantErr: "coordinate automatic publication"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newAutomaticPublicationFixture()
			if tt.change != nil {
				tt.change(f)
			}
			err := f.handler(t)(context.Background(), "coordinate_automatic_publication", f.payload(t))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr, "unresolved admission must remain retryable through the normal durable job")
				var fatal *FatalError
				require.False(t, errors.As(err, &fatal), "transient admission failures must consume ordinary retries instead of immediate dead-lettering")
			} else {
				require.NoError(t, err, "eligible work must coordinate and authoritative exclusions must settle without publication")
			}
			require.Equal(t, tt.wantRequests, len(f.requests), "only a new eligible automatic handoff may reach the publication coordinator")
			require.Equal(t, tt.wantPolicyCalls, f.policyCalls, "existing ownership and per-target exclusions must resolve before mutable work")
			if tt.wantRequests > 0 {
				require.Equal(t, publicationintent.RequestPullRequest{Source: models.SessionPublicationSourceAutomation, TriggerKind: models.SessionPublicationTriggerPolicy, IssueSnapshotID: &f.snapshotID}, f.requests[0], "durable retry must preserve policy authority and issue context without overriding staged team or draft metadata")
			}
		})
	}
}

func TestAutomaticPublicationHandlerRetriesAdmissionBeforeIntentExists(t *testing.T) {
	t.Parallel()
	f := newAutomaticPublicationFixture()
	f.coordinatorErr = errors.New("temporary freeze failure")
	handler, payload := f.handler(t), f.payload(t)
	require.ErrorContains(t, handler(context.Background(), "coordinate_automatic_publication", payload), "temporary freeze failure", "a failure before intent creation must keep the durable completion job retryable")
	f.coordinatorErr = nil
	require.NoError(t, handler(context.Background(), "coordinate_automatic_publication", payload), "the next ordinary job attempt must retry coordination without rerunning the agent")
	require.Equal(t, []publicationintent.RequestPullRequest{
		{Source: models.SessionPublicationSourceAutomation, TriggerKind: models.SessionPublicationTriggerPolicy, IssueSnapshotID: &f.snapshotID},
		{Source: models.SessionPublicationSourceAutomation, TriggerKind: models.SessionPublicationTriggerPolicy, IssueSnapshotID: &f.snapshotID},
	}, f.requests, "admission retry must reuse the same authority and frozen reviewer-intent source")
	f.publicationErr = nil
	require.NoError(t, handler(context.Background(), "coordinate_automatic_publication", payload), "a committed intent must transfer retry ownership to its own durable worker")
	require.Equal(t, 2, len(f.requests), "coordination replay must not reopen an existing terminal intent")
}

func TestAutomaticPublicationHandlerWaitsForFinalSnapshot(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		pendingSnapshot bool
	}{
		{name: "pending final snapshot", pendingSnapshot: true},
		{name: "active completion runtime holder"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newAutomaticPublicationFixture()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create an isolated runtime holder query fixture")
			t.Cleanup(mock.Close)
			f.holders = db.NewSessionSandboxHolderStore(mock)
			if tt.pendingSnapshot {
				key := "synthetic-pending-snapshot"
				f.session.PendingSnapshotKey = &key
			} else {
				mock.ExpectQuery(`SELECT count\(\*\) FROM session_sandbox_holders`).WithArgs(f.session.OrgID, f.session.ID).
					WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))
			}
			err = f.handler(t)(context.Background(), "coordinate_automatic_publication", f.payload(t))
			var retry *RetryableError
			require.ErrorAs(t, err, &retry, "completion admission must wait while the final workspace snapshot can still change")
			require.False(t, retry.ConsumeAttempt, "normal completion cleanup must not spend the bounded external-error retry budget")
			if !tt.pendingSnapshot {
				delay := 5 * time.Second
				require.Equal(t, &delay, retry.RetryAfter, "an active holder should defer admission until its final snapshot is durable")
			}
			require.Equal(t, 0, f.policyCalls, "publication policy must not be resolved against an unfinished completion boundary")
			require.Empty(t, f.requests, "staged metadata must not freeze before the runtime releases its final snapshot")
			require.NoError(t, mock.ExpectationsWereMet(), "quiescence must inspect only the scoped active runtime holders")
		})
	}
}
