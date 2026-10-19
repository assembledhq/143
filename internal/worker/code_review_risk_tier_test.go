package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/codereview"
)

const (
	workerRiskTierHead  = "abcdef1111111111111111111111111111111111"
	workerRiskTierApp   = "example-risk-tier"
	workerRiskTierAppID = int64(101)
)

type fakeRiskTierCheckpoints struct {
	records   map[string]models.CodeReviewPromptRecord
	getErr    error
	createErr error
	winner    *models.CodeReviewPromptRecord
	creates   int
	createdAt time.Time
}

func (f *fakeRiskTierCheckpoints) GetPromptRecordByKey(_ context.Context, orgID uuid.UUID, key string) (models.CodeReviewPromptRecord, error) {
	if f.getErr != nil {
		return models.CodeReviewPromptRecord{}, f.getErr
	}
	record, ok := f.records[key]
	if !ok || record.OrgID != orgID {
		return models.CodeReviewPromptRecord{}, pgx.ErrNoRows
	}
	return record, nil
}

func (f *fakeRiskTierCheckpoints) CreatePromptRecordIfAbsent(_ context.Context, record *models.CodeReviewPromptRecord) (bool, error) {
	f.creates++
	if f.createErr != nil {
		return false, f.createErr
	}
	if f.winner != nil {
		*record = *f.winner
		return false, nil
	}
	if existing, ok := f.records[record.RecordKey]; ok {
		*record = existing
		return false, nil
	}
	if f.records == nil {
		f.records = map[string]models.CodeReviewPromptRecord{}
	}
	record.CreatedAt = f.createdAt
	f.records[record.RecordKey] = *record
	return true, nil
}

type fakeRiskTierCheckRuns struct {
	runs  []models.CodeReviewCheckRun
	err   error
	calls []string
}

func (f *fakeRiskTierCheckRuns) ListCodeReviewCheckRuns(_ context.Context, _, _ uuid.UUID, headSHA, checkName string) ([]models.CodeReviewCheckRun, error) {
	f.calls = append(f.calls, headSHA+"/"+checkName)
	return f.runs, f.err
}

func workerRiskTierPayload(head, tier, render string) string {
	return models.CodeReviewRiskTierPayloadFence + "\n" + fmt.Sprintf(`{"schema_version":1,"generation":{"pr":7,"head_sha":%q,"base_sha":%q,"merge_base_sha":%q,"trunk_sha":%q,"index_id":null,"policy_hash":%q,"mode":"enforce"},"tier":%q,"gate":"success","override":false,"ui_render_change":%q,"published_at":"2026-10-18T12:00:00Z"}`,
		head, strings.Repeat("b", 40), strings.Repeat("b", 40), strings.Repeat("c", 40), strings.Repeat("d", 64), tier, render) + "\n```"
}

func workerRiskTierRun(id int64, appID int64, payloadHead, tier, render string) models.CodeReviewCheckRun {
	return models.CodeReviewCheckRun{ID: id, Name: models.DefaultCodeReviewRiskTierCheckName, HeadSHA: workerRiskTierHead, Status: "completed", Conclusion: "success", AppID: appID, AppSlug: workerRiskTierApp, OutputText: workerRiskTierPayload(payloadHead, tier, render)}
}

func workerRiskTierSetting(appID int64) *models.CodeReviewRiskTierCheckPolicy {
	return &models.CodeReviewRiskTierCheckPolicy{Enabled: true, AppID: appID, AppSlug: workerRiskTierApp}
}

func workerRiskTierPolicy(appID int64) models.CodeReviewPolicyConfig {
	policy := models.DefaultCodeReviewPolicyConfig()
	policy.RiskPolicy.RiskTierCheck = workerRiskTierSetting(appID)
	return policy
}

func workerRiskTierSignal(tier models.CodeReviewRiskTier, render models.CodeReviewUIRenderChange) *models.CodeReviewRiskTierSignal {
	signal := models.ResolveCodeReviewRiskTierSignal(workerRiskTierSetting(workerRiskTierAppID), workerRiskTierHead, []models.CodeReviewCheckRun{workerRiskTierRun(4, workerRiskTierAppID, workerRiskTierHead, string(tier), string(render))})
	return &signal
}

