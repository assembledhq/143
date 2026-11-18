package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

type fallbackHarvestWrite struct {
	Scope        models.Scope
	CredentialID uuid.UUID
	Subscription models.AnthropicSubscription
}

type fallbackHarvestClaudeAuth struct {
	mockClaudeCodeAuthProvider
	refreshError error
	writes       []fallbackHarvestWrite
}

func (p *fallbackHarvestClaudeAuth) RefreshTokenByID(context.Context, models.Scope, uuid.UUID) (*models.AnthropicSubscription, error) {
	return nil, p.refreshError
}

func (p *fallbackHarvestClaudeAuth) StoreTokenByID(_ context.Context, scope models.Scope, credentialID uuid.UUID, subscription models.AnthropicSubscription) (bool, error) {
	p.writes = append(p.writes, fallbackHarvestWrite{Scope: scope, CredentialID: credentialID, Subscription: subscription})
	return true, nil
}

func TestRunAgent_CredentialFallbackHarvestFollowsExecutedAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		initialAPIKey   bool
		fallbackAPIKey  bool
		fallbackRefresh bool
	}{
		{name: "failed fallback auth never harvests the original subscription file", fallbackRefresh: true},
		{name: "API key to subscription harvests the executed fallback credential", initialAPIKey: true},
		{name: "subscription to API key uses the fallback auth mode", fallbackAPIKey: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID, userID := testOrg(), uuid.New()
			issue := testIssue(orgID)
			run := testRun(orgID, issue.ID)
			run.AgentType = models.AgentTypeClaudeCode
			run.TriggeredByUserID = &userID
			firstID, fallbackID := uuid.New(), uuid.New()
			expiry := time.Now().Add(time.Hour).Truncate(time.Millisecond)
			first := models.DecryptedCodingCredential{
				ID: firstID, OrgID: orgID, UserID: &userID, Provider: models.ProviderAnthropicSubscription,
				Status: models.CodingCredentialStatusActive, Priority: 1,
				Config: models.AnthropicSubscriptionConfig{AccessToken: "synthetic-access-a", RefreshToken: "synthetic-refresh-a", ExpiresAt: expiry},
			}
			if tt.initialAPIKey {
				first.Provider = models.ProviderAnthropic
				first.Config = models.AnthropicConfig{APIKey: "synthetic-api-key-a"}
			}
			fallbackExpiry := expiry
			if tt.fallbackRefresh {
				fallbackExpiry = time.Now().Add(-time.Minute)
			}
			fallback := models.DecryptedCodingCredential{
				ID: fallbackID, OrgID: orgID, UserID: &userID, Provider: models.ProviderAnthropicSubscription,
				Status: models.CodingCredentialStatusActive, Priority: 2,
				Config: models.AnthropicSubscriptionConfig{AccessToken: "synthetic-access-b", RefreshToken: "synthetic-refresh-b", ExpiresAt: fallbackExpiry},
			}
			if tt.fallbackAPIKey {
				fallback.Provider = models.ProviderAnthropic
				fallback.Config = models.AnthropicConfig{APIKey: "synthetic-api-key-b"}
			}
			coding := &mockCodingCredentialProvider{resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{}}
			for _, credential := range []models.DecryptedCodingCredential{first, fallback} {
				coding.resolvable[credential.Provider] = append(coding.resolvable[credential.Provider], credential)
			}
			auth := &fallbackHarvestClaudeAuth{refreshError: errors.New("synthetic fallback refresh rejected")}
			d := defaultDeps()
			d.codingCreds, d.claudeCodeAuth, d.issues.issue = coding, auth, issue
			refreshed := models.AnthropicSubscription{AccessToken: "synthetic-rotated-access-b", RefreshToken: "synthetic-rotated-refresh-b", ExpiresAt: expiry.Add(time.Hour)}
			refreshedJSON, err := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": refreshed.AccessToken, "refreshToken": refreshed.RefreshToken, "expiresAt": refreshed.ExpiresAt.UnixMilli()}})
			require.NoError(t, err, "synthetic CLI-refreshed tokens should marshal")
			executions := 0
			d.adapter.executeFn = func(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, _ chan<- agent.LogEntry) (*agent.AgentResult, error) {
				executions++
				if executions == 1 {
					return &agent.AgentResult{ExitCode: 1, Error: "429 too many requests"}, nil
				}
				require.False(t, tt.fallbackRefresh, "a fallback whose auth preparation failed must never execute")
				if tt.fallbackAPIKey {
					require.Equal(t, agent.TokenBillingModeAPIKey, prompt.UsageHint.BillingMode, "a subscription-to-API fallback must replace the prior attempt's billing mode")
				} else {
					require.Equal(t, agent.TokenBillingModeSubscription, prompt.UsageHint.BillingMode, "an API-to-subscription fallback must replace the prior attempt's billing mode")
					require.NoError(t, d.provider.WriteFile(ctx, sandbox, "/home/sandbox/.claude/.credentials.json", refreshedJSON), "the executed fallback CLI should be able to rotate its synthetic token file")
				}
				return &agent.AgentResult{Summary: "completed with fallback credential"}, nil
			}

			err = buildOrchestrator(d).RunAgent(context.Background(), run)

			if tt.fallbackRefresh {
				require.ErrorContains(t, err, "synthetic fallback refresh rejected", "a failed fallback refresh should fail without invoking its CLI")
				require.Equal(t, 1, executions, "failed fallback auth should leave only the original credential execution")
				require.Equal(t, []fallbackHarvestWrite(nil), auth.writes, "an original A auth file must never be stored under a fallback B that failed auth preparation")
				var actual map[string]any
				require.NoError(t, json.Unmarshal(d.provider.Files["/home/sandbox/.claude/.credentials.json"], &actual), "the unchanged original credential file should remain valid JSON")
				require.Equal(t, "synthetic-access-a", actual["claudeAiOauth"].(map[string]any)["accessToken"], "the failure fixture should retain A's file to expose any attempted wrong-row harvest")
			} else {
				require.NoError(t, err, "an API-key run should complete through a subscription fallback")
				require.Equal(t, 2, executions, "the subscription fallback should execute exactly once")
				expectedWrites := []fallbackHarvestWrite(nil)
				if !tt.fallbackAPIKey {
					expectedWrites = []fallbackHarvestWrite{{Scope: models.Scope{OrgID: orgID, UserID: &userID}, CredentialID: fallbackID, Subscription: refreshed}}
				}
				require.Equal(t, expectedWrites, auth.writes, "fallback harvesting should use B's actual auth kind, exact row, and personal scope")
			}
		})
	}
}

