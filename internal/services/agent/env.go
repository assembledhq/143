package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

// ErrCodexAuthInvalid marks errors that genuinely indicate the user's ChatGPT
// credential is unusable: refresh token revoked, reused, or missing after the
// cached access token has expired. Store, network, OAuth server, and
// sandbox-side operations (Docker exec, file write) are NOT wrapped with this
// sentinel. The orchestrator branches on errors.Is so that only true auth
// failures show the "re-authenticate with ChatGPT" banner.
var ErrCodexAuthInvalid = errors.New("codex auth invalid")

// wrapCodexAuthInvalid tags err as a genuine auth failure so callers can
// distinguish it from sandbox/transport errors via errors.Is. Returns nil
// when err is nil so it is safe to use in a return chain.
func wrapCodexAuthInvalid(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrCodexAuthInvalid, err)
}

type codexAuthInvalidReporter interface {
	IsAuthInvalid(error) bool
}

func maybeWrapCodexAuthInvalid(source any, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrCodexAuthInvalid) {
		return err
	}
	reporter, ok := source.(codexAuthInvalidReporter)
	if ok && reporter.IsAuthInvalid(err) {
		return wrapCodexAuthInvalid(err)
	}
	return err
}

// AuthError is returned by CheckAuth and parsePlan's auth-detection heuristic
// when an agent run cannot authenticate. Callers can errors.As to distinguish
// auth failures from generic errors — the PM service uses this to persist a
// descriptive failure on the plan record so the UI can show actionable guidance
// ("PM paused: configure Codex") instead of an opaque parse error.
type AuthError struct {
	AgentType                     models.AgentType
	Detail                        string
	Provider                      models.ProviderName
	RateLimitedUntil              *time.Time
	FallbackCandidatesUnavailable bool
}

func (e *AuthError) Error() string {
	if e.AgentType != "" {
		return fmt.Sprintf("agent auth failed (%s): %s", e.AgentType, e.Detail)
	}
	return fmt.Sprintf("agent auth failed: %s", e.Detail)
}

// AgentEnv owns the logic for shaping the sandbox environment and auth
// setup for a coding agent run. It is the single source of truth for:
//   - per-agent-type provider credential resolution (user → team → org)
//   - integration credentials (Sentry / Linear / Notion)
//   - agent_config overrides for non-secret agent defaults (Amp / Pi only)
//   - auth pre-flight checks
//   - Codex auth.json injection
//
// Both Orchestrator (interactive sessions) and the PM service (cron-triggered
// agent runs) depend on AgentEnv so that any change to agent auth lives in
// exactly one place — no more "PM works for Claude Code but breaks for Codex".
type AgentEnv struct {
	credentials       CredentialProvider
	codingCredentials CodingCredentialProvider
	orgs              OrgStore
	orgSettingsCache  *OrgSettingsCache
	codexAuth         CodexAuthProvider
	claudeCodeAuth    ClaudeCodeAuthProvider
	pagerDutySettings pagerDutyWritebackSettingsReader
	// linearTokens, when set, supplies the LINEAR_ACCESS_TOKEN env var via a
	// refresh-aware resolver. Without it, the sandbox falls back to reading
	// the raw credential row (legacy path; the access token may have aged
	// out by the time the sandbox runs). Production wiring always sets this
	// to *linear.Service so the orchestrator gets the same refresh-on-expiry
	// guarantee that worker handlers do.
	linearTokens LinearTokenResolver
	// pagerDutyTokens, when set, proactively refreshes a near-expiry PagerDuty
	// OAuth access token before injecting PAGERDUTY_ACCESS_TOKEN, mirroring the
	// Linear refresh-on-expiry guarantee. Without it the sandbox uses the raw
	// credential row, which can be expired for scoped OAuth installs.
	pagerDutyTokens PagerDutyTokenRefresher
	provider        SandboxProvider
	logger          zerolog.Logger

	// recentPicks remembers the credential id chosen for each (orgID, userID,
	// provider) tuple by the most recent pickFromCodingProvider call. It feeds
	// ShedRateLimited / ShedAuthRejected so the orchestrator can surface a
	// 429/401 back to the unified store's in-process health cache without
	// plumbing credential ids through every call site. The map is bounded by
	// pickTrackerMax with simple time-based eviction; concurrent sessions for
	// the same scope race to write the slot, which is acceptable per the
	// design's eventual-consistency note (`docs/design/future/65-…` § health
	// cache).
	recentPicks        map[pickKey]pickRecord
	recentPicksMu      sync.Mutex
	credentialBlocks   map[pickKey]credentialBlock
	credentialBlocksMu sync.Mutex
}

type pickKey struct {
	orgID    uuid.UUID
	userID   uuid.UUID // uuid.Nil for org-scope
	provider models.ProviderName
}

type pickRecord struct {
	credID     uuid.UUID
	credential *models.DecryptedCodingCredential
	binding    runtimeCredentialBinding
	at         time.Time
}

type runtimeCredentialBinding struct {
	CredentialID      uuid.UUID
	AgentType         models.AgentType
	Provider          models.ProviderName
	BackingProvider   models.ProviderName
	EffectiveModel    string
	CredentialScope   models.CodingCredentialScope
	CredentialOwnerID *uuid.UUID
}

type credentialBlock struct {
	detail                        string
	provider                      models.ProviderName
	rateLimitedUntil              *time.Time
	fallbackCandidatesUnavailable bool
	at                            time.Time
}

// CredentialFailureSignal is the normalized credential-level failure parsed
// from an agent runtime result.
type CredentialFailureSignal struct {
	RateLimited      bool
	AuthRejected     bool
	RateLimitedUntil time.Time
	Message          string
}

// pickTrackerTTL bounds how long after a pick a Shed call still applies.
// Longer than the store's health-cache TTL because the latency between a
// session start and the failure detection (token_expired retry, post-run
// classification) can run minutes.
const pickTrackerTTL = 5 * time.Minute

// pickTrackerMax bounds the recentPicks map so it cannot grow unboundedly
// under churn. When exceeded, fully-expired (>pickTrackerTTL old) entries
// are swept; if still over the limit, half-aged (>pickTrackerTTL/2) entries
// are swept too; a single-oldest backstop runs only if both passes left the
// map full. Batch eviction keeps recordPick amortized cheap under sustained
// load on >4096 distinct (org, user, provider) tuples — a single-oldest
// sweep on every insert would walk the whole map for every record.
const pickTrackerMax = 4096

// codexSubscriptionRefreshWindow mirrors codexauth.refreshWindow. The agent
// package cannot import codexauth without creating a service-layer cycle, so
// keep the value local to the injection path that handles unified subscription
// rows.
const codexSubscriptionRefreshWindow = 5 * time.Minute

// AgentEnvDeps holds the dependencies for constructing an AgentEnv. Named
// AgentEnvDeps (rather than AgentEnvConfig) to avoid confusion with
// models.AgentEnvConfig, which is a per-org override map consumed by this
// helper.
type AgentEnvDeps struct {
	Credentials           CredentialProvider
	CodingCredentials     CodingCredentialProvider         // preferred — unified resolver. Reads `coding_credentials` and is the source of truth post-migration.
	Orgs                  OrgStore                         // optional — enables agent_config overrides
	OrgSettingsCache      *OrgSettingsCache                // optional — caches agent_config lookups
	CodexAuth             CodexAuthProvider                // optional — enables ChatGPT OAuth for Codex
	ClaudeCodeAuth        ClaudeCodeAuthProvider           // optional — enables Claude subscription OAuth for Claude Code
	PagerDutyIntegrations pagerDutyWritebackSettingsReader // optional — enables PagerDuty agent write-tool gating from integration settings
	// LinearTokens optionally supplies a refresh-aware Linear access token
	// for the sandbox env. When set, the orchestrator injects the result of
	// GetValidAccessToken (rotating expired tokens transparently). Without
	// it, the sandbox falls back to the raw credential read — fine for
	// tests and pre-refresh-flow installs, but those env vars can be stale
	// for any session that starts within refreshWindow of expiry.
	LinearTokens LinearTokenResolver
	// PagerDutyTokens optionally supplies a refresh-aware PagerDuty access token
	// for the sandbox. When set, the resolver rotates a near-expiry token (and
	// persists the rotation) before injection. Without it, the sandbox uses the
	// raw credential read — fine for tests and classic tokens, but scoped OAuth
	// tokens can be expired for sessions starting after the access-token TTL.
	PagerDutyTokens PagerDutyTokenRefresher
	Provider        SandboxProvider // required for sandbox credential file injection
	Logger          zerolog.Logger
}

// PagerDutyTokenRefresher is the narrow surface AgentEnv needs to refresh a
// near-expiry PagerDuty OAuth token for a specific credential. Implemented by
// *pagerduty.TokenService.
type PagerDutyTokenRefresher interface {
	EnsureFresh(ctx context.Context, orgID, credentialID uuid.UUID, observed models.PagerDutyConfig) (models.PagerDutyConfig, error)
}

// LinearTokenResolver is the narrow surface AgentEnv needs from the Linear
// service to inject a fresh access token into the sandbox. The signature
// returns "" with nil error to mean "this org has no Linear integration
// installed" so env.go can distinguish that from "we tried to refresh and
// it failed" without importing Linear-specific sentinels.
type LinearTokenResolver interface {
	GetValidAccessToken(ctx context.Context, orgID uuid.UUID) (string, error)
}

type pagerDutyWritebackSettingsReader interface {
	ListManageable(ctx context.Context, orgID uuid.UUID) ([]models.PagerDutyIntegration, error)
}

type pagerDutyCredentialByIDReader interface {
	GetByID(ctx context.Context, orgID uuid.UUID, id uuid.UUID) (*models.DecryptedCredential, error)
}

// NewAgentEnv constructs an AgentEnv. The Provider is required; all other
// dependencies are optional and disable the corresponding feature when nil
// (e.g. no UserCredentials → personal/team resolution is skipped and only
// org-scoped credentials are consulted).
func NewAgentEnv(deps AgentEnvDeps) *AgentEnv {
	return &AgentEnv{
		credentials:       deps.Credentials,
		codingCredentials: deps.CodingCredentials,
		orgs:              deps.Orgs,
		orgSettingsCache:  deps.OrgSettingsCache,
		codexAuth:         deps.CodexAuth,
		claudeCodeAuth:    deps.ClaudeCodeAuth,
		pagerDutySettings: deps.PagerDutyIntegrations,
		linearTokens:      deps.LinearTokens,
		pagerDutyTokens:   deps.PagerDutyTokens,
		provider:          deps.Provider,
		logger:            deps.Logger,
		recentPicks:       make(map[pickKey]pickRecord),
		credentialBlocks:  make(map[pickKey]credentialBlock),
	}
}

// SetLinearTokens installs (or replaces) the Linear refresh-aware token
// resolver after construction. NewAgentEnv is called early during process
// boot (before stores like *db.SessionIssueLinkStore exist), but the
// linear service depends on those stores, so it is built later. Rather
// than deferring NewAgentEnv to the latest possible moment, the
// orchestrator wiring calls SetLinearTokens once linear.Build has
// returned. Safe to call with nil to detach the resolver in tests.
func (e *AgentEnv) SetLinearTokens(r LinearTokenResolver) {
	if e == nil {
		return
	}
	e.linearTokens = r
}

