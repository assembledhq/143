package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/storage"
)

type captureUploadStore struct {
	key         string
	body        []byte
	contentType string
}

func (s *captureUploadStore) Save(_ context.Context, key string, reader io.Reader, contentType string) (string, error) {
	body, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	s.key = key
	s.body = body
	s.contentType = contentType
	return "/api/v1/uploads/files/" + key, nil
}

func (s *captureUploadStore) Open(context.Context, string) (io.ReadCloser, string, error) {
	return nil, "", errors.New("not implemented")
}

func (s *captureUploadStore) URL(key string) string {
	return "/api/v1/uploads/files/" + key
}

func (s *captureUploadStore) Serve(http.ResponseWriter, *http.Request, string) {
}

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

func TestUploadHandler_RejectsSVG(t *testing.T) {
	t.Parallel()

	store := storage.NewFileUploadStore(t.TempDir(), "/uploads")
	h := NewUploadHandler(store)
	req := newUploadRequest(t, "file", "diagram.svg", "image/svg+xml", []byte(`<svg onload="alert(1)"></svg>`))
	w := httptest.NewRecorder()

	h.Upload(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "SVG uploads should be rejected")
	require.Contains(t, w.Body.String(), "INVALID_FILE_TYPE", "SVG rejection should use the invalid file type response")
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

func TestUploadHandler_HEICConvertsToJPEG(t *testing.T) {
	t.Parallel()

	converter := func(_ context.Context, body []byte) ([]byte, error) {
		require.Equal(t, []byte("heic-data"), body, "converter should receive the uploaded HEIC bytes")
		return []byte("jpeg-data"), nil
	}

	store := &captureUploadStore{}
	h := NewUploadHandler(store)
	h.heicConverter = converter
	req := newUploadRequest(t, "file", "photo.HEIC", "image/heic", []byte("heic-data"))
	w := httptest.NewRecorder()

	h.Upload(w, req)

	require.Equal(t, http.StatusOK, w.Code, "HEIC upload should be accepted after conversion")
	require.Equal(t, "image/jpeg", store.contentType, "converted upload should be persisted as JPEG")
	require.Equal(t, []byte("jpeg-data"), store.body, "converted JPEG bytes should be stored")
	require.True(t, strings.HasSuffix(store.key, ".jpg"), "converted upload key should use a JPEG extension")

	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "upload response should be valid JSON")
	require.Equal(t, "photo.jpg", resp["file_name"], "response filename should match the converted JPEG")
	require.Equal(t, "image/jpeg", resp["content_type"], "response content type should match the converted JPEG")
	require.True(t, strings.HasSuffix(resp["url"], ".jpg"), "returned URL should point at the converted JPEG object")
}

func TestUploadHandler_HEICWithOctetStreamContentTypeConvertsToJPEG(t *testing.T) {
	t.Parallel()

	converter := func(_ context.Context, body []byte) ([]byte, error) {
		require.Equal(t, []byte("heif-data"), body, "converter should receive the uploaded HEIF bytes")
		return []byte("jpeg-data"), nil
	}

	store := &captureUploadStore{}
	h := NewUploadHandler(store)
	h.heicConverter = converter
	req := newUploadRequest(t, "file", "photo.heif", "application/octet-stream", []byte("heif-data"))
	w := httptest.NewRecorder()

	h.Upload(w, req)

	require.Equal(t, http.StatusOK, w.Code, "HEIF upload with generic browser content type should be accepted after conversion")
	require.Equal(t, "image/jpeg", store.contentType, "converted HEIF upload should be persisted as JPEG")
	require.True(t, strings.HasSuffix(store.key, ".jpg"), "converted HEIF upload key should use a JPEG extension")
}

func TestUploadHandler_HEICRejectsOversizedConvertedJPEG(t *testing.T) {
	t.Parallel()

	converter := func(_ context.Context, body []byte) ([]byte, error) {
		require.Equal(t, []byte("heic-data"), body, "converter should receive the uploaded HEIC bytes")
		return bytes.Repeat([]byte("x"), maxUploadSize+1), nil
	}

	store := &captureUploadStore{}
	h := NewUploadHandler(store)
	h.heicConverter = converter
	req := newUploadRequest(t, "file", "photo.heic", "image/heic", []byte("heic-data"))
	w := httptest.NewRecorder()

	h.Upload(w, req)

	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code, "oversized converted JPEG should be rejected")
	require.Empty(t, store.body, "oversized converted JPEG should not be stored")
	require.Contains(t, w.Body.String(), "FILE_TOO_LARGE", "response should report the upload size limit")
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
		{"image/heic", ".heic"},
		{"image/heif", ".heif"},
		{"image/heic-sequence", ".heic"},
		{"image/heif-sequence", ".heif"},
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

func TestRunHEICConverterRequiresDedicatedConverter(t *testing.T) {
	t.Parallel()

	err := runHEICConverterWithLookPath(
		context.Background(),
		"/tmp/input.heic",
		"/tmp/output.jpg",
		func(name string) (string, error) {
			require.Equal(t, "heif-convert", name, "HEIC conversion should only look for the dedicated converter")
			return "", errors.New("not found")
		},
	)

	require.Error(t, err, "missing heif-convert should fail conversion")
	require.Contains(t, err.Error(), "install heif-convert", "missing converter error should name the required dependency")
	require.NotContains(t, err.Error(), "ImageMagick", "missing converter error should not suggest broad fallback dependencies")
}

func TestConvertHEICToJPEGWithCommand_ConvertsRealHEICFixture(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("heif-convert"); err != nil {
		t.Skip("heif-convert is not installed")
	}
	heifEncPath, err := exec.LookPath("heif-enc")
	if err != nil {
		t.Skip("heif-enc is not installed")
	}

	tmpDir := t.TempDir()
	pngPath := filepath.Join(tmpDir, "input.png")
	heicPath := filepath.Join(tmpDir, "input.heic")

	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(1, 0, color.RGBA{G: 255, A: 255})
	img.Set(0, 1, color.RGBA{B: 255, A: 255})
	img.Set(1, 1, color.RGBA{R: 255, G: 255, B: 255, A: 255})

	pngFile, err := os.Create(pngPath)
	require.NoError(t, err, "test PNG fixture should be creatable")
	require.NoError(t, png.Encode(pngFile, img), "test PNG fixture should be encodable")
	require.NoError(t, pngFile.Close(), "test PNG fixture should close cleanly")

	encodeCmd := exec.CommandContext(context.Background(), heifEncPath, pngPath, "-o", heicPath)
	encodeOutput, err := encodeCmd.CombinedOutput()
	require.NoError(t, err, "heif-enc should create a real HEIC fixture: %s", strings.TrimSpace(string(encodeOutput)))

	heicBytes, err := os.ReadFile(heicPath)
	require.NoError(t, err, "real HEIC fixture should be readable")
	require.NotEmpty(t, heicBytes, "real HEIC fixture should not be empty")

	jpegBytes, err := convertHEICToJPEGWithCommand(context.Background(), heicBytes)
	require.NoError(t, err, "real HEIC fixture should convert to JPEG")
	require.NotEmpty(t, jpegBytes, "converted JPEG should not be empty")

	decoded, err := jpeg.Decode(bytes.NewReader(jpegBytes))
	require.NoError(t, err, "converted bytes should decode as JPEG")
	require.Equal(t, 2, decoded.Bounds().Dx(), "converted JPEG should preserve fixture width")
	require.Equal(t, 2, decoded.Bounds().Dy(), "converted JPEG should preserve fixture height")
}
