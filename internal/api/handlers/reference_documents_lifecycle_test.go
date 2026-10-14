package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func referenceLifecycleRows(doc models.ReferenceDocument) *pgxmock.Rows {
	return pgxmock.NewRows(pmDocTestColumns).AddRow(doc.ID, doc.OrgID, doc.Title, doc.Content, doc.DocType, doc.SortOrder, doc.SourceType, doc.SourceURL, doc.SourceID, doc.SourceMeta, doc.LastSyncedAt, doc.Active, doc.LogicalID, doc.ContentHash, doc.CreatedBy, doc.CreatedAt, doc.UpdatedAt)
}

func referenceLifecycleRequest(orgID, docID uuid.UUID, user *models.User, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/reference-documents", strings.NewReader(body))
	route := chi.NewRouteContext()
	route.URLParams.Add("docId", docID.String())
	route.URLParams.Add("pinId", docID.String())
	ctx := middleware.WithUser(middleware.WithOrgID(r.Context(), orgID), user)
	return r.WithContext(context.WithValue(ctx, chi.RouteCtxKey, route))
}

func referenceLifecycleArgs(doc models.ReferenceDocument) pgx.NamedArgs {
	return pgx.NamedArgs{"org_id": doc.OrgID, "title": doc.Title, "content": doc.Content, "doc_type": doc.DocType, "sort_order": doc.SortOrder, "source_type": doc.SourceType, "source_url": doc.SourceURL, "source_id": doc.SourceID, "source_meta": doc.SourceMeta, "last_synced_at": doc.LastSyncedAt, "content_hash": doc.ContentHash, "created_by": doc.CreatedBy}
}

func TestReferenceDocumentCreateAndUpdateContracts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		update bool
		body   string
		custom bool
	}{
		{name: "create uses defaults", body: `{"title":"Roadmap"}`},
		{name: "create retains source provenance", body: `{"title":"Roadmap","content":"Context","doc_type":"context","source_type":"notion","source_url":"https://example.test/doc","source_id":"source-1","source_meta":{"author":"team"}}`, custom: true},
		{name: "update retains omitted fields", body: `{"title":"Renamed"}`, update: true},
		{name: "update replaces every provided field", body: `{"title":"Renamed","content":"Context","doc_type":"context","source_type":"notion","source_url":"https://example.test/doc","source_id":"source-1","source_meta":{"author":"team"}}`, update: true, custom: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "reference document fixture should initialize")
			defer mock.Close()
			orgID, oldID, newID, logicalID, userID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
			original := models.ReferenceDocument{ID: oldID, OrgID: orgID, LogicalID: logicalID, Title: "Roadmap", Content: "Original", DocType: "roadmap", SourceType: models.ReferenceDocSourceManual, Active: true, SortOrder: 3, CreatedBy: &userID, CreatedAt: now, UpdatedAt: now}
			hash := sha256.Sum256([]byte(original.Content))
			original.ContentHash = hex.EncodeToString(hash[:])
			expected := models.ReferenceDocument{ID: newID, OrgID: orgID, LogicalID: logicalID, Title: "Roadmap", DocType: "roadmap", SourceType: models.ReferenceDocSourceManual, Active: true, CreatedBy: &userID, CreatedAt: now, UpdatedAt: now}
			if tt.update {
				expected = original
				expected.ID, expected.Title = newID, "Renamed"
				mock.ExpectQuery(`FROM reference_documents WHERE id = @id AND org_id = @org_id`).WithArgs(pgx.NamedArgs{"id": oldID, "org_id": orgID}).WillReturnRows(referenceLifecycleRows(original))
				mock.ExpectBegin()
				mock.ExpectQuery(`FROM reference_documents WHERE id = @id AND org_id = @org_id AND active = true FOR UPDATE`).WithArgs(pgx.NamedArgs{"id": oldID, "org_id": orgID}).WillReturnRows(referenceLifecycleRows(original))
				mock.ExpectExec(`UPDATE reference_documents SET active = false WHERE id = @id AND org_id = @org_id`).WithArgs(pgx.NamedArgs{"id": oldID, "org_id": orgID}).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			}
			if tt.custom {
				url, sourceID := "https://example.test/doc", "source-1"
				expected.Content, expected.DocType, expected.SourceType = "Context", "context", models.ReferenceDocSourceNotion
				expected.SourceURL, expected.SourceID, expected.SourceMeta = &url, &sourceID, json.RawMessage(`{"author":"team"}`)
			}
			hash = sha256.Sum256([]byte(expected.Content))
			expected.ContentHash = hex.EncodeToString(hash[:])
			args := referenceLifecycleArgs(expected)
			insert := mock.ExpectQuery(`INSERT INTO reference_documents`)
			if tt.update {
				args["logical_id"] = logicalID
				insert.WithArgs(args).WillReturnRows(referenceLifecycleRows(expected))
				mock.ExpectCommit()
			} else {
				insert.WithArgs(args).WillReturnRows(pgxmock.NewRows([]string{"id", "logical_id", "created_at", "updated_at"}).AddRow(newID, logicalID, now, now))
			}
			handler := NewReferenceDocumentHandler(db.NewReferenceDocumentStore(mock), nil)
			// Authenticated tenant context may differ from a user's default tenant.
			req := referenceLifecycleRequest(orgID, oldID, &models.User{ID: userID, OrgID: uuid.New()}, tt.body)
			rr := httptest.NewRecorder()
			status := http.StatusCreated
			if tt.update {
				handler.Update(rr, req)
				status = http.StatusOK
			} else {
				handler.Create(rr, req)
			}
			require.Equal(t, status, rr.Code, "document write should return the correct success status: %s", rr.Body.String())
			var response models.SingleResponse[models.ReferenceDocument]
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "document write should return a complete API document")
			require.Equal(t, models.SingleResponse[models.ReferenceDocument]{Data: expected}, response, "document write should preserve omitted values, provenance and the authenticated tenant")
			require.NoError(t, mock.ExpectationsWereMet(), "writes should use the exact tenant and version transaction")
		})
	}
}

