package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ReviewerDispatchControllerScope comes from the original controller, not the
// retained command JSON. It grants no admission or transaction authority.
type ReviewerDispatchControllerScope struct {
	MetadataID, SessionID uuid.UUID
	ReviewOutputKey       string
}

func (s ReviewerDispatchControllerScope) valid() bool {
	return s.MetadataID != uuid.Nil && s.SessionID != uuid.Nil && s.ReviewOutputKey != ""
}

type CodeReviewReviewerOwner struct {
	Result                          models.CodeReviewAgentResult
	Found, Retained, IntentsEnabled bool
}

// CreateLegacyCodeReviewReviewerInTx retains the preselected legacy shape but
// uses the already preminted result identity. The controller serializes its
// exact reviewer key at the metadata owner before invoking this DB-only leaf.
func CreateLegacyCodeReviewReviewerInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, result models.CodeReviewAgentResult) (models.CodeReviewAgentResult, error) {
	if ctx == nil || nilLifecycleDependency(tx) || orgID == uuid.Nil || result.OrgID != orgID || result.ID == uuid.Nil || result.SessionID == uuid.Nil || result.Role != models.CodeReviewAgentRoleReviewer || result.Status.Validate() != nil {
		return models.CodeReviewAgentResult{}, ErrReviewerDispatchInput
	}
	rows, err := tx.Query(ctx, `INSERT INTO code_review_agent_results(id,org_id,session_id,agent_provider,agent_model,role,status,raw_output,structured_result)
 VALUES($1,$2,$3,$4,$5,'reviewer',$6,$7,$8::jsonb) RETURNING `+codeReviewAgentResultColumns, result.ID, orgID, result.SessionID, result.AgentProvider, result.AgentModel, result.Status, result.RawOutput, []byte(result.StructuredResult))
	if err != nil {
		return models.CodeReviewAgentResult{}, err
	}
	return collectOneCodeReviewAgentResult(rows)
}

// ClaimCodeReviewReviewerBeforeDeadlineInTx reuses the primitive's ordered locks
// before checking the existing contribution deadline. It adds no timeout or
// retry policy. Pending work that reached the deadline remains no-call work.
func ClaimCodeReviewReviewerBeforeDeadlineInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, e ReviewerDispatchExpectation, attemptID uuid.UUID, deadline time.Time) (ReviewerDispatchIntent, bool, error) {
	if deadline.IsZero() {
		return ReviewerDispatchIntent{}, false, ErrReviewerDispatchInput
	}
	i, found, err := lockReviewerDispatch(ctx, orgID, tx, e)
	if err != nil || !found {
		return ReviewerDispatchIntent{}, false, err
	}
	if !i.MetadataEligible() || i.ContributionStatus() != "queued" {
		return ClaimReviewerDispatchInTx(ctx, orgID, tx, e, attemptID)
	}
	var before bool
	if err := tx.QueryRow(ctx, `SELECT statement_timestamp()<$1::timestamptz`, deadline).Scan(&before); err != nil {
		return ReviewerDispatchIntent{}, false, err
	}
	if !before {
		return ReviewerDispatchIntent{}, false, nil
	}
	return ClaimReviewerDispatchInTx(ctx, orgID, tx, e, attemptID)
}

// CancelIneligibleCodeReviewReviewerDispatchInTx is an observe-only entry
// cleanup: it can cancel exact pending work after a fresh ordered-lock metadata
// reread, but cannot install an attempting marker or release send authority.
func CancelIneligibleCodeReviewReviewerDispatchInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, e ReviewerDispatchExpectation) (bool, error) {
	i, found, err := lockReviewerDispatch(ctx, orgID, tx, e)
	if err != nil || !found || i.MetadataEligible() {
		return false, err
	}
	_, changed, err := CancelPendingReviewerDispatchInTx(ctx, orgID, tx, e, ReviewerDispatchOwnerIneligible)
	return changed, err
}

