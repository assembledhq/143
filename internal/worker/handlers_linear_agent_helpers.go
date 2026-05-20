package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/linear"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// upsertLinearIssueForAgent persists or refreshes the 143 issues row for
// a Linear issue. Reuses the existing IssueStore semantics: source=linear,
// external_id=Linear's UUID, repository_id=resolved repo so detection-time
// joins still work. The fingerprint shape ("linear:<external_id>") matches
// services/linear/service.go upsertLinearIssue so a session that triggers
// through both the agent path and a manual paste sees a single issues row.
func upsertLinearIssueForAgent(ctx context.Context, stores *Stores, orgID uuid.UUID, fetched *linear.FetchedIssue, repoID *uuid.UUID) (*models.Issue, error) {
	now := timeNow()
	title := fetched.Title
	if fetched.Identifier != "" {
		title = fetched.Identifier + ": " + fetched.Title
	}
	desc := fetched.Description
	issue := &models.Issue{
		OrgID:           orgID,
		Source:          models.IssueSourceLinear,
		ExternalID:      fetched.ID,
		Title:           title,
		Description:     &desc,
		Status:          "open",
		FirstSeenAt:     now,
		LastSeenAt:      now,
		OccurrenceCount: 1,
		Severity:        "medium",
		Fingerprint:     models.IssueFingerprint(models.IssueSourceLinear, fetched.ID),
		RepositoryID:    repoID,
	}
	if err := stores.Issues.Upsert(ctx, issue); err != nil {
		return nil, fmt.Errorf("issues upsert: %w", err)
	}
	return issue, nil
}

// buildAgentSession assembles the session row written by the worker. The
// PMApproach carries the issue body so run_agent has all the context it
// needs without re-fetching Linear data.
//
// The Linear AgentSessionID is intentionally NOT a parameter here — it
// lives on session_issue_link_provider_state.AgentSessionID (written by
// writeAgentProviderState after Create) rather than on the session row,
// because not all sessions are agent-triggered and we don't want to pay
// schema cost on the hot sessions table for a provider-specific field.
func buildAgentSession(orgID uuid.UUID, repo linear.AgentRepoResolveResult, issue *models.Issue, fetched *linear.FetchedIssue, agentType models.AgentType) *models.Session {
	if agentType == "" {
		agentType = models.DefaultDefaultAgentType
	}
	primaryIssueID := issue.ID
	repoID := repo.RepositoryID
	identifier := fetched.Identifier
	title := fetched.Title
	if title == "" {
		title = identifier
	}
	approach := buildIssueApproachPrompt(fetched)
	var targetBranch *string
	if repo.DefaultBranch != "" {
		targetBranch = &repo.DefaultBranch
	}
	return &models.Session{
		OrgID:                orgID,
		AgentType:            agentType,
		Status:               string(models.SessionStatusPending),
		Origin:               models.SessionOriginIssueTrigger,
		Title:                &title,
		PMApproach:           &approach,
		AutonomyLevel:        string(models.DefaultSessionAutonomy),
		TokenMode:            "low",
		RepositoryID:         &repoID,
		PrimaryIssueID:       &primaryIssueID,
		LinearIdentifierHint: &identifier,
		LinearPrepareState:   models.LinearPrepareStateReady,
		// SingleRun is hardcoded for issue-triggered sessions because the
		// inbound model is request/response: Linear sends `created`, the
		// agent runs once and posts a PR. Follow-up @-mentions arrive as
		// distinct `prompted` events and append a new turn — we do not
		// want the agent to keep iterating between user replies on its
		// own. Org-level interaction defaults intentionally do not apply
		// here; agent-triggered sessions have their own UX contract.
		InteractionMode:  models.SessionInteractionModeSingleRun,
		ValidationPolicy: models.SessionValidationPolicyOnTurnComplete,
		TargetBranch:     targetBranch,
	}
}

func resolveLinearAgentSessionAgentType(ctx context.Context, deps LinearAgentEventHandlerDeps, orgID uuid.UUID) (models.AgentType, error) {
	if deps.OrgSettingsLoader == nil {
		return models.DefaultDefaultAgentType, nil
	}
	settings, err := deps.OrgSettingsLoader(ctx, orgID)
	if err != nil {
		return "", fmt.Errorf("load org settings: %w", err)
	}
	if settings.DefaultAgentType == "" {
		return models.DefaultDefaultAgentType, nil
	}
	return settings.DefaultAgentType, nil
}

// buildIssueApproachPrompt is the deterministic prompt body 143 hands the
// agent on session start. Captures issue identifier, title, description,
// and recent comments so the agent can boot without a live Linear fetch.
func buildIssueApproachPrompt(fetched *linear.FetchedIssue) string {
	if fetched == nil {
		return ""
	}
	out := fmt.Sprintf("Linear issue %s — %s\n\n%s",
		fetched.Identifier, fetched.Title, fetched.Description)
	if len(fetched.Comments) > 0 {
		out += "\n\nRecent discussion:\n"
		for _, c := range fetched.Comments {
			out += fmt.Sprintf("- %s: %s\n", c.Author, c.Body)
		}
	}
	if len(fetched.Attachments) > 0 {
		out += "\nLinked references:\n"
		for _, a := range fetched.Attachments {
			out += fmt.Sprintf("- %s — %s\n", a.Title, a.URL)
		}
	}
	return out
}

