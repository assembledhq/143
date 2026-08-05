package codereview

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type captureDisputeStore struct {
	created          models.CodeReviewDispute
	current          models.CodeReviewDispute
	triage           models.CodeReviewDisputeTriageResult
	authorizations   []models.CodeReviewDisputeAuthorization
	admitted         bool
	admittedCooldown time.Duration
	admittedHash     string
	admittedPayload  ReviewChangedInput
	guards           []db.CodeReviewDisputeIntakeGuard
	createErr        error
	failed           bool
	failDetail       string
	failReply        bool
}

func (s *captureDisputeStore) CreateAndEnqueueTriage(_ context.Context, dispute *models.CodeReviewDispute, guard db.CodeReviewDisputeIntakeGuard) (bool, error) {
	s.guards = append(s.guards, guard)
	if s.createErr != nil {
		return false, s.createErr
	}
	dispute.ID = uuid.New()
	s.created = *dispute
	return true, nil
}
func (s *captureDisputeStore) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.CodeReviewDispute, error) {
	return s.current, nil
}
func (s *captureDisputeStore) ListBySession(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID, int) (models.CodeReviewDisputePage, error) {
	return models.CodeReviewDisputePage{}, nil
}
func (s *captureDisputeStore) ListQueue(context.Context, uuid.UUID, models.CodeReviewDisputeListFilters) (models.CodeReviewDisputePage, error) {
	return models.CodeReviewDisputePage{}, nil
}
func (s *captureDisputeStore) ListRecentKinds(context.Context, uuid.UUID, int) ([]string, error) {
	return nil, nil
}
func (s *captureDisputeStore) SetTriage(_ context.Context, _ uuid.UUID, _ uuid.UUID, result models.CodeReviewDisputeTriageResult, adjudicationEligible bool, detail string) (models.CodeReviewDispute, error) {
	s.triage = result
	s.current.Direction = &result.Direction
	s.current.Routing = &result.Routing
	s.current.IntakeStatus = models.CodeReviewDisputeIntakeTriaged
	if result.Routing == models.CodeReviewDisputeRoutingNotADispute || result.Routing == models.CodeReviewDisputeRoutingReviewRequest {
		s.current.IntakeStatus = models.CodeReviewDisputeIntakeDiscarded
	}
	s.current.StatusDetail = &detail
	if adjudicationEligible {
		status := models.CodeReviewDisputeAdjudicationPending
		s.current.AdjudicationStatus = &status
	}
	return s.current, nil
}
func (s *captureDisputeStore) FailTriage(_ context.Context, _ uuid.UUID, _ uuid.UUID, detail string, replyApplicable bool) error {
	s.failed = true
	s.failDetail = detail
	s.failReply = replyApplicable
	return nil
}

func (s *captureDisputeStore) RecordAuthorization(_ context.Context, authorization models.CodeReviewDisputeAuthorization) error {
	s.authorizations = append(s.authorizations, authorization)
	return nil
}
func (s *captureDisputeStore) AdmitAndEnqueueReassessment(_ context.Context, _ models.CodeReviewDispute, _ *uuid.UUID, semanticHash string, cooldown time.Duration, _ int, payload any) (bool, error) {
	s.admitted = true
	s.admittedCooldown = cooldown
	s.admittedHash = semanticHash
	s.admittedPayload, _ = payload.(ReviewChangedInput)
	return false, nil
}
func (s *captureDisputeStore) CompleteReassessment(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, models.CodeReviewSessionStatus, *models.CodeReviewDecision, string) error {
	return nil
}
func (s *captureDisputeStore) MarkReassessmentFailed(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}
func (s *captureDisputeStore) Escalate(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string) (models.CodeReviewDispute, error) {
	return models.CodeReviewDispute{}, nil
}
func (s *captureDisputeStore) Adjudicate(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, models.CodeReviewDisputeAdjudicationUpdate) (models.CodeReviewDispute, error) {
	return models.CodeReviewDispute{}, nil
}

type disputeReviewStoreStub struct {
	metadata models.CodeReviewSessionMetadata
	item     models.CodeReviewListItem
	reasons  []models.CodeReviewRiskReasonCode
	details  []models.CodeReviewRiskReason
	policy   models.CodeReviewPolicyRecord
}

func (s disputeReviewStoreStub) GetBySessionID(context.Context, uuid.UUID, uuid.UUID) (models.CodeReviewSessionMetadata, error) {
	return s.metadata, nil
}
func (s disputeReviewStoreStub) GetLatestCompletedByPullRequest(context.Context, uuid.UUID, uuid.UUID) (models.CodeReviewSessionMetadata, error) {
	return s.metadata, nil
}
func (s disputeReviewStoreStub) GetByGitHubFindingComment(context.Context, uuid.UUID, int64) (models.CodeReviewSessionMetadata, error) {
	return s.metadata, nil
}
func (s disputeReviewStoreStub) GetListItemBySessionID(context.Context, uuid.UUID, uuid.UUID) (models.CodeReviewListItem, error) {
	return s.item, nil
}
func (s disputeReviewStoreStub) GetRiskReasonCodesBySession(context.Context, uuid.UUID, uuid.UUID) ([]models.CodeReviewRiskReasonCode, error) {
	return s.reasons, nil
}
func (s disputeReviewStoreStub) ListFindings(context.Context, uuid.UUID, uuid.UUID, bool) ([]models.CodeReviewFinding, error) {
	return nil, nil
}
func (s disputeReviewStoreStub) GetPolicyByID(context.Context, uuid.UUID, uuid.UUID) (models.CodeReviewPolicyRecord, error) {
	return s.policy, nil
}
func (s disputeReviewStoreStub) GetRiskReasonsBySession(context.Context, uuid.UUID, uuid.UUID) ([]models.CodeReviewRiskReason, error) {
	return s.details, nil
}

