// Package storage provides UploadStore for persisting user-uploaded files
// (images, documents) to an object store for display in session chat.
package storage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
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

	// URL returns the public URL for a previously-uploaded file.
	URL(key string) string
}

// S3UploadStore stores uploads in an S3-compatible bucket.
type S3UploadStore struct {
	client   S3Client
	bucket   string
	prefix   string
	endpoint string // base URL for constructing public URLs
}

// NewS3UploadStore creates an S3UploadStore.
// endpoint is the base URL for the bucket (e.g. "https://mybucket.s3.amazonaws.com").
func NewS3UploadStore(client S3Client, bucket, prefix, endpoint string) *S3UploadStore {
	return &S3UploadStore{
		client:   client,
		bucket:   bucket,
		prefix:   strings.TrimSuffix(prefix, "/"),
		endpoint: strings.TrimSuffix(endpoint, "/"),
	}
}

func (s *S3UploadStore) fullKey(key string) string {
	if s.prefix == "" {
		return key
	}
	return s.prefix + "/" + key
}

func (s *S3UploadStore) Save(ctx context.Context, key string, reader io.Reader, contentType string) (string, error) {
	fullKey := s.fullKey(key)
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
	return s.URL(key), nil
}

func (s *S3UploadStore) URL(key string) string {
	return s.endpoint + "/" + s.fullKey(key)
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
	path := filepath.Join(f.baseDir, filepath.Clean(key))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("create upload dir: %w", err)
	}

	file, err := os.Create(path) // #nosec G304 -- path is sanitized
	if err != nil {
		return "", fmt.Errorf("create upload file: %w", err)
	}
	defer file.Close()

	if _, err := io.Copy(file, reader); err != nil {
		return "", fmt.Errorf("write upload file: %w", err)
	}
	return f.URL(key), ctx.Err()
}

func (f *FileUploadStore) URL(key string) string {
	return f.baseURL + "/" + key
}

// ServeFile serves a locally-stored upload file. Only used with FileUploadStore.
func (f *FileUploadStore) ServeFile(w http.ResponseWriter, r *http.Request, key string) {
	path := filepath.Join(f.baseDir, filepath.Clean(key))
	http.ServeFile(w, r, path)
}
