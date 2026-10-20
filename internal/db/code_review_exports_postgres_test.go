package db

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type exportFixture struct {
	conn                           *pgx.Conn
	ctx                            context.Context
	org, repo, pr, session, policy uuid.UUID
	url, schema                    string
}

func newExportFixture(t *testing.T) exportFixture {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL for real PostgreSQL export snapshot tests")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	require.NoError(t, err, "connect isolated export test")
	schema := "review_export_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = conn.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "create independent schema")
	t.Cleanup(func() {
		_, err := conn.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, err, "drop export test schema")
		require.NoError(t, conn.Close(ctx), "close test connection")
	})
	_, err = conn.Exec(ctx, `SET search_path TO `+schema+`,public`)
	require.NoError(t, err, "select test schema")
	_, err = conn.Exec(ctx, `
 CREATE TABLE organizations(id uuid PRIMARY KEY);
 CREATE TABLE repositories(id uuid PRIMARY KEY,org_id uuid NOT NULL,full_name text,github_id bigint DEFAULT 123,UNIQUE(org_id,id));
 CREATE TABLE pull_requests(id uuid PRIMARY KEY,org_id uuid,github_repo text,github_pr_number int,status text,merged_at timestamptz,github_state_synced_at timestamptz);
 CREATE TABLE code_review_policies(id uuid PRIMARY KEY,org_id uuid,repository_id uuid,version int,risk_policy jsonb,description_policy jsonb);
 CREATE TABLE code_review_session_metadata(id uuid PRIMARY KEY,org_id uuid,repository_id uuid,pull_request_id uuid,session_id uuid,policy_id uuid,assessment_id uuid,head_sha text,base_sha text,review_output_key text,github_review_id bigint,decision text,risk_reason_details jsonb,created_at timestamptz,completed_at timestamptz);
 CREATE TABLE code_review_revision_assessments(id uuid PRIMARY KEY,org_id uuid,repository_id uuid,pull_request_id uuid,session_id uuid,policy_id uuid,generation bigint,head_sha text,base_sha text,base_ref text,publication_key text,github_review_id bigint,review_scope text,coverage_complete bool,status text,decision text,risk_reason_details jsonb,created_at timestamptz,completed_at timestamptz);
 CREATE TABLE webhook_deliveries(id uuid PRIMARY KEY,org_id uuid,provider text,event_type text,signature_valid bool,payload jsonb,received_at timestamptz);
 CREATE TABLE code_review_agent_results(id uuid PRIMARY KEY,org_id uuid,session_id uuid,assessment_id uuid,status text,structured_result jsonb,created_at timestamptz);`)
	require.NoError(t, err, "create retained source table shapes")
	up, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000320_code_review_exports.up.sql"))
	require.NoError(t, err, "read real export migration")
	_, err = conn.Exec(ctx, string(up))
	require.NoError(t, err, "apply real migration with tenant FKs")
	f := exportFixture{conn: conn, ctx: ctx, url: url, schema: schema, org: uuid.New(), repo: uuid.New(), pr: uuid.New(), session: uuid.New(), policy: uuid.New()}
	f.exec(t, `INSERT INTO organizations VALUES($1)`, f.org)
	f.exec(t, `INSERT INTO repositories(id,org_id,full_name) VALUES($1,$2,'synthetic/repo')`, f.repo, f.org)
	f.exec(t, `INSERT INTO pull_requests VALUES($1,$2,'synthetic/repo',7,'open',NULL,NULL)`, f.pr, f.org)
	f.exec(t, `INSERT INTO code_review_policies VALUES($1,$2,NULL,3,'{"max_additions":750}','{"requirements":[]}')`, f.policy, f.org)
	return f
}
func (f exportFixture) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	_, err := f.conn.Exec(f.ctx, q, args...)
	require.NoError(t, err, "seed or mutate synthetic export evidence")
}
func (f exportFixture) filters(basis models.ReviewExportTimeBasis) models.CodeReviewExportFilters {
	return models.CodeReviewExportFilters{RepositoryID: f.repo, From: time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), TimeBasis: basis, IncludeUnresolvedPublicationTime: true}
}
func (f exportFixture) assessment(t *testing.T, id uuid.UUID, created, completed, key, head string, provider int64, generation int, scope string) {
	t.Helper()
	f.exec(t, `INSERT INTO code_review_revision_assessments VALUES($1,$2,$3,$4,$5,$6,$7,$8,'base','main',$9,$10,$11,true,'completed','approved','[]',$12,$13)`, id, f.org, f.repo, f.pr, f.session, f.policy, generation, head, key, provider, scope, created, completed)
}
func (f exportFixture) legacy(t *testing.T, id uuid.UUID, created, completed, key string, provider int64, link *uuid.UUID) {
	t.Helper()
	f.exec(t, `INSERT INTO code_review_session_metadata VALUES($1,$2,$3,$4,$5,$6,$7,'old-head','base',$8,$9,'blocked','[{"code":"additions_limit_exceeded","actual":900,"limit":750}]',$10,$11)`, id, f.org, f.repo, f.pr, f.session, f.policy, link, key, provider, created, completed)
}

