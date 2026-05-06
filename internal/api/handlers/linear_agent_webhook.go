package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/linear"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// LinearAgentEventType is the value carried in `Linear-Event` header (or
// inferred from `AppUserNotification` payloads in the legacy path).
type LinearAgentEventType string

const (
	// LinearAgentEventAgentSession is the modern AgentSessionEvent envelope.
	// Carries `payload.agentSession`, `action: created|prompted` and the
	// structured `promptContext` blob with issue + comments + prior
	// activities. Preferred over AppUserNotification.
	LinearAgentEventAgentSession LinearAgentEventType = "AgentSessionEvent"

	// LinearAgentEventAppUserNotification is the legacy envelope. Phase 4
	// adds support for it as a fallback for orgs whose webhook config
	// isn't subscribed to AgentSessionEvent yet. Phase 2 ignores it.
	LinearAgentEventAppUserNotification LinearAgentEventType = "AppUserNotification"
)

// linearAgentEventEnvelope is the minimal subset of the AgentSessionEvent
// payload the dispatcher needs to (a) decide whether to act, (b) idempotency-
// upsert the linear_agent_sessions row, and (c) build the worker job
// payload.
//
// We deliberately don't try to consume the full Linear payload here — the
// worker fetches the live issue from Linear when it runs (see Phase 2.7),
// which gives it the freshest issue body and label set. Caching the inbound
// payload would just risk going stale between dispatch and worker
// execution.
type linearAgentEventEnvelope struct {
	Type    string `json:"type"`
	Action  string `json:"action"`
	Payload struct {
		AgentSession struct {
			ID        string `json:"id"`
			IssueID   string `json:"issueId"`
			CommentID string `json:"commentId,omitempty"`
			Issue     struct {
				ID         string `json:"id"`
				Identifier string `json:"identifier,omitempty"`
				TeamID     string `json:"teamId,omitempty"`
				ProjectID  string `json:"projectId,omitempty"`
			} `json:"issue,omitempty"`
			Creator struct {
				ID string `json:"id,omitempty"`
			} `json:"creator,omitempty"`
		} `json:"agentSession"`
	} `json:"payload"`
	// AppUserID is the id of our @143 agent user as Linear sees it. We
	// use it to filter out webhooks delivered to other apps that share a
	// webhook URL (rare but possible in shared-tenant setups). Empty in
	// AgentSessionEvent payloads — it lives on Linear's side.
	AppUserID string `json:"appUserId,omitempty"`
}

// linearAgentEventAction names a value of envelope.action. The worker
// dispatches on the same vocabulary, so changes here must mirror in
// internal/worker/handlers_linear_agent.go.
type linearAgentEventAction string

const (
	linearAgentActionCreated  linearAgentEventAction = "created"
	linearAgentActionPrompted linearAgentEventAction = "prompted"
)

// linearAgentJobEnqueuer is the narrow surface the dispatcher needs from
// the JobStore. Pulled into an interface so tests can verify the dedupe key
// and payload shape without standing up Postgres.
type linearAgentJobEnqueuer interface {
	Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
}

// linearAgentBootstrapEmitter is the narrow surface the dispatcher needs
// from the AgentActivityWriter. Pulled into an interface for the same
// reason — tests can pin the bootstrap-thought behavior without exercising
// the full Linear GraphQL stack.
type linearAgentBootstrapEmitter interface {
	Emit(ctx context.Context, in linear.EmitInput) (linear.EmitResult, error)
}

// LinearAgentDispatcher is the handoff point between the inbound webhook
// and the worker queue. Every method on this type is invoked from inside
// HandleLinear *after* the HMAC verify has already passed, so it can
// assume the request is trustworthy.
//
// The dispatcher is split out from the ingestion handler because its
// failure modes are agent-specific (e.g. "feature flag off, return 200
// ignored" vs ingestion's "issue upsert succeeded, return 200 processed")
// and grouping the logic keeps HandleLinear's branch a one-liner.
type LinearAgentDispatcher struct {
	logger         zerolog.Logger
	agentSessions  *db.LinearAgentSessionStore
	jobs           linearAgentJobEnqueuer
	emitter        linearAgentBootstrapEmitter
	activities     *db.LinearAgentActivityLogStore
	settingsLoader func(ctx context.Context, orgID uuid.UUID) (models.LinearAgentSettings, error)
	clientForOrg   func(ctx context.Context, orgID uuid.UUID) (linear.Client, error)
	// metrics records dispatch-side observability. Optional — nil falls
	// back to no-op counters via the nil-safe RecordX helpers, so a boot
	// stage that hasn't constructed the metrics package can still wire
	// the dispatcher.
	metrics *metrics.LinearAgentMetrics
	// featureEnabled gates the entire path — when false, every Dispatch
	// returns immediately without doing any work. Process-wide kill switch
	// lifted from cfg.LinearAgentEnabled.
	featureEnabled bool
}

