package db

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrCodeReviewTerminalCapability = errors.New("code review retirement transaction capability is required")
	ErrCodeReviewTerminalInput      = errors.New("invalid code review retirement input")
	ErrCodeReviewTerminalScope      = errors.New("code review retirement owner changed")
	ErrCodeReviewTerminalStorage    = errors.New("code review retirement storage failed")
)

type codeReviewTerminalError struct{ cause error }

func (*codeReviewTerminalError) Error() string        { return ErrCodeReviewTerminalStorage.Error() }
func (e *codeReviewTerminalError) Unwrap() error      { return e.cause }
func (*codeReviewTerminalError) Is(target error) bool { return target == ErrCodeReviewTerminalStorage }

// CodeReviewTerminalFailure is an internal, bounded failure classification,
// never a public receipt or a classification of an uncertain message send.
// The worker retains its existing error classification; the DB supplies the
// scoped unresolved override from actual retained intent before cancellation.
type CodeReviewTerminalFailure struct {
	JobError  string                      `json:"-"`
	Reason    string                      `json:"-"`
	Code      models.CodeReviewStatusCode `json:"-"`
	Message   string                      `json:"-"`
	Retryable bool                        `json:"-"`
}

func (f CodeReviewTerminalFailure) valid() bool {
	return utf8.ValidString(f.JobError) && utf8.ValidString(f.Reason) && utf8.ValidString(f.Message) &&
		strings.TrimSpace(f.Reason) != "" && utf8.RuneCountInString(f.Reason) <= 2000 && f.Code.Validate() == nil
}

// CodeReviewTerminalScope is value-only correlation for one exact job/owner.
// Has-owner=false outcomes contain only job correlation, never target rights.
type CodeReviewTerminalScope struct {
	OrgID, JobID, LockToken                                                    uuid.UUID              `json:"-"`
	JobType                                                                    string                 `json:"-"`
	SessionID, MetadataID, RepositoryID, PullRequestID, PolicyID, AssessmentID uuid.UUID              `json:"-"`
	HeadSHA, OutputKey                                                         string                 `json:"-"`
	ReviewScope                                                                models.CodeReviewScope `json:"-"`
}

// CodeReviewTerminalOutcome is sealed in-process evidence, not send, runtime,
// publication or current authorization proof. InTx returns it PROVISIONALLY:
// its caller owns rollback/known commit and must not infer a commit from its
// accessors. Only the pool wrapper can set committed; Matches and publishing
// refuse provisional outcomes. Every final pool error exposes the zero value.
type CodeReviewTerminalOutcome struct {
	owned, hasOwner, committed, unresolved bool
	completionPending                      bool
	scope                                  CodeReviewTerminalScope
	metadata                               *models.CodeReviewSessionMetadata
	assessment                             *models.CodeReviewAssessment
}

func (o CodeReviewTerminalOutcome) Owned() bool { return o.owned }
func (o CodeReviewTerminalOutcome) Matches(orgID, jobID, lockToken uuid.UUID) bool {
	return o.owned && o.committed && o.scope.OrgID == orgID && o.scope.JobID == jobID && o.scope.LockToken == lockToken
}
func (o CodeReviewTerminalOutcome) Scope() (CodeReviewTerminalScope, bool) {
	if !o.owned {
		return CodeReviewTerminalScope{}, false
	}
	return o.scope, o.hasOwner
}
func (o CodeReviewTerminalOutcome) UnresolvedDispatch() bool {
	return o.owned && o.hasOwner && o.unresolved
}

// CompletionRecoveryPending is a positive, known-committed full-controller
// disposition, never an inference from an unchanged assessment descriptor.
// The original confirmed outcome still requires its normal local completion.
func (o CodeReviewTerminalOutcome) CompletionRecoveryPending() bool {
	return o.owned && o.hasOwner && o.committed && o.completionPending &&
		o.scope.JobType == models.JobTypeRunCodeReview && o.scope.ReviewScope == models.CodeReviewScopeFull && o.scope.AssessmentID != uuid.Nil
}
func (o CodeReviewTerminalOutcome) Metadata() (models.CodeReviewSessionMetadata, bool) {
	if !o.owned || !o.hasOwner || o.metadata == nil {
		return models.CodeReviewSessionMetadata{}, false
	}
	return copyCodeReviewTerminalMetadata(*o.metadata), true
}
func (o CodeReviewTerminalOutcome) Assessment() (models.CodeReviewAssessment, bool) {
	if !o.owned || !o.hasOwner || o.assessment == nil {
		return models.CodeReviewAssessment{}, false
	}
	return copyCodeReviewTerminalAssessment(*o.assessment), true
}

