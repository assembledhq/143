package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type helperSessionIssueLinkStore struct {
	links []models.SessionIssueLink
	err   error
}

func (s *helperSessionIssueLinkStore) ListBySession(context.Context, uuid.UUID, uuid.UUID) ([]models.SessionIssueLink, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.links, nil
}

type helperIssueSnapshotStore struct {
	created   []*models.SessionTurnIssueSnapshot
	createErr error
}

func (s *helperIssueSnapshotStore) Create(_ context.Context, snapshot *models.SessionTurnIssueSnapshot) error {
	if s.createErr != nil {
		return s.createErr
	}
	snapshot.ID = uuid.New()
	s.created = append(s.created, snapshot)
	return nil
}

func (s *helperIssueSnapshotStore) GetByTurn(context.Context, uuid.UUID, uuid.UUID, int) (models.SessionTurnIssueSnapshot, error) {
	return models.SessionTurnIssueSnapshot{}, errors.New("unexpected call")
}

type helperIssueStore struct {
	issue models.Issue
	err   error
}

func (s *helperIssueStore) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Issue, error) {
	if s.err != nil {
		return models.Issue{}, s.err
	}
	return s.issue, nil
}

func TestLatestUserMessage(t *testing.T) {
	t.Parallel()

	message := latestUserMessage([]models.SessionMessage{
		{Role: models.MessageRoleAssistant, Content: "assistant"},
		{Role: models.MessageRoleUser, Content: "first user"},
		{Role: models.MessageRoleAssistant, Content: "assistant again"},
		{Role: models.MessageRoleUser, Content: "latest user"},
	})

	require.NotNil(t, message, "latestUserMessage should return the most recent user message")
	require.Equal(t, "latest user", message.Content, "latestUserMessage should scan from newest to oldest")
}

func TestCanonicalReferences_FiltersInvalidEntries(t *testing.T) {
	t.Parallel()

	message := &models.SessionMessage{
		References: models.SessionInputReferences{
			{Kind: models.SessionInputReferenceKindFile, Path: "/repo/main.go", Display: "main.go"},
			{Kind: models.SessionInputReferenceKindFile, Display: "missing path"},
			{Kind: models.SessionInputReferenceKindApp, ID: "github", Display: "GitHub"},
		},
	}

	refs := canonicalReferences(message)
	require.Len(t, refs, 2, "canonicalReferences should keep only valid references")
	require.Equal(t, "/repo/main.go", refs[0].Path, "canonicalReferences should preserve valid file references")
	require.Equal(t, "github", refs[1].ID, "canonicalReferences should preserve valid app references")
}

func TestCanonicalCommands_FiltersInvalidAndOtherAgents(t *testing.T) {
	t.Parallel()

	message := &models.SessionMessage{
		Commands: models.SessionInputCommands{
			{Kind: "command", AgentType: models.AgentTypeClaudeCode, Name: "review", Token: "/review", Display: "/review"},
			{Kind: "command", AgentType: models.AgentTypeCodex, Name: "diff", Token: "/diff", Display: "/diff"},
			{Kind: "command", AgentType: models.AgentTypeClaudeCode, Name: "", Token: "/x", Display: "/x"},
		},
	}

	commands := canonicalCommands(message, models.AgentTypeClaudeCode)
	require.Len(t, commands, 1, "canonicalCommands should drop invalid entries and entries targeting another agent")
	require.Equal(t, "review", commands[0].Name)

	require.Nil(t, canonicalCommands(nil, models.AgentTypeClaudeCode), "nil message returns nil")
	require.Nil(t, canonicalCommands(&models.SessionMessage{}, models.AgentTypeClaudeCode), "empty commands returns nil")
}

