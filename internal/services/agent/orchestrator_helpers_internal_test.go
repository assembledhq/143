package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
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

func (s *helperIssueStore) UpdateStatus(context.Context, uuid.UUID, uuid.UUID, string) error {
	return s.err
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

func TestUnprocessedUserMessages_SessionScope(t *testing.T) {
	t.Parallel()

	// Trailing user messages with no thread are returned in chronological
	// order; messages preceding the most recent assistant are excluded.
	pending := unprocessedUserMessages([]models.SessionMessage{
		{Role: models.MessageRoleUser, Content: "old (already processed)"},
		{Role: models.MessageRoleAssistant, Content: "assistant reply"},
		{Role: models.MessageRoleUser, Content: "queued one"},
		{Role: models.MessageRoleUser, Content: "queued two"},
	}, nil)

	require.Len(t, pending, 2, "should return only messages after the latest in-scope assistant")
	require.Equal(t, "queued one", pending[0].Content, "should be oldest-first")
	require.Equal(t, "queued two", pending[1].Content)
}

func TestUnprocessedUserMessages_ThreadScope(t *testing.T) {
	t.Parallel()

	threadA := uuid.New()
	threadB := uuid.New()

	// Thread B's activity (its own user + assistant exchange) sits between
	// the two thread-A user messages, but it must not break the thread-A
	// scan: cross-thread messages are skipped, and only a same-thread
	// assistant terminates the trailing window.
	pending := unprocessedUserMessages([]models.SessionMessage{
		{Role: models.MessageRoleUser, Content: "A first", ThreadID: &threadA},
		{Role: models.MessageRoleAssistant, Content: "A reply", ThreadID: &threadA},
		{Role: models.MessageRoleUser, Content: "B prompt", ThreadID: &threadB},
		{Role: models.MessageRoleAssistant, Content: "B reply", ThreadID: &threadB},
		{Role: models.MessageRoleUser, Content: "A queued one", ThreadID: &threadA},
		{Role: models.MessageRoleUser, Content: "A queued two", ThreadID: &threadA},
	}, &threadA)

	require.Len(t, pending, 2, "thread-scoped scan should ignore cross-thread activity")
	require.Equal(t, "A queued one", pending[0].Content)
	require.Equal(t, "A queued two", pending[1].Content)
}

func TestUnprocessedUserMessages_IgnoresAssistantAfterLatestUserBoundary(t *testing.T) {
	t.Parallel()

	threadID := uuid.New()
	latestQueuedUserID := int64(12)

	pending := unprocessedUserMessagesThrough([]models.SessionMessage{
		{ID: 9, Role: models.MessageRoleAssistant, Content: "previous reply", ThreadID: &threadID},
		{ID: 10, Role: models.MessageRoleUser, Content: "queued one", ThreadID: &threadID},
		{ID: 11, Role: models.MessageRoleUser, Content: "queued two", ThreadID: &threadID},
		{ID: latestQueuedUserID, Role: models.MessageRoleUser, Content: "queued three", ThreadID: &threadID},
		{ID: 13, Role: models.MessageRoleAssistant, Content: "reply from the in-flight turn", ThreadID: &threadID},
	}, &threadID, latestQueuedUserID)

	require.Equal(t, []models.SessionMessage{
		{ID: 10, Role: models.MessageRoleUser, Content: "queued one", ThreadID: &threadID},
		{ID: 11, Role: models.MessageRoleUser, Content: "queued two", ThreadID: &threadID},
		{ID: latestQueuedUserID, Role: models.MessageRoleUser, Content: "queued three", ThreadID: &threadID},
	}, pending, "queued users before a later assistant should all be delivered to the next turn")
}

func TestUnprocessedUserMessages_NoMessages(t *testing.T) {
	t.Parallel()

	require.Empty(t, unprocessedUserMessages(nil, nil), "nil input returns empty")
	require.Empty(t, unprocessedUserMessages([]models.SessionMessage{
		{Role: models.MessageRoleAssistant, Content: "no trailing user"},
	}, nil), "no trailing user message returns empty")
}

func TestDrainAcceptableStatus(t *testing.T) {
	t.Parallel()

	for _, status := range []string{"idle", "running", "awaiting_input", "needs_human_guidance"} {
		require.True(t, drainAcceptableStatus(status), "status %q should accept a drain enqueue", status)
	}
	for _, status := range []string{"completed", "failed", "cancelled", "skipped", "pr_created", "pending", ""} {
		require.False(t, drainAcceptableStatus(status), "status %q should not accept a drain enqueue", status)
	}
}

func TestContinueSessionDedupeKey(t *testing.T) {
	t.Parallel()

	id := uuid.MustParse("00000000-0000-0000-0000-000000000abc")
	require.Equal(t, "continue_session:"+id.String(), continueSessionDedupeKey(id),
		"local helper must mirror db.ContinueSessionDedupeKey verbatim")
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

	t.Run("hydrates linear primary snapshot metadata from provider state", func(t *testing.T) {
		t.Parallel()

		rawLinearSnapshot := []byte(`{
			"identifier":"ACS-1234",
			"title":"Fix checkout timeout",
			"description":"Linear issue body",
			"state_name":"In Progress",
			"state_type":"started",
			"priority":"high",
			"assignee_name":"Ada Lovelace",
			"team_key":"ACS",
			"team_name":"App Core",
			"url":"https://linear.app/acme/issue/ACS-1234",
			"attachments":[{"title":"Trace","url":"https://example.com/trace","source":"sentry"}],
			"comments":[{"author":"Grace","body":"Please include the edge case.","created_at":"2026-04-30T12:00:00Z"}]
		}`)
		source := models.IssueSourceLinear
		link := models.SessionIssueLink{
			IssueID:                  uuid.New(),
			Role:                     models.SessionIssueLinkRolePrimary,
			Position:                 0,
			IssueSource:              &source,
			RawLinearPrimarySnapshot: rawLinearSnapshot,
		}
		issueSnapshots := &helperIssueSnapshotStore{}
		orchestrator := &Orchestrator{
			sessionIssueLinks: &helperSessionIssueLinkStore{links: []models.SessionIssueLink{link}},
			issueSnapshots:    issueSnapshots,
		}

		snapshot, err := orchestrator.createIssueSnapshotForTurn(context.Background(), &models.Session{
			ID:    uuid.New(),
			OrgID: uuid.New(),
		}, 1)

		require.NoError(t, err, "createIssueSnapshotForTurn should hydrate provider-state snapshots")
		require.Len(t, snapshot.LinkedIssues, 1, "snapshot should include the linked Linear issue")
		got := snapshot.LinkedIssues[0]
		require.Equal(t, "ACS-1234", got.ExternalID, "Linear snapshot should provide the human identifier")
		require.Equal(t, "Linear issue body", got.Description, "Linear snapshot should provide the issue description")
		require.Equal(t, "high", got.Priority, "Linear snapshot should provide priority metadata")
		require.Equal(t, "Ada Lovelace", got.AssigneeName, "Linear snapshot should provide assignee metadata")
		require.Equal(t, "ACS", got.TeamKey, "Linear snapshot should provide team metadata")
		require.Len(t, got.Attachments, 1, "Linear snapshot should include attachment references")
		require.Equal(t, "Trace", got.Attachments[0].Title, "Linear attachment title should be preserved")
		require.Len(t, got.Comments, 1, "Linear snapshot should include bounded comments")
		require.Equal(t, "Please include the edge case.", got.Comments[0].Body, "Linear comment body should be preserved")
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

	t.Run("uses pm approach as the synthetic issue title when the session title is missing", func(t *testing.T) {
		t.Parallel()

		approach := "Inspect the retry path"
		reasoning := "Timeouts started after the last deploy."
		orchestrator := &Orchestrator{}
		issue, gotLinkedIssues := orchestrator.promptSeedForSession(
			&models.Session{
				PMApproach:  &approach,
				PMReasoning: &reasoning,
			},
			nil,
			nil,
		)

		require.NotNil(t, issue, "promptSeedForSession should synthesize an issue when PM context exists without a title")
		require.Equal(t, approach, issue.Title, "promptSeedForSession should derive the synthetic issue title from the PM approach before falling back to a placeholder")
		require.NotNil(t, issue.Description, "promptSeedForSession should still include PM details in the description")
		require.Empty(t, gotLinkedIssues, "promptSeedForSession should not synthesize linked issues when no snapshot exists")
	})

	t.Run("uses the latest message as the synthetic issue title when the session title is missing", func(t *testing.T) {
		t.Parallel()

		orchestrator := &Orchestrator{}
		issue, gotLinkedIssues := orchestrator.promptSeedForSession(
			&models.Session{Origin: models.SessionOriginRevision},
			&models.SessionMessage{Content: "Rework the flaky checkout retry logic\nKeep the current API shape."},
			nil,
		)

		require.NotNil(t, issue, "promptSeedForSession should synthesize an issue when a non-manual session has a latest message")
		require.Equal(t, "Rework the flaky checkout retry logic", issue.Title, "promptSeedForSession should derive the synthetic issue title from the latest message before falling back to a placeholder")
		require.Nil(t, issue.Description, "promptSeedForSession should not invent a description when only a follow-up message is available")
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

// snapshotSessionStubProvider is a no-op SandboxProvider whose only relevant
// behavior is Snapshot — every other call panics so an unexpected provider
// interaction shows up loudly in tests instead of returning silent zero values.
type snapshotSessionStubProvider struct {
	snapshotFn   func(ctx context.Context, sb *Sandbox) (io.ReadCloser, error)
	snapshotCals int32
}

func (s *snapshotSessionStubProvider) Snapshot(ctx context.Context, sb *Sandbox) (io.ReadCloser, error) {
	atomic.AddInt32(&s.snapshotCals, 1)
	if s.snapshotFn != nil {
		return s.snapshotFn(ctx, sb)
	}
	return io.NopCloser(bytes.NewReader(nil)), nil
}
func (s *snapshotSessionStubProvider) calls() int { return int(atomic.LoadInt32(&s.snapshotCals)) }

func (s *snapshotSessionStubProvider) Name() string { return "stub" }
func (s *snapshotSessionStubProvider) Create(context.Context, SandboxConfig) (*Sandbox, error) {
	panic("snapshotSessionStubProvider.Create called unexpectedly")
}
func (s *snapshotSessionStubProvider) CloneRepo(context.Context, *Sandbox, string, string, string) error {
	panic("snapshotSessionStubProvider.CloneRepo called unexpectedly")
}
func (s *snapshotSessionStubProvider) Exec(context.Context, *Sandbox, string, io.Writer, io.Writer) (int, error) {
	panic("snapshotSessionStubProvider.Exec called unexpectedly")
}
func (s *snapshotSessionStubProvider) ReadFile(context.Context, *Sandbox, string) ([]byte, error) {
	panic("snapshotSessionStubProvider.ReadFile called unexpectedly")
}
func (s *snapshotSessionStubProvider) WriteFile(context.Context, *Sandbox, string, []byte) error {
	panic("snapshotSessionStubProvider.WriteFile called unexpectedly")
}
func (s *snapshotSessionStubProvider) Destroy(context.Context, *Sandbox) error { return nil }
func (s *snapshotSessionStubProvider) IsAlive(context.Context, *Sandbox) (bool, error) {
	panic("snapshotSessionStubProvider.IsAlive called unexpectedly")
}
func (s *snapshotSessionStubProvider) ConnectionInfo(context.Context, *Sandbox) (*SandboxConnectionInfo, error) {
	panic("snapshotSessionStubProvider.ConnectionInfo called unexpectedly")
}
func (s *snapshotSessionStubProvider) Restore(context.Context, *Sandbox, io.Reader) error {
	panic("snapshotSessionStubProvider.Restore called unexpectedly")
}
func (s *snapshotSessionStubProvider) ExecStream(context.Context, *Sandbox, string, func([]byte), io.Writer) (int, error) {
	panic("snapshotSessionStubProvider.ExecStream called unexpectedly")
}

// snapshotSessionRecordingStore is a SnapshotStore that records every
// Save call so tests can assert that we did NOT save a corrupt archive.
type snapshotSessionRecordingStore struct {
	saves []string
}

func (s *snapshotSessionRecordingStore) Save(ctx context.Context, key string, reader io.Reader) error {
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return err
	}
	s.saves = append(s.saves, key)
	return nil
}
func (s *snapshotSessionRecordingStore) Load(context.Context, string, io.Writer) error {
	return errors.New("not implemented")
}
func (s *snapshotSessionRecordingStore) Delete(context.Context, string) error { return nil }

func TestSnapshotSessionOnTurnSuccess_SkipsWhenAgentExitedNonZero(t *testing.T) {
	t.Parallel()

	provider := &snapshotSessionStubProvider{}
	store := &snapshotSessionRecordingStore{}
	o := &Orchestrator{
		provider:  provider,
		snapshots: store,
		logger:    zerolog.Nop(),
	}

	session := &models.Session{ID: uuid.New(), OrgID: uuid.New()}
	sandbox := &Sandbox{ID: "sandbox-1"}
	result := &AgentResult{
		ExitCode: 128,
		Error:    `codex CLI exited with code 128: urpc method "containerManager.WaitPID" failed: EOF`,
	}

	key, size, err := o.snapshotSessionOnTurnSuccess(context.Background(), session, sandbox, result, zerolog.Nop())
	require.NoError(t, err, "skipping should not surface as an error — callers log err and continue")
	require.Empty(t, key, "no snapshot key should be returned when we skipped")
	require.Zero(t, size)
	require.Nil(t, session.SnapshotKey, "the prior snapshot pointer must not be touched")
	require.Equal(t, 0, provider.calls(), "provider.Snapshot must not be called for a non-zero-exit run on the success path — that's exactly how the corrupt 298-byte archive was produced")
	require.Empty(t, store.saves, "no Save call should reach storage")
}

func TestSnapshotSessionOnTurnSuccess_SnapshotsWhenAgentExitedClean(t *testing.T) {
	t.Parallel()

	provider := &snapshotSessionStubProvider{
		snapshotFn: func(ctx context.Context, sb *Sandbox) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader([]byte("archive-bytes"))), nil
		},
	}
	store := &snapshotSessionRecordingStore{}
	o := &Orchestrator{
		provider:  provider,
		snapshots: store,
		logger:    zerolog.Nop(),
	}

	session := &models.Session{ID: uuid.New(), OrgID: uuid.New()}
	sandbox := &Sandbox{ID: "sandbox-1"}
	result := &AgentResult{ExitCode: 0}

	key, size, err := o.snapshotSessionOnTurnSuccess(context.Background(), session, sandbox, result, zerolog.Nop())
	require.NoError(t, err)
	require.Equal(t, 1, provider.calls())
	require.Equal(t, []string{key}, store.saves)
	require.NotNil(t, session.SnapshotKey)
	require.Equal(t, key, *session.SnapshotKey)
	require.Equal(t, int64(len("archive-bytes")), size)
}

func TestSnapshotSessionOnTurnSuccess_PassesNilResultThrough(t *testing.T) {
	// nil result has no exit code to check; the wrapper must not block — the
	// underlying snapshotSession is responsible for the rest.
	t.Parallel()

	provider := &snapshotSessionStubProvider{
		snapshotFn: func(ctx context.Context, sb *Sandbox) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader([]byte("nil-result-archive"))), nil
		},
	}
	store := &snapshotSessionRecordingStore{}
	o := &Orchestrator{
		provider:  provider,
		snapshots: store,
		logger:    zerolog.Nop(),
	}
	session := &models.Session{ID: uuid.New(), OrgID: uuid.New()}

	key, _, err := o.snapshotSessionOnTurnSuccess(context.Background(), session, &Sandbox{ID: "sandbox-1"}, nil, zerolog.Nop())
	require.NoError(t, err)
	require.NotEmpty(t, key)
	require.Equal(t, 1, provider.calls())
}

// TestSnapshotSession_AlwaysSnapshotsRegardlessOfExitCode pins the contract
// that the cancel/policy paths rely on: snapshotSession itself is unconditional
// once snapshots is configured. The exit-code guard lives only in the
// snapshotSessionOnTurnSuccess wrapper so graceful stops (where the agent
// exits non-zero on purpose because it caught a signal) still checkpoint.
func TestSnapshotSession_AlwaysSnapshotsRegardlessOfExitCode(t *testing.T) {
	t.Parallel()

	provider := &snapshotSessionStubProvider{
		snapshotFn: func(context.Context, *Sandbox) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader([]byte("graceful-stop-archive"))), nil
		},
	}
	store := &snapshotSessionRecordingStore{}
	o := &Orchestrator{provider: provider, snapshots: store, logger: zerolog.Nop()}

	session := &models.Session{ID: uuid.New(), OrgID: uuid.New()}
	gracefulResult := &AgentResult{ExitCode: 1, Summary: "Interrupted cleanly"}

	key, _, err := o.snapshotSession(context.Background(), session, &Sandbox{ID: "sandbox-1"}, gracefulResult)
	require.NoError(t, err)
	require.NotEmpty(t, key, "policy/cancel paths must still get a checkpoint despite the non-zero exit")
	require.Equal(t, 1, provider.calls())
	require.Equal(t, []string{key}, store.saves)
}

