package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	human "github.com/assembledhq/143/internal/services/humaninput"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type humanInputHandlerRepo struct {
	human.Repository
	t            *testing.T
	org, session uuid.UUID
	filters      db.HumanInputRequestFilters
	err          error
	calls        int
}

func (f *humanInputHandlerRepo) GetSession(_ context.Context, org, session uuid.UUID) (models.Session, error) {
	f.calls++
	require.Equal(f.t, []uuid.UUID{f.org, f.session}, []uuid.UUID{org, session}, "human input list must use authenticated tenant and route session")
	return models.Session{ID: session, OrgID: org}, f.err
}
func (f *humanInputHandlerRepo) ListRequests(_ context.Context, org, session uuid.UUID, filters human.RequestFilters) ([]models.HumanInputRequest, error) {
	f.calls++
	require.Equal(f.t, f.filters, filters, "human input filters must retain validated status and thread")
	return []models.HumanInputRequest{{ID: f.session, OrgID: org, SessionID: session, Title: "Choose framework", Status: models.HumanInputRequestStatusPending}}, f.err
}
func (f *humanInputHandlerRepo) WithTx(context.Context, func(context.Context, human.Tx) error) error {
	f.calls++
	return f.err
}

func TestSessionHumanInputHTTPValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, action, query, body, code             string
		err                                         error
		status                                      int
		anonymous, disabled, badSession, badRequest bool
		calls                                       int
	}{
		{name: "list defaults", action: "list", status: 200, calls: 2},
		{name: "list filtered", action: "list", query: "status=pending&thread_id=thread", status: 200, calls: 2},
		{name: "list missing session", action: "list", err: pgx.ErrNoRows, status: 404, code: "NOT_FOUND", calls: 1},
		{name: "list store failure", action: "list", err: errors.New("private database error"), status: 500, code: "LIST_FAILED", calls: 1},
		{name: "invalid list status", action: "list", query: "status=unknown", status: 400, code: "INVALID_STATUS"},
		{name: "invalid list thread", action: "list", query: "thread_id=bad", status: 400, code: "INVALID_THREAD_ID"},
		{name: "list unavailable", action: "list", disabled: true, status: 501, code: "NOT_CONFIGURED"},
		{name: "invalid list session", action: "list", badSession: true, status: 400, code: "INVALID_ID"},
		{name: "answer unavailable", action: "answer", disabled: true, status: 501, code: "NOT_CONFIGURED"},
		{name: "cancel unavailable", action: "cancel", disabled: true, status: 501, code: "NOT_CONFIGURED"},
		{name: "invalid answer session", action: "answer", badSession: true, status: 400, code: "INVALID_ID"},
		{name: "invalid cancel session", action: "cancel", badSession: true, status: 400, code: "INVALID_ID"},
		{name: "invalid answer request", action: "answer", badRequest: true, status: 400, code: "INVALID_REQUEST_ID"},
		{name: "invalid cancel request", action: "cancel", badRequest: true, status: 400, code: "INVALID_REQUEST_ID"},
		{name: "anonymous answer", action: "answer", anonymous: true, status: 401, code: "UNAUTHORIZED"},
		{name: "anonymous cancel", action: "cancel", anonymous: true, status: 401, code: "UNAUTHORIZED"},
		{name: "malformed answer", action: "answer", body: "{", status: 400, code: "INVALID_BODY"},
	}
	serviceErrors := []struct {
		name, code string
		err        error
		status     int
	}{{"missing", "NOT_FOUND", human.ErrNotFound, 404}, {"expired", "SNAPSHOT_EXPIRED", human.ErrSnapshotExpired, 410}, {"invalid", "INVALID_ANSWER", human.ErrInvalidAnswer, 400}, {"answered", "NOT_PENDING", human.ErrNotPending, 409}, {"limit", "RUNNING_LIMIT", human.ErrRunningLimit, 409}, {"checkpoint", "CHECKPOINT_PENDING", human.ErrCheckpointPending, 409}, {"resumability", "NOT_RESUMABLE", human.ErrNotResumable, 409}, {"database", "HUMAN_INPUT_FAILED", errors.New("private database error"), 500}}
	for _, action := range []string{"answer", "cancel"} {
		for _, failure := range serviceErrors {
			tests = append(tests, struct {
				name, action, query, body, code             string
				err                                         error
				status                                      int
				anonymous, disabled, badSession, badRequest bool
				calls                                       int
			}{name: action + " " + failure.name, action: action, err: failure.err, status: failure.status, code: failure.code, calls: 1})
		}
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, session, request, user, thread := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			repo := &humanInputHandlerRepo{t: t, org: org, session: session, err: tt.err}
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated ownership guard fixture")
			defer mock.Close()
			h := &SessionHandler{humanInputService: human.New(repo), runStore: db.NewSessionStore(mock), logger: zerolog.Nop()}
			if tt.action != "list" && !tt.disabled && !tt.badSession && !tt.badRequest {
				mock.ExpectQuery(`SELECT code_review_owner_pr_id FROM sessions WHERE org_id=\$1 AND id=\$2`).WithArgs(org, session).WillReturnRows(pgxmock.NewRows([]string{"code_review_owner_pr_id"}).AddRow(nil))
			}
			if tt.disabled {
				h.humanInputService = nil
			}
			query := strings.ReplaceAll(tt.query, "=thread", "="+thread.String())
			if tt.name == "list filtered" {
				repo.filters = db.HumanInputRequestFilters{Status: models.HumanInputRequestStatusPending, ThreadID: &thread}
			}
			body := tt.body
			if body == "" {
				body = `{"answer_text":"Use Go"}`
			}
			r := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/?"+query, strings.NewReader(body))
			ctx := middleware.WithOrgID(r.Context(), org)
			if !tt.anonymous {
				ctx = middleware.WithUser(ctx, &models.User{ID: user, OrgID: uuid.New()})
			}
			route := chi.NewRouteContext()
			sid, rid := session.String(), request.String()
			if tt.badSession {
				sid = "bad"
			}
			if tt.badRequest {
				rid = "bad"
			}
			route.URLParams.Add("id", sid)
			route.URLParams.Add("request_id", rid)
			r = r.WithContext(context.WithValue(ctx, chi.RouteCtxKey, route))
			rr := httptest.NewRecorder()
			switch tt.action {
			case "list":
				h.ListHumanInputRequests(rr, r)
			case "answer":
				h.AnswerHumanInputRequest(rr, r)
			case "cancel":
				h.CancelHumanInputRequest(rr, r)
			}
			require.Equal(t, tt.status, rr.Code, "human input endpoint must map validation and service failure precisely")
			require.Equal(t, tt.calls, repo.calls, "rejected input must not reach repository")
			require.NoError(t, mock.ExpectationsWereMet(), "ownership guard must inspect exact tenant before human mutations")
			if tt.code != "" {
				requireAPIClientError(t, rr, tt.code)
			} else {
				var actual models.ListResponse[models.HumanInputRequest]
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &actual), "list response should decode")
				require.Equal(t, models.ListResponse[models.HumanInputRequest]{Data: []models.HumanInputRequest{{ID: session, OrgID: org, SessionID: session, Title: "Choose framework", Status: models.HumanInputRequestStatusPending}}}, actual, "list response must preserve exact requests for the authenticated tenant")
			}
		})
	}
}
