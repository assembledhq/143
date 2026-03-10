package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var projectAttachmentHandlerColumns = []string{
	"id", "project_id", "org_id", "file_name", "file_url", "file_type",
	"thumbnail_url", "file_size", "category", "caption", "sort_order", "uploaded_by", "created_at", "updated_at",
}

func withProjectAttachmentRouteParams(ctx context.Context, projectID, attachmentID uuid.UUID) context.Context {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", projectID.String())
	rctx.URLParams.Add("attachmentId", attachmentID.String())
	return context.WithValue(ctx, chi.RouteCtxKey, rctx)
}

func TestProjectAttachmentHandler_UpdateRejectsMismatchedProjectPath(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	handler := NewProjectAttachmentHandler(db.NewProjectAttachmentStore(mock), nil)
	orgID := uuid.New()
	projectInPath := uuid.New()
	attachmentProjectID := uuid.New()
	attachmentID := uuid.New()
	uploadedBy := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM project_attachments WHERE id = @id AND org_id = @org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(projectAttachmentHandlerColumns).AddRow(
				attachmentID, attachmentProjectID, orgID, "name.png", "https://example.com/name.png", "image",
				nil, nil, "screenshot", nil, 0, &uploadedBy, now, now,
			),
		)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/projects/"+projectInPath.String()+"/attachments/"+attachmentID.String(), bytes.NewBufferString(`{"file_name":"new-name.png"}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(withProjectAttachmentRouteParams(req.Context(), projectInPath, attachmentID))
	rr := httptest.NewRecorder()

	handler.Update(rr, req)

	require.Equal(t, http.StatusNotFound, rr.Code, "Update should reject attachment IDs that do not belong to the project in the path")
	require.Contains(t, rr.Body.String(), "NOT_FOUND", "Update should return a not found error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestProjectAttachmentHandler_DeleteRejectsMismatchedProjectPath(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	handler := NewProjectAttachmentHandler(db.NewProjectAttachmentStore(mock), nil)
	orgID := uuid.New()
	projectInPath := uuid.New()
	attachmentProjectID := uuid.New()
	attachmentID := uuid.New()
	uploadedBy := uuid.New()
	now := time.Now()

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

	require.Equal(t, http.StatusNotFound, rr.Code, "Delete should reject attachment IDs that do not belong to the project in the path")
	require.Contains(t, rr.Body.String(), "NOT_FOUND", "Delete should return a not found error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
