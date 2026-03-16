package handlers

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// auditLogReader abstracts the read operations on audit logs so the handler
// can be tested with a simple mock instead of pgxmock.
type auditLogReader interface {
	List(ctx context.Context, orgID uuid.UUID, filters db.AuditLogFilters) ([]models.AuditLog, error)
	GetByID(ctx context.Context, orgID uuid.UUID, id int64) (*models.AuditLog, error)
}

type AuditLogHandler struct {
	store auditLogReader
}

func NewAuditLogHandler(store auditLogReader) *AuditLogHandler {
	return &AuditLogHandler{store: store}
}

// encodeCursor produces an opaque, base64-encoded cursor from the last row's
// created_at and id. Format: "RFC3339Nano,id" → base64.
func encodeAuditCursor(createdAt time.Time, id int64) string {
	raw := fmt.Sprintf("%s,%d", createdAt.UTC().Format(time.RFC3339Nano), id)
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

// decodeAuditCursor is the inverse of encodeAuditCursor.
func decodeAuditCursor(cursor string) (time.Time, int64, error) {
	b, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("invalid cursor encoding: %w", err)
	}
	parts := strings.SplitN(string(b), ",", 2)
	if len(parts) != 2 {
		return time.Time{}, 0, fmt.Errorf("invalid cursor format")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("invalid cursor timestamp: %w", err)
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("invalid cursor id: %w", err)
	}
	return t, id, nil
}

// List handles GET /api/v1/audit-logs.
func (h *AuditLogHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	q := r.URL.Query()
	limit := queryInt(r, "limit", 50)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	filters := db.AuditLogFilters{
		ActorType:    models.AuditActorType(q.Get("actor_type")),
		Action:       models.AuditAction(q.Get("action")),
		ActionPrefix: q.Get("action_prefix"),
		ResourceType: models.AuditResourceType(q.Get("resource_type")),
		ResourceID:   q.Get("resource_id"),
		Limit:        limit,
	}

	if uid := q.Get("user_id"); uid != "" {
		parsed, err := uuid.Parse(uid)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_USER_ID", "invalid user_id parameter")
			return
		}
		filters.UserID = &parsed
	}
	if sid := q.Get("session_id"); sid != "" {
		parsed, err := uuid.Parse(sid)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_SESSION_ID", "invalid session_id parameter")
			return
		}
		filters.SessionID = &parsed
	}
	if pid := q.Get("project_id"); pid != "" {
		parsed, err := uuid.Parse(pid)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_PROJECT_ID", "invalid project_id parameter")
			return
		}
		filters.ProjectID = &parsed
	}
	if since := q.Get("since"); since != "" {
		t, err := time.Parse(time.RFC3339, since)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_SINCE", "invalid since parameter, expected ISO 8601")
			return
		}
		filters.Since = &t
	}
	if until := q.Get("until"); until != "" {
		t, err := time.Parse(time.RFC3339, until)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_UNTIL", "invalid until parameter, expected ISO 8601")
			return
		}
		filters.Until = &t
	}
	if cursor := q.Get("cursor"); cursor != "" {
		t, id, err := decodeAuditCursor(cursor)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "invalid cursor")
			return
		}
		filters.CursorTime = &t
		filters.CursorID = &id
	}

	entries, err := h.store.List(r.Context(), orgID, filters)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "failed to list audit logs")
		return
	}
	if entries == nil {
		entries = []models.AuditLog{}
	}

	var nextCursor string
	if len(entries) == limit {
		last := entries[len(entries)-1]
		nextCursor = encodeAuditCursor(last.CreatedAt, last.ID)
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.AuditLog]{
		Data: entries,
		Meta: models.PaginationMeta{NextCursor: nextCursor},
	})
}

// Get handles GET /api/v1/audit-logs/{id}.
func (h *AuditLogHandler) Get(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid audit log ID")
		return
	}

	entry, err := h.store.GetByID(r.Context(), orgID, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "audit log entry not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "GET_FAILED", "failed to get audit log entry")
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[*models.AuditLog]{Data: entry})
}
