package worker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/linear"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type emitOnceRetryClient struct {
	linear.Client
	err error
}

func (c *emitOnceRetryClient) AgentActivityCreate(context.Context, linear.AgentActivityInput) (linear.AgentActivityResult, error) {
	return linear.AgentActivityResult{}, c.err
}

// These tests cover the pure helpers in handlers_linear_agent_helpers.go.
// The full created-path closure relies on too many concrete stores to
// unit-test cleanly without a Postgres harness; these tests pin the
// payload-construction logic that drives the agent's first-turn
// experience, since that's what users actually see if the helpers
// produce wrong output.

func TestBuildAgentSession(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	issue := &models.Issue{ID: uuid.New(), OrgID: orgID, ExternalID: "iss_1"}
	fetched := &linear.FetchedIssue{
		ID:          "iss_1",
		Identifier:  "ACS-42",
		Title:       "Fix the thing",
		Description: "It's broken",
		TeamID:      "team_1",
	}
	repo := linear.AgentRepoResolveResult{RepositoryID: repoID, DefaultBranch: "release/2026-05", Source: "team_default_mapping"}

	session := buildAgentSession(orgID, repo, issue, fetched, models.AgentTypePi)
	require.Equal(t, orgID, session.OrgID, "session inherits org from caller, not from the issue (defense against cross-org bugs)")
	require.Equal(t, models.AgentTypePi, session.AgentType, "Linear-triggered sessions should honor the org default agent type resolved by the caller")
	require.Equal(t, models.SessionOriginIssueTrigger, session.Origin,
		"origin must mark this as an inbound trigger, not a manual session — drives downstream PM/automation behavior")
	require.NotNil(t, session.PrimaryIssueID, "primary issue link is what makes HandleAgentMilestone fire")
	require.Equal(t, issue.ID, *session.PrimaryIssueID)
	require.NotNil(t, session.RepositoryID)
	require.Equal(t, repoID, *session.RepositoryID)
	require.NotNil(t, session.TargetBranch, "mapped default branch should become the session target branch")
	require.Equal(t, "release/2026-05", *session.TargetBranch, "session target branch should honor the team repo mapping override")
	require.NotNil(t, session.LinearIdentifierHint)
	require.Equal(t, "ACS-42", *session.LinearIdentifierHint,
		"identifier hint feeds the branch-naming logic and the PR title prefix")
	require.Equal(t, models.LinearPrepareStateReady, session.LinearPrepareState,
		"agent-triggered sessions skip the prepare-and-link work; PrepareState must already be ready")
	require.NotNil(t, session.Title)
	require.Equal(t, "Fix the thing", *session.Title)
	require.NotNil(t, session.PMApproach, "PMApproach carries the issue context so run_agent doesn't need a fresh Linear fetch")
	require.Contains(t, *session.PMApproach, "ACS-42")
}

func TestBuildAgentSession_FallsBackToIdentifierWhenTitleEmpty(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	issue := &models.Issue{ID: uuid.New()}
	fetched := &linear.FetchedIssue{
		Identifier: "ACS-42",
		// Title intentionally empty
	}
	session := buildAgentSession(orgID, linear.AgentRepoResolveResult{RepositoryID: uuid.New()}, issue, fetched, models.AgentTypeCodex)
	require.NotNil(t, session.Title)
	require.Equal(t, "ACS-42", *session.Title,
		"empty title falls back to identifier so the sessions list never shows a blank row")
}

func TestResolveLinearAgentSessionAgentType(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	tests := []struct {
		name      string
		loader    func(context.Context, uuid.UUID) (models.OrgSettings, error)
		expected  models.AgentType
		expectErr bool
	}{
		{
			name:     "missing loader falls back to platform default",
			expected: models.DefaultDefaultAgentType,
		},
		{
			name: "uses org default",
			loader: func(context.Context, uuid.UUID) (models.OrgSettings, error) {
				return models.OrgSettings{DefaultAgentType: models.AgentTypePi}, nil
			},
			expected: models.AgentTypePi,
		},
		{
			name: "empty org default falls back",
			loader: func(context.Context, uuid.UUID) (models.OrgSettings, error) {
				return models.OrgSettings{}, nil
			},
			expected: models.DefaultDefaultAgentType,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := resolveLinearAgentSessionAgentType(context.Background(), LinearAgentEventHandlerDeps{OrgSettingsLoader: tt.loader}, orgID)
			if tt.expectErr {
				require.Error(t, err, "agent type loader errors should propagate to the worker retry path")
				return
			}
			require.NoError(t, err, "agent type resolution should succeed")
			require.Equal(t, tt.expected, got, "agent type resolution should match the org default fallback rules")
		})
	}
}

func TestEnqueueRunAgentForLinearAgentUsesAgentQueue(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	jobID := uuid.New()
	dedupe := db.RunAgentDedupeKey(sessionID)

	mock.ExpectQuery("SELECT .* FROM sessions").
		WithArgs(sessionID, orgID).
		WillReturnRows(pgxmock.NewRows(workerSessionColumns).
			AddRow(workerSessionRow(sessionID, issueID, orgID, models.SessionStatusPending, 0, nil, nil)...))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(orgID, "agent", "run_agent", pgxmock.AnyArg(), 5, &dedupe).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))

	err = enqueueRunAgentForLinearAgent(context.Background(), &Stores{Sessions: db.NewSessionStore(mock), Jobs: db.NewJobStore(mock)}, orgID, sessionID)
	require.NoError(t, err, "run_agent enqueue should succeed")
	require.NoError(t, mock.ExpectationsWereMet(), "Linear-created run_agent jobs should use the agent worker queue")
}

