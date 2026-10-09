package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// newObsoleteExperimentHandler consumes legacy jobs from the removed producer.
// Experiment evaluation has never had an implementation or persisted model.
func newObsoleteExperimentHandler(logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, jobType string, payload json.RawMessage) error {
		var input struct {
			OrgID         string `json:"org_id"`
			PullRequestID string `json:"pull_request_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return &FatalError{Err: fmt.Errorf("decode obsolete experiment payload: %w", err)}
		}
		orgID, err := parseOrgID(input.OrgID, ctx)
		if err != nil {
			return &FatalError{Err: fmt.Errorf("parse obsolete experiment organization: %w", err)}
		}
		if orgID == uuid.Nil {
			return &FatalError{Err: fmt.Errorf("obsolete experiment job requires a nonzero organization")}
		}
		if owner, ok := jobOrgIDFromContext(ctx); ok && owner != orgID {
			return &FatalError{Err: fmt.Errorf("obsolete experiment organization does not match job owner")}
		}
		prID, err := uuid.Parse(input.PullRequestID)
		if err != nil {
			return &FatalError{Err: fmt.Errorf("parse obsolete experiment pull request: %w", err)}
		}
		if prID == uuid.Nil {
			return &FatalError{Err: fmt.Errorf("obsolete experiment job requires a nonzero pull request")}
		}
		logger.Info().Str("job_type", jobType).Str("org_id", orgID.String()).Str("pull_request_id", prID.String()).Msg("skipping obsolete experiment evaluation job: feature is not implemented")
		return nil
	}
}
