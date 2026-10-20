package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
)

const (
	codeReviewCheckRunsPageSize = 100
	codeReviewCheckRunsMaxPages = 10
)

type gitHubCodeReviewCheckRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	HeadSHA    string `json:"head_sha"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	App        struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
	} `json:"app"`
	Output struct {
		Text string `json:"text"`
	} `json:"output"`
}

type gitHubCodeReviewCheckRunsResponse struct {
	TotalCount int                        `json:"total_count"`
	CheckRuns  []gitHubCodeReviewCheckRun `json:"check_runs"`
}

func (s *PRService) ListCodeReviewCheckRuns(ctx context.Context, orgID, repositoryID uuid.UUID, headSHA, checkName string) ([]models.CodeReviewCheckRun, error) {
	if s == nil || s.repos == nil || s.tokenProvider == nil {
		return nil, errors.New("code review check run lookup is not configured")
	}
	if strings.TrimSpace(headSHA) == "" || checkName == "" {
		return nil, errors.New("code review check run lookup requires a head SHA and check name")
	}
	repo, err := s.repos.GetByID(ctx, orgID, repositoryID)
	if err != nil {
		return nil, fmt.Errorf("load repository for code review check runs: %w", err)
	}
	owner, repoName, ok := strings.Cut(repo.FullName, "/")
	if !ok || owner == "" || repoName == "" {
		return nil, fmt.Errorf("malformed repository name %q", repo.FullName)
	}
	resolution, err := s.getInstallationResolutionForRepo(ctx, orgID, &repo)
	if err != nil {
		return nil, fmt.Errorf("get installation token for code review check runs: %w", err)
	}
	ctx = withGitHubResolutionContext(ctx, resolution, repo.InstallationID, "code_review_risk_tier")
	return s.listCodeReviewCheckRuns(ctx, resolution.Token, owner, repoName, headSHA, checkName)
}

func (s *PRService) listCodeReviewCheckRuns(ctx context.Context, token, owner, repo, ref, checkName string) ([]models.CodeReviewCheckRun, error) {
	runs := make([]models.CodeReviewCheckRun, 0)
	for page := 1; page <= codeReviewCheckRunsMaxPages; page++ {
		query := url.Values{}
		query.Set("check_name", checkName)
		query.Set("filter", "all")
		query.Set("per_page", strconv.Itoa(codeReviewCheckRunsPageSize))
		query.Set("page", strconv.Itoa(page))
		path := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs?%s", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(ref), query.Encode())
		body, err := s.doGitHubRequest(ctx, token, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		var resp gitHubCodeReviewCheckRunsResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("decode GitHub code review check runs: %w", err)
		}
		for _, run := range resp.CheckRuns {
			runs = append(runs, models.CodeReviewCheckRun{
				ID: run.ID, Name: run.Name, HeadSHA: run.HeadSHA, Status: run.Status, Conclusion: run.Conclusion,
				AppID: run.App.ID, AppSlug: run.App.Slug, OutputText: run.Output.Text,
			})
		}
		if len(runs) >= resp.TotalCount || len(resp.CheckRuns) == 0 {
			return runs, nil
		}
	}
	return nil, fmt.Errorf("GitHub reports more than %d %q check runs for one commit", codeReviewCheckRunsPageSize*codeReviewCheckRunsMaxPages, checkName)
}
