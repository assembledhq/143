package linear

import (
	"context"
	"encoding/json"
	"errors"
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

type createPathClient struct {
	fetch map[string]*FetchedIssue
	teams []TeamKeyInfo
	err   error
}

func (c createPathClient) FetchIssue(_ context.Context, identifier string) (*FetchedIssue, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.fetch[identifier], nil
}

func (c createPathClient) ListTeamKeys(context.Context) ([]TeamKeyInfo, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.teams, nil
}

func (c createPathClient) CreateOrUpdateAttachment(context.Context, AttachmentWriteInput) (AttachmentResult, error) {
	return AttachmentResult{}, errors.New("CreateOrUpdateAttachment not used")
}

func (c createPathClient) CreateComment(context.Context, string, string) (string, error) {
	return "", errors.New("CreateComment not used")
}

func (c createPathClient) UpdateComment(context.Context, string, string) error {
	return errors.New("UpdateComment not used")
}

func (c createPathClient) FindRecentBotCommentByURL(context.Context, string, string) (string, error) {
	return "", nil
}

func (c createPathClient) WorkflowStateForType(context.Context, string, []string, string) (*WorkflowState, error) {
	return nil, errors.New("WorkflowStateForType not used")
}

func (c createPathClient) UpdateIssueState(context.Context, string, string) error {
	return errors.New("UpdateIssueState not used")
}

func (c createPathClient) IssueRecentHumanEdits(context.Context, string, time.Time) (bool, error) {
	return false, errors.New("IssueRecentHumanEdits not used")
}

func (c createPathClient) HasGitHubIntegrationAttachment(context.Context, string) (bool, error) {
	return false, errors.New("HasGitHubIntegrationAttachment not used")
}

func (c createPathClient) AgentActivityCreate(context.Context, AgentActivityInput) (AgentActivityResult, error) {
	return AgentActivityResult{}, errors.New("AgentActivityCreate not used")
}

func (c createPathClient) AgentSessionUpdate(context.Context, AgentSessionUpdateInput) error {
	return errors.New("AgentSessionUpdate not used")
}

func (c createPathClient) AgentSessionGet(context.Context, string) (*FetchedAgentSession, error) {
	return nil, errors.New("AgentSessionGet not used")
}

func (c createPathClient) FetchComment(context.Context, string) (*FetchedComment, error) {
	return nil, errors.New("FetchComment not used")
}

type createPathIntegrationReader struct {
	integration models.Integration
	err         error
}

func (r createPathIntegrationReader) GetByOrgAndProvider(context.Context, uuid.UUID, models.IntegrationProvider) (models.Integration, error) {
	if r.err != nil {
		return models.Integration{}, r.err
	}
	return r.integration, nil
}

type createPathCredentialReader struct {
	credential *models.DecryptedCredential
	err        error
}

func (r createPathCredentialReader) Get(context.Context, uuid.UUID, models.ProviderName) (*models.DecryptedCredential, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.credential, nil
}

func newCreatePathService(mock pgxmock.PgxPoolIface, client Client, providerState providerStateStore) *Service {
	return &Service{
		logger:        zerolog.Nop(),
		integrations:  fakeIntegrationReader{},
		credentials:   fakeCredentialReader{},
		issues:        db.NewIssueStore(mock),
		links:         db.NewSessionIssueLinkStore(mock),
		sessions:      db.NewSessionStore(mock),
		providerState: providerState,
		teamKeyCache:  &teamKeyAllowlistCache{},
		clientFactory: func(context.Context, string) (Client, error) {
			return client, nil
		},
	}
}

func fetchedIssueForTest(identifier string) *FetchedIssue {
	return &FetchedIssue{
		ID:            "linear-" + identifier,
		Identifier:    identifier,
		Title:         "Fix " + identifier,
		Description:   "issue body",
		URL:           "https://linear.app/acme/issue/" + identifier,
		StateName:     "Todo",
		StateType:     "unstarted",
		TeamID:        "team-1",
		TeamKey:       "ACS",
		TeamName:      "Core",
		WorkspaceSlug: "acme",
		Comments:      []FetchedComment{{Author: "Ada", Body: "context", CreatedAt: time.Date(2026, 4, 27, 10, 0, 0, 0, time.UTC)}},
		Attachments:   []FetchedAttachment{{Title: "Spec", URL: "https://example.test/spec"}},
	}
}

func expectIssueUpsert(t *testing.T, mock pgxmock.PgxPoolIface, issueID uuid.UUID, now time.Time) {
	t.Helper()
	mock.ExpectQuery("INSERT INTO issues").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(issueID, now, now))
}

func expectLinearLinkInsert(t *testing.T, mock pgxmock.PgxPoolIface, linkID uuid.UUID) {
	t.Helper()
	mock.ExpectQuery("INSERT INTO session_issue_links").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(linkID))
}

func expectPrepareStateUpdate(t *testing.T, mock pgxmock.PgxPoolIface, rowsAffected int64) {
	t.Helper()
	mock.ExpectExec("UPDATE sessions[\\s\\S]+SET linear_prepare_state").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", rowsAffected))
}

func expectPrepareStateUpdateError(t *testing.T, mock pgxmock.PgxPoolIface, err error) {
	t.Helper()
	mock.ExpectExec("UPDATE sessions[\\s\\S]+SET linear_prepare_state").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(err)
}

func expectIdentifierHintUpdate(t *testing.T, mock pgxmock.PgxPoolIface, rowsAffected int64) {
	t.Helper()
	mock.ExpectExec("UPDATE sessions[\\s\\S]+SET linear_identifier_hint").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", rowsAffected))
}