// SetPagerDutyTokens installs (or replaces) the PagerDuty refresh-aware token
// resolver after construction, mirroring SetLinearTokens. Safe to call with nil.
func (e *AgentEnv) SetPagerDutyTokens(r PagerDutyTokenRefresher) {
	if e == nil {
		return
	}
	e.pagerDutyTokens = r
}

// CodingCredentialShedder is the subset of CodingCredentialStore the agent
// runtime needs to surface 429/401 back into the in-process health cache.
// Defined as an interface so env.go avoids a package import cycle and tests
// can substitute a fake.
type CodingCredentialShedder interface {
	MarkRateLimited(id uuid.UUID)
	MarkAuthRejected(id uuid.UUID)
}

// CodingCredentialPersistentShedder is implemented by the unified store when
// runtime credential failures should be persisted across workers.
type CodingCredentialPersistentShedder interface {
	MarkRateLimitedForScope(ctx context.Context, scope models.Scope, id uuid.UUID, limit models.CodingCredentialRateLimit) error
	MarkAuthRejectedForScope(ctx context.Context, scope models.Scope, id uuid.UUID) error
}

// CodexAuthRefresher is the optional capability implemented by the Codex auth
// service. Unified subscription resolution chooses a concrete credential id;
// this interface lets auth.json injection refresh that exact row before
// writing an access token into the sandbox.
//
// The scope must match the credential's owner: personal subscriptions live
// in coding_credentials with user_id set, and the underlying lookup filters
// on (org_id, user_id). Passing org scope for a personal credential would
// mis-route the lookup and surface as "credential not found", silently
// dropping personal subscriptions back to the org fallback after their
// first 8h of token life.
type CodexAuthRefresher interface {
	RefreshTokenByID(ctx context.Context, scope models.Scope, credID uuid.UUID) (*models.OpenAISubscriptionConfig, error)
}

// CodingCredentialMultiPicker is implemented by the real unified store for
// agent requests that can be satisfied by multiple provider rows, such as an
// API key provider plus its subscription twin. It merges those providers
// before selection so personal rows always outrank org fallback rows.
type CodingCredentialMultiPicker interface {
	PickRunnableMulti(ctx context.Context, scope models.Scope, providers []models.ProviderName) (*models.DecryptedCodingCredential, error)
}

const (
	internalAuthBlockedKey                              = "__143_AUTH_BLOCKED_DETAIL"
	internalAuthBlockedProviderKey                      = "__143_AUTH_BLOCKED_PROVIDER"
	internalAuthBlockedRateLimitedUntilKey              = "__143_AUTH_BLOCKED_RATE_LIMITED_UNTIL"
	internalAuthBlockedFallbackCandidatesUnavailableKey = "__143_AUTH_BLOCKED_FALLBACK_CANDIDATES_UNAVAILABLE"
)

// codingShedder type-asserts the configured CodingCredentialProvider into the
// shed-capable interface. Returns nil when the provider does not implement
// shedding (older test rigs), in which case the Shed* methods become no-ops.
func (e *AgentEnv) codingShedder() CodingCredentialShedder {
	if e == nil || e.codingCredentials == nil {
		return nil
	}
	if shedder, ok := e.codingCredentials.(CodingCredentialShedder); ok {
		return shedder
	}
	return nil
}

func (e *AgentEnv) recordCredentialPick(orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName, cred models.DecryptedCodingCredential) {
	copied := cred
	e.recordPickWithCredential(orgID, userID, provider, cred.ID, &copied)
}

func (e *AgentEnv) recordPickWithCredential(orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName, credID uuid.UUID, cred *models.DecryptedCodingCredential) {
	if e == nil {
		return
	}
	key := pickKey{orgID: orgID, provider: provider}
	if userID != nil {
		key.userID = *userID
	}
	e.recentPicksMu.Lock()
	defer e.recentPicksMu.Unlock()
	if e.recentPicks == nil {
		e.recentPicks = make(map[pickKey]pickRecord)
	}
	now := time.Now()
	if len(e.recentPicks) >= pickTrackerMax {
		e.evictAgedPicksLocked(now, pickTrackerTTL)
	}
	if len(e.recentPicks) >= pickTrackerMax {
		e.evictAgedPicksLocked(now, pickTrackerTTL/2)
	}
	if len(e.recentPicks) >= pickTrackerMax {
		e.evictOldestPickLocked()
	}
	e.recentPicks[key] = pickRecord{
		credID:     credID,
		credential: cred,
		binding:    runtimeCredentialBindingForPick(provider, credID, cred),
		at:         now,
	}
}

func runtimeCredentialBindingForPick(provider models.ProviderName, credID uuid.UUID, cred *models.DecryptedCodingCredential) runtimeCredentialBinding {
	binding := runtimeCredentialBinding{
		CredentialID:    credID,
		AgentType:       agentTypeForRuntimeProvider(provider),
		Provider:        provider,
		BackingProvider: provider,
	}
	if cred == nil {
		return binding
	}
	binding.CredentialScope = cred.Scope().Label()
	binding.CredentialOwnerID = cred.UserID
	binding.Provider = cred.Provider
	binding.AgentType = agentTypeForRuntimeProvider(cred.Provider)
	binding.BackingProvider = cred.Provider
	switch cfg := cred.Config.(type) {
	case models.OpenCodeConfig:
		binding.AgentType = models.AgentTypeOpenCode
		binding.BackingProvider = cfg.NormalizedBackingProvider()
		binding.EffectiveModel = cfg.Model
	case models.GeminiConfig:
		binding.EffectiveModel = cfg.Model
	}
	if binding.AgentType == "" {
		binding.AgentType = agentTypeForRuntimeProvider(provider)
	}
	return binding
}

func agentTypeForRuntimeProvider(provider models.ProviderName) models.AgentType {
	switch provider {
	case models.ProviderOpenAI, models.ProviderOpenAISubscription:
		return models.AgentTypeCodex
	case models.ProviderAnthropic, models.ProviderAnthropicSubscription:
		return models.AgentTypeClaudeCode
	case models.ProviderAmp:
		return models.AgentTypeAmp
	case models.ProviderPi:
		return models.AgentTypePi
	case models.ProviderOpenCode:
		return models.AgentTypeOpenCode
	default:
		return ""
	}
}

func (e *AgentEnv) lookupRuntimeCredentialBinding(orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName) (runtimeCredentialBinding, bool) {
	rec, ok := e.lookupRecentPickRecord(orgID, userID, provider)
	if !ok || rec.binding.CredentialID == uuid.Nil {
		return runtimeCredentialBinding{}, false
	}
	return rec.binding, true
}

func (e *AgentEnv) lookupRecentCredential(orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName) (models.DecryptedCodingCredential, bool) {
	rec, ok := e.lookupRecentPickRecord(orgID, userID, provider)
	if !ok || rec.credential == nil {
		return models.DecryptedCodingCredential{}, false
	}
	return *rec.credential, true
}

func (e *AgentEnv) lookupRecentPickRecord(orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName) (pickRecord, bool) {
	key := pickKey{orgID: orgID, provider: provider}
	if userID != nil {
		key.userID = *userID
	}
	e.recentPicksMu.Lock()
	defer e.recentPicksMu.Unlock()
	rec, ok := e.recentPicks[key]
	if !ok {
		return pickRecord{}, false
	}
	if time.Since(rec.at) > pickTrackerTTL {
		delete(e.recentPicks, key)
		return pickRecord{}, false
	}
	return rec, true
}

// evictAgedPicksLocked drops every entry older than the supplied threshold.
// recordPick first calls this with the full TTL (drops expired entries that
// can never be picked again), then under continued pressure with TTL/2 to
// shed half-aged entries — far cheaper than calling evictOldestPickLocked
// per insert, which walks the whole map to drop a single record.
func (e *AgentEnv) evictAgedPicksLocked(now time.Time, olderThan time.Duration) {
	for k, v := range e.recentPicks {
		if now.Sub(v.at) > olderThan {
			delete(e.recentPicks, k)
		}
	}
}

func (e *AgentEnv) evictOldestPickLocked() {
	var (
		oldestKey pickKey
		oldestAt  time.Time
		first     = true
	)
	for k, v := range e.recentPicks {
		if first || v.at.Before(oldestAt) {
			oldestKey = k
			oldestAt = v.at
			first = false
		}
	}
	if !first {
		delete(e.recentPicks, oldestKey)
	}
}

// ShedRateLimited surfaces a 429 from an upstream provider call back to the
// unified store's in-process health cache. The orchestrator calls this when a
// session run fails with rate-limit signals so the next pick within the TTL
// window skips the just-throttled credential. Safe to call when the env was
// constructed without a coding-credential store; in that case it is a no-op.
//
// userID may be nil for org-scope picks.
func (e *AgentEnv) ShedRateLimited(orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName) {
	e.ShedRateLimitedWithSignal(context.Background(), orgID, userID, provider, CredentialFailureSignal{})
}

func (e *AgentEnv) ShedRateLimitedWithSignal(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName, signal CredentialFailureSignal) {
	shedder := e.codingShedder()
	if shedder == nil {
		return
	}
	rec, ok := e.lookupRecentPickRecord(orgID, userID, provider)
	if !ok {
		return
	}
	limit := models.CodingCredentialRateLimit{Until: signal.RateLimitedUntil, Message: signal.Message}
	if limit.Until.IsZero() {
		limit.Until = time.Now().Add(75 * time.Second)
	}
	if persistent, ok := shedder.(CodingCredentialPersistentShedder); ok && rec.credential != nil {
		if err := persistent.MarkRateLimitedForScope(ctx, rec.credential.Scope(), rec.credID, limit); err != nil {
			e.logger.Warn().Err(err).
				Str("cred_id", rec.credID.String()).
				Str("provider", string(provider)).
				Msg("failed to persist coding credential rate-limit marker")
		}
		return
	}
	shedder.MarkRateLimited(rec.credID)
}

// ShedAuthRejected surfaces a 401 / token_expired from an upstream provider
// call back to the unified store's in-process health cache. The orchestrator
// calls this when a session run fails with auth signals after a refresh+retry
// has already been attempted, indicating the credential is structurally
// broken and should not be picked again until the cache TTL expires (and the
// OAuth services flip the persisted status to invalid).
//
// userID may be nil for org-scope picks.
func (e *AgentEnv) ShedAuthRejected(orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName) {
	e.ShedAuthRejectedWithContext(context.Background(), orgID, userID, provider)
}

func (e *AgentEnv) ShedAuthRejectedWithContext(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName) {
	shedder := e.codingShedder()
	if shedder == nil {
		return
	}
	rec, ok := e.lookupRecentPickRecord(orgID, userID, provider)
	if !ok {
		return
	}
	if persistent, ok := shedder.(CodingCredentialPersistentShedder); ok && rec.credential != nil {
		if err := persistent.MarkAuthRejectedForScope(ctx, rec.credential.Scope(), rec.credID); err != nil {
			e.logger.Warn().Err(err).
				Str("cred_id", rec.credID.String()).
				Str("provider", string(provider)).
				Msg("failed to persist coding credential auth rejection")
		}
		return
	}
	shedder.MarkAuthRejected(rec.credID)
}

