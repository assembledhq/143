package codereview

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type assessmentOrgFixture struct{ org models.Organization }

type assessmentExternalFixture struct{ digest string }

type assessmentTeamFixture struct {
	active bool
	err    error
}

func (f assessmentTeamFixture) IsActiveTeamMember(context.Context, int64, string, string, string) (bool, error) {
	return f.active, f.err
}

func (f assessmentExternalFixture) ResolveCodeReviewExternalContext(context.Context, uuid.UUID) (string, string, error) {
	if f.digest == "" {
		return "review tool instructions", "external-digest", nil
	}
	return "review tool instructions", f.digest, nil
}

func (f assessmentOrgFixture) GetByID(context.Context, uuid.UUID) (models.Organization, error) {
	return f.org, nil
}

type assessmentVisualFixture struct {
	snapshot models.CodeReviewVisualEvidenceSnapshot
	request  CaptureVisualEvidenceInput
	restored bool
}

func (f *assessmentVisualFixture) Capture(ctx context.Context, in CaptureVisualEvidenceInput) (models.CodeReviewVisualEvidenceSnapshot, error) {
	f.request = in
	if !f.restored && in.Discover != nil {
		if _, err := in.Discover(ctx); err != nil {
			return models.CodeReviewVisualEvidenceSnapshot{}, err
		}
	}
	return f.snapshot, nil
}

type assessmentFilesFixture struct{}

func (assessmentFilesFixture) ListPullRequestFiles(context.Context, PullRequestFilesRequest) ([]PullRequestFile, error) {
	return []PullRequestFile{{Filename: "web/view.tsx", Status: "modified", Patch: "@@ -1 +1 @@\n-old\n+new"}}, nil
}

type assessmentSnapshotFixture struct {
	snapshot                 ghservice.CodeReviewPullRequestSnapshot
	changeDuringCapture      bool
	calls                    int
	syncErr                  error
	textSources              []ghservice.CodeReviewTextSource
	textCalls, combinedCalls int
	discoveryErr             error
	textHead, textBody       *string
	incompleteText           bool
}

func (f *assessmentSnapshotFixture) PrepareCodeReviewPullRequestSnapshot(context.Context, uuid.UUID, uuid.UUID) (ghservice.CodeReviewPullRequestSnapshotReader, error) {
	return func(context.Context, int) (ghservice.CodeReviewPullRequestSnapshot, error) {
		f.calls++
		snapshot := f.snapshot
		if f.changeDuringCapture && f.calls > 1 {
			snapshot.Body += " changed intent"
		}
		return snapshot, nil
	}, nil
}
func (f *assessmentSnapshotFixture) SyncPullRequestState(context.Context, uuid.UUID, uuid.UUID) error {
	return f.syncErr
}
func (f *assessmentSnapshotFixture) DiscoverCodeReviewTextEvidence(context.Context, uuid.UUID, uuid.UUID, int) (ghservice.CodeReviewTextDiscovery, error) {
	f.textCalls++
	return f.textDiscovery(), f.discoveryErr
}

func (f *assessmentSnapshotFixture) textDiscovery() ghservice.CodeReviewTextDiscovery {
	discovery := ghservice.CodeReviewTextDiscovery{HeadSHA: f.snapshot.HeadSHA, Body: f.snapshot.Body, Sources: f.textSources, Complete: !f.incompleteText}
	if f.textHead != nil {
		discovery.HeadSHA = *f.textHead
	}
	if f.textBody != nil {
		discovery.Body = *f.textBody
	}
	return discovery
}

func (f *assessmentSnapshotFixture) DiscoverCodeReviewEvidence(context.Context, uuid.UUID, uuid.UUID, int) (ghservice.CodeReviewEvidenceDiscovery, error) {
	f.combinedCalls++
	return ghservice.CodeReviewEvidenceDiscovery{Text: f.textDiscovery()}, f.discoveryErr
}

