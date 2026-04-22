package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/require"
)

type snapshotMockS3Client struct {
	putInput       *s3.PutObjectInput
	getInput       *s3.GetObjectInput
	deleteInput    *s3.DeleteObjectInput
	headBucketFunc func(ctx context.Context, params *s3.HeadBucketInput, optFns ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	getBody        []byte
	getErr         error
}

func (m *snapshotMockS3Client) PutObject(_ context.Context, params *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	m.putInput = params
	return &s3.PutObjectOutput{}, nil
}

func (m *snapshotMockS3Client) GetObject(_ context.Context, params *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	m.getInput = params
	if m.getErr != nil {
		return nil, m.getErr
	}
	return &s3.GetObjectOutput{
		Body: io.NopCloser(bytes.NewReader(m.getBody)),
	}, nil
}

func (m *snapshotMockS3Client) DeleteObject(_ context.Context, params *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	m.deleteInput = params
	return &s3.DeleteObjectOutput{}, nil
}

func (m *snapshotMockS3Client) HeadBucket(ctx context.Context, params *s3.HeadBucketInput, optFns ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	if m.headBucketFunc != nil {
		return m.headBucketFunc(ctx, params, optFns...)
	}
	return &s3.HeadBucketOutput{}, nil
}

func TestS3SnapshotStore_UsesConfiguredPrefix(t *testing.T) {
	client := &snapshotMockS3Client{getBody: []byte("snapshot-bytes")}
	store := NewS3SnapshotStore(client, "snapshot-bucket", "sessions")

	err := store.Save(context.Background(), "snapshots/org-1/session-1/workspace.tar.zst", bytes.NewReader([]byte("snapshot-bytes")))
	require.NoError(t, err, "Save should upload the snapshot without error")
	require.NotNil(t, client.putInput, "Save should issue a PutObject request")
	require.Equal(t, "snapshot-bucket", aws.ToString(client.putInput.Bucket), "Save should target the configured bucket")
	require.Equal(t, "sessions/snapshots/org-1/session-1/workspace.tar.zst", aws.ToString(client.putInput.Key), "Save should prepend the configured prefix to snapshot keys")
	require.Equal(t, s3types.ServerSideEncryptionAes256, client.putInput.ServerSideEncryption, "Save should enable AES256 server-side encryption")
	require.NotNil(t, client.putInput.ContentLength, "Save should declare the snapshot content length for S3-compatible backends")
	require.EqualValues(t, len("snapshot-bytes"), aws.ToInt64(client.putInput.ContentLength), "Save should set ContentLength to the uploaded byte size")

	var loaded bytes.Buffer
	err = store.Load(context.Background(), "snapshots/org-1/session-1/workspace.tar.zst", &loaded)
	require.NoError(t, err, "Load should read the snapshot without error")
	require.NotNil(t, client.getInput, "Load should issue a GetObject request")
	require.Equal(t, "sessions/snapshots/org-1/session-1/workspace.tar.zst", aws.ToString(client.getInput.Key), "Load should use the prefixed snapshot key")
	require.Equal(t, []byte("snapshot-bytes"), loaded.Bytes(), "Load should stream the stored snapshot payload to the caller")

	err = store.Delete(context.Background(), "snapshots/org-1/session-1/workspace.tar.zst")
	require.NoError(t, err, "Delete should remove the snapshot without error")
	require.NotNil(t, client.deleteInput, "Delete should issue a DeleteObject request")
	require.Equal(t, "sessions/snapshots/org-1/session-1/workspace.tar.zst", aws.ToString(client.deleteInput.Key), "Delete should use the prefixed snapshot key")
}

func TestS3SnapshotStore_LoadNotFoundWrapsSnapshotSentinel(t *testing.T) {
	client := &snapshotMockS3Client{getErr: &s3types.NoSuchKey{}}
	store := NewS3SnapshotStore(client, "snapshot-bucket", "sessions")

	var loaded bytes.Buffer
	err := store.Load(context.Background(), "snapshots/org-1/session-1/workspace.tar.zst", &loaded)
	require.Error(t, err, "Load should return an error when the object does not exist")
	require.True(t, errors.Is(err, ErrSnapshotNotFound), "Load should wrap ErrSnapshotNotFound for missing snapshot objects")
}

func TestS3SnapshotStore_SaveSetsContentLengthForStreamingReader(t *testing.T) {
	client := &snapshotMockS3Client{}
	store := NewS3SnapshotStore(client, "snapshot-bucket", "sessions")

	reader := io.NopCloser(bytes.NewBufferString("streamed-snapshot"))
	err := store.Save(context.Background(), "snapshots/org-1/session-1/workspace.tar.zst", reader)
	require.NoError(t, err, "Save should upload snapshots from streaming readers without error")
	require.NotNil(t, client.putInput, "Save should issue a PutObject request")
	require.NotNil(t, client.putInput.ContentLength, "Save should compute ContentLength even for streaming readers")
	require.EqualValues(t, len("streamed-snapshot"), aws.ToInt64(client.putInput.ContentLength), "Save should set ContentLength to the streamed byte size")
}

func TestS3SnapshotStore_SaveReturnsTempFileCreationError(t *testing.T) {
	origCreateTemp := createSnapshotTempFile
	createSnapshotTempFile = func(dir, pattern string) (*os.File, error) {
		return nil, errors.New("disk full")
	}
	t.Cleanup(func() {
		createSnapshotTempFile = origCreateTemp
	})

	store := NewS3SnapshotStore(&snapshotMockS3Client{}, "snapshot-bucket", "sessions")

	err := store.Save(context.Background(), "snapshots/org-1/session-1/workspace.tar.zst", bytes.NewReader([]byte("snapshot-bytes")))
	require.Error(t, err, "Save should return an error when temp file creation fails")
	require.Contains(t, err.Error(), "create temp snapshot file snapshots/org-1/session-1/workspace.tar.zst", "Save should identify temp file creation failures")
}

func TestS3SnapshotStore_SaveReturnsCopyError(t *testing.T) {
	origCopy := copySnapshotToTemp
	copySnapshotToTemp = func(dst io.Writer, src io.Reader) (int64, error) {
		return 0, errors.New("read failed")
	}
	t.Cleanup(func() {
		copySnapshotToTemp = origCopy
	})

	store := NewS3SnapshotStore(&snapshotMockS3Client{}, "snapshot-bucket", "sessions")

	err := store.Save(context.Background(), "snapshots/org-1/session-1/workspace.tar.zst", bytes.NewReader([]byte("snapshot-bytes")))
	require.Error(t, err, "Save should return an error when buffering the snapshot fails")
	require.Contains(t, err.Error(), "buffer snapshot snapshots/org-1/session-1/workspace.tar.zst", "Save should identify snapshot buffering failures")
}

func TestS3SnapshotStore_SaveReturnsRewindError(t *testing.T) {
	origRewind := rewindSnapshotTempFile
	rewindSnapshotTempFile = func(f *os.File) (int64, error) {
		return 0, errors.New("seek failed")
	}
	t.Cleanup(func() {
		rewindSnapshotTempFile = origRewind
	})

	store := NewS3SnapshotStore(&snapshotMockS3Client{}, "snapshot-bucket", "sessions")

	err := store.Save(context.Background(), "snapshots/org-1/session-1/workspace.tar.zst", bytes.NewReader([]byte("snapshot-bytes")))
	require.Error(t, err, "Save should return an error when rewinding the temp file fails")
	require.Contains(t, err.Error(), "rewind snapshot snapshots/org-1/session-1/workspace.tar.zst", "Save should identify snapshot rewind failures")
}
