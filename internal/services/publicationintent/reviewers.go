package publicationintent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type AutomationRunStore interface {
	GetByRunID(ctx context.Context, orgID, runID uuid.UUID) (models.AutomationRun, error)
}

type SessionPRIntentStore interface {
	Stage(ctx context.Context, orgID uuid.UUID, intent models.SessionPRIntent) error
	Freeze(ctx context.Context, orgID, sessionID, automationRunID, changesetID uuid.UUID) (models.SessionPRIntent, error)
}

func (c *Coordinator) SetAutomationPRIntentStores(runs AutomationRunStore, intents SessionPRIntentStore) {
	c.automationRuns = runs
	c.prIntents = intents
}

func (c *Coordinator) normalizeTeamReviewers(ctx context.Context, orgID uuid.UUID, session models.Session, requested []string) ([]string, error) {
	if session.RepositoryID == nil || c.repositories == nil {
		return nil, &Error{Code: ErrorSessionNotEligible, Err: errors.New("repository is required for team review requests")}
	}
	repo, err := c.repositories.GetByID(ctx, orgID, *session.RepositoryID)
	if err != nil {
		return nil, &Error{Code: ErrorPublicationFailed, Err: fmt.Errorf("load team review repository: %w", err)}
	}
	teams, err := models.NormalizeTeamReviewers(repo.FullName, requested)
	if err != nil {
		return nil, &Error{Code: ErrorInvalidTeamReviewers, Err: err}
	}
	return teams, nil
}

func (c *Coordinator) reviewerDraftChoice(ctx context.Context, orgID uuid.UUID, payload map[string]any) (bool, error) {
	draft, explicit := payload["draft"].(bool)
	if !explicit {
		if c.organizations == nil {
			return false, &Error{Code: ErrorPublicationFailed, Err: errors.New("organization draft policy is unavailable")}
		}
		org, err := c.organizations.GetByID(ctx, orgID)
		if err != nil {
			return false, &Error{Code: ErrorPublicationFailed, Err: fmt.Errorf("load reviewer publication draft policy: %w", err)}
		}
		settings, err := models.ParseOrgSettings(org.Settings)
		if err != nil {
			return false, &Error{Code: ErrorPublicationFailed, Err: err}
		}
		draft = settings.PRDraftDefault
	}
	return draft, nil
}

func (c *Coordinator) freezeReviewerDraftPolicy(ctx context.Context, orgID uuid.UUID, payload map[string]any) error {
	draft, err := c.reviewerDraftChoice(ctx, orgID, payload)
	if err != nil {
		return err
	}
	if draft {
		return &Error{Code: ErrorInvalidPublicationOptions, Err: errors.New("team_reviewers requires a ready pull request; the configured draft default requires an explicit draft=false")}
	}
	// Freeze the resolved default alongside reviewer intent, so later settings
	// changes cannot turn the accepted ready publication into a draft.
	payload["draft"] = false
	return nil
}

func (c *Coordinator) validateAutomaticReviewerReadiness(ctx context.Context, orgID uuid.UUID, session models.Session) error {
	repo, err := c.repositories.GetByID(ctx, orgID, *session.RepositoryID)
	if err != nil {
		return &Error{Code: ErrorPublicationFailed, Err: fmt.Errorf("load automatic reviewer handoff policy: %w", err)}
	}
	settings, err := models.ParseRepositorySettings(repo.Settings)
	if err != nil {
		// Match the completion coordinator's conservative pre-publish fallback.
		c.logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("falling back to pre-publish policy for automatic reviewer metadata")
	} else if settings.PRHandoffMode == models.PRHandoffModeDraftFirst {
		return nil
	}
	draft, err := c.reviewerDraftChoice(ctx, orgID, nil)
	if err != nil {
		return err
	}
	if draft {
		return &Error{Code: ErrorInvalidPublicationOptions, Err: errors.New("automatic publication is configured to remain a draft; team_reviewers requires a ready pull request or draft_first handoff")}
	}
	return nil
}

