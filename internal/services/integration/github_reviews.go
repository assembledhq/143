package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/rs/zerolog"
)

// maxPaginationPages caps how many pages we follow when paginating GitHub
// API results. 10 pages * 100 per_page = 1000 items max.
const maxPaginationPages = 10

// linkNextRe extracts the URL from a GitHub Link header rel="next" entry.
// Example: <https://api.github.com/repos/...?page=2>; rel="next"
var linkNextRe = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// GitHubCodeReviewSource implements CodeReviewSource for GitHub pull requests.
// It uses the GitHub REST API to list recent PRs and their review comments.
//
// The token is a GitHub installation token (short-lived, generated from the
// GitHub App credentials). It's injected as GITHUB_TOKEN in the sandbox env.
type GitHubCodeReviewSource struct {
	httpClient *http.Client
	baseURL    string
	token      string
	owner      string
	repo       string
}

// GitHubCodeReviewConfig holds the connection details for a GitHub CodeReviewSource.
type GitHubCodeReviewConfig struct {
	BaseURL string // defaults to "https://api.github.com"
	Token   string // installation token or PAT
	Owner   string // repository owner
	Repo    string // repository name
}

// NewGitHubCodeReviewSource creates a GitHub CodeReviewSource from the given config.
func NewGitHubCodeReviewSource(cfg GitHubCodeReviewConfig) *GitHubCodeReviewSource {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}

	return &GitHubCodeReviewSource{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    baseURL,
		token:      cfg.Token,
		owner:      cfg.Owner,
		repo:       cfg.Repo,
	}
}

func (g *GitHubCodeReviewSource) Name() string { return "github" }

// ListRecentPRs returns recently merged (or open/closed) pull requests.
func (g *GitHubCodeReviewSource) ListRecentPRs(ctx context.Context, filter PRFilter) ([]PRSummary, error) {
	state := filter.State
	if state == "" || state == "merged" {
		// GitHub API doesn't have a "merged" state — use "closed" and filter.
		state = "closed"
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = 20
	}

	params := url.Values{
		"state":     {state},
		"sort":      {"updated"},
		"direction": {"desc"},
		"per_page":  {strconv.Itoa(limit)},
	}

	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls?%s",
		g.baseURL, g.owner, g.repo, params.Encode())

	var ghPRs []ghPullRequest
	if err := g.doGet(ctx, endpoint, &ghPRs); err != nil {
		return nil, fmt.Errorf("list PRs: %w", err)
	}

	wantMerged := filter.State == "" || filter.State == "merged"
	results := make([]PRSummary, 0, len(ghPRs))
	for _, pr := range ghPRs {
		if wantMerged && pr.MergedAt == nil {
			continue
		}
		summary := PRSummary{
			Number:    pr.Number,
			Title:     pr.Title,
			Author:    pr.User.Login,
			CreatedAt: pr.CreatedAt,
			WebURL:    pr.HTMLURL,
		}
		// Note: additions, deletions, and changed_files are NOT populated
		// from the list endpoint — the GitHub /pulls list API does not return
		// these fields. They are only available via the single-PR endpoint.
		// The PM agent can drill into specific PRs for change stats.
		if pr.MergedAt != nil {
			summary.State = "merged"
			summary.MergedAt = *pr.MergedAt
		} else if pr.State == "open" {
			summary.State = "open"
		} else {
			summary.State = "closed"
		}
		summary.ReviewStatus = reviewDecision(pr.ReviewComments)
		results = append(results, summary)
	}

	return results, nil
}

// GetPRReviews returns all reviews and inline review comments for a PR.
func (g *GitHubCodeReviewSource) GetPRReviews(ctx context.Context, prNumber int) ([]PRReview, error) {
	// Fetch reviews (the top-level review decisions).
	reviewsEndpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews",
		g.baseURL, g.owner, g.repo, prNumber)

	var ghReviews []ghReview
	if err := g.doGet(ctx, reviewsEndpoint, &ghReviews); err != nil {
		return nil, fmt.Errorf("get reviews: %w", err)
	}

	// Fetch all inline review comments, paginating via Link headers.
	commentsEndpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/comments?per_page=100",
		g.baseURL, g.owner, g.repo, prNumber)

	ghComments, err := doGetPaginated[ghReviewComment](ctx, g, commentsEndpoint)
	if err != nil {
		return nil, fmt.Errorf("get review comments: %w", err)
	}

	// Group inline comments by review ID.
	commentsByReview := make(map[int64][]PRReviewComment)
	for _, c := range ghComments {
		rc := PRReviewComment{
			Path:     c.Path,
			Line:     c.Line,
			Body:     c.Body,
			Author:   c.User.Login,
			DiffHunk: c.DiffHunk,
		}
		commentsByReview[c.PullRequestReviewID] = append(commentsByReview[c.PullRequestReviewID], rc)
	}

	results := make([]PRReview, 0, len(ghReviews))
	for _, r := range ghReviews {
		review := PRReview{
			Author:    r.User.Login,
			State:     r.State,
			Body:      r.Body,
			CreatedAt: r.SubmittedAt,
			Comments:  commentsByReview[r.ID],
		}
		results = append(results, review)
	}

	return results, nil
}

