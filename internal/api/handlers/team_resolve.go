package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

// teamLookup is the subset of *db.TeamStore needed to verify a team_id.
type teamLookup interface {
	GetByID(ctx context.Context, orgID, teamID uuid.UUID) (models.Team, error)
}

// resolveTeamID parses and validates a team_id string from a request body,
// optionally verifying that it belongs to orgID via lookup (pass nil to skip
// the lookup — useful in tests or for handlers that don't have a team store).
// Returns (nil, true) when raw is empty.
// Returns (nil, false) after writing an error response on validation failure.
func resolveTeamID(w http.ResponseWriter, r *http.Request, lookup teamLookup, orgID uuid.UUID, raw string) (*uuid.UUID, bool) {
	if raw == "" {
		return nil, true
	}
	parsed, err := uuid.Parse(raw)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_TEAM_ID", "invalid team_id")
		return nil, false
	}
	if lookup != nil {
		if _, err := lookup.GetByID(r.Context(), orgID, parsed); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, r, http.StatusNotFound, "TEAM_NOT_FOUND", "team not found in this organization")
				return nil, false
			}
			writeError(w, r, http.StatusInternalServerError, "TEAM_LOOKUP_FAILED", "failed to verify team", err)
			return nil, false
		}
	}
	return &parsed, true
}
