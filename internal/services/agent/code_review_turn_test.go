package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakeCodeReviewTurnStore struct {
	owned    bool
	claimErr error
	claimed  int
	usage    map[string]json.RawMessage
}

func (f *fakeCodeReviewTurnStore) Claim(_ context.Context, _, _, _, _, _, _ uuid.UUID, _ int, _ int64) (bool, error) {
	f.claimed++
	return f.owned, f.claimErr
}
func (f *fakeCodeReviewTurnStore) NativeResumeProvider(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) (string, bool, error) {
	return "", false, nil
}
func (f *fakeCodeReviewTurnStore) Complete(context.Context, models.CodeReviewRecheckTurnCompletion) (int64, error) {
	return 1, nil
}
func (f *fakeCodeReviewTurnStore) Fail(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, string) error {
	return nil
}
func (f *fakeCodeReviewTurnStore) RecordAttemptUsage(_ context.Context, _, _, _, _ uuid.UUID, key string, usage json.RawMessage) error {
	if f.usage == nil {
		f.usage = make(map[string]json.RawMessage)
	}
	f.usage[key] = usage
	return nil
}

func TestBeginCodeReviewTurnRequiresExactJobLease(t *testing.T) {
	t.Parallel()
	orgID, sessionID, assessmentID, threadID, jobID, token := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	tests := []struct {
		name       string
		withLease  bool
		owned      bool
		claimErr   error
		wantErr    bool
		wantClaims int
	}{
		{"missing lease", false, true, nil, true, 0},
		{"reclaimed lease", true, false, nil, true, 1},
		{"store error", true, true, errors.New("db unavailable"), true, 1},
		{"owned lease", true, true, nil, false, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := &fakeCodeReviewTurnStore{owned: tt.owned, claimErr: tt.claimErr}
			o := &Orchestrator{codeReviewTurns: store}
			ctx := context.Background()
			if tt.withLease {
				ctx = jobctx.WithJobID(jobctx.WithLockToken(ctx, token), jobID)
			}
			state, err := o.beginCodeReviewTurn(ctx, &models.Session{OrgID: orgID, ID: sessionID}, &CodeReviewTurnContinueOptions{AssessmentID: assessmentID, ThreadID: threadID, ExpectedTurn: 2, MessageID: 7})
			if tt.wantErr {
				require.Error(t, err, "a missing or stale lease must prevent provider launch")
				require.Nil(t, state, "rejected launch must not return turn state")
			} else {
				require.NoError(t, err, "owned job lease should admit the turn")
				require.Equal(t, jobID, state.jobID, "turn state must retain exact job identity")
				require.Equal(t, token, state.lockToken, "turn state must retain exact lock token")
			}
			require.Equal(t, tt.wantClaims, store.claimed, "lease store must only be called with a complete identity")
		})
	}
}

func TestCodeReviewAttemptUsageSeparatesProviderLaunches(t *testing.T) {
	t.Parallel()
	orgID, assessmentID, jobID, token := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	store := &fakeCodeReviewTurnStore{}
	o := &Orchestrator{codeReviewTurns: store}
	state := &codeReviewTurnState{options: CodeReviewTurnContinueOptions{AssessmentID: assessmentID}, jobID: jobID, lockToken: token}
	session := &models.Session{OrgID: orgID}
	require.NoError(t, o.recordCodeReviewAttemptUsage(context.Background(), session, state, "primary", &AgentResult{TokenUsage: TokenUsage{InputTokens: 10, OutputTokens: 2}}), "primary launch should persist observed usage")
	require.NoError(t, o.recordCodeReviewAttemptUsage(context.Background(), session, state, "fallback", nil), "fallback without provider usage should persist unknown usage")
	require.JSONEq(t, `{"input_tokens":10,"output_tokens":2}`, string(store.usage[token.String()+":primary"]), "primary attempt should preserve its own token usage")
	require.Equal(t, "", string(store.usage[token.String()+":fallback"]), "unknown fallback usage should remain unknown at the store boundary")
}
