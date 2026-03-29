package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/llm"
	"github.com/assembledhq/143/internal/models"
)

const (
	defaultGitHubAPI  = "https://api.github.com"
	maxBranchSlugLen  = 60
	maxLabelsToCreate = 5
)

// PRService handles GitHub PR creation and webhook-based tracking.
type PRService struct {
	tokenProvider    *Service
	pullRequests     *db.PullRequestStore
	sessions         *db.SessionStore
	issues           *db.IssueStore
	deploys          *db.DeployStore
	validations      *db.ValidationStore
	repos            *db.RepositoryStore
	jobs             *db.JobStore
	reviewComments   *db.ReviewCommentStore
	userCredentials  *db.UserCredentialStore
	users            *db.UserStore
	orgs             *db.OrganizationStore
	llmClient        llm.Client
	logger           zerolog.Logger
	baseURL          string
	httpClient       *http.Client
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
		sessions:     sessions,
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

// SetOrgStore sets the organization store for fetching org settings.
func (s *PRService) SetOrgStore(store *db.OrganizationStore) {
	s.orgs = store
}

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
		if err == nil && cred.Config != nil {
			cfg, ok := cred.Config.(models.GitHubOAuthConfig)
			if ok && cfg.AccessToken != "" {
				user, userErr := s.users.GetByID(ctx, run.OrgID, *run.TriggeredByUserID)
				if userErr == nil {
					return &tokenResolution{
						Token:       cfg.AccessToken,
						IsUserToken: true,
						User:        &user,
					}, nil
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

// SetBaseURL overrides the GitHub API base URL (for testing).
func (s *PRService) SetBaseURL(url string) {
	s.baseURL = url
}

// CreatePR creates a GitHub PR from a completed agent run.
// The session may or may not have an associated issue — issueless sessions
// (e.g. manually created) derive PR metadata from the session itself.
func (s *PRService) CreatePR(ctx context.Context, run *models.Session) (*models.PullRequest, error) {
	if run.Diff == nil || *run.Diff == "" {
		return nil, fmt.Errorf("agent run %s has no diff", run.ID)
	}

	// Issue lookup is now optional — sessions may not have an associated issue.
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

	// Resolve org settings for PR authorship and draft defaults.
	orgSettings := models.OrgSettings{PRAuthorship: models.PRAuthorshipUserPreferred}
	if s.orgs != nil {
		org, orgErr := s.orgs.GetByID(ctx, run.OrgID)
		if orgErr == nil {
			parsed, parseErr := models.ParseOrgSettings(org.Settings)
			if parseErr == nil {
				orgSettings = parsed
			}
		}
	}

	// Resolve GitHub token: user token (preferred) → app installation token.
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

	// 1. Get default branch SHA.
	baseSHA, err := s.getRef(ctx, token, owner, repoName, "refs/heads/"+defaultBranch)
	if err != nil {
		return nil, fmt.Errorf("get default branch ref: %w", err)
	}

	// 2. Create branch.
	branchName := formatBranchName(run, issue)
	if err := s.createRef(ctx, token, owner, repoName, "refs/heads/"+branchName, baseSHA); err != nil {
		return nil, fmt.Errorf("create branch: %w", err)
	}

	// 3. Parse diff and create blobs, tree, commit.
	files := parseDiff(*run.Diff)
	if len(files) == 0 {
		return nil, fmt.Errorf("diff produced no file changes")
	}

	treeEntries := make([]treeEntry, 0, len(files))
	for _, f := range files {
		if f.Deleted {
			treeEntries = append(treeEntries, treeEntry{
				Path: f.Path,
				Mode: "100644",
				Type: "blob",
				SHA:  nil, // null SHA deletes the file
			})
			continue
		}
		blobSHA, err := s.createBlob(ctx, token, owner, repoName, f.Content)
		if err != nil {
			return nil, fmt.Errorf("create blob for %s: %w", f.Path, err)
		}
		treeEntries = append(treeEntries, treeEntry{
			Path: f.Path,
			Mode: "100644",
			Type: "blob",
			SHA:  &blobSHA,
		})
	}

	treeSHA, err := s.createTree(ctx, token, owner, repoName, baseSHA, treeEntries)
	if err != nil {
		return nil, fmt.Errorf("create tree: %w", err)
	}

	commitMsg := formatCommitMessage(run, issue)

	// Set commit author when using a user token; add Co-authored-by trailer for app token.
	var author *commitAuthor
	if resolution.IsUserToken && resolution.User != nil {
		author = &commitAuthor{
			Name:  resolution.User.Name,
			Email: resolution.User.Email,
			Date:  time.Now().UTC().Format(time.RFC3339),
		}
	} else if !resolution.IsUserToken && run.TriggeredByUserID != nil && s.users != nil {
		// App token: add Co-authored-by trailer for attribution.
		user, userErr := s.users.GetByID(ctx, run.OrgID, *run.TriggeredByUserID)
		if userErr == nil {
			commitMsg += fmt.Sprintf("\n\nCo-authored-by: %s <%s>", user.Name, user.Email)
		}
	}

	commitSHA, err := s.createCommitWithAuthor(ctx, token, owner, repoName, commitMsg, treeSHA, baseSHA, author)
	if err != nil {
		return nil, fmt.Errorf("create commit: %w", err)
	}

	// 4. Update branch ref to point to new commit.
	if err := s.updateRef(ctx, token, owner, repoName, "refs/heads/"+branchName, commitSHA); err != nil {
		return nil, fmt.Errorf("update branch ref: %w", err)
	}

	// 5. Create PR.
	title := formatPRTitle(run, issue)
	body := s.generatePRBody(ctx, token, owner, repoName, defaultBranch, run, issue)

	var prOpts []prCreateOption
	if orgSettings.PRDraftDefault {
		prOpts = append(prOpts, withDraft(true))
	}

	prNumber, prURL, err := s.createPullRequest(ctx, token, owner, repoName, title, body, branchName, defaultBranch, prOpts...)
	if err != nil {
		return nil, fmt.Errorf("create pull request: %w", err)
	}

	// 6. Add labels (best-effort).
	labels := buildLabels(issue)
	if len(labels) > 0 {
		if err := s.addLabels(ctx, token, owner, repoName, prNumber, labels); err != nil {
			s.logger.Warn().Err(err).Int("pr_number", prNumber).Msg("failed to add labels to PR")
		}
	}

	// 7. Store PR in DB.
	pr := &models.PullRequest{
		SessionID:      run.ID,
		OrgID:          run.OrgID,
		GitHubPRNumber: prNumber,
		GitHubPRURL:    prURL,
		GitHubRepo:     repo.FullName,
		Title:          title,
		Body:           &body,
		Status:         "open",
		ReviewStatus:   "pending",
	}
	if err := s.pullRequests.Create(ctx, pr); err != nil {
		return nil, fmt.Errorf("store pull request: %w", err)
	}

	// 8. Update agent run status.
	if err := s.sessions.UpdateStatus(ctx, run.OrgID, run.ID, "pr_created"); err != nil {
		s.logger.Warn().Err(err).Str("run_id", run.ID.String()).Msg("failed to update agent run status")
	}

	// 9. Update issue status (only when an issue is attached).
	if issue != nil {
		if err := s.issues.UpdateStatus(ctx, run.OrgID, run.IssueID, "in_progress"); err != nil {
			s.logger.Warn().Err(err).Str("issue_id", run.IssueID.String()).Msg("failed to update issue status")
		}
	}

	return pr, nil
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

		// Get the agent run to find the issue.
		run, err := s.sessions.GetByID(ctx, pr.OrgID, pr.SessionID)
		if err != nil {
			return fmt.Errorf("get agent run: %w", err)
		}

		// Update issue status to fixed.
		if err := s.issues.UpdateStatus(ctx, pr.OrgID, run.IssueID, "fixed"); err != nil {
			s.logger.Warn().Err(err).Str("issue_id", run.IssueID.String()).Msg("failed to update issue status to fixed")
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

	return nil
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

// PushRevision pushes additional commits to an existing PR branch for a revision run.
func (s *PRService) PushRevision(ctx context.Context, pr *models.PullRequest, run *models.Session) error {
	if run.Diff == nil || *run.Diff == "" {
		return fmt.Errorf("revision run %s has no diff", run.ID)
	}

	// Resolve repository: session.RepositoryID first, then issue.RepositoryID.
	var repoID *uuid.UUID
	if run.RepositoryID != nil {
		repoID = run.RepositoryID
	} else {
		issue, err := s.issues.GetByID(ctx, run.OrgID, run.IssueID)
		if err != nil {
			return fmt.Errorf("get issue: %w", err)
		}
		repoID = issue.RepositoryID
	}
	if repoID == nil {
		return fmt.Errorf("session %s has no repository", run.ID)
	}
	repo, err := s.repos.GetByID(ctx, run.OrgID, *repoID)
	if err != nil {
		return fmt.Errorf("get repository: %w", err)
	}

	token, err := s.tokenProvider.GetInstallationToken(ctx, repo.InstallationID)
	if err != nil {
		return fmt.Errorf("get installation token: %w", err)
	}

	owner, repoName := splitRepo(repo.FullName)

	// 1. Get current HEAD of the PR branch via GitHub API.
	headSHA, headBranch, err := s.getPRHead(ctx, token, owner, repoName, pr.GitHubPRNumber)
	if err != nil {
		return fmt.Errorf("get PR head: %w", err)
	}

	// 2. Parse diff and create blobs/tree/commit.
	files := parseDiff(*run.Diff)
	if len(files) == 0 {
		return fmt.Errorf("revision diff produced no file changes")
	}

	treeEntries := make([]treeEntry, 0, len(files))
	for _, f := range files {
		if f.Deleted {
			treeEntries = append(treeEntries, treeEntry{
				Path: f.Path, Mode: "100644", Type: "blob", SHA: nil,
			})
			continue
		}
		blobSHA, err := s.createBlob(ctx, token, owner, repoName, f.Content)
		if err != nil {
			return fmt.Errorf("create blob for %s: %w", f.Path, err)
		}
		treeEntries = append(treeEntries, treeEntry{
			Path: f.Path, Mode: "100644", Type: "blob", SHA: &blobSHA,
		})
	}

	treeSHA, err := s.createTree(ctx, token, owner, repoName, headSHA, treeEntries)
	if err != nil {
		return fmt.Errorf("create tree: %w", err)
	}

	commitMsg := fmt.Sprintf("address review feedback\n\nRevision of agent run %s", run.ID)
	if run.ParentSessionID != nil {
		commitMsg = fmt.Sprintf("address review feedback\n\nRevision of agent run %s (parent: %s)", run.ID, run.ParentSessionID)
	}

	commitSHA, err := s.createCommit(ctx, token, owner, repoName, commitMsg, treeSHA, headSHA)
	if err != nil {
		return fmt.Errorf("create commit: %w", err)
	}

	// 3. Update branch ref.
	if err := s.updateRef(ctx, token, owner, repoName, "refs/heads/"+headBranch, commitSHA); err != nil {
		return fmt.Errorf("update branch ref: %w", err)
	}

	// 4. Post a comment summarizing the revision.
	summaryBody := "## Revision Applied\n\nThis commit addresses reviewer feedback from the previous review.\n\n"
	if run.ResultSummary != nil {
		summaryBody += *run.ResultSummary
	}
	summaryBody += fmt.Sprintf("\n\n*Revision run: %s*", run.ID)

	s.postComment(ctx, token, owner, repoName, pr.GitHubPRNumber, summaryBody)

	return nil
}

func (s *PRService) getPRHead(ctx context.Context, token, owner, repo string, prNumber int) (sha, branch string, err error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, prNumber)
	body, err := s.doGitHubRequest(ctx, token, http.MethodGet, path, nil)
	if err != nil {
		return "", "", err
	}
	var result struct {
		Head struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", "", err
	}
	return result.Head.SHA, result.Head.Ref, nil
}

func (s *PRService) postComment(ctx context.Context, token, owner, repo string, prNumber int, body string) {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, prNumber)
	if _, err := s.doGitHubRequest(ctx, token, http.MethodPost, path, map[string]string{
		"body": body,
	}); err != nil {
		s.logger.Warn().Err(err).Int("pr_number", prNumber).Msg("failed to post PR comment")
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
		return nil, fmt.Errorf("GitHub API %s %s returned %d: %s", method, path, resp.StatusCode, respBody)
	}

	return respBody, nil
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

// commitAuthor represents the author/committer for a Git commit.
type commitAuthor struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Date  string `json:"date"`
}

func (s *PRService) createCommit(ctx context.Context, token, owner, repo, message, treeSHA, parentSHA string) (string, error) {
	return s.createCommitWithAuthor(ctx, token, owner, repo, message, treeSHA, parentSHA, nil)
}

func (s *PRService) createCommitWithAuthor(ctx context.Context, token, owner, repo, message, treeSHA, parentSHA string, author *commitAuthor) (string, error) {
	path := fmt.Sprintf("/repos/%s/%s/git/commits", owner, repo)
	payload := map[string]any{
		"message": message,
		"tree":    treeSHA,
		"parents": []string{parentSHA},
	}
	if author != nil {
		payload["author"] = author
	}
	body, err := s.doGitHubRequest(ctx, token, http.MethodPost, path, payload)
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

func (s *PRService) addLabels(ctx context.Context, token, owner, repo string, prNumber int, labels []string) error {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/labels", owner, repo, prNumber)
	_, err := s.doGitHubRequest(ctx, token, http.MethodPost, path, map[string][]string{
		"labels": labels,
	})
	return err
}

// --- Formatting helpers ---

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
// Returns empty string if no template is found.
func (s *PRService) fetchPRTemplate(ctx context.Context, token, owner, repo, ref string) string {
	for _, path := range prTemplatePaths {
		url := fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s", owner, repo, path, ref)
		body, err := s.doGitHubRequest(ctx, token, http.MethodGet, url, nil)
		if err != nil {
			continue
		}
		var content struct {
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		}
		if err := json.Unmarshal(body, &content); err != nil {
			continue
		}
		if content.Encoding == "base64" && content.Content != "" {
			decoded, err := decodeBase64Content(content.Content)
			if err == nil && decoded != "" {
				return decoded
			}
		}
	}
	return ""
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

// generatePRBody produces the PR description. It tries the repo's PR template first
// (filled via LLM), falling back to the minimal default template.
func (s *PRService) generatePRBody(ctx context.Context, token, owner, repoName, defaultBranch string, run *models.Session, issue *models.Issue) string {
	// 1. Try repo template.
	template := s.fetchPRTemplate(ctx, token, owner, repoName, defaultBranch)
	if template != "" {
		filled, err := s.fillRepoTemplate(ctx, template, run, issue)
		if err == nil && filled != "" {
			return filled + "\n\n---\n*Generated by [143.dev](https://143.dev)*\n"
		}
		s.logger.Warn().Err(err).Msg("failed to fill repo PR template, falling back to default")
	}

	// 2. Fall back to minimal default.
	return s.formatPRBody(ctx, run, issue)
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

// --- Diff parser ---

type diffFile struct {
	Path    string
	Content string
	Deleted bool
}

// parseDiff extracts file paths and their full new content from a unified diff.
// For simplicity, this parser expects the diff to contain the full file content
// in the "+" lines (as produced by `git diff --no-index` or agent-generated diffs).
func parseDiff(diff string) []diffFile {
	var files []diffFile
	lines := strings.Split(diff, "\n")

	var currentPath string
	var contentLines []string
	var isDeleted bool

	flush := func() {
		if currentPath != "" {
			files = append(files, diffFile{
				Path:    currentPath,
				Content: strings.Join(contentLines, "\n"),
				Deleted: isDeleted,
			})
		}
		currentPath = ""
		contentLines = nil
		isDeleted = false
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git") {
			flush()
			continue
		}
		if strings.HasPrefix(line, "+++ b/") {
			currentPath = strings.TrimPrefix(line, "+++ b/")
			continue
		}
		if strings.HasPrefix(line, "+++ /dev/null") {
			isDeleted = true
			continue
		}
		if strings.HasPrefix(line, "--- ") {
			continue
		}
		if strings.HasPrefix(line, "deleted file mode") {
			isDeleted = true
			continue
		}
		if strings.HasPrefix(line, "@@") || strings.HasPrefix(line, "index ") ||
			strings.HasPrefix(line, "new file mode") || strings.HasPrefix(line, "old mode") ||
			strings.HasPrefix(line, "new mode") || strings.HasPrefix(line, "Binary") {
			continue
		}
		// Collect content lines (strip the leading + for added lines, skip - lines).
		if currentPath != "" && !isDeleted {
			if strings.HasPrefix(line, "+") {
				contentLines = append(contentLines, strings.TrimPrefix(line, "+"))
			} else if strings.HasPrefix(line, "-") {
				// Removed line — skip.
			} else if strings.HasPrefix(line, " ") {
				// Context line — also part of the new file content.
				contentLines = append(contentLines, strings.TrimPrefix(line, " "))
			} else if line == "" {
				// Empty context line (no prefix) — part of the new file content.
				contentLines = append(contentLines, "")
			}
		}
	}
	flush()

	// Filter out empty paths.
	var result []diffFile
	for _, f := range files {
		path := strings.TrimSpace(f.Path)
		if path == "" {
			continue
		}
		// Sanitize path: reject traversal attempts and absolute paths.
		if strings.Contains(path, "..") || strings.HasPrefix(path, "/") {
			continue
		}
		f.Path = path
		// Trim trailing whitespace-only lines.
		f.Content = strings.TrimRightFunc(f.Content, unicode.IsSpace)
		if !f.Deleted && f.Content != "" {
			f.Content += "\n"
		}
		result = append(result, f)
	}
	return result
}