func TestReferenceDocumentListAndPinReadContracts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		kind  string
		empty bool
	}{
		{name: "documents", kind: "documents"},
		{name: "empty documents normalize to array", kind: "documents", empty: true},
		{name: "pins", kind: "pins"},
		{name: "empty pins normalize to array", kind: "pins", empty: true},
		{name: "pin resolves exact historical document", kind: "members"},
		{name: "empty pin documents normalize to array", kind: "members", empty: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "reference read fixture should initialize")
			defer mock.Close()
			orgID, pinID := uuid.New(), uuid.New()
			now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
			doc := models.ReferenceDocument{ID: uuid.New(), OrgID: orgID, LogicalID: uuid.New(), Title: "Pinned", Content: "Historical context", DocType: "context", SourceType: "manual", Active: false, CreatedAt: now, UpdatedAt: now}
			pin := models.ReferenceContextSetPin{ID: pinID, OrgID: orgID, CreatedAt: now}
			docs := []models.ReferenceDocument{}
			pins := []models.ReferenceContextSetPin{}
			if !tt.empty {
				docs, pins = []models.ReferenceDocument{doc}, []models.ReferenceContextSetPin{pin}
			}
			var expected any
			switch tt.kind {
			case "documents":
				rows := pgxmock.NewRows(pmDocTestColumns)
				if !tt.empty {
					rows = referenceLifecycleRows(doc)
				}
				mock.ExpectQuery(`FROM reference_documents WHERE org_id = @org_id AND active = true`).WithArgs(pgx.NamedArgs{"org_id": orgID}).WillReturnRows(rows)
				expected = models.ListResponse[models.ReferenceDocument]{Data: docs}
			case "pins":
				rows := pgxmock.NewRows([]string{"id", "org_id", "created_at"})
				if !tt.empty {
					rows.AddRow(pin.ID, pin.OrgID, pin.CreatedAt)
				}
				mock.ExpectQuery(`FROM reference_context_set_pins WHERE org_id = @org_id ORDER BY created_at DESC LIMIT @limit`).WithArgs(pgx.NamedArgs{"org_id": orgID, "limit": 100}).WillReturnRows(rows)
				expected = models.ListResponse[models.ReferenceContextSetPin]{Data: pins}
			case "members":
				mock.ExpectQuery(`FROM reference_context_set_pins WHERE id = @id AND org_id = @org_id`).WithArgs(pgx.NamedArgs{"id": pinID, "org_id": orgID}).WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "created_at"}).AddRow(pin.ID, pin.OrgID, pin.CreatedAt))
				rows := pgxmock.NewRows(pmDocTestColumns)
				if !tt.empty {
					rows = referenceLifecycleRows(doc)
				}
				mock.ExpectQuery(`(?s)FROM reference_documents d.*WHERE m.pin_id = @pin_id AND d.org_id = @org_id`).WithArgs(pgx.NamedArgs{"pin_id": pinID, "org_id": orgID}).WillReturnRows(rows)
				expected = map[string]any{"pin": pin, "documents": docs}
			}
			handler := NewReferenceDocumentHandler(db.NewReferenceDocumentStore(mock), nil)
			rr := httptest.NewRecorder()
			req := referenceLifecycleRequest(orgID, pinID, nil, "")
			switch tt.kind {
			case "documents":
				handler.List(rr, req)
			case "pins":
				handler.ListDocumentSetPins(rr, req)
			case "members":
				handler.GetDocumentSetPin(rr, req)
			}
			require.Equal(t, http.StatusOK, rr.Code, "document reads should return complete successful responses")
			encoded, err := json.Marshal(expected)
			require.NoError(t, err, "expected document API response should encode")
			require.JSONEq(t, string(encoded), rr.Body.String(), "reads should return exact tenant data and normalize empty collections")
			require.NoError(t, mock.ExpectationsWereMet(), "document and pin reads should retain the authenticated tenant")
		})
	}
}

