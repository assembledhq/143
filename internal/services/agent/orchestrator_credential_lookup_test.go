package agent_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

type transientExecutionCredentialProvider struct {
	*mockCodingCredentialProvider
	guard           sync.Mutex
	lookupError     error
	successfulPicks int
	picks           int
	lookupFailed    bool
}

func (p *transientExecutionCredentialProvider) PickRunnableMulti(ctx context.Context, scope models.Scope, providers []models.ProviderName) (*models.DecryptedCodingCredential, error) {
	p.guard.Lock()
	defer p.guard.Unlock()
	if p.picks >= p.successfulPicks {
		p.lookupFailed = true
		return nil, p.lookupError
	}
	p.picks++
	return p.mockCodingCredentialProvider.PickRunnableMulti(ctx, scope, providers)
}

func (p *transientExecutionCredentialProvider) ListResolvable(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName) ([]models.DecryptedCodingCredential, error) {
	p.guard.Lock()
	defer p.guard.Unlock()
	if p.lookupFailed || p.successfulPicks == 0 {
		return nil, p.lookupError
	}
	return p.mockCodingCredentialProvider.ListResolvable(ctx, orgID, userID, provider)
}

func TestExecutionCredentialLookupFailureRespectsExecutionBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		agentType       models.AgentType
		continuation    bool
		successfulPicks int
	}{
		{name: "initial Codex lookup", agentType: models.AgentTypeCodex},
		{name: "initial Claude lookup", agentType: models.AgentTypeClaudeCode},
		{name: "continued Codex lookup", agentType: models.AgentTypeCodex, continuation: true},
		{name: "continued Claude lookup", agentType: models.AgentTypeClaudeCode, continuation: true},
		{name: "Codex fallback lookup", agentType: models.AgentTypeCodex, successfulPicks: 1},
		{name: "Claude fallback lookup", agentType: models.AgentTypeClaudeCode, successfulPicks: 1},
		{name: "continued Codex fallback lookup", agentType: models.AgentTypeCodex, continuation: true, successfulPicks: 1},
		{name: "continued Claude fallback lookup", agentType: models.AgentTypeClaudeCode, continuation: true, successfulPicks: 1},
		{name: "Codex post-fallback probe lookup", agentType: models.AgentTypeCodex, successfulPicks: 2},
		{name: "Claude post-fallback probe lookup", agentType: models.AgentTypeClaudeCode, successfulPicks: 2},
		{name: "continued Codex post-fallback probe lookup", agentType: models.AgentTypeCodex, continuation: true, successfulPicks: 2},
		{name: "continued Claude post-fallback probe lookup", agentType: models.AgentTypeClaudeCode, continuation: true, successfulPicks: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID, userID := testOrg(), uuid.New()
			issue := testIssue(orgID)
			session := testRun(orgID, issue.ID)
			session.AgentType, session.TriggeredByUserID = tt.agentType, &userID
			providerName := models.ProviderOpenAI
			var firstConfig, secondConfig models.ProviderConfig = models.OpenAIConfig{APIKey: "synthetic-key-a"}, models.OpenAIConfig{APIKey: "synthetic-key-b"}
			if tt.agentType == models.AgentTypeClaudeCode {
				providerName = models.ProviderAnthropic
				firstConfig, secondConfig = models.AnthropicConfig{APIKey: "synthetic-key-a"}, models.AnthropicConfig{APIKey: "synthetic-key-b"}
			}
			credentials := []models.DecryptedCodingCredential{
				{ID: uuid.New(), OrgID: orgID, UserID: &userID, Provider: providerName, Status: models.CodingCredentialStatusActive, Priority: 1, Config: firstConfig},
				{ID: uuid.New(), OrgID: orgID, UserID: &userID, Provider: providerName, Status: models.CodingCredentialStatusActive, Priority: 2, Config: secondConfig},
			}
			lookupErr := errors.New("synthetic credential database unavailable")
			coding := &transientExecutionCredentialProvider{mockCodingCredentialProvider: &mockCodingCredentialProvider{resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{providerName: credentials}}, lookupError: lookupErr, successfulPicks: tt.successfulPicks}
			d := defaultDeps()
			d.codingCreds, d.issues.issue = coding, issue
			d.adapter = &mockAgentAdapter{name: tt.agentType}
			if tt.continuation {
				session.Status, session.CurrentTurn = models.SessionStatusIdle, 1
				d.messages.messages = []models.SessionMessage{{ID: 1, SessionID: session.ID, OrgID: orgID, TurnNumber: 2, Role: models.MessageRoleUser, Content: "Continue the synthetic task."}}
			}
			executions := 0
			d.adapter.executeFn = func(context.Context, *agent.Sandbox, *agent.AgentPrompt, chan<- agent.LogEntry) (*agent.AgentResult, error) {
				executions++
				return &agent.AgentResult{Error: "429 too many requests", ExitCode: 1}, nil
			}
			ctx := jobctx.WithDeadLetterHooks(context.Background())
			orchestrator := buildOrchestrator(d)
			var err error
			if tt.continuation {
				err = orchestrator.ContinueSession(ctx, session, nil)
			} else {
				err = orchestrator.RunAgent(ctx, session)
			}

			require.Equal(t, tt.successfulPicks, executions, "no CLI may run with a failed or unbound credential lookup")
			require.Empty(t, d.sessions.getFailureUpdates(), "lookup failure must not write permanent missing-credential failure metadata")
			for _, message := range d.messages.getMessages() {
				require.NotEqual(t, models.MessageRoleAssistant, message.Role, "lookup errors must not publish missing-credential or reconnect guidance")
			}
			if tt.successfulPicks == 0 {
				require.ErrorIs(t, err, lookupErr, "a lookup failure before any CLI starts should retain its transient infrastructure cause")
				require.Empty(t, d.sessions.getResultUpdates(), "pre-execution lookup retries must not terminalize the session inline")
				jobctx.RunDeadLetterHooks(ctx, err)
				failures := d.sessions.getResultsWithFailureCategory(agent.FailureCategoryTooling)
				require.Len(t, failures, 1, "exhausted pre-execution lookup retries should publish one infrastructure failure")
				require.Equal(t, "Coding credentials could not be loaded because of a temporary infrastructure error. Retry the session.", *failures[0].result.FailureExplanation, "exhausted lookup failures should explain the infrastructure failure without blaming credentials")
				require.Equal(t, []string{"Retry the session when the credential service is available"}, failures[0].result.FailureNextSteps, "credential infrastructure errors should provide retry guidance rather than reconnect guidance")
			} else {
				require.ErrorContains(t, err, "429 too many requests", "a lookup failure after execution must preserve the executed attempt's rate-limit outcome")
				require.NotErrorIs(t, err, lookupErr, "an auxiliary lookup must not turn an already-executed turn into a replayable startup error")
				updates := d.sessions.getResultUpdates()
				require.Len(t, updates, 1, "the already-executed turn must settle through its normal terminal result path")
				require.Equal(t, models.SessionStatusFailed, updates[0].status, "the executed rate-limit failure should be durable before returning to the worker")
				require.Equal(t, "429 too many requests", *updates[0].result.Error, "settlement must retain the executed provider error")
				require.Equal(t, tt.successfulPicks, len(coding.rateLimitedIDs), "only credentials whose CLIs ran may receive the preceding rate-limit health signal")
				jobctx.RunDeadLetterHooks(ctx, err)
				require.Len(t, d.sessions.getResultUpdates(), 1, "post-execution lookups must not register a competing infrastructure exhaustion hook")
				require.Empty(t, d.sessions.getResultsWithFailureCategory(agent.FailureCategoryTooling), "a post-execution lookup must not overwrite the settled provider result with a startup failure")
			}
		})
	}
}

