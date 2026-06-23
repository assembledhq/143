package linear

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// Service is the single owned boundary for Linear session-linking work
// (detection resolution, issue upsert, provider-state reads/writes,
// attachment/comment/state mutations, coexistence checks). The session-create
// handler decides only "resolve inline or enqueue pre-start preparation"; the
// service answers everything else.
//
// Authorization model:
//
// Service writes (HandleMilestone, HandleStateTransition, the linker's
// attachmentCreate / commentCreate / state-move calls) authenticate to
// Linear with the org's stored integration token, NOT the requesting user's
// token. This is intentional and follows Linear's own integration model
// (one workspace-scoped install per org), but it has a consequence:
//
//	Any 143 user who can create a session linked to an issue in the
//	connected Linear workspace can indirectly trigger an attachment, a
//	rolling comment, and (when MoveWorkflowStates is enabled) workflow
//	state transitions on that issue. There is no per-user check that the
//	requesting user has Linear-side edit access to the issue.
//
// Org admins control the surface in two places:
//
//   - LinearAutomationSettings.MoveWorkflowStates / PostSessionLinks gate
//     state transitions and comments at the org or per-team level. With
//     MoveWorkflowStates=false, the service never calls workflow-state
//     mutation APIs even on a session create that links to that team.
//   - LinearAutomationSettings.AllowPerSessionOverrides gates whether an
//     individual session can opt out of state-sync (linear_state_sync_disabled)
//     or attachment/comment writes (linear_private). Setting this to false
//     centralizes the policy decision with the admin.
//
// Per-user authorization (e.g. "this user shouldn't move that team's
// issues") is intentionally out of scope for v1. Orgs that need stricter
// isolation should disable MoveWorkflowStates org-wide or per-team.
type Service struct {
	logger zerolog.Logger

	integrations       IntegrationReader
	integrationsWriter IntegrationWriter
	credentials        CredentialReader
	issues             *db.IssueStore
	links              *db.SessionIssueLinkStore
	teamKeys           *db.LinearTeamKeyStore
	providerState      providerStateStore
	stateEvents        stateEventStore
	sessions           *db.SessionStore
	clientFactory      ClientFactory
	orgSettingsLoader  OrgSettingsLoader
	// jobEnqueuer / linksChanged are populated by the boot-time wiring in
	// router.go and cmd/server/main.go *after* NewService returns: the SSE
	// streams aren't constructed until later, and the JobStore comes from a
	// shared infra bundle. We hold them in atomic.Pointer wrappers so a
	// request that lands during boot (between NewService and the Set* call)
	// reads `nil` cleanly instead of racing on the field assignment, which
	// the race detector would otherwise flag.
	jobEnqueuer  atomic.Pointer[jobEnqueuerHolder]
	linksChanged atomic.Pointer[linksChangedHolder]
	// pool is used to begin transactions for the rolling-comment and
	// state-transition writes that need a SELECT ... FOR UPDATE row lock to
	// be race-safe across concurrent milestone events. Optional: when nil,
	// the writes fall back to the non-transactional path used by tests.
	pool db.TxStarter
	// appBaseURL is the absolute origin (e.g. "https://app.143.dev") used to
	// build session deep-links sent to Linear. Linear renders the attachment
	// URL and comment body verbatim, so a relative path would arrive as
	// non-clickable text. Always set via Build / Config.
	appBaseURL string
	// teamKeyCache is a TTL'd in-process cache of the per-org team-key
	// allowlist used by detection. Refreshed lazily on miss; the
	// refresh_linear_team_keys cron continues to write through the store, and
	// invalidation is best-effort via the TTL since stale entries only cause
	// transient detection misses (the next session create within the TTL
	// after a refresh might miss new keys, then self-heal).
	//
	// Held by pointer so multiple Service instances inside the same process
	// (e.g. MODE=all wires one for the API router + one for the worker bundle)
	// share a single cache and a single invalidation surface — RefreshTeamKeys
	// from the worker invalidates the API-side cache without requiring inter-
	// instance plumbing.
	//
	// Multi-node staleness: cache invalidation (RefreshTeamKeys) only
	// invalidates the *local* process's entry. With more than one API/worker
	// pod, a session create on a different pod can miss new keys for up to
	// the TTL window after the OAuth post-install or 24h cron refresh fires
	// on another node. This is by design — detection misses are
	// self-healing within the TTL and the alternative (LISTEN/NOTIFY for
	// cross-pod invalidation) wasn't worth the wiring for a 60s ceiling.
	teamKeyCache *teamKeyAllowlistCache

	// agentSessions is the bridge from a 143 sessions row to the Linear
	// AgentSession that triggered it (when applicable). Populated by phase
	// 1 of the inbound agent feature; nil when the feature is dark.
	// HandleMilestone consults it inside the locked tx to decide whether
	// to fan out an AgentActivity emit alongside the durable attachment +
	// rolling comment writes. nil-safe: when unset the agent fan-out is
	// silently skipped.
	agentSessions *db.LinearAgentSessionStore
	// agentActivities backs the at-most-once activity log used by the
	// AgentActivityWriter. Same nil-safety contract as agentSessions.
	agentActivities *db.LinearAgentActivityLogStore
	// agentMetrics is the per-emit observability recorder. Optional —
	// nil means "no metrics", and the writer's nil-safe recordEmit
	// silently no-ops. Threaded through the Service so HandleAgentMilestone
	// and the worker handler share a single recorder per process.
	agentMetrics AgentActivityMetricsRecorder

	// credentialsWriter persists rotated tokens after a successful refresh.
	// Optional: when nil, GetValidToken still refreshes in-memory but logs a
	// warning that the new token will not survive the process. Production
	// always wires *db.OrgCredentialStore via Build; tests that don't
	// exercise refresh can leave it nil.
	credentialsWriter CredentialWriter
	// oauthClient holds LINEAR_OAUTH_CLIENT_ID / LINEAR_OAUTH_CLIENT_SECRET.
	// Required for the refresh-token path; without these, GetValidToken on a
	// credential that needs refreshing returns ErrOAuthClientNotConfigured.
	oauthClient OAuthClientCreds
	// refreshHTTPClient is the HTTP client used for the /oauth/token POST.
	// Tests inject a stubbed transport; production uses an http.Client with
	// refreshHTTPTimeout.
	refreshHTTPClient *http.Client
	// refreshMuRegistry holds per-org sync.Mutex values keyed by orgID
	// string. Wrapped in atomic.Pointer so direct &Service{} construction
	// (used widely in tests) stays race-free without forcing every test
	// to remember to allocate a sync.Map. NewService eagerly populates
	// the pointer; orgRefreshMu falls back to a CAS-installed registry
	// when the pointer is nil. A prior sync.Once-based lazy init was
	// race-prone: goroutines that observed a non-nil registry skipped
	// the Once.Do call entirely, losing the happens-before relationship
	// that makes Once safe.
	//
	// Growth: bounded by the working set of orgs that refresh; entries
	// are *sync.Mutex (tiny). No eviction needed at any realistic scale.
	refreshMuRegistry atomic.Pointer[sync.Map]
}

