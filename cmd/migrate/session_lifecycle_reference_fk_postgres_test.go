package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestSessionLifecycleReferenceForeignKeysPostgres(t *testing.T) {
	t.Parallel()
	migration, conn := newContentMigrationPostgres(t, true)
	require.NoError(t, migration.Migrate(331), "build all three referenced keys through the real migration source")
	createLifecycleReferenceProbeTables(t, context.Background(), conn)
	tests := []struct {
		name, kind, mismatch, code string
		threadless                 bool
	}{
		{name: "exact thread", kind: "thread"},
		{name: "thread foreign organization", kind: "thread", mismatch: "org", code: "23503"},
		{name: "thread wrong session", kind: "thread", mismatch: "session", code: "23503"},
		{name: "thread wrong id", kind: "thread", mismatch: "id", code: "23503"},
		{name: "exact threaded request", kind: "request"},
		{name: "exact threadless request", kind: "request", threadless: true},
		{name: "request foreign organization", kind: "request", mismatch: "org", code: "23503"},
		{name: "request wrong session", kind: "request", mismatch: "session", code: "23503"},
		{name: "request wrong id", kind: "request", mismatch: "id", code: "23503"},
		{name: "exact runtime", kind: "runtime"},
		{name: "runtime foreign organization", kind: "runtime", mismatch: "org", code: "23503"},
		{name: "runtime wrong session", kind: "runtime", mismatch: "session", code: "23503"},
		{name: "runtime wrong lane", kind: "runtime", mismatch: "thread", code: "23503"},
		{name: "runtime wrong id", kind: "runtime", mismatch: "id", code: "23503"},
		{name: "runtime present without lane cannot escape match simple", kind: "runtime", mismatch: "missing lane", code: "23514"},
		{name: "no runtime and no lane is an allowed absent reference", kind: "runtime", mismatch: "absent reference"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, actor := lifecycleReferenceProbeConnection(t, conn)
			fixture := seedEpochMigrationFixture(t, ctx, actor)
			org, session, parentID := fixture.orgID, fixture.sessionID, fixture.threadID
			var lane, runtimeID *uuid.UUID
			lane = &fixture.threadID
			constraint := "fk_lifecycle_probe_thread"
			query := `INSERT INTO lifecycle_probe_thread(id,org_id,session_id,thread_id) VALUES($1,$2,$3,$4)`
			if tt.kind == "request" {
				request := &models.HumanInputRequest{OrgID: fixture.orgID, SessionID: fixture.sessionID, ThreadID: lane, AgentType: models.DefaultDefaultAgentType, Kind: models.HumanInputRequestKindFreeText, Title: "Synthetic referenced request", Body: "Synthetic input"}
				if tt.threadless {
					request.ThreadID = nil
					lane = nil
				}
				require.NoError(t, db.NewSessionHumanInputRequestStore(actor).Create(ctx, request), "create a real scoped request through unchanged legacy SQL")
				parentID = request.ID
				constraint = "fk_lifecycle_probe_request"
				query = `INSERT INTO lifecycle_probe_request(id,org_id,session_id,request_id,thread_id) VALUES($1,$2,$3,$4,$5)`
			} else if tt.kind == "runtime" {
				runtime := createExecutionEpochLegacyRuntime(t, ctx, actor, fixture)
				runtimeID = &runtime.ID
				parentID = runtime.ID
				constraint = "fk_lifecycle_probe_runtime"
				query = `INSERT INTO lifecycle_probe_runtime(id,org_id,session_id,runtime_id,thread_id) VALUES($1,$2,$3,$4,$5)`
			}
			switch tt.mismatch {
			case "org":
				org = uuid.New()
			case "session":
				session = uuid.New()
			case "thread":
				wrong := uuid.New()
				lane = &wrong
			case "id":
				parentID = uuid.New()
			case "missing lane":
				lane = nil
				constraint = "chk_lifecycle_probe_runtime_lane"
			case "absent reference":
				lane, runtimeID = nil, nil
			}
			id := uuid.New()
			args := []any{id, org, session, parentID}
			if tt.kind == "request" {
				args = append(args, lane)
			} else if tt.kind == "runtime" {
				if tt.mismatch == "id" {
					runtimeID = &parentID
				}
				args[3] = runtimeID
				args = append(args, lane)
			}
			_, err := actor.Exec(ctx, query, args...)
			wantRows := int64(1)
			if tt.code == "" {
				require.NoError(t, err, "a complete exact tuple or explicit absent optional reference must satisfy the synthetic strong FK")
			} else {
				var pgErr *pgconn.PgError
				require.ErrorAs(t, err, &pgErr, "PostgreSQL must reject the mismatched tuple or missing runtime lane")
				require.Equal(t, tt.code, pgErr.Code, "the exact FK or MATCH SIMPLE guard must produce its intended SQLSTATE")
				require.Equal(t, constraint, pgErr.ConstraintName, "rejection must come from the intended scoped foreign key or lane check")
				wantRows = 0
			}
			var rows int64
			require.NoError(t, actor.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM lifecycle_probe_%s WHERE id=$1`, tt.kind), id).Scan(&rows), "inspect only this case's synthetic child row")
			require.Equal(t, wantRows, rows, "a rejected reference must not leave a partially accepted child")
		})
	}
}

func TestSessionLifecycleReferenceKeysDoNotSupplyCanonicalAuthorityPostgres(t *testing.T) {
	t.Parallel()
	migration, conn := newContentMigrationPostgres(t, true)
	require.NoError(t, migration.Migrate(331), "exercise the same referenced keys without installing any production authority policy")
	createLifecycleReferenceProbeTables(t, context.Background(), conn)
	tests := []struct {
		name, payload     string
		threadlessRequest bool
	}{
		{name: "threaded request FK does not enforce a different child lane", payload: `{"synthetic":"unbound-job"}`},
		{name: "threadless request FK does not imply null child lane", threadlessRequest: true, payload: `{"session_id":"unrelated-synthetic-value","thread_id":"unrelated-synthetic-value"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, actor := lifecycleReferenceProbeConnection(t, conn)
			fixture := seedEpochMigrationFixture(t, ctx, actor)
			request := &models.HumanInputRequest{OrgID: fixture.orgID, SessionID: fixture.sessionID, ThreadID: &fixture.threadID, AgentType: models.DefaultDefaultAgentType, Kind: models.HumanInputRequestKindFreeText, Title: "Synthetic authority boundary", Body: "Synthetic input"}
			if tt.threadlessRequest {
				request.ThreadID = nil
			}
			require.NoError(t, db.NewSessionHumanInputRequestStore(actor).Create(ctx, request), "create the actual request whose nullable lane remains outside the referenced triple")
			id, unrelatedLane := uuid.New(), uuid.New()
			_, err := actor.Exec(ctx, `INSERT INTO lifecycle_probe_request(id,org_id,session_id,request_id,thread_id,job_payload) VALUES($1,$2,$3,$4,$5,$6)`, id, fixture.orgID, fixture.sessionID, request.ID, unrelatedLane, tt.payload)
			require.NoError(t, err, "the exact request triple alone deliberately cannot validate nullable lane equality or job JSON identity")
			var lane uuid.UUID
			var matchesPayload bool
			require.NoError(t, actor.QueryRow(ctx, `SELECT thread_id,job_payload=$2::jsonb FROM lifecycle_probe_request WHERE id=$1`, id, tt.payload).Scan(&lane, &matchesPayload), "inspect the deliberately unvalidated synthetic lane and job document")
			require.Equal(t, unrelatedLane, lane, "successful FK validation is not proof that the request's nullable lane matches its consumer")
			require.True(t, matchesPayload, "the referenced index must not be represented as validation of job JSON ownership")
		})
	}
}

func TestSessionLifecycleReferenceIndexNameConflictStaysDirtyPostgres(t *testing.T) {
	t.Parallel()
	tests := lifecycleReferenceIndexCases()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			migration, conn := newContentMigrationPostgres(t, true)
			require.NoError(t, migration.Migrate(tt.version-1), "construct the exact preceding clean migration boundary")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, err := conn.Exec(ctx, `CREATE TABLE lifecycle_probe_duplicate_index(value integer NOT NULL)`)
			require.NoError(t, err, "create an ordinary duplicate probe table only in this owned disposable database")
			_, err = conn.Exec(ctx, `INSERT INTO lifecycle_probe_duplicate_index(value) VALUES(1),(1)`)
			require.NoError(t, err, "seed deliberate synthetic duplicates rather than corrupting production parent keys")
			// This is an explicit NAME CONFLICT fixture: a real failed concurrent
			// build on another table leaves the same schema-wide index name. It is
			// not a simulation or proof of interruption during the target build.
			_, err = conn.Exec(ctx, fmt.Sprintf(`CREATE UNIQUE INDEX CONCURRENTLY %s ON lifecycle_probe_duplicate_index(value)`, tt.index))
			var buildErr *pgconn.PgError
			require.ErrorAs(t, err, &buildErr, "the real concurrent duplicate build must fail before the migration is attempted")
			require.Equal(t, "23505", buildErr.Code, "the failed fixture build must be a real PostgreSQL uniqueness violation")
			requireLifecycleReferenceInvalidIndex(t, ctx, conn, tt.index)
			err = migration.Migrate(tt.version)
			var migrationErr database.Error
			require.ErrorAs(t, err, &migrationErr, "the real migration must expose rather than skip the invalid named index")
			var namedErr *pq.Error
			require.ErrorAs(t, migrationErr.OrigErr, &namedErr, "inspect the migration driver's original PostgreSQL failure")
			require.Equal(t, pq.ErrorCode("42P07"), namedErr.Code, "a schema-wide conflicting index name must fail with duplicate-relation SQLSTATE")
			require.Contains(t, namedErr.Message, tt.index, "the failure must identify the exact invalid conflicting index")
			version, dirty, err := migration.Version()
			require.NoError(t, err, "read the actual migration ledger after failed standalone concurrent DDL")
			require.Equal(t, tt.version, version, "the dirty marker must remain at the exact attempted migration")
			require.True(t, dirty, "a failed concurrent index migration must remain visibly dirty")
			recorder := &lifecycleReferenceDirtyRepairRecorder{Migrate: migration}
			repairedVersion, repaired, err := repairKnownDirtyMigration(recorder)
			require.EqualError(t, err, fmt.Sprintf("database is dirty at version %d; refusing repair because only versions 267 and 281 are allowlisted", tt.version), "known-dirty repair must refuse every new index version")
			require.Equal(t, uint(0), repairedVersion, "refused repair must not claim any repaired migration version")
			require.False(t, repaired, "the existing allowlist must not auto-repair new concurrent index migrations")
			require.Equal(t, 0, recorder.forceCalls, "repair refusal must never invoke Force against the real ledger")
			requireLifecycleReferenceInvalidIndex(t, ctx, conn, tt.index)
			version, dirty, err = migration.Version()
			require.NoError(t, err, "re-read the ledger after the refused repair")
			require.Equal(t, tt.version, version, "refused repair must not rewind the real dirty marker")
			require.True(t, dirty, "invalid concurrent index state must still require explicit investigation")
			var retryErr migrate.ErrDirty
			require.ErrorAs(t, migration.Migrate(tt.version), &retryErr, "ordinary retry must remain blocked rather than clear or mask the dirty state")
			require.Equal(t, int(tt.version), retryErr.Version, "retry refusal must refer to the same exact dirty migration")
		})
	}
}

