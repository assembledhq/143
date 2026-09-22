package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrAutomationActionUnauthorized = errors.New("automation action scope is not authorized")
var ErrAutomationActionConflict = errors.New("action content conflicts with the reserved step")
var ErrAutomationActionLimit = errors.New("operation already contains 100 steps")
var ErrAutomationActionBusy = errors.New("action is already sending or cannot be retried")

type AutomationActionStore struct{ db TxStarter }

func NewAutomationActionStore(pool TxStarter) *AutomationActionStore {
	return &AutomationActionStore{db: pool}
}

// Resolve verifies current authority and the executing job lease for every run mode.
func (s *AutomationActionStore) Resolve(ctx context.Context, orgID uuid.UUID, actor models.AutomationActionActor) (models.AutomationActionScope, error) {
	return resolveAutomationActionScope(ctx, s.db, orgID, actor, true)
}

// ResolveStatus verifies historical membership without granting write authority.
func (s *AutomationActionStore) ResolveStatus(ctx context.Context, orgID uuid.UUID, actor models.AutomationActionActor) (models.AutomationActionScope, error) {
	return resolveAutomationActionScope(ctx, s.db, orgID, actor, false)
}
func resolveAutomationActionScope(ctx context.Context, q DBTX, orgID uuid.UUID, actor models.AutomationActionActor, write bool) (models.AutomationActionScope, error) {
	var out models.AutomationActionScope
	if orgID != actor.OrgID || orgID == uuid.Nil || actor.RunID == uuid.Nil || actor.SessionID == uuid.Nil || actor.ThreadID == uuid.Nil || actor.RepositoryID == uuid.Nil {
		return out, ErrAutomationActionUnauthorized
	}
	if write && (actor.AttemptToken == uuid.Nil || actor.JobID == uuid.Nil) {
		return out, ErrAutomationActionUnauthorized
	}
	query := `SELECT COALESCE(t.id,'00000000-0000-0000-0000-000000000000'::uuid),r.automation_id,COALESCE(t.target_key,''),repo.full_name,repo.installation_id,COALESCE(r.resolved_head_sha,''),cap->'config'
 FROM automation_runs r
 JOIN sessions sess ON sess.org_id=r.org_id AND sess.id=$3 AND sess.repository_id=$5
 JOIN session_threads thread ON thread.org_id=r.org_id AND thread.session_id=sess.id AND thread.id=$4
 JOIN repositories repo ON repo.org_id=r.org_id AND repo.id=$5
 LEFT JOIN automation_targets t ON t.org_id=r.org_id AND t.id=r.target_id AND t.automation_id=r.automation_id AND t.repository_id=$5
 CROSS JOIN LATERAL jsonb_array_elements(r.capability_snapshot) cap
 WHERE r.org_id=$1 AND r.id=$2 AND cap->>'id'='automation_actions' AND cap->>'access_level'='write'
 AND ((r.target_id IS NULL AND EXISTS(SELECT 1 FROM session_automation_links sal WHERE sal.org_id=r.org_id AND sal.session_id=sess.id AND sal.automation_run_id=r.id))
 OR (t.id IS NOT NULL AND r.session_id=sess.id AND r.thread_id=thread.id AND t.target_kind='github_pull_request'))`
	args := []any{orgID, actor.RunID, actor.SessionID, actor.ThreadID, actor.RepositoryID}
	if write {
		// Mutual JSONB containment compares action arrays as sets while retaining every destination field.
		query += ` AND sess.deleted_at IS NULL AND repo.status='active'
 AND EXISTS(SELECT 1 FROM automations a JOIN agent_capability_policies p ON p.org_id=a.org_id AND p.automation_id=a.id AND p.active AND p.policy_type='automation'
 JOIN agent_capability_policy_grants g ON g.org_id=p.org_id AND g.policy_id=p.id
 WHERE a.org_id=r.org_id AND a.id=r.automation_id AND a.enabled AND a.deleted_at IS NULL
 AND g.capability_id='automation_actions' AND g.enabled AND g.access_level='write' AND g.config @> (cap->'config') AND (cap->'config') @> g.config)
 AND EXISTS(SELECT 1 FROM jobs j WHERE j.org_id=r.org_id AND j.id=$6 AND j.status='running' AND j.lock_token=$7 AND j.lease_expires_at>now()
 AND j.job_type IN ('run_agent','continue_session') AND j.payload->>'session_id'=sess.id::text
 AND (COALESCE(j.payload->>'thread_id','')='' OR j.payload->>'thread_id'=thread.id::text))
 AND ((r.target_id IS NULL AND r.status='running') OR (r.job_id=$6 AND r.attempt_lock_token=$7 AND r.dispatch_state='executing' AND (t.lifecycle_state='open' OR (t.lifecycle_state='merged' AND r.config_snapshot->>'github_event'='github.pull_request.merged')) AND r.resolved_head_sha IS NOT NULL
 AND EXISTS(SELECT 1 FROM automation_target_sessions gen WHERE gen.org_id=r.org_id AND gen.target_id=t.id AND gen.generation=r.target_generation
 AND gen.generation=t.active_generation AND gen.session_id=sess.id AND gen.status='active' AND sess.automation_owner_generation_id=gen.id)))`
		args = append(args, actor.JobID, actor.AttemptToken)
	}
	var key string
	var cfg json.RawMessage
	err := q.QueryRow(ctx, query, args...).Scan(&out.TargetID, &out.AutomationID, &key, &out.RepositoryName, &out.InstallationID, &out.HeadSHA, &cfg)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrAutomationActionUnauthorized
	}
	if err != nil {
		return out, fmt.Errorf("resolve automation action scope: %w", err)
	}
	out.Config, err = models.ParseAutomationActionConfig(cfg)
	if err != nil {
		return out, ErrAutomationActionUnauthorized
	}
	out.ScopeKey = "repository:" + actor.RepositoryID.String()
	if out.TargetID != uuid.Nil {
		out.PRNumber, err = strconv.Atoi(key)
		if err != nil || out.PRNumber <= 0 {
			return out, ErrAutomationActionUnauthorized
		}
		out.ScopeKey = "target:" + out.TargetID.String()
	}
	out.Actor = actor
	return out, nil
}

