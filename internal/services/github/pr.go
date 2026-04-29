package github

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/llm"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/github/identity"
	"github.com/assembledhq/143/internal/services/storage"
)

const (
	defaultGitHubAPI   = "https://api.github.com"
	maxBranchSlugLen   = 60
	maxLabelsToCreate  = 5
	maxPRTitleLen      = 120
	prTemplateCacheTTL = 24 * time.Hour // re-fetch repo PR template after this duration
)

// PreviewStopper stops a running preview instance. Implemented by
// *preview.Manager; extracted as an interface here to avoid importing the
// preview package (which depends on this package).
type PreviewStopper interface {
	StopPreview(ctx context.Context, orgID, previewID uuid.UUID) error
}

// PRService handles GitHub PR creation and webhook-based tracking.
type PRService struct {
	tokenProvider   *Service
	pullRequests    *db.PullRequestStore
	sessions        *db.SessionStore
	issues          *db.IssueStore
	deploys         *db.DeployStore
	validations     *db.ValidationStore
	repos           *db.RepositoryStore
	jobs            *db.JobStore
	reviewComments  *db.ReviewCommentStore
	integrations    *db.IntegrationStore
	userCredentials *db.UserCredentialStore
	sessionMessages *db.SessionMessageStore
	appUserAuth     interface {
		GetValidCredential(ctx context.Context, orgID, userID uuid.UUID) (*models.GitHubAppUserConfig, error)
	}
	users            *db.UserStore
	orgs             *db.OrganizationStore
	prTemplates      *db.PRTemplateStore
	previews         *db.PreviewStore
	previewStopper   PreviewStopper
	prHealthStreams  *cache.PullRequestStreams
	llmClient        llm.Client
	audit            *db.AuditEmitter
	sandboxProvider  agent.SandboxProvider // used by the push-based PR flow
	snapshots        storage.SnapshotStore // used by the push-based PR flow
	logger           zerolog.Logger
	baseURL          string
	httpClient       *http.Client
	linearMilestones LinearMilestoneEnqueuer // nil-safe: Linear writes disabled if nil

	// cachedResolverMu guards lazy construction of cachedResolver. The
	// resolver is built from the currently-wired dependencies on first use
	// and cached for subsequent calls; any Set* mutator that changes a
	// resolver-relevant dependency invalidates it via invalidateResolver()
	// so a late wiring change picks up before the next Resolve call.
	cachedResolverMu sync.Mutex
	cachedResolver   *identity.Resolver

	// postPRSnapshotUploads tracks in-flight goroutines that stream the
	// post-PR sandbox snapshot to object storage. Used by tests to await
	// upload completion before asserting on session state. Production
	// callers don't need to wait — the goroutine self-completes and updates
	// the session row atomically.
	postPRSnapshotUploads sync.WaitGroup
}

// LinearMilestoneEnqueuer is the post-event hook that fires the Linear
// attachment + comment + state-sync writes after a session reaches a
// milestone (PR opened / PR merged / etc.). Held as a function so PRService
// stays decoupled from the Linear package — the function lives in the
// linker service and packages a worker enqueue + payload.
type LinearMilestoneEnqueuer func(ctx context.Context, orgID, sessionID uuid.UUID, event string, prNumber int)

// SetLinearMilestoneEnqueuer wires the Linear post-event hook.
func (s *PRService) SetLinearMilestoneEnqueuer(enq LinearMilestoneEnqueuer) {
	s.linearMilestones = enq
}

func NewPRService(
	tokenProvider *Service,
	pullRequests *db.PullRequestStore,
	sessions *db.SessionStore,
	issues *db.IssueStore,
	deploys *db.DeployStore,
	validations *db.ValidationStore,
	repos *db.RepositoryStore,
	jobs *db.JobStore,
	logger zerolog.Logger,
) *PRService {
	return &PRService{
		tokenProvider: tokenProvider,
		pullRequests:  pullRequests,
		sessions:      sessions,
		issues:        issues,
		deploys:       deploys,
		validations:   validations,
		repos:         repos,
		jobs:          jobs,
		logger:        logger,
		baseURL:       defaultGitHubAPI,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
	}
}

// SetReviewCommentStore sets the review comment store for the feedback loop.
func (s *PRService) SetReviewCommentStore(store *db.ReviewCommentStore) {
	s.reviewComments = store
}

// SetIntegrationStore sets the integration store for installation fallback lookups.
func (s *PRService) SetIntegrationStore(store *db.IntegrationStore) {
	s.integrations = store
	s.invalidateResolver()
}

// IntegrationStore exposes the configured integration store for wiring tests.
func (s *PRService) IntegrationStore() *db.IntegrationStore {
	return s.integrations
}

// SetUserCredentialStore sets the user credential store for user-authored PRs.
func (s *PRService) SetUserCredentialStore(store *db.UserCredentialStore) {
	s.userCredentials = store
}

func (s *PRService) SetSessionMessageStore(store *db.SessionMessageStore) {
	s.sessionMessages = store
}

// SetAppUserAuth wires the refresh-aware GitHub App user auth service used to
// author PRs as the triggering user.
func (s *PRService) SetAppUserAuth(auth interface {
	GetValidCredential(ctx context.Context, orgID, userID uuid.UUID) (*models.GitHubAppUserConfig, error)
}) {
	s.appUserAuth = auth
	s.invalidateResolver()
}

// HasAppUserAuth reports whether the refresh-aware GitHub App user auth
// service has been wired. Exposed for worker wiring tests.
func (s *PRService) HasAppUserAuth() bool {
	return s.appUserAuth != nil
}

// SetLLMClient sets the LLM client for repo PR template filling.
func (s *PRService) SetLLMClient(client llm.Client) {
	s.llmClient = client
}

// LLMClient exposes the configured LLM client for wiring tests.
func (s *PRService) LLMClient() llm.Client {
	return s.llmClient
}

// SetUserStore sets the user store for fetching user info during PR creation.
func (s *PRService) SetUserStore(store *db.UserStore) {
	s.users = store
	s.invalidateResolver()
}

// UserStore exposes the configured user store for wiring tests.
func (s *PRService) UserStore() *db.UserStore {
	return s.users
}

// SetAuditEmitter sets the audit emitter used for webhook-triggered audit events.
func (s *PRService) SetAuditEmitter(audit *db.AuditEmitter) {
	s.audit = audit
}

// SetOrgStore sets the organization store for fetching org settings.
func (s *PRService) SetOrgStore(store *db.OrganizationStore) {
	s.orgs = store
}

// OrgStore exposes the configured organization store for wiring tests.
func (s *PRService) OrgStore() *db.OrganizationStore {
	return s.orgs
}

// SetPRTemplateStore sets the PR template cache store.
func (s *PRService) SetPRTemplateStore(store *db.PRTemplateStore) {
	s.prTemplates = store
}

// PRTemplateStore exposes the configured PR template store for wiring tests.
func (s *PRService) PRTemplateStore() *db.PRTemplateStore {
	return s.prTemplates
}

// SetPreviewTeardown wires the preview store and stopper used to stop any
// active preview when a PR is closed. Both args may be nil (no-op) in
// configurations without the preview subsystem.
func (s *PRService) SetPreviewTeardown(previews *db.PreviewStore, stopper PreviewStopper) {
	s.previews = previews
	s.previewStopper = stopper
}

// SetSandboxPushDeps wires the sandbox provider and snapshot store used by the
// push-based PR creation flow. Both must be non-nil to create PRs; if either
// is missing (e.g. tests) CreatePR returns a configuration error.
func (s *PRService) SetSandboxPushDeps(provider agent.SandboxProvider, snapshots storage.SnapshotStore) {
	s.sandboxProvider = provider
	s.snapshots = snapshots
}

func (s *PRService) SetPullRequestStreams(streams *cache.PullRequestStreams) {
	s.prHealthStreams = streams
}

// SandboxProvider exposes the configured sandbox provider for wiring tests.
func (s *PRService) SandboxProvider() agent.SandboxProvider {
	return s.sandboxProvider
}

// SnapshotStore exposes the configured snapshot store for wiring tests.
func (s *PRService) SnapshotStore() storage.SnapshotStore {
	return s.snapshots
}

const (
	// SnapshotExpiredPRMessage is shown when a previously-saved checkpoint was
	// legitimately reaped and can no longer be restored.
	SnapshotExpiredPRMessage = "This session snapshot expired before a PR could be created. Send a new message to rebuild the sandbox, then create the PR again."
	// SnapshotNotCapturedPRMessage is shown when the run completed but never
	// saved a reusable checkpoint for the PR flow.
	SnapshotNotCapturedPRMessage = "This session finished without saving a reusable checkpoint for PR creation. Send a new message to rebuild the sandbox, then create the PR again."
	// SnapshotUnavailablePRMessage is shown when the DB points at a checkpoint
	// that is no longer present in storage.
	SnapshotUnavailablePRMessage = "This session had a saved checkpoint, but it is no longer available in storage. Send a new message to rebuild the sandbox, then create the PR again."
)

// WaitForPostPRSnapshotUploads blocks until every in-flight post-PR snapshot
// upload goroutine spawned by CreatePR has finished. Two callers:
//
//   - Server graceful shutdown drains uploads before exiting so a terminated
//     worker doesn't strand sessions with pending_snapshot_key set forever
//     (cmd/server/main.go).
//   - Tests await upload completion before asserting on the resulting
//     Session state (snapshot_key / pending_snapshot_key).
//
// The wait respects only the goroutines' own per-upload timeout
// (postPRSnapshotUploadTimeout, currently 6 minutes); callers that need a
// shorter budget should run this in a separate goroutine and select on
// their own ctx.
func (s *PRService) WaitForPostPRSnapshotUploads() {
	s.postPRSnapshotUploads.Wait()
}

// ErrSnapshotExpired is returned by CreatePR when the session's saved
// snapshot has truly expired (reaped or cleaned up after retention).
var ErrSnapshotExpired = errors.New("session snapshot expired")

// ErrSnapshotNotCaptured is returned by CreatePR when the session has no
// saved snapshot key at all, meaning the run never persisted a reusable
// checkpoint for PR creation.
var ErrSnapshotNotCaptured = errors.New("session snapshot not captured")

// ErrSnapshotUnavailable is returned by CreatePR when the session points at a
// saved snapshot key but the underlying blob is missing from storage.
var ErrSnapshotUnavailable = errors.New("session snapshot unavailable")

// ErrGitHubUserAuthRequired and ErrGitHubUserAuthRepoAccessDenied are
// re-exports of the identity-package sentinels so handlers in the api/handlers
// package can match resolver errors via errors.Is without taking a direct
// dependency on the identity package.
var (
	ErrGitHubUserAuthRequired         = identity.ErrUserAuthRequired
	ErrGitHubUserAuthRepoAccessDenied = identity.ErrUserAuthRepoAccessDenied
)

// ErrNoChanges is returned by CreatePR when the restored workspace has no
// uncommitted changes relative to the base branch — there's nothing to push.
// This typically means the diff was reverted or the session produced no edits.
var ErrNoChanges = errors.New("no changes to push")

// identityResolver returns a resolver wired with the PRService's current
// dependencies. Lazily built on first use and cached; any Set* mutator
// that changes a resolver-relevant field calls invalidateResolver() so
// the next call rebuilds. We cache because the resolver itself is
// stateless after construction (all its state is on PRService) and hot
// paths like validateUserToken would otherwise allocate a new resolver
// on every call.
func (s *PRService) identityResolver() *identity.Resolver {
	s.cachedResolverMu.Lock()
	defer s.cachedResolverMu.Unlock()
	if s.cachedResolver != nil {
		return s.cachedResolver
	}
	r := identity.NewResolver(s.tokenProvider, s.logger)
	if s.appUserAuth != nil {
		r.SetAppUserAuth(s.appUserAuth)
	}
	if s.users != nil {
		r.SetUsers(s.users)
	}
	if s.integrations != nil {
		r.SetIntegrations(s.integrations)
	}
	// Reuse the PRService's HTTP client / base URL so test overrides flow
	// through to repo-access probes without re-configuring the resolver.
	if s.httpClient != nil {
		r.SetHTTPClient(s.httpClient)
	}
	if s.baseURL != "" {
		r.SetAPIBaseURL(s.baseURL)
	}
	s.cachedResolver = r
	return r
}

