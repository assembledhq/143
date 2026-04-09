package handlers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
)

func encodeIssueCursor(lastSeenAt time.Time, id uuid.UUID) string {
	return encodeCursor(lastSeenAt, id.String())
}

func decodeIssueCursor(cursor string) (time.Time, uuid.UUID, error) {
	t, rawID, err := decodeCursor(cursor)
	if err != nil {
		return time.Time{}, uuid.UUID{}, err
	}
	id, err := uuid.Parse(rawID)
	if err != nil {
		return time.Time{}, uuid.UUID{}, fmt.Errorf("invalid cursor id: %w", err)
	}
	return t, id, nil
}

type IssueHandler struct {
	issueStore *db.IssueStore
}
		Limit:    limit,
		Cursor:   r.URL.Query().Get("cursor"),
	}
	if filters.Cursor != "" && filters.Sort != "priority" {
		lastSeenAt, cursorID, err := decodeIssueCursor(filters.Cursor)
		if err == nil {
			filters.CursorLastSeenAt = &lastSeenAt
			filters.CursorID = &cursorID
		} else {
			legacyCursorID, legacyErr := uuid.Parse(filters.Cursor)
			if legacyErr != nil {
				writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "invalid cursor")
				return
			}
			filters.CursorID = &legacyCursorID
		}
	}

	issues, err := h.issueStore.ListByOrg(r.Context(), orgID, filters)
	if err != nil {
	}

	var nextCursor string
	if filters.Sort != "priority" && len(issues) > 0 && len(issues) == filters.Limit {
		last := issues[len(issues)-1]
		nextCursor = encodeIssueCursor(last.LastSeenAt, last.ID)
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.Issue]{
