package handlers

import (
	"bytes"
	"encoding/json"
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

// -- row helpers --

func newAttachmentRow(id, projectID, orgID uuid.UUID, now time.Time) []interface{} {
	userID := uuid.New()
	return []interface{}{
		id, projectID, orgID, "test.png", "https://example.com/test.png", "image",
		nil, nil, "screenshot", nil, 0, &userID, now, now,
	}
}

func newSpecRow(id, projectID, orgID uuid.UUID, specType string, content string, now time.Time) []interface{} {
	userID := uuid.New()
	return []interface{}{
		id, projectID, orgID, "Test Spec", content, specType, 0, 1, &userID, now, now,
	}
}

// ===== ProjectAttachmentHandler tests =====

func TestProjectAttachmentHandler_List(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectAttachmentHandler(db.NewProjectAttachmentStore(mock), nil)
	orgID := uuid.New()
	projectID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM project_attachments WHERE project_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectAttachmentHandlerColumns))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+projectID.String()+"/attachments", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"data":[]`)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectAttachmentHandler_Create(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectAttachmentHandler(db.NewProjectAttachmentStore(mock), db.NewProjectStore(mock))
	orgID := uuid.New()
	userID := uuid.New()
	projectID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	// GetByID to verify project exists
	mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectColumns()).
				AddRow(newProjectRow(projectID, orgID, repoID, models.ProjectStatusDraft, now)...),
		)

	// INSERT attachment (11 named args)
	mock.ExpectQuery("INSERT INTO project_attachments").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))

	body, _ := json.Marshal(map[string]string{
		"file_name": "screenshot.png",
		"file_url":  "https://example.com/screenshot.png",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/attachments", bytes.NewBuffer(body))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID})
	req = req.WithContext(ctx)
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	require.Equal(t, http.StatusCreated, rr.Code)
	require.Contains(t, rr.Body.String(), "screenshot.png")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectAttachmentHandler_Create_InvalidURL(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectAttachmentHandler(db.NewProjectAttachmentStore(mock), db.NewProjectStore(mock))
	orgID := uuid.New()
	userID := uuid.New()
	projectID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	// GetByID to verify project exists
	mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectColumns()).
				AddRow(newProjectRow(projectID, orgID, repoID, models.ProjectStatusDraft, now)...),
		)

	body, _ := json.Marshal(map[string]string{
		"file_name": "screenshot.png",
		"file_url":  "ftp://example.com/screenshot.png",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/attachments", bytes.NewBuffer(body))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID})
	req = req.WithContext(ctx)
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "INVALID_URL")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectAttachmentHandler_Create_MissingFields(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectAttachmentHandler(db.NewProjectAttachmentStore(mock), db.NewProjectStore(mock))
	orgID := uuid.New()
	userID := uuid.New()
	projectID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	// GetByID to verify project exists
	mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectColumns()).
				AddRow(newProjectRow(projectID, orgID, repoID, models.ProjectStatusDraft, now)...),
		)

	body, _ := json.Marshal(map[string]string{
		"file_name": "screenshot.png",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/attachments", bytes.NewBuffer(body))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID})
	req = req.WithContext(ctx)
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "MISSING_FIELD")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectAttachmentHandler_Delete(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectAttachmentHandler(db.NewProjectAttachmentStore(mock), nil)
	orgID := uuid.New()
	projectID := uuid.New()
	attachmentID := uuid.New()
	uploadedBy := uuid.New()
	now := time.Now()

	// GetByID returns attachment belonging to the correct project
	mock.ExpectQuery("SELECT .+ FROM project_attachments WHERE id = @id AND org_id = @org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectAttachmentHandlerColumns).AddRow(
				attachmentID, projectID, orgID, "name.png", "https://example.com/name.png", "image",
				nil, nil, "screenshot", nil, 0, &uploadedBy, now, now,
			),
		)

	// DELETE
	mock.ExpectExec("DELETE FROM project_attachments WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/"+projectID.String()+"/attachments/"+attachmentID.String(), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectAttachmentRouteParams(req.Context(), projectID, attachmentID))
	rr := httptest.NewRecorder()

	handler.Delete(rr, req)

	require.Equal(t, http.StatusNoContent, rr.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectAttachmentHandler_Delete_MismatchedProject(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectAttachmentHandler(db.NewProjectAttachmentStore(mock), nil)
	orgID := uuid.New()
	projectInPath := uuid.New()
	attachmentProjectID := uuid.New()
	attachmentID := uuid.New()
	uploadedBy := uuid.New()
	now := time.Now()

	// GetByID returns attachment belonging to a DIFFERENT project
	mock.ExpectQuery("SELECT .+ FROM project_attachments WHERE id = @id AND org_id = @org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectAttachmentHandlerColumns).AddRow(
				attachmentID, attachmentProjectID, orgID, "name.png", "https://example.com/name.png", "image",
				nil, nil, "screenshot", nil, 0, &uploadedBy, now, now,
			),
		)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/"+projectInPath.String()+"/attachments/"+attachmentID.String(), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectAttachmentRouteParams(req.Context(), projectInPath, attachmentID))
	rr := httptest.NewRecorder()

	handler.Delete(rr, req)

	require.Equal(t, http.StatusNotFound, rr.Code)
	require.Contains(t, rr.Body.String(), "NOT_FOUND")
	require.NoError(t, mock.ExpectationsWereMet())
}

// ===== ProjectSpecHandler tests =====

func TestProjectSpecHandler_List(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectSpecHandler(db.NewProjectSpecStore(mock), nil)
	orgID := uuid.New()
	projectID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM project_specs WHERE project_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectSpecHandlerColumns))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+projectID.String()+"/specs", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"data":[]`)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectSpecHandler_Create(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectSpecHandler(db.NewProjectSpecStore(mock), db.NewProjectStore(mock))
	orgID := uuid.New()
	userID := uuid.New()
	projectID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	// GetByID to verify project exists
	mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectColumns()).
				AddRow(newProjectRow(projectID, orgID, repoID, models.ProjectStatusDraft, now)...),
		)

	// INSERT spec (7 named args)
	mock.ExpectQuery("INSERT INTO project_specs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "version", "created_at", "updated_at"}).AddRow(uuid.New(), 1, now, now))

	body, _ := json.Marshal(map[string]string{
		"title":   "Product Requirements",
		"content": "The product should do X, Y, Z.",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/specs", bytes.NewBuffer(body))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID})
	req = req.WithContext(ctx)
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	require.Equal(t, http.StatusCreated, rr.Code)
	require.Contains(t, rr.Body.String(), "Product Requirements")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectSpecHandler_Create_MissingTitle(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectSpecHandler(db.NewProjectSpecStore(mock), db.NewProjectStore(mock))
	orgID := uuid.New()
	userID := uuid.New()
	projectID := uuid.New()
	repoID := uuid.New()
	now := time.Now()

	// GetByID to verify project exists
	mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectColumns()).
				AddRow(newProjectRow(projectID, orgID, repoID, models.ProjectStatusDraft, now)...),
		)

	body, _ := json.Marshal(map[string]string{
		"content": "Some content without a title",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/specs", bytes.NewBuffer(body))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID})
	req = req.WithContext(ctx)
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "MISSING_FIELD")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectSpecHandler_Get(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectSpecHandler(db.NewProjectSpecStore(mock), nil)
	orgID := uuid.New()
	projectID := uuid.New()
	specID := uuid.New()
	createdBy := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM project_specs WHERE id = @id AND org_id = @org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectSpecHandlerColumns).AddRow(
				specID, projectID, orgID, "Spec Title", "Spec content", "prd", 0, 1, &createdBy, now, now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+projectID.String()+"/specs/"+specID.String(), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectSpecRouteParams(req.Context(), projectID, specID))
	rr := httptest.NewRecorder()

	handler.Get(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "Spec Title")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectSpecHandler_Get_MismatchedProject(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectSpecHandler(db.NewProjectSpecStore(mock), nil)
	orgID := uuid.New()
	projectInPath := uuid.New()
	specProjectID := uuid.New()
	specID := uuid.New()
	createdBy := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM project_specs WHERE id = @id AND org_id = @org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectSpecHandlerColumns).AddRow(
				specID, specProjectID, orgID, "Spec", "Content", "prd", 1, 1, &createdBy, now, now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+projectInPath.String()+"/specs/"+specID.String(), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectSpecRouteParams(req.Context(), projectInPath, specID))
	rr := httptest.NewRecorder()

	handler.Get(rr, req)

	require.Equal(t, http.StatusNotFound, rr.Code)
	require.Contains(t, rr.Body.String(), "NOT_FOUND")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectSpecHandler_Delete(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := NewProjectSpecHandler(db.NewProjectSpecStore(mock), nil)
	orgID := uuid.New()
	projectID := uuid.New()
	specID := uuid.New()
	createdBy := uuid.New()
	now := time.Now()

	// GetByID returns spec belonging to the correct project
	mock.ExpectQuery("SELECT .+ FROM project_specs WHERE id = @id AND org_id = @org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectSpecHandlerColumns).AddRow(
				specID, projectID, orgID, "Spec", "Content", "prd", 0, 1, &createdBy, now, now,
			),
		)

	// DELETE
	mock.ExpectExec("DELETE FROM project_specs WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/"+projectID.String()+"/specs/"+specID.String(), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectSpecRouteParams(req.Context(), projectID, specID))
	rr := httptest.NewRecorder()

	handler.Delete(rr, req)

	require.Equal(t, http.StatusNoContent, rr.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// ===== ProjectAnalysisHandler tests =====

func TestProjectAnalysisHandler_Improve_NoSpecs(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	projectStore := db.NewProjectStore(mock)
	specStore := db.NewProjectSpecStore(mock)
	attachmentStore := db.NewProjectAttachmentStore(mock)
	taskStore := db.NewProjectTaskStore(mock)
	handler := NewProjectAnalysisHandler(projectStore, specStore, attachmentStore, taskStore)

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
	mock.ExpectQuery("SELECT .+ FROM project_specs WHERE project_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectSpecHandlerColumns))
	mock.ExpectQuery("SELECT .+ FROM project_attachments WHERE project_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectAttachmentHandlerColumns))
	mock.ExpectQuery("SELECT .+ FROM project_tasks WHERE project_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectTaskHandlerColumns))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/ai/improve", bytes.NewBufferString(`{"target":"spec"}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.Improve(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "Add a Product Requirements Document")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectAnalysisHandler_Improve_ShortSpec(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	projectStore := db.NewProjectStore(mock)
	specStore := db.NewProjectSpecStore(mock)
	attachmentStore := db.NewProjectAttachmentStore(mock)
	taskStore := db.NewProjectTaskStore(mock)
	handler := NewProjectAnalysisHandler(projectStore, specStore, attachmentStore, taskStore)

	orgID := uuid.New()
	projectID := uuid.New()
	repoID := uuid.New()
	specID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectColumns()).
				AddRow(newProjectRow(projectID, orgID, repoID, models.ProjectStatusDraft, now)...),
		)
	mock.ExpectQuery("SELECT .+ FROM project_specs WHERE project_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectSpecHandlerColumns).
				AddRow(newSpecRow(specID, projectID, orgID, "prd", "Short", now)...),
		)
	mock.ExpectQuery("SELECT .+ FROM project_attachments WHERE project_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectAttachmentHandlerColumns))
	mock.ExpectQuery("SELECT .+ FROM project_tasks WHERE project_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectTaskHandlerColumns))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/ai/improve", bytes.NewBufferString(`{"target":"spec"}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.Improve(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "Expand spec")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectAnalysisHandler_Improve_PRDWithoutTechSpec(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	projectStore := db.NewProjectStore(mock)
	specStore := db.NewProjectSpecStore(mock)
	attachmentStore := db.NewProjectAttachmentStore(mock)
	taskStore := db.NewProjectTaskStore(mock)
	handler := NewProjectAnalysisHandler(projectStore, specStore, attachmentStore, taskStore)

	orgID := uuid.New()
	projectID := uuid.New()
	repoID := uuid.New()
	specID := uuid.New()
	now := time.Now()

	// Content must be >= 100 chars to avoid "short spec" suggestion
	longContent := "This is a detailed product requirements document with user stories, acceptance criteria, and technical requirements for the feature."

	mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectColumns()).
				AddRow(newProjectRow(projectID, orgID, repoID, models.ProjectStatusDraft, now)...),
		)
	mock.ExpectQuery("SELECT .+ FROM project_specs WHERE project_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectSpecHandlerColumns).
				AddRow(newSpecRow(specID, projectID, orgID, "prd", longContent, now)...),
		)
	mock.ExpectQuery("SELECT .+ FROM project_attachments WHERE project_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectAttachmentHandlerColumns))
	mock.ExpectQuery("SELECT .+ FROM project_tasks WHERE project_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectTaskHandlerColumns))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/ai/improve", bytes.NewBufferString(`{"target":"spec"}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.Improve(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "Consider adding a technical spec")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectAnalysisHandler_Improve_TechSpecExists(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	projectStore := db.NewProjectStore(mock)
	specStore := db.NewProjectSpecStore(mock)
	attachmentStore := db.NewProjectAttachmentStore(mock)
	taskStore := db.NewProjectTaskStore(mock)
	handler := NewProjectAnalysisHandler(projectStore, specStore, attachmentStore, taskStore)

	orgID := uuid.New()
	projectID := uuid.New()
	repoID := uuid.New()
	prdID := uuid.New()
	techSpecID := uuid.New()
	now := time.Now()

	longContent := "This is a detailed product requirements document with user stories, acceptance criteria, and technical requirements for the feature."

	mock.ExpectQuery("SELECT .+ FROM projects WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectColumns()).
				AddRow(newProjectRow(projectID, orgID, repoID, models.ProjectStatusDraft, now)...),
		)
	mock.ExpectQuery("SELECT .+ FROM project_specs WHERE project_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectSpecHandlerColumns).
				AddRow(newSpecRow(prdID, projectID, orgID, "prd", longContent, now)...).
				AddRow(newSpecRow(techSpecID, projectID, orgID, "technical", longContent, now)...),
		)
	mock.ExpectQuery("SELECT .+ FROM project_attachments WHERE project_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectAttachmentHandlerColumns))
	mock.ExpectQuery("SELECT .+ FROM project_tasks WHERE project_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(projectTaskHandlerColumns))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/ai/improve", bytes.NewBufferString(`{"target":"spec"}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.Improve(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	// Should NOT suggest adding a technical spec since one already exists
	body := rr.Body.String()
	require.NotContains(t, body, "Consider adding a technical spec")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestProjectAnalysisHandler_Improve_MissingTarget(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	projectStore := db.NewProjectStore(mock)
	specStore := db.NewProjectSpecStore(mock)
	attachmentStore := db.NewProjectAttachmentStore(mock)
	taskStore := db.NewProjectTaskStore(mock)
	handler := NewProjectAnalysisHandler(projectStore, specStore, attachmentStore, taskStore)

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

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID.String()+"/ai/improve", bytes.NewBufferString(`{}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectRouteParam(req.Context(), projectID))
	rr := httptest.NewRecorder()

	handler.Improve(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "MISSING_FIELD")
	require.NoError(t, mock.ExpectationsWereMet())
}