type disputePullRequestStoreStub struct {
	pullRequest models.PullRequest
}

type disputePullRequestSnapshotterStub struct {
	snapshot ghservice.CodeReviewPullRequestSnapshot
	err      error
}

func (s disputePullRequestSnapshotterStub) GetCodeReviewPullRequestSnapshot(context.Context, uuid.UUID, uuid.UUID, int) (ghservice.CodeReviewPullRequestSnapshot, error) {
	return s.snapshot, s.err
}

func (s disputePullRequestStoreStub) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.PullRequest, error) {
	return s.pullRequest, nil
}
func (disputePullRequestStoreStub) GetHealthCurrent(context.Context, uuid.UUID, uuid.UUID) (models.PullRequestHealthCurrent, error) {
	return models.PullRequestHealthCurrent{}, nil
}

type disputeJobStoreStub struct{ enqueued []db.EnqueueOpts }

func (s *disputeJobStoreStub) EnqueueWithOpts(_ context.Context, _ uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
	s.enqueued = append(s.enqueued, opts)
	return uuid.New(), nil
}

type disputeLLMStub struct {
	response   string
	userPrompt string
}

type disputeReviewRequestQueuerStub struct {
	inputs []ReviewChangedInput
	result ReviewRequestedResult
	err    error
}

func (s *disputeReviewRequestQueuerStub) QueueReviewChanged(_ context.Context, input ReviewChangedInput) (ReviewRequestedResult, error) {
	s.inputs = append(s.inputs, input)
	result := s.result
	if result == (ReviewRequestedResult{}) && s.err == nil {
		result.Processed = true
	}
	return result, s.err
}

func (s *disputeLLMStub) Complete(_ context.Context, _ string, userPrompt string) (string, error) {
	s.userPrompt = userPrompt
	return s.response, nil
}

func TestDisputeService_FileInAppCapturesImmutableSemanticInput(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	pullRequestID := uuid.New()
	repositoryID := uuid.New()
	policyID := uuid.New()
	decision := models.CodeReviewDecisionBlocked
	pullRequestBody := "Existing test and rollout details"
	store := &captureDisputeStore{}
	reviews := disputeReviewStoreStub{
		metadata: models.CodeReviewSessionMetadata{
			OrgID: orgID, SessionID: sessionID, PullRequestID: pullRequestID,
			RepositoryID: repositoryID, PolicyID: policyID, HeadSHA: "abc123",
			Status: models.CodeReviewSessionStatusCompleted, Decision: &decision,
		},
		item: models.CodeReviewListItem{
			PullRequestAuthor: "octocat", PullRequestTitle: "Fix payment authorization",
			GitHubRepo: "acme/payments", GitHubPRNumber: 42, GitHubPRURL: "https://github.com/acme/payments/pull/42",
		},
		reasons: []models.CodeReviewRiskReasonCode{models.CodeReviewRiskReasonBlockingFindings},
	}
	pullRequests := disputePullRequestStoreStub{pullRequest: models.PullRequest{
		ID: pullRequestID, OrgID: orgID, Title: "Fix payment authorization", Body: &pullRequestBody,
	}}
	service := NewDisputeService(store, reviews, pullRequests, &disputeJobStoreStub{}, nil, "", zerolog.Nop())

	dispute, err := service.FileInApp(context.Background(), FileCodeReviewDisputeInput{
		OrgID: orgID, SessionID: sessionID, FiledByLogin: "octocat", AuthorAssociation: "MEMBER",
		RepositoryVisibility: "private", Body: "The blocking finding was already addressed.",
		ContestedReasonCodes: []models.CodeReviewRiskReasonCode{models.CodeReviewRiskReasonBlockingFindings},
	})

	require.NoError(t, err, "filing a dispute against a completed review should succeed")
	require.Equal(t, store.created.ID, dispute.ID, "the returned dispute should reference the immutable row sent to storage")
	require.True(t, dispute.Trusted, "private-repository member evidence should be trusted at filing")
	require.True(t, dispute.AuthorIsPRAuthor, "the filing snapshot should identify the pull request author")
	require.Equal(t,
		codeReviewDisputeSemanticHash("abc123", policyID, dispute.Body, "Fix payment authorization", pullRequestBody),
		dispute.SemanticInputHashAtFiling,
		"the filing fingerprint should include the exact PR title and body context",
	)
	require.Equal(t, []models.CodeReviewRiskReasonCode{models.CodeReviewRiskReasonBlockingFindings}, dispute.ContestedReasonCodes, "only available risk reasons should be retained")
	var signals map[string]any
	require.NoError(t, json.Unmarshal(dispute.QueueSignals, &signals), "queue context should be valid JSON")
	require.Equal(t, "Fix payment authorization", signals["pull_request_title"], "queue context should preserve the reviewed pull request title")
	require.Equal(t, "https://github.com/acme/payments/pull/42", signals["github_pr_url"], "queue context should link policy owners to the pull request")
}