// integrationCredentials holds the resolved integration configs for an org.
type integrationCredentials struct {
	Sentry    *models.SentryConfig
	Linear    *models.LinearConfig
	PagerDuty *models.PagerDutyConfig
	Notion    *models.NotionConfig
	CircleCI  *models.CircleCIConfig
	Mezmo     *models.MezmoConfig
}

type integrationCredentialProvider interface {
	GetAllIntegrations(ctx context.Context, orgID uuid.UUID, providers []models.ProviderName) (map[models.ProviderName]*models.DecryptedCredential, error)
}

var integrationProviderNames = []models.ProviderName{
	models.ProviderSentry,
	models.ProviderLinear,
	models.ProviderPagerDuty,
	models.ProviderNotion,
	models.ProviderCircleCI,
	models.ProviderMezmo,
}

// fetchIntegrationCredentials retrieves integration configs for an org from
// the credential provider. Returns zero-value configs (nil pointers inside the
// returned struct) when a credential is unavailable — callers should nil-check
// each pointer before use.
func (e *AgentEnv) fetchIntegrationCredentials(ctx context.Context, orgID uuid.UUID) integrationCredentials {
	var ic integrationCredentials
	if e.credentials == nil {
		return ic
	}

	batch, ok := e.credentials.(integrationCredentialProvider)
	if !ok {
		return e.fetchIntegrationCredentialsLegacy(ctx, orgID)
	}
	creds, err := batch.GetAllIntegrations(ctx, orgID, integrationProviderNames)
	if err != nil {
		return ic
	}
	ic.apply(creds)
	return ic
}

func (e *AgentEnv) fetchIntegrationCredentialsLegacy(ctx context.Context, orgID uuid.UUID) integrationCredentials {
	creds := make(map[models.ProviderName]*models.DecryptedCredential, len(integrationProviderNames))
	for _, provider := range integrationProviderNames {
		if cred, err := e.credentials.Get(ctx, orgID, provider); err == nil && cred != nil {
			creds[provider] = cred
		}
	}
	var ic integrationCredentials
	ic.apply(creds)
	return ic
}

func (ic *integrationCredentials) apply(creds map[models.ProviderName]*models.DecryptedCredential) {
	if cred := creds[models.ProviderSentry]; cred != nil {
		if cfg, ok := cred.Config.(models.SentryConfig); ok {
			ic.Sentry = &cfg
		}
	}
	if cred := creds[models.ProviderLinear]; cred != nil {
		if cfg, ok := cred.Config.(models.LinearConfig); ok {
			ic.Linear = &cfg
		}
	}
	if cred := creds[models.ProviderPagerDuty]; cred != nil {
		if cfg, ok := cred.Config.(models.PagerDutyConfig); ok {
			ic.PagerDuty = &cfg
		}
	}
	if cred := creds[models.ProviderNotion]; cred != nil {
		if cfg, ok := cred.Config.(models.NotionConfig); ok {
			ic.Notion = &cfg
		}
	}
	if cred := creds[models.ProviderCircleCI]; cred != nil {
		if cfg, ok := cred.Config.(models.CircleCIConfig); ok {
			ic.CircleCI = &cfg
		}
	}
	if cred := creds[models.ProviderMezmo]; cred != nil {
		if cfg, ok := cred.Config.(models.MezmoConfig); ok {
			ic.Mezmo = &cfg
		}
	}
}

// pagerDutyWritebackEnabled resolves the writeback flag for the singleton-credential
// fallback path, where we did not source the token from a specific integration.
// Writeback is only enabled when the attribution is unambiguous: exactly one
// manageable (active/degraded) integration exists. With multiple integrations we
// cannot tell which one the singleton token belongs to, so we fail closed rather
// than risk enabling writes against an account where the operator disabled them.
func (e *AgentEnv) pagerDutyWritebackEnabled(ctx context.Context, orgID uuid.UUID) bool {
	if e == nil || e.pagerDutySettings == nil {
		return false
	}
	integrations, err := e.pagerDutySettings.ListManageable(ctx, orgID)
	if err != nil {
		e.logger.Warn().Err(err).
			Str("org_id", orgID.String()).
			Msg("env: PagerDuty integration settings lookup failed; sandbox write tools will be disabled")
		return false
	}
	var manageable []models.PagerDutyIntegration
	for _, integration := range integrations {
		if integration.Status == models.PagerDutyIntegrationStatusActive ||
			integration.Status == models.PagerDutyIntegrationStatusDegraded {
			manageable = append(manageable, integration)
		}
	}
	if len(manageable) != 1 {
		if len(manageable) > 1 {
			e.logger.Warn().
				Str("org_id", orgID.String()).
				Int("manageable_integrations", len(manageable)).
				Msg("env: multiple PagerDuty integrations present; disabling sandbox writeback on the singleton-credential fallback path to avoid mis-attribution")
		}
		return false
	}
	return manageable[0].WritebackEnabled
}

func (e *AgentEnv) resolvePagerDutyEnvConfig(ctx context.Context, orgID uuid.UUID, fallback *models.PagerDutyConfig) (*models.PagerDutyConfig, bool) {
	if e != nil && e.pagerDutySettings != nil {
		if reader, ok := e.credentials.(pagerDutyCredentialByIDReader); ok {
			integrations, err := e.pagerDutySettings.ListManageable(ctx, orgID)
			if err != nil {
				e.logger.Warn().Err(err).
					Str("org_id", orgID.String()).
					Msg("env: PagerDuty integration settings lookup failed; sandbox will fall back to singleton PagerDuty credential")
			} else {
				for _, integration := range integrations {
					credentialID, err := pagerDutyCredentialIDFromRef(integration.CredentialRef)
					if err != nil {
						e.logger.Warn().Err(err).
							Str("org_id", orgID.String()).
							Str("pagerduty_integration_id", integration.ID.String()).
							Msg("env: PagerDuty credential reference is invalid")
						continue
					}
					credential, err := reader.GetByID(ctx, orgID, credentialID)
					if err != nil {
						e.logger.Warn().Err(err).
							Str("org_id", orgID.String()).
							Str("pagerduty_integration_id", integration.ID.String()).
							Str("credential_id", credentialID.String()).
							Msg("env: failed to load PagerDuty credential by integration reference")
						continue
					}
					cfg, ok := credential.Config.(models.PagerDutyConfig)
					if !ok {
						e.logger.Warn().
							Str("org_id", orgID.String()).
							Str("pagerduty_integration_id", integration.ID.String()).
							Str("credential_id", credentialID.String()).
							Msg("env: referenced PagerDuty credential has unexpected config type")
						continue
					}
					// The integration row is the source of truth for region; the
					// credential config may be stale or unset. This keeps the
					// sandbox PAGERDUTY_API_URL consistent with the integration.
					if region := strings.TrimSpace(integration.ServiceRegion); region != "" {
						cfg.ServiceRegion = region
					}
					// Proactively refresh a near-expiry scoped OAuth token so the
					// sandbox doesn't start with an access token that expires
					// mid-session. Falls back to the observed token on failure.
					if e.pagerDutyTokens != nil {
						refreshed, err := e.pagerDutyTokens.EnsureFresh(ctx, orgID, credentialID, cfg)
						if err != nil {
							e.logger.Warn().Err(err).
								Str("org_id", orgID.String()).
								Str("pagerduty_integration_id", integration.ID.String()).
								Msg("env: PagerDuty token refresh failed; using existing token")
						} else {
							cfg = refreshed
						}
					}
					return &cfg, integration.WritebackEnabled
				}
			}
		}
	}
	if fallback == nil {
		return nil, false
	}
	return fallback, e.pagerDutyWritebackEnabled(ctx, orgID)
}

