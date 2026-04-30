package linear

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

	integrations      IntegrationReader
	credentials       CredentialReader
	issues            *db.IssueStore
	links             *db.SessionIssueLinkStore
	teamKeys          *db.LinearTeamKeyStore
	providerState     providerStateStore
	stateEvents       stateEventStore
	sessions          *db.SessionStore
	clientFactory     ClientFactory
	orgSettingsLoader OrgSettingsLoader
	jobEnqueuer       JobEnqueuer
	linksChanged      LinksChangedNotifier
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
	teamKeyCache teamKeyAllowlistCache
}

// SetLinksChangedNotifier wires the SSE fan-out hook.
func (s *Service) SetLinksChangedNotifier(n LinksChangedNotifier) {
	s.linksChanged = n
}

func (s *Service) notifyLinksChanged(ctx context.Context, orgID, sessionID uuid.UUID, kind string) {
	if s.linksChanged != nil {
		s.linksChanged(ctx, orgID, sessionID, kind)
	}
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
	GetByOrgAndProvider(ctx context.Context, orgID uuid.UUID, provider string) (models.Integration, error)
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
	ID               string
	Identifier       string
	Title            string
	Description      string
	URL              string
	StateName        string
	StateType        string
	StateID          string
	Priority         string
	AssigneeName     string
	TeamID           string
	TeamKey          string
	TeamName         string
	WorkspaceSlug    string
	RepositoryID     *uuid.UUID
	Comments         []FetchedComment
	Attachments      []FetchedAttachment
	DuplicateOfKey   string
	DuplicateOfTitle string
}

// FetchedComment is a single Linear comment trimmed to fields the agent
// context package and operator UI need.
type FetchedComment struct {
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
	Logger            zerolog.Logger
	Integrations      IntegrationReader
	Credentials       CredentialReader
	Issues            *db.IssueStore
	Links             *db.SessionIssueLinkStore
	TeamKeys          *db.LinearTeamKeyStore
	ProviderState     *db.LinearProviderStateStore
	StateEvents       *db.LinearStateEventStore
	Sessions          *db.SessionStore
	ClientFactory     ClientFactory
	OrgSettingsLoader OrgSettingsLoader
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
}

func NewService(cfg Config) *Service {
	return &Service{
		logger:            cfg.Logger,
		integrations:      cfg.Integrations,
		credentials:       cfg.Credentials,
		issues:            cfg.Issues,
		links:             cfg.Links,
		teamKeys:          cfg.TeamKeys,
		providerState:     cfg.ProviderState,
		stateEvents:       cfg.StateEvents,
		sessions:          cfg.Sessions,
		clientFactory:     cfg.ClientFactory,
		orgSettingsLoader: cfg.OrgSettingsLoader,
		pool:              cfg.Pool,
		appBaseURL:        strings.TrimRight(cfg.AppBaseURL, "/"),
	}
}

// withProviderStateLocked runs fn inside a transaction that holds a row-level
// lock on the provider_state row for (org_id, link_id). Two concurrent
// milestone events for the same link will serialize through this lock so
// only one of them can observe CommentID == "" and create a fresh comment;
// the loser sees the winner's CommentID and takes the update branch. fn
// receives stores bound to the tx so any subsequent writes participate in
// the same transaction.
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

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

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

// Enabled returns true when the org has an active Linear integration. The
// session-create path uses this to short-circuit detection — design 62
// §"Path C — Linear is not enabled" demands a silent no-op.
func (s *Service) Enabled(ctx context.Context, orgID uuid.UUID) bool {
	if s == nil || s.integrations == nil {
		return false
	}
	integration, err := s.integrations.GetByOrgAndProvider(ctx, orgID, "linear")
	if err != nil {
		return false
	}
	return integration.Status == "active"
}

