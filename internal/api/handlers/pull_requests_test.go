package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
)

type stubPullRequestHealthService struct {
	getHealthFunc func(context.Context, uuid.UUID, uuid.UUID) (*models.PullRequestHealthResponse, error)
	repairFunc    func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, models.PullRequestRepairActionType) (*models.PullRequestRepairResponse, error)
}

type stubPullRequestMembershipStore struct {
	getFunc func(context.Context, uuid.UUID, uuid.UUID) (models.OrganizationMembership, error)
}

func (s *stubPullRequestHealthService) GetPullRequestHealth(ctx context.Context, orgID, pullRequestID uuid.UUID) (*models.PullRequestHealthResponse, error) {
	return s.getHealthFunc(ctx, orgID, pullRequestID)
}

func (s *stubPullRequestHealthService) StartPullRequestRepair(ctx context.Context, orgID, pullRequestID, userID uuid.UUID, action models.PullRequestRepairActionType) (*models.PullRequestRepairResponse, error) {
	return s.repairFunc(ctx, orgID, pullRequestID, userID, action)
}

func (s *stubPullRequestMembershipStore) Get(ctx context.Context, userID, orgID uuid.UUID) (models.OrganizationMembership, error) {
	return s.getFunc(ctx, userID, orgID)
}

func TestPullRequestHandler_GetHealth(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()
	svc := &stubPullRequestHealthService{
		getHealthFunc: func(_ context.Context, gotOrgID, gotPRID uuid.UUID) (*models.PullRequestHealthResponse, error) {
			require.Equal(t, orgID, gotOrgID, "GetHealth should pass the active org ID to the service")
			require.Equal(t, prID, gotPRID, "GetHealth should pass the parsed pull request ID to the service")
			return &models.PullRequestHealthResponse{
				PullRequestID:       prID,
				PullRequestNumber:   184,
				Repository:          "acme/repo",
				MergeState:          models.PullRequestMergeStateConflicted,
				HasConflicts:        true,
				FailingTestCount:    2,
				CanResolveConflicts: true,
				CanFixTests:         true,
				Summary:             "PR #184 is blocked by conflicts and 2 failing test jobs.",
			}, nil
		},
	}

	handler := NewPullRequestHandler(svc)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/pull-requests/"+prID.String()+"/health", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: uuid.New(), OrgID: orgID}))
	req = req.WithContext(withURLParam(req.Context(), "id", prID.String()))
	rr := httptest.NewRecorder()

	handler.GetHealth(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "GetHealth should return 200 for a valid pull request")
	var resp models.SingleResponse[models.PullRequestHealthResponse]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "GetHealth should return valid JSON")
	require.Equal(t, prID, resp.Data.PullRequestID, "GetHealth should serialize the pull request health payload")
	require.True(t, resp.Data.CanResolveConflicts, "GetHealth should include conflict repair eligibility")
}

func TestPullRequestHandler_StartRepair(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	prID := uuid.New()
	sessionID := uuid.New()
	svc := &stubPullRequestHealthService{
		getHealthFunc: func(context.Context, uuid.UUID, uuid.UUID) (*models.PullRequestHealthResponse, error) {
			return nil, nil
		},
		repairFunc: func(_ context.Context, gotOrgID, gotPRID, gotUserID uuid.UUID, action models.PullRequestRepairActionType) (*models.PullRequestRepairResponse, error) {
			require.Equal(t, orgID, gotOrgID, "StartRepair should pass the active org ID to the service")
			require.Equal(t, prID, gotPRID, "StartRepair should pass the parsed pull request ID to the service")
			require.Equal(t, userID, gotUserID, "StartRepair should pass the current user ID to the service")
			require.Equal(t, models.PullRequestRepairActionTypeFixTests, action, "StartRepair should use the endpoint's repair action type")
			return &models.PullRequestRepairResponse{
				SessionID:        sessionID,
				Mode:             "revision",
				HealthVersion:    4,
				RepairActionType: action,
			}, nil
		},
	}

	handler := NewPullRequestHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/"+prID.String()+"/repair/fix-tests", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID}))
	req = req.WithContext(withURLParam(req.Context(), "id", prID.String()))
	rr := httptest.NewRecorder()

	handler.FixTests(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "FixTests should return 200 for a successful repair launch")
	var resp models.SingleResponse[models.PullRequestRepairResponse]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "FixTests should return valid JSON")
	require.Equal(t, sessionID, resp.Data.SessionID, "FixTests should return the launched repair session ID")
	require.Equal(t, models.PullRequestRepairActionTypeFixTests, resp.Data.RepairActionType, "FixTests should preserve the repair action type")
}

func TestPullRequestHandler_streamOrgIDFromRequest(t *testing.T) {
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
			expectedErr: errPullRequestStreamOrgInvalid,
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
			expectedErr: errPullRequestStreamOrgForbidden,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewPullRequestHandler(nil)
			if tt.setupMemberships != nil {
				handler.SetMembershipStore(tt.setupMemberships())
			}

			req := httptest.NewRequest(http.MethodGet, "/api/v1/pull-requests/stream"+tt.query, nil)
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

func withURLParam(ctx context.Context, key, value string) context.Context {
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add(key, value)
	return context.WithValue(ctx, chi.RouteCtxKey, routeCtx)
}