func pagerDutyCredentialIDFromRef(ref string) (uuid.UUID, error) {
	value, ok := strings.CutPrefix(strings.TrimSpace(ref), "org_credential:")
	if !ok {
		return uuid.Nil, fmt.Errorf("unsupported PagerDuty credential ref %q", ref)
	}
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// Resolve builds the sandbox env vars for the given agent type. It checks
// credentials in order: user personal → team default → org credential.
// Codex CLI auth is handled via auth.json injection (InjectCodexAuth), not
// env vars.
//
// Invariant: sandbox env must only come from org-scoped DB credentials. Do
// NOT fall back to server-level env vars (e.g. cfg.AnthropicAPIKey,
// cfg.OpenAIAPIKey) — those are 143.dev-level platform credentials and would
// leak across orgs in a multi-tenant deployment. Server-level LLM keys are
// reserved for 143's own internal LLM calls via Config.LLMConfig().
func (e *AgentEnv) Resolve(ctx context.Context, orgID uuid.UUID, agentType models.AgentType, userID *uuid.UUID) map[string]string {
	return e.ResolveForModel(ctx, orgID, agentType, userID, "")
}

func (e *AgentEnv) ResolveForModel(ctx context.Context, orgID uuid.UUID, agentType models.AgentType, userID *uuid.UUID, modelOverride string) map[string]string {
	merged := make(map[string]string)

	switch agentType {
	case models.AgentTypeClaudeCode:
		cfg := e.resolveProviderConfig(ctx, orgID, userID, models.ProviderAnthropic)
		if ac, ok := cfg.(models.AnthropicConfig); ok {
			if ac.APIKey != "" {
				merged["ANTHROPIC_API_KEY"] = ac.APIKey
			}
			if ac.BaseURL != "" {
				merged["ANTHROPIC_BASE_URL"] = ac.BaseURL
			}
		} else if sub, ok := cfg.(models.AnthropicSubscriptionConfig); ok && sub.IsSetupToken() && sub.OAuthToken != "" && !sub.IsExpired() {
			merged["CLAUDE_CODE_OAUTH_TOKEN"] = sub.OAuthToken
		} else if block, ok := e.lookupCredentialBlock(orgID, userID, models.ProviderAnthropic); ok {
			setAuthBlockedEnv(merged, block)
		}
	case models.AgentTypeCodex:
		// Codex CLI authenticates via ~/.codex/auth.json (injected by
		// InjectCodexAuth), NOT via the CODEX_API_KEY env var. The env var
		// makes Codex call api.openai.com/v1/responses which requires the
		// api.responses.write scope — a scope the ChatGPT OAuth token does
		// not carry. The auth.json path uses the ChatGPT backend instead,
		// which accepts the OAuth token as-is.
		//
		// Inject the general OpenAI API key as OPENAI_API_KEY for other
		// tools in the sandbox (not used by Codex CLI itself).
		cfg := e.resolveProviderConfig(ctx, orgID, userID, models.ProviderOpenAI)
		if oc, ok := cfg.(models.OpenAIConfig); ok {
			if oc.APIKey != "" {
				merged["OPENAI_API_KEY"] = oc.APIKey
			}
			if oc.BaseURL != "" {
				merged["OPENAI_BASE_URL"] = oc.BaseURL
			}
		} else if block, ok := e.lookupCredentialBlock(orgID, userID, models.ProviderOpenAI); ok {
			setAuthBlockedEnv(merged, block)
		}
		// Skip Codex CLI's internal bwrap (bubblewrap) sandboxing. The
		// container is already isolated by Docker + gVisor (dropped caps,
		// read-only rootfs, non-root user, PID limits), so bwrap is
		// redundant and fails because gVisor doesn't support the
		// unprivileged user namespaces that bwrap requires.
		merged["CODEX_UNSAFE_ALLOW_NO_SANDBOX"] = "1"
	case models.AgentTypeAmp:
		cfg := e.resolveProviderConfig(ctx, orgID, userID, models.ProviderAmp)
		if amp, ok := cfg.(models.AmpConfig); ok && amp.APIKey != "" {
			merged["AMP_API_KEY"] = amp.APIKey
		} else if block, ok := e.lookupCredentialBlock(orgID, userID, models.ProviderAmp); ok {
			setAuthBlockedEnv(merged, block)
		}
	case models.AgentTypePi:
		cfg := e.resolveProviderConfig(ctx, orgID, userID, models.ProviderPi)
		if pi, ok := cfg.(models.PiConfig); ok && pi.APIKey != "" {
			merged["PI_API_KEY"] = pi.APIKey
		} else if block, ok := e.lookupCredentialBlock(orgID, userID, models.ProviderPi); ok {
			setAuthBlockedEnv(merged, block)
		}
	case models.AgentTypeOpenCode:
		cfg := e.resolveOpenCodeProviderConfig(ctx, orgID, userID, modelOverride)
		if oc, ok := cfg.(models.OpenCodeConfig); ok && oc.APIKey != "" {
			if modelOverride != "" {
				oc.Model = strings.TrimSpace(modelOverride)
			} else {
				oc.Model = e.effectiveOpenCodeModel(ctx, orgID, oc.Model)
			}
			applyOpenCodeEnv(merged, oc)
			e.updateRuntimeCredentialBindingModel(orgID, userID, models.ProviderOpenCode, oc.Model)
		} else if block, ok := e.lookupCredentialBlock(orgID, userID, models.ProviderOpenCode); ok {
			setAuthBlockedEnv(merged, block)
		}
	}

	// Integration credentials — consumed by the 143-tools CLI (preferred)
	// and 143-mcp binary inside the sandbox. Agents use the CLI via shell
	// commands; the MCP server is only for IDE integrations. See
	// internal/services/mcp/AGENTS.md.
	ic := e.fetchIntegrationCredentials(ctx, orgID)
	if ic.Sentry != nil {
		if ic.Sentry.AccessToken != "" {
			merged["SENTRY_AUTH_TOKEN"] = ic.Sentry.AccessToken
		}
		if ic.Sentry.OrgSlug != "" {
			merged["SENTRY_ORG_SLUG"] = ic.Sentry.OrgSlug
		}
	}
	// Linear access token injection. The refresh-aware resolver is preferred:
	// it rotates a near-expiring token before sandbox-start so the agent
	// can run a multi-minute turn without crossing the access-token expiry
	// boundary. The raw fetchIntegrationCredentials read is the fallback
	// for test wiring that doesn't supply a resolver — those callers
	// accept that the env var may be stale.
	//
	// Hard refresh failures (revoked refresh token, missing OAuth client
	// config) deliberately leave LINEAR_ACCESS_TOKEN unset rather than
	// injecting a known-bad token: a missing env var causes the agent's
	// 143-tools to report "linear not configured", which is more honest
	// than a 401 from inside the agent's tool call.
	switch {
	case e.linearTokens != nil:
		token, err := e.linearTokens.GetValidAccessToken(ctx, orgID)
		switch {
		case err != nil:
			e.logger.Warn().Err(err).
				Str("org_id", orgID.String()).
				Msg("env: linear token resolution failed; sandbox will run without LINEAR_ACCESS_TOKEN until next reconnect")
		case token != "":
			merged["LINEAR_ACCESS_TOKEN"] = token
		}
	case ic.Linear != nil && ic.Linear.AccessToken != "":
		merged["LINEAR_ACCESS_TOKEN"] = ic.Linear.AccessToken
	}
	pagerDutyConfig, pagerDutyWritebackEnabled := e.resolvePagerDutyEnvConfig(ctx, orgID, ic.PagerDuty)
	if pagerDutyConfig != nil {
		if pagerDutyConfig.AccessToken != "" {
			merged["PAGERDUTY_ACCESS_TOKEN"] = pagerDutyConfig.AccessToken
			// Pin the region-correct API base URL so the in-sandbox provider
			// (registry_builder reads PAGERDUTY_API_URL) talks to the right
			// PagerDuty domain. Without this, EU-region tokens hit the US
			// endpoint and every incident tool call fails.
			merged["PAGERDUTY_API_URL"] = pagerDutyConfig.APIBaseURL()
			if pagerDutyWritebackEnabled {
				merged["PAGERDUTY_WRITEBACK_ENABLED"] = "true"
			} else {
				merged["PAGERDUTY_WRITEBACK_ENABLED"] = "false"
			}
		}
	}
	if ic.Notion != nil {
		if ic.Notion.AccessToken != "" {
			merged["NOTION_ACCESS_TOKEN"] = ic.Notion.AccessToken
		}
	}
	if ic.CircleCI != nil {
		if ic.CircleCI.AuthToken != "" {
			merged["CIRCLECI_TOKEN"] = ic.CircleCI.AuthToken
		}
		if ic.CircleCI.ProjectSlug != "" {
			merged["CIRCLECI_PROJECT_SLUG"] = ic.CircleCI.ProjectSlug
		}
	}
	if ic.Mezmo != nil {
		if ic.Mezmo.APIKey != "" {
			merged["MEZMO_API_KEY"] = ic.Mezmo.APIKey
		}
		if ic.Mezmo.BaseURL != "" {
			merged["MEZMO_BASE_URL"] = ic.Mezmo.BaseURL
		}
		if ic.Mezmo.Dataset != "" {
			merged["MEZMO_DATASET"] = ic.Mezmo.Dataset
		}
	}

	e.applyAgentModelDefault(ctx, orgID, agentType, modelOverride, merged)

	// Apply per-agent env overrides from org settings (agent_config.<type>.*).
	// Scoped to Amp and Pi only — these are non-secret runtime defaults
	// (AMP_MODE, PI_MODEL, PI_MODEL_CUSTOM), while auth itself comes from the
	// credential stores. Codex and Claude Code model defaults are applied
	// above without changing which credential key is active.
	if agentType == models.AgentTypeAmp || agentType == models.AgentTypePi {
		e.applyAgentConfigOverrides(ctx, orgID, agentType, merged)
	}

	if len(merged) == 0 {
		return nil
	}

	return merged
}

func (e *AgentEnv) applyAgentModelDefault(ctx context.Context, orgID uuid.UUID, agentType models.AgentType, modelOverride string, merged map[string]string) {
	envVar := models.ModelEnvVarForAgentType(agentType)
	if envVar == "" || merged[envVar] != "" {
		return
	}

	if model := strings.TrimSpace(modelOverride); model != "" {
		merged[envVar] = model
		return
	}
	if agentType == models.AgentTypeCodex || agentType == models.AgentTypeClaudeCode {
		if agentConfig, ok := e.loadAgentConfig(ctx, orgID, agentType); ok {
			if cfg := agentConfig[string(agentType)]; cfg != nil {
				if model := strings.TrimSpace(cfg[envVar]); model != "" {
					merged[envVar] = model
					return
				}
			}
		}
	}
	switch agentType {
	case models.AgentTypeCodex:
		merged[envVar] = models.DefaultCodexModel
	case models.AgentTypeClaudeCode:
		merged[envVar] = models.DefaultClaudeCodeModel
	}
}

// CheckAuth returns a user-facing error when an agent type has no chance of
// authenticating against its upstream because the required credential is
// missing from the resolved sandbox env. This is a pre-flight check intended
// to beat the generic "CLI exited 1" failure with something the user can act
// on — "configure Pi auth" instead of "pi: invalid api key".
//
// Invariant: callers must pass the already-merged sandbox env — i.e. after
// Resolve has run (which layers agent_config overrides on top of resolved
// provider creds) and after any per-run ModelOverride has been applied.
func (e *AgentEnv) CheckAuth(agentType models.AgentType, env map[string]string) error {
	if detail := env[internalAuthBlockedKey]; detail != "" {
		authErr := &AuthError{
			AgentType:                     agentType,
			Detail:                        detail,
			Provider:                      models.ProviderName(env[internalAuthBlockedProviderKey]),
			FallbackCandidatesUnavailable: env[internalAuthBlockedFallbackCandidatesUnavailableKey] == "true",
		}
		if rawUntil := env[internalAuthBlockedRateLimitedUntilKey]; rawUntil != "" {
			if until, err := time.Parse(time.RFC3339Nano, rawUntil); err == nil {
				authErr.RateLimitedUntil = &until
			}
		}
		return authErr
	}
	switch agentType {
	case models.AgentTypeAmp:
		if env["AMP_API_KEY"] == "" {
			return &AuthError{
				AgentType: agentType,
				Detail:    "missing AMP_API_KEY: configure Amp under Settings → Default Agent → Amp before starting a session",
			}
		}
	case models.AgentTypePi:
		if env["PI_API_KEY"] == "" {
			return &AuthError{
				AgentType: agentType,
				Detail:    "missing PI_API_KEY: configure Pi under Settings → Default Agent or My settings before starting a session",
			}
		}
	case models.AgentTypeOpenCode:
		if !hasOpenCodeRuntimeKey(env) {
			return &AuthError{
				AgentType: agentType,
				Detail:    "missing OpenCode credential: configure an explicit OpenCode API key under Settings → Default Agent or My settings before starting a session",
			}
		}
	}
	return nil
}

func applyOpenCodeEnv(env map[string]string, cfg models.OpenCodeConfig) {
	backing := cfg.NormalizedBackingProvider()
	env["OPENCODE_BACKING_PROVIDER"] = string(backing)
	env["OPENCODE_DISABLE_AUTOUPDATE"] = "true"
	env["OPENCODE_DISABLE_DEFAULT_PLUGINS"] = "true"
	env["OPENCODE_DISABLE_MODELS_FETCH"] = "true"
	env["OPENCODE_PERMISSION"] = `{"permission":"allow"}`
	if cfg.Model != "" {
		env["OPENCODE_MODEL"] = cfg.Model
	}
	env["OPENCODE_CONFIG_CONTENT"] = openCodeRuntimeConfigContent(cfg)
	switch backing {
	case models.ProviderAnthropic:
		env["ANTHROPIC_API_KEY"] = cfg.APIKey
		if cfg.BaseURL != "" {
			env["ANTHROPIC_BASE_URL"] = cfg.BaseURL
		}
	case models.ProviderOpenAI:
		env["OPENAI_API_KEY"] = cfg.APIKey
		if cfg.BaseURL != "" {
			env["OPENAI_BASE_URL"] = cfg.BaseURL
		}
	case models.ProviderGemini:
		env["GEMINI_API_KEY"] = cfg.APIKey
	case models.ProviderOpenRouter:
		env["OPENROUTER_API_KEY"] = cfg.APIKey
		if cfg.BaseURL != "" {
			env["OPENROUTER_BASE_URL"] = cfg.BaseURL
		}
	default:
		env["OPENCODE_API_KEY"] = cfg.APIKey
		if cfg.BaseURL != "" {
			env["OPENCODE_BASE_URL"] = cfg.BaseURL
		}
	}
}

func (e *AgentEnv) effectiveOpenCodeModel(ctx context.Context, orgID uuid.UUID, credentialModel string) string {
	if credentialModel != "" {
		return credentialModel
	}
	return e.openCodeModelFromAgentConfig(ctx, orgID)
}

func (e *AgentEnv) resolveOpenCodeProviderConfig(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, modelOverride string) models.ProviderConfig {
	selectionModel := strings.TrimSpace(modelOverride)
	if selectionModel == "" {
		selectionModel = e.openCodeModelFromAgentConfig(ctx, orgID)
	}
	targetBacking := openCodeBackingProviderForModel(selectionModel)
	if targetBacking == "" {
		return e.resolveProviderConfig(ctx, orgID, userID, models.ProviderOpenCode)
	}
	rowsByProvider, sawRows, ok := e.listCodingProviderRows(ctx, orgID, userID, []models.ProviderName{models.ProviderOpenCode})
	if !ok || !sawRows {
		return e.resolveProviderConfig(ctx, orgID, userID, models.ProviderOpenCode)
	}

	rows := append([]models.DecryptedCodingCredential(nil), rowsByProvider[models.ProviderOpenCode]...)
	sortCodingCredentialResolutionRows(rows)
	sawMatchingRows := false
	sawRateLimitedMatchingRows := false
	for _, cred := range rows {
		cfg, ok := cred.Config.(models.OpenCodeConfig)
		if !ok || cfg.APIKey == "" {
			continue
		}
		if cfg.NormalizedBackingProvider() != targetBacking {
			continue
		}
		sawMatchingRows = true
		if !credentialRunnableForModelAwarePick(cred) {
			if cred.RateLimitedUntil != nil && cred.RateLimitedUntil.After(time.Now()) {
				sawRateLimitedMatchingRows = true
			}
			continue
		}
		cfg.BackingProvider = cfg.NormalizedBackingProvider()
		e.recordCredentialPick(orgID, userID, models.ProviderOpenCode, cred)
		return cfg
	}

	if sawRateLimitedMatchingRows {
		e.recordCredentialBlock(orgID, userID, rateLimitBlockForProvider(models.ProviderOpenCode, rowsByProvider, []models.ProviderName{models.ProviderOpenCode}))
	} else if !sawMatchingRows {
		e.recordCredentialBlock(orgID, userID, missingOpenCodeBackingBlock(targetBacking))
	}
	return nil
}

func (e *AgentEnv) openCodeModelFromAgentConfig(ctx context.Context, orgID uuid.UUID) string {
	agentConfig, ok := e.loadAgentConfig(ctx, orgID, models.AgentTypeOpenCode)
	if !ok {
		return ""
	}
	envVars := agentConfig[string(models.AgentTypeOpenCode)]
	if envVars == nil {
		return ""
	}
	if custom := strings.TrimSpace(envVars["OPENCODE_MODEL_CUSTOM"]); custom != "" {
		return custom
	}
	return strings.TrimSpace(envVars["OPENCODE_MODEL"])
}

func openCodeBackingProviderForModel(model string) models.ProviderName {
	provider, _ := splitProviderModel(strings.TrimSpace(model))
	switch provider {
	case "anthropic":
		return models.ProviderAnthropic
	case "openai":
		return models.ProviderOpenAI
	case "google", "gemini":
		return models.ProviderGemini
	case "openrouter":
		return models.ProviderOpenRouter
	case "opencode":
		return models.ProviderOpenCode
	case "minimax", "moonshot", "qwen", "deepseek":
		// These providers are not directly backed by a first-party API; they
		// require an OpenRouter-backed OpenCode key to access.
		return models.ProviderOpenRouter
	default:
		return ""
	}
}

func credentialRunnableForModelAwarePick(cred models.DecryptedCodingCredential) bool {
	if cred.Status != models.CodingCredentialStatusActive {
		return false
	}
	return cred.RateLimitedUntil == nil || !cred.RateLimitedUntil.After(time.Now())
}

func missingOpenCodeBackingBlock(backing models.ProviderName) credentialBlock {
	label := openCodeBackingProviderLabel(backing)
	return credentialBlock{
		provider: models.ProviderOpenCode,
		detail:   fmt.Sprintf("missing OpenCode credential for %s. Add an OpenCode via %s auth or choose a model backed by an existing OpenCode key.", label, label),
	}
}

func openCodeBackingProviderLabel(backing models.ProviderName) string {
	switch backing {
	case models.ProviderAnthropic:
		return "Anthropic"
	case models.ProviderOpenAI:
		return "OpenAI"
	case models.ProviderGemini:
		return "Gemini"
	case models.ProviderOpenRouter:
		return "OpenRouter"
	case models.ProviderOpenCode:
		return "OpenCode native"
	default:
		return string(backing)
	}
}

func (e *AgentEnv) updateRuntimeCredentialBindingModel(orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName, model string) {
	if e == nil || model == "" {
		return
	}
	key := pickKey{orgID: orgID, provider: provider}
	if userID != nil {
		key.userID = *userID
	}
	e.recentPicksMu.Lock()
	defer e.recentPicksMu.Unlock()
	rec, ok := e.recentPicks[key]
	if !ok || time.Since(rec.at) > pickTrackerTTL {
		if ok {
			delete(e.recentPicks, key)
		}
		return
	}
	rec.binding.EffectiveModel = model
	e.recentPicks[key] = rec
}

type openCodeProviderConfig struct {
	Options map[string]any                 `json:"options"`
	Models  map[string]openCodeModelConfig `json:"models,omitempty"`
}

type openCodeModelConfig struct {
	Options map[string]any `json:"options,omitempty"`
}

type openCodeRuntimeConfig struct {
	Schema           string                            `json:"$schema"`
	Permission       string                            `json:"permission"`
	Model            string                            `json:"model,omitempty"`
	EnabledProviders []string                          `json:"enabled_providers,omitempty"`
	Provider         map[string]openCodeProviderConfig `json:"provider,omitempty"`
}

func openCodeRuntimeConfigContent(cfg models.OpenCodeConfig) string {
	providerID, apiKeyEnv := openCodeProviderConfigIDAndKey(cfg.NormalizedBackingProvider())
	options := map[string]any{"apiKey": "{env:" + apiKeyEnv + "}"}
	if cfg.BaseURL != "" {
		options["baseURL"] = cfg.BaseURL
	}
	providerConfig := openCodeProviderConfig{Options: options}
	if cfg.NormalizedBackingProvider() == models.ProviderOpenRouter {
		providerConfig.Models = openCodeOpenRouterModelConfigs(cfg.Model)
	}
	payload := openCodeRuntimeConfig{
		Schema:           "https://opencode.ai/config.json",
		Permission:       "allow",
		Model:            cfg.Model,
		EnabledProviders: []string{providerID},
		Provider: map[string]openCodeProviderConfig{
			providerID: providerConfig,
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return `{"permission":"allow"}`
	}
	return string(data)
}

// Audited against OpenRouter endpoint lists and provider-company locations on
// 2026-06-26. Keys use OpenCode's "~upstream/provider-model" custom model key
// format for model IDs that contain slashes. Keep
// docs/design/implemented/95-opencode-agent-adapter.md in sync when changing
// this map.
var auditedUSOpenRouterModelProviders = map[string][]string{
	"~anthropic/claude-fable-5":      {"anthropic", "amazon-bedrock/us", "azure"},
	"~deepseek/deepseek-v4-flash":    {"deepinfra", "cloudflare", "fireworks"},
	"~deepseek/deepseek-v4-pro":      {"deepinfra", "together", "fireworks"},
	"~google/gemini-3.1-pro-preview": {"google-ai-studio", "google-vertex/global"},
	"~google/gemini-3.5-flash":       {"google-ai-studio", "google-vertex/global"},
	"~minimax/minimax-m2.5":          {"deepinfra", "digitalocean", "parasail"},
	"~minimax/minimax-m2.7":          {"deepinfra", "fireworks", "together"},
	"~moonshotai/kimi-k2.5":          {"digitalocean", "deepinfra"},
	"~moonshotai/kimi-k2.6":          {"deepinfra", "baseten", "fireworks"},
	"~openai/gpt-5.2":                {"openai", "azure"},
	"~openai/gpt-5.5":                {"openai", "azure"},
	// OpenRouter currently exposes only OpenAI for GPT 5.5 Pro.
	"~openai/gpt-5.5-pro": {"openai"},
	"~z-ai/glm-5.1":       {"deepinfra", "baseten", "together"},
	"~z-ai/glm-5.2":       {"deepinfra", "together", "fireworks"},
}

func openCodeOpenRouterModelConfigs(model string) map[string]openCodeModelConfig {
	modelKey, ok := openCodeOpenRouterModelKey(model)
	if !ok {
		return nil
	}
	providers := auditedUSOpenRouterModelProviders[modelKey]
	if len(providers) == 0 {
		return nil
	}
	return map[string]openCodeModelConfig{
		modelKey: {
			Options: map[string]any{
				"provider": map[string]any{
					"only":               providers,
					"order":              providers,
					"allow_fallbacks":    false,
					"data_collection":    "deny",
					"require_parameters": true,
				},
			},
		},
	}
}

func openCodeOpenRouterModelKey(model string) (string, bool) {
	const prefix = "openrouter/"
	upstreamModel, ok := strings.CutPrefix(model, prefix)
	if !ok || !strings.Contains(upstreamModel, "/") {
		return "", false
	}
	return "~" + upstreamModel, true
}

func openCodeProviderConfigIDAndKey(provider models.ProviderName) (string, string) {
	switch provider {
	case models.ProviderAnthropic:
		return "anthropic", "ANTHROPIC_API_KEY"
	case models.ProviderOpenAI:
		return "openai", "OPENAI_API_KEY"
	case models.ProviderGemini:
		return "google", "GEMINI_API_KEY"
	case models.ProviderOpenRouter:
		return "openrouter", "OPENROUTER_API_KEY"
	default:
		return "opencode", "OPENCODE_API_KEY"
	}
}

func hasOpenCodeRuntimeKey(env map[string]string) bool {
	return env["OPENCODE_API_KEY"] != "" ||
		env["ANTHROPIC_API_KEY"] != "" ||
		env["OPENAI_API_KEY"] != "" ||
		env["GEMINI_API_KEY"] != "" ||
		env["OPENROUTER_API_KEY"] != ""
}

func setAuthBlockedEnv(env map[string]string, block credentialBlock) {
	env[internalAuthBlockedKey] = block.detail
	if block.provider != "" {
		env[internalAuthBlockedProviderKey] = string(block.provider)
	}
	if block.rateLimitedUntil != nil {
		env[internalAuthBlockedRateLimitedUntilKey] = block.rateLimitedUntil.Format(time.RFC3339Nano)
	}
	if block.fallbackCandidatesUnavailable {
		env[internalAuthBlockedFallbackCandidatesUnavailableKey] = "true"
	}
}

// applyAgentConfigOverrides layers agent_config.<agentType>.* entries from org
// settings on top of the already-resolved provider credentials in `merged`.
// Only called for Amp and Pi; agent_config stores their non-secret runtime
// defaults while auth is resolved from the credential stores. Non-empty values
// win over any prior env value.
//
// Reads go through OrgSettingsCache when configured so a burst of Amp/Pi
// session starts for the same org amortizes to one DB lookup per TTL window.
// The settings update handler invalidates the cache after a write, so
// configuration changes take effect immediately rather than waiting for the
// TTL to expire.
func (e *AgentEnv) applyAgentConfigOverrides(ctx context.Context, orgID uuid.UUID, agentType models.AgentType, merged map[string]string) {
	agentConfig, ok := e.loadAgentConfig(ctx, orgID, agentType)
	if !ok {
		return
	}
	for k, v := range agentConfig[string(agentType)] {
		if v != "" {
			merged[k] = v
		}
	}
}

// loadAgentConfig returns the org's AgentEnvConfig, using the configured
// OrgSettingsCache as a front when present. Returns (nil, false) and logs a
// warning if the org can't be loaded; callers should treat that as "no
// overrides available" rather than failing the session start.
func (e *AgentEnv) loadAgentConfig(ctx context.Context, orgID uuid.UUID, agentType models.AgentType) (models.AgentEnvConfig, bool) {
	if e.orgs == nil {
		e.logger.Warn().
			Str("agent_type", string(agentType)).
			Str("org_id", orgID.String()).
			Msg("agent env helper has no orgs store; skipping agent_config overrides (agent may run without auth)")
		return nil, false
	}

	if e.orgSettingsCache != nil {
		if cached, hit := e.orgSettingsCache.Get(orgID); hit {
			return cached, true
		}
	}

	org, err := e.orgs.GetByID(ctx, orgID)
	if err != nil {
		e.logger.Warn().
			Err(err).
			Str("agent_type", string(agentType)).
			Str("org_id", orgID.String()).
			Msg("failed to load org for agent_config overrides; agent may run without auth")
		return nil, false
	}
	orgSettings, parseErr := models.ParseOrgSettings(org.Settings)
	if parseErr != nil {
		e.logger.Warn().
			Err(parseErr).
			Str("agent_type", string(agentType)).
			Str("org_id", orgID.String()).
			Msg("failed to parse org settings for agent_config overrides; agent may run without auth")
		return nil, false
	}

	if e.orgSettingsCache != nil {
		// Store the (possibly nil) AgentConfig so a second hit doesn't
		// re-fetch just to discover the org has no agent_config.
		e.orgSettingsCache.Set(orgID, orgSettings.AgentConfig)
	}

	return orgSettings.AgentConfig, true
}

// resolveProviderConfig returns the best ProviderConfig for a provider.
//
// The unified `coding_credentials` table is the only credential source (see
// docs/design/future/65-unified-coding-credentials.md):
// CodingCredentialProvider.ListResolvable returns one ordered list
// (personal-then-org, priority-within-scope) covering both API-key and
// subscription rows, and the first runnable row wins. With no unified store
// wired (older test rigs) or no runnable rows, the resolver returns nil.
func (e *AgentEnv) resolveProviderConfig(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName) models.ProviderConfig {
	cfg, _ := e.resolveFromCodingCredentials(ctx, orgID, userID, provider)
	return cfg
}

// resolveFromCodingCredentials walks the unified resolver result, plus its
// subscription twin for providers that have one. The twin lookup is what
// lets a Claude Code subscription row (provider=anthropic_subscription) be
// found when a caller asks for a `claude_code` agent that resolves to
// ProviderAnthropic — API keys and subscriptions are two distinct provider
// partitions on the unified table.
func (e *AgentEnv) resolveFromCodingCredentials(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName) (models.ProviderConfig, bool) {
	if e.codingCredentials == nil {
		return nil, false
	}

	providers := []models.ProviderName{provider}
	if twin := unifiedSubscriptionTwin(provider); twin != "" {
		providers = append(providers, twin)
	}
	if cfg, _, sawRows := e.pickFromCodingProviderSet(ctx, orgID, userID, provider, providers); cfg != nil || sawRows {
		return cfg, sawRows
	}
	return nil, false
}

func (e *AgentEnv) pickFromCodingProviderSet(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, requestedProvider models.ProviderName, providers []models.ProviderName) (models.ProviderConfig, *models.DecryptedCodingCredential, bool) {
	rowsByProvider, sawRows, ok := e.listCodingProviderRows(ctx, orgID, userID, providers)
	if !ok {
		// Lookup errored. Report "not handled" rather than "unified
		// authoritative with zero rows" so callers can distinguish a
		// transient pgx error from a genuinely empty credential stack.
		return nil, nil, false
	}
	if !sawRows {
		return nil, nil, true
	}

	if picker, ok := e.codingCredentials.(CodingCredentialMultiPicker); ok {
		picked, pickErr := picker.PickRunnableMulti(ctx, models.Scope{OrgID: orgID, UserID: userID}, providers)
		if pickErr != nil {
			// pickErr discriminates between "no candidate exists" (config
			// error) and "every candidate is currently shed" (transient) via
			// db.ErrCodingCredentialNotFound vs db.ErrAllCredentialsShed.
			// When the whole stack is rate-limited, record a structured block
			// so CheckAuth can produce a clear user-facing continue-session
			// failure instead of a generic missing-key error.
			e.logger.Warn().Err(pickErr).Str("provider", string(requestedProvider)).Msg("coding credential picker found no eligible credential")
			if isAllCredentialsShedError(pickErr) {
				e.recordCredentialBlock(orgID, userID, rateLimitBlockForProvider(requestedProvider, rowsByProvider, providers))
			}
			return nil, nil, true
		}
		if picked == nil {
			return nil, nil, true
		}
		if cfg, ok := compatibleCodingProviderConfig(picked.Provider, picked.Config); ok {
			e.recordCredentialPick(orgID, userID, picked.Provider, *picked)
			if picked.Provider != requestedProvider {
				e.recordCredentialPick(orgID, userID, requestedProvider, *picked)
			}
			return cfg, picked, true
		}
		return nil, picked, true
	}

	creds := make([]models.DecryptedCodingCredential, 0)
	for _, provider := range providers {
		creds = append(creds, rowsByProvider[provider]...)
	}
	sortCodingCredentialResolutionRows(creds)
	for _, cred := range creds {
		if cred.Status != models.CodingCredentialStatusActive {
			continue
		}
		if cfg, ok := compatibleCodingProviderConfig(cred.Provider, cred.Config); ok {
			e.recordCredentialPick(orgID, userID, cred.Provider, cred)
			if cred.Provider != requestedProvider {
				e.recordCredentialPick(orgID, userID, requestedProvider, cred)
			}
			picked := cred
			return cfg, &picked, true
		}
	}
	return nil, nil, true
}

func (e *AgentEnv) recordCredentialBlock(orgID uuid.UUID, userID *uuid.UUID, block credentialBlock) {
	if block.detail == "" || block.provider == "" {
		return
	}
	key := pickKey{orgID: orgID, provider: block.provider}
	if userID != nil {
		key.userID = *userID
	}
	e.credentialBlocksMu.Lock()
	defer e.credentialBlocksMu.Unlock()
	if e.credentialBlocks == nil {
		e.credentialBlocks = make(map[pickKey]credentialBlock)
	}
	block.at = time.Now()
	e.credentialBlocks[key] = block
}

func isAllCredentialsShedError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "all eligible coding credentials are currently shed")
}

