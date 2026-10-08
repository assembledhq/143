package codereview

import (
	"errors"
	"fmt"
	"net/http"

	ghservice "github.com/assembledhq/143/internal/services/github"
)

// ErrReviewPublicationRejected means GitHub rejected this attempt before the
// assessment's summary or approval was published. Earlier inline comments may
// exist and retain their dedupe markers. This says nothing about prior attempts.
var ErrReviewPublicationRejected = errors.New("review publication rejected before summary")

func reviewPublicationRejection(err error) error {
	var apiErr *ghservice.GitHubAPIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnprocessableEntity {
		return fmt.Errorf("%w: %w", ErrReviewPublicationRejected, err)
	}
	return err
}
