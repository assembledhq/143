package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrReviewLoopStartIdentity = errors.New("retained review start identity conflict")
	ErrReviewLoopStartDisabled = errors.New("retained review start is disabled")
	ErrReviewLoopStartTooLarge = errors.New("retained review start exceeds storage limit")
	ErrReviewLoopStartCaller   = errors.New("retained review start caller changed")
)

// ReviewLoopStartScope is independently retained correlation, not an API input
// or permission to create a replacement when a start is temporarily invisible.
type ReviewLoopStartScope struct {
	ID, SessionID                uuid.UUID
	Source                       models.ReviewLoopSource
	AutomationRunID, ChangesetID *uuid.UUID
}

func (s ReviewLoopStartScope) valid() bool {
	if s.ID == uuid.Nil || s.SessionID == uuid.Nil || s.Source.Validate() != nil {
		return false
	}
	switch s.Source {
	case models.ReviewLoopSourceManual:
		return s.AutomationRunID == nil && s.ChangesetID == nil
	case models.ReviewLoopSourceAutomation:
		return s.AutomationRunID != nil && *s.AutomationRunID != uuid.Nil && s.ChangesetID == nil
	case models.ReviewLoopSourcePublication:
		return s.ChangesetID != nil && *s.ChangesetID != uuid.Nil
	}
	return false
}

// ReviewLoopStartPreparation carries private persistence inputs that must never
// be serialized in a response or log. The service validates their codec and
// semantic digest.
type ReviewLoopStartPreparation struct {
	Scope                         ReviewLoopStartScope `json:"-"`
	PassID, ThreadID              uuid.UUID            `json:"-"`
	Envelope, Input               json.RawMessage      `json:"-"`
	EnvelopeDigest, StartupDigest string               `json:"-"`
}

type ReviewLoopStartIntent struct {
	orgID         uuid.UUID
	prepared      ReviewLoopStartPreparation
	state, detail string
	cancelled     bool
}

func (s ReviewLoopStartIntent) Scope() ReviewLoopStartScope { return s.Preparation().Scope }
func (s ReviewLoopStartIntent) OrgID() uuid.UUID            { return s.orgID }
func (s ReviewLoopStartIntent) State() string               { return s.state }
func (s ReviewLoopStartIntent) Detail() string              { return s.detail }
func (s ReviewLoopStartIntent) Cancelled() bool             { return s.cancelled }
func (s ReviewLoopStartIntent) Preparation() ReviewLoopStartPreparation {
	p := s.prepared
	p.Envelope, p.Input = bytes.Clone(p.Envelope), bytes.Clone(p.Input)
	if p.Scope.AutomationRunID != nil {
		v := *p.Scope.AutomationRunID
		p.Scope.AutomationRunID = &v
	}
	if p.Scope.ChangesetID != nil {
		v := *p.Scope.ChangesetID
		p.Scope.ChangesetID = &v
	}
	return p
}

// ReviewLoopStartEnabledInTx is a fresh-owner choice only. A retained owner is
// always recovered even when the current process has since disabled creation.
func ReviewLoopStartEnabledInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx) (bool, error) {
	if ctx == nil || nilLifecycleDependency(tx) || orgID == uuid.Nil {
		return false, ErrReviewLoopStartIdentity
	}
	var enabled bool
	err := tx.QueryRow(ctx, `SELECT schema_ready AND dispatch_status_ready AND intents_enabled FROM caller_send_intent_control WHERE singleton FOR SHARE`).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
		return false, nil
	}
	return enabled, err
}