func TestReferenceDocumentFailuresKeepAPIErrorContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		action  string
		body    string
		invalid bool
		query   string
		status  int
		code    string
		message string
	}{
		{name: "invalid create body", action: "create", body: `{`, status: http.StatusBadRequest, code: "INVALID_JSON", message: "invalid request body"},
		{name: "missing create title", action: "create", body: `{}`, status: http.StatusBadRequest, code: "MISSING_FIELD", message: "title is required"},
		{name: "create dependency failure", action: "create", body: `{"title":"Roadmap"}`, query: `INSERT INTO reference_documents`, status: http.StatusInternalServerError, code: "CREATE_FAILED", message: "failed to create reference document"},
		{name: "list dependency failure", action: "list", query: `FROM reference_documents`, status: http.StatusInternalServerError, code: "LIST_FAILED", message: "failed to list reference documents"},
		{name: "get invalid id", action: "get", invalid: true, status: http.StatusBadRequest, code: "INVALID_ID", message: "invalid document ID"},
		{name: "get missing document", action: "get", query: `FROM reference_documents`, status: http.StatusNotFound, code: "NOT_FOUND", message: "document not found"},
		{name: "update missing document", action: "update", query: `FROM reference_documents`, status: http.StatusNotFound, code: "NOT_FOUND", message: "document not found"},
		{name: "delete invalid id", action: "delete", invalid: true, status: http.StatusBadRequest, code: "INVALID_ID", message: "invalid document ID"},
		{name: "delete missing document", action: "delete", query: `FROM reference_documents`, status: http.StatusNotFound, code: "NOT_FOUND", message: "document not found"},
		{name: "version listing dependency failure", action: "versions", query: `FROM reference_documents`, status: http.StatusInternalServerError, code: "LIST_VERSIONS_FAILED", message: "failed to list document versions"},
		{name: "pin listing dependency failure", action: "pins", query: `FROM reference_context_set_pins`, status: http.StatusInternalServerError, code: "LIST_PINS_FAILED", message: "failed to list document set pins"},
		{name: "get pin invalid id", action: "pin", invalid: true, status: http.StatusBadRequest, code: "INVALID_ID", message: "invalid pin ID"},
		{name: "get missing pin", action: "pin", query: `FROM reference_context_set_pins`, status: http.StatusNotFound, code: "NOT_FOUND", message: "pin not found"},
		{name: "restore invalid JSON", action: "restore", body: `{`, status: http.StatusBadRequest, code: "INVALID_JSON", message: "invalid request body"},
		{name: "restore missing source", action: "restore", body: `{}`, status: http.StatusBadRequest, code: "MISSING_FIELD", message: "restore_from_id is required"},
		{name: "discovery without credentials", action: "discover", status: http.StatusServiceUnavailable, code: "NOT_CONFIGURED", message: "credential store not available"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "error contract fixture should initialize")
			defer mock.Close()
			orgID, docID, userID := uuid.New(), uuid.New(), uuid.New()
			if tt.query != "" {
				args := pgx.NamedArgs{"org_id": orgID, "id": docID}
				switch tt.action {
				case "create":
					hash := sha256.Sum256(nil)
					args = referenceLifecycleArgs(models.ReferenceDocument{OrgID: orgID, Title: "Roadmap", DocType: "roadmap", SourceType: models.ReferenceDocSourceManual, ContentHash: hex.EncodeToString(hash[:]), CreatedBy: &userID})
				case "list":
					args = pgx.NamedArgs{"org_id": orgID}
				case "versions":
					args["limit"] = 100
				case "pins":
					args = pgx.NamedArgs{"org_id": orgID, "limit": 100}
				}
				mock.ExpectQuery(tt.query).WithArgs(args).WillReturnError(errors.New("private database diagnostic"))
			}
			h := NewReferenceDocumentHandler(db.NewReferenceDocumentStore(mock), nil)
			actions := map[string]http.HandlerFunc{"create": h.Create, "list": h.List, "get": h.Get, "update": h.Update, "delete": h.Delete, "versions": h.ListVersions, "pins": h.ListDocumentSetPins, "pin": h.GetDocumentSetPin, "restore": h.RestoreVersion, "discover": h.DiscoverNotion}
			req := referenceLifecycleRequest(orgID, docID, &models.User{ID: userID}, tt.body)
			if tt.invalid {
				route := chi.NewRouteContext()
				route.URLParams.Add("docId", "invalid")
				route.URLParams.Add("pinId", "invalid")
				req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
			}
			rr := httptest.NewRecorder()
			actions[tt.action](rr, req)
			require.Equal(t, tt.status, rr.Code, "error should preserve the endpoint's documented status")
			var response models.ErrorResponse
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &response), "errors should use the standard JSON response")
			require.Equal(t, models.ErrorResponse{Error: models.ErrorDetail{Code: tt.code, Message: tt.message}}, response, "API errors should preserve stable codes without leaking dependency diagnostics")
			require.NoError(t, mock.ExpectationsWereMet(), "invalid requests must stop before unexpected dependency calls")
		})
	}
}
