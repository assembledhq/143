package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

func TestCodeReviewHandler_PolicyWritesValidatePublisherResultApplicability(t *testing.T) {
	t.Parallel()
	fallback := map[string]any{"kind": "paths", "path_patterns": []string{"**/*.tsx"}}
	requirement := func(appliesWhen map[string]any) map[string]any {
		return map[string]any{"key": "ui_evidence", "title": "Screens", "prompt": "Show it", "required": true, "evidence_kind": "visual", "applies_when": appliesWhen}
	}
	fullConfig := func(t *testing.T, appliesWhen map[string]any) map[string]any {
		raw, err := json.Marshal(models.DefaultCodeReviewPolicyConfig())
		require.NoError(t, err, "default policy should marshal")
		var config map[string]any
		require.NoError(t, json.Unmarshal(raw, &config), "default policy should decode to a map")
		config["description_policy"] = map[string]any{"requirements": []any{requirement(appliesWhen)}}
		return config
	}
	saved := models.DefaultCodeReviewPolicyConfig()
	saved.DescriptionPolicy.Requirements = []models.CodeReviewDescriptionRequirement{{Key: "ui_evidence", Title: "Screens", Prompt: "Show it", Required: true, EvidenceKind: models.CodeReviewDescriptionEvidenceKindVisual,
		AppliesWhen: models.CodeReviewDescriptionApplicability{Kind: models.CodeReviewDescriptionApplicabilityPublisher, Fallback: &models.CodeReviewDescriptionApplicability{Kind: models.CodeReviewDescriptionApplicabilityPaths, PathPatterns: []string{"**/*.tsx"}}}}}
	tests := []struct {
		name         string
		method       string
		body         func(t *testing.T) any
		rejected     bool
		expectedJSON string
	}{
		{name: "PUT saves publisher_result with a paths fallback", method: http.MethodPut,
			body: func(t *testing.T) any {
				return map[string]any{"config": fullConfig(t, map[string]any{"kind": "publisher_result", "fallback": fallback})}
			},
			expectedJSON: `{"kind":"publisher_result","fallback":{"kind":"paths","path_patterns":["**/*.tsx"]}}`},
		{name: "PUT rejects publisher_result without a fallback", method: http.MethodPut,
			body: func(t *testing.T) any {
				return map[string]any{"config": fullConfig(t, map[string]any{"kind": "publisher_result"})}
			}, rejected: true},
		{name: "PUT rejects a fallback on a paths requirement", method: http.MethodPut,
			body: func(t *testing.T) any {
				return map[string]any{"config": fullConfig(t, map[string]any{"kind": "paths", "path_patterns": []string{"web/**"}, "fallback": fallback})}
			}, rejected: true},
		{name: "PATCH saves publisher_result with a paths fallback", method: http.MethodPatch,
			body: func(*testing.T) any {
				return map[string]any{"config": map[string]any{"description_policy": map[string]any{"requirements": []any{requirement(map[string]any{"kind": "publisher_result", "fallback": fallback})}}}, "expected_version": 1}
			},
			expectedJSON: `{"kind":"publisher_result","fallback":{"kind":"paths","path_patterns":["**/*.tsx"]}}`},
		{name: "PATCH rejects publisher_result with a nontrivial fallback", method: http.MethodPatch,
			body: func(*testing.T) any {
				return map[string]any{"config": map[string]any{"description_policy": map[string]any{"requirements": []any{requirement(map[string]any{"kind": "publisher_result", "fallback": map[string]any{"kind": "nontrivial", "min_files_changed": 2}})}}}, "expected_version": 1}
			}, rejected: true},
		{name: "PATCH rejects path patterns outside the fallback", method: http.MethodPatch,
			body: func(*testing.T) any {
				return map[string]any{"config": map[string]any{"description_policy": map[string]any{"requirements": []any{requirement(map[string]any{"kind": "publisher_result", "path_patterns": []string{"web/**"}, "fallback": fallback})}}}, "expected_version": 1}
			}, rejected: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, userID := uuid.New(), uuid.New()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock should initialize")
			defer mock.Close()
			expectCodeReviewPolicySavePrelude(t, mock, orgID, models.DefaultCodeReviewPolicyConfig())
			var descriptionPolicy []byte
			if tt.rejected {
				mock.ExpectRollback()
			} else {
				description, risk, roster := marshalCodeReviewPolicyPartsForHandlerTest(t, saved)
				mock.ExpectExec("UPDATE code_review_policies").WithArgs(pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectQuery("INSERT INTO code_review_policies").WithArgs(
					pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
					capturedPolicyJSON{value: &descriptionPolicy}, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				).WillReturnRows(codeReviewPolicyRowsForHandlerTest().AddRow(uuid.New(), orgID, nil, true, 2, saved.Enabled, saved.ApprovalMode, saved.ReviewInstructions, saved.AutomatedApprovalPolicy, description, risk, roster, saved.InlineCommentLimit, nil, time.Now().UTC(), []byte("{}"), []byte("{}")))
				mock.ExpectCommit()
			}
			handler := NewCodeReviewHandler(db.NewCodeReviewStore(mock), nil)
			rr := httptest.NewRecorder()
			req := codeReviewPolicyWriteRequest(t, tt.method, orgID, userID, tt.body(t))
			if tt.method == http.MethodPut {
				handler.PutPolicy(rr, req)
			} else {
				handler.PatchPolicy(rr, req)
			}
			if tt.rejected {
				require.Equal(t, http.StatusBadRequest, rr.Code, "invalid publisher applicability should be rejected: %s", rr.Body.String())
				var response models.ErrorResponse
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "rejection should be valid JSON")
				require.Equal(t, "CODE_REVIEW_POLICY_INVALID", response.Error.Code, "rejection should use the policy validation code")
				require.Equal(t, map[string]any{"field": models.CodeReviewPolicyFieldDescriptionPolicy}, response.Error.Details, "rejection should point at description_policy")
			} else {
				require.Equal(t, http.StatusOK, rr.Code, "valid publisher applicability should save: %s", rr.Body.String())
				var stored models.CodeReviewDescriptionPolicy
				require.NoError(t, json.Unmarshal(descriptionPolicy, &stored), "stored description policy should be JSON")
				require.Len(t, stored.Requirements, 1, "the stored policy should keep the supplied requirement")
				encoded, err := json.Marshal(stored.Requirements[0].AppliesWhen)
				require.NoError(t, err, "stored applicability should encode")
				require.JSONEq(t, tt.expectedJSON, string(encoded), "the stored applicability should keep the publisher kind and its fallback")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "the database interaction should match the write path")
		})
	}
}
