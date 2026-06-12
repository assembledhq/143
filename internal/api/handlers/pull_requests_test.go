package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
)

type lockedRecorder struct {
	*httptest.ResponseRecorder
	mu sync.Mutex
}

func newLockedRecorder() *lockedRecorder {
	return &lockedRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (r *lockedRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ResponseRecorder.Write(b)
}

func (r *lockedRecorder) BodyString() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ResponseRecorder.Body.String()
}

type stubPullRequestHealthService struct {
	getHealthFunc            func(context.Context, uuid.UUID, uuid.UUID) (*models.PullRequestHealthResponse, error)
	repairFunc               func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, ghservice.StartPullRequestRepairOptions) (*models.PullRequestRepairResponse, error)
	mergeFunc                func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*models.PullRequestMergeResponse, error)
	queueMergeWhenReadyFunc  func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*models.PullRequestMergeWhenReadyStatus, error)
	cancelMergeWhenReadyFunc func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*models.PullRequestMergeWhenReadyStatus, error)
}

type stubPullRequestMembershipStore struct {
	getFunc func(context.Context, uuid.UUID, uuid.UUID) (models.OrganizationMembership, error)
}

func (s *stubPullRequestHealthService) GetPullRequestHealth(ctx context.Context, orgID, pullRequestID uuid.UUID) (*models.PullRequestHealthResponse, error) {
	return s.getHealthFunc(ctx, orgID, pullRequestID)
}

func (s *stubPullRequestHealthService) StartPullRequestRepair(ctx context.Context, orgID, pullRequestID, userID uuid.UUID, opts ghservice.StartPullRequestRepairOptions) (*models.PullRequestRepairResponse, error) {
	return s.repairFunc(ctx, orgID, pullRequestID, userID, opts)
}

func (s *stubPullRequestHealthService) MergePullRequest(ctx context.Context, orgID, pullRequestID, userID uuid.UUID) (*models.PullRequestMergeResponse, error) {
	if s.mergeFunc == nil {
		return nil, errors.New("merge not stubbed")
	}
	return s.mergeFunc(ctx, orgID, pullRequestID, userID)
}

func (s *stubPullRequestHealthService) QueueMergeWhenReady(ctx context.Context, orgID, pullRequestID, userID uuid.UUID) (*models.PullRequestMergeWhenReadyStatus, error) {
	if s.queueMergeWhenReadyFunc == nil {
		return nil, errors.New("queue merge when ready not stubbed")
	}
	return s.queueMergeWhenReadyFunc(ctx, orgID, pullRequestID, userID)
}

func (s *stubPullRequestHealthService) CancelMergeWhenReady(ctx context.Context, orgID, pullRequestID, userID uuid.UUID) (*models.PullRequestMergeWhenReadyStatus, error) {
	if s.cancelMergeWhenReadyFunc == nil {
		return nil, errors.New("cancel merge when ready not stubbed")
	}
	return s.cancelMergeWhenReadyFunc(ctx, orgID, pullRequestID, userID)
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

func TestPullRequestHandler_GetHealth_Errors(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	prID := uuid.New()

	tests := []struct {
		name           string
		handler        *PullRequestHandler
		requestPathID  string
		expectedCode   int
		expectedSubstr string
	}{
		{
			name:           "returns not implemented when service missing",
			handler:        NewPullRequestHandler(nil),
			requestPathID:  prID.String(),
			expectedCode:   http.StatusNotImplemented,
			expectedSubstr: "NOT_CONFIGURED",
		},
		{
			name: "returns bad request for invalid pull request id",
			handler: NewPullRequestHandler(&stubPullRequestHealthService{
				getHealthFunc: func(context.Context, uuid.UUID, uuid.UUID) (*models.PullRequestHealthResponse, error) {
					return nil, nil
				},
			}),
			requestPathID:  "not-a-uuid",
			expectedCode:   http.StatusBadRequest,
			expectedSubstr: "INVALID_ID",
		},
		{
			name: "returns internal server error when service fails",
			handler: NewPullRequestHandler(&stubPullRequestHealthService{
				getHealthFunc: func(context.Context, uuid.UUID, uuid.UUID) (*models.PullRequestHealthResponse, error) {
					return nil, errors.New("boom")
				},
			}),
			requestPathID:  prID.String(),
			expectedCode:   http.StatusInternalServerError,
			expectedSubstr: "PULL_REQUEST_HEALTH_FAILED",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "/api/v1/pull-requests/"+tt.requestPathID+"/health", nil)
			req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
			req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: uuid.New(), OrgID: orgID}))
			req = req.WithContext(withURLParam(req.Context(), "id", tt.requestPathID))
			rr := httptest.NewRecorder()

			tt.handler.GetHealth(rr, req)

			require.Equal(t, tt.expectedCode, rr.Code, "GetHealth should return the expected error code")
			require.Contains(t, rr.Body.String(), tt.expectedSubstr, "GetHealth should encode the expected error payload")
		})
	}
}