func TestHydrateSessionPolicyForExecution(t *testing.T) {
	t.Parallel()

	hydrateSessionPolicyForExecution(nil, nil)

	tests := []struct {
		name            string
		session         *models.Session
		wantOrigin      models.SessionOrigin
		wantInteraction models.SessionInteractionMode
		wantValidation  models.SessionValidationPolicy
	}{
		{
			name: "automation sessions become single-run turn validation",
			session: &models.Session{
				AutomationRunID: func() *uuid.UUID { id := uuid.New(); return &id }(),
			},
			wantOrigin:      models.SessionOriginAutomation,
			wantInteraction: models.SessionInteractionModeSingleRun,
			wantValidation:  models.SessionValidationPolicyOnTurnComplete,
		},
		{
			name: "triggered by user becomes manual",
			session: &models.Session{
				TriggeredByUserID: func() *uuid.UUID { id := uuid.New(); return &id }(),
			},
			wantOrigin:      models.SessionOriginManual,
			wantInteraction: models.SessionInteractionModeInteractive,
			wantValidation:  models.SessionValidationPolicyOnSessionEnd,
		},
		{
			name: "revision sessions derive origin from parent session",
			session: &models.Session{
				ParentSessionID: func() *uuid.UUID { id := uuid.New(); return &id }(),
			},
			wantOrigin:      models.SessionOriginRevision,
			wantInteraction: models.SessionInteractionModeSingleRun,
			wantValidation:  models.SessionValidationPolicyOnTurnComplete,
		},
		{
			name:            "empty session defaults to issue_trigger single-run",
			session:         &models.Session{},
			wantOrigin:      models.SessionOriginIssueTrigger,
			wantInteraction: models.SessionInteractionModeSingleRun,
			wantValidation:  models.SessionValidationPolicyOnTurnComplete,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hydrateSessionPolicyForExecution(tt.session, nil)

			require.Equal(t, tt.wantOrigin, tt.session.Origin, "hydrateSessionPolicyForExecution should set the session origin")
			require.Equal(t, tt.wantInteraction, tt.session.InteractionMode, "hydrateSessionPolicyForExecution should set the interaction mode")
			require.Equal(t, tt.wantValidation, tt.session.ValidationPolicy, "hydrateSessionPolicyForExecution should set the validation policy")
		})
	}

	t.Run("legacy synthetic manual issue overrides migrated defaults", func(t *testing.T) {
		t.Parallel()

		session := &models.Session{
			Origin:           models.SessionOriginIssueTrigger,
			InteractionMode:  models.SessionInteractionModeSingleRun,
			ValidationPolicy: models.SessionValidationPolicyOnTurnComplete,
		}

		hydrateSessionPolicyForExecution(session, &models.Issue{Source: models.IssueSourceManual})

		require.Equal(t, models.SessionOriginManual, session.Origin, "legacy synthetic manual sessions should retain manual origin semantics")
		require.Equal(t, models.SessionInteractionModeInteractive, session.InteractionMode, "legacy synthetic manual sessions should remain interactive")
		require.Equal(t, models.SessionValidationPolicyOnSessionEnd, session.ValidationPolicy, "legacy synthetic manual sessions should validate on session end")
	})
}

