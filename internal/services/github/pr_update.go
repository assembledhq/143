package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/db"
)

// ErrPullRequestWritePermissionRequired means the installed GitHub App has not
// been granted (or the installation owner has not yet approved) Pull requests
// Read & Write access.
var ErrPullRequestWritePermissionRequired = errors.New("GitHub App Pull requests write permission is required")

// ErrSessionPullRequestNotFound means the token-scoped session has no primary
// Pull Request recorded in 143.
var ErrSessionPullRequestNotFound = errors.New("current session Pull Request not found")

// UpdateSessionPullRequestParams contains the user-owned metadata that an
// agent may replace on the current session's primary Pull Request.
type UpdateSessionPullRequestParams struct {
	Title *string
	Body  *string
}

// UpdateSessionPullRequestResult identifies the exact Pull Request updated on
// GitHub and mirrored locally.
type UpdateSessionPullRequestResult struct {
	PullRequestID     uuid.UUID
	PullRequestNumber int
	PullRequestURL    string
	Title             string
}

type updatedGitHubPullRequest struct {
	Number  int     `json:"number"`
	HTMLURL string  `json:"html_url"`
	Title   string  `json:"title"`
	Body    *string `json:"body"`
	Head    struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		SHA string `json:"sha"`
	} `json:"base"`
}

// UpdateSessionPullRequest updates the current session's primary Pull Request
// with a server-held GitHub installation token. Sandboxes retain read-only PR
// tokens, so the write remains session-scoped, tenant-scoped, and auditable.
func (s *PRService) UpdateSessionPullRequest(
	ctx context.Context,
	orgID, sessionID uuid.UUID,
	params UpdateSessionPullRequestParams,
) (*UpdateSessionPullRequestResult, error) {
	if orgID == uuid.Nil || sessionID == uuid.Nil {
		return nil, errors.New("org_id and session_id are required")
	}
	if params.Title == nil && params.Body == nil {
		return nil, errors.New("title or body is required")
	}
	if params.Title != nil {
		title := strings.TrimSpace(*params.Title)
		if title == "" {
			return nil, errors.New("title must not be empty")
		}
		if len(title) > maxPRTitleLen {
			return nil, fmt.Errorf("title must be at most %d characters", maxPRTitleLen)
		}
		params.Title = &title
	}
	if s == nil || s.pullRequests == nil || s.repos == nil || s.tokenProvider == nil {
		return nil, errors.New("PRService: Pull Request update dependencies not configured")
	}

	pr, err := s.pullRequests.GetPrimaryBySessionID(ctx, orgID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: %w", ErrSessionPullRequestNotFound, err)
		}
		return nil, fmt.Errorf("load current session pull request: %w", err)
	}
	repository, err := s.repos.GetByFullNameAnyStatus(ctx, orgID, pr.GitHubRepo)
	if err != nil {
		return nil, fmt.Errorf("load pull request repository: %w", err)
	}
	token, err := s.getInstallationTokenForRepo(ctx, orgID, &repository)
	if err != nil {
		return nil, fmt.Errorf("get installation token for pull request update: %w", err)
	}

	owner, repoName := splitRepo(pr.GitHubRepo)
	payload := make(map[string]any, 2)
	if params.Title != nil {
		payload["title"] = *params.Title
	}
	if params.Body != nil {
		body := *params.Body
		markerChangesetID := pr.ChangesetID
		if markerChangesetID == nil && pr.Body != nil {
			markerSessionID, parsedChangesetID, ok := parsePublicationMarker(*pr.Body)
			if ok && markerSessionID == sessionID {
				markerChangesetID = &parsedChangesetID
			}
		}
		if markerChangesetID != nil {
			body = upsertPublicationMarker(body, sessionID, markerChangesetID)
		}
		if pr.Body != nil {
			if previewLink := s.existingPRPreviewFooterLink(*pr.Body, owner, repoName, pr.GitHubPRNumber); previewLink != "" {
				body = upsertPRPreviewFooter(body, previewLink)
			}
		}
		params.Body = &body
		payload["body"] = body
	}

	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repoName, pr.GitHubPRNumber)
	responseBody, err := s.doGitHubRequest(ctx, token, http.MethodPatch, path, payload)
	if err != nil {
		if pullRequestWritePermissionMissing(err) {
			return nil, fmt.Errorf("%w: %w", ErrPullRequestWritePermissionRequired, err)
		}
		return nil, fmt.Errorf("update pull request on GitHub: %w", err)
	}
	var updated updatedGitHubPullRequest
	if err := json.Unmarshal(responseBody, &updated); err != nil {
		return nil, fmt.Errorf("decode updated pull request: %w", err)
	}
	if updated.Number == 0 {
		updated.Number = pr.GitHubPRNumber
	}
	if updated.HTMLURL == "" {
		updated.HTMLURL = pr.GitHubPRURL
	}
	if updated.Title == "" {
		if params.Title != nil {
			updated.Title = *params.Title
		} else {
			updated.Title = pr.Title
		}
	}
	if updated.Body == nil {
		if params.Body != nil {
			updated.Body = params.Body
		} else {
			updated.Body = pr.Body
		}
	}
	headSHA, headRef, baseSHA := pr.HeadSHA, pr.HeadRef, pr.BaseSHA
	if updated.Head.SHA != "" {
		headSHA = &updated.Head.SHA
	}
	if updated.Head.Ref != "" {
		headRef = &updated.Head.Ref
	}
	if updated.Base.SHA != "" {
		baseSHA = &updated.Base.SHA
	}
	if err := s.pullRequests.UpdateGitHubSnapshot(ctx, orgID, pr.ID, db.PullRequestGitHubSnapshot{
		GitHubPRURL: updated.HTMLURL,
		Title:       updated.Title,
		Body:        updated.Body,
		HeadSHA:     headSHA,
		HeadRef:     headRef,
		BaseSHA:     baseSHA,
	}); err != nil {
		return nil, fmt.Errorf("store updated pull request metadata: %w", err)
	}

	return &UpdateSessionPullRequestResult{
		PullRequestID:     pr.ID,
		PullRequestNumber: updated.Number,
		PullRequestURL:    updated.HTMLURL,
		Title:             updated.Title,
	}, nil
}

