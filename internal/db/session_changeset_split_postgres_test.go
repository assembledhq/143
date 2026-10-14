package db

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

func TestSessionChangesetStoreReplaceSplitPathsPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		paths    []string
		fence    string
		expected []string
	}{
		{name: "replaces and normalizes nonempty assignment", paths: []string{" gamma.go ", "beta.go", "beta.go"}, expected: []string{"beta.go", "gamma.go"}},
		{name: "retains an existing path while replacing assignment", paths: []string{"alpha.go", "beta.go"}, expected: []string{"alpha.go", "beta.go"}},
		{name: "clears assignment with nil paths", expected: []string{}},
		{name: "clears assignment with empty paths", paths: []string{}, expected: []string{}},
		{name: "rejects another organization", paths: []string{"beta.go"}, fence: "org"},
		{name: "rejects another session", paths: []string{"beta.go"}, fence: "session"},
		{name: "rejects another session changeset", paths: []string{"beta.go"}, fence: "changeset"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			tx := newChangesetSplitPostgres(t, ctx)
			f := seedChangesetSplitPostgres(t, ctx, tx)
			beforePaths := readChangesetSplitPathsPostgres(t, ctx, tx)
			beforeOmissions := readChangesetSplitOmissionsPostgres(t, ctx, tx)
			orgID, sessionID, changesetID := f.target.orgID, f.target.sessionID, f.target.changesetID
			switch tt.fence {
			case "org":
				orgID = f.foreign.orgID
			case "session":
				sessionID = f.other.sessionID
			case "changeset":
				changesetID = f.other.changesetID
			}
			err := NewSessionChangesetStore(tx).ReplaceSplitPaths(ctx, orgID, sessionID, changesetID, tt.paths)
			expected := beforePaths
			if tt.fence != "" {
				require.ErrorIs(t, err, pgx.ErrNoRows, "an assignment outside its organization and session should be rejected")
			} else {
				require.NoError(t, err, "the actual assignment query should execute with UUID parents and text lock keys")
				expected = []changesetSplitPathPostgresRow{}
				for _, row := range beforePaths {
					if row.ChangesetID != f.target.changesetID {
						expected = append(expected, row)
					}
				}
				for _, path := range tt.expected {
					expected = append(expected, changesetSplitPathPostgresRow{
						OrgID: f.target.orgID, SessionID: f.target.sessionID, ChangesetID: f.target.changesetID,
						Path: path, CreatedAt: f.createdAt,
					})
				}
				sort.Slice(expected, func(i, j int) bool { return expected[i].Path < expected[j].Path })
			}
			require.Equal(t, expected, readChangesetSplitPathsPostgres(t, ctx, tx), "replacement should persist exactly the normalized target paths and preserve sibling, session, and tenant assignments")
			require.Equal(t, beforeOmissions, readChangesetSplitOmissionsPostgres(t, ctx, tx), "assignment replacement should preserve every existing omission")
		})
	}
}

