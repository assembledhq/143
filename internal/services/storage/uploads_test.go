package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"
)

// mockS3Client implements S3Client for testing.
type mockS3Client struct {
	getObjectFunc    func(ctx context.Context, input *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	putObjectFunc    func(ctx context.Context, input *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	deleteObjectFunc func(ctx context.Context, input *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	headBucketFunc   func(ctx context.Context, input *s3.HeadBucketInput, optFns ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
}

func (m *mockS3Client) GetObject(ctx context.Context, input *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if m.getObjectFunc != nil {
		return m.getObjectFunc(ctx, input, optFns...)
	}
	return nil, fmt.Errorf("not implemented")
}

func (m *mockS3Client) PutObject(ctx context.Context, input *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if m.putObjectFunc != nil {
		return m.putObjectFunc(ctx, input, optFns...)
	}
	return nil, fmt.Errorf("not implemented")
}

func (m *mockS3Client) DeleteObject(ctx context.Context, input *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	if m.deleteObjectFunc != nil {
		return m.deleteObjectFunc(ctx, input, optFns...)
	}
	return nil, fmt.Errorf("not implemented")
}

func (m *mockS3Client) HeadBucket(ctx context.Context, input *s3.HeadBucketInput, optFns ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	if m.headBucketFunc != nil {
		return m.headBucketFunc(ctx, input, optFns...)
	}
	return nil, fmt.Errorf("not implemented")
}

// Compile-time check that mockS3Client implements S3Client.
var _ S3Client = (*mockS3Client)(nil)

func TestFileUploadStore_SaveAndURL(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	store := NewFileUploadStore(baseDir, "/api/v1/uploads/files")
	ctx := context.Background()
	key := "org-1/2026-03/test-file.png"
	payload := []byte("fake-image-data")

	url, err := store.Save(ctx, key, bytes.NewReader(payload), "image/png")
	require.NoError(t, err, "Save should succeed")
	require.Equal(t, "/api/v1/uploads/files/"+key, url, "Save should return the correct URL")

	// Verify file on disk.
	data, err := os.ReadFile(filepath.Join(baseDir, key))
	require.NoError(t, err, "file should exist on disk")
	require.Equal(t, payload, data, "file content should match")
}

func TestFileUploadStore_Open(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	store := NewFileUploadStore(baseDir, "/api/v1/uploads/files")
	key := "org-1/2026-05/screenshot.png"
	body := []byte("png-data")
	require.NoError(t, os.MkdirAll(filepath.Join(baseDir, "org-1/2026-05"), 0o750), "test fixture directory should be created")
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, key), body, 0o600), "test fixture file should be written")

	rc, contentType, err := store.Open(context.Background(), key)
	require.NoError(t, err, "Open should read a locally stored upload")
	defer rc.Close()

	got, err := io.ReadAll(rc)
	require.NoError(t, err, "Open reader should be readable")
	require.Equal(t, body, got, "Open should return the uploaded bytes")
	require.Equal(t, "image/png", contentType, "Open should infer content type from the uploaded filename")
}

func TestFileUploadStore_OpenRejectsTraversal(t *testing.T) {
	t.Parallel()

	store := NewFileUploadStore(t.TempDir(), "/api/v1/uploads/files")

	_, _, err := store.Open(context.Background(), "../secret.png")

	require.Error(t, err, "Open should reject path traversal keys before storage access")
}

func TestFileUploadStore_URL(t *testing.T) {
	t.Parallel()

	store := NewFileUploadStore("/tmp/uploads", "/api/v1/uploads/files")
	require.Equal(t, "/api/v1/uploads/files/org-1/file.png", store.URL("org-1/file.png"))
}

func TestFileUploadStore_SaveMkdirFails(t *testing.T) {
	t.Parallel()

	// Use a file as base dir so MkdirAll fails.
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "not-a-dir")
	require.NoError(t, os.WriteFile(filePath, []byte("x"), 0o600))

	store := NewFileUploadStore(filePath, "/uploads")
	_, err := store.Save(context.Background(), "sub/key.png", bytes.NewReader([]byte("data")), "image/png")
	require.Error(t, err, "Save should fail when MkdirAll fails")
	require.Contains(t, err.Error(), "create upload dir")
}

