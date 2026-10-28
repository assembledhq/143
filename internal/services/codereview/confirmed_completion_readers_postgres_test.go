package codereview

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// Each leaf owns a separate fully migrated database, a literal two-connection
// pool, a pre-acquired observer and the actual service's remaining connection.
// The only external dependencies are traps; positive publication ownership
// always comes from actual metadata/assessment storage and the shared decoder.
// This is not provider delivery, RepairMissingWakes, activation or a claim that
// every public admission route is covered.
type confirmedReadersFixture struct {
	input      ReviewRequestedInput
	metadata   models.CodeReviewSessionMetadata
	assessment models.CodeReviewAssessment
	receipt    json.RawMessage
	older      uuid.UUID
}

func confirmedReadersJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err, "encode only complete synthetic publication or expected graph data")
	return raw
}

func confirmedReadersStore(t *testing.T, pool *pgxpool.Pool) *db.CodeReviewStore {
	t.Helper()
	transactions, err := db.NewThreadSendTransactions(pool)
	require.NoError(t, err, "retain the existing bounded transaction capability on the same literal two-slot fixture pool")
	store, err := db.NewCodeReviewStoreWithTerminalTransactions(pool, transactions)
	require.NoError(t, err, "real terminal metadata paths must have their required original transaction capability")
	return store
}

func confirmedReadersRetireFixtureJob(t *testing.T, ctx context.Context, pool *pgxpool.Pool, org, job uuid.UUID) {
	t.Helper()
	tag, err := pool.Exec(ctx, `UPDATE jobs SET status='dead_letter',completed_at=statement_timestamp(),
		last_error='Synthetic controller exhausted before local completion',lock_token=NULL,lease_expires_at=NULL
		WHERE org_id=$1 AND id=$2 AND queue='agent' AND job_type='run_code_review'`, org, job)
	require.NoError(t, err, "retain a genuine terminal original controller job instead of deleting its non-NULL original dedupe identity")
	require.Equal(t, int64(1), tag.RowsAffected(), "only the original actual service-created controller may become terminal for this fixture")
}