// LinearAgentDispatcherConfig packages the wiring parameters. The
// dispatcher is intentionally constructed via a config struct rather than
// a long positional argument list so future fields don't ripple through
// every test.
type LinearAgentDispatcherConfig struct {
	Logger         zerolog.Logger
	AgentSessions  *db.LinearAgentSessionStore
	Activities     *db.LinearAgentActivityLogStore
	Jobs           linearAgentJobEnqueuer
	SettingsLoader func(ctx context.Context, orgID uuid.UUID) (models.LinearAgentSettings, error)
	ClientForOrg   func(ctx context.Context, orgID uuid.UUID) (linear.Client, error)
	Metrics        *metrics.LinearAgentMetrics
	FeatureEnabled bool
}

// NewLinearAgentDispatcher constructs the dispatcher. Returns nil + nil
// when the agent stores aren't wired (phase 1 plumbing not yet rolled out)
// — callers handle nil gracefully.
func NewLinearAgentDispatcher(cfg LinearAgentDispatcherConfig) *LinearAgentDispatcher {
	if cfg.AgentSessions == nil || cfg.Jobs == nil {
		return nil
	}
	return &LinearAgentDispatcher{
		logger:         cfg.Logger.With().Str("component", "linear_agent_dispatcher").Logger(),
		agentSessions:  cfg.AgentSessions,
		activities:     cfg.Activities,
		jobs:           cfg.Jobs,
		settingsLoader: cfg.SettingsLoader,
		clientForOrg:   cfg.ClientForOrg,
		metrics:        cfg.Metrics,
		featureEnabled: cfg.FeatureEnabled,
	}
}

// DispatchResult is the post-Dispatch summary returned to the webhook
// handler. Status is one of:
//   - "agent_dispatched" — we recorded the row + enqueued + (best-effort)
//     emitted the bootstrap thought
//   - "feature_off"      — global kill switch / org opt-out / unknown event
//   - "ignored"          — recognized but non-actionable (e.g. action not
//     yet supported in this phase)
type DispatchResult struct {
	Status               string
	AgentSessionRowID    uuid.UUID
	JobID                uuid.UUID
	AgentSessionID       string
	BootstrapEmitSkipped bool
}

