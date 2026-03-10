package handlers

import (
	"bytes"
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

func TestProjectAIHandler_ImproveFailsWhenSpecsQueryFails(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	projectStore := db.NewProjectStore(mock)
	specStore := db.NewProjectSpecStore(mock)
	attachmentStore := db.NewProjectAttachmentStore(mock)
	taskStore := db.NewProjectTaskStore(mock)
	handler := NewProjectAIHandler(projectStore, specStore, attachmentStore, taskStore)

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
	mock.ExpectQuery("SELECT .+ FROM project_specs WHERE project_id = @project_id AND org_id = @org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("spec query failed"))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/ai/improve", bytes.NewBufferString(`{"target":"spec"}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.Improve(rr, req)

	require.Equal(t, http.StatusInternalServerError, rr.Code, "Improve should fail when context specs query fails")
	require.Contains(t, rr.Body.String(), "LIST_SPECS_FAILED", "Improve should return a specs lookup error code")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
