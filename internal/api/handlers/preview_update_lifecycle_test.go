package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPreviewUpdateLifecycleValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, body, statusValue, code         string
		anonymous, badID, noActive, dbFailure bool
		status                                int
		logCount                              int
		mode                                  models.PreviewUpdateMode
	}{
		{name: "malformed request", body: "{", status: 400, code: "INVALID_BODY"},
		{name: "unsupported mode", body: `{"force_mode":"bad"}`, status: 422, code: "PREVIEW_UPDATE_MODE_UNSUPPORTED"},
		{name: "invalid session", badID: true, status: 400, code: "INVALID_SESSION_ID"},
		{name: "anonymous", anonymous: true, status: 401, code: "UNAUTHORIZED"},
		{name: "lookup unavailable", dbFailure: true, status: 500, code: "INTERNAL_ERROR"},
		{name: "browser reload requires active preview", noActive: true, body: `{"force_mode":"browser_reload"}`, status: 409, code: "PREVIEW_NOT_READY"},
		{name: "starting preview conflicts", statusValue: "starting", status: 409, code: "PREVIEW_UPDATE_CONFLICT"},
		{name: "noop current", body: `{"force_mode":"noop_current"}`, status: 200, logCount: 1, mode: models.PreviewUpdateModeNoopCurrent},
		{name: "reload disabled", body: `{"force_mode":"browser_reload","reload_browser":false}`, status: 200, logCount: 2, mode: models.PreviewUpdateModeBrowserReload},
		{name: "reload missing inspector", body: `{"force_mode":"browser_reload"}`, status: 500, code: "PREVIEW_UPDATE_FAILED", logCount: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated preview update fixture")
			defer mock.Close()
			org, user, session, id := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			h := newPreviewHandlerWithMock(mock)
			h.sessionStore = nil
			row := newActivePreviewRow(id, session, org, user, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
			if tt.statusValue != "" {
				row[7] = tt.statusValue
			}
			body := tt.body
			if body == "" {
				body = `{}`
			}
			lookup := !tt.anonymous && !tt.badID && tt.name != "malformed request" && tt.name != "unsupported mode"
			if lookup {
				query := mock.ExpectQuery(`SELECT .* FROM preview_instances WHERE org_id = @org_id AND session_id = @session_id`).WithArgs(pgx.NamedArgs{"org_id": org, "session_id": session})
				if tt.dbFailure {
					query.WillReturnError(errors.New("database unavailable"))
				} else if tt.noActive {
					query.WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))
				} else {
					query.WillReturnRows(pgxmock.NewRows(previewInstanceTestCols).AddRow(row...))
				}
			}
			if tt.logCount > 0 {
				mock.ExpectQuery(`SELECT .* FROM preview_instances WHERE id = @id AND org_id = @org_id`).WithArgs(pgx.NamedArgs{"org_id": org, "id": id}).WillReturnError(errors.New("optional status unavailable"))
				for i := 0; i < tt.logCount; i++ {
					mock.ExpectQuery(`INSERT INTO preview_logs`).WithArgs(id, org, pgxmock.AnyArg(), models.PreviewLogStepUpdate, pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("log sink unavailable"))
				}
			}
			route := session.String()
			if tt.badID {
				route = "bad"
			}
			if tt.anonymous {
				user = uuid.Nil
			}
			rr := httptest.NewRecorder()
			r := previewTestContextWithIDs(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)), org, user, route)
			if tt.anonymous {
				r = apiClientLifecycleRequest(org, uuid.Nil, route, "", body)
			}
			h.UpdatePreview(rr, r)
			require.Equal(t, tt.status, rr.Code, "preview update must distinguish input, ownership, lookup and runtime failures")
			if tt.code != "" {
				requireAPIClientError(t, rr, tt.code)
			} else {
				var response models.SingleResponse[models.PreviewUpdateResponse]
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "safe preview update response must decode")
				expected := models.PreviewUpdateResponse{PreviewID: id, SessionID: session, Mode: tt.mode, Status: models.PreviewStatusReady, Action: models.PreviewUpdateActionAlreadyCurrent, Message: "preview is already current", Freshness: &models.PreviewFreshness{State: models.PreviewFreshnessUnknown, Reason: "preview_revision_missing"}}
				if tt.mode == models.PreviewUpdateModeBrowserReload {
					expected.Action = models.PreviewUpdateActionUpdated
					expected.Message = "browser reloaded"
				}
				require.Equal(t, expected, response.Data, "safe update must preserve exact preview identity, action and freshness")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "preview update must use exact tenant-scoped lookup and expected best-effort diagnostics")
		})
	}
}
