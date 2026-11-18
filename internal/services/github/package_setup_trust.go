package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

func (s *PRService) ValidatePackageSetupPullRequest(ctx context.Context, orgID, repositoryID uuid.UUID, number int, headSHA string) error {
	if s == nil || s.repos == nil || orgID == uuid.Nil || repositoryID == uuid.Nil || number <= 0 || headSHA == "" {
		return errors.New("package setup pull request trust is unavailable")
	}
	repository, err := s.repos.GetByID(ctx, orgID, repositoryID)
	if err != nil {
		return errors.New("package setup repository trust is unavailable")
	}
	resolution, err := s.getInstallationResolutionForRepo(ctx, orgID, &repository)
	if err != nil {
		return errors.New("package setup repository authentication is unavailable")
	}
	owner, repo := splitRepo(repository.FullName)
	ctx = withGitHubResolutionContext(ctx, resolution, repository.InstallationID, "package_setup_trust")
	raw, err := s.doGitHubRequest(ctx, resolution.Token, http.MethodGet, fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, number), nil)
	if err != nil {
		return errors.New("package setup pull request trust lookup failed")
	}
	return validatePackageSetupPullRequestResponse(raw, repository.FullName, headSHA)
}

func validatePackageSetupPullRequestResponse(raw []byte, repository, headSHA string) error {
	type repoIdentity struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	}
	var response struct {
		Head struct {
			SHA  string       `json:"sha"`
			Repo repoIdentity `json:"repo"`
		} `json:"head"`
		Base struct {
			Repo repoIdentity `json:"repo"`
		} `json:"base"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Head.SHA != headSHA || response.Head.Repo.ID <= 0 || response.Base.Repo.ID != response.Head.Repo.ID || !strings.EqualFold(response.Head.Repo.FullName, repository) || !strings.EqualFold(response.Base.Repo.FullName, repository) {
		return errors.New("package credentials require the exact head of a verified same-repository pull request")
	}
	return nil
}
