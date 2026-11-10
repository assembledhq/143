package github

import (
	"context"
	"crypto/sha1" // #nosec G505 -- GitHub Git blob IDs require SHA-1; this is not a security primitive.
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

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
	cacheKey := fmt.Sprintf("github:review-guidance:v3:%s:%s:%d:%s", orgID, repositoryID, resolution.InstallationID, repository.FullName)
	details, err := readReviewGuidanceResource(ctx, s, resolution.Token, root, cacheKey+":repository", decodeReviewGuidanceRepository, 0)
	if err != nil {
		return result, fmt.Errorf("read review guidance default branch: %w", err)
	}
	result.Branch = details.DefaultBranch
	if err := result.Validate(); err != nil {
		return result, err
	}
	branch := url.PathEscape(result.Branch)
	tree, err := readReviewGuidanceResource(ctx, s, resolution.Token, root+"/git/trees/"+branch+"?recursive=1", cacheKey+":tree:"+branch, decodeReviewGuidanceTree, 64*1024*1024)
	if err != nil {
		return result, fmt.Errorf("read review guidance tree: %w", err)
	}
	entries, err := s.resolveReviewGuidanceTree(ctx, resolution.Token, root, cacheKey, result.Branch, tree, reviewGuidanceTraversalLimits{
		Requests: 128, Entries: 1_000_000, ResponseBytes: 64 * 1024 * 1024, Depth: 128,
	})
	if err != nil {
		return result, err
	}
	result, err = reviewGuidanceMetadata(result.Branch, entries)
	if err != nil {
		return result, err
	}
	nestedIndex := 0
	for _, entry := range entries {
		blobKey := cacheKey + ":blob:" + entry.SHA
		raw, found := s.readReviewGuidanceCache(ctx, blobKey)
		content := string(raw)
		cached := found && reviewGuidanceBlobMatches(entry, content)
		if !cached {
			content, err = s.readReviewGuidanceBlob(ctx, resolution.Token, root, entry)
			if err != nil {
				return result, err
			}
		}
		if entry.Path == "REVIEW.md" {
			result.Content = content
		} else {
			result.NestedFiles[nestedIndex].Content = content
			nestedIndex++
		}
		if err := result.Validate(); err != nil {
			return result, err
		}
		if !cached {
			s.writeReviewGuidanceCache(ctx, blobKey, []byte(content))
		}
	}
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

type reviewGuidanceTree struct {
	SHA           string                    `json:"sha"`
	Truncated     bool                      `json:"truncated"`
	EntryCount    int                       `json:"entry_count"`
	ResponseBytes int                       `json:"response_bytes"`
	Entries       []reviewGuidanceTreeEntry `json:"entries"`
}

type reviewGuidanceTraversalLimits struct {
	Requests, Entries, ResponseBytes, Depth int
}

type reviewGuidanceTreeTask struct {
	SHA       string
	Prefix    string
	Depth     int
	Recursive bool
}

type reviewGuidanceResolvedTree struct {
	SHA     string                    `json:"sha"`
	Entries []reviewGuidanceTreeEntry `json:"entries"`
}

func decodeReviewGuidanceTree(body []byte) (reviewGuidanceTree, error) {
	return decodeReviewGuidanceTreeResponse(body, true)
}

func decodeReviewGuidanceShallowTree(body []byte) (reviewGuidanceTree, error) {
	return decodeReviewGuidanceTreeResponse(body, false)
}

func decodeReviewGuidanceTreeResponse(body []byte, recursive bool) (reviewGuidanceTree, error) {
	var tree struct {
		SHA       string                    `json:"sha"`
		Truncated bool                      `json:"truncated"`
		Entries   []reviewGuidanceTreeEntry `json:"tree"`
	}
	if err := json.Unmarshal(body, &tree); err != nil {
		return reviewGuidanceTree{}, fmt.Errorf("%w: decode tree: %v", models.ErrIncompleteCodeReviewGuidance, err)
	}
	if !validReviewGuidanceSHA(tree.SHA) || tree.Entries == nil {
		return reviewGuidanceTree{}, fmt.Errorf("%w: tree identity or entries are missing", models.ErrIncompleteCodeReviewGuidance)
	}
	result := reviewGuidanceTree{SHA: tree.SHA, Truncated: tree.Truncated, EntryCount: len(tree.Entries), ResponseBytes: len(body), Entries: make([]reviewGuidanceTreeEntry, 0)}
	if tree.Truncated {
		return result, nil
	}
	for _, entry := range tree.Entries {
		if path.Base(entry.Path) == "REVIEW.md" || (!recursive && (entry.Type == "tree" || entry.Mode == "040000")) {
			result.Entries = append(result.Entries, entry)
		}
	}
	sort.Slice(result.Entries, func(i, j int) bool { return result.Entries[i].Path < result.Entries[j].Path })
	return result, nil
}

func (s *PRService) resolveReviewGuidanceTree(ctx context.Context, token, root, cacheKey, branch string, initial reviewGuidanceTree, limits reviewGuidanceTraversalLimits) ([]reviewGuidanceTreeEntry, error) {
	if limits.Requests < 1 || limits.Entries < 1 || limits.ResponseBytes < 1 || limits.Depth < 0 {
		return nil, fmt.Errorf("%w: invalid traversal limits", models.ErrIncompleteCodeReviewGuidance)
	}
	requests, entryCount, responseBytes := 0, 0, 0
	consume := func(tree reviewGuidanceTree) error {
		if !validReviewGuidanceSHA(tree.SHA) || tree.EntryCount < len(tree.Entries) || tree.EntryCount < 0 || tree.ResponseBytes < 0 {
			return fmt.Errorf("%w: invalid tree metadata", models.ErrIncompleteCodeReviewGuidance)
		}
		if requests >= limits.Requests || tree.EntryCount > limits.Entries-entryCount || tree.ResponseBytes > limits.ResponseBytes-responseBytes {
			return fmt.Errorf("%w: tree traversal limit exceeded", models.ErrIncompleteCodeReviewGuidance)
		}
		requests++
		entryCount += tree.EntryCount
		responseBytes += tree.ResponseBytes
		return nil
	}
	if err := consume(initial); err != nil {
		return nil, err
	}
	if !initial.Truncated {
		if _, err := reviewGuidanceMetadata(branch, initial.Entries); err != nil {
			return nil, err
		}
		return initial.Entries, nil
	}
	resolvedKey := cacheKey + ":resolved-tree:" + initial.SHA
	if raw, found := s.readReviewGuidanceCache(ctx, resolvedKey); found {
		var resolved reviewGuidanceResolvedTree
		if err := json.Unmarshal(raw, &resolved); err == nil && resolved.SHA == initial.SHA && resolved.Entries != nil {
			if _, err := reviewGuidanceMetadata(branch, resolved.Entries); err == nil {
				return resolved.Entries, nil
			}
		}
	}
	tasks := []reviewGuidanceTreeTask{{SHA: initial.SHA}}
	entries := make([]reviewGuidanceTreeEntry, 0)
	for len(tasks) > 0 {
		if requests >= limits.Requests {
			return nil, fmt.Errorf("%w: tree request limit exceeded", models.ErrIncompleteCodeReviewGuidance)
		}
		task := tasks[0]
		tasks = tasks[1:]
		requestPath := root + "/git/trees/" + task.SHA
		decode := decodeReviewGuidanceShallowTree
		if task.Recursive {
			requestPath += "?recursive=1"
			decode = decodeReviewGuidanceTree
		}
		remainingBytes := limits.ResponseBytes - responseBytes
		if remainingBytes <= 0 {
			return nil, fmt.Errorf("%w: tree response byte limit exceeded", models.ErrIncompleteCodeReviewGuidance)
		}
		response, err := s.doGitHubRESTRequestWithResponseLimit(ctx, token, http.MethodGet, requestPath, nil, "application/vnd.github+json", "", int64(remainingBytes))
		if errors.Is(err, errGitHubRESTResponseLimit) {
			return nil, fmt.Errorf("%w: tree response byte limit exceeded", models.ErrIncompleteCodeReviewGuidance)
		}
		if err != nil {
			return nil, fmt.Errorf("read pinned review guidance tree: %w", err)
		}
		tree, err := decode(response.body)
		if err != nil {
			return nil, err
		}
		if tree.SHA != task.SHA {
			return nil, fmt.Errorf("%w: fetched tree does not match requested SHA", models.ErrIncompleteCodeReviewGuidance)
		}
		if err := consume(tree); err != nil {
			return nil, err
		}
		if tree.Truncated {
			if !task.Recursive {
				return nil, fmt.Errorf("%w: nonrecursive tree is truncated", models.ErrIncompleteCodeReviewGuidance)
			}
			if requests+len(tasks)+1 > limits.Requests {
				return nil, fmt.Errorf("%w: pending tree requests exceed limit", models.ErrIncompleteCodeReviewGuidance)
			}
			tasks = append(tasks, reviewGuidanceTreeTask{SHA: task.SHA, Prefix: task.Prefix, Depth: task.Depth})
			continue
		}
		previousPath := ""
		for _, entry := range tree.Entries {
			if !validReviewGuidancePath(entry.Path) || (!task.Recursive && strings.Contains(entry.Path, "/")) || entry.Path <= previousPath {
				return nil, fmt.Errorf("%w: invalid or duplicate tree entry path", models.ErrIncompleteCodeReviewGuidance)
			}
			previousPath = entry.Path
			fullPath := entry.Path
			if task.Prefix != "" {
				fullPath = task.Prefix + "/" + entry.Path
			}
			if !validReviewGuidancePath(fullPath) || strings.Count(fullPath, "/") > limits.Depth {
				return nil, fmt.Errorf("%w: tree path or depth limit exceeded", models.ErrIncompleteCodeReviewGuidance)
			}
			if path.Base(entry.Path) == "REVIEW.md" {
				entry.Path = fullPath
				entries = append(entries, entry)
				if len(entries) > models.CodeReviewGuidanceMaxFiles {
					return nil, fmt.Errorf("%w: at most %d files are supported", models.ErrInvalidCodeReviewGuidanceFile, models.CodeReviewGuidanceMaxFiles)
				}
			} else if entry.Type == "tree" || entry.Mode == "040000" {
				if task.Recursive || entry.Type != "tree" || entry.Mode != "040000" || !validReviewGuidanceSHA(entry.SHA) {
					return nil, fmt.Errorf("%w: invalid subtree entry", models.ErrIncompleteCodeReviewGuidance)
				}
				if task.Depth+1 > limits.Depth || requests+len(tasks)+1 > limits.Requests {
					return nil, fmt.Errorf("%w: pending tree requests or depth exceed limit", models.ErrIncompleteCodeReviewGuidance)
				}
				tasks = append(tasks, reviewGuidanceTreeTask{SHA: entry.SHA, Prefix: fullPath, Depth: task.Depth + 1, Recursive: true})
			}
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	if _, err := reviewGuidanceMetadata(branch, entries); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(reviewGuidanceResolvedTree{SHA: initial.SHA, Entries: entries})
	if err != nil {
		return nil, err
	}
	s.writeReviewGuidanceCache(ctx, resolvedKey, raw)
	return entries, nil
}

func validReviewGuidanceSHA(sha string) bool {
	return len(sha) == 40 && strings.Trim(sha, "0123456789abcdef") == ""
}

func validReviewGuidancePath(filePath string) bool {
	return fs.ValidPath(filePath) && filePath != "." && len(filePath) <= 1024 && !strings.Contains(filePath, "\\") && !strings.ContainsFunc(filePath, unicode.IsControl)
}

func reviewGuidanceMetadata(branch string, entries []reviewGuidanceTreeEntry) (models.CodeReviewRepositoryGuidance, error) {
	result := models.CodeReviewRepositoryGuidance{Branch: branch}
	if len(entries) > models.CodeReviewGuidanceMaxFiles {
		return result, fmt.Errorf("%w: at most %d files are supported", models.ErrInvalidCodeReviewGuidanceFile, models.CodeReviewGuidanceMaxFiles)
	}
	totalBytes := 0
	previousPath := ""
	for _, entry := range entries {
		if entry.Path <= previousPath || path.Base(entry.Path) != "REVIEW.md" {
			return result, fmt.Errorf("%w: rubric paths must be unique and sorted", models.ErrInvalidCodeReviewGuidanceFile)
		}
		previousPath = entry.Path
		if entry.Type != "blob" || (entry.Mode != "100644" && entry.Mode != "100755") || entry.Size < 0 || entry.Size > models.CodeReviewGuidanceMaxBytes {
			return result, fmt.Errorf("%w: %s must be a regular file of at most %d bytes", models.ErrInvalidCodeReviewGuidanceFile, entry.Path, models.CodeReviewGuidanceMaxBytes)
		}
		totalBytes += entry.Size
		if totalBytes > models.CodeReviewGuidanceMaxBytes {
			return result, fmt.Errorf("%w: combined content must be at most %d bytes", models.ErrInvalidCodeReviewGuidanceFile, models.CodeReviewGuidanceMaxBytes)
		}
		if entry.Path == "REVIEW.md" {
			result.Present, result.BlobSHA = true, entry.SHA
		} else {
			result.NestedFiles = append(result.NestedFiles, models.CodeReviewGuidanceFile{Path: entry.Path, BlobSHA: entry.SHA})
		}
	}
	return result, result.Validate()
}

const reviewGuidanceCacheTTL = 24 * time.Hour

type reviewGuidanceCacheEntry[T any] struct {
	ETag  string `json:"etag"`
	Value *T     `json:"value"`
}

func readReviewGuidanceResource[T any](ctx context.Context, s *PRService, token, path, key string, decode func([]byte) (T, error), responseLimit int64) (T, error) {
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
	response, err := s.doGitHubRESTRequestWithResponseLimit(ctx, token, http.MethodGet, path, nil, "application/vnd.github+json", cached.ETag, responseLimit)
	var zero T
	if errors.Is(err, errGitHubRESTResponseLimit) {
		return zero, fmt.Errorf("%w: tree response byte limit exceeded", models.ErrIncompleteCodeReviewGuidance)
	}
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
	if etag := response.header.Get("ETag"); s.redisClient != nil && etag != "" {
		raw, err := json.Marshal(reviewGuidanceCacheEntry[T]{ETag: etag, Value: &value})
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