func TestDisputeService_FileFromGitHubAppliesUntrustedIntakeGuard(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	pullRequestID := uuid.New()
	decision := models.CodeReviewDecisionBlocked
	reviews := disputeReviewStoreStub{
		metadata: models.CodeReviewSessionMetadata{
			OrgID: orgID, SessionID: sessionID, PullRequestID: pullRequestID,
			RepositoryID: uuid.New(), PolicyID: uuid.New(), HeadSHA: "abc123",
			Status: models.CodeReviewSessionStatusCompleted, Decision: &decision,
		},
		item: models.CodeReviewListItem{PullRequestAuthor: "octocat"},
	}
	config := DisputeConfig{ReassessmentsEnabled: true, IntakePerUntrustedLogin: 5, IntakePerPullRequest: 20}
	tests := []struct {
		name          string
		association   string
		createErr     error
		expectedGuard db.CodeReviewDisputeIntakeGuard
		expectCapture bool
	}{
		{
			name: "untrusted filing is admitted under a guard", association: "NONE",
			expectedGuard: db.CodeReviewDisputeIntakeGuard{
				Window: codeReviewDisputeIntakeWindow, PerLoginMax: 5, PerPullRequestMax: 20,
			},
			expectCapture: true,
		},
		{
			name: "capped untrusted filing cannot fall through to direct review", association: "NONE",
			createErr: db.ErrCodeReviewDisputeIntakeCapped,
			expectedGuard: db.CodeReviewDisputeIntakeGuard{
				Window: codeReviewDisputeIntakeWindow, PerLoginMax: 5, PerPullRequestMax: 20,
			},
		},
		{
			// Trust removes the per-login ceiling but not the per-pull-request
			// one: every edit of one comment files a fresh dispute, so trusted
			// GitHub traffic still has to be bounded somewhere.
			name: "trusted filing keeps the per-pull-request ceiling", association: "MEMBER",
			expectedGuard: db.CodeReviewDisputeIntakeGuard{
				Window: codeReviewDisputeIntakeWindow, PerPullRequestMax: 20,
			},
			expectCapture: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := &captureDisputeStore{createErr: tt.createErr}
			service := NewDisputeService(store, reviews, disputePullRequestStoreStub{}, &disputeJobStoreStub{}, nil, "", zerolog.Nop(), config)

			dispute, captured, err := service.FileFromGitHub(context.Background(), FileGitHubCodeReviewDisputeInput{
				OrgID: orgID, PullRequestID: pullRequestID, AuthorLogin: "drive-by",
				AuthorType: models.PRFeedbackAuthorTypeUser, AuthorAssociation: tt.association,
				Body: "This should never have been blocked.", GitHubCommentID: 99, SourceVersion: 1,
			})

			if tt.createErr != nil {
				require.ErrorIs(t, err, db.ErrCodeReviewDisputeIntakeCapped, "a capped intake should stop direct review fallback without failing the webhook delivery")
			} else {
				require.NoError(t, err, "admitted intake should not return an error")
			}
			require.Equal(t, tt.expectCapture, captured, "only a stored dispute may suppress ordinary PR feedback handling")
			require.Equal(t, []db.CodeReviewDisputeIntakeGuard{tt.expectedGuard}, store.guards,
				"the ceiling must be evaluated in the same transaction as the insert, so it travels with the create call")
			if tt.expectCapture {
				require.NotEqual(t, uuid.Nil, dispute.ID, "a captured dispute should carry its stored identity")
			} else {
				require.Equal(t, uuid.Nil, dispute.ID, "a declined dispute must not look stored to the caller")
			}
		})
	}
}

func TestDisputeService_TriageRecordsTrustNotEligibilityInAuthorizationSnapshot(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	disputeID := uuid.New()
	store := &captureDisputeStore{current: models.CodeReviewDispute{
		ID: disputeID, OrgID: orgID, SessionID: uuid.New(),
		Source: models.CodeReviewDisputeSourceGitHubComment, Decision: models.CodeReviewDecisionBlocked,
		Body: "Why did this get blocked?", AuthorAssociation: "MEMBER",
		RepositoryVisibility: models.CodeReviewRepositoryVisibilityPublic,
		IntakeStatus:         models.CodeReviewDisputeIntakePending,
		ReassessmentStatus:   models.CodeReviewDisputeReassessmentNotRequested,
	}}
	reviews := disputeReviewStoreStub{item: models.CodeReviewListItem{PullRequestAuthor: "octocat"}}
	client := &disputeLLMStub{response: `{"direction":"should_have_approved","contested_reason_codes":[],"dispute_kind":"explanation_question","asserts_new_information":false,"routing":"answer_only","confidence":0.98,"reply":"The review evidence explains the block."}`}
	service := NewDisputeService(store, reviews, disputePullRequestStoreStub{}, &disputeJobStoreStub{}, client, "", zerolog.Nop())

	err := service.Triage(context.Background(), orgID, disputeID)

	require.NoError(t, err, "an explanation question should still be triaged")
	require.Equal(t, models.CodeReviewDisputeRoutingAnswerOnly, store.triage.Routing, "a question without disagreement should route to answer_only")
	require.Len(t, store.authorizations, 1, "queue influence should record exactly one authorization snapshot")
	require.True(t, store.authorizations[0].Trusted, "the snapshot must record the trust decision, not adjudication eligibility")
	require.Equal(t, "trusted GitHub association", store.authorizations[0].DecisionReason, "the recorded reason must agree with the recorded trust")
}

