package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// These fixtures exercise a retained, constraint-valid storage conflict, not
// ordinary full-request admission: production full requests normally create
// separate sessions. A terminal full A and later active full B may nevertheless
// inhabit this same composite-FK domain. The existing evidence linker can bind
// a still-NULL metadata pointer to B without changing A's original head/key.
// A preexisting conflict permits exact job-only retirement; a link committed
// after initial scope capture must refuse the whole unit. Neither may fail B.
// No provider, Worker.poll,
// notification, ACK, publication or activation behavior is proved here.
func terminalOwnerPointerFixture(t *testing.T, pool *pgxpool.Pool, status models.CodeReviewAssessmentStatus, legacy bool) (strandedReviewFixture, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	f := seedStrandedReview(t, pool, "idle")
	seedSavedReviewResults(t, pool, f)
	other := uuid.New()
	var originalRequestAssessment any = f.assessment
	var currentAssessment any
	if status == models.CodeReviewAssessmentCompleted && !legacy {
		currentAssessment = f.assessment
	}
	if legacy {
		originalRequestAssessment = nil
	}
	for _, row := range []struct {
		query string
		args  []any
	}{
		{`UPDATE code_review_revision_assessments SET status=$3,completed_at=statement_timestamp(),publication_state='not_required',
			result_origin='executed',decision='needs_human_review',acceptable=false,coverage_complete=true,
			risk_reason_details='[]',structured_outcome='{}',rendered_body='Synthetic retained full A',
			failure_detail=CASE WHEN $3='failed' THEN 'Synthetic original A failure' ELSE NULL END
			WHERE org_id=$1 AND id=$2`, []any{f.job.OrgID, f.assessment, status}},
		{`INSERT INTO code_review_revision_assessments(id,org_id,repository_id,repository_full_name,pull_request_id,metadata_id,session_id,policy_id,generation,
			base_sha,base_ref,head_sha,input_version,code_digest,contract_digest,intent_digest,visual_digest,request_digest,gate_digest,input_digest,input_manifest,
			review_scope,route_reason,status,publication_key)
			SELECT $3,org_id,repository_id,repository_full_name,pull_request_id,metadata_id,session_id,policy_id,2,
			base_sha,base_ref,head_sha,input_version,code_digest,contract_digest,intent_digest,visual_digest,request_digest,gate_digest,input_digest,input_manifest,
			'full','initial_full','running',$4 FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, []any{f.job.OrgID, f.assessment, other, "synthetic-full-B:" + other.String()}},
		{`UPDATE code_review_pr_state SET generation=2,active_assessment_id=$3,active_session_id=$4,current_assessment_id=$5,
			state='running',wait_reason='active_review',retry_at=statement_timestamp()+interval '1 hour',eligible_at=statement_timestamp()+interval '2 hours'
			WHERE org_id=$1 AND pull_request_id=$2`, []any{f.job.OrgID, f.job.PullRequestID, other, f.job.SessionID, currentAssessment}},
		{`INSERT INTO code_review_requests(org_id,repository_id,pull_request_id,source_kind,source_identity,mode,input_hash,target_generation,session_id,assessment_id,status)
			VALUES($1,$2,$3,'synthetic_pointer','original-A','review_now','synthetic-retained-A',1,$4,$5,'joined'),
			($1,$2,$3,'synthetic_pointer','active-B','review_now','synthetic-active-B',2,$4,$6,'joined')`, []any{f.job.OrgID, f.job.RepositoryID, f.job.PullRequestID, f.job.SessionID, originalRequestAssessment, other}},
		{`INSERT INTO jobs(org_id,queue,job_type,payload,status,dedupe_key,run_at)
			VALUES($1,'agent','reconcile_code_review_schedule',jsonb_build_object('org_id',$1::uuid::text,'pull_request_id',$2::uuid::text),'pending',$3,statement_timestamp()+interval '1 hour')`, []any{f.job.OrgID, f.job.PullRequestID, "code_review_schedule:" + f.job.PullRequestID.String()}},
		{`INSERT INTO code_review_findings(org_id,session_id,dedupe_key,severity,confidence,path,summary,body)
			VALUES($1,$2,'synthetic-owner-pointer-finding','info','high','synthetic.go','Synthetic retained finding','Synthetic retained evidence')`, []any{f.job.OrgID, f.job.SessionID}},
		{`INSERT INTO code_review_prompt_records(org_id,session_id,record_key,role,content)
			VALUES($1,$2,'synthetic-owner-pointer-prompt','reviewer','Synthetic retained prompt')`, []any{f.job.OrgID, f.job.SessionID}},
	} {
		tag, err := pool.Exec(ctx, row.query, row.args...)
		require.NoError(t, err, "create legal terminal-A then active-B rows without colliding with the full-schema one-active-assessment-per-PR constraint")
		require.Positive(t, tag.RowsAffected(), "the owner-conflict fixture must genuinely seed every intended original row and reservation")
	}
	if legacy {
		tag, err := pool.Exec(ctx, `DELETE FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
		require.NoError(t, err, "retain genuine original-A absence without removing active B, metadata, original job or legacy joined request")
		require.Equal(t, int64(1), tag.RowsAffected(), "legacy absence must be real rather than a guessed or fabricated missing assessment")
	}
	var pointer *uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `SELECT assessment_id FROM code_review_session_metadata WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.MetadataID).Scan(&pointer), "observe the genuine NULL storage pointer before the existing evidence linker runs")
	require.Nil(t, pointer, "the real linker must acquire an initially unbound compatible metadata row, not overwrite a previous owner")
	return f, other
}

// This is a separate historical fixture writer, not a replacement retirement
// runner. It owns only the reserved second connection, preserves the existing
// bounded READ COMMITTED policy/order, and invokes the actual evidence linker.
func terminalOwnerPointerLink(ctx context.Context, reserved *pgxpool.Conn, f strandedReviewFixture, other uuid.UUID) error {
	linkCtx, cancel := context.WithTimeout(ctx, db.LifecycleTransactionTimeout)
	defer cancel()
	tx, err := reserved.BeginTx(linkCtx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin synthetic historical evidence link: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := db.ConfigureLifecycleTransactionInTx(linkCtx, tx); err != nil {
		return err
	}
	if err := db.NewCodeReviewRecheckStore(tx).LockFullAssessmentCompletion(linkCtx, tx, f.job.OrgID, f.job.PullRequestID); err != nil {
		return err
	}
	if err := db.LockCodeReviewMetadataForCompletionInTx(linkCtx, f.job.OrgID, tx, f.job.SessionID); err != nil {
		return err
	}
	if err := db.NewCodeReviewAssessmentStore(tx).LinkFullReviewEvidence(linkCtx, f.job.OrgID, other, f.job.SessionID); err != nil {
		return err
	}
	return tx.Commit(linkCtx)
}

// All application statements and Scan results are delegated unchanged. The
// optional late fixture callback runs only after the real unlocked full-owner
// assessment lookup has completed, before retirement takes its PR/metadata/job
// fences. Its own committed rows, not its earlier NULL-pointer graph, form the
// expected rollback baseline. No sleep or synthetic SQL result orders the race.
type terminalOwnerPointerRun struct {
	actual         *fullAssessmentDispatchRunObserver
	late           func(context.Context) error
	lateCalls      int
	lateErr        error
	mutations      []string
	domainLocks    []string
	committedGraph json.RawMessage
}

func (r *terminalOwnerPointerRun) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	return r.actual.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		return work(unitCtx, &terminalOwnerPointerTx{Tx: tx, owner: r})
	})
}

type terminalOwnerPointerTx struct {
	pgx.Tx
	owner *terminalOwnerPointerRun
}

func (tx *terminalOwnerPointerTx) record(query string) {
	normalized := strings.ToLower(strings.Join(strings.Fields(query), " "))
	if strings.Contains(normalized, "pg_advisory_xact_lock") {
		tx.owner.domainLocks = append(tx.owner.domainLocks, "pr")
	}
	if strings.Contains(normalized, "for update") {
		if strings.Contains(normalized, "code_review_session_metadata") {
			tx.owner.domainLocks = append(tx.owner.domainLocks, "metadata")
		}
		if strings.Contains(normalized, "code_review_revision_assessments") {
			tx.owner.domainLocks = append(tx.owner.domainLocks, "assessment")
		}
	}
	fields := strings.Fields(normalized)
	if len(fields) < 2 {
		return
	}
	switch fields[0] {
	case "update":
		tx.owner.mutations = append(tx.owner.mutations, "update:"+fields[1])
	case "insert":
		if len(fields) > 2 && fields[1] == "into" {
			tx.owner.mutations = append(tx.owner.mutations, "insert:"+fields[2])
		}
	case "delete":
		if len(fields) > 2 && fields[1] == "from" {
			tx.owner.mutations = append(tx.owner.mutations, "delete:"+fields[2])
		}
	case "with":
		if strings.Contains(normalized, "update code_review_agent_results ") {
			tx.owner.mutations = append(tx.owner.mutations, "update:code_review_agent_results")
		}
	}
}

func (tx *terminalOwnerPointerTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	tx.record(query)
	return tx.Tx.Exec(ctx, query, args...)
}

func (tx *terminalOwnerPointerTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	tx.record(query)
	return tx.Tx.Query(ctx, query, args...)
}

func (tx *terminalOwnerPointerTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	tx.record(query)
	row := tx.Tx.QueryRow(ctx, query, args...)
	normalized := strings.ToLower(strings.Join(strings.Fields(query), " "))
	if tx.owner.late != nil && tx.owner.lateCalls == 0 && strings.Contains(normalized, "from code_review_revision_assessments a where a.org_id=$1") && !strings.Contains(normalized, "for update") {
		return &terminalOwnerPointerResolutionRow{Row: row, owner: tx.owner, ctx: ctx}
	}
	return row
}

type terminalOwnerPointerResolutionRow struct {
	pgx.Row
	owner *terminalOwnerPointerRun
	ctx   context.Context
}

func (row *terminalOwnerPointerResolutionRow) Scan(dest ...any) error {
	err := row.Row.Scan(dest...)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	row.owner.lateCalls++
	row.owner.lateErr = row.owner.late(row.ctx)
	if row.owner.lateErr != nil {
		return row.owner.lateErr
	}
	return err
}

func terminalOwnerPointerAssertLinked(t *testing.T, ctx context.Context, q terminalContentionRowReader, f strandedReviewFixture, other uuid.UUID, legacy bool) {
	t.Helper()
	var head, key, otherKey, otherStatus string
	var pointer, active, activeSession *uuid.UUID
	var originalCount int
	require.NoError(t, q.QueryRow(ctx, `SELECT m.head_sha,m.review_output_key,m.assessment_id,s.active_assessment_id,s.active_session_id,b.publication_key,b.status,
		(SELECT count(*)::int FROM code_review_revision_assessments a WHERE a.org_id=$1 AND a.id=$5)
		FROM code_review_session_metadata m JOIN code_review_pr_state s ON s.org_id=m.org_id AND s.pull_request_id=m.pull_request_id
		JOIN code_review_revision_assessments b ON b.org_id=m.org_id AND b.id=$4
		WHERE m.org_id=$1 AND m.id=$2 AND m.session_id=$3`, f.job.OrgID, f.job.MetadataID, f.job.SessionID, other, f.assessment).
		Scan(&head, &key, &pointer, &active, &activeSession, &otherKey, &otherStatus, &originalCount),
		"independently read the legal current metadata pointer, original controller head/key, active B and actual scheduling reservation before testing retirement refusal")
	require.Equal(t, f.job.HeadSHA, head, "the conflict must not reduce to an ordinary full-owner head mismatch")
	require.Equal(t, f.job.OutputKey, key, "the conflict must preserve the exact original A output key")
	require.NotEqual(t, key, otherKey, "active B must not impersonate the retained original publication key")
	require.Equal(t, &other, pointer, "the real evidence linker must have committed the different full B pointer")
	require.Equal(t, &other, active, "the scheduler's actual active assessment must be B, not the retiring original A")
	require.Equal(t, &f.job.SessionID, activeSession, "B must share the exact compatible full-schema session domain rather than use a foreign-row violation")
	require.Equal(t, "running", otherStatus, "the one-active-assessment-per-PR index must retain genuine active full B")
	wantOriginal := 1
	if legacy {
		wantOriginal = 0
	}
	require.Equal(t, wantOriginal, originalCount, "the retained-A versus genuine-legacy-absence variants must execute different real storage paths")
}

// Compare the complete graph, allowing only the documented exact job update.
// The two generated timestamps must be one actual transaction timestamp; all
// other rows and every unlisted field of the retired job remain byte-exact.
func terminalOwnerPointerAssertJobOnly(t *testing.T, before, after json.RawMessage, jobID uuid.UUID, failure string) {
	t.Helper()
	var expected, actual map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(before, &expected), "decode the complete pre-retirement graph without converting integer values")
	require.NoError(t, json.Unmarshal(after, &actual), "decode the complete actual committed graph")
	var originalJobs, actualJobs []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(expected["jobs"], &originalJobs), "retain every original job column and unrelated job")
	require.NoError(t, json.Unmarshal(actual["jobs"], &actualJobs), "inspect every actual job column and unrelated job")
	require.Equal(t, len(originalJobs), len(actualJobs), "job-only retirement may not insert or delete any job")
	matched := 0
	for i, original := range originalJobs {
		var id uuid.UUID
		require.NoError(t, json.Unmarshal(original["id"], &id), "read the exact original job identity")
		if id != jobID {
			continue
		}
		matched++
		var actualID uuid.UUID
		require.NoError(t, json.Unmarshal(actualJobs[i]["id"], &actualID), "read actual same-order job identity")
		require.Equal(t, jobID, actualID, "the same exact original job must be updated, not replaced")
		fixed := map[string]any{"status": "dead_letter", "last_error": failure, "locked_by_node_id": nil, "run_owner_id": nil, "owner_kind": "worker", "lock_token": nil, "locked_at": nil, "lease_expires_at": nil}
		for field, value := range fixed {
			encoded, err := json.Marshal(value)
			require.NoError(t, err, "encode the exact documented job-only terminal field")
			require.JSONEq(t, string(encoded), string(actualJobs[i][field]), "job-only terminal field must have its exact committed value: %s", field)
			original[field] = encoded
		}
		var completed, updated *time.Time
		require.NoError(t, json.Unmarshal(actualJobs[i]["completed_at"], &completed), "read actual generated completion timestamp")
		require.NoError(t, json.Unmarshal(actualJobs[i]["updated_at"], &updated), "read actual generated update timestamp")
		require.NotNil(t, completed, "known committed job-only retirement must record completion")
		require.Equal(t, completed, updated, "both generated job fields must use the same actual retirement transaction clock")
		original["completed_at"] = actualJobs[i]["completed_at"]
		original["updated_at"] = actualJobs[i]["updated_at"]
	}
	require.Equal(t, 1, matched, "the exact original job must appear once in the complete graph")
	var err error
	expected["jobs"], err = json.Marshal(originalJobs)
	require.NoError(t, err, "construct the complete expected graph with only documented job-only deltas")
	encoded, err := json.Marshal(expected)
	require.NoError(t, err, "retain every complete non-job business, scheduler and evidence row")
	require.JSONEq(t, string(encoded), string(after), "only the exact leased job may change; metadata, A/B, requests, reservations, results, evidence, control and every other job must remain identical")
}

func TestCodeReviewTerminalFullOwnerPointerConflictPostgres(t *testing.T) {
	t.Parallel()
	variants := []struct {
		name   string
		status models.CodeReviewAssessmentStatus
		legacy bool
	}{
		{name: "retained failed A", status: models.CodeReviewAssessmentFailed},
		{name: "retained completed A", status: models.CodeReviewAssessmentCompleted},
		{name: "genuine original A absence", status: models.CodeReviewAssessmentFailed, legacy: true},
	}
	timings := []struct {
		name string
		late bool
	}{
		{name: "conflict already committed"},
		{name: "link commits after actual unlocked owner resolution", late: true},
	}
	for _, variant := range variants {
		for _, timing := range timings {
			t.Run(variant.name+"/"+timing.name, func(t *testing.T) {
				t.Parallel() // Each leaf owns a separate full-schema database; root reserves aggregate capacity with -parallel=1.
				ctx := context.Background()
				pool := fullRecoveryPostgresPoolWithMaxConns(t, ctx, 2)
				f, other := terminalOwnerPointerFixture(t, pool, variant.status, variant.legacy)
				reserved, err := pool.Acquire(ctx)
				require.NoError(t, err, "reserve exactly the second actual connection before invoking the existing bounded retirement runner")
				defer reserved.Release()
				observed := &terminalOwnerPointerRun{actual: &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}}
				if timing.late {
					observed.late = func(unitCtx context.Context) error {
						if err := terminalOwnerPointerLink(unitCtx, reserved, f, other); err != nil {
							return err
						}
						var err error
						observed.committedGraph, err = terminalContentionSnapshot(unitCtx, reserved, f)
						return err
					}
				} else {
					require.NoError(t, terminalOwnerPointerLink(ctx, reserved, f, other), "create the real conflicting metadata pointer using the unchanged guarded evidence linker before retirement starts")
					terminalOwnerPointerAssertLinked(t, ctx, reserved, f, other, variant.legacy)
				}
				before, err := terminalContentionSnapshot(ctx, reserved, f)
				require.NoError(t, err, "retain all original job, metadata, assessment, results, requests, scheduler and immutable business/evidence rows before retirement")
				jobs, err := db.NewJobStoreWithCodeReviewTerminalTransactions(pool, observed)
				require.NoError(t, err, "compose the real JobStore with the original explicit bounded transaction capability rather than fake outcome authority")
				outcome, retireErr := jobs.DeadLetterCodeReviewWithLease(ctx, f.job.OrgID, f.jobID, f.token, db.CodeReviewTerminalFailure{
					JobError: "Synthetic original controller failure", Reason: "Synthetic original controller failure",
					Code: models.CodeReviewStatusCodeWorkerFailed, Message: "Synthetic original controller failure",
				})
				fullAssessmentDispatchAssertRun(t, observed.actual)
				if timing.late {
					require.Equal(t, 1, observed.lateCalls, "the fixture must actually commit B through the reserved connection after the real original owner lookup and before retirement fences, not guess an ordering")
					require.NoError(t, observed.lateErr, "the historical linker and independent baseline read must succeed rather than manufacture an application refusal")
					require.NotEmpty(t, observed.committedGraph, "the late writer must retain its genuinely committed complete graph as the expected rollback baseline")
					before = observed.committedGraph
					terminalOwnerPointerAssertLinked(t, ctx, reserved, f, other, variant.legacy)
				}
				after, err := terminalContentionSnapshot(ctx, reserved, f)
				require.NoError(t, err, "independently inspect all committed rows after the actual bounded retirement unit finishes")
				if timing.late {
					require.ErrorIs(t, retireErr, db.ErrCodeReviewTerminalScope,
						"a pointer conflict committed after initial capture must abort, not downgrade; actual DML=%v, exposed_owned=%t, graph_changed=%t", observed.mutations, outcome.Owned(), !bytes.Equal(before, after))
					require.Equal(t, db.CodeReviewTerminalOutcome{}, outcome, "a refused later association exposes no provisional or committed target authority")
					require.Equal(t, []string(nil), observed.mutations, "the fresh pointer fence must precede every domain and job mutation")
					require.Equal(t, before, after, "whole rollback must preserve the late link's independently committed complete graph")
				} else {
					require.NoError(t, retireErr, "an already-visible positive owner conflict must retire exactly the poison job")
					require.True(t, outcome.Matches(f.job.OrgID, f.jobID, f.token), "the actual known commit must retain exactly the original lease correlation")
					scope, hasOwner := outcome.Scope()
					require.False(t, hasOwner, "initial pointer poison may authorize no target-derived hook")
					require.Equal(t, db.CodeReviewTerminalScope{OrgID: f.job.OrgID, JobID: f.jobID, LockToken: f.token, JobType: models.JobTypeRunCodeReview}, scope, "all target fields must be erased instead of adopting B")
					require.False(t, outcome.UnresolvedDispatch(), "the unrelated owner may not supply an unresolved-dispatch descriptor")
					metadata, metadataPresent := outcome.Metadata()
					require.False(t, metadataPresent, "initial conflict may not retain a metadata event descriptor")
					require.Equal(t, models.CodeReviewSessionMetadata{}, metadata, "no target metadata may escape job-only retirement")
					assessment, assessmentPresent := outcome.Assessment()
					require.False(t, assessmentPresent, "initial conflict may not retain an assessment event descriptor")
					require.Equal(t, models.CodeReviewAssessment{}, assessment, "neither A nor B may escape as an authorized assessment")
					require.Equal(t, []string(nil), observed.domainLocks, "positive initial inconsistency must retire without any PR, metadata or assessment lock authority")
					require.Equal(t, []string{"update:jobs"}, observed.mutations, "initial conflict must issue only the exact job DML, not fail a different metadata owner")
					terminalOwnerPointerAssertJobOnly(t, before, after, f.jobID, "Synthetic original controller failure")
				}
			})
		}
	}
}