// invalidateResolver drops the cached resolver. Called from each Set*
// method that changes a dependency the resolver consumes, so a late
// wiring change picks up before the next Resolve call.
func (s *PRService) invalidateResolver() {
	s.cachedResolverMu.Lock()
	s.cachedResolver = nil
	s.cachedResolverMu.Unlock()
}

// getInstallationTokenForRepo is a thin compatibility wrapper around the
// identity resolver, kept so the in-package callers (pr_health_service.go,
// SyncSessionTitle) don't need to change. New call sites should use the
// resolver directly.
func (s *PRService) getInstallationTokenForRepo(ctx context.Context, orgID uuid.UUID, repo *models.Repository) (string, error) {
	res, err := s.identityResolver().InstallationTokenForRepo(ctx, orgID, repo, nil)
	if err != nil {
		return "", err
	}
	return res.Token, nil
}

// resolveToken delegates to the shared identity resolver. Kept as a method
// so existing tests that call svc.resolveToken continue to exercise the
// in-package wiring (not just the resolver in isolation).
func (s *PRService) resolveToken(ctx context.Context, run *models.Session, repo *models.Repository, orgSettings models.OrgSettings, authorMode string) (*identity.Resolution, error) {
	return s.identityResolver().Resolve(ctx, run, repo, orgSettings, authorMode)
}

// validateUserToken checks if a user's GitHub token is still valid by calling GET /user.
// Returns false if the token is revoked or expired.
func (s *PRService) validateUserToken(ctx context.Context, token string) bool {
	_, err := s.doGitHubRequest(ctx, token, http.MethodGet, "/user", nil)
	return err == nil
}

// SetBaseURL overrides the GitHub API base URL (for testing).
func (s *PRService) SetBaseURL(url string) {
	s.baseURL = url
	s.invalidateResolver()
}

// CreatePRParams holds optional parameters for PR creation that come from the
// API request (as opposed to org-level defaults). Fields use pointers to
// distinguish "caller explicitly set this" from "use org default".
type CreatePRParams struct {
	Draft      *bool  `json:"draft,omitempty"`
	AuthorMode string `json:"author_mode,omitempty"`
}

// CreatePR opens a GitHub PR from a completed agent session by restoring the
// session's sandbox snapshot, committing any uncommitted changes, pushing to
// a new remote branch, and opening a pull request against the repo's default
// branch via the REST API.
//
// Pushing directly from the restored working tree preserves file modes,
// symlinks, binaries, and `.gitattributes` normalization — all lost by the
// previous flow that reconstructed file content from a unified diff and
// uploaded blobs through the GitHub Trees API. That flow silently truncated
// files whose changes spanned less than the whole file (PR #442).
//
// Returns ErrSnapshotNotCaptured when the session never persisted a
// reusable checkpoint, ErrSnapshotExpired when it was retention-reaped, or
// ErrSnapshotUnavailable when the DB points at a key whose blob is missing
// in storage — the UI prompts the user to re-run in each case.
// Returns ErrNoChanges when the pushed branch produces no diff against the
// base branch; the session's snapshot has no meaningful changes to ship.
func (s *PRService) CreatePR(ctx context.Context, run *models.Session, params ...CreatePRParams) (*models.PullRequest, error) {
	// Idempotency: a worker retry after a partial success (push landed, PR
	// created, but state update crashed) would otherwise create a duplicate
	// PR. If a PR already exists for this session, return it without
	// re-pushing.
	if existing, err := s.pullRequests.GetBySessionID(ctx, run.OrgID, run.ID); err == nil {
		// If the original CreatePR crashed mid-upload, the session may
		// still carry a pending_snapshot_key with no in-flight uploader.
		// Surface that here so the stuck-resume case is observable; the
		// orchestrator's gate will keep continue_session retrying until a
		// reconciler (or worker shutdown drain) clears it.
		if run.PendingSnapshotKey != nil && *run.PendingSnapshotKey != "" {
			s.logger.Warn().
				Str("session_id", run.ID.String()).
				Str("pending_snapshot_key", *run.PendingSnapshotKey).
				Msg("CreatePR retry hit existing PR with pending_snapshot_key still set; resume will block until cleared")
		}
		return &existing, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("check existing pull request: %w", err)
	}

	if s.sandboxProvider == nil || s.snapshots == nil {
		return nil, fmt.Errorf("PRService: sandbox push dependencies not configured")
	}

	if run.SnapshotKey == nil || *run.SnapshotKey == "" {
		return nil, ErrSnapshotNotCaptured
	}

	// Issue lookup is optional — sessions may not have an associated issue.
	var issue *models.Issue
	if run.PrimaryIssueID != nil {
		i, err := s.issues.GetByID(ctx, run.OrgID, *run.PrimaryIssueID)
		if err == nil {
			issue = &i
		} else {
			s.logger.Warn().Err(err).Str("issue_id", run.PrimaryIssueID.String()).Msg("failed to look up issue, proceeding without it")
		}
	}

	// Resolve repository. sessions.repository_id is the canonical source of
	// truth — session creation copies issue.repository_id into it up front.
	if run.RepositoryID == nil {
		return nil, fmt.Errorf("session %s has no repository", run.ID)
	}
	repo, err := s.repos.GetByID(ctx, run.OrgID, *run.RepositoryID)
	if err != nil {
		return nil, fmt.Errorf("get repository: %w", err)
	}

	orgSettings := models.OrgSettings{PRAuthorship: models.PRAuthorshipUserPreferred}
	if s.orgs != nil {
		org, orgErr := s.orgs.GetByID(ctx, run.OrgID)
		if orgErr == nil {
			if parsed, parseErr := models.ParseOrgSettings(org.Settings); parseErr == nil {
				orgSettings = parsed
			}
		}
	}

	var opts CreatePRParams
	for _, param := range params {
		if param.Draft != nil {
			opts.Draft = param.Draft
		}
		if param.AuthorMode != "" {
			opts.AuthorMode = param.AuthorMode
		}
	}

	resolution, err := s.resolveToken(ctx, run, &repo, orgSettings, opts.AuthorMode)
	if err != nil {
		return nil, fmt.Errorf("resolve token: %w", err)
	}
	token := resolution.Token

	owner, repoName := splitRepo(repo.FullName)
	defaultBranch := repo.DefaultBranch
	if defaultBranch == "" {
		defaultBranch = "main"
	}

	branchName := formatBranchName(run, issue)
	commitMsg := formatCommitMessage(run, issue)
	authorName, authorEmail := identity.CommitIdentity(resolution)
	if !resolution.IsUserToken() && run.TriggeredByUserID != nil && s.users != nil {
		// App token: attribute the change via a Co-authored-by trailer so the
		// GitHub UI surfaces the human who kicked off the run.
		if user, userErr := s.users.GetByID(ctx, run.OrgID, *run.TriggeredByUserID); userErr == nil {
			if trailer := identity.CoAuthorTrailer(&user); trailer != "" {
				commitMsg += "\n\n" + trailer
			}
		}
	}

	pushed, err := s.pushSessionBranch(ctx, run, &repo, token, *run.SnapshotKey, branchName, commitMsg, authorName, authorEmail)
	if err != nil {
		return nil, err
	}
	// Ensure the captured tar is removed on every error path between here
	// and the dispatch site below — including PR-content generation, label
	// attachment, pullRequests.Create, and SetPendingSnapshotKey failures.
	// On the success path, dispatchPostPRSnapshotUpload zeroes
	// pushed.CapturedSnapshotPath to transfer ownership to the goroutine,
	// and this defer becomes a no-op (intentional asymmetry: the goroutine
	// removes the file on completion; double-removing would race a future
	// caller's tempfile if pid recycled the inode).
	defer func() {
		if pushed != nil && pushed.CapturedSnapshotPath != "" {
			_ = os.Remove(pushed.CapturedSnapshotPath)
		}
	}()

	var title, body string
	if generated, genErr := s.generatePRContent(ctx, token, owner, repoName, defaultBranch, *run.RepositoryID, run.OrgID, run, issue); genErr == nil {
		title = generated.Title
		body = generated.Body
	} else {
		s.logger.Warn().Err(genErr).Msg("LLM PR content generation failed, falling back to static")
	}
	if title == "" {
		title = formatPRTitle(run, issue)
	}
	if body == "" {
		body = s.formatPRBody(ctx, run, issue)
	}

	draft := orgSettings.PRDraftDefault
	if opts.Draft != nil {
		draft = *opts.Draft
	}
	var prOpts []prCreateOption
	if draft {
		prOpts = append(prOpts, withDraft(true))
	}

	prNumber, prURL, err := s.createOrGetPullRequest(ctx, token, owner, repoName, title, body, branchName, defaultBranch, prOpts...)
	if err != nil {
		// GitHub returns 422 "No commits between" when the pushed branch
		// has no diff against the base. Map that to ErrNoChanges so the UI
		// can show a targeted message instead of a generic API error.
		var apiErr *GitHubAPIError
		if errors.As(err, &apiErr) && apiErr.IsNoCommitsBetween() {
			return nil, ErrNoChanges
		}
		return nil, fmt.Errorf("create pull request: %w", err)
	}

	labels := buildLabels(issue)
	if len(labels) > 0 {
		if labelErr := s.addLabels(ctx, token, owner, repoName, prNumber, labels); labelErr != nil {
			s.logger.Warn().Err(labelErr).Int("pr_number", prNumber).Msg("failed to add labels to PR")
		}
	}

	authoredBy := resolution.AuthoredBy()
	headSHA := pushed.HeadSHA
	pr := &models.PullRequest{
		SessionID:      &run.ID,
		OrgID:          run.OrgID,
		GitHubPRNumber: prNumber,
		GitHubPRURL:    prURL,
		GitHubRepo:     repo.FullName,
		Title:          title,
		Body:           &body,
		Status:         "open",
		ReviewStatus:   "pending",
		AuthoredBy:     authoredBy,
		HeadSHA:        &headSHA,
	}
	if err := s.pullRequests.Create(ctx, pr); err != nil {
		return nil, fmt.Errorf("store pull request: %w", err)
	}

	// Record the pending snapshot, then dispatch the upload. If capture
	// failed in pushSessionBranch the path is empty — log and skip the
	// upload, leaving Session.SnapshotKey at its pre-PR value (degraded
	// resume, but the PR itself is fine).
	if pushed.CapturedSnapshotErr != nil {
		s.logger.Warn().
			Err(pushed.CapturedSnapshotErr).
			Str("session_id", run.ID.String()).
			Msg("post-PR sandbox snapshot capture failed; resume will see stale state")
	} else if pushed.CapturedSnapshotPath != "" {
		newSnapshotKey := fmt.Sprintf("snapshots/%s/%s/post-pr.tar.zst", run.OrgID, run.ID)
		if setErr := s.sessions.SetPendingSnapshotKey(ctx, run.OrgID, run.ID, newSnapshotKey); setErr != nil {
			s.logger.Warn().Err(setErr).Str("session_id", run.ID.String()).Msg("failed to set pending snapshot key")
		} else {
			s.dispatchPostPRSnapshotUpload(run.OrgID, run.ID, newSnapshotKey, pushed.CapturedSnapshotPath, pushed.CapturedSnapshotSize)
			// Ownership invariant: from this point on, the goroutine
			// spawned by dispatchPostPRSnapshotUpload solely owns the temp
			// file at pushed.CapturedSnapshotPath. Clearing the path here
			// prevents the deferred cleanup at function entry from
			// double-removing it. Anything that needs the file (re-upload,
			// recovery) must coordinate via pending_snapshot_key, not by
			// re-reading the path.
			pushed.CapturedSnapshotPath = ""
		}
	}

	// Fire the Linear PR-opened milestone (attachment subtitle update,
	// rolling-comment refresh, optional workflow-state move under guards).
	// nil-safe: when no Linear linker is wired, this is a no-op.
	if s.linearMilestones != nil {
		s.linearMilestones(ctx, run.OrgID, run.ID, "pr_opened", prNumber)
	}

	if err := s.sessions.UpdateStatus(ctx, run.OrgID, run.ID, "pr_created"); err != nil {
		s.logger.Warn().Err(err).Str("run_id", run.ID.String()).Msg("failed to update agent run status")
	}

	if issue != nil && run.PrimaryIssueID != nil {
		if err := s.issues.UpdateStatus(ctx, run.OrgID, *run.PrimaryIssueID, "in_progress"); err != nil {
			s.logger.Warn().Err(err).Str("issue_id", run.PrimaryIssueID.String()).Msg("failed to update issue status")
		}
	}

	return pr, nil
}