// NewJobStoreWithCodeReviewTerminalTransactions is explicit pool composition;
// it never discovers Run/Begin authority from DBTX or installs another runner.
func NewJobStoreWithCodeReviewTerminalTransactions(database DBTX, transactions CodeReviewMetadataTransactions) (*JobStore, error) {
	if nilLifecycleDependency(database) || nilLifecycleDependency(transactions) {
		return nil, ErrCodeReviewTerminalCapability
	}
	store := NewJobStore(database)
	store.codeReviewTerminalTransactions = transactions
	return store, nil
}

// DeadLetterCodeReviewWithLease owns exactly one supplied existing Run. It
// retires the complete applicable DB graph before acknowledging retirement;
// it neither invokes hooks nor publishes. The ended-parent exception remains
// an explicit worker job-only path, outside this controller retirement method.
func (s *JobStore) DeadLetterCodeReviewWithLease(ctx context.Context, orgID, jobID, lockToken uuid.UUID, failure CodeReviewTerminalFailure) (CodeReviewTerminalOutcome, error) {
	if s == nil || ctx == nil || nilLifecycleDependency(s.codeReviewTerminalTransactions) {
		return CodeReviewTerminalOutcome{}, ErrCodeReviewTerminalCapability
	}
	if orgID == uuid.Nil || jobID == uuid.Nil || lockToken == uuid.Nil || !failure.valid() {
		return CodeReviewTerminalOutcome{}, ErrCodeReviewTerminalInput
	}
	var staged CodeReviewTerminalOutcome
	called := false
	err := s.codeReviewTerminalTransactions.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		staged = CodeReviewTerminalOutcome{}
		called = true
		var err error
		staged, err = DeadLetterCodeReviewInTx(unitCtx, orgID, tx, jobID, lockToken, failure)
		return err
	})
	if err != nil {
		return CodeReviewTerminalOutcome{}, err
	}
	if !called {
		return CodeReviewTerminalOutcome{}, ErrCodeReviewTerminalCapability
	}
	if staged.owned {
		staged.committed = true
	}
	return staged, nil
}

