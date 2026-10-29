package github

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	automationevents "github.com/assembledhq/143/internal/services/automations"
)

func (s *PRService) triggerGitHubCheckRunAutomations(ctx context.Context, req automationevents.GitHubEventTriggerRequest, ownerOrgID *uuid.UUID, githubRepoID int64) error {
	if s.automationEventTriggers == nil || s.repos == nil {
		return nil
	}
	repo, err := s.repositoryFromWebhookRepo(ctx, ownerOrgID, githubRepoID, req.Repository)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("resolve repository for github check automation: %w", err)
		}
		return nil
	}
	req.OrgID, req.RepositoryID = repo.OrgID, repo.ID
	err = s.automationEventTriggers.TriggerGitHubEvent(ctx, req)
	if errors.Is(err, automationevents.ErrCheckRunTrigger) {
		return fmt.Errorf("github check automation: %w", err)
	}
	if err != nil {
		s.logger.Warn().Err(err).Str("repo", req.Repository).Msg("failed to trigger github check automation")
	}
	return nil
}