func TestEnqueueRunAgentForLinearAgentSkipsTerminalSessions(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()

	mock.ExpectQuery("SELECT .* FROM sessions").
		WithArgs(sessionID, orgID).
		WillReturnRows(pgxmock.NewRows(workerSessionColumns).
			AddRow(workerSessionRow(sessionID, issueID, orgID, models.SessionStatusCompleted, 1, nil, nil)...))

	err = enqueueRunAgentForLinearAgent(context.Background(), &Stores{Sessions: db.NewSessionStore(mock), Jobs: db.NewJobStore(mock)}, orgID, sessionID)
	require.NoError(t, err, "terminal Linear agent reconciliation should be a no-op instead of enqueueing duplicate completed work")
	require.NoError(t, mock.ExpectationsWereMet(), "completed sessions should only be loaded; no run_agent job should be inserted")
}

func TestUpsertLinearIssueForAgentUsesCanonicalFingerprint(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	issueID := uuid.New()
	now := time.Now().UTC()
	fetched := &linear.FetchedIssue{
		ID:          "2563b72a-e241-44db-85a3-4267084bb274",
		Identifier:  "VIR-102",
		Title:       "Make a full screen mode for the file diff viewer",
		Description: "Diff viewer should support full-screen review.",
	}
	expectedFingerprint := "linear:2072004d71b40dd3c2eac1cdfa1c7290"

	mock.ExpectQuery("INSERT INTO issues").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), expectedFingerprint,
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(issueID, now, now))

	issue, err := upsertLinearIssueForAgent(context.Background(), &Stores{
		Issues: db.NewIssueStore(mock),
	}, orgID, fetched, &repoID)
	require.NoError(t, err, "linear agent issue upsert should use the canonical fingerprint")
	require.Equal(t, issueID, issue.ID, "linear agent issue upsert should return the existing or inserted issue id")
	require.Equal(t, expectedFingerprint, issue.Fingerprint, "linear agent issue model should carry the canonical fingerprint")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestLinearAgentPinSessionStateUsesRequestedState(t *testing.T) {
	t.Parallel()

	require.Equal(t, "error", linearAgentPinSessionState(models.LinearAgentSessionStateError),
		"unsupported-session finalization should pin Linear to the supplied terminal state")
}

func TestEmitOnceDiscardsReservationOnLinearFailure(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "test should create pgx mock")
	defer mock.Close()

	orgID := uuid.New()
	rowID := uuid.New()

	mock.ExpectQuery("INSERT INTO linear_agent_activity_log").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "inserted"}).AddRow(uuid.New(), true))
	mock.ExpectExec("DELETE FROM linear_agent_activity_log").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	err = emitOnce(
		context.Background(),
		&emitOnceRetryClient{err: errors.New("linear unavailable")},
		db.NewLinearAgentActivityLogStore(mock),
		orgID,
		rowID,
		"as_1",
		linear.AgentMilestoneActivity{
			Type:    models.LinearAgentActivityResponse,
			Body:    "This session cannot start.",
			IdemKey: "bootstrap:not_supported",
		},
		zerolog.Nop(),
	)
	require.Error(t, err, "Linear emit failure should still surface to the worker retry path")
	require.NoError(t, mock.ExpectationsWereMet(), "terminal response emits should discard failed reservations so retries can re-emit")
}

func TestBuildIssueApproachPrompt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		fetched     *linear.FetchedIssue
		mustHave    []string
		mustNotHave []string
	}{
		{
			name:    "nil fetched returns empty",
			fetched: nil,
		},
		{
			name: "renders title and description",
			fetched: &linear.FetchedIssue{
				Identifier:  "ACS-42",
				Title:       "Fix the thing",
				Description: "It's broken",
			},
			mustHave: []string{"ACS-42", "Fix the thing", "It's broken"},
		},
		{
			name: "includes recent comments when present",
			fetched: &linear.FetchedIssue{
				Identifier:  "ACS-42",
				Title:       "X",
				Description: "Y",
				Comments: []linear.FetchedComment{
					{Author: "alice", Body: "first"},
					{Author: "bob", Body: "second"},
				},
			},
			mustHave: []string{"alice", "first", "bob", "second", "Recent discussion"},
		},
		{
			name: "no Recent discussion section when comments empty",
			fetched: &linear.FetchedIssue{
				Identifier: "ACS-42",
				Title:      "X",
			},
			mustNotHave: []string{"Recent discussion"},
		},
		{
			name: "includes attachments when present",
			fetched: &linear.FetchedIssue{
				Identifier: "ACS-42",
				Title:      "X",
				Attachments: []linear.FetchedAttachment{
					{Title: "Design doc", URL: "https://example.com/doc"},
				},
			},
			mustHave: []string{"Linked references", "Design doc", "https://example.com/doc"},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := buildIssueApproachPrompt(tc.fetched)
			if tc.fetched == nil {
				require.Empty(t, out, "nil fetched must produce empty prompt; downstream callers handle that as 'no PM approach'")
				return
			}
			for _, want := range tc.mustHave {
				require.True(t, strings.Contains(out, want),
					"prompt %q missing required substring %q", out, want)
			}
			for _, must := range tc.mustNotHave {
				require.False(t, strings.Contains(out, must),
					"prompt %q must NOT contain %q", out, must)
			}
		})
	}
}