// This is a closed, bounded scalar owner projection, not a whole JSON payload
// decode. Malformed UUIDs never enter SQL casts. The existing retained-command
// limit bounds the projected head/key pair independently of payload size.
const codeReviewTerminalCandidateQuery = `SELECT job_type,
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

type codeReviewTerminalCandidate struct {
	kind   string
	object bool
	raw    [9]string
}

func readCodeReviewTerminalCandidate(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, jobID, token uuid.UUID, locked bool) (codeReviewTerminalCandidate, bool, error) {
	query := codeReviewTerminalCandidateQuery
	if locked {
		query += ` FOR UPDATE`
	}
	var c codeReviewTerminalCandidate
	err := tx.QueryRow(ctx, query, orgID, jobID, token, codeReviewDispatchPreparationMaxBytes).Scan(&c.kind, &c.object,
		&c.raw[0], &c.raw[1], &c.raw[2], &c.raw[3], &c.raw[4], &c.raw[5], &c.raw[6], &c.raw[7], &c.raw[8])
	if errors.Is(err, pgx.ErrNoRows) {
		return codeReviewTerminalCandidate{}, false, nil
	}
	if err != nil {
		return codeReviewTerminalCandidate{}, false, &codeReviewTerminalError{cause: err}
	}
	if c.kind != models.JobTypeRunCodeReview && c.kind != models.JobTypeRunCodeReviewRecheck {
		return codeReviewTerminalCandidate{}, false, ErrCodeReviewTerminalStorage
	}
	return c, true, nil
}

func codeReviewTerminalUUID(raw string) (uuid.UUID, bool) {
	id, err := uuid.Parse(raw)
	return id, err == nil && id != uuid.Nil && id.String() == raw
}

func (c codeReviewTerminalCandidate) owner(orgID, jobID, token uuid.UUID) (CodeReviewTerminalScope, bool) {
	scope := CodeReviewTerminalScope{OrgID: orgID, JobID: jobID, LockToken: token, JobType: c.kind}
	actualOrg, valid := codeReviewTerminalUUID(c.raw[0])
	if !c.object || !valid || actualOrg != orgID {
		return scope, false
	}
	if c.kind == models.JobTypeRunCodeReviewRecheck {
		scope.AssessmentID, valid = codeReviewTerminalUUID(c.raw[8])
		return scope, valid
	}
	if c.kind != models.JobTypeRunCodeReview {
		return scope, false
	}
	ids := []*uuid.UUID{&scope.SessionID, &scope.MetadataID, &scope.RepositoryID, &scope.PullRequestID, &scope.PolicyID}
	for i, dst := range ids {
		id, ok := codeReviewTerminalUUID(c.raw[i+1])
		if !ok {
			return CodeReviewTerminalScope{OrgID: orgID, JobID: jobID, LockToken: token, JobType: c.kind}, false
		}
		*dst = id
	}
	scope.HeadSHA, scope.OutputKey, scope.ReviewScope = c.raw[6], c.raw[7], models.CodeReviewScopeFull
	return scope, scope.HeadSHA != "" && scope.OutputKey != "" && len(scope.HeadSHA)+len(scope.OutputKey) <= codeReviewDispatchPreparationMaxBytes
}

const codeReviewTerminalMetadataMatchesQuery = `SELECT EXISTS(SELECT 1 FROM code_review_session_metadata
 WHERE org_id=$1 AND id=$2 AND session_id=$3 AND repository_id=$4 AND pull_request_id=$5 AND policy_id=$6
 AND ($7='run_code_review_recheck' OR (head_sha=$8 AND review_output_key=$9)))`

// Capture the optional reverse pointer together with the initial exact full
// scope, before resolving A. Do not filter it against the still-unknown A or
// reread it after A's lookup: a later link must reach the locked abort fence.
const codeReviewTerminalInitialMetadataQuery = `SELECT assessment_id FROM code_review_session_metadata
 WHERE org_id=$1 AND id=$2 AND session_id=$3 AND repository_id=$4 AND pull_request_id=$5 AND policy_id=$6
 AND head_sha=$7 AND review_output_key=$8`

func codeReviewTerminalMetadataMatches(ctx context.Context, tx pgx.Tx, scope CodeReviewTerminalScope) (bool, error) {
	var valid bool
	err := tx.QueryRow(ctx, codeReviewTerminalMetadataMatchesQuery, scope.OrgID, scope.MetadataID, scope.SessionID, scope.RepositoryID, scope.PullRequestID, scope.PolicyID, scope.JobType, scope.HeadSHA, scope.OutputKey).Scan(&valid)
	if err != nil {
		return false, &codeReviewTerminalError{cause: err}
	}
	return valid, nil
}

const codeReviewTerminalRecheckScopeQuery = `SELECT a.session_id,a.metadata_id,a.repository_id,a.pull_request_id,a.policy_id,
 CASE WHEN octet_length(a.head_sha)+octet_length(a.publication_key)<=$3 THEN a.head_sha ELSE '' END,
 CASE WHEN octet_length(a.head_sha)+octet_length(a.publication_key)<=$3 THEN a.publication_key ELSE '' END,a.review_scope
 FROM code_review_revision_assessments a JOIN code_review_session_metadata m
 ON m.org_id=a.org_id AND m.id=a.metadata_id AND m.session_id=a.session_id AND m.repository_id=a.repository_id
 AND m.pull_request_id=a.pull_request_id AND m.policy_id=a.policy_id WHERE a.org_id=$1 AND a.id=$2`

func resolveCodeReviewTerminalOwner(ctx context.Context, tx pgx.Tx, scope CodeReviewTerminalScope) (CodeReviewTerminalScope, bool, error) {
	if scope.JobType == models.JobTypeRunCodeReview {
		var initialAssessment *uuid.UUID
		err := tx.QueryRow(ctx, codeReviewTerminalInitialMetadataQuery, scope.OrgID, scope.MetadataID, scope.SessionID, scope.RepositoryID, scope.PullRequestID, scope.PolicyID, scope.HeadSHA, scope.OutputKey).Scan(&initialAssessment)
		if errors.Is(err, pgx.ErrNoRows) {
			return scope, false, nil
		}
		if err != nil {
			return CodeReviewTerminalScope{}, false, &codeReviewTerminalError{cause: err}
		}
		actual := scope
		err = tx.QueryRow(ctx, codeReviewTerminalAssessmentQuery, scope.OrgID, scope.JobType, scope.SessionID, scope.OutputKey, scope.AssessmentID, codeReviewDispatchPreparationMaxBytes).
			Scan(&actual.AssessmentID, &actual.SessionID, &actual.MetadataID, &actual.RepositoryID, &actual.PullRequestID, &actual.PolicyID, &actual.HeadSHA, &actual.OutputKey, &actual.ReviewScope)
		if errors.Is(err, pgx.ErrNoRows) {
			return scope, initialAssessment == nil, nil
		}
		if err != nil {
			return CodeReviewTerminalScope{}, false, &codeReviewTerminalError{cause: err}
		}
		expected := scope
		expected.AssessmentID = actual.AssessmentID
		// A positively inconsistent original owner has only job retirement,
		// never authority over a different metadata/assessment domain.
		return actual, actual.AssessmentID != uuid.Nil && actual == expected && (initialAssessment == nil || *initialAssessment == actual.AssessmentID), nil
	}
	err := tx.QueryRow(ctx, codeReviewTerminalRecheckScopeQuery, scope.OrgID, scope.AssessmentID, codeReviewDispatchPreparationMaxBytes).
		Scan(&scope.SessionID, &scope.MetadataID, &scope.RepositoryID, &scope.PullRequestID, &scope.PolicyID, &scope.HeadSHA, &scope.OutputKey, &scope.ReviewScope)
	if errors.Is(err, pgx.ErrNoRows) {
		return scope, false, nil
	}
	if err != nil {
		return CodeReviewTerminalScope{}, false, &codeReviewTerminalError{cause: err}
	}
	valid := scope.SessionID != uuid.Nil && scope.MetadataID != uuid.Nil && scope.RepositoryID != uuid.Nil && scope.PullRequestID != uuid.Nil && scope.PolicyID != uuid.Nil && scope.HeadSHA != "" && scope.OutputKey != "" && scope.ReviewScope.Validate() == nil
	return scope, valid, nil
}

const codeReviewTerminalAssessmentQuery = `SELECT a.id,a.session_id,a.metadata_id,a.repository_id,a.pull_request_id,a.policy_id,
 CASE WHEN octet_length(a.head_sha)+octet_length(a.publication_key)<=$6 THEN a.head_sha ELSE '' END,
 CASE WHEN octet_length(a.head_sha)+octet_length(a.publication_key)<=$6 THEN a.publication_key ELSE '' END,a.review_scope
 FROM code_review_revision_assessments a WHERE a.org_id=$1
 AND (($2='run_code_review' AND (($5='00000000-0000-0000-0000-000000000000'::uuid AND a.review_scope='full' AND a.session_id=$3 AND a.publication_key=$4)
 OR ($5<>'00000000-0000-0000-0000-000000000000'::uuid AND a.id=$5)))
 OR ($2='run_code_review_recheck' AND a.id=$5))`

const codeReviewTerminalAssessmentLockQuery = codeReviewTerminalAssessmentQuery + ` FOR UPDATE OF a`

// Initially compatible full owners recheck the fresh pointer after their
// metadata/job/assessment fences. A change after initial capture aborts the
// whole unit. Legacy absence permits only NULL; rechecks use another baseline.
const codeReviewTerminalMetadataAssessmentMatchesQuery = `SELECT EXISTS(SELECT 1 FROM code_review_session_metadata
 WHERE org_id=$1 AND id=$2
 AND (assessment_id IS NULL OR ($3<>'00000000-0000-0000-0000-000000000000'::uuid AND assessment_id=$3)))`

// Retirement has already locked the exact metadata and retained assessment.
// Use the same bounded stored-byte observation as preparation and liveness.
func codeReviewTerminalCompletionPending(ctx context.Context, tx pgx.Tx, scope CodeReviewTerminalScope) (bool, error) {
	if scope.JobType != models.JobTypeRunCodeReview || scope.ReviewScope != models.CodeReviewScopeFull || scope.AssessmentID == uuid.Nil {
		return false, nil
	}
	observation, err := observeConfirmedFullAssessmentCompletionPending(ctx, tx, scope.OrgID, scope.MetadataID)
	if err != nil {
		return false, &codeReviewTerminalError{cause: err}
	}
	if !observation.found || observation.assessmentID != scope.AssessmentID {
		return false, ErrCodeReviewTerminalScope
	}
	return observation.pending, nil
}

// Both retirements update only this exact locked confirmed assessment's
// operational diagnostic. The first permits the original retained repair;
// the second reuses the existing operator pause. A prior operator pause stays.
const codeReviewTerminalCompletionRecoveryMarkerQuery = `UPDATE code_review_revision_assessments a
 SET failure_detail=CASE
 WHEN a.failure_detail LIKE 'operator_reconciliation_required:%' THEN a.failure_detail
 WHEN a.failure_detail=$3 THEN $4 ELSE $3 END
 WHERE a.org_id=$1 AND a.id=$2 AND a.status='publishing' AND a.publication_state='confirmed'`

func lockCodeReviewTerminalAssessment(ctx context.Context, tx pgx.Tx, scope CodeReviewTerminalScope) (CodeReviewTerminalScope, error) {
	actual := scope
	err := tx.QueryRow(ctx, codeReviewTerminalAssessmentLockQuery, scope.OrgID, scope.JobType, scope.SessionID, scope.OutputKey, scope.AssessmentID, codeReviewDispatchPreparationMaxBytes).
		Scan(&actual.AssessmentID, &actual.SessionID, &actual.MetadataID, &actual.RepositoryID, &actual.PullRequestID, &actual.PolicyID, &actual.HeadSHA, &actual.OutputKey, &actual.ReviewScope)
	if errors.Is(err, pgx.ErrNoRows) && scope.JobType == models.JobTypeRunCodeReview && scope.AssessmentID == uuid.Nil {
		return scope, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return CodeReviewTerminalScope{}, ErrCodeReviewTerminalScope
	}
	if err != nil {
		return CodeReviewTerminalScope{}, &codeReviewTerminalError{cause: err}
	}
	if actual.AssessmentID == uuid.Nil || actual != scope {
		return CodeReviewTerminalScope{}, ErrCodeReviewTerminalScope
	}
	return actual, nil
}

const codeReviewTerminalJobUpdateQuery = `UPDATE jobs SET status='dead_letter',last_error=$4,completed_at=now(),
 locked_by_node_id=NULL,run_owner_id=NULL,owner_kind='worker',lock_token=NULL,
 locked_at=NULL,lease_expires_at=NULL,updated_at=now()
 WHERE org_id=$1 AND id=$2 AND status='running' AND lock_token=$3 AND job_type=$5`

const codeReviewTerminalAssessmentUpdateQuery = `UPDATE code_review_revision_assessments SET status='failed',failure_detail=$3,completed_at=now()
 WHERE org_id=$1 AND id=$2 AND publication_receipt IS NULL AND github_review_id IS NULL
 AND ((status IN ('reserved','running') AND publication_state='not_started') OR (status='publishing' AND publication_state='reserved')) RETURNING *`

// Session prelocking establishes order, not authority over sibling metadata.
// These immutable wake bindings are already checked by Prepare. Missing or
// ambiguous bindings have no fallback; only this exact original controller's
// intent can influence its unresolved marker or pending-only retirement.
const codeReviewTerminalDispatchOwner = `r.org_id=$1 AND r.session_id=$2 AND r.role='reviewer' AND r.dispatch_version=1
 AND r.dispatch_wake->>'job_type'='run_code_review' AND r.dispatch_wake->>'queue'='agent'
 AND r.dispatch_wake->>'dedupe_key'='code_review:'||$4
 AND r.dispatch_wake->'payload'->>'org_id'=$1::text AND r.dispatch_wake->'payload'->>'session_id'=$2::text
 AND r.dispatch_wake->'payload'->>'metadata_id'=$3 AND r.dispatch_wake->'payload'->>'review_output_key'=$4`

const codeReviewTerminalUnresolvedQuery = `SELECT EXISTS(SELECT 1 FROM code_review_agent_results r WHERE ` + codeReviewTerminalDispatchOwner + ` AND r.dispatch_due_at IS NOT NULL)`
const codeReviewTerminalPendingCancelQuery = `WITH pending AS MATERIALIZED (
 SELECT r.id FROM code_review_agent_results r WHERE ` + codeReviewTerminalDispatchOwner + `
 AND r.dispatch_state='pending' ORDER BY r.id FOR UPDATE OF r
)
UPDATE code_review_agent_results r SET dispatch_state='cancelled',dispatch_resolution='owner_cancelled',dispatch_due_at=NULL,
 dispatch_writer_seq=r.dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp()
 FROM pending p WHERE r.org_id=$1 AND r.id=p.id AND r.role='reviewer' AND r.dispatch_version=1 AND r.dispatch_state='pending'`

// DeadLetterCodeReviewInTx composes only DB work in an explicitly supplied
// existing bounded READ COMMITTED unit. Its result is provisional; any error
// requires whole rollback. There are no effects, settings, retries or nested
// settlement. Malformed/missing original scope permits only the exact job's
// lease-owned retirement, never another target or a storage-error fallback.
func DeadLetterCodeReviewInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, jobID, lockToken uuid.UUID, failure CodeReviewTerminalFailure) (CodeReviewTerminalOutcome, error) {
	if ctx == nil || nilLifecycleDependency(tx) {
		return CodeReviewTerminalOutcome{}, ErrCodeReviewTerminalCapability
	}
	if orgID == uuid.Nil || jobID == uuid.Nil || lockToken == uuid.Nil || !failure.valid() {
		return CodeReviewTerminalOutcome{}, ErrCodeReviewTerminalInput
	}
	if err := requireCodeReviewMetadataTerminalTx(ctx, tx); err != nil {
		return CodeReviewTerminalOutcome{}, err
	}
	candidate, owned, err := readCodeReviewTerminalCandidate(ctx, orgID, tx, jobID, lockToken, false)
	if err != nil || !owned {
		return CodeReviewTerminalOutcome{}, err
	}
	scope, hasOwner := candidate.owner(orgID, jobID, lockToken)
	if hasOwner {
		scope, hasOwner, err = resolveCodeReviewTerminalOwner(ctx, tx, scope)
		if err != nil {
			return CodeReviewTerminalOutcome{}, err
		}
	}
	if hasOwner {
		if err := NewCodeReviewRecheckStore(tx).LockFullAssessmentCompletion(ctx, tx, orgID, scope.PullRequestID); err != nil {
			return CodeReviewTerminalOutcome{}, &codeReviewTerminalError{cause: err}
		}
		if err := LockCodeReviewMetadataForCompletionInTx(ctx, orgID, tx, scope.SessionID); err != nil {
			return CodeReviewTerminalOutcome{}, err
		}
		valid, err := codeReviewTerminalMetadataMatches(ctx, tx, scope)
		if err != nil {
			return CodeReviewTerminalOutcome{}, err
		}
		if !valid {
			return CodeReviewTerminalOutcome{}, ErrCodeReviewTerminalScope
		}
	}
	current, stillOwned, err := readCodeReviewTerminalCandidate(ctx, orgID, tx, jobID, lockToken, true)
	if err != nil || !stillOwned {
		return CodeReviewTerminalOutcome{}, err
	}
	if current != candidate {
		return CodeReviewTerminalOutcome{}, ErrCodeReviewTerminalScope
	}
	if hasOwner {
		scope, err = lockCodeReviewTerminalAssessment(ctx, tx, scope)
		if err != nil {
			return CodeReviewTerminalOutcome{}, err
		}
		if scope.JobType == models.JobTypeRunCodeReview {
			var valid bool
			if err := tx.QueryRow(ctx, codeReviewTerminalMetadataAssessmentMatchesQuery, orgID, scope.MetadataID, scope.AssessmentID).Scan(&valid); err != nil {
				return CodeReviewTerminalOutcome{}, &codeReviewTerminalError{cause: err}
			}
			if !valid {
				return CodeReviewTerminalOutcome{}, ErrCodeReviewTerminalScope
			}
		}
	}
	// A missing owner retains only actual job correlation, not projected target
	// values. This suppresses every target-derived postcommit hook.
	if !hasOwner {
		scope = CodeReviewTerminalScope{OrgID: orgID, JobID: jobID, LockToken: lockToken, JobType: candidate.kind}
	}
	result := CodeReviewTerminalOutcome{scope: scope, hasOwner: hasOwner}
	if hasOwner {
		result.completionPending, err = codeReviewTerminalCompletionPending(ctx, tx, scope)
		if err != nil {
			return CodeReviewTerminalOutcome{}, err
		}
		if err := tx.QueryRow(ctx, codeReviewTerminalUnresolvedQuery, orgID, scope.SessionID, scope.MetadataID.String(), scope.OutputKey).Scan(&result.unresolved); err != nil {
			return CodeReviewTerminalOutcome{}, &codeReviewTerminalError{cause: err}
		}
	}
	tag, err := tx.Exec(ctx, codeReviewTerminalJobUpdateQuery, orgID, jobID, lockToken, failure.JobError, candidate.kind)
	if err != nil {
		return CodeReviewTerminalOutcome{}, &codeReviewTerminalError{cause: err}
	}
	if tag.RowsAffected() != 1 {
		return CodeReviewTerminalOutcome{}, ErrCodeReviewTerminalScope
	}
	result.owned = true
	if !hasOwner {
		if err := ctx.Err(); err != nil {
			return CodeReviewTerminalOutcome{}, &codeReviewTerminalError{cause: err}
		}
		return result, nil
	}
	if result.completionPending {
		tag, err := tx.Exec(ctx, codeReviewTerminalCompletionRecoveryMarkerQuery, orgID, scope.AssessmentID,
			CodeReviewConfirmedCompletionRecoveryPending, CodeReviewConfirmedCompletionOperatorRequired)
		if err != nil {
			return CodeReviewTerminalOutcome{}, &codeReviewTerminalError{cause: err}
		}
		if tag.RowsAffected() != 1 {
			return CodeReviewTerminalOutcome{}, ErrCodeReviewTerminalScope
		}
	}
	reason := strings.TrimSpace(failure.Reason)
	if result.unresolved {
		reason = "dispatch_unresolved:" + reason
	}
	if scope.AssessmentID != uuid.Nil && !result.completionPending {
		rows, err := tx.Query(ctx, codeReviewTerminalAssessmentUpdateQuery, orgID, scope.AssessmentID, reason)
		if err != nil {
			return CodeReviewTerminalOutcome{}, &codeReviewTerminalError{cause: err}
		}
		changed, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.CodeReviewAssessment])
		if err != nil {
			return CodeReviewTerminalOutcome{}, &codeReviewTerminalError{cause: err}
		}
		if len(changed) > 1 {
			return CodeReviewTerminalOutcome{}, ErrCodeReviewTerminalStorage
		}
		if len(changed) == 1 {
			a := changed[0]
			if a.OrgID != orgID || a.ID != scope.AssessmentID || a.SessionID != scope.SessionID || a.MetadataID != scope.MetadataID || a.RepositoryID != scope.RepositoryID || a.PullRequestID != scope.PullRequestID || a.PolicyID != scope.PolicyID || a.HeadSHA != scope.HeadSHA || a.PublicationKey != scope.OutputKey || a.ReviewScope != scope.ReviewScope || a.Status != models.CodeReviewAssessmentFailed {
				return CodeReviewTerminalOutcome{}, ErrCodeReviewTerminalStorage
			}
			result.assessment = &a
		}
	}
	if scope.JobType == models.JobTypeRunCodeReview {
		params := FailCodeReviewParams{SessionID: scope.SessionID, Reason: strings.TrimSpace(failure.Reason), Code: failure.Code, Message: failure.Message, Retryable: failure.Retryable}
		if result.unresolved {
			params.Code = models.CodeReviewStatusCodeDispatchUnresolved
			params.Message = "The review stopped with a reviewer dispatch still unresolved. Its original intent is retained for reconciliation; do not resend that command."
			params.Retryable = false
		}
		var changed []models.CodeReviewSessionMetadata
		if !result.completionPending {
			changed, err = failCodeReviewMetadataLockedInTx(ctx, orgID, tx, params, []uuid.UUID{scope.MetadataID})
			if err != nil {
				return CodeReviewTerminalOutcome{}, &codeReviewTerminalError{cause: err}
			}
		}
		if len(changed) > 1 {
			return CodeReviewTerminalOutcome{}, ErrCodeReviewTerminalStorage
		}
		if len(changed) == 1 {
			m := changed[0]
			if m.OrgID != orgID || m.ID != scope.MetadataID || m.SessionID != scope.SessionID || m.RepositoryID != scope.RepositoryID || m.PullRequestID != scope.PullRequestID || m.PolicyID != scope.PolicyID || m.HeadSHA != scope.HeadSHA || m.ReviewOutputKey != scope.OutputKey || m.Status != models.CodeReviewSessionStatusFailed {
				return CodeReviewTerminalOutcome{}, ErrCodeReviewTerminalStorage
			}
		}
		if len(changed) == 1 || result.completionPending {
			// This is a fresh statement after complete metadata RETURNING, not a
			// dependent-CTE snapshot or session-wide sibling-owner cancellation.
			// A confirmed pending completion preserves active metadata, but must
			// still cancel its original pending intents under the same locks.
			if _, err := tx.Exec(ctx, codeReviewTerminalPendingCancelQuery, orgID, scope.SessionID, scope.MetadataID.String(), scope.OutputKey); err != nil {
				return CodeReviewTerminalOutcome{}, &codeReviewTerminalError{cause: err}
			}
		}
		if len(changed) == 1 {
			result.metadata = &changed[0]
		}
	}
	if err := ctx.Err(); err != nil {
		return CodeReviewTerminalOutcome{}, &codeReviewTerminalError{cause: err}
	}
	return result, nil
}

func copyCodeReviewTerminalPointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
func copyCodeReviewTerminalMetadata(m models.CodeReviewSessionMetadata) models.CodeReviewSessionMetadata {
	m.TriggeringDisputeID = copyCodeReviewTerminalPointer(m.TriggeringDisputeID)
	m.Phase = copyCodeReviewTerminalPointer(m.Phase)
	m.StatusCode = copyCodeReviewTerminalPointer(m.StatusCode)
	m.StatusMessage = copyCodeReviewTerminalPointer(m.StatusMessage)
	m.RetryAt = copyCodeReviewTerminalPointer(m.RetryAt)
	m.LastErrorAt = copyCodeReviewTerminalPointer(m.LastErrorAt)
	m.Decision = copyCodeReviewTerminalPointer(m.Decision)
	m.Acceptable = copyCodeReviewTerminalPointer(m.Acceptable)
	m.SupersededBySessionID = copyCodeReviewTerminalPointer(m.SupersededBySessionID)
	m.PromptRecordKey = copyCodeReviewTerminalPointer(m.PromptRecordKey)
	m.GitHubReviewID = copyCodeReviewTerminalPointer(m.GitHubReviewID)
	m.GitHubReviewURL = copyCodeReviewTerminalPointer(m.GitHubReviewURL)
	m.FinalReviewBody = copyCodeReviewTerminalPointer(m.FinalReviewBody)
	m.FailureReason = copyCodeReviewTerminalPointer(m.FailureReason)
	m.CompletedAt = copyCodeReviewTerminalPointer(m.CompletedAt)
	return m
}
func copyCodeReviewTerminalAssessment(a models.CodeReviewAssessment) models.CodeReviewAssessment {
	a.ConversationID = copyCodeReviewTerminalPointer(a.ConversationID)
	a.SourceAssessmentID = copyCodeReviewTerminalPointer(a.SourceAssessmentID)
	a.PreviousAssessmentID = copyCodeReviewTerminalPointer(a.PreviousAssessmentID)
	a.PreviousPublishedAssessmentID = copyCodeReviewTerminalPointer(a.PreviousPublishedAssessmentID)
	a.SupersededByAssessmentID = copyCodeReviewTerminalPointer(a.SupersededByAssessmentID)
	a.ResultOrigin = copyCodeReviewTerminalPointer(a.ResultOrigin)
	a.Decision = copyCodeReviewTerminalPointer(a.Decision)
	a.Acceptable = copyCodeReviewTerminalPointer(a.Acceptable)
	a.RenderedBody = copyCodeReviewTerminalPointer(a.RenderedBody)
	a.FailureDetail = copyCodeReviewTerminalPointer(a.FailureDetail)
	a.GitHubReviewID = copyCodeReviewTerminalPointer(a.GitHubReviewID)
	a.GitHubReviewURL = copyCodeReviewTerminalPointer(a.GitHubReviewURL)
	a.SubmittedCommitSHA = copyCodeReviewTerminalPointer(a.SubmittedCommitSHA)
	a.CompletedAt = copyCodeReviewTerminalPointer(a.CompletedAt)
	a.SupersededAt = copyCodeReviewTerminalPointer(a.SupersededAt)
	a.InputManifest = bytes.Clone(a.InputManifest)
	a.RiskReasonDetails = bytes.Clone(a.RiskReasonDetails)
	a.StructuredOutcome = bytes.Clone(a.StructuredOutcome)
	a.PublicationReceipt = bytes.Clone(a.PublicationReceipt)
	return a
}