func confirmedReadersSeed(t *testing.T, ctx context.Context, pool *pgxpool.Pool, older bool) confirmedReadersFixture {
	t.Helper()
	org, integration, repository, pr := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, row := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO organizations(id,name) VALUES($1,'Synthetic confirmed readers')`, []any{org}},
		{`INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, []any{integration, org}},
		{`INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id)
			VALUES($1,$2,$3,(hashtextextended($1::uuid::text,0)&9223372036854775807),'synthetic/confirmed-readers','https://example.invalid/repository.git',1)`, []any{repository, org, integration}},
		{`INSERT INTO pull_requests(id,org_id,github_pr_number,github_pr_url,github_repo,title)
			VALUES($1,$2,17,'https://example.invalid/pull/17','synthetic/confirmed-readers','Synthetic confirmed readers')`, []any{pr, org}},
	} {
		_, err := pool.Exec(ctx, row.query, row.args...)
		require.NoError(t, err, "create only synthetic tenant/repository/PR parents with every full-schema guard enabled")
	}
	metadata := confirmedReadersStore(t, pool)
	_, err := metadata.SavePolicy(ctx, org, models.DefaultCodeReviewPolicyConfig(), nil)
	require.NoError(t, err, "materialize the real reusable code review policy before actual public service admission")
	input := ReviewRequestedInput{OrgID: org, RepositoryID: repository, PullRequestID: pr, GitHubRepo: "synthetic/confirmed-readers",
		GitHubPRNumber: 17, GitHubPRURL: "https://example.invalid/pull/17", PullRequestTitle: "Synthetic confirmed readers",
		BaseSHA: strings.Repeat("b", 40), HeadSHA: strings.Repeat("a", 40), RequestedLogin: "synthetic-reviewer"}
	service := NewService(metadata, metadata, db.NewSessionStore(pool), db.NewJobStore(pool), zerolog.Nop(), Config{AppReviewerLogins: []string{"synthetic-reviewer"}})
	created, err := service.HandleReviewRequested(ctx, input)
	require.NoError(t, err, "create the actual ordinary review session, metadata, frozen controller payload and original wake through the public service entry point")
	require.True(t, created.Processed && !created.Reused && created.SessionID != uuid.Nil && created.JobID != uuid.Nil, "the fixture must genuinely admit one original review rather than reuse an empty or unrelated row")
	f := confirmedReadersFixture{input: input}
	if older {
		f.older = created.SessionID
		confirmedReadersRetireFixtureJob(t, ctx, pool, org, created.JobID)
		_, err := metadata.FailReviewWithStatus(ctx, org, db.FailCodeReviewParams{SessionID: created.SessionID,
			Reason: "Synthetic earlier retryable attempt", Code: models.CodeReviewStatusCodeWorkerFailed, Message: "Synthetic earlier retryable attempt", Retryable: true})
		require.NoError(t, err, "create a genuine older terminal retryable source through the existing failure store, not a fabricated service answer")
		require.NoError(t, db.NewSessionStore(pool).UpdateStatus(ctx, org, created.SessionID, models.SessionStatusFailed), "retain the actual older parent failure before creating its newer original owner")
		_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET created_at=statement_timestamp()-interval '20 minutes'
			WHERE org_id=$1 AND session_id=$2`, org, created.SessionID)
		require.NoError(t, err, "make older-source ordering explicit through a fixture database clock update, without sleep")
		created, err = service.HandleReviewRequested(ctx, input)
		require.NoError(t, err, "admit the genuinely newer same-head attempt through the real public entry point")
		require.NotEqual(t, f.older, created.SessionID, "the retry conflict must compare a different actual older source, not itself")
	}
	f.metadata, err = metadata.MarkRunning(ctx, org, created.SessionID)
	require.NoError(t, err, "use the actual queued-to-running metadata transition for the retained completion owner")
	assessments := db.NewCodeReviewAssessmentStore(pool)
	f.assessment, _, err = assessments.Create(ctx, models.CodeReviewAssessmentCapture{OrgID: org, RepositoryID: repository, PullRequestID: pr,
		PolicyID: f.metadata.PolicyID, SessionID: created.SessionID, Generation: 1, BaseSHA: input.BaseSHA, BaseRef: "main", HeadSHA: input.HeadSHA,
		InputVersion: 1, CodeDigest: "synthetic-code", ContractDigest: "synthetic-contract", IntentDigest: "synthetic-intent",
		VisualDigest: "synthetic-visual", RequestDigest: "synthetic-request", GateDigest: "synthetic-gates", InputDigest: "synthetic-original-input",
		InputManifest: json.RawMessage(`{"synthetic":"confirmed reader inputs"}`), ReviewScope: models.CodeReviewScopeFull,
		RouteReason: models.CodeReviewRouteInitialFull, PublicationKey: f.metadata.ReviewOutputKey})
	require.NoError(t, err, "create the exact original full assessment through its real composite-scope and immutable-input store")
	require.NoError(t, assessments.MarkRunning(ctx, org, f.assessment.ID, f.assessment.Generation, f.assessment.InputDigest), "start the actual captured full assessment through its original generation/digest CAS")
	completion := models.CodeReviewAssessmentCompletion{ResultOrigin: models.CodeReviewResultExecuted, CoverageComplete: true,
		Decision: models.CodeReviewDecisionNeedsHumanReview, Acceptable: false, RiskReasonDetails: json.RawMessage(`[]`),
		StructuredOutcome: json.RawMessage(`{"synthetic":"retained confirmed reader outcome"}`), RenderedBody: "Synthetic retained confirmed reader result"}
	require.NoError(t, assessments.StageOutcome(ctx, org, f.assessment.ID, f.assessment.Generation, f.assessment.InputDigest, completion), "retain actual staged outcome fields before any publication transition")
	require.NoError(t, assessments.ReservePublication(ctx, org, f.assessment.ID, f.assessment.Generation, f.assessment.InputDigest, input.HeadSHA), "reserve the actual original head without disabling any receipt/state guard")
	require.NoError(t, assessments.MarkPublicationAttemptUncertain(ctx, org, f.assessment.ID, f.assessment.Generation, f.assessment.InputDigest), "record the genuine existing uncertainty transition before confirmed observation")
	f.receipt = confirmedReadersJSON(t, map[string]any{"assessment_id": f.assessment.ID.String(), "input_digest": f.assessment.InputDigest,
		"submitted_commit_sha": input.HeadSHA, "github_review_id": int64(77)})
	review, reviewURL := int64(77), "https://example.invalid/review/77"
	require.NoError(t, assessments.RecordPublication(ctx, org, f.assessment.ID, f.assessment.Generation, f.assessment.InputDigest,
		models.CodeReviewPublicationConfirmed, f.receipt, &review, &reviewURL), "persist the canonical confirmed receipt through actual RecordPublication, never direct assessment-only state seeding or fake positive proof")
	confirmedReadersRetireFixtureJob(t, ctx, pool, org, created.JobID)
	request := uuid.New()
	for _, row := range []struct {
		query string
		args  []any
	}{
		{`UPDATE code_review_session_metadata SET created_at=statement_timestamp()-interval '10 minutes'
			WHERE org_id=$1 AND id=$2`, []any{org, f.metadata.ID}},
		{`INSERT INTO code_review_requests(id,org_id,repository_id,pull_request_id,source_kind,source_identity,mode,input_hash,target_generation,session_id,assessment_id,status)
			VALUES($1,$2,$3,$4,'synthetic_confirmed_reader','original','review_now','synthetic-request',1,$5,$6,'joined')`, []any{request, org, repository, pr, created.SessionID, f.assessment.ID}},
		{`INSERT INTO code_review_pr_state(org_id,repository_id,pull_request_id,generation,head_sha,base_sha,base_ref,active_session_id,active_assessment_id,pending_request_id,pending_input,state,wait_reason)
			VALUES($1,$2,$3,1,$4,$5,'main',$6,$7,$8,'{"synthetic":"retained pending intention"}','running','active_review')`, []any{org, repository, pr, input.HeadSHA, input.BaseSHA, created.SessionID, f.assessment.ID, request}},
		{`INSERT INTO code_review_decision_disputes(org_id,session_id,pull_request_id,repository_id,policy_id,reviewed_head_sha,decision,source,source_body_hash,body,semantic_input_hash_at_filing)
			VALUES($1,$2,$3,$4,$5,$6,'needs_human_review','api','synthetic-body','Synthetic retained objection','synthetic-input')`, []any{org, created.SessionID, pr, repository, f.metadata.PolicyID, input.HeadSHA}},
	} {
		tag, err := pool.Exec(ctx, row.query, row.args...)
		require.NoError(t, err, "retain real expired grace, original reservation/request and nonempty dispute canaries without bypassing assessment receipt guards")
		require.Equal(t, int64(1), tag.RowsAffected(), "each complete-graph canary must actually exist for this exact original tenant/owner")
	}
	f.metadata, err = metadata.GetByOutputKey(ctx, org, f.metadata.ReviewOutputKey)
	require.NoError(t, err, "reload the actual aged original metadata instead of retaining its pre-clock fixture copy")
	f.assessment, err = assessments.GetByID(ctx, org, f.assessment.ID)
	require.NoError(t, err, "reload the actual staged publication receipt and immutable original identity")
	active, err := db.NewJobStore(pool).HasActiveByDedupeKey(ctx, org, "agent", "code_review:"+f.metadata.ReviewOutputKey)
	require.NoError(t, err, "independently query actual queue liveness before the service reader proof")
	require.False(t, active, "the confirmed guard must be necessary: the original key must have no pending/running wake shortcut")
	var expired bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT created_at<statement_timestamp()-interval '1 minute' FROM code_review_session_metadata WHERE org_id=$1 AND id=$2`, org, f.metadata.ID).Scan(&expired), "read grace expiration from the real database clock, not a timing sleep or zero timestamp shortcut")
	require.True(t, expired, "the ordinary one-minute enqueue grace cannot explain the intended positive preservation")
	pending, err := metadata.HasConfirmedFullAssessmentCompletionPending(ctx, org, f.metadata.ID)
	require.NoError(t, err, "independently use the real store/shared decoder to verify this fixture's exact original publication truth")
	require.True(t, pending, "the initial real persisted exact original receipt must establish confirmed completion ownership")
	return f
}