// jobEnqueuerHolder / linksChangedHolder wrap the function values stored in
// atomic.Pointer fields so atomic load/store can operate on a single pointer
// type. Without the wrapper we'd need to round-trip through unsafe pointers
// — these holders give us race-detector-clean Set/Get without that cost.
type jobEnqueuerHolder struct{ fn JobEnqueuer }
type linksChangedHolder struct{ fn LinksChangedNotifier }

// SetLinksChangedNotifier wires the SSE fan-out hook. Safe to call after
// NewService has returned and even concurrently with handler goroutines —
// the atomic store lets late-running tests or multi-stage boot wire the
// notifier without racing the read path.
func (s *Service) SetLinksChangedNotifier(n LinksChangedNotifier) {
	if n == nil {
		s.linksChanged.Store(nil)
		return
	}
	s.linksChanged.Store(&linksChangedHolder{fn: n})
}

func (s *Service) notifyLinksChanged(ctx context.Context, orgID, sessionID uuid.UUID, kind string) {
	holder := s.linksChanged.Load()
	if holder == nil || holder.fn == nil {
		return
	}
	holder.fn(ctx, orgID, sessionID, kind)
}

// providerStateStore is the narrow store surface HandleMilestone /
// HandleStateTransition need. Held as an interface so writes_test.go can
// substitute an in-memory fake without standing up pgxmock; concrete
// production usage is *db.LinearProviderStateStore.
type providerStateStore interface {
	Get(ctx context.Context, orgID, linkID uuid.UUID) (db.LinearProviderState, error)
	Upsert(ctx context.Context, orgID, linkID uuid.UUID, state db.LinearProviderState) error
	Merge(ctx context.Context, orgID, linkID uuid.UUID, patch db.LinearProviderState) error
}

// stateEventStore is the narrow surface used to record state-transition or
// skip events. Held as an interface for the same reason as
// providerStateStore.
type stateEventStore interface {
	Insert(ctx context.Context, orgID uuid.UUID, in db.LinearStateEventInput) error
}

// OrgSettingsLoader resolves an org's parsed settings. Held as a function
// rather than a store so wiring stays decoupled and tests can inject any
// settings shape.
type OrgSettingsLoader func(ctx context.Context, orgID uuid.UUID) (models.OrgSettings, error)

// LinksChangedNotifier is the SSE fan-out hook. When set, the linker calls
// it after every link insert/remove/promote/stale/refreshed so the session
// detail UI updates without page reload (design 62 §"Realtime contract and
// fallback behavior"). The session-detail query is still the source of
// truth — SSE is an invalidation hint, not the only correctness mechanism.
type LinksChangedNotifier func(ctx context.Context, orgID, sessionID uuid.UUID, kind string)

// IntegrationReader is the narrow surface the service needs to check
// integration status for an org.
type IntegrationReader interface {
	GetByOrgAndProvider(ctx context.Context, orgID uuid.UUID, provider models.IntegrationProvider) (models.Integration, error)
}

// IntegrationWriter is the optional surface used by Mark/ClearIntegration*
// to flip status and patch config when the service observes auth failures
// or a successful probe. Optional and nil-safe so test harnesses (and any
// wiring path that doesn't need write access) can stay minimal.
type IntegrationWriter interface {
	UpdateStatus(ctx context.Context, orgID, id uuid.UUID, status models.IntegrationStatus) error
	UpdateConfig(ctx context.Context, orgID, integrationID uuid.UUID, config json.RawMessage) error
	// UpdateStatusAndConfig is used by Mark/ClearIntegration* when both
	// fields need to change so the row can't be observed mid-flip.
	UpdateStatusAndConfig(ctx context.Context, orgID, integrationID uuid.UUID, status models.IntegrationStatus, config json.RawMessage) error
}

// CredentialReader is the narrow surface the service needs to resolve a
// Linear access token for an org. Implemented by *db.OrgCredentialStore in
// production and a fake in tests.
type CredentialReader interface {
	Get(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error)
}

