package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type ProjectAttachmentHandler struct {
	attachmentStore *db.ProjectAttachmentStore
	projectStore    *db.ProjectStore
}

func NewProjectAttachmentHandler(
	attachmentStore *db.ProjectAttachmentStore,
	projectStore *db.ProjectStore,
) *ProjectAttachmentHandler {
	return &ProjectAttachmentHandler{
		attachmentStore: attachmentStore,
		projectStore:    projectStore,
	}
}

func (h *ProjectAttachmentHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid project ID")
		return
	}

	attachments, err := h.attachmentStore.ListByProject(r.Context(), orgID, projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "failed to list attachments")
		return
	}
	if attachments == nil {
		attachments = []models.ProjectAttachment{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.ProjectAttachment]{
		Data: attachments,
		Meta: models.PaginationMeta{},
	})
}

func (h *ProjectAttachmentHandler) Create(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid project ID")
		return
	}

	// Verify project exists
	if _, err := h.projectStore.GetByID(r.Context(), orgID, projectID); err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "project not found")
		return
	}

	var req struct {
		FileName     string  `json:"file_name"`
		FileURL      string  `json:"file_url"`
		FileType     *string `json:"file_type"`
		ThumbnailURL *string `json:"thumbnail_url"`
		FileSize     *int    `json:"file_size"`
		Category     *string `json:"category"`
		Caption      *string `json:"caption"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	if req.FileName == "" || req.FileURL == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FIELD", "file_name and file_url are required")
		return
	}

	if !strings.HasPrefix(req.FileURL, "https://") && !strings.HasPrefix(req.FileURL, "http://") {
		writeError(w, http.StatusBadRequest, "INVALID_URL", "file_url must start with https:// or http://")
		return
	}

	fileType := "image"
	if req.FileType != nil {
		fileType = *req.FileType
	}

	category := "screenshot"
	if req.Category != nil {
		category = *req.Category
	}

	attachment := models.ProjectAttachment{
		ProjectID:    projectID,
		OrgID:        orgID,
		FileName:     req.FileName,
		FileURL:      req.FileURL,
		FileType:     fileType,
		ThumbnailURL: req.ThumbnailURL,
		FileSize:     req.FileSize,
		Category:     category,
		Caption:      req.Caption,
		UploadedBy:   &user.ID,
	}

	if err := h.attachmentStore.Create(r.Context(), &attachment); err != nil {
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "failed to create attachment")
		return
	}

	writeJSON(w, http.StatusCreated, models.SingleResponse[models.ProjectAttachment]{Data: attachment})
}

func (h *ProjectAttachmentHandler) Update(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid project ID")
		return
	}
	attachmentID, err := uuid.Parse(chi.URLParam(r, "attachmentId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid attachment ID")
		return
	}

	attachment, err := h.attachmentStore.GetByID(r.Context(), orgID, attachmentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "attachment not found")
		return
	}
	if attachment.ProjectID != projectID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "attachment not found")
		return
	}

	var req struct {
		FileName *string `json:"file_name"`
		Caption  *string `json:"caption"`
		Category *string `json:"category"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	if req.FileName != nil {
		attachment.FileName = *req.FileName
	}
	if req.Caption != nil {
		attachment.Caption = req.Caption
	}
	if req.Category != nil {
		attachment.Category = *req.Category
	}

	if err := h.attachmentStore.Update(r.Context(), &attachment); err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update attachment")
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.ProjectAttachment]{Data: attachment})
}

func (h *ProjectAttachmentHandler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid project ID")
		return
	}
	attachmentID, err := uuid.Parse(chi.URLParam(r, "attachmentId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid attachment ID")
		return
	}
	attachment, err := h.attachmentStore.GetByID(r.Context(), orgID, attachmentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "attachment not found")
		return
	}
	if attachment.ProjectID != projectID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "attachment not found")
		return
	}

	if err := h.attachmentStore.Delete(r.Context(), orgID, attachmentID); err != nil {
		writeError(w, http.StatusInternalServerError, "DELETE_FAILED", "failed to delete attachment")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
