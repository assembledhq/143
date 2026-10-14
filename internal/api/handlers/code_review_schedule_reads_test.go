package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func schedulingReadRequest(orgID uuid.UUID, target, query, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/?"+query, strings.NewReader(body))
	route := chi.NewRouteContext()
	route.URLParams.Add("id", target)
	return req.WithContext(context.WithValue(middleware.WithOrgID(req.Context(), orgID), chi.RouteCtxKey, route))
}

func TestCodeReviewScheduleReadAndPause(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, body, code           string
		pause, invalidID, disabled bool
		readErr, pauseErr          error
		status                     int
		expectedPaused             bool
		reads, writes              int
	}{
		{name: "read", status: 200, reads: 1},
		{name: "read missing target", readErr: pgx.ErrNoRows, status: 404, code: "CODE_REVIEW_NOT_FOUND", reads: 1},
		{name: "read dependency failure", readErr: errors.New("private DB details"), status: 500, code: "CODE_REVIEW_SCHEDULING_FAILED", reads: 1},
		{name: "read invalid ID", invalidID: true, status: 400, code: "INVALID_ID"},
		{name: "read disabled scheduling", disabled: true, status: 503, code: "CODE_REVIEW_SCHEDULING_UNAVAILABLE"},
		{name: "pause", pause: true, body: `{"automatic_paused":true}`, expectedPaused: true, status: 200, reads: 1, writes: 1},
		{name: "resume", pause: true, body: `{"automatic_paused":false}`, status: 200, reads: 1, writes: 1},
		{name: "pause requires field", pause: true, body: `{}`, status: 400, code: "CODE_REVIEW_REQUEST_INVALID"},
		{name: "pause rejects null", pause: true, body: `{"automatic_paused":null}`, status: 400, code: "CODE_REVIEW_REQUEST_INVALID"},
		{name: "pause invalid JSON", pause: true, body: `{`, status: 400, code: "CODE_REVIEW_REQUEST_INVALID"},
		{name: "pause invalid ID", pause: true, invalidID: true, status: 400, code: "INVALID_ID"},
		{name: "pause unavailable", pause: true, disabled: true, status: 503, code: "CODE_REVIEW_SCHEDULING_UNAVAILABLE"},
		{name: "pause missing target", pause: true, body: `{"automatic_paused":true}`, expectedPaused: true, pauseErr: pgx.ErrNoRows, status: 404, code: "CODE_REVIEW_NOT_FOUND", writes: 1},
		{name: "pause conflict", pause: true, body: `{"automatic_paused":false}`, pauseErr: fmt.Errorf("wrapped: %w", db.ErrCodeReviewRequestConflict), status: 409, code: "CODE_REVIEW_REQUEST_ID_CONFLICT", writes: 1},
		{name: "pause dependency failure", pause: true, body: `{"automatic_paused":false}`, pauseErr: errors.New("private DB details"), status: 500, code: "CODE_REVIEW_SCHEDULING_FAILED", writes: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, prID := uuid.New(), uuid.New()
			expected := models.CodeReviewPRState{ID: uuid.New(), OrgID: orgID, PullRequestID: prID, State: models.CodeReviewScheduleWaiting, AutomaticPaused: tt.expectedPaused}
			reads, writes := 0, 0
			stub := &scheduleHandlerStub{enabled: !tt.disabled,
				get: func(org, pr uuid.UUID) (models.CodeReviewPRState, error) {
					reads++
					require.Equal(t, orgID, org, "read must use authenticated tenant")
					require.Equal(t, prID, pr, "read must use route target")
					return expected, tt.readErr
				},
				pause: func(org, pr uuid.UUID, paused bool) error {
					writes++
					require.Equal(t, orgID, org, "pause must use authenticated tenant")
					require.Equal(t, prID, pr, "pause must use route target")
					require.Equal(t, tt.expectedPaused, paused, "explicit false must resume")
					return tt.pauseErr
				},
			}
			h := NewCodeReviewHandler(nil, nil)
			h.retryService = stub
			target := prID.String()
			if tt.invalidID {
				target = "bad-id"
			}
			req := schedulingReadRequest(orgID, target, "", tt.body)
			rr := httptest.NewRecorder()
			if tt.pause {
				h.PauseSchedule(rr, req)
			} else {
				h.GetSchedule(rr, req)
			}
			require.Equal(t, tt.status, rr.Code, "schedule endpoint should preserve outcome status")
			require.Equal(t, tt.reads, reads, "invalid requests should not read schedules")
			require.Equal(t, tt.writes, writes, "invalid requests should not mutate schedules")
			if tt.code != "" {
				var response models.ErrorResponse
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "error should use JSON envelope")
				require.Equal(t, tt.code, response.Error.Code, "schedule errors should expose stable code")
				require.NotContains(t, rr.Body.String(), "private DB details", "dependency details should not reach clients")
			} else {
				var response models.SingleResponse[models.CodeReviewPRState]
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "schedule response should decode")
				expected.OrgID = uuid.Nil
				require.Equal(t, expected, response.Data, "read should return durable state including paused flag")
			}
		})
	}
}

