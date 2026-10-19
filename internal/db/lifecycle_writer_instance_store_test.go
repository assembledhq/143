package db

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestLifecycleWriterInstanceStore_ObserveValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		instanceID uuid.UUID
		role       models.LifecycleWriterRole
		buildSHA   string
		wantErr    string
	}{
		{
			name: "zero process identity",
			role: models.LifecycleWriterRoleSessionExecutor, buildSHA: "test-build",
			wantErr: "observe lifecycle writer instance: instance ID must be nonzero",
		},
		{
			name: "empty role", instanceID: uuid.New(), buildSHA: "test-build",
			wantErr: `observe lifecycle writer instance: invalid LifecycleWriterRole: ""`,
		},
		{
			name: "unknown role", instanceID: uuid.New(), role: "other", buildSHA: "test-build",
			wantErr: `observe lifecycle writer instance: invalid LifecycleWriterRole: "other"`,
		},
		{
			name: "empty build", instanceID: uuid.New(), role: models.LifecycleWriterRoleSessionExecutor,
			wantErr: "observe lifecycle writer instance: build SHA must contain non-whitespace text and be at most 128 bytes",
		},
		{
			name: "whitespace build", instanceID: uuid.New(), role: models.LifecycleWriterRoleSessionExecutor, buildSHA: " \t\n",
			wantErr: "observe lifecycle writer instance: build SHA must contain non-whitespace text and be at most 128 bytes",
		},
		{
			name: "oversized build", instanceID: uuid.New(), role: models.LifecycleWriterRoleSessionExecutor, buildSHA: strings.Repeat("b", 129),
			wantErr: "observe lifecycle writer instance: build SHA must contain non-whitespace text and be at most 128 bytes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create an isolated observation validation fixture")
			defer mock.Close()

			err = NewLifecycleWriterInstanceStore(mock).Observe(context.Background(), tt.instanceID, tt.role, tt.buildSHA)
			require.EqualError(t, err, tt.wantErr, "invalid process identity must fail before issuing SQL")
			require.NoError(t, mock.ExpectationsWereMet(), "validation must not send a registry write")
		})
	}
}

func TestLifecycleWriterInstanceStore_Observe(t *testing.T) {
	t.Parallel()

	const expectedSQL = `
		WITH observation AS (
			SELECT statement_timestamp() AS observed_at
		)
		INSERT INTO lifecycle_writer_instances (
			instance_id, role, build_sha, lifecycle_writer_version, last_heartbeat, expires_at
		)
		SELECT $1, $2, $3, $4, observed_at, observed_at + interval '2 minutes'
		FROM observation
		ON CONFLICT (instance_id) DO UPDATE
		SET last_heartbeat = EXCLUDED.last_heartbeat,
			expires_at = EXCLUDED.expires_at
		WHERE lifecycle_writer_instances.role = EXCLUDED.role
		  AND lifecycle_writer_instances.build_sha = EXCLUDED.build_sha
		  AND lifecycle_writer_instances.lifecycle_writer_version = EXCLUDED.lifecycle_writer_version`
	databaseErr := errors.New("database unavailable")
	tests := []struct {
		name     string
		role     models.LifecycleWriterRole
		buildSHA string
		rows     int64
		dbErr    error
		wantErr  error
	}{
		{name: "api process", role: models.LifecycleWriterRoleAPI, buildSHA: "api-build", rows: 1},
		{name: "worker process", role: models.LifecycleWriterRoleWorker, buildSHA: "worker-build", rows: 1},
		{name: "one combined process", role: models.LifecycleWriterRoleAll, buildSHA: "all-build", rows: 1},
		{name: "fresh observation", role: models.LifecycleWriterRoleSessionExecutor, buildSHA: "test-build", rows: 1},
		{name: "renew same identity", role: models.LifecycleWriterRoleSessionExecutor, buildSHA: "same-build", rows: 1},
		{name: "development build", role: models.LifecycleWriterRoleSessionExecutor, buildSHA: "dev", rows: 1},
		{name: "maximum build length", role: models.LifecycleWriterRoleSessionExecutor, buildSHA: strings.Repeat("b", 128), rows: 1},
		{name: "immutable identity differs", role: models.LifecycleWriterRoleSessionExecutor, buildSHA: "different-build", wantErr: ErrLifecycleWriterInstanceIdentityMismatch},
		{name: "database error", role: models.LifecycleWriterRoleSessionExecutor, buildSHA: "test-build", dbErr: databaseErr, wantErr: databaseErr},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
			require.NoError(t, err, "create an isolated observation SQL fixture")
			defer mock.Close()

			instanceID := uuid.New()
			expectation := mock.ExpectExec(expectedSQL).
				WithArgs(instanceID, tt.role, tt.buildSHA, 0)
			if tt.dbErr != nil {
				expectation.WillReturnError(tt.dbErr)
			} else {
				expectation.WillReturnResult(pgxmock.NewResult("INSERT", tt.rows))
			}

			err = NewLifecycleWriterInstanceStore(mock).Observe(context.Background(), instanceID, tt.role, tt.buildSHA)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr, "failed observations must preserve the identity or database failure")
			} else {
				require.NoError(t, err, "fresh and matching observations should record version zero")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "observation must only update timestamps for a matching immutable identity using one database clock")
		})
	}
}

func TestLifecycleWriterInstanceStore_DeleteExpired(t *testing.T) {
	t.Parallel()

	const expectedSQL = `
		WITH expired AS (
			SELECT instance_id
			FROM lifecycle_writer_instances
			WHERE expires_at < statement_timestamp() - interval '7 days'
			ORDER BY expires_at, instance_id
			LIMIT 1000
			FOR UPDATE SKIP LOCKED
		)
		DELETE FROM lifecycle_writer_instances AS writer
		USING expired
		WHERE writer.instance_id = expired.instance_id
		  AND writer.expires_at < statement_timestamp() - interval '7 days'`
	databaseErr := errors.New("database unavailable")
	tests := []struct {
		name    string
		rows    int64
		dbErr   error
		want    int64
		wantErr string
	}{
		{name: "nothing expired"},
		{name: "one expired observation", rows: 1, want: 1},
		{name: "full bounded sweep", rows: 1000, want: 1000},
		{name: "database error", dbErr: databaseErr, wantErr: "delete expired lifecycle writer instances: database unavailable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
			require.NoError(t, err, "create an isolated bounded retention fixture")
			defer mock.Close()

			expectation := mock.ExpectExec(expectedSQL)
			if tt.dbErr != nil {
				expectation.WillReturnError(tt.dbErr)
			} else {
				expectation.WillReturnResult(pgxmock.NewResult("DELETE", tt.rows))
			}

			actual, err := NewLifecycleWriterInstanceStore(mock).DeleteExpired(context.Background())
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr, "retention failures should include operation context")
				require.ErrorIs(t, err, databaseErr, "retention failures should preserve the database cause")
			} else {
				require.NoError(t, err, "bounded retention should succeed even when there is no expired work")
			}
			require.Equal(t, tt.want, actual, "retention must return exactly the count removed by its single bounded statement")
			require.NoError(t, mock.ExpectationsWereMet(), "retention must lock and delete only the deterministic bounded expired batch")
		})
	}
}
