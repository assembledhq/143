package codereview

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAssessmentOpaqueIntentRouting(t *testing.T) {
	t.Parallel()
	const template = "## Description\nPurpose\n<!-- template guidance -->\n## Testing\nPending\n"
	const opaqueDiscussion = "<details>\n## Evidence\nThis heading is not a trusted boundary\n</details>\n"
	const screenshots = "## Description\nRemove unused `IconBase` exports.\n## Screenshots\nBefore `Icons`\n![before](https://example.test/before.png)\n## Deploy steps\nNo special steps.\n"
	const stackComment = "* **#42** <a href=\"https://app.graphite.com/github/pr/acme/web/42\">View in Graphite</a>\n<h2></h2>\n<!-- Current dependencies on/for this PR: -->"
	const preview = "## Description\nOpen the agent from `Edit details`.\n## Screenshots\nNot verified in the running app.\n## Deploy steps\nLow risk.\n"
	tests := []struct {
		name                   string
		body, discussion       string
		newBody, newDiscussion string
		addEvidence            bool
		wantAmbiguous          bool
		want                   RecheckPlan
	}{
		{name: "unchanged template reuses exact result", body: template, newBody: template, wantAmbiguous: true, want: RecheckPlan{RecheckRouteReuse, RecheckReasonUnchanged}},
		{name: "template permits separate evidence", body: template, newBody: template, addEvidence: true, wantAmbiguous: true, want: RecheckPlan{RecheckRouteEvidenceOnly, RecheckReasonEvidenceChanged}},
		{name: "opaque body evidence edits are reassessed as evidence", body: template, newBody: strings.Replace(template, "Pending", "Passed", 1), wantAmbiguous: true, want: RecheckPlan{RecheckRouteEvidenceOnly, RecheckReasonEvidenceChanged}},
		{name: "opaque hidden heading edits are reassessed as evidence", body: template, newBody: strings.Replace(template, "template guidance", "## Testing\nNew intent", 1), addEvidence: true, wantAmbiguous: true, want: RecheckPlan{RecheckRouteEvidenceOnly, RecheckReasonEvidenceChanged}},
		{name: "opaque whitespace remains significant", body: template, newBody: template + " ", addEvidence: true, wantAmbiguous: true, want: RecheckPlan{RecheckRouteEvidenceOnly, RecheckReasonEvidenceChanged}},
		{name: "body changes from opaque to parsed", body: template, newBody: strings.Replace(template, "<!-- template guidance -->\n", "", 1), addEvidence: true, wantAmbiguous: false, want: RecheckPlan{RecheckRouteEvidenceOnly, RecheckReasonEvidenceChanged}},
		{name: "body changes from parsed to opaque", body: "Purpose\n", newBody: template, addEvidence: true, wantAmbiguous: true, want: RecheckPlan{RecheckRouteEvidenceOnly, RecheckReasonEvidenceChanged}},
		{name: "opaque discussion permits separate evidence", body: "Purpose\n", newBody: "Purpose\n", discussion: opaqueDiscussion, newDiscussion: opaqueDiscussion, addEvidence: true, wantAmbiguous: true, want: RecheckPlan{RecheckRouteEvidenceOnly, RecheckReasonEvidenceChanged}},
		{name: "opaque discussion edit is reassessed as evidence", body: "Purpose\n", newBody: "Purpose\n", discussion: opaqueDiscussion, newDiscussion: opaqueDiscussion + "Changed\n", addEvidence: true, wantAmbiguous: true, want: RecheckPlan{RecheckRouteEvidenceOnly, RecheckReasonEvidenceChanged}},
		{name: "opaque discussion deletion is reassessed as evidence", body: "Purpose\n", newBody: "Purpose\n", discussion: opaqueDiscussion, addEvidence: true, want: RecheckPlan{RecheckRouteEvidenceOnly, RecheckReasonEvidenceChanged}},
		{name: "opaque discussion becomes evidence is reassessed as evidence", body: "Purpose\n", newBody: "Purpose\n", discussion: opaqueDiscussion, newDiscussion: "## Evidence\nPassed\n", want: RecheckPlan{RecheckRouteEvidenceOnly, RecheckReasonEvidenceChanged}},
		{name: "new opaque discussion is reassessed as evidence", body: "Purpose\n", newBody: "Purpose\n", newDiscussion: opaqueDiscussion, addEvidence: true, wantAmbiguous: true, want: RecheckPlan{RecheckRouteEvidenceOnly, RecheckReasonEvidenceChanged}},
		{name: "screenshot captions and images are evidence", body: screenshots, newBody: strings.ReplaceAll(screenshots, "before", "after"), want: RecheckPlan{RecheckRouteEvidenceOnly, RecheckReasonEvidenceChanged}},
		{name: "prose outside screenshots is reassessed as evidence", body: screenshots, newBody: strings.Replace(screenshots, "No special steps.", "New behavior.", 1), want: RecheckPlan{RecheckRouteEvidenceOnly, RecheckReasonEvidenceChanged}},
		{name: "Graphite discussion permits added preview evidence", body: preview, newBody: strings.Replace(preview, "Not verified in the running app.", "https://www.loom.com/share/preview", 1), discussion: stackComment, newDiscussion: stackComment, wantAmbiguous: true, want: RecheckPlan{RecheckRouteEvidenceOnly, RecheckReasonEvidenceChanged}},
		{name: "Graphite dependency edit still is reassessed as evidence", body: preview, newBody: preview, discussion: stackComment, newDiscussion: stackComment + "\n* **#41**", addEvidence: true, wantAmbiguous: true, want: RecheckPlan{RecheckRouteEvidenceOnly, RecheckReasonEvidenceChanged}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, repoID, prID := uuid.New(), uuid.New(), uuid.New()
			in := AssessmentInputCaptureRequest{OrgID: orgID, RepositoryID: repoID, PullRequestID: prID, SessionID: uuid.New(), AssessmentID: uuid.New()}
			head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
			policy := models.CodeReviewPolicyRecord{ID: uuid.New(), OrgID: orgID, Version: 1, Enabled: true}
			policies := &policyStub{resolved: models.CodeReviewResolvedPolicy{Config: policy.Config(), Policy: &policy}}
			snapshots := &assessmentSnapshotFixture{snapshot: ghservice.CodeReviewPullRequestSnapshot{State: "open", Number: 42, Title: "Update view", HeadSHA: head, BaseSHA: base, BaseRef: "main"}}
			prs := &pullRequestStub{result: models.PullRequest{ID: prID, OrgID: orgID, GitHubRepo: "acme/web", GitHubPRNumber: 42}, health: models.PullRequestHealthCurrent{HeadSHA: head, BaseSHA: base, SummaryJSON: json.RawMessage(`{"checks_confirmed":true,"check_set_complete":true,"checks":[]}`)}}
			visual := &assessmentVisualFixture{snapshot: models.CodeReviewVisualEvidenceSnapshot{AssessmentID: &in.AssessmentID, Complete: true}}
			service := NewAssessmentInputCaptureService(policies, prs, &visualEvidenceRepositoryStoreStub{repository: models.Repository{ID: repoID, OrgID: orgID, FullName: "acme/web", InstallationID: 1}}, assessmentOrgFixture{models.Organization{Settings: json.RawMessage(`{}`)}}, snapshots, visual, assessmentFilesFixture{})
			service.SetExternalContextResolver(assessmentExternalFixture{})
			capture := func(body, discussion string, addEvidence bool) ReviewInputManifest {
				snapshots.snapshot.Body = body
				snapshots.textSources = nil
				if discussion != "" {
					snapshots.textSources = append(snapshots.textSources, ghservice.CodeReviewTextSource{Surface: models.CodeReviewEvidenceSurfaceIssueComment, ProviderObjectID: "10", SourceURL: "https://github.com/acme/web/pull/42#issuecomment-10", AuthorLogin: "author", Body: discussion})
				}
				if addEvidence {
					snapshots.textSources = append(snapshots.textSources, ghservice.CodeReviewTextSource{Surface: models.CodeReviewEvidenceSurfaceIssueComment, ProviderObjectID: "11", SourceURL: "https://github.com/acme/web/pull/42#issuecomment-11", AuthorLogin: "author", Body: "## Evidence\nTest output: passed\n"})
				}
				result, err := service.CaptureAssessmentInputs(context.Background(), in)
				require.NoError(t, err, "complete source inventory must remain capturable")
				require.NoError(t, ValidateReviewInputManifest(result.Manifest), "manifest must rebuild with the current intent contract")
				if discussion == opaqueDiscussion || discussion == stackComment {
					var discussionEvidence []ReviewTextEvidence
					for _, item := range result.Manifest.TextEvidence.Items {
						if item.Surface == "issue_comment" && item.ProviderObjectID == "10" {
							discussionEvidence = append(discussionEvidence, item)
						}
					}
					require.Equal(t, []ReviewTextEvidence{newReviewTextEvidence("issue_comment", "10", "https://github.com/acme/web/pull/42#issuecomment-10", "author", discussion, "full")}, discussionEvidence, "opaque discussion should retain its exact source as untrusted evidence")
				}
				return result.Manifest
			}
			before := capture(tt.body, tt.discussion, false)
			after := capture(tt.newBody, tt.newDiscussion, tt.addEvidence)
			require.True(t, before.ReuseEligible, "opaque sources must not disable a complete baseline")
			require.Equal(t, tt.wantAmbiguous, after.TextEvidence.ParseAmbiguous, "unsupported Markdown should remain visible for diagnostics")
			plan := PlanReviewRecheck(RecheckPlanInput{Current: &after, Baseline: reviewTestBaseline(before), Previous: &RecheckPrevious{Inputs: before, Completed: true, EvidenceValidated: true}})
			require.Equal(t, tt.want, plan, "all text changes should use evidence reassessment when code and policy match")
			if tt.addEvidence {
				require.Contains(t, after.TextEvidence.Items, newReviewTextEvidence("issue_comment", "11", "https://github.com/acme/web/pull/42#issuecomment-11", "author", "## Evidence\nTest output: passed\n", "evidence"), "separate evidence must retain exact citable provenance")
			}
		})
	}
}