func TestCodeReviewListPendingSchedules(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, query, code     string
		limit, count, status  int
		filtered, err, called bool
	}{
		{name: "defaults", limit: 50, count: 1, status: 200, called: true},
		{name: "filtered pagination", limit: 2, count: 2, status: 200, filtered: true, called: true},
		{name: "last page", limit: 2, count: 1, status: 200, filtered: true, called: true},
		{name: "empty", limit: 50, status: 200, called: true},
		{name: "invalid repository", query: "repository_id=bad", status: 400, code: "INVALID_FILTER"},
		{name: "invalid cursor", query: "cursor=bad", status: 400, code: "INVALID_FILTER"},
		{name: "non numeric limit", query: "limit=no", status: 400, code: "INVALID_LIMIT"},
		{name: "zero limit", query: "limit=0", status: 400, code: "INVALID_LIMIT"},
		{name: "negative limit", query: "limit=-1", status: 400, code: "INVALID_LIMIT"},
		{name: "too large limit", query: "limit=101", status: 400, code: "INVALID_LIMIT"},
		{name: "dependency failure", limit: 50, status: 500, code: "CODE_REVIEW_SCHEDULING_FAILED", err: true, called: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, repo, after := uuid.New(), uuid.New(), uuid.New()
			called := false
			rows := make([]models.CodeReviewScheduledTarget, tt.count)
			for i := range rows {
				rows[i].Schedule = models.CodeReviewPRState{ID: uuid.New(), OrgID: org, PullRequestID: uuid.New(), State: models.CodeReviewScheduleWaiting}
			}
			stub := &scheduleHandlerStub{enabled: true, list: func(o uuid.UUID, r, a *uuid.UUID, limit int) ([]models.CodeReviewScheduledTarget, error) {
				called = true
				require.Equal(t, org, o, "pending list must be tenant scoped")
				require.Equal(t, tt.limit, limit, "limit should be bounded and forwarded")
				if tt.filtered {
					require.Equal(t, &repo, r, "repository filter should be forwarded")
					require.Equal(t, &after, a, "cursor should be forwarded")
				} else {
					require.Nil(t, r, "default list should not filter repository")
					require.Nil(t, a, "first page should not have a cursor")
				}
				if tt.err {
					return nil, errors.New("private DB details")
				}
				return rows, nil
			}}
			h := NewCodeReviewHandler(nil, nil)
			h.retryService = stub
			query := tt.query
			if tt.filtered {
				query = fmt.Sprintf("repository_id=%s&cursor=%s&limit=%d", repo, after, tt.limit)
			}
			rr := httptest.NewRecorder()
			h.ListPendingSchedules(rr, schedulingReadRequest(org, "", query, ""))
			require.Equal(t, tt.status, rr.Code, "list should preserve validation and dependency status")
			require.Equal(t, tt.called, called, "invalid filters must not reach store")
			if tt.code != "" {
				var response models.ErrorResponse
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "error envelope should decode")
				require.Equal(t, tt.code, response.Error.Code, "list should return stable error code")
			} else {
				var response struct {
					Data []models.CodeReviewScheduledTarget `json:"data"`
					Meta struct {
						NextCursor string `json:"next_cursor"`
					} `json:"meta"`
				}
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "list envelope should decode")
				for i := range rows {
					rows[i].Schedule.OrgID = uuid.Nil
				}
				require.Equal(t, rows, response.Data, "list should return exact scheduled targets")
				next := ""
				if tt.count == tt.limit {
					next = rows[len(rows)-1].Schedule.ID.String()
				}
				require.Equal(t, next, response.Meta.NextCursor, "only a full page should expose a next cursor")
			}
		})
	}
}

var _ codeReviewSchedulingService = (*scheduleHandlerStub)(nil)
