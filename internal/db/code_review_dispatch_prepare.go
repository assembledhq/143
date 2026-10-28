package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const codeReviewDispatchPreparationMaxBytes = 8 << 20

var (
	ErrCodeReviewDispatchPreparationInvalid    = errors.New("invalid reviewer dispatch preparation")
	ErrCodeReviewDispatchPreparationDisabled   = errors.New("reviewer dispatch preparation is disabled")
	ErrCodeReviewDispatchPreparationIneligible = errors.New("reviewer dispatch preparation owner is ineligible")
	ErrCodeReviewDispatchPreparationTooLarge   = errors.New("reviewer dispatch preparation exceeds storage limit")
	ErrCodeReviewDispatchPreparationStorage    = errors.New("reviewer dispatch preparation storage failed")
)

type codeReviewDispatchPreparationError struct{ cause error }

func (*codeReviewDispatchPreparationError) Error() string {
	return ErrCodeReviewDispatchPreparationStorage.Error()
}
func (e *codeReviewDispatchPreparationError) Unwrap() error { return e.cause }
func (*codeReviewDispatchPreparationError) Is(target error) bool {
	return target == ErrCodeReviewDispatchPreparationStorage
}

// CodeReviewDispatchPreparation is private controller persistence input, not a
// public API model or authorization descriptor. The caller supplies its original
// codec-validated command/digest, preminted result, actual lane and frozen wake.
// This helper only checks storage/scope; it does not render or recompute a codec.
type CodeReviewDispatchPreparation struct {
	ResultID, SessionID, MetadataID, ThreadID uuid.UUID       `json:"-"`
	AgentProvider                             string          `json:"-"`
	AgentModel, RawOutput                     *string         `json:"-"`
	StructuredResult                          json.RawMessage `json:"-"`
	Version                                   int16           `json:"-"`
	Key, Digest, ReviewOutputKey              string          `json:"-"`
	Input, Wake                               json.RawMessage `json:"-"`
}

// PreparedCodeReviewDispatch is provisional until the supplied transaction's
// known successful commit. A zero WakeJobID is an ordinary dedupe no-op, not
// acceptance, existing-consumer linkage, or permission to send or notify.
type PreparedCodeReviewDispatch struct {
	result    models.CodeReviewAgentResult
	wakeJobID uuid.UUID
}

func (p PreparedCodeReviewDispatch) Result() models.CodeReviewAgentResult {
	r := p.result
	r.StructuredResult = bytes.Clone(r.StructuredResult)
	if r.AgentModel != nil {
		v := *r.AgentModel
		r.AgentModel = &v
	}
	if r.RawOutput != nil {
		v := *r.RawOutput
		r.RawOutput = &v
	}
	return r
}
func (p PreparedCodeReviewDispatch) WakeJobID() uuid.UUID { return p.wakeJobID }
func (p PreparedCodeReviewDispatch) WakeInserted() bool   { return p.wakeJobID != uuid.Nil }

func codeReviewDispatchPreparationObject(raw []byte) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 || len(raw) > codeReviewDispatchPreparationMaxBytes || !utf8.Valid(raw) || !json.Valid(raw) {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if validateLifecycleJobJSONKeys(decoder) != nil {
		return nil, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, false
	}
	return object, true
}

func codeReviewDispatchPreparationString(fields map[string]json.RawMessage, key, expected string) bool {
	var actual string
	return json.Unmarshal(fields[key], &actual) == nil && actual == expected
}