// Use the same advisory -> target -> automation lock order as continuity dispatch.
// The advisory lock also serializes per-run action reservations for this automation.
func lockAutomationAction(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, actor models.AutomationActionActor) error {
	var automationID uuid.UUID
	err := tx.QueryRow(ctx, `SELECT automation_id FROM automation_runs WHERE org_id=$1 AND id=$2`, orgID, actor.RunID).Scan(&automationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAutomationActionUnauthorized
	}
	if err != nil {
		return err
	}
	if err = lockAutomationTargets(ctx, tx, orgID, automationID); err != nil {
		return err
	}
	var targetID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT t.id FROM automation_targets t JOIN automation_runs r ON r.org_id=t.org_id AND r.target_id=t.id WHERE t.org_id=$1 AND r.id=$2 FOR UPDATE OF t`, orgID, actor.RunID).Scan(&targetID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	err = tx.QueryRow(ctx, `SELECT id FROM automations WHERE org_id=$1 AND id=$2 FOR UPDATE`, orgID, automationID).Scan(&automationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAutomationActionUnauthorized
	}
	return err
}

const automationActionColumns = `id,org_id,automation_id,repository_id,scope_key,operation_key,action_key,pr_number,kind,created_run_id,last_run_id,head_sha,request_digest,destination,payload,status,attempt_count,send_token,send_deadline_at,provider_object_id,provider_url,last_error_code,created_at,updated_at,completed_at`

func listAutomationActions(ctx context.Context, q DBTX, orgID uuid.UUID, scope models.AutomationActionScope, operation string) ([]models.AutomationAction, error) {
	rows, err := q.Query(ctx, `SELECT `+automationActionColumns+` FROM automation_actions WHERE org_id=$1 AND automation_id=$2 AND scope_key=$3 AND operation_key=$4 ORDER BY created_at,id`, orgID, scope.AutomationID, scope.ScopeKey, operation)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.AutomationAction])
}
func (s *AutomationActionStore) List(ctx context.Context, orgID uuid.UUID, scope models.AutomationActionScope, operation string) ([]models.AutomationAction, error) {
	return listAutomationActions(ctx, s.db, orgID, scope, operation)
}
func actionRequestAllowed(scope models.AutomationActionScope, request models.AutomationActionRequest) bool {
	if request.ValidateFor(scope.Config) != nil {
		return false
	}
	if scope.PRNumber > 0 && request.PRNumber > 0 && (request.PRNumber != scope.PRNumber || request.HeadSHA != scope.HeadSHA) {
		return false
	}
	if strings.HasPrefix(string(request.Kind), "github_") && !strings.EqualFold(scope.Config.Repository, scope.RepositoryName) {
		return false
	}
	return true
}

// Reserve freezes exactly one step; another step of the same kind gets its own key.
func (s *AutomationActionStore) Reserve(ctx context.Context, orgID uuid.UUID, actor models.AutomationActionActor, request models.AutomationActionRequest, digest string, config models.AutomationActionConfig, payload json.RawMessage) (models.AutomationAction, error) {
	var out models.AutomationAction
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockAutomationAction(ctx, tx, orgID, actor); err != nil {
		return out, err
	}
	scope, err := resolveAutomationActionScope(ctx, tx, orgID, actor, true)
	if err != nil {
		return out, err
	}
	if !reflect.DeepEqual(scope.Config.Canonical(), config.Canonical()) || !actionRequestAllowed(scope, request) {
		return out, ErrAutomationActionUnauthorized
	}
	existing, err := listAutomationActions(ctx, tx, orgID, scope, request.OperationKey)
	if err != nil {
		return out, err
	}
	for _, a := range existing {
		if a.ActionKey == request.ActionKey {
			if a.RequestDigest != digest {
				return out, ErrAutomationActionConflict
			}
			return a, tx.Commit(ctx)
		}
	}
	if len(existing) >= 100 {
		return out, ErrAutomationActionLimit
	}
	destination, err := json.Marshal(scope.Config)
	if err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `INSERT INTO automation_actions (org_id,automation_id,repository_id,scope_key,operation_key,action_key,pr_number,kind,created_run_id,last_run_id,head_sha,request_digest,destination,payload)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$9,$10,$11,$12,$13) RETURNING `+automationActionColumns, orgID, scope.AutomationID, actor.RepositoryID, scope.ScopeKey, request.OperationKey, request.ActionKey, request.PRNumber, request.Kind, actor.RunID, request.HeadSHA, digest, destination, payload)
	if err != nil {
		return out, err
	}
	out, err = pgx.CollectOneRow(rows, pgx.RowToStructByName[models.AutomationAction])
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

// Claim rechecks current authority. Neither succeeded nor uncertain sends are retryable.
func (s *AutomationActionStore) Claim(ctx context.Context, orgID uuid.UUID, actor models.AutomationActionActor, id uuid.UUID) (models.AutomationAction, error) {
	var out models.AutomationAction
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockAutomationAction(ctx, tx, orgID, actor); err != nil {
		return out, err
	}
	scope, err := resolveAutomationActionScope(ctx, tx, orgID, actor, true)
	if err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT `+automationActionColumns+` FROM automation_actions WHERE org_id=$1 AND automation_id=$2 AND scope_key=$3 AND id=$4`, orgID, scope.AutomationID, scope.ScopeKey, id)
	if err != nil {
		return out, err
	}
	out, err = pgx.CollectOneRow(rows, pgx.RowToStructByName[models.AutomationAction])
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrAutomationActionUnauthorized
	}
	if err != nil {
		return out, err
	}
	var frozen struct {
		Request models.AutomationActionRequest `json:"request"`
	}
	var old models.AutomationActionConfig
	if err = json.Unmarshal(out.Payload, &frozen); err != nil {
		return out, err
	}
	if err = json.Unmarshal(out.Destination, &old); err != nil {
		return out, err
	}
	if !reflect.DeepEqual(old.Canonical(), scope.Config.Canonical()) || !actionRequestAllowed(scope, frozen.Request) {
		return out, ErrAutomationActionConflict
	}
	rows, err = tx.Query(ctx, `UPDATE automation_actions SET status='sending',attempt_count=attempt_count+1,send_token=$3,send_deadline_at=clock_timestamp()+interval '8 seconds',last_run_id=$4,last_error_code=NULL,updated_at=now()
 WHERE org_id=$1 AND id=$2 AND status IN ('pending','failed') RETURNING `+automationActionColumns, orgID, id, uuid.New(), actor.RunID)
	if err != nil {
		return out, err
	}
	out, err = pgx.CollectOneRow(rows, pgx.RowToStructByName[models.AutomationAction])
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrAutomationActionBusy
	}
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

