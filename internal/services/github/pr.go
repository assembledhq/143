package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/llm"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/storage"
)

const (
	defaultGitHubAPI   = "https://api.github.com"
	maxBranchSlugLen   = 60
	maxLabelsToCreate  = 5
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
	userCredentials *db.UserCredentialStore
	users           *db.UserStore
	orgs            *db.OrganizationStore
	prTemplates     *db.PRTemplateStore
	previews        *db.PreviewStore
	previewStopper  PreviewStopper
	llmClient       llm.Client
	audit           *db.AuditEmitter
	sandboxProvider agent.SandboxProvider // used by the push-based PR flow
	snapshots       storage.SnapshotStore // used by the push-based PR flow
	logger          zerolog.Logger
	baseURL         string
	httpClient      *http.Client
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

// SetUserCredentialStore sets the user credential store for user-authored PRs.
func (s *PRService) SetUserCredentialStore(store *db.UserCredentialStore) {
	s.userCredentials = store
}

// SetLLMClient sets the LLM client for repo PR template filling.
func (s *PRService) SetLLMClient(client llm.Client) {
	s.llmClient = client
}

// SetUserStore sets the user store for fetching user info during PR creation.
func (s *PRService) SetUserStore(store *db.UserStore) {
	s.users = store
}

// SetAuditEmitter sets the audit emitter used for webhook-triggered audit events.
func (s *PRService) SetAuditEmitter(audit *db.AuditEmitter) {
	s.audit = audit
}

// SetOrgStore sets the organization store for fetching org settings.
func (s *PRService) SetOrgStore(store *db.OrganizationStore) {
	s.orgs = store
}

// SetPRTemplateStore sets the PR template cache store.
func (s *PRService) SetPRTemplateStore(store *db.PRTemplateStore) {
	s.prTemplates = store
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

// SandboxProvider exposes the configured sandbox provider for wiring tests.
func (s *PRService) SandboxProvider() agent.SandboxProvider {
	return s.sandboxProvider
}

// SnapshotStore exposes the configured snapshot store for wiring tests.
func (s *PRService) SnapshotStore() storage.SnapshotStore {
	return s.snapshots
}

// ErrSnapshotExpired is returned by CreatePR when the session's sandbox
// snapshot is missing (reaped, merged/archived cleanup, or never captured).
// The UI translates this into "session state expired — re-run to create a PR".
var ErrSnapshotExpired = errors.New("session snapshot expired")

// ErrNoChanges is returned by CreatePR when the restored workspace has no
// uncommitted changes relative to the base branch — there's nothing to push.
// This typically means the diff was reverted or the session produced no edits.
var ErrNoChanges = errors.New("no changes to push")

// tokenResolution holds the resolved token and metadata about how it was resolved.
type tokenResolution struct {
	Token       string
	IsUserToken bool
	User        *models.User // set when IsUserToken is true
}

// resolveToken determines which GitHub token to use for PR creation.
// Order: user's personal GitHub token → GitHub App installation token.
func (s *PRService) resolveToken(ctx context.Context, run *models.Session, repo *models.Repository, orgSettings models.OrgSettings) (*tokenResolution, error) {
	// If org is set to app_only, skip user token lookup.
	if orgSettings.PRAuthorship == models.PRAuthorshipAppOnly {
		token, err := s.tokenProvider.GetInstallationToken(ctx, repo.InstallationID)
		if err != nil {
			return nil, fmt.Errorf("get installation token: %w", err)
		}
		return &tokenResolution{Token: token, IsUserToken: false}, nil
	}

	// Try user token if user triggered the session and we have credential stores.
	if run.TriggeredByUserID != nil && s.userCredentials != nil && s.users != nil {
		cred, err := s.userCredentials.GetForUser(ctx, run.OrgID, *run.TriggeredByUserID, models.ProviderGitHubOAuth)
		if err == nil && cred != nil && cred.Config != nil {
			cfg, ok := cred.Config.(models.GitHubOAuthConfig)
			// The token must have "repo" scope to push code and create PRs.
			// Login-only tokens (read:user,user:email) lack this — skip them.
			if ok && cfg.AccessToken != "" && hasRepoScope(cfg.Scope) {
				// Validate token is still active before using it.
				if s.validateUserToken(ctx, cfg.AccessToken) {
					user, userErr := s.users.GetByID(ctx, run.OrgID, *run.TriggeredByUserID)
					if userErr == nil {
						return &tokenResolution{
							Token:       cfg.AccessToken,
							IsUserToken: true,
							User:        &user,
						}, nil
					}
				} else {
					// Token was revoked — disable the stored credential and fall through.
					s.logger.Warn().Str("user_id", run.TriggeredByUserID.String()).Msg("user GitHub token revoked, disabling credential and falling back to app token")
					_ = s.userCredentials.Disable(ctx, run.OrgID, *run.TriggeredByUserID, models.ProviderGitHubOAuth)
				}
			}
		}
	}

	// If org requires user auth and we couldn't get a user token, block.
	if orgSettings.PRAuthorship == models.PRAuthorshipUserRequired {
		return nil, fmt.Errorf("org requires user GitHub auth for PR creation, but no valid user token found")
	}

	// Fall back to app installation token.
	token, err := s.tokenProvider.GetInstallationToken(ctx, repo.InstallationID)
	if err != nil {
		return nil, fmt.Errorf("get installation token: %w", err)
	}
	return &tokenResolution{Token: token, IsUserToken: false}, nil
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
}

// CreatePRParams holds optional parameters for PR creation that come from the
// API request (as opposed to org-level defaults). Fields use pointers to
// distinguish "caller explicitly set this" from "use org default".
type CreatePRParams struct {
	Draft *bool `json:"draft,omitempty"`
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
// Returns ErrSnapshotExpired when the session's snapshot is missing (reaped,
// cleanup-on-merge, or never captured) — the UI prompts the user to re-run.
// Returns ErrNoChanges when the pushed branch produces no diff against the
// base branch; the session's snapshot has no meaningful changes to ship.
func (s *PRService) CreatePR(ctx context.Context, run *models.Session, params ...CreatePRParams) (*models.PullRequest, error) {
	// Idempotency: a worker retry after a partial success (push landed, PR
	// created, but state update crashed) would otherwise create a duplicate
	// PR. If a PR already exists for this session, return it without
	// re-pushing.
	if existing, err := s.pullRequests.GetBySessionID(ctx, run.OrgID, run.ID); err == nil {
		return &existing, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("check existing pull request: %w", err)
	}

	if s.sandboxProvider == nil || s.snapshots == nil {
		return nil, fmt.Errorf("PRService: sandbox push dependencies not configured")
	}

	if run.SnapshotKey == nil || *run.SnapshotKey == "" {
		return nil, ErrSnapshotExpired
	}

	// Issue lookup is optional — sessions may not have an associated issue.
	var issue *models.Issue
	if run.IssueID != uuid.Nil {
		i, err := s.issues.GetByID(ctx, run.OrgID, run.IssueID)
		if err == nil {
			issue = &i
		} else {
			s.logger.Warn().Err(err).Str("issue_id", run.IssueID.String()).Msg("failed to look up issue, proceeding without it")
		}
	}

	// Resolve repository: session.RepositoryID first, then issue.RepositoryID.
	var repoID *uuid.UUID
	if run.RepositoryID != nil {
		repoID = run.RepositoryID
	} else if issue != nil && issue.RepositoryID != nil {
		repoID = issue.RepositoryID
	}
	if repoID == nil {
		return nil, fmt.Errorf("session %s has no repository", run.ID)
	}
	repo, err := s.repos.GetByID(ctx, run.OrgID, *repoID)
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

	resolution, err := s.resolveToken(ctx, run, &repo, orgSettings)
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
	authorName, authorEmail := commitIdentity(resolution)
	if !resolution.IsUserToken && run.TriggeredByUserID != nil && s.users != nil {
		// App token: attribute the change via a Co-authored-by trailer so the
		// GitHub UI surfaces the human who kicked off the run.
		if user, userErr := s.users.GetByID(ctx, run.OrgID, *run.TriggeredByUserID); userErr == nil {
			commitMsg += fmt.Sprintf("\n\nCo-authored-by: %s <%s>", user.Name, user.Email)
		}
	}

	if err := s.pushSessionBranch(ctx, run, &repo, token, *run.SnapshotKey, branchName, commitMsg, authorName, authorEmail); err != nil {
		return nil, err
	}

	var title, body string
	if generated, genErr := s.generatePRContent(ctx, token, owner, repoName, defaultBranch, *repoID, run.OrgID, run, issue); genErr == nil {
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
	if len(params) > 0 && params[0].Draft != nil {
		draft = *params[0].Draft
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
		var apiErr *githubAPIError
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

	authoredBy := "app"
	if resolution.IsUserToken {
		authoredBy = "user"
	}
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
	}
	if err := s.pullRequests.Create(ctx, pr); err != nil {
		return nil, fmt.Errorf("store pull request: %w", err)
	}

	if err := s.sessions.UpdateStatus(ctx, run.OrgID, run.ID, "pr_created"); err != nil {
		s.logger.Warn().Err(err).Str("run_id", run.ID.String()).Msg("failed to update agent run status")
	}

	if issue != nil {
		if err := s.issues.UpdateStatus(ctx, run.OrgID, run.IssueID, "in_progress"); err != nil {
			s.logger.Warn().Err(err).Str("issue_id", run.IssueID.String()).Msg("failed to update issue status")
		}
	}

	return pr, nil
}

// pushSessionBranch hydrates the session's sandbox snapshot into a fresh
// container, stages + commits any uncommitted changes, and pushes HEAD to a
// new remote branch. The sandbox is always destroyed on return.
//
// The GitHub token is never passed via argv or URL. It's written to a file
// inside the sandbox and read by a GIT_ASKPASS helper, so `ps` inside the
// container shows only a plain https URL with no credentials.
func (s *PRService) pushSessionBranch(
	ctx context.Context,
	run *models.Session,
	repo *models.Repository,
	token, snapshotKey, branchName, commitMsg, authorName, authorEmail string,
) error {
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
			return ErrSnapshotExpired
		}
		return fmt.Errorf("hydrate sandbox: %w", err)
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
		return fmt.Errorf("write commit message to sandbox: %w", err)
	}
	if err := s.sandboxProvider.WriteFile(ctx, sandbox, pushInputPath, []byte(token)); err != nil {
		return fmt.Errorf("write credential to sandbox: %w", err)
	}
	helperScript := "#!/bin/sh\nexec cat " + shellQuote(pushInputPath) + "\n"
	if err := s.sandboxProvider.WriteFile(ctx, sandbox, pushHelperPath, []byte(helperScript)); err != nil {
		return fmt.Errorf("write push helper to sandbox: %w", err)
	}

	pushURL := fmt.Sprintf("https://x-access-token@github.com/%s.git", repo.FullName)
	script := buildPushScript(sandbox.WorkDir, authorName, authorEmail, branchName, pushURL)

	var stdout, stderr bytes.Buffer
	exitCode, execErr := s.sandboxProvider.Exec(ctx, sandbox, script, &stdout, &stderr)
	if execErr != nil {
		return fmt.Errorf("exec push script: %w", execErr)
	}
	switch exitCode {
	case 0:
		return nil
	case pushExitNoChanges:
		return ErrNoChanges
	default:
		msg := strings.TrimSpace(stderr.String())
		// Defense in depth: if the token ever leaks into stderr (e.g. via a
		// future code path that reintroduces it), scrub before returning.
		msg = strings.ReplaceAll(msg, token, "***")
		if msg == "" {
			msg = "(no stderr)"
		}
		return fmt.Errorf("git push failed (exit %d): %s", exitCode, msg)
	}
}

// Sandbox-internal paths used by the push flow. Under /tmp so they're
// auto-cleaned on sandbox destroy; the trap inside the script also removes
// them explicitly on exit for defense in depth.
const (
	pushCommitMsgPath = "/tmp/143-pr-commit-msg"
	pushInputPath     = "/tmp/143-pr-input"
	pushHelperPath    = "/tmp/143-pr-helper.sh"
)

// pushExitNoChanges is the sentinel exit code the push script uses when the
// restored working tree has no uncommitted changes AND no commits ahead of
// the remote tracking branch — i.e. there is nothing meaningful to push.
const pushExitNoChanges = 77

// pushScriptTemplate is the shell script executed inside the restored
// sandbox. All variable interpolations are pre-quoted by the caller (see
// buildPushScript) so they're safe to embed directly. The credential is read
// by the `GIT_ASKPASS` helper from pushInputPath — it never appears in argv.
//
// The cleanup function is hoisted into a shell function rather than inlined
// in the trap because `trap 'rm -f %[1]s ...'` would interleave single-
// quoted strings in a way that works but is fragile to reason about.
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
	)
}