// postPRSnapshotUploadTimeout bounds a single upload attempt. Deliberately
// kept under the worker package's maxRetryableDuration (8m) so that a
// continue_session job blocked on ErrSnapshotPending will outlast the upload
// window — i.e. when the gate finally lifts (Promote or Clear), the job is
// still alive to retry, not already dead-lettered. If you raise this, raise
// maxRetryableDuration in lockstep or expect resumes to dead-letter under
// load (large sandbox tar + slow object storage).
const postPRSnapshotUploadTimeout = 6 * time.Minute

// dispatchPostPRSnapshotUpload streams the captured push-sandbox tar to
// object storage in the background, then atomically promotes
// pending_snapshot_key into snapshot_key on success or clears it on failure.
// The goroutine uses a fresh background context (the worker job's ctx is
// likely cancelled by the time we return here), bounded by
// postPRSnapshotUploadTimeout to keep upload retries from leaking forever.
//
// Owns the temp file at tarPath: removes it on completion regardless of
// outcome. tarSize is logged for observability so spikes in PR-creation
// snapshot size (e.g. a session that produced an unexpected node_modules
// tree) are visible without re-stat'ing the file.
func (s *PRService) dispatchPostPRSnapshotUpload(orgID, sessionID uuid.UUID, key, tarPath string, tarSize int64) {
	s.postPRSnapshotUploads.Add(1)
	go func() {
		defer s.postPRSnapshotUploads.Done()
		bgCtx, cancel := context.WithTimeout(context.Background(), postPRSnapshotUploadTimeout)
		defer cancel()
		defer func() { _ = os.Remove(tarPath) }()

		// #nosec G304 -- tarPath was produced by os.CreateTemp inside captureSandboxSnapshot earlier in this same process; it is not user input.
		f, openErr := os.Open(tarPath)
		if openErr != nil {
			s.logger.Warn().Err(openErr).Str("session_id", sessionID.String()).Msg("open post-PR snapshot tar failed")
			if clearErr := s.sessions.ClearPendingSnapshot(bgCtx, orgID, sessionID); clearErr != nil {
				s.logger.Warn().Err(clearErr).Str("session_id", sessionID.String()).Msg("clear pending snapshot after open failure failed")
			}
			return
		}
		defer f.Close()

		startedAt := time.Now()
		if saveErr := s.snapshots.Save(bgCtx, key, f); saveErr != nil {
			s.logger.Warn().Err(saveErr).Str("session_id", sessionID.String()).Str("key", key).Int64("tar_size_bytes", tarSize).Msg("post-PR snapshot upload failed")
			if clearErr := s.sessions.ClearPendingSnapshot(bgCtx, orgID, sessionID); clearErr != nil {
				s.logger.Warn().Err(clearErr).Str("session_id", sessionID.String()).Msg("clear pending snapshot after save failure failed")
			}
			return
		}

		if promoteErr := s.sessions.PromotePendingSnapshot(bgCtx, orgID, sessionID, key); promoteErr != nil {
			s.logger.Warn().Err(promoteErr).Str("session_id", sessionID.String()).Str("key", key).Msg("promote pending snapshot failed")
			return
		}
		s.logger.Info().
			Str("session_id", sessionID.String()).
			Str("key", key).
			Int64("tar_size_bytes", tarSize).
			Dur("upload_duration", time.Since(startedAt)).
			Msg("post-PR snapshot promoted")
	}()
}

// SyncSessionTitle propagates a user-edited session title to the existing PR,
// if one exists. Edited titles intentionally override any result-summary-based
// auto title so the session header and PR stay aligned.
func (s *PRService) SyncSessionTitle(ctx context.Context, session *models.Session) error {
	if session == nil || session.Title == nil || strings.TrimSpace(*session.Title) == "" {
		return nil
	}
	if s.pullRequests == nil || s.repos == nil || s.tokenProvider == nil {
		return fmt.Errorf("PRService: title sync dependencies not configured")
	}

	pr, err := s.pullRequests.GetBySessionID(ctx, session.OrgID, session.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("load pull request: %w", err)
	}

	var issue *models.Issue
	if session.PrimaryIssueID != nil && s.issues != nil {
		i, err := s.issues.GetByID(ctx, session.OrgID, *session.PrimaryIssueID)
		if err == nil {
			issue = &i
		} else {
			s.logger.Warn().Err(err).Str("issue_id", session.PrimaryIssueID.String()).Msg("failed to load issue for PR title sync")
		}
	}

	repo, err := s.repos.GetByFullNameAnyStatus(ctx, session.OrgID, pr.GitHubRepo)
	if err != nil {
		return fmt.Errorf("get repository: %w", err)
	}

	titleSession := *session
	titleSession.ResultSummary = nil
	title := formatSyncedPRTitle(&titleSession, issue)
	if title == "" {
		return nil
	}

	token, err := s.getInstallationTokenForRepo(ctx, session.OrgID, &repo)
	if err != nil {
		return fmt.Errorf("get installation token: %w", err)
	}

	owner, repoName := splitRepo(pr.GitHubRepo)
	if err := s.updatePullRequestTitle(ctx, token, owner, repoName, pr.GitHubPRNumber, title); err != nil {
		return fmt.Errorf("update pull request title: %w", err)
	}

	if err := s.pullRequests.UpdateTitle(ctx, session.OrgID, pr.ID, title); err != nil {
		return fmt.Errorf("store pull request title: %w", err)
	}

	return nil
}

// pushSessionBranch hydrates the session's sandbox snapshot into a fresh
// container, stages + commits any uncommitted changes, and pushes HEAD to a
// new remote branch. The sandbox is always destroyed on return.
//
// The GitHub token is never passed via argv or URL. It's written to a file
// inside the sandbox and read by a GIT_ASKPASS helper, so `ps` inside the
// container shows only a plain https URL with no credentials.
// pushResult captures what pushSessionBranch produced on a successful push:
// the new HEAD SHA from the remote (parsed from the script's stdout sentinel),
// and an optional snapshot of the post-push sandbox spooled to a local temp
// file. The snapshot is best-effort — if capture fails, the caller still gets
// a valid HeadSHA (PR creation succeeds) and CapturedSnapshotErr is set so
// the caller can log it. The caller owns the temp file and MUST remove it
// once it has finished streaming or has decided to abandon it.
type pushResult struct {
	HeadSHA              string
	CapturedSnapshotPath string
	CapturedSnapshotSize int64
	CapturedSnapshotErr  error
}

func (s *PRService) pushSessionBranch(
	ctx context.Context,
	run *models.Session,
	repo *models.Repository,
	token, snapshotKey, branchName, commitMsg, authorName, authorEmail string,
) (*pushResult, error) {
	cfg := agent.DefaultSandboxConfig()
	cfg.SessionID = run.ID.String()
	cfg.OrgID = run.OrgID.String()
	cfg.Purpose = "pr_push"
	// Mirror the workspace layout used at session start so restore overlays
	// the snapshot onto the path git already has recorded in .git/config.
	if slug := agent.SlugForRepo(repo.FullName); slug != "" {
		cfg.WorkDir = fmt.Sprintf("%s/%s", cfg.HomeDir, slug)
	}

	sandbox, err := agent.HydrateSandboxFromSnapshot(ctx, s.sandboxProvider, s.snapshots, snapshotKey, cfg)
	if err != nil {
		if errors.Is(err, agent.ErrSnapshotMissing) {
			return nil, ErrSnapshotUnavailable
		}
		return nil, fmt.Errorf("hydrate sandbox: %w", err)
	}
	defer func() {
		// Use a detached context with a short timeout so cleanup runs even
		// when the caller's ctx was cancelled (and bounds the cleanup cost).
		destroyCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if destroyErr := s.sandboxProvider.Destroy(destroyCtx, sandbox); destroyErr != nil {
			s.logger.Warn().Err(destroyErr).Str("session_id", run.ID.String()).Msg("failed to destroy push sandbox")
		}
	}()

	// Write the commit message, credential, and askpass helper to files.
	// Passing the credential via file keeps it out of argv and shell history.
	if err := s.sandboxProvider.WriteFile(ctx, sandbox, pushCommitMsgPath, []byte(commitMsg)); err != nil {
		return nil, fmt.Errorf("write commit message to sandbox: %w", err)
	}
	if err := s.sandboxProvider.WriteFile(ctx, sandbox, pushInputPath, []byte(token)); err != nil {
		return nil, fmt.Errorf("write credential to sandbox: %w", err)
	}
	helperScript := "#!/bin/sh\nexec cat " + shellQuote(pushInputPath) + "\n"
	if err := s.sandboxProvider.WriteFile(ctx, sandbox, pushHelperPath, []byte(helperScript)); err != nil {
		return nil, fmt.Errorf("write push helper to sandbox: %w", err)
	}

	pushURL := fmt.Sprintf("https://x-access-token@github.com/%s.git", repo.FullName)
	script := buildPushScript(sandbox.WorkDir, authorName, authorEmail, branchName, pushURL)

	var stdout, stderr bytes.Buffer
	exitCode, execErr := s.sandboxProvider.Exec(ctx, sandbox, script, &stdout, &stderr)
	if execErr != nil {
		return nil, fmt.Errorf("exec push script: %w", execErr)
	}
	switch exitCode {
	case 0:
		// Continue to HeadSHA parse + snapshot capture below.
	case pushExitNoChanges:
		return nil, ErrNoChanges
	default:
		msg := strings.TrimSpace(stderr.String())
		// Defense in depth: if the token ever leaks into stderr (e.g. via a
		// future code path that reintroduces it), scrub before returning.
		msg = strings.ReplaceAll(msg, token, "***")
		if msg == "" {
			msg = "(no stderr)"
		}
		return nil, fmt.Errorf("git push failed (exit %d): %s", exitCode, msg)
	}

	headSHA, parseErr := parsePushHeadSHA(stdout.String())
	if parseErr != nil {
		return nil, fmt.Errorf("parse push head sha: %w", parseErr)
	}

	result := &pushResult{HeadSHA: headSHA}
	// Capture a snapshot of the post-push sandbox before the deferred Destroy
	// runs. This is the state we want a "Fix tests" / continue resume to see:
	// clean working tree, HEAD at the just-pushed commit, working branch
	// tracking origin. Best-effort — if it fails, the caller logs and
	// proceeds without advancing Session.SnapshotKey.
	if path, size, captureErr := s.captureSandboxSnapshot(ctx, sandbox); captureErr != nil {
		result.CapturedSnapshotErr = captureErr
	} else {
		result.CapturedSnapshotPath = path
		result.CapturedSnapshotSize = size
	}
	return result, nil
}

// parsePushHeadSHA scans the push script's stdout for the well-known
// __143_HEAD_SHA=<sha> sentinel line and returns the SHA. Returns an error if
// no sentinel is present (the script always emits one on the success branch,
// so its absence indicates the script ran a different path than expected).
//
// The SHA must match a 40-char lowercase hex string (git's SHA-1 object
// format). Anchoring on the format prevents a future server-side hook,
// custom git config, or unrelated stdout line from masquerading as the
// sentinel — only `__143_HEAD_SHA=<40-hex>` on its own line is accepted.
func parsePushHeadSHA(stdout string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(stdout))
	var sawSentinelButInvalid bool
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		match := pushHeadSHALineRE.FindStringSubmatch(line)
		if match == nil {
			if strings.HasPrefix(line, pushHeadSHASentinel) {
				sawSentinelButInvalid = true
			}
			continue
		}
		return match[1], nil
	}
	if sawSentinelButInvalid {
		return "", fmt.Errorf("head sha sentinel %q present but value is not a 40-char hex SHA", pushHeadSHASentinel)
	}
	return "", fmt.Errorf("head sha sentinel %q not found in stdout", pushHeadSHASentinel)
}