func TestPullRequestHandler_StartRepair(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	prID := uuid.New()
	threadID := uuid.New()
	sessionID := uuid.New()
	svc := &stubPullRequestHealthService{
		getHealthFunc: func(context.Context, uuid.UUID, uuid.UUID) (*models.PullRequestHealthResponse, error) {
			return nil, nil
		},
		repairFunc: func(_ context.Context, gotOrgID, gotPRID, gotUserID uuid.UUID, opts ghservice.StartPullRequestRepairOptions) (*models.PullRequestRepairResponse, error) {
			require.Equal(t, orgID, gotOrgID, "StartRepair should pass the active org ID to the service")
			require.Equal(t, prID, gotPRID, "StartRepair should pass the parsed pull request ID to the service")
			require.Equal(t, userID, gotUserID, "StartRepair should pass the current user ID to the service")
			require.Equal(t, models.PullRequestRepairActionTypeFixTests, opts.Action, "StartRepair should use the endpoint's repair action type")
			require.Equal(t, &threadID, opts.ThreadID, "StartRepair should pass the requested thread ID to the service")
			require.NotNil(t, opts.PushChanges, "StartRepair should pass an explicit push preference to the service")
			require.True(t, *opts.PushChanges, "StartRepair should default repair launches to pushing changes")
			return &models.PullRequestRepairResponse{
				SessionID:        sessionID,
				ThreadID:         &threadID,
				Mode:             "reconstructed",
				HealthVersion:    4,
				RepairActionType: opts.Action,
			}, nil
		},
	}

	handler := NewPullRequestHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/"+prID.String()+"/repair/fix-tests", strings.NewReader(`{"thread_id":"`+threadID.String()+`"}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID}))
	req = req.WithContext(withURLParam(req.Context(), "id", prID.String()))
	rr := httptest.NewRecorder()

	handler.FixTests(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "FixTests should return 200 for a successful repair launch")
	var resp models.SingleResponse[models.PullRequestRepairResponse]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "FixTests should return valid JSON")
	require.Equal(t, sessionID, resp.Data.SessionID, "FixTests should return the launched repair session ID")
	require.Equal(t, &threadID, resp.Data.ThreadID, "FixTests should return the selected repair thread ID")
	require.Equal(t, models.PullRequestRepairActionTypeFixTests, resp.Data.RepairActionType, "FixTests should preserve the repair action type")
}

func TestPullRequestHandler_StartRepairWithoutPushingChanges(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	prID := uuid.New()
	svc := &stubPullRequestHealthService{
		getHealthFunc: func(context.Context, uuid.UUID, uuid.UUID) (*models.PullRequestHealthResponse, error) {
			return nil, nil
		},
		repairFunc: func(_ context.Context, gotOrgID, gotPRID, gotUserID uuid.UUID, opts ghservice.StartPullRequestRepairOptions) (*models.PullRequestRepairResponse, error) {
			require.Equal(t, orgID, gotOrgID, "StartRepair should pass the active org ID to the service")
			require.Equal(t, prID, gotPRID, "StartRepair should pass the parsed pull request ID to the service")
			require.Equal(t, userID, gotUserID, "StartRepair should pass the current user ID to the service")
			require.Equal(t, models.PullRequestRepairActionTypeFixTests, opts.Action, "StartRepair should preserve the repair action type")
			require.NotNil(t, opts.PushChanges, "StartRepair should pass an explicit push preference to the service")
			require.False(t, *opts.PushChanges, "StartRepair should pass the no-push dropdown option to the service")
			return &models.PullRequestRepairResponse{
				SessionID:        uuid.New(),
				Mode:             "reconstructed",
				HealthVersion:    4,
				RepairActionType: opts.Action,
			}, nil
		},
	}

	handler := NewPullRequestHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/"+prID.String()+"/repair/fix-tests", strings.NewReader(`{"push_changes":false}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID}))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", prID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handler.FixTests(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "FixTests should return 200 for a successful no-push repair launch")
}

