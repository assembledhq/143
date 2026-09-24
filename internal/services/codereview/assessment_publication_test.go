package codereview

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type assessmentPublicationTransport func(*http.Request) (*http.Response, error)

func (f assessmentPublicationTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestReconcileAssessmentPublication(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                        string
		decision                    SubmitReviewDecision
		approvalHead, approvalState string
		includeApproval, expected   bool
	}{
		{"both receipts", SubmitReviewDecisionApproved, "head", "APPROVED", true, true},
		{"summary alone is not approval", SubmitReviewDecisionApproved, "head", "APPROVED", false, false},
		{"approval for another commit", SubmitReviewDecisionApproved, "old-head", "APPROVED", true, false},
		{"dismissed approval", SubmitReviewDecisionApproved, "head", "DISMISSED", true, false},
		{"comment needs only summary", SubmitReviewDecisionCommentOnly, "", "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			key := "assessment-output"
			requests := 0
			client := &http.Client{Transport: assessmentPublicationTransport(func(r *http.Request) (*http.Response, error) {
				requests++
				require.Equal(t, http.MethodGet, r.Method, "reconciliation must never replay an external mutation")
				body := "[]"
				if strings.HasSuffix(r.URL.Path, "/reviews") {
					reviews := []githubReviewListItem{{ID: 10, HTMLURL: "https://github.test/summary", Body: withCodeReviewOutputMarker("summary", key)}}
					if tt.includeApproval {
						reviews = append(reviews, githubReviewListItem{ID: 11, HTMLURL: "https://github.test/approval", Body: withCodeReviewOutputMarker("", key+":formal-approval"), CommitID: tt.approvalHead, State: tt.approvalState})
					}
					raw, err := json.Marshal(reviews)
					require.NoError(t, err, "fixture reviews must encode")
					body = string(raw)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}
			s := NewGitHubSubmitter(&tokenStub{token: "test"}, WithGitHubSubmitterHTTPClient(client))
			result, found, err := s.ReconcileAssessmentPublication(context.Background(), SubmitReviewRequest{InstallationID: 1, Repository: "acme/repo", PullNumber: 2, HeadSHA: "head", OutputKey: key, ExistingReviewID: 10, Decision: tt.decision, Body: "immutable assessment"})
			require.NoError(t, err, "reconciliation should read both publication identities")
			require.Equal(t, tt.expected, found, "approval must require the exact commit and confirmed formal marker")
			if found {
				require.Equal(t, int64(10), result.ID, "persistent summary ID should remain distinct from formal approval")
				if tt.decision == SubmitReviewDecisionApproved {
					require.Equal(t, int64(11), *result.FormalApprovalID, "receipt must preserve separate formal approval ID")
				}
			}
			require.Positive(t, requests, "reconciliation must check GitHub before reporting success")
		})
	}
}

func TestFormalApprovalRequiresReceiptWhenRequested(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, body  string
		expectError bool
	}{
		{"confirmed identity", `{"id":42,"html_url":"https://github.test/approval"}`, false},
		{"empty body", "", true}, {"missing id", `{}`, true}, {"truncated body", `{"id":`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			s := NewGitHubSubmitter(&tokenStub{}, WithGitHubSubmitterHTTPClient(&http.Client{Transport: assessmentPublicationTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 201, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tt.body)), Request: r}, nil
			})}))
			result, err := s.ensureFormalApprovalReceipt(context.Background(), "test", "acme", "repo", SubmitReviewRequest{RequirePublicationReceipt: true, PullNumber: 2, HeadSHA: "head"})
			if tt.expectError {
				require.Error(t, err, "ambiguous success must remain unresolved until marker reconciliation")
			} else {
				require.NoError(t, err, "confirmed identity should be retained")
				require.Equal(t, int64(42), result.ID, "formal approval receipt must include returned ID")
			}
			require.Equal(t, 1, calls, "an ambiguous response must not trigger a blind second approval")
		})
	}
}

func TestFormalApprovalMarkerMustConfirmCommitAndState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, head, state string
		wantError         bool
	}{
		{"same approved commit", "head", "APPROVED", false},
		{"different commit", "old", "APPROVED", true},
		{"dismissed approval", "head", "DISMISSED", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := NewGitHubSubmitter(&tokenStub{}, WithGitHubSubmitterHTTPClient(&http.Client{Transport: assessmentPublicationTransport(func(r *http.Request) (*http.Response, error) {
				require.Equal(t, http.MethodGet, r.Method, "an existing marker must never trigger duplicate approval")
				body, err := json.Marshal([]githubReviewListItem{{ID: 42, CommitID: tt.head, State: tt.state, Body: withCodeReviewOutputMarker("", "output:formal-approval")}})
				require.NoError(t, err, "fixture should encode")
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
			})}))
			_, err := s.ensureFormalApprovalReceipt(context.Background(), "token", "acme", "repo", SubmitReviewRequest{RequirePublicationReceipt: true, OutputKey: "output", HeadSHA: "head", PullNumber: 2})
			if tt.wantError {
				require.Error(t, err, "stale or dismissed approval cannot confirm an assessment")
			} else {
				require.NoError(t, err, "exact approved commit should reconcile")
			}
		})
	}
}