func (e *AgentEnv) lookupCredentialBlock(orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName) (credentialBlock, bool) {
	key := pickKey{orgID: orgID, provider: provider}
	if userID != nil {
		key.userID = *userID
	}
	e.credentialBlocksMu.Lock()
	defer e.credentialBlocksMu.Unlock()
	block, ok := e.credentialBlocks[key]
	if !ok {
		return credentialBlock{}, false
	}
	if time.Since(block.at) > pickTrackerTTL {
		delete(e.credentialBlocks, key)
		return credentialBlock{}, false
	}
	return block, true
}

func rateLimitBlockForProvider(provider models.ProviderName, rowsByProvider map[models.ProviderName][]models.DecryptedCodingCredential, providers []models.ProviderName) credentialBlock {
	var earliest *time.Time
	rateLimitedCandidates := 0
	now := time.Now()
	for _, p := range providers {
		for _, cred := range rowsByProvider[p] {
			if cred.RateLimitedUntil == nil || !cred.RateLimitedUntil.After(now) {
				continue
			}
			rateLimitedCandidates++
			if earliest == nil || cred.RateLimitedUntil.Before(*earliest) {
				t := *cred.RateLimitedUntil
				earliest = &t
			}
		}
	}
	label := agentLabelForProvider(provider)
	block := credentialBlock{
		provider:                      provider,
		rateLimitedUntil:              earliest,
		fallbackCandidatesUnavailable: rateLimitedCandidates > 1,
	}
	if earliest != nil {
		block.detail = fmt.Sprintf("all %s auths are rate limited until %s. Try again then or add another %s auth.", label, earliest.Format(time.Kitchen), label)
		return block
	}
	block.detail = fmt.Sprintf("all %s auths are temporarily unavailable. Try again shortly or add another %s auth.", label, label)
	return block
}