// ReadReviewLoopStartInTx locks only the start. It never takes a caller lock
// afterward. T0/T1 take their caller lock first in a separate statement.
func ReadReviewLoopStartInTx(ctx context.Context, orgID, sessionID, startID uuid.UUID, tx pgx.Tx) (ReviewLoopStartIntent, bool, error) {
	if ctx == nil || nilLifecycleDependency(tx) || orgID == uuid.Nil || sessionID == uuid.Nil || startID == uuid.Nil {
		return ReviewLoopStartIntent{}, false, ErrReviewLoopStartIdentity
	}
	var locked uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM review_loop_start_intents WHERE org_id=$1 AND session_id=$2 AND id=$3 FOR UPDATE`, orgID, sessionID, startID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
		return ReviewLoopStartIntent{}, false, nil
	}
	if err != nil {
		return ReviewLoopStartIntent{}, false, err
	}
	// A later statement observes a creator that committed while the lock waited.
	i := ReviewLoopStartIntent{orgID: orgID}
	i.prepared.Scope.ID, i.prepared.Scope.SessionID = startID, sessionID
	var envelope, input *string
	err = tx.QueryRow(ctx, `SELECT caller_kind,automation_run_id,publication_changeset_id,pass_id,thread_id,
 CASE WHEN octet_length(envelope::text)<=8388608 THEN envelope::text END,envelope_digest,
 CASE WHEN octet_length(startup_input::text)<=8388608 THEN startup_input::text END,startup_digest,
 state,COALESCE(detail,''),cancel_requested_at IS NOT NULL
 FROM review_loop_start_intents WHERE org_id=$1 AND session_id=$2 AND id=$3`, orgID, sessionID, startID).Scan(
		&i.prepared.Scope.Source, &i.prepared.Scope.AutomationRunID, &i.prepared.Scope.ChangesetID, &i.prepared.PassID, &i.prepared.ThreadID,
		&envelope, &i.prepared.EnvelopeDigest, &input, &i.prepared.StartupDigest, &i.state, &i.detail, &i.cancelled)
	if err != nil {
		return ReviewLoopStartIntent{}, false, err
	}
	if envelope == nil || input == nil || !i.prepared.Scope.valid() {
		return ReviewLoopStartIntent{}, false, ErrReviewLoopStartIdentity
	}
	i.prepared.Envelope, i.prepared.Input = []byte(*envelope), []byte(*input)
	return i, true, nil
}

func lockReviewLoopStartCaller(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, scope ReviewLoopStartScope) (*uuid.UUID, error) {
	var current *uuid.UUID
	switch scope.Source {
	case models.ReviewLoopSourceManual:
		return nil, nil
	case models.ReviewLoopSourceAutomation:
		err := tx.QueryRow(ctx, `SELECT review_start_id FROM automation_runs a WHERE a.org_id=$1 AND a.id=$2 AND
 (a.session_id=$3 OR EXISTS(SELECT 1 FROM session_automation_links l WHERE l.org_id=a.org_id AND l.automation_run_id=a.id AND l.session_id=$3)) FOR UPDATE OF a`, orgID, scope.AutomationRunID, scope.SessionID).Scan(&current)
		return current, err
	case models.ReviewLoopSourcePublication:
		err := tx.QueryRow(ctx, `SELECT review_start_id FROM session_publications WHERE org_id=$1 AND session_id=$2 AND changeset_id=$3 FOR UPDATE`, orgID, scope.SessionID, scope.ChangesetID).Scan(&current)
		return current, err
	}
	return nil, ErrReviewLoopStartIdentity
}

func reviewLoopStartWake(orgID, threadID uuid.UUID) ([]byte, string, error) {
	payload, err := json.Marshal(map[string]any{"org_id": orgID.String(), "limit": 50})
	return payload, "reconcile_session_review_loop:" + threadID.String(), err
}

func enqueueReviewLoopStartWake(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, threadID uuid.UUID) error {
	payload, key, err := reviewLoopStartWake(orgID, threadID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO jobs(org_id,queue,job_type,payload,priority,dedupe_key,max_attempts)
 VALUES($1,'agent','reconcile_session_review_loop',$2::jsonb,5,$3,8) ON CONFLICT DO NOTHING`, orgID, payload, key)
	return err
}