func TestCodeReviewExportPostgresCohortsAndIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		basis    models.ReviewExportTimeBasis
		expected []string
		counts   models.CodeReviewExportCounts
	}{
		{"created inclusive exclusive", models.ReviewExportCreated, []string{"a1", "legacyDifferent", "legacyConflict", "a2", "legacyOutsideAssessment", "a3"}, models.CodeReviewExportCounts{Records: 6, Assessments: 3, Legacy: 3, RepresentedLegacy: 1, UnresolvedPublicationTime: 6, IncompleteSubstantiveEvidence: 4, MissingBaseRef: 3}},
		{"completion differs from creation", models.ReviewExportCompleted, []string{"legacyEarlierCreation", "a1", "legacyDifferent", "legacyConflict", "a2", "legacyOutsideAssessment"}, models.CodeReviewExportCounts{Records: 6, Assessments: 2, Legacy: 4, RepresentedLegacy: 1, UnresolvedPublicationTime: 6, IncompleteSubstantiveEvidence: 5, MissingBaseRef: 4}},
		{"explicit 96 day union", models.ReviewExportCreatedOrCompleted, []string{"legacyEarlierCreation", "a1", "legacyDifferent", "legacyConflict", "a2", "legacyOutsideAssessment", "a3"}, models.CodeReviewExportCounts{Records: 7, Assessments: 3, Legacy: 4, RepresentedLegacy: 1, UnresolvedPublicationTime: 7, IncompleteSubstantiveEvidence: 5, MissingBaseRef: 4}},
		{"publication retains unresolved before creation upper bound", models.ReviewExportPublished, []string{"aBefore", "legacyEarlierCreation", "a1", "legacyDifferent", "legacyConflict", "a2", "a3"}, models.CodeReviewExportCounts{Records: 7, Assessments: 4, Legacy: 3, RepresentedLegacy: 2, UnresolvedPublicationTime: 7, IncompleteSubstantiveEvidence: 4, MissingBaseRef: 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newExportFixture(t)
			store := NewCodeReviewExportStore(f.conn)
			names := map[uuid.UUID]string{}
			id := func(name string) uuid.UUID { v := uuid.New(); names[v] = name; return v }
			before, a1, a2, a3 := id("aBefore"), id("a1"), id("a2"), id("a3")
			f.assessment(t, before, "2026-07-13", "2026-07-13", "outside", "old-head", 106, 1, "full")
			f.assessment(t, a1, "2026-07-15T00:00:00Z", "2026-07-15T01:00:00Z", "one", "old-head", 101, 2, "full")
			f.assessment(t, a2, "2026-08-01", "2026-08-01", "two", "old-head", 102, 3, "evidence_only")
			f.assessment(t, a3, "2026-10-18T23:59:59.999999Z", "2026-10-19T00:00:00Z", "three", "superseded-head", 103, 4, "full")
			// Superseded and unmerged reviews must remain; no latest-head filter is valid.
			f.exec(t, `UPDATE code_review_revision_assessments SET status='superseded' WHERE id=$1`, a3)
			f.legacy(t, id("represented"), "2026-07-15", "2026-07-15T01:00:00Z", "one", 101, &a1)
			f.legacy(t, id("legacyDifferent"), "2026-07-16", "2026-07-16", "different", 104, &a1)
			f.legacy(t, id("legacyConflict"), "2026-07-17", "2026-07-17", "one", 105, &a1)
			f.legacy(t, id("legacyOutsideAssessment"), "2026-08-02", "2026-08-02", "outside", 106, &before)
			f.legacy(t, id("legacyEarlierCreation"), "2026-07-14", "2026-07-15T00:00:00Z", "earlier", 107, nil)
			f.legacy(t, id("legacyAtEnd"), "2026-10-19T00:00:00Z", "2026-10-19T00:00:00Z", "end", 108, nil)
			foreignOrg, foreignRepo := uuid.New(), uuid.New()
			f.exec(t, `INSERT INTO organizations VALUES($1)`, foreignOrg)
			f.exec(t, `INSERT INTO repositories(id,org_id,full_name) VALUES($1,$2,'foreign/repo')`, foreignRepo, foreignOrg)
			f.exec(t, `INSERT INTO code_review_agent_results VALUES($1,$2,$3,$4,'foreign','{"private":"foreign"}',now())`, uuid.New(), foreignOrg, f.session, a1)
			// Other repositories in this tenant must not contribute sources or aliases.
			otherRepo, otherPR := uuid.New(), uuid.New()
			f.exec(t, `INSERT INTO repositories(id,org_id,full_name,github_id) VALUES($1,$2,'synthetic/other',456)`, otherRepo, f.org)
			f.exec(t, `INSERT INTO pull_requests VALUES($1,$2,'synthetic/other',7,'open',NULL,NULL)`, otherPR, f.org)
			f.exec(t, `INSERT INTO code_review_session_metadata SELECT $1,org_id,$2,$3,session_id,policy_id,assessment_id,head_sha,base_sha,review_output_key,github_review_id,decision,risk_reason_details,created_at,completed_at FROM code_review_session_metadata WHERE org_id=$4 AND review_output_key='one' AND github_review_id=101`, uuid.New(), otherRepo, otherPR, f.org)
			f.exec(t, `INSERT INTO code_review_revision_assessments SELECT $1,org_id,$2,$3,session_id,policy_id,generation,head_sha,base_sha,base_ref,publication_key,github_review_id,review_scope,coverage_complete,status,decision,risk_reason_details,created_at,completed_at FROM code_review_revision_assessments WHERE org_id=$4 AND id=$5`, uuid.New(), otherRepo, otherPR, f.org, a1)
			filters := f.filters(tt.basis)
			acquisition, err := store.Acquire(f.ctx, f.org, filters)
			require.NoError(t, err, "acquire exact bounded cohort")
			require.Equal(t, tt.counts, acquisition.Counts, "counts describe exported sources and distinct represented legacy identities")
			var got []string
			for position := int64(0); position < acquisition.Counts.Records; {
				a, rows, err := store.Page(f.ctx, f.org, models.CodeReviewExportCursor{AcquisitionID: acquisition.ID, Position: position}, 2)
				require.NoError(t, err, "read stable bounded page")
				require.Equal(t, acquisition, a, "every page repeats acquisition metadata exactly")
				for _, row := range rows {
					got = append(got, names[row.ID])
					require.JSONEq(t, "[]", string(row.AgentResults), "foreign tenant evidence must never join by session or assessment alone")
				}
				position += int64(len(rows))
			}
			require.Equal(t, tt.expected, got, "all source identities survive in immutable creation order")
			_, _, err = store.Page(f.ctx, foreignOrg, models.CodeReviewExportCursor{AcquisitionID: acquisition.ID}, 2)
			require.ErrorIs(t, err, ErrReviewExportExpired, "another tenant cannot read acquisition payloads")
			filters.RepositoryID = foreignRepo
			_, err = store.Acquire(f.ctx, f.org, filters)
			require.ErrorIs(t, err, pgx.ErrNoRows, "repository ownership is checked before acquisition")
		})
	}
}