func TestDisputeService_TriageDoesNotLetTrustedNonAuthorStartReassessment(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	disputeID := uuid.New()
	sessionID := uuid.New()
	store := &captureDisputeStore{current: models.CodeReviewDispute{
		ID: disputeID, OrgID: orgID, SessionID: sessionID,
		Source: models.CodeReviewDisputeSourceAppUI, Decision: models.CodeReviewDecisionBlocked,
		Body: "The review missed new evidence.", AuthorAssociation: "MEMBER",
		RepositoryVisibility: models.CodeReviewRepositoryVisibilityPublic,
		AuthorIsPRAuthor:     false, IntakeStatus: models.CodeReviewDisputeIntakePending,
		ReassessmentStatus: models.CodeReviewDisputeReassessmentNotRequested,
	}}
	reviews := disputeReviewStoreStub{
		item:    models.CodeReviewListItem{PullRequestAuthor: "octocat"},
		reasons: []models.CodeReviewRiskReasonCode{models.CodeReviewRiskReasonBlockingFindings},
	}
	service := NewDisputeService(store, reviews, disputePullRequestStoreStub{}, &disputeJobStoreStub{}, nil, "", zerolog.Nop())

	err := service.Triage(context.Background(), orgID, disputeID)

	require.NoError(t, err, "trusted maintainer feedback should still be recorded and routed")
	require.Equal(t, models.CodeReviewDisputeRoutingPolicySignalOnly, store.triage.Routing, "only the pull request author may start a Phase 1A reassessment")
	require.Equal(t, "Only the pull request author can trigger an automatic re-review right now. The objection was recorded for a policy owner.", store.triage.Reply, "the filer should receive a clear authorization explanation")
	require.False(t, store.admitted, "a trusted non-author must not reach reassessment admission")
	actions := make([]models.CodeReviewDisputeAuthorizationAction, 0, len(store.authorizations))
	for _, authorization := range store.authorizations {
		actions = append(actions, authorization.Action)
	}
	require.Equal(t, []models.CodeReviewDisputeAuthorizationAction{models.CodeReviewDisputeAuthorizationQueueInfluence}, actions, "queue influence should have one exact durable authorization snapshot")
}

// A triage job is enqueued at insert, so it still runs after a later edit of
// the same GitHub comment replaced the objection. The reply belongs to the live
// dispute and a reassessment would spend a whole agent run on a body nobody
// will be shown a verdict for.
func TestDisputeService_TriageSkipsWorkForASupersededDispute(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	disputeID := uuid.New()
	supersededBy := uuid.New()
	store := &captureDisputeStore{current: models.CodeReviewDispute{
		ID: disputeID, OrgID: orgID, SessionID: uuid.New(),
		Source: models.CodeReviewDisputeSourceAppUI, Decision: models.CodeReviewDecisionBlocked,
		Body: "The review missed new evidence.", AuthorAssociation: "MEMBER",
		RepositoryVisibility: models.CodeReviewRepositoryVisibilityPrivate,
		AuthorIsPRAuthor:     true, IntakeStatus: models.CodeReviewDisputeIntakePending,
		ReassessmentStatus:    models.CodeReviewDisputeReassessmentNotRequested,
		SupersededByDisputeID: &supersededBy,
	}}
	reviews := disputeReviewStoreStub{
		item:    models.CodeReviewListItem{PullRequestAuthor: "octocat"},
		reasons: []models.CodeReviewRiskReasonCode{models.CodeReviewRiskReasonBlockingFindings},
	}
	jobs := &disputeJobStoreStub{}
	service := NewDisputeService(store, reviews, disputePullRequestStoreStub{}, jobs, nil, "", zerolog.Nop())

	err := service.Triage(context.Background(), orgID, disputeID)

	require.NoError(t, err, "a superseded dispute should still triage cleanly")
	require.Equal(t, models.CodeReviewDisputeRoutingReassess, store.triage.Routing,
		"the classification is still recorded; only the follow-on work is skipped")
	require.False(t, store.admitted, "a superseded objection must not spend an agent reassessment run")
	require.Empty(t, jobs.enqueued, "a superseded objection must not enqueue a reply the live dispute owns")
	actions := make([]models.CodeReviewDisputeAuthorizationAction, 0, len(store.authorizations))
	for _, authorization := range store.authorizations {
		actions = append(actions, authorization.Action)
	}
	require.Equal(t, []models.CodeReviewDisputeAuthorizationAction{models.CodeReviewDisputeAuthorizationQueueInfluence}, actions,
		"the authorization snapshot is the audit trail of what we decided about this filer and is still recorded")
}

