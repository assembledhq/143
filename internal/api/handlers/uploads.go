package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/storage"
)

// maxUploadSize is the maximum allowed file size (10 MB).
const maxUploadSize = 10 << 20

// allowedUploadTypes are the MIME types accepted for upload.
var allowedUploadTypes = map[string]bool{
	"image/png":           true,
	"image/jpeg":          true,
	"image/gif":           true,
	"image/webp":          true,
	"image/heic":          true,
	"image/heif":          true,
	"image/heic-sequence": true,
	"image/heif-sequence": true,
	"image/svg+xml":       true,
	"application/pdf":     true,
	"text/plain":          true,
	"text/markdown":       true,
	"text/csv":            true,
	"application/json":    true,
}

type heicConverterFunc func(context.Context, []byte) ([]byte, error)

// uploadMembershipStore is the subset of OrganizationMembershipStore that
// ServeUpload needs to authorize file reads. Defined as an interface so tests
// can stub it without spinning up a real Postgres connection.
type uploadMembershipStore interface {
	Get(ctx context.Context, userID, orgID uuid.UUID) (models.OrganizationMembership, error)
}

// UploadHandler handles file uploads to object storage.
type UploadHandler struct {
	store         storage.UploadStore
	memberships   uploadMembershipStore
	heicConverter heicConverterFunc
}

// NewUploadHandler creates an UploadHandler backed by the given store.
func NewUploadHandler(store storage.UploadStore) *UploadHandler {
	return &UploadHandler{store: store, heicConverter: convertHEICToJPEGWithCommand}
}

// SetMembershipStore wires the membership store used by ServeUpload to
// authorize cross-org reads. Required for ServeUpload to authorize requests
// that arrive without an X-Active-Org-ID header (e.g. <img> tag fetches).
func (h *UploadHandler) SetMembershipStore(store uploadMembershipStore) {
	h.memberships = store
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
	baseType = normalizeUploadContentType(baseType, header.Filename)

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
	reader := io.Reader(file)
	fileName := header.Filename
	if isHEICUpload(baseType) {
		data, err := io.ReadAll(io.LimitReader(file, maxUploadSize+1))
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "failed to read uploaded HEIC file", err)
			return
		}
		if len(data) > maxUploadSize {
			writeError(w, r, http.StatusRequestEntityTooLarge, "FILE_TOO_LARGE", "file size exceeds 10MB limit")
			return
		}
		converted, err := h.heicConverter(r.Context(), data)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "HEIC_CONVERSION_FAILED", "failed to convert HEIC image to JPEG", err)
			return
		}
		reader = bytes.NewReader(converted)
		baseType = "image/jpeg"
		ext = ".jpg"
		fileName = replaceExtension(header.Filename, ".jpg")
	}
	now := time.Now().UTC()
	key := fmt.Sprintf("%s/%s/%s%s",
		orgID.String(),
		now.Format("2006-01"),
		uuid.New().String(),
		ext,
	)

	url, err := h.store.Save(r.Context(), key, reader, baseType)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPLOAD_FAILED", "failed to store file", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"url":          url,
		"file_name":    fileName,
		"content_type": baseType,
	})
}

// ServeUpload handles GET /api/v1/uploads/files/* — serves uploaded files.
// Works with both local filesystem and S3 storage backends.
//
// Authorization: file URLs are loaded by browser mechanisms (<img>, <a>) that
// cannot send the X-Active-Org-ID header, so the auth middleware's resolved
// active-org context may not match the file's owning org for multi-org users.
// Instead, we parse the org-id from the path itself and verify the requesting
// user holds a membership in that org. Cross-org access still 403s.
func (h *UploadHandler) ServeUpload(w http.ResponseWriter, r *http.Request) {
	// Extract the file key from the URL path after /api/v1/uploads/files/
	key := strings.TrimPrefix(r.URL.Path, "/api/v1/uploads/files/")
	if key == "" || strings.Contains(key, "..") {
		writeError(w, r, http.StatusBadRequest, "INVALID_KEY", "invalid file key")
		return
	}

	// File keys are formatted as `<orgID>/<YYYY-MM>/<uuid>.<ext>` (see Upload).
	// The first path segment is the owning org's UUID; reject anything else.
	prefix, _, hasSlash := strings.Cut(key, "/")
	if !hasSlash {
		writeError(w, r, http.StatusBadRequest, "INVALID_KEY", "invalid file key")
		return
	}
	pathOrgID, err := uuid.Parse(prefix)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_KEY", "invalid file key")
		return
	}

	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "missing user")
		return
	}
	if h.memberships == nil {
		writeError(w, r, http.StatusInternalServerError, "NOT_CONFIGURED", "membership store not configured")
		return
	}
	if _, err := h.memberships.Get(r.Context(), user.ID, pathOrgID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusForbidden, "FORBIDDEN", "access denied")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INTERNAL", "failed to authorize upload", err)
		return
	}

	h.store.Serve(w, r, key)
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
	case "image/heic":
		return ".heic"
	case "image/heif":
		return ".heif"
	case "image/heic-sequence":
		return ".heic"
	case "image/heif-sequence":
		return ".heif"
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

func isHEICUpload(contentType string) bool {
	return contentType == "image/heic" ||
		contentType == "image/heif" ||
		contentType == "image/heic-sequence" ||
		contentType == "image/heif-sequence"
}

func normalizeUploadContentType(contentType, fileName string) string {
	if contentType == "application/octet-stream" {
		switch strings.ToLower(path.Ext(fileName)) {
		case ".heic":
			return "image/heic"
		case ".heif":
			return "image/heif"
		}
	}
	return contentType
}

func replaceExtension(name, ext string) string {
	base := strings.TrimSuffix(name, path.Ext(name))
	if base == "" {
		base = "upload"
	}
	return base + ext
}

func convertHEICToJPEGWithCommand(ctx context.Context, body []byte) ([]byte, error) {
	tmpDir, err := os.MkdirTemp("", "143-heic-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	inputPath := filepath.Join(tmpDir, "input.heic")
	outputPath := filepath.Join(tmpDir, "output.jpg")
	if err := os.WriteFile(inputPath, body, 0o600); err != nil {
		return nil, fmt.Errorf("write temp HEIC: %w", err)
	}

	if err := runHEICConverter(ctx, inputPath, outputPath); err != nil {
		return nil, err
	}

	converted, err := os.ReadFile(outputPath) // #nosec G304 -- outputPath is created under tmpDir by this function and is not user-controlled
	if err != nil {
		return nil, fmt.Errorf("read converted JPEG: %w", err)
	}
	if len(converted) == 0 {
		return nil, fmt.Errorf("converted JPEG is empty")
	}
	return converted, nil
}

func runHEICConverter(ctx context.Context, inputPath, outputPath string) error {
	return runHEICConverterWithLookPath(ctx, inputPath, outputPath, exec.LookPath)
}

func runHEICConverterWithLookPath(ctx context.Context, inputPath, outputPath string, lookPath func(string) (string, error)) error {
	if _, err := lookPath("heif-convert"); err != nil {
		return fmt.Errorf("missing HEIC converter: install heif-convert")
	}
	cmd := exec.CommandContext(ctx, "heif-convert", inputPath, outputPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("heif-convert failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
