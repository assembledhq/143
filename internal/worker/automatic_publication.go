package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/publicationintent"
)

type automaticPublicationDependencies struct {
	session     func(context.Context, uuid.UUID, uuid.UUID) (models.Session, error)
	primary     func(context.Context, uuid.UUID, uuid.UUID) (models.SessionChangeset, error)
	publication func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.SessionPublication, error)
	pullRequest func(context.Context, uuid.UUID, uuid.UUID) (models.PullRequest, error)
	run         func(context.Context, uuid.UUID, uuid.UUID) (models.AutomationRun, error)
	policy      func(context.Context, uuid.UUID, uuid.UUID) (models.AutomationPublishPolicy, error)
	snapshot    func(context.Context, uuid.UUID, uuid.UUID, int) (models.SessionTurnIssueSnapshot, error)
	quiescent   func(context.Context, models.Session) error
	coordinator publicationintent.PublicationIntentCoordinator
}

func newAutomaticPublicationHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	deps := automaticPublicationDependencies{}
	if stores != nil && stores.Sessions != nil && stores.SessionChangesets != nil &&
		stores.SessionPublications != nil && stores.PullRequests != nil && stores.AutomationRuns != nil &&
		services != nil && services.AutomationRuns != nil && services.PublicationIntents != nil {
		deps.session = stores.Sessions.GetByID
		deps.primary = stores.SessionChangesets.GetPrimary
		deps.publication = stores.SessionPublications.GetByChangeset
		deps.pullRequest = stores.PullRequests.GetPrimaryBySessionID
		deps.run = stores.AutomationRuns.GetByRunID
		deps.policy = services.AutomationRuns.AutomaticPublishPolicy
		deps.coordinator = services.PublicationIntents
		deps.quiescent = func(ctx context.Context, session models.Session) error {
			return ensureSessionSnapshotQuiescent(ctx, stores, session)
		}
		if stores.IssueSnapshots != nil {
			deps.snapshot = stores.IssueSnapshots.GetByTurn
		}
	}
	return newAutomaticPublicationHandlerWithDependencies(deps, logger)
}

func newAutomaticPublicationHandlerWithDependencies(deps automaticPublicationDependencies, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, _ string, payload json.RawMessage) error {
		var input struct {
			OrgID     uuid.UUID `json:"org_id"`
			SessionID uuid.UUID `json:"session_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return &FatalError{Err: fmt.Errorf("decode automatic publication coordination: %w", err)}
		}
		if input.OrgID == uuid.Nil || input.SessionID == uuid.Nil {
			return &FatalError{Err: errors.New("automatic publication coordination requires scoped session IDs")}
		}
		if deps.session == nil || deps.primary == nil || deps.publication == nil || deps.pullRequest == nil ||
			deps.run == nil || deps.policy == nil || deps.coordinator == nil || deps.quiescent == nil {
			return errors.New("automatic publication coordination dependencies are unavailable")
		}
		session, err := deps.session(ctx, input.OrgID, input.SessionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("load automatic publication session: %w", err)
		}
		if session.AutomationRunID == nil || session.Status != models.SessionStatusCompleted || session.IsInteractive() ||
			session.Origin == models.SessionOriginAutomationGoalImprovement || session.Diff == nil || strings.TrimSpace(*session.Diff) == "" {
			return nil
		}
		changeset, err := deps.primary(ctx, input.OrgID, input.SessionID)
		if err != nil {
			return fmt.Errorf("load automatic publication changeset: %w", err)
		}
		if _, err := deps.publication(ctx, input.OrgID, input.SessionID, changeset.ID); err == nil {
			// The completion job only repairs admission before an intent exists.
			// The publication's own worker/reconciler owns every later state,
			// including terminal outcomes that must not be reopened by this job.
			return nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("check automatic publication intent: %w", err)
		}
		if _, err := deps.pullRequest(ctx, input.OrgID, input.SessionID); err == nil {
			return nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("check existing automatic pull request: %w", err)
		}
		if err := deps.quiescent(ctx, session); err != nil {
			return err
		}
		run, err := deps.run(ctx, input.OrgID, *session.AutomationRunID)
		if err != nil {
			return fmt.Errorf("load automatic publication run: %w", err)
		}
		var snapshot struct {
			Continuity models.AutomationSessionContinuity `json:"session_continuity"`
		}
		if len(run.ConfigSnapshot) > 0 {
			if err := json.Unmarshal(run.ConfigSnapshot, &snapshot); err != nil {
				return fmt.Errorf("decode automatic publication run snapshot: %w", err)
			}
		}
		if run.TargetID != nil || snapshot.Continuity.OrDefault() != models.AutomationSessionContinuityPerRun {
			return nil
		}
		policy, err := deps.policy(ctx, input.OrgID, *session.AutomationRunID)
		if err != nil {
			return fmt.Errorf("resolve automatic publication policy: %w", err)
		}
		if policy == models.AutomationPublishPolicyNone {
			return nil
		}
		if policy != models.AutomationPublishPolicyPullRequest {
			return fmt.Errorf("invalid automatic publication policy: %q", policy)
		}
		request := publicationintent.RequestPullRequest{
			Source: models.SessionPublicationSourceAutomation, TriggerKind: models.SessionPublicationTriggerPolicy,
		}
		if deps.snapshot != nil {
			turn := max(session.CurrentTurn, 1)
			issueSnapshot, err := deps.snapshot(ctx, input.OrgID, input.SessionID, turn)
			if err == nil {
				request.IssueSnapshotID = &issueSnapshot.ID
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("load automatic publication issue snapshot: %w", err)
			}
		}
		result, err := deps.coordinator.RequestPullRequest(ctx, input.OrgID, input.SessionID, request)
		if err != nil {
			return fmt.Errorf("coordinate automatic publication: %w", err)
		}
		if result == nil {
			return errors.New("automatic publication coordinator returned no result")
		}
		logger.Info().Str("session_id", input.SessionID.String()).Str("publication_status", string(result.Status)).Msg("automatic publication coordination completed")
		return nil
	}
}
