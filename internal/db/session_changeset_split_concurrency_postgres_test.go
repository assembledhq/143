package db

import (
	"context"
	"errors"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

func TestSessionChangesetStoreConcurrentSplitReplacementPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		operation  string
		secondPath string
	}{
		{name: "later assignment replaces an earlier disjoint path", operation: "paths", secondPath: "gamma.go"},
		{name: "later assignment replaces the same path without a duplicate key", operation: "paths", secondPath: "beta.go"},
		{name: "later omission replaces an earlier disjoint path", operation: "omissions", secondPath: "gamma.go"},
		{name: "later omission replaces the same path and reason without a duplicate key", operation: "omissions", secondPath: "beta.go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			fixtureTx := newChangesetSplitPostgres(t, ctx)
			f := seedChangesetSplitPostgres(t, ctx, fixtureTx)
			beforePaths := readChangesetSplitPathsPostgres(t, ctx, fixtureTx)
			beforeOmissions := readChangesetSplitOmissionsPostgres(t, ctx, fixtureTx)
			var schema string
			require.NoError(t, fixtureTx.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema), "capture the isolated schema shared by the two concurrent connections")
			firstConn := fixtureTx.Conn()
			require.NoError(t, fixtureTx.Commit(ctx), "commit synthetic split fixtures so both connections see the same initial rows")
			t.Cleanup(func() {
				_, err := firstConn.Exec(context.Background(), `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
				require.NoError(t, err, "remove the committed isolated concurrency schema and synthetic rows")
			})
			searchPath := `SET search_path TO ` + pgx.Identifier{schema}.Sanitize() + `,public`
			_, err := firstConn.Exec(ctx, searchPath)
			require.NoError(t, err, "bind the first connection to its committed isolated schema")
			firstTx, err := firstConn.Begin(ctx)
			require.NoError(t, err, "begin the first replacement transaction before holding its advisory key")
			firstCommitted := false
			t.Cleanup(func() {
				err := firstTx.Rollback(context.Background())
				if firstCommitted && errors.Is(err, pgx.ErrTxClosed) {
					return
				}
				require.NoError(t, err, "release the first replacement transaction if it did not commit")
			})
			lockSQL := `SELECT pg_advisory_xact_lock(hashtextextended($1::uuid::text || ':' || $2::uuid::text, 0))`
			lockArgs := []any{f.target.orgID, f.target.sessionID}
			if tt.operation == "paths" {
				lockSQL = `SELECT pg_advisory_xact_lock(hashtextextended($1::uuid::text || ':' || $2::uuid::text || ':' || $3::uuid::text, 0))`
				lockArgs = append(lockArgs, f.target.changesetID)
			}
			_, err = firstTx.Exec(ctx, lockSQL, lockArgs...)
			require.NoError(t, err, "hold the original exact organization, session, and optional changeset advisory key before either save")

			secondConn, err := pgx.Connect(ctx, os.Getenv("TEST_DATABASE_URL"))
			require.NoError(t, err, "open a separate PostgreSQL connection for the competing store call")
			t.Cleanup(func() {
				require.NoError(t, secondConn.Close(context.Background()), "close the competing replacement connection")
			})
			secondConn.TypeMap().RegisterType(&pgtype.Type{Name: "timestamptz", OID: pgtype.TimestamptzOID, Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC}})
			_, err = secondConn.Exec(ctx, searchPath)
			require.NoError(t, err, "bind the competing connection to the same committed schema")
			secondTx, err := secondConn.Begin(ctx)
			require.NoError(t, err, "begin the second replacement transaction while the first holds its key")
			var secondCreatedAt time.Time
			require.NoError(t, secondTx.QueryRow(ctx, `SELECT now()`).Scan(&secondCreatedAt), "capture the exact timestamp for rows written by the later transaction")

			replace := func(ctx context.Context, database DBTX, path, reason string) error {
				store := NewSessionChangesetStore(database)
				if tt.operation == "paths" {
					return store.ReplaceSplitPaths(ctx, f.target.orgID, f.target.sessionID, f.target.changesetID, []string{path})
				}
				return store.ReplaceSplitOmissions(ctx, f.target.orgID, f.target.sessionID, f.userID, []models.ChangesetSplitOmission{{Path: path, Reason: reason}})
			}
			secondCtx, cancelSecond := context.WithCancel(ctx)
			result := make(chan error, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				err := replace(secondCtx, secondTx, tt.secondPath, "Later synthetic reason")
				if err != nil {
					result <- errors.Join(err, secondTx.Rollback(context.Background()))
					return
				}
				result <- secondTx.Commit(secondCtx)
			}()
			t.Cleanup(func() {
				cancelSecond()
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					t.Error("competing replacement should finish after cancellation before its connection closes")
				}
			})
			var waitErr, earlyErr error
			completedBeforeWait := false
			require.Eventually(t, func() bool {
				select {
				case earlyErr = <-result:
					completedBeforeWait = true
					return true
				default:
				}
				var waiting bool
				waitErr = firstTx.QueryRow(ctx, `SELECT EXISTS (
    SELECT 1 FROM pg_locks WHERE pid = $1 AND locktype = 'advisory' AND NOT granted
)`, int32(secondConn.PgConn().PID())).Scan(&waiting)
				return waitErr != nil || waiting
			}, 5*time.Second, 10*time.Millisecond, "the competing exported store call should visibly wait on the exact original advisory key")
			require.NoError(t, waitErr, "read PostgreSQL lock wait evidence before allowing the first replacement to write")
			require.NoError(t, earlyErr, "the competing store call should not fail before reaching the lock wait")
			require.False(t, completedBeforeWait, "the competing store call must wait for the held key before completing")

			require.NoError(t, replace(ctx, firstTx, "beta.go", "Earlier synthetic reason"), "the first actual store call should save while holding its own advisory key")
			require.NoError(t, firstTx.Commit(ctx), "commit the earlier replacement before releasing the waiting store call")
			firstCommitted = true
			select {
			case err := <-result:
				require.NoError(t, err, "the waiting actual store replacement should finish after the earlier commit without a duplicate key")
			case <-ctx.Done():
				t.Fatalf("waiting replacement should complete after the earlier commit: %v", ctx.Err())
			}

			if tt.operation == "paths" {
				expected := []changesetSplitPathPostgresRow{}
				for _, row := range beforePaths {
					if row.ChangesetID != f.target.changesetID {
						expected = append(expected, row)
					}
				}
				expected = append(expected, changesetSplitPathPostgresRow{
					OrgID: f.target.orgID, SessionID: f.target.sessionID, ChangesetID: f.target.changesetID,
					Path: tt.secondPath, CreatedAt: secondCreatedAt,
				})
				sort.Slice(expected, func(i, j int) bool { return expected[i].Path < expected[j].Path })
				require.Equal(t, expected, readChangesetSplitPathsPostgres(t, ctx, firstConn), "the later save should replace the earlier assignment exactly and preserve unrelated rows")
				require.Equal(t, beforeOmissions, readChangesetSplitOmissionsPostgres(t, ctx, firstConn), "concurrent assignment saves should preserve all omissions")
			} else {
				expected := []changesetSplitOmissionPostgresRow{}
				for _, row := range beforeOmissions {
					if row.SessionID != f.target.sessionID {
						expected = append(expected, row)
					}
				}
				expected = append(expected, changesetSplitOmissionPostgresRow{
					OrgID: f.target.orgID, SessionID: f.target.sessionID, Path: tt.secondPath,
					Reason: "Later synthetic reason", ConfirmedByUserID: f.userID, CreatedAt: secondCreatedAt,
				})
				sort.Slice(expected, func(i, j int) bool { return expected[i].Path < expected[j].Path })
				require.Equal(t, expected, readChangesetSplitOmissionsPostgres(t, ctx, firstConn), "the later save should replace the earlier omission and reason exactly while preserving unrelated rows")
				require.Equal(t, beforePaths, readChangesetSplitPathsPostgres(t, ctx, firstConn), "concurrent omission saves should preserve all assignments")
			}
		})
	}
}
