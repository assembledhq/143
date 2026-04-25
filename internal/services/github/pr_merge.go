package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
)

// ErrPullRequestNotMergeable is returned when a caller invokes MergePullRequest
// on a PR that is not currently in a clean, mergeable state. The handler maps
// this to HTTP 409 so the UI can refresh its view of PR health.
var ErrPullRequestNotMergeable = errors.New("pull request is not in a mergeable state")

// ErrNoMergeMethodAllowed is returned when the repository has all merge
// methods disabled — a misconfiguration the user must fix on GitHub.
var ErrNoMergeMethodAllowed = errors.New("no merge method is allowed on this repository")

// ErrMergeStateRefreshFailed is returned when we couldn't re-sync GitHub's
// view of the PR before merging. We refuse to merge on a stale snapshot — the
// user should retry once the refresh succeeds.
var ErrMergeStateRefreshFailed = errors.New("failed to refresh pull request state before merge")

// gitHubRepoMergeSettings captures the subset of repository fields we use to
// pick a merge method. GitHub's repo response has many more fields; we only
// decode what we need.
type gitHubRepoMergeSettings struct {
	AllowSquashMerge *bool `json:"allow_squash_merge"`
	AllowMergeCommit *bool `json:"allow_merge_commit"`
	AllowRebaseMerge *bool `json:"allow_rebase_merge"`
}

type gitHubMergeRequest struct {
	CommitTitle   string `json:"commit_title,omitempty"`
	CommitMessage string `json:"commit_message,omitempty"`
	SHA           string `json:"sha,omitempty"`
	MergeMethod   string `json:"merge_method,omitempty"`
}

type gitHubMergeResponse struct {
	SHA     string `json:"sha"`
	Merged  bool   `json:"merged"`
	Message string `json:"message"`
}

// MergePullRequest performs the GitHub merge for a PR that has been deemed
// ready by the user. It re-syncs PR health one last time before merging so
// that we don't merge stale state, then calls the GitHub merge API with a
// head-SHA guard so a race against a new push aborts the merge cleanly.
//
// Auth follows the same precedence as PR creation: user token when available
// and accessible, app installation token otherwise.
func (s *PRService) MergePullRequest(ctx context.Context, orgID, pullRequestID, userID uuid.UUID) (*models.PullRequestMergeResponse, error) {
	pr, err := s.pullRequests.GetByID(ctx, orgID, pullRequestID)
	if err != nil {
		return nil, fmt.Errorf("load pull request: %w", err)
	}
	if pr.Status != "open" {
		return nil, fmt.Errorf("%w: pull request status is %q", ErrPullRequestNotMergeable, pr.Status)
	}

	// Refresh GitHub state so we don't merge based on a stale snapshot. The
	// CanMerge gate downstream is meaningful only if it ran against fresh
	// GitHub data — falling through on a stale snapshot would defeat the
	// safety check, so we surface the failure and let the user retry.
	if err := s.SyncPullRequestState(ctx, orgID, pullRequestID); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMergeStateRefreshFailed, err)
	}
	// Reload PR after the sync so we see the freshest persisted state.
	pr, err = s.pullRequests.GetByID(ctx, orgID, pullRequestID)
	if err != nil {
		return nil, fmt.Errorf("reload pull request after sync: %w", err)
	}

	health, err := s.buildPullRequestHealthResponse(ctx, pr)
	if err != nil {
		return nil, fmt.Errorf("build pull request health: %w", err)
	}
	if !health.CanMerge {
		return nil, fmt.Errorf("%w: merge_state=%s, has_conflicts=%t, failing_tests=%d, checks=%d",
			ErrPullRequestNotMergeable, health.MergeState, health.HasConflicts, health.FailingTestCount, len(health.Checks))
	}
	if health.HeadSHA == "" {
		// CanMerge=true requires a clean merge_state, which in turn requires a
		// successful sync that wrote a snapshot with a head SHA. Reaching this
		// branch means our invariants drifted; refuse rather than skip the
		// race-protection guard that the SHA gives us downstream.
		return nil, fmt.Errorf("%w: pull request health is missing head SHA", ErrPullRequestNotMergeable)
	}

	repo, err := s.repos.GetByFullName(ctx, orgID, pr.GitHubRepo)
	if err != nil {
		return nil, fmt.Errorf("load repository: %w", err)
	}

	orgSettings := models.OrgSettings{PRAuthorship: models.PRAuthorshipUserPreferred}
	if s.orgs != nil {
		if org, orgErr := s.orgs.GetByID(ctx, orgID); orgErr == nil {
			if parsed, parseErr := models.ParseOrgSettings(org.Settings); parseErr == nil {
				orgSettings = parsed
			}
		}
	}

	// Synthesize a Session-shaped value so we can reuse the shared identity
	// resolver. The merge endpoint isn't tied to a specific session, but the
	// resolver only consumes orgID and the triggering user, both of which we
	// have on the request.
	resolverSession := &models.Session{OrgID: orgID, TriggeredByUserID: &userID}
	resolution, err := s.identityResolver().Resolve(ctx, resolverSession, &repo, orgSettings, "")
	if err != nil {
		return nil, fmt.Errorf("resolve github token: %w", err)
	}

	owner, repoName := splitRepo(pr.GitHubRepo)
	settings, err := s.fetchRepoMergeSettings(ctx, resolution.Token, owner, repoName)
	if err != nil {
		return nil, fmt.Errorf("fetch repo merge settings: %w", err)
	}

	method, ok := selectMergeMethod(settings)
	if !ok {
		return nil, ErrNoMergeMethodAllowed
	}
	if !hasAnyMergeFlagSet(settings) {
		// GitHub.com always returns the allow_* flags; an empty response
		// usually means a GitHub Enterprise variant or a partial JSON. We
		// still default to "merge" so the merge can proceed, but surface the
		// shape so it's debuggable if a user ever reports a wrong method.
		s.logger.Warn().
			Str("repo", pr.GitHubRepo).
			Msg("repository merge settings response had no allow_* flags; defaulting merge method")
	}

	mergeResp, err := s.mergePullRequestOnGitHub(ctx, resolution.Token, owner, repoName, pr.GitHubPRNumber, gitHubMergeRequest{
		SHA:         health.HeadSHA,
		MergeMethod: string(method),
	})
	if err != nil {
		return nil, err
	}
	if !mergeResp.Merged {
		return nil, fmt.Errorf("github reported merge not completed: %s", mergeResp.Message)
	}

	// Eagerly persist merged status so the UI flips immediately. The GitHub
	// pull_request closed webhook will idempotently confirm.
	if err := s.pullRequests.UpdateStatus(ctx, orgID, pullRequestID, "merged"); err != nil {
		s.logger.Warn().Err(err).Str("pull_request_id", pullRequestID.String()).Msg("failed to persist merged status after successful merge")
	}
	s.runMergedPullRequestFollowUps(ctx, pr, mergeResp.SHA)

	if current, err := s.pullRequests.GetHealthCurrent(ctx, orgID, pullRequestID); err == nil {
		s.publishPullRequestUpdated(ctx, pr, current)
	}

	s.logger.Info().
		Str("pull_request_id", pullRequestID.String()).
		Str("repo", pr.GitHubRepo).
		Int("number", pr.GitHubPRNumber).
		Str("merge_method", string(method)).
		Bool("user_token", resolution.IsUserToken()).
		Msg("merged pull request via API")

	return &models.PullRequestMergeResponse{
		Merged:      true,
		SHA:         mergeResp.SHA,
		Message:     mergeResp.Message,
		MergeMethod: method,
	}, nil
}

