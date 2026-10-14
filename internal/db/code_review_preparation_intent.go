package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CaptureCodeReviewPreparationIntent validates admission against the same rows
// locked by failure projection. A claimant either observes terminal metadata,
// or remains visible as a live continuation when projection checks ownership.
func (s *CodeReviewStore) CaptureCodeReviewPreparationIntent(ctx context.Context, orgID, jobID, token uuid.UUID, session models.Session, thread *models.SessionThread) (models.CodeReviewPreparationIntent, error) {
	i := models.CodeReviewPreparationIntent{Version: 1, OrgID: orgID, JobID: jobID, SessionID: session.ID, WorkspaceGeneration: session.WorkspaceGeneration, Turn: session.CurrentTurn + 1, TurnScope: models.CodeReviewPreparationTurnSession, Stage: models.CodeReviewPreparationStageReadiness, FailureClass: models.CodeReviewPreparationFailureSetup}
	if thread != nil {
		id := thread.ID
		i.ThreadID = &id
		i.Turn = thread.CurrentTurn + 1
		i.TurnScope = models.CodeReviewPreparationTurnThread
	}
	// The source metadata join is scoped independently of caller payload fields.
	var pr uuid.UUID
	err := s.db.QueryRow(ctx, `SELECT pull_request_id FROM code_review_session_metadata WHERE org_id=$1 AND session_id=$2`, orgID, session.ID).Scan(&pr)
	if err != nil {
		return i, fmt.Errorf("load review preparation admission identity: %w", err)
	}
	err = s.RunWithGitHubPublicationLock(ctx, orgID, pr, func(lockCtx context.Context, tx DBTX) error {
		// Head writers do not share the publication advisory lock. Lock the
		// PR row first, before any job/session/metadata rows, and retain it
		// through admission. MATERIALIZED forces this read fence before the
		// outer join acquires those other row locks. NO KEY UPDATE excludes
		// head writers while allowing FK-only KEY SHARE locks from scheduling.
		return tx.QueryRow(lockCtx, `WITH locked_pr AS MATERIALIZED (
 SELECT org_id,id,head_sha FROM pull_requests
 WHERE org_id=@org_id AND id=@pr_id FOR NO KEY UPDATE
 )
 SELECT m.id,m.pull_request_id,m.head_sha
 FROM sessions s JOIN code_review_session_metadata m ON m.org_id=s.org_id AND m.session_id=s.id
 JOIN jobs j ON j.org_id=s.org_id AND j.id=@job_id
 JOIN locked_pr p ON p.org_id=m.org_id AND p.id=m.pull_request_id
 WHERE s.org_id=@org_id AND s.id=@session_id AND s.origin='code_review'
 AND s.workspace_generation=@generation AND s.status NOT IN ('cancelled','completed','failed')
 AND m.pull_request_id=@pr_id AND m.head_sha=p.head_sha AND m.status IN ('queued','running') AND NOT m.stale AND m.github_review_id IS NULL
 AND j.job_type='continue_session' AND j.status='running' AND j.lock_token=@token
 AND j.lease_expires_at>clock_timestamp() AND j.payload->>'session_id'=s.id::text
 AND COALESCE(j.payload->>'code_review_assessment_id','')=''
 AND ((@thread_id::uuid IS NULL AND COALESCE(j.payload->>'thread_id','')='' AND s.current_turn=@before_turn)
 OR (j.payload->>'thread_id'=@thread_id::text AND EXISTS(SELECT 1 FROM session_threads t WHERE t.org_id=@org_id AND t.session_id=s.id AND t.id=@thread_id AND t.current_turn=@before_turn AND t.cancel_requested_at IS NULL AND t.status<>'cancelled')))
 AND NOT EXISTS(SELECT 1 FROM code_review_revision_assessments a WHERE a.org_id=@org_id AND a.metadata_id=m.id AND a.session_id=s.id AND (a.publication_state NOT IN ('not_started','not_required') OR a.publication_receipt IS NOT NULL OR a.github_review_id IS NOT NULL OR a.result_origin IS NOT NULL))
 FOR NO KEY UPDATE OF s,m,j`, pgx.NamedArgs{"org_id": orgID, "job_id": jobID, "session_id": session.ID, "pr_id": pr, "generation": i.WorkspaceGeneration, "token": token, "thread_id": i.ThreadID, "before_turn": i.Turn - 1}).Scan(&i.MetadataID, &i.PullRequestID, &i.HeadSHA)
	})
	if err != nil {
		return i, fmt.Errorf("admit review preparation continuation: %w", err)
	}
	return i, i.Validate()
}