func TestFileUploadStore_TrailingSlashTrimmed(t *testing.T) {
	t.Parallel()

	store := NewFileUploadStore("/tmp/uploads", "/api/v1/uploads/files/")
	require.Equal(t, "/api/v1/uploads/files/key.png", store.URL("key.png"),
		"trailing slash on baseURL should be trimmed")
}

func TestS3UploadStore_URL(t *testing.T) {
	t.Parallel()

	store := NewS3UploadStore(nil, "mybucket", "uploads")
	require.Equal(t, "/api/v1/uploads/files/org-1/file.png", store.URL("org-1/file.png"),
		"S3 URLs should be proxied through the backend")
}

func TestS3UploadStore_URL_NoPrefix(t *testing.T) {
	t.Parallel()

	store := NewS3UploadStore(nil, "mybucket", "")
	require.Equal(t, "/api/v1/uploads/files/org-1/file.png", store.URL("org-1/file.png"),
		"S3 URLs should be proxied through the backend regardless of prefix")
}

func TestS3UploadStore_Serve(t *testing.T) {
	t.Parallel()

	body := []byte("fake-image-data")
	contentType := "image/png"
	contentLength := int64(len(body))

	mock := &mockS3Client{
		getObjectFunc: func(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			require.Equal(t, "mybucket", *input.Bucket)
			require.Equal(t, "uploads/org-1/2026-01/abc.png", *input.Key)
			return &s3.GetObjectOutput{
				Body:          io.NopCloser(bytes.NewReader(body)),
				ContentType:   &contentType,
				ContentLength: &contentLength,
			}, nil
		},
	}

	store := NewS3UploadStore(mock, "mybucket", "uploads")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/uploads/files/org-1/2026-01/abc.png", nil)
	w := httptest.NewRecorder()

	store.Serve(w, req, "org-1/2026-01/abc.png")

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "image/png", w.Header().Get("Content-Type"))
	require.Equal(t, fmt.Sprintf("%d", contentLength), w.Header().Get("Content-Length"))
	require.Equal(t, "inline", w.Header().Get("Content-Disposition"))
	require.Equal(t, "private, max-age=86400", w.Header().Get("Cache-Control"))
	require.Equal(t, body, w.Body.Bytes())
}

func TestS3UploadStore_Open(t *testing.T) {
	t.Parallel()

	body := []byte("fake-image-data")
	contentType := "image/png"
	mock := &mockS3Client{
		getObjectFunc: func(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			require.Equal(t, "mybucket", *input.Bucket, "Open should read from the configured bucket")
			require.Equal(t, "uploads/org-1/2026-01/abc.png", *input.Key, "Open should apply the configured prefix")
			return &s3.GetObjectOutput{
				Body:        io.NopCloser(bytes.NewReader(body)),
				ContentType: &contentType,
			}, nil
		},
	}
	store := NewS3UploadStore(mock, "mybucket", "uploads")

	rc, gotContentType, err := store.Open(context.Background(), "org-1/2026-01/abc.png")
	require.NoError(t, err, "Open should read an uploaded object from S3")
	defer rc.Close()

	got, err := io.ReadAll(rc)
	require.NoError(t, err, "Open reader should be readable")
	require.Equal(t, body, got, "Open should return the uploaded object bytes")
	require.Equal(t, contentType, gotContentType, "Open should return the S3 content type")
}

func TestS3UploadStore_OpenRejectsTraversal(t *testing.T) {
	t.Parallel()

	called := false
	mock := &mockS3Client{
		getObjectFunc: func(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			called = true
			return nil, fmt.Errorf("should not be called")
		},
	}
	store := NewS3UploadStore(mock, "mybucket", "uploads")

	_, _, err := store.Open(context.Background(), "org-1/../../secret.png")

	require.Error(t, err, "Open should reject path traversal keys before S3 access")
	require.False(t, called, "Open should not call S3 for invalid keys")
}

func TestS3UploadStore_Serve_NonImageAttachment(t *testing.T) {
	t.Parallel()

	body := []byte(`{"key": "value"}`)
	contentType := "application/json"
	contentLength := int64(len(body))

	mock := &mockS3Client{
		getObjectFunc: func(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			return &s3.GetObjectOutput{
				Body:          io.NopCloser(bytes.NewReader(body)),
				ContentType:   &contentType,
				ContentLength: &contentLength,
			}, nil
		},
	}

	store := NewS3UploadStore(mock, "mybucket", "uploads")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/uploads/files/org-1/2026-01/data.json", nil)
	w := httptest.NewRecorder()

	store.Serve(w, req, "org-1/2026-01/data.json")

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, `attachment; filename="data.json"`, w.Header().Get("Content-Disposition"))
}

