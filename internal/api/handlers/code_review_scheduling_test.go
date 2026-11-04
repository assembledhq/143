package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type scheduleHandlerStub struct {
	enabled bool
	request func(codereviewsvc.ScheduleRequestInput) (codereviewsvc.ScheduleRequestResult, error)
	get     func(uuid.UUID, uuid.UUID) (models.CodeReviewPRState, error)
	pause   func(uuid.UUID, uuid.UUID, bool) error
	list    func(uuid.UUID, *uuid.UUID, *uuid.UUID, int) ([]models.CodeReviewScheduledTarget, error)
}

func (s *scheduleHandlerStub) SchedulingEnabled() bool { return s.enabled }
func (s *scheduleHandlerStub) RetryReview(context.Context, codereviewsvc.RetryReviewInput) (codereviewsvc.RetryReviewResult, error) {
	panic("unexpected retry")
}
func (s *scheduleHandlerStub) GetSchedule(_ context.Context, orgID, prID uuid.UUID) (models.CodeReviewPRState, error) {
	return s.get(orgID, prID)
}
func (s *scheduleHandlerStub) PauseSchedule(_ context.Context, orgID, prID uuid.UUID, paused bool) error {
	return s.pause(orgID, prID, paused)
}
func (s *scheduleHandlerStub) ListPendingSchedules(_ context.Context, orgID uuid.UUID, repoID, after *uuid.UUID, limit int) ([]models.CodeReviewScheduledTarget, error) {
	return s.list(orgID, repoID, after, limit)
}
func (s *scheduleHandlerStub) RequestScheduledReview(_ context.Context, input codereviewsvc.ScheduleRequestInput) (codereviewsvc.ScheduleRequestResult, error) {
	return s.request(input)
}