// DeadLetterContinueSessionWithPreparationIntentWithLease commits the only
// replay checkpoint together with retirement of its exact continuation lease.
func (s *JobStore) DeadLetterContinueSessionWithPreparationIntentWithLease(ctx context.Context, orgID, jobID, token uuid.UUID, intent models.CodeReviewPreparationIntent) (bool, error) {
	if err := intent.Validate(); err != nil {
		return false, err
	}
	if intent.OrgID != orgID || intent.JobID != jobID {
		return false, fmt.Errorf("review preparation intent job identity mismatch")
	}
	raw, err := json.Marshal(intent)
	if err != nil {
		return false, fmt.Errorf("encode review preparation intent: %w", err)
	}
	tag, err := s.execLeaseTerminalUpdate(ctx, `UPDATE jobs SET status='dead_letter', last_error='Review workspace preparation stopped; terminal projection pending.',
 payload=payload || jsonb_build_object('terminal_preparation_intent',$4::jsonb),completed_at=now(),
 locked_by_node_id=NULL,run_owner_id=NULL,owner_kind='worker',lock_token=NULL,locked_at=NULL,lease_expires_at=NULL,updated_at=now()
 WHERE org_id=$1 AND id=$2 AND status='running' AND lock_token=$3 AND job_type='continue_session'
 AND payload->>'session_id'=$5 AND COALESCE(payload->>'thread_id','')=$6
 AND COALESCE(payload->>'code_review_assessment_id','')=''
 AND NOT (payload ? 'terminal_preparation_intent')`, orgID, jobID, token, raw, intent.SessionID.String(), preparationIntentThread(intent.ThreadID))
	if err != nil {
		return false, fmt.Errorf("checkpoint terminal review preparation intent: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
func preparationIntentThread(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

// CodeReviewPreparationIntentBatch contains a bounded set of pending intents
// and indicates whether additional records may require another polling pass.
type CodeReviewPreparationIntentBatch struct {
	Intents []models.CodeReviewPreparationIntent
	More    bool
}

// PendingCodeReviewPreparationIntents returns a bounded, tenant-scoped set;
// malformed records have no authority and never fall back to last_error.
func (s *JobStore) PendingCodeReviewPreparationIntents(ctx context.Context, orgID, metadataID, sessionID uuid.UUID) (CodeReviewPreparationIntentBatch, error) {
	var batch CodeReviewPreparationIntentBatch
	rows, err := s.db.Query(ctx, `SELECT id, CASE WHEN octet_length((payload->'terminal_preparation_intent')::text)<=2048 THEN (payload->'terminal_preparation_intent')::text ELSE NULL END, octet_length((payload->'terminal_preparation_intent')::text)>2048 AS oversized FROM jobs
 WHERE org_id=$1 AND job_type='continue_session' AND status='dead_letter'
 AND payload->>'session_id'=$2 AND payload->'terminal_preparation_intent'->>'metadata_id'=$3
 AND payload ? 'terminal_preparation_intent'
 AND NOT (payload ? 'terminal_preparation_intent_consumed') ORDER BY updated_at DESC,id LIMIT 32`, orgID, sessionID.String(), metadataID.String())
	if err != nil {
		return batch, fmt.Errorf("list terminal review preparation intents: %w", err)
	}
	type record struct {
		id        uuid.UUID
		raw       []byte
		oversized bool
	}
	records := make([]record, 0, 32)
	for rows.Next() {
		var r record
		if err := rows.Scan(&r.id, &r.raw, &r.oversized); err != nil {
			rows.Close()
			return batch, err
		}
		records = append(records, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return batch, err
	}
	batch.More = len(records) == 32
	for _, r := range records {
		if r.oversized {
			tag, err := s.db.Exec(ctx, `UPDATE jobs SET payload=payload || jsonb_build_object('terminal_preparation_intent_consumed',true),updated_at=now()
 WHERE org_id=$1 AND id=$2 AND job_type='continue_session' AND status='dead_letter'
 AND payload->>'session_id'=$3 AND payload->'terminal_preparation_intent'->>'metadata_id'=$4
 AND octet_length((payload->'terminal_preparation_intent')::text)>2048 AND NOT(payload ? 'terminal_preparation_intent_consumed')`, orgID, r.id, sessionID.String(), metadataID.String())
			if err != nil {
				return batch, fmt.Errorf("discard oversized terminal review preparation intent: %w", err)
			}
			if tag.RowsAffected() != 1 {
				batch.More = true
			}
			continue
		}
		i, decodeErr := models.DecodeCodeReviewPreparationIntent(r.raw)
		if decodeErr != nil || i.OrgID != orgID || i.JobID != r.id || i.MetadataID != metadataID || i.SessionID != sessionID {
			// Discarding malformed journal data grants no review mutation authority.
			// Raw-value CAS prevents consuming a concurrently corrected replacement.
			tag, err := s.db.Exec(ctx, `UPDATE jobs SET payload=payload || jsonb_build_object('terminal_preparation_intent_consumed',true),updated_at=now()
 WHERE org_id=$1 AND id=$2 AND job_type='continue_session' AND status='dead_letter'
 AND payload->>'session_id'=$3 AND payload->'terminal_preparation_intent'->>'metadata_id'=$4
 AND payload->'terminal_preparation_intent'=$5::jsonb AND NOT(payload ? 'terminal_preparation_intent_consumed')`, orgID, r.id, sessionID.String(), metadataID.String(), r.raw)
			if err != nil {
				return batch, fmt.Errorf("discard malformed terminal review preparation intent: %w", err)
			}
			if tag.RowsAffected() != 1 {
				batch.More = true
			}
			continue
		}
		batch.Intents = append(batch.Intents, i)
	}
	return batch, nil
}

type CodeReviewPreparationProjection struct {
	Projected bool
	Pending   bool
}

// ProjectCodeReviewPreparationIntent applies the existing operational failure
// CAS and consumes the intent in one publication-serialized transaction. No
// provider or preparation operation is reachable from this store method.
func (s *CodeReviewStore) ProjectCodeReviewPreparationIntent(ctx context.Context, orgID uuid.UUID, intent models.CodeReviewPreparationIntent, reason string) (CodeReviewPreparationProjection, error) {
	var result CodeReviewPreparationProjection
	if err := intent.Validate(); err != nil {
		return result, err
	}
	if intent.OrgID != orgID {
		return result, fmt.Errorf("review preparation intent tenant mismatch")
	}
	var metadata models.CodeReviewSessionMetadata
	err := s.RunWithGitHubPublicationLock(ctx, orgID, intent.PullRequestID, func(lockCtx context.Context, tx DBTX) error {
		// Match admission's PR-first row order. The head comparison and the
		// metadata CAS must be protected by the same row lock, including
		// against plain UpdateHeadSHA/UpdateGitHubSnapshot writers.
		var headMatches bool
		err := tx.QueryRow(lockCtx, `SELECT COALESCE(head_sha,'')=$3 FROM pull_requests WHERE org_id=$1 AND id=$2 FOR NO KEY UPDATE`, orgID, intent.PullRequestID, intent.HeadSHA).Scan(&headMatches)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var raw []byte
		err = tx.QueryRow(lockCtx, `SELECT payload->'terminal_preparation_intent' FROM jobs WHERE org_id=$1 AND id=$2 AND status='dead_letter' AND job_type='continue_session' AND payload->>'session_id'=$3 AND NOT(payload ? 'terminal_preparation_intent_consumed') FOR UPDATE`, orgID, intent.JobID, intent.SessionID.String()).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		persisted, err := models.DecodeCodeReviewPreparationIntent(raw)
		if err != nil {
			return err
		}
		wanted, err := json.Marshal(intent)
		if err != nil {
			return err
		}
		actual, err := json.Marshal(persisted)
		if err != nil {
			return err
		}
		if string(wanted) != string(actual) {
			return fmt.Errorf("terminal review preparation intent changed")
		}
		// Lock session and metadata before inspecting any generation/turn/owner.
		var generation int64
		var turn int
		var cancelled bool
		err = tx.QueryRow(lockCtx, `SELECT s.workspace_generation,s.current_turn,s.status IN ('cancelled','completed','failed') FROM sessions s JOIN code_review_session_metadata m ON m.org_id=s.org_id AND m.session_id=s.id WHERE s.org_id=$1 AND s.id=$2 AND m.id=$3 FOR UPDATE OF s,m`, orgID, intent.SessionID, intent.MetadataID).Scan(&generation, &turn, &cancelled)
		missingMetadata := errors.Is(err, pgx.ErrNoRows)
		if err != nil && !missingMetadata {
			return err
		}
		superseded := !headMatches || missingMetadata
		if !superseded {
			superseded = cancelled || generation != intent.WorkspaceGeneration
			if intent.ThreadID == nil {
				superseded = superseded || turn != intent.Turn-1
			} else {
				var current int
				var stopped bool
				err = tx.QueryRow(lockCtx, `SELECT current_turn,cancel_requested_at IS NOT NULL OR status='cancelled' FROM session_threads WHERE org_id=$1 AND session_id=$2 AND id=$3 FOR UPDATE`, orgID, intent.SessionID, *intent.ThreadID).Scan(&current, &stopped)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				superseded = superseded || errors.Is(err, pgx.ErrNoRows) || stopped || current != intent.Turn-1
			}
		}
		if !superseded {
			var overlap bool
			err = tx.QueryRow(lockCtx, `SELECT EXISTS(SELECT 1 FROM jobs j WHERE j.org_id=$1 AND j.id<>$2 AND j.job_type='continue_session' AND j.payload->>'session_id'=$3 AND ((j.status='pending' AND j.created_at >= (SELECT source.created_at FROM jobs source WHERE source.org_id=$1 AND source.id=$2)) OR (j.status='running' AND j.lock_token IS NOT NULL AND j.lease_expires_at>clock_timestamp())) AND ($4::text='' OR COALESCE(j.payload->>'thread_id','') IN ('',$4)))`, orgID, intent.JobID, intent.SessionID.String(), preparationIntentThread(intent.ThreadID)).Scan(&overlap)
			if err != nil {
				return err
			}
			superseded = overlap
		}
		if !superseded {
			// The failing executor can still be finishing its own terminal write.
			// Retain the intent while an active owner remains before the captured
			// timeout. Replacement continuation fences above apply at every age.
			var live bool
			err = tx.QueryRow(lockCtx, `SELECT EXISTS(SELECT 1 FROM session_executors WHERE org_id=$1 AND session_id=$2 AND status IN ('starting','running','draining')) OR EXISTS(SELECT 1 FROM thread_runtimes WHERE org_id=$1 AND session_id=$2 AND status IN ('starting','live','paused','draining'))`, orgID, intent.SessionID).Scan(&live)
			if err != nil {
				return err
			}
			if live {
				deadline, now, err := codeReviewPreparationProjectionDeadline(lockCtx, tx, orgID, intent)
				if err != nil {
					return err
				}
				// Active statuses are conservative ownership evidence, even when
				// their lease is expired or unknown. At the captured recovery deadline
				// this known fatal preparation failure may project and request durable
				// cancellation; this does not establish owner drain or result timeout.
				if now.Before(deadline) {
					result.Pending = true
					return nil
				}
			}
		}
		if !superseded {
			metadata, err = NewCodeReviewStore(tx).failCodeReviewWorkspacePreparationLocked(lockCtx, orgID, FailCodeReviewWorkspacePreparationParams{ReviewID: intent.MetadataID, SessionID: intent.SessionID, PullRequestID: intent.PullRequestID, HeadSHA: intent.HeadSHA, ExpectedGeneration: intent.WorkspaceGeneration, Reason: reason})
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			result.Projected = err == nil
		}
		_, err = tx.Exec(lockCtx, `UPDATE jobs SET payload=payload || jsonb_build_object('terminal_preparation_intent_consumed',true),updated_at=now() WHERE org_id=$1 AND id=$2 AND status='dead_letter'`, orgID, intent.JobID)
		return err
	})
	if err != nil {
		return CodeReviewPreparationProjection{}, err
	}
	if result.Projected {
		s.publishUpdated(ctx, metadata)
	}
	return result, nil
}

// codeReviewPreparationProjectionDeadline reads only captured timeout authority
// inside the already fenced transaction. It never renews an owner or starts a
// new timeout from observation time. Explicit synthesis uses its exact baseline
// result that existed before the journal checkpoint. Review creation bounds
// recovery for reviewers, inferred scope, and
// absent results; this can settle failed inferred synthesis before its ordinary
// synthesis-result timeout without choosing a mutable primary or new budget.
func codeReviewPreparationProjectionDeadline(ctx context.Context, tx DBTX, orgID uuid.UUID, intent models.CodeReviewPreparationIntent) (time.Time, time.Time, error) {
	var createdAt, now time.Time
	var rosterJSON []byte
	var orchestratorCreatedAt *time.Time
	var orchestratorCount int64
	err := tx.QueryRow(ctx, `SELECT m.created_at,p.agent_roster,r.created_at,r.matches,clock_timestamp()
 FROM code_review_session_metadata m
 JOIN code_review_policies p ON p.org_id=m.org_id AND p.id=m.policy_id
 JOIN jobs source ON source.org_id=m.org_id AND source.id=$5 AND source.status='dead_letter'
 AND source.job_type='continue_session' AND source.payload->>'session_id'=m.session_id::text
 AND source.completed_at IS NOT NULL
 LEFT JOIN LATERAL (
 SELECT MIN(a.created_at) FILTER(WHERE a.role='orchestrator' AND a.status IN ('queued','running')) AS created_at,
 CASE WHEN COUNT(DISTINCT a.role)>1 THEN -1
 ELSE COUNT(*) FILTER(WHERE a.role='orchestrator' AND a.status IN ('queued','running')) END AS matches
 FROM code_review_agent_results a
 WHERE a.org_id=$1 AND a.session_id=m.session_id AND a.assessment_id IS NULL
 AND a.created_at<=source.completed_at
 AND $4::text<>'' AND a.structured_result->>'thread_id'=$4
 ) r ON true
 WHERE m.org_id=$1 AND m.id=$2 AND m.session_id=$3`, orgID, intent.MetadataID, intent.SessionID, preparationIntentThread(intent.ThreadID), intent.JobID).Scan(&createdAt, &rosterJSON, &orchestratorCreatedAt, &orchestratorCount, &now)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("load captured review preparation projection deadline: %w", err)
	}
	var rosterFields map[string]json.RawMessage
	if err := json.Unmarshal(rosterJSON, &rosterFields); err != nil || rosterFields == nil {
		return time.Time{}, time.Time{}, errors.New("invalid captured review preparation timeout authority")
	}
	if raw, present := rosterFields["timeout_seconds"]; present && string(raw) == "null" {
		return time.Time{}, time.Time{}, errors.New("invalid captured review preparation timeout authority")
	}
	var roster models.CodeReviewAgentRoster
	if err := json.Unmarshal(rosterJSON, &roster); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("decode captured review preparation timeout authority: %w", err)
	}
	policy := models.ResolveCodeReviewPolicyConfig(&models.CodeReviewPolicyConfig{AgentRoster: roster})
	if createdAt.IsZero() || now.IsZero() || orchestratorCount < 0 || orchestratorCount > 1 ||
		(orchestratorCount == 1 && (orchestratorCreatedAt == nil || orchestratorCreatedAt.IsZero() || orchestratorCreatedAt.Before(createdAt))) ||
		int64(policy.AgentRoster.TimeoutSeconds) > int64((1<<63-1)/time.Second) {
		return time.Time{}, time.Time{}, errors.New("invalid captured review preparation timeout authority")
	}
	if orchestratorCount == 1 {
		createdAt = *orchestratorCreatedAt
	}
	return models.CodeReviewAgentDeadline(policy, createdAt), now, nil
}