// PrepareReviewLoopStartInTx owns only DB leaves. Intent, caller CAS and wake
// are committed together by the existing bounded outer transaction runner.
func PrepareReviewLoopStartInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, p ReviewLoopStartPreparation) (ReviewLoopStartIntent, error) {
	if ctx == nil || nilLifecycleDependency(tx) || orgID == uuid.Nil || !p.Scope.valid() || p.PassID == uuid.Nil || p.ThreadID == uuid.Nil {
		return ReviewLoopStartIntent{}, ErrReviewLoopStartIdentity
	}
	current, err := lockReviewLoopStartCaller(ctx, orgID, tx, p.Scope)
	if err != nil {
		return ReviewLoopStartIntent{}, err
	}
	if current != nil && *current != p.Scope.ID {
		var unresolved bool
		if err := tx.QueryRow(ctx, `SELECT caller143_start_unresolved($1,$2)`, orgID, *current).Scan(&unresolved); err != nil {
			return ReviewLoopStartIntent{}, err
		}
		if unresolved {
			winner, found, err := ReadReviewLoopStartInTx(ctx, orgID, p.Scope.SessionID, *current, tx)
			if err != nil {
				return ReviewLoopStartIntent{}, err
			}
			if !found {
				return ReviewLoopStartIntent{}, ErrReviewLoopStartCaller
			}
			return winner, nil
		}
	}
	old, found, err := ReadReviewLoopStartInTx(ctx, orgID, p.Scope.SessionID, p.Scope.ID, tx)
	if err != nil {
		return ReviewLoopStartIntent{}, err
	}
	if found {
		if p.Scope.Source != models.ReviewLoopSourceManual && (current == nil || *current != p.Scope.ID) {
			return ReviewLoopStartIntent{}, ErrReviewLoopStartCaller
		}
		if old.prepared.EnvelopeDigest != p.EnvelopeDigest || old.prepared.StartupDigest != p.StartupDigest || old.prepared.PassID != p.PassID || old.prepared.ThreadID != p.ThreadID {
			return ReviewLoopStartIntent{}, ErrReviewLoopStartIdentity
		}
		return old, nil
	}
	if _, err := ParseLifecycleDigest(p.EnvelopeDigest); err != nil {
		return ReviewLoopStartIntent{}, ErrReviewLoopStartIdentity
	}
	if _, err := ParseLifecycleDigest(p.StartupDigest); err != nil {
		return ReviewLoopStartIntent{}, ErrReviewLoopStartIdentity
	}
	if _, ok := codeReviewDispatchPreparationObject(p.Envelope); !ok {
		return ReviewLoopStartIntent{}, ErrReviewLoopStartIdentity
	}
	if _, ok := codeReviewDispatchPreparationObject(p.Input); !ok {
		return ReviewLoopStartIntent{}, ErrReviewLoopStartIdentity
	}
	enabled, err := ReviewLoopStartEnabledInTx(ctx, orgID, tx)
	if err != nil {
		return ReviewLoopStartIntent{}, err
	}
	if !enabled {
		return ReviewLoopStartIntent{}, ErrReviewLoopStartDisabled
	}
	var fits bool
	if err := tx.QueryRow(ctx, `SELECT octet_length($1::jsonb::text)<=8388608 AND octet_length($2::jsonb::text)<=8388608`, p.Envelope, p.Input).Scan(&fits); err != nil {
		return ReviewLoopStartIntent{}, err
	}
	if !fits {
		return ReviewLoopStartIntent{}, ErrReviewLoopStartTooLarge
	}
	var automation *uuid.UUID
	if p.Scope.Source == models.ReviewLoopSourceAutomation {
		automation = p.Scope.AutomationRunID
	}
	_, err = tx.Exec(ctx, `INSERT INTO review_loop_start_intents(id,org_id,session_id,caller_kind,automation_run_id,publication_changeset_id,
 envelope,envelope_digest,pass_id,thread_id,startup_input,startup_digest,state,due_at,created_at,updated_at)
 VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10,$11::jsonb,$12,'prepared',statement_timestamp(),statement_timestamp(),statement_timestamp())
 ON CONFLICT(id) DO NOTHING`, p.Scope.ID, orgID, p.Scope.SessionID, p.Scope.Source, automation, p.Scope.ChangesetID, p.Envelope, p.EnvelopeDigest, p.PassID, p.ThreadID, p.Input, p.StartupDigest)
	if err != nil {
		return ReviewLoopStartIntent{}, err
	}
	installed, found, err := ReadReviewLoopStartInTx(ctx, orgID, p.Scope.SessionID, p.Scope.ID, tx)
	if err != nil {
		return ReviewLoopStartIntent{}, err
	}
	if !found || installed.prepared.EnvelopeDigest != p.EnvelopeDigest || installed.prepared.StartupDigest != p.StartupDigest || installed.prepared.ThreadID != p.ThreadID || installed.prepared.PassID != p.PassID {
		return ReviewLoopStartIntent{}, ErrReviewLoopStartIdentity
	}
	if p.Scope.Source != models.ReviewLoopSourceManual {
		query := `UPDATE automation_runs SET review_start_id=$3 WHERE org_id=$1 AND id=$2 AND (review_start_id IS NULL OR review_start_id=$3 OR NOT caller143_start_unresolved(org_id,review_start_id))`
		args := []any{orgID, p.Scope.AutomationRunID, p.Scope.ID}
		if p.Scope.Source == models.ReviewLoopSourcePublication {
			query = `UPDATE session_publications SET review_start_id=$4 WHERE org_id=$1 AND session_id=$2 AND changeset_id=$3
 AND review_loop_id IS NULL AND state='review_pending' AND review_gate_state='pending'
 AND (review_start_id IS NULL OR review_start_id=$4 OR NOT caller143_start_unresolved(org_id,review_start_id))`
			args = []any{orgID, p.Scope.SessionID, p.Scope.ChangesetID, p.Scope.ID}
		}
		tag, err := tx.Exec(ctx, query, args...)
		if err != nil {
			return ReviewLoopStartIntent{}, err
		}
		if tag.RowsAffected() != 1 {
			return ReviewLoopStartIntent{}, ErrReviewLoopStartCaller
		}
	}
	if err := enqueueReviewLoopStartWake(ctx, orgID, tx, p.ThreadID); err != nil {
		return ReviewLoopStartIntent{}, err
	}
	return installed, nil
}

func RefuseReviewLoopStartInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, expected ReviewLoopStartIntent, detail string) error {
	if expected.orgID != orgID || !expected.prepared.Scope.valid() {
		return ErrReviewLoopStartIdentity
	}
	switch detail {
	case "cancelled_before_creation", "session_unavailable", "running_conflict", "thread_identity_conflict":
	case "creation_thread_capacity", "creation_session_terminal", "creation_code_review_owned", "creation_automation_owned", "creation_invalid_model", "creation_invalid_agent":
		// E1 may have raced a successful same-ID creator after its own pre-row
		// refusal. A fresh positive row wins over that stale refusal; recovery
		// must adopt/validate it instead. The held start lock still prevents
		// any T1 after a committed refused outcome.
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_threads WHERE org_id=$1 AND id=$2)`, orgID, expected.prepared.ThreadID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
	default:
		return ErrReviewLoopStartIdentity
	}
	_, err := tx.Exec(ctx, `UPDATE review_loop_start_intents SET state='refused',due_at=NULL,detail=$5,updated_at=statement_timestamp()
 WHERE org_id=$1 AND session_id=$2 AND id=$3 AND envelope_digest=$4 AND state='prepared'`, orgID, expected.prepared.Scope.SessionID, expected.prepared.Scope.ID, expected.prepared.EnvelopeDigest, detail)
	return err
}

// InspectReviewLoopStartInTx is the short recovery read unit. Cancellation and
// unavailable sessions can be resolved before E1, but no caller lock is taken
// after the start lock and no thread/service work happens here.
func InspectReviewLoopStartInTx(ctx context.Context, orgID, sessionID, startID uuid.UUID, tx pgx.Tx) (ReviewLoopStartIntent, bool, error) {
	i, found, err := ReadReviewLoopStartInTx(ctx, orgID, sessionID, startID, tx)
	if err != nil || !found || i.state != "prepared" {
		return i, found, err
	}
	var visible bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE org_id=$1 AND id=$2 AND deleted_at IS NULL AND archived_at IS NULL)`, orgID, sessionID).Scan(&visible)
	if err != nil {
		return ReviewLoopStartIntent{}, false, err
	}
	if i.cancelled || !visible {
		detail := "session_unavailable"
		if i.cancelled {
			detail = "cancelled_before_creation"
		}
		if err := RefuseReviewLoopStartInTx(ctx, orgID, tx, i, detail); err != nil {
			return ReviewLoopStartIntent{}, false, err
		}
		i.state, i.detail = "refused", detail
	}
	return i, true, nil
}