func agentLabelForProvider(provider models.ProviderName) string {
	switch provider {
	case models.ProviderOpenAI:
		return "Codex"
	case models.ProviderAnthropic:
		return "Claude Code"
	case models.ProviderGemini:
		return "Gemini"
	case models.ProviderAmp:
		return "Amp"
	case models.ProviderPi:
		return "Pi"
	case models.ProviderOpenCode:
		return "OpenCode"
	default:
		return string(provider)
	}
}

func (e *AgentEnv) listCodingProviderRows(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, providers []models.ProviderName) (map[models.ProviderName][]models.DecryptedCodingCredential, bool, bool) {
	rowsByProvider := make(map[models.ProviderName][]models.DecryptedCodingCredential, len(providers))
	sawRows := false
	for _, provider := range providers {
		creds, err := e.codingCredentials.ListResolvable(ctx, orgID, userID, provider)
		if err != nil {
			e.logger.Warn().Err(err).Str("provider", string(provider)).Msg("coding credential resolver lookup failed")
			return nil, false, false
		}
		if len(creds) > 0 {
			sawRows = true
		}
		rowsByProvider[provider] = creds
	}
	return rowsByProvider, sawRows, true
}

func sortCodingCredentialResolutionRows(creds []models.DecryptedCodingCredential) {
	sort.SliceStable(creds, func(i, j int) bool {
		leftPersonal := creds[i].UserID != nil
		rightPersonal := creds[j].UserID != nil
		if leftPersonal != rightPersonal {
			return leftPersonal
		}
		if creds[i].Priority != creds[j].Priority {
			return creds[i].Priority < creds[j].Priority
		}
		if !creds[i].CreatedAt.Equal(creds[j].CreatedAt) {
			return creds[i].CreatedAt.Before(creds[j].CreatedAt)
		}
		return false
	})
}

// unifiedSubscriptionTwin returns the new subscription provider name for an
// API-key provider, or "" if there is no subscription flavor. Lets the
// resolver answer "give me an Anthropic config" with either an API key
// (provider=anthropic) or a subscription token (provider=anthropic_subscription).
func unifiedSubscriptionTwin(provider models.ProviderName) models.ProviderName {
	switch provider {
	case models.ProviderAnthropic:
		return models.ProviderAnthropicSubscription
	case models.ProviderOpenAI:
		return models.ProviderOpenAISubscription
	default:
		return ""
	}
}

