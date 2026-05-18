package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
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
		AgentActivity struct {
			Body string `json:"body,omitempty"`
		} `json:"agentActivity,omitempty"`
	} `json:"payload"`
	// AppUserID is the id of the Linear app user this webhook is
	// addressed to. Linear sets it on AppUserNotification deliveries and
	// recent AgentSessionEvent payloads; older AgentSessionEvent
	// deliveries omit it. When present, Dispatch compares it against the
	// AppUserID the OAuth callback persisted on the integration and
	// rejects mismatches (defense in depth against shared-tenant webhook
	// URL setups). Empty here means "Linear didn't tell us the target
	// user" — we don't filter on it in that case.
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

// bootstrapEmitTimeout caps the synchronous Linear roundtrip during
// dispatch. Picked well under the 5s ack SLA so a slow Linear can't
// blow our webhook timeout. The worker re-emits the same idempotent
// activity on its first run, so a clipped emit is recoverable.
const bootstrapEmitTimeout = 1500 * time.Millisecond

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
	logger           zerolog.Logger
	agentSessions    *db.LinearAgentSessionStore
	jobs             linearAgentJobEnqueuer
	emitter          linearAgentBootstrapEmitter
	activities       *db.LinearAgentActivityLogStore
	settingsLoader   func(ctx context.Context, orgID uuid.UUID) (models.LinearAgentSettings, error)
	clientForOrg     func(ctx context.Context, orgID uuid.UUID) (linear.Client, error)
	credentialLookup webhookSecretLookup
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
	Credentials    webhookSecretLookup
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
		logger:           cfg.Logger.With().Str("component", "linear_agent_dispatcher").Logger(),
		agentSessions:    cfg.AgentSessions,
		activities:       cfg.Activities,
		jobs:             cfg.Jobs,
		emitter:          newLinearAgentBootstrapWriter(cfg.ClientForOrg, cfg.Activities, cfg.Logger),
		settingsLoader:   cfg.SettingsLoader,
		clientForOrg:     cfg.ClientForOrg,
		credentialLookup: cfg.Credentials,
		metrics:          cfg.Metrics,
		featureEnabled:   cfg.FeatureEnabled,
	}
}

// DispatchResult is the post-Dispatch summary returned to the webhook
// handler. Status is one of:
//   - "agent_dispatched"                 — row + worker enqueue + bootstrap emit all succeeded
//   - "agent_dispatched_bootstrap_failed" — row + worker enqueue succeeded but the
//     dispatcher-side bootstrap emit failed. Distinct from the happy path
//     because it indicates a likely SLA breach (Linear's 10s first-activity
//     ceiling is anchored on this emit). The worker still picks up the
//     enqueued job, so user-visible state will recover, but the operator
//     should be alerted on this outcome.
//   - "feature_off"                       — global kill switch / org opt-out
//   - "ignored"                           — recognized but non-actionable (e.g. action not
//     yet supported in this phase)
//   - "enqueue_failed"                    — webhook delivery saved but worker enqueue
//     failed; Linear will retry the delivery
//   - "retryable_error"                   — transient failure (DB hiccup, schema drift
//     after HMAC verify, unknown action). Sets Err so the handler 500s and Linear
//     redelivers, giving us a chance to recover after a fix deploys.
type DispatchResult struct {
	Status               string
	AgentSessionRowID    uuid.UUID
	JobID                uuid.UUID
	AgentSessionID       string
	BootstrapEmitSkipped bool
	BootstrapEmitFailed  bool
	Err                  error
}