// CompleteReviewLoopStartInTx is T1. It reacquires caller then start and uses a
// fresh snapshot after both waits. No service call or send crosses this unit.
func CompleteReviewLoopStartInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, expected ReviewLoopStartIntent, loop models.SessionReviewLoop) (ReviewLoopStartIntent, error) {
	if ctx == nil || nilLifecycleDependency(tx) || expected.orgID != orgID || !expected.prepared.Scope.valid() {
		return ReviewLoopStartIntent{}, ErrReviewLoopStartIdentity
	}
	scope := expected.prepared.Scope
	current, err := lockReviewLoopStartCaller(ctx, orgID, tx, scope)
	if err != nil {
		return ReviewLoopStartIntent{}, err
	}
	if scope.Source != models.ReviewLoopSourceManual && (current == nil || *current != scope.ID) {
		return ReviewLoopStartIntent{}, ErrReviewLoopStartCaller
	}
	i, found, err := ReadReviewLoopStartInTx(ctx, orgID, scope.SessionID, scope.ID, tx)
	if err != nil {
		return ReviewLoopStartIntent{}, err
	}
	if !found || i.prepared.EnvelopeDigest != expected.prepared.EnvelopeDigest || i.prepared.StartupDigest != expected.prepared.StartupDigest {
		return ReviewLoopStartIntent{}, ErrReviewLoopStartIdentity
	}
	if i.state != "prepared" {
		return i, nil
	}
	var visible bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE org_id=$1 AND id=$2 AND deleted_at IS NULL AND archived_at IS NULL)`, orgID, scope.SessionID).Scan(&visible)
	if err != nil {
		return ReviewLoopStartIntent{}, err
	}
	if i.cancelled || !visible {
		detail := "session_unavailable"
		if i.cancelled {
			detail = "cancelled_before_creation"
		}
		if err := RefuseReviewLoopStartInTx(ctx, orgID, tx, i, detail); err != nil {
			return ReviewLoopStartIntent{}, err
		}
		i.state, i.detail = "refused", detail
		return i, nil
	}
	if loop.ID != scope.ID || loop.OrgID != orgID || loop.SessionID != scope.SessionID || loop.ThreadID == nil || *loop.ThreadID != i.prepared.ThreadID || loop.Source != scope.Source {
		return ReviewLoopStartIntent{}, ErrReviewLoopStartIdentity
	}
	var exactThread bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM review_loop_start_intents s JOIN session_threads t
 ON t.org_id=s.org_id AND t.session_id=s.session_id AND t.id=s.thread_id
 WHERE s.org_id=$1 AND s.session_id=$2 AND s.id=$3 AND t.archived_at IS NULL
 AND t.agent_type=s.envelope#>>'{thread,agent_type}' AND t.label=s.envelope#>>'{thread,label}'
 AND t.model_override IS NOT DISTINCT FROM s.envelope#>>'{thread,model}')`, orgID, scope.SessionID, scope.ID).Scan(&exactThread)
	if err != nil {
		return ReviewLoopStartIntent{}, err
	}
	if !exactThread {
		if err := RefuseReviewLoopStartInTx(ctx, orgID, tx, i, "thread_identity_conflict"); err != nil {
			return ReviewLoopStartIntent{}, err
		}
		i.state, i.detail = "refused", "thread_identity_conflict"
		return i, nil
	}
	var conflicting bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_review_loops WHERE org_id=$1 AND session_id=$2 AND id<>$3 AND status='running')`, orgID, scope.SessionID, scope.ID).Scan(&conflicting)
	if err != nil {
		return ReviewLoopStartIntent{}, err
	}
	if conflicting {
		if err := RefuseReviewLoopStartInTx(ctx, orgID, tx, i, "running_conflict"); err != nil {
			return ReviewLoopStartIntent{}, err
		}
		i.state, i.detail = "refused", "running_conflict"
		return i, nil
	}
	_, err = tx.Exec(ctx, `UPDATE review_loop_start_intents SET state='created',due_at=NULL,updated_at=statement_timestamp()
 WHERE org_id=$1 AND session_id=$2 AND id=$3 AND state='prepared' AND cancel_requested_at IS NULL`, orgID, scope.SessionID, scope.ID)
	if err != nil {
		return ReviewLoopStartIntent{}, err
	}
	key, err := CallerSendStampKey(CallerSendStampReviewLoopStartup, scope.ID)
	if err != nil {
		return ReviewLoopStartIntent{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO session_review_loops(id,org_id,session_id,thread_id,status,source,automation_run_id,
 changeset_id,workspace_revision,desired_head_sha,agent_type,max_passes,fix_mode,review_required,started_by_user_id,
 loop_start_checkpoint_key,latest_checkpoint_key,startup_send_version,startup_send_key,startup_send_input,startup_send_digest,
 startup_send_thread_id,startup_send_state,startup_send_attempt_seq,startup_send_due_at,startup_send_writer_seq,startup_send_updated_at,startup_send_pass_id)
 VALUES($1,$2,$3,$4,'running',$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$15,1,$16,$17::jsonb,$18,$4,'pending',0,statement_timestamp(),0,statement_timestamp(),$19)
 ON CONFLICT(id) DO NOTHING`, loop.ID, orgID, loop.SessionID, loop.ThreadID, loop.Source, loop.AutomationRunID, loop.ChangesetID,
		loop.WorkspaceRevision, loop.DesiredHeadSHA, loop.AgentType, loop.MaxPasses, loop.FixMode, loop.ReviewRequired, loop.StartedByUserID,
		loop.LoopStartCheckpointKey, key, i.prepared.Input, i.prepared.StartupDigest, i.prepared.PassID)
	if err != nil {
		return ReviewLoopStartIntent{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO session_review_loop_passes(id,org_id,session_id,loop_id,pass_index,status,review_started_at)
 VALUES($1,$2,$3,$4,1,'reviewing',statement_timestamp()) ON CONFLICT(id) DO NOTHING`, i.prepared.PassID, orgID, scope.SessionID, scope.ID)
	if err != nil {
		return ReviewLoopStartIntent{}, err
	}
	if err := verifyReviewLoopStartIdentity(ctx, orgID, tx, i, loop); err != nil {
		return ReviewLoopStartIntent{}, err
	}
	if err := enqueueReviewLoopStartWake(ctx, orgID, tx, i.prepared.ThreadID); err != nil {
		return ReviewLoopStartIntent{}, err
	}
	i.state = "created"
	return i, nil
}

func verifyReviewLoopStartIdentity(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, i ReviewLoopStartIntent, loop models.SessionReviewLoop) error {
	var matches bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_review_loops l JOIN session_review_loop_passes p
 ON p.org_id=l.org_id AND p.session_id=l.session_id AND p.loop_id=l.id AND p.id=$16
 WHERE l.org_id=$1 AND l.session_id=$2 AND l.id=$3 AND l.thread_id=$4 AND l.source=$5
 AND l.automation_run_id IS NOT DISTINCT FROM $6::uuid AND l.changeset_id IS NOT DISTINCT FROM $7::uuid
 AND l.agent_type=$8 AND l.max_passes=$9 AND l.fix_mode=$10 AND l.started_by_user_id IS NOT DISTINCT FROM $11::uuid
 AND l.loop_start_checkpoint_key IS NOT DISTINCT FROM $12::text AND l.startup_send_version=1
 AND l.startup_send_key=$13 AND l.startup_send_digest=$14 AND l.startup_send_thread_id=$4 AND l.startup_send_pass_id=$16
 AND p.pass_index=1 AND l.startup_send_input=$15::jsonb)`, orgID, loop.SessionID, loop.ID, loop.ThreadID, loop.Source,
		loop.AutomationRunID, loop.ChangesetID, loop.AgentType, loop.MaxPasses, loop.FixMode, loop.StartedByUserID, loop.LoopStartCheckpointKey,
		"rl-startup:v1:"+loop.ID.String(), i.prepared.StartupDigest, i.prepared.Input, i.prepared.PassID).Scan(&matches)
	if err != nil {
		return err
	}
	if !matches {
		return ErrReviewLoopStartIdentity
	}
	return nil
}