func TestPullRequestHandler_ResolveConflictsWithoutPushingChanges(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	prID := uuid.New()
	svc := &stubPullRequestHealthService{
		getHealthFunc: func(context.Context, uuid.UUID, uuid.UUID) (*models.PullRequestHealthResponse, error) {
			return nil, nil
		},
		repairFunc: func(_ context.Context, gotOrgID, gotPRID, gotUserID uuid.UUID, opts ghservice.StartPullRequestRepairOptions) (*models.PullRequestRepairResponse, error) {
			require.Equal(t, orgID, gotOrgID, "ResolveConflicts should pass the active org ID to the service")
			require.Equal(t, prID, gotPRID, "ResolveConflicts should pass the parsed pull request ID to the service")
			require.Equal(t, userID, gotUserID, "ResolveConflicts should pass the current user ID to the service")
			require.Equal(t, models.PullRequestRepairActionTypeResolveConflicts, opts.Action, "ResolveConflicts should preserve the repair action type")
			require.NotNil(t, opts.PushChanges, "ResolveConflicts should pass an explicit push preference to the service")
			require.False(t, *opts.PushChanges, "ResolveConflicts should pass the no-push dropdown option to the service")
			return &models.PullRequestRepairResponse{
				SessionID:        uuid.New(),
				Mode:             "reconstructed",
				HealthVersion:    2,
				RepairActionType: opts.Action,
			}, nil
		},
	}

	handler := NewPullRequestHandler(svc)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/"+prID.String()+"/repair/resolve-conflicts", strings.NewReader(`{"push_changes":false}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID}))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", prID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handler.ResolveConflicts(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "ResolveConflicts should return 200 for a successful no-push repair launch")
}

