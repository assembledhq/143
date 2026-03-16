package storage

import (
	"context"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Compile-time check that S3SnapshotStore implements SnapshotStore.
var _ SnapshotStore = (*S3SnapshotStore)(nil)

// S3Client defines the subset of the S3 API used by S3SnapshotStore.
type S3Client interface {
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

// S3SnapshotStore implements SnapshotStore using an S3-compatible object store.
// Works with AWS S3, GCS (S3-compatible), and MinIO for local development.
type S3SnapshotStore struct {
	client S3Client
	bucket string
}

// NewS3SnapshotStore creates an S3SnapshotStore for the given bucket.
func NewS3SnapshotStore(client S3Client, bucket string) *S3SnapshotStore {
	return &S3SnapshotStore{
		client: client,
		bucket: bucket,
	}
}

func (s *S3SnapshotStore) Save(ctx context.Context, key string, reader io.Reader) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:               aws.String(s.bucket),
		Key:                  aws.String(key),
		Body:                 reader,
		ServerSideEncryption: s3types.ServerSideEncryptionAes256,
	})
	if err != nil {
		return fmt.Errorf("upload snapshot %s: %w", key, err)
	}
	return nil
}

func (s *S3SnapshotStore) Load(ctx context.Context, key string, writer io.Writer) error {
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("download snapshot %s: %w", key, err)
	}
	defer result.Body.Close()

	if _, err := io.Copy(writer, result.Body); err != nil {
		return fmt.Errorf("read snapshot %s: %w", key, err)
	}
	return nil
}

func (s *S3SnapshotStore) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("delete snapshot %s: %w", key, err)
	}
	return nil
}