func TestResolveAndLinkAtCreate(t *testing.T) {
	t.Parallel()

	t.Run("disabled integration is no-op", func(t *testing.T) {
		t.Parallel()

		svc := &Service{}
		got, err := svc.ResolveAndLinkAtCreate(context.Background(), CreateInput{OrgID: uuid.New(), SessionID: uuid.New(), MessageBody: "https://linear.app/acme/issue/ACS-1"})
		require.NoError(t, err, "ResolveAndLinkAtCreate should silently no-op when Linear is disabled")
		require.Equal(t, CreateResult{PrepareInline: true}, got, "disabled Linear should leave the session unblocked")
	})

	t.Run("no refs is no-op", func(t *testing.T) {
		t.Parallel()

		svc := &Service{integrations: fakeIntegrationReader{}}
		got, err := svc.ResolveAndLinkAtCreate(context.Background(), CreateInput{OrgID: uuid.New(), SessionID: uuid.New(), MessageBody: "plain request"})
		require.NoError(t, err, "ResolveAndLinkAtCreate should not fail when no Linear refs are present")
		require.Equal(t, CreateResult{PrepareInline: true}, got, "sessions without refs should continue inline")
	})

	t.Run("team key allowlist errors are returned", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		svc := newCreatePathService(mock, createPathClient{}, nil)
		svc.teamKeys = db.NewLinearTeamKeyStore(mock)
		mock.ExpectQuery(`SELECT k\.org_id, k\.integration_id, k\.workspace_id, k\.team_id, k\.team_key, k\.team_name, k\.refreshed_at`).
			WithArgs(pgxmock.AnyArg()).
			WillReturnError(errors.New("db unavailable"))

		got, err := svc.ResolveAndLinkAtCreate(context.Background(), CreateInput{
			OrgID:       uuid.New(),
			SessionID:   uuid.New(),
			MessageBody: "please start from ACS-123",
		})
		require.Error(t, err, "ResolveAndLinkAtCreate should return allowlist load errors")
		require.Contains(t, err.Error(), "load team key allowlist", "ResolveAndLinkAtCreate should wrap allowlist errors")
		require.Equal(t, CreateResult{PrepareInline: true}, got, "allowlist errors should leave sessions unblocked")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("failed inline resolve gates run and enqueues worker", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		enqueued := false
		svc := newCreatePathService(mock, createPathClient{err: errors.New("linear timeout")}, nil)
		svc.SetJobEnqueuer(func(_ context.Context, gotOrgID uuid.UUID, jobType string, payload any, dedupeKey *string) error {
			require.Equal(t, orgID, gotOrgID, "enqueue should preserve org scope")
			require.Equal(t, "prepare_linear_primary", jobType, "enqueue should schedule the prepare worker")
			require.NotNil(t, dedupeKey, "enqueue should use a dedupe key")
			require.Contains(t, *dedupeKey, sessionID.String(), "dedupe key should include the session id")
			require.NotNil(t, payload, "enqueue should include a payload")
			enqueued = true
			return nil
		})
		expectPrepareStateUpdate(t, mock, 1)

		got, err := svc.ResolveAndLinkAtCreate(context.Background(), CreateInput{
			OrgID:       orgID,
			SessionID:   sessionID,
			MessageBody: "please start from https://linear.app/acme/issue/ACS-123",
		})
		require.NoError(t, err, "ResolveAndLinkAtCreate should fall back to async on inline resolution failure")
		require.Equal(t, CreateResult{PrepareInline: false}, got, "failed inline resolution should gate the session")
		require.True(t, enqueued, "ResolveAndLinkAtCreate should enqueue async linking")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("cross workspace URL ref is dropped without async fallback", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		svc := newCreatePathService(mock, createPathClient{fetch: map[string]*FetchedIssue{
			"ACS-123": {
				ID:            "linear-ACS-123",
				Identifier:    "ACS-123",
				WorkspaceSlug: "other-workspace",
			},
		}}, nil)
		svc.SetJobEnqueuer(func(context.Context, uuid.UUID, string, any, *string) error {
			require.Fail(t, "cross-workspace refs must not enqueue async fallback work")
			return nil
		})

		got, err := svc.ResolveAndLinkAtCreate(context.Background(), CreateInput{
			OrgID:       uuid.New(),
			SessionID:   uuid.New(),
			MessageBody: "please start from https://linear.app/acme/issue/ACS-123",
		})
		require.NoError(t, err, "ResolveAndLinkAtCreate should silently drop cross-workspace URL refs")
		require.Equal(t, CreateResult{PrepareInline: true}, got, "cross-workspace refs should leave the session unblocked")
		require.NoError(t, mock.ExpectationsWereMet(), "cross-workspace drops should not write prepare state or links")
	})

	t.Run("failed inline resolve returns error when pending state cannot be persisted", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		svc := newCreatePathService(mock, createPathClient{err: errors.New("linear timeout")}, nil)
		svc.SetJobEnqueuer(func(context.Context, uuid.UUID, string, any, *string) error {
			require.Fail(t, "enqueue must not run when the prepare-state gate was not persisted")
			return nil
		})
		mock.ExpectExec("UPDATE sessions[\\s\\S]+SET linear_prepare_state").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(errors.New("db unavailable"))

		got, err := svc.ResolveAndLinkAtCreate(context.Background(), CreateInput{
			OrgID:       uuid.New(),
			SessionID:   uuid.New(),
			MessageBody: "please start from https://linear.app/acme/issue/ACS-123",
		})
		require.Error(t, err, "ResolveAndLinkAtCreate should fail closed when it cannot persist the prepare-state gate")
		require.Contains(t, err.Error(), "mark linear prepare pending", "ResolveAndLinkAtCreate should wrap the prepare-state error")
		require.Equal(t, CreateResult{}, got, "prepare-state failures should not report an unblocked create result")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("failed inline resolve marks failed when enqueue fallback fails", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		svc := newCreatePathService(mock, createPathClient{err: errors.New("linear timeout")}, nil)
		svc.SetJobEnqueuer(func(context.Context, uuid.UUID, string, any, *string) error {
			return errors.New("queue unavailable")
		})
		expectPrepareStateUpdate(t, mock, 1)
		expectPrepareStateUpdate(t, mock, 1)

		got, err := svc.ResolveAndLinkAtCreate(context.Background(), CreateInput{
			OrgID:       uuid.New(),
			SessionID:   uuid.New(),
			MessageBody: "please start from https://linear.app/acme/issue/ACS-123",
		})
		require.Error(t, err, "ResolveAndLinkAtCreate should fail closed when the fallback worker cannot be enqueued")
		require.Contains(t, err.Error(), "enqueue linear prepare worker", "ResolveAndLinkAtCreate should wrap the enqueue error")
		require.Equal(t, CreateResult{}, got, "enqueue failures should not report an unblocked create result")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("rejected primary link does not block create path", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		now := time.Now().UTC()
		orgID := uuid.New()
		sessionID := uuid.New()
		issueID := uuid.New()
		svc := newCreatePathService(mock, createPathClient{fetch: map[string]*FetchedIssue{"ACS-123": fetchedIssueForTest("ACS-123")}}, newFakeProviderStateStore())

		expectIssueUpsert(t, mock, issueID, now)
		mock.ExpectQuery("INSERT INTO session_issue_links").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(pgx.ErrNoRows)
		mock.ExpectQuery("SELECT id FROM session_issue_links").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(pgx.ErrNoRows)

		got, err := svc.ResolveAndLinkAtCreate(context.Background(), CreateInput{
			OrgID:       orgID,
			SessionID:   sessionID,
			MessageBody: "please start from https://linear.app/acme/issue/ACS-123",
		})
		require.NoError(t, err, "ResolveAndLinkAtCreate should swallow rejected primary links")
		require.Equal(t, CreateResult{PrepareInline: true}, got, "rejected primary links should not gate the session")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("resolved primary links and snapshots context", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		now := time.Now().UTC()
		orgID := uuid.New()
		sessionID := uuid.New()
		issueID := uuid.New()
		linkID := uuid.New()
		provider := newFakeProviderStateStore()
		svc := newCreatePathService(mock, createPathClient{fetch: map[string]*FetchedIssue{"ACS-123": fetchedIssueForTest("ACS-123")}}, provider)

		expectIssueUpsert(t, mock, issueID, now)
		expectLinearLinkInsert(t, mock, linkID)
		expectPrepareStateUpdate(t, mock, 1)

		got, err := svc.ResolveAndLinkAtCreate(context.Background(), CreateInput{
			OrgID:       orgID,
			SessionID:   sessionID,
			MessageBody: "please start from https://linear.app/acme/issue/ACS-123",
		})
		require.NoError(t, err, "ResolveAndLinkAtCreate should link a resolvable primary")
		require.Equal(t, CreateResult{PrepareInline: true, PrimaryIdentifier: "ACS-123", PrimaryTitle: "Fix ACS-123"}, got, "ResolveAndLinkAtCreate should return primary metadata")
		state, err := provider.Get(context.Background(), orgID, linkID)
		require.NoError(t, err, "provider state should be readable")
		require.Equal(t, "ACS-123", state.Identifier, "LinkResolved should persist the Linear identifier")
		require.NotEmpty(t, state.PrimarySnapshot, "snapshotPrimaryContext should store turn-zero context")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("resolved primary enqueues linked milestone", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		now := time.Now().UTC()
		orgID := uuid.New()
		sessionID := uuid.New()
		linkID := uuid.New()
		var enqueuedJobType string
		var enqueuedPayload map[string]any
		var enqueuedDedupe string
		provider := newFakeProviderStateStore()
		svc := newCreatePathService(mock, createPathClient{fetch: map[string]*FetchedIssue{"ACS-123": fetchedIssueForTest("ACS-123")}}, provider)
		svc.SetJobEnqueuer(func(_ context.Context, gotOrgID uuid.UUID, jobType string, payload any, dedupeKey *string) error {
			require.Equal(t, orgID, gotOrgID, "linked milestone enqueue should preserve org scope")
			require.Equal(t, "linear_milestone", jobType, "linked milestone should use the milestone worker")
			require.NotNil(t, dedupeKey, "linked milestone should use a dedupe key")
			enqueuedJobType = jobType
			enqueuedDedupe = *dedupeKey
			body, err := json.Marshal(payload)
			require.NoError(t, err, "linked milestone payload should marshal for assertion")
			require.NoError(t, json.Unmarshal(body, &enqueuedPayload), "linked milestone payload should be an object")
			return nil
		})

		expectIssueUpsert(t, mock, uuid.New(), now)
		expectLinearLinkInsert(t, mock, linkID)
		expectPrepareStateUpdate(t, mock, 1)

		got, err := svc.ResolveAndLinkAtCreate(context.Background(), CreateInput{
			OrgID:       orgID,
			SessionID:   sessionID,
			MessageBody: "please start from https://linear.app/acme/issue/ACS-123",
		})
		require.NoError(t, err, "ResolveAndLinkAtCreate should succeed after enqueueing the linked milestone")
		require.Equal(t, CreateResult{PrepareInline: true, PrimaryIdentifier: "ACS-123", PrimaryTitle: "Fix ACS-123"}, got, "inline primary should still return metadata")
		require.Equal(t, "linear_milestone", enqueuedJobType, "linked milestone enqueue should fire")
		require.Contains(t, enqueuedDedupe, sessionID.String(), "linked milestone dedupe key should include the session id")
		require.Equal(t, orgID.String(), enqueuedPayload["org_id"], "linked milestone payload should include org_id")
		require.Equal(t, sessionID.String(), enqueuedPayload["session_id"], "linked milestone payload should include session_id")
		require.Equal(t, string(MilestoneLinked), enqueuedPayload["event"], "linked milestone payload should use the linked event")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("additional refs are enqueued after inline primary link", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		now := time.Now().UTC()
		orgID := uuid.New()
		sessionID := uuid.New()
		linkID := uuid.New()
		var enqueuedIdentifiers []string
		var linkedMilestoneEnqueued bool
		svc := newCreatePathService(mock, createPathClient{fetch: map[string]*FetchedIssue{"ACS-123": fetchedIssueForTest("ACS-123")}}, newFakeProviderStateStore())
		svc.SetJobEnqueuer(func(_ context.Context, _ uuid.UUID, jobType string, payload any, dedupeKey *string) error {
			require.NotNil(t, dedupeKey, "additional refs should use a dedupe key")
			body, err := json.Marshal(payload)
			require.NoError(t, err, "enqueued payload should marshal for assertion")
			switch jobType {
			case "linear_milestone":
				var decoded struct {
					Event string `json:"event"`
				}
				require.NoError(t, json.Unmarshal(body, &decoded), "milestone payload should include event")
				linkedMilestoneEnqueued = decoded.Event == string(MilestoneLinked)
			case "link_linear_issue":
				var decoded struct {
					Identifiers []string `json:"identifiers"`
				}
				require.NoError(t, json.Unmarshal(body, &decoded), "enqueued payload should include identifiers")
				enqueuedIdentifiers = decoded.Identifiers
			default:
				require.Failf(t, "unexpected job type", "jobType=%s", jobType)
			}
			return nil
		})

		expectIssueUpsert(t, mock, uuid.New(), now)
		expectLinearLinkInsert(t, mock, linkID)
		expectPrepareStateUpdate(t, mock, 1)

		got, err := svc.ResolveAndLinkAtCreate(context.Background(), CreateInput{
			OrgID:       orgID,
			SessionID:   sessionID,
			MessageBody: "please start from https://linear.app/acme/issue/ACS-123 and then https://linear.app/acme/issue/ACS-124",
			UserID:      &orgID,
		})
		require.NoError(t, err, "ResolveAndLinkAtCreate should succeed with additional refs")
		require.Equal(t, CreateResult{PrepareInline: true, PrimaryIdentifier: "ACS-123", PrimaryTitle: "Fix ACS-123"}, got, "inline primary should still return metadata")
		require.True(t, linkedMilestoneEnqueued, "inline primary should enqueue the initial linked milestone even when related refs exist")
		require.Equal(t, []string{"ACS-123", "ACS-124"}, enqueuedIdentifiers, "additional refs should be forwarded with the primary first so the worker preserves roles")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("snapshot failures do not block inline primary link", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		now := time.Now().UTC()
		orgID := uuid.New()
		sessionID := uuid.New()
		linkID := uuid.New()
		provider := newFakeProviderStateStore()
		provider.getErr = errors.New("provider state unavailable")
		svc := newCreatePathService(mock, createPathClient{fetch: map[string]*FetchedIssue{"ACS-123": fetchedIssueForTest("ACS-123")}}, provider)

		expectIssueUpsert(t, mock, uuid.New(), now)
		expectLinearLinkInsert(t, mock, linkID)
		expectPrepareStateUpdate(t, mock, 1)

		got, err := svc.ResolveAndLinkAtCreate(context.Background(), CreateInput{
			OrgID:       orgID,
			SessionID:   sessionID,
			MessageBody: "please start from https://linear.app/acme/issue/ACS-123",
		})
		require.NoError(t, err, "ResolveAndLinkAtCreate should ignore snapshot failures")
		require.Equal(t, CreateResult{PrepareInline: true, PrimaryIdentifier: "ACS-123", PrimaryTitle: "Fix ACS-123"}, got, "snapshot failures should not clear primary metadata")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("ready-flip failure on inline success is non-fatal", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		now := time.Now().UTC()
		orgID := uuid.New()
		sessionID := uuid.New()
		linkID := uuid.New()
		provider := newFakeProviderStateStore()
		svc := newCreatePathService(mock, createPathClient{fetch: map[string]*FetchedIssue{"ACS-123": fetchedIssueForTest("ACS-123")}}, provider)

		expectIssueUpsert(t, mock, uuid.New(), now)
		expectLinearLinkInsert(t, mock, linkID)
		// Simulate a transient DB hiccup on the final "ready" flip. The
		// inline path must still return success — the run-agent gate
		// passes through on the default "none" state, so turn 1 still
		// boots with the snapshot already written.
		mock.ExpectExec("UPDATE sessions[\\s\\S]+SET linear_prepare_state").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(errors.New("db hiccup"))

		got, err := svc.ResolveAndLinkAtCreate(context.Background(), CreateInput{
			OrgID:       orgID,
			SessionID:   sessionID,
			MessageBody: "please start from https://linear.app/acme/issue/ACS-123",
		})
		require.NoError(t, err, "ready-flip DB failures must NOT propagate; the link is durable and the run-agent gate falls through on default state")
		require.Equal(t, CreateResult{PrepareInline: true, PrimaryIdentifier: "ACS-123", PrimaryTitle: "Fix ACS-123"}, got, "ready-flip failure should not clear primary metadata")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

}

func TestLinkRelatedLinearIssues(t *testing.T) {
	t.Parallel()

	t.Run("primary replay failure does not fail prepared session", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		now := time.Now().UTC()
		orgID := uuid.New()
		sessionID := uuid.New()
		relatedLinkID := uuid.New()
		svc := newCreatePathService(mock, createPathClient{fetch: map[string]*FetchedIssue{
			"ACS-124": fetchedIssueForTest("ACS-124"),
		}}, newFakeProviderStateStore())
		expectIssueUpsert(t, mock, uuid.New(), now)
		expectLinearLinkInsert(t, mock, relatedLinkID)

		err = svc.LinkRelatedLinearIssues(context.Background(), orgID, sessionID, []string{"ACS-123", "ACS-124"}, nil)
		require.NoError(t, err, "related-link catch-up should not fail the session when primary replay cannot resolve")
		require.NoError(t, mock.ExpectationsWereMet(), "primary replay failure should not write linear_prepare_state")
	})
}

func TestPrepareLinearPrimary(t *testing.T) {
	t.Parallel()

	t.Run("empty identifiers clears prepare state", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		svc := newCreatePathService(mock, createPathClient{}, nil)
		expectPrepareStateUpdate(t, mock, 1)

		err = svc.PrepareLinearPrimary(context.Background(), uuid.New(), uuid.New(), nil, nil)
		require.NoError(t, err, "PrepareLinearPrimary should clear state when no identifiers are present")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("empty identifiers surfaces prepare state clear failure", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		svc := newCreatePathService(mock, createPathClient{}, nil)
		expectPrepareStateUpdateError(t, mock, errors.New("db unavailable"))

		err = svc.PrepareLinearPrimary(context.Background(), uuid.New(), uuid.New(), nil, nil)
		require.Error(t, err, "PrepareLinearPrimary should retry when clearing pending prepare state fails")
		require.ErrorContains(t, err, "clear linear prepare state", "error should identify the failed state clear")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("resolution failure leaves prepare state pending until job dead-letter", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		svc := newCreatePathService(mock, createPathClient{err: errors.New("linear unavailable")}, nil)

		err = svc.PrepareLinearPrimary(context.Background(), uuid.New(), uuid.New(), []string{"ACS-123"}, nil)
		require.Error(t, err, "PrepareLinearPrimary should surface resolution errors so the worker can retry")
		require.NoError(t, mock.ExpectationsWereMet(), "transient failures should not mark prepare state failed before retries exhaust")
	})

	t.Run("link failure leaves prepare state pending until job dead-letter", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		svc := newCreatePathService(mock, createPathClient{fetch: map[string]*FetchedIssue{
			"ACS-123": fetchedIssueForTest("ACS-123"),
		}}, nil)
		expectIssueUpsert(t, mock, uuid.New(), time.Now())
		mock.ExpectQuery("INSERT INTO session_issue_links").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(errors.New("repo mismatch"))

		err = svc.PrepareLinearPrimary(context.Background(), uuid.New(), uuid.New(), []string{"ACS-123"}, nil)
		require.Error(t, err, "PrepareLinearPrimary should surface link errors so the worker can retry")
		require.NoError(t, mock.ExpectationsWereMet(), "link failures should not mark prepare state failed before retries exhaust")
	})

	t.Run("cross workspace worker ref clears prepare state without failing session", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		svc := newCreatePathService(mock, createPathClient{fetch: map[string]*FetchedIssue{
			"ACS-123": {
				ID:            "linear-ACS-123",
				Identifier:    "ACS-123",
				WorkspaceSlug: "other-workspace",
			},
		}}, nil)
		expectPrepareStateUpdate(t, mock, 1)

		err = svc.PrepareLinearPrimaryRefs(context.Background(), uuid.New(), uuid.New(), []LinkRef{{Identifier: "ACS-123", Workspace: "acme"}}, nil)
		require.NoError(t, err, "PrepareLinearPrimaryRefs should silently drop cross-workspace URL refs")
		require.NoError(t, mock.ExpectationsWereMet(), "cross-workspace worker drops should only clear prepare state")
	})

	t.Run("cross workspace worker ref surfaces prepare state clear failure", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		svc := newCreatePathService(mock, createPathClient{fetch: map[string]*FetchedIssue{
			"ACS-123": {
				ID:            "linear-ACS-123",
				Identifier:    "ACS-123",
				WorkspaceSlug: "other-workspace",
			},
		}}, nil)
		expectPrepareStateUpdateError(t, mock, errors.New("db unavailable"))

		err = svc.PrepareLinearPrimaryRefs(context.Background(), uuid.New(), uuid.New(), []LinkRef{{Identifier: "ACS-123", Workspace: "acme"}}, nil)
		require.Error(t, err, "PrepareLinearPrimaryRefs should retry when clearing a cross-workspace pending state fails")
		require.ErrorContains(t, err, "clear linear prepare state", "error should identify the failed state clear")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("links primary and related identifiers", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		now := time.Now().UTC()
		orgID := uuid.New()
		sessionID := uuid.New()
		primaryLinkID := uuid.New()
		relatedLinkID := uuid.New()
		provider := newFakeProviderStateStore()
		var enqueuedEvent string
		svc := newCreatePathService(mock, createPathClient{fetch: map[string]*FetchedIssue{
			"ACS-123": fetchedIssueForTest("ACS-123"),
			"ACS-124": fetchedIssueForTest("ACS-124"),
		}}, provider)
		svc.SetJobEnqueuer(func(_ context.Context, gotOrgID uuid.UUID, jobType string, payload any, dedupeKey *string) error {
			require.Equal(t, orgID, gotOrgID, "prepare worker milestone enqueue should preserve org scope")
			require.Equal(t, "linear_milestone", jobType, "prepare worker should enqueue the linked milestone after primary link")
			require.NotNil(t, dedupeKey, "prepare worker milestone should use a dedupe key")
			body, err := json.Marshal(payload)
			require.NoError(t, err, "prepare worker milestone payload should marshal for assertion")
			var decoded struct {
				Event string `json:"event"`
			}
			require.NoError(t, json.Unmarshal(body, &decoded), "prepare worker milestone payload should include event")
			enqueuedEvent = decoded.Event
			return nil
		})

		expectIssueUpsert(t, mock, uuid.New(), now)
		expectLinearLinkInsert(t, mock, primaryLinkID)
		expectIdentifierHintUpdate(t, mock, 1)
		expectIssueUpsert(t, mock, uuid.New(), now)
		expectLinearLinkInsert(t, mock, relatedLinkID)
		expectPrepareStateUpdate(t, mock, 1)

		err = svc.PrepareLinearPrimary(context.Background(), orgID, sessionID, []string{"ACS-123", "ACS-124"}, nil)
		require.NoError(t, err, "PrepareLinearPrimary should resolve and link primary plus related refs")
		primaryState, err := provider.Get(context.Background(), orgID, primaryLinkID)
		require.NoError(t, err, "primary provider state should be readable")
		require.Equal(t, "ACS-123", primaryState.Identifier, "primary link should persist the resolved identifier")
		relatedState, err := provider.Get(context.Background(), orgID, relatedLinkID)
		require.NoError(t, err, "related provider state should be readable")
		require.Equal(t, "ACS-124", relatedState.Identifier, "related link should persist the resolved identifier")
		require.Equal(t, string(MilestoneLinked), enqueuedEvent, "prepare worker should enqueue the initial linked milestone")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})
}

// TestPrepareLinearPrimaryRefs_IdempotentOnRetry pins the worker contract:
// `prepare_linear_primary` is retried on transient failures (Linear 5xx,
// network blips, lease loss), so a second invocation with the same payload
// must produce the same outcome — same link UUID surfaced, same provider
// state recorded, prepare_state stays "ready" — without errors. This is
// the property the worker job's at-least-once delivery relies on.
//
// The DB-level `ON CONFLICT (session_id, issue_id) DO NOTHING` on
// session_issue_links plus the lookupLinkID fallback give us idempotency
// at the link layer; the test here exercises the full PrepareLinearPrimary
// path twice and asserts that nothing breaks.
func TestPrepareLinearPrimaryRefs_IdempotentOnRetry(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	now := time.Now().UTC()
	orgID := uuid.New()
	sessionID := uuid.New()
	primaryLinkID := uuid.New()
	provider := newFakeProviderStateStore()
	enqueueCount := 0
	svc := newCreatePathService(mock, createPathClient{fetch: map[string]*FetchedIssue{
		"ACS-123": fetchedIssueForTest("ACS-123"),
	}}, provider)
	svc.SetJobEnqueuer(func(_ context.Context, _ uuid.UUID, _ string, _ any, _ *string) error {
		enqueueCount++
		return nil
	})

	// First attempt: full happy path (issue upsert → link insert → identifier
	// hint → prepare_state=ready).
	expectIssueUpsert(t, mock, uuid.New(), now)
	expectLinearLinkInsert(t, mock, primaryLinkID)
	expectIdentifierHintUpdate(t, mock, 1)
	expectPrepareStateUpdate(t, mock, 1)

	require.NoError(t, svc.PrepareLinearPrimaryRefs(context.Background(), orgID, sessionID,
		[]LinkRef{{Identifier: "ACS-123"}}, nil), "first invocation should succeed")

	state, err := provider.Get(context.Background(), orgID, primaryLinkID)
	require.NoError(t, err, "primary provider state should be readable after first attempt")
	require.Equal(t, "ACS-123", state.Identifier, "primary link should record the identifier")
	require.Equal(t, 1, enqueueCount, "linked-milestone should enqueue exactly once on first attempt")

	// Second attempt: the worker retried after a transient failure outside
	// our control (e.g. lease loss after success). The replay must surface
	// no error and the link UUID must be the same — idempotency would be
	// broken if a stale CommentID or duplicate row leaked through.
	//
	// At the DB layer the SQL is the same shape: a fresh issue upsert (UPDATE
	// path on conflict), a link INSERT that hits ON CONFLICT DO NOTHING and
	// returns the existing id via lookup, identifier hint update, and another
	// prepare_state UPDATE (no-op transition "ready" → "ready").
	expectIssueUpsert(t, mock, uuid.New(), now)
	// On conflict: insert returns ErrNoRows, store falls back to lookupLinkID.
	mock.ExpectQuery("INSERT INTO session_issue_links").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`SELECT id FROM session_issue_links`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(primaryLinkID))
	expectIdentifierHintUpdate(t, mock, 1)
	expectPrepareStateUpdate(t, mock, 1)

	require.NoError(t, svc.PrepareLinearPrimaryRefs(context.Background(), orgID, sessionID,
		[]LinkRef{{Identifier: "ACS-123"}}, nil), "retried invocation must not surface a 'duplicate link' error")

	stateAfterRetry, err := provider.Get(context.Background(), orgID, primaryLinkID)
	require.NoError(t, err, "primary provider state should remain readable after retry")
	require.Equal(t, "ACS-123", stateAfterRetry.Identifier, "retry must not corrupt the identifier")
	require.Equal(t, 2, enqueueCount, "linked-milestone re-enqueue is acceptable; the milestone job's own dedupe key is the fire-once gate")

	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met across both attempts")
}

// TestLinkRelatedLinearIssues_IdempotentOnRetry pins the same retry contract
// for the related-only path. The link_linear_issue worker fires after the
// primary is already prepared, and a transient retry must produce the same
// link UUIDs without surfacing duplicate-row errors.
func TestLinkRelatedLinearIssues_IdempotentOnRetry(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	now := time.Now().UTC()
	orgID := uuid.New()
	sessionID := uuid.New()
	relatedLinkID := uuid.New()
	svc := newCreatePathService(mock, createPathClient{fetch: map[string]*FetchedIssue{
		"ACS-124": fetchedIssueForTest("ACS-124"),
	}}, newFakeProviderStateStore())

	// First attempt: fresh insert for ACS-124 (the primary "ACS-123" is
	// always skipped on this code path — it's already linked).
	expectIssueUpsert(t, mock, uuid.New(), now)
	expectLinearLinkInsert(t, mock, relatedLinkID)

	require.NoError(t, svc.LinkRelatedLinearIssues(context.Background(), orgID, sessionID,
		[]string{"ACS-123", "ACS-124"}, nil), "first related-link attempt should succeed")

	// Second attempt (retry): ON CONFLICT DO NOTHING falls through to lookup,
	// which returns the same link UUID. No errors, no spurious skips.
	expectIssueUpsert(t, mock, uuid.New(), now)
	mock.ExpectQuery("INSERT INTO session_issue_links").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`SELECT id FROM session_issue_links`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(relatedLinkID))

	require.NoError(t, svc.LinkRelatedLinearIssues(context.Background(), orgID, sessionID,
		[]string{"ACS-123", "ACS-124"}, nil), "retried related-link attempt must not surface duplicate errors")

	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met across both attempts")
}

func TestResolveWithBudgetCrossWorkspace(t *testing.T) {
	t.Parallel()

	svc := &Service{
		integrations: fakeIntegrationReader{},
		credentials:  fakeCredentialReader{},
		clientFactory: func(context.Context, string) (Client, error) {
			return createPathClient{fetch: map[string]*FetchedIssue{
				"ACS-123": {
					ID:            "linear-ACS-123",
					Identifier:    "ACS-123",
					WorkspaceSlug: "other-workspace",
				},
			}}, nil
		},
	}

	resolved, err := svc.resolveWithBudget(context.Background(), uuid.New(), Detected{Identifier: "ACS-123", Workspace: "acme"})
	require.ErrorIs(t, err, errLinearRefDropped, "resolveWithBudget should distinguish cross-workspace drops from async fallback")
	require.Nil(t, resolved, "cross-workspace refs should not resolve a primary")
}

func TestServiceIntegrationAndTeamKeys(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	activeIntegration := models.Integration{ID: uuid.New(), Status: "active"}

	t.Run("enabled checks active integration", func(t *testing.T) {
		t.Parallel()

		require.False(t, (*Service)(nil).Enabled(context.Background(), orgID), "nil service should not be enabled")
		require.False(t, (&Service{integrations: createPathIntegrationReader{err: errors.New("not found")}}).Enabled(context.Background(), orgID), "integration lookup errors should disable Linear")
		require.False(t, (&Service{integrations: createPathIntegrationReader{integration: models.Integration{Status: "inactive"}}}).Enabled(context.Background(), orgID), "inactive integration should disable Linear")
		require.True(t, (&Service{integrations: createPathIntegrationReader{integration: activeIntegration}}).Enabled(context.Background(), orgID), "active integration should enable Linear")
	})

	t.Run("integrationFor maps pgx.ErrNoRows to ErrIntegrationNotFound", func(t *testing.T) {
		t.Parallel()
		// Workers branch on errors.Is(err, ErrIntegrationNotFound) to dead-letter
		// instead of burning the 8-minute retryable window — guard the pgx.ErrNoRows
		// → sentinel mapping so a future swap of the store query (e.g. CollectOneRow
		// → manual scan) doesn't quietly drop the classification.
		svc := &Service{integrations: createPathIntegrationReader{err: pgx.ErrNoRows}}
		_, _, err := svc.integrationFor(context.Background(), orgID)
		require.Error(t, err, "integrationFor should surface the no-rows lookup")
		require.ErrorIs(t, err, ErrIntegrationNotFound, "integrationFor should map pgx.ErrNoRows to the worker-fatal sentinel")
	})

	t.Run("integrationFor preserves non-no-rows lookup errors", func(t *testing.T) {
		t.Parallel()
		// A transient query-level failure must NOT masquerade as the missing-row
		// sentinel — workers would otherwise dead-letter on a flake.
		lookupErr := errors.New("connection reset by peer")
		svc := &Service{integrations: createPathIntegrationReader{err: lookupErr}}
		_, _, err := svc.integrationFor(context.Background(), orgID)
		require.Error(t, err, "integrationFor should surface non-no-rows errors")
		require.ErrorIs(t, err, lookupErr, "integrationFor should preserve transient lookup errors")
		require.NotErrorIs(t, err, ErrIntegrationNotFound, "transient errors should not collapse onto the missing-row sentinel")
	})

	t.Run("integrationFor validates credential shape", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name        string
			credentials CredentialReader
			expectedErr string
		}{
			{name: "credential lookup error", credentials: createPathCredentialReader{err: errors.New("credential lookup failed")}, expectedErr: "lookup linear credential"},
			{name: "missing credential", credentials: createPathCredentialReader{}, expectedErr: "linear credential not found"},
			{name: "wrong config type", credentials: createPathCredentialReader{credential: &models.DecryptedCredential{Config: models.AnthropicConfig{}}}, expectedErr: "wrong type"},
		}
		for _, tt := range tests {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				svc := &Service{integrations: createPathIntegrationReader{integration: activeIntegration}, credentials: tt.credentials}
				_, _, err := svc.integrationFor(context.Background(), orgID)
				require.Error(t, err, "integrationFor should reject invalid credential state")
				require.Contains(t, err.Error(), tt.expectedErr, "integrationFor should wrap the expected failure")
			})
		}

		svc := &Service{
			integrations: createPathIntegrationReader{integration: activeIntegration},
			credentials:  createPathCredentialReader{credential: &models.DecryptedCredential{Config: models.LinearConfig{AccessToken: "tok"}}},
		}
		integration, token, err := svc.integrationFor(context.Background(), orgID)
		require.NoError(t, err, "integrationFor should return active integration credentials")
		require.Equal(t, activeIntegration.ID, integration.ID, "integrationFor should preserve integration row")
		require.Equal(t, "tok", token, "integrationFor should extract Linear access token")
	})

	t.Run("team key allowlist caches database results", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		mock.ExpectQuery(`SELECT k\.org_id, k\.integration_id, k\.workspace_id, k\.team_id, k\.team_key, k\.team_name, k\.refreshed_at`).
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"org_id", "integration_id", "workspace_id", "team_id", "team_key", "team_name", "refreshed_at"}).
				AddRow(orgID, activeIntegration.ID, "workspace-1", "team-1", "ACS", "Core", time.Now().UTC()))

		svc := &Service{teamKeys: db.NewLinearTeamKeyStore(mock), teamKeyCache: NewTeamKeyCache()}
		allow, err := svc.TeamKeyAllowlist(context.Background(), orgID)
		require.NoError(t, err, "TeamKeyAllowlist should load team keys")
		require.Equal(t, map[string]bool{"ACS": true}, allow, "TeamKeyAllowlist should build lookup table")
		cached, err := svc.TeamKeyAllowlist(context.Background(), orgID)
		require.NoError(t, err, "TeamKeyAllowlist should return cached entries")
		require.Equal(t, allow, cached, "TeamKeyAllowlist should cache per org")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})
}

