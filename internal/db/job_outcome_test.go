package db

import (
	"context"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestJobStoreLatestOutcomeFiltersTenantAndGeneration(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "create isolated outcome fixture")
	defer mock.Close()
	orgID := uuid.New()
	key, failure := "review:session:generation:2", "safe failure"
	mock.ExpectQuery(`SELECT status, last_error FROM jobs\s+WHERE org_id = \$1 AND queue = \$2 AND dedupe_key = \$3\s+ORDER BY created_at DESC, id DESC LIMIT 1`).
		WithArgs(orgID, "agent", key).
		WillReturnRows(pgxmock.NewRows([]string{"status", "last_error"}).AddRow(models.JobStatusDeadLetter, &failure))
	status, got, err := NewJobStore(mock).LatestJobOutcomeByDedupeKey(context.Background(), orgID, "agent", key)
	require.NoError(t, err, "read the latest outcome atomically")
	require.Equal(t, models.JobStatusDeadLetter, status, "retain the observed terminal status")
	require.Equal(t, &failure, got, "failure must come from the same latest job")
	require.NoError(t, mock.ExpectationsWereMet(), "outcome lookup must filter tenant, queue, and generation key")
}