// pushHeadSHALineRE anchors on a complete sentinel line: the prefix, exactly
// 40 lowercase hex chars (git's SHA-1 object format), and nothing else. The
// scanner trims surrounding whitespace before matching.
var pushHeadSHALineRE = regexp.MustCompile(`^` + regexp.QuoteMeta(pushHeadSHASentinel) + `([0-9a-f]{40})$`)

// captureSandboxSnapshot tars the sandbox via the provider and spools the
// archive to a local temp file. Returns (path, size, nil) on success — the
// caller owns the file and is responsible for os.Remove. On failure, the
// temp file (if any was created) is removed before returning.
func (s *PRService) captureSandboxSnapshot(ctx context.Context, sandbox *agent.Sandbox) (string, int64, error) {
	reader, err := s.sandboxProvider.Snapshot(ctx, sandbox)
	if err != nil {
		return "", 0, fmt.Errorf("snapshot sandbox: %w", err)
	}
	defer reader.Close()

	f, err := os.CreateTemp("", "143-pr-snapshot-*.tar.zst")
	if err != nil {
		return "", 0, fmt.Errorf("create temp file: %w", err)
	}
	path := f.Name()

	size, copyErr := io.Copy(f, reader)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(path)
		return "", 0, fmt.Errorf("spool snapshot: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return "", 0, fmt.Errorf("close snapshot file: %w", closeErr)
	}
	return path, size, nil
}

// Sandbox-internal paths used by the push flow. Under /tmp so they're
// auto-cleaned on sandbox destroy; the trap inside the script also removes
// them explicitly on exit for defense in depth.
const (
	pushCommitMsgPath = "/tmp/143-pr-commit-msg"
	pushInputPath     = "/tmp/143-pr-input"
	// /tmp is mounted noexec in sandbox containers; the askpass helper must
	// live on the exec-allowed scratch tmpfs so git can invoke it.
	pushHelperPath = "/var/tmp/143-pr-helper.sh"
)

// pushExitNoChanges is the sentinel exit code the push script uses when the
// restored working tree has no uncommitted changes AND no commits ahead of
// the remote tracking branch — i.e. there is nothing meaningful to push.
const pushExitNoChanges = 77

// pushHeadSHASentinel is a well-known prefix the push script writes to stdout
// after a successful `git push`. The caller scans stdout for this line and
// extracts the just-pushed commit SHA. Chosen to be unlikely to collide with
// any line git itself prints.
const pushHeadSHASentinel = "__143_HEAD_SHA="

// pushScriptTemplate is the shell script executed inside the restored
// sandbox. All variable interpolations are pre-quoted by the caller (see
// buildPushScript) so they're safe to embed directly. The credential is read
// by the `GIT_ASKPASS` helper from pushInputPath — it never appears in argv.
//
// The cleanup function is hoisted into a shell function rather than inlined
// in the trap because `trap 'rm -f %[1]s ...'` would interleave single-
// quoted strings in a way that works but is fragile to reason about.
//
// On the success branch (push lands), the script prints
// `__143_HEAD_SHA=<sha>` so the caller can persist the just-pushed commit
// onto the PullRequest row without a second GitHub round-trip. The line is
// only emitted on the success branch — the `exit %[7]d` no-changes contract
// is unchanged.
const pushScriptTemplate = `set -eu
cleanup() { rm -f %[1]s %[2]s %[3]s; }
trap cleanup EXIT
cd %[4]s
git config user.name %[5]s
git config user.email %[6]s
git add -A
if ! git diff --cached --quiet; then
    git commit -F %[1]s
fi
if git rev-parse --abbrev-ref --symbolic-full-name @{u} >/dev/null 2>&1; then
    if git merge-base --is-ancestor HEAD @{u}; then
        exit %[7]d
    fi
fi
chmod +x %[3]s
GIT_ASKPASS=%[3]s GIT_TERMINAL_PROMPT=0 git push %[8]s HEAD:refs/heads/%[9]s
echo "%[10]s$(git rev-parse HEAD)"
`

// buildPushScript renders pushScriptTemplate with caller-supplied values.
// Every %s interpolation is passed through shellQuote, which correctly
// handles embedded single quotes (via the `'\”` trick) — so any UTF-8
// string is safe to interpolate.
func buildPushScript(workDir, authorName, authorEmail, branchName, pushURL string) string {
	return fmt.Sprintf(
		pushScriptTemplate,
		shellQuote(pushCommitMsgPath),
		shellQuote(pushInputPath),
		shellQuote(pushHelperPath),
		shellQuote(workDir),
		shellQuote(authorName),
		shellQuote(authorEmail),
		pushExitNoChanges,
		shellQuote(pushURL),
		shellQuote(branchName),
		// pushHeadSHASentinel is a compile-time string literal containing only
		// safe shell characters ("__143_HEAD_SHA="), so it's intentionally
		// interpolated raw — no shellQuote. Anyone who later changes the
		// sentinel to include shell metacharacters MUST add quoting here.
		pushHeadSHASentinel,
	)
}