// Dispatch is the single entry point invoked by HandleLinear. body is the
// raw webhook body (signature verified upstream). integration is the
// active Linear integration row that owns this webhook URL.
//
// Dispatch is never expected to return an error — all failure modes resolve
// to a 200 OK with an explanatory status string in DispatchResult.Status.
// The 5s Linear SLA for ack is much tighter than 200 vs 4xx semantic
// fidelity; ack first, log loudly, work asynchronously.
func (d *LinearAgentDispatcher) Dispatch(ctx context.Context, integration *models.Integration, eventType LinearAgentEventType, body []byte) (result DispatchResult) {
	if d == nil {
		// Nil-receiver short-circuit. We intentionally return *before*
		// registering the deferred metrics record below — d.metrics
		// would deref nil. A "feature_off" outcome on this path only
		// happens when the dispatcher itself was never wired (boot-
		// time mis-configuration), which is rare enough that losing
		// the metric is acceptable; the operator already sees a
		// configuration warning at boot.
		return DispatchResult{Status: "feature_off"}
	}
	// Named return so the deferred metrics record sees the final outcome
	// regardless of which branch returned it. action is the parsed
	// envelope action; for outcomes recorded before we've parsed
	// (feature_off / unsupported event_type), the empty string is the
	// right cardinality-bounded label.
	var action linearAgentEventAction
	defer func() {
		d.metrics.RecordEvent(ctx, string(eventType), string(action), result.Status)
	}()
	if !d.featureEnabled {
		d.logger.Debug().
			Str("integration_id", integration.ID.String()).
			Msg("agent feature globally disabled; ignoring event")
		return DispatchResult{Status: "feature_off"}
	}
	if eventType != LinearAgentEventAgentSession {
		// AppUserNotification is the legacy envelope — we recognize it
		// for audit completeness but don't act on it. Modern Linear
		// installs subscribe to Agent session events which carry the
		// promptContext we need; a workspace whose webhook config still
		// points to AppUserNotification should be migrated.
		d.logger.Info().
			Str("integration_id", integration.ID.String()).
			Str("event_type", string(eventType)).
			Msg("agent dispatcher: AppUserNotification received; subscribe to Agent session events on the Linear OAuth app to enable inbound agent triggering")
		return DispatchResult{Status: "ignored"}
	}

	var env linearAgentEventEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		d.logger.Warn().Err(err).Msg("failed to parse linear agent event envelope")
		return DispatchResult{Status: "ignored"}
	}

	action = linearAgentEventAction(env.Action)
	if action != linearAgentActionCreated && action != linearAgentActionPrompted {
		// Linear may add new actions in the future. Both `created` and
		// `prompted` are handled below; anything else logs and skips.
		return DispatchResult{Status: "ignored"}
	}
	if env.Payload.AgentSession.ID == "" || env.Payload.AgentSession.IssueID == "" {
		d.logger.Warn().
			Str("integration_id", integration.ID.String()).
			Msg("agent event missing agentSession.id or issueId; ignoring")
		return DispatchResult{Status: "ignored"}
	}

	// Per-org opt-in. Loaded once here so the worker doesn't re-fetch.
	if d.settingsLoader != nil {
		settings, err := d.settingsLoader(ctx, integration.OrgID)
		if err != nil {
			d.logger.Warn().Err(err).
				Str("org_id", integration.OrgID.String()).
				Msg("failed to load agent settings; ignoring event")
			return DispatchResult{Status: "feature_off"}
		}
		if !settings.EffectiveEnabled() {
			d.logger.Debug().
				Str("org_id", integration.OrgID.String()).
				Msg("agent feature not enabled for org; ignoring event")
			return DispatchResult{Status: "feature_off"}
		}
	}

	var (
		row     *db.LinearAgentSession
		created bool
		err     error
	)
	if action == linearAgentActionCreated {
		// 1a. Idempotent upsert. Re-deliveries collide on UNIQUE
		// (org_id, linear_agent_session_id) and the row's session_id (if
		// any) is preserved so the worker can recover the prior 143
		// session.
		row, created, err = d.agentSessions.UpsertOnCreated(ctx, integration.OrgID, db.UpsertOnCreatedInput{
			OrgID:                 integration.OrgID,
			IntegrationID:         integration.ID,
			LinearAgentSessionID:  env.Payload.AgentSession.ID,
			LinearIssueID:         env.Payload.AgentSession.IssueID,
			LinearIssueIdentifier: env.Payload.AgentSession.Issue.Identifier,
			LinearAppUserID:       env.AppUserID,
			LinearCreatorUserID:   env.Payload.AgentSession.Creator.ID,
		})
		if err != nil {
			d.logger.Error().Err(err).
				Str("agent_session_id", env.Payload.AgentSession.ID).
				Msg("failed to upsert linear_agent_sessions; ignoring event")
			return DispatchResult{Status: "ignored"}
		}
	} else {
		// 1b. Prompted event: lookup-only. The corresponding `created`
		// already created the row + 143 session. If we don't have a
		// row, this `prompted` is racing the `created` (Linear can
		// deliver out of order under recovery). Return ignored;
		// Linear's webhook retry will re-deliver after `created`
		// lands.
		row, err = d.agentSessions.Lookup(ctx, integration.OrgID, env.Payload.AgentSession.ID)
		if err != nil {
			if errors.Is(err, db.ErrLinearAgentSessionNotFound) {
				d.logger.Warn().
					Str("agent_session_id", env.Payload.AgentSession.ID).
					Msg("prompted event arrived before created; ignoring (Linear retry will re-deliver)")
				return DispatchResult{Status: "ignored"}
			}
			d.logger.Error().Err(err).
				Str("agent_session_id", env.Payload.AgentSession.ID).
				Msg("failed to lookup linear_agent_sessions; ignoring prompted event")
			return DispatchResult{Status: "ignored"}
		}
	}

	result = DispatchResult{
		AgentSessionRowID: row.ID,
		AgentSessionID:    row.LinearAgentSessionID,
	}

	// 2. Best-effort bootstrap thought (created only). Skip on prompted
	// because the AgentSession is already alive and Linear's UI doesn't
	// need a "Reading…" thought for follow-ups.
	if action == linearAgentActionCreated && d.emitter != nil {
		bootstrap := linear.BootstrapActivity(env.Payload.AgentSession.Issue.Identifier)
		bootstrapStart := time.Now()
		emitRes, emitErr := d.emitter.Emit(ctx, linear.EmitInput{
			OrgID:             integration.OrgID,
			AgentSessionRowID: row.ID,
			AgentSessionID:    row.LinearAgentSessionID,
			Activity:          bootstrap,
		})
		// Latency includes the Reserve INSERT + the GraphQL emit. Tracked
		// here (rather than inside the writer) because the dispatcher
		// owns the 10s SLA contract and the latency budget is
		// dispatcher-anchored.
		d.metrics.RecordBootstrapLatency(ctx, float64(time.Since(bootstrapStart).Milliseconds()))
		d.metrics.RecordActivityEmitted(ctx, string(bootstrap.Type), emitRes.Skipped)
		if emitErr != nil {
			d.logger.Warn().Err(emitErr).
				Str("agent_session_id", row.LinearAgentSessionID).
				Msg("agent bootstrap activity emit failed; worker will retry on first run")
		}
		result.BootstrapEmitSkipped = emitRes.Skipped
	}

	// 3. Enqueue the worker job. Dedupe on (agent_session_id, action) so
	// re-deliveries collapse. For prompted events the dedupe also
	// includes the comment id (when present) so a different follow-up
	// comment doesn't collapse onto a previous prompted job.
	dedupeParts := []string{"linear_agent_event", row.LinearAgentSessionID, string(action)}
	if action == linearAgentActionPrompted && env.Payload.AgentSession.CommentID != "" {
		dedupeParts = append(dedupeParts, env.Payload.AgentSession.CommentID)
	}
	dedupe := strings.Join(dedupeParts, ":")
	jobPayload := map[string]any{
		"action":                  string(action),
		"org_id":                  integration.OrgID.String(),
		"integration_id":          integration.ID.String(),
		"agent_session_row_id":    row.ID.String(),
		"linear_agent_session_id": row.LinearAgentSessionID,
		"linear_issue_id":         env.Payload.AgentSession.IssueID,
		"linear_issue_team_id":    env.Payload.AgentSession.Issue.TeamID,
		"linear_issue_project_id": env.Payload.AgentSession.Issue.ProjectID,
		"linear_creator_user_id":  env.Payload.AgentSession.Creator.ID,
		"linear_comment_id":       env.Payload.AgentSession.CommentID,
	}
	jobID, err := d.jobs.Enqueue(ctx, integration.OrgID, "linear", "linear_agent_event", jobPayload, 5, &dedupe)
	if err != nil {
		d.logger.Error().Err(err).
			Str("agent_session_id", row.LinearAgentSessionID).
			Bool("created", created).
			Msg("failed to enqueue linear_agent_event; webhook delivery recorded but session creation will not happen")
		return result
	}
	result.JobID = jobID
	result.Status = "agent_dispatched"
	d.logger.Info().
		Str("agent_session_id", row.LinearAgentSessionID).
		Str("action", string(action)).
		Str("job_id", jobID.String()).
		Bool("created_row", created).
		Bool("bootstrap_emit_skipped", result.BootstrapEmitSkipped).
		Msg("linear agent event dispatched")
	return result
}

// SetBootstrapEmitter wires the agent activity writer post-construction.
// Separated from NewLinearAgentDispatcher because the writer needs a
// resolved Client (per-org token), and we don't want the dispatcher's
// constructor to require a per-org client factory at boot. Set is
// idempotent and goroutine-safe in the sense that callers should only
// invoke it during boot, before any webhook can land.
func (d *LinearAgentDispatcher) SetBootstrapEmitter(e linearAgentBootstrapEmitter) {
	if d == nil {
		return
	}
	d.emitter = e
}

// emitOpenedBootstrap is exposed to internal callers (worker recovery
// path) that want to ensure the bootstrap thought has been delivered even
// if the dispatcher's best-effort emit failed.
func (d *LinearAgentDispatcher) emitOpenedBootstrap(ctx context.Context, orgID, rowID uuid.UUID, agentSessionID, issueIdentifier string) error {
	if d == nil || d.emitter == nil {
		return errors.New("dispatcher emitter not configured")
	}
	_, err := d.emitter.Emit(ctx, linear.EmitInput{
		OrgID:             orgID,
		AgentSessionRowID: rowID,
		AgentSessionID:    agentSessionID,
		Activity:          linear.BootstrapActivity(issueIdentifier),
	})
	return err
}