func TestResolveCodeReviewRiskTierSignalWith(t *testing.T) {
	t.Parallel()
	orgID, sessionID := uuid.New(), uuid.New()
	job := runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, RepositoryID: uuid.New(), HeadSHA: workerRiskTierHead}
	setting := *workerRiskTierSetting(workerRiskTierAppID)
	key := codeReviewRiskTierRecordKey(sessionID, workerRiskTierHead, setting)
	stored := func(signal models.CodeReviewRiskTierSignal) models.CodeReviewPromptRecord {
		metadata, err := json.Marshal(signal)
		require.NoError(t, err, "fixture checkpoint should encode")
		return models.CodeReviewPromptRecord{OrgID: orgID, SessionID: sessionID, RecordKey: key, Role: codeReviewRiskTierPromptRole, Metadata: metadata}
	}
	low := *workerRiskTierSignal(models.CodeReviewRiskTierLow, models.CodeReviewUIRenderChangeFalse)
	high := *workerRiskTierSignal(models.CodeReviewRiskTierHigh, models.CodeReviewUIRenderChangeTrue)
	staleCheckpoint := low
	staleCheckpoint.HeadSHA = strings.Repeat("e", 40)
	tests := []struct {
		name            string
		policy          models.CodeReviewPolicyConfig
		checkpoints     *fakeRiskTierCheckpoints
		checks          *fakeRiskTierCheckRuns
		expected        *models.CodeReviewRiskTierSignal
		expectedCalls   []string
		expectedCreates int
		expectError     bool
	}{
		{name: "disabled setting does not read GitHub", policy: models.DefaultCodeReviewPolicyConfig(), checkpoints: &fakeRiskTierCheckpoints{}, checks: &fakeRiskTierCheckRuns{}, expected: &models.CodeReviewRiskTierSignal{}},
		{name: "disabled setting needs no dependencies", policy: models.DefaultCodeReviewPolicyConfig(), expected: &models.CodeReviewRiskTierSignal{}},
		{name: "enabled setting without dependencies fails", policy: workerRiskTierPolicy(workerRiskTierAppID), expectError: true},
		{
			name: "first resolution reads GitHub and checkpoints the result", policy: workerRiskTierPolicy(workerRiskTierAppID),
			checkpoints: &fakeRiskTierCheckpoints{}, checks: &fakeRiskTierCheckRuns{runs: []models.CodeReviewCheckRun{workerRiskTierRun(4, workerRiskTierAppID, workerRiskTierHead, "low", "false")}},
			expected: &low, expectedCalls: []string{workerRiskTierHead + "/risk-tier"}, expectedCreates: 1,
		},
		{
			name: "retried attempt reuses the checkpoint for the same head", policy: workerRiskTierPolicy(workerRiskTierAppID),
			checkpoints: &fakeRiskTierCheckpoints{records: map[string]models.CodeReviewPromptRecord{key: stored(low)}},
			checks:      &fakeRiskTierCheckRuns{runs: []models.CodeReviewCheckRun{workerRiskTierRun(9, workerRiskTierAppID, workerRiskTierHead, "high", "true")}},
			expected:    &low,
		},
		{
			name: "concurrent attempt adopts the first persisted checkpoint", policy: workerRiskTierPolicy(workerRiskTierAppID),
			checkpoints: &fakeRiskTierCheckpoints{winner: func() *models.CodeReviewPromptRecord { r := stored(high); return &r }()},
			checks:      &fakeRiskTierCheckRuns{runs: []models.CodeReviewCheckRun{workerRiskTierRun(4, workerRiskTierAppID, workerRiskTierHead, "low", "false")}},
			expected:    &high, expectedCalls: []string{workerRiskTierHead + "/risk-tier"}, expectedCreates: 1,
		},
		{
			name: "a different configured app id is resolved independently", policy: workerRiskTierPolicy(202),
			checkpoints: &fakeRiskTierCheckpoints{records: map[string]models.CodeReviewPromptRecord{key: stored(low)}},
			checks:      &fakeRiskTierCheckRuns{runs: []models.CodeReviewCheckRun{workerRiskTierRun(4, workerRiskTierAppID, workerRiskTierHead, "low", "false")}},
			expected: &models.CodeReviewRiskTierSignal{Enabled: true, HeadSHA: workerRiskTierHead, AppID: 202, AppSlug: workerRiskTierApp, CheckName: "risk-tier", Source: models.CodeReviewRiskTierSourceUntrustedApp,
				Observations: []models.CodeReviewRiskTierObservation{{Source: models.CodeReviewRiskTierSourceUntrustedApp}}},
			expectedCalls: []string{workerRiskTierHead + "/risk-tier"}, expectedCreates: 1,
		},
		{
			name: "GitHub failure is retried rather than checkpointed", policy: workerRiskTierPolicy(workerRiskTierAppID),
			checkpoints: &fakeRiskTierCheckpoints{}, checks: &fakeRiskTierCheckRuns{err: errors.New("secondary rate limit")},
			expectedCalls: []string{workerRiskTierHead + "/risk-tier"}, expectError: true,
		},
		{
			name: "checkpoint read failure stops the attempt", policy: workerRiskTierPolicy(workerRiskTierAppID),
			checkpoints: &fakeRiskTierCheckpoints{getErr: errors.New("connection reset")}, checks: &fakeRiskTierCheckRuns{}, expectError: true,
		},
		{
			name: "checkpoint write failure stops the attempt", policy: workerRiskTierPolicy(workerRiskTierAppID),
			checkpoints: &fakeRiskTierCheckpoints{createErr: errors.New("connection reset")}, checks: &fakeRiskTierCheckRuns{},
			expectedCalls: []string{workerRiskTierHead + "/risk-tier"}, expectedCreates: 1, expectError: true,
		},
		{
			name: "checkpoint for another head is rejected", policy: workerRiskTierPolicy(workerRiskTierAppID),
			checkpoints: &fakeRiskTierCheckpoints{records: map[string]models.CodeReviewPromptRecord{key: stored(staleCheckpoint)}}, checks: &fakeRiskTierCheckRuns{}, expectError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var checkpoints codeReviewRiskTierCheckpointStore
			if tt.checkpoints != nil {
				checkpoints = tt.checkpoints
			}
			var checks codeReviewRiskTierCheckRunLister
			if tt.checks != nil {
				checks = tt.checks
			}
			signal, err := resolveCodeReviewRiskTierSignalWith(context.Background(), checkpoints, checks, job, tt.policy)
			if tt.expectError {
				require.Error(t, err, "an unresolved risk-tier signal must fail the attempt instead of guessing")
			} else {
				require.NoError(t, err, "the risk-tier signal should resolve")
				require.Equal(t, tt.expected, signal, "the resolved signal should be the checkpointed one for this session and head")
			}
			if tt.checks != nil {
				require.Equal(t, tt.expectedCalls, tt.checks.calls, "GitHub should be read only when no checkpoint exists for the assessed head")
			}
			if tt.checkpoints != nil {
				require.Equal(t, tt.expectedCreates, tt.checkpoints.creates, "a fresh lookup should be checkpointed exactly once")
			}
		})
	}
}