type exportBarrierDB struct {
	TxStarter
	ready, resume chan struct{}
}
type exportBarrierTx struct {
	pgx.Tx
	ready, resume chan struct{}
}

func (b exportBarrierDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := b.TxStarter.Begin(ctx)
	return exportBarrierTx{Tx: tx, ready: b.ready, resume: b.resume}, err
}
func (b exportBarrierTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "eligible_assessments AS (") {
		close(b.ready)
		select {
		case <-b.resume:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return b.Tx.Query(ctx, sql, args...)
}
func TestCodeReviewExportPostgresConcurrentAcquisitionAndPaging(t *testing.T) {
	t.Parallel()
	f := newExportFixture(t)
	first, legacy := uuid.New(), uuid.New()
	f.assessment(t, first, "2026-08-01", "2026-08-01", "one", "old-head", 101, 1, "full")
	f.legacy(t, legacy, "2026-08-02", "2026-08-02", "two", 102, nil)
	writer, err := pgx.Connect(f.ctx, f.url)
	require.NoError(t, err, "connect concurrent writer")
	defer func() { require.NoError(t, writer.Close(f.ctx), "close concurrent writer") }()
	_, err = writer.Exec(f.ctx, `SET search_path TO `+f.schema+`,public`)
	require.NoError(t, err, "scope writer to isolated schema")
	ready, resume := make(chan struct{}), make(chan struct{})
	store := NewCodeReviewExportStore(exportBarrierDB{TxStarter: f.conn, ready: ready, resume: resume})
	type result struct {
		a   models.CodeReviewExportAcquisition
		err error
	}
	resultCh := make(chan result, 1)
	go func() {
		a, err := store.Acquire(f.ctx, f.org, f.filters(models.ReviewExportCreatedOrCompleted))
		resultCh <- result{a, err}
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("acquisition did not reach repeatable-read barrier")
	}
	// Commit changes after MVCC boundary but before source materialization.
	_, err = writer.Exec(f.ctx, `UPDATE code_review_session_metadata SET risk_reason_details='[]',decision='approved' WHERE org_id=$1`, f.org)
	require.NoError(t, err, "commit concurrent legacy update")
	_, err = writer.Exec(f.ctx, `UPDATE code_review_policies SET risk_policy='{"max_additions":9000}'; UPDATE pull_requests SET status='merged',merged_at='2026-10-20'`)
	require.NoError(t, err, "commit concurrent policy and PR updates")
	_, err = writer.Exec(f.ctx, `INSERT INTO code_review_session_metadata SELECT $1,org_id,repository_id,pull_request_id,session_id,policy_id,NULL,head_sha,base_sha,'backdated',103,decision,risk_reason_details,'2026-08-03',completed_at FROM code_review_session_metadata WHERE id=$2`, uuid.New(), legacy)
	require.NoError(t, err, "commit backdated source after boundary")
	close(resume)
	resultValue := <-resultCh
	require.NoError(t, resultValue.err, "materialize a single MVCC state despite concurrent changes")
	plain := NewCodeReviewExportStore(f.conn)
	_, initial, err := plain.Page(f.ctx, f.org, models.CodeReviewExportCursor{AcquisitionID: resultValue.a.ID}, 100)
	require.NoError(t, err, "read frozen rows")
	require.Equal(t, int64(2), resultValue.a.Counts.Records, "late backdated insert is excluded")
	var frozen map[string]any
	require.NoError(t, json.Unmarshal(initial[1].Source, &frozen), "decode legacy source")
	require.Equal(t, "blocked", frozen["decision"], "legacy value precedes concurrent update")
	var policy map[string]any
	require.NoError(t, json.Unmarshal(initial[0].Policy, &policy), "decode retained policy")
	require.Equal(t, float64(750), policy["risk_policy"].(map[string]any)["max_additions"], "policy snapshot precedes concurrent update")
	f.exec(t, `DELETE FROM code_review_session_metadata`)
	f.exec(t, `DELETE FROM code_review_revision_assessments`)
	var paged []models.CodeReviewExportSource
	for position := int64(0); position < 2; position++ {
		_, page, err := plain.Page(f.ctx, f.org, models.CodeReviewExportCursor{AcquisitionID: resultValue.a.ID, Position: position}, 1)
		require.NoError(t, err, "page payloads survive source updates/deletes")
		paged = append(paged, page...)
	}
	require.Equal(t, initial, paged, "pagination returns exactly the frozen evidence, without missing or duplicated rows")
	f.exec(t, `UPDATE code_review_exports SET expires_at=now()-interval '1 second' WHERE org_id=$1`, f.org)
	_, _, err = plain.Page(f.ctx, f.org, models.CodeReviewExportCursor{AcquisitionID: resultValue.a.ID}, 1)
	require.ErrorIs(t, err, ErrReviewExportExpired, "expired acquisition cannot silently continue")
}

func TestCodeReviewExportPostgresBoundsAndMigration(t *testing.T) {
	t.Parallel()
	f := newExportFixture(t)
	store := NewCodeReviewExportStore(f.conn)
	var acquisitions []models.CodeReviewExportAcquisition
	for _, basis := range []models.ReviewExportTimeBasis{models.ReviewExportCreated, models.ReviewExportCompleted, models.ReviewExportCreatedOrCompleted} {
		a, err := store.Acquire(f.ctx, f.org, f.filters(basis))
		require.NoError(t, err, "three bounded acquisitions are supported")
		acquisitions = append(acquisitions, a)
	}
	_, err := store.Acquire(f.ctx, f.org, f.filters(models.ReviewExportPublished))
	require.ErrorIs(t, err, ErrReviewExportLimit, "active snapshots cannot grow without bound")
	_, empty, err := store.Page(f.ctx, f.org, models.CodeReviewExportCursor{AcquisitionID: acquisitions[0].ID}, 100)
	require.NoError(t, err, "empty acquisitions remain pageable")
	require.Equal(t, []models.CodeReviewExportSource{}, empty, "empty acquisition is a complete empty data array")
	_, _, err = store.Page(f.ctx, f.org, models.CodeReviewExportCursor{AcquisitionID: acquisitions[0].ID, Position: 1}, 1)
	require.ErrorIs(t, err, ErrReviewExportPosition, "out-of-range cursor is rejected")
	f.exec(t, `UPDATE code_review_exports SET expires_at=now()-interval '1 second' WHERE org_id=$1`, f.org)
	_, err = store.Acquire(f.ctx, f.org, f.filters(models.ReviewExportCreated))
	require.NoError(t, err, "expired acquisitions are reclaimed before admitting new ones")
	var retained int
	err = f.conn.QueryRow(f.ctx, `SELECT count(*) FROM code_review_exports WHERE org_id=$1`, f.org).Scan(&retained)
	require.NoError(t, err, "count retained acquisitions")
	require.Equal(t, 1, retained, "expired headers and cascading payloads are reclaimed")
	down, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000320_code_review_exports.down.sql"))
	require.NoError(t, err, "read rollback migration")
	_, err = f.conn.Exec(f.ctx, string(down))
	require.NoError(t, err, "rollback drops export cache tables without touching source history")
}

func TestCodeReviewExportPostgresRejectsTruncation(t *testing.T) {
	t.Parallel()
	f := newExportFixture(t)
	f.exec(t, `INSERT INTO code_review_session_metadata(id,org_id,repository_id,pull_request_id,session_id,policy_id,head_sha,review_output_key,created_at)
 SELECT gen_random_uuid(),$1,$2,$3,$4,$5,'head','key-'||n,'2026-08-01'::timestamptz FROM generate_series(1,20001) n`, f.org, f.repo, f.pr, f.session, f.policy)
	_, err := NewCodeReviewExportStore(f.conn).Acquire(f.ctx, f.org, f.filters(models.ReviewExportCreated))
	require.ErrorIs(t, err, ErrReviewExportLimit, "oversized cohorts fail instead of exporting silently truncated evidence")
	var count int
	err = f.conn.QueryRow(f.ctx, `SELECT count(*) FROM code_review_exports WHERE org_id=$1`, f.org).Scan(&count)
	require.NoError(t, err, "check atomic acquisition rollback")
	require.Zero(t, count, "failed acquisition leaves no partial snapshot")
}

func TestCodeReviewExportPostgresRetainedProviderTiming(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, submitted    string
		foreign, otherRepo bool
		expected           []string
		unresolved         int64
	}{
		{"inclusive publication boundary", "2026-07-15T00:00:00Z", false, false, []string{"assessment", "unknown"}, 1},
		{"exclusive publication boundary preserves unresolved legacy", "2026-10-19T00:00:00Z", false, false, []string{"legacy", "unknown"}, 2},
		{"foreign event cannot resolve publication", "2026-07-15T00:00:00Z", true, false, []string{"assessment", "unknown"}, 2},
		{"same tenant other repository event cannot resolve publication", "2026-07-15T00:00:00Z", false, true, []string{"assessment", "unknown"}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newExportFixture(t)
			assessment, legacy, unknown := uuid.New(), uuid.New(), uuid.New()
			f.assessment(t, assessment, "2026-07-14", "2026-07-15", "one", "old-head", 101, 1, "full")
			// An existing provider object's timestamp cannot be assigned to metadata
			// created by a later historical attempt with an unmatched head, even when
			// its provider ID/key match. A filtered assessment cannot hide that unknown.
			f.legacy(t, legacy, "2026-10-18T01:00:00Z", "2026-10-18T02:00:00Z", "one", 101, &assessment)
			f.legacy(t, unknown, "2026-10-18T03:00:00Z", "2026-10-18T04:00:00Z", "unknown", 102, nil)
			f.exec(t, `UPDATE code_review_session_metadata SET head_sha='unmatched-head' WHERE org_id=$1 AND id=$2`, f.org, legacy)
			org := f.org
			if tt.foreign {
				org = uuid.New()
			}
			digest := sha256.Sum256([]byte("one"))
			marker := fmt.Sprintf("<!-- 143-code-review-output:%x -->", digest[:])
			providerRepoID := 123
			if tt.otherRepo {
				providerRepoID = 456
			}
			payload, err := json.Marshal(map[string]any{"action": "submitted", "repository": map[string]any{"id": providerRepoID}, "pull_request": map[string]any{"number": 7}, "review": map[string]any{"id": 101, "commit_id": "old-head", "body": marker, "submitted_at": tt.submitted}})
			require.NoError(t, err, "encode exact synthetic provider event")
			f.exec(t, `INSERT INTO webhook_deliveries VALUES($1,$2,'github','pull_request_review',NULL,$3,now())`, uuid.New(), org, payload)
			store := NewCodeReviewExportStore(f.conn)
			a, err := store.Acquire(f.ctx, f.org, f.filters(models.ReviewExportPublished))
			require.NoError(t, err, "acquire publication-time cohort with unresolved timing")
			_, rows, err := store.Page(f.ctx, f.org, models.CodeReviewExportCursor{AcquisitionID: a.ID}, 100)
			require.NoError(t, err, "page provider-bound evidence")
			names := map[uuid.UUID]string{assessment: "assessment", legacy: "legacy", unknown: "unknown"}
			var actual []string
			for _, row := range rows {
				actual = append(actual, names[row.ID])
			}
			require.Equal(t, tt.expected, actual, "actual publication bounds and unknown inclusion precede representation overlap handling")
			require.Equal(t, tt.unresolved, a.Counts.UnresolvedPublicationTime, "completeness distinguishes bound provider timestamps from unresolved ones")
			if !tt.foreign && !tt.otherRepo && tt.expected[0] == "assessment" {
				require.Equal(t, exportDBTime(tt.submitted), rows[0].PublicationTime, "actual provider submission time is retained rather than completion time")
			}
		})
	}
}
func exportDBTime(value string) *time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return &parsed
}

