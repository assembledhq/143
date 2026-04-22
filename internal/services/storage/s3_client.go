package storage

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type SnapshotStoreConfig struct {
	StorageDir     string
	S3Bucket       string
	S3Prefix       string
	S3Region       string
	S3Endpoint     string
	S3UsePathStyle bool
}

type SnapshotStoreInfo struct {
	Backend      string
	StorageDir   string
	Bucket       string
	Prefix       string
	EndpointHost string
	UsePathStyle bool
}

var loadAWSConfig = awsconfig.LoadDefaultConfig

var newS3ClientFromConfig = func(cfg aws.Config, optFns ...func(*s3.Options)) S3Client {
	return s3.NewFromConfig(cfg, optFns...)
}

// BuildSnapshotStore selects the snapshot backend from the provided runtime
// configuration. S3 is enabled when S3Bucket is non-empty; otherwise it falls
// back to local filesystem storage.
func BuildSnapshotStore(ctx context.Context, cfg SnapshotStoreConfig) (SnapshotStore, SnapshotStoreInfo, error) {
	if strings.TrimSpace(cfg.S3Bucket) == "" {
		return NewFileSnapshotStore(cfg.StorageDir), SnapshotStoreInfo{
			Backend:    "file",
			StorageDir: cfg.StorageDir,
		}, nil
	}

	endpointHost, err := validateS3Endpoint(cfg.S3Endpoint)
	if err != nil {
		return nil, SnapshotStoreInfo{}, err
	}

	awsCfg, err := loadAWSConfig(ctx, awsconfig.WithRegion(cfg.S3Region))
	if err != nil {
		return nil, SnapshotStoreInfo{}, fmt.Errorf("load snapshot S3 config: %w", err)
	}

	client := newS3ClientFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = cfg.S3UsePathStyle
		if strings.TrimSpace(cfg.S3Endpoint) != "" {
			o.BaseEndpoint = aws.String(strings.TrimSpace(cfg.S3Endpoint))
		}
	})
	if _, err := client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(cfg.S3Bucket),
	}); err != nil {
		return nil, SnapshotStoreInfo{}, fmt.Errorf("probe snapshot S3 bucket %s: %w", cfg.S3Bucket, err)
	}

	return NewS3SnapshotStore(client, cfg.S3Bucket, cfg.S3Prefix), SnapshotStoreInfo{
		Backend:      "s3",
		Bucket:       cfg.S3Bucket,
		Prefix:       strings.Trim(strings.TrimSpace(cfg.S3Prefix), "/"),
		EndpointHost: endpointHost,
		UsePathStyle: cfg.S3UsePathStyle,
	}, nil
}

func validateS3Endpoint(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}

	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("parse snapshot S3 endpoint: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("parse snapshot S3 endpoint: endpoint must include scheme and host")
	}
	return parsed.Hostname(), nil
}