func TestSnapshotSession_NilStoreIsNoOp(t *testing.T) {
	t.Parallel()

	provider := &snapshotSessionStubProvider{}
	o := &Orchestrator{provider: provider, snapshots: nil, logger: zerolog.Nop()}

	key, size, err := o.snapshotSession(context.Background(), &models.Session{ID: uuid.New(), OrgID: uuid.New()}, &Sandbox{ID: "sandbox-1"}, &AgentResult{ExitCode: 0})
	require.NoError(t, err)
	require.Empty(t, key)
	require.Zero(t, size)
	require.Equal(t, 0, provider.calls(), "no provider call should happen when snapshots store is unset")
}

func TestSnapshotSession_PropagatesProviderErrorWithoutUpdatingSession(t *testing.T) {
	t.Parallel()

	provider := &snapshotSessionStubProvider{
		snapshotFn: func(context.Context, *Sandbox) (io.ReadCloser, error) {
			return nil, errors.New("provider boom")
		},
	}
	store := &snapshotSessionRecordingStore{}
	o := &Orchestrator{provider: provider, snapshots: store, logger: zerolog.Nop()}

	session := &models.Session{ID: uuid.New(), OrgID: uuid.New()}
	priorKey := "snapshots/prior/key"
	session.SnapshotKey = &priorKey

	_, _, err := o.snapshotSession(context.Background(), session, &Sandbox{ID: "sandbox-1"}, &AgentResult{ExitCode: 0})
	require.Error(t, err)
	require.Contains(t, err.Error(), "snapshot sandbox")
	require.Equal(t, &priorKey, session.SnapshotKey, "the prior snapshot pointer must remain intact when Snapshot fails")
	require.Empty(t, store.saves, "Save must not be called when Snapshot failed")
}