func TestSessionLifecycleReferencedIndexDropRetainsDependentForeignKeysPostgres(t *testing.T) {
	t.Parallel()
	migration, conn := newContentMigrationPostgres(t, true)
	require.NoError(t, migration.Migrate(331), "install the exact referenced indexes before constructing synthetic dependents")
	createLifecycleReferenceProbeTables(t, context.Background(), conn)
	for _, tt := range lifecycleReferenceIndexCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, actor := lifecycleReferenceProbeConnection(t, conn)
			_, err := actor.Exec(ctx, fmt.Sprintf(`DROP INDEX CONCURRENTLY %s`, tt.index))
			var dependencyErr *pgconn.PgError
			require.ErrorAs(t, err, &dependencyErr, "concurrent rollback must refuse to remove an index supporting a foreign key")
			require.Equal(t, "2BP01", dependencyErr.Code, "dependency refusal must be an exact dependent-objects SQLSTATE")
			_, err = actor.Exec(ctx, fmt.Sprintf(`DROP INDEX CONCURRENTLY %s CASCADE`, tt.index))
			var cascadeErr *pgconn.PgError
			require.ErrorAs(t, err, &cascadeErr, "concurrent index drop must not allow cascading away child constraints")
			require.Equal(t, "0A000", cascadeErr.Code, "concurrent CASCADE must fail as unsupported rather than delete the foreign key")
			var valid, referenced bool
			require.NoError(t, actor.QueryRow(ctx, `SELECT i.indisvalid,EXISTS(SELECT 1 FROM pg_constraint fk WHERE fk.contype='f' AND fk.conindid=i.indexrelid) FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid WHERE c.relnamespace='public'::regnamespace AND c.relname=$1`, tt.index).Scan(&valid, &referenced), "inspect the retained index and exact FK-to-index dependency")
			require.Equal(t, []bool{true, true}, []bool{valid, referenced}, "both failed drops must preserve the valid referenced index and its child FK")
			var version int64
			var dirty bool
			require.NoError(t, actor.QueryRow(ctx, `SELECT version,dirty FROM schema_migrations`).Scan(&version, &dirty), "inspect the ledger without rerunning migrations after the direct refusal probe")
			require.Equal(t, int64(331), version, "direct rollback refusal must not alter the migration version")
			require.False(t, dirty, "direct rollback refusal must leave the previously clean ledger unchanged")
		})
	}
}