func TestCodeReviewExportPostgresRetainsFormalApprovalObservation(t *testing.T) {
	t.Parallel()
	f := newExportFixture(t)
	assessment := uuid.New()
	f.assessment(t, assessment, "2026-07-14", "2026-08-01", "one", "old-head", 101, 1, "full")
	f.exec(t, `ALTER TABLE code_review_revision_assessments ADD COLUMN publication_receipt jsonb`)
	f.exec(t, `UPDATE code_review_revision_assessments SET publication_receipt='{"formal_approval_id":777}' WHERE org_id=$1 AND id=$2`, f.org, assessment)
	events := []struct {
		id      int64
		key, at string
	}{{101, "one", "2026-08-01T12:00:00Z"}, {777, "one:formal-approval", "2026-08-01T12:00:01Z"}}
	for _, event := range events {
		digest := sha256.Sum256([]byte(event.key))
		marker := fmt.Sprintf("<!-- 143-code-review-output:%x -->", digest[:])
		payload, err := json.Marshal(map[string]any{"action": "submitted", "repository": map[string]any{"id": 123}, "pull_request": map[string]any{"number": 7}, "review": map[string]any{"id": event.id, "commit_id": "old-head", "body": marker, "submitted_at": event.at}})
		require.NoError(t, err, "encode receipt-linked provider event")
		f.exec(t, `INSERT INTO webhook_deliveries VALUES($1,$2,'github','pull_request_review',true,$3,now())`, uuid.New(), f.org, payload)
	}
	store := NewCodeReviewExportStore(f.conn)
	a, err := store.Acquire(f.ctx, f.org, f.filters(models.ReviewExportPublished))
	require.NoError(t, err, "acquire receipt-linked publication evidence")
	_, rows, err := store.Page(f.ctx, f.org, models.CodeReviewExportCursor{AcquisitionID: a.ID}, 1)
	require.NoError(t, err, "read primary and formal provider submissions")
	var observations []struct {
		ReviewID    int64  `json:"review_id"`
		SubmittedAt string `json:"submitted_at"`
	}
	require.NoError(t, json.Unmarshal(rows[0].ProviderObservations, &observations), "decode retained provider submission identities and actual times")
	require.Equal(t, []struct {
		ReviewID    int64  `json:"review_id"`
		SubmittedAt string `json:"submitted_at"`
	}{{101, "2026-08-01T12:00:00Z"}, {777, "2026-08-01T12:00:01Z"}}, observations, "both provider objects retain their distinct submission timestamps")
	require.Equal(t, exportDBTime("2026-08-01T12:00:00Z"), rows[0].PublicationTime, "formal approval does not replace the primary review's clock")
}