// selectMergeMethod picks a merge method honoring the repository's allow_*
// flags. GitHub does not expose a single "default" — instead each method is
// independently allowed/disallowed. We pick the most squash-friendly option
// available, which matches how the GitHub web UI defaults its merge button
// when multiple options are enabled. When all flags are nil (e.g. an older
// GitHub Enterprise that omits them), we fall back to "merge" for safety.
func selectMergeMethod(settings *gitHubRepoMergeSettings) (models.PullRequestMergeMethod, bool) {
	if !hasAnyMergeFlagSet(settings) {
		return models.PullRequestMergeMethodMerge, true
	}
	if settings.AllowSquashMerge != nil && *settings.AllowSquashMerge {
		return models.PullRequestMergeMethodSquash, true
	}
	if settings.AllowMergeCommit != nil && *settings.AllowMergeCommit {
		return models.PullRequestMergeMethodMerge, true
	}
	if settings.AllowRebaseMerge != nil && *settings.AllowRebaseMerge {
		return models.PullRequestMergeMethodRebase, true
	}
	return "", false
}

// hasAnyMergeFlagSet reports whether the GitHub repo response carried at
// least one of the allow_* flags. Used both to drive selectMergeMethod's
// "fall back to merge" branch and to log when the response shape is unusual.
func hasAnyMergeFlagSet(settings *gitHubRepoMergeSettings) bool {
	if settings == nil {
		return false
	}
	return settings.AllowSquashMerge != nil || settings.AllowMergeCommit != nil || settings.AllowRebaseMerge != nil
}

func (s *PRService) fetchRepoMergeSettings(ctx context.Context, token, owner, repo string) (*gitHubRepoMergeSettings, error) {
	path := fmt.Sprintf("/repos/%s/%s", owner, repo)
	body, err := s.doGitHubRequest(ctx, token, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var settings gitHubRepoMergeSettings
	if err := json.Unmarshal(body, &settings); err != nil {
		return nil, fmt.Errorf("decode GitHub repo merge settings: %w", err)
	}
	return &settings, nil
}

func (s *PRService) mergePullRequestOnGitHub(ctx context.Context, token, owner, repo string, number int, req gitHubMergeRequest) (*gitHubMergeResponse, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/merge", owner, repo, number)
	body, err := s.doGitHubRequest(ctx, token, http.MethodPut, path, req)
	if err != nil {
		return nil, err
	}
	var resp gitHubMergeResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode GitHub merge response: %w", err)
	}
	return &resp, nil
}