func TestDisputeService_QueueReassessmentUsesLatestGitHubSnapshotAndCapturedPolicyCooldown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		cooldownSeconds int
		expected        time.Duration
	}{
		{name: "uses default for legacy policy", expected: 15 * time.Minute},
		{name: "uses versioned policy value", cooldownSeconds: 30 * 60, expected: 30 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := uuid.New()
			policyID := uuid.New()
			pullRequestID := uuid.New()
			reviewedHeadSHA := "reviewed-head"
			staleMirrorHeadSHA := "stale-mirror-head"
			latestHeadSHA := "latest-github-head"
			store := &captureDisputeStore{}
			reviews := disputeReviewStoreStub{policy: models.CodeReviewPolicyRecord{
				ID: policyID,
				RiskPolicy: models.CodeReviewRiskPolicy{
					SemanticDedupeCooldownSeconds: tt.cooldownSeconds,
				},
			}}
			pullRequests := disputePullRequestStoreStub{pullRequest: models.PullRequest{
				ID: pullRequestID, OrgID: orgID, GitHubRepo: "acme/payments", GitHubPRNumber: 42,
				Title: "Stale title", HeadSHA: &staleMirrorHeadSHA,
			}}
			service := NewDisputeService(store, reviews, pullRequests, &disputeJobStoreStub{}, nil, "", zerolog.Nop())
			service.SetPullRequestSnapshotter(disputePullRequestSnapshotterStub{snapshot: ghservice.CodeReviewPullRequestSnapshot{
				Number: 42, HTMLURL: "https://github.com/acme/payments/pull/42",
				Title: "Latest authorization fix", Body: "Latest pull request body", AuthorLogin: "octocat",
				HeadSHA: latestHeadSHA, BaseSHA: "latest-base", FromFork: true,
			}})

			err := service.queueReassessment(context.Background(), models.CodeReviewDispute{
				ID: uuid.New(), OrgID: orgID, PullRequestID: pullRequestID, PolicyID: policyID,
				RepositoryID: uuid.New(), ReviewedHeadSHA: reviewedHeadSHA,
				Body: "The check missed the authorization guard.",
			})

			require.NoError(t, err, "queueReassessment should admit a dispute against GitHub's latest revision")
			require.True(t, store.admitted, "queueReassessment should reach admission")
			require.Equal(t, tt.expected, store.admittedCooldown, "admission should use the captured policy cooldown")
			require.Equal(t, codeReviewDisputeSemanticHash(
				latestHeadSHA, policyID, "The check missed the authorization guard.",
				"Latest authorization fix", "Latest pull request body",
			), store.admittedHash, "admission identity should use the authoritative GitHub revision")
			require.Equal(t, latestHeadSHA, store.admittedPayload.HeadSHA, "reassessment payload should target GitHub's latest head")
			require.Equal(t, "Latest authorization fix", store.admittedPayload.PullRequestTitle, "reassessment payload should use GitHub's latest title")
			require.Equal(t, "octocat", store.admittedPayload.PullRequestAuthor, "reassessment payload should use GitHub's current author")
			require.True(t, store.admittedPayload.FromFork, "reassessment payload should use GitHub's current fork state")
		})
	}
}

func TestDisputeService_TriageResultDeterministic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		dispute  models.CodeReviewDispute
		expected models.CodeReviewDisputeTriageResult
	}{
		{
			name: "explicit non-approval requests reassessment",
			dispute: models.CodeReviewDispute{
				Source: models.CodeReviewDisputeSourceAppUI, Decision: models.CodeReviewDecisionBlocked,
				ContestedReasonCodes: []models.CodeReviewRiskReasonCode{models.CodeReviewRiskReasonBlockingFindings},
			},
			expected: models.CodeReviewDisputeTriageResult{
				Direction:            models.CodeReviewDisputeDirectionShouldHaveApproved,
				ContestedReasonCodes: []models.CodeReviewRiskReasonCode{models.CodeReviewRiskReasonBlockingFindings},
				DisputeKind:          "explicit_reconsideration", AssertsNewInformation: true,
				Routing: models.CodeReviewDisputeRoutingReassess, Confidence: 1,
			},
		},
		{
			name: "explicit unsafe approval becomes policy signal",
			dispute: models.CodeReviewDispute{
				Source: models.CodeReviewDisputeSourceAppUI, Decision: models.CodeReviewDecisionApproved,
			},
			expected: models.CodeReviewDisputeTriageResult{
				Direction:   models.CodeReviewDisputeDirectionShouldNotHaveApproved,
				DisputeKind: "explicit_reconsideration", AssertsNewInformation: true,
				Routing: models.CodeReviewDisputeRoutingPolicySignalOnly, Confidence: 1,
			},
		},
	}

	service := &DisputeService{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual, err := service.triageResult(context.Background(), tt.dispute, nil, models.CodeReviewListItem{}, nil, nil)
			require.NoError(t, err, "explicit in-app dispute actions should not require an LLM")
			require.Equal(t, tt.expected, actual, "explicit actions should return the exact deterministic route")
		})
	}
}

func TestDisputeService_TriageAnswerOnlyUsesReviewEvidence(t *testing.T) {
	t.Parallel()

	client := &disputeLLMStub{response: `{"direction":"should_have_approved","contested_reason_codes":[],"dispute_kind":"explanation_question","asserts_new_information":false,"routing":"answer_only","confidence":0.99,"reply":"The review blocked because the authorization finding is P1."}`}
	service := &DisputeService{llm: client}

	result, err := service.triageResult(context.Background(), models.CodeReviewDispute{
		Source: models.CodeReviewDisputeSourceGitHubComment, Decision: models.CodeReviewDecisionBlocked,
		Body: "Why did this block?", FiledByLogin: "octocat",
	}, []models.CodeReviewRiskReasonCode{models.CodeReviewRiskReasonBlockingFindings}, models.CodeReviewListItem{
		CodeReviewSessionMetadata: models.CodeReviewSessionMetadata{FinalReviewBody: stringPtrForDisputeTest("A P1 authorization finding blocked approval.")},
		PullRequestTitle:          "Fix authorization",
	}, nil, nil)

	require.NoError(t, err, "an explanation question should be answered from review evidence")
	require.Equal(t, models.CodeReviewDisputeRoutingAnswerOnly, result.Routing, "the evidence answer should remain answer-only")
	require.Equal(t, "The review blocked because the authorization finding is P1.", result.Reply, "the reply should contain the evidence-grounded answer")
	require.NotContains(t, client.userPrompt, "deterministic_route_hint", "punctuation must not bias the LLM route")
	require.Contains(t, client.userPrompt, "A P1 authorization finding blocked approval.", "the LLM should receive the bounded review evidence")
}

