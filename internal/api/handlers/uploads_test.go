package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/services/storage"
)

func newUploadRequest(t *testing.T, fieldName, fileName, contentType string, body []byte) *http.Request {
	t.Helper()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Create a part with the correct Content-Type (CreateFormFile always uses application/octet-stream).
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, fieldName, fileName))
	h.Set("Content-Type", contentType)
	part, err := writer.CreatePart(h)
	require.NoError(t, err)
	_, err = part.Write(body)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	// Inject org ID into context (middleware normally does this).
	orgID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	ctx := middleware.WithOrgID(req.Context(), orgID)
	return req.WithContext(ctx)
}

func TestUploadHandler_NotConfigured(t *testing.T) {
	t.Parallel()

	h := NewUploadHandler(nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", nil)
	w := httptest.NewRecorder()

	h.Upload(w, req)

	require.Equal(t, http.StatusNotImplemented, w.Code)
	require.Contains(t, w.Body.String(), "NOT_CONFIGURED")
}

func TestUploadHandler_MissingFile(t *testing.T) {
	t.Parallel()

	store := storage.NewFileUploadStore(t.TempDir(), "/uploads")
	h := NewUploadHandler(store)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", nil)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=xxx")
	orgID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.Upload(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUploadHandler_InvalidFileType(t *testing.T) {
	t.Parallel()

	store := storage.NewFileUploadStore(t.TempDir(), "/uploads")
	h := NewUploadHandler(store)
	req := newUploadRequest(t, "file", "malware.exe", "application/x-executable", []byte("bad"))
	w := httptest.NewRecorder()

	h.Upload(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_FILE_TYPE")
}

func TestUploadHandler_Success(t *testing.T) {
	t.Parallel()

	store := storage.NewFileUploadStore(t.TempDir(), "/api/v1/uploads/files")
	h := NewUploadHandler(store)
	req := newUploadRequest(t, "file", "screenshot.png", "image/png", []byte("png-data"))
	w := httptest.NewRecorder()

	h.Upload(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotEmpty(t, resp["url"], "response should contain a url")
	require.Equal(t, "screenshot.png", resp["file_name"])
	require.Equal(t, "image/png", resp["content_type"])
}

func TestUploadHandler_ServeUpload_PathTraversal(t *testing.T) {
	t.Parallel()

	store := storage.NewFileUploadStore(t.TempDir(), "/api/v1/uploads/files")
	h := NewUploadHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/uploads/files/../../etc/passwd", nil)
	orgID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ServeUpload(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_KEY")
}

func TestUploadHandler_ServeUpload_EmptyKey(t *testing.T) {
	t.Parallel()

	store := storage.NewFileUploadStore(t.TempDir(), "/api/v1/uploads/files")
	h := NewUploadHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/uploads/files/", nil)
	orgID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ServeUpload(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_KEY")
}

func TestUploadHandler_ServeUpload_CrossOrgDenied(t *testing.T) {
	t.Parallel()

	store := storage.NewFileUploadStore(t.TempDir(), "/api/v1/uploads/files")
	h := NewUploadHandler(store)

	otherOrgID := uuid.MustParse("00000000-0000-0000-0000-000000000099")
	// Request a file keyed under a different org.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/uploads/files/00000000-0000-0000-0000-000000000001/2025-01/file.png", nil)
	ctx := middleware.WithOrgID(req.Context(), otherOrgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ServeUpload(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "FORBIDDEN")
}

func TestUploadHandler_ServeUpload_S3Mode(t *testing.T) {
	t.Parallel()

	s3Store := storage.NewS3UploadStore(nil, "bucket", "prefix", "https://example.com")
	h := NewUploadHandler(s3Store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/uploads/files/some-key.png", nil)
	w := httptest.NewRecorder()

	h.ServeUpload(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestExtensionFromMIME(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mime string
		ext  string
	}{
		{"image/png", ".png"},
		{"image/jpeg", ".jpg"},
		{"image/gif", ".gif"},
		{"image/webp", ".webp"},
		{"image/svg+xml", ".svg"},
		{"application/pdf", ".pdf"},
		{"text/plain", ".txt"},
		{"text/markdown", ".md"},
		{"text/csv", ".csv"},
		{"application/json", ".json"},
		{"application/octet-stream", ""},
	}

	for _, tt := range tests {
		t.Run(tt.mime, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.ext, extensionFromMIME(tt.mime))
		})
	}
}
