package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/sandbox"
	"github.com/assembledhq/143/internal/services/workspace"
)

// SessionFileHandler serves file content from a session's workspace.
// The workspace can be either a live Docker sandbox or a persisted
// snapshot tar; the handler dispatches based on the session's state and
// only returns NO_SANDBOX when neither source is available.
type SessionFileHandler struct {
	sessionStore  *db.SessionStore
	repoStore     *db.RepositoryStore // optional; used to resolve per-session WorkDir
	fileReader    sandbox.FileReader
	snapshotCache *workspace.SnapshotCache // optional; nil disables snapshot fallback
	logger        zerolog.Logger

	// repoWorkDirCache memoizes the resolved sandbox WorkDir for a given
	// repository ID so a reviewer scrolling through a diff and clicking
	// "Show more" repeatedly does not hit the DB on every request. The
	// resolution depends only on repo.FullName + the sandbox defaults,
	// both of which are effectively immutable for a process lifetime.
	// On rename, the stale entry expires at the next process restart —
	// the same drift window already documented for snapshot prefixes.
	repoWorkDirCache sync.Map // map[uuid.UUID]string
}

// errRepoLookupFailed marks a repo lookup error so resolveReader can map
// it to 500 SNAPSHOT_UNAVAILABLE rather than degrading to /workspace
// (which would produce misleading FILE_NOT_FOUND for repo-attached
// sessions whose snapshot is rooted under home/<user>/<slug>).
var errRepoLookupFailed = errors.New("session_files: repo lookup for sandbox WorkDir failed")

// NewSessionFileHandler creates a SessionFileHandler. snapshotCache may be
// nil — that disables the snapshot fallback path and the handler keeps
// the original behavior of returning NO_SANDBOX when no live container is
// attached. repoStore may also be nil; in that case the handler falls
// back to the default workspace path (no per-session WorkDir lookup),
// matching how preview handler degrades when the repo store is absent.
func NewSessionFileHandler(
	sessionStore *db.SessionStore,
	repoStore *db.RepositoryStore,
	fileReader sandbox.FileReader,
	snapshotCache *workspace.SnapshotCache,
	logger zerolog.Logger,
) *SessionFileHandler {
	return &SessionFileHandler{
		sessionStore:  sessionStore,
		repoStore:     repoStore,
		fileReader:    fileReader,
		snapshotCache: snapshotCache,
		logger:        logger,
	}
}

// resolveSandboxWorkDir returns the absolute in-container path where the
// session's workspace files live. Mirrors PreviewHandler.resolveSandboxWorkDir
// (and the orchestrator's `HomeDir + "/" + slug` rule) so file reads
// resolve to the same place the agent uses.
//
// For sessions WITHOUT an attached repo (or when h.repoStore is nil for
// degraded test setups) this returns the default /workspace path. For
// repo-attached sessions, it looks up the repo and resolves the slug.
// A repo lookup failure on a repo-attached session returns an error so
// the handler can return 500 — the previous behavior of silently
// falling back to /workspace produced misleading FILE_NOT_FOUND
// responses for snapshot reads (snapshots are tarred at home/<user>/<slug>,
// not /workspace) and incorrect live-container reads (orchestrator now
// places the workspace under home/<user>/<slug>).
//
// The returned path drives BOTH:
//   - the live container reader's exec workdir
//   - the snapshot reader's tar prefix (with the leading "/" stripped)
//
// so the two readers always look in the same place.
//
// Snapshot prefix drift: the snapshot is tarred with the slug at capture
// time. If repo.FullName changes between capture and review (a rename or
// transfer), this function returns a workdir derived from the *current*
// FullName, which won't match the in-tar prefix and the snapshot reader
// will surface FILE_NOT_FOUND. This is a known minor failure mode that
// will be addressed by storing the in-tar prefix on the session row when
// Phase 1 (immutable diff/snapshot provenance — see
// docs/design/55-code-diff-context-navigation.md) lands; until then renames
// are rare enough that the fallback to disabled-expander UI is acceptable.
func (h *SessionFileHandler) resolveSandboxWorkDir(ctx context.Context, session *models.Session) (string, error) {
	defaults := agent.DefaultSandboxConfig()
	if session.RepositoryID == nil || h.repoStore == nil {
		return defaults.WorkDir, nil
	}
	repoID := *session.RepositoryID
	if cached, ok := h.repoWorkDirCache.Load(repoID); ok {
		return cached.(string), nil
	}
	repo, err := h.repoStore.GetByID(ctx, session.OrgID, repoID)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errRepoLookupFailed, err)
	}
	slug := agent.SlugForRepo(repo.FullName)
	if slug == "" {
		// FullName produced no slug — fall back rather than fail. This
		// matches the previous behavior for unparseable repo names; the
		// resulting /workspace path may not serve correct content, but
		// it is the same path the orchestrator would have used.
		return defaults.WorkDir, nil
	}
	resolved := defaults.HomeDir + "/" + slug
	h.repoWorkDirCache.Store(repoID, resolved)
	return resolved, nil
}

