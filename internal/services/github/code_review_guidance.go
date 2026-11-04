package github

import (
	"context"
	"crypto/sha1" // #nosec G505 -- GitHub Git blob IDs require SHA-1; this is not a security primitive.
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
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
	cacheKey := fmt.Sprintf("github:review-guidance:v1:%s:%s:%d:%s", orgID, repositoryID, resolution.InstallationID, repository.FullName)
	details, err := readReviewGuidanceResource(ctx, s, resolution.Token, root, cacheKey+":repository", decodeReviewGuidanceRepository)
	if err != nil {
		return result, fmt.Errorf("read review guidance default branch: %w", err)
	}
	result.Branch = details.DefaultBranch
	if err := result.Validate(); err != nil {
		return result, err
	}
	branch := url.PathEscape(result.Branch)
	entry, err := readReviewGuidanceResource(ctx, s, resolution.Token, root+"/git/trees/"+branch, cacheKey+":tree:"+branch, decodeReviewGuidanceTree)
	if err != nil {
		return result, fmt.Errorf("read review guidance tree: %w", err)
	}
	if entry.Path == "" {
		return result, nil
	}
	if entry.Type != "blob" || (entry.Mode != "100644" && entry.Mode != "100755") || entry.Size < 0 || entry.Size > models.CodeReviewGuidanceMaxBytes {
		return result, fmt.Errorf("%w: must be a regular file of at most %d bytes", models.ErrInvalidCodeReviewGuidanceFile, models.CodeReviewGuidanceMaxBytes)
	}
	result.Present, result.BlobSHA = true, entry.SHA
	if err := result.Validate(); err != nil {
		return result, err
	}
	blobKey := cacheKey + ":blob:" + entry.SHA
	if raw, found := s.readReviewGuidanceCache(ctx, blobKey); found && reviewGuidanceBlobMatches(entry, string(raw)) {
		result.Content = string(raw)
		return result, result.Validate()
	}
	result.Content, err = s.readReviewGuidanceBlob(ctx, resolution.Token, root, entry)
	if err != nil {
		return result, err
	}
	if err := result.Validate(); err != nil {
		return result, err
	}
	s.writeReviewGuidanceCache(ctx, blobKey, []byte(result.Content))
	return result, nil
}

type reviewGuidanceRepository struct {
	DefaultBranch string `json:"default_branch"`
}

func decodeReviewGuidanceRepository(body []byte) (reviewGuidanceRepository, error) {
	var repo reviewGuidanceRepository
	err := json.Unmarshal(body, &repo)
	return repo, err
}

type reviewGuidanceTreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size int    `json:"size"`
}

func decodeReviewGuidanceTree(body []byte) (reviewGuidanceTreeEntry, error) {
	var tree struct {
		SHA       string                    `json:"sha"`
		Truncated bool                      `json:"truncated"`
		Entries   []reviewGuidanceTreeEntry `json:"tree"`
	}
	if err := json.Unmarshal(body, &tree); err != nil {
		return reviewGuidanceTreeEntry{}, err
	}
	if tree.SHA == "" || tree.Truncated || tree.Entries == nil {
		return reviewGuidanceTreeEntry{}, fmt.Errorf("review guidance root tree is incomplete")
	}
	for _, entry := range tree.Entries {
		if entry.Path == "REVIEW.md" {
			return entry, nil
		}
	}
	return reviewGuidanceTreeEntry{}, nil
}

const reviewGuidanceCacheTTL = 24 * time.Hour

type reviewGuidanceCacheEntry[T any] struct {
	ETag  string `json:"etag"`
	Value *T     `json:"value"`
}

// Cache only the decoded branch or REVIEW.md tree entry, and reuse it only
// after GitHub validates the response for this repository and installation.
func readReviewGuidanceResource[T any](ctx context.Context, s *PRService, token, path, key string, decode func([]byte) (T, error)) (T, error) {
	var cached reviewGuidanceCacheEntry[T]
	if raw, found := s.readReviewGuidanceCache(ctx, key); found {
		if err := json.Unmarshal(raw, &cached); err != nil {
			s.logger.Warn().Err(err).Msg("could not decode cached review guidance metadata")
			cached = reviewGuidanceCacheEntry[T]{}
		}
		if cached.Value == nil {
			cached.ETag = ""
		}
	}
	response, err := s.doGitHubRESTRequest(ctx, token, http.MethodGet, path, nil, "application/vnd.github+json", cached.ETag)
	var zero T
	if err != nil {
		return zero, err
	}
	if response.status == http.StatusNotModified {
		if cached.ETag == "" {
			return zero, errors.New("GitHub returned not modified without cached review guidance metadata")
		}
		return *cached.Value, nil
	}
	value, err := decode(response.body)
	if err != nil {
		return zero, err
	}
	if s.redisClient != nil && response.etag != "" {
		raw, err := json.Marshal(reviewGuidanceCacheEntry[T]{ETag: response.etag, Value: &value})
		if err != nil {
			s.logger.Warn().Err(err).Msg("could not encode review guidance metadata for caching")
		} else {
			s.writeReviewGuidanceCache(ctx, key, raw)
		}
	}
	return value, nil
}

func (s *PRService) readReviewGuidanceBlob(ctx context.Context, token, root string, entry reviewGuidanceTreeEntry) (string, error) {
	body, err := s.doGitHubRequest(ctx, token, http.MethodGet, root+"/git/blobs/"+entry.SHA, nil)
	if err != nil {
		return "", fmt.Errorf("read review guidance blob: %w", err)
	}
	var blob struct {
		SHA      string `json:"sha"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal(body, &blob); err != nil {
		return "", fmt.Errorf("decode review guidance blob: %w", err)
	}
	if blob.Encoding != "base64" || blob.SHA != entry.SHA || len(blob.Content) > 2*models.CodeReviewGuidanceMaxBytes {
		return "", errors.New("review guidance blob is invalid or oversized")
	}
	content, err := decodeBase64Content(blob.Content)
	if err != nil {
		return "", fmt.Errorf("decode review guidance text: %w", err)
	}
	if !reviewGuidanceBlobMatches(entry, content) {
		return "", errors.New("review guidance bytes do not match captured blob")
	}
	return content, nil
}

func reviewGuidanceBlobMatches(entry reviewGuidanceTreeEntry, content string) bool {
	if len(content) != entry.Size {
		return false
	}
	digest := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(content), content))) // #nosec G401 -- Verify GitHub Git blob identity.
	return fmt.Sprintf("%x", digest) == entry.SHA
}

func (s *PRService) readReviewGuidanceCache(ctx context.Context, key string) ([]byte, bool) {
	if s.redisClient == nil {
		return nil, false
	}
	value, err := s.redisClient.GetBytes(ctx, key)
	if err != nil && !errors.Is(err, redis.Nil) {
		s.logger.Warn().Err(err).Msg("could not read review guidance cache")
	}
	return value, err == nil
}

func (s *PRService) writeReviewGuidanceCache(ctx context.Context, key string, value []byte) {
	if s.redisClient == nil {
		return
	}
	if err := s.redisClient.SetBytes(ctx, key, value, reviewGuidanceCacheTTL); err != nil {
		s.logger.Warn().Err(err).Msg("could not write review guidance cache")
	}
}