func TestCodeReviewRiskTierRecordKeyBindsSessionHeadAndCheck(t *testing.T) {
	t.Parallel()
	sessionID := uuid.New()
	setting := *workerRiskTierSetting(workerRiskTierAppID)
	base := codeReviewRiskTierRecordKey(sessionID, workerRiskTierHead, setting)
	require.Equal(t, base, codeReviewRiskTierRecordKey(sessionID, strings.ToUpper(workerRiskTierHead), setting), "head SHA casing should not split checkpoints")
	require.Equal(t, base, codeReviewRiskTierRecordKey(sessionID, workerRiskTierHead, models.CodeReviewRiskTierCheckPolicy{Enabled: true, AppID: workerRiskTierAppID, CheckName: "risk-tier"}), "the default check name should match an explicit one")
	require.Equal(t, base, codeReviewRiskTierRecordKey(sessionID, workerRiskTierHead, models.CodeReviewRiskTierCheckPolicy{Enabled: true, AppID: workerRiskTierAppID, AppSlug: "renamed-app"}), "the display slug is not part of the trusted binding")
	require.NotEqual(t, base, codeReviewRiskTierRecordKey(uuid.New(), workerRiskTierHead, setting), "each review session has its own checkpoint")
	require.NotEqual(t, base, codeReviewRiskTierRecordKey(sessionID, strings.Repeat("e", 40), setting), "each head has its own checkpoint")
	require.NotEqual(t, base, codeReviewRiskTierRecordKey(sessionID, workerRiskTierHead, models.CodeReviewRiskTierCheckPolicy{Enabled: true, AppID: 202}), "changing the trusted app id invalidates the checkpoint")
	require.NotEqual(t, base, codeReviewRiskTierRecordKey(sessionID, workerRiskTierHead, models.CodeReviewRiskTierCheckPolicy{Enabled: true, AppID: workerRiskTierAppID, CheckName: "pr-risk"}), "changing the check name invalidates the checkpoint")
}

func TestRefreshCodeReviewRiskTierSignalWith(t *testing.T) {
	t.Parallel()
	low := workerRiskTierSignal(models.CodeReviewRiskTierLow, models.CodeReviewUIRenderChangeFalse)
	high := workerRiskTierSignal(models.CodeReviewRiskTierHigh, models.CodeReviewUIRenderChangeFalse)
	tests := []struct {
		name          string
		policy        models.CodeReviewPolicyConfig
		checkpoint    *models.CodeReviewRiskTierSignal
		checks        *fakeRiskTierCheckRuns
		expected      func() *models.CodeReviewRiskTierSignal
		expectedCalls int
		expectError   bool
		waives        bool
	}{
		{name: "disabled setting skips the live read", policy: models.DefaultCodeReviewPolicyConfig(), checkpoint: &models.CodeReviewRiskTierSignal{}, checks: &fakeRiskTierCheckRuns{}, expected: func() *models.CodeReviewRiskTierSignal { return &models.CodeReviewRiskTierSignal{} }},
		{name: "unchanged live result keeps the checkpoint and the waiver", policy: workerRiskTierPolicy(workerRiskTierAppID), checkpoint: low,
			checks:   &fakeRiskTierCheckRuns{runs: []models.CodeReviewCheckRun{workerRiskTierRun(4, workerRiskTierAppID, workerRiskTierHead, "low", "false")}},
			expected: func() *models.CodeReviewRiskTierSignal { return low }, expectedCalls: 1, waives: true},
		{name: "live rescore to high revokes the waiver", policy: workerRiskTierPolicy(workerRiskTierAppID), checkpoint: low,
			checks:   &fakeRiskTierCheckRuns{runs: []models.CodeReviewCheckRun{workerRiskTierRun(4, workerRiskTierAppID, workerRiskTierHead, "high", "false")}},
			expected: func() *models.CodeReviewRiskTierSignal { combined := low.WithLive(*high); return &combined }, expectedCalls: 1},
		{name: "live lookup failure after a waived checkpoint applies the limits", policy: workerRiskTierPolicy(workerRiskTierAppID), checkpoint: low,
			checks: &fakeRiskTierCheckRuns{err: errors.New("secondary rate limit")},
			expected: func() *models.CodeReviewRiskTierSignal {
				combined := low.WithLive(models.CodeReviewRiskTierLookupFailedSignal(workerRiskTierSetting(workerRiskTierAppID), workerRiskTierHead))
				return &combined
			}, expectedCalls: 1, expectError: true},
		{name: "live lookup failure after an applied checkpoint keeps applying the limits", policy: workerRiskTierPolicy(workerRiskTierAppID), checkpoint: high,
			checks: &fakeRiskTierCheckRuns{err: errors.New("secondary rate limit")},
			expected: func() *models.CodeReviewRiskTierSignal {
				combined := high.WithLive(models.CodeReviewRiskTierLookupFailedSignal(workerRiskTierSetting(workerRiskTierAppID), workerRiskTierHead))
				return &combined
			}, expectedCalls: 1, expectError: true},
		{name: "missing reader is a lookup failure", policy: workerRiskTierPolicy(workerRiskTierAppID), checkpoint: low,
			expected: func() *models.CodeReviewRiskTierSignal {
				combined := low.WithLive(models.CodeReviewRiskTierLookupFailedSignal(workerRiskTierSetting(workerRiskTierAppID), workerRiskTierHead))
				return &combined
			}, expectError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var checks codeReviewRiskTierCheckRunLister
			if tt.checks != nil {
				checks = tt.checks
			}
			job := runCodeReviewPayload{OrgID: uuid.New(), SessionID: uuid.New(), RepositoryID: uuid.New(), HeadSHA: workerRiskTierHead, RiskTierSignal: tt.checkpoint}
			combined, err := refreshCodeReviewRiskTierSignalWith(context.Background(), checks, job, tt.policy)
			if tt.expectError {
				require.Error(t, err, "a failed live read should be reported to the caller")
			} else {
				require.NoError(t, err, "a successful live read should not error")
			}
			require.Equal(t, tt.expected(), combined, "the live read should combine with the checkpoint by the most restrictive result")
			require.Equal(t, tt.waives, combined.WaivesSizeSafeguard(), "only an authoritative low or medium across checkpoint and live state waives the limits")
			if tt.checks != nil {
				require.Len(t, tt.checks.calls, tt.expectedCalls, "the live read should query GitHub once when the setting is active")
			}
		})
	}
}

