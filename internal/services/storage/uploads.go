// Package storage provides UploadStore for persisting user-uploaded files
// (images, documents) to an object store for display in session chat.
package storage

import (
	"context"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// UploadStore abstracts persistence for user-uploaded files (images, etc.).
type UploadStore interface {
	// Save stores a file and returns the public URL to access it.
	Save(ctx context.Context, key string, reader io.Reader, contentType string) (url string, err error)

	// Open reads a previously uploaded file by storage key and returns a
	// caller-owned reader plus the best-known content type.
	Open(ctx context.Context, key string) (io.ReadCloser, string, error)

	// URL returns the public URL for a previously-uploaded file.
	URL(key string) string

	// Serve writes the file contents to the response. Implementations may
	// serve directly (local filesystem) or proxy from object storage (S3).
	Serve(w http.ResponseWriter, r *http.Request, key string)
}

// S3UploadStore stores uploads in an S3-compatible bucket.
type S3UploadStore struct {
	client  S3Client
	bucket  string
	prefix  string
	baseURL string // backend URL prefix for proxied file serving
}

// NewS3UploadStore creates an S3UploadStore.
func NewS3UploadStore(client S3Client, bucket, prefix string) *S3UploadStore {
	return &S3UploadStore{
		client:  client,
		bucket:  bucket,
		prefix:  strings.TrimSuffix(prefix, "/"),
		baseURL: "/api/v1/uploads/files",
	}
}

func (s *S3UploadStore) fullKey(key string) string {
	if s.prefix == "" {
		return key
	}
	return s.prefix + "/" + key
}

func (s *S3UploadStore) Save(ctx context.Context, key string, reader io.Reader, contentType string) (string, error) {
	cleanKey, err := validateUploadKey(key)
	if err != nil {
		return "", err
	}
	fullKey := s.fullKey(cleanKey)
	input := &s3.PutObjectInput{
		Bucket:               aws.String(s.bucket),
		Key:                  aws.String(fullKey),
		Body:                 reader,
		ServerSideEncryption: s3types.ServerSideEncryptionAes256,
	}
	if contentType != "" {
		input.ContentType = aws.String(contentType)
	}
	if _, err := s.client.PutObject(ctx, input); err != nil {
		return "", fmt.Errorf("upload file %s: %w", fullKey, err)
	}
	return s.URL(cleanKey), nil
}

func (s *S3UploadStore) URL(key string) string {
	return s.baseURL + "/" + key
}

func (s *S3UploadStore) Open(ctx context.Context, key string) (io.ReadCloser, string, error) {
	cleanKey, err := validateUploadKey(key)
	if err != nil {
		return nil, "", err
	}
	if s.client == nil {
		return nil, "", fmt.Errorf("upload storage is not configured")
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.fullKey(cleanKey)),
	})
	if err != nil {
		return nil, "", fmt.Errorf("open upload %s: %w", cleanKey, err)
	}
	contentType := "application/octet-stream"
	if out.ContentType != nil && *out.ContentType != "" {
		contentType = *out.ContentType
	}
	return out.Body, contentType, nil
}

// Serve fetches the file from S3 and streams it to the HTTP response.
func (s *S3UploadStore) Serve(w http.ResponseWriter, r *http.Request, key string) {
	cleanKey, err := validateUploadKey(key)
	if err != nil {
		http.Error(w, "invalid file key", http.StatusBadRequest)
		return
	}
	if s.client == nil {
		http.Error(w, "storage not configured", http.StatusNotFound)
		return
	}
	out, err := s.client.GetObject(r.Context(), &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.fullKey(cleanKey)),
	})
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	defer out.Body.Close()

	if out.ContentType != nil {
		w.Header().Set("Content-Type", *out.ContentType)
	}
	if out.ContentLength != nil {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", *out.ContentLength))
	}
	// Images display inline; other file types download as attachments.
	fileName := path.Base(cleanKey)
	if out.ContentType != nil && strings.HasPrefix(*out.ContentType, "image/") {
		w.Header().Set("Content-Disposition", "inline")
	} else {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	if _, err := io.Copy(w, out.Body); err != nil {
		log.Printf("upload serve: streaming %s: %v", key, err)
	}
}

// FileUploadStore stores uploads on the local filesystem and serves them
// via a configurable base URL. Used for development when S3 is not configured.
type FileUploadStore struct {
	baseDir string
	baseURL string // e.g. "/api/v1/uploads/files"
}

// NewFileUploadStore creates a local filesystem upload store.
func NewFileUploadStore(baseDir, baseURL string) *FileUploadStore {
	return &FileUploadStore{
		baseDir: baseDir,
		baseURL: strings.TrimSuffix(baseURL, "/"),
	}
}

func (f *FileUploadStore) Save(ctx context.Context, key string, reader io.Reader, _ string) (string, error) {
	cleanKey, err := validateUploadKey(key)
	if err != nil {
		return "", err
	}
	filePath := filepath.Join(f.baseDir, filepath.FromSlash(cleanKey))
	if err := os.MkdirAll(filepath.Dir(filePath), 0o750); err != nil {
		return "", fmt.Errorf("create upload dir: %w", err)
	}

	file, err := os.Create(filePath) // #nosec G304 -- path is validated and rooted under baseDir
	if err != nil {
		return "", fmt.Errorf("create upload file: %w", err)
	}
	defer file.Close()

	if _, err := io.Copy(file, reader); err != nil {
		return "", fmt.Errorf("write upload file: %w", err)
	}
	return f.URL(cleanKey), ctx.Err()
}

func (f *FileUploadStore) URL(key string) string {
	return f.baseURL + "/" + key
}

// Serve serves a locally-stored upload file.
func (f *FileUploadStore) Serve(w http.ResponseWriter, r *http.Request, key string) {
	cleanKey, err := validateUploadKey(key)
	if err != nil {
		http.Error(w, "invalid file key", http.StatusBadRequest)
		return
	}
	filePath := filepath.Join(f.baseDir, filepath.FromSlash(cleanKey))
	http.ServeFile(w, r, filePath)
}

func (f *FileUploadStore) Open(ctx context.Context, key string) (io.ReadCloser, string, error) {
	cleanKey, err := validateUploadKey(key)
	if err != nil {
		return nil, "", err
	}
	filePath := filepath.Join(f.baseDir, filepath.FromSlash(cleanKey))
	file, err := os.Open(filePath) // #nosec G304 -- path is validated and rooted under baseDir
	if err != nil {
		return nil, "", fmt.Errorf("open upload %s: %w", cleanKey, err)
	}
	contentType := mime.TypeByExtension(path.Ext(cleanKey))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if err := ctx.Err(); err != nil {
		_ = file.Close()
		return nil, "", err
	}
	return file, contentType, nil
}

func validateUploadKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("invalid upload key")
	}
	if strings.Contains(key, "\\") || strings.HasPrefix(key, "/") {
		return "", fmt.Errorf("invalid upload key")
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("invalid upload key")
		}
	}
	return path.Clean(key), nil
}
