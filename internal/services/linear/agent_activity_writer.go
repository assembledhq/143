package linear

import (
	"context"
	"errors"
	"fmt"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// AgentActivityWriter emits AgentActivities to Linear with at-most-once
// semantics. The writer pairs the (agent_session_row_id, idem_key) UNIQUE
// constraint in linear_agent_activity_log with a two-phase reserve/complete
// flow:
//
//	Reserve  → INSERT the dedupe row; if UNIQUE collision, short-circuit
//	emit     → call Linear's agentActivityCreate
//	Complete → record the linear_activity_id on the reserved row
//
// On Linear failure, the reservation row stays present without a
// linear_activity_id. The next attempt sees the UNIQUE collision and
// short-circuits. This is the right tradeoff for milestone activities —
// duplicate "PR merged" thoughts are far worse than a missing one, and
// milestones already carry a durable echo in the rolling comment +
// attachment.
//
// For elicitation activities (where missing emission is more visible), the
// writer exposes EmitOrDiscard which deletes the reservation when the emit
// fails so a later replay actually re-emits.
type AgentActivityWriter struct {
	client     Client
	activities *db.LinearAgentActivityLogStore
	metrics    AgentActivityMetricsRecorder
	logger     zerolog.Logger
}

// AgentActivityMetricsRecorder is the narrow surface the writer needs to
// record per-emit observability. The metrics package's
// LinearAgentMetrics.RecordActivityEmitted method satisfies this; tests
// can pass a stub. nil means "no metrics", honored by the writer.
type AgentActivityMetricsRecorder interface {
	RecordActivityEmitted(ctx context.Context, activityType string, skipped bool)
}

// NewAgentActivityWriter wires the writer. Pass a fully-resolved Client
// (built with the org's actor=app token); the writer does not handle
// token resolution itself — that lives one layer up so the same writer
// instance can serve concurrent emits across orgs by varying the Client.
//
// metrics may be nil for tests / boot stages that haven't constructed
// the metrics package; emits silently skip the record call in that case.
func NewAgentActivityWriter(client Client, activities *db.LinearAgentActivityLogStore, metrics AgentActivityMetricsRecorder, logger zerolog.Logger) *AgentActivityWriter {
	return &AgentActivityWriter{
		client:     client,
		activities: activities,
		metrics:    metrics,
		logger:     logger.With().Str("component", "linear_agent_activity_writer").Logger(),
	}
}

// EmitInput packages everything the writer needs for one activity. The
// AgentSessionRowID and AgentSessionID come from the linear_agent_sessions
// row resolved by the caller — the writer doesn't redo that lookup so
// the hot path stays a single DB write + one Linear call + one DB write.
type EmitInput struct {
	OrgID             uuid.UUID
	AgentSessionRowID uuid.UUID
	AgentSessionID    string
	Activity          AgentMilestoneActivity
}

// EmitResult communicates whether the writer actually emitted to Linear.
// Skipped=true means the dedupe slot was already taken — no error, just
// "another caller got there first".
type EmitResult struct {
	Skipped          bool
	LinearActivityID string
}

// Emit reserves the slot, calls Linear, and persists the linear_activity_id
// on success. On failure the slot stays reserved (see writer doc).
//
// The activity's Ephemeral flag is normalized against the type — Linear
// only honors ephemeral on thought/action and the writer enforces this so
// we don't get a runtime GraphQL rejection.
func (w *AgentActivityWriter) Emit(ctx context.Context, in EmitInput) (EmitResult, error) {
	if w == nil || w.client == nil || w.activities == nil {
		return EmitResult{}, errors.New("agent activity writer not configured")
	}
	if in.AgentSessionID == "" {
		return EmitResult{}, errors.New("agent_session_id is required")
	}
	if in.Activity.IdemKey == "" {
		return EmitResult{}, errors.New("activity idem_key is required")
	}
	if err := in.Activity.Type.Validate(); err != nil {
		return EmitResult{}, fmt.Errorf("invalid activity type: %w", err)
	}
	// Normalize ephemeral: Linear rejects ephemeral=true on response/error/
	// elicitation. Drop it silently here so callers don't have to repeat
	// the type-aware logic.
	ephemeral := in.Activity.Ephemeral && in.Activity.Type.CanBeEphemeral()

	res, err := w.activities.Reserve(ctx, in.OrgID, in.AgentSessionRowID, in.Activity.IdemKey, in.Activity.Type)
	if err != nil {
		return EmitResult{}, fmt.Errorf("reserve activity slot: %w", err)
	}
	if !res.Reserved {
		// Another caller already won this idem_key. Either the prior emit
		// succeeded (the linear_activity_id is set on the row) or it died
		// mid-flight (row is present without an id). Either way, the safe
		// answer is to skip — replays won't re-emit and Linear won't see
		// duplicates. Operators investigating "why did my activity not
		// appear" can check linear_activity_id IS NULL on the row to
		// distinguish the two cases.
		w.recordEmit(ctx, in.Activity.Type, true /*skipped*/)
		return EmitResult{Skipped: true}, nil
	}

	apiResult, err := w.client.AgentActivityCreate(ctx, AgentActivityInput{
		AgentSessionID: in.AgentSessionID,
		Type:           string(in.Activity.Type),
		Body:           in.Activity.Body,
		Action:         in.Activity.Action,
		Ephemeral:      ephemeral,
	})
	if err != nil {
		// Leave the row present-but-incomplete; subsequent replays
		// short-circuit on UNIQUE collision rather than re-firing a
		// possibly-already-delivered request to Linear. The operator
		// debug surface flags rows where linear_activity_id IS NULL as
		// "emitted but unconfirmed" so a human can decide whether to
		// retry by hand.
		w.logger.Warn().Err(err).
			Str("agent_session_id", in.AgentSessionID).
			Str("idem_key", in.Activity.IdemKey).
			Msg("agent activity create failed; reservation kept to prevent duplicate")
		return EmitResult{}, fmt.Errorf("agent activity create: %w", err)
	}

	w.recordEmit(ctx, in.Activity.Type, false /*skipped*/)
	if completeErr := w.activities.Complete(ctx, in.OrgID, res.RowID, apiResult.ActivityID); completeErr != nil {
		// Linear got the activity but we failed to record the id. Log
		// loudly — replays will short-circuit (correct), but the missing
		// id breaks the operator debug join. Not worth failing the
		// whole emit; the milestone payload still landed in Linear.
		w.logger.Error().Err(completeErr).
			Str("agent_session_id", in.AgentSessionID).
			Str("linear_activity_id", apiResult.ActivityID).
			Msg("agent activity emit succeeded but Complete failed; operator debug surface will show the row as unconfirmed")
	}

	if in.Activity.PinSessionState != "" {
		if err := w.client.AgentSessionUpdate(ctx, AgentSessionUpdateInput{
			AgentSessionID: in.AgentSessionID,
			State:          in.Activity.PinSessionState,
		}); err != nil {
			// AgentSessionUpdate is best-effort. Linear will eventually
			// derive the correct state from the activity stream; the
			// explicit pin just shaves the latency in the UI. Don't fail
			// the whole emit on this.
			w.logger.Warn().Err(err).
				Str("agent_session_id", in.AgentSessionID).
				Str("state", in.Activity.PinSessionState).
				Msg("agent session state pin failed; Linear will derive from activity stream")
		}
	}

	return EmitResult{
		Skipped:          false,
		LinearActivityID: apiResult.ActivityID,
	}, nil
}

// EmitOrDiscard is the strict-semantics variant for activities that must
// re-fire on transient failures (elicitations especially — a missing one
// leaves the AgentSession stuck in awaitingInput forever). On Linear
// failure, deletes the reservation so the next replay actually retries.
//
// Don't use for milestone activities; the whole point of Emit's keep-the-
// reservation behavior is to prevent duplicate "PR merged" notifications
// in Linear.
func (w *AgentActivityWriter) EmitOrDiscard(ctx context.Context, in EmitInput) (EmitResult, error) {
	res, err := w.Emit(ctx, in)
	if err == nil {
		return res, nil
	}
	// Lookup the reservation row id — Reserve returned it on the Emit
	// path but we don't have direct access to it here. Discard tolerates
	// "no such row" so a re-Reserve race is safe.
	// We don't have the row id here, but Discard scoped by orgID +
	// idem_key + agent_session_row_id is what we want. To keep the API
	// minimal, delete by the agent_session_row_id + idem_key combo —
	// safe because UNIQUE on the same pair guarantees at most one row.
	if discardErr := w.discardReservation(ctx, in.OrgID, in.AgentSessionRowID, in.Activity.IdemKey); discardErr != nil {
		w.logger.Warn().Err(discardErr).
			Str("agent_session_id", in.AgentSessionID).
			Str("idem_key", in.Activity.IdemKey).
			Msg("failed to discard agent activity reservation after emit failure; replays will short-circuit on the dead row")
	}
	return res, err
}

// recordEmit is the nil-safe wrapper around the metrics recorder.
// Centralizes the nil check so callers don't have to repeat it.
func (w *AgentActivityWriter) recordEmit(ctx context.Context, activityType models.LinearAgentActivityType, skipped bool) {
	if w.metrics == nil {
		return
	}
	w.metrics.RecordActivityEmitted(ctx, string(activityType), skipped)
}

// discardReservation deletes the in-flight reservation row for an
// (agent_session, idem_key) pair when its linear_activity_id is still
// NULL. Race-safe: a concurrent successful emit will have completed first,
// at which point the row's id is non-NULL and the delete is a no-op.
func (w *AgentActivityWriter) discardReservation(ctx context.Context, orgID, agentSessionRowID uuid.UUID, idemKey string) error {
	logs, err := w.activities.ListForAgentSession(ctx, orgID, agentSessionRowID)
	if err != nil {
		return err
	}
	for _, log := range logs {
		if log.IdemKey == idemKey && log.LinearActivityID == "" {
			return w.activities.Discard(ctx, orgID, log.ID)
		}
	}
	return nil
}