func createLifecycleReferenceProbeTables(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	// PostgreSQL forbids temporary children referencing permanent parents. These
	// ordinary synthetic tables exist only in the helper's owned disposable DB;
	// they are not proposed migrations or production foreign keys.
	queries := []string{
		`CREATE TABLE lifecycle_probe_thread(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL,thread_id uuid NOT NULL,CONSTRAINT fk_lifecycle_probe_thread FOREIGN KEY(org_id,session_id,thread_id) REFERENCES session_threads(org_id,session_id,id))`,
		`CREATE TABLE lifecycle_probe_request(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL,request_id uuid NOT NULL,thread_id uuid,job_payload jsonb,CONSTRAINT fk_lifecycle_probe_request FOREIGN KEY(org_id,session_id,request_id) REFERENCES session_human_input_requests(org_id,session_id,id))`,
		`CREATE TABLE lifecycle_probe_runtime(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL,thread_id uuid,runtime_id uuid,CONSTRAINT chk_lifecycle_probe_runtime_lane CHECK(runtime_id IS NULL OR thread_id IS NOT NULL),CONSTRAINT fk_lifecycle_probe_runtime FOREIGN KEY(org_id,session_id,thread_id,runtime_id) REFERENCES thread_runtimes(org_id,session_id,thread_id,id) MATCH SIMPLE)`,
	}
	// Setup shares one pgx.Conn before parallel child fixtures acquire their own
	// connections, so the three independent DDL statements run sequentially.
	for _, query := range queries {
		_, err := conn.Exec(ctx, query)
		require.NoError(t, err, "create the synthetic child with its exact composite reference and explicit null-lane policy")
	}
}