func TestDisputeService_GitHubCommentMeaningIsClassifiedOnlyByLLM(t *testing.T) {
	t.Parallel()

	comments := []string{
		"Thanks!",
		"@assembledhq/143-code-reviewer can you re-review this?",
		"@assembledhq/143-code-reviewer I don't believe this is a sensitive change judgment; this is only minor security hardening.",
		"@assembledhq/143-code-reviewer while this technically touches OAuth code, it only fixes the mutex error path.",
		"@assembledhq/143-code-reviewer this is adding security tightening and should be safe based on the observed sign-on rates.",
	}
	for _, comment := range comments {
		comment := comment
		t.Run(comment, func(t *testing.T) {
			t.Parallel()

			client := &disputeLLMStub{response: `{"direction":"should_have_approved","contested_reason_codes":[],"dispute_kind":"security_hardening","asserts_new_information":true,"routing":"reassess","confidence":0.97,"reply":"I will reassess the stated hardening rationale."}`}
			service := &DisputeService{llm: client}
			result, err := service.triageResult(context.Background(), models.CodeReviewDispute{
				Source: models.CodeReviewDisputeSourceGitHubComment, Decision: models.CodeReviewDecisionBlocked,
				Body: comment, FiledByLogin: "octocat",
			}, nil, models.CodeReviewListItem{}, nil, nil)

			require.NoError(t, err, "every post-decision GitHub comment should reach LLM classification")
			require.Equal(t, models.CodeReviewDisputeRoutingReassess, result.Routing, "the LLM output should determine the semantic route")
			require.Contains(t, client.userPrompt, comment, "the exact human comment should be passed as untrusted evidence")
			require.NotContains(t, client.userPrompt, "deterministic_route_hint", "keyword and punctuation hints should not influence classification")
		})
	}
}

func TestDisputeService_ReviewRequestQueuesNormalReviewWithoutDisputeArtifacts(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	disputeID := uuid.New()
	pullRequestID := uuid.New()
	repositoryID := uuid.New()
	commentID := int64(5196810672)
	store := &captureDisputeStore{current: models.CodeReviewDispute{
		ID: disputeID, OrgID: orgID, PullRequestID: pullRequestID, RepositoryID: repositoryID,
		SessionID: uuid.New(), PolicyID: uuid.New(), Source: models.CodeReviewDisputeSourceGitHubComment,
		Decision: models.CodeReviewDecisionBlocked, Body: "@acme/reviewers please review again",
		FiledByLogin: "octocat", AuthorAssociation: "MEMBER", AuthorIsPRAuthor: true,
		RepositoryVisibility: models.CodeReviewRepositoryVisibilityPublic,
		GitHubCommentID:      &commentID,
		QueueSignals:         json.RawMessage(`{"review_request_allowed":true,"github_comment_url":"https://github.com/acme/payments/pull/42#issuecomment-5196810672"}`),
		IntakeStatus:         models.CodeReviewDisputeIntakePending,
		ReassessmentStatus:   models.CodeReviewDisputeReassessmentNotRequested,
	}}
	client := &disputeLLMStub{response: `{"direction":"should_have_approved","contested_reason_codes":[],"dispute_kind":"review_request","asserts_new_information":false,"routing":"review_request","confidence":0.99,"reply":""}`}
	jobs := &disputeJobStoreStub{}
	queue := &disputeReviewRequestQueuerStub{}
	service := NewDisputeService(store, disputeReviewStoreStub{reasons: []models.CodeReviewRiskReasonCode{models.CodeReviewRiskReasonLinesLimitExceeded}}, disputePullRequestStoreStub{pullRequest: models.PullRequest{
		ID: pullRequestID, OrgID: orgID, GitHubRepo: "acme/payments", GitHubPRNumber: 42,
	}}, jobs, client, "", zerolog.Nop())
	service.SetPullRequestSnapshotter(disputePullRequestSnapshotterStub{snapshot: ghservice.CodeReviewPullRequestSnapshot{
		Number: 42, HTMLURL: "https://github.com/acme/payments/pull/42", Title: "Harden auth",
		AuthorLogin: "octocat", HeadSHA: "latest-head", BaseSHA: "base", FromFork: false,
	}})
	service.SetReviewRequestQueuer(queue)

	err := service.Triage(context.Background(), orgID, disputeID)

	require.NoError(t, err, "a bare review request should return to the ordinary durable review path")
	require.Equal(t, models.CodeReviewDisputeRoutingReviewRequest, store.triage.Routing, "the review_request classification should be retained for audit")
	require.Nil(t, store.current.AdjudicationStatus, "an ordinary review request must not enter the disputes queue")
	require.Empty(t, store.authorizations, "an ordinary review request must not record dispute influence authorization")
	require.Empty(t, jobs.enqueued, "the dispute reply path should not publish a second status comment")
	require.Len(t, queue.inputs, 1, "the normal review lifecycle should receive exactly one idempotent request")
	queued := queue.inputs[0]
	require.Equal(t, models.CodeReviewTriggerSourceTeamReviewer, queued.TriggerSource, "the ordinary review should retain the team-reviewer trigger")
	require.Nil(t, queued.TriggeringDisputeID, "an ordinary review request must not be linked as a dispute reassessment")
	require.Equal(t, &disputeID, queued.ReviewRequestDisputeID, "the starter should retain failure-only correlation to the classified request")
	require.Equal(t, "latest-head", queued.HeadSHA, "the review should target the authoritative current head")
	require.NotNil(t, queued.RequestContext, "the classified review request should retain its human-authored context")
	require.Equal(t, "@acme/reviewers please review again", queued.RequestContext.Body, "the comment should still reach orchestrator synthesis")
}