func (in CodeReviewDispatchPreparation) validate(orgID uuid.UUID) error {
	if orgID == uuid.Nil || in.ResultID == uuid.Nil || in.SessionID == uuid.Nil || in.MetadataID == uuid.Nil || in.ThreadID == uuid.Nil ||
		in.Version != CallerSendStampVersion || in.AgentProvider == "" || models.AgentType(in.AgentProvider).Validate() != nil ||
		in.ReviewOutputKey == "" || !utf8.ValidString(in.ReviewOutputKey) || strings.ContainsRune(in.ReviewOutputKey, 0) {
		return ErrCodeReviewDispatchPreparationInvalid
	}
	key, err := CallerSendStampKey(CallerSendStampCodeReviewDispatch, in.ResultID)
	if err != nil || key != in.Key {
		return ErrCodeReviewDispatchPreparationInvalid
	}
	if _, err := ParseLifecycleDigest(in.Digest); err != nil {
		return ErrCodeReviewDispatchPreparationInvalid
	}
	if len(in.Input) > codeReviewDispatchPreparationMaxBytes || len(in.Wake) > codeReviewDispatchPreparationMaxBytes {
		return ErrCodeReviewDispatchPreparationTooLarge
	}
	command, ok := codeReviewDispatchPreparationObject(in.Input)
	if !ok || string(command["version"]) != "1" ||
		!codeReviewDispatchPreparationString(command, "purpose", string(CallerSendStampCodeReviewDispatch)) ||
		!codeReviewDispatchPreparationString(command, "org_id", orgID.String()) ||
		!codeReviewDispatchPreparationString(command, "session_id", in.SessionID.String()) ||
		!codeReviewDispatchPreparationString(command, "thread_id", in.ThreadID.String()) ||
		!codeReviewDispatchPreparationString(command, "client_message_id", in.Key) {
		return ErrCodeReviewDispatchPreparationInvalid
	}
	wake, ok := codeReviewDispatchPreparationObject(in.Wake)
	if !ok || len(wake) != 4 || !codeReviewDispatchPreparationString(wake, "job_type", "run_code_review") ||
		!codeReviewDispatchPreparationString(wake, "queue", "agent") ||
		!codeReviewDispatchPreparationString(wake, "dedupe_key", "code_review:"+in.ReviewOutputKey) {
		return ErrCodeReviewDispatchPreparationInvalid
	}
	payload, ok := codeReviewDispatchPreparationObject(wake["payload"])
	if !ok || !codeReviewDispatchPreparationString(payload, "org_id", orgID.String()) ||
		!codeReviewDispatchPreparationString(payload, "session_id", in.SessionID.String()) ||
		!codeReviewDispatchPreparationString(payload, "metadata_id", in.MetadataID.String()) ||
		!codeReviewDispatchPreparationString(payload, "review_output_key", in.ReviewOutputKey) {
		return ErrCodeReviewDispatchPreparationInvalid
	}
	if len(in.StructuredResult) != 0 && !json.Valid(in.StructuredResult) {
		return ErrCodeReviewDispatchPreparationInvalid
	}
	return nil
}

// Consume this exact metadata SHARE prelock before the later INSERT statement
// takes its fresh READ COMMITTED snapshot. An in-statement row-lock wait alone
// cannot refresh that statement's assessment/receipt snapshot after retirement.
const codeReviewDispatchPreparationMetadataPrelockQuery = `SELECT m.id FROM code_review_session_metadata m
 WHERE m.org_id=$1 AND m.session_id=$2 AND m.id=$3
 AND m.status IN ('queued','running') AND NOT m.stale AND m.review_output_key=$4
 FOR SHARE OF m`

