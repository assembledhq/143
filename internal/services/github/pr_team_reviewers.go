package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/github/identity"
)

var ErrTeamReviewRequestUncertain = errors.New("team review request outcome is uncertain; automatic resend is disabled")

type TeamReviewRequestStore interface {
	Begin(ctx context.Context, orgID, publicationID uuid.UUID, team string) (models.TeamReviewRequestReceipt, bool, error)
	MarkRequested(ctx context.Context, orgID, publicationID uuid.UUID, team string) error
	MarkRetryable(ctx context.Context, orgID, publicationID uuid.UUID, team string) error
}

func (s *PRService) SetTeamReviewRequestStore(store TeamReviewRequestStore) {
	s.teamReviewRequests = store
}

// RequestPublicationTeamReviews runs only after publication/review finalization.
// The durable per-publication receipts survive GitHub removing a team from its
// pending list after a reviewer completes their review.
func (s *PRService) RequestPublicationTeamReviews(ctx context.Context, run *models.Session, pr *models.PullRequest, publication *models.SessionPublication, requested []string) error {
	if len(requested) == 0 {
		return nil
	}
	if run == nil || pr == nil || publication == nil || s.teamReviewRequests == nil || s.repos == nil {
		return errors.New("team review request dependencies are unavailable")
	}
	if pr.OrgID != run.OrgID || publication.OrgID != run.OrgID || publication.SessionID != run.ID || pr.SessionID == nil || *pr.SessionID != run.ID ||
		pr.ChangesetID == nil || *pr.ChangesetID != publication.ChangesetID || publication.GitHubPRNumber == nil || *publication.GitHubPRNumber != pr.GitHubPRNumber {
		return errors.New("team review request publication scope does not match pull request")
	}
	if publication.State != models.SessionPublicationStateCompleted ||
		(publication.ReviewGateState != models.SessionPublicationReviewGatePassed && publication.ReviewGateState != models.SessionPublicationReviewGateNotRequired) {
		return errors.New("team review requests must wait for publication and review finalization")
	}
	teams, err := models.NormalizeTeamReviewers(pr.GitHubRepo, requested)
	if err != nil {
		return err
	}
	owner, repoName := splitRepo(pr.GitHubRepo)
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/requested_reviewers", owner, repoName, pr.GitHubPRNumber)
	var token string
	for _, team := range teams {
		receipt, claimed, err := s.teamReviewRequests.Begin(ctx, publication.OrgID, publication.ID, team)
		if err != nil {
			return fmt.Errorf("reserve team review request: %w", err)
		}
		if receipt.State == models.TeamReviewRequestRequested {
			continue
		}
		if token == "" {
			resolution, installationID, resolveErr := s.originalPublicationPublisher(ctx, run, pr, publication)
			if resolveErr != nil {
				if claimed {
					resolveErr = errors.Join(resolveErr, s.teamReviewRequests.MarkRetryable(ctx, publication.OrgID, publication.ID, team))
				}
				return resolveErr
			}
			token = resolution.Token
			ctx = withGitHubResolutionContext(ctx, resolution, installationID, "pr_team_review_request")
		}
		if err := s.requestReservedTeamReview(ctx, token, path, publication.OrgID, publication.ID, team, claimed); err != nil {
			return err
		}
	}
	return nil
}

func (s *PRService) originalPublicationPublisher(ctx context.Context, run *models.Session, pr *models.PullRequest, publication *models.SessionPublication) (*identity.Resolution, int64, error) {
	repo, err := s.repos.GetByFullNameAnyStatus(ctx, run.OrgID, pr.GitHubRepo)
	if err != nil {
		return nil, 0, fmt.Errorf("load team review repository: %w", err)
	}
	publisher := *run
	if publication.InitiatedByUserID != nil {
		publisher.TriggeredByUserID = publication.InitiatedByUserID
	}
	mode := string(pr.AuthoredBy)
	if mode != "app" && mode != "user" {
		return nil, 0, errors.New("original pull request publisher identity is unavailable")
	}
	resolution, err := s.resolveToken(ctx, &publisher, &repo, models.OrgSettings{}, mode)
	if err != nil {
		return nil, 0, fmt.Errorf("resolve original pull request publisher: %w", err)
	}
	return resolution, repo.InstallationID, nil
}

func (s *PRService) requestPublicationTeamReview(ctx context.Context, token, path string, orgID, publicationID uuid.UUID, team string) error {
	receipt, claimed, err := s.teamReviewRequests.Begin(ctx, orgID, publicationID, team)
	if err != nil {
		return fmt.Errorf("reserve team review request: %w", err)
	}
	if receipt.State == models.TeamReviewRequestRequested {
		return nil
	}
	return s.requestReservedTeamReview(ctx, token, path, orgID, publicationID, team, claimed)
}

func (s *PRService) requestReservedTeamReview(ctx context.Context, token, path string, orgID, publicationID uuid.UUID, team string, claimed bool) error {
	_, slug, _ := strings.Cut(team, "/")
	pending, err := s.pendingTeamReview(ctx, token, path, slug)
	if err != nil {
		if claimed {
			// No mutation was attempted, so a later attempt may safely retry.
			err = errors.Join(err, s.teamReviewRequests.MarkRetryable(ctx, orgID, publicationID, team))
		}
		return err
	}
	if pending {
		return s.teamReviewRequests.MarkRequested(ctx, orgID, publicationID, team)
	}
	if !claimed {
		return fmt.Errorf("%w: %s", ErrTeamReviewRequestUncertain, team)
	}
	_, err = s.doGitHubRequest(ctx, token, http.MethodPost, path, map[string]any{"team_reviewers": []string{slug}})
	if err == nil {
		return s.teamReviewRequests.MarkRequested(ctx, orgID, publicationID, team)
	}
	var apiErr *GitHubAPIError
	if errors.As(err, &apiErr) && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 {
		// An explicit rejection did not request a review. Keep retry eligibility
		// durable; transport errors and server failures remain uncertain.
		return errors.Join(fmt.Errorf("request team review: %w", err), s.teamReviewRequests.MarkRetryable(ctx, orgID, publicationID, team))
	}
	// A timeout, interrupted response, or server error may follow a successful
	// write. Absence cannot prove failure: the team might already have reviewed.
	pending, reconcileErr := s.pendingTeamReview(ctx, token, path, slug)
	if reconcileErr == nil && pending {
		return s.teamReviewRequests.MarkRequested(ctx, orgID, publicationID, team)
	}
	return errors.Join(fmt.Errorf("%w: %s", ErrTeamReviewRequestUncertain, team), err, reconcileErr)
}

func (s *PRService) pendingTeamReview(ctx context.Context, token, path, slug string) (bool, error) {
	body, err := s.doGitHubRequest(ctx, token, http.MethodGet, path, nil)
	if err != nil {
		return false, fmt.Errorf("read pending team reviews: %w", err)
	}
	var pending struct {
		Teams []struct {
			Slug string `json:"slug"`
		} `json:"teams"`
	}
	if err := json.Unmarshal(body, &pending); err != nil {
		return false, fmt.Errorf("decode pending team reviews: %w", err)
	}
	for _, team := range pending.Teams {
		if strings.EqualFold(team.Slug, slug) {
			return true, nil
		}
	}
	return false, nil
}