func TestCreateIssueSnapshotForTurn(t *testing.T) {
	t.Parallel()

	t.Run("persists snapshot entries derived from linked issues", func(t *testing.T) {
		t.Parallel()

		repoID := uuid.New()
		title := "Fix checkout timeout"
		description := "Customers hit a timeout after payment authorization."
		status := "open"
		source := models.IssueSourceLinear
		links := []models.SessionIssueLink{
			{
				IssueID:      uuid.New(),
				Role:         models.SessionIssueLinkRolePrimary,
				Position:     0,
				IssueTitle:   &title,
				Description:  &description,
				IssueStatus:  &status,
				IssueSource:  &source,
				RepositoryID: &repoID,
			},
			{
				IssueID:      uuid.New(),
				Role:         models.SessionIssueLinkRoleRelated,
				Position:     1,
				IssueTitle:   func() *string { s := "Related checkout flake"; return &s }(),
				RepositoryID: &repoID,
			},
		}
		issueSnapshots := &helperIssueSnapshotStore{}
		orchestrator := &Orchestrator{
			sessionIssueLinks: &helperSessionIssueLinkStore{links: links},
			issueSnapshots:    issueSnapshots,
		}

		snapshot, err := orchestrator.createIssueSnapshotForTurn(context.Background(), &models.Session{
			ID:    uuid.New(),
			OrgID: uuid.New(),
		}, 3)

		require.NoError(t, err, "createIssueSnapshotForTurn should persist snapshots when links are valid")
		require.NotNil(t, snapshot, "createIssueSnapshotForTurn should return the created snapshot")
		require.Len(t, issueSnapshots.created, 1, "createIssueSnapshotForTurn should call the snapshot store")
		require.Len(t, snapshot.LinkedIssues, 2, "createIssueSnapshotForTurn should snapshot all linked issues")
		require.Equal(t, title, snapshot.LinkedIssues[0].Title, "createIssueSnapshotForTurn should preserve issue titles in the snapshot")
		require.Equal(t, models.IssueSourcePMAgent, snapshot.LinkedIssues[1].Source, "createIssueSnapshotForTurn should default missing sources to pm_agent")
	})

	t.Run("rejects link sets without a primary issue", func(t *testing.T) {
		t.Parallel()

		orchestrator := &Orchestrator{
			sessionIssueLinks: &helperSessionIssueLinkStore{links: []models.SessionIssueLink{
				{IssueID: uuid.New(), Role: models.SessionIssueLinkRoleRelated},
			}},
			issueSnapshots: &helperIssueSnapshotStore{},
		}

		_, err := orchestrator.createIssueSnapshotForTurn(context.Background(), &models.Session{ID: uuid.New(), OrgID: uuid.New()}, 1)
		require.Error(t, err, "createIssueSnapshotForTurn should reject link sets without a primary issue")
		require.Contains(t, err.Error(), "exactly one primary issue", "createIssueSnapshotForTurn should explain the primary-issue invariant")
	})

	t.Run("returns nil when snapshotting is disabled", func(t *testing.T) {
		t.Parallel()

		orchestrator := &Orchestrator{}
		snapshot, err := orchestrator.createIssueSnapshotForTurn(context.Background(), &models.Session{ID: uuid.New(), OrgID: uuid.New()}, 1)
		require.NoError(t, err, "createIssueSnapshotForTurn should be a no-op when snapshot dependencies are absent")
		require.Nil(t, snapshot, "createIssueSnapshotForTurn should return nil when snapshotting is disabled")
	})

	t.Run("returns link lookup errors", func(t *testing.T) {
		t.Parallel()

		orchestrator := &Orchestrator{
			sessionIssueLinks: &helperSessionIssueLinkStore{err: errors.New("db unavailable")},
			issueSnapshots:    &helperIssueSnapshotStore{},
		}

		_, err := orchestrator.createIssueSnapshotForTurn(context.Background(), &models.Session{ID: uuid.New(), OrgID: uuid.New()}, 1)
		require.Error(t, err, "createIssueSnapshotForTurn should return link lookup errors")
		require.Contains(t, err.Error(), "list session issue links", "createIssueSnapshotForTurn should wrap link lookup errors")
	})

	t.Run("returns snapshot persistence errors", func(t *testing.T) {
		t.Parallel()

		orchestrator := &Orchestrator{
			sessionIssueLinks: &helperSessionIssueLinkStore{links: []models.SessionIssueLink{{IssueID: uuid.New(), Role: models.SessionIssueLinkRolePrimary}}},
			issueSnapshots:    &helperIssueSnapshotStore{createErr: errors.New("insert failed")},
		}

		_, err := orchestrator.createIssueSnapshotForTurn(context.Background(), &models.Session{ID: uuid.New(), OrgID: uuid.New()}, 1)
		require.Error(t, err, "createIssueSnapshotForTurn should return snapshot store errors")
		require.Contains(t, err.Error(), "create issue snapshot", "createIssueSnapshotForTurn should wrap snapshot persistence errors")
	})
}