// AdoptCreatedReviewLoopInTx verifies only insert-time identity. Mutable loop
// evidence, status and progress are returned intact rather than compared to P0.
func AdoptCreatedReviewLoopInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, i ReviewLoopStartIntent, loop models.SessionReviewLoop) (models.SessionReviewLoop, error) {
	if i.orgID != orgID || i.state != "created" {
		return models.SessionReviewLoop{}, ErrReviewLoopStartIdentity
	}
	if err := verifyReviewLoopStartIdentity(ctx, orgID, tx, i, loop); err != nil {
		return models.SessionReviewLoop{}, err
	}
	return NewSessionReviewLoopStore(tx).GetLoopByID(ctx, orgID, i.prepared.Scope.ID)
}

// ReadReviewLoopCallerStartInTx reads only durable identity before any fresh
// policy/head/snapshot preflight. Resolved terminal history may yield a fresh
// generation; prepared, unresolved and running originals must be recovered.
func ReadReviewLoopCallerStartInTx(ctx context.Context, orgID, sessionID uuid.UUID, tx pgx.Tx, source models.ReviewLoopSource, callerID uuid.UUID) (*uuid.UUID, error) {
	if ctx == nil || nilLifecycleDependency(tx) || orgID == uuid.Nil || sessionID == uuid.Nil || callerID == uuid.Nil {
		return nil, ErrReviewLoopStartIdentity
	}
	var startID *uuid.UUID
	var err error
	switch source {
	case models.ReviewLoopSourcePublication:
		err = tx.QueryRow(ctx, `SELECT review_start_id FROM session_publications WHERE org_id=$1 AND session_id=$2 AND changeset_id=$3`, orgID, sessionID, callerID).Scan(&startID)
	case models.ReviewLoopSourceAutomation:
		err = tx.QueryRow(ctx, `SELECT review_start_id FROM automation_runs a WHERE a.org_id=$1 AND a.id=$3 AND
   (a.session_id=$2 OR EXISTS(SELECT 1 FROM session_automation_links l WHERE l.org_id=a.org_id AND l.automation_run_id=a.id AND l.session_id=$2))`, orgID, sessionID, callerID).Scan(&startID)
	default:
		return nil, ErrReviewLoopStartIdentity
	}
	if err != nil || startID == nil {
		return nil, err
	}
	var recover bool
	err = tx.QueryRow(ctx, `SELECT caller143_start_unresolved($1,$3) OR EXISTS(SELECT 1 FROM session_review_loops WHERE org_id=$1 AND session_id=$2 AND id=$3 AND status='running')`, orgID, sessionID, startID).Scan(&recover)
	if err != nil || !recover {
		return nil, err
	}
	return startID, nil
}