func TestRefreshTeamKeys(t *testing.T) {
	t.Parallel()

	activeIntegration := models.Integration{ID: uuid.New(), Status: "active"}

	t.Run("refreshes and invalidates cache", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		orgID := uuid.New()
		integrationID := uuid.New()
		mock.ExpectBegin()
		mock.ExpectExec("DELETE FROM linear_team_keys").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("DELETE", 1))
		mock.ExpectExec("INSERT INTO linear_team_keys").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
		mock.ExpectCommit()

		svc := &Service{
			integrations: createPathIntegrationReader{integration: models.Integration{ID: integrationID, Status: "active"}},
			credentials:  createPathCredentialReader{credential: &models.DecryptedCredential{Config: models.LinearConfig{AccessToken: "tok"}}},
			teamKeys:     db.NewLinearTeamKeyStore(mock),
			teamKeyCache: NewTeamKeyCache(),
			clientFactory: func(context.Context, string) (Client, error) {
				return createPathClient{teams: []TeamKeyInfo{{TeamID: "team-1", Key: "ACS", Name: "Core", WorkspaceID: "workspace-1"}}}, nil
			},
		}
		svc.teamKeyCache.put(orgID, map[string]bool{"OLD": true})

		err = svc.RefreshTeamKeys(context.Background(), orgID)
		require.NoError(t, err, "RefreshTeamKeys should replace team-key cache")
		_, ok := svc.teamKeyCache.get(orgID)
		require.False(t, ok, "RefreshTeamKeys should invalidate cached allowlists")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("wraps client factory and list errors", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name        string
			factory     ClientFactory
			expectedErr string
		}{
			{
				name: "client factory error",
				factory: func(context.Context, string) (Client, error) {
					return nil, errors.New("factory failed")
				},
				expectedErr: "build linear client",
			},
			{
				name: "list team keys error",
				factory: func(context.Context, string) (Client, error) {
					return createPathClient{err: errors.New("linear unavailable")}, nil
				},
				expectedErr: "list linear team keys",
			},
		}
		for _, tt := range tests {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				svc := &Service{
					integrations:  createPathIntegrationReader{integration: activeIntegration},
					credentials:   createPathCredentialReader{credential: &models.DecryptedCredential{Config: models.LinearConfig{AccessToken: "tok"}}},
					clientFactory: tt.factory,
				}
				err := svc.RefreshTeamKeys(context.Background(), uuid.New())
				require.Error(t, err, "RefreshTeamKeys should return client/list errors")
				require.Contains(t, err.Error(), tt.expectedErr, "RefreshTeamKeys should wrap errors with context")
			})
		}
	})
}