func TestCodeReviewExportPostgresPublicationOverlap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, assessmentKey, legacyKey     string
		assessmentProvider, legacyProvider int64
		linked, represented                bool
	}{
		{"matching publication", "one", "one", 101, 101, true, true},
		{"edited provider object is a distinct publication", "new-output", "old-output", 101, 101, true, false},
		{"unkeyed shared provider object is uncertain", "", "", 101, 101, true, false},
		{"missing legacy key cannot prove overlap", "one", "", 101, 101, false, false},
		{"matching key with conflicting provider objects", "one", "one", 101, 102, true, false},
		{"matching key with unavailable provider", "one", "one", 101, 0, false, true},
		{"explicit unpublished capture link", "one", "", 0, 0, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newExportFixture(t)
			assessment, legacy := uuid.New(), uuid.New()
			f.assessment(t, assessment, "2026-08-02", "2026-08-02", tt.assessmentKey, "new-head", tt.assessmentProvider, 1, "full")
			var link *uuid.UUID
			if tt.linked {
				link = &assessment
			}
			f.legacy(t, legacy, "2026-08-01", "2026-08-01", tt.legacyKey, tt.legacyProvider, link)
			store := NewCodeReviewExportStore(f.conn)
			a, err := store.Acquire(f.ctx, f.org, f.filters(models.ReviewExportCreatedOrCompleted))
			require.NoError(t, err, "acquire publications sharing session and provider object")
			_, rows, err := store.Page(f.ctx, f.org, models.CodeReviewExportCursor{AcquisitionID: a.ID}, 100)
			require.NoError(t, err, "read independent publication identities")
			actual := make([]uuid.UUID, 0, len(rows))
			for _, row := range rows {
				actual = append(actual, row.ID)
			}
			expected := []uuid.UUID{legacy, assessment}
			var represented int64
			if tt.represented {
				expected = []uuid.UUID{assessment}
				represented = 1
			}
			require.Equal(t, expected, actual, "a mutable provider object never collapses different or uncertain publications")
			require.Equal(t, represented, a.Counts.RepresentedLegacy, "overlap counts reflect only established publication identity")
		})
	}
}

