package linear

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// BuildDeps captures the shared infra (pool + already-constructed top-level
// stores) the Linear service needs. Pulling this out of router.go and
// cmd/server/main.go avoids two diverging copies of identical wiring.
type BuildDeps struct {
	Pool               *pgxpool.Pool
	Logger             zerolog.Logger
	Integrations       IntegrationReader
	IntegrationsWriter IntegrationWriter
	Credentials        CredentialReader
	// CredentialsWriter persists rotated OAuth tokens after a successful
	// refresh. Required for the refresh-on-expiry flow to be durable;
	// without it, refreshed tokens would be lost when the process restarts
	// and the integration would silently churn refresh tokens at Linear.
	CredentialsWriter CredentialWriter
	Issues            *db.IssueStore
	Sessions          *db.SessionStore
	IssueLinks        *db.SessionIssueLinkStore
	Orgs              *db.OrganizationStore
	Jobs              *db.JobStore
	JobQueueName      string
	JobPriority       int
	ClientFactory     ClientFactory
	// OAuthClient holds LINEAR_OAUTH_CLIENT_ID and LINEAR_OAUTH_CLIENT_SECRET.
	// Required for refresh-token redemption: Linear treats /oauth/token as a
	// confidential-client endpoint and rejects refreshes without these.
	// Without OAuthClient set, GetValidToken on an expiring credential
	// surfaces ErrOAuthClientNotConfigured, which the operator must fix by
	// populating the env vars rather than the user reconnecting.
	OAuthClient OAuthClientCreds
	// AppBaseURL is the absolute origin (e.g. cfg.FrontendURL) we send to
	// Linear inside attachment URLs and comment bodies. Empty falls back to
	// a relative path which Linear renders as plain text — production
	// callers must always set this.
	AppBaseURL string
	// TeamKeyCache is the optional shared in-process team-key allowlist
	// cache. In MODE=all the API router and the worker bundle both call
	// Build inside the same process; passing the same cache to both keeps
	// detection consistent (a refresh on the worker side immediately
	// invalidates the API-side lookup). Leave nil when only one Build call
	// runs in the process — NewService will allocate a per-Service cache.
	TeamKeyCache *TeamKeyCache
}

// TeamKeyCache exposes the concrete cache type so cmd/server/main.go can
// allocate one and pass it to both linear.Build call sites. The internal
// implementation (TTL'd map) stays unexported; callers only need to be
// able to construct a fresh empty instance with NewTeamKeyCache.
type TeamKeyCache = teamKeyAllowlistCache

// NewTeamKeyCache returns an empty cache suitable for sharing between the
// API router's and the worker bundle's linear.Build calls in the same
// process. MODE=all callers should allocate one with NewTeamKeyCache and
// pass it to BuildDeps.TeamKeyCache so a refresh on either side invalidates
// the other; otherwise the two Services keep independent caches and can
// disagree on the allowlist for up to the 60s TTL window (the same staleness
// envelope multi-node deployments already accept).
func NewTeamKeyCache() *TeamKeyCache {
	return &teamKeyAllowlistCache{}
}