func pullRequestWritePermissionMissing(err error) bool {
	var apiError *GitHubAPIError
	return errors.As(err, &apiError) &&
		apiError.StatusCode == http.StatusForbidden &&
		strings.Contains(strings.ToLower(apiError.Message()), "resource not accessible by integration")
}

func (s *PRService) existingPRPreviewFooterLink(body, owner, repo string, number int) string {
	stableURL, err := url.Parse(s.stablePRPreviewURL(owner, repo, number))
	if err != nil {
		stableURL = nil
	}
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "Preview: ") {
			continue
		}
		rawURL := strings.TrimSpace(strings.TrimPrefix(trimmed, "Preview: "))
		parsed, parseErr := url.Parse(rawURL)
		if parseErr != nil || parsed.Scheme == "" || parsed.Host == "" {
			continue
		}
		if stableURL != nil &&
			strings.EqualFold(parsed.Scheme, stableURL.Scheme) &&
			strings.EqualFold(parsed.Host, stableURL.Host) &&
			parsed.EscapedPath() == stableURL.EscapedPath() {
			return rawURL
		}
		if previewURLMatchesOriginTemplate(rawURL, s.previewOriginTemplate) {
			return rawURL
		}
		// Preserve hosted legacy per-preview origins even when an old record is
		// updated before PREVIEW_ORIGIN_TEMPLATE has been wired into the service.
		if (strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")) &&
			strings.HasSuffix(strings.ToLower(parsed.Hostname()), ".preview.143.dev") {
			return rawURL
		}
	}
	return ""
}

func previewURLMatchesOriginTemplate(rawURL, template string) bool {
	template = strings.TrimSpace(template)
	if template == "" || !strings.Contains(template, "{id}") {
		return false
	}
	pattern := regexp.QuoteMeta(template)
	pattern = strings.ReplaceAll(pattern, regexp.QuoteMeta("{id}"), `[^/?#]+`)
	matched, err := regexp.MatchString("^"+pattern+"$", rawURL)
	return err == nil && matched
}