// Client is the narrow API surface the service needs from a Linear API
// client. Production code passes a *integration.LinearTaskManager wrapped in
// a small adapter; tests pass an in-memory fake.
type Client interface {
	FetchIssue(ctx context.Context, identifier string) (*FetchedIssue, error)
	FetchUser(ctx context.Context, userID string) (*FetchedUser, error)
	ListTeamKeys(ctx context.Context) ([]TeamKeyInfo, error)
	CreateOrUpdateAttachment(ctx context.Context, in AttachmentWriteInput) (AttachmentResult, error)
	CreateComment(ctx context.Context, issueID, body string) (string, error)
	UpdateComment(ctx context.Context, commentID, body string) error
	// FindRecentBotCommentByURL scans recent comments on the issue for one
	// whose body contains the given session URL (used as a deterministic
	// per-session marker). Returns "" if none match. Best-effort recovery
	// for the lost-response zone in HandleMilestone — see writes.go.
	FindRecentBotCommentByURL(ctx context.Context, issueID, sessionURL string) (string, error)
	WorkflowStateForType(ctx context.Context, teamID string, prefer []string, stateType string) (*WorkflowState, error)
	UpdateIssueState(ctx context.Context, issueID, stateID string) error
	IssueRecentHumanEdits(ctx context.Context, issueID string, since time.Time) (bool, error)
	HasGitHubIntegrationAttachment(ctx context.Context, issueID string) (bool, error)

	// Linear Agent Interaction surface — only used by sessions whose origin
	// is an inbound Linear assignment / @-mention. The same Client
	// implementation backs both flows; tests that don't exercise the agent
	// path can return canned errors from these methods.
	AgentActivityCreate(ctx context.Context, in AgentActivityInput) (AgentActivityResult, error)
	AgentSessionUpdate(ctx context.Context, in AgentSessionUpdateInput) error
	AgentSessionGet(ctx context.Context, agentSessionID string) (*FetchedAgentSession, error)
	FetchComment(ctx context.Context, commentID string) (*FetchedComment, error)
}

// ClientFactory creates a per-org Linear API client from an access token
// (resolved via OrgCredentialStore). The factory pattern keeps the service
// free of HTTP/auth concerns and lets us inject a fake Client in tests.
type ClientFactory func(ctx context.Context, accessToken string) (Client, error)

// FetchedIssue is a normalized snapshot of a Linear issue used to populate
// the local issues row and the linked-issue context package. We deliberately
// keep this superset-ish so the session-bootstrap context contract has
// everything it needs in one round trip.
type FetchedIssue struct {
	ID            string
	Identifier    string
	Title         string
	Description   string
	URL           string
	StateName     string
	StateType     string
	StateID       string
	Priority      string
	AssigneeName  string
	CreatorID     string
	CreatorEmail  string
	CreatorName   string
	TeamID        string
	TeamKey       string
	TeamName      string
	WorkspaceSlug string
	// ProjectID is the Linear project id when the issue belongs to one.
	// Empty when the issue is not in a project. Used by the inbound agent
	// repo resolver to pick a per-project mapping over the team default.
	ProjectID string
	// Labels is the issue's full label set (verbatim names; case
	// preserved). Powers the `repo:<full-name>` override in the inbound
	// agent resolver. Bounded to 50 by the GraphQL query — issues with
	// more labels than that are not a realistic use case.
	Labels           []string
	RepositoryID     *uuid.UUID
	Comments         []FetchedComment
	Attachments      []FetchedAttachment
	DuplicateOfKey   string
	DuplicateOfTitle string
}

type FetchedUser struct {
	ID    string
	Name  string
	Email string
}