// Dispatch is the single entry point invoked by HandleLinear. body is the
// raw webhook body (signature verified upstream). integration is the
// active Linear integration row that owns this webhook URL.
//
// preParsed, when non-nil, lets a caller that already body-sniffed the
// envelope (e.g. to determine event_type) avoid re-parsing. nil is the
// normal case — Dispatch unmarshals body itself.
//
// Dispatch is never expected to return an error — all failure modes resolve
// to a 200 OK with an explanatory status string in DispatchResult.Status.
// The 5s Linear SLA for ack is much tighter than 200 vs 4xx semantic
// fidelity; ack first, log loudly, work asynchronously.
func (d *LinearAgentDispatcher) Dispatch(ctx context.Context, integration *models.Integration, eventType LinearAgentEventType, body []byte, preParsed *linearAgentEventEnvelope) (result DispatchResult) {
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
	if preParsed != nil {
		env = *preParsed
	} else if err := json.Unmarshal(body, &env); err != nil {
		// Post-HMAC-verify malformed body is loud: either Linear changed
		// the wire contract under us, or someone with the signing secret
		// is sending malformed payloads. Both warrant operator
		// attention, not a quiet "ignored". The signature already
		// authenticated the sender, so the body should be well-formed.
		// Surface as 500 so Linear redelivers — once we deploy a
		// forward-compat fix the retry will succeed.
		d.logger.Error().Err(err).
			Str("integration_id", integration.ID.String()).
			Int("body_bytes", len(body)).
			Msg("agent dispatcher: HMAC verified but body failed to parse; requesting Linear redelivery")
		return DispatchResult{Status: "retryable_error", Err: fmt.Errorf("parse agent event envelope: %w", err)}
	}

	action = linearAgentEventAction(env.Action)
	if action != linearAgentActionCreated && action != linearAgentActionPrompted {
		// Linear may add new actions in the future. Surface as 500 so
		// Linear redelivers — once we add support, the retry succeeds.
		// Silently 200-ack'ing here permanently drops every event of a
		// new action type until Linear's retry budget expires.
		d.logger.Warn().
			Str("integration_id", integration.ID.String()).
			Str("action", env.Action).
			Msg("agent dispatcher: unrecognized action; requesting Linear redelivery")
		return DispatchResult{Status: "retryable_error", Err: fmt.Errorf("unrecognized agent event action: %q", env.Action)}
	}
	if env.Payload.AgentSession.ID == "" || env.Payload.AgentSession.IssueID == "" {
		// Missing required IDs is a contract violation, not transient.
		// Don't retry — Linear sending the same malformed payload again
		// won't help. Operator must investigate.
		d.logger.Warn().
			Str("integration_id", integration.ID.String()).
			Msg("agent event missing agentSession.id or issueId; ignoring")
		return DispatchResult{Status: "ignored"}
	}

	// AppUserID filter (defense in depth). If Linear addressed this webhook
	// to a different app user that happens to share our webhook URL — rare
	// today because Linear's per-app webhook routes events for that app
	// only, but possible in shared-tenant or wildcard-subscription setups
	// — skip it. We compare the envelope's appUserId against the AppUserID
	// the OAuth callback persisted on the Linear credential; mismatch means
	// the webhook is not addressed to our agent. Empty on either side
	// means "can't filter, fall through" (legacy installs, slimmer
	// payloads). The check is intentionally before the agent-row upsert
	// so a stray event doesn't pollute the bridge table.
	if env.AppUserID != "" {
		expected, ok, err := d.expectedLinearAppUserID(ctx, integration)
		if err != nil {
			d.logger.Error().Err(err).
				Str("integration_id", integration.ID.String()).
				Str("org_id", integration.OrgID.String()).
				Msg("agent dispatcher: failed to load Linear app user filter; requesting Linear redelivery")
			return DispatchResult{Status: "retryable_error", Err: fmt.Errorf("load Linear app user filter: %w", err)}
		}
		if ok && expected != "" && expected != env.AppUserID {
			d.logger.Info().
				Str("integration_id", integration.ID.String()).
				Str("envelope_app_user_id", env.AppUserID).
				Msg("agent event addressed to a different app user; ignoring")
			return DispatchResult{Status: "ignored"}
		}
	}

	// Per-org opt-in. Loaded once here so the worker doesn't re-fetch.
	if d.settingsLoader != nil {
		settings, err := d.settingsLoader(ctx, integration.OrgID)
		if err != nil {
			// Transient DB hiccup. Surface as 500 so Linear retries — we
			// must not silently drop an event for an org that actually has
			// the feature enabled.
			d.logger.Error().Err(err).
				Str("org_id", integration.OrgID.String()).
				Msg("failed to load agent settings; requesting Linear redelivery")
			return DispatchResult{Status: "retryable_error", Err: fmt.Errorf("load agent settings: %w", err)}
		}
		if !settings.EffectiveEnabled() && !hasEnabledTeamOverride(settings) {
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
				Msg("failed to upsert linear_agent_sessions; requesting Linear redelivery")
			return DispatchResult{Status: "retryable_error", Err: fmt.Errorf("upsert linear_agent_sessions: %w", err)}
		}
	} else {
		// 1b. Prompted event: lookup the existing row. The corresponding
		// `created` usually created the row + 143 session. If we don't
		// have one, this `prompted` is racing the `created` (Linear can
		// deliver out of order under recovery). Persist a synthetic row
		// now via UpsertOnCreated so the worker — and any downstream
		// activity-log writes that key on agent_session_row_id — have a
		// real UUID to bind to. The late-arriving `created` re-upserts
		// idempotently on the (org_id, linear_agent_session_id) UNIQUE.
		row, err = d.agentSessions.Lookup(ctx, integration.OrgID, env.Payload.AgentSession.ID)
		if err != nil {
			if errors.Is(err, db.ErrLinearAgentSessionNotFound) {
				d.logger.Warn().
					Str("agent_session_id", env.Payload.AgentSession.ID).
					Msg("prompted event arrived before created; persisting synthetic row")
				row, _, err = d.agentSessions.UpsertOnCreated(ctx, integration.OrgID, db.UpsertOnCreatedInput{
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
						Msg("failed to persist synthetic linear_agent_sessions row; requesting Linear redelivery")
					return DispatchResult{Status: "retryable_error", Err: fmt.Errorf("persist synthetic linear_agent_sessions: %w", err)}
				}
			} else {
				d.logger.Error().Err(err).
					Str("agent_session_id", env.Payload.AgentSession.ID).
					Msg("failed to lookup linear_agent_sessions; requesting Linear redelivery")
				return DispatchResult{Status: "retryable_error", Err: fmt.Errorf("lookup linear_agent_sessions: %w", err)}
			}
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
		// Hard sub-second cap on the synchronous Linear roundtrip. The
		// outer Linear webhook ack SLA is 5s and we still owe an enqueue
		// after this; without a bounded timeout a slow Linear stalls the
		// dispatcher and Linear retries the delivery, fanning the work
		// out further. The bootstrap writer discards failed reservations
		// so a webhook retry can re-emit instead of short-circuiting on a
		// dead idem_key.
		emitCtx, cancel := context.WithTimeout(ctx, bootstrapEmitTimeout)
		emitRes, emitErr := d.emitter.Emit(emitCtx, linear.EmitInput{
			OrgID:             integration.OrgID,
			AgentSessionRowID: row.ID,
			AgentSessionID:    row.LinearAgentSessionID,
			Activity:          bootstrap,
		})
		cancel()
		// Latency includes the Reserve INSERT + the GraphQL emit. Tracked
		// here (rather than inside the writer) because the dispatcher
		// owns the 10s SLA contract and the latency budget is
		// dispatcher-anchored.
		d.metrics.RecordBootstrapLatency(ctx, float64(time.Since(bootstrapStart).Milliseconds()))
		d.metrics.RecordActivityEmitted(ctx, string(bootstrap.Type), emitRes.Skipped)
		if emitErr != nil {
			d.logger.Warn().Err(emitErr).
				Str("agent_session_id", row.LinearAgentSessionID).
				Msg("agent bootstrap activity emit failed; reservation discarded so webhook retry can re-emit")
			result.BootstrapEmitFailed = true
		}
		result.BootstrapEmitSkipped = emitRes.Skipped
	}

	// 3. Enqueue the worker job. Dedupe on (org_id, agent_session_id,
	// action) so re-deliveries collapse. The jobs UNIQUE is (queue,
	// dedupe_key) — not org-scoped — so prefixing the org id is what
	// keeps two orgs from colliding if they ever share a Linear-side
	// AgentSession ID (Linear ids are workspace-scoped opaque strings,
	// so collision is astronomically unlikely today; the prefix is
	// defense in depth against any future global-uniqueness regression).
	// For prompted events the dedupe also includes the comment id (when
	// present) so a different follow-up comment doesn't collapse onto a
	// previous prompted job. Each dynamic part is URL-escaped so a
	// Linear-side id that ever contains `:` can't smear two adjacent
	// fields together.
	dedupeParts := []string{
		"linear_agent_event",
		integration.OrgID.String(),
		url.QueryEscape(row.LinearAgentSessionID),
		url.QueryEscape(string(action)),
	}
	if action == linearAgentActionPrompted && env.Payload.AgentSession.CommentID != "" {
		dedupeParts = append(dedupeParts, url.QueryEscape(env.Payload.AgentSession.CommentID))
	}
	dedupe := strings.Join(dedupeParts, ":")
	// Surface payload context that's missing from the webhook envelope so
	// operators can spot misconfigured Linear webhook subscriptions in
	// production logs. The worker tolerates empty values (FetchIssue
	// re-derives them), but a chronically-empty team_id usually means the
	// workspace is sending a slimmer payload than we expect.
	if env.Payload.AgentSession.Issue.TeamID == "" || env.Payload.AgentSession.Issue.ProjectID == "" {
		d.logger.Debug().
			Str("agent_session_id", row.LinearAgentSessionID).
			Bool("missing_team_id", env.Payload.AgentSession.Issue.TeamID == "").
			Bool("missing_project_id", env.Payload.AgentSession.Issue.ProjectID == "").
			Msg("agent event payload missing optional issue context; worker will derive from FetchIssue")
	}
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
		"linear_prompt_body":      env.Payload.AgentActivity.Body,
	}
	jobID, err := d.jobs.Enqueue(ctx, integration.OrgID, "linear", "linear_agent_event", jobPayload, 5, &dedupe)
	if err != nil {
		d.logger.Error().Err(err).
			Str("agent_session_id", row.LinearAgentSessionID).
			Bool("created", created).
			Msg("failed to enqueue linear_agent_event; webhook delivery recorded but session creation will not happen")
		result.Status = "enqueue_failed"
		result.Err = err
		return result
	}
	result.JobID = jobID
	// Differentiate the outcome label so a bootstrap emit failure (which
	// usually means we missed the Linear 10s first-activity SLA) is
	// pivotable independently of the happy path. The worker still picks
	// up the enqueued job either way, so this is observability, not a
	// correctness signal.
	if result.BootstrapEmitFailed {
		result.Status = "agent_dispatched_bootstrap_failed"
	} else {
		result.Status = "agent_dispatched"
	}
	d.logger.Info().
		Str("agent_session_id", row.LinearAgentSessionID).
		Str("action", string(action)).
		Str("job_id", jobID.String()).
		Bool("created_row", created).
		Bool("bootstrap_emit_skipped", result.BootstrapEmitSkipped).
		Bool("bootstrap_emit_failed", result.BootstrapEmitFailed).
		Msg("linear agent event dispatched")
	return result
}

func (d *LinearAgentDispatcher) expectedLinearAppUserID(ctx context.Context, integration *models.Integration) (string, bool, error) {
	if d.credentialLookup != nil {
		cred, err := d.credentialLookup.Get(ctx, integration.OrgID, models.ProviderLinear)
		if err != nil {
			return "", false, err
		}
		if cred == nil {
			return "", false, nil
		}
		cfg, ok := cred.Config.(models.LinearConfig)
		if !ok || cfg.AppUserID == "" {
			return "", false, nil
		}
		return cfg.AppUserID, true, nil
	}
	expected, ok := decodeIntegrationAppUserID(integration.Config)
	return expected, ok, nil
}

func hasEnabledTeamOverride(settings models.LinearAgentSettings) bool {
	for _, enabled := range settings.PerTeamEnabled {
		if enabled != nil && *enabled {
			return true
		}
	}
	return false
}

// decodeIntegrationAppUserID extracts the persisted @143 agent user id from
// the integration's config jsonb. Returns ok=false when the config is
// malformed or the field is absent; callers treat that as "no filter
// available" rather than a hard failure so a legacy install that predates
// the agent flow still keeps working.
func decodeIntegrationAppUserID(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var cfg models.LinearConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", false
	}
	return cfg.AppUserID, true
}

type linearAgentBootstrapWriter struct {
	clientForOrg func(ctx context.Context, orgID uuid.UUID) (linear.Client, error)
	activities   *db.LinearAgentActivityLogStore
	logger       zerolog.Logger
}

func newLinearAgentBootstrapWriter(
	clientForOrg func(ctx context.Context, orgID uuid.UUID) (linear.Client, error),
	activities *db.LinearAgentActivityLogStore,
	logger zerolog.Logger,
) linearAgentBootstrapEmitter {
	if clientForOrg == nil || activities == nil {
		return nil
	}
	return &linearAgentBootstrapWriter{
		clientForOrg: clientForOrg,
		activities:   activities,
		logger:       logger,
	}
}

func (w *linearAgentBootstrapWriter) Emit(ctx context.Context, in linear.EmitInput) (linear.EmitResult, error) {
	client, err := w.clientForOrg(ctx, in.OrgID)
	if err != nil {
		return linear.EmitResult{}, err
	}
	return linear.NewAgentActivityWriter(client, w.activities, nil, w.logger).EmitOrDiscard(ctx, in)
}
