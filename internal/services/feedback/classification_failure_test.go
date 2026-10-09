package feedback

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestProcessCommentClassificationOutcome(t *testing.T) {
	t.Parallel()
	const body = "Check the error before dereferencing the returned value."
	tests := []struct {
		name, response                                    string
		providerFailure, nilClient                        bool
		expectedStatus, expectedCategory, expectedSummary string
		expectedActionable                                bool
	}{
		{name: "provider failure preserves pending comment", providerFailure: true},
		{name: "malformed JSON preserves pending comment", response: `{"actionable":`},
		{name: "successful classification persists", response: `{"actionable":true,"category":"logic_bug","summary":"Check returned errors.","generalizable":false}`, expectedStatus: "accepted", expectedCategory: "logic_bug", expectedSummary: "Check returned errors.", expectedActionable: true},
		{name: "non-actionable result persists", response: `{"actionable":false,"category":"nit","summary":"No change required.","generalizable":false}`, expectedStatus: "filtered_not_actionable", expectedCategory: "nit", expectedSummary: "No change required."},
		{name: "intentional nil client retains conservative behavior", nilClient: true, expectedStatus: "accepted", expectedCategory: "nit", expectedSummary: body, expectedActionable: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, commentID := uuid.New(), uuid.New()
			original := models.ReviewComment{ID: commentID, OrgID: orgID, Reviewer: "human-reviewer", Body: body, FilterStatus: "pending"}
			stored := original
			writes, calls := 0, 0
			providerErr := errors.New("classification provider unavailable")
			var client LLMClient
			if !tt.nilClient {
				client = &mockLLMClient{completeFn: func(context.Context, string, string) (string, error) {
					calls++
					if tt.providerFailure {
						return "", providerErr
					}
					return tt.response, nil
				}}
			}
			comments := &mockReviewCommentStore{
				getByIDFn: func(ctx context.Context, gotOrg, gotID uuid.UUID) (models.ReviewComment, error) {
					require.Equal(t, orgID, gotOrg, "classification must load the owning tenant")
					require.Equal(t, commentID, gotID, "classification must load the requested comment")
					return stored, nil
				},
				updateClassificationFn: func(ctx context.Context, gotOrg, gotID uuid.UUID, status string, category *string, actionable, generalizable bool, rule, summary *string) error {
					require.Equal(t, orgID, gotOrg, "classification writes must retain the tenant")
					require.Equal(t, commentID, gotID, "classification writes must retain the comment identity")
					writes++
					stored.FilterStatus = status
					stored.Category = category
					stored.Actionable = actionable
					stored.Generalizable = generalizable
					stored.GeneralizedRule = rule
					stored.Summary = summary
					return nil
				},
			}
			err := newTestService(comments, &mockMemoryStore{}, &mockJobStore{}, client).ProcessComment(context.Background(), commentID, orgID)
			expected := original
			if tt.expectedStatus == "" {
				require.Error(t, err, "failed classification must reach bounded worker retries instead of job success")
				if tt.providerFailure {
					require.ErrorIs(t, err, providerErr, "provider error identity must survive classification wrapping")
				} else {
					var syntax *json.SyntaxError
					require.ErrorAs(t, err, &syntax, "malformed response must preserve its parse error")
				}
				require.Equal(t, 0, writes, "classification failure must not persist a fabricated result")
			} else {
				require.NoError(t, err, "valid or intentionally absent classification provider should preserve successful behavior")
				expected.FilterStatus = tt.expectedStatus
				expected.Category = strPtr(tt.expectedCategory)
				expected.Summary = strPtr(tt.expectedSummary)
				expected.Actionable = tt.expectedActionable
				require.Equal(t, 1, writes, "successful classification should persist once")
			}
			require.Equal(t, expected, stored, "failed comments must stay pending while successful comments retain exact classification")
			expectedCalls := 1
			if tt.nilClient {
				expectedCalls = 0
			}
			require.Equal(t, expectedCalls, calls, "only a configured classifier should be invoked")
		})
	}
}