func TestCodeReviewRiskTierSignalGatesEverySizeEvaluation(t *testing.T) {
	t.Parallel()
	policy := models.DefaultCodeReviewPolicyConfig()
	policy.ApprovalMode = models.CodeReviewApprovalModeApproveAcceptable
	files := make([]codereview.PullRequestFile, 0, 6)
	for _, name := range []string{"a", "b", "c", "d", "e", "f"} {
		files = append(files, codereview.PullRequestFile{Filename: "docs/guide/" + name + ".md", Additions: 70, Deletions: 1})
	}
	sizeReasons := []models.CodeReviewRiskReason{
		{Code: models.CodeReviewRiskReasonFilesLimitExceeded, Actual: 6, Limit: 5},
		{Code: models.CodeReviewRiskReasonAdditionsLimitExceeded, Actual: 420, Limit: 300},
	}
	record := func(outcome models.CodeReviewSizeSafeguardOutcome, tier models.CodeReviewRiskTier) models.CodeReviewRiskReason {
		decision := workerRiskTierSignal(tier, models.CodeReviewUIRenderChangeFalse).SizeSafeguardDecision(
			models.CodeReviewSizeMeasurement{Actual: 6, Limit: 5, Exceeded: true},
			models.CodeReviewSizeMeasurement{Actual: 420, Limit: 300, Exceeded: true},
			models.CodeReviewSizeMeasurement{Actual: 6, Limit: 300},
		)
		require.Equal(t, outcome, decision.Outcome, "the expected record should carry the expected outcome")
		require.Equal(t, int64(4), decision.CheckRunID, "the record should name the deciding check run")
		return models.CodeReviewRiskReason{Code: models.CodeReviewRiskReasonSizeSafeguard, SizeSafeguard: &decision}
	}
	tests := []struct {
		name             string
		signal           *models.CodeReviewRiskTierSignal
		expectedStable   []models.CodeReviewRiskReason
		expectedPrompt   []string
		expectedDecision models.CodeReviewDecision
		expectedArchived []models.CodeReviewRiskReason
	}{
		{
			name: "setting disabled keeps today's blockers everywhere", signal: &models.CodeReviewRiskTierSignal{},
			expectedStable: sizeReasons, expectedPrompt: models.CodeReviewRiskReasonMessages(sizeReasons),
			expectedDecision: models.CodeReviewDecisionNeedsHumanReview, expectedArchived: sizeReasons,
		},
		{
			name: "low tier waives size in early stop, prompt, and decision", signal: workerRiskTierSignal(models.CodeReviewRiskTierLow, models.CodeReviewUIRenderChangeFalse),
			expectedStable: []models.CodeReviewRiskReason{record(models.CodeReviewSizeSafeguardWaived, models.CodeReviewRiskTierLow)}, expectedPrompt: []string{},
			expectedDecision: models.CodeReviewDecisionApproved, expectedArchived: []models.CodeReviewRiskReason{record(models.CodeReviewSizeSafeguardWaived, models.CodeReviewRiskTierLow)},
		},
		{
			name: "live rescore to high blocks approval after a low checkpoint", signal: func() *models.CodeReviewRiskTierSignal {
				combined := workerRiskTierSignal(models.CodeReviewRiskTierLow, models.CodeReviewUIRenderChangeFalse).WithLive(*workerRiskTierSignal(models.CodeReviewRiskTierHigh, models.CodeReviewUIRenderChangeFalse))
				return &combined
			}(),
			expectedStable: func() []models.CodeReviewRiskReason {
				combined := workerRiskTierSignal(models.CodeReviewRiskTierLow, models.CodeReviewUIRenderChangeFalse).WithLive(*workerRiskTierSignal(models.CodeReviewRiskTierHigh, models.CodeReviewUIRenderChangeFalse))
				decision := combined.SizeSafeguardDecision(models.CodeReviewSizeMeasurement{Actual: 6, Limit: 5, Exceeded: true}, models.CodeReviewSizeMeasurement{Actual: 420, Limit: 300, Exceeded: true}, models.CodeReviewSizeMeasurement{Actual: 6, Limit: 300})
				return append(append([]models.CodeReviewRiskReason{}, sizeReasons...), models.CodeReviewRiskReason{Code: models.CodeReviewRiskReasonSizeSafeguard, SizeSafeguard: &decision})
			}(),
			expectedPrompt:   models.CodeReviewRiskReasonMessages(sizeReasons),
			expectedDecision: models.CodeReviewDecisionNeedsHumanReview,
			expectedArchived: func() []models.CodeReviewRiskReason {
				combined := workerRiskTierSignal(models.CodeReviewRiskTierLow, models.CodeReviewUIRenderChangeFalse).WithLive(*workerRiskTierSignal(models.CodeReviewRiskTierHigh, models.CodeReviewUIRenderChangeFalse))
				decision := combined.SizeSafeguardDecision(models.CodeReviewSizeMeasurement{Actual: 6, Limit: 5, Exceeded: true}, models.CodeReviewSizeMeasurement{Actual: 420, Limit: 300, Exceeded: true}, models.CodeReviewSizeMeasurement{Actual: 6, Limit: 300})
				require.Len(t, decision.Observations, 2, "the record should keep both the checkpoint and the live observation")
				return append(append([]models.CodeReviewRiskReason{}, sizeReasons...), models.CodeReviewRiskReason{Code: models.CodeReviewRiskReasonSizeSafeguard, SizeSafeguard: &decision})
			}(),
		},
		{
			name: "high tier applies size in early stop, prompt, and decision", signal: workerRiskTierSignal(models.CodeReviewRiskTierHigh, models.CodeReviewUIRenderChangeFalse),
			expectedStable:   append(append([]models.CodeReviewRiskReason{}, sizeReasons...), record(models.CodeReviewSizeSafeguardApplied, models.CodeReviewRiskTierHigh)),
			expectedPrompt:   models.CodeReviewRiskReasonMessages(sizeReasons),
			expectedDecision: models.CodeReviewDecisionNeedsHumanReview,
			expectedArchived: append(append([]models.CodeReviewRiskReason{}, sizeReasons...), record(models.CodeReviewSizeSafeguardApplied, models.CodeReviewRiskTierHigh)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID := uuid.New()
			job := runCodeReviewPayload{OrgID: orgID, SessionID: uuid.New(), HeadSHA: "head", RiskTierSignal: tt.signal}
			body := "Expands the deployment guide.\n\nTesting: docs build."
			pr := models.PullRequest{OrgID: orgID, Body: &body, HeadSHA: stringPtr("head"), Status: models.PullRequestStatusOpen, AuthoredBy: models.GitIdentitySourceUser}
			health := &models.PullRequestHealthResponse{HeadSHA: "head", Status: models.PullRequestStatusOpen, CanMerge: true, ChecksConfirmed: true, MergeState: models.PullRequestMergeStateClean}
			reviewers := []models.CodeReviewAgentResult{
				{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
				{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
			}

			stable := codeReviewStableDeterministicRisk(policy, job, pr, files, true)
			require.Equal(t, tt.expectedStable, stable.ReasonDetails, "deterministic early stop should honor the tier for the assessed head")
			require.Equal(t, len(models.CodeReviewBlockingRiskReasons(tt.expectedStable)) == 0, stable.Acceptable, "only blocking reasons should trigger early stop")

			prompt := models.CodeReviewRiskReasonMessages(codeReviewPromptRiskReasons(job, pr, health, policy, files, reviewers, nil, models.CodeReviewVisualEvidenceSnapshot{}))
			require.Equal(t, tt.expectedPrompt, prompt, "the orchestrator prompt should list the same size blockers as the decision")

			input := liveCodeReviewOutcomeInput{Policy: policy, Job: job, PullRequest: pr, Health: health, AgentResults: reviewers, ChangedFiles: files, ChangedFilesAvailable: true}
			input = withApprovingRiskTierSynthesis(input)
			decision, rendered := evaluateLiveCodeReviewOutcome(input)
			require.Equal(t, tt.expectedDecision, decision.Decision, "the final decision should honor the tier for the assessed head")
			require.Equal(t, tt.expectedArchived, decision.RiskReasonDetails, "the archived reasons should record which size safeguard decision was made")
			require.NotContains(t, rendered, "size_safeguard", "the GitHub review body must not render the informational record")
		})
	}
}

func withApprovingRiskTierSynthesis(input liveCodeReviewOutcomeInput) liveCodeReviewOutcomeInput {
	assessments := make([]codeReviewDescriptionAssessment, 0)
	for _, requirement := range codeReviewApplicableDescriptionRequirements(input.Policy, input.ChangedFiles) {
		assessments = append(assessments, codeReviewDescriptionAssessment{
			Key: requirement.Key, Status: codeReviewDescriptionAssessmentSatisfied, EvidenceBasis: models.CodeReviewDescriptionEvidenceBasisPullRequestDescription,
			EvidenceIDs: []string{}, Reason: "The coding agent found the requirement satisfied.",
		})
	}
	synthesis := codeReviewOrchestratorSynthesis{
		ApprovalRecommended: true, DescriptionAssessments: assessments, DescriptionInputHash: codeReviewDescriptionInputHash(input.PullRequest, input.VisualEvidence),
		Summary: "The coding agent completed the approval assessment.", ReviewSummary: "The coding-agent review found no blocking issues.",
		RiskNotes: []string{}, Findings: []codeReviewOrchestratorFinding{}, HumanReviewReasons: []codeReviewOrchestratorHumanReviewReason{},
	}
	input.OrchestratorSynthesis = synthesis
	input.AgentResults = append(append([]models.CodeReviewAgentResult{}, input.AgentResults...), models.CodeReviewAgentResult{
		Role: models.CodeReviewAgentRoleOrchestrator, Status: models.CodeReviewAgentResultStatusCompleted,
		StructuredResult: marshalCodeReviewOrchestratorStructuredResult(codeReviewOrchestratorStructuredResult{DescriptionInputHash: synthesis.DescriptionInputHash, SynthesisValidated: true, Synthesis: synthesis}),
	})
	return input
}

func TestRefreshCodeReviewRiskTierSignalForDecisionBoundsLookupRetries(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	low := workerRiskTierSignal(models.CodeReviewRiskTierLow, models.CodeReviewUIRenderChangeFalse)
	failed := func() *models.CodeReviewRiskTierSignal {
		combined := low.WithLive(models.CodeReviewRiskTierLookupFailedSignal(workerRiskTierSetting(workerRiskTierAppID), workerRiskTierHead))
		return &combined
	}
	sessionID := uuid.New()
	failureKey := codeReviewRiskTierRecordKey(sessionID, workerRiskTierHead, *workerRiskTierSetting(workerRiskTierAppID)) + "/live-lookup-failure"
	tests := []struct {
		name          string
		checkpoints   *fakeRiskTierCheckpoints
		checks        *fakeRiskTierCheckRuns
		expected      *models.CodeReviewRiskTierSignal
		expectRetryAt *time.Time
		expectError   bool
	}{
		{name: "a successful live read decides immediately", checkpoints: &fakeRiskTierCheckpoints{createdAt: now},
			checks: &fakeRiskTierCheckRuns{runs: []models.CodeReviewCheckRun{workerRiskTierRun(4, workerRiskTierAppID, workerRiskTierHead, "low", "false")}}, expected: low},
		{name: "the first failure opens a durable retry window", checkpoints: &fakeRiskTierCheckpoints{createdAt: now},
			checks: &fakeRiskTierCheckRuns{err: errors.New("secondary rate limit")}, expectRetryAt: &now},
		{name: "a failure inside the window keeps retrying from the original start", checkpoints: &fakeRiskTierCheckpoints{records: map[string]models.CodeReviewPromptRecord{failureKey: {RecordKey: failureKey, CreatedAt: now.Add(-5 * time.Minute)}}},
			checks: &fakeRiskTierCheckRuns{err: errors.New("secondary rate limit")}, expectRetryAt: func() *time.Time { started := now.Add(-5 * time.Minute); return &started }()},
		{name: "an exhausted window decides with the conservative signal", checkpoints: &fakeRiskTierCheckpoints{records: map[string]models.CodeReviewPromptRecord{failureKey: {RecordKey: failureKey, CreatedAt: now.Add(-maxRetryableDuration)}}},
			checks: &fakeRiskTierCheckRuns{err: errors.New("secondary rate limit")}, expected: failed()},
		{name: "an unrecordable failure is returned", checkpoints: &fakeRiskTierCheckpoints{createErr: errors.New("connection reset")},
			checks: &fakeRiskTierCheckRuns{err: errors.New("secondary rate limit")}, expectError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			job := runCodeReviewPayload{OrgID: uuid.New(), SessionID: sessionID, RepositoryID: uuid.New(), HeadSHA: workerRiskTierHead, RiskTierSignal: low}
			signal, err := refreshCodeReviewRiskTierSignalForDecisionWith(context.Background(), tt.checkpoints, tt.checks, zerolog.Nop(), job, workerRiskTierPolicy(workerRiskTierAppID), func() time.Time { return now })
			switch {
			case tt.expectError:
				require.Error(t, err, "a failure that cannot be recorded should fail the attempt")
				var retry *RetryableError
				require.False(t, errors.As(err, &retry), "an unrecordable failure should not claim a retry window")
			case tt.expectRetryAt != nil:
				var retry *RetryableError
				require.ErrorAs(t, err, &retry, "a failure inside the window should retry without spending the attempt budget")
				require.False(t, retry.ConsumeAttempt, "the retry should not consume the job's attempt budget")
				require.Equal(t, *tt.expectRetryAt, *retry.RetryWindowStartedAt, "the retry window should start at the first recorded failure")
				require.Equal(t, codeReviewRiskTierLiveRetryDelay, *retry.RetryAfter, "the retry should wait before reading GitHub again")
				require.True(t, retry.BypassMaxRetryDuration, "the helper's durable window should be the only deadline for its retries")
				expired, _ := retryableDurationExceeded(*retry.RetryWindowStartedAt, retry, now.Add(maxRetryableDuration))
				require.False(t, expired, "the worker must not dead-letter a retry the handler still intends to take")
			default:
				require.NoError(t, err, "the decision should proceed")
				require.Equal(t, tt.expected, signal, "the decision should use the combined signal")
			}
		})
	}
}

func TestExhaustedRiskTierLookupPublishesWithTheLimits(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	policy := workerRiskTierPolicy(workerRiskTierAppID)
	policy.ApprovalMode = models.CodeReviewApprovalModeApproveAcceptable
	sessionID := uuid.New()
	failureKey := codeReviewRiskTierRecordKey(sessionID, "head", *policy.RiskPolicy.RiskTierCheck) + "/live-lookup-failure"
	checkpoints := &fakeRiskTierCheckpoints{records: map[string]models.CodeReviewPromptRecord{failureKey: {RecordKey: failureKey, CreatedAt: now.Add(-time.Hour)}}}
	job := runCodeReviewPayload{OrgID: uuid.New(), SessionID: sessionID, RepositoryID: uuid.New(), HeadSHA: "head", RiskTierSignal: workerRiskTierSignal(models.CodeReviewRiskTierLow, models.CodeReviewUIRenderChangeFalse)}
	signal, err := refreshCodeReviewRiskTierSignalForDecisionWith(context.Background(), checkpoints, &fakeRiskTierCheckRuns{err: errors.New("checks API unavailable")}, zerolog.Nop(), job, policy, func() time.Time { return now })
	require.NoError(t, err, "an exhausted lookup window should let the review publish")
	job.RiskTierSignal = signal
	requireRiskTierLookupFailurePublishesWithTheLimits(t, policy, job)
}

type riskTierTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *riskTierTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *riskTierTestClock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

func (c *riskTierTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type slowFailingRiskTierCheckRuns struct {
	clock   *riskTierTestClock
	latency time.Duration
}

func (f *slowFailingRiskTierCheckRuns) ListCodeReviewCheckRuns(context.Context, uuid.UUID, uuid.UUID, string, string) ([]models.CodeReviewCheckRun, error) {
	f.clock.Advance(f.latency)
	return nil, errors.New("checks API timed out")
}

func TestRiskTierLookupRetryNearTheWindowEdgePublishesWithTheLimits(t *testing.T) {
	t.Parallel()
	const lookupLatency = 5 * time.Second
	tests := []struct {
		name        string
		helperAge   time.Duration
		workerAge   time.Duration
		expectRetry bool
	}{
		{name: "a slow lookup that ends too late for another retry falls back immediately", helperAge: 7*time.Minute + 29*time.Second, workerAge: 8*time.Minute + 4*time.Second},
		{name: "a retry decided inside the window survives a later worker clock", helperAge: 7 * time.Minute, workerAge: 7*time.Minute + 45*time.Second, expectRetry: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			policy := workerRiskTierPolicy(workerRiskTierAppID)
			policy.ApprovalMode = models.CodeReviewApprovalModeApproveAcceptable
			job := runCodeReviewPayload{OrgID: uuid.New(), SessionID: uuid.New(), RepositoryID: uuid.New(), HeadSHA: workerRiskTierHead, RiskTierSignal: workerRiskTierSignal(models.CodeReviewRiskTierLow, models.CodeReviewUIRenderChangeFalse)}
			startedAt := time.Now().Add(-tt.workerAge)
			failureKey := codeReviewRiskTierRecordKey(job.SessionID, job.HeadSHA, *policy.RiskPolicy.RiskTierCheck) + "/live-lookup-failure"
			checkpoints := &fakeRiskTierCheckpoints{records: map[string]models.CodeReviewPromptRecord{failureKey: {RecordKey: failureKey, CreatedAt: startedAt}}}
			clock := &riskTierTestClock{now: startedAt.Add(tt.helperAge)}
			checks := &slowFailingRiskTierCheckRuns{clock: clock, latency: lookupLatency}
			var decided *models.CodeReviewRiskTierSignal
			var retry *RetryableError
			w, mock := newTestWorker(t)
			defer mock.Close()
			w.Register(models.JobTypeRunCodeReview, func(ctx context.Context, _ string, _ json.RawMessage) error {
				signal, err := refreshCodeReviewRiskTierSignalForDecisionWith(ctx, checkpoints, checks, zerolog.Nop(), job, policy, clock.Now)
				if err != nil {
					errors.As(err, &retry)
					return fmt.Errorf("refresh code review risk-tier signal before decision: %w", err)
				}
				decided = signal
				return nil
			})
			jobID, lockToken := uuid.New(), uuid.New()

			expectClaim(mock, jobID, job.OrgID, models.JobTypeRunCodeReview, json.RawMessage(`{}`), startedAt, lockToken)
			if tt.expectRetry {
				mock.ExpectExec("attempts = GREATEST\\(attempts - 1, 0\\)").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), jobID, lockToken).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			} else {
				mock.ExpectExec("UPDATE jobs\\s+SET status = 'succeeded'").
					WithArgs(jobID, lockToken).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			}
			w.poll(context.Background())
			require.NoError(t, mock.ExpectationsWereMet(), "the worker should retry or finish the review, never dead-letter it")

			if tt.expectRetry {
				require.NotNil(t, retry, "a failure decided inside the window should ask for another lookup")
				require.Nil(t, decided, "the review should not decide while a retry is still inside the window")
				clock.Set(startedAt.Add(tt.workerAge + *retry.RetryAfter))
				expectClaim(mock, jobID, job.OrgID, models.JobTypeRunCodeReview, json.RawMessage(`{}`), startedAt, lockToken)
				mock.ExpectExec("UPDATE jobs\\s+SET status = 'succeeded'").
					WithArgs(jobID, lockToken).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				w.poll(context.Background())
				require.NoError(t, mock.ExpectationsWereMet(), "the retried attempt should finish the review instead of dead-lettering it")
			}
			require.NotNil(t, decided, "the review should reach the conservative decision")
			job.RiskTierSignal = decided
			requireRiskTierLookupFailurePublishesWithTheLimits(t, policy, job)
		})
	}
}