func lifecycleReferenceProbeConnection(t *testing.T, source *pgx.Conn) (context.Context, *pgx.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	conn, err := pgx.ConnectConfig(ctx, source.Config().Copy())
	require.NoError(t, err, "open an isolated connection for the parallel synthetic reference case")
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		require.NoError(t, conn.Close(closeCtx), "close only this synthetic reference case's connection")
	})
	return ctx, conn
}

func lifecycleReferenceIndexCases() []struct {
	name, index string
	version     uint
} {
	return []struct {
		name, index string
		version     uint
	}{
		{name: "thread key", index: "idx_session_threads_org_session_id", version: 329},
		{name: "request key", index: "idx_session_human_input_requests_org_session_id", version: 330},
		{name: "runtime key", index: "idx_thread_runtimes_org_session_thread_id", version: 331},
	}
}

func requireLifecycleReferenceInvalidIndex(t *testing.T, ctx context.Context, conn *pgx.Conn, name string) {
	t.Helper()
	var valid bool
	var parent string
	require.NoError(t, conn.QueryRow(ctx, `SELECT i.indisvalid,p.relname FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_class p ON p.oid=i.indrelid WHERE c.relnamespace='public'::regnamespace AND c.relname=$1`, name).Scan(&valid, &parent), "inspect the actual invalid named index left by the failed concurrent build")
	require.False(t, valid, "the synthetic name-conflict fixture must remain a genuinely invalid PostgreSQL index")
	require.Equal(t, "lifecycle_probe_duplicate_index", parent, "the fixture must remain explicitly a conflicting probe index rather than a claimed interrupted target build")
}

type lifecycleReferenceDirtyRepairRecorder struct {
	*migrate.Migrate
	forceCalls int
}

func (r *lifecycleReferenceDirtyRepairRecorder) Force(int) error {
	r.forceCalls++
	return errors.New("this reference-index test forbids Force against its real migration ledger")
}
