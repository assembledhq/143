package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// newObsoleteMemoryReinforcementHandler retires legacy approval-based jobs.
// Approval alone does not prove which memory versions influenced an agent.
func newObsoleteMemoryReinforcementHandler(logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			OrgID string `json:"org_id"`
			Repo  string `json:"repo"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return &FatalError{Err: fmt.Errorf("decode obsolete memory reinforcement payload: %w", err)}
		}
		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return &FatalError{Err: fmt.Errorf("parse obsolete memory reinforcement organization: %w", err)}
		}
		if orgID == uuid.Nil {
			return &FatalError{Err: fmt.Errorf("obsolete memory reinforcement requires a nonzero organization")}
		}
		if owner, ok := jobOrgIDFromContext(ctx); ok && owner != orgID {
			return &FatalError{Err: fmt.Errorf("obsolete memory reinforcement organization does not match job owner")}
		}
		if strings.TrimSpace(input.Repo) == "" {
			return &FatalError{Err: fmt.Errorf("obsolete memory reinforcement requires a repository")}
		}
		logger.Info().Str("job_type", jobType).Str("org_id", orgID.String()).Str("repo", input.Repo).Msg("skipping obsolete memory reinforcement job: exact consumed memory identities are unavailable")
		return nil
	}
}
