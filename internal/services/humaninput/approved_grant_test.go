package humaninput

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agentcapabilities"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type answerGrantBuilder struct {
	item     models.AgentCapabilitySnapshotItem
	identity []uuid.UUID
	calls    int
}

func (a *answerGrantBuilder) BuildApprovedGrant(_ context.Context, orgID, sessionID, requestID uuid.UUID, capability models.AgentCapabilityID, access models.AgentCapabilityAccessLevel) (models.AgentCapabilitySnapshotItem, error) {
	a.calls++
	a.identity = []uuid.UUID{orgID, sessionID, requestID}
	item, err := agentcapabilities.NewService(nil).BuildApprovedGrant(agentcapabilities.ApprovedGrantInput{OrgID: orgID, SessionID: sessionID, HumanInputRequestID: requestID, Capability: capability, AccessLevel: access})
	a.item = item
	return item, err
}

func TestHumanInputApprovedGrantTransactionBoundary(t *testing.T) {
	t.Parallel()
	failure := errors.New("synthetic later resume failure")
	tests := []struct {
		name, stage string
		nilApprover bool
		invalid     bool
		duplicate   bool
	}{
		{name: "approval persists through the answer transaction"},
		{name: "nil capability service retains ordinary resume", nilApprover: true},
		{name: "invalid construction stops before persistence", invalid: true},
		{name: "already granted safe retry", duplicate: true},
		{name: "grant error prevents resume", stage: "grant"},
		{name: "claim error rejects answer and grant", stage: "claim session"},
		{name: "message error rejects answer and grant", stage: "message"},
		{name: "enqueue error rejects answer and grant", stage: "enqueue"},
		{name: "commit error prevents notification", stage: "commit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := newRecoveryRepository(t)
			repo.request.Kind = models.HumanInputRequestKindToolApproval
			repo.request.Choices = []models.HumanInputChoice{{ID: "approve", Label: "Approve"}, {ID: "deny", Label: "Deny"}}
			repo.request.ProviderPayload = json.RawMessage(`{"type":"agent_capability_request","capability_id":"session_history","access_level":"read"}`)
			if tt.invalid {
				repo.request.ProviderPayload = json.RawMessage(`{"type":"agent_capability_request","capability_id":"unknown","access_level":"read"}`)
			}
			if tt.duplicate {
				repo.grantErr = db.ErrCapabilityAlreadyGranted
			}
			repo.failStage, repo.failure = tt.stage, failure
			svc, builder := New(repo), &answerGrantBuilder{}
			if !tt.nilApprover {
				svc.SetCapabilityApprover(builder)
			}
			result, err := svc.Answer(context.Background(), AnswerInput{OrgID: repo.session.OrgID, SessionID: repo.session.ID, RequestID: repo.request.ID, UserID: uuid.New(), Answer: models.HumanInputAnswerInput{SelectedChoiceIDs: []string{"approve"}}})
			if tt.stage != "" || tt.invalid {
				if tt.invalid {
					require.ErrorIs(t, err, agentcapabilities.ErrInvalidGrant, "construction validation must stop the answer before a grant write")
				} else if tt.stage == "claim session" {
					require.ErrorIs(t, err, ErrNotResumable, "claim failure must retain the existing service classification")
				} else {
					require.ErrorIs(t, err, failure, "grant and later persistence failures must reach the transaction owner")
				}
				require.Equal(t, Result{}, result, "rejected transaction boundaries must not return partial success")
				require.False(t, repo.committed, "no rejection may be represented as a committed answer")
				require.Equal(t, uuid.Nil, repo.notifiedJobID, "no rejection may notify a provider resume")
			} else {
				require.NoError(t, err, "valid, duplicate-safe and nil-service paths must retain their existing successful resume behavior")
				require.Equal(t, repo.jobID, result.JobID, "successful answers must return their committed provider resume")
			}
			if tt.nilApprover {
				require.Zero(t, builder.calls, "an unconfigured capability service must not construct grants")
				require.Nil(t, repo.grant, "an unconfigured capability service must not append grants")
			} else {
				require.Equal(t, []uuid.UUID{repo.session.OrgID, repo.session.ID, repo.request.ID}, builder.identity, "grant construction must preserve the exact locked request, session and tenant binding")
				require.Equal(t, 1, builder.calls, "each answer must construct at most the one explicitly approved grant")
				if tt.invalid || tt.duplicate || tt.stage == "grant" {
					require.Nil(t, repo.grant, "invalid, duplicate or rejected appends must not install a replacement grant")
				} else {
					require.Equal(t, &builder.item, repo.grant, "the transaction must receive the exact validated item, including its original timestamp and request ID")
				}
			}
		})
	}
}

func TestHumanInputDBGrantRejectsNilTransaction(t *testing.T) {
	t.Parallel()
	var typedNil *answerGrantGuardTx
	tests := []struct {
		name string
		tx   pgx.Tx
	}{
		{name: "nil interface"}, {name: "typed nil", tx: typedNil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			snapshot, err := newDBTx(tt.tx, nil).AppendApprovedCapabilityGrant(context.Background(), uuid.New(), uuid.New(), models.AgentCapabilitySnapshotItem{})
			require.ErrorContains(t, err, "nil transaction", "the repository must use the explicit transaction seam and never fall back to an independent appender")
			require.Nil(t, snapshot, "missing transactions cannot return an accepted capability snapshot")
		})
	}
}