// compatibleCodingProviderConfig returns the runtime ProviderConfig that
// matches the given (provider, stored config) pair, or (nil, false) when the
// pair is not usable: the provider is unknown, the type assertion fails, the
// blob is missing required credentials, or the row is structurally
// incompatible (e.g. an Anthropic API-key row with a Subscription set).
//
// The explicit `ok` return makes the unknown-provider case impossible to
// confuse with the "valid provider but empty config" case at call sites.
func compatibleCodingProviderConfig(provider models.ProviderName, cfg models.ProviderConfig) (models.ProviderConfig, bool) {
	switch provider {
	case models.ProviderAnthropic:
		anthropic, ok := cfg.(models.AnthropicConfig)
		if !ok || anthropic.APIKey == "" {
			return nil, false
		}
		return anthropic, true
	case models.ProviderOpenAI:
		openAI, ok := cfg.(models.OpenAIConfig)
		if !ok || openAI.APIKey == "" {
			return nil, false
		}
		return openAI, true
	case models.ProviderAnthropicSubscription:
		sub, ok := cfg.(models.AnthropicSubscriptionConfig)
		if !ok {
			return nil, false
		}
		if sub.IsSetupToken() {
			if sub.OAuthToken == "" || sub.IsExpired() {
				return nil, false
			}
			return models.AnthropicSubscriptionConfig{
				AuthMode:            models.AnthropicSubscriptionAuthModeSetupToken,
				OAuthToken:          sub.OAuthToken,
				OAuthTokenExpiresAt: sub.OAuthTokenExpiresAt,
				AccountType:         sub.AccountType,
				RateLimitTier:       sub.RateLimitTier,
				Scopes:              sub.Scopes,
			}, true
		}
		if sub.AccessToken == "" || sub.RefreshToken == "" {
			return nil, false
		}
		// Drop PKCE-only fields (State, CodeVerifier, AuthorizeURL) when
		// constructing the runtime config. They are pre-completion artifacts;
		// the Status='active' filter upstream already excludes pending rows,
		// but re-asserting their absence here keeps the runtime config minimal
		// in case that filter ever loosens.
		return models.AnthropicSubscriptionConfig{
			AccessToken:   sub.AccessToken,
			RefreshToken:  sub.RefreshToken,
			ExpiresAt:     sub.ExpiresAt,
			AccountType:   sub.AccountType,
			RateLimitTier: sub.RateLimitTier,
			Scopes:        sub.Scopes,
		}, true
	case models.ProviderOpenAISubscription:
		sub, ok := cfg.(models.OpenAISubscriptionConfig)
		if !ok || sub.AccessToken == "" || sub.RefreshToken == "" {
			return nil, false
		}
		// Strip device-code pending fields (DeviceAuthID, UserCode,
		// VerificationURI, PollInterval) when constructing the runtime
		// config. The Status='active' filter upstream already excludes
		// pending rows, but re-asserting their absence here keeps the
		// runtime config minimal in case that filter ever loosens.
		return models.OpenAISubscriptionConfig{
			AccessToken:  sub.AccessToken,
			RefreshToken: sub.RefreshToken,
			IDToken:      sub.IDToken,
			ExpiresAt:    sub.ExpiresAt,
			AccountType:  sub.AccountType,
		}, true
	case models.ProviderGemini:
		gemini, ok := cfg.(models.GeminiConfig)
		if !ok || gemini.APIKey == "" {
			return nil, false
		}
		return gemini, true
	case models.ProviderOpenRouter:
		openRouter, ok := cfg.(models.OpenRouterConfig)
		if !ok || openRouter.APIKey == "" {
			return nil, false
		}
		return openRouter, true
	case models.ProviderAmp:
		amp, ok := cfg.(models.AmpConfig)
		if !ok || amp.APIKey == "" {
			return nil, false
		}
		return amp, true
	case models.ProviderPi:
		pi, ok := cfg.(models.PiConfig)
		if !ok || pi.APIKey == "" {
			return nil, false
		}
		return pi, true
	case models.ProviderOpenCode:
		openCode, ok := cfg.(models.OpenCodeConfig)
		if !ok || openCode.APIKey == "" {
			return nil, false
		}
		openCode.BackingProvider = openCode.NormalizedBackingProvider()
		return openCode, true
	default:
		return nil, false
	}
}

// InjectCodexAuth writes a ~/.codex/auth.json file into the sandbox if a
// ChatGPT OAuth token exists for this org. This is the primary Codex auth
// mechanism — auth.json tells the CLI to use the ChatGPT backend which
// accepts the OAuth token without needing api.responses.write scope. Returns
// (true, nil) if auth was injected, (false, nil) if no OAuth token is
// available, or (false, err) on failure.
func (e *AgentEnv) InjectCodexAuth(ctx context.Context, orgID uuid.UUID, sandbox *Sandbox) (bool, error) {
	return e.InjectCodexAuthForUser(ctx, orgID, nil, sandbox)
}

func (e *AgentEnv) InjectCodexAuthForUser(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, sandbox *Sandbox) (bool, error) {
	if e.codingCredentials != nil {
		if picked, ok := e.lookupRecentCredential(orgID, userID, models.ProviderOpenAI); ok {
			if chatGPT, ok := codexSubscriptionConfigFromPicked(picked); ok {
				if picked.Provider == models.ProviderOpenAISubscription {
					// Refresh against the picked row's actual scope —
					// personal credentials carry UserID, org rows do not.
					refreshed, err := e.refreshCodexSubscriptionIfNeeded(ctx, models.Scope{OrgID: orgID, UserID: picked.UserID}, picked.ID, chatGPT)
					if err != nil {
						return false, err
					}
					chatGPT = *refreshed
				}
				return e.writeCodexAuth(ctx, orgID, sandbox, chatGPT)
			}
			return false, nil
		}
		cfg, picked, handled := e.pickFromCodingProviderSet(ctx, orgID, userID, models.ProviderOpenAI, []models.ProviderName{
			models.ProviderOpenAI,
			models.ProviderOpenAISubscription,
		})
		if handled {
			if chatGPT, ok := cfg.(models.OpenAISubscriptionConfig); ok {
				if picked != nil && picked.Provider == models.ProviderOpenAISubscription {
					refreshed, err := e.refreshCodexSubscriptionIfNeeded(ctx, models.Scope{OrgID: orgID, UserID: picked.UserID}, picked.ID, chatGPT)
					if err != nil {
						return false, err
					}
					chatGPT = *refreshed
				}
				return e.writeCodexAuth(ctx, orgID, sandbox, chatGPT)
			}
			return false, nil
		}
	}

	if e.codexAuth == nil {
		return false, nil
	}

	// Use round-robin selection across all active subscriptions for this org.
	// GetValidToken claims the least-recently-used credential, refreshing it
	// in-band if it's near expiry. This is the canonical path; the legacy
	// single-credential RefreshToken would always pick the same row and
	// bypass round-robin entirely.
	cfg, err := e.codexAuth.GetValidToken(ctx, orgID)
	if err != nil {
		return false, maybeWrapCodexAuthInvalid(e.codexAuth, fmt.Errorf("get codex auth token: %w", err))
	}
	if cfg == nil {
		// No OAuth token — not an error, agent will use API key.
		return false, nil
	}
	return e.writeCodexAuth(ctx, orgID, sandbox, *cfg)
}

func codexSubscriptionConfigFromPicked(picked models.DecryptedCodingCredential) (models.OpenAISubscriptionConfig, bool) {
	cfg, ok := compatibleCodingProviderConfig(picked.Provider, picked.Config)
	if !ok {
		return models.OpenAISubscriptionConfig{}, false
	}
	chatGPT, ok := cfg.(models.OpenAISubscriptionConfig)
	return chatGPT, ok
}

func (e *AgentEnv) refreshCodexSubscriptionIfNeeded(ctx context.Context, scope models.Scope, credID uuid.UUID, cfg models.OpenAISubscriptionConfig) (*models.OpenAISubscriptionConfig, error) {
	if !cfg.NeedsRefresh(codexSubscriptionRefreshWindow) {
		return &cfg, nil
	}

	refresher, ok := e.codexAuth.(CodexAuthRefresher)
	if !ok {
		if !cfg.IsExpired() {
			e.logger.Warn().
				Str("cred_id", credID.String()).
				Msg("codex subscription needs refresh but no refresher is configured; using cached token")
			return &cfg, nil
		}
		return nil, wrapCodexAuthInvalid(fmt.Errorf("codex subscription %s is expired and no refresh provider is configured", credID))
	}

	refreshed, err := refresher.RefreshTokenByID(ctx, scope, credID)
	if err != nil {
		if !cfg.IsExpired() {
			e.logger.Warn().
				Err(err).
				Str("cred_id", credID.String()).
				Msg("codex subscription refresh failed; using cached token")
			return &cfg, nil
		}
		return nil, maybeWrapCodexAuthInvalid(refresher, fmt.Errorf("refresh codex subscription %s: %w", credID, err))
	}
	if refreshed == nil {
		if !cfg.IsExpired() {
			e.logger.Warn().
				Str("cred_id", credID.String()).
				Msg("codex subscription refresh returned no token; using cached token")
			return &cfg, nil
		}
		return nil, wrapCodexAuthInvalid(fmt.Errorf("refresh codex subscription %s returned no token", credID))
	}
	return refreshed, nil
}

func (e *AgentEnv) writeCodexAuth(ctx context.Context, orgID uuid.UUID, sandbox *Sandbox, cfg models.OpenAISubscriptionConfig) (bool, error) {
	// Omit the refresh_token from auth.json so the Codex CLI never attempts
	// to refresh the token itself. If the CLI refreshes the token inside the
	// sandbox, it consumes the refresh_token on OpenAI's servers, but the
	// sandbox-side token is lost when the container is destroyed. Our DB
	// then holds a stale refresh_token, and the next turn's RefreshToken()
	// call gets refresh_token_reused. By omitting refresh_token, the CLI
	// uses the fresh access_token (15-min TTL) as-is, and our server retains
	// sole ownership of the refresh_token for future turns.
	authJSON, err := json.Marshal(map[string]interface{}{
		"auth_mode": "chatgpt",
		"tokens": map[string]string{
			"access_token":  cfg.AccessToken,
			"refresh_token": "",
			"id_token":      cfg.IDToken,
		},
		"last_refresh": time.Now().Format(time.RFC3339),
	})
	if err != nil {
		return false, fmt.Errorf("marshal auth.json: %w", err)
	}

	// Write auth.json under $HOME/.codex. The sandbox env sets HOME to the
	// sandbox user's home dir (see RunAgent's sandbox setup) so the Codex
	// CLI resolves ~/.codex/auth.json to this path.
	authDir := path.Join(sandbox.HomeDir, ".codex")
	mkdirCmd := fmt.Sprintf("mkdir -p '%s'", shellEscapeSingleQuote(authDir))

	var mkdirOut, mkdirErr bytes.Buffer
	exitCode, err := e.provider.Exec(ctx, sandbox, mkdirCmd, &mkdirOut, &mkdirErr)
	if err != nil {
		return false, fmt.Errorf("create .codex dir: %w", err)
	}
	if exitCode != 0 {
		return false, fmt.Errorf("create .codex dir: mkdir exited with code %d: %s", exitCode, mkdirErr.String())
	}

	authPath := authDir + "/auth.json"
	if err := e.provider.WriteFile(ctx, sandbox, authPath, authJSON); err != nil {
		return false, fmt.Errorf("write auth.json: %w", err)
	}

	// Write config.toml to disable Codex's internal bwrap sandboxing. The
	// container is already isolated by Docker + gVisor so bwrap is redundant
	// and fails because gVisor doesn't support the unprivileged user
	// namespaces that bwrap requires.
	configTOML := []byte("sandbox_mode = \"danger-full-access\"\n")
	configPath := authDir + "/config.toml"
	if err := e.provider.WriteFile(ctx, sandbox, configPath, configTOML); err != nil {
		return false, fmt.Errorf("write config.toml: %w", err)
	}

	e.logger.Debug().
		Str("org_id", orgID.String()).
		Msg("injected codex auth.json and config.toml into sandbox")

	return true, nil
}

