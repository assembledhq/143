package handlers

import (
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/services/storage"
	"github.com/google/uuid"
)

// maxUploadSize is the maximum allowed file size (10 MB).
const maxUploadSize = 10 << 20

// allowedImageTypes are the MIME types accepted for upload.
var allowedUploadTypes = map[string]bool{
	"image/png":      true,
	"image/jpeg":     true,
	"image/gif":      true,
	"image/webp":     true,
	"image/svg+xml":  true,
	"application/pdf": true,
	"text/plain":      true,
	"text/markdown":   true,
	"text/csv":        true,
	"application/json": true,
}

// UploadHandler handles file uploads to object storage.
type UploadHandler struct {
	store storage.UploadStore
}

// NewUploadHandler creates an UploadHandler backed by the given store.
func NewUploadHandler(store storage.UploadStore) *UploadHandler {
	return &UploadHandler{store: store}
}

// Upload handles POST /api/v1/uploads — accepts a multipart file upload,
// stores it, and returns the URL for the uploaded file.
func (h *UploadHandler) Upload(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeError(w, r, http.StatusNotImplemented, "NOT_CONFIGURED", "file uploads not configured")
		return
	}

	// Parse multipart form with 10 MB limit.
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "failed to parse multipart form")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "MISSING_FILE", "file field is required")
		return
	}
	defer file.Close()

	if header.Size > maxUploadSize {
		writeError(w, r, http.StatusRequestEntityTooLarge, "FILE_TOO_LARGE", "file size exceeds 10MB limit")
		return
	}

	// Validate content type.
	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	// Normalize content type (strip charset params).
	baseType := strings.Split(contentType, ";")[0]
	baseType = strings.TrimSpace(baseType)

	if !allowedUploadTypes[baseType] {
		writeError(w, r, http.StatusBadRequest, "INVALID_FILE_TYPE",
			fmt.Sprintf("file type %s is not allowed", baseType))
		return
	}

	// Build a unique key: orgId/YYYY-MM/uuid.ext
	orgID := middleware.OrgIDFromContext(r.Context())
	ext := path.Ext(header.Filename)
	if ext == "" {
		ext = extensionFromMIME(baseType)
	}
	now := time.Now().UTC()
	key := fmt.Sprintf("%s/%s/%s%s",
		orgID.String(),
		now.Format("2006-01"),
		uuid.New().String(),
		ext,
	)

	url, err := h.store.Save(r.Context(), key, file, baseType)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPLOAD_FAILED", "failed to store file", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"url":          url,
		"file_name":    header.Filename,
		"content_type": baseType,
	})
}

// ServeUpload handles GET /api/v1/uploads/files/* — serves locally-stored uploads.
// Only functional when using FileUploadStore.
func (h *UploadHandler) ServeUpload(w http.ResponseWriter, r *http.Request) {
	fileStore, ok := h.store.(*storage.FileUploadStore)
	if !ok {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "file serving not available in S3 mode")
		return
	}

	// Extract the file key from the URL path after /api/v1/uploads/files/
	key := strings.TrimPrefix(r.URL.Path, "/api/v1/uploads/files/")
	if key == "" || strings.Contains(key, "..") {
		writeError(w, r, http.StatusBadRequest, "INVALID_KEY", "invalid file key")
		return
	}

	// Validate that the file belongs to the requesting user's org.
	orgID := middleware.OrgIDFromContext(r.Context())
	if !strings.HasPrefix(key, orgID.String()+"/") {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "access denied")
		return
	}

	fileStore.ServeFile(w, r, key)
}

func extensionFromMIME(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "text/markdown":
		return ".md"
	case "text/csv":
		return ".csv"
	case "application/json":
		return ".json"
	default:
		return ""
	}
}