func confirmedReadersGraph(ctx context.Context, q db.DBTX, org uuid.UUID) (json.RawMessage, error) {
	admission, err := scheduledCreateGraph(ctx, q, org)
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	err = q.QueryRow(ctx, `SELECT jsonb_build_object('admission',$2::jsonb,
		'assessments',COALESCE((SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM code_review_revision_assessments a WHERE a.org_id=$1),'[]'::jsonb),
		'disputes',COALESCE((SELECT jsonb_agg(to_jsonb(d) ORDER BY d.id) FROM code_review_decision_disputes d WHERE d.org_id=$1),'[]'::jsonb))`, org, admission).Scan(&raw)
	return raw, err
}

type confirmedReadersMetadata struct {
	*db.CodeReviewStore
	observed []bool
}

func (s *confirmedReadersMetadata) HasConfirmedFullAssessmentCompletionPending(ctx context.Context, org, metadata uuid.UUID) (bool, error) {
	pending, err := s.CodeReviewStore.HasConfirmedFullAssessmentCompletionPending(ctx, org, metadata)
	if err != nil {
		return false, err
	}
	s.observed = append(s.observed, pending)
	return pending, nil
}

type confirmedReadersProviderTrap struct{ prepared, reads, syncs int }

func (s *confirmedReadersProviderTrap) PrepareCodeReviewPullRequestSnapshot(context.Context, uuid.UUID, uuid.UUID) (ghservice.CodeReviewPullRequestSnapshotReader, error) {
	s.prepared++
	return func(context.Context, int) (ghservice.CodeReviewPullRequestSnapshot, error) {
		s.reads++
		return ghservice.CodeReviewPullRequestSnapshot{}, errors.New("synthetic provider reader must remain uncalled")
	}, nil
}