// TestShedOnRunResultDispatch wires a real AgentEnv with an instrumented
// CodingCredentialProvider and asserts that the orchestrator's
// shedOnRunResult routes a finished run's result.Error to the right
// (ShedRateLimited / ShedAuthRejected / no-op) branch based on the error
// surface. This is the regression guard that prevents a silent shedding
// failure if the helper string matchers ever drift apart from the dispatcher.
func TestShedOnRunResultDispatch(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	credID := uuid.New()

	cases := []struct {
		name             string
		agentType        models.AgentType
		result           *AgentResult
		runErr           error
		wantRateLimited  bool
		wantAuthRejected bool
	}{
		{
			name:             "claude code rate limit error sheds rate limited",
			agentType:        models.AgentTypeClaudeCode,
			result:           &AgentResult{Error: "anthropic api: 429 too many requests"},
			wantRateLimited:  true,
			wantAuthRejected: false,
		},
		{
			name:             "claude code auth rejected error sheds auth rejected",
			agentType:        models.AgentTypeClaudeCode,
			result:           &AgentResult{Error: "401 Unauthorized: refresh_token_reused"},
			wantRateLimited:  false,
			wantAuthRejected: true,
		},
		{
			name:             "claude code clean result is a no-op",
			agentType:        models.AgentTypeClaudeCode,
			result:           &AgentResult{Error: ""},
			wantRateLimited:  false,
			wantAuthRejected: false,
		},
		{
			name:             "codex api key rate limit error sheds rate limited",
			agentType:        models.AgentTypeCodex,
			result:           &AgentResult{Error: "rate limit hit"},
			wantRateLimited:  true,
			wantAuthRejected: false,
		},
		{
			name:             "nil result is a silent no-op",
			agentType:        models.AgentTypeClaudeCode,
			result:           nil,
			wantRateLimited:  false,
			wantAuthRejected: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			provider := codingProviderForAgent(tc.agentType)
			resolvable := map[models.ProviderName][]models.DecryptedCodingCredential{}
			if provider != "" {
				resolvable[provider] = []models.DecryptedCodingCredential{
					{
						ID:       credID,
						OrgID:    orgID,
						UserID:   &userID,
						Provider: provider,
						Status:   models.CodingCredentialStatusActive,
						Config:   testConfigForShedProvider(provider),
					},
				}
			}
			coding := &envCodingCredentialProvider{
				resolvable: resolvable,
			}
			env := NewAgentEnv(AgentEnvDeps{
				CodingCredentials: coding,
				Provider:          &envSandboxProvider{},
				Logger:            zerolog.Nop(),
			})
			// Seed recentPicks for the provider key so the shed lookup resolves
			// to credID. Unknown agent types return an empty provider and skip
			// the seed path.
			if provider != "" {
				_ = env.resolveProviderConfig(context.Background(), orgID, &userID, provider)
			}

			o := &Orchestrator{env: env, logger: zerolog.Nop()}
			o.shedOnRunResult(tc.agentType, orgID, &userID, tc.result, tc.runErr, zerolog.Nop())

			require.Equal(t, tc.wantRateLimited, len(coding.rateLimitedIDs) > 0,
				"rate-limit shedding state mismatch for %q", tc.name)
			require.Equal(t, tc.wantAuthRejected, len(coding.authRejectedIDs) > 0,
				"auth-rejected shedding state mismatch for %q", tc.name)
			if tc.wantRateLimited {
				require.Equal(t, credID, coding.rateLimitedIDs[0],
					"shed call must target the just-picked credential")
			}
			if tc.wantAuthRejected {
				require.Equal(t, credID, coding.authRejectedIDs[0],
					"shed call must target the just-picked credential")
			}
		})
	}
}

