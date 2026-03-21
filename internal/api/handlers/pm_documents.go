package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/integration"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type PMDocumentHandler struct {
	store       *db.PMDocumentStore
	credentials *db.OrgCredentialStore
}

func NewPMDocumentHandler(store *db.PMDocumentStore, credentials *db.OrgCredentialStore) *PMDocumentHandler {
	return &PMDocumentHandler{store: store, credentials: credentials}
}

func (h *PMDocumentHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	docs, err := h.store.ListByOrg(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list PM documents", err)
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
		Title      string          `json:"title"`
		Content    *string         `json:"content"`
		DocType    *string         `json:"doc_type"`
		SourceType *string         `json:"source_type"`
		SourceURL  *string         `json:"source_url"`
		SourceID   *string         `json:"source_id"`
		SourceMeta json.RawMessage `json:"source_meta,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	if req.Title == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "title is required")
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
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create PM document", err)
		return
	}

	writeJSON(w, http.StatusCreated, models.SingleResponse[models.PMDocument]{Data: doc})
}

func (h *PMDocumentHandler) Get(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	docID, err := uuid.Parse(chi.URLParam(r, "docId"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid document ID")
		return
	}

	doc, err := h.store.GetByID(r.Context(), orgID, docID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "document not found")
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.PMDocument]{Data: doc})
}

func (h *PMDocumentHandler) Update(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	docID, err := uuid.Parse(chi.URLParam(r, "docId"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid document ID")
		return
	}

	doc, err := h.store.GetByID(r.Context(), orgID, docID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "document not found")
		return
	}

	var req struct {
		Title      *string         `json:"title"`
		Content    *string         `json:"content"`
		DocType    *string         `json:"doc_type"`
		SourceType *string         `json:"source_type"`
		SourceURL  *string         `json:"source_url"`
		SourceID   *string         `json:"source_id"`
		SourceMeta json.RawMessage `json:"source_meta,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
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
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update PM document", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.PMDocument]{Data: doc})
}

func (h *PMDocumentHandler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	docID, err := uuid.Parse(chi.URLParam(r, "docId"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid document ID")
		return
	}

	if _, err := h.store.GetByID(r.Context(), orgID, docID); err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "document not found")
		return
	}

	if err := h.store.Delete(r.Context(), orgID, docID); err != nil {
		writeError(w, r, http.StatusInternalServerError, "DELETE_FAILED", "failed to delete PM document", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// getNotionStore returns a configured NotionDocumentStore for the org, or
// writes an error response and returns nil if Notion is not configured.
func (h *PMDocumentHandler) getNotionStore(w http.ResponseWriter, r *http.Request, orgID uuid.UUID) *integration.NotionDocumentStore {
	if h.credentials == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_CONFIGURED", "credential store not available")
		return nil
	}

	cred, err := h.credentials.Get(r.Context(), orgID, models.ProviderNotion)
	if err != nil || cred == nil {
		writeError(w, r, http.StatusNotFound, "NOTION_NOT_CONFIGURED", "Notion integration is not configured for this organization")
		return nil
	}

	cfg, ok := cred.Config.(models.NotionConfig)
	if !ok || cfg.AccessToken == "" {
		writeError(w, r, http.StatusNotFound, "NOTION_NOT_CONFIGURED", "Notion integration is not configured for this organization")
		return nil
	}

	return integration.NewNotionDocumentStore(integration.NotionDocumentStoreConfig{
		AuthToken: cfg.AccessToken,
	})
}

// SyncFromNotion re-fetches a PM document's content from Notion using its
// source_id (Notion page ID). Updates the local copy with fresh content.
func (h *PMDocumentHandler) SyncFromNotion(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	docID, err := uuid.Parse(chi.URLParam(r, "docId"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid document ID")
		return
	}

	doc, err := h.store.GetByID(r.Context(), orgID, docID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "document not found")
		return
	}

	if doc.SourceType != models.PMDocSourceNotion || doc.SourceID == nil || *doc.SourceID == "" {
		writeError(w, r, http.StatusBadRequest, "NOT_NOTION_SOURCE", "document is not sourced from Notion")
		return
	}

	store := h.getNotionStore(w, r, orgID)
	if store == nil {
		return // error already written
	}

	notionDoc, err := store.GetDocument(r.Context(), *doc.SourceID)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "NOTION_FETCH_FAILED", "failed to fetch document from Notion", err)
		return
	}

	// Update the local copy.
	doc.Title = notionDoc.Title
	doc.Content = notionDoc.Content
	now := time.Now()
	doc.LastSyncedAt = &now
	doc.SourceURL = &notionDoc.WebURL

	// Store Notion metadata.
	meta, _ := json.Marshal(map[string]interface{}{
		"last_edited": notionDoc.LastEdited,
		"author":      notionDoc.Author,
		"properties":  notionDoc.Properties,
	})
	doc.SourceMeta = meta

	if err := h.store.Update(r.Context(), &doc); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update PM document", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.PMDocument]{Data: doc})
}

// DiscoverNotion searches the org's Notion workspace for product-relevant
// documents (roadmaps, strategy, OKRs, etc.) and returns summaries. Users
// can then select which ones to import as PM documents.
func (h *PMDocumentHandler) DiscoverNotion(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	store := h.getNotionStore(w, r, orgID)
	if store == nil {
		return // error already written
	}

	// Product-relevant search queries.
	queries := []string{
		"roadmap",
		"product direction",
		"strategy",
		"OKR",
		"vision",
		"product requirements",
		"architecture",
		"RFC",
	}

	seen := make(map[string]bool)
	var results []integration.DocSummary

	for _, q := range queries {
		docs, err := store.SearchDocuments(r.Context(), q, integration.DocFilter{Limit: 10})
		if err != nil {
			// Log but continue with other queries.
			continue
		}
		for _, doc := range docs {
			if !seen[doc.ID] {
				seen[doc.ID] = true
				results = append(results, doc)
			}
		}
	}

	if results == nil {
		results = []integration.DocSummary{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[integration.DocSummary]{
		Data: results,
		Meta: models.PaginationMeta{},
	})
}
