package reviewloop

import (
	"context"
	"errors"
	"fmt"

	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ReconcileRetainedStarts repairs prepared creations and due startup outcomes
// before the legacy stranded-loop path may consider retiring any owner.
func (s *Service) ReconcileRetainedStarts(ctx context.Context, orgID uuid.UUID, limit int) (int, error) {
	if s.startTransactions == nil {
		return 0, db.ErrReviewLoopStartDisabled
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var candidates []db.ReviewLoopStartCandidate
	err := s.startTransactions.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		candidates = nil
		var err error
		candidates, err = db.ListDueReviewLoopStartsInTx(ctx, orgID, tx, limit)
		return err
	})
	if err != nil {
		return 0, err
	}
	var failures error
	processed := 0
	for _, candidate := range candidates {
		_, err := s.RecoverStart(ctx, orgID, candidate.SessionID, candidate.ID)
		if err != nil && !errors.Is(err, ErrReviewLoopStartRefused) {
			failures = errors.Join(failures, fmt.Errorf("recover retained loop %s: %w", candidate.ID, err))
			continue
		}
		processed++
	}
	return processed, failures
}