func TestCredentialLookupDeadlineIsNotMissingCredential(t *testing.T) {
	t.Parallel()

	// Expired rows are not needed to reproduce a lookup outage; an ordinary
	// configured credential must remain configured despite infrastructure loss.
	orgID := testOrg()
	issue := testIssue(orgID)
	session := testRun(orgID, issue.ID)
	session.AgentType = models.AgentTypeCodex
	coding := &transientExecutionCredentialProvider{mockCodingCredentialProvider: &mockCodingCredentialProvider{resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{}}, lookupError: context.DeadlineExceeded}
	d := defaultDeps()
	d.codingCreds, d.issues.issue = coding, issue
	d.adapter = &mockAgentAdapter{name: models.AgentTypeCodex}
	d.adapter.executeFn = func(context.Context, *agent.Sandbox, *agent.AgentPrompt, chan<- agent.LogEntry) (*agent.AgentResult, error) {
		t.Fatal("a credential lookup deadline must not invoke the CLI")
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	err := buildOrchestrator(d).RunAgent(ctx, session)

	require.ErrorIs(t, err, context.DeadlineExceeded, "credential lookup context failures should retain their retryable cause")
	require.Empty(t, d.sessions.getFailureUpdates(), "lookup context failure must not become a permanent missing-credential failure")
}

func TestReviewCredentialLookupFailureDoesNotRegisterParentFailureHook(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		recheck bool
	}{
		{name: "ordinary reviewer thread"},
		{name: "lease-fenced recheck thread", recheck: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID, threadID, primaryThreadID, siblingThreadID := testOrg(), uuid.New(), uuid.New(), uuid.New()
			issue := testIssue(orgID)
			session := testRun(orgID, issue.ID)
			session.Origin = models.SessionOriginCodeReview
			session.AgentType = models.AgentTypeCodex
			session.Status, session.CurrentTurn = models.SessionStatusRunning, 1
			session.PrimaryThreadID = &primaryThreadID
			lookupErr := errors.New("synthetic credential database unavailable")
			coding := &transientExecutionCredentialProvider{mockCodingCredentialProvider: &mockCodingCredentialProvider{resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{}}, lookupError: lookupErr}
			d := defaultDeps()
			d.codingCreds, d.issues.issue = coding, issue
			d.adapter = &mockAgentAdapter{name: session.AgentType}
			d.adapter.executeFn = func(context.Context, *agent.Sandbox, *agent.AgentPrompt, chan<- agent.LogEntry) (*agent.AgentResult, error) {
				t.Fatal("a review credential lookup failure must not invoke the CLI")
				return nil, nil
			}
			d.sessionThreads = &mockSessionThreadStore{getByIDResult: &models.SessionThread{ID: threadID, SessionID: session.ID, OrgID: orgID}}
			messageID := int64(7)
			d.messages.messages = []models.SessionMessage{{ID: messageID, SessionID: session.ID, OrgID: orgID, ThreadID: &threadID, TurnNumber: 2, Role: models.MessageRoleUser, Content: "Run the synthetic review."}}
			jobID := uuid.New()
			ctx := jobctx.WithJobID(jobctx.WithDeadLetterHooks(context.Background()), jobID)
			opts := &agent.ContinueSessionOptions{ThreadID: &threadID, QueuedMessageID: &messageID}
			orchestrator := buildOrchestrator(d)
			if tt.recheck {
				ctx = jobctx.WithLockToken(ctx, uuid.New())
				opts.CodeReviewTurn = &agent.CodeReviewTurnContinueOptions{AssessmentID: uuid.New(), ThreadID: threadID, ExpectedTurn: 2, MessageID: messageID}
				orchestrator.SetCodeReviewTurnStore(credentialRecheckTurnStore{})
			}

			err := orchestrator.ContinueSession(ctx, session, opts)
			require.NotContains(t, d.sessionThreads.statuses(), models.ThreadStatusFailed, "lookup retries must not terminalize the review thread before exhaustion")
			jobctx.RunDeadLetterHooks(ctx, err)

			require.ErrorIs(t, err, lookupErr, "review retry should preserve the actual transient lookup cause")
			require.Empty(t, d.sessions.getResultUpdates(), "review exhaustion hooks must not fail the parent session outside its controller")
			require.Empty(t, d.sessions.getFailureUpdates(), "review credential infrastructure failures must not register permanent auth failures")
			require.NotContains(t, d.sessionThreads.statusesForThread(primaryThreadID), models.ThreadStatusFailed, "review exhaustion must leave the primary orchestrator thread untouched")
			require.NotContains(t, d.sessionThreads.statusesForThread(siblingThreadID), models.ThreadStatusFailed, "review exhaustion must leave sibling reviewer threads untouched")
			expectedCalls := []credentialLookupDeadLetterCall(nil)
			if !tt.recheck {
				expectedCalls = []credentialLookupDeadLetterCall{{orgID: orgID, sessionID: session.ID, threadID: threadID, jobID: jobID, expectedTurn: 2, detail: "Coding credentials could not be loaded because of a temporary infrastructure error. Retry the session."}}
			}
			require.Equal(t, expectedCalls, d.sessionThreads.credentialLookupDeadLetters, "ordinary review exhaustion must target its exact job/thread/turn through the guarded store; rechecks remain receipt-owned")
			require.Empty(t, d.sessionThreads.resultsForThread(threadID), "lookup exhaustion must never use an unfenced thread result update")
		})
	}
}
