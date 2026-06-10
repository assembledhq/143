package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/sandbox"
	"github.com/assembledhq/143/internal/services/workspace"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// slashCommandNamePattern restricts the user-supplied `name` query param to
// shapes that can appear as a command file basename. The colon separator is
// allowed because nested project commands serialize as `dir:name` (Claude
// Code's convention). Anchors and the leading-character class together
// reject path-traversal-shaped inputs like `../../etc/passwd`.
var slashCommandNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9:_-]{0,127}$`)

const sessionComposerMentionLimit = 20
const sessionComposerSlashCommandLimit = 30
const sessionComposerTreeCacheTTL = 30 * time.Second
const sessionComposerCommandContentCacheTTL = 5 * time.Minute

type sessionComposerRepoTreeService interface {
	GetInstallationToken(ctx context.Context, installationID int64) (string, error)
	ListRepositoryTree(ctx context.Context, token, owner, repo, branch string) ([]models.RepositoryTreeEntry, error)
	GetFileContent(ctx context.Context, token, owner, repo, ref, path string) (string, error)
}

type SessionComposerHandler struct {
	repoStore       *db.RepositoryStore
	repoTree        sessionComposerRepoTreeService
	workspace       *sessionWorkspaceResolver
	mentionIndexes  *workspace.MentionIndexCache
	logger          zerolog.Logger
	clock           func() time.Time
	cacheMu         sync.RWMutex
	treeCache       map[string]cachedSessionComposerTree
	contentCacheMu  sync.RWMutex
	commandContents map[string]cachedSessionComposerCommandContent
}

type cachedSessionComposerTree struct {
	tree      []models.RepositoryTreeEntry
	expiresAt time.Time
}

type cachedSessionComposerCommandContent struct {
	content   string
	expiresAt time.Time
}

func NewSessionComposerHandler(repoStore *db.RepositoryStore, repoTree sessionComposerRepoTreeService) *SessionComposerHandler {
	return NewSessionComposerHandlerWithWorkspace(repoStore, nil, repoTree, nil, nil, nil, zerolog.Nop())
}

func NewSessionComposerHandlerWithWorkspace(
	repoStore *db.RepositoryStore,
	sessionStore *db.SessionStore,
	repoTree sessionComposerRepoTreeService,
	fileReader sandbox.FileReader,
	snapshotCache *workspace.SnapshotCache,
	redisClient *cache.Client,
	logger zerolog.Logger,
) *SessionComposerHandler {
	return &SessionComposerHandler{
		repoStore:       repoStore,
		repoTree:        repoTree,
		workspace:       newSessionWorkspaceResolver(sessionStore, repoStore, fileReader, snapshotCache, logger),
		mentionIndexes:  workspace.NewMentionIndexCache(workspace.MentionIndexCacheConfig{Redis: redisClient, Logger: logger}),
		logger:          logger,
		clock:           time.Now,
		treeCache:       make(map[string]cachedSessionComposerTree),
		commandContents: make(map[string]cachedSessionComposerCommandContent),
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
		// Customize the disconnected message for the file-mention surface so
		// the existing "search mentions" copy is preserved verbatim.
		if err == errRepoDisconnected {
			writeError(w, r, http.StatusBadRequest, "REPO_DISCONNECTED", "repository is disconnected; reconnect it to search mentions")
			return
		}
		h.writeRepoLookupError(w, r, err)
		return
	}

	if h.repoTree == nil {
		writeError(w, r, http.StatusServiceUnavailable, "GITHUB_NOT_CONFIGURED", "GitHub App is not configured")
		return
	}

	parts := strings.SplitN(repo.FullName, "/", 2)
	if len(parts) != 2 {
		logInvalidRepoFullName(r, repo.ID, repo.FullName)
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
	entries := make([]workspace.MentionIndexEntry, 0, len(tree))
	for _, entry := range tree {
		if entry.Path == "" {
			continue
		}
		switch entry.Type {
		case models.RepositoryTreeEntryTypeFile:
			entries = append(entries, workspace.MentionIndexEntry{Kind: string(models.SessionInputReferenceKindFile), Path: entry.Path})
		case models.RepositoryTreeEntryTypeDirectory:
			entries = append(entries, workspace.MentionIndexEntry{Kind: string(models.SessionInputReferenceKindDirectory), Path: entry.Path})
		}
	}
	return rankSessionComposerIndexEntries(query, entries)
}

func rankSessionComposerIndexEntries(query string, entries []workspace.MentionIndexEntry) []models.SessionInputReference {
	type rankedReference struct {
		reference    models.SessionInputReference
		pathPrefix   bool
		basePrefix   bool
		baseContains bool
		length       int
	}

	queryLower := strings.ToLower(query)
	ranked := make([]rankedReference, 0, len(entries))
	for _, entry := range entries {
		if entry.Path == "" {
			continue
		}

		var kind models.SessionInputReferenceKind
		switch entry.Kind {
		case string(models.SessionInputReferenceKindFile):
			kind = models.SessionInputReferenceKindFile
		case string(models.SessionInputReferenceKindDirectory):
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

// sessionMentionIndexKeys returns the exact cache key for the session's
// current workspace source plus the cross-turn stale alias. The exact key
// drops SnapshotKey when a live container will serve the build, mirroring
// resolveReaderForSession's source selection.
func (h *SessionComposerHandler) sessionMentionIndexKeys(session *models.Session) (string, string) {
	cacheSession := *session
	if session.ContainerID != nil && *session.ContainerID != "" && h.workspace.fileReader != nil {
		cacheSession.SnapshotKey = nil
	}
	return workspace.SessionMentionIndexCacheKey(&cacheSession), workspace.SessionMentionIndexStaleCacheKey(session)
}

// warmSessionMentionIndex builds the session's mention index in the
// background so a subsequent non-empty query is served from cache. Invoked
// from the empty-q request the composer fires as soon as the @-picker opens
// (and from the composer-mount prefetch), so the ~seconds-long workspace walk
// overlaps with the user typing instead of blocking the first ranked
// response. Best-effort: every failure is silent because the caller has
// already responded.
func (h *SessionComposerHandler) warmSessionMentionIndex(r *http.Request) {
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	detached := context.WithoutCancel(r.Context())

	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				h.logger.Warn().Interface("panic", rec).Str("session_id", sessionID.String()).Msg("panic warming session mention index")
			}
		}()
		ctx, cancel := context.WithTimeout(detached, defaultMentionIndexWarmTimeout)
		defer cancel()

		session, err := h.workspace.loadSession(ctx, orgID, sessionID)
		if err != nil {
			return
		}
		reader, _, err := h.workspace.resolveReaderForSession(ctx, &session)
		if err != nil {
			return
		}
		cacheKey, staleKey := h.sessionMentionIndexKeys(&session)
		if _, _, err := h.mentionIndexes.GetOrBuildStale(ctx, cacheKey, staleKey, func(ctx context.Context) (workspace.MentionIndex, error) {
			return workspace.BuildMentionIndex(ctx, reader)
		}); err != nil {
			h.logger.Debug().Err(err).Str("session_id", sessionID.String()).Msg("failed to warm session mention index")
		}
	}()
}

const defaultMentionIndexWarmTimeout = 60 * time.Second

func (h *SessionComposerHandler) ListSessionFileMentions(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		// The composer fires an empty-q request the moment the @-picker
		// opens; use it to start building the index before the first
		// character of the actual query arrives.
		h.warmSessionMentionIndex(r)
		writeJSON(w, http.StatusOK, models.ListResponse[models.SessionInputReference]{Data: []models.SessionInputReference{}})
		return
	}

	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())
	session, err := h.workspace.loadSession(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}

	reader, _, err := h.workspace.resolveReaderForSession(r.Context(), &session)
	if err != nil {
		switch {
		case errors.Is(err, workspace.ErrSnapshotMissing):
			writeError(w, r, http.StatusConflict, "NO_SANDBOX", "session has no active sandbox container or snapshot")
		case errors.Is(err, errRepoLookupFailed):
			writeError(w, r, http.StatusInternalServerError, "WORKDIR_UNAVAILABLE", "could not resolve session workspace path")
		default:
			writeError(w, r, http.StatusInternalServerError, "SNAPSHOT_UNAVAILABLE", "session workspace could not be loaded")
		}
		return
	}

	cacheKey, staleKey := h.sessionMentionIndexKeys(&session)
	index, stale, err := h.mentionIndexes.GetOrBuildStale(r.Context(), cacheKey, staleKey, func(ctx context.Context) (workspace.MentionIndex, error) {
		return workspace.BuildMentionIndex(ctx, reader)
	})
	if err != nil {
		h.logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to build session workspace mention index")
		switch {
		case errors.Is(err, workspace.ErrSnapshotMissing):
			writeError(w, r, http.StatusConflict, "NO_SANDBOX", "session has no active sandbox container or snapshot")
		case errors.Is(err, workspace.ErrSnapshotUnreadable):
			writeError(w, r, http.StatusInternalServerError, "SNAPSHOT_UNREADABLE", "session snapshot exists but cannot be read")
		default:
			writeError(w, r, http.StatusInternalServerError, "SNAPSHOT_UNAVAILABLE", "session workspace could not be loaded")
		}
		return
	}
	if stale {
		h.logger.Debug().Str("session_id", sessionID.String()).Msg("served stale session mention index while refreshing in background")
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.SessionInputReference]{
		Data: rankSessionComposerIndexEntries(query, index.Entries),
	})
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

// SlashCommandGroup is a single named cluster of commands surfaced in the
// picker. The frontend renders one section per group; future groups (MCP
// prompts, plugin commands, etc.) can be added without changing the response
// shape.
type SlashCommandGroup struct {
	Source models.SessionInputCommandSource `json:"source"`
	Label  string                           `json:"label"`
	Items  []models.SessionInputCommand     `json:"items"`
}

// SlashCommandListResponse is the response body for ListSlashCommands. We use
// a typed wrapper rather than ListResponse[T] because the picker is grouped
// (built-in vs project) — collapsing groups into a flat list would lose the
// section labeling the design requires.
type SlashCommandListResponse struct {
	Groups []SlashCommandGroup `json:"groups"`
}

// SlashCommandDetailResponse is the response body for GetSlashCommandDetail.
// Description is fetched lazily from the project command file's frontmatter
// (or full body, when no frontmatter is present) and cached per repo+branch+path.
type SlashCommandDetailResponse struct {
	Command models.SessionInputCommand `json:"command"`
}

// ListSlashCommands serves GET /api/v1/session-composer/slash-commands. It
// returns the union of built-in commands and (when repository_id+branch are
// provided) repo-scoped commands, filtered server-side by the optional q
// query string. The endpoint is intentionally cheap on the cold path: project
// discovery reuses the existing 30s repo-tree cache.
func (h *SessionComposerHandler) ListSlashCommands(w http.ResponseWriter, r *http.Request) {
	agentTypeRaw := strings.TrimSpace(r.URL.Query().Get("agent_type"))
	if agentTypeRaw == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_AGENT_TYPE", "agent_type is required")
		return
	}
	agentType := models.AgentType(agentTypeRaw)
	if err := agentType.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_AGENT_TYPE", err.Error())
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("q"))

	groups := []SlashCommandGroup{}

	builtin := buildBuiltinSlashCommandGroup(agentType, query)
	if len(builtin.Items) > 0 {
		groups = append(groups, builtin)
	}

	repoIDRaw := strings.TrimSpace(r.URL.Query().Get("repository_id"))
	if repoIDRaw != "" && models.SupportsProjectCommands(agentType) {
		projectGroup, err := h.buildProjectSlashCommandGroup(r, agentType, repoIDRaw, strings.TrimSpace(r.URL.Query().Get("branch")), query)
		if err != nil {
			h.writeRepoTreeError(w, r, err)
			return
		}
		if projectGroup != nil {
			groups = append(groups, *projectGroup)
		}
	}

	writeJSON(w, http.StatusOK, SlashCommandListResponse{Groups: groups})
}

// GetSlashCommandDetail serves GET /api/v1/session-composer/slash-commands/details.
// Used by the frontend after the user inserts a project-defined command to
// hydrate description metadata from the command file's frontmatter without
// paying for content fetches up front during picker open.
func (h *SessionComposerHandler) GetSlashCommandDetail(w http.ResponseWriter, r *http.Request) {
	agentTypeRaw := strings.TrimSpace(r.URL.Query().Get("agent_type"))
	if agentTypeRaw == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_AGENT_TYPE", "agent_type is required")
		return
	}
	agentType := models.AgentType(agentTypeRaw)
	if err := agentType.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_AGENT_TYPE", err.Error())
		return
	}
	if !models.SupportsProjectCommands(agentType) {
		writeError(w, r, http.StatusBadRequest, "UNSUPPORTED_AGENT_TYPE", "agent type has no project command convention")
		return
	}

	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_NAME", "name is required")
		return
	}
	if !slashCommandNamePattern.MatchString(name) {
		writeError(w, r, http.StatusBadRequest, "INVALID_NAME", "name must match [a-zA-Z0-9][a-zA-Z0-9:_-]{0,127}")
		return
	}

	repoIDRaw := strings.TrimSpace(r.URL.Query().Get("repository_id"))
	if repoIDRaw == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY_ID", "repository_id is required")
		return
	}
	repoID, err := uuid.Parse(repoIDRaw)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY_ID", "invalid repository_id")
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())
	repo, err := requireActiveRepo(r.Context(), h.repoStore, orgID, repoID)
	if err != nil {
		h.writeRepoLookupError(w, r, err)
		return
	}
	if h.repoTree == nil {
		writeError(w, r, http.StatusServiceUnavailable, "GITHUB_NOT_CONFIGURED", "GitHub App is not configured")
		return
	}

	branch := strings.TrimSpace(r.URL.Query().Get("branch"))
	if branch == "" {
		branch = repo.DefaultBranch
	}
	parts := strings.SplitN(repo.FullName, "/", 2)
	if len(parts) != 2 {
		logInvalidRepoFullName(r, repo.ID, repo.FullName)
		writeError(w, r, http.StatusInternalServerError, "INVALID_REPOSITORY", "invalid repository full name")
		return
	}

	spec := models.ProjectCommandPaths[agentType]
	path := spec.Dir + "/" + projectCommandPathFromName(name, spec.FileExtension)

	content, err := h.fetchCommandContent(r.Context(), repo, parts[0], parts[1], branch, path)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "GITHUB_API_FAILED", "failed to load command file")
		return
	}

	detail := models.SessionInputCommand{
		Kind:        "command",
		AgentType:   agentType,
		Name:        name,
		Token:       "/" + name,
		Display:     "/" + name,
		Description: extractCommandDescription(content),
		Source:      models.SessionInputCommandSourceProject,
	}
	writeJSON(w, http.StatusOK, SlashCommandDetailResponse{Command: detail})
}

func buildBuiltinSlashCommandGroup(agentType models.AgentType, query string) SlashCommandGroup {
	catalog := models.SlashCommandsForAgent(agentType)
	items := rankSlashCommands(query, catalog, func(cmd models.SlashCommand) models.SessionInputCommand {
		return models.SessionInputCommand{
			Kind:        "command",
			AgentType:   agentType,
			Name:        cmd.Name,
			Token:       cmd.Token(),
			Display:     cmd.Token(),
			Description: cmd.Description,
			Source:      models.SessionInputCommandSourceBuiltin,
		}
	})
	return SlashCommandGroup{
		Source: models.SessionInputCommandSourceBuiltin,
		Label:  models.SlashCommandAgentLabel(agentType),
		Items:  items,
	}
}

func (h *SessionComposerHandler) buildProjectSlashCommandGroup(r *http.Request, agentType models.AgentType, repoIDRaw, branchRaw, query string) (*SlashCommandGroup, error) {
	repoID, err := uuid.Parse(repoIDRaw)
	if err != nil {
		return nil, errInvalidRepoID
	}

	orgID := middleware.OrgIDFromContext(r.Context())
	repo, err := requireActiveRepo(r.Context(), h.repoStore, orgID, repoID)
	if err != nil {
		return nil, err
	}
	if h.repoTree == nil {
		return nil, errGitHubUnconfigured
	}

	parts := strings.SplitN(repo.FullName, "/", 2)
	if len(parts) != 2 {
		return nil, invalidRepoFullNameError{
			repositoryID: repo.ID,
			fullName:     repo.FullName,
		}
	}

	branch := branchRaw
	if branch == "" {
		branch = repo.DefaultBranch
	}

	tree, err := h.repositoryTree(r.Context(), repo, parts[0], parts[1], branch)
	if err != nil {
		return nil, err
	}

	spec := models.ProjectCommandPaths[agentType]
	queryLower := strings.ToLower(query)
	candidates := make([]slashCommandCandidate, 0)
	for _, entry := range tree {
		if entry.Type != models.RepositoryTreeEntryTypeFile {
			continue
		}
		name := spec.CommandNameFromPath(entry.Path)
		if name == "" {
			continue
		}
		if !slashCommandNamePattern.MatchString(name) {
			continue
		}
		nameLower := strings.ToLower(name)
		namePrefix := strings.HasPrefix(nameLower, queryLower)
		nameContains := strings.Contains(nameLower, queryLower)
		if query != "" && !nameContains {
			continue
		}
		candidates = append(candidates, slashCommandCandidate{
			command: models.SessionInputCommand{
				Kind:      "command",
				AgentType: agentType,
				Name:      name,
				Token:     "/" + name,
				Display:   "/" + name,
				Source:    models.SessionInputCommandSourceProject,
			},
			matchBits: []bool{namePrefix, nameContains},
			length:    len(name),
		})
	}

	return &SlashCommandGroup{
		Source: models.SessionInputCommandSourceProject,
		Label:  "Project commands",
		Items:  rankAndLimitSlashCommandCandidates(candidates),
	}, nil
}

func rankSlashCommands(query string, catalog []models.SlashCommand, toCommand func(models.SlashCommand) models.SessionInputCommand) []models.SessionInputCommand {
	queryLower := strings.ToLower(query)
	candidates := make([]slashCommandCandidate, 0, len(catalog))
	for _, cmd := range catalog {
		if cmd.Name == "" {
			continue
		}
		nameLower := strings.ToLower(cmd.Name)
		descLower := strings.ToLower(cmd.Description)
		namePrefix := strings.HasPrefix(nameLower, queryLower)
		nameContains := strings.Contains(nameLower, queryLower)
		descContains := strings.Contains(descLower, queryLower)
		if query != "" && !nameContains && !descContains {
			continue
		}
		candidates = append(candidates, slashCommandCandidate{
			command:   toCommand(cmd),
			matchBits: []bool{namePrefix, nameContains, descContains},
			length:    len(cmd.Name),
		})
	}
	return rankAndLimitSlashCommandCandidates(candidates)
}

// slashCommandCandidate carries everything the shared ranker needs to produce
// a stable ordering. matchBits is the ordered set of "did this candidate
// match dimension N?" flags — true wins for any earlier dimension. length and
// command.Name are the deterministic tiebreakers.
type slashCommandCandidate struct {
	command   models.SessionInputCommand
	matchBits []bool
	length    int
}

// rankAndLimitSlashCommandCandidates sorts by matchBits lexicographically
// (true > false on each dimension), then by length ascending, then by name.
// All callers must populate matchBits with the same number of dimensions
// across a single sort batch — which is naturally true because each call site
// owns its own scoring function.
func rankAndLimitSlashCommandCandidates(candidates []slashCommandCandidate) []models.SessionInputCommand {
	sort.SliceStable(candidates, func(i, j int) bool {
		left := candidates[i]
		right := candidates[j]
		for k := range left.matchBits {
			if k >= len(right.matchBits) {
				break
			}
			if left.matchBits[k] != right.matchBits[k] {
				return left.matchBits[k]
			}
		}
		if left.length != right.length {
			return left.length < right.length
		}
		return left.command.Name < right.command.Name
	})

	limit := len(candidates)
	if limit > sessionComposerSlashCommandLimit {
		limit = sessionComposerSlashCommandLimit
	}
	out := make([]models.SessionInputCommand, 0, limit)
	for _, item := range candidates[:limit] {
		out = append(out, item.command)
	}
	return out
}

func projectCommandPathFromName(name, extension string) string {
	// Names are colon-separated for nested directories (e.g. "auth:review"
	// → "auth/review.md"). This mirrors CommandNameFromPath's encoding.
	rest := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		ch := name[i]
		if ch == ':' {
			rest = append(rest, '/')
		} else {
			rest = append(rest, ch)
		}
	}
	if extension == "" {
		return string(rest)
	}
	return string(rest) + "." + extension
}

func (h *SessionComposerHandler) fetchCommandContent(ctx context.Context, repo models.Repository, owner, name, branch, path string) (string, error) {
	cacheKey := repo.ID.String() + ":" + branch + ":" + path
	now := h.clock()

	h.contentCacheMu.RLock()
	cached, ok := h.commandContents[cacheKey]
	h.contentCacheMu.RUnlock()
	if ok && now.Before(cached.expiresAt) {
		return cached.content, nil
	}

	token, err := h.repoTree.GetInstallationToken(ctx, repo.InstallationID)
	if err != nil {
		return "", err
	}
	content, err := h.repoTree.GetFileContent(ctx, token, owner, name, branch, path)
	if err != nil {
		return "", err
	}

	h.contentCacheMu.Lock()
	h.pruneExpiredCommandContentCacheLocked(now)
	h.commandContents[cacheKey] = cachedSessionComposerCommandContent{
		content:   content,
		expiresAt: now.Add(sessionComposerCommandContentCacheTTL),
	}
	h.contentCacheMu.Unlock()
	return content, nil
}

func (h *SessionComposerHandler) pruneExpiredCommandContentCacheLocked(now time.Time) {
	for key, cached := range h.commandContents {
		if !now.Before(cached.expiresAt) {
			delete(h.commandContents, key)
		}
	}
}

// extractCommandDescription pulls a one-line description from a project
// command file. It first looks for the simplest YAML frontmatter
// `description: value` scalar shape (Claude Code convention); complex YAML
// forms such as block scalars or heavily escaped quoted strings are not
// parsed and will fall back to the body's first non-empty non-frontmatter
// line instead. Returns "" if neither is present.
func extractCommandDescription(content string) string {
	if content == "" {
		return ""
	}

	body := content
	if strings.HasPrefix(content, "---\n") || strings.HasPrefix(content, "---\r\n") {
		// Walk to the closing "---". The frontmatter slice is everything
		// in between; the body is everything after.
		end := strings.Index(content[4:], "\n---")
		if end >= 0 {
			frontmatter := content[4 : 4+end]
			body = strings.TrimLeft(content[4+end+4:], "\r\n")
			for _, line := range strings.Split(frontmatter, "\n") {
				trimmed := strings.TrimSpace(line)
				if !strings.HasPrefix(strings.ToLower(trimmed), "description:") {
					continue
				}
				value := strings.TrimSpace(trimmed[len("description:"):])
				value = strings.Trim(value, `"'`)
				if value != "" {
					return value
				}
			}
		}
	}

	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Strip a leading markdown heading marker (`# `, `## `, ... up to `######`).
		// Use prefix matching rather than TrimLeft on "# ": TrimLeft would also
		// eat hashtag-style leading characters from non-heading content like
		// "#tag content".
		for _, prefix := range []string{"###### ", "##### ", "#### ", "### ", "## ", "# "} {
			if strings.HasPrefix(trimmed, prefix) {
				trimmed = trimmed[len(prefix):]
				break
			}
		}
		if trimmed != "" {
			return strings.TrimSpace(trimmed)
		}
	}
	return ""
}