func TestCodeReviewExportPostgresRetryReusesAcquisition(t *testing.T) {
	t.Parallel()
	f := newExportFixture(t)
	store := NewCodeReviewExportStore(f.conn)
	first, err := store.Acquire(f.ctx, f.org, f.filters(models.ReviewExportCreated))
	require.NoError(t, err, "acquire first-page snapshot")
	f.legacy(t, uuid.New(), "2026-08-01", "2026-08-01", "later-insert", 101, nil)
	for range 4 {
		retried, err := store.Acquire(f.ctx, f.org, f.filters(models.ReviewExportCreated))
		require.NoError(t, err, "retry first-page acquisition after interrupted transfer")
		require.Equal(t, first, retried, "same filters reuse the original boundary, counts, and slot until expiry")
	}
	var retained int
	err = f.conn.QueryRow(f.ctx, `SELECT count(*) FROM code_review_exports WHERE org_id=$1`, f.org).Scan(&retained)
	require.NoError(t, err, "count retained retry acquisitions")
	require.Equal(t, 1, retained, "failed client transfers do not exhaust all organization slots")
}

func TestCodeReviewExportPostgresPublishedExcludesFutureSourcesBeforeCap(t *testing.T) {
	t.Parallel()
	f := newExportFixture(t)
	legacy := uuid.New()
	f.legacy(t, legacy, "2026-07-14", "2026-07-14", "unknown-before-window", 101, nil)
	f.assessment(t, uuid.New(), "2026-10-19", "2026-10-19", "future-assessment", "future-head", 102, 1, "full")
	f.exec(t, `INSERT INTO code_review_session_metadata(id,org_id,repository_id,pull_request_id,session_id,policy_id,head_sha,review_output_key,created_at)
 SELECT gen_random_uuid(),$1,$2,$3,$4,$5,'head','future-'||n,'2026-10-19'::timestamptz FROM generate_series(1,20001) n`, f.org, f.repo, f.pr, f.session, f.policy)
	store := NewCodeReviewExportStore(f.conn)
	a, err := store.Acquire(f.ctx, f.org, f.filters(models.ReviewExportPublished))
	require.NoError(t, err, "future unresolved sources do not consume the historical candidate budget")
	_, rows, err := store.Page(f.ctx, f.org, models.CodeReviewExportCursor{AcquisitionID: a.ID}, 100)
	require.NoError(t, err, "read unresolved evidence preceding the publication interval")
	actual := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		actual = append(actual, row.ID)
	}
	require.Equal(t, []uuid.UUID{legacy}, actual, "preserve old unresolved evidence and exclude the exclusive creation upper boundary")
	require.Equal(t, int64(1), a.Counts.UnresolvedPublicationTime, "unknown evidence remains explicitly counted")
}