func TestS3UploadStore_Serve_SVGAttachment(t *testing.T) {
	t.Parallel()

	body := []byte(`<svg onload="alert(1)"></svg>`)
	contentType := "image/svg+xml"
	contentLength := int64(len(body))

	mock := &mockS3Client{
		getObjectFunc: func(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			return &s3.GetObjectOutput{
				Body:          io.NopCloser(bytes.NewReader(body)),
				ContentType:   &contentType,
				ContentLength: &contentLength,
			}, nil
		},
	}

	store := NewS3UploadStore(mock, "mybucket", "uploads")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/uploads/files/org-1/2026-01/unsafe.svg", nil)
	w := httptest.NewRecorder()

	store.Serve(w, req, "org-1/2026-01/unsafe.svg")

	require.Equal(t, http.StatusOK, w.Code, "legacy SVG objects should still be retrievable")
	require.Equal(t, `attachment; filename="unsafe.svg"`, w.Header().Get("Content-Disposition"), "legacy SVG objects should download instead of rendering inline")
	require.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"), "legacy SVG responses should prevent content sniffing")
}

func TestFileUploadStore_Serve_SVGAttachment(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	key := "org-1/2026-01/unsafe.svg"
	require.NoError(t, os.MkdirAll(filepath.Join(baseDir, "org-1/2026-01"), 0o750), "test fixture directory should be created")
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, key), []byte(`<svg onload="alert(1)"></svg>`), 0o600), "test SVG fixture should be written")

	store := NewFileUploadStore(baseDir, "/api/v1/uploads/files")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/uploads/files/"+key, nil)
	w := httptest.NewRecorder()

	store.Serve(w, req, key)

	require.Equal(t, http.StatusOK, w.Code, "legacy local SVG uploads should still be retrievable")
	require.Equal(t, `attachment; filename="unsafe.svg"`, w.Header().Get("Content-Disposition"), "legacy local SVG uploads should download instead of rendering inline")
	require.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"), "legacy local SVG responses should prevent content sniffing")
}

func TestFileUploadStore_Serve_HTMLAttachment(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	key := "org-1/2026-01/evil.html"
	require.NoError(t, os.MkdirAll(filepath.Join(baseDir, "org-1/2026-01"), 0o750), "test fixture directory should be created")
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, key), []byte(`<script>alert(1)</script>`), 0o600), "test HTML fixture should be written")

	store := NewFileUploadStore(baseDir, "/api/v1/uploads/files")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/uploads/files/"+key, nil)
	w := httptest.NewRecorder()

	store.Serve(w, req, key)

	require.Equal(t, http.StatusOK, w.Code, "HTML uploads should still be retrievable")
	require.Equal(t, `attachment; filename="evil.html"`, w.Header().Get("Content-Disposition"), "HTML uploads must download instead of rendering inline to prevent stored XSS")
	require.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"), "HTML responses should prevent content sniffing")
}

func TestFileUploadStore_Serve_ImageInline(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	key := "org-1/2026-01/photo.png"
	require.NoError(t, os.MkdirAll(filepath.Join(baseDir, "org-1/2026-01"), 0o750), "test fixture directory should be created")
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, key), []byte("\x89PNG\r\n\x1a\n"), 0o600), "test PNG fixture should be written")

	store := NewFileUploadStore(baseDir, "/api/v1/uploads/files")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/uploads/files/"+key, nil)
	w := httptest.NewRecorder()

	store.Serve(w, req, key)

	require.Equal(t, http.StatusOK, w.Code, "image uploads should be retrievable")
	require.Empty(t, w.Header().Get("Content-Disposition"), "safe raster images should render inline")
	require.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"), "all upload responses should prevent content sniffing")
}

func TestS3UploadStore_Serve_GetObjectError(t *testing.T) {
	t.Parallel()

	mock := &mockS3Client{
		getObjectFunc: func(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			return nil, fmt.Errorf("NoSuchKey: the specified key does not exist")
		},
	}

	store := NewS3UploadStore(mock, "mybucket", "uploads")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/uploads/files/org-1/missing.png", nil)
	w := httptest.NewRecorder()

	store.Serve(w, req, "org-1/missing.png")

	require.Equal(t, http.StatusNotFound, w.Code)
}
