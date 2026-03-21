package handlers

import (
	"net/http"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/sandbox"
)

// SessionFileHandler serves file content from a session's sandbox container.
type SessionFileHandler struct {
	sessionStore *db.SessionStore
	fileReader   sandbox.FileReader
	logger       zerolog.Logger
}

// NewSessionFileHandler creates a SessionFileHandler.
func NewSessionFileHandler(sessionStore *db.SessionStore, fileReader sandbox.FileReader, logger zerolog.Logger) *SessionFileHandler {
	return &SessionFileHandler{
		sessionStore: sessionStore,
		fileReader:   fileReader,
		logger:       logger,
	}
}

// defaultWorkDir is used when the session doesn't specify a work directory.
const defaultWorkDir = "/workspace"

// getSessionContainer looks up the session and returns its container ID.
// It writes an appropriate error response and returns ("", "", false) on failure.
func (h *SessionFileHandler) getSessionContainer(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return "", "", false
	}

	session, err := h.sessionStore.GetByID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "session not found")
		return "", "", false
	}

	if session.ContainerID == nil || *session.ContainerID == "" {
		writeError(w, http.StatusConflict, "NO_SANDBOX", "session has no active sandbox container")
		return "", "", false
	}

	workDir := defaultWorkDir
	return *session.ContainerID, workDir, true
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
	containerID, workDir, ok := h.getSessionContainer(w, r)
	if !ok {
		return
	}

	dirPath := r.URL.Query().Get("path")
	cleanPath, valid := validatePath(dirPath)
	if !valid {
		writeError(w, http.StatusBadRequest, "INVALID_PATH", "path contains invalid characters")
		return
	}

	entries, err := h.fileReader.ListDir(r.Context(), containerID, workDir, cleanPath)
	if err != nil {
		h.logger.Warn().Err(err).Str("path", cleanPath).Msg("failed to list directory")
		writeError(w, http.StatusNotFound, "DIR_NOT_FOUND", "directory not found or not accessible")
		return
	}

	if entries == nil {
		entries = []sandbox.FileEntry{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[sandbox.FileEntry]{Data: entries})
}

// GetFileContent handles GET /api/v1/sessions/{id}/files/content
func (h *SessionFileHandler) GetFileContent(w http.ResponseWriter, r *http.Request) {
	containerID, workDir, ok := h.getSessionContainer(w, r)
	if !ok {
		return
	}

	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		writeError(w, http.StatusBadRequest, "MISSING_PATH", "path query parameter is required")
		return
	}

	cleanPath, valid := validatePath(filePath)
	if !valid {
		writeError(w, http.StatusBadRequest, "INVALID_PATH", "path contains invalid characters")
		return
	}

	content, err := h.fileReader.ReadFile(r.Context(), containerID, workDir, cleanPath)
	if err != nil {
		h.logger.Warn().Err(err).Str("path", cleanPath).Msg("failed to read file")
		writeError(w, http.StatusNotFound, "FILE_NOT_FOUND", "file not found or not readable")
		return
	}

	lang := inferLanguage(cleanPath)

	writeJSON(w, http.StatusOK, models.SingleResponse[sandbox.FileContent]{
		Data: sandbox.FileContent{
			Path:     cleanPath,
			Content:  content,
			Language: lang,
		},
	})
}

// GetFileContext handles GET /api/v1/sessions/{id}/files/context
func (h *SessionFileHandler) GetFileContext(w http.ResponseWriter, r *http.Request) {
	containerID, workDir, ok := h.getSessionContainer(w, r)
	if !ok {
		return
	}

	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		writeError(w, http.StatusBadRequest, "MISSING_PATH", "path query parameter is required")
		return
	}

	cleanPath, valid := validatePath(filePath)
	if !valid {
		writeError(w, http.StatusBadRequest, "INVALID_PATH", "path contains invalid characters")
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

	lines, err := h.fileReader.ReadFileContext(r.Context(), containerID, workDir, cleanPath, line, above, below)
	if err != nil {
		h.logger.Warn().Err(err).Str("path", cleanPath).Int("line", line).Msg("failed to read file context")
		writeError(w, http.StatusNotFound, "FILE_NOT_FOUND", "file not found or line out of range")
		return
	}

	if lines == nil {
		lines = []sandbox.FileLine{}
	}

	type contextResponse struct {
		Lines []sandbox.FileLine `json:"lines"`
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[contextResponse]{
		Data: contextResponse{Lines: lines},
	})
}