func TestPromptSeedForSession(t *testing.T) {
	t.Parallel()

	repoID := uuid.New()
	primaryIssueID := uuid.New()
	linkedIssues := []models.SessionIssueSnapshotEntry{
		{
			IssueID:      primaryIssueID,
			Role:         models.SessionIssueLinkRolePrimary,
			Position:     0,
			Title:        "Fix checkout timeout",
			ExternalID:   "ENG-123",
			Source:       models.IssueSourceLinear,
			Description:  "Customers hit a timeout after payment authorization.",
			RepositoryID: &repoID,
			Status:       "open",
		},
	}

	t.Run("prefers the primary issue from the snapshot", func(t *testing.T) {
		t.Parallel()

		orchestrator := &Orchestrator{}
		issue, gotLinkedIssues := orchestrator.promptSeedForSession(
			&models.Session{Origin: models.SessionOriginIssueTrigger},
			nil,
			&models.SessionTurnIssueSnapshot{LinkedIssues: linkedIssues},
		)

		require.NotNil(t, issue, "promptSeedForSession should synthesize an issue from the snapshot primary issue")
		require.Equal(t, primaryIssueID, issue.ID, "promptSeedForSession should prefer the snapshot primary issue")
		require.Equal(t, linkedIssues, gotLinkedIssues, "promptSeedForSession should return the snapshot linked issues")
	})

	t.Run("builds a manual issue from the latest user message", func(t *testing.T) {
		t.Parallel()

		title := "Investigate checkout timeout"
		orchestrator := &Orchestrator{}
		issue, gotLinkedIssues := orchestrator.promptSeedForSession(
			&models.Session{Origin: models.SessionOriginManual, Title: &title},
			&models.SessionMessage{Content: "Please fix the cart timeout."},
			nil,
		)

		require.NotNil(t, issue, "promptSeedForSession should synthesize an issue for manual sessions")
		require.Equal(t, models.IssueSourceManual, issue.Source, "promptSeedForSession should mark manual sessions with manual source")
		require.Equal(t, title, issue.Title, "promptSeedForSession should prefer the session title for manual sessions")
		require.NotNil(t, issue.Description, "promptSeedForSession should include the latest user message as the issue description")
		require.Empty(t, gotLinkedIssues, "promptSeedForSession should return no linked issues when no snapshot exists")
	})

	t.Run("builds a pm-agent issue from session context when there is no linked issue", func(t *testing.T) {
		t.Parallel()

		title := "Investigate checkout timeout"
		approach := "Inspect the retry path."
		reasoning := "Timeouts started after the last deploy."
		orchestrator := &Orchestrator{}
		issue, gotLinkedIssues := orchestrator.promptSeedForSession(
			&models.Session{
				Title:        &title,
				PMApproach:   &approach,
				PMReasoning:  &reasoning,
				RepositoryID: &repoID,
			},
			nil,
			nil,
		)

		require.NotNil(t, issue, "promptSeedForSession should synthesize an issue from PM context")
		require.Equal(t, models.IssueSourcePMAgent, issue.Source, "promptSeedForSession should synthesize a pm_agent issue when there is no linked issue")
		require.NotNil(t, issue.Description, "promptSeedForSession should combine PM approach and reasoning into the description")
		require.Contains(t, *issue.Description, approach, "promptSeedForSession should preserve the PM approach in the description")
		require.Contains(t, *issue.Description, reasoning, "promptSeedForSession should preserve the PM reasoning in the description")
		require.Empty(t, gotLinkedIssues, "promptSeedForSession should not synthesize linked issues when no snapshot exists")
	})
}

func TestResolvePromptSeed_FallsBackToPrimaryIssueStore(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	primaryIssueID := uuid.New()
	repoID := uuid.New()
	fallbackIssue := models.Issue{
		ID:           primaryIssueID,
		OrgID:        orgID,
		RepositoryID: &repoID,
		Source:       models.IssueSourceSentry,
		Title:        "Fix checkout timeout",
	}
	orchestrator := &Orchestrator{
		issues: &helperIssueStore{issue: fallbackIssue},
	}
	session := &models.Session{
		OrgID:          orgID,
		PrimaryIssueID: &primaryIssueID,
	}

	issue, linkedIssues, err := orchestrator.resolvePromptSeed(context.Background(), session, nil, nil)

	require.NoError(t, err, "resolvePromptSeed should fall back to the primary issue store when no snapshot exists")
	require.NotNil(t, issue, "resolvePromptSeed should return the fallback issue")
	require.Equal(t, primaryIssueID, issue.ID, "resolvePromptSeed should return the fetched primary issue")
	require.Len(t, linkedIssues, 1, "resolvePromptSeed should synthesize a single primary snapshot entry from the fallback issue")
	require.Equal(t, models.SessionOriginIssueTrigger, session.Origin, "resolvePromptSeed should hydrate session policy from the fetched issue")
}

func TestResolvePromptSeed_EarlyReturnAndErrors(t *testing.T) {
	t.Parallel()

	t.Run("returns prompt seed when there is no primary issue store fallback", func(t *testing.T) {
		t.Parallel()

		orchestrator := &Orchestrator{}
		session := &models.Session{}

		issue, linkedIssues, err := orchestrator.resolvePromptSeed(context.Background(), session, nil, nil)
		require.NoError(t, err, "resolvePromptSeed should return the synthesized prompt seed when there is no fallback issue store")
		require.NotNil(t, issue, "resolvePromptSeed should still return a synthesized issue for non-manual sessions")
		require.Empty(t, linkedIssues, "resolvePromptSeed should return no linked issues when no snapshot exists")
	})

	t.Run("returns fallback fetch errors", func(t *testing.T) {
		t.Parallel()

		primaryIssueID := uuid.New()
		orchestrator := &Orchestrator{
			issues: &helperIssueStore{err: errors.New("db unavailable")},
		}
		session := &models.Session{
			OrgID:          uuid.New(),
			PrimaryIssueID: &primaryIssueID,
		}

		_, _, err := orchestrator.resolvePromptSeed(context.Background(), session, nil, nil)
		require.Error(t, err, "resolvePromptSeed should return primary issue fetch errors")
		require.Contains(t, err.Error(), "fetch primary issue", "resolvePromptSeed should wrap primary issue fetch errors")
	})
}