func TestPullRequestHandler_StartRepair_ErrorsAndResolveConflicts(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	prID := uuid.New()

	tests := []struct {
		name           string
		handler        *PullRequestHandler
		pathID         string
		body           string
		withUser       bool
		expectedCode   int
		expectedSubstr string
	}{
		{
			name:           "returns not implemented when repair service missing",
			handler:        NewPullRequestHandler(nil),
			pathID:         prID.String(),
			withUser:       true,
			expectedCode:   http.StatusNotImplemented,
			expectedSubstr: "NOT_CONFIGURED",
		},
		{
			name: "returns unauthorized when user missing",
			handler: NewPullRequestHandler(&stubPullRequestHealthService{
				repairFunc: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, ghservice.StartPullRequestRepairOptions) (*models.PullRequestRepairResponse, error) {
					return nil, nil
				},
			}),
			pathID:         prID.String(),
			withUser:       false,
			expectedCode:   http.StatusUnauthorized,
			expectedSubstr: "UNAUTHORIZED",
		},
		{
			name: "returns bad request for invalid pull request id",
			handler: NewPullRequestHandler(&stubPullRequestHealthService{
				repairFunc: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, ghservice.StartPullRequestRepairOptions) (*models.PullRequestRepairResponse, error) {
					return nil, nil
				},
			}),
			pathID:         "not-a-uuid",
			withUser:       true,
			expectedCode:   http.StatusBadRequest,
			expectedSubstr: "INVALID_ID",
		},
		{
			name: "returns bad request for invalid repair thread id",
			handler: NewPullRequestHandler(&stubPullRequestHealthService{
				repairFunc: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, ghservice.StartPullRequestRepairOptions) (*models.PullRequestRepairResponse, error) {
					return nil, nil
				},
			}),
			pathID:         prID.String(),
			body:           `{"thread_id":"not-a-uuid"}`,
			withUser:       true,
			expectedCode:   http.StatusBadRequest,
			expectedSubstr: "INVALID_THREAD_ID",
		},
		{
			name: "returns bad request when thread does not belong to the canonical session",
			handler: NewPullRequestHandler(&stubPullRequestHealthService{
				repairFunc: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, ghservice.StartPullRequestRepairOptions) (*models.PullRequestRepairResponse, error) {
					return nil, ghservice.ErrRepairThreadNotFound
				},
			}),
			pathID:         prID.String(),
			body:           `{"thread_id":"` + uuid.New().String() + `"}`,
			withUser:       true,
			expectedCode:   http.StatusBadRequest,
			expectedSubstr: "INVALID_THREAD_ID",
		},
		{
			name: "returns internal server error when repair launch fails",
			handler: NewPullRequestHandler(&stubPullRequestHealthService{
				repairFunc: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, ghservice.StartPullRequestRepairOptions) (*models.PullRequestRepairResponse, error) {
					return nil, errors.New("boom")
				},
			}),
			pathID:         prID.String(),
			withUser:       true,
			expectedCode:   http.StatusInternalServerError,
			expectedSubstr: "PULL_REQUEST_REPAIR_FAILED",
		},
		{
			name: "returns conflict when a repair session is already in progress",
			handler: NewPullRequestHandler(&stubPullRequestHealthService{
				repairFunc: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, ghservice.StartPullRequestRepairOptions) (*models.PullRequestRepairResponse, error) {
					return nil, ghservice.ErrRepairAlreadyInProgress
				},
			}),
			pathID:         prID.String(),
			withUser:       true,
			expectedCode:   http.StatusConflict,
			expectedSubstr: "REPAIR_ALREADY_IN_PROGRESS",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/"+tt.pathID+"/repair/fix-tests", strings.NewReader(tt.body))
			req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
			if tt.withUser {
				req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID}))
			}
			req = req.WithContext(withURLParam(req.Context(), "id", tt.pathID))
			rr := httptest.NewRecorder()

			tt.handler.FixTests(rr, req)

			require.Equal(t, tt.expectedCode, rr.Code, "FixTests should return the expected error code")
			require.Contains(t, rr.Body.String(), tt.expectedSubstr, "FixTests should encode the expected error payload")
		})
	}

	resolveHandler := NewPullRequestHandler(&stubPullRequestHealthService{
		repairFunc: func(_ context.Context, gotOrgID, gotPRID, gotUserID uuid.UUID, opts ghservice.StartPullRequestRepairOptions) (*models.PullRequestRepairResponse, error) {
			require.Equal(t, orgID, gotOrgID, "ResolveConflicts should pass the active org ID to the service")
			require.Equal(t, prID, gotPRID, "ResolveConflicts should pass the parsed pull request ID to the service")
			require.Equal(t, userID, gotUserID, "ResolveConflicts should pass the current user ID to the service")
			require.Equal(t, models.PullRequestRepairActionTypeResolveConflicts, opts.Action, "ResolveConflicts should use the resolve_conflicts action")
			return &models.PullRequestRepairResponse{
				SessionID:        uuid.New(),
				Mode:             "existing",
				RepairActionType: opts.Action,
			}, nil
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/"+prID.String()+"/repair/resolve-conflicts", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID}))
	req = req.WithContext(withURLParam(req.Context(), "id", prID.String()))
	rr := httptest.NewRecorder()

	resolveHandler.ResolveConflicts(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "ResolveConflicts should return 200 when the repair launch succeeds")
	require.Contains(t, rr.Body.String(), "resolve_conflicts", "ResolveConflicts should serialize the selected repair action")
}

func TestPullRequestHandler_Merge(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	prID := uuid.New()

	t.Run("returns 200 with merge response on success", func(t *testing.T) {
		t.Parallel()

		svc := &stubPullRequestHealthService{
			mergeFunc: func(_ context.Context, gotOrgID, gotPRID, gotUserID uuid.UUID) (*models.PullRequestMergeResponse, error) {
				require.Equal(t, orgID, gotOrgID, "Merge should pass the active org ID to the service")
				require.Equal(t, prID, gotPRID, "Merge should pass the parsed pull request ID to the service")
				require.Equal(t, userID, gotUserID, "Merge should pass the current user ID to the service")
				return &models.PullRequestMergeResponse{
					Merged:      true,
					SHA:         "merge-sha",
					Message:     "Pull Request successfully merged",
					MergeMethod: models.PullRequestMergeMethodSquash,
				}, nil
			},
		}

		handler := NewPullRequestHandler(svc)
		req := mergeRequest(prID.String(), &models.User{ID: userID, OrgID: orgID}, orgID)
		rr := httptest.NewRecorder()
		handler.Merge(rr, req)

		require.Equal(t, http.StatusOK, rr.Code, "Merge should return 200 on success")
		var resp models.SingleResponse[models.PullRequestMergeResponse]
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "Merge should return valid JSON")
		require.True(t, resp.Data.Merged, "Merge response should expose merged=true")
		require.Equal(t, "merge-sha", resp.Data.SHA, "Merge response should expose the resulting SHA")
		require.Equal(t, models.PullRequestMergeMethodSquash, resp.Data.MergeMethod, "Merge response should expose the chosen method")
	})

	t.Run("returns 409 PR_NOT_MERGEABLE when service rejects", func(t *testing.T) {
		t.Parallel()

		svc := &stubPullRequestHealthService{
			mergeFunc: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*models.PullRequestMergeResponse, error) {
				return nil, ghservice.ErrPullRequestNotMergeable
			},
		}

		handler := NewPullRequestHandler(svc)
		req := mergeRequest(prID.String(), &models.User{ID: userID, OrgID: orgID}, orgID)
		rr := httptest.NewRecorder()
		handler.Merge(rr, req)

		require.Equal(t, http.StatusConflict, rr.Code, "Merge should return 409 when the PR is not mergeable")
		require.Contains(t, rr.Body.String(), "PR_NOT_MERGEABLE", "Merge should return the PR_NOT_MERGEABLE error code")
	})

	t.Run("returns 503 PR_MERGE_STATE_UNAVAILABLE when refresh fails", func(t *testing.T) {
		t.Parallel()

		svc := &stubPullRequestHealthService{
			mergeFunc: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*models.PullRequestMergeResponse, error) {
				return nil, ghservice.ErrMergeStateRefreshFailed
			},
		}

		handler := NewPullRequestHandler(svc)
		req := mergeRequest(prID.String(), &models.User{ID: userID, OrgID: orgID}, orgID)
		rr := httptest.NewRecorder()
		handler.Merge(rr, req)

		require.Equal(t, http.StatusServiceUnavailable, rr.Code, "Merge should return 503 when GitHub refresh fails")
		require.Contains(t, rr.Body.String(), "PR_MERGE_STATE_UNAVAILABLE")
	})

	t.Run("returns 409 NO_MERGE_METHOD_ALLOWED", func(t *testing.T) {
		t.Parallel()

		svc := &stubPullRequestHealthService{
			mergeFunc: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*models.PullRequestMergeResponse, error) {
				return nil, ghservice.ErrNoMergeMethodAllowed
			},
		}

		handler := NewPullRequestHandler(svc)
		req := mergeRequest(prID.String(), &models.User{ID: userID, OrgID: orgID}, orgID)
		rr := httptest.NewRecorder()
		handler.Merge(rr, req)

		require.Equal(t, http.StatusConflict, rr.Code, "Merge should return 409 when no merge method is allowed")
		require.Contains(t, rr.Body.String(), "NO_MERGE_METHOD_ALLOWED")
	})

	t.Run("returns 409 when GitHub user auth is required", func(t *testing.T) {
		t.Parallel()

		svc := &stubPullRequestHealthService{
			mergeFunc: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*models.PullRequestMergeResponse, error) {
				return nil, ghservice.ErrGitHubUserAuthRequired
			},
		}

		handler := NewPullRequestHandler(svc)
		req := mergeRequest(prID.String(), &models.User{ID: userID, OrgID: orgID}, orgID)
		rr := httptest.NewRecorder()
		handler.Merge(rr, req)

		require.Equal(t, http.StatusConflict, rr.Code, "Merge should return 409 when GitHub user auth is required")
		require.Contains(t, rr.Body.String(), "GITHUB_USER_AUTH_REQUIRED", "Merge should return the user-auth-required code")
	})

	t.Run("returns 409 when GitHub user auth cannot access the repo", func(t *testing.T) {
		t.Parallel()

		svc := &stubPullRequestHealthService{
			mergeFunc: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*models.PullRequestMergeResponse, error) {
				return nil, ghservice.ErrGitHubUserAuthRepoAccessDenied
			},
		}

		handler := NewPullRequestHandler(svc)
		req := mergeRequest(prID.String(), &models.User{ID: userID, OrgID: orgID}, orgID)
		rr := httptest.NewRecorder()
		handler.Merge(rr, req)

		require.Equal(t, http.StatusConflict, rr.Code, "Merge should return 409 when the user's GitHub account lacks repo access")
		require.Contains(t, rr.Body.String(), "GITHUB_USER_AUTH_REPO_ACCESS_DENIED", "Merge should return the repo-access-denied code")
	})

	t.Run("returns 502 when GitHub reports merged=false", func(t *testing.T) {
		t.Parallel()

		svc := &stubPullRequestHealthService{
			mergeFunc: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*models.PullRequestMergeResponse, error) {
				return nil, ghservice.ErrGitHubMergeIncomplete
			},
		}

		handler := NewPullRequestHandler(svc)
		req := mergeRequest(prID.String(), &models.User{ID: userID, OrgID: orgID}, orgID)
		rr := httptest.NewRecorder()
		handler.Merge(rr, req)

		require.Equal(t, http.StatusBadGateway, rr.Code, "Merge should return 502 when GitHub responds 200 but merged=false")
		require.Contains(t, rr.Body.String(), "PULL_REQUEST_MERGE_INCOMPLETE", "Merge should return the merge-incomplete code")
	})

	t.Run("returns 500 on unknown failures", func(t *testing.T) {
		t.Parallel()

		svc := &stubPullRequestHealthService{
			mergeFunc: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*models.PullRequestMergeResponse, error) {
				return nil, errors.New("boom")
			},
		}

		handler := NewPullRequestHandler(svc)
		req := mergeRequest(prID.String(), &models.User{ID: userID, OrgID: orgID}, orgID)
		rr := httptest.NewRecorder()
		handler.Merge(rr, req)

		require.Equal(t, http.StatusInternalServerError, rr.Code, "Merge should return 500 on unknown failures")
		require.Contains(t, rr.Body.String(), "PULL_REQUEST_MERGE_FAILED")
	})

	for _, tt := range []struct {
		name           string
		err            error
		expectedCode   int
		expectedBody   string
		expectedErrMsg string
	}{
		{
			name: "returns 409 when GitHub disallows the merge method",
			err: &ghservice.GitHubAPIError{
				StatusCode: http.StatusMethodNotAllowed,
				Body:       []byte(`{"message":"Merge commits are disabled for this repository."}`),
			},
			expectedCode:   http.StatusConflict,
			expectedBody:   "PULL_REQUEST_MERGE_REJECTED",
			expectedErrMsg: "Merge commits are disabled for this repository.",
		},
		{
			name: "returns 409 when GitHub reports a merge conflict",
			err: &ghservice.GitHubAPIError{
				StatusCode: http.StatusConflict,
				Body:       []byte(`{"message":"Head branch was modified."}`),
			},
			expectedCode:   http.StatusConflict,
			expectedBody:   "PULL_REQUEST_MERGE_REJECTED",
			expectedErrMsg: "Head branch was modified.",
		},
		{
			name: "returns 422 when GitHub rejects the merge payload",
			err: &ghservice.GitHubAPIError{
				StatusCode: http.StatusUnprocessableEntity,
				Body:       []byte(`{"message":"Validation Failed"}`),
			},
			expectedCode:   http.StatusUnprocessableEntity,
			expectedBody:   "PULL_REQUEST_MERGE_REJECTED",
			expectedErrMsg: "Validation Failed",
		},
		{
			name: "returns 500 for unclassified GitHub API errors",
			err: &ghservice.GitHubAPIError{
				StatusCode: http.StatusInternalServerError,
				Body:       []byte(`{"message":"Upstream failed"}`),
			},
			expectedCode:   http.StatusInternalServerError,
			expectedBody:   "PULL_REQUEST_MERGE_FAILED",
			expectedErrMsg: "",
		},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := &stubPullRequestHealthService{
				mergeFunc: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*models.PullRequestMergeResponse, error) {
					return nil, tt.err
				},
			}

			handler := NewPullRequestHandler(svc)
			req := mergeRequest(prID.String(), &models.User{ID: userID, OrgID: orgID}, orgID)
			rr := httptest.NewRecorder()
			handler.Merge(rr, req)

			require.Equal(t, tt.expectedCode, rr.Code, "Merge should return the expected status for classified GitHub API errors")
			require.Contains(t, rr.Body.String(), tt.expectedBody, "Merge should serialize the expected error code")
			if tt.expectedErrMsg != "" {
				require.Contains(t, rr.Body.String(), tt.expectedErrMsg, "Merge should bubble up GitHub's actionable error message")
			}
		})
	}

	t.Run("returns 401 when user missing", func(t *testing.T) {
		t.Parallel()

		handler := NewPullRequestHandler(&stubPullRequestHealthService{})
		req := mergeRequest(prID.String(), nil, orgID)
		rr := httptest.NewRecorder()
		handler.Merge(rr, req)

		require.Equal(t, http.StatusUnauthorized, rr.Code, "Merge should return 401 when user is missing")
	})

	t.Run("returns 400 for invalid PR id", func(t *testing.T) {
		t.Parallel()

		handler := NewPullRequestHandler(&stubPullRequestHealthService{})
		req := mergeRequest("not-a-uuid", &models.User{ID: userID, OrgID: orgID}, orgID)
		rr := httptest.NewRecorder()
		handler.Merge(rr, req)

		require.Equal(t, http.StatusBadRequest, rr.Code, "Merge should return 400 for malformed PR id")
	})

	t.Run("returns 501 when service unavailable", func(t *testing.T) {
		t.Parallel()

		handler := NewPullRequestHandler(nil)
		req := mergeRequest(prID.String(), &models.User{ID: userID, OrgID: orgID}, orgID)
		rr := httptest.NewRecorder()
		handler.Merge(rr, req)

		require.Equal(t, http.StatusNotImplemented, rr.Code, "Merge should return 501 when the service is unconfigured")
	})
}