// Metadata is already prelocked before the enabled-control row, then the fresh
// result is inserted. A miss cannot discover another newly eligible owner in
// this later statement: $16 carries only whether that exact prelock succeeded.
// Cancellation/completion must serialize at that same metadata owner.
// The bounded Go observation is usable here only if the original-key candidate
// still has its observed identity/version, or its observed absence persists.
// A changed or newly appeared assessment prevents both atomic insertions.
// The lane EXISTS is scope/provider/model correlation, not a new lane lock or
// admission policy. Actual PostgreSQL JSONB presentation is measured in this
// statement before either data-modifying CTE can run. Never wrap concurrent DDL
// or call a service here. The fixed wake options match the existing controller.
const codeReviewDispatchPreparationQuery = `WITH document AS MATERIALIZED (
 SELECT $10::jsonb AS command,$11::jsonb AS wake
), owner AS MATERIALIZED (
 SELECT m.id FROM code_review_session_metadata m
 WHERE $16 AND m.org_id=$1 AND m.session_id=$2 AND m.id=$3
 AND m.status IN ('queued','running') AND NOT m.stale AND m.review_output_key=$12
 AND NOT $20 AND (
 ($17 AND EXISTS (SELECT 1 FROM code_review_revision_assessments a
 WHERE a.org_id=m.org_id AND a.publication_key=m.review_output_key AND a.id=$18 AND a.xmin::text=$19))
 OR (NOT $17 AND NOT EXISTS (SELECT 1 FROM code_review_revision_assessments a
 WHERE a.org_id=m.org_id AND a.publication_key=m.review_output_key)))
 FOR SHARE OF m
), enabled AS MATERIALIZED (
 SELECT c.singleton FROM caller_send_intent_control c CROSS JOIN owner
 WHERE c.singleton AND c.schema_ready AND c.dispatch_status_ready AND c.intents_enabled FOR SHARE OF c
), bounded AS MATERIALIZED (
 SELECT command,wake,octet_length(command::text)<=$13 AND octet_length(wake::text)<=$13 AS fits
 FROM document
), inserted AS (
 INSERT INTO code_review_agent_results (
 id,org_id,session_id,agent_provider,agent_model,role,status,raw_output,structured_result,
 dispatch_version,dispatch_key,dispatch_input,dispatch_digest,dispatch_thread_id,dispatch_state,
 dispatch_attempt_id,dispatch_attempt_seq,dispatch_attempted_at,dispatch_due_at,dispatch_observe_deadline,
 dispatch_attention_at,dispatch_observed_at,dispatch_message_id,dispatch_resolution,dispatch_detail,
 dispatch_writer_seq,dispatch_updated_at,dispatch_wake
 ) SELECT $4,$1,$2,$6,$7,'reviewer','queued',$14,$15::jsonb,
 1,$8,command,$9,$5,'pending',NULL,0,NULL,statement_timestamp(),NULL,
 NULL,NULL,NULL,NULL,NULL,0,statement_timestamp(),wake
 FROM bounded CROSS JOIN enabled WHERE fits AND EXISTS (
 SELECT 1 FROM session_threads t WHERE t.org_id=$1 AND t.session_id=$2 AND t.id=$5
 AND t.agent_type=$6 AND t.model_override IS NOT DISTINCT FROM $7::text
 ) RETURNING ` + codeReviewAgentResultColumns + `
), inserted_wake AS (
 INSERT INTO jobs(org_id,queue,job_type,payload,priority,dedupe_key,max_attempts)
 SELECT $1,'agent','run_code_review',bounded.wake->'payload',5,bounded.wake->>'dedupe_key',8
 FROM inserted CROSS JOIN bounded ON CONFLICT DO NOTHING RETURNING id
)
SELECT EXISTS(SELECT 1 FROM owner),EXISTS(SELECT 1 FROM enabled),(SELECT fits FROM bounded),
 (SELECT row_to_json(inserted) FROM inserted),COALESCE((SELECT id FROM inserted_wake),'00000000-0000-0000-0000-000000000000'::uuid)`