func testConfigForShedProvider(provider models.ProviderName) models.ProviderConfig {
	switch provider {
	case models.ProviderAnthropic:
		return models.AnthropicConfig{APIKey: "sk-ant-test-1234"}
	case models.ProviderOpenAI:
		return models.OpenAIConfig{APIKey: "sk-openai-test-1234"}
	case models.ProviderGemini:
		return models.GeminiConfig{APIKey: "gemini-test-1234"}
	case models.ProviderAmp:
		return models.AmpConfig{APIKey: "amp-test-1234"}
	case models.ProviderPi:
		return models.PiConfig{APIKey: "pi-test-1234"}
	default:
		return nil
	}
}

// TestIsRateLimitedError pins the surface that triggers a ShedRateLimited
// call. Drift here would silently turn shedding off on an upstream change in
// provider error wording.
func TestIsRateLimitedError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty string is not rate limited", "", false},
		{"unrelated error is not rate limited", "context canceled", false},
		{"phrase rate limit hit", "anthropic api: rate limit exceeded", true},
		{"snake-case rate_limit code", `{"code":"rate_limit_exceeded"}`, true},
		{"http 429 status", "got status 429 from upstream", true},
		{"too many requests phrase", "Error: too many requests; retry later", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, isRateLimitedError(tc.in))
		})
	}
}