// integrationFor returns the integration row + linear access token for an
// org. Both must be present for any read/write to Linear; if either is
// missing, callers should treat it as a silent no-op.
func (s *Service) integrationFor(ctx context.Context, orgID uuid.UUID) (models.Integration, string, error) {
	integration, err := s.integrations.GetByOrgAndProvider(ctx, orgID, "linear")
	if err != nil {
		return models.Integration{}, "", fmt.Errorf("lookup linear integration: %w", err)
	}
	cred, err := s.credentials.Get(ctx, orgID, models.ProviderLinear)
	if err != nil {
		return integration, "", fmt.Errorf("lookup linear credential: %w", err)
	}
	if cred == nil {
		return integration, "", fmt.Errorf("linear credential not found")
	}
	cfg, ok := cred.Config.(models.LinearConfig)
	if !ok {
		// Include the observed concrete type so operators chasing this
		// don't have to repro to find out what we got. Most common cause
		// is a credential row that was written through the wrong provider
		// path (e.g. GitHub config saved under a Linear credential).
		return integration, "", fmt.Errorf("linear credential config is wrong type: got %T", cred.Config)
	}
	return integration, cfg.AccessToken, nil
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
	if cached, ok := s.teamKeyCache.get(orgID); ok {
		// Defensive copy: callers iterate this map during detection but the
		// cache shares the underlying storage across requests, so a mutation
		// here would silently corrupt every future caller's allowlist for the
		// org. Cheap relative to the DB hit it replaces.
		return copyAllowlist(cached), nil
	}
	keys, err := s.teamKeys.ListByOrg(ctx, orgID)
	if err != nil {
		return nil, err
	}
	allow := make(map[string]bool, len(keys))
	for _, k := range keys {
		allow[k.TeamKey] = true
	}
	if evicted := s.teamKeyCache.put(orgID, allow); evicted > 0 {
		// Eviction count surfaces TTL activity to operators chasing "is the
		// cache actually expiring?" without committing to per-hit logging.
		s.logger.Debug().
			Int("evicted", evicted).
			Str("org_id", orgID.String()).
			Msg("linear team-key cache: swept expired entries on miss")
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

// RefreshTeamKeys pulls the team list from Linear and replaces the cache.
// Called after OAuth install and on a 24h cron. Idempotent.
func (s *Service) RefreshTeamKeys(ctx context.Context, orgID uuid.UUID) error {
	integration, token, err := s.integrationFor(ctx, orgID)
	if err != nil {
		return err
	}
	client, err := s.clientFactory(ctx, token)
	if err != nil {
		return fmt.Errorf("build linear client: %w", err)
	}
	teams, err := client.ListTeamKeys(ctx)
	if err != nil {
		return fmt.Errorf("list linear team keys: %w", err)
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
	s.teamKeyCache.invalidate(orgID)
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
	integration, token, err := s.integrationFor(ctx, orgID)
	if err != nil {
		return nil, err
	}
	client, err := s.clientFactory(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("build linear client: %w", err)
	}

	fetched, err := client.FetchIssue(ctx, hit.Identifier)
	if err != nil {
		return nil, fmt.Errorf("fetch linear issue %q: %w", hit.Identifier, err)
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
	linkID, err := s.links.CreateAllowingNullRepo(ctx, orgID, sessionID, resolved.LocalID, role, position, addedByUserID)
	if err != nil {
		return uuid.Nil, err
	}
	auditReason := ""
	if resolved.Issue.RepositoryID == nil {
		auditReason = "linear_null_repo_carveout"
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
	issue := &models.Issue{
		OrgID:               orgID,
		ExternalID:          fetched.ID,
		Source:              models.IssueSourceLinear,
		SourceIntegrationID: &integrationID,
		RepositoryID:        fetched.RepositoryID,
		Title:               title,
		Description:         &desc,
		Status:              "open",
		FirstSeenAt:         now,
		LastSeenAt:          now,
		OccurrenceCount:     1,
		Severity:            "medium",
		Tags:                tags,
		Fingerprint:         "linear:" + fetched.ID,
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