// shellQuote single-quotes s for safe interpolation into a POSIX shell.
// Embedded single quotes are escaped by closing the quoted string, emitting
// an escaped quote, and reopening: foo'bar -> 'foo'\”bar'.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// commitIdentity returns the name and email to set as the local git author
// for the push. Prefers the authenticated user's identity when a user token
// was resolved; otherwise falls back to the 143 bot identity.
func commitIdentity(resolution *tokenResolution) (name, email string) {
	if resolution != nil && resolution.IsUserToken && resolution.User != nil {
		return resolution.User.Name, resolution.User.Email
	}
	return "143 Agent", "noreply@143.dev"
}

// PullRequestEvent represents a GitHub pull_request webhook event.
type PullRequestEvent struct {
	Action string `json:"action"`
	Number int    `json:"number"`
	PR     struct {
		Merged   bool   `json:"merged"`
		HTMLURL  string `json:"html_url"`
		MergedAt string `json:"merged_at"`
		Head     struct {
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

	if event.Action != "closed" {
		return nil
	}

	if event.PR.Merged {
		// PR was merged.
		if err := s.pullRequests.UpdateStatus(ctx, pr.OrgID, pr.ID, "merged"); err != nil {
			return fmt.Errorf("update PR status to merged: %w", err)
		}

		// Get the agent run to find the issue (only if PR is linked to a session).
		if pr.SessionID != nil {
			run, err := s.sessions.GetByID(ctx, pr.OrgID, *pr.SessionID)
			if err != nil {
				return fmt.Errorf("get agent run: %w", err)
			}

			// Update issue status to fixed.
			if err := s.issues.UpdateStatus(ctx, pr.OrgID, run.IssueID, "fixed"); err != nil {
				s.logger.Warn().Err(err).Str("issue_id", run.IssueID.String()).Msg("failed to update issue status to fixed")
			}

			// Snapshot is no longer needed now that the PR is merged. Cleanup
			// is best-effort: reaper + archive sweep pick up any stragglers.
			if s.snapshots != nil {
				if err := storage.CleanupSessionSnapshot(ctx, s.snapshots, s.sessions, pr.OrgID, *pr.SessionID, run.SnapshotKey); err != nil {
					s.logger.Warn().Err(err).Str("session_id", pr.SessionID.String()).Msg("failed to clean up snapshot on merge")
				}
			}
		}

		// Create deploy record.
		commitSHA := event.PR.Head.SHA
		deploy := &models.Deploy{
			PullRequestID: pr.ID,
			OrgID:         pr.OrgID,
			Environment:   "production",
			CommitSHA:     &commitSHA,
		}
		if err := s.deploys.Create(ctx, deploy); err != nil {
			s.logger.Warn().Err(err).Str("pr_id", pr.ID.String()).Msg("failed to create deploy record")
		}

		// Enqueue experiment evaluation job.
		dedupeKey := fmt.Sprintf("evaluate_experiment:%s", pr.ID)
		if _, err := s.jobs.Enqueue(ctx, pr.OrgID, "default", "evaluate_experiment", map[string]string{
			"pull_request_id": pr.ID.String(),
			"commit_sha":      commitSHA,
		}, 5, &dedupeKey); err != nil {
			s.logger.Warn().Err(err).Str("pr_id", pr.ID.String()).Msg("failed to enqueue evaluate_experiment job")
		}
	} else {
		// PR was closed without merging.
		if err := s.pullRequests.UpdateStatus(ctx, pr.OrgID, pr.ID, "closed"); err != nil {
			return fmt.Errorf("update PR status to closed: %w", err)
		}
	}

	// Stop any active preview for this PR. Runs in both merged and closed
	// branches: once a PR is no longer open, the preview is obsolete.
	s.teardownPRPreview(ctx, pr, event.PR.Merged)

	// Auto-archive the linked session if the org has opted in.
	if pr.SessionID != nil && s.orgs != nil {
		if org, err := s.orgs.GetByID(ctx, pr.OrgID); err != nil {
			s.logger.Warn().Err(err).Str("org_id", pr.OrgID.String()).Msg("failed to load org for auto-archive check")
		} else if settings, err := models.ParseOrgSettings(org.Settings); err != nil {
			s.logger.Warn().Err(err).Str("org_id", pr.OrgID.String()).Msg("failed to parse org settings for auto-archive")
		} else if settings.AutoArchiveOnPRClose {
			// Fetch snapshot_key before archiving so we only round-trip to
			// the DB once for the cleanup path.
			var snapshotKey *string
			if s.snapshots != nil {
				if run, err := s.sessions.GetByID(ctx, pr.OrgID, *pr.SessionID); err == nil {
					snapshotKey = run.SnapshotKey
				}
			}
			if err := s.sessions.ArchiveSystem(ctx, pr.OrgID, *pr.SessionID); err != nil {
				s.logger.Warn().Err(err).
					Str("session_id", pr.SessionID.String()).
					Str("pr_id", pr.ID.String()).
					Msg("failed to auto-archive session on PR close")
			} else {
				if s.snapshots != nil {
					if err := storage.CleanupSessionSnapshot(ctx, s.snapshots, s.sessions, pr.OrgID, *pr.SessionID, snapshotKey); err != nil {
						s.logger.Warn().Err(err).Str("session_id", pr.SessionID.String()).Msg("failed to clean up snapshot on auto-archive")
					}
				}
				if s.audit != nil {
					sessionIDStr := pr.SessionID.String()
					details, marshalErr := json.Marshal(map[string]any{
						"session_id":       sessionIDStr,
						"pull_request_id":  pr.ID.String(),
						"github_repo":      pr.GitHubRepo,
						"github_pr_number": pr.GitHubPRNumber,
						"merged":           event.PR.Merged,
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
		}
	}

	return nil
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
		return nil, &githubAPIError{
			Method:     method,
			Path:       path,
			StatusCode: resp.StatusCode,
			Body:       respBody,
		}
	}

	return respBody, nil
}

// githubAPIError wraps a non-2xx response from the GitHub REST API so callers
// can inspect the status and parsed error details with errors.As rather than
// matching on prose.
type githubAPIError struct {
	Method     string
	Path       string
	StatusCode int
	Body       []byte
}

func (e *githubAPIError) Error() string {
	return fmt.Sprintf("GitHub API %s %s returned %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
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
func (e *githubAPIError) IsNoCommitsBetween() bool {
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

func (e *githubAPIError) IsExistingPullRequest() bool {
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

func (s *PRService) createRef(ctx context.Context, token, owner, repo, ref, sha string) error {
	path := fmt.Sprintf("/repos/%s/%s/git/refs", owner, repo)
	_, err := s.doGitHubRequest(ctx, token, http.MethodPost, path, map[string]string{
		"ref": ref,
		"sha": sha,
	})
	return err
}

func (s *PRService) updateRef(ctx context.Context, token, owner, repo, ref, sha string) error {
	path := fmt.Sprintf("/repos/%s/%s/git/%s", owner, repo, ref)
	_, err := s.doGitHubRequest(ctx, token, http.MethodPatch, path, map[string]any{
		"sha":   sha,
		"force": true,
	})
	return err
}

func (s *PRService) createBlob(ctx context.Context, token, owner, repo, content string) (string, error) {
	path := fmt.Sprintf("/repos/%s/%s/git/blobs", owner, repo)
	body, err := s.doGitHubRequest(ctx, token, http.MethodPost, path, map[string]string{
		"content":  content,
		"encoding": "utf-8",
	})
	if err != nil {
		return "", err
	}
	var result struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	return result.SHA, nil
}

type treeEntry struct {
	Path string  `json:"path"`
	Mode string  `json:"mode"`
	Type string  `json:"type"`
	SHA  *string `json:"sha"` // nil = delete
}

func (s *PRService) createTree(ctx context.Context, token, owner, repo, baseTreeSHA string, entries []treeEntry) (string, error) {
	path := fmt.Sprintf("/repos/%s/%s/git/trees", owner, repo)
	body, err := s.doGitHubRequest(ctx, token, http.MethodPost, path, map[string]any{
		"base_tree": baseTreeSHA,
		"tree":      entries,
	})
	if err != nil {
		return "", err
	}
	var result struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	return result.SHA, nil
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

	var apiErr *githubAPIError
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

// firstLine returns the first non-empty line of s, truncated to 72 chars.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			if len(line) > 72 {
				return line[:72]
			}
			return line
		}
	}
	return ""
}

func formatBranchName(session *models.Session, issue *models.Issue) string {
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
	return fmt.Sprintf("143/%s/%s", short, slug)
}

func formatPRTitle(session *models.Session, issue *models.Issue) string {
	// Issue-based sessions: keep current behavior.
	if issue != nil {
		switch issue.Source {
		case models.IssueSourceLinear:
			return fmt.Sprintf("%s: %s", issue.ExternalID, issue.Title)
		default:
			return fmt.Sprintf("fix: %s", issue.Title)
		}
	}

	// Issueless sessions: use session title or result summary.
	if session.Title != nil && *session.Title != "" {
		return *session.Title
	}
	if session.ResultSummary != nil && *session.ResultSummary != "" {
		return firstLine(*session.ResultSummary)
	}
	return fmt.Sprintf("Session %s", session.ID.String()[:8])
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
	}

	return nil
}