func TestWithProviderStateLockedUsesTransaction(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()
	linkID := uuid.New()
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO session_issue_link_provider_state").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("SELECT 1 FROM session_issue_link_provider_state").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT state FROM session_issue_link_provider_state").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow([]byte(`{"identifier":"ACS-1"}`)))
	mock.ExpectCommit()

	svc := &Service{pool: mock, providerState: newFakeProviderStateStore(), stateEvents: newFakeStateEventStore()}
	called := false
	err = svc.withProviderStateLocked(context.Background(), orgID, linkID, func(_ context.Context, _ providerStateStore, _ stateEventStore, state db.LinearProviderState) error {
		require.Equal(t, "ACS-1", state.Identifier, "withProviderStateLocked should read locked provider state")
		called = true
		return nil
	})
	require.NoError(t, err, "withProviderStateLocked should commit successful callbacks")
	require.True(t, called, "withProviderStateLocked should call the callback")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestWithProviderStateLockedDuplicateStateEventStillCommits(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	linkID := uuid.New()
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO session_issue_link_provider_state").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("SELECT 1 FROM session_issue_link_provider_state").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT state FROM session_issue_link_provider_state").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"state"}).AddRow([]byte(`{"identifier":"ACS-1"}`)))
	mock.ExpectExec("INSERT INTO session_issue_link_state_events[\\s\\S]+ON CONFLICT \\(session_id, issue_id, event_kind\\) DO NOTHING").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 0))
	mock.ExpectCommit()

	svc := &Service{pool: mock, providerState: newFakeProviderStateStore(), stateEvents: newFakeStateEventStore()}
	err = svc.withProviderStateLocked(context.Background(), orgID, linkID, func(ctx context.Context, _ providerStateStore, txEvents stateEventStore, _ db.LinearProviderState) error {
		err := txEvents.Insert(ctx, orgID, db.LinearStateEventInput{
			SessionID:      sessionID,
			IssueID:        issueID,
			EventKind:      db.LinearStateEventStarted,
			TransitionFrom: "Backlog",
			TransitionTo:   "In Progress",
		})
		require.ErrorIs(t, err, db.ErrLinearStateEventExists, "duplicate fire-once events should surface as the idempotent sentinel")
		return nil
	})
	require.NoError(t, err, "duplicate state event handling should not abort the provider-state transaction")
	require.NoError(t, mock.ExpectationsWereMet(), "duplicate event no-op must still allow the outer transaction to commit")
}

