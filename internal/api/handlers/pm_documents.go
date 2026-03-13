package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type PMDocumentHandler struct {
	store *db.PMDocumentStore
}

func NewPMDocumentHandler(store *db.PMDocumentStore) *PMDocumentHandler {
	return &PMDocumentHandler{store: store}
}

func (h *PMDocumentHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	docs, err := h.store.ListByOrg(r.Context(), orgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "failed to list PM documents")
		return
	}
	if docs == nil {
		docs = []models.PMDocument{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.PMDocument]{
		Data: docs,
		Meta: models.PaginationMeta{},
	})
}

func (h *PMDocumentHandler) Create(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())

	var req struct {
		Title      string           `json:"title"`
		Content    *string          `json:"content"`
		DocType    *string          `json:"doc_type"`
		SourceType *string          `json:"source_type"`
		SourceURL  *string          `json:"source_url"`
		SourceID   *string          `json:"source_id"`
		SourceMeta json.RawMessage  `json:"source_meta,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	if req.Title == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FIELD", "title is required")
		return
	}

	docType := "roadmap"
	if req.DocType != nil {
		docType = *req.DocType
	}

	content := ""
	if req.Content != nil {
		content = *req.Content
	}

	sourceType := models.PMDocSourceManual
	if req.SourceType != nil {
		sourceType = *req.SourceType
	}

	doc := models.PMDocument{
		OrgID:      orgID,
		Title:      req.Title,
		Content:    content,
		DocType:    docType,
		SourceType: sourceType,
		SourceURL:  req.SourceURL,
		SourceID:   req.SourceID,
		SourceMeta: req.SourceMeta,
		CreatedBy:  &user.ID,
	}

	if err := h.store.Create(r.Context(), &doc); err != nil {
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "failed to create PM document")
		return
	}

	writeJSON(w, http.StatusCreated, models.SingleResponse[models.PMDocument]{Data: doc})
}

func (h *PMDocumentHandler) Get(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	docID, err := uuid.Parse(chi.URLParam(r, "docId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid document ID")
		return
	}

	doc, err := h.store.GetByID(r.Context(), orgID, docID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "document not found")
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.PMDocument]{Data: doc})
}

func (h *PMDocumentHandler) Update(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	docID, err := uuid.Parse(chi.URLParam(r, "docId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid document ID")
		return
	}

	doc, err := h.store.GetByID(r.Context(), orgID, docID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "document not found")
		return
	}

	var req struct {
		Title      *string          `json:"title"`
		Content    *string          `json:"content"`
		DocType    *string          `json:"doc_type"`
		SourceType *string          `json:"source_type"`
		SourceURL  *string          `json:"source_url"`
		SourceID   *string          `json:"source_id"`
		SourceMeta json.RawMessage  `json:"source_meta,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	if req.Title != nil {
		doc.Title = *req.Title
	}
	if req.Content != nil {
		doc.Content = *req.Content
	}
	if req.DocType != nil {
		doc.DocType = *req.DocType
	}
	if req.SourceType != nil {
		doc.SourceType = *req.SourceType
	}
	if req.SourceURL != nil {
		doc.SourceURL = req.SourceURL
	}
	if req.SourceID != nil {
		doc.SourceID = req.SourceID
	}
	if req.SourceMeta != nil {
		doc.SourceMeta = req.SourceMeta
	}

	if err := h.store.Update(r.Context(), &doc); err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update PM document")
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.PMDocument]{Data: doc})
}

func (h *PMDocumentHandler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	docID, err := uuid.Parse(chi.URLParam(r, "docId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid document ID")
		return
	}

	if _, err := h.store.GetByID(r.Context(), orgID, docID); err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "document not found")
		return
	}

	if err := h.store.Delete(r.Context(), orgID, docID); err != nil {
		writeError(w, http.StatusInternalServerError, "DELETE_FAILED", "failed to delete PM document")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
