package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

type exportBeginErrorDB struct {
	TxStarter
	err error
}

func (d exportBeginErrorDB) Begin(context.Context) (pgx.Tx, error) { return nil, d.err }
func TestCodeReviewExportAcquireTransactionErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, code string
		busy       bool
	}{
		{"serialization conflict", "40001", true},
		{"deadlock", "40P01", true},
		{"query timeout remains bounded failure", "57014", false},
		{"other database failure", "XX000", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			original := &pgconn.PgError{Code: tt.code, Message: "synthetic database error"}
			from := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
			filters := models.CodeReviewExportFilters{RepositoryID: uuid.New(), From: from, Until: from.AddDate(0, 0, 96), TimeBasis: models.ReviewExportCreated, IncludeUnresolvedPublicationTime: true}
			_, err := NewCodeReviewExportStore(exportBeginErrorDB{err: original}).Acquire(context.Background(), uuid.New(), filters)
			expected := error(original)
			if tt.busy {
				expected = ErrReviewExportBusy
			}
			require.ErrorIs(t, err, expected, "only transaction contention maps to the retryable busy error")
		})
	}
}