func TestCodeReviewExportPostgresCacheDoesNotBlockParentDeletion(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, table string }{{"repository", "repositories"}, {"organization", "organizations"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newExportFixture(t)
			a, err := NewCodeReviewExportStore(f.conn).Acquire(f.ctx, f.org, f.filters(models.ReviewExportCreated))
			require.NoError(t, err, "cache an otherwise empty historical acquisition")
			f.exec(t, `INSERT INTO code_review_export_rows(org_id,export_id,position,payload) VALUES($1,$2,1,'{}')`, f.org, a.ID)
			if tt.table == "repositories" {
				f.exec(t, `DELETE FROM repositories WHERE org_id=$1 AND id=$2`, f.org, f.repo)
			} else {
				f.exec(t, `DELETE FROM organizations WHERE id=$1`, f.org)
			}
			var headers, payloads int
			err = f.conn.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM code_review_exports WHERE org_id=$1),(SELECT count(*) FROM code_review_export_rows WHERE org_id=$1)`, f.org).Scan(&headers, &payloads)
			require.NoError(t, err, "inspect only disposable cache children after parent deletion")
			require.Equal(t, []int{0, 0}, []int{headers, payloads}, "deleting a parent reclaims cache copies without a foreign-key failure")
		})
	}
}

// The focused schema tests above isolate concurrency and intentionally incomplete
// evidence. This companion also exercises the unmodified complete migration chain.
func TestCodeReviewExportPostgresFullSchema(t *testing.T) {
	t.Parallel()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL to disposable PostgreSQL 15+ for the full export schema")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, raw)
	require.NoError(t, err, "connect explicit disposable test server")
	var version int
	err = conn.QueryRow(ctx, `SELECT current_setting('server_version_num')::integer`).Scan(&version)
	closeErr := conn.Close(ctx)
	require.NoError(t, err, "check full migration prerequisites")
	require.NoError(t, closeErr, "close migration prerequisite connection")
	if version < 150000 {
		t.Skip("the complete unmodified migration chain requires PostgreSQL 15+")
	}
	pool := newFeedbackLifecyclePool(t)
	org, pr, session := seedFeedbackLifecycle(t, pool)
	integration, repo, policy, oldMetadata, currentMetadata := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, integration, org)
	require.NoError(t, err, "create synthetic full-schema integration")
	_, err = pool.Exec(ctx, `INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id)
 VALUES($1,$2,$3,123,'example/app','https://example.test/app.git',1)`, repo, org, integration)
	require.NoError(t, err, "create repository with the real constraints")
	_, err = pool.Exec(ctx, `INSERT INTO code_review_policies(id,org_id,version,approval_mode,review_instructions,automated_approval_policy,description_policy,risk_policy,agent_roster)
 VALUES($1,$2,1,'comment_only','Synthetic review instructions','Synthetic approval policy','{}','{"max_additions":750}','{}')`, policy, org)
	require.NoError(t, err, "retain a real versioned policy with explicit prompts and schema defaults")
	_, err = pool.Exec(ctx, `INSERT INTO code_review_session_metadata(id,org_id,session_id,repository_id,pull_request_id,policy_id,base_sha,head_sha,trigger_source,status,review_output_key,github_review_id,created_at,completed_at)
 VALUES($1,$3,$4,$5,$6,$7,'base','old-head','slash_command','completed','old-output',101,'2026-08-01','2026-08-01'),
 ($2,$3,$4,$5,$6,$7,'base','head','slash_command','completed','current-output',101,'2026-08-02','2026-08-02')`, oldMetadata, currentMetadata, org, session, repo, pr, policy)
	require.NoError(t, err, "seed distinct publications on one mutable provider object using real metadata columns")
	capture := testAssessmentCapture()
	capture.OrgID, capture.RepositoryID, capture.PullRequestID, capture.PolicyID, capture.SessionID = org, repo, pr, policy, session
	capture.PublicationKey = "current-output"
	assessment, reused, err := NewCodeReviewAssessmentStore(pool).Create(ctx, capture)
	require.NoError(t, err, "capture real immutable assessment with composite parent ownership")
	require.False(t, reused, "first capture inserts a distinct assessment")
	_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET created_at='2026-08-02' WHERE org_id=$1 AND id=$2`, org, assessment.ID)
	require.NoError(t, err, "place synthetic capture in the explicit historical cohort")
	filters := models.CodeReviewExportFilters{RepositoryID: repo, From: time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), TimeBasis: models.ReviewExportCreatedOrCompleted, IncludeUnresolvedPublicationTime: true}
	store := NewCodeReviewExportStore(pool)
	a, err := store.Acquire(ctx, org, filters)
	require.NoError(t, err, "acquire against all real source and export columns")
	_, rows, err := store.Page(ctx, org, models.CodeReviewExportCursor{AcquisitionID: a.ID}, 100)
	require.NoError(t, err, "page full-schema frozen payloads")
	actual := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		actual = append(actual, row.ID)
	}
	require.Equal(t, []uuid.UUID{oldMetadata, assessment.ID}, actual, "real constraints preserve distinct legacy publication and suppress only its matching representation")
	require.Equal(t, models.CodeReviewExportCounts{Records: 2, Assessments: 1, Legacy: 1, RepresentedLegacy: 1, UnresolvedPublicationTime: 2, IncompleteSubstantiveEvidence: 2, MissingBaseRef: 1}, a.Counts, "full-schema completeness counts reflect actual captured and missing evidence")
	var source map[string]any
	require.NoError(t, json.Unmarshal(rows[1].Source, &source), "decode real assessment row projection")
	require.Equal(t, currentMetadata.String(), source["metadata_id"], "raw JSON retains exact metadata lineage from real source columns")
	require.Equal(t, "main", source["base_ref"], "assessment-time base ref comes from the real capture")
}
