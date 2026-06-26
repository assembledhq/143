package handlers

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/reviewartifact"
	"github.com/assembledhq/143/internal/services/sandbox"
	"github.com/assembledhq/143/internal/services/workspace"
)

// SessionFileHandler serves file content from a session's workspace.
// The workspace can be either a live Docker sandbox or a persisted
// snapshot tar; the handler dispatches based on the session's state and
// only returns NO_SANDBOX when neither source is available.
type SessionFileHandler struct {
	resolver       *sessionWorkspaceResolver
	artifactReader *reviewartifact.CachedReader
	logger         zerolog.Logger
}

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
	artifactReader *reviewartifact.CachedReader,
	logger zerolog.Logger,
) *SessionFileHandler {
	return &SessionFileHandler{
		resolver:       newSessionWorkspaceResolver(sessionStore, repoStore, fileReader, snapshotCache, logger),
		artifactReader: artifactReader,
		logger:         logger,
	}
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

	session, err := h.resolver.loadSession(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		return nil, false
	}

	reader, _, err := h.resolver.resolveReaderForSession(r.Context(), &session)
	if err != nil {
		if errors.Is(err, workspace.ErrSnapshotMissing) {
			writeError(w, r, http.StatusConflict, "NO_SANDBOX", "session has no active sandbox container or snapshot")
			return nil, false
		}
		h.logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("session_files: refusing to serve with unresolved workspace")
		writeError(w, r, http.StatusInternalServerError, "WORKDIR_UNAVAILABLE", "could not resolve session workspace path")
		return nil, false
	}
	return reader, true
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

	if h.tryReviewArtifactContext(w, r, cleanPath, line, above, below) {
		return
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

func (h *SessionFileHandler) tryReviewArtifactContext(w http.ResponseWriter, r *http.Request, cleanPath string, line, above, below int) bool {
	if h == nil || h.artifactReader == nil || h.resolver == nil || h.resolver.sessionStore == nil {
		return false
	}
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return true
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	ref, err := h.resolver.sessionStore.GetLatestReviewArtifactRef(r.Context(), orgID, sessionID)
	if err != nil || ref.Key == nil || *ref.Key == "" {
		return false
	}
	contextResult, ok, err := h.artifactReader.ReadFileContext(r.Context(), *ref.Key, cleanPath, line, above, below)
	if err != nil {
		h.logger.Warn().Err(err).
			Str("session_id", sessionID.String()).
			Str("review_artifact_key", *ref.Key).
			Str("path", cleanPath).
			Msg("failed to read review artifact context; falling back to workspace reader")
		return false
	}
	if !ok {
		return false
	}
	if contextResult.Lines == nil {
		contextResult.Lines = []sandbox.FileLine{}
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[sandbox.FileContextResult]{Data: contextResult})
	return true
}
