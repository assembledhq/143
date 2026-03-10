package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var projectCycleHandlerColumns = []string{
	"id", "project_id", "org_id", "pm_plan_id", "cycle_number", "analysis", "decisions", "progress_pct",
	"tasks_completed_this_cycle", "tasks_failed_this_cycle", "tasks_created_this_cycle", "created_at",
}

func TestProjectHandler_Get_FailsWhenAttachmentsQueryFails(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	projectStore := db.NewProjectStore(mock)
	taskStore := db.NewProjectTaskStore(mock)
	cycleStore := db.NewProjectCycleStore(mock)
	attachmentStore := db.NewProjectAttachmentStore(mock)
	specStore := db.NewProjectSpecStore(mock)
	handler := NewProjectHandler(projectStore, taskStore, cycleStore)
	handler.SetAttachmentStore(attachmentStore)
	handler.SetSpecStore(specStore)

	orgID := uuid.New()
	projectID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectColumns()).
				AddRow(newProjectRow(projectID, orgID, repoID, models.ProjectStatusDraft, now)...),
		)
	mock.ExpectQuery("SELECT .+ FROM project_tasks WHERE project_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectTaskHandlerColumns))
	mock.ExpectQuery("SELECT .+ FROM project_cycles WHERE project_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectCycleHandlerColumns))
	mock.ExpectQuery("SELECT .+ FROM project_attachments WHERE project_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("attachments query failed"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+projectID.String(), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.Get(rr, req)

	require.Equal(t, http.StatusInternalServerError, rr.Code, "Get should fail when attachments lookup fails")
	require.Contains(t, rr.Body.String(), "LIST_ATTACHMENTS_FAILED", "Get should return an attachments lookup error code")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