func TestCodeReviewSchedulingRequestHandler(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, mode            string
		overrideSizeLimits    bool
		hasSource, zeroSource bool
		disposition           models.CodeReviewRequestDisposition
		disabled, anonymous   bool
		err                   error
		status                int
		code                  string
	}{
		{name: "records authorized tenant request", mode: "review_now", status: 202},
		{name: "supports ensure current", mode: "ensure_current", status: 202},
		{name: "carries the size limit override source", mode: "review_now", overrideSizeLimits: true, hasSource: true, status: 202},
		{name: "rejects override without its source", mode: "review_now", overrideSizeLimits: true, status: 400, code: "CODE_REVIEW_REQUEST_INVALID"},
		{name: "rejects override with a zero source", mode: "review_now", overrideSizeLimits: true, hasSource: true, zeroSource: true, status: 400, code: "CODE_REVIEW_REQUEST_INVALID"},
		{name: "rejects source without override", mode: "review_now", hasSource: true, status: 400, code: "CODE_REVIEW_REQUEST_INVALID"},
		{name: "rejects the size limit override on other modes", mode: "ensure_current", overrideSizeLimits: true, hasSource: true, status: 400, code: "CODE_REVIEW_REQUEST_INVALID"},
		{name: "rejects the size limit override on rechecks", mode: "recheck", overrideSizeLimits: true, hasSource: true, status: 400, code: "CODE_REVIEW_REQUEST_INVALID"},
		{name: "stale override link", mode: "review_now", overrideSizeLimits: true, hasSource: true, err: codereviewsvc.ErrStaleSizeLimitOverride, status: 409, code: "CODE_REVIEW_OVERRIDE_STALE"},
		{name: "returns completed reuse", mode: "review_now", disposition: models.CodeReviewRequestReused, status: 200},
		{name: "returns cancelled request redelivery", mode: "review_now", disposition: models.CodeReviewRequestCancelled, status: 200},
		{name: "rejects force fresh until capability exists", mode: "force_fresh", status: 409, code: "CODE_REVIEW_CAPABILITY_UNAVAILABLE"},
		{name: "rejects unknown mode", mode: "surprise", status: 400, code: "CODE_REVIEW_REQUEST_INVALID"},
		{name: "requires identity", mode: "review_now", anonymous: true, status: 401, code: "UNAUTHORIZED"},
		{name: "scheduling service unavailable", mode: "review_now", disabled: true, status: 503, code: "CODE_REVIEW_SCHEDULING_UNAVAILABLE"},
		{name: "closed or policy disabled", mode: "review_now", err: codereviewsvc.ErrReviewIneligible, status: 409, code: "CODE_REVIEW_PR_INELIGIBLE"},
		{name: "conflicting request identity", mode: "review_now", err: db.ErrCodeReviewRequestConflict, status: 409, code: "CODE_REVIEW_REQUEST_ID_CONFLICT"},
		{name: "terminal evidence capture failure", mode: "review_now", err: codereviewsvc.ErrRecheckUnavailable, status: 409, code: "CODE_REVIEW_RECHECK_UNAVAILABLE"},
		{name: "foreign target", mode: "review_now", err: pgx.ErrNoRows, status: 404, code: "CODE_REVIEW_NOT_FOUND"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, pr, userID, requestID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			var sourceID *uuid.UUID
			body := map[string]any{"request_id": requestID, "mode": tt.mode, "override_size_limits": tt.overrideSizeLimits}
			if tt.hasSource {
				source := uuid.New()
				if tt.zeroSource {
					source = uuid.Nil
				}
				sourceID = &source
				body["override_review_session_id"] = source
			}
			called := false
			expected := codereviewsvc.ScheduleRequestInput{OrgID: org, PullRequestID: pr, RequesterID: &userID, RequestID: requestID, Mode: models.CodeReviewRequestMode(tt.mode), OverrideSizeLimits: tt.overrideSizeLimits, OverrideReviewSessionID: sourceID}
			result := codereviewsvc.ScheduleRequestResult{RequestID: requestID, Disposition: "queued", Schedule: models.CodeReviewPRState{PullRequestID: pr, State: models.CodeReviewScheduleWaiting}}
			if tt.disposition != "" {
				result.Disposition = tt.disposition
			}
			stub := &scheduleHandlerStub{enabled: !tt.disabled, request: func(input codereviewsvc.ScheduleRequestInput) (codereviewsvc.ScheduleRequestResult, error) {
				called = true
				require.Equal(t, expected, input, "request carries authenticated org and requester, not body-supplied identity")
				return result, tt.err
			}}
			handler := NewCodeReviewHandler(nil, nil)
			handler.retryService = stub
			payload, err := json.Marshal(body)
			require.NoError(t, err, "encode the scheduling request")
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
			ctx := middleware.WithOrgID(req.Context(), org)
			if !tt.anonymous {
				ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: org, Role: models.RoleMember})
			}
			route := chi.NewRouteContext()
			route.URLParams.Add("id", pr.String())
			req = req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, route))
			rr := httptest.NewRecorder()
			handler.RequestReviewNow(rr, req)
			require.Equal(t, tt.status, rr.Code, "handler returns the expected scheduling outcome")
			if tt.status < 300 {
				var actual models.SingleResponse[codereviewsvc.ScheduleRequestResult]
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &actual), "decode recorded request")
				require.Equal(t, result, actual.Data, "return durable request state")
			} else {
				require.Contains(t, rr.Body.String(), tt.code, "failure has stable API error code")
				if tt.code == "CODE_REVIEW_PR_INELIGIBLE" {
					require.Contains(t, rr.Body.String(), "Review requires an open pull request and an enabled policy.", "ineligible explanation does not forbid draft reviews")
				}
				if tt.code == "CODE_REVIEW_OVERRIDE_STALE" {
					require.Contains(t, rr.Body.String(), "Turn off the override to request a standard review", "explain how to continue without the stale override")
				}
			}
			require.Equal(t, tt.status < 300 || tt.err != nil, called, "invalid or disabled requests must not reach admission")
		})
	}
}