func TestPullRequestHandler_MergeWhenReady(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	prID := uuid.New()
	now := time.Now().UTC()
	healthVersion := int64(7)

	t.Run("queues merge when ready", func(t *testing.T) {
		t.Parallel()

		svc := &stubPullRequestHealthService{
			queueMergeWhenReadyFunc: func(_ context.Context, gotOrgID, gotPRID, gotUserID uuid.UUID) (*models.PullRequestMergeWhenReadyStatus, error) {
				require.Equal(t, orgID, gotOrgID, "QueueMergeWhenReady should pass the active org ID")
				require.Equal(t, prID, gotPRID, "QueueMergeWhenReady should pass the parsed pull request ID")
				require.Equal(t, userID, gotUserID, "QueueMergeWhenReady should pass the current user ID")
				return &models.PullRequestMergeWhenReadyStatus{
					State:                  models.PullRequestMergeWhenReadyStateQueued,
					RequestedByUserID:      &userID,
					RequestedAt:            &now,
					RequestedHeadSHA:       "head",
					RequestedHealthVersion: &healthVersion,
				}, nil
			},
		}

		handler := NewPullRequestHandler(svc)
		req := mergeWhenReadyRequest(http.MethodPost, prID.String(), &models.User{ID: userID, OrgID: orgID}, orgID)
		rr := httptest.NewRecorder()
		handler.QueueMergeWhenReady(rr, req)

		require.Equal(t, http.StatusOK, rr.Code, "QueueMergeWhenReady should return 200 on success")
		var resp models.SingleResponse[models.PullRequestMergeWhenReadyStatus]
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "QueueMergeWhenReady should return valid JSON")
		require.Equal(t, models.PullRequestMergeWhenReadyStateQueued, resp.Data.State, "QueueMergeWhenReady should serialize the queued state")
		require.Equal(t, "head", resp.Data.RequestedHeadSHA, "QueueMergeWhenReady should serialize the requested head")
	})

	t.Run("cancels merge when ready", func(t *testing.T) {
		t.Parallel()

		svc := &stubPullRequestHealthService{
			cancelMergeWhenReadyFunc: func(_ context.Context, gotOrgID, gotPRID, gotUserID uuid.UUID) (*models.PullRequestMergeWhenReadyStatus, error) {
				require.Equal(t, orgID, gotOrgID, "CancelMergeWhenReady should pass the active org ID")
				require.Equal(t, prID, gotPRID, "CancelMergeWhenReady should pass the parsed pull request ID")
				require.Equal(t, userID, gotUserID, "CancelMergeWhenReady should pass the current user ID")
				return &models.PullRequestMergeWhenReadyStatus{
					State:                  models.PullRequestMergeWhenReadyStateCancelled,
					RequestedByUserID:      &userID,
					RequestedAt:            &now,
					RequestedHeadSHA:       "head",
					RequestedHealthVersion: &healthVersion,
				}, nil
			},
		}

		handler := NewPullRequestHandler(svc)
		req := mergeWhenReadyRequest(http.MethodDelete, prID.String(), &models.User{ID: userID, OrgID: orgID}, orgID)
		rr := httptest.NewRecorder()
		handler.CancelMergeWhenReady(rr, req)

		require.Equal(t, http.StatusOK, rr.Code, "CancelMergeWhenReady should return 200 on success")
		var resp models.SingleResponse[models.PullRequestMergeWhenReadyStatus]
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "CancelMergeWhenReady should return valid JSON")
		require.Equal(t, models.PullRequestMergeWhenReadyStateCancelled, resp.Data.State, "CancelMergeWhenReady should serialize the cancelled state")
	})

	t.Run("requeue after cancellation is delegated to service", func(t *testing.T) {
		t.Parallel()

		called := false
		svc := &stubPullRequestHealthService{
			queueMergeWhenReadyFunc: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*models.PullRequestMergeWhenReadyStatus, error) {
				called = true
				return &models.PullRequestMergeWhenReadyStatus{State: models.PullRequestMergeWhenReadyStateQueued}, nil
			},
		}

		handler := NewPullRequestHandler(svc)
		req := mergeWhenReadyRequest(http.MethodPost, prID.String(), &models.User{ID: userID, OrgID: orgID}, orgID)
		rr := httptest.NewRecorder()
		handler.QueueMergeWhenReady(rr, req)

		require.Equal(t, http.StatusOK, rr.Code, "QueueMergeWhenReady should allow service-level re-queueing after cancellation")
		require.True(t, called, "QueueMergeWhenReady should call the queue service even if prior state was cancelled")
	})
}