func TestSessionChangesetStoreReplaceSplitOmissionsPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		omissions   []models.ChangesetSplitOmission
		fence       string
		expected    []models.ChangesetSplitOmission
		expectedErr error
	}{
		{
			name:      "replaces and normalizes nonempty omissions",
			omissions: []models.ChangesetSplitOmission{{Path: " gamma.go ", Reason: "  Deferred gamma change  "}, {Path: "beta.go", Reason: "Deferred beta change"}},
			expected:  []models.ChangesetSplitOmission{{Path: "beta.go", Reason: "Deferred beta change"}, {Path: "gamma.go", Reason: "Deferred gamma change"}},
		},
		{
			name:      "replaces the reason for an existing omission",
			omissions: []models.ChangesetSplitOmission{{Path: "docs.md", Reason: "Updated synthetic reason"}},
			expected:  []models.ChangesetSplitOmission{{Path: "docs.md", Reason: "Updated synthetic reason"}},
		},
		{name: "clears omissions with nil input", expected: []models.ChangesetSplitOmission{}},
		{name: "clears omissions with empty input", omissions: []models.ChangesetSplitOmission{}, expected: []models.ChangesetSplitOmission{}},
		{name: "rejects another organization", omissions: []models.ChangesetSplitOmission{{Path: "beta.go", Reason: "Deferred beta change"}}, fence: "org", expectedErr: pgx.ErrNoRows},
		{name: "rejects a foreign organization session", omissions: []models.ChangesetSplitOmission{{Path: "beta.go", Reason: "Deferred beta change"}}, fence: "session", expectedErr: pgx.ErrNoRows},
		{
			name:        "rejects mixed assigned and unassigned paths without changing omissions",
			omissions:   []models.ChangesetSplitOmission{{Path: "alpha.go", Reason: "Already assigned"}, {Path: "beta.go", Reason: "Unassigned path"}},
			expectedErr: ErrInvalidSplitPath,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			tx := newChangesetSplitPostgres(t, ctx)
			f := seedChangesetSplitPostgres(t, ctx, tx)
			beforePaths := readChangesetSplitPathsPostgres(t, ctx, tx)
			beforeOmissions := readChangesetSplitOmissionsPostgres(t, ctx, tx)
			orgID, sessionID := f.target.orgID, f.target.sessionID
			switch tt.fence {
			case "org":
				orgID = f.foreign.orgID
			case "session":
				sessionID = f.foreign.sessionID
			}
			err := NewSessionChangesetStore(tx).ReplaceSplitOmissions(ctx, orgID, sessionID, f.userID, tt.omissions)
			expected := beforeOmissions
			if tt.expectedErr != nil {
				require.ErrorIs(t, err, tt.expectedErr, "invalid ownership or already assigned omission input should return its exact rejection error")
			} else {
				require.NoError(t, err, "the actual omission query should execute with UUID parents and text lock keys")
				expected = []changesetSplitOmissionPostgresRow{}
				for _, row := range beforeOmissions {
					if row.SessionID != f.target.sessionID {
						expected = append(expected, row)
					}
				}
				for _, omission := range tt.expected {
					expected = append(expected, changesetSplitOmissionPostgresRow{
						OrgID: f.target.orgID, SessionID: f.target.sessionID, Path: omission.Path, Reason: omission.Reason,
						ConfirmedByUserID: f.userID, CreatedAt: f.createdAt,
					})
				}
				sort.Slice(expected, func(i, j int) bool { return expected[i].Path < expected[j].Path })
			}
			require.Equal(t, expected, readChangesetSplitOmissionsPostgres(t, ctx, tx), "replacement should persist exact trimmed omissions and confirming user while preserving other sessions and tenants")
			require.Equal(t, beforePaths, readChangesetSplitPathsPostgres(t, ctx, tx), "omission replacement should preserve every existing path assignment")
		})
	}
}

type changesetSplitPostgresScope struct {
	orgID, sessionID, changesetID uuid.UUID
}

type changesetSplitPostgresFixture struct {
	target, other, foreign changesetSplitPostgresScope
	userID                 uuid.UUID
	createdAt              time.Time
}

type changesetSplitPathPostgresRow struct {
	OrgID       uuid.UUID
	SessionID   uuid.UUID
	ChangesetID uuid.UUID
	Path        string
	CreatedAt   time.Time
}

type changesetSplitOmissionPostgresRow struct {
	OrgID             uuid.UUID
	SessionID         uuid.UUID
	Path              string
	Reason            string
	ConfirmedByUserID uuid.UUID
	CreatedAt         time.Time
}