func requireRiskTierLookupFailurePublishesWithTheLimits(t *testing.T, policy models.CodeReviewPolicyConfig, job runCodeReviewPayload) {
	t.Helper()
	files := make([]codereview.PullRequestFile, 0, 6)
	for _, name := range []string{"a", "b", "c", "d", "e", "f"} {
		files = append(files, codereview.PullRequestFile{Filename: "docs/guide/" + name + ".md", Additions: 70, Deletions: 1})
	}
	body := "Expands the deployment guide.\n\nTesting: docs build."
	input := liveCodeReviewOutcomeInput{
		Policy: policy, Job: job,
		PullRequest: models.PullRequest{OrgID: job.OrgID, Body: &body, HeadSHA: stringPtr(job.HeadSHA), Status: models.PullRequestStatusOpen, AuthoredBy: models.GitIdentitySourceUser},
		Health:      &models.PullRequestHealthResponse{HeadSHA: job.HeadSHA, Status: models.PullRequestStatusOpen, CanMerge: true, ChecksConfirmed: true, MergeState: models.PullRequestMergeStateClean},
		AgentResults: []models.CodeReviewAgentResult{
			{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
			{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
		},
		ChangedFiles: files, ChangedFilesAvailable: true,
	}
	decision, _ := evaluateLiveCodeReviewOutcome(withApprovingRiskTierSynthesis(input))
	require.Equal(t, models.CodeReviewDecisionNeedsHumanReview, decision.Decision, "a persistent lookup outage should publish the conservative decision")
	require.Contains(t, decision.RiskReasonDetails, models.CodeReviewRiskReason{Code: models.CodeReviewRiskReasonFilesLimitExceeded, Actual: 6, Limit: 5}, "the size limits should apply")
	var record *models.CodeReviewSizeSafeguardDecision
	for _, reason := range decision.RiskReasonDetails {
		if reason.SizeSafeguard != nil {
			record = reason.SizeSafeguard
		}
	}
	require.NotNil(t, record, "the archive should record the safeguard decision")
	require.Equal(t, models.CodeReviewRiskTierSourceLookupFailed, record.Source, "the archive should record the failed live lookup")
	require.Equal(t, models.CodeReviewSizeSafeguardApplied, record.Outcome, "the archive should record that the limits applied")
}

func TestCodeReviewRiskTierApprovalHolds(t *testing.T) {
	t.Parallel()
	low := workerRiskTierSignal(models.CodeReviewRiskTierLow, models.CodeReviewUIRenderChangeFalse)
	high := workerRiskTierSignal(models.CodeReviewRiskTierHigh, models.CodeReviewUIRenderChangeFalse)
	exceeded := low.SizeSafeguardDecision(models.CodeReviewSizeMeasurement{Actual: 6, Limit: 5, Exceeded: true}, models.CodeReviewSizeMeasurement{Actual: 1, Limit: 300}, models.CodeReviewSizeMeasurement{Actual: 1, Limit: 300})
	within := low.SizeSafeguardDecision(models.CodeReviewSizeMeasurement{Actual: 1, Limit: 5}, models.CodeReviewSizeMeasurement{Actual: 1, Limit: 300}, models.CodeReviewSizeMeasurement{Actual: 1, Limit: 300})
	applied := high.SizeSafeguardDecision(models.CodeReviewSizeMeasurement{Actual: 1, Limit: 5}, models.CodeReviewSizeMeasurement{Actual: 1, Limit: 300}, models.CodeReviewSizeMeasurement{Actual: 1, Limit: 300})
	record := func(decision models.CodeReviewSizeSafeguardDecision) []models.CodeReviewRiskReason {
		return []models.CodeReviewRiskReason{{Code: models.CodeReviewRiskReasonSizeSafeguard, SizeSafeguard: &decision}}
	}
	tests := []struct {
		name    string
		staged  []models.CodeReviewRiskReason
		current *models.CodeReviewRiskTierSignal
		holds   bool
	}{
		{name: "no safeguard record", current: high, holds: true},
		{name: "waived within limits", staged: record(within), current: high, holds: true},
		{name: "staged with applied limits", staged: record(applied), current: high, holds: true},
		{name: "waived over limits and still low", staged: record(exceeded), current: low, holds: true},
		{name: "waived over limits and now high", staged: record(exceeded), current: high},
		{name: "waived over limits and now disabled", staged: record(exceeded), current: &models.CodeReviewRiskTierSignal{}},
		{name: "waived over limits and now unreadable", staged: record(exceeded), current: func() *models.CodeReviewRiskTierSignal {
			combined := low.WithLive(models.CodeReviewRiskTierLookupFailedSignal(workerRiskTierSetting(workerRiskTierAppID), workerRiskTierHead))
			return &combined
		}()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			holds, detail, err := codeReviewRiskTierApprovalHolds(tt.staged, tt.current)
			require.NoError(t, err, "the check should not fail")
			require.Equal(t, tt.holds, holds, "a staged approval should hold only while its size waiver is still supported")
			require.Equal(t, tt.holds, detail == "", "a withheld approval should explain the change")
		})
	}
}
