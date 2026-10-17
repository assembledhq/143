package db

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type TeamReviewRequestStore struct{ db DBTX }

func NewTeamReviewRequestStore(db DBTX) *TeamReviewRequestStore {
	return &TeamReviewRequestStore{db: db}
}

const teamReviewRequestColumns = `org_id, publication_id, team, state, created_at, updated_at`

// Begin durably reserves one external request. Existing uncertain (sending)
// and confirmed (requested) rows never authorize another POST. Only a caller
// that has established no delivery occurred may mark a reservation retryable.
func (s *TeamReviewRequestStore) Begin(ctx context.Context, orgID, publicationID uuid.UUID, team string) (models.TeamReviewRequestReceipt, bool, error) {
	if orgID == uuid.Nil || publicationID == uuid.Nil {
		return models.TeamReviewRequestReceipt{}, false, errors.New("team review request requires organization and publication")
	}
	if err := validateCanonicalTeamReviewers([]string{team}); err != nil {
		return models.TeamReviewRequestReceipt{}, false, err
	}
	rows, err := s.db.Query(ctx, `INSERT INTO publication_team_review_requests (org_id, publication_id, team)
	SELECT p.org_id, p.id, $3 FROM session_publications p
	WHERE p.org_id = $1 AND p.id = $2
	ON CONFLICT (org_id, publication_id, team) DO UPDATE
	SET state = 'sending', updated_at = now()
	WHERE publication_team_review_requests.state = 'retryable'
	RETURNING `+teamReviewRequestColumns, orgID, publicationID, team)
	if err != nil {
		return models.TeamReviewRequestReceipt{}, false, err
	}
	receipt, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.TeamReviewRequestReceipt])
	if err == nil {
		return receipt, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return models.TeamReviewRequestReceipt{}, false, err
	}
	// Use a new statement so a concurrent INSERT that won the reservation is
	// visible even when the first statement waited on its unique-key lock.
	rows, err = s.db.Query(ctx, `SELECT `+teamReviewRequestColumns+`
	FROM publication_team_review_requests
	WHERE org_id = $1 AND publication_id = $2 AND team = $3`, orgID, publicationID, team)
	if err != nil {
		return models.TeamReviewRequestReceipt{}, false, err
	}
	receipt, err = pgx.CollectOneRow(rows, pgx.RowToStructByName[models.TeamReviewRequestReceipt])
	return receipt, false, err
}

func (s *TeamReviewRequestStore) MarkRequested(ctx context.Context, orgID, publicationID uuid.UUID, team string) error {
	tag, err := s.db.Exec(ctx, `UPDATE publication_team_review_requests
	SET state = 'requested', updated_at = now()
	WHERE org_id = $1 AND publication_id = $2 AND team = $3 AND state IN ('sending', 'requested')`, orgID, publicationID, team)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

// MarkRetryable must only follow a definitive rejection or a failure known to
// precede the POST. It cannot overwrite a confirmed receipt.
func (s *TeamReviewRequestStore) MarkRetryable(ctx context.Context, orgID, publicationID uuid.UUID, team string) error {
	tag, err := s.db.Exec(ctx, `UPDATE publication_team_review_requests
	SET state = 'retryable', updated_at = now()
	WHERE org_id = $1 AND publication_id = $2 AND team = $3 AND state = 'sending'`, orgID, publicationID, team)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}