func TestAssessmentInputCaptureBracketsMutableSources(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                                                                  string
		change, staleHealth, incompleteVisual, overflow, closed, syncFailure, invalidSettings bool
		restored, discoveryFailure, incompleteText, textHeadChanged, textBodyChanged          bool
	}{
		{name: "complete capture"}, {name: "restored visual captures only text", restored: true}, {name: "shared provider failure", discoveryFailure: true}, {name: "shared text over budget", incompleteText: true}, {name: "shared text head changes", textHeadChanged: true}, {name: "shared text body changes", textBodyChanged: true}, {name: "intent changes during capture", change: true}, {name: "health for old head", staleHealth: true}, {name: "incomplete visual discovery", incompleteVisual: true}, {name: "visual source overflow", overflow: true}, {name: "closed target", closed: true}, {name: "failed gate refresh", syncFailure: true}, {name: "invalid settings JSON", invalidSettings: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, repoID, prID, policyID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			in := AssessmentInputCaptureRequest{OrgID: orgID, RepositoryID: repoID, PullRequestID: prID, SessionID: uuid.New(), AssessmentID: uuid.New(), Fresh: true}
			head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
			policy := models.CodeReviewPolicyRecord{ID: policyID, OrgID: orgID, Version: 1, Enabled: true}
			cfg := policy.Config()
			policies := &policyStub{resolved: models.CodeReviewResolvedPolicy{Config: cfg, Policy: &policy}}
			snapshots := &assessmentSnapshotFixture{snapshot: ghservice.CodeReviewPullRequestSnapshot{State: "open", Number: 42, Title: "Update view", Body: "Intent stays unchanged", HeadSHA: head, BaseSHA: base, BaseRef: "main"}, changeDuringCapture: tt.change}
			if tt.discoveryFailure {
				snapshots.discoveryErr = errors.New("shared inventory unavailable")
			}
			snapshots.incompleteText = tt.incompleteText
			if tt.textHeadChanged {
				changed := strings.Repeat("c", 40)
				snapshots.textHead = &changed
			}
			if tt.textBodyChanged {
				changed := "changed description"
				snapshots.textBody = &changed
			}
			if tt.closed {
				snapshots.snapshot.State = "closed"
			}
			if tt.syncFailure {
				snapshots.syncErr = errors.New("provider unavailable")
			}
			health := models.PullRequestHealthCurrent{HeadSHA: head, BaseSHA: base, SummaryJSON: json.RawMessage(`{"checks_confirmed":true,"check_set_complete":true,"checks":[]}`)}
			if tt.staleHealth {
				health.HeadSHA = "old"
			}
			prs := &pullRequestStub{result: models.PullRequest{ID: prID, OrgID: orgID, GitHubRepo: "acme/web", GitHubPRNumber: 42}, health: health}
			visual := &assessmentVisualFixture{snapshot: models.CodeReviewVisualEvidenceSnapshot{AssessmentID: &in.AssessmentID, Complete: !tt.incompleteVisual, Overflow: tt.overflow}, restored: tt.restored}
			orgSettings := json.RawMessage(`{}`)
			if tt.invalidSettings {
				orgSettings = json.RawMessage(`{broken`)
			}
			s := NewAssessmentInputCaptureService(policies, prs, &visualEvidenceRepositoryStoreStub{repository: models.Repository{ID: repoID, OrgID: orgID, FullName: "acme/web", InstallationID: 1}}, assessmentOrgFixture{models.Organization{Settings: orgSettings}}, snapshots, visual, assessmentFilesFixture{})
			s.SetExternalContextResolver(assessmentExternalFixture{})
			result, err := s.CaptureAssessmentInputs(context.Background(), in)
			if tt.change || tt.staleHealth || tt.incompleteVisual || tt.overflow || tt.closed || tt.syncFailure || tt.invalidSettings || tt.discoveryFailure || tt.incompleteText || tt.textHeadChanged || tt.textBodyChanged {
				require.Error(t, err, "capture must reject mixed, stale, or unavailable inputs")
				if tt.overflow {
					require.ErrorIs(t, err, ErrAssessmentReuseUnavailable, "visual source overflow should permit legacy full review without reuse")
				}
				if tt.invalidSettings {
					require.ErrorIs(t, err, ErrAssessmentReuseUnavailable, "invalid raw settings must never hash as an empty contract")
				}
				return
			}
			require.NoError(t, err, "complete authoritative inputs should produce a manifest")
			require.NoError(t, ValidateReviewInputManifest(result.Manifest), "persisted manifest must rebuild exactly")
			require.Equal(t, "main", result.Manifest.Code.BaseRef, "base ref must come from GitHub rather than repository default")
			require.True(t, result.Manifest.Gates.ChecksVerified, "complete confirmed check inventory should support check-only rechecks")
			require.Equal(t, head, result.Health.HeadSHA, "captured health must describe the exact reviewed head")
			require.True(t, result.Health.ChecksConfirmed, "captured health should preserve confirmed check provenance")
			require.Equal(t, []ReviewTextEvidence{newReviewTextEvidence("pull_request_description", "42", "https://github.com/acme/web/pull/42", "", "Intent stays unchanged", "full")}, result.Manifest.TextEvidence.Items, "full PR description should be immutable citable text")
			if tt.restored {
				require.Equal(t, 0, snapshots.combinedCalls, "restored visual checkpoint should avoid combined remote discovery")
				require.Equal(t, 1, snapshots.textCalls, "restored capture should read fresh bounded text once")
			} else {
				require.Equal(t, 1, snapshots.combinedCalls, "uncached capture should share one discovery between text and images")
				require.Equal(t, 0, snapshots.textCalls, "shared inventory must avoid a second standalone text discovery")
			}
			require.Equal(t, 2, snapshots.calls, "provider snapshot must bracket files and visual capture")
			require.True(t, visual.request.Fresh, "publication refresh must bypass immutable visual snapshot restoration")
			require.Equal(t, in.AssessmentID, *visual.request.AssessmentID, "capture must retain assessment identity")
			s.SetExternalContextResolver(assessmentExternalFixture{digest: "changed-integration-tools"})
			changed, err := s.CaptureAssessmentInputs(context.Background(), in)
			require.NoError(t, err, "changed external prompt context should remain capturable")
			require.NotEqual(t, result.Manifest.ContractDigest, changed.Manifest.ContractDigest, "integration prompt changes must invalidate source contract")
			policies.resolved.Config.RiskPolicy.EligibleAuthorTeams = []string{"acme/reviewers"}
			snapshots.snapshot.AuthorLogin = "alice"
			_, err = s.CaptureAssessmentInputs(context.Background(), in)
			require.ErrorIs(t, err, ErrAssessmentReuseUnavailable, "unfingerprinted author team membership must disallow reuse")
			s.SetAuthorTeamMembershipChecker(assessmentTeamFixture{err: errors.New("provider unavailable")})
			_, err = s.CaptureAssessmentInputs(context.Background(), in)
			require.Error(t, err, "author team provider failure must remain retryable")
			require.NotErrorIs(t, err, ErrAssessmentReuseUnavailable, "team provider failure must not be treated as deterministic ineligibility")
			s.SetAuthorTeamMembershipChecker(assessmentTeamFixture{active: true})
			member, err := s.CaptureAssessmentInputs(context.Background(), in)
			require.NoError(t, err, "active author membership should be captured")
			s.SetAuthorTeamMembershipChecker(assessmentTeamFixture{active: false})
			nonmember, err := s.CaptureAssessmentInputs(context.Background(), in)
			require.NoError(t, err, "inactive author membership should be captured")
			require.NotEqual(t, member.Manifest.GateDigest, nonmember.Manifest.GateDigest, "author team membership changes must invalidate approval gates")
		})
	}
}