type credentialRecheckRuntimeStore struct {
	activeRuntimeConflictStore
}

func (s *credentialRecheckRuntimeStore) CreateStarting(_ context.Context, orgID uuid.UUID, params db.CreateThreadRuntimeParams) (models.ThreadRuntime, error) {
	return models.ThreadRuntime{ID: uuid.New(), OrgID: orgID, SessionID: params.SessionID, ThreadID: params.ThreadID, LeaseToken: params.LeaseToken, Status: models.ThreadRuntimeStatusStarting}, nil
}

type credentialRecheckTurnStore struct {
	agent.CodeReviewTurnStore
}

func (s credentialRecheckTurnStore) Claim(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, int, int64) (bool, error) {
	return true, nil
}

func (s credentialRecheckTurnStore) NativeResumeProvider(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) (string, bool, error) {
	return "", false, nil
}

func (s credentialRecheckTurnStore) RecordAttemptUsage(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, string, json.RawMessage) error {
	return nil
}

type credentialRecheckInboxStore struct {
	agent.ThreadInboxStore
}

func (s credentialRecheckInboxStore) ListAppliedMessageIDs(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, []int64) ([]int64, error) {
	return nil, nil
}

func (s credentialRecheckInboxStore) LastAcknowledgedSequence(context.Context, uuid.UUID, uuid.UUID) (int64, error) {
	return 0, nil
}

func (s credentialRecheckInboxStore) MarkDeliveringForMessages(_ context.Context, _, _, _, _ uuid.UUID, _ string, messageIDs []int64) (int64, error) {
	return int64(len(messageIDs)), nil
}