func mergeRequest(pathID string, user *models.User, orgID uuid.UUID) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pull-requests/"+pathID+"/merge", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	if user != nil {
		req = req.WithContext(middleware.WithUser(req.Context(), user))
	}
	req = req.WithContext(withURLParam(req.Context(), "id", pathID))
	return req
}

func mergeWhenReadyRequest(method, pathID string, user *models.User, orgID uuid.UUID) *http.Request {
	req := httptest.NewRequest(method, "/api/v1/pull-requests/"+pathID+"/merge-when-ready", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	if user != nil {
		req = req.WithContext(middleware.WithUser(req.Context(), user))
	}
	req = req.WithContext(withURLParam(req.Context(), "id", pathID))
	return req
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

func TestPullRequestHandler_streamOrgIDFromRequest_AdditionalErrors(t *testing.T) {
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
			expectedErr: errPullRequestStreamUnauthorized,
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

			handler := NewPullRequestHandler(nil)
			if tt.membershipErr != nil {
				handler.SetMembershipStore(&stubPullRequestMembershipStore{
					getFunc: func(context.Context, uuid.UUID, uuid.UUID) (models.OrganizationMembership, error) {
						return models.OrganizationMembership{}, tt.membershipErr
					},
				})
			}

			req := httptest.NewRequest(http.MethodGet, "/api/v1/pull-requests/stream?org_id="+requestedOrgID.String(), nil)
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

func TestPullRequestHandler_StreamUpdates(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	user := &models.User{ID: uuid.New(), OrgID: orgID}

	tests := []struct {
		name           string
		handler        *PullRequestHandler
		query          string
		expectedCode   int
		expectedSubstr string
	}{
		{
			name:           "returns service unavailable when streams missing",
			handler:        NewPullRequestHandler(nil),
			expectedCode:   http.StatusServiceUnavailable,
			expectedSubstr: "pull request streams unavailable",
		},
		{
			name: "returns bad request for invalid requested org",
			handler: func() *PullRequestHandler {
				h := NewPullRequestHandler(nil)
				h.SetStreams(newTestPullRequestStreams(t))
				return h
			}(),
			query:          "?org_id=not-a-uuid",
			expectedCode:   http.StatusBadRequest,
			expectedSubstr: "invalid pull request stream org",
		},
		{
			name: "returns forbidden when explicit org is not allowed",
			handler: func() *PullRequestHandler {
				h := NewPullRequestHandler(nil)
				h.SetStreams(newTestPullRequestStreams(t))
				h.SetMembershipStore(&stubPullRequestMembershipStore{
					getFunc: func(context.Context, uuid.UUID, uuid.UUID) (models.OrganizationMembership, error) {
						return models.OrganizationMembership{}, pgx.ErrNoRows
					},
				})
				return h
			}(),
			query:          "?org_id=" + uuid.New().String(),
			expectedCode:   http.StatusForbidden,
			expectedSubstr: "forbidden pull request stream org",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "/api/v1/pull-requests/stream"+tt.query, nil)
			req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
			req = req.WithContext(middleware.WithUser(req.Context(), user))
			rr := httptest.NewRecorder()

			tt.handler.StreamUpdates(rr, req)

			require.Equal(t, tt.expectedCode, rr.Code, "StreamUpdates should return the expected error code")
			require.Contains(t, rr.Body.String(), tt.expectedSubstr, "StreamUpdates should encode the expected error text")
		})
	}

	streams := newTestPullRequestStreams(t)
	handler := NewPullRequestHandler(nil)
	handler.SetStreams(streams)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/pull-requests/stream", nil).WithContext(ctx)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(middleware.WithUser(req.Context(), user))
	rr := newLockedRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.StreamUpdates(rr, req)
	}()

	event := models.PullRequestUpdatedEvent{
		PullRequestID: uuid.New(),
		Version:       7,
		HeadSHA:       "head",
		BaseSHA:       "base",
		SyncedAt:      time.Now().UTC(),
	}
	require.Eventually(t, func() bool {
		err := streams.PublishUpdated(context.Background(), orgID, event)
		if err != nil {
			return false
		}
		return strings.Contains(rr.BodyString(), "pull_request.updated")
	}, 2*time.Second, 20*time.Millisecond, "StreamUpdates should write published pull request update events to the SSE response")

	cancel()
	<-done

	require.Contains(t, rr.BodyString(), event.PullRequestID.String(), "StreamUpdates should serialize the published pull request ID")
}

func newTestPullRequestStreams(t *testing.T) *cache.PullRequestStreams {
	t.Helper()

	mr := miniredis.RunT(t)
	metrics, err := cache.NewMetrics()
	require.NoError(t, err, "Redis metrics should initialize")
	client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + mr.Addr()}, zerolog.Nop(), metrics)
	require.NotNil(t, client, "Redis client should initialize for pull request stream tests")
	t.Cleanup(func() {
		closeErr := client.Close()
		if closeErr != nil && !strings.Contains(closeErr.Error(), "client is closed") {
			require.NoError(t, closeErr, "pull request stream test client should close cleanly")
		}
	})
	return cache.NewPullRequestStreams(client, zerolog.Nop())
}

func withURLParam(ctx context.Context, key, value string) context.Context {
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add(key, value)
	return context.WithValue(ctx, chi.RouteCtxKey, routeCtx)
}
