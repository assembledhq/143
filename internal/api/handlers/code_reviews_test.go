package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewHandler_GetPolicyReturnsPromptFields(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	config := models.DefaultCodeReviewPolicyConfig()
	config.ReviewInstructions = "team review guidance"
	config.AutomatedApprovalPolicy = "team approval guidance"
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()
	expectCodeReviewResolvedPolicy(t, mock, orgID, nil, config)
	handler := NewCodeReviewHandler(db.NewCodeReviewStore(mock), nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/code-review-policies", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.GetPolicy(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "policy GET should succeed")
	var response models.SingleResponse[models.CodeReviewResolvedPolicy]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "policy GET response should be valid JSON")
	require.Equal(t, config.ReviewInstructions, response.Data.Config.ReviewInstructions, "policy GET should return review instructions")
	require.Equal(t, config.AutomatedApprovalPolicy, response.Data.Config.AutomatedApprovalPolicy, "policy GET should return automated approval policy")
}

func TestCodeReviewHandler_PutPolicyRejectsEmptyApprovalPolicyWithFieldDetails(t *testing.T) {
	t.Parallel()
	orgID, userID := uuid.New(), uuid.New()
	config := models.DefaultCodeReviewPolicyConfig()
	config.ApprovalMode = models.CodeReviewApprovalModeApproveAcceptable
	config.AutomatedApprovalPolicy = ""
	body, err := json.Marshal(map[string]any{"config": config})
	require.NoError(t, err, "policy request should marshal")
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()
	handler := NewCodeReviewHandler(db.NewCodeReviewStore(mock), nil)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/code-review-policies", bytes.NewReader(body))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: models.RoleAdmin})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.PutPolicy(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "empty approval policy should be rejected in approve mode")
	var response models.ErrorResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "invalid policy response should be valid JSON")
	require.Equal(t, "CODE_REVIEW_POLICY_INVALID", response.Error.Code, "invalid prompt should use policy validation code")
	require.Equal(t, map[string]any{"field": "automated_approval_policy"}, response.Error.Details, "invalid prompt should identify its field")
	require.NoError(t, mock.ExpectationsWereMet(), "invalid prompt should fail before database mutation")
}

func TestCodeReviewHandler_PutPolicyRetainsEachOmittedPromptIndependently(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		omittedField string
	}{
		{name: "retains omitted review instructions", omittedField: "review_instructions"},
		{name: "retains omitted automated approval policy", omittedField: "automated_approval_policy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, userID, policyID := uuid.New(), uuid.New(), uuid.New()
			current := models.DefaultCodeReviewPolicyConfig()
			current.ReviewInstructions = "persisted review guidance"
			current.AutomatedApprovalPolicy = "persisted approval guidance"
			requested := current
			requested.Enabled = false
			var configMap map[string]any
			rawConfig, err := json.Marshal(requested)
			require.NoError(t, err, "policy config should marshal")
			require.NoError(t, json.Unmarshal(rawConfig, &configMap), "policy config should decode to map")
			delete(configMap, tt.omittedField)
			body, err := json.Marshal(map[string]any{"config": configMap})
			require.NoError(t, err, "compatibility request should marshal")

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock should initialize")
			defer mock.Close()
			expectCodeReviewResolvedPolicy(t, mock, orgID, nil, current)
			description, risk, roster, inheritance := marshalCodeReviewPolicyPartsForHandlerTest(t, requested)
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT COALESCE").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows([]string{"version"}).AddRow(2))
			mock.ExpectExec("UPDATE code_review_policies").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			mock.ExpectQuery("INSERT INTO code_review_policies").WithArgs(
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				current.ReviewInstructions, current.AutomatedApprovalPolicy, pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			).WillReturnRows(codeReviewPolicyRowsForHandlerTest().AddRow(policyID, orgID, nil, true, 2, requested.Enabled, requested.ApprovalMode, current.ReviewInstructions, current.AutomatedApprovalPolicy, description, risk, roster, requested.InlineCommentLimit, inheritance, &userID, time.Now().UTC()))
			mock.ExpectCommit()
			handler := NewCodeReviewHandler(db.NewCodeReviewStore(mock), nil)
			req := httptest.NewRequest(http.MethodPut, "/api/v1/code-review-policies", bytes.NewReader(body))
			ctx := middleware.WithOrgID(req.Context(), orgID)
			ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: models.RoleAdmin})
			req = req.WithContext(ctx)
			rr := httptest.NewRecorder()

			handler.PutPolicy(rr, req)

			require.Equal(t, http.StatusOK, rr.Code, "older client request should remain compatible")
			require.NoError(t, mock.ExpectationsWereMet(), "compatibility update should preserve both prompt values")
		})
	}
}