func TestAssessmentInputCaptureCheckStatusProvenance(t *testing.T) {
	t.Parallel()
	orgID, repoID, prID := uuid.New(), uuid.New(), uuid.New()
	in := AssessmentInputCaptureRequest{OrgID: orgID, RepositoryID: repoID, PullRequestID: prID, SessionID: uuid.New(), AssessmentID: uuid.New()}
	head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
	policy := models.CodeReviewPolicyRecord{ID: uuid.New(), OrgID: orgID, Version: 1, Enabled: true}
	policies := &policyStub{resolved: models.CodeReviewResolvedPolicy{Config: policy.Config(), Policy: &policy}}
	snapshots := &assessmentSnapshotFixture{snapshot: ghservice.CodeReviewPullRequestSnapshot{State: "open", Number: 42, Title: "Update view", Body: "Purpose\n## Testing\nPending CI\n", HeadSHA: head, BaseSHA: base, BaseRef: "main"}}
	prs := &pullRequestStub{result: models.PullRequest{ID: prID, OrgID: orgID, GitHubRepo: "acme/web", GitHubPRNumber: 42}, health: models.PullRequestHealthCurrent{HeadSHA: head, BaseSHA: base, SummaryJSON: json.RawMessage(`{"merge_state":"blocked","needs_agent_action":true,"failing_test_count":1,"checks_confirmed":true,"check_set_complete":true,"checks":[{"name":"unit","category":"test","status":"failed","provider":"GitHub","details_url":"https://example.test/check","summary":"tests failed"}]}`)}}
	visual := &assessmentVisualFixture{snapshot: models.CodeReviewVisualEvidenceSnapshot{AssessmentID: &in.AssessmentID, Complete: true}}
	s := NewAssessmentInputCaptureService(policies, prs, &visualEvidenceRepositoryStoreStub{repository: models.Repository{ID: repoID, OrgID: orgID, FullName: "acme/web", InstallationID: 1}}, assessmentOrgFixture{models.Organization{Settings: json.RawMessage(`{}`)}}, snapshots, visual, assessmentFilesFixture{})
	s.SetExternalContextResolver(assessmentExternalFixture{})
	failed, err := s.CaptureAssessmentInputs(context.Background(), in)
	require.NoError(t, err, "failed check projection should be capturable")
	require.Equal(t, 3, len(failed.Manifest.TextEvidence.Items), "full body, Testing section, and check status should be citable")
	check := failed.Manifest.TextEvidence.Items[0]
	for _, item := range failed.Manifest.TextEvidence.Items {
		if item.Surface == "check_status" {
			check = item
		}
	}
	require.Equal(t, "status", check.Section, "check source should declare status-only semantics")
	require.Contains(t, check.Content, "Status: failed", "check evidence should quote the authoritative status")
	require.Equal(t, "https://example.test/check", check.SourceURL, "details URL should be retained as provenance")
	prs.health.SummaryJSON = json.RawMessage(`{"merge_state":"clean","needs_agent_action":false,"failing_test_count":0,"checks_confirmed":true,"check_set_complete":true,"checks":[{"name":"unit","category":"test","status":"passed","provider":"GitHub","details_url":"https://example.test/check","summary":"tests passed"}]}`)
	passed, err := s.CaptureAssessmentInputs(context.Background(), in)
	require.NoError(t, err, "passing check projection should be capturable")
	require.Equal(t, failed.Manifest.Gates.EligibilityDigest, passed.Manifest.Gates.EligibilityDigest, "check-only update should preserve independent eligibility gates")
	require.NotEqual(t, failed.Manifest.Gates.ChecksDigest, passed.Manifest.Gates.ChecksDigest, "check status change should alter the verified check digest")
	require.NotEqual(t, failed.Manifest.TextDigest, passed.Manifest.TextDigest, "check status change should alter citable text evidence")
	plan := PlanReviewRecheck(RecheckPlanInput{Current: &passed.Manifest, Baseline: &RecheckBaseline{Inputs: failed.Manifest, CompletedFull: true, CoverageComplete: true}})
	require.Equal(t, RecheckPlan{Route: RecheckRouteEvidenceOnly, Reason: RecheckReasonChecksChanged}, plan, "fresh verified CI success should qualify without changing code or intent")
}