// FetchedComment is a single Linear comment trimmed to fields the agent
// context package and operator UI need. Used both by FetchIssue (which
// returns recent comments inline) and by FetchComment (single-id
// resolver) — the same shape works for both because the agent path
// only cares about author + body + when.
type FetchedComment struct {
	// ID is set on FetchComment results; FetchIssue's inline comments
	// leave it empty for backwards compatibility.
	ID        string    `json:"id,omitempty"`
	IssueID   string    `json:"issue_id,omitempty"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// FetchedAttachment is a Linear attachment exposed to the agent's prompt as
// a reference. We default to metadata-first; the agent decides whether to
// inspect the URL further.
type FetchedAttachment struct {
	Title  string `json:"title,omitempty"`
	URL    string `json:"url"`
	Source string `json:"source,omitempty"`
}

// TeamKeyInfo is one row returned by ListTeamKeys.
type TeamKeyInfo struct {
	TeamID      string
	Key         string
	Name        string
	WorkspaceID string
}

// AttachmentWriteInput captures the desired state for an attachment write.
// The Client decides whether this is a create or an update — if PriorID is
// set, it calls attachmentUpdate; otherwise attachmentCreate. This keeps the
// idempotency choice on the side that has the API surface.
type AttachmentWriteInput struct {
	IssueID  string
	PriorID  string
	Title    string
	Subtitle string
	URL      string
	IconURL  string
	Metadata db.LinearAttachmentMetadata
}

// AttachmentResult carries the durable handle returned by Linear so the
// service can persist it in provider_state.
type AttachmentResult struct {
	ID  string
	URL string
}

// WorkflowState is one Linear workflow state.
type WorkflowState struct {
	ID   string
	Name string
	Type string
}

// Config packages constructor dependencies. Using a config struct keeps the
// constructor signature stable as we add more stores over time.
type Config struct {
	Logger             zerolog.Logger
	Integrations       IntegrationReader
	IntegrationsWriter IntegrationWriter
	Credentials        CredentialReader
	// CredentialsWriter persists rotated OAuth tokens after a successful
	// refresh. Optional in tests; production must always wire one (Build
	// passes *db.OrgCredentialStore here) or refreshed tokens will live
	// only inside the calling process.
	CredentialsWriter CredentialWriter
	Issues            *db.IssueStore
	Links             *db.SessionIssueLinkStore
	TeamKeys          *db.LinearTeamKeyStore
	ProviderState     *db.LinearProviderStateStore
	StateEvents       *db.LinearStateEventStore
	Sessions          *db.SessionStore
	ClientFactory     ClientFactory
	OrgSettingsLoader OrgSettingsLoader
	// OAuthClient is required when callers expect refresh-on-expiry to work:
	// without ClientID + ClientSecret, GetValidToken returns
	// ErrOAuthClientNotConfigured for any credential that has reached the
	// refresh window. Tests that never exercise refresh can leave both
	// fields zero; production must always populate from
	// LINEAR_OAUTH_CLIENT_ID / LINEAR_OAUTH_CLIENT_SECRET.
	OAuthClient OAuthClientCreds
	// RefreshHTTPClient overrides the default HTTP client used for the
	// /oauth/token POST. Tests inject a stubbed transport; production
	// callers can leave nil to get the package default with
	// refreshHTTPTimeout.
	RefreshHTTPClient *http.Client
	// Pool is used by HandleMilestone / HandleStateTransition to begin a tx
	// for SELECT ... FOR UPDATE on the provider-state row, so two concurrent
	// milestone events for the same link can't both create a rolling
	// comment or both move the issue past a forward-only state check.
	// Optional in tests.
	Pool db.TxStarter
	// AppBaseURL is the absolute origin used to build session deep-links
	// posted to Linear. Required for clickable attachment URLs and comment
	// bodies; passing a relative path here is treated as a misconfiguration.
	AppBaseURL string
	// TeamKeyCache is the optional in-process team-key allowlist cache. When
	// MODE=all wires both an API-side and a worker-side Service in the same
	// process, both should be passed the same cache so a refresh on one
	// invalidates the other (without this, the API and worker can disagree
	// on the allowlist for up to the TTL window). When nil, NewService
	// allocates a fresh cache local to the Service — fine for tests and for
	// MODE=api / MODE=worker single-Service processes.
	TeamKeyCache *teamKeyAllowlistCache

	// AgentSessions and AgentActivities wire the inbound agent feature.
	// Both nil ↔ feature-flag-off / dark launch — the milestone fan-out
	// silently no-ops, the dispatcher refuses to construct, and the
	// existing outbound flow stays unaffected.
	AgentSessions   *db.LinearAgentSessionStore
	AgentActivities *db.LinearAgentActivityLogStore
	// AgentMetrics is the per-emit observability recorder shared with
	// the inbound dispatcher. Optional; nil means "no metrics".
	AgentMetrics AgentActivityMetricsRecorder
}

func NewService(cfg Config) *Service {
	cache := cfg.TeamKeyCache
	if cache == nil {
		cache = &teamKeyAllowlistCache{}
	}
	httpClient := cfg.RefreshHTTPClient
	if httpClient == nil {
		// Per-call timeout context wraps each request inside postLinearRefresh,
		// so the http.Client itself doesn't need a global Timeout. Leaving
		// Timeout zero lets the per-call deadline be the single source of
		// truth and avoids a confusing "two timeouts compete" interaction.
		httpClient = &http.Client{}
	}
	svc := &Service{
		teamKeyCache:       cache,
		logger:             cfg.Logger,
		integrations:       cfg.Integrations,
		integrationsWriter: cfg.IntegrationsWriter,
		credentials:        cfg.Credentials,
		credentialsWriter:  cfg.CredentialsWriter,
		oauthClient:        cfg.OAuthClient,
		refreshHTTPClient:  httpClient,
		issues:             cfg.Issues,
		links:              cfg.Links,
		teamKeys:           cfg.TeamKeys,
		providerState:      cfg.ProviderState,
		stateEvents:        cfg.StateEvents,
		sessions:           cfg.Sessions,
		clientFactory:      cfg.ClientFactory,
		orgSettingsLoader:  cfg.OrgSettingsLoader,
		pool:               cfg.Pool,
		appBaseURL:         strings.TrimRight(cfg.AppBaseURL, "/"),
		agentSessions:      cfg.AgentSessions,
		agentActivities:    cfg.AgentActivities,
		agentMetrics:       cfg.AgentMetrics,
	}
	svc.refreshMuRegistry.Store(&sync.Map{})
	return svc
}

// AgentSessionStore exposes the agent-session store so handlers and worker
// glue can construct the dispatcher / repo resolver without re-deriving
// the wiring path. Returns nil when the agent feature is dark.
func (s *Service) AgentSessionStore() *db.LinearAgentSessionStore {
	return s.agentSessions
}

// AgentActivityStore exposes the activity log store. Same nil contract as
// AgentSessionStore.
func (s *Service) AgentActivityStore() *db.LinearAgentActivityLogStore {
	return s.agentActivities
}

// maxProviderStateLockedDuration bounds how long a single milestone or
// state-transition is allowed to hold the provider_state row lock. The
// underlying GraphQL HTTP client already imposes a 30s per-call timeout
// (client.go), but HandleStateTransition can chain up to five Linear API
// calls inside the locked region (HasGitHubIntegrationAttachment → FetchIssue
// → IssueRecentHumanEdits → WorkflowStateForType → UpdateIssueState), so a
// slow-but-not-failing Linear API can pin a pool connection + the row lock
// for >2 minutes worst case. Capping the locked region at 60s keeps a Linear
// outage from cascading into DB connection pool exhaustion: a single
// milestone job either makes progress within the window or is failed by ctx
// cancellation, and the worker's RetryableError + dedupe key collapse the
// retry into the next attempt without burning the fire-once event row.
//
// Tuned generously enough that a healthy Linear API has all the headroom it
// needs (median FetchIssue is ~250ms; a five-call chain comfortably fits in
// a couple of seconds), but tight enough that hung calls don't accumulate.
const maxProviderStateLockedDuration = 60 * time.Second

// withProviderStateLocked runs fn inside a transaction that holds a row-level
// lock on the provider_state row for (org_id, link_id). Two concurrent
// milestone events for the same link will serialize through this lock so
// only one of them can observe CommentID == "" and create a fresh comment;
// the loser sees the winner's CommentID and takes the update branch. fn
// receives stores bound to the tx so any subsequent writes participate in
// the same transaction.
//
// The ctx passed to fn is wrapped with maxProviderStateLockedDuration so the
// chained Linear API calls inside the locked region can't pin a pool
// connection during a Linear outage — see the constant's doc for the
// rationale. Callers that need a tighter or looser bound must wrap the
// outer ctx themselves before calling this method.
//
// When the service has no pool (older tests) the call falls through to fn
// with the non-transactional stores. Any caller that needs strict
// serialization must check for that path explicitly.
func (s *Service) withProviderStateLocked(
	ctx context.Context,
	orgID, linkID uuid.UUID,
	fn func(ctx context.Context, txState providerStateStore, txEvents stateEventStore, state db.LinearProviderState) error,
) error {
	if s.pool == nil {
		state, err := s.providerState.Get(ctx, orgID, linkID)
		if err != nil {
			return fmt.Errorf("read provider state: %w", err)
		}
		return fn(ctx, s.providerState, s.stateEvents, state)
	}

	ctx, cancel := context.WithTimeout(ctx, maxProviderStateLockedDuration)
	defer cancel()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		// pgx's Rollback returns ErrTxClosed after a successful Commit, which
		// is the expected no-op shape; anything else is genuine
		// rollback-on-error trouble (lock not released, connection stuck) that
		// operators chasing a hang need to see in logs.
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			s.logger.Warn().Err(rbErr).
				Str("link_id", linkID.String()).
				Msg("linear: deferred tx.Rollback returned non-ErrTxClosed; provider_state lock may be slow to release")
		}
	}()

	// Lock or insert-then-lock the provider_state row. We can't SELECT FOR
	// UPDATE a row that doesn't exist yet, so insert a zero-state row first
	// (idempotent — ON CONFLICT DO NOTHING) and then take the lock. This
	// matches the behavior of LinearProviderStateStore.Get returning an
	// empty state when no row exists.
	if _, err := tx.Exec(ctx, `
		INSERT INTO session_issue_link_provider_state (link_id, org_id, provider, state, updated_at)
		VALUES (@link_id, @org_id, @provider, '{}'::jsonb, now())
		ON CONFLICT (link_id) DO NOTHING`,
		pgx.NamedArgs{
			"link_id":  linkID,
			"org_id":   orgID,
			"provider": "linear",
		}); err != nil {
		// FK violation on link_id means the link row was deleted between
		// resolution and this milestone job firing (e.g. session deleted,
		// link unlinked, or a DB-level CASCADE swept it). The milestone is
		// moot — short-circuit cleanly so the worker doesn't retry forever
		// chasing a link that no longer exists. Operators chasing "why is
		// this milestone job in failure mode?" would otherwise see a
		// confusing wrapped FK error with no obvious resolution.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.ForeignKeyViolation {
			s.logger.Debug().
				Str("link_id", linkID.String()).
				Str("org_id", orgID.String()).
				Msg("linear: provider_state seed hit FK violation; link is gone, treating milestone as no-op")
			return nil
		}
		return fmt.Errorf("seed provider state: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		SELECT 1 FROM session_issue_link_provider_state
		WHERE link_id = @link_id AND org_id = @org_id AND provider = @provider
		FOR UPDATE`,
		pgx.NamedArgs{
			"link_id":  linkID,
			"org_id":   orgID,
			"provider": "linear",
		}); err != nil {
		return fmt.Errorf("lock provider state: %w", err)
	}

	txState := db.NewLinearProviderStateStore(tx)
	txEvents := db.NewLinearStateEventStore(tx)
	state, err := txState.Get(ctx, orgID, linkID)
	if err != nil {
		return fmt.Errorf("read locked provider state: %w", err)
	}
	if err := fn(ctx, txState, txEvents, state); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit provider state tx: %w", err)
	}
	return nil
}