// InjectClaudeCodeAuth writes ~/.claude/.credentials.json when a Claude Code
// subscription credential is selected. API-key credentials intentionally return
// false so callers can use ANTHROPIC_API_KEY after removing any stale file.
func (e *AgentEnv) InjectClaudeCodeAuth(ctx context.Context, orgID uuid.UUID, sandbox *Sandbox) (bool, error) {
	return e.InjectClaudeCodeAuthWithEnv(ctx, orgID, sandbox, nil)
}

func (e *AgentEnv) InjectClaudeCodeAuthWithEnv(ctx context.Context, orgID uuid.UUID, sandbox *Sandbox, env map[string]string) (bool, error) {
	return e.InjectClaudeCodeAuthForUserWithEnv(ctx, orgID, nil, sandbox, env)
}

func (e *AgentEnv) InjectClaudeCodeAuthForUser(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, sandbox *Sandbox) (bool, error) {
	return e.InjectClaudeCodeAuthForUserWithEnv(ctx, orgID, userID, sandbox, nil)
}

func (e *AgentEnv) InjectClaudeCodeAuthForUserWithEnv(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, sandbox *Sandbox, env map[string]string) (bool, error) {
	if e == nil {
		return false, nil
	}
	model := ""
	if env != nil {
		model = env[models.ModelEnvVarForAgentType(models.AgentTypeClaudeCode)]
	}
	if e.codingCredentials != nil {
		if picked, ok := e.lookupRecentCredential(orgID, userID, models.ProviderAnthropic); ok {
			return e.injectPickedClaudeCodeAuthWithEnv(ctx, orgID, sandbox, picked, model, env)
		}
		_, picked, handled := e.pickFromCodingProviderSet(ctx, orgID, userID, models.ProviderAnthropic, []models.ProviderName{
			models.ProviderAnthropic,
			models.ProviderAnthropicSubscription,
		})
		if handled {
			if picked == nil {
				return false, nil
			}
			return e.injectPickedClaudeCodeAuthWithEnv(ctx, orgID, sandbox, *picked, model, env)
		}
	}

	if e.claudeCodeAuth == nil {
		return false, nil
	}
	sub, _, err := e.claudeCodeAuth.GetValidToken(ctx, orgID)
	if err != nil {
		return false, fmt.Errorf("get claude code subscription token: %w", err)
	}
	if sub == nil {
		return false, nil
	}
	return e.writeClaudeCodeAuth(ctx, orgID, sandbox, *sub, model)
}

func (e *AgentEnv) injectPickedClaudeCodeAuthWithEnv(ctx context.Context, orgID uuid.UUID, sandbox *Sandbox, picked models.DecryptedCodingCredential, model string, env map[string]string) (bool, error) {
	if picked.Provider != models.ProviderAnthropicSubscription {
		return false, nil
	}
	cfg, ok := picked.Config.(models.AnthropicSubscriptionConfig)
	if !ok {
		return false, nil
	}
	if cfg.IsSetupToken() {
		if cfg.OAuthToken == "" || cfg.IsExpired() {
			return false, nil
		}
		if env == nil {
			return false, fmt.Errorf("sandbox env is required for Claude Code setup-token auth")
		}
		if err := e.RemoveClaudeCodeCredentialsFile(ctx, sandbox); err != nil {
			return false, err
		}
		env["CLAUDE_CODE_OAUTH_TOKEN"] = cfg.OAuthToken
		delete(env, "ANTHROPIC_API_KEY")
		delete(env, "ANTHROPIC_AUTH_TOKEN")
		version := e.detectClaudeCodeVersion(ctx, sandbox)
		setClaudeCodePermissionMode(sandbox, claudeCodePermissionModeForAuth(TokenBillingModeSubscription, cfg.AccountType, model, version))
		return true, nil
	}
	if cfg.AccessToken == "" || cfg.RefreshToken == "" {
		return false, nil
	}
	sub := models.AnthropicSubscription{
		AccessToken:   cfg.AccessToken,
		RefreshToken:  cfg.RefreshToken,
		ExpiresAt:     cfg.ExpiresAt,
		AccountType:   cfg.AccountType,
		RateLimitTier: cfg.RateLimitTier,
		Scopes:        cfg.Scopes,
	}
	if sub.NeedsRefresh(codexSubscriptionRefreshWindow) {
		refresher, ok := e.claudeCodeAuth.(ClaudeCodeAuthRefresher)
		if ok {
			scope := models.Scope{OrgID: orgID, UserID: picked.UserID}
			refreshed, err := refresher.RefreshTokenByID(ctx, scope, picked.ID)
			if err == nil && refreshed != nil {
				sub = *refreshed
			} else if sub.IsExpired() {
				if err != nil {
					return false, fmt.Errorf("refresh unified claude subscription %s: %w", picked.ID, err)
				}
				return false, fmt.Errorf("refresh unified claude subscription %s returned no token", picked.ID)
			} else if err != nil {
				e.logger.Warn().
					Err(err).
					Str("cred_id", picked.ID.String()).
					Msg("unified claude subscription refresh failed; using cached token")
			}
		} else if sub.IsExpired() {
			return false, fmt.Errorf("unified claude subscription %s is expired and no refresh provider is configured", picked.ID)
		}
	}
	return e.writeClaudeCodeAuth(ctx, orgID, sandbox, sub, model)
}

func (e *AgentEnv) writeClaudeCodeAuth(ctx context.Context, orgID uuid.UUID, sandbox *Sandbox, sub models.AnthropicSubscription, model string) (bool, error) {
	if e.provider == nil {
		return false, fmt.Errorf("sandbox provider is required to write claude credentials")
	}
	oauthPayload := map[string]interface{}{
		"accessToken":  sub.AccessToken,
		"refreshToken": sub.RefreshToken,
		"expiresAt":    sub.ExpiresAt.UnixMilli(),
	}
	if len(sub.Scopes) > 0 {
		oauthPayload["scopes"] = sub.Scopes
	}
	if sub.AccountType != "" {
		oauthPayload["subscriptionType"] = sub.AccountType
	}
	if sub.RateLimitTier != "" {
		oauthPayload["rateLimitTier"] = sub.RateLimitTier
	}
	credsJSON, err := json.Marshal(map[string]interface{}{"claudeAiOauth": oauthPayload})
	if err != nil {
		return false, fmt.Errorf("marshal claude credentials: %w", err)
	}

	authDir := path.Join(sandbox.HomeDir, ".claude")
	credsPath := authDir + "/.credentials.json"
	prepCmd := fmt.Sprintf(
		"mkdir -p '%s' && install -m 600 /dev/null '%s'",
		shellEscapeSingleQuote(authDir),
		shellEscapeSingleQuote(credsPath),
	)

	var prepOut, prepErr bytes.Buffer
	exitCode, err := e.provider.Exec(ctx, sandbox, prepCmd, &prepOut, &prepErr)
	if err != nil {
		return false, fmt.Errorf("prepare claude credentials file: %w", err)
	}
	if exitCode != 0 {
		return false, fmt.Errorf("prepare claude credentials file: exited with code %d: %s", exitCode, prepErr.String())
	}

	if err := e.provider.WriteFile(ctx, sandbox, credsPath, credsJSON); err != nil {
		return false, fmt.Errorf("write claude credentials: %w", err)
	}

	e.logger.Debug().
		Str("org_id", orgID.String()).
		Msg("injected claude subscription credentials into sandbox")

	version := e.detectClaudeCodeVersion(ctx, sandbox)
	setClaudeCodePermissionMode(sandbox, claudeCodePermissionModeForAuth(TokenBillingModeSubscription, sub.AccountType, model, version))

	return true, nil
}

func (e *AgentEnv) PrepareClaudeCodeAPIKeyFallback(ctx context.Context, sandbox *Sandbox, env map[string]string) error {
	if env["ANTHROPIC_API_KEY"] == "" {
		return errClaudeCodeFallbackUnavailable
	}
	delete(env, "ANTHROPIC_AUTH_TOKEN")
	delete(env, "CLAUDE_CODE_OAUTH_TOKEN")
	if err := e.RemoveClaudeCodeCredentialsFile(ctx, sandbox); err != nil {
		return err
	}
	version := e.detectClaudeCodeVersion(ctx, sandbox)
	model := env[models.ModelEnvVarForAgentType(models.AgentTypeClaudeCode)]
	setClaudeCodePermissionMode(sandbox, claudeCodePermissionModeForAuth(TokenBillingModeAPIKey, "", model, version))
	return nil
}

func (e *AgentEnv) detectClaudeCodeVersion(ctx context.Context, sandbox *Sandbox) string {
	if e == nil {
		return ""
	}
	return detectClaudeCodeVersion(ctx, sandbox, e.provider, e.logger)
}

func (e *AgentEnv) RemoveClaudeCodeCredentialsFile(ctx context.Context, sandbox *Sandbox) error {
	if e == nil || e.provider == nil {
		return fmt.Errorf("sandbox provider is required to remove claude credentials")
	}
	credsPath := path.Join(sandbox.HomeDir, ".claude", ".credentials.json")
	if _, err := e.provider.ReadFile(ctx, sandbox, credsPath); err != nil {
		if isSandboxFileMissing(err) {
			return nil
		}
		return fmt.Errorf("check stale claude credentials: %w", err)
	}

	cmd := fmt.Sprintf("rm -f '%s'", shellEscapeSingleQuote(credsPath))
	var stdout, stderr bytes.Buffer
	exitCode, err := e.provider.Exec(ctx, sandbox, cmd, &stdout, &stderr)
	if err != nil {
		return fmt.Errorf("remove stale claude credentials: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("remove stale claude credentials: exited with code %d: %s", exitCode, stderr.String())
	}
	return nil
}

func (e *AgentEnv) unifiedCodingCredentialIsAPIKey(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName) bool {
	if e == nil || e.codingCredentials == nil {
		return false
	}
	if picked, ok := e.lookupRecentCredential(orgID, userID, provider); ok {
		cfg, compatible := compatibleCodingProviderConfig(picked.Provider, picked.Config)
		if !compatible {
			return false
		}
		return codingProviderConfigIsAPIKey(cfg)
	}
	cfg, handled := e.resolveFromCodingCredentials(ctx, orgID, userID, provider)
	if !handled {
		return false
	}
	return codingProviderConfigIsAPIKey(cfg)
}

func codingProviderConfigIsAPIKey(cfg models.ProviderConfig) bool {
	switch c := cfg.(type) {
	case models.OpenAIConfig:
		return c.APIKey != ""
	case models.AnthropicConfig:
		return c.APIKey != ""
	default:
		return false
	}
}
