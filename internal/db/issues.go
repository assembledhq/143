import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
}

type IssueFilters struct {
	Status           string
	Source           models.IssueSource
	Severity         string
	Sort             string
	Limit            int
	Cursor           string // legacy cursor support for raw issue IDs
	CursorLastSeenAt *time.Time
	CursorID         *uuid.UUID
}

func (s *IssueStore) ListByOrg(ctx context.Context, orgID uuid.UUID, filters IssueFilters) ([]models.Issue, error) {
		query += ` AND severity = @severity`
		args["severity"] = filters.Severity
	}
	if filters.Sort != "priority" {
		switch {
		case filters.CursorLastSeenAt != nil && filters.CursorID != nil:
			query += ` AND (last_seen_at, id) < (@cursor_last_seen_at, @cursor_id)`
			args["cursor_last_seen_at"] = *filters.CursorLastSeenAt
			args["cursor_id"] = *filters.CursorID
		case filters.CursorID != nil:
			query += ` AND id < @cursor_id`
			args["cursor_id"] = *filters.CursorID
		case filters.Cursor != "":
			cursorID, err := uuid.Parse(filters.Cursor)
			if err == nil {
				query += ` AND id < @cursor_id`
				args["cursor_id"] = cursorID
			}
		}
	}

	if filters.Sort == "priority" {
		query += ` ORDER BY ps.score DESC NULLS LAST, i.id DESC`
	} else {
		query += ` ORDER BY last_seen_at DESC, id DESC`
	}

	limit := filters.Limit