// SessionURL builds the absolute deep-link to a session that we send to
// Linear. Centralized here so attachment URL and comment body never disagree
// and so callers don't have to know the FRONTEND_URL plumbing. NewService
// already trims trailing slashes; we re-trim here as defense-in-depth so
// any future direct-construction path (notably tests) still produces a
// clean URL instead of `//sessions/<id>` that breaks Linear's renderer.
func (s *Service) SessionURL(sessionID uuid.UUID) string {
	if s == nil || s.appBaseURL == "" {
		return fmt.Sprintf("/sessions/%s", sessionID.String())
	}
	return fmt.Sprintf("%s/sessions/%s", strings.TrimRight(s.appBaseURL, "/"), sessionID.String())
}

// AgentSessionDebugURL builds the operator debug URL for a Linear
// AgentSession bridge row. This is used before a 143 session exists, so it
// intentionally targets the Linear agent debug endpoint rather than
// /sessions/<id>.
func (s *Service) AgentSessionDebugURL(linearAgentSessionID string) string {
	path := fmt.Sprintf("/api/v1/integrations/linear/agent/sessions/%s", url.PathEscape(linearAgentSessionID))
	if s == nil || s.appBaseURL == "" {
		return path
	}
	return strings.TrimRight(s.appBaseURL, "/") + path
}