// resolveReader picks a workspace.Reader for the session: live container
// when a sandbox is attached, snapshot tar when one was persisted, and
// 409 NO_SANDBOX when neither is available. On error, an HTTP response is
// already written and the second return is false.
func (h *SessionFileHandler) resolveReader(w http.ResponseWriter, r *http.Request) (workspace.Reader, bool) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return nil, false
	}

	session, err := h.sessionStore.GetByID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		return nil, false
	}

	workDir, err := h.resolveSandboxWorkDir(r.Context(), &session)
	if err != nil {
		// Repo-attached sessions that can't resolve their slug cannot serve
		// correct content from either reader — surface a 500 rather than
		// degrading to /workspace and producing misleading FILE_NOT_FOUND.
		h.logger.Warn().Err(err).
			Str("session_id", sessionID.String()).
			Str("repository_id", session.RepositoryID.String()).
			Msg("session_files: refusing to serve with unresolved WorkDir")
		writeError(w, r, http.StatusInternalServerError, "WORKDIR_UNAVAILABLE", "could not resolve session workspace path")
		return nil, false
	}

	if session.ContainerID != nil && *session.ContainerID != "" {
		return workspace.NewLiveContainerReader(h.fileReader, *session.ContainerID, workDir), true
	}

	if h.snapshotCache != nil && session.SnapshotKey != nil && *session.SnapshotKey != "" {
		// The snapshot tar holds entries rooted at the in-container WorkDir
		// without the leading slash (DockerProvider.Snapshot calls
		// `tar -C / <workDirRel>`), so strip the slash here so the cache
		// can join it under the extraction directory verbatim.
		workspaceRel := strings.TrimPrefix(workDir, "/")
		h.logger.Debug().
			Str("session_id", sessionID.String()).
			Str("snapshot_key", *session.SnapshotKey).
			Str("workspace_rel", workspaceRel).
			Msg("session_files: serving from snapshot reader")
		return workspace.NewSnapshotReader(h.snapshotCache, *session.SnapshotKey, workspaceRel), true
	}

	writeError(w, r, http.StatusConflict, "NO_SANDBOX", "session has no active sandbox container or snapshot")
	return nil, false
}

// writeWorkspaceError maps a workspace.Reader error to the appropriate
// HTTP response and returns true if it handled the error. Centralizes
// the ErrSnapshotMissing/Unreadable/Unavailable mapping so ListFiles,
// GetFileContent, and GetFileContext don't each repeat the same
// errors.Is ladder. fallback404Code is the error code to emit for
// generic, non-sentinel errors (e.g. the underlying file is missing or
// unreadable inside an otherwise-healthy snapshot); fallback404Msg is
// the human-readable message paired with that code.
func (h *SessionFileHandler) writeWorkspaceError(w http.ResponseWriter, r *http.Request, err error, op string, logFields map[string]interface{}, fallback404Code, fallback404Msg string) bool {
	if err == nil {
		return false
	}
	logEvent := func(level zerolog.Level, msg string) {
		ev := h.logger.WithLevel(level).Err(err).Str("op", op)
		for k, v := range logFields {
			ev = ev.Interface(k, v)
		}
		ev.Msg(msg)
	}
	switch {
	case errors.Is(err, workspace.ErrSnapshotMissing):
		writeError(w, r, http.StatusConflict, "NO_SANDBOX", "session snapshot is no longer available")
	case errors.Is(err, workspace.ErrSnapshotUnreadable):
		logEvent(zerolog.ErrorLevel, "snapshot exists but cannot be read")
		writeError(w, r, http.StatusInternalServerError, "SNAPSHOT_UNREADABLE", "session snapshot exists but cannot be read")
	case errors.Is(err, workspace.ErrSnapshotUnavailable):
		logEvent(zerolog.ErrorLevel, "snapshot could not be loaded")
		writeError(w, r, http.StatusInternalServerError, "SNAPSHOT_UNAVAILABLE", "session snapshot could not be loaded")
	default:
		logEvent(zerolog.WarnLevel, op+" failed")
		writeError(w, r, http.StatusNotFound, fallback404Code, fallback404Msg)
	}
	return true
}