func TestServiceProviderStateSnapshotAndNotifier(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	linkID := uuid.New()
	provider := newFakeProviderStateStore()
	provider.rows[linkID] = db.LinearProviderState{
		AttachmentID:      "attachment-1",
		CommentID:         "comment-1",
		TeamID:            "team-1",
		LastWriteOutcome:  "merged",
		LastSkippedReason: string(db.LinearStateSkipPrivateSession),
		IssueRepoStale:    db.BoolPtr(true),
		LinkAuditReason:   "linear_null_repo_carveout",
	}
	var notified []string
	svc := &Service{providerState: provider}
	svc.SetLinksChangedNotifier(func(_ context.Context, gotOrgID, gotSessionID uuid.UUID, kind string) {
		require.Equal(t, orgID, gotOrgID, "notifier should receive the org scope")
		require.Equal(t, sessionID, gotSessionID, "notifier should receive the session id")
		notified = append(notified, kind)
	})

	svc.notifyLinksChanged(context.Background(), orgID, sessionID, "inserted")
	require.Equal(t, []string{"inserted"}, notified, "notifyLinksChanged should call the configured notifier")
	require.True(t, svc.HasLinearProviderState(context.Background(), orgID, linkID), "HasLinearProviderState should detect populated Linear state")

	snapshot := svc.ProviderStateSnapshot(context.Background(), orgID, linkID)
	require.Equal(t, ProviderStateSnapshot{
		AttachmentPresent: true,
		CommentPresent:    true,
		LastWriteOutcome:  "merged",
		LastSkippedReason: string(db.LinearStateSkipPrivateSession),
		IssueRepoStale:    true,
		LinkAuditReason:   "linear_null_repo_carveout",
	}, snapshot, "ProviderStateSnapshot should expose the operator-safe state summary")

	provider.getErr = errors.New("provider state unavailable")
	require.False(t, svc.HasLinearProviderState(context.Background(), orgID, linkID), "HasLinearProviderState should return false on store errors")
	require.Equal(t, ProviderStateSnapshot{}, svc.ProviderStateSnapshot(context.Background(), orgID, linkID), "ProviderStateSnapshot should return zero value on store errors")
}