// Build constructs the Linear service plus its three side-table stores from
// a shared dependency bundle. The returned *Service already has its job
// enqueuer wired onto the supplied JobStore — callers only need to add the
// SSE notifier (router) or PR-milestone enqueuer (PR service) themselves.
//
// JobQueueName defaults to "linear" and JobPriority to 5 when zero. The
// defaults match the design's worker conventions.
//
// AppBaseURL misconfiguration is logged at boot rather than failing the
// process. Linear renders attachment URLs and comment bodies verbatim, so
// a relative path here produces non-clickable text in Linear — visible to
// operators chasing "why are session links broken in Linear?" but not
// catastrophic. Falling back to a relative path keeps tests and dev
// environments (where FRONTEND_URL is often unset) workable.
func Build(deps BuildDeps) *Service {
	queue := deps.JobQueueName
	if queue == "" {
		queue = "linear"
	}
	priority := deps.JobPriority
	if priority == 0 {
		priority = 5
	}
	if strings.TrimSpace(deps.AppBaseURL) == "" {
		deps.Logger.Warn().Msg("linear.Build: AppBaseURL is empty; session deep-links posted to Linear will be relative paths and render as plain text. Set FRONTEND_URL in production.")
	} else if !strings.HasPrefix(deps.AppBaseURL, "http://") && !strings.HasPrefix(deps.AppBaseURL, "https://") {
		deps.Logger.Warn().
			Str("app_base_url", deps.AppBaseURL).
			Msg("linear.Build: AppBaseURL is not absolute; Linear renders session links as plain text without an http(s):// scheme")
	}

	clientFactory := deps.ClientFactory
	if clientFactory == nil {
		clientFactory = func(_ context.Context, token string) (Client, error) {
			return NewClient(token), nil
		}
	}

	teamKeyCache := deps.TeamKeyCache
	if teamKeyCache == nil {
		// No shared cache passed; allocate a fresh one local to this
		// Service. MODE=all callers that want API+worker invalidation to
		// stay in sync within the same process should pass an explicit
		// NewTeamKeyCache() result to both linear.Build calls. Without a
		// shared cache the two Services are still correct — refreshes
		// land in the DB and the local TTL bounds staleness — they just
		// don't proactively invalidate each other on a single-node MODE=all
		// deployment, which matches the multi-node staleness envelope.
		teamKeyCache = NewTeamKeyCache()
	}

	loader := func(ctx context.Context, orgID uuid.UUID) (models.OrgSettings, error) {
		org, err := deps.Orgs.GetByID(ctx, orgID)
		if err != nil {
			return models.OrgSettings{}, err
		}
		return models.ParseOrgSettings(org.Settings)
	}

	if deps.OAuthClient.ClientID == "" || deps.OAuthClient.ClientSecret == "" {
		// Logged at warn (not fatal) so MODE=worker / MODE=api dev rigs that
		// don't have Linear OAuth provisioned can still boot. Refresh on
		// expiring tokens will fail with ErrOAuthClientNotConfigured, which
		// is preferable to crashing the process.
		deps.Logger.Warn().
			Msg("linear.Build: OAuthClient is incomplete; refresh-token rotation will fail until LINEAR_OAUTH_CLIENT_ID / LINEAR_OAUTH_CLIENT_SECRET are configured")
	}

	svc := NewService(Config{
		Logger:             deps.Logger,
		Integrations:       deps.Integrations,
		IntegrationsWriter: deps.IntegrationsWriter,
		Credentials:        deps.Credentials,
		CredentialsWriter:  deps.CredentialsWriter,
		OAuthClient:        deps.OAuthClient,
		Issues:             deps.Issues,
		Links:              deps.IssueLinks,
		TeamKeys:           db.NewLinearTeamKeyStore(deps.Pool),
		ProviderState:      db.NewLinearProviderStateStore(deps.Pool),
		StateEvents:        db.NewLinearStateEventStore(deps.Pool),
		Sessions:           deps.Sessions,
		ClientFactory:      clientFactory,
		OrgSettingsLoader:  loader,
		Pool:               deps.Pool,
		AppBaseURL:         deps.AppBaseURL,
		TeamKeyCache:       teamKeyCache,
	})

	if deps.Jobs != nil {
		jobs := deps.Jobs
		svc.SetJobEnqueuer(func(ctx context.Context, orgID uuid.UUID, jobType string, payload any, dedupeKey *string) error {
			_, err := jobs.Enqueue(ctx, orgID, queue, jobType, payload, priority, dedupeKey)
			return err
		})
	}

	return svc
}

// MilestoneJobEnqueuer is the narrow surface EnqueueMilestone needs — both
// *db.JobStore and the agent package's local JobStore interface satisfy it
// structurally, so the consolidator works across import boundaries without
// pulling additional packages into either side.
type MilestoneJobEnqueuer interface {
	Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
}

// EnqueueMilestone is the canonical enqueuer for linear_milestone jobs:
// every caller (PR-event closure via MilestoneEnqueuerFor, terminal-state
// orchestrator path, no-changes worker handler) routes through here so the
// queue name, priority, and dedupe-key shape never drift. Best-effort —
// failures only log because terminal session bookkeeping must not stall on
// Linear-side hiccups.
func EnqueueMilestone(ctx context.Context, jobs MilestoneJobEnqueuer, logger zerolog.Logger, orgID, sessionID uuid.UUID, event string, prNumber int) {
	if jobs == nil {
		return
	}
	dedupe := "linear_milestone:" + sessionID.String() + ":" + event
	payload := map[string]any{
		"org_id":     orgID.String(),
		"session_id": sessionID.String(),
		"event":      event,
		"pr_number":  prNumber,
	}
	if _, err := jobs.Enqueue(ctx, orgID, "linear", "linear_milestone", payload, 5, &dedupe); err != nil {
		logger.Warn().Err(err).
			Str("session_id", sessionID.String()).
			Str("event", event).
			Msg("failed to enqueue linear_milestone job")
	}
}

// MilestoneEnqueuerFor returns the closure that PRService.SetLinearMilestoneEnqueuer
// expects, sharing the same JobStore + queue convention as Build via
// EnqueueMilestone.
//
// The concrete-pointer nil check on jobs is load-bearing: passing a typed
// nil (*db.JobStore)(nil) into EnqueueMilestone's interface-typed parameter
// produces a non-nil interface wrapping a nil pointer, so EnqueueMilestone's
// own `if jobs == nil` guard does NOT fire and Enqueue() would panic. We
// short-circuit at the concrete-pointer layer here so test harnesses (and
// any future api-only mode that wires the enqueuer without a job store) are
// safe to call the returned closure.
func MilestoneEnqueuerFor(jobs *db.JobStore, logger zerolog.Logger) func(ctx context.Context, orgID, sessionID uuid.UUID, event string, prNumber int) {
	return func(ctx context.Context, orgID, sessionID uuid.UUID, event string, prNumber int) {
		if jobs == nil {
			return
		}
		EnqueueMilestone(ctx, jobs, logger, orgID, sessionID, event, prNumber)
	}
}