// doGet performs an authenticated GET request and decodes the JSON response.
func (g *GitHubCodeReviewSource) doGet(ctx context.Context, url string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	return json.NewDecoder(resp.Body).Decode(target)
}

// doGetPaginated fetches all pages of a GitHub API list endpoint by following
// Link rel="next" headers. It stops after maxPaginationPages to prevent
// runaway pagination. On mid-pagination errors it returns whatever was
// collected so far along with a nil error (logging a warning instead).
func doGetPaginated[T any](ctx context.Context, g *GitHubCodeReviewSource, initialURL string) ([]T, error) {
	var all []T
	nextURL := initialURL

	for page := 0; page < maxPaginationPages; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			if len(all) > 0 {
				zerolog.Ctx(ctx).Warn().Err(err).Int("pages_fetched", page).Msg("pagination stopped: failed to create request")
				return all, nil
			}
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+g.token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

		resp, err := g.httpClient.Do(req)
		if err != nil {
			if len(all) > 0 {
				zerolog.Ctx(ctx).Warn().Err(err).Int("pages_fetched", page).Msg("pagination stopped: request failed")
				return all, nil
			}
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			if len(all) > 0 {
				zerolog.Ctx(ctx).Warn().Int("status", resp.StatusCode).Int("pages_fetched", page).Msg("pagination stopped: non-200 status")
				return all, nil
			}
			return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
		}

		var items []T
		decodeErr := json.NewDecoder(resp.Body).Decode(&items)
		resp.Body.Close()
		if decodeErr != nil {
			if len(all) > 0 {
				zerolog.Ctx(ctx).Warn().Err(decodeErr).Int("pages_fetched", page).Msg("pagination stopped: decode error")
				return all, nil
			}
			return nil, decodeErr
		}

		all = append(all, items...)

		// Check for next page.
		nextURL = parseLinkNext(resp.Header.Get("Link"))
		if nextURL == "" {
			break
		}
	}

	return all, nil
}

// parseLinkNext extracts the URL for rel="next" from a GitHub Link header.
// Returns "" if no next link is found.
func parseLinkNext(header string) string {
	if header == "" {
		return ""
	}
	matches := linkNextRe.FindStringSubmatch(header)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}

// reviewDecision returns a simple summary based on review comment count.
// The list endpoint doesn't include the actual review decision (approved,
// changes_requested), so we can only distinguish "has reviews" vs "no reviews".
// The PM agent can drill into specific PRs via GetPRReviews for full decisions.
func reviewDecision(reviewComments int) string {
	if reviewComments > 0 {
		return "has_reviews"
	}
	return "pending"
}

// GitHub API response types — minimal structs for the fields we need.

type ghPullRequest struct {
	Number         int       `json:"number"`
	Title          string    `json:"title"`
	State          string    `json:"state"`
	HTMLURL        string    `json:"html_url"`
	User           ghUser    `json:"user"`
	CreatedAt      time.Time `json:"created_at"`
	MergedAt       *time.Time `json:"merged_at"`
	Additions      int       `json:"additions"`
	Deletions      int       `json:"deletions"`
	ChangedFiles   int       `json:"changed_files"`
	ReviewComments int       `json:"review_comments"`
}

type ghReview struct {
	ID          int64     `json:"id"`
	User        ghUser    `json:"user"`
	State       string    `json:"state"`
	Body        string    `json:"body"`
	SubmittedAt time.Time `json:"submitted_at"`
}

type ghReviewComment struct {
	ID                    int64  `json:"id"`
	PullRequestReviewID   int64  `json:"pull_request_review_id"`
	Path                  string `json:"path"`
	Line                  int    `json:"line"`
	Body                  string `json:"body"`
	DiffHunk              string `json:"diff_hunk"`
	User                  ghUser `json:"user"`
}

type ghUser struct {
	Login string `json:"login"`
}
