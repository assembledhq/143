package github

import (
	"context"
	"crypto/sha1" // #nosec G505 -- GitHub Git blob IDs require SHA-1; this is not a security primitive.
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

// CaptureCodeReviewRepositoryGuidance reads the base repository's live default
// branch, independently of the PR head and target branch. The root tree pins
// the blob before its bytes are read; unrelated default-branch commits do not
// change the captured guidance identity. Errors never fall back to PR content.
func (s *PRService) CaptureCodeReviewRepositoryGuidance(ctx context.Context, orgID, repositoryID uuid.UUID) (models.CodeReviewRepositoryGuidance, error) {
	var result models.CodeReviewRepositoryGuidance
	repository, err := s.repos.GetByID(ctx, orgID, repositoryID)
	if err != nil {
		return result, err
	}
	resolution, err := s.getInstallationResolutionForRepo(ctx, orgID, &repository)
	if err != nil {
		return result, err
	}
	ctx = withGitHubResolutionContext(ctx, resolution, repository.InstallationID, "code_review_repository_guidance")
	owner, repo := splitRepo(repository.FullName)
	root := fmt.Sprintf("/repos/%s/%s", owner, repo)
	body, err := s.doGitHubRequest(ctx, resolution.Token, http.MethodGet, root, nil)
	if err != nil {
		return result, fmt.Errorf("read review guidance default branch: %w", err)
	}
	var details struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal(body, &details); err != nil {
		return result, fmt.Errorf("decode review guidance repository: %w", err)
	}
	result.Branch = details.DefaultBranch
	if err := result.Validate(); err != nil {
		return result, err
	}
	body, err = s.doGitHubRequest(ctx, resolution.Token, http.MethodGet, root+"/git/trees/"+url.PathEscape(result.Branch), nil)
	if err != nil {
		return result, fmt.Errorf("read review guidance tree: %w", err)
	}
	var tree struct {
		SHA       string `json:"sha"`
		Truncated bool   `json:"truncated"`
		Entries   []struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
			Size int    `json:"size"`
		} `json:"tree"`
	}
	if err := json.Unmarshal(body, &tree); err != nil {
		return result, fmt.Errorf("decode review guidance tree: %w", err)
	}
	if tree.SHA == "" || tree.Truncated || tree.Entries == nil {
		return result, fmt.Errorf("review guidance root tree is incomplete")
	}
	for _, entry := range tree.Entries {
		if entry.Path != "REVIEW.md" {
			continue
		}
		if entry.Type != "blob" || (entry.Mode != "100644" && entry.Mode != "100755") || entry.Size < 0 || entry.Size > models.CodeReviewGuidanceMaxBytes {
			return result, fmt.Errorf("REVIEW.md must be a regular file of at most %d bytes", models.CodeReviewGuidanceMaxBytes)
		}
		result.Present, result.BlobSHA = true, entry.SHA
		if err := result.Validate(); err != nil {
			return result, err
		}
		body, err = s.doGitHubRequest(ctx, resolution.Token, http.MethodGet, root+"/git/blobs/"+entry.SHA, nil)
		if err != nil {
			return result, fmt.Errorf("read review guidance blob: %w", err)
		}
		var blob struct {
			SHA      string `json:"sha"`
			Encoding string `json:"encoding"`
			Content  string `json:"content"`
		}
		if err := json.Unmarshal(body, &blob); err != nil {
			return result, fmt.Errorf("decode review guidance blob: %w", err)
		}
		if blob.Encoding != "base64" || blob.SHA != entry.SHA || len(blob.Content) > 2*models.CodeReviewGuidanceMaxBytes {
			return result, fmt.Errorf("review guidance blob is invalid or oversized")
		}
		result.Content, err = decodeBase64Content(blob.Content)
		if err != nil {
			return result, fmt.Errorf("decode review guidance text: %w", err)
		}
		digest := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(result.Content), result.Content))) // #nosec G401 -- Verify GitHub Git blob identity.
		if len(result.Content) != entry.Size || fmt.Sprintf("%x", digest) != entry.SHA {
			return result, fmt.Errorf("review guidance bytes do not match captured blob")
		}
		return result, result.Validate()
	}
	return result, nil
}
