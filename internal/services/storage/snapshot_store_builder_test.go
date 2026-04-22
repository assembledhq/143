package storage

import (
	"context"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"
)

func TestBuildSnapshotStore_FallsBackToFileStore(t *testing.T) {
	t.Parallel()

	store, info, err := BuildSnapshotStore(context.Background(), SnapshotStoreConfig{
		StorageDir: "/tmp/snapshots",
	})
	require.NoError(t, err, "BuildSnapshotStore should fall back to a file store when no S3 bucket is configured")

	fileStore, ok := store.(*FileSnapshotStore)
	require.True(t, ok, "BuildSnapshotStore should return a FileSnapshotStore when S3 is disabled")
	require.Equal(t, "/tmp/snapshots", fileStore.baseDir, "BuildSnapshotStore should preserve the configured local snapshot directory")
	require.Equal(t, "file", info.Backend, "BuildSnapshotStore should describe the file backend in its metadata")
	require.Equal(t, "/tmp/snapshots", info.StorageDir, "BuildSnapshotStore metadata should include the local snapshot directory")
}

//nolint:paralleltest // overrides package-level test hooks for isolated builder wiring assertions
func TestBuildSnapshotStore_UsesS3Config(t *testing.T) {
	origLoader := loadAWSConfig
	origConstructor := newS3ClientFromConfig
	t.Cleanup(func() {
		loadAWSConfig = origLoader
		newS3ClientFromConfig = origConstructor
	})

	var gotRegion string
	var gotUsePathStyle bool
	var gotEndpoint string
	var gotHeadBucket string

	loadAWSConfig = func(_ context.Context, optFns ...func(*awsconfig.LoadOptions) error) (aws.Config, error) {
		var opts awsconfig.LoadOptions
		for _, fn := range optFns {
			require.NoError(t, fn(&opts), "BuildSnapshotStore should apply all AWS config options without error")
		}
		gotRegion = opts.Region
		return aws.Config{Region: opts.Region}, nil
	}
	newS3ClientFromConfig = func(cfg aws.Config, optFns ...func(*s3.Options)) S3Client {
		var opts s3.Options
		for _, fn := range optFns {
			fn(&opts)
		}
		gotRegion = cfg.Region
		gotUsePathStyle = opts.UsePathStyle
		if opts.BaseEndpoint != nil {
			gotEndpoint = *opts.BaseEndpoint
		}
		return &snapshotMockS3Client{
			headBucketFunc: func(_ context.Context, input *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
				gotHeadBucket = aws.ToString(input.Bucket)
				return &s3.HeadBucketOutput{}, nil
			},
		}
	}

	store, info, err := BuildSnapshotStore(context.Background(), SnapshotStoreConfig{
		StorageDir:     "/tmp/snapshots",
		S3Bucket:       "session-snapshots",
		S3Prefix:       "sessions",
		S3Region:       "us-west-2",
		S3Endpoint:     "https://r2.example.com",
		S3UsePathStyle: true,
	})
	require.NoError(t, err, "BuildSnapshotStore should create an S3-backed store when a bucket is configured")

	_, ok := store.(*S3SnapshotStore)
	require.True(t, ok, "BuildSnapshotStore should return an S3SnapshotStore when S3 is enabled")
	require.Equal(t, "us-west-2", gotRegion, "BuildSnapshotStore should pass the configured region into AWS config loading")
	require.True(t, gotUsePathStyle, "BuildSnapshotStore should pass the path-style flag into the S3 client")
	require.Equal(t, "https://r2.example.com", gotEndpoint, "BuildSnapshotStore should pass the endpoint override into the S3 client")
	require.Equal(t, "session-snapshots", gotHeadBucket, "BuildSnapshotStore should probe the configured bucket before returning an S3 store")
	require.Equal(t, "s3", info.Backend, "BuildSnapshotStore metadata should describe the S3 backend")
	require.Equal(t, "session-snapshots", info.Bucket, "BuildSnapshotStore metadata should include the S3 bucket")
	require.Equal(t, "sessions", info.Prefix, "BuildSnapshotStore metadata should include the S3 prefix")
	require.Equal(t, "r2.example.com", info.EndpointHost, "BuildSnapshotStore metadata should include the parsed endpoint host")
	require.True(t, info.UsePathStyle, "BuildSnapshotStore metadata should include the path-style flag")
}

func TestBuildSnapshotStore_InvalidS3EndpointFails(t *testing.T) {
	t.Parallel()

	_, _, err := BuildSnapshotStore(context.Background(), SnapshotStoreConfig{
		StorageDir: "/tmp/snapshots",
		S3Bucket:   "session-snapshots",
		S3Endpoint: "://bad-endpoint",
	})
	require.Error(t, err, "BuildSnapshotStore should fail fast when S3 is enabled with an invalid endpoint")
}

//nolint:paralleltest // overrides package-level test hooks for isolated startup probe assertions
func TestBuildSnapshotStore_S3ProbeFailureFails(t *testing.T) {
	origLoader := loadAWSConfig
	origConstructor := newS3ClientFromConfig
	t.Cleanup(func() {
		loadAWSConfig = origLoader
		newS3ClientFromConfig = origConstructor
	})

	loadAWSConfig = func(_ context.Context, _ ...func(*awsconfig.LoadOptions) error) (aws.Config, error) {
		return aws.Config{Region: "us-west-2"}, nil
	}
	newS3ClientFromConfig = func(_ aws.Config, _ ...func(*s3.Options)) S3Client {
		return &snapshotMockS3Client{
			headBucketFunc: func(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
				return nil, fmt.Errorf("forbidden")
			},
		}
	}

	_, _, err := BuildSnapshotStore(context.Background(), SnapshotStoreConfig{
		StorageDir: "/tmp/snapshots",
		S3Bucket:   "session-snapshots",
		S3Region:   "us-west-2",
	})
	require.Error(t, err, "BuildSnapshotStore should fail when the snapshot bucket probe fails")
	require.Contains(t, err.Error(), "probe snapshot S3 bucket", "BuildSnapshotStore should report startup probe failures clearly")
}
