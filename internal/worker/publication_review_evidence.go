package worker

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	reviewloopsvc "github.com/assembledhq/143/internal/services/reviewloop"
)

type publicationReviewEvidenceRefresher struct {
	stores *Stores
	pr     prCreator
}

func NewPublicationReviewEvidenceRefresher(stores *Stores, pr prCreator) reviewloopsvc.PublicationEvidenceRefresher {
	return &publicationReviewEvidenceRefresher{stores: stores, pr: pr}
}

func (r *publicationReviewEvidenceRefresher) RefreshPublicationEvidence(
	ctx context.Context,
	loop models.SessionReviewLoop,
) (int64, string, error) {
	if r == nil || r.stores == nil || r.stores.Sessions == nil || r.stores.SessionChangesets == nil ||
		r.stores.SessionPublications == nil || r.pr == nil || loop.ChangesetID == nil {
		return 0, "", errors.New("publication evidence refresh dependencies are unavailable")
	}
	session, err := r.stores.Sessions.GetByID(ctx, loop.OrgID, loop.SessionID)
	if err != nil {
		return 0, "", fmt.Errorf("load publication review session: %w", err)
	}
	changeset, err := r.stores.SessionChangesets.GetByID(ctx, loop.OrgID, loop.SessionID, *loop.ChangesetID)
	if err != nil {
		return 0, "", fmt.Errorf("load publication review changeset: %w", err)
	}
	publication, err := r.stores.SessionPublications.GetByChangeset(ctx, loop.OrgID, loop.SessionID, *loop.ChangesetID)
	if err != nil {
		return 0, "", fmt.Errorf("load publication review intent: %w", err)
	}

	var headSHA string
	if publication.HandoffMode == models.PRHandoffModeDraftFirst {
		pr, pushErr := r.pr.PushChangesToPR(ctx, &session, ghservice.CreatePRParams{ChangesetID: loop.ChangesetID})
		if errors.Is(pushErr, ghservice.ErrNoChanges) {
			headSHA = stringValue(changeset.HeadSHA)
			pushErr = nil
		}
		if pushErr != nil {
			return 0, "", fmt.Errorf("push draft review fixes: %w", pushErr)
		}
		if headSHA == "" && (pr == nil || pr.HeadSHA == nil) {
			return 0, "", errors.New("draft review fix push returned no head SHA")
		}
		if headSHA == "" {
			headSHA = *pr.HeadSHA
		}
	} else {
		branch, branchErr := r.pr.CreateBranch(ctx, &session, ghservice.CreatePRParams{ChangesetID: loop.ChangesetID})
		if errors.Is(branchErr, ghservice.ErrNoChanges) {
			headSHA = stringValue(changeset.HeadSHA)
			branchErr = nil
		} else if branchErr != nil {
			return 0, "", fmt.Errorf("push pre-publish review fixes: %w", branchErr)
		} else if branch == nil {
			return 0, "", errors.New("review fix branch push returned no result")
		} else {
			headSHA = branch.HeadSHA
		}
	}
	if headSHA == "" {
		return 0, "", errors.New("review fix push returned an empty head SHA")
	}
	r.pr.WaitForPostPRSnapshotUploads()
	fresh, err := r.stores.Sessions.GetByID(ctx, loop.OrgID, loop.SessionID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, "", fmt.Errorf("reload publication review revision: %w", err)
	}
	if err == nil {
		session = fresh
	}
	_ = changeset // Scope validation above is intentional even for primary snapshots.
	return session.WorkspaceRevision, headSHA, nil
}
