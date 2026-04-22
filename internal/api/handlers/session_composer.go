package handlers

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

const sessionComposerMentionLimit = 20
const sessionComposerTreeCacheTTL = 30 * time.Second

type sessionComposerRepoTreeService interface {
	GetInstallationToken(ctx context.Context, installationID int64) (string, error)
	ListRepositoryTree(ctx context.Context, token, owner, repo, branch string) ([]models.RepositoryTreeEntry, error)
}

type SessionComposerHandler struct {
	repoStore *db.RepositoryStore
	repoTree  sessionComposerRepoTreeService
	clock     func() time.Time
	cacheMu   sync.RWMutex
	treeCache map[string]cachedSessionComposerTree
}

type cachedSessionComposerTree struct {
	tree      []models.RepositoryTreeEntry
	expiresAt time.Time
}

func NewSessionComposerHandler(repoStore *db.RepositoryStore, repoTree sessionComposerRepoTreeService) *SessionComposerHandler {
	return &SessionComposerHandler{
		repoStore: repoStore,
		repoTree:  repoTree,
		clock:     time.Now,
		treeCache: make(map[string]cachedSessionComposerTree),
	}
}

func (h *SessionComposerHandler) ListFileMentions(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeJSON(w, http.StatusOK, models.ListResponse[models.SessionInputReference]{Data: []models.SessionInputReference{}})
		return
	}
	branch := strings.TrimSpace(r.URL.Query().Get("branch"))

	repoID, err := uuid.Parse(r.URL.Query().Get("repository_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY_ID", "invalid repository_id")
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())
	repo, err := requireActiveRepo(r.Context(), h.repoStore, orgID, repoID)
	if err != nil {
		switch err {
		case errRepoDisconnected:
			writeError(w, r, http.StatusBadRequest, "REPO_DISCONNECTED", "repository is disconnected; reconnect it to search mentions")
		case errRepoStoreUnconfigured:
			writeError(w, r, http.StatusInternalServerError, "REPO_STORE_UNCONFIGURED", "repository lookup not configured")
		default:
			writeError(w, r, http.StatusNotFound, "REPOSITORY_NOT_FOUND", "repository not found")
		}
		return
	}

	if h.repoTree == nil {
		writeError(w, r, http.StatusServiceUnavailable, "GITHUB_NOT_CONFIGURED", "GitHub App is not configured")
		return
	}

	parts := strings.SplitN(repo.FullName, "/", 2)
	if len(parts) != 2 {
		writeError(w, r, http.StatusInternalServerError, "INVALID_REPOSITORY", "invalid repository full name")
		return
	}

	if branch == "" {
		branch = repo.DefaultBranch
	}

	tree, err := h.repositoryTree(r.Context(), repo, parts[0], parts[1], branch)
	if err != nil {
		if strings.Contains(err.Error(), "token") {
			writeError(w, r, http.StatusBadGateway, "GITHUB_TOKEN_FAILED", "failed to get GitHub token")
			return
		}
		writeError(w, r, http.StatusBadGateway, "GITHUB_API_FAILED", "failed to load repository tree")
		return
	}

	results := rankSessionComposerReferences(query, tree)
	writeJSON(w, http.StatusOK, models.ListResponse[models.SessionInputReference]{Data: results})
}

func rankSessionComposerReferences(query string, tree []models.RepositoryTreeEntry) []models.SessionInputReference {
	type rankedReference struct {
		reference    models.SessionInputReference
		pathPrefix   bool
		basePrefix   bool
		baseContains bool
		length       int
	}

	queryLower := strings.ToLower(query)
	ranked := make([]rankedReference, 0, len(tree))
	for _, entry := range tree {
		if entry.Path == "" {
			continue
		}

		var kind models.SessionInputReferenceKind
		switch entry.Type {
		case models.RepositoryTreeEntryTypeFile:
			kind = models.SessionInputReferenceKindFile
		case models.RepositoryTreeEntryTypeDirectory:
			kind = models.SessionInputReferenceKindDirectory
		default:
			continue
		}

		pathLower := strings.ToLower(entry.Path)
		baseLower := pathLower
		if idx := strings.LastIndex(pathLower, "/"); idx >= 0 {
			baseLower = pathLower[idx+1:]
		}

		pathPrefix := strings.HasPrefix(pathLower, queryLower)
		basePrefix := strings.HasPrefix(baseLower, queryLower)
		baseContains := strings.Contains(baseLower, queryLower)
		if !pathPrefix && !baseContains && !strings.Contains(pathLower, queryLower) {
			continue
		}

		ranked = append(ranked, rankedReference{
			reference: models.SessionInputReference{
				Kind:    kind,
				Token:   "@" + entry.Path,
				Path:    entry.Path,
				Display: entry.Path,
			},
			pathPrefix:   pathPrefix,
			basePrefix:   basePrefix,
			baseContains: baseContains,
			length:       len(entry.Path),
		})
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		left := ranked[i]
		right := ranked[j]

		if left.pathPrefix != right.pathPrefix {
			return left.pathPrefix
		}
		if left.basePrefix != right.basePrefix {
			return left.basePrefix
		}
		if left.baseContains != right.baseContains {
			return left.baseContains
		}
		if left.length != right.length {
			return left.length < right.length
		}
		return left.reference.Path < right.reference.Path
	})

	limit := len(ranked)
	if limit > sessionComposerMentionLimit {
		limit = sessionComposerMentionLimit
	}

	results := make([]models.SessionInputReference, 0, limit)
	for _, item := range ranked[:limit] {
		results = append(results, item.reference)
	}
	return results
}

func (h *SessionComposerHandler) repositoryTree(ctx context.Context, repo models.Repository, owner, name, branch string) ([]models.RepositoryTreeEntry, error) {
	cacheKey := repo.ID.String() + ":" + branch
	now := h.clock()

	h.cacheMu.RLock()
	cached, ok := h.treeCache[cacheKey]
	h.cacheMu.RUnlock()
	if ok && now.Before(cached.expiresAt) {
		return cached.tree, nil
	}

	token, err := h.repoTree.GetInstallationToken(ctx, repo.InstallationID)
	if err != nil {
		return nil, err
	}

	tree, err := h.repoTree.ListRepositoryTree(ctx, token, owner, name, branch)
	if err != nil {
		return nil, err
	}

	h.cacheMu.Lock()
	h.pruneExpiredTreeCacheLocked(now)
	h.treeCache[cacheKey] = cachedSessionComposerTree{
		tree:      tree,
		expiresAt: now.Add(sessionComposerTreeCacheTTL),
	}
	h.cacheMu.Unlock()
	return tree, nil
}

func (h *SessionComposerHandler) pruneExpiredTreeCacheLocked(now time.Time) {
	for key, cached := range h.treeCache {
		if !now.Before(cached.expiresAt) {
			delete(h.treeCache, key)
		}
	}
}
