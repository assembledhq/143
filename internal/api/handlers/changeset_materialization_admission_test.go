package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestMaterializeChangesetRejectsPrimaryBeforeQueueing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		primary    bool
		status     models.ChangesetStatus
		worktree   bool
		wantStatus int
	}{
		{name: "planned primary", primary: true, status: models.ChangesetStatusPlanned, wantStatus: http.StatusConflict},
		{name: "ready primary", primary: true, status: models.ChangesetStatusReady, wantStatus: http.StatusConflict},
		{name: "imported primary", primary: true, status: models.ChangesetStatusExternalUpdateDetected, wantStatus: http.StatusConflict},
		{name: "primary with a path", primary: true, status: models.ChangesetStatusReady, worktree: true, wantStatus: http.StatusConflict},
		{name: "existing split worktree remains idempotent", status: models.ChangesetStatusReady, worktree: true, wantStatus: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "materialization admission should have an isolated database mock")
			t.Cleanup(mock.Close)
			orgID, sessionID, changesetID := uuid.New(), uuid.New(), uuid.New()
			now := time.Now().UTC()
			row := retrySessionRow(sessionID, orgID, models.SessionStatusRunning, nil, nil, models.SandboxStateRunning, nil, now)
			for i, column := range sessionColumns {
				if column == "container_id" {
					row[i] = ptr("synthetic-container")
				}
			}
			mock.ExpectQuery("SELECT .+ FROM sessions").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(sessionColumns).AddRow(row...))
			mock.ExpectQuery("SELECT code_review_owner_pr_id FROM sessions").WithArgs(orgID, sessionID).
				WillReturnRows(pgxmock.NewRows([]string{"code_review_owner_pr_id"}).AddRow(nil))
			var worktree *string
			if tt.worktree {
				worktree = ptr("/workspace/split")
			}
			mock.ExpectQuery("SELECT .*FROM session_changesets").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(sessionChangesetColumns).AddRow(
					changesetID, orgID, sessionID, tt.primary, 0, "Changes", "", tt.status,
					"main", "main", ptr("session-branch"), nil, nil, nil, nil, worktree,
					nil, nil, nil, nil, false, models.PRCreationStateIdle, nil, now, now,
				))
			h := newSessionHandler(t, mock)
			h.SetChangesetStore(db.NewSessionChangesetStore(mock))
			w := httptest.NewRecorder()
			h.MaterializeChangeset(w, changesetRequest(http.MethodPost, "/changesets/materialize", sessionID.String(), &changesetID, orgID, ""))
			require.Equal(t, tt.wantStatus, w.Code, "primary admission must fail without enqueueing an impossible job")
			if tt.primary {
				var response models.ErrorResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response), "admission rejection should be a structured error")
				require.Equal(t, models.ErrorResponse{Error: models.ErrorDetail{
					Code:    "PRIMARY_CHANGESET_NOT_MATERIALIZABLE",
					Message: "the primary changeset already uses the session workspace; only split changesets can be materialized",
				}}, response, "rejection should explain that the primary already has its session workspace")
			} else {
				var response map[string]any
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response), "existing split should return its materialization receipt")
				require.Equal(t, map[string]any{"status": "materialized", "changeset_id": changesetID.String()}, response, "existing split materialization should remain idempotent")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "admission should use tenant-scoped reads and never enqueue a primary job")
		})
	}
}
