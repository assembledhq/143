package worker

import (
	"context"
	"errors"
	"regexp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

// codeReviewHandlerCPUTransactions supplies an explicitly composed transaction
// to the actual terminal DB helpers. Its ordered Begin/Commit are CPU mocks,
// not evidence of real PostgreSQL isolation, transaction budgets or pool use.
type codeReviewHandlerCPUTransactions struct {
	mock    pgxmock.PgxPoolIface
	calls   int
	commits int
}

func (r *codeReviewHandlerCPUTransactions) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	r.calls++
	tx, err := r.mock.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	if err := work(ctx, tx); err != nil {
		return errors.Join(err, tx.Rollback(ctx))
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	r.commits++
	return nil
}

func expectCodeReviewHandlerCPUBegin(mock pgxmock.PgxPoolIface) {
	mock.ExpectBeginTx(pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
}

func expectCodeReviewHandlerCPUMetadataLock(mock pgxmock.PgxPoolIface, orgID, sessionID, metadataID uuid.UUID, activeOnly bool) {
	mock.ExpectQuery(`^SHOW transaction_isolation$`).
		WillReturnRows(pgxmock.NewRows([]string{"transaction_isolation"}).AddRow(string(pgx.ReadCommitted)))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM code_review_session_metadata
 WHERE org_id=@org_id AND session_id=@session_id
 AND (NOT @active_only OR status IN ('queued','running')) ORDER BY id FOR UPDATE`)).
		WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID, "active_only": activeOnly}).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(metadataID)).RowsWillBeClosed()
}

// This separate expectation follows complete metadata RETURNING consumption.
// The zero-row result models no pending reviewer intents in these fixtures.
func expectCodeReviewHandlerCPUPendingCancellation(mock pgxmock.PgxPoolIface, orgID, sessionID, metadataID uuid.UUID) {
	mock.ExpectExec(regexp.QuoteMeta(`WITH pending AS MATERIALIZED (
 SELECT r.id FROM code_review_agent_results r
 WHERE r.org_id=$1 AND r.role='reviewer' AND r.dispatch_version=1 AND r.dispatch_state='pending'
 AND EXISTS (
 SELECT 1 FROM unnest($2::uuid[],$3::uuid[]) AS changed(metadata_id,session_id)
 JOIN code_review_session_metadata m ON m.org_id=$1 AND m.id=changed.metadata_id AND m.session_id=changed.session_id
 WHERE r.session_id=changed.session_id
 ) ORDER BY r.id FOR UPDATE OF r
)
UPDATE code_review_agent_results r
 SET dispatch_state='cancelled',dispatch_resolution='owner_cancelled',dispatch_due_at=NULL,
 dispatch_writer_seq=r.dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp()
 FROM pending p WHERE r.org_id=$1 AND r.id=p.id AND r.role='reviewer'
 AND r.dispatch_version=1 AND r.dispatch_state='pending'`)).
		WithArgs(orgID, []uuid.UUID{metadataID}, []uuid.UUID{sessionID}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
}

// The legacy fixture has an exact full owner but no assessment and no pending
// or unresolved dispatch. All outcome authority comes from the real JobStore
// wrapper and Worker.deadLetterJob after the mock transaction's known commit.
func expectCodeReviewHandlerCPULegacyRetirementLocks(mock pgxmock.PgxPoolIface, job runCodeReviewPayload, jobID, token uuid.UUID) {
	expectCodeReviewHandlerCPUBegin(mock)
	mock.ExpectQuery(`^SHOW transaction_isolation$`).
		WillReturnRows(pgxmock.NewRows([]string{"transaction_isolation"}).AddRow(string(pgx.ReadCommitted)))
	expectCodeReviewHandlerCPUCandidate(mock, job, jobID, token, false)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT assessment_id FROM code_review_session_metadata
 WHERE org_id=$1 AND id=$2 AND session_id=$3 AND repository_id=$4 AND pull_request_id=$5 AND policy_id=$6
 AND head_sha=$7 AND review_output_key=$8`)).
		WithArgs(job.OrgID, job.MetadataID, job.SessionID, job.RepositoryID, job.PullRequestID, job.PolicyID, job.HeadSHA, job.OutputKey).
		WillReturnRows(pgxmock.NewRows([]string{"assessment_id"}).AddRow(nil))
	expectCodeReviewHandlerCPULegacyAssessment(mock, job, false)
	mock.ExpectExec(regexp.QuoteMeta(`SELECT pg_advisory_xact_lock(hashtextextended($1,0))`)).
		WithArgs("code_review_pr:" + job.OrgID.String() + ":" + job.PullRequestID.String()).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	expectCodeReviewHandlerCPUMetadataLock(mock, job.OrgID, job.SessionID, job.MetadataID, false)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT EXISTS(SELECT 1 FROM code_review_session_metadata
 WHERE org_id=$1 AND id=$2 AND session_id=$3 AND repository_id=$4 AND pull_request_id=$5 AND policy_id=$6
 AND ($7='run_code_review_recheck' OR (head_sha=$8 AND review_output_key=$9)))`)).
		WithArgs(job.OrgID, job.MetadataID, job.SessionID, job.RepositoryID, job.PullRequestID, job.PolicyID, "run_code_review", job.HeadSHA, job.OutputKey).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	expectCodeReviewHandlerCPUCandidate(mock, job, jobID, token, true)
	expectCodeReviewHandlerCPULegacyAssessment(mock, job, true)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT EXISTS(SELECT 1 FROM code_review_session_metadata
 WHERE org_id=$1 AND id=$2
 AND (assessment_id IS NULL OR ($3<>'00000000-0000-0000-0000-000000000000'::uuid AND assessment_id=$3)))`)).
		WithArgs(job.OrgID, job.MetadataID, uuid.Nil).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT EXISTS(SELECT 1 FROM code_review_agent_results r WHERE `+codeReviewHandlerCPUTerminalDispatchOwner+` AND r.dispatch_due_at IS NOT NULL)`)).
		WithArgs(job.OrgID, job.SessionID, job.MetadataID.String(), job.OutputKey).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
}

func expectCodeReviewHandlerCPUCandidate(mock pgxmock.PgxPoolIface, job runCodeReviewPayload, jobID, token uuid.UUID, locked bool) {
	query := `SELECT job_type,
 COALESCE(jsonb_typeof(payload)='object',false),
 COALESCE(CASE WHEN jsonb_typeof(payload->'org_id')='string' AND octet_length(payload->>'org_id')=36 THEN payload->>'org_id' END,''),
 COALESCE(CASE WHEN jsonb_typeof(payload->'session_id')='string' AND octet_length(payload->>'session_id')=36 THEN payload->>'session_id' END,''),
 COALESCE(CASE WHEN jsonb_typeof(payload->'metadata_id')='string' AND octet_length(payload->>'metadata_id')=36 THEN payload->>'metadata_id' END,''),
 COALESCE(CASE WHEN jsonb_typeof(payload->'repository_id')='string' AND octet_length(payload->>'repository_id')=36 THEN payload->>'repository_id' END,''),
 COALESCE(CASE WHEN jsonb_typeof(payload->'pull_request_id')='string' AND octet_length(payload->>'pull_request_id')=36 THEN payload->>'pull_request_id' END,''),
 COALESCE(CASE WHEN jsonb_typeof(payload->'policy_id')='string' AND octet_length(payload->>'policy_id')=36 THEN payload->>'policy_id' END,''),
 COALESCE(CASE WHEN jsonb_typeof(payload->'head_sha')='string' AND octet_length(COALESCE(payload->>'head_sha',''))+octet_length(COALESCE(payload->>'review_output_key',''))<=$4 THEN payload->>'head_sha' END,''),
 COALESCE(CASE WHEN jsonb_typeof(payload->'review_output_key')='string' AND octet_length(COALESCE(payload->>'head_sha',''))+octet_length(COALESCE(payload->>'review_output_key',''))<=$4 THEN payload->>'review_output_key' END,''),
 COALESCE(CASE WHEN jsonb_typeof(payload->'assessment_id')='string' AND octet_length(payload->>'assessment_id')=36 THEN payload->>'assessment_id' END,'')
 FROM jobs WHERE org_id=$1 AND id=$2 AND status='running' AND lock_token=$3
 AND job_type IN ('run_code_review','run_code_review_recheck')`
	if locked {
		query += ` FOR UPDATE`
	}
	mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs(job.OrgID, jobID, token, 8<<20).
		WillReturnRows(pgxmock.NewRows([]string{"job_type", "object", "org", "session", "metadata", "repository", "pr", "policy", "head", "output", "assessment"}).
			AddRow("run_code_review", true, job.OrgID.String(), job.SessionID.String(), job.MetadataID.String(), job.RepositoryID.String(), job.PullRequestID.String(), job.PolicyID.String(), job.HeadSHA, job.OutputKey, ""))
}

func expectCodeReviewHandlerCPULegacyAssessment(mock pgxmock.PgxPoolIface, job runCodeReviewPayload, locked bool) {
	query := `SELECT a.id,a.session_id,a.metadata_id,a.repository_id,a.pull_request_id,a.policy_id,
 CASE WHEN octet_length(a.head_sha)+octet_length(a.publication_key)<=$6 THEN a.head_sha ELSE '' END,
 CASE WHEN octet_length(a.head_sha)+octet_length(a.publication_key)<=$6 THEN a.publication_key ELSE '' END,a.review_scope
 FROM code_review_revision_assessments a WHERE a.org_id=$1
 AND (($2='run_code_review' AND (($5='00000000-0000-0000-0000-000000000000'::uuid AND a.review_scope='full' AND a.session_id=$3 AND a.publication_key=$4)
 OR ($5<>'00000000-0000-0000-0000-000000000000'::uuid AND a.id=$5)))
 OR ($2='run_code_review_recheck' AND a.id=$5))`
	if locked {
		query += ` FOR UPDATE OF a`
	}
	mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs(job.OrgID, "run_code_review", job.SessionID, job.OutputKey, uuid.Nil, 8<<20).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "metadata_id", "repository_id", "pull_request_id", "policy_id", "head_sha", "publication_key", "review_scope"}))
}

const codeReviewHandlerCPUTerminalDispatchOwner = `r.org_id=$1 AND r.session_id=$2 AND r.role='reviewer' AND r.dispatch_version=1
 AND r.dispatch_wake->>'job_type'='run_code_review' AND r.dispatch_wake->>'queue'='agent'
 AND r.dispatch_wake->>'dedupe_key'='code_review:'||$4
 AND r.dispatch_wake->'payload'->>'org_id'=$1::text AND r.dispatch_wake->'payload'->>'session_id'=$2::text
 AND r.dispatch_wake->'payload'->>'metadata_id'=$3 AND r.dispatch_wake->'payload'->>'review_output_key'=$4`

func expectCodeReviewHandlerCPURetiredPendingCancellation(mock pgxmock.PgxPoolIface, job runCodeReviewPayload) {
	mock.ExpectExec(regexp.QuoteMeta(`WITH pending AS MATERIALIZED (
 SELECT r.id FROM code_review_agent_results r WHERE `+codeReviewHandlerCPUTerminalDispatchOwner+`
 AND r.dispatch_state='pending' ORDER BY r.id FOR UPDATE OF r
)
UPDATE code_review_agent_results r SET dispatch_state='cancelled',dispatch_resolution='owner_cancelled',dispatch_due_at=NULL,
 dispatch_writer_seq=r.dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp()
 FROM pending p WHERE r.org_id=$1 AND r.id=p.id AND r.role='reviewer' AND r.dispatch_version=1 AND r.dispatch_state='pending'`)).
		WithArgs(job.OrgID, job.SessionID, job.MetadataID.String(), job.OutputKey).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
}