func TestCodeReviewHandler_PutPolicyRejectsCrossOrganizationRepository(t *testing.T) {
	t.Parallel()
	orgID, userID, repositoryID := uuid.New(), uuid.New(), uuid.New()
	body, err := json.Marshal(map[string]any{"repository_id": repositoryID, "config": models.DefaultCodeReviewPolicyConfig()})
	require.NoError(t, err, "policy request should marshal")
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()
	mock.ExpectQuery("FROM repositories").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows([]string{"id"}))
	handler := NewCodeReviewHandler(db.NewCodeReviewStore(mock), db.NewRepositoryStore(mock))
	req := httptest.NewRequest(http.MethodPut, "/api/v1/code-review-policies", bytes.NewReader(body))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: models.RoleAdmin})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.PutPolicy(rr, req)

	require.Equal(t, http.StatusNotFound, rr.Code, "repository outside the active organization should be rejected")
	require.Contains(t, rr.Body.String(), "REPOSITORY_NOT_FOUND", "cross-organization repository should not be distinguishable from a missing repository")
	require.NoError(t, mock.ExpectationsWereMet(), "repository ownership lookup should remain organization-scoped")
}

func expectCodeReviewResolvedPolicy(t *testing.T, mock pgxmock.PgxPoolIface, orgID uuid.UUID, repositoryID *uuid.UUID, config models.CodeReviewPolicyConfig) {
	t.Helper()
	description, risk, roster, inheritance := marshalCodeReviewPolicyPartsForHandlerTest(t, config)
	mock.ExpectQuery("FROM code_review_policies").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(
		codeReviewPolicyRowsForHandlerTest().AddRow(uuid.New(), orgID, repositoryID, true, 1, config.Enabled, config.ApprovalMode, config.ReviewInstructions, config.AutomatedApprovalPolicy, description, risk, roster, config.InlineCommentLimit, inheritance, nil, time.Now().UTC()),
	)
}

func codeReviewPolicyRowsForHandlerTest() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"id", "org_id", "repository_id", "active", "version", "enabled", "approval_mode", "review_instructions", "automated_approval_policy", "description_policy", "risk_policy", "agent_roster", "inline_comment_limit", "inheritance", "created_by_user_id", "created_at"})
}

func marshalCodeReviewPolicyPartsForHandlerTest(t *testing.T, config models.CodeReviewPolicyConfig) ([]byte, []byte, []byte, []byte) {
	t.Helper()
	values := []any{config.DescriptionPolicy, config.RiskPolicy, config.AgentRoster, config.Inheritance}
	encoded := make([][]byte, len(values))
	for idx, value := range values {
		var err error
		encoded[idx], err = json.Marshal(value)
		require.NoError(t, err, "policy JSON section should marshal")
	}
	return encoded[0], encoded[1], encoded[2], encoded[3]
}

func TestCodeReviewHandler_SetupGitHubTriggerMapsMissingUserAuth(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	handler := NewCodeReviewHandler(nil, nil)
	handler.SetGitHubTriggerSetupService(codereviewsvc.NewGitHubTriggerSetupService(
		&codeReviewTriggerHandlerStoreStub{},
		&codeReviewTriggerHandlerRepoStub{repo: models.Repository{ID: repoID, OrgID: orgID, FullName: "acme/api"}},
		&codeReviewTriggerHandlerAuthStub{err: ghservice.ErrGitHubAppUserCredentialMissing},
		zerolog.Nop(),
	))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/code-review-github-trigger/setup", bytes.NewBufferString(`{"repository_id":"`+repoID.String()+`"}`))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: models.RoleAdmin})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.SetupGitHubTrigger(rr, req)

	require.Equal(t, http.StatusConflict, rr.Code, "missing GitHub user authorization should return a conflict")
	require.Contains(t, rr.Body.String(), "GITHUB_USER_AUTH_REQUIRED", "response should expose the reconnect error code")
}

func TestCodeReviewHandler_ListRejectsInvalidOutcome(t *testing.T) {
	t.Parallel()

	handler := NewCodeReviewHandler(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/code-reviews?outcome=bogus", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), uuid.New()))
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "an invalid outcome filter should return a bad request")
	require.Contains(t, rr.Body.String(), "INVALID_OUTCOME", "the response should identify the invalid outcome filter")
}

type codeReviewTriggerHandlerStoreStub struct{}

func (s *codeReviewTriggerHandlerStoreStub) GetActiveGitHubTrigger(context.Context, uuid.UUID, uuid.UUID) (models.CodeReviewGitHubTriggerSetting, error) {
	return models.CodeReviewGitHubTriggerSetting{}, pgx.ErrNoRows
}

func (s *codeReviewTriggerHandlerStoreStub) SaveGitHubTrigger(context.Context, uuid.UUID, db.SaveCodeReviewGitHubTriggerParams) (models.CodeReviewGitHubTriggerSetting, error) {
	return models.CodeReviewGitHubTriggerSetting{}, nil
}

func (s *codeReviewTriggerHandlerStoreStub) DeactivateGitHubTrigger(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID) error {
	return nil
}

type codeReviewTriggerHandlerRepoStub struct {
	repo models.Repository
}

func (s *codeReviewTriggerHandlerRepoStub) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Repository, error) {
	return s.repo, nil
}

type codeReviewTriggerHandlerAuthStub struct {
	err error
}