func seedChangesetSplitPostgres(t *testing.T, ctx context.Context, tx pgx.Tx) changesetSplitPostgresFixture {
	t.Helper()
	f := changesetSplitPostgresFixture{
		target:  changesetSplitPostgresScope{orgID: uuid.New(), sessionID: uuid.New(), changesetID: uuid.New()},
		foreign: changesetSplitPostgresScope{orgID: uuid.New(), sessionID: uuid.New(), changesetID: uuid.New()},
		userID:  uuid.New(),
	}
	f.other = changesetSplitPostgresScope{orgID: f.target.orgID, sessionID: uuid.New(), changesetID: uuid.New()}
	require.NoError(t, tx.QueryRow(ctx, `SELECT now()`).Scan(&f.createdAt), "capture the transaction timestamp for exact persisted row comparisons")
	_, err := tx.Exec(ctx, `INSERT INTO users (id) VALUES ($1)`, f.userID)
	require.NoError(t, err, "seed the synthetic user confirming omitted paths")
	for _, scope := range []changesetSplitPostgresScope{f.target, f.other, f.foreign} {
		_, err := tx.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1) ON CONFLICT DO NOTHING`, scope.orgID)
		require.NoError(t, err, "seed each synthetic tenant without sharing mutable fixture state")
		snapshotID := uuid.New()
		_, err = tx.Exec(ctx, `INSERT INTO sessions (id, org_id, latest_diff_snapshot_id) VALUES ($1,$2,$3)`, scope.sessionID, scope.orgID, snapshotID)
		require.NoError(t, err, "seed the synthetic session and frozen diff pointer")
		var diff strings.Builder
		for _, path := range []string{"alpha.go", "beta.go", "gamma.go", "docs.md", "same-session.go", "other-session.go", "other-session.md", "other-tenant.go", "other-tenant.md"} {
			diff.WriteString("diff --git a/" + path + " b/" + path + "\n+synthetic change\n")
		}
		_, err = tx.Exec(ctx, `INSERT INTO session_diff_snapshots (id, org_id, session_id, diff) VALUES ($1,$2,$3,$4)`, snapshotID, scope.orgID, scope.sessionID, diff.String())
		require.NoError(t, err, "seed a complete synthetic diff with all requested repository paths")
		_, err = tx.Exec(ctx, `INSERT INTO session_changesets (id, org_id, session_id, title, order_index, target_branch, base_branch) VALUES ($1,$2,$3,'Synthetic split',0,'main','main')`, scope.changesetID, scope.orgID, scope.sessionID)
		require.NoError(t, err, "seed the UUID changeset ownership tuple used by the exported store lookup")
		require.NoError(t, NewSessionChangesetStore(tx).InitializeSplit(ctx, scope.orgID, scope.sessionID), "initialize a draft plan through the actual tenant-scoped store method")
		path, omissionPath := "alpha.go", "docs.md"
		if scope == f.other {
			path, omissionPath = "other-session.go", "other-session.md"
		} else if scope == f.foreign {
			path, omissionPath = "other-tenant.go", "other-tenant.md"
		}
		_, err = tx.Exec(ctx, `INSERT INTO session_changeset_split_paths (org_id, session_id, changeset_id, path) VALUES ($1,$2,$3,$4)`, scope.orgID, scope.sessionID, scope.changesetID, path)
		require.NoError(t, err, "seed target and unrelated path assignments for replacement isolation")
		_, err = tx.Exec(ctx, `INSERT INTO session_changeset_split_omissions (org_id, session_id, path, reason, confirmed_by_user_id) VALUES ($1,$2,$3,'Keep original omission',$4)`, scope.orgID, scope.sessionID, omissionPath, f.userID)
		require.NoError(t, err, "seed target and unrelated omissions for replacement isolation")
	}
	siblingID := uuid.New()
	_, err = tx.Exec(ctx, `INSERT INTO session_changesets (id, org_id, session_id, title, order_index, target_branch, base_branch) VALUES ($1,$2,$3,'Sibling split',1,'main','main')`, siblingID, f.target.orgID, f.target.sessionID)
	require.NoError(t, err, "seed another changeset in the target session")
	_, err = tx.Exec(ctx, `INSERT INTO session_changeset_split_paths (org_id, session_id, changeset_id, path) VALUES ($1,$2,$3,'same-session.go')`, f.target.orgID, f.target.sessionID, siblingID)
	require.NoError(t, err, "seed a sibling assignment that must survive replacing target paths")
	return f
}

func readChangesetSplitPathsPostgres(t *testing.T, ctx context.Context, tx DBTX) []changesetSplitPathPostgresRow {
	t.Helper()
	rows, err := tx.Query(ctx, `SELECT org_id, session_id, changeset_id, path, created_at FROM session_changeset_split_paths ORDER BY path`)
	require.NoError(t, err, "read all synthetic assignments to detect mutations outside the target tuple")
	actual, err := pgx.CollectRows(rows, pgx.RowToStructByName[changesetSplitPathPostgresRow])
	require.NoError(t, err, "collect complete persisted assignment rows")
	return actual
}

func readChangesetSplitOmissionsPostgres(t *testing.T, ctx context.Context, tx DBTX) []changesetSplitOmissionPostgresRow {
	t.Helper()
	rows, err := tx.Query(ctx, `SELECT org_id, session_id, path, reason, confirmed_by_user_id, created_at FROM session_changeset_split_omissions ORDER BY path`)
	require.NoError(t, err, "read all synthetic omissions to detect mutations outside the target tuple")
	actual, err := pgx.CollectRows(rows, pgx.RowToStructByName[changesetSplitOmissionPostgresRow])
	require.NoError(t, err, "collect complete persisted omission rows")
	return actual
}

func newChangesetSplitPostgres(t *testing.T, ctx context.Context) pgx.Tx {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL for session split UUID parameter and replacement isolation proof")
	}
	conn, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err, "connect to disposable PostgreSQL for actual split store execution")
	t.Cleanup(func() {
		require.NoError(t, conn.Close(context.Background()), "close the isolated split regression connection")
	})
	conn.TypeMap().RegisterType(&pgtype.Type{Name: "timestamptz", OID: pgtype.TimestamptzOID, Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC}})
	innerTx, err := conn.Begin(ctx)
	require.NoError(t, err, "begin an isolated transaction for each parallel split case")
	tx := &changesetSplitPostgresTx{Tx: innerTx}
	t.Cleanup(func() {
		err := tx.Rollback(context.Background())
		if tx.committed && errors.Is(err, pgx.ErrTxClosed) {
			return
		}
		require.NoError(t, err, "roll back the isolated schema unless its fixture was successfully committed")
	})
	schema := "changeset_split_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = tx.Exec(ctx, `CREATE SCHEMA `+schema+`; SET LOCAL search_path TO `+schema+`,public`)
	require.NoError(t, err, "create a distinct schema for each parallel regression case")
	_, err = tx.Exec(ctx, `
CREATE TABLE organizations (id uuid PRIMARY KEY);
CREATE TABLE users (id uuid PRIMARY KEY);
CREATE TABLE sessions (
    id uuid PRIMARY KEY, org_id uuid NOT NULL REFERENCES organizations(id),
    latest_diff_snapshot_id uuid, deleted_at timestamptz, UNIQUE (id, org_id)
);
CREATE TABLE session_diff_snapshots (
    id uuid PRIMARY KEY, org_id uuid NOT NULL REFERENCES organizations(id), session_id uuid NOT NULL,
    diff text NOT NULL, review_bundle_truncated boolean NOT NULL DEFAULT false,
    UNIQUE (id, org_id, session_id), FOREIGN KEY (session_id, org_id) REFERENCES sessions(id, org_id)
);
CREATE TABLE session_changesets (
    id uuid PRIMARY KEY, org_id uuid NOT NULL REFERENCES organizations(id), session_id uuid NOT NULL,
    is_primary boolean NOT NULL DEFAULT false, order_index integer NOT NULL, title text NOT NULL,
    summary text NOT NULL DEFAULT '', status text NOT NULL DEFAULT 'planned', target_branch text NOT NULL,
    base_branch text NOT NULL, working_branch text, stacked_on_changeset_id uuid, head_sha text,
    expected_remote_head_sha text, base_head_sha text, worktree_path text, materialization_error text,
    materialized_diff text, restack_delta_kind text, restack_delta_summary text,
    restack_confirmation_required boolean NOT NULL DEFAULT false, pr_creation_state text NOT NULL DEFAULT 'idle',
    pr_creation_error text, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, org_id, session_id), FOREIGN KEY (session_id, org_id) REFERENCES sessions(id, org_id)
);
CREATE TABLE session_changeset_split_plans (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(), org_id uuid NOT NULL REFERENCES organizations(id),
    session_id uuid NOT NULL, source_diff_snapshot_id uuid NOT NULL,
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'accepted')),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, session_id), FOREIGN KEY (session_id, org_id) REFERENCES sessions(id, org_id),
    FOREIGN KEY (source_diff_snapshot_id, org_id, session_id) REFERENCES session_diff_snapshots(id, org_id, session_id)
);
CREATE TABLE session_changeset_split_paths (
    org_id uuid NOT NULL REFERENCES organizations(id), session_id uuid NOT NULL, changeset_id uuid NOT NULL,
    path text NOT NULL CHECK (path <> '' AND path = btrim(path)), created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, session_id, changeset_id, path),
    FOREIGN KEY (org_id, session_id) REFERENCES session_changeset_split_plans(org_id, session_id),
    FOREIGN KEY (changeset_id, org_id, session_id) REFERENCES session_changesets(id, org_id, session_id)
);
CREATE TABLE session_changeset_split_omissions (
    org_id uuid NOT NULL REFERENCES organizations(id), session_id uuid NOT NULL,
    path text NOT NULL CHECK (path <> '' AND path = btrim(path)), reason text NOT NULL CHECK (btrim(reason) <> ''),
    confirmed_by_user_id uuid NOT NULL REFERENCES users(id), created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, session_id, path), FOREIGN KEY (org_id, session_id) REFERENCES session_changeset_split_plans(org_id, session_id)
);`)
	require.NoError(t, err, "create minimal UUID parent and split table shapes with the production ownership constraints")
	return tx
}

type changesetSplitPostgresTx struct {
	pgx.Tx
	committed bool
}

func (tx *changesetSplitPostgresTx) Commit(ctx context.Context) error {
	err := tx.Tx.Commit(ctx)
	if err == nil {
		tx.committed = true
	}
	return err
}
