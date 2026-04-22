package storage

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type cleanupTestSnapshotStore struct {
	deleted []string
	err     error
}

func (s *cleanupTestSnapshotStore) Save(context.Context, string, io.Reader) error { return nil }
func (s *cleanupTestSnapshotStore) Load(context.Context, string, io.Writer) error { return nil }
func (s *cleanupTestSnapshotStore) Delete(_ context.Context, key string) error {
	s.deleted = append(s.deleted, key)
	return s.err
}

type cleanupTestClearer struct {
	called    bool
	orgID     uuid.UUID
	sessionID uuid.UUID
	err       error
}

func (c *cleanupTestClearer) ClearSnapshotKey(_ context.Context, orgID, sessionID uuid.UUID) error {
	c.called = true
	c.orgID = orgID
	c.sessionID = sessionID
	return c.err
}

func TestCleanupSessionSnapshot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		snapshotKey *string
		deleteErr   error
		clearErr    error
		wantErr     string
		wantDelete  []string
		wantClear   bool
	}{
		{
			name:        "nil snapshot key is noop",
			snapshotKey: nil,
		},
		{
			name:        "empty snapshot key is noop",
			snapshotKey: func() *string { s := ""; return &s }(),
		},
		{
			name:        "missing snapshot still clears row",
			snapshotKey: func() *string { s := "snapshots/key.tar"; return &s }(),
			deleteErr:   ErrSnapshotNotFound,
			wantDelete:  []string{"snapshots/key.tar"},
			wantClear:   true,
		},
		{
			name:        "delete failure returns error",
			snapshotKey: func() *string { s := "snapshots/key.tar"; return &s }(),
			deleteErr:   errors.New("s3 down"),
			wantErr:     "delete snapshot snapshots/key.tar",
			wantDelete:  []string{"snapshots/key.tar"},
		},
		{
			name:        "success clears row",
			snapshotKey: func() *string { s := "snapshots/key.tar"; return &s }(),
			wantDelete:  []string{"snapshots/key.tar"},
			wantClear:   true,
		},
		{
			name:        "clear failure returns error",
			snapshotKey: func() *string { s := "snapshots/key.tar"; return &s }(),
			clearErr:    errors.New("db down"),
			wantErr:     "db down",
			wantDelete:  []string{"snapshots/key.tar"},
			wantClear:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := &cleanupTestSnapshotStore{err: tt.deleteErr}
			clearer := &cleanupTestClearer{err: tt.clearErr}
			orgID := uuid.New()
			sessionID := uuid.New()

			err := CleanupSessionSnapshot(context.Background(), store, clearer, orgID, sessionID, tt.snapshotKey)
			if tt.wantErr != "" {
				require.Error(t, err, "CleanupSessionSnapshot should return an error")
				require.Contains(t, err.Error(), tt.wantErr, "CleanupSessionSnapshot should include the underlying failure")
			} else {
				require.NoError(t, err, "CleanupSessionSnapshot should succeed")
			}

			require.Equal(t, tt.wantDelete, store.deleted, "CleanupSessionSnapshot should delete the expected snapshot keys")
			require.Equal(t, tt.wantClear, clearer.called, "CleanupSessionSnapshot should only clear the DB row when appropriate")
			if tt.wantClear {
				require.Equal(t, orgID, clearer.orgID, "CleanupSessionSnapshot should pass org ID through to the clearer")
				require.Equal(t, sessionID, clearer.sessionID, "CleanupSessionSnapshot should pass session ID through to the clearer")
			}
		})
	}
}
