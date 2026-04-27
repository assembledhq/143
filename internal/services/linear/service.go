package linear

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// Service is the single owned boundary for Linear session-linking work
// (detection resolution, issue upsert, provider-state reads/writes,
// attachment/comment/state mutations, coexistence checks). The session-create
// handler decides only "resolve inline or enqueue pre-start preparation"; the
// service answers everything else.
type Service struct {
	logger zerolog.Logger

	integrations      IntegrationReader
	credentials       CredentialReader
	issues            *db.IssueStore
	links             *db.SessionIssueLinkStore
	teamKeys          *db.LinearTeamKeyStore
	providerState     *db.LinearProviderStateStore
	stateEvents       *db.LinearStateEventStore
	sessions          *db.SessionStore
	clientFactory     ClientFactory
	orgSettingsLoader OrgSettingsLoader
	jobEnqueuer       JobEnqueuer
	linksChanged      LinksChangedNotifier
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
	}
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
		return integration, "", fmt.Errorf("linear credential config is wrong type")
	}
	return integration, cfg.AccessToken, nil
}

// TeamKeyAllowlist returns the org's cached team-key allowlist as a map for
// detection. Cheap; detection runs on every session create. Refreshes are
// driven by the integration sync worker, not this hot path.
func (s *Service) TeamKeyAllowlist(ctx context.Context, orgID uuid.UUID) (map[string]bool, error) {
	if s == nil || s.teamKeys == nil {
		return map[string]bool{}, nil
	}
	keys, err := s.teamKeys.ListByOrg(ctx, orgID)
	if err != nil {
		return nil, err
	}
	allow := make(map[string]bool, len(keys))
	for _, k := range keys {
		allow[k.TeamKey] = true
	}
	return allow, nil
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
	return s.teamKeys.ReplaceForIntegration(ctx, orgID, integration.ID, workspaceID, rows)
}

// ResolvePrimary fetches the unambiguous primary Linear issue and the local
// issues row for it. Used by the session-create fast path and the
// prepare_linear_primary worker.
//
// Workspace verification: URL refs must match the connected workspace; bare
// identifiers are confirmed via the Linear API. Cross-workspace refs drop
// silently as required by design.
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