// PrepareCodeReviewDispatchInTx inserts one fresh reviewer intent and its
// original controller wake in one atomic statement after an exact metadata
// prelock. It never upgrades a legacy row, chooses modes/fallback,
// starts/settles a transaction, notifies, or sends.
// Any error requires the outer owner to discard staged results and roll back;
// only that owner can classify its final settlement. Known size/eligibility
// refusals write nothing, including when the compact input fits but JSONB does
// not. The caller must not publish the preparation carrier or private JSON.
func PrepareCodeReviewDispatchInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, in CodeReviewDispatchPreparation) (PreparedCodeReviewDispatch, error) {
	if ctx == nil || nilLifecycleDependency(tx) {
		return PreparedCodeReviewDispatch{}, ErrCodeReviewDispatchPreparationInvalid
	}
	if err := in.validate(orgID); err != nil {
		return PreparedCodeReviewDispatch{}, err
	}
	if err := ctx.Err(); err != nil {
		return PreparedCodeReviewDispatch{}, &codeReviewDispatchPreparationError{cause: err}
	}
	// CollectOneRow consumes/closes this finite one-row statement before the
	// fresh INSERT snapshot below, including on an empty result or scan error.
	// A known miss preserves fits/owner/enabled refusal precedence, but cannot
	// authorize discovery of a later owner.
	rows, err := tx.Query(ctx, codeReviewDispatchPreparationMetadataPrelockQuery, orgID, in.SessionID, in.MetadataID, in.ReviewOutputKey)
	if err != nil {
		return PreparedCodeReviewDispatch{}, &codeReviewDispatchPreparationError{cause: err}
	}
	lockedMetadata, err := pgx.CollectOneRow(rows, pgx.RowTo[uuid.UUID])
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return PreparedCodeReviewDispatch{}, &codeReviewDispatchPreparationError{cause: err}
	}
	prelocked := err == nil
	if prelocked && lockedMetadata != in.MetadataID {
		return PreparedCodeReviewDispatch{}, ErrCodeReviewDispatchPreparationStorage
	}
	if err := ctx.Err(); err != nil {
		return PreparedCodeReviewDispatch{}, &codeReviewDispatchPreparationError{cause: err}
	}
	var observation codeReviewConfirmedCompletionObservation
	if prelocked {
		observation, err = observeConfirmedFullAssessmentCompletionPending(ctx, tx, orgID, in.MetadataID)
		if err != nil {
			return PreparedCodeReviewDispatch{}, &codeReviewDispatchPreparationError{cause: err}
		}
	}
	var owner, enabled, fits bool
	var raw json.RawMessage
	var jobID uuid.UUID
	err = tx.QueryRow(ctx, codeReviewDispatchPreparationQuery, orgID, in.SessionID, in.MetadataID, in.ResultID, in.ThreadID,
		in.AgentProvider, in.AgentModel, in.Key, in.Digest, []byte(in.Input), []byte(in.Wake), in.ReviewOutputKey,
		codeReviewDispatchPreparationMaxBytes, in.RawOutput, []byte(in.StructuredResult), prelocked,
		observation.found, observation.assessmentID, observation.version, observation.pending).Scan(&owner, &enabled, &fits, &raw, &jobID)
	if err != nil {
		return PreparedCodeReviewDispatch{}, &codeReviewDispatchPreparationError{cause: err}
	}
	if err := ctx.Err(); err != nil {
		return PreparedCodeReviewDispatch{}, &codeReviewDispatchPreparationError{cause: err}
	}
	if !fits {
		return PreparedCodeReviewDispatch{}, ErrCodeReviewDispatchPreparationTooLarge
	}
	if !prelocked || !owner {
		return PreparedCodeReviewDispatch{}, ErrCodeReviewDispatchPreparationIneligible
	}
	if !enabled {
		return PreparedCodeReviewDispatch{}, ErrCodeReviewDispatchPreparationDisabled
	}
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return PreparedCodeReviewDispatch{}, ErrCodeReviewDispatchPreparationIneligible
	}
	var result models.CodeReviewAgentResult
	if json.Unmarshal(raw, &result) != nil || result.ID != in.ResultID || result.OrgID != orgID || result.SessionID != in.SessionID ||
		result.AgentProvider != in.AgentProvider || result.Role != models.CodeReviewAgentRoleReviewer || result.Status != models.CodeReviewAgentResultStatusQueued || result.CreatedAt.IsZero() {
		return PreparedCodeReviewDispatch{}, ErrCodeReviewDispatchPreparationStorage
	}
	return PreparedCodeReviewDispatch{result: result, wakeJobID: jobID}, nil
}