// TestIsAuthRejectedError pins the surface that triggers a ShedAuthRejected
// call. We keep the input lower-cased because the orchestrator does that
// before matching; the test mirrors that contract.
func TestIsAuthRejectedError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty string is not auth rejected", "", false},
		{"unrelated error is not auth rejected", "context canceled", false},
		{"refresh token reused", `error code: refresh_token_reused`, true},
		{"invalid grant", `oauth: invalid_grant`, true},
		{"invalid api key human form", "the provider returned: invalid api key", true},
		{"invalid api key snake-case", `{"error":"invalid_api_key"}`, true},
		{"401 unauthorized", "received 401 unauthorized from upstream", true},
		{"401 unauthenticated", "401 unauthenticated: token expired and refresh failed", true},
		{"authentication_error code", `{"type":"authentication_error"}`, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, isAuthRejectedError(tc.in))
		})
	}
}

// TestCodingProviderForAgent locks the agent-type → provider mapping the
// shed dispatcher uses. Codex maps to OpenAI for API-key auth; subscription
// auth has no recent pick under that provider and remains a no-op.
func TestCodingProviderForAgent(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		agentType models.AgentType
		want      models.ProviderName
	}{
		{"claude code maps to anthropic api key", models.AgentTypeClaudeCode, models.ProviderAnthropic},
		{"gemini cli maps to gemini", models.AgentTypeGeminiCLI, models.ProviderGemini},
		{"amp maps to amp", models.AgentTypeAmp, models.ProviderAmp},
		{"pi maps to pi", models.AgentTypePi, models.ProviderPi},
		{"codex maps to openai api key", models.AgentTypeCodex, models.ProviderOpenAI},
		{"unknown agent type returns empty", models.AgentType("unknown"), ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, codingProviderForAgent(tc.agentType))
		})
	}
}

func TestTruncateForLog(t *testing.T) {
	t.Parallel()

	require.Equal(t, "hello", truncateForLog("hello", 10), "no truncation when under cap")
	require.Equal(t, "hello", truncateForLog("hello", 5), "no truncation when exactly at cap")
	require.Equal(t, "hell…", truncateForLog("hello there", 4), "should truncate and append ellipsis")
	// UTF-8 boundary: "héllo" is 6 bytes (h é l l o, where é is 2 bytes).
	// Cutting at byte 2 lands on the second byte of é; we should back up to
	// byte 1 so we don't emit invalid UTF-8.
	out := truncateForLog("héllo", 2)
	require.Equal(t, "h…", out, "should rewind to a rune boundary before appending the ellipsis")
	require.Equal(t, "x", truncateForLog("x", 0), "non-positive max disables truncation")
}