// ReadCodeReviewReviewerOwnerInTx serializes fresh result creation at its exact
// metadata owner, then re-reads the exact reviewer key in a LATER statement.
// Waiting on a prior creator proves only its settled row identity, never the
// outcome of a message. The caller releases this transaction before rendering,
// creating a lane, sending or observing, and repeats this check at insertion.
func ReadCodeReviewReviewerOwnerInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, scope ReviewerDispatchControllerScope, reviewerKey string) (CodeReviewReviewerOwner, error) {
	if ctx == nil || nilLifecycleDependency(tx) || orgID == uuid.Nil || !scope.valid() || reviewerKey == "" {
		return CodeReviewReviewerOwner{}, ErrReviewerDispatchInput
	}
	var id uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM code_review_session_metadata WHERE org_id=$1 AND session_id=$2 AND id=$3
 AND review_output_key=$4 AND status IN ('queued','running') AND NOT stale FOR UPDATE`, orgID, scope.SessionID, scope.MetadataID, scope.ReviewOutputKey).Scan(&id)
	if err != nil {
		return CodeReviewReviewerOwner{}, err
	}
	rows, err := tx.Query(ctx, `SELECT `+codeReviewAgentResultColumns+` FROM code_review_agent_results
 WHERE org_id=$1 AND session_id=$2 AND role='reviewer' AND structured_result->>'reviewer_key'=$3 ORDER BY id LIMIT 2`, orgID, scope.SessionID, reviewerKey)
	if err != nil {
		return CodeReviewReviewerOwner{}, err
	}
	results, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.CodeReviewAgentResult])
	if err != nil {
		return CodeReviewReviewerOwner{}, err
	}
	if len(results) > 1 {
		return CodeReviewReviewerOwner{}, ErrReviewerDispatchInput
	}
	if len(results) == 1 {
		owner, found, err := ReadRetainedCallerSendInTx(ctx, orgID, tx, CallerSendOwnerRef{Purpose: CallerSendStampCodeReviewDispatch, OwnerID: results[0].ID, SessionID: scope.SessionID})
		if err != nil {
			return CodeReviewReviewerOwner{}, err
		}
		if !found {
			return CodeReviewReviewerOwner{}, ErrReviewerDispatchStorage
		}
		if owner.Shape() != CallerSendRetainedLegacy {
			if owner.Shape() != CallerSendRetainedSupported {
				return CodeReviewReviewerOwner{}, ErrReviewerDispatchInput
			}
			if err := requireReviewerDispatchControllerScope(ctx, orgID, tx, scope, results[0].ID); err != nil {
				return CodeReviewReviewerOwner{}, err
			}
		}
		return CodeReviewReviewerOwner{Result: results[0], Found: true, Retained: owner.Shape() != CallerSendRetainedLegacy}, nil
	}
	var enabled bool
	err = tx.QueryRow(ctx, `SELECT schema_ready AND dispatch_status_ready AND intents_enabled FROM caller_send_intent_control WHERE singleton FOR SHARE`).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
		return CodeReviewReviewerOwner{}, nil
	}
	if err != nil {
		return CodeReviewReviewerOwner{}, err
	}
	return CodeReviewReviewerOwner{IntentsEnabled: enabled}, ctx.Err()
}

func requireReviewerDispatchControllerScope(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, scope ReviewerDispatchControllerScope, resultID uuid.UUID) error {
	var matches bool
	err := tx.QueryRow(ctx, `SELECT true FROM code_review_agent_results r JOIN code_review_session_metadata m
 ON m.org_id=r.org_id AND m.session_id=r.session_id WHERE r.org_id=$1 AND r.session_id=$2 AND r.id=$3 AND r.role='reviewer'
 AND m.id=$4 AND m.review_output_key=$5 AND r.dispatch_wake->'payload'->>'org_id'=$1::uuid::text
 AND r.dispatch_wake->'payload'->>'session_id'=$2::uuid::text AND r.dispatch_wake->'payload'->>'metadata_id'=$4::uuid::text
 AND r.dispatch_wake->'payload'->>'review_output_key'=$5`, orgID, scope.SessionID, resultID, scope.MetadataID, scope.ReviewOutputKey).Scan(&matches)
	if err != nil {
		return err
	}
	if !matches {
		return ErrReviewerDispatchInput
	}
	return ctx.Err()
}

// ReadCodeReviewReviewerDispatchInTx refuses malformed/unsupported retained
// owners and metadata borrowed from another controller. It never enables a
// legacy fallback. The returned command is bounded by the existing reader.
func ReadCodeReviewReviewerDispatchInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, scope ReviewerDispatchControllerScope, resultID uuid.UUID) (RetainedCallerSend, ReviewerDispatchIntent, bool, error) {
	if ctx == nil || nilLifecycleDependency(tx) || orgID == uuid.Nil || !scope.valid() || resultID == uuid.Nil {
		return RetainedCallerSend{}, ReviewerDispatchIntent{}, false, ErrReviewerDispatchInput
	}
	owner, found, err := ReadRetainedCallerSendInTx(ctx, orgID, tx, CallerSendOwnerRef{Purpose: CallerSendStampCodeReviewDispatch, OwnerID: resultID, SessionID: scope.SessionID})
	if err != nil || !found {
		return RetainedCallerSend{}, ReviewerDispatchIntent{}, false, err
	}
	if owner.Shape() == CallerSendRetainedLegacy {
		return owner, ReviewerDispatchIntent{}, false, nil
	}
	if owner.Shape() != CallerSendRetainedSupported {
		return RetainedCallerSend{}, ReviewerDispatchIntent{}, false, ErrReviewerDispatchInput
	}
	if err := requireReviewerDispatchControllerScope(ctx, orgID, tx, scope, resultID); err != nil {
		return RetainedCallerSend{}, ReviewerDispatchIntent{}, false, err
	}
	i, found, err := ReadReviewerDispatchIntentInTx(ctx, orgID, tx, scope.MetadataID, scope.SessionID, resultID)
	if err != nil {
		return RetainedCallerSend{}, ReviewerDispatchIntent{}, false, err
	}
	if !found {
		return RetainedCallerSend{}, ReviewerDispatchIntent{}, false, ErrReviewerDispatchInput
	}
	e, err := ReviewerDispatchExpectationFromRetained(scope.MetadataID, owner)
	if err != nil || e != i.Expectation() {
		return RetainedCallerSend{}, ReviewerDispatchIntent{}, false, ErrReviewerDispatchInput
	}
	return owner, i, true, nil
}

// ListCodeReviewRetainedReviewerIDsInTx is bounded and scoped. A saturated
// result set refuses truncation rather than pretending all owners were seen.
// It includes settled and terminal contributions: status is not receipt state.
func ListCodeReviewRetainedReviewerIDsInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, scope ReviewerDispatchControllerScope) ([]uuid.UUID, error) {
	if ctx == nil || nilLifecycleDependency(tx) || orgID == uuid.Nil || !scope.valid() {
		return nil, ErrReviewerDispatchInput
	}
	rows, err := tx.Query(ctx, `SELECT r.id FROM code_review_agent_results r JOIN code_review_session_metadata m ON m.org_id=r.org_id AND m.session_id=r.session_id
 WHERE r.org_id=$1 AND r.session_id=$2 AND m.id=$3 AND m.review_output_key=$4 AND r.role='reviewer' AND r.dispatch_version IS NOT NULL ORDER BY r.id LIMIT 101`, orgID, scope.SessionID, scope.MetadataID, scope.ReviewOutputKey)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, err
	}
	if len(ids) > 100 {
		return nil, ErrReviewerDispatchInput
	}
	return ids, ctx.Err()
}

// CloseCodeReviewReviewerDispatchDeadlineInTx rechecks retained state AFTER
// the ordered lock waits. If acceptance won, ordinary harvest owns that result
// instead; a stale unresolved snapshot cannot discard on-time accepted output.
func CloseCodeReviewReviewerDispatchDeadlineInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, scope ReviewerDispatchControllerScope, resultID uuid.UUID, raw *string, structured json.RawMessage, deadline time.Time) (bool, error) {
	if ctx == nil || nilLifecycleDependency(tx) || orgID == uuid.Nil || !scope.valid() || resultID == uuid.Nil || deadline.IsZero() {
		return false, ErrReviewerDispatchInput
	}
	var id uuid.UUID
	if err := tx.QueryRow(ctx, reviewerDispatchMetadataLockQuery, orgID, scope.SessionID, scope.MetadataID).Scan(&id); err != nil {
		return false, err
	}
	if err := tx.QueryRow(ctx, reviewerDispatchResultLockQuery, orgID, scope.SessionID, resultID).Scan(&id); err != nil {
		return false, err
	}
	_, i, retained, err := ReadCodeReviewReviewerDispatchInTx(ctx, orgID, tx, scope, resultID)
	if err != nil {
		return false, err
	}
	if !retained {
		return false, ErrReviewerDispatchInput
	}
	switch i.State() {
	case ReviewerDispatchPending, ReviewerDispatchAttempting, ReviewerDispatchUnknown, ReviewerDispatchAttention:
	default:
		return false, nil
	}
	_, err = UpdateCodeReviewReviewerContributionInTx(ctx, orgID, tx, scope, resultID, models.CodeReviewAgentResultStatusTimedOut, raw, structured, deadline)
	return err == nil, err
}

// UpdateCodeReviewReviewerContributionInTx is the controller's aware outcome
// writer. The metadata owner precedes the result lock. Terminal contributions
// never regress; unresolved sends are not ordinary failed/running/completed
// evidence. Deadline closure keeps their separate immutable attempt intact.
func UpdateCodeReviewReviewerContributionInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, scope ReviewerDispatchControllerScope, resultID uuid.UUID, status models.CodeReviewAgentResultStatus, raw *string, structured json.RawMessage, deadline time.Time) (models.CodeReviewAgentResult, error) {
	if ctx == nil || nilLifecycleDependency(tx) || orgID == uuid.Nil || !scope.valid() || resultID == uuid.Nil || status.Validate() != nil || (len(structured) > 0 && !json.Valid(structured)) {
		return models.CodeReviewAgentResult{}, ErrReviewerDispatchInput
	}
	var id uuid.UUID
	if err := tx.QueryRow(ctx, reviewerDispatchMetadataLockQuery, orgID, scope.SessionID, scope.MetadataID).Scan(&id); err != nil {
		return models.CodeReviewAgentResult{}, err
	}
	if err := tx.QueryRow(ctx, reviewerDispatchResultLockQuery, orgID, scope.SessionID, resultID).Scan(&id); err != nil {
		return models.CodeReviewAgentResult{}, err
	}
	owner, i, retained, err := ReadCodeReviewReviewerDispatchInTx(ctx, orgID, tx, scope, resultID)
	if err != nil {
		return models.CodeReviewAgentResult{}, err
	}
	if !retained {
		return NewCodeReviewStore(tx).UpdateAgentResultOutcome(ctx, orgID, resultID, status, raw, structured)
	}
	if owner.Shape() != CallerSendRetainedSupported {
		return models.CodeReviewAgentResult{}, ErrReviewerDispatchInput
	}
	unresolved := i.State() == ReviewerDispatchPending || i.State() == ReviewerDispatchAttempting || i.State() == ReviewerDispatchUnknown || i.State() == ReviewerDispatchAttention
	if !deadline.IsZero() && status == models.CodeReviewAgentResultStatusTimedOut && unresolved {
		var due bool
		if err := tx.QueryRow(ctx, `SELECT statement_timestamp()>=$1::timestamptz`, deadline).Scan(&due); err != nil {
			return models.CodeReviewAgentResult{}, err
		}
		if !due {
			return models.CodeReviewAgentResult{}, ErrReviewerDispatchInput
		}
		if i.State() == ReviewerDispatchPending {
			if _, _, err := CancelPendingReviewerDispatchInTx(ctx, orgID, tx, i.Expectation(), ReviewerDispatchDeadlineBeforeAttempt); err != nil {
				return models.CodeReviewAgentResult{}, err
			}
		} else {
			var fields map[string]json.RawMessage
			if json.Unmarshal(structured, &fields) != nil || fields == nil {
				return models.CodeReviewAgentResult{}, ErrReviewerDispatchInput
			}
			fields["error"] = json.RawMessage(`"dispatch_unresolved"`)
			fields["dispatch_unresolved"] = json.RawMessage(`true`)
			structured, err = json.Marshal(fields)
			if err != nil {
				return models.CodeReviewAgentResult{}, err
			}
			text := "Review contribution deadline reached; dispatch confirmation remains unresolved."
			raw = &text
		}
	} else if unresolved {
		return models.CodeReviewAgentResult{}, ErrReviewerDispatchInput
	}
	rows, err := tx.Query(ctx, `UPDATE code_review_agent_results SET status=$4,raw_output=$5,structured_result=COALESCE($6::jsonb,structured_result),
 dispatch_writer_seq=dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp()
 WHERE org_id=$1 AND session_id=$2 AND id=$3 AND role='reviewer' AND status IN ('queued','running')
 AND ($4<>'running' OR status='queued') AND dispatch_writer_seq<2147483647 RETURNING `+codeReviewAgentResultColumns, orgID, scope.SessionID, resultID, status, raw, []byte(structured))
	if err != nil {
		return models.CodeReviewAgentResult{}, err
	}
	result, err := collectOneCodeReviewAgentResult(rows)
	if errors.Is(err, pgx.ErrNoRows) {
		rows, readErr := tx.Query(ctx, `SELECT `+codeReviewAgentResultColumns+` FROM code_review_agent_results WHERE org_id=$1 AND session_id=$2 AND id=$3 AND role='reviewer'`, orgID, scope.SessionID, resultID)
		if readErr != nil {
			return models.CodeReviewAgentResult{}, readErr
		}
		return collectOneCodeReviewAgentResult(rows)
	}
	return result, err
}