// writeAgentProviderState records the AgentSessionID against the link's
// provider_state row so HandleMilestone fan-outs can discover it. The
// link itself was inserted by SessionStore.Create when PrimaryIssueID was
// set on the session; we look it up by (session, issue) and write the
// merged provider_state in a single round-trip.
func writeAgentProviderState(ctx context.Context, stores *Stores, providerState *db.LinearProviderStateStore, orgID, sessionID, issueID uuid.UUID, agentSessionID string, fetched *linear.FetchedIssue) error {
	if stores.SessionIssueLinks == nil {
		return errors.New("session_issue_links store unavailable")
	}
	if providerState == nil {
		return errors.New("provider_state store unavailable")
	}
	primary, err := stores.SessionIssueLinks.LookupPrimaryByIssue(ctx, orgID, sessionID, issueID)
	if err != nil {
		return fmt.Errorf("lookup primary session issue link: %w", err)
	}
	return providerState.Merge(ctx, orgID, primary.ID, db.LinearProviderState{
		Identifier:     fetched.Identifier,
		TeamID:         fetched.TeamID,
		WorkspaceSlug:  fetched.WorkspaceSlug,
		AgentSessionID: agentSessionID,
	})
}

// enqueueRunAgentForLinearAgent fires the agent orchestrator for the
// freshly-created session. Same dedupe shape as the manual path so retries
// collapse cleanly.
func enqueueRunAgentForLinearAgent(ctx context.Context, stores *Stores, orgID, sessionID uuid.UUID) error {
	if stores == nil || stores.Sessions == nil || stores.Jobs == nil {
		return errors.New("linear agent run enqueue stores unavailable")
	}
	session, err := stores.Sessions.GetByID(ctx, orgID, sessionID)
	if err != nil {
		return fmt.Errorf("load session before run_agent enqueue: %w", err)
	}
	if !linearAgentCreatedShouldEnqueueRun(models.SessionStatus(session.Status)) {
		return nil
	}

	dedupe := db.RunAgentDedupeKey(sessionID)
	_, err = stores.Jobs.Enqueue(ctx, orgID, "agent", "run_agent", map[string]any{
		"org_id":     orgID.String(),
		"session_id": sessionID.String(),
	}, 5, &dedupe)
	return err
}

func linearAgentCreatedShouldEnqueueRun(status models.SessionStatus) bool {
	return status == models.SessionStatusPending || status == models.SessionStatusRunning
}

// finalizeUnsupported is the close-AgentSession-with-explanation path
// invoked when the team is disabled or the resolver gives up. Emits a
// `response` activity and pins state to the supplied terminal value.
func finalizeUnsupported(ctx context.Context, client linear.Client, sessions *db.LinearAgentSessionStore, activities *db.LinearAgentActivityLogStore, row *db.LinearAgentSession, orgID uuid.UUID, body string, state models.LinearAgentSessionState, logger zerolog.Logger) error {
	activity := linear.AgentMilestoneActivity{
		Type:            models.LinearAgentActivityResponse,
		Body:            body,
		IdemKey:         "bootstrap:not_supported",
		PinSessionState: linearAgentPinSessionState(state),
	}
	if err := emitOnce(ctx, client, activities, orgID, row.ID, row.LinearAgentSessionID, activity, logger); err != nil {
		return fmt.Errorf("emit close activity: %w", err)
	}
	return sessions.SetState(ctx, orgID, row.ID, state)
}

func linearAgentPinSessionState(state models.LinearAgentSessionState) string {
	return string(state)
}

// emitOnce is a tiny wrapper that constructs an AgentActivityWriter on
// demand and calls EmitOrDiscard. Used by the worker for one-off close
// activities where missing the response leaves the user without an
// explanation; HandleAgentMilestone uses its own writer for at-most-once
// milestone fan-out.
//
// EmitOrDiscard vs Emit: this wrapper deliberately uses EmitOrDiscard,
// which discards the Reserve row on a failed Linear emit so retries can
// re-attempt the emit. The trade-off is "duplicate notifications on retry
// after a transport blip that Linear actually accepted" vs "permanent
// silent drop of the response". For the activities routed through this
// helper today — bootstrap:unmapped_repo, bootstrap:not_supported,
// prompted:revision_disabled, prompted:awaiting_created_timeout — a
// duplicate is idempotent on Linear's side (the body is a fixed close
// message and the session state pin is the same), so the duplicate cost
// is essentially zero while the silent-drop cost is the user never
// learning why the agent went quiet. Milestone fan-out (HandleAgentMilestone)
// uses Emit, not EmitOrDiscard, because milestones carry semantically
// distinct payloads (PR numbers etc.) where a duplicate would be
// user-visible noise.
//
// The caller's logger is passed through so writer-internal warnings
// (e.g. failed best-effort AgentSessionUpdate pins) surface in operator
// logs instead of disappearing into a Nop sink.
func emitOnce(ctx context.Context, client linear.Client, activities *db.LinearAgentActivityLogStore, orgID, rowID uuid.UUID, agentSessionID string, activity linear.AgentMilestoneActivity, logger zerolog.Logger) error {
	if activities == nil {
		return errors.New("activity log store not configured")
	}
	writer := linear.NewAgentActivityWriter(client, activities, nil, logger)
	_, err := writer.EmitOrDiscard(ctx, linear.EmitInput{
		OrgID:             orgID,
		AgentSessionRowID: rowID,
		AgentSessionID:    agentSessionID,
		Activity:          activity,
	})
	return err
}

// timeNow is a thin alias so tests can stub it via the worker test
// helpers if they need deterministic FirstSeenAt/LastSeenAt values. Today
// it just delegates; the indirection keeps a future hook trivial.
func timeNow() time.Time {
	return time.Now().UTC()
}