// Enabled returns true when the org has an active Linear integration. The
// session-create path uses this to short-circuit detection — design 62
// §"Path C — Linear is not enabled" demands a silent no-op.
func (s *Service) Enabled(ctx context.Context, orgID uuid.UUID) bool {
	if s == nil || s.integrations == nil {
		return false
	}
	integration, err := s.integrations.GetByOrgAndProvider(ctx, orgID, models.IntegrationProviderLinear)
	if err != nil {
		return false
	}
	return integration.Status == "active"
}

// ErrIntegrationNotFound is returned by integrationFor when the org has no
// linear integration row at all. Workers should treat this as fatal/skip
// rather than a retryable error: the row will not appear by retrying, and
// the typical cause is an integration that was disconnected after the job
// was enqueued. Bare `errors.Is(err, ErrIntegrationNotFound)` works through
// the wrap added by integrationFor.
var ErrIntegrationNotFound = errors.New("linear integration not found")

// ClientForOrg returns a fully-resolved Linear API client backed by the
// org's stored credential. Public so the inbound-agent dispatcher and
// the settings loader can build clients without re-deriving the token
// resolution logic. Returns an error when no integration / credential
// is configured.
func (s *Service) ClientForOrg(ctx context.Context, orgID uuid.UUID) (Client, error) {
	if s == nil {
		return nil, errors.New("linear service unavailable")
	}
	_, token, err := s.integrationFor(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return s.clientFactory(ctx, token)
}

// integrationFor returns the integration row + a Linear access token that is
// valid right now. It delegates to GetValidToken, which transparently
// rotates the access token via the refresh-token flow when the cached token
// is within refreshWindow of expiry.
//
// This is the single read-plus-refresh entry point that every service-layer
// caller (RefreshTeamKeys, ResolvePrimary, mid-session linking, the
// session-create inline path) routes through, so there is exactly one place
// in the code where stale tokens become fresh tokens.
//
// Behavior on edge cases:
//
//   - Missing integration row: returns ErrIntegrationNotFound (wrapped).
//   - Legacy connection without refresh-token fields: returns the cached token
//     unchanged. The token works until Linear revokes it, at which point
//     the caller's 401 path triggers MarkIntegrationUnauthorized and the
//     user reconnects; the new connection captures a refresh token.
//   - Refresh-token revoked: the refresh path zeroes the row's refresh
//     token and flips the integration to errored, then returns
//     ErrRefreshTokenRevoked. Callers should not retry.
//   - Transient refresh failure (network blip, 5xx): returns the cached
//     token if still inside its validity window; otherwise propagates the
//     error so the caller can decide whether to retry.
func (s *Service) integrationFor(ctx context.Context, orgID uuid.UUID) (models.Integration, string, error) {
	return s.GetValidToken(ctx, orgID)
}

// TeamKeyAllowlist returns the org's cached team-key allowlist as a map for
// detection. Cheap; detection runs on every session create. Refreshes are
// driven by the integration sync worker, not this hot path.
//
// A short in-process TTL cache fronts the DB query so back-to-back session
// creates from the same org don't hammer linear_team_keys. RefreshTeamKeys
// invalidates the entry after writing, and the TTL bounds staleness when
// another process refreshes (e.g. the cron worker on a different node).
func (s *Service) TeamKeyAllowlist(ctx context.Context, orgID uuid.UUID) (map[string]bool, error) {
	if s == nil || s.teamKeys == nil {
		return map[string]bool{}, nil
	}
	if s.teamKeyCache != nil {
		if cached, ok := s.teamKeyCache.get(orgID); ok {
			// Defensive copy: callers iterate this map during detection but the
			// cache shares the underlying storage across requests, so a mutation
			// here would silently corrupt every future caller's allowlist for the
			// org. Cheap relative to the DB hit it replaces.
			return copyAllowlist(cached), nil
		}
	}
	keys, err := s.teamKeys.ListByOrg(ctx, orgID)
	if err != nil {
		return nil, err
	}
	allow := make(map[string]bool, len(keys))
	for _, k := range keys {
		allow[k.TeamKey] = true
	}
	if s.teamKeyCache != nil {
		if evicted := s.teamKeyCache.put(orgID, allow); evicted > 0 {
			// Eviction count surfaces TTL activity to operators chasing "is the
			// cache actually expiring?" without committing to per-hit logging.
			s.logger.Debug().
				Int("evicted", evicted).
				Str("org_id", orgID.String()).
				Msg("linear team-key cache: swept expired entries on miss")
		}
	}
	return copyAllowlist(allow), nil
}

func copyAllowlist(src map[string]bool) map[string]bool {
	out := make(map[string]bool, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// InvalidateTeamKeyCache drops the in-process team-key allowlist entry for
// an org. Wired into the Linear integration disconnect path so a session
// created right after disconnect doesn't see stale team keys for up to the
// TTL window. The DB-side ListByOrg already filters by `integrations.status
// = 'active'`, so this is purely a cache-coherency concern: without it, a
// detection that hits the cached entry mid-disconnect would still admit
// bare-identifier matches against teams whose integration was just paused.
//
// Best-effort, no error: a no-op when the org has no entry, and idempotent
// across repeated calls. Multi-node deployments still tolerate the same
// 60s TTL staleness on other nodes — operators who need stricter cross-pod
// invalidation should treat the disconnect as an integration outage and
// expect the next session-create per-pod to refresh.
func (s *Service) InvalidateTeamKeyCache(orgID uuid.UUID) {
	if s == nil || s.teamKeyCache == nil {
		return
	}
	s.teamKeyCache.invalidate(orgID)
}

// RefreshTeamKeys pulls the team list from Linear and replaces the cache.
// Called after OAuth install and on a 24h cron. Idempotent.
//
// The Linear-side read goes through withRefreshableClient so an expired
// token rotates transparently and a 401 mid-call triggers exactly one
// force-refresh + retry. The integration row is read up front (before
// withRefreshableClient) because we need the integration ID to scope the
// team-key replace; that read goes through GetValidToken too so a missing
// integration short-circuits cleanly.
func (s *Service) RefreshTeamKeys(ctx context.Context, orgID uuid.UUID) error {
	integration, _, err := s.GetValidToken(ctx, orgID)
	if err != nil {
		return err
	}

	var teams []TeamKeyInfo
	err = s.withRefreshableClient(ctx, orgID, func(client Client) error {
		got, listErr := client.ListTeamKeys(ctx)
		if listErr != nil {
			return fmt.Errorf("list linear team keys: %w", listErr)
		}
		teams = got
		return nil
	})
	if err != nil {
		return err
	}

	rows := make([]db.LinearTeamKey, 0, len(teams))
	workspaceID := ""
	for _, t := range teams {
		rows = append(rows, db.LinearTeamKey{
			TeamID:   t.TeamID,
			TeamKey:  t.Key,
			TeamName: t.Name,
		})
		if workspaceID == "" {
			workspaceID = t.WorkspaceID
		}
	}
	if err := s.teamKeys.ReplaceForIntegration(ctx, orgID, integration.ID, workspaceID, rows); err != nil {
		return err
	}
	if s.teamKeyCache != nil {
		s.teamKeyCache.invalidate(orgID)
	}
	return nil
}

// ResolvePrimary fetches the unambiguous primary Linear issue and the local
// issues row for it. Used by the session-create fast path and the
// prepare_linear_primary worker.
//
// Workspace verification is asymmetric by design (62 §"Path B"):
//
//   - URL refs carry an explicit workspace slug and must match the org's
//     connected workspace; mismatches drop silently via ErrCrossWorkspace.
//   - Bare identifiers (e.g. "ACS-1234") have no workspace component, so
//     they implicitly resolve against whichever workspace the integration
//     is currently connected to. Detection only fires on bare identifiers
//     for keys present in linear_team_keys, which limits collisions, but
//     it does not validate workspace identity.
//
// Operational consequence: if an org's Linear workspace slug is renamed,
// existing URL-ref'd issues created before the rename will start failing
// the workspace check and drop silently, while bare-identifier refs will
// continue to resolve against the new workspace as if nothing changed.
// We accept this asymmetry — Linear treats workspace slugs as effectively
// immutable after install — but operators reconnecting an integration to
// a different workspace should expect URL-based detection to break for
// historical references. Slug changes warrant an integration health note.
func (s *Service) ResolvePrimary(ctx context.Context, orgID uuid.UUID, hit Detected) (*ResolvedIssue, error) {
	integration, _, err := s.GetValidToken(ctx, orgID)
	if err != nil {
		return nil, err
	}

	var fetched *FetchedIssue
	if err := s.withRefreshableClient(ctx, orgID, func(client Client) error {
		got, fetchErr := client.FetchIssue(ctx, hit.Identifier)
		if fetchErr != nil {
			return fmt.Errorf("fetch linear issue %q: %w", hit.Identifier, fetchErr)
		}
		fetched = got
		return nil
	}); err != nil {
		return nil, err
	}

	if hit.Workspace != "" && fetched.WorkspaceSlug != "" && !strings.EqualFold(hit.Workspace, fetched.WorkspaceSlug) {
		return nil, ErrCrossWorkspace
	}

	issueID, err := s.upsertLinearIssue(ctx, orgID, integration.ID, fetched)
	if err != nil {
		return nil, fmt.Errorf("upsert linear issue: %w", err)
	}

	return &ResolvedIssue{
		Identifier: fetched.Identifier,
		Issue:      fetched,
		LocalID:    issueID,
	}, nil
}

// ErrCrossWorkspace is returned when a URL ref's workspace doesn't match the
// org's connected Linear workspace. Surface in detection drop counts; do not
// surface to the user.
var ErrCrossWorkspace = errors.New("linear ref points to a different workspace")

// ResolvedIssue is the output of ResolvePrimary.
type ResolvedIssue struct {
	Identifier string
	Issue      *FetchedIssue
	LocalID    uuid.UUID
}

type LinkOptions struct {
	AllowRepositoryMismatch bool
}

// LinkResolved inserts (or upserts) the link row, applies the null-repo
// carve-out, persists provider_state.team_id and link_audit_reason, and
// returns the resulting link id.
func (s *Service) LinkResolved(
	ctx context.Context,
	orgID, sessionID uuid.UUID,
	resolved *ResolvedIssue,
	role models.SessionIssueLinkRole,
	position int,
	addedByUserID *uuid.UUID,
) (uuid.UUID, error) {
	return s.LinkResolvedWithOptions(ctx, orgID, sessionID, resolved, role, position, addedByUserID, LinkOptions{})
}

func (s *Service) LinkResolvedWithOptions(
	ctx context.Context,
	orgID, sessionID uuid.UUID,
	resolved *ResolvedIssue,
	role models.SessionIssueLinkRole,
	position int,
	addedByUserID *uuid.UUID,
	opts LinkOptions,
) (uuid.UUID, error) {
	var linkID uuid.UUID
	var err error
	if opts.AllowRepositoryMismatch {
		linkID, err = s.links.CreateAllowingRepositoryMismatch(ctx, orgID, sessionID, resolved.LocalID, role, position, addedByUserID)
	} else {
		linkID, err = s.links.CreateAllowingNullRepo(ctx, orgID, sessionID, resolved.LocalID, role, position, addedByUserID)
	}
	if err != nil {
		return uuid.Nil, err
	}
	auditReason := ""
	if resolved.Issue.RepositoryID == nil {
		auditReason = "linear_null_repo_carveout"
	} else if opts.AllowRepositoryMismatch {
		auditReason = "manual_repository_override"
	}
	if err := s.providerState.Merge(ctx, orgID, linkID, db.LinearProviderState{
		Identifier:         resolved.Identifier,
		TeamID:             resolved.Issue.TeamID,
		WorkspaceSlug:      resolved.Issue.WorkspaceSlug,
		LinkAuditReason:    auditReason,
		LastKnownStateName: resolved.Issue.StateName,
		LastKnownStateType: resolved.Issue.StateType,
	}); err != nil {
		return uuid.Nil, fmt.Errorf("persist linear provider state: %w", err)
	}
	s.notifyLinksChanged(ctx, orgID, sessionID, "inserted")
	return linkID, nil
}

// upsertLinearIssue is the shared helper used by both detection (this
// service) and webhook ingestion. INSERT ... ON CONFLICT DO UPDATE prefers
// the more complete payload and breaks ties with a freshness timestamp; we
// achieve "more complete wins" via GREATEST(last_seen_at) and by always
// passing the freshly-fetched description through.
func (s *Service) upsertLinearIssue(ctx context.Context, orgID, integrationID uuid.UUID, fetched *FetchedIssue) (uuid.UUID, error) {
	if fetched == nil || fetched.ID == "" {
		return uuid.Nil, fmt.Errorf("linear fetched issue is empty")
	}
	now := time.Now()
	tags := []string{}
	if fetched.TeamKey != "" {
		tags = append(tags, "team:"+fetched.TeamKey)
	}
	title := fetched.Title
	if fetched.Identifier != "" {
		title = fetched.Identifier + ": " + fetched.Title
	}
	desc := fetched.Description
	rawData, err := json.Marshal(struct {
		ID         string `json:"id"`
		Identifier string `json:"identifier"`
		URL        string `json:"url,omitempty"`
	}{
		ID:         fetched.ID,
		Identifier: fetched.Identifier,
		URL:        fetched.URL,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("marshal linear issue raw data: %w", err)
	}
	issue := &models.Issue{
		OrgID:               orgID,
		ExternalID:          fetched.ID,
		Source:              models.IssueSourceLinear,
		SourceIntegrationID: &integrationID,
		RepositoryID:        fetched.RepositoryID,
		Title:               title,
		Description:         &desc,
		RawData:             rawData,
		Status:              "open",
		FirstSeenAt:         now,
		LastSeenAt:          now,
		OccurrenceCount:     1,
		Severity:            "medium",
		Tags:                tags,
		Fingerprint:         models.IssueFingerprint(models.IssueSourceLinear, fetched.ID),
	}
	if err := s.issues.Upsert(ctx, issue); err != nil {
		return uuid.Nil, err
	}
	return issue.ID, nil
}

// SnapshotForTurn captures the resolved Linear context needed by an agent
// turn. Returns a JSON-marshalable structure suitable for the turn snapshot
// prompt builder. Comments are bounded to maxComments to avoid dumping the
// full thread.
func (s *Service) SnapshotForTurn(ctx context.Context, fetched *FetchedIssue, maxComments int) LinearTurnContext {
	if fetched == nil {
		return LinearTurnContext{}
	}
	if maxComments <= 0 {
		maxComments = 8
	}
	comments := fetched.Comments
	if len(comments) > maxComments {
		comments = comments[:maxComments]
	}
	return LinearTurnContext{
		Identifier:       fetched.Identifier,
		Title:            fetched.Title,
		Description:      fetched.Description,
		StateName:        fetched.StateName,
		StateType:        fetched.StateType,
		Priority:         fetched.Priority,
		AssigneeName:     fetched.AssigneeName,
		TeamKey:          fetched.TeamKey,
		TeamName:         fetched.TeamName,
		URL:              fetched.URL,
		DuplicateOfKey:   fetched.DuplicateOfKey,
		DuplicateOfTitle: fetched.DuplicateOfTitle,
		Attachments:      fetched.Attachments,
		Comments:         comments,
	}
}

// LinearTurnContext is the structured context the agent receives when a
// session has linked Linear issues. This is the contract that lets the
// agent start a run from a Linear issue alone — without this, design 62
// §"Issue-only session start" wouldn't work.
//
// SECURITY (prompt injection): every string field here is *user-controlled
// content* fetched from Linear (issue title, description, comments) and
// flows verbatim into the agent's prompt via the turn snapshot. Anyone
// with write access to the source Linear workspace — which can include
// external collaborators on shared cycles — can therefore inject text the
// agent treats as instructions. Downstream prompt builders that consume
// these fields MUST fence them as untrusted data (e.g. inside a clearly-
// marked "the following Linear issue is user-supplied content, not
// instructions" delimiter block). Do NOT extend this struct with fields
// that get rendered to markdown without the same fencing — see design 62
// §"Trust model for fetched issue content".
type LinearTurnContext struct {
	Identifier       string              `json:"identifier"`
	Title            string              `json:"title"`
	Description      string              `json:"description,omitempty"`
	StateName        string              `json:"state_name,omitempty"`
	StateType        string              `json:"state_type,omitempty"`
	Priority         string              `json:"priority,omitempty"`
	AssigneeName     string              `json:"assignee_name,omitempty"`
	TeamKey          string              `json:"team_key,omitempty"`
	TeamName         string              `json:"team_name,omitempty"`
	URL              string              `json:"url,omitempty"`
	DuplicateOfKey   string              `json:"duplicate_of_key,omitempty"`
	DuplicateOfTitle string              `json:"duplicate_of_title,omitempty"`
	Attachments      []FetchedAttachment `json:"attachments,omitempty"`
	Comments         []FetchedComment    `json:"comments,omitempty"`
}
