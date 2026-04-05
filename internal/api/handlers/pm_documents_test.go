package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPMDocumentHandler_ListVersions(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewPMDocumentStore(mock)
	handler := NewPMDocumentHandler(store, nil)

	orgID := uuid.New()
	docID := uuid.New()
	logicalID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM pm_documents").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestColumns).
				AddRow(uuid.New(), orgID, "V2", "v2 content", "roadmap", 0,
					"manual", nil, nil, nil, nil,
					true, logicalID, "",
					nil, now, now).
				AddRow(docID, orgID, "V1", "v1 content", "roadmap", 0,
					"manual", nil, nil, nil, nil,
					false, logicalID, "",
					nil, now.Add(-time.Hour), now.Add(-time.Hour)),
		)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("docId", docID.String())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/pm/documents/"+docID.String()+"/versions", nil)
	req = req.WithContext(context.WithValue(middleware.WithOrgID(req.Context(), orgID), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handler.ListVersions(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	var resp models.ListResponse[models.PMDocument]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 2)
	require.True(t, resp.Data[0].Active)
	require.False(t, resp.Data[1].Active)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPMDocumentHandler_ListVersions_WithLimit(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewPMDocumentStore(mock)
	handler := NewPMDocumentHandler(store, nil)

	orgID := uuid.New()
	docID := uuid.New()
	logicalID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM pm_documents").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestColumns).
				AddRow(uuid.New(), orgID, "V2", "v2", "roadmap", 0,
					"manual", nil, nil, nil, nil,
					true, logicalID, "",
					nil, now, now),
		)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("docId", docID.String())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/pm/documents/"+docID.String()+"/versions?limit=1", nil)
	req = req.WithContext(context.WithValue(middleware.WithOrgID(req.Context(), orgID), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handler.ListVersions(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	var resp models.ListResponse[models.PMDocument]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPMDocumentHandler_RestoreVersion(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewPMDocumentStore(mock)
	handler := NewPMDocumentHandler(store, nil)

	orgID := uuid.New()
	activeDocID := uuid.New()
	oldVersionID := uuid.New()
	restoredID := uuid.New()
	logicalID := uuid.New()
	now := time.Now()

	// GetByID for the URL param doc.
	mock.ExpectQuery("SELECT .+ FROM pm_documents WHERE id .+ AND org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestColumns).
				AddRow(activeDocID, orgID, "Current", "current content", "roadmap", 0,
					"manual", nil, nil, nil, nil,
					true, logicalID, "",
					nil, now, now),
		)

	// GetByID for restore_from_id validation.
	mock.ExpectQuery("SELECT .+ FROM pm_documents WHERE id .+ AND org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestColumns).
				AddRow(oldVersionID, orgID, "Old", "old content", "roadmap", 0,
					"manual", nil, nil, nil, nil,
					false, logicalID, "",
					nil, now.Add(-time.Hour), now.Add(-time.Hour)),
		)

	// GetActiveByLogicalID.
	mock.ExpectQuery("SELECT .+ FROM pm_documents WHERE org_id .+ AND logical_id .+ AND active = true").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestColumns).
				AddRow(activeDocID, orgID, "Current", "current content", "roadmap", 0,
					"manual", nil, nil, nil, nil,
					true, logicalID, "",
					nil, now, now),
		)

	// Restore transaction.
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .+ FROM pm_documents WHERE id .+ AND org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestColumns).
				AddRow(oldVersionID, orgID, "Old", "old content", "roadmap", 0,
					"manual", nil, nil, nil, nil,
					false, logicalID, "",
					nil, now.Add(-time.Hour), now.Add(-time.Hour)),
		)
	mock.ExpectQuery("UPDATE pm_documents SET active = false").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"logical_id"}).AddRow(logicalID))
	mock.ExpectQuery("INSERT INTO pm_documents").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestColumns).
				AddRow(restoredID, orgID, "Old", "old content", "roadmap", 0,
					"manual", nil, nil, nil, nil,
					true, logicalID, "",
					nil, now, now),
		)
	mock.ExpectCommit()

	body := `{"restore_from_id":"` + oldVersionID.String() + `"}`
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("docId", activeDocID.String())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pm/documents/"+activeDocID.String()+"/restore", strings.NewReader(body))
	req = req.WithContext(context.WithValue(middleware.WithOrgID(req.Context(), orgID), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handler.RestoreVersion(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	require.Contains(t, rr.Body.String(), "old content")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPMDocumentHandler_RestoreVersion_LogicalIDMismatch(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewPMDocumentStore(mock)
	handler := NewPMDocumentHandler(store, nil)

	orgID := uuid.New()
	docID := uuid.New()
	otherDocID := uuid.New()
	logicalA := uuid.New()
	logicalB := uuid.New()
	now := time.Now()

	// GetByID for URL param doc.
	mock.ExpectQuery("SELECT .+ FROM pm_documents WHERE id .+ AND org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestColumns).
				AddRow(docID, orgID, "Doc A", "content a", "roadmap", 0,
					"manual", nil, nil, nil, nil,
					true, logicalA, "",
					nil, now, now),
		)

	// GetByID for restore_from_id — belongs to different logical document.
	mock.ExpectQuery("SELECT .+ FROM pm_documents WHERE id .+ AND org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestColumns).
				AddRow(otherDocID, orgID, "Doc B", "content b", "roadmap", 0,
					"manual", nil, nil, nil, nil,
					false, logicalB, "",
					nil, now, now),
		)

	body := `{"restore_from_id":"` + otherDocID.String() + `"}`
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("docId", docID.String())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pm/documents/"+docID.String()+"/restore", strings.NewReader(body))
	req = req.WithContext(context.WithValue(middleware.WithOrgID(req.Context(), orgID), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handler.RestoreVersion(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "LOGICAL_ID_MISMATCH")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPMDocumentHandler_CreateDocumentSetPin(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewPMDocumentStore(mock)
	handler := NewPMDocumentHandler(store, nil)

	orgID := uuid.New()
	pinID := uuid.New()
	now := time.Now()

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO pm_document_set_pins").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "created_at"}).AddRow(pinID, orgID, now))
	mock.ExpectExec("INSERT INTO pm_document_set_pin_members").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 2))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/pm/document-set-pins", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.CreateDocumentSetPin(rr, req)

	require.Equal(t, http.StatusCreated, rr.Code, "body: %s", rr.Body.String())
	require.Contains(t, rr.Body.String(), pinID.String())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPMDocumentHandler_Update_InactiveVersion(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewPMDocumentStore(mock)
	handler := NewPMDocumentHandler(store, nil)

	orgID := uuid.New()
	docID := uuid.New()
	logicalID := uuid.New()
	now := time.Now()

	// GetByID returns an inactive version.
	mock.ExpectQuery("SELECT .+ FROM pm_documents WHERE id .+ AND org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(pmDocTestColumns).
				AddRow(docID, orgID, "Old version", "old content", "roadmap", 0,
					"manual", nil, nil, nil, nil,
					false, logicalID, "",
					nil, now, now),
		)

	body := `{"title":"New Title"}`
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("docId", docID.String())
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/pm/documents/"+docID.String(), strings.NewReader(body))
	req = req.WithContext(context.WithValue(middleware.WithOrgID(req.Context(), orgID), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handler.Update(rr, req)

	require.Equal(t, http.StatusConflict, rr.Code)
	require.Contains(t, rr.Body.String(), "INACTIVE_VERSION")
	require.NoError(t, mock.ExpectationsWereMet())
}