// errInvalidRepoID, errGitHubUnconfigured, errInvalidRepoFullName are sentinels
// returned by buildProjectSlashCommandGroup so ListSlashCommands can convert
// them to HTTP responses without re-parsing error strings.
var (
	errInvalidRepoID       = fmt.Errorf("invalid repository_id")
	errGitHubUnconfigured  = fmt.Errorf("github not configured")
	errInvalidRepoFullName = fmt.Errorf("invalid repository full name")
)

type invalidRepoFullNameError struct {
	repositoryID uuid.UUID
	fullName     string
}

func (e invalidRepoFullNameError) Error() string {
	return errInvalidRepoFullName.Error()
}

func (e invalidRepoFullNameError) Unwrap() error {
	return errInvalidRepoFullName
}

func logInvalidRepoFullName(r *http.Request, repoID uuid.UUID, fullName string) {
	zerolog.Ctx(r.Context()).Error().
		Str("repository_id", repoID.String()).
		Str("repository_full_name", fullName).
		Msg("invalid repository full name")
}

func (h *SessionComposerHandler) writeRepoLookupError(w http.ResponseWriter, r *http.Request, err error) {
	switch err {
	case errRepoDisconnected:
		writeError(w, r, http.StatusBadRequest, "REPO_DISCONNECTED", "repository is disconnected; reconnect it to load commands")
	case errRepoStoreUnconfigured:
		writeError(w, r, http.StatusInternalServerError, "REPO_STORE_UNCONFIGURED", "repository lookup not configured")
	default:
		writeError(w, r, http.StatusNotFound, "REPOSITORY_NOT_FOUND", "repository not found")
	}
}

