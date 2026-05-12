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
		Fingerprint:     "linear:" + fetched.ID,
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
func buildAgentSession(orgID uuid.UUID, repo linear.AgentRepoResolveResult, issue *models.Issue, fetched *linear.FetchedIssue) *models.Session {
	primaryIssueID := issue.ID
	repoID := repo.RepositoryID
	identifier := fetched.Identifier
	title := fetched.Title
	if title == "" {
		title = identifier
	}
	approach := buildIssueApproachPrompt(fetched)
	return &models.Session{
		OrgID:                orgID,
		AgentType:            models.AgentTypeCodex,
		Status:               string(models.SessionStatusPending),
		Origin:               models.SessionOriginIssueTrigger,
		Title:                &title,
		PMApproach:           &approach,
		RepositoryID:         &repoID,
		PrimaryIssueID:       &primaryIssueID,
		LinearIdentifierHint: &identifier,
		LinearPrepareState:   models.LinearPrepareStateReady,
		InteractionMode:      models.SessionInteractionModeSingleRun,
		ValidationPolicy:     models.SessionValidationPolicyOnTurnComplete,
	}
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
	dedupe := "run_agent:" + sessionID.String()
	_, err := stores.Jobs.Enqueue(ctx, orgID, "default", "run_agent", map[string]any{
		"org_id":     orgID.String(),
		"session_id": sessionID.String(),
	}, 5, &dedupe)
	return err
}

// finalizeUnsupported is the close-AgentSession-with-explanation path
// invoked when the team is disabled or the resolver gives up. Emits a
// `response` activity and pins state to the supplied terminal value.
func finalizeUnsupported(ctx context.Context, client linear.Client, sessions *db.LinearAgentSessionStore, activities *db.LinearAgentActivityLogStore, row *db.LinearAgentSession, orgID uuid.UUID, body string, state models.LinearAgentSessionState, logger zerolog.Logger) error {
	activity := linear.AgentMilestoneActivity{
		Type:            models.LinearAgentActivityResponse,
		Body:            body,
		IdemKey:         "bootstrap:not_supported",
		PinSessionState: "complete",
	}
	if err := emitOnce(ctx, client, activities, orgID, row.ID, row.LinearAgentSessionID, activity, logger); err != nil {
		return fmt.Errorf("emit close activity: %w", err)
	}
	return sessions.SetState(ctx, orgID, row.ID, state)
}

// emitOnce is a tiny wrapper that constructs an AgentActivityWriter on
// demand and calls Emit. Used by the worker for one-off close activities;
// HandleAgentMilestone uses its own writer for the milestone fan-out.
// The caller's logger is passed through so writer-internal warnings
// (e.g. failed best-effort AgentSessionUpdate pins) surface in operator
// logs instead of disappearing into a Nop sink.
func emitOnce(ctx context.Context, client linear.Client, activities *db.LinearAgentActivityLogStore, orgID, rowID uuid.UUID, agentSessionID string, activity linear.AgentMilestoneActivity, logger zerolog.Logger) error {
	if activities == nil {
		return errors.New("activity log store not configured")
	}
	writer := linear.NewAgentActivityWriter(client, activities, nil, logger)
	_, err := writer.Emit(ctx, linear.EmitInput{
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