// Automation agents register only publication metadata. Their frozen run policy
// remains the sole authority to start publication after execution completes.
func (c *Coordinator) stageAutomationPRIntent(ctx context.Context, orgID uuid.UUID, session models.Session, req RequestPullRequest) (*PublicationIntentResult, error) {
	deny := func(reason string) (*PublicationIntentResult, error) {
		return nil, &Error{Code: ErrorSessionNotEligible, Err: errors.New(reason)}
	}
	if session.Origin != models.SessionOriginAutomation || session.RepositoryID == nil || c.automationRuns == nil || c.prIntents == nil {
		return deny("automatic publication metadata is unavailable for this session")
	}
	if req.Draft != nil || (req.AuthorMode != "" && req.AuthorMode != "auto") || req.MergeWhenReady {
		return nil, &Error{Code: ErrorInvalidPublicationOptions, Err: errors.New("automatic publication metadata accepts team_reviewers only; draft and author policy are owned by the configured completion handoff")}
	}
	hasPublishing := false
	for _, capability := range session.CapabilitySnapshot {
		if capability.ID == models.AgentCapabilityPublishing && capability.AccessLevel.Rank() >= models.AgentCapabilityAccessWrite.Rank() {
			hasPublishing = true
		}
	}
	if !hasPublishing {
		return deny("publishing capability is required to register publication metadata")
	}
	run, err := c.automationRuns.GetByRunID(ctx, orgID, *session.AutomationRunID)
	if err != nil {
		return nil, &Error{Code: ErrorPublicationFailed, Err: fmt.Errorf("load automatic publication authority: %w", err)}
	}
	if run.OrgID != orgID || run.ID != *session.AutomationRunID {
		return deny("automation run does not own this publication metadata")
	}
	var snapshot struct {
		Continuity models.AutomationSessionContinuity `json:"session_continuity"`
	}
	if len(run.ConfigSnapshot) > 0 {
		if err := json.Unmarshal(run.ConfigSnapshot, &snapshot); err != nil {
			return nil, &Error{Code: ErrorPublicationFailed, Err: fmt.Errorf("decode automatic publication authority: %w", err)}
		}
	}
	policy, err := models.AutomationPublishPolicyFromConfigSnapshot(run.ConfigSnapshot)
	if err != nil {
		return nil, &Error{Code: ErrorPublicationFailed, Err: err}
	}
	if run.TargetID != nil || (snapshot.Continuity != "" && snapshot.Continuity != models.AutomationSessionContinuityPerRun) || policy != models.AutomationPublishPolicyPullRequest {
		return deny("publication metadata requires a per-run automation with frozen pull_request policy")
	}
	changeset, err := c.changesets.GetPrimary(ctx, orgID, session.ID)
	if err != nil {
		return nil, &Error{Code: ErrorWorkspaceNotReady, Err: fmt.Errorf("load automatic publication changeset: %w", err)}
	}
	if req.ChangesetID != nil && *req.ChangesetID != changeset.ID {
		return nil, &Error{Code: ErrorInvalidPublicationOptions, Err: errors.New("automatic publication metadata must select the session's primary changeset")}
	}
	publication, publicationErr := c.publications.GetByChangeset(ctx, orgID, session.ID, changeset.ID)
	if publicationErr == nil {
		result := ExistingPublicationResult(publication)
		if len(req.TeamReviewers) > 0 {
			reason := "publication metadata is already frozen; the existing publication retains its original team reviewers"
			result.Reason = &reason
		}
		return result, nil
	}
	if !errors.Is(publicationErr, pgx.ErrNoRows) {
		return nil, &Error{Code: ErrorPublicationFailed, Err: publicationErr}
	}
	if len(req.TeamReviewers) > 0 {
		teams, err := c.normalizeTeamReviewers(ctx, orgID, session, req.TeamReviewers)
		if err != nil {
			return nil, err
		}
		if err := c.validateAutomaticReviewerReadiness(ctx, orgID, session); err != nil {
			return nil, err
		}
		if err := c.prIntents.Stage(ctx, orgID, models.SessionPRIntent{
			OrgID: orgID, SessionID: session.ID, AutomationRunID: run.ID, ChangesetID: changeset.ID, TeamReviewers: teams,
		}); err != nil {
			return nil, &Error{Code: ErrorPublicationFailed, Err: fmt.Errorf("stage automatic publication metadata: %w", err)}
		}
	}
	return &PublicationIntentResult{Status: ResultAwaitingAutomaticPublication, SessionID: session.ID}, nil
}
