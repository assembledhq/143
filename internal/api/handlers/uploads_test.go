package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/storage"
)

// stubUploadMembershipStore implements uploadMembershipStore for tests.
// allowed lists (userID, orgID) pairs that resolve to a membership; everything
// else returns pgx.ErrNoRows so the handler 403s. errOverride lets a test
// short-circuit Get to a non-ErrNoRows failure (e.g. a synthetic DB error).
type stubUploadMembershipStore struct {
	allowed     map[[2]uuid.UUID]bool
	errOverride error
}

func (s *stubUploadMembershipStore) Get(_ context.Context, userID, orgID uuid.UUID) (models.OrganizationMembership, error) {
	if s.errOverride != nil {
		return models.OrganizationMembership{}, s.errOverride
	}
	if s.allowed[[2]uuid.UUID{userID, orgID}] {
		return models.OrganizationMembership{UserID: userID, OrgID: orgID, Role: "member"}, nil
	}
	return models.OrganizationMembership{}, pgx.ErrNoRows
}

func newStubMembershipStore(t *testing.T, pairs ...[2]uuid.UUID) *stubUploadMembershipStore {
	t.Helper()
	allowed := make(map[[2]uuid.UUID]bool, len(pairs))
	for _, p := range pairs {
		allowed[p] = true
	}
	return &stubUploadMembershipStore{allowed: allowed}
}

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
	h.SetMembershipStore(newStubMembershipStore(t))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/uploads/files/../../etc/passwd", nil)
	w := httptest.NewRecorder()

	h.ServeUpload(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_KEY")
}

func TestUploadHandler_ServeUpload_EmptyKey(t *testing.T) {
	t.Parallel()

	store := storage.NewFileUploadStore(t.TempDir(), "/api/v1/uploads/files")
	h := NewUploadHandler(store)
	h.SetMembershipStore(newStubMembershipStore(t))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/uploads/files/", nil)
	w := httptest.NewRecorder()

	h.ServeUpload(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_KEY")
}

func TestUploadHandler_ServeUpload_MalformedOrgID(t *testing.T) {
	t.Parallel()

	store := storage.NewFileUploadStore(t.TempDir(), "/api/v1/uploads/files")
	h := NewUploadHandler(store)
	h.SetMembershipStore(newStubMembershipStore(t))

	// First path segment is not a UUID — must reject before touching the store.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/uploads/files/not-a-uuid/file.png", nil)
	w := httptest.NewRecorder()

	h.ServeUpload(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_KEY")
}

func TestUploadHandler_ServeUpload_CrossOrgDenied(t *testing.T) {
	t.Parallel()

	store := storage.NewFileUploadStore(t.TempDir(), "/api/v1/uploads/files")
	h := NewUploadHandler(store)

	userID := uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
	memberOrgID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	otherOrgID := uuid.MustParse("00000000-0000-0000-0000-000000000099")
	// User is a member of memberOrg but the file is keyed under otherOrg.
	h.SetMembershipStore(newStubMembershipStore(t, [2]uuid.UUID{userID, memberOrgID}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/uploads/files/"+otherOrgID.String()+"/2025-01/file.png", nil)
	ctx := middleware.WithUser(req.Context(), &models.User{ID: userID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ServeUpload(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "FORBIDDEN")
}

// Regression: a multi-org user fetching a file via <img> sends no
// X-Active-Org-ID header, so the auth middleware's resolved active org may
// differ from the file's owning org. ServeUpload must authorize off the
// path-encoded org plus user membership, not the active-org context.
func TestUploadHandler_ServeUpload_NoActiveOrgHeader_AllowsMember(t *testing.T) {
	t.Parallel()

	s3Store := storage.NewS3UploadStore(nil, "bucket", "prefix")
	h := NewUploadHandler(s3Store)

	userID := uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
	fileOrgID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	resolvedActiveOrgID := uuid.MustParse("00000000-0000-0000-0000-000000000099")
	h.SetMembershipStore(newStubMembershipStore(t, [2]uuid.UUID{userID, fileOrgID}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/uploads/files/"+fileOrgID.String()+"/2026-04/file.png", nil)
	// Active-org context points at a *different* org (the user's other
	// membership) — this mirrors the production case where the session's
	// last_org_id hint races against the client's X-Active-Org-ID header.
	ctx := middleware.WithUser(req.Context(), &models.User{ID: userID})
	ctx = middleware.WithOrgID(ctx, resolvedActiveOrgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ServeUpload(w, req)

	// S3 client is nil, so GetObject will fail — handler returns 404. The
	// important assertion is that we made it past the membership check (i.e.
	// did not 403 like before the fix).
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestUploadHandler_ServeUpload_S3Mode(t *testing.T) {
	t.Parallel()

	s3Store := storage.NewS3UploadStore(nil, "bucket", "prefix")
	h := NewUploadHandler(s3Store)

	userID := uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
	orgID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	h.SetMembershipStore(newStubMembershipStore(t, [2]uuid.UUID{userID, orgID}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/uploads/files/"+orgID.String()+"/file.png", nil)
	ctx := middleware.WithUser(req.Context(), &models.User{ID: userID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ServeUpload(w, req)

	// S3 client is nil, so GetObject will fail — handler returns 404.
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestUploadHandler_ServeUpload_KeyWithoutSlash(t *testing.T) {
	t.Parallel()

	store := storage.NewFileUploadStore(t.TempDir(), "/api/v1/uploads/files")
	h := NewUploadHandler(store)
	h.SetMembershipStore(newStubMembershipStore(t))

	// Single-segment key (no "/") — must be rejected before parsing it as a UUID.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/uploads/files/no-slash-here", nil)
	w := httptest.NewRecorder()

	h.ServeUpload(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_KEY")
}

func TestUploadHandler_ServeUpload_MissingUser(t *testing.T) {
	t.Parallel()

	store := storage.NewFileUploadStore(t.TempDir(), "/api/v1/uploads/files")
	h := NewUploadHandler(store)
	h.SetMembershipStore(newStubMembershipStore(t))

	orgID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	// No user injected into the request context.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/uploads/files/"+orgID.String()+"/file.png", nil)
	w := httptest.NewRecorder()

	h.ServeUpload(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Contains(t, w.Body.String(), "UNAUTHORIZED")
}

func TestUploadHandler_ServeUpload_MembershipNotConfigured(t *testing.T) {
	t.Parallel()

	store := storage.NewFileUploadStore(t.TempDir(), "/api/v1/uploads/files")
	h := NewUploadHandler(store) // no SetMembershipStore — programmer error.

	userID := uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
	orgID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/uploads/files/"+orgID.String()+"/file.png", nil)
	ctx := middleware.WithUser(req.Context(), &models.User{ID: userID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ServeUpload(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "NOT_CONFIGURED")
}

func TestUploadHandler_ServeUpload_MembershipStoreError(t *testing.T) {
	t.Parallel()

	store := storage.NewFileUploadStore(t.TempDir(), "/api/v1/uploads/files")
	h := NewUploadHandler(store)
	stub := newStubMembershipStore(t)
	stub.errOverride = errors.New("boom")
	h.SetMembershipStore(stub)

	userID := uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
	orgID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/uploads/files/"+orgID.String()+"/file.png", nil)
	ctx := middleware.WithUser(req.Context(), &models.User{ID: userID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	h.ServeUpload(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "INTERNAL")
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
