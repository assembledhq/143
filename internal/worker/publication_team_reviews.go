package worker

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
)

type publicationTeamReviewRequester interface {
	RequestPublicationTeamReviews(context.Context, *models.Session, *models.PullRequest, *models.SessionPublication, []string) error
}

func completePublicationTeamReviews(ctx context.Context, stores *Stores, services *Services, run models.Session, input openPRJobInput, changesetID *uuid.UUID, pr *models.PullRequest) error {
	if len(input.TeamReviewers) == 0 {
		return nil
	}
	if stores == nil || stores.SessionPublications == nil || changesetID == nil || services == nil {
		return errors.New("team review publication dependencies are unavailable")
	}
	publication, err := stores.SessionPublications.GetByChangeset(ctx, run.OrgID, run.ID, *changesetID)
	if err != nil {
		return fmt.Errorf("load finalized team review publication: %w", err)
	}
	frozen, _, err := persistedOpenPRJobInput(publication)
	if err != nil {
		return err
	}
	requester, ok := services.PR.(publicationTeamReviewRequester)
	if !ok {
		return errors.New("pull request team review service is unavailable")
	}
	if err := requester.RequestPublicationTeamReviews(ctx, &run, pr, &publication, frozen.TeamReviewers); err != nil {
		return fmt.Errorf("complete publication team review requests: %w", err)
	}
	return nil
}