// shellQuote single-quotes s for safe interpolation into a POSIX shell.
// Embedded single quotes are escaped by closing the quoted string, emitting
// an escaped quote, and reopening: foo'bar -> 'foo'\”bar'.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// PullRequestEvent represents a GitHub pull_request webhook event.
type PullRequestEvent struct {
	Action string `json:"action"`
	Number int    `json:"number"`
	PR     struct {
		Merged         bool   `json:"merged"`
		HTMLURL        string `json:"html_url"`
		MergedAt       string `json:"merged_at"`
		MergeCommitSHA string `json:"merge_commit_sha"`
		Head           struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// HandlePullRequestEvent processes pull_request webhook events.
func (s *PRService) HandlePullRequestEvent(ctx context.Context, event PullRequestEvent) error {
	pr, err := s.pullRequests.GetByRepoAndNumber(ctx, event.Repository.FullName, event.Number)
	if err != nil {
		// Not a 143-generated PR — ignore.
		return nil
	}

	switch event.Action {
	case "opened", "reopened", "synchronize":
		s.enqueuePullRequestStateSync(ctx, pr)
		return nil
	case "closed":
		// handled below
	default:
		return nil
	}

	if err := s.applyClosedPRTransition(ctx, pr, event.PR.Merged, event.PR.MergeCommitSHA, event.PR.Head.SHA); err != nil {
		return err
	}

	s.enqueuePullRequestStateSync(ctx, pr)
	return nil
}

// applyClosedPRTransition flips a PR's status to merged/closed and runs the
// matching follow-ups. Shared by the webhook handler and the periodic state
// sync (which self-heals when a webhook is dropped). Prefers the merge commit
// SHA so the deploy row reflects the commit that landed on the base branch
// (squash/rebase merges produce a new SHA distinct from the head); falls back
// to head SHA when GitHub omits merge_commit_sha.
func (s *PRService) applyClosedPRTransition(ctx context.Context, pr models.PullRequest, merged bool, mergeCommitSHA, headSHA string) error {
	if merged {
		if err := s.pullRequests.UpdateStatus(ctx, pr.OrgID, pr.ID, "merged"); err != nil {
			return fmt.Errorf("update PR status to merged: %w", err)
		}
		commitSHA := mergeCommitSHA
		if commitSHA == "" {
			commitSHA = headSHA
		}
		s.runMergedPullRequestFollowUps(ctx, pr, commitSHA)
		return nil
	}

	if err := s.pullRequests.UpdateStatus(ctx, pr.OrgID, pr.ID, "closed"); err != nil {
		return fmt.Errorf("update PR status to closed: %w", err)
	}
	// Tell the Linear linker the session ended without a merge so the
	// attachment subtitle stops saying "PR open" forever and the audit log
	// records the terminal state. Coexistence + per-session disable guards
	// inside the linker still apply. Event string must match
	// linear.MilestoneEndedNoPR; we use a string literal to avoid importing
	// the linear package and creating a cycle.
	if pr.SessionID != nil && s.linearMilestones != nil {
		s.linearMilestones(ctx, pr.OrgID, *pr.SessionID, "ended_no_pr", pr.GitHubPRNumber)
	}
	s.teardownPRPreview(ctx, pr, false)
	s.maybeAutoArchiveSessionOnPRClose(ctx, pr, nil, false)
	return nil
}

func (s *PRService) runMergedPullRequestFollowUps(ctx context.Context, pr models.PullRequest, commitSHA string) {
	// Fire the Linear PR-merged milestone. Coexistence guards inside the
	// linker suppress this when Linear's GitHub integration is already
	// active on the issue (avoiding double cycle/sprint membership).
	if pr.SessionID != nil && s.linearMilestones != nil {
		s.linearMilestones(ctx, pr.OrgID, *pr.SessionID, "pr_merged", pr.GitHubPRNumber)
	}

	var snapshotKey *string

	if pr.SessionID != nil && s.sessions != nil {
		run, err := s.sessions.GetByID(ctx, pr.OrgID, *pr.SessionID)
		if err != nil {
			s.logger.Warn().Err(err).Str("session_id", pr.SessionID.String()).Msg("failed to load session for merged pull request follow-ups")
		} else {
			snapshotKey = run.SnapshotKey
			if s.issues != nil {
				if run.PrimaryIssueID != nil {
					if err := s.issues.UpdateStatus(ctx, pr.OrgID, *run.PrimaryIssueID, "fixed"); err != nil {
						s.logger.Warn().Err(err).Str("issue_id", run.PrimaryIssueID.String()).Msg("failed to update issue status to fixed")
					}
				}
			}
			if s.snapshots != nil {
				if err := storage.CleanupSessionSnapshot(ctx, s.snapshots, s.sessions, pr.OrgID, *pr.SessionID, snapshotKey); err != nil {
					s.logger.Warn().Err(err).Str("session_id", pr.SessionID.String()).Msg("failed to clean up snapshot on merge")
				}
			}
		}
	}

	if s.deploys != nil {
		if _, err := s.deploys.GetByPullRequestID(ctx, pr.OrgID, pr.ID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				commitSHAPtr := commitSHA
				deploy := &models.Deploy{
					PullRequestID: pr.ID,
					OrgID:         pr.OrgID,
					Environment:   "production",
					CommitSHA:     &commitSHAPtr,
				}
				if err := s.deploys.Create(ctx, deploy); err != nil {
					s.logger.Warn().Err(err).Str("pr_id", pr.ID.String()).Msg("failed to create deploy record")
				}
			} else {
				s.logger.Warn().Err(err).Str("pr_id", pr.ID.String()).Msg("failed to check existing deploy record")
			}
		}
	}

	if s.jobs != nil {
		dedupeKey := fmt.Sprintf("evaluate_experiment:%s", pr.ID)
		if _, err := s.jobs.Enqueue(ctx, pr.OrgID, "default", "evaluate_experiment", map[string]string{
			"pull_request_id": pr.ID.String(),
			"commit_sha":      commitSHA,
		}, 5, &dedupeKey); err != nil {
			s.logger.Warn().Err(err).Str("pr_id", pr.ID.String()).Msg("failed to enqueue evaluate_experiment job")
		}
	}

	s.teardownPRPreview(ctx, pr, true)
	s.maybeAutoArchiveSessionOnPRClose(ctx, pr, snapshotKey, true)
}

func (s *PRService) maybeAutoArchiveSessionOnPRClose(ctx context.Context, pr models.PullRequest, snapshotKey *string, merged bool) {
	if pr.SessionID == nil || s.orgs == nil || s.sessions == nil {
		return
	}

	org, err := s.orgs.GetByID(ctx, pr.OrgID)
	if err != nil {
		s.logger.Warn().Err(err).Str("org_id", pr.OrgID.String()).Msg("failed to load org for auto-archive check")
		return
	}
	settings, err := models.ParseOrgSettings(org.Settings)
	if err != nil {
		s.logger.Warn().Err(err).Str("org_id", pr.OrgID.String()).Msg("failed to parse org settings for auto-archive")
		return
	}
	if !settings.AutoArchiveOnPRClose {
		return
	}

	archived, err := s.sessions.ArchiveSystem(ctx, pr.OrgID, *pr.SessionID)
	if err != nil {
		s.logger.Warn().Err(err).
			Str("session_id", pr.SessionID.String()).
			Str("pr_id", pr.ID.String()).
			Msg("failed to auto-archive session on PR close")
		return
	}

	if s.snapshots != nil {
		if snapshotKey == nil {
			run, err := s.sessions.GetByID(ctx, pr.OrgID, *pr.SessionID)
			if err != nil {
				s.logger.Warn().Err(err).Str("session_id", pr.SessionID.String()).Msg("failed to load session snapshot key for auto-archive")
			} else {
				snapshotKey = run.SnapshotKey
			}
		}
		if err := storage.CleanupSessionSnapshot(ctx, s.snapshots, s.sessions, pr.OrgID, *pr.SessionID, snapshotKey); err != nil {
			s.logger.Warn().Err(err).Str("session_id", pr.SessionID.String()).Msg("failed to clean up snapshot on auto-archive")
		}
	}

	if !archived {
		return
	}

	if s.audit != nil {
		sessionIDStr := pr.SessionID.String()
		details, marshalErr := json.Marshal(map[string]any{
			"session_id":       sessionIDStr,
			"pull_request_id":  pr.ID.String(),
			"github_repo":      pr.GitHubRepo,
			"github_pr_number": pr.GitHubPRNumber,
			"merged":           merged,
			"auto_archive":     true,
			"changes": map[string]any{
				"archived_at":         map[string]any{"before": nil, "after": "set"},
				"archived_by_user_id": map[string]any{"before": nil, "after": nil},
			},
		})
		if marshalErr != nil {
			s.logger.Warn().Err(marshalErr).Str("session_id", sessionIDStr).Msg("failed to marshal auto-archive audit details")
		}
		s.audit.EmitWebhookAction(ctx, db.WebhookActionParams{
			OrgID:        pr.OrgID,
			ProviderName: "github",
			Action:       models.AuditActionSessionArchived,
			ResourceType: models.AuditResourceSession,
			ResourceID:   &sessionIDStr,
			Details:      details,
			SessionID:    pr.SessionID,
		})
	}
}

// teardownPRPreview stops the running preview (if any) for a closed PR and
// advances its pr_preview_state row to the terminal status. Best-effort: all
// failures are logged and swallowed so webhook processing continues.
func (s *PRService) teardownPRPreview(ctx context.Context, pr models.PullRequest, merged bool) {
	if s.previews == nil || s.previewStopper == nil || s.repos == nil {
		return
	}

	repo, err := s.repos.GetByFullName(ctx, pr.OrgID, pr.GitHubRepo)
	if err != nil {
		s.logger.Debug().Err(err).Str("repo", pr.GitHubRepo).Msg("no repo row for PR preview teardown")
		return
	}

	state, err := s.previews.GetPRPreviewState(ctx, pr.OrgID, repo.ID, pr.GitHubPRNumber)
	if err != nil {
		// No pr_preview_state row is the common case (PR never had a
		// preview) and not worth a warning. Anything else is an actual
		// database error — log it so we don't silently mask ops issues.
		if !errors.Is(err, pgx.ErrNoRows) {
			s.logger.Warn().Err(err).
				Str("repo", pr.GitHubRepo).
				Int("pr_number", pr.GitHubPRNumber).
				Msg("failed to load pr_preview_state for PR preview teardown")
		}
		return
	}

	if state.LastPreviewInstanceID != nil {
		if stopErr := s.previewStopper.StopPreview(ctx, pr.OrgID, *state.LastPreviewInstanceID); stopErr != nil {
			s.logger.Warn().Err(stopErr).
				Str("preview_id", state.LastPreviewInstanceID.String()).
				Str("pr_id", pr.ID.String()).
				Msg("failed to stop preview on PR close")
		}
	}

	nextStatus := models.PRPreviewStatusClosed
	if merged {
		nextStatus = models.PRPreviewStatusMerged
	}
	if err := s.previews.UpdatePRPreviewStatus(ctx, pr.OrgID, state.ID, nextStatus); err != nil {
		s.logger.Warn().Err(err).
			Str("pr_preview_state_id", state.ID.String()).
			Msg("failed to update pr_preview_state status")
	}
}

// PullRequestReviewEvent represents a GitHub pull_request_review webhook event.
type PullRequestReviewEvent struct {
	Action string `json:"action"`
	Review struct {
		ID    int64  `json:"id"`
		State string `json:"state"`
		Body  string `json:"body"`
		User  struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"review"`
	PullRequest struct {
		Number int `json:"number"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// HandlePullRequestReviewEvent processes pull_request_review webhook events.
func (s *PRService) HandlePullRequestReviewEvent(ctx context.Context, event PullRequestReviewEvent) error {
	if event.Action != "submitted" {
		return nil
	}

	pr, err := s.pullRequests.GetByRepoAndNumber(ctx, event.Repository.FullName, event.PullRequest.Number)
	if err != nil {
		// Not a 143-generated PR — ignore.
		return nil
	}

	var reviewStatus string
	switch event.Review.State {
	case "approved":
		reviewStatus = "approved"
	case "changes_requested":
		reviewStatus = "changes_requested"
	default:
		return nil
	}

	if err := s.pullRequests.UpdateReviewStatus(ctx, pr.OrgID, pr.ID, reviewStatus); err != nil {
		return fmt.Errorf("update review status: %w", err)
	}

	// If the PR was approved, reinforce memories that were active for this repo.
	// This closes the feedback loop: memories that helped produce approved code
	// get stronger, while unused memories naturally decay.
	if reviewStatus == "approved" {
		s.enqueueReinforceMemories(ctx, pr.OrgID, pr.GitHubRepo)
	}

	// If changes were requested and we have review comments from the review body,
	// enqueue a processing job. Individual inline comments are captured by
	// HandlePullRequestReviewCommentEvent.
	if reviewStatus == "changes_requested" && event.Review.Body != "" {
		if s.reviewComments != nil {
			comment := &models.ReviewComment{
				PullRequestID:   pr.ID,
				OrgID:           pr.OrgID,
				GitHubCommentID: event.Review.ID,
				Reviewer:        event.Review.User.Login,
				Body:            event.Review.Body,
				FilterStatus:    "pending",
			}
			if err := s.reviewComments.Create(ctx, comment); err != nil {
				s.logger.Warn().Err(err).Str("pr_id", pr.ID.String()).Msg("failed to create review comment record")
			} else {
				s.enqueueProcessReviewComment(ctx, pr.OrgID, comment.ID, pr.GitHubRepo)
			}
		}
	}

	return nil
}

// PullRequestReviewCommentEvent represents a GitHub pull_request_review_comment webhook event.
type PullRequestReviewCommentEvent struct {
	Action  string `json:"action"`
	Comment struct {
		ID       int64  `json:"id"`
		Body     string `json:"body"`
		Path     string `json:"path"`
		Position *int   `json:"position"`
		User     struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"comment"`
	PullRequest struct {
		Number int `json:"number"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// HandlePullRequestReviewCommentEvent processes pull_request_review_comment webhook events.
// These are inline comments on specific diff lines.
func (s *PRService) HandlePullRequestReviewCommentEvent(ctx context.Context, event PullRequestReviewCommentEvent) error {
	if event.Action != "created" {
		return nil
	}

	pr, err := s.pullRequests.GetByRepoAndNumber(ctx, event.Repository.FullName, event.PullRequest.Number)
	if err != nil {
		// Not a 143-generated PR — ignore.
		return nil
	}

	if s.reviewComments == nil {
		return nil
	}

	comment := &models.ReviewComment{
		PullRequestID:   pr.ID,
		OrgID:           pr.OrgID,
		GitHubCommentID: event.Comment.ID,
		Reviewer:        event.Comment.User.Login,
		Body:            event.Comment.Body,
		FilterStatus:    "pending",
	}
	if event.Comment.Path != "" {
		comment.DiffPath = &event.Comment.Path
	}
	if event.Comment.Position != nil {
		comment.DiffPosition = event.Comment.Position
	}

	if err := s.reviewComments.Create(ctx, comment); err != nil {
		s.logger.Warn().Err(err).Str("pr_id", pr.ID.String()).Msg("failed to create review comment record")
		return nil
	}

	s.enqueueProcessReviewComment(ctx, pr.OrgID, comment.ID, pr.GitHubRepo)
	return nil
}

func (s *PRService) enqueueProcessReviewComment(ctx context.Context, orgID uuid.UUID, commentID uuid.UUID, repo string) {
	dedupeKey := fmt.Sprintf("process_review_comment:%s", commentID)
	if _, err := s.jobs.Enqueue(ctx, orgID, "feedback", "process_review_comment", map[string]string{
		"comment_id": commentID.String(),
		"org_id":     orgID.String(),
		"repo":       repo,
	}, 3, &dedupeKey); err != nil {
		s.logger.Warn().Err(err).Str("comment_id", commentID.String()).Msg("failed to enqueue process_review_comment job")
	}
}

// enqueueReinforceMemories enqueues a job to reinforce memories for a repo.
// The dedupe key is per-repo (not per-PR) so that rapid successive approvals
// for the same repo collapse into a single reinforcement pass. This is correct
// because the handler re-derives which memories are active for the repo rather
// than tracking the specific memories injected into each PR.
func (s *PRService) enqueueReinforceMemories(ctx context.Context, orgID uuid.UUID, repo string) {
	dedupeKey := fmt.Sprintf("reinforce_memories:%s:%s", orgID, repo)
	if _, err := s.jobs.Enqueue(ctx, orgID, "feedback", "reinforce_memories", map[string]string{
		"org_id": orgID.String(),
		"repo":   repo,
	}, 5, &dedupeKey); err != nil {
		s.logger.Warn().Err(err).Str("repo", repo).Msg("failed to enqueue reinforce_memories job")
	}
}

// --- GitHub API helpers ---

func (s *PRService) doGitHubRequest(ctx context.Context, token, method, path string, body any) ([]byte, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, s.baseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := s.httpClient.Do(req) // #nosec G704 -- URL is GitHub API endpoint from config
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		return nil, &GitHubAPIError{
			Method:     method,
			Path:       path,
			StatusCode: resp.StatusCode,
			Body:       respBody,
		}
	}

	return respBody, nil
}

// GitHubAPIError wraps a non-2xx response from the GitHub REST API so callers
// can inspect the status and parsed error details with errors.As rather than
// matching on prose.
type GitHubAPIError struct {
	Method     string
	Path       string
	StatusCode int
	Body       []byte
}

func (e *GitHubAPIError) Error() string {
	return fmt.Sprintf("GitHub API %s %s returned %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

// HTTPStatus exposes the underlying HTTP status code for callers that only
// care about classifying the response (e.g. retry-on-404). Defined as a
// method (not just the StatusCode field) so that downstream packages can
// detect the status structurally with errors.As against a small interface
// without taking a build-time dependency on this concrete type.
func (e *GitHubAPIError) HTTPStatus() int {
	if e == nil {
		return 0
	}
	return e.StatusCode
}

// Message extracts the human-readable message from GitHub's standard error
// envelope ({"message": "..."}). Falls back to the raw body when the body
// is not JSON or the message field is empty. Returned strings are suitable
// for surfacing in toasts.
func (e *GitHubAPIError) Message() string {
	if e == nil {
		return ""
	}
	var parsed struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(e.Body, &parsed); err == nil && strings.TrimSpace(parsed.Message) != "" {
		return parsed.Message
	}
	if len(e.Body) > 0 {
		return string(e.Body)
	}
	return fmt.Sprintf("GitHub API returned %d", e.StatusCode)
}

// IsNoCommitsBetween reports whether this 422 indicates the pushed branch
// has no diff against the base. GitHub's response shape is:
//
//	{ "message": "Validation Failed",
//	  "errors": [ { "resource": "PullRequest", "code": "custom",
//	               "message": "No commits between main and feature" } ] }
//
// We parse the structured body so the check doesn't break on wrapped errors
// and so an unrelated field containing the phrase can't trigger a false
// positive.
func (e *GitHubAPIError) IsNoCommitsBetween() bool {
	if e == nil || e.StatusCode != http.StatusUnprocessableEntity {
		return false
	}
	var parsed struct {
		Errors []struct {
			Resource string `json:"resource"`
			Code     string `json:"code"`
			Message  string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(e.Body, &parsed); err != nil {
		return false
	}
	for _, item := range parsed.Errors {
		if strings.Contains(item.Message, "No commits between") {
			return true
		}
	}
	return false
}

func (e *GitHubAPIError) IsExistingPullRequest() bool {
	if e == nil || e.StatusCode != http.StatusUnprocessableEntity {
		return false
	}
	var parsed struct {
		Errors []struct {
			Resource string `json:"resource"`
			Code     string `json:"code"`
			Message  string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(e.Body, &parsed); err != nil {
		return false
	}
	for _, item := range parsed.Errors {
		if strings.Contains(item.Message, "A pull request already exists") {
			return true
		}
	}
	return false
}

// GetInstallationToken returns a GitHub installation token for the given installation ID.
func (s *PRService) GetInstallationToken(ctx context.Context, installationID int64) (string, error) {
	return s.tokenProvider.GetInstallationToken(ctx, installationID)
}

// GitHubBranch represents a branch returned by the GitHub API.
type GitHubBranch struct {
	Name      string `json:"name"`
	Protected bool   `json:"protected"`
}

type GitHubTreeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
}

// ListBranches returns the branches for a repository from the GitHub API.
func (s *PRService) ListBranches(ctx context.Context, token, owner, repo string) ([]GitHubBranch, error) {
	var all []GitHubBranch
	page := 1
	for {
		path := fmt.Sprintf("/repos/%s/%s/branches?per_page=100&page=%d", owner, repo, page)
		body, err := s.doGitHubRequest(ctx, token, http.MethodGet, path, nil)
		if err != nil {
			return nil, fmt.Errorf("list branches: %w", err)
		}
		var branches []GitHubBranch
		if err := json.Unmarshal(body, &branches); err != nil {
			return nil, fmt.Errorf("decode branches: %w", err)
		}
		all = append(all, branches...)
		if len(branches) < 100 || page >= 10 {
			break
		}
		page++
	}
	return all, nil
}

func (s *PRService) ListRepositoryTree(ctx context.Context, token, owner, repo, branch string) ([]models.RepositoryTreeEntry, error) {
	commitSHA, err := s.getRef(ctx, token, owner, repo, "heads/"+branch)
	if err != nil {
		return nil, fmt.Errorf("get branch ref: %w", err)
	}

	path := fmt.Sprintf("/repos/%s/%s/git/commits/%s", owner, repo, commitSHA)
	body, err := s.doGitHubRequest(ctx, token, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("get commit: %w", err)
	}

	var commit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := json.Unmarshal(body, &commit); err != nil {
		return nil, fmt.Errorf("decode commit: %w", err)
	}
	if commit.Tree.SHA == "" {
		return nil, fmt.Errorf("commit tree sha missing")
	}

	path = fmt.Sprintf("/repos/%s/%s/git/trees/%s?recursive=1", owner, repo, commit.Tree.SHA)
	body, err = s.doGitHubRequest(ctx, token, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("get tree: %w", err)
	}

	var tree struct {
		Tree []GitHubTreeEntry `json:"tree"`
	}
	if err := json.Unmarshal(body, &tree); err != nil {
		return nil, fmt.Errorf("decode tree: %w", err)
	}

	results := make([]models.RepositoryTreeEntry, 0, len(tree.Tree))
	for _, entry := range tree.Tree {
		results = append(results, models.RepositoryTreeEntry{
			Path: entry.Path,
			Type: models.RepositoryTreeEntryType(entry.Type),
		})
	}
	return results, nil
}

func (s *PRService) getRef(ctx context.Context, token, owner, repo, ref string) (string, error) {
	path := fmt.Sprintf("/repos/%s/%s/git/ref/%s", owner, repo, strings.TrimPrefix(ref, "refs/"))
	body, err := s.doGitHubRequest(ctx, token, http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}
	var result struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	return result.Object.SHA, nil
}

// prCreateConfig holds optional configuration for createPullRequest.
type prCreateConfig struct {
	draft bool
}

type prCreateOption func(*prCreateConfig)

func withDraft(draft bool) prCreateOption {
	return func(c *prCreateConfig) {
		c.draft = draft
	}
}

func (s *PRService) updatePullRequestTitle(ctx context.Context, token, owner, repo string, number int, title string) error {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, number)
	_, err := s.doGitHubRequest(ctx, token, http.MethodPatch, path, map[string]any{
		"title": title,
	})
	return err
}

func (s *PRService) createPullRequest(ctx context.Context, token, owner, repo, title, body, head, base string, opts ...prCreateOption) (int, string, error) {
	cfg := prCreateConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls", owner, repo)
	payload := map[string]any{
		"title": title,
		"body":  body,
		"head":  head,
		"base":  base,
	}
	if cfg.draft {
		payload["draft"] = true
	}
	respBody, err := s.doGitHubRequest(ctx, token, http.MethodPost, path, payload)
	if err != nil {
		return 0, "", err
	}
	var result struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return 0, "", err
	}
	return result.Number, result.HTMLURL, nil
}

func (s *PRService) createOrGetPullRequest(ctx context.Context, token, owner, repo, title, body, head, base string, opts ...prCreateOption) (int, string, error) {
	prNumber, prURL, err := s.createPullRequest(ctx, token, owner, repo, title, body, head, base, opts...)
	if err == nil {
		return prNumber, prURL, nil
	}

	var apiErr *GitHubAPIError
	if errors.As(err, &apiErr) && apiErr.IsExistingPullRequest() {
		return s.findOpenPullRequestByHead(ctx, token, owner, repo, head)
	}
	return 0, "", err
}

func (s *PRService) findOpenPullRequestByHead(ctx context.Context, token, owner, repo, head string) (int, string, error) {
	query := url.Values{}
	query.Set("head", owner+":"+head)
	query.Set("state", "open")

	path := fmt.Sprintf("/repos/%s/%s/pulls?%s", owner, repo, query.Encode())
	respBody, err := s.doGitHubRequest(ctx, token, http.MethodGet, path, nil)
	if err != nil {
		return 0, "", fmt.Errorf("find existing pull request by head: %w", err)
	}

	var pulls []struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(respBody, &pulls); err != nil {
		return 0, "", fmt.Errorf("decode existing pull request lookup: %w", err)
	}
	if len(pulls) == 0 {
		return 0, "", fmt.Errorf("find existing pull request by head: no open pull request found for %s", head)
	}
	return pulls[0].Number, pulls[0].HTMLURL, nil
}

func (s *PRService) addLabels(ctx context.Context, token, owner, repo string, prNumber int, labels []string) error {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/labels", owner, repo, prNumber)
	_, err := s.doGitHubRequest(ctx, token, http.MethodPost, path, map[string][]string{
		"labels": labels,
	})
	return err
}

// --- Formatting helpers ---

// hasRepoScope returns true if the comma/space-separated scope string includes "repo".
func hasRepoScope(scope string) bool {
	for _, s := range strings.FieldsFunc(scope, func(r rune) bool { return r == ',' || r == ' ' }) {
		if s == "repo" {
			return true
		}
	}
	return false
}

func splitRepo(fullName string) (owner, repo string) {
	parts := strings.SplitN(fullName, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return fullName, fullName
}

var nonAlphanumRegexp = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(s)
	s = nonAlphanumRegexp.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > maxBranchSlugLen {
		// Truncate at last hyphen before limit to avoid mid-word cut.
		s = s[:maxBranchSlugLen]
		if i := strings.LastIndex(s, "-"); i > 0 {
			s = s[:i]
		}
	}
	return s
}

// firstLine returns the first non-empty line of s.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func normalizePRTitleCandidate(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'`")
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}

	s = strings.TrimRight(s, ".!?")

	return truncatePRTitle(s, maxPRTitleLen)
}

func truncatePRTitle(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}

	truncated := strings.TrimSpace(s[:limit])
	if idx := strings.LastIndex(truncated, " "); idx >= limit/2 {
		truncated = truncated[:idx]
	}
	return strings.TrimRight(truncated, " ,:;-")
}

func bestPRTitleSubject(session *models.Session, fallback string) string {
	if session.Title != nil && *session.Title != "" {
		if title := normalizePRTitleCandidate(*session.Title); title != "" {
			return title
		}
	}
	if session.ResultSummary != nil && *session.ResultSummary != "" {
		if title := normalizePRTitleCandidate(firstLine(*session.ResultSummary)); title != "" {
			return title
		}
	}
	if fallback != "" {
		if title := normalizePRTitleCandidate(fallback); title != "" {
			return title
		}
	}
	return ""
}

func formatBranchName(session *models.Session, issue *models.Issue) string {
	if session.WorkingBranch != nil && *session.WorkingBranch != "" {
		return *session.WorkingBranch
	}
	short := session.ID.String()[:8]
	var title string
	if issue != nil {
		title = issue.Title
	} else if session.Title != nil {
		title = *session.Title
	}
	slug := slugify(title)
	if slug == "" {
		slug = "changes"
	}
	// Linear's GitHub integration matches branches with a key prefix
	// independently of the PR title — see design 62 §"Branch naming hint".
	// When the session has a primary Linear identifier hint, embed it in
	// the slug so the integration can claim the branch even if the PR title
	// path failed.
	if session != nil && session.LinearIdentifierHint != nil && *session.LinearIdentifierHint != "" {
		hint := strings.ToLower(*session.LinearIdentifierHint)
		// Avoid double-embedding when the slug already contains the key.
		if !strings.Contains(slug, hint) {
			slug = hint + "-" + slug
		}
	}
	return fmt.Sprintf("143/%s/%s", short, slug)
}

func formatPRTitle(session *models.Session, issue *models.Issue) string {
	if issue != nil {
		switch issue.Source {
		case models.IssueSourceLinear:
			title := normalizePRTitleCandidate(issue.Title)
			if title == "" {
				title = issue.Title
			}
			// Strip "ACS-1234: " preamble baked into the title — we add
			// [ACS-1234] prefixes ourselves so the two formats don't
			// double up.
			title = stripLinearColonPrefix(title)
			return applyLinearKeyPrefixes(session, title, issue)
		default:
			title := bestPRTitleSubject(session, issue.Title)
			if strings.HasPrefix(strings.ToLower(title), "fix: ") {
				return applyLinearKeyPrefixes(session, truncatePRTitle(title, maxPRTitleLen), nil)
			}
			if title != "" {
				return applyLinearKeyPrefixes(session, "fix: "+truncatePRTitle(title, maxPRTitleLen-len("fix: ")), nil)
			}
			return applyLinearKeyPrefixes(session, fmt.Sprintf("fix: Session %s", session.ID.String()[:8]), nil)
		}
	}

	if title := bestPRTitleSubject(session, ""); title != "" {
		return applyLinearKeyPrefixes(session, title, nil)
	}
	return applyLinearKeyPrefixes(session, fmt.Sprintf("Session %s", session.ID.String()[:8]), nil)
}

// stripLinearColonPrefix removes a leading "ACS-1234: " preamble from a
// title so applyLinearKeyPrefixes can take over without duplicating the key.
var linearColonPrefixRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,9}-\d+\s*:\s*`)

// linearKeyShapeRE matches a Linear human key like "ACS-1234". Used to gate
// PR title prefixing — if a link's external_id is the Linear UUID (because
// provider_state.identifier hasn't been written yet) we must not bake it
// into the title; linearBracketPrefixRE only strips properly-shaped prefixes
// on resync, so a UUID prefix would stick forever.
var linearKeyShapeRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,9}-[0-9]+$`)

func stripLinearColonPrefix(s string) string {
	return linearColonPrefixRE.ReplaceAllString(s, "")
}

// linearBracketPrefixRE matches a single leading "[KEY-N] " prefix; we use
// it to strip stale prefixes from a title before re-prefixing on title
// resync, so resync never double-prefixes (design 62 §"Title resync").
var linearBracketPrefixRE = regexp.MustCompile(`^\[[A-Z][A-Z0-9_]{0,9}-[0-9]+\]\s+`)

// stripLeadingBracketPrefixes removes every leading "[KEY-N] " from title.
// Order matters: the loop trims one prefix at a time so a title like
// "[ACS-1] [ACS-2] feat: x" returns "feat: x", not just "[ACS-2] feat: x".
//
// maxStripIterations is a defensive cap. Each iteration strictly shrinks
// the string when ReplaceAllString matches, so the loop terminates on its
// own — but a regex change (or a future caller) shouldn't be able to spin
// us forever. Twenty iterations is well above any reasonable real PR title.
func stripLeadingBracketPrefixes(s string) string {
	const maxStripIterations = 20
	for i := 0; i < maxStripIterations; i++ {
		stripped := linearBracketPrefixRE.ReplaceAllString(s, "")
		if stripped == s {
			return s
		}
		s = stripped
	}
	return s
}

// conventionalCommitPrefixRE matches the leading conventional commit prefix
// like `feat:`, `fix(scope):`, etc., capturing it as group 1 and the rest
// of the title as group 2. Linear bracket prefixes go *after* this so the
// PR title reads `feat: [ACS-1234] Add OAuth callback handler`.
var conventionalCommitPrefixRE = regexp.MustCompile(`^([a-z]+(?:\([^)]*\))?:\s+)(.*)`)

// applyLinearKeyPrefixes inserts one `[KEY-N] ` prefix per linked Linear
// issue, ordered primary first then related by session-link order. Honors:
//
//   - Conventional commit prefix preserved (prefixes go after `feat:`).
//   - Identifiers already present in the title are not double-prefixed.
//   - Leading stale `[KEY-N] ` prefixes are stripped before re-prefixing.
//   - Body truncation, never prefix truncation: if total length would
//     exceed maxPRTitleLen we only clamp the trailing subject.
//
// primaryIssue may be nil — in that case the function reads identifiers
// solely from session.LinkedIssues. When primaryIssue is non-nil and Linear-
// sourced, its identifier is used as the primary key in case LinkedIssues
// hasn't been hydrated yet.
func applyLinearKeyPrefixes(session *models.Session, title string, primaryIssue *models.Issue) string {
	identifiers := collectLinearIdentifiers(session, primaryIssue)
	if len(identifiers) == 0 {
		return truncatePRTitle(title, maxPRTitleLen)
	}

	// Strip stale `[KEY-N] ` prefixes left over from a prior resync so we
	// never double-prefix.
	title = stripLeadingBracketPrefixes(title)

	// Drop identifiers that already appear inside the title — the user
	// (or a manual edit) embedded them, no need for a duplicate. Compare
	// case-insensitively: the canonical key is uppercase but a user who
	// typed `acs-1234` in their commit subject still embedded the same
	// reference, and double-prefixing would land us with both casings.
	upperTitle := strings.ToUpper(title)
	keep := identifiers[:0]
	for _, id := range identifiers {
		if !strings.Contains(upperTitle, strings.ToUpper(id)) {
			keep = append(keep, id)
		}
	}
	identifiers = keep
	if len(identifiers) == 0 {
		return truncatePRTitle(title, maxPRTitleLen)
	}

	// Cap prefix consumption so a session linked to many Linear issues
	// doesn't push the descriptive subject out of the title. The primary
	// (identifiers[0]) is always kept — Linear's GitHub integration only
	// needs that one to claim the PR — and additional related identifiers
	// are appended only while the running prefix fits the budget. Anything
	// that doesn't fit is silently dropped from the title; users still see
	// the full link set in the session detail header chips.
	const prefixBudget = maxPRTitleLen / 2
	bracketPrefix := strings.Builder{}
	for i, id := range identifiers {
		next := "[" + id + "] "
		if i > 0 && bracketPrefix.Len()+len(next) > prefixBudget {
			break
		}
		bracketPrefix.WriteString(next)
	}

	// Conventional commit prefix preserved: place Linear prefixes after it.
	if m := conventionalCommitPrefixRE.FindStringSubmatch(title); len(m) == 3 {
		conv := m[1]
		rest := m[2]
		joined := conv + bracketPrefix.String() + rest
		if len(joined) <= maxPRTitleLen {
			return joined
		}
		// Trim the rest, never the prefixes.
		fixed := conv + bracketPrefix.String()
		if len(fixed) >= maxPRTitleLen {
			return truncatePRTitle(strings.TrimSpace(fixed), maxPRTitleLen)
		}
		return fixed + truncatePRTitle(rest, maxPRTitleLen-len(fixed))
	}

	joined := bracketPrefix.String() + title
	if len(joined) <= maxPRTitleLen {
		return joined
	}
	fixed := bracketPrefix.String()
	if len(fixed) >= maxPRTitleLen {
		return truncatePRTitle(strings.TrimSpace(fixed), maxPRTitleLen)
	}
	return fixed + truncatePRTitle(title, maxPRTitleLen-len(fixed))
}

// collectLinearIdentifiers returns the deterministically-ordered Linear
// keys for a session: primary first, then related in session-link order.
// primaryIssue is a fallback when LinkedIssues isn't populated.
func collectLinearIdentifiers(session *models.Session, primaryIssue *models.Issue) []string {
	if session == nil {
		if primaryIssue != nil && primaryIssue.Source == models.IssueSourceLinear && linearKeyShapeRE.MatchString(primaryIssue.ExternalID) {
			return []string{primaryIssue.ExternalID}
		}
		return nil
	}
	if len(session.LinkedIssues) > 0 {
		seen := map[string]bool{}
		out := make([]string, 0, len(session.LinkedIssues))
		// LinkedIssues comes back ordered (primary first, then position).
		for _, link := range session.LinkedIssues {
			if link.IssueSource == nil || *link.IssueSource != models.IssueSourceLinear {
				continue
			}
			if link.ExternalID == nil || *link.ExternalID == "" {
				continue
			}
			id := *link.ExternalID
			// Drop links whose external_id isn't the human Linear key. The
			// COALESCE in sessionIssueLinkSelectColumns falls through to the
			// Linear UUID when provider_state.identifier hasn't been written,
			// and a UUID baked into the PR title sticks across resyncs
			// (linearBracketPrefixRE only strips KEY-N shaped prefixes).
			if !linearKeyShapeRE.MatchString(id) {
				continue
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
		if len(out) > 0 {
			return out
		}
	}
	if primaryIssue != nil && primaryIssue.Source == models.IssueSourceLinear && linearKeyShapeRE.MatchString(primaryIssue.ExternalID) {
		return []string{primaryIssue.ExternalID}
	}
	return nil
}

func formatSyncedPRTitle(session *models.Session, issue *models.Issue) string {
	if issue != nil && issue.Source == models.IssueSourceLinear {
		title := bestPRTitleSubject(session, issue.Title)
		if title == "" {
			title = normalizePRTitleCandidate(issue.Title)
			if title == "" {
				title = issue.Title
			}
		}
		title = stripLinearColonPrefix(title)
		return applyLinearKeyPrefixes(session, title, issue)
	}

	return formatPRTitle(session, issue)
}

func formatCommitMessage(session *models.Session, issue *models.Issue) string {
	if issue != nil {
		msg := fmt.Sprintf("fix: %s", issue.Title)
		switch issue.Source {
		case models.IssueSourceLinear:
			msg += fmt.Sprintf("\n\nFixes #%s", issue.ExternalID)
		case models.IssueSourceSentry:
			msg += fmt.Sprintf("\n\nResolves %s", issue.ExternalID)
		}
		return msg
	}

	// Issueless sessions: derive from session title or summary.
	if session.Title != nil && *session.Title != "" {
		return *session.Title
	}
	if session.ResultSummary != nil && *session.ResultSummary != "" {
		return firstLine(*session.ResultSummary)
	}
	return fmt.Sprintf("Session %s", session.ID.String()[:8])
}

// prTemplatePaths lists the conventional locations for GitHub PR templates.
var prTemplatePaths = []string{
	".github/pull_request_template.md",
	".github/PULL_REQUEST_TEMPLATE.md",
	"docs/pull_request_template.md",
	"pull_request_template.md",
	"PULL_REQUEST_TEMPLATE.md",
	".github/PULL_REQUEST_TEMPLATE/default.md",
}

// fetchPRTemplate retrieves the repo's PR template via the GitHub Contents API.
// Returns (content, path) — both empty if no template is found.
// Checks well-known single-file paths first, then falls back to listing
// .github/PULL_REQUEST_TEMPLATE/ for repos that use multiple templates.
func (s *PRService) fetchPRTemplate(ctx context.Context, token, owner, repo, ref string) (string, string) {
	// 1. Try well-known single-file paths.
	for _, path := range prTemplatePaths {
		if content, ok := s.fetchFileContent(ctx, token, owner, repo, ref, path); ok {
			return content, path
		}
	}

	// 2. Try listing the multi-template directory.
	templateDir := ".github/PULL_REQUEST_TEMPLATE"
	if content, path := s.fetchTemplateFromDirectory(ctx, token, owner, repo, ref, templateDir); content != "" {
		return content, path
	}

	return "", ""
}

// GetFileContent retrieves a single file's decoded content from GitHub. It is
// the exported counterpart of fetchFileContent and surfaces an explicit error
// (rather than the (string, bool) shape) so callers outside this package can
// distinguish "missing file" from "GitHub returned an error".
func (s *PRService) GetFileContent(ctx context.Context, token, owner, repo, ref, path string) (string, error) {
	url := fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s", owner, repo, path, ref)
	body, err := s.doGitHubRequest(ctx, token, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("get file contents: %w", err)
	}
	var content struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.Unmarshal(body, &content); err != nil {
		return "", fmt.Errorf("decode file contents: %w", err)
	}
	if content.Encoding != "base64" || content.Content == "" {
		return "", nil
	}
	decoded, err := decodeBase64Content(content.Content)
	if err != nil {
		return "", fmt.Errorf("decode base64 content: %w", err)
	}
	return decoded, nil
}

// fetchFileContent retrieves a single file's decoded content from GitHub.
func (s *PRService) fetchFileContent(ctx context.Context, token, owner, repo, ref, path string) (string, bool) {
	url := fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s", owner, repo, path, ref)
	body, err := s.doGitHubRequest(ctx, token, http.MethodGet, url, nil)
	if err != nil {
		return "", false
	}
	var content struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.Unmarshal(body, &content); err != nil {
		return "", false
	}
	if content.Encoding == "base64" && content.Content != "" {
		decoded, err := decodeBase64Content(content.Content)
		if err == nil && decoded != "" {
			return decoded, true
		}
	}
	return "", false
}

// fetchTemplateFromDirectory lists a GitHub directory and returns the best
// template file found. Prefers "default.md"; otherwise uses the first .md file.
// Returns (content, path) matching fetchPRTemplate's convention.
func (s *PRService) fetchTemplateFromDirectory(ctx context.Context, token, owner, repo, ref, dir string) (string, string) {
	url := fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s", owner, repo, dir, ref)
	body, err := s.doGitHubRequest(ctx, token, http.MethodGet, url, nil)
	if err != nil {
		return "", ""
	}
	var entries []struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &entries); err != nil {
		return "", ""
	}

	// Collect .md files, preferring default.md.
	var defaultPath, firstMDPath string
	for _, entry := range entries {
		if entry.Type != "file" {
			continue
		}
		lower := strings.ToLower(entry.Name)
		if !strings.HasSuffix(lower, ".md") {
			continue
		}
		if lower == "default.md" {
			defaultPath = entry.Path
			break
		}
		if firstMDPath == "" {
			firstMDPath = entry.Path
		}
	}

	target := defaultPath
	if target == "" {
		target = firstMDPath
	}
	if target == "" {
		return "", ""
	}

	if content, ok := s.fetchFileContent(ctx, token, owner, repo, ref, target); ok {
		return content, target
	}
	return "", ""
}

// decodeBase64Content decodes GitHub's base64-encoded file content.
func decodeBase64Content(encoded string) (string, error) {
	// GitHub base64 content may contain newlines.
	clean := strings.ReplaceAll(encoded, "\n", "")
	decoded, err := base64.StdEncoding.DecodeString(clean)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

// fillRepoTemplate uses the LLM to fill a repo's PR template with session context.
func (s *PRService) fillRepoTemplate(ctx context.Context, template string, run *models.Session, issue *models.Issue) (string, error) {
	if s.llmClient == nil {
		return "", fmt.Errorf("no LLM client configured")
	}

	systemPrompt := `You are a PR description writer. Fill in the sections of the given PR template using the provided context. Be concise. If a section is not applicable, write "N/A" or remove it. Do not add sections that aren't in the template. Return only the filled-in template, nothing else.`

	var contextBuilder strings.Builder
	contextBuilder.WriteString("## PR Template to fill:\n\n")
	contextBuilder.WriteString(template)
	contextBuilder.WriteString("\n\n## Context:\n\n")

	if run.ResultSummary != nil && *run.ResultSummary != "" {
		fmt.Fprintf(&contextBuilder, "### What the agent did:\n%s\n\n", *run.ResultSummary)
	}
	if run.Diff != nil && *run.Diff != "" {
		// Include a truncated diff stat for context.
		diffLines := strings.Split(*run.Diff, "\n")
		if len(diffLines) > 100 {
			diffLines = diffLines[:100]
		}
		fmt.Fprintf(&contextBuilder, "### Diff (truncated):\n```\n%s\n```\n\n", strings.Join(diffLines, "\n"))
	}
	if issue != nil {
		fmt.Fprintf(&contextBuilder, "### Issue:\n- Title: %s\n- Source: %s\n- Severity: %s\n\n", issue.Title, issue.Source, issue.Severity)
	}

	filled, err := s.llmClient.Complete(ctx, systemPrompt, contextBuilder.String())
	if err != nil {
		return "", fmt.Errorf("LLM template fill: %w", err)
	}
	return filled, nil
}

// generatedPR holds LLM-generated PR title and body.
type generatedPR struct {
	Title string
	Body  string
}

// generatePRContent uses the LLM to produce both a PR title and body in a
// single call. When the repo has a PR template, it is included in the prompt
// so the LLM fills it in. Falls back with an error if the LLM is unavailable
// or the response cannot be parsed — callers should use formatPRTitle /
// formatPRBody as static fallbacks.
func (s *PRService) generatePRContent(ctx context.Context, token, owner, repoName, defaultBranch string, repoID, orgID uuid.UUID, run *models.Session, issue *models.Issue) (*generatedPR, error) {
	if s.llmClient == nil {
		return nil, fmt.Errorf("no LLM client configured")
	}

	// 1. Fetch repo PR template (DB-cached, 24h TTL).
	repoTemplate := s.getOrFetchPRTemplate(ctx, token, owner, repoName, defaultBranch, repoID, orgID)

	// 2. Render system prompt via template.
	systemPrompt := prompts.PRContentPrompt(prompts.PRContentPromptData{
		HasTemplate: repoTemplate != "",
	})

	// 3. Build user prompt data.
	userData := prompts.PRContentUserPromptData{
		RepoTemplate: repoTemplate,
	}

	if run.ResultSummary != nil && *run.ResultSummary != "" {
		userData.ResultSummary = *run.ResultSummary
	}
	if run.Title != nil && *run.Title != "" {
		userData.SessionTitle = *run.Title
	}
	if issue != nil {
		userData.IssueTitle = issue.Title
		userData.IssueSource = string(issue.Source)
		userData.IssueSeverity = issue.Severity
	}

	// Collect validation results.
	if s.validations != nil {
		if validation, err := s.validations.GetBySessionID(ctx, run.OrgID, run.ID); err == nil {
			var checks []string
			if validation.RegressionTestCheck == "pass" || validation.RegressionTestCheck == "passed" {
				checks = append(checks, "Regression tests passed")
			}
			if validation.CorrectnessCheck == "pass" || validation.CorrectnessCheck == "passed" {
				checks = append(checks, "Correctness check passed")
			}
			if validation.SecurityScan == "pass" || validation.SecurityScan == "passed" {
				checks = append(checks, "Security scan passed")
			}
			if validation.CICheck == "pass" || validation.CICheck == "passed" {
				checks = append(checks, "CI/CD passed")
			}
			userData.ValidationChecks = checks
		}
	}

	// Include the diff — truncated to keep the prompt manageable.
	if run.Diff != nil && *run.Diff != "" {
		fileSummary, truncatedDiff := summarizeDiff(*run.Diff, 4000)
		userData.FileSummary = fileSummary
		userData.Diff = truncatedDiff
	}

	userPrompt := prompts.PRContentUserPrompt(userData)

	// 4. Call LLM.
	response, err := s.llmClient.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("LLM PR content generation: %w", err)
	}

	// 5. Parse response into title + body.
	result := parsePRContentResponse(response)
	result.Title = normalizePRTitleCandidate(result.Title)
	if result.Title == "" && result.Body == "" {
		return nil, fmt.Errorf("LLM returned empty PR content")
	}

	// Append footer to body.
	if result.Body != "" {
		result.Body += fmt.Sprintf("\n\n---\n*Generated by [143.dev](https://143.dev) — [session %s](https://app.143.dev/sessions/%s)*\n", run.ID.String()[:8], run.ID)
	}

	return result, nil
}

// prTitleRegexp and prBodyRegexp extract content from the XML tags the LLM returns.
var (
	prTitleRegexp = regexp.MustCompile(`(?s)<pr_title>\s*(.*?)\s*</pr_title>`)
	prBodyRegexp  = regexp.MustCompile(`(?s)<pr_body>\s*(.*?)\s*</pr_body>`)
)

// parsePRContentResponse extracts the title and body from the LLM XML-tagged response.
func parsePRContentResponse(response string) *generatedPR {
	result := &generatedPR{}
	response = strings.TrimSpace(response)
	if response == "" {
		return result
	}

	if m := prTitleRegexp.FindStringSubmatch(response); len(m) > 1 {
		result.Title = strings.TrimSpace(m[1])
	}
	if m := prBodyRegexp.FindStringSubmatch(response); len(m) > 1 {
		result.Body = strings.TrimSpace(m[1])
	}

	// Fallback: if no tags found, treat entire response as body.
	if result.Title == "" && result.Body == "" {
		result.Body = response
	}

	return result
}

// summarizeDiff produces a file-level summary (like diffstat) and a truncated
// version of the raw diff. maxChars controls the truncation of the raw diff.
func summarizeDiff(diff string, maxChars int) (summary string, truncated string) {
	lines := strings.Split(diff, "\n")

	// Build file-level summary.
	var files []string
	var additions, deletions int
	for _, line := range lines {
		if strings.HasPrefix(line, "+++ b/") {
			files = append(files, strings.TrimPrefix(line, "+++ b/"))
		} else if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			additions++
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			deletions++
		}
	}
	if len(files) > 0 {
		var sb strings.Builder
		for _, f := range files {
			fmt.Fprintf(&sb, "- %s\n", f)
		}
		fmt.Fprintf(&sb, "\n%d additions, %d deletions", additions, deletions)
		summary = sb.String()
	}

	// Truncate raw diff.
	if len(diff) <= maxChars {
		truncated = diff
	} else {
		truncated = diff[:maxChars] + "\n... (truncated)"
	}

	return summary, truncated
}

// getOrFetchPRTemplate returns the cached PR template if fresh, otherwise
// fetches from GitHub and caches the result.
func (s *PRService) getOrFetchPRTemplate(ctx context.Context, token, owner, repoName, defaultBranch string, repoID, orgID uuid.UUID) string {
	// Check DB cache.
	if s.prTemplates != nil {
		cached, err := s.prTemplates.GetByRepositoryID(ctx, orgID, repoID)
		if err == nil && time.Since(cached.FetchedAt) < prTemplateCacheTTL {
			return cached.TemplateContent
		}
	}

	// Cache miss or stale — fetch from GitHub.
	template, path := s.fetchPRTemplate(ctx, token, owner, repoName, defaultBranch)

	// Persist to cache (even if empty — avoids re-fetching repos with no template).
	if s.prTemplates != nil {
		if err := s.prTemplates.Upsert(ctx, repoID, orgID, template, path); err != nil {
			s.logger.Warn().Err(err).Msg("failed to cache PR template")
		}
	}

	return template
}

// formatPRBody builds the default PR body when no repo template is found.
// It produces a minimal, scannable body: Summary, optional Issue info, and Test plan.
func (s *PRService) formatPRBody(ctx context.Context, run *models.Session, issue *models.Issue) string {
	var b strings.Builder

	b.WriteString("## Summary\n\n")
	if run.ResultSummary != nil && *run.ResultSummary != "" {
		b.WriteString(*run.ResultSummary)
	} else {
		b.WriteString("Automated changes generated by 143.dev")
	}
	b.WriteString("\n\n")

	// Issue context — only when an issue is attached.
	if issue != nil {
		fmt.Fprintf(&b, "**Issue**: %s — %s", issue.Source, issue.Title)
		if issue.Severity != "" {
			fmt.Fprintf(&b, " (%s)", issue.Severity)
		}
		b.WriteString("\n\n")
	}

	// Test plan: summarize validation results if available, otherwise use a placeholder.
	b.WriteString("## Test plan\n\n")
	validationWritten := false
	if s.validations != nil {
		validation, err := s.validations.GetBySessionID(ctx, run.OrgID, run.ID)
		if err == nil {
			var checks []string
			if validation.RegressionTestCheck == "pass" || validation.RegressionTestCheck == "passed" {
				checks = append(checks, "Regression tests passed")
			}
			if validation.CorrectnessCheck == "pass" || validation.CorrectnessCheck == "passed" {
				checks = append(checks, "Correctness check passed")
			}
			if validation.SecurityScan == "pass" || validation.SecurityScan == "passed" {
				checks = append(checks, "Security scan passed")
			}
			if validation.CICheck == "pass" || validation.CICheck == "passed" {
				checks = append(checks, "CI/CD passed")
			}
			if len(checks) > 0 {
				for _, c := range checks {
					fmt.Fprintf(&b, "- %s\n", c)
				}
				validationWritten = true
			}
		}
	}
	if !validationWritten {
		b.WriteString("Validated by automated agent run.\n")
	}

	b.WriteString("\n---\n")
	fmt.Fprintf(&b, "*Generated by [143.dev](https://143.dev) — [session %s](https://app.143.dev/sessions/%s)*\n", run.ID.String()[:8], run.ID)

	return b.String()
}

func buildLabels(issue *models.Issue) []string {
	labels := []string{"143-generated"}
	if issue == nil {
		return labels
	}
	if issue.Severity != "" {
		labels = append(labels, "severity:"+issue.Severity)
	}
	if issue.Source != "" {
		labels = append(labels, "source:"+string(issue.Source))
	}
	return labels
}

// CheckSuiteEvent represents a GitHub check_suite webhook payload.
type CheckSuiteEvent struct {
	Action     string `json:"action"`
	CheckSuite struct {
		Conclusion   *string `json:"conclusion"`
		HeadBranch   string  `json:"head_branch"`
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	} `json:"check_suite"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// HandleCheckSuiteEvent processes check_suite webhook events to update PR CI status.
func (s *PRService) HandleCheckSuiteEvent(ctx context.Context, event CheckSuiteEvent) error {
	if event.Action != "completed" {
		return nil
	}

	for _, prRef := range event.CheckSuite.PullRequests {
		pr, err := s.pullRequests.GetByRepoAndNumber(ctx, event.Repository.FullName, prRef.Number)
		if err != nil {
			continue // Not a 143-managed PR.
		}

		ciStatus := "failure"
		if event.CheckSuite.Conclusion != nil {
			switch *event.CheckSuite.Conclusion {
			case "success", "neutral", "skipped":
				ciStatus = "success"
			}
		}

		if err := s.pullRequests.UpdateCIStatus(ctx, pr.OrgID, pr.ID, ciStatus); err != nil {
			s.logger.Warn().Err(err).Str("pr_id", pr.ID.String()).Msg("failed to update CI status")
		}
		s.enqueuePullRequestStateSync(ctx, pr)
	}

	return nil
}

// CheckRunEvent represents a GitHub check_run webhook payload.
type CheckRunEvent struct {
	Action   string `json:"action"`
	CheckRun struct {
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	} `json:"check_run"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// HandleCheckRunEvent processes check_run webhook events and refreshes repairable PR state.
func (s *PRService) HandleCheckRunEvent(ctx context.Context, event CheckRunEvent) error {
	if event.Action != "completed" {
		return nil
	}

	for _, prRef := range event.CheckRun.PullRequests {
		pr, err := s.pullRequests.GetByRepoAndNumber(ctx, event.Repository.FullName, prRef.Number)
		if err != nil {
			continue // Not a 143-managed PR.
		}
		s.enqueuePullRequestStateSync(ctx, pr)
	}

	return nil
}
