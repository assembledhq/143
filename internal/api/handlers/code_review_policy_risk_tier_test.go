package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

type capturedPolicyJSON struct {
	value *[]byte
}

func (c capturedPolicyJSON) Match(value any) bool {
	switch typed := value.(type) {
	case []byte:
		*c.value = append([]byte(nil), typed...)
	case string:
		*c.value = []byte(typed)
	case json.RawMessage:
		*c.value = append([]byte(nil), typed...)
	default:
		return false
	}
	return true
}

func codeReviewPolicyWriteRequest(t *testing.T, method string, orgID, userID uuid.UUID, body any) *http.Request {
	t.Helper()
	encoded, err := json.Marshal(body)
	require.NoError(t, err, "policy request should marshal")
	req := httptest.NewRequest(method, "/api/v1/code-review-policies", bytes.NewReader(encoded))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: models.RoleAdmin})
	return req.WithContext(ctx)
}

func expectCodeReviewPolicySavePrelude(t *testing.T, mock pgxmock.PgxPoolIface, orgID uuid.UUID, current models.CodeReviewPolicyConfig) {
	t.Helper()
	mock.ExpectBegin()
	mock.ExpectExec("pg_advisory_xact_lock").WithArgs("code_review_policy:" + orgID.String()).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT COALESCE").WithArgs(pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows([]string{"version"}).AddRow(1))
	expectCodeReviewResolvedPolicy(t, mock, orgID, current)
}

func expectCodeReviewPolicyInsert(t *testing.T, mock pgxmock.PgxPoolIface, orgID uuid.UUID, saved models.CodeReviewPolicyConfig, riskPolicy *[]byte) {
	t.Helper()
	description, risk, roster := marshalCodeReviewPolicyPartsForHandlerTest(t, saved)
	mock.ExpectExec("UPDATE code_review_policies").WithArgs(pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO code_review_policies").WithArgs(
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		capturedPolicyJSON{value: riskPolicy}, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
	).WillReturnRows(codeReviewPolicyRowsForHandlerTest().AddRow(uuid.New(), orgID, nil, true, 2, saved.Enabled, saved.ApprovalMode, saved.ReviewInstructions, saved.AutomatedApprovalPolicy, description, risk, roster, saved.InlineCommentLimit, nil, time.Now().UTC(), []byte("{}"), []byte("{}")))
	mock.ExpectCommit()
}