// Expire preserves uncertainty instead of making crashed sends retryable.
func (s *AutomationActionStore) Expire(ctx context.Context, orgID uuid.UUID, scope models.AutomationActionScope, operation string) error {
	_, err := s.db.Exec(ctx, `UPDATE automation_actions SET status='unknown',last_error_code='SEND_DEADLINE_EXPIRED',updated_at=now() WHERE org_id=$1 AND automation_id=$2 AND scope_key=$3 AND operation_key=$4 AND status='sending' AND send_deadline_at<=clock_timestamp()`, orgID, scope.AutomationID, scope.ScopeKey, operation)
	return err
}

// Finish deliberately accepts the original send token after authority is revoked.
func (s *AutomationActionStore) Finish(ctx context.Context, orgID, id, sendToken uuid.UUID, status models.AutomationActionStatus, providerID, providerURL, code string) error {
	if status != models.AutomationActionSucceeded && status != models.AutomationActionFailed && status != models.AutomationActionUnknown {
		return errors.New("invalid terminal send status")
	}
	tag, err := s.db.Exec(ctx, `UPDATE automation_actions SET status=$4,provider_object_id=NULLIF($5,''),provider_url=NULLIF($6,''),last_error_code=NULLIF($7,''),completed_at=CASE WHEN $4='succeeded' THEN now() ELSE NULL END,updated_at=now()
 WHERE org_id=$1 AND id=$2 AND send_token=$3 AND status IN ('sending','unknown')`, orgID, id, sendToken, status, providerID, providerURL, code)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrAutomationActionBusy
	}
	return nil
}

// ListAutomationActionCommentOwners identifies all automations owning this own-app comment in one query.
func (s *AutomationActionStore) ListAutomationActionCommentOwners(ctx context.Context, orgID, repoID uuid.UUID, pr int, commentID int64, body string) ([]uuid.UUID, error) {
	rows, err := s.db.Query(ctx, `SELECT DISTINCT automation_id FROM automation_actions WHERE org_id=$1 AND repository_id=$2 AND pr_number=$3 AND kind='github_issue_comment' AND attempt_count>0 AND status IN ('sending','unknown','succeeded') AND (provider_object_id=$4 OR (payload #>> '{request,text}') || E'\n\n<!-- 143-automation-action:' || id::text || ' -->'=$5)`, orgID, repoID, pr, strconv.FormatInt(commentID, 10), body)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}