func (s *codeReviewTriggerHandlerAuthStub) GetValidCredential(context.Context, uuid.UUID, uuid.UUID) (*models.GitHubAppUserConfig, error) {
	return nil, s.err
}

func TestCodeReviewHandler_streamOrgIDFromRequest(t *testing.T) {
	t.Parallel()

	ctxOrgID := uuid.New()
	userID := uuid.New()
	requestedOrgID := uuid.New()

	tests := []struct {
		name             string
		query            string
		setupMemberships func() *stubPullRequestMembershipStore
		expectedOrgID    uuid.UUID
		expectedErr      error
	}{
		{
			name:          "uses active org when request does not override it",
			expectedOrgID: ctxOrgID,
		},
		{
			name:  "uses requested org when user belongs to it",
			query: "?org_id=" + requestedOrgID.String(),
			setupMemberships: func() *stubPullRequestMembershipStore {
				return &stubPullRequestMembershipStore{
					getFunc: func(_ context.Context, gotUserID, gotOrgID uuid.UUID) (models.OrganizationMembership, error) {
						require.Equal(t, userID, gotUserID, "streamOrgIDFromRequest should validate membership for the current user")
						require.Equal(t, requestedOrgID, gotOrgID, "streamOrgIDFromRequest should validate the explicitly requested org")
						return models.OrganizationMembership{UserID: gotUserID, OrgID: gotOrgID, Role: models.RoleMember}, nil
					},
				}
			},
			expectedOrgID: requestedOrgID,
		},
		{
			name:        "rejects malformed requested org IDs",
			query:       "?org_id=not-a-uuid",
			expectedErr: errCodeReviewStreamOrgInvalid,
		},
		{
			name:  "rejects requested orgs the user is not a member of",
			query: "?org_id=" + requestedOrgID.String(),
			setupMemberships: func() *stubPullRequestMembershipStore {
				return &stubPullRequestMembershipStore{
					getFunc: func(context.Context, uuid.UUID, uuid.UUID) (models.OrganizationMembership, error) {
						return models.OrganizationMembership{}, pgx.ErrNoRows
					},
				}
			},
			expectedErr: errCodeReviewStreamOrgForbidden,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewCodeReviewHandler(nil, nil)
			if tt.setupMemberships != nil {
				handler.SetMembershipStore(tt.setupMemberships())
			}

			req := httptest.NewRequest(http.MethodGet, "/api/v1/code-reviews/stream"+tt.query, nil)
			req = req.WithContext(middleware.WithOrgID(req.Context(), ctxOrgID))
			req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: ctxOrgID}))

			orgID, err := handler.streamOrgIDFromRequest(req)
			if tt.expectedErr != nil {
				require.Error(t, err, "streamOrgIDFromRequest should reject invalid explicit org selections")
				require.True(t, errors.Is(err, tt.expectedErr), "streamOrgIDFromRequest should return the expected error sentinel")
				return
			}

			require.NoError(t, err, "streamOrgIDFromRequest should resolve the stream org without error")
			require.Equal(t, tt.expectedOrgID, orgID, "streamOrgIDFromRequest should resolve the expected org ID")
		})
	}
}

func TestCodeReviewHandler_streamOrgIDFromRequest_AdditionalErrors(t *testing.T) {
	t.Parallel()

	ctxOrgID := uuid.New()
	requestedOrgID := uuid.New()

	tests := []struct {
		name           string
		withUser       bool
		membershipErr  error
		expectedErr    error
		expectedSubstr string
	}{
		{
			name:        "returns unauthorized when explicit org requested without user",
			withUser:    false,
			expectedErr: errCodeReviewStreamUnauthorized,
		},
		{
			name:           "returns config error when membership store missing",
			withUser:       true,
			expectedSubstr: "membership store not configured",
		},
		{
			name:           "returns membership lookup errors",
			withUser:       true,
			membershipErr:  errors.New("db down"),
			expectedSubstr: "db down",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewCodeReviewHandler(nil, nil)
			if tt.membershipErr != nil {
				handler.SetMembershipStore(&stubPullRequestMembershipStore{
					getFunc: func(context.Context, uuid.UUID, uuid.UUID) (models.OrganizationMembership, error) {
						return models.OrganizationMembership{}, tt.membershipErr
					},
				})
			}

			req := httptest.NewRequest(http.MethodGet, "/api/v1/code-reviews/stream?org_id="+requestedOrgID.String(), nil)
			req = req.WithContext(middleware.WithOrgID(req.Context(), ctxOrgID))
			if tt.withUser {
				req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: uuid.New(), OrgID: ctxOrgID}))
			}

			_, err := handler.streamOrgIDFromRequest(req)
			require.Error(t, err, "streamOrgIDFromRequest should fail for this scenario")
			if tt.expectedErr != nil {
				require.True(t, errors.Is(err, tt.expectedErr), "streamOrgIDFromRequest should return the expected sentinel error")
			}
			if tt.expectedSubstr != "" {
				require.Contains(t, err.Error(), tt.expectedSubstr, "streamOrgIDFromRequest should preserve the underlying error context")
			}
		})
	}
}