func (s credentialRecheckInboxStore) MarkAckedForSeedMessages(_ context.Context, _, _, _, _ uuid.UUID, messageIDs []int64) (int64, error) {
	return int64(len(messageIDs)), nil
}

func (s credentialRecheckInboxStore) ClaimDeliverableAfter(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, string, int64, int) ([]models.ThreadInboxEntry, error) {
	return nil, nil
}

func (s credentialRecheckInboxStore) ListUnsettledAppliedMessageIDs(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, []int64) ([]int64, error) {
	return nil, nil
}

func TestContinueSession_RecheckRateLimitShedsExecutedCredentialWithoutGenericRetry(t *testing.T) {
	t.Parallel()

	orgID, userID, threadID := testOrg(), uuid.New(), uuid.New()
	issue := testIssue(orgID)
	issue.RepositoryID = nil
	session := testRun(orgID, issue.ID)
	session.AgentType = models.AgentTypeAmp
	// Exercise the lease-fenced recheck mode without repository preparation;
	// workspace source-integrity contracts have separate integration coverage.
	session.RepositoryID = nil
	session.Status = models.SessionStatusIdle
	session.CurrentTurn = 1
	session.TriggeredByUserID, session.PrimaryThreadID = &userID, &threadID
	credential := models.DecryptedCodingCredential{ID: uuid.New(), OrgID: orgID, UserID: &userID, Provider: models.ProviderAmp, Status: models.CodingCredentialStatusActive, Config: models.AmpConfig{APIKey: "synthetic-recheck-key"}}
	coding := &mockCodingCredentialProvider{resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{models.ProviderAmp: {credential}}}
	d := defaultDeps()
	d.adapter = &mockAgentAdapter{name: models.AgentTypeAmp}
	d.codingCreds, d.issues.issue = coding, issue
	d.threadRuntimes = &credentialRecheckRuntimeStore{}
	d.threadInbox = credentialRecheckInboxStore{}
	d.sessionThreads = &mockSessionThreadStore{getByIDResult: &models.SessionThread{ID: threadID, SessionID: session.ID, OrgID: orgID}}
	messageID := int64(7)
	d.messages.messages = []models.SessionMessage{{ID: messageID, SessionID: session.ID, OrgID: orgID, ThreadID: &threadID, TurnNumber: 2, Role: models.MessageRoleUser, Content: "Perform the synthetic recheck."}}
	executions := 0
	resetAt := time.Now().Add(time.Hour)
	d.adapter.executeFn = func(context.Context, *agent.Sandbox, *agent.AgentPrompt, chan<- agent.LogEntry) (*agent.AgentResult, error) {
		executions++
		return &agent.AgentResult{Error: "429 too many requests", ExitCode: 1, CredentialFailure: &agent.CredentialFailureSignal{RateLimited: true, RateLimitedUntil: resetAt}}, nil
	}
	orchestrator := buildOrchestrator(d)
	orchestrator.SetCodeReviewTurnStore(credentialRecheckTurnStore{})
	ctx := jobctx.WithJobID(jobctx.WithLockToken(context.Background(), uuid.New()), uuid.New())

	err := orchestrator.ContinueSession(ctx, session, &agent.ContinueSessionOptions{ThreadID: &threadID, QueuedMessageID: &messageID, CodeReviewTurn: &agent.CodeReviewTurnContinueOptions{AssessmentID: uuid.New(), ThreadID: threadID, ExpectedTurn: 2, MessageID: messageID}})

	require.ErrorContains(t, err, "code review recheck turn did not complete successfully", "the lease-fenced worker should retain ownership of recheck failure bookkeeping")
	require.Equal(t, 1, executions, "a recheck rate limit must not enable the ordinary session's generic retry")
	require.Equal(t, map[uuid.UUID]models.CodingCredentialRateLimit{credential.ID: {Until: resetAt, Message: "429 too many requests"}}, coding.rateLimitedIDs, "a recheck rate limit should mark only the exact executed credential with the provider reset time")
}