func TestCodeReviewHandler_PolicyWritesValidateRiskTierCheck(t *testing.T) {
	t.Parallel()
	enabled := &models.CodeReviewRiskTierCheckPolicy{Enabled: true, AppID: 101, AppSlug: "example-risk-tier"}
	withSetting := func(setting *models.CodeReviewRiskTierCheckPolicy) models.CodeReviewPolicyConfig {
		config := models.DefaultCodeReviewPolicyConfig()
		config.RiskPolicy.RiskTierCheck = setting
		return config
	}
	typoConfig := func(t *testing.T) map[string]any {
		raw, err := json.Marshal(models.DefaultCodeReviewPolicyConfig())
		require.NoError(t, err, "default policy should marshal")
		var config map[string]any
		require.NoError(t, json.Unmarshal(raw, &config), "default policy should decode to a map")
		config["risk_policy"].(map[string]any)["risk_tier_check"] = map[string]any{"enabled": true, "app_id": 101, "check_nam": "pr-risk"}
		return config
	}
	tests := []struct {
		name         string
		method       string
		body         func(t *testing.T) any
		expectDB     func(t *testing.T, mock pgxmock.PgxPoolIface, orgID uuid.UUID, riskPolicy *[]byte)
		expectedCode int
		expectedJSON string
	}{
		{
			name: "PUT saves an enabled setting", method: http.MethodPut,
			body: func(*testing.T) any { return map[string]any{"config": withSetting(enabled)} },
			expectDB: func(t *testing.T, mock pgxmock.PgxPoolIface, orgID uuid.UUID, riskPolicy *[]byte) {
				expectCodeReviewPolicySavePrelude(t, mock, orgID, models.DefaultCodeReviewPolicyConfig())
				expectCodeReviewPolicyInsert(t, mock, orgID, withSetting(enabled), riskPolicy)
			},
			expectedCode: http.StatusOK, expectedJSON: `{"enabled":true,"app_id":101,"app_slug":"example-risk-tier"}`,
		},
		{
			name: "PUT rejects a misspelled setting field before the database", method: http.MethodPut,
			body:         func(t *testing.T) any { return map[string]any{"config": typoConfig(t)} },
			expectDB:     func(*testing.T, pgxmock.PgxPoolIface, uuid.UUID, *[]byte) {},
			expectedCode: http.StatusBadRequest,
		},
		{
			name: "PUT rejects an enabled setting without an app id", method: http.MethodPut,
			body: func(*testing.T) any {
				return map[string]any{"config": withSetting(&models.CodeReviewRiskTierCheckPolicy{Enabled: true, AppSlug: "example-risk-tier"})}
			},
			expectDB: func(t *testing.T, mock pgxmock.PgxPoolIface, orgID uuid.UUID, _ *[]byte) {
				expectCodeReviewPolicySavePrelude(t, mock, orgID, models.DefaultCodeReviewPolicyConfig())
				mock.ExpectRollback()
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name: "PATCH saves an enabled setting", method: http.MethodPatch,
			body: func(*testing.T) any {
				return map[string]any{"config": map[string]any{"risk_policy": map[string]any{"risk_tier_check": map[string]any{"enabled": true, "app_id": 101, "app_slug": "example-risk-tier"}}}, "expected_version": 1}
			},
			expectDB: func(t *testing.T, mock pgxmock.PgxPoolIface, orgID uuid.UUID, riskPolicy *[]byte) {
				expectCodeReviewPolicySavePrelude(t, mock, orgID, models.DefaultCodeReviewPolicyConfig())
				expectCodeReviewPolicyInsert(t, mock, orgID, withSetting(enabled), riskPolicy)
			},
			expectedCode: http.StatusOK, expectedJSON: `{"enabled":true,"app_id":101,"app_slug":"example-risk-tier"}`,
		},
		{
			name: "PATCH rejects a misspelled setting field", method: http.MethodPatch,
			body: func(*testing.T) any {
				return map[string]any{"config": map[string]any{"risk_policy": map[string]any{"risk_tier_check": map[string]any{"enabled": true, "app_id": 101, "check_nam": "pr-risk"}}}, "expected_version": 1}
			},
			expectDB: func(t *testing.T, mock pgxmock.PgxPoolIface, orgID uuid.UUID, _ *[]byte) {
				expectCodeReviewPolicySavePrelude(t, mock, orgID, models.DefaultCodeReviewPolicyConfig())
				mock.ExpectRollback()
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name: "PATCH rejects an enabled setting without an app id", method: http.MethodPatch,
			body: func(*testing.T) any {
				return map[string]any{"config": map[string]any{"risk_policy": map[string]any{"risk_tier_check": map[string]any{"enabled": true}}}, "expected_version": 1}
			},
			expectDB: func(t *testing.T, mock pgxmock.PgxPoolIface, orgID uuid.UUID, _ *[]byte) {
				expectCodeReviewPolicySavePrelude(t, mock, orgID, models.DefaultCodeReviewPolicyConfig())
				mock.ExpectRollback()
			},
			expectedCode: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, userID := uuid.New(), uuid.New()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock should initialize")
			defer mock.Close()
			var riskPolicy []byte
			tt.expectDB(t, mock, orgID, &riskPolicy)
			handler := NewCodeReviewHandler(db.NewCodeReviewStore(mock), nil)
			rr := httptest.NewRecorder()
			req := codeReviewPolicyWriteRequest(t, tt.method, orgID, userID, tt.body(t))
			if tt.method == http.MethodPut {
				handler.PutPolicy(rr, req)
			} else {
				handler.PatchPolicy(rr, req)
			}
			require.Equal(t, tt.expectedCode, rr.Code, "policy writes should accept only a complete, exactly spelled risk-tier setting: %s", rr.Body.String())
			if tt.expectedCode == http.StatusBadRequest {
				var response models.ErrorResponse
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "rejection should be valid JSON")
				require.Equal(t, "CODE_REVIEW_POLICY_INVALID", response.Error.Code, "rejection should use the policy validation code")
				require.Equal(t, map[string]any{"field": models.CodeReviewPolicyFieldRiskPolicy}, response.Error.Details, "rejection should point at risk_policy")
			}
			if tt.expectedJSON != "" {
				var stored struct {
					RiskTierCheck json.RawMessage `json:"risk_tier_check"`
				}
				require.NoError(t, json.Unmarshal(riskPolicy, &stored), "stored risk policy should be JSON")
				require.JSONEq(t, tt.expectedJSON, string(stored.RiskTierCheck), "the stored risk policy should carry the exact setting")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "the database interaction should match the write path")
		})
	}
}