func (s *confirmedReadersProviderTrap) SyncPullRequestState(context.Context, uuid.UUID, uuid.UUID) error {
	s.syncs++
	return errors.New("synthetic provider sync must remain uncalled")
}

type confirmedReadersScheduleOwner struct {
	*pgxpool.Pool
	begins, commits, rollbacks int
}

func (s *confirmedReadersScheduleOwner) Begin(ctx context.Context) (pgx.Tx, error) {
	s.begins++
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &confirmedReadersScheduleTx{Tx: tx, owner: s}, nil
}

type confirmedReadersScheduleTx struct {
	pgx.Tx
	owner *confirmedReadersScheduleOwner
}

func (tx *confirmedReadersScheduleTx) Commit(ctx context.Context) error {
	tx.owner.commits++
	return tx.Tx.Commit(ctx)
}

func (tx *confirmedReadersScheduleTx) Rollback(ctx context.Context) error {
	tx.owner.rollbacks++
	return tx.Tx.Rollback(ctx)
}

func TestConfirmedCompletionReadersPreserveOriginalPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, route string
		scheduling  bool
	}{
		{name: "same-head public review request reuses aged confirmed owner without live wake", route: "request"},
		{name: "retry older source preserves newer confirmed owner rather than false-failing it", route: "retry"},
		{name: "captured-identity queue preserves confirmed original source", route: "capture"},
		{name: "scheduling-enabled captured-identity fast path has no wrapper state write", route: "capture", scheduling: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := newScheduledSessionPostgresPool(t)
			require.Equal(t, int32(2), pool.Config().MaxConns, "each actual public-reader proof must use the existing literal two-slot full-schema fixture")
			f := confirmedReadersSeed(t, ctx, pool, tt.route == "retry")
			foreign := confirmedReadersSeed(t, ctx, pool, false)
			observer, err := pool.Acquire(ctx)
			require.NoError(t, err, "pre-acquire the independent observer so actual service admission and any existing PR transaction have only the other pool slot")
			defer observer.Release()
			before, err := confirmedReadersGraph(ctx, observer, f.input.OrgID)
			require.NoError(t, err, "retain the whole nonempty original session/metadata/assessment/dispute/request/reservation/job graph before service entry")
			foreignBefore, err := confirmedReadersGraph(ctx, observer, foreign.input.OrgID)
			require.NoError(t, err, "retain a distinct foreign tenant's actual full confirmed original, reservation, dispute and queue history")
			store := confirmedReadersStore(t, pool)
			metadata := &confirmedReadersMetadata{CodeReviewStore: store}
			provider := &confirmedReadersProviderTrap{}
			service := NewService(store, metadata, db.NewSessionStore(pool), db.NewJobStore(pool), zerolog.Nop(), Config{AppReviewerLogins: []string{"synthetic-reviewer"}})
			schedule := &confirmedReadersScheduleOwner{Pool: pool}
			if tt.scheduling {
				service.SetScheduling(db.NewCodeReviewScheduleStore(schedule), provider)
			}
			input := ReviewChangedInput{OrgID: f.input.OrgID, RepositoryID: f.input.RepositoryID, PullRequestID: f.input.PullRequestID,
				PriorSessionID: f.metadata.SessionID, PriorMetadataID: &f.metadata.ID, GitHubRepo: f.input.GitHubRepo,
				GitHubPRNumber: f.input.GitHubPRNumber, GitHubPRURL: f.input.GitHubPRURL, PullRequestTitle: f.input.PullRequestTitle,
				BaseSHA: f.input.BaseSHA, HeadSHA: f.input.HeadSHA, TriggerSource: f.metadata.TriggerSource, ChangeReason: captureIdentityChangeReason}
			var result ReviewRequestedResult
			if tt.route == "request" {
				result, err = service.HandleReviewRequested(ctx, f.input)
			} else if tt.route == "retry" {
				service.SetRetryDependencies(db.NewPullRequestStore(pool), provider)
				var retryResult RetryReviewResult
				retryResult, err = service.RetryReview(ctx, RetryReviewInput{OrgID: f.input.OrgID, SessionID: f.older})
				var conflict *RetryReviewConflictError
				require.ErrorAs(t, err, &conflict, "retrying the actual older source must conflict with the real newer confirmed completion owner")
				require.Equal(t, &RetryReviewConflictError{Code: RetryReviewConflictNewerAttempt, Message: "A newer code review attempt already exists."}, conflict, "the conflict must identify newer ownership, not manufacture completion or retryability failure")
				require.Equal(t, RetryReviewResult{}, retryResult, "a newer retained completion owner must release no replacement session/job identity")
			} else {
				result, err = service.QueueCapturedIdentityReplacement(ctx, input)
			}
			if tt.route != "retry" {
				require.NoError(t, err, "actual service entry must preserve the exact retained completion owner without replacement/provider work")
				want := ReviewRequestedResult{Processed: true, Reused: true, SessionID: f.metadata.SessionID, MetadataID: f.metadata.ID, TriggerSource: f.metadata.TriggerSource}
				if tt.route != "request" {
					want.IgnoredReason = "source_confirmed_completion_pending"
				}
				require.Equal(t, want, result, "preservation must name only the exact original owner, carry the distinct capture reason, and never claim dispatch confirmation or replacement handoff")
			}
			after, err := confirmedReadersGraph(ctx, observer, f.input.OrgID)
			require.NoError(t, err, "read every real original graph row independently after the actual public entry point settles")
			foreignAfter, err := confirmedReadersGraph(ctx, observer, foreign.input.OrgID)
			require.NoError(t, err, "read every real foreign row independently after service entry")
			require.Equal(t, before, after, "confirmed-owner readers must not fail/replace sessions or metadata, alter original A/dispute/outcome/reservation, enqueue a wake/request, link an older retry or commit a no-op schedule timestamp")
			require.Equal(t, foreignBefore, foreignAfter, "no original completion observation or service route may borrow or mutate a foreign owner, receipt, reservation, dispute or job")
			require.Equal(t, []bool{true}, metadata.observed, "the public route must actually reach the real shared confirmed-owner guard exactly once after grace and queue shortcuts are excluded")
			require.Equal(t, [3]int{}, [3]int{schedule.begins, schedule.commits, schedule.rollbacks}, "positive captured-source fast paths must not enter or write through the scheduling wrapper")
			require.Zero(t, provider.prepared, "positive preservation must not even prepare external provider work")
			require.Equal(t, [2]int{}, [2]int{provider.reads, provider.syncs}, "all positive ownership decisions must precede external snapshot hydration or retry synchronization")
			pending, err := store.HasConfirmedFullAssessmentCompletionPending(ctx, f.input.OrgID, f.metadata.ID)
			require.NoError(t, err, "independently recheck actual exact original publication truth after the service-owned transaction has settled")
			require.True(t, pending, "all preserved original owners must retain the same real confirmed staged receipt after service entry")
		})
	}
}