func TestDisputeService_ApprovedDecisionQuestionRemainsAnswerOnly(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	disputeID := uuid.New()
	store := &captureDisputeStore{current: models.CodeReviewDispute{
		ID: disputeID, OrgID: orgID, SessionID: uuid.New(), Source: models.CodeReviewDisputeSourceGitHubComment,
		Decision: models.CodeReviewDecisionApproved, Body: "Why was this approved?",
		AuthorAssociation: "MEMBER", RepositoryVisibility: models.CodeReviewRepositoryVisibilityPublic,
		IntakeStatus: models.CodeReviewDisputeIntakePending,
	}}
	client := &disputeLLMStub{response: `{"direction":"should_not_have_approved","contested_reason_codes":[],"dispute_kind":"approval_question","asserts_new_information":false,"routing":"answer_only","confidence":0.98,"reply":"The stored evidence explains the approval."}`}
	jobs := &disputeJobStoreStub{}
	service := NewDisputeService(store, disputeReviewStoreStub{}, disputePullRequestStoreStub{}, jobs, client, "", zerolog.Nop())

	err := service.Triage(context.Background(), orgID, disputeID)

	require.NoError(t, err, "approval monotonicity should not turn a question into a policy dispute")
	require.Equal(t, models.CodeReviewDisputeRoutingAnswerOnly, store.triage.Routing, "only reassessment routes should be closed after an approval")
	require.Nil(t, store.current.AdjudicationStatus, "an explanation question should not enter policy-owner adjudication")
	require.Len(t, jobs.enqueued, 1, "the evidence-grounded answer should be published once")
}

func TestDisputeService_ReviewRequestSafetyChecksRunAfterClassification(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	disputeID := uuid.New()
	commentID := int64(123)
	store := &captureDisputeStore{current: models.CodeReviewDispute{
		ID: disputeID, OrgID: orgID, SessionID: uuid.New(), Source: models.CodeReviewDisputeSourceGitHubComment,
		Decision: models.CodeReviewDecisionBlocked, Body: "@acme/reviewers please review again",
		FiledByLogin: "octocat", AuthorAssociation: "MEMBER", AuthorIsPRAuthor: true,
		RepositoryVisibility: models.CodeReviewRepositoryVisibilityPublic, GitHubCommentID: &commentID,
		QueueSignals: json.RawMessage(`{"review_request_allowed":false}`), IntakeStatus: models.CodeReviewDisputeIntakePending,
	}}
	client := &disputeLLMStub{response: `{"direction":"should_have_approved","contested_reason_codes":[],"dispute_kind":"review_request","asserts_new_information":false,"routing":"review_request","confidence":0.99,"reply":""}`}
	jobs := &disputeJobStoreStub{}
	queue := &disputeReviewRequestQueuerStub{}
	service := NewDisputeService(store, disputeReviewStoreStub{}, disputePullRequestStoreStub{}, jobs, client, "", zerolog.Nop())
	service.SetReviewRequestQueuer(queue)

	err := service.Triage(context.Background(), orgID, disputeID)

	require.NoError(t, err, "an edited review request should be classified without starting work")
	require.Equal(t, models.CodeReviewDisputeRoutingNotADispute, store.triage.Routing, "the event provenance guard should override the expensive route after LLM classification")
	require.Equal(t, "An edited comment can't start another code review. Post a new reviewer mention to request one.", store.triage.Reply, "the commenter should receive the exact safety explanation")
	require.Empty(t, queue.inputs, "editing an existing comment must not start another agent fan-out")
	require.Len(t, jobs.enqueued, 1, "the safety explanation should use one marker-protected dispute reply")
}

func TestDisputeService_FailedTriageFallsBackForTrustedPullRequestAuthor(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	disputeID := uuid.New()
	pullRequestID := uuid.New()
	commentID := int64(456)
	store := &captureDisputeStore{current: models.CodeReviewDispute{
		ID: disputeID, OrgID: orgID, PullRequestID: pullRequestID, RepositoryID: uuid.New(),
		Source: models.CodeReviewDisputeSourceGitHubComment, Decision: models.CodeReviewDecisionBlocked,
		Body: "@acme/reviewers please take another look", FiledByLogin: "octocat",
		AuthorAssociation: "MEMBER", AuthorIsPRAuthor: true,
		RepositoryVisibility: models.CodeReviewRepositoryVisibilityPublic, GitHubCommentID: &commentID,
		QueueSignals: json.RawMessage(`{"review_request_allowed":true}`), IntakeStatus: models.CodeReviewDisputeIntakePending,
	}}
	jobs := &disputeJobStoreStub{}
	queue := &disputeReviewRequestQueuerStub{}
	service := NewDisputeService(store, disputeReviewStoreStub{}, disputePullRequestStoreStub{pullRequest: models.PullRequest{
		ID: pullRequestID, OrgID: orgID, GitHubRepo: "acme/payments", GitHubPRNumber: 42,
	}}, jobs, nil, "", zerolog.Nop())
	service.SetPullRequestSnapshotter(disputePullRequestSnapshotterStub{snapshot: ghservice.CodeReviewPullRequestSnapshot{
		Number: 42, HeadSHA: "latest-head", AuthorLogin: "octocat",
	}})
	service.SetReviewRequestQueuer(queue)

	err := service.FailTriage(context.Background(), orgID, disputeID, "classification failed")

	require.NoError(t, err, "trusted-author comments should retain a useful fallback when classification exhausts retries")
	require.True(t, store.failed, "the failed intake should remain durable and visible")
	require.False(t, store.failReply, "the normal review status should replace a duplicate dispute reply")
	require.Equal(t, "We could not classify this comment automatically, so we started a normal code review instead.", store.failDetail, "the durable failure state should explain the fallback")
	require.Len(t, queue.inputs, 1, "the original comment should still reach the ordinary review lifecycle")
	require.Empty(t, jobs.enqueued, "a successful fallback should not enqueue a second GitHub reply")
}

