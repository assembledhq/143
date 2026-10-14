package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type PullRequestHealthSyncSnapshot struct {
	ExpectedHeadSHA   string
	ExpectedBaseSHA   string
	HeadSHA           string
	BaseSHA           string
	CheckStateVersion int64
	Checks            []models.PullRequestCheckState
	Summary           models.PullRequestHealthSummary
	CIStatus          models.PullRequestCIStatus
}

func (s *PullRequestStore) ApplyHealthSync(ctx context.Context, orgID, pullRequestID uuid.UUID, snapshot PullRequestHealthSyncSnapshot) (models.PullRequestHealthCurrent, bool, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return models.PullRequestHealthCurrent{}, false, fmt.Errorf("begin pull request health sync: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var headSHA, baseSHA string
	var status models.PullRequestStatus
	if err := tx.QueryRow(ctx, `SELECT COALESCE(head_sha, ''), COALESCE(base_sha, ''), status
		FROM pull_requests
		WHERE org_id = @org_id AND id = @pull_request_id FOR UPDATE`, pgx.NamedArgs{
		"org_id": orgID, "pull_request_id": pullRequestID,
	}).Scan(&headSHA, &baseSHA, &status); err != nil {
		return models.PullRequestHealthCurrent{}, false, fmt.Errorf("lock pull request health sync: %w", err)
	}
	store := NewPullRequestStore(tx)
	current, err := store.GetHealthCurrent(ctx, orgID, pullRequestID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return models.PullRequestHealthCurrent{}, false, err
	}
	if status != models.PullRequestStatusOpen ||
		(headSHA != snapshot.ExpectedHeadSHA && headSHA != snapshot.HeadSHA) ||
		(baseSHA != snapshot.ExpectedBaseSHA && baseSHA != snapshot.BaseSHA) ||
		current.CheckStateVersion > snapshot.CheckStateVersion {
		return current, false, nil
	}
	if err := store.ReconcileCheckStates(ctx, orgID, pullRequestID, snapshot.HeadSHA, snapshot.CheckStateVersion, snapshot.Checks); err != nil {
		return models.PullRequestHealthCurrent{}, false, err
	}
	current, err = store.UpsertHealthSummary(ctx, orgID, pullRequestID, snapshot.HeadSHA, snapshot.BaseSHA, snapshot.Summary, nil, snapshot.CheckStateVersion)
	if err != nil {
		return models.PullRequestHealthCurrent{}, false, err
	}
	if err := store.UpdateCIStatus(ctx, orgID, pullRequestID, snapshot.CIStatus); err != nil {
		return models.PullRequestHealthCurrent{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return models.PullRequestHealthCurrent{}, false, fmt.Errorf("commit pull request health sync: %w", err)
	}
	return current, true, nil
}
