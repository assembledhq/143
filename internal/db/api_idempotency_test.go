package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestAPIIdempotencyStore_CreateReportsClaimOwnership(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		rows      int64
		wantClaim bool
		wantErr   bool
	}{
		{name: "inserted placeholder owns claim", rows: 1, wantClaim: true},
		{name: "conflicting placeholder does not own claim", rows: 0, wantClaim: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should initialize")
			defer mock.Close()

			mock.ExpectExec("INSERT INTO api_idempotency_keys").
				WithArgs(anyArgs(8)...).
				WillReturnResult(pgxmock.NewResult("INSERT", tt.rows))

			store := NewAPIIdempotencyStore(mock)
			got, err := store.Create(context.Background(), uuid.New(), uuid.New(), uuid.New(), "POST", "/api/v1/sessions", "key", "sha256:test", time.Now())
			if tt.wantErr {
				require.Error(t, err, "Create should return the expected error")
				return
			}
			require.NoError(t, err, "Create should not return an error")
			require.Equal(t, tt.wantClaim, got, "Create should report whether this request owns the idempotency key")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}