func TestDisputeService_FailedReviewRequestBecomesVisibleAndReplyable(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	disputeID := uuid.New()
	routing := models.CodeReviewDisputeRoutingReviewRequest
	store := &captureDisputeStore{current: models.CodeReviewDispute{
		ID: disputeID, OrgID: orgID, Source: models.CodeReviewDisputeSourceGitHubComment,
		Routing: &routing, IntakeStatus: models.CodeReviewDisputeIntakeDiscarded,
	}}
	jobs := &disputeJobStoreStub{}
	service := NewDisputeService(store, disputeReviewStoreStub{}, disputePullRequestStoreStub{}, jobs, nil, "", zerolog.Nop())

	err := service.FailTriage(context.Background(), orgID, disputeID, "triage job exhausted retries")

	require.NoError(t, err, "a classified review request that cannot start should reach a terminal visible state")
	require.True(t, store.failed, "the discarded routing row should transition to failed")
	require.True(t, store.failReply, "the commenter should receive a marker-protected failure response")
	require.Equal(t, "We classified this as a code review request but could not start the review. Post a new reviewer mention to try again.", store.failDetail, "the terminal detail should explain the failed handoff")
	require.Len(t, jobs.enqueued, 1, "the terminal failure should enqueue exactly one GitHub response")
}

func TestDeterministicPolicySignalReplyNamesSettingsAndValues(t *testing.T) {
	t.Parallel()

	reply := deterministicPolicySignalReply(
		[]models.CodeReviewRiskReasonCode{
			models.CodeReviewRiskReasonLinesLimitExceeded,
			models.CodeReviewRiskReasonBlockedPath,
		},
		[]models.CodeReviewRiskReason{
			{Code: models.CodeReviewRiskReasonLinesLimitExceeded, Actual: 431, Limit: 300},
			{Code: models.CodeReviewRiskReasonBlockedPath, Subject: "migrations/unsafe.sql"},
		},
	)

	require.Equal(t, "This objection concerns deterministic policy: Lines changed limit is 300 (observed 431); Blocked paths includes `migrations/unsafe.sql`. Reassessment would apply the same rule, so it was recorded for a policy owner instead.", reply, "the deterministic reply should identify the binding settings and observed values")
}

func stringPtrForDisputeTest(value string) *string {
	return &value
}

func TestBuildCodeReviewDisputeReply(t *testing.T) {
	t.Parallel()

	unsafeDirection := models.CodeReviewDisputeDirectionShouldNotHaveApproved
	upheld := models.CodeReviewDisputeAdjudicationUpheld
	disputeID := uuid.New()
	sessionID := uuid.New()
	tests := []struct {
		name     string
		dispute  models.CodeReviewDispute
		expected string
	}{
		{
			name: "unsafe approval mentions pull request author",
			dispute: models.CodeReviewDispute{
				ID: disputeID, SessionID: sessionID, Direction: &unsafeDirection,
				QueueSignals: json.RawMessage(`{"pull_request_author":"octocat"}`),
			},
			expected: "@octocat I recorded this objection for review. [View the dispute](https://app.example.com/code-reviews?evidence=" + sessionID.String() + ")\n\n" +
				ghservice.PRFeedbackHiddenMarker("code-review-dispute:"+disputeID.String()),
		},
		{
			name: "adjudication replaces interim status",
			dispute: models.CodeReviewDispute{
				ID: disputeID, SessionID: sessionID, AdjudicationStatus: &upheld,
				ReassessmentStatus: models.CodeReviewDisputeReassessmentFailed,
			},
			expected: "A policy owner upheld this objection. The decision is retained as feedback for policy tuning. " +
				"[View the dispute](https://app.example.com/code-reviews?evidence=" + sessionID.String() + ")\n\n" +
				ghservice.PRFeedbackHiddenMarker("code-review-dispute:"+disputeID.String()),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := buildCodeReviewDisputeReply(tt.dispute, "https://app.example.com")
			require.Equal(t, tt.expected, actual, "reply should include the exact durable status detail")
		})
	}
}

func TestCodeReviewDisputeSemanticHashNormalization(t *testing.T) {
	t.Parallel()

	policyID := uuid.New()
	baseline := codeReviewDisputeSemanticHash("ABC123", policyID, "New   EVIDENCE", "Fix Payment", "Body text")
	tests := []struct {
		name     string
		head     string
		evidence string
		title    string
		body     string
		expected bool
	}{
		{name: "case and whitespace are semantic no-ops", head: "abc123", evidence: " new evidence ", title: "fix  payment", body: "BODY TEXT", expected: true},
		{name: "changed evidence changes the semantic input", head: "abc123", evidence: "different evidence", title: "fix payment", body: "body text", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := codeReviewDisputeSemanticHash(tt.head, policyID, tt.evidence, tt.title, tt.body)
			require.Equal(t, tt.expected, baseline == actual, "semantic input hashing should normalize non-meaningful text changes")
		})
	}
}