func (h *SessionComposerHandler) writeRepoTreeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errInvalidRepoID):
		writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY_ID", "invalid repository_id")
	case errors.Is(err, errGitHubUnconfigured):
		writeError(w, r, http.StatusServiceUnavailable, "GITHUB_NOT_CONFIGURED", "GitHub App is not configured")
	case errors.Is(err, errInvalidRepoFullName):
		var invalidRepoErr invalidRepoFullNameError
		if errors.As(err, &invalidRepoErr) {
			logInvalidRepoFullName(r, invalidRepoErr.repositoryID, invalidRepoErr.fullName)
		}
		writeError(w, r, http.StatusInternalServerError, "INVALID_REPOSITORY", "invalid repository full name")
	case errors.Is(err, errRepoDisconnected):
		writeError(w, r, http.StatusBadRequest, "REPO_DISCONNECTED", "repository is disconnected; reconnect it to load commands")
	case errors.Is(err, errRepoStoreUnconfigured):
		writeError(w, r, http.StatusInternalServerError, "REPO_STORE_UNCONFIGURED", "repository lookup not configured")
	default:
		if strings.Contains(err.Error(), "not found") {
			writeError(w, r, http.StatusNotFound, "REPOSITORY_NOT_FOUND", "repository not found")
			return
		}
		if strings.Contains(err.Error(), "token") {
			writeError(w, r, http.StatusBadGateway, "GITHUB_TOKEN_FAILED", "failed to get GitHub token")
			return
		}
		writeError(w, r, http.StatusBadGateway, "GITHUB_API_FAILED", "failed to load repository tree")
	}
}