// validatePath checks that a path is safe (no traversal attacks) and normalizes it.
func validatePath(rawPath string) (string, bool) {
	if rawPath == "" || rawPath == "." || rawPath == "/" {
		return ".", true
	}

	// Clean the path to resolve ".." and normalize separators.
	cleaned := filepath.Clean(rawPath)
	cleaned = strings.TrimPrefix(cleaned, "/")

	// Reject paths that resolve outside the workspace root.
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}

	return cleaned, true
}

// inferLanguage returns a language identifier from a file extension.
func inferLanguage(filePath string) string {
	ext := strings.TrimPrefix(filepath.Ext(filePath), ".")
	switch strings.ToLower(ext) {
	case "ts":
		return "typescript"
	case "tsx":
		return "tsx"
	case "js":
		return "javascript"
	case "jsx":
		return "jsx"
	case "go":
		return "go"
	case "py":
		return "python"
	case "rb":
		return "ruby"
	case "rs":
		return "rust"
	case "java":
		return "java"
	case "kt":
		return "kotlin"
	case "swift":
		return "swift"
	case "c", "h":
		return "c"
	case "cpp", "hpp", "cc":
		return "cpp"
	case "cs":
		return "csharp"
	case "css":
		return "css"
	case "scss":
		return "scss"
	case "html":
		return "html"
	case "json":
		return "json"
	case "yaml", "yml":
		return "yaml"
	case "toml":
		return "toml"
	case "xml":
		return "xml"
	case "md":
		return "markdown"
	case "sql":
		return "sql"
	case "sh", "bash", "zsh":
		return "bash"
	case "dockerfile":
		return "dockerfile"
	case "graphql":
		return "graphql"
	default:
		return "text"
	}
}

// ListFiles handles GET /api/v1/sessions/{id}/files
func (h *SessionFileHandler) ListFiles(w http.ResponseWriter, r *http.Request) {
	dirPath := r.URL.Query().Get("path")
	cleanPath, valid := validatePath(dirPath)
	if !valid {
		writeError(w, r, http.StatusBadRequest, "INVALID_PATH", "path contains invalid characters")
		return
	}

	reader, ok := h.resolveReader(w, r)
	if !ok {
		return
	}

	entries, err := reader.ListDir(r.Context(), cleanPath)
	if h.writeWorkspaceError(w, r, err, "list_dir",
		map[string]interface{}{"path": cleanPath},
		"DIR_NOT_FOUND", "directory not found or not accessible") {
		return
	}

	if entries == nil {
		entries = []sandbox.FileEntry{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[sandbox.FileEntry]{Data: entries})
}

// GetFileContent handles GET /api/v1/sessions/{id}/files/content
func (h *SessionFileHandler) GetFileContent(w http.ResponseWriter, r *http.Request) {
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_PATH", "path query parameter is required")
		return
	}

	cleanPath, valid := validatePath(filePath)
	if !valid {
		writeError(w, r, http.StatusBadRequest, "INVALID_PATH", "path contains invalid characters")
		return
	}

	reader, ok := h.resolveReader(w, r)
	if !ok {
		return
	}

	content, truncated, err := reader.ReadFile(r.Context(), cleanPath)
	if h.writeWorkspaceError(w, r, err, "read_file",
		map[string]interface{}{"path": cleanPath},
		"FILE_NOT_FOUND", "file not found or not readable") {
		return
	}

	lang := inferLanguage(cleanPath)

	writeJSON(w, http.StatusOK, models.SingleResponse[sandbox.FileContent]{
		Data: sandbox.FileContent{
			Path:      cleanPath,
			Content:   content,
			Language:  lang,
			Truncated: truncated,
		},
	})
}

// GetFileContext handles GET /api/v1/sessions/{id}/files/context
func (h *SessionFileHandler) GetFileContext(w http.ResponseWriter, r *http.Request) {
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_PATH", "path query parameter is required")
		return
	}

	cleanPath, valid := validatePath(filePath)
	if !valid {
		writeError(w, r, http.StatusBadRequest, "INVALID_PATH", "path contains invalid characters")
		return
	}

	line := queryInt(r, "line", 1)
	above := queryInt(r, "above", 10)
	below := queryInt(r, "below", 10)

	// Cap context range to prevent excessive reads.
	if above > 100 {
		above = 100
	}
	if below > 100 {
		below = 100
	}

	reader, ok := h.resolveReader(w, r)
	if !ok {
		return
	}

	contextResult, err := reader.ReadFileContext(r.Context(), cleanPath, line, above, below)
	if h.writeWorkspaceError(w, r, err, "read_file_context",
		map[string]interface{}{"path": cleanPath, "line": line},
		"FILE_NOT_FOUND", "file not found or line out of range") {
		return
	}

	if contextResult.Lines == nil {
		contextResult.Lines = []sandbox.FileLine{}
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[sandbox.FileContextResult]{Data: contextResult})
}
