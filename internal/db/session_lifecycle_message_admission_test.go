package db

import (
	"encoding/json"
	"errors"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestMessageAdmissionDecision(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		change   func(*lockedLifecycleScope, *messageAdmissionState)
		comments bool
		want     messageAdmissionDecision
		cause    error
	}{
		{name: "idle new job", want: messageAdmissionDecision{disposition: MessageAdmissionNewJob, turn: 1}},
		{name: "completed is resumable", change: func(s *lockedLifecycleScope, v *messageAdmissionState) {
			s.session.status = models.SessionStatusCompleted
			v.status = models.ThreadStatusCompleted
		}, want: messageAdmissionDecision{disposition: MessageAdmissionNewJob, turn: 1}},
		{name: "failed is resumable", change: func(s *lockedLifecycleScope, v *messageAdmissionState) {
			s.session.status = models.SessionStatusFailed
			v.status = models.ThreadStatusFailed
		}, want: messageAdmissionDecision{disposition: MessageAdmissionNewJob, turn: 1}},
		{name: "cancelled is resumable", change: func(s *lockedLifecycleScope, v *messageAdmissionState) {
			s.session.status = models.SessionStatusCancelled
			v.status = models.ThreadStatusCancelled
		}, want: messageAdmissionDecision{disposition: MessageAdmissionNewJob, turn: 1}},
		{name: "pending queues next turn", change: func(_ *lockedLifecycleScope, v *messageAdmissionState) {
			v.status = models.ThreadStatusPending
			v.currentTurn = 4
		}, want: messageAdmissionDecision{disposition: MessageAdmissionQueued, turn: 6, reason: "lane_running"}},
		{name: "running queues next turn", change: func(_ *lockedLifecycleScope, v *messageAdmissionState) {
			v.status = models.ThreadStatusRunning
			v.currentTurn = 8
		}, want: messageAdmissionDecision{disposition: MessageAdmissionQueued, turn: 10, reason: "lane_running"}},
		{name: "three slots make fourth wait", change: func(s *lockedLifecycleScope, _ *messageAdmissionState) {
			for i := 0; i < 3; i++ {
				s.threads = append(s.threads, lifecycleLockedThread{id: uuid.New(), status: models.ThreadStatusRunning})
			}
		}, want: messageAdmissionDecision{disposition: MessageAdmissionWaiting, turn: 1, reason: "capacity"}},
		{name: "two slots permit third", change: func(s *lockedLifecycleScope, _ *messageAdmissionState) {
			for i := 0; i < 2; i++ {
				s.threads = append(s.threads, lifecycleLockedThread{id: uuid.New(), status: models.ThreadStatusRunning})
			}
		}, want: messageAdmissionDecision{disposition: MessageAdmissionNewJob, turn: 1}},
		{name: "hidden active sibling does not consume slot", change: func(s *lockedLifecycleScope, _ *messageAdmissionState) {
			at := time.Now()
			for i := 0; i < 3; i++ {
				s.threads = append(s.threads, lifecycleLockedThread{id: uuid.New(), status: models.ThreadStatusRunning, archivedAt: &at})
			}
		}, want: messageAdmissionDecision{disposition: MessageAdmissionNewJob, turn: 1}},
		{name: "parent aggregate awaiting does not veto independent lane", change: func(s *lockedLifecycleScope, _ *messageAdmissionState) {
			s.session.status = models.SessionStatusAwaitingInput
		}, want: messageAdmissionDecision{disposition: MessageAdmissionNewJob, turn: 1}},
		{name: "pending target or whole request never answered", change: func(_ *lockedLifecycleScope, v *messageAdmissionState) { v.pendingAnswer = true }, cause: ErrMessageAdmissionAwaitingInput},
		{name: "parent awaiting plus pending target never answered", change: func(s *lockedLifecycleScope, v *messageAdmissionState) {
			s.session.status, v.pendingAnswer = models.SessionStatusAwaitingInput, true
		}, cause: ErrMessageAdmissionAwaitingInput},
		{name: "lane awaiting input never answers", change: func(_ *lockedLifecycleScope, v *messageAdmissionState) { v.status = models.ThreadStatusAwaitingInput }, cause: ErrMessageAdmissionAwaitingInput},
		{name: "parent explicit end requires B3", change: func(s *lockedLifecycleScope, _ *messageAdmissionState) {
			s.session.epochs.EndEpoch = cloneMessageAdmissionPointer(new(int64))
		}, cause: ErrMessageAdmissionConflict},
		{name: "lane explicit end requires B3", change: func(s *lockedLifecycleScope, _ *messageAdmissionState) {
			s.threads[0].epochs.EndEpoch = cloneMessageAdmissionPointer(new(int64))
		}, cause: ErrMessageAdmissionConflict},
		{name: "negative turn refuses", change: func(_ *lockedLifecycleScope, v *messageAdmissionState) { v.currentTurn = -1 }, cause: errMessageAdmissionTestAny},
		{name: "claim overflow refuses", change: func(_ *lockedLifecycleScope, v *messageAdmissionState) { v.currentTurn = math.MaxInt32 }, cause: errMessageAdmissionTestAny},
		{name: "queue overflow refuses", change: func(_ *lockedLifecycleScope, v *messageAdmissionState) {
			v.status = models.ThreadStatusRunning
			v.currentTurn = math.MaxInt32 - 1
		}, cause: errMessageAdmissionTestAny},
		{name: "last claim turn fits", change: func(_ *lockedLifecycleScope, v *messageAdmissionState) { v.currentTurn = math.MaxInt32 - 1 }, want: messageAdmissionDecision{disposition: MessageAdmissionNewJob, turn: math.MaxInt32}},
		{name: "comments on running refuse", comments: true, change: func(_ *lockedLifecycleScope, v *messageAdmissionState) { v.status = models.ThreadStatusRunning }, cause: ErrMessageAdmissionBusy},
		{name: "comments without slot refuse", comments: true, change: func(s *lockedLifecycleScope, _ *messageAdmissionState) {
			for i := 0; i < 3; i++ {
				s.threads = append(s.threads, lifecycleLockedThread{id: uuid.New(), status: models.ThreadStatusRunning})
			}
		}, cause: ErrMessageAdmissionBusy},
		{name: "idle comments can bind", comments: true, want: messageAdmissionDecision{disposition: MessageAdmissionNewJob, turn: 1}},
		{name: "active repair is accepted for reconciliation not refused", change: func(_ *lockedLifecycleScope, v *messageAdmissionState) { v.repairReconciliation = true }, want: messageAdmissionDecision{disposition: MessageAdmissionWaiting, turn: 1, reason: "repair_reconciliation"}},
		{name: "running repair wait preserves message turn", change: func(_ *lockedLifecycleScope, v *messageAdmissionState) {
			v.status, v.currentTurn, v.repairReconciliation = models.ThreadStatusRunning, 8, true
		}, want: messageAdmissionDecision{disposition: MessageAdmissionWaiting, turn: 10, reason: "repair_reconciliation"}},
		{name: "repair wait cannot resolve comments", comments: true, change: func(_ *lockedLifecycleScope, v *messageAdmissionState) { v.repairReconciliation = true }, cause: ErrMessageAdmissionBusy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			lane := uuid.New()
			scope := lockedLifecycleScope{session: lifecycleLockedSession{status: models.SessionStatusIdle}, threads: []lifecycleLockedThread{{id: lane, status: models.ThreadStatusIdle}}}
			state := messageAdmissionState{status: models.ThreadStatusIdle}
			if tt.change != nil {
				tt.change(&scope, &state)
			}
			actual, err := decideMessageAdmission(scope, state, lane, tt.comments)
			if tt.cause != nil {
				if tt.cause == errMessageAdmissionTestAny {
					require.Error(t, err, "invalid turn must not narrow or wrap into an admitted turn")
				} else {
					require.ErrorIs(t, err, tt.cause, "strict non-answer and explicit-end policy must refuse without a success variant")
				}
				require.Equal(t, messageAdmissionDecision{}, actual, "refusal must expose no partial accepted disposition")
			} else {
				require.NoError(t, err, "eligible ordinary input must preserve its exact queue and capacity semantics")
				require.Equal(t, tt.want, actual, "decision must preserve exact outcome turn attribution and wait reason")
			}
		})
	}
}

var errMessageAdmissionTestAny = errors.New("any expected admission validation error")

func newMessageAdmissionReceiptFixture(t *testing.T) (MessageAdmission, Admission) {
	t.Helper()
	input := newMessageAdmissionCommandFixture(t)
	command, err := NewMessageAdmission(input)
	require.NoError(t, err, "receipt fixture command must retain a real semantic ingress")
	dispatch, job, token, inboxID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	lane, _ := input.Fence.ThreadID()
	client := "client-key"
	at := input.Dispatch.RunAt
	pass := 1
	message := models.SessionMessage{ID: 19, OrgID: input.OrgID, SessionID: input.Fence.sessionID, ThreadID: &lane, UserID: input.ActorID, TurnNumber: 1, Role: models.MessageRoleUser, Content: "[PLAN_MODE]\n" + input.Message,
		Attachments: input.Images, References: input.References, Commands: input.Commands, Source: input.Source, CreatedAt: at, TokenUsage: json.RawMessage(`{"input":1}`)}
	inbox := models.ThreadInboxEntry{ID: inboxID, OrgID: input.OrgID, SessionID: input.Fence.sessionID, ThreadID: lane, MessageID: message.ID, ClientMessageID: &client, EntryType: models.ThreadInboxEntryTypeUserMessage,
		Payload: json.RawMessage(`{"message_id":19}`), DeliveryState: models.ThreadInboxDeliveryStatePending, AcceptedAt: at, CreatedAt: at, UpdatedAt: at, DeliveredAt: &at, AckedAt: &at, AppliedAt: &at, RuntimeID: &token, LastError: &client, OwnerNodeID: &client}
	authority, err := NewAdmissionAuthority(input.OrgID, input.Fence, dispatch, token, 1, input.Ingress)
	require.NoError(t, err, "receipt fixture needs only the exact stamped logical claim identity")
	a := Admission{orgID: input.OrgID, fence: input.Fence, ingress: input.Ingress, dispatchID: dispatch, disposition: MessageAdmissionNewJob, original: MessageAdmissionNewJob, message: cloneMessageAdmissionMessage(message), inbox: cloneMessageAdmissionInbox(inbox),
		comments: []models.SessionReviewComment{{ID: input.ResolveReviewCommentIDs[0], OrgID: input.OrgID, SessionID: input.Fence.sessionID, Resolved: true, ResolvedAt: &at, ResolvedByPass: &pass}}, jobID: job, authority: authority, options: input.Dispatch,
		holderID: cloneMessageAdmissionPointer(input.LeaseHolderID), targetNodeID: &client, threadState: models.ThreadStatusRunning, valid: true}
	require.NoError(t, a.validateLifecycleReceipt(), "synthetic receipt must satisfy all exact-scope associations before mutation tests")
	return command, a
}

func TestMessageAdmissionReceiptDefensiveGetters(t *testing.T) {
	t.Parallel()
	_, a := newMessageAdmissionReceiptFixture(t)
	wantMessage, wantInbox, wantComments := a.Message(), a.InboxEntry(), a.ResolvedComments()
	message := a.Message()
	*message.ThreadID = uuid.New()
	*message.UserID = uuid.New()
	message.Attachments[0] = "changed"
	message.References[0].Path = "changed"
	message.Commands[0].Name = "changed"
	message.TokenUsage[0] = 'x'
	inbox := a.InboxEntry()
	*inbox.ClientMessageID = "changed"
	*inbox.RuntimeID = uuid.New()
	*inbox.OwnerNodeID = "changed"
	*inbox.LastError = "changed"
	*inbox.DeliveredAt = time.Time{}
	*inbox.AckedAt = time.Time{}
	*inbox.AppliedAt = time.Time{}
	inbox.Payload[0] = 'x'
	comments := a.ResolvedComments()
	comments[0].Body = "changed"
	*comments[0].ResolvedAt = time.Time{}
	*comments[0].ResolvedByPass = 99
	*a.TargetNodeID() = "changed"
	require.Equal(t, wantMessage, a.Message(), "all message pointer slice and raw JSON getters must return isolated copies")
	require.Equal(t, wantInbox, a.InboxEntry(), "all inbox pointer timestamp and raw JSON getters must return isolated copies")
	require.Equal(t, wantComments, a.ResolvedComments(), "comment getters must clone nested resolution pointers")
	require.Equal(t, "client-key", *a.TargetNodeID(), "target getters must not mutate retained routing options")
	require.NoError(t, a.validateLifecycleReceipt(), "mutating returned data must not corrupt a retained sealed receipt")
}

func TestMessageAdmissionReceiptInvalid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*Admission)
	}{
		{"zero", func(a *Admission) { *a = Admission{} }},
		{"foreign message", func(a *Admission) { a.message.OrgID = uuid.New() }},
		{"wrong lane", func(a *Admission) { a.message.ThreadID = cloneMessageAdmissionPointer(&a.orgID) }},
		{"wrong inbox message", func(a *Admission) { a.inbox.MessageID++ }},
		{"queue cannot claim job", func(a *Admission) { a.original, a.disposition = MessageAdmissionQueued, MessageAdmissionQueued }},
		{"queue cannot retain resolved comments", func(a *Admission) {
			a.original, a.disposition = MessageAdmissionQueued, MessageAdmissionQueued
			a.jobID, a.authority = uuid.Nil, AdmissionAuthority{}
		}},
		{"job cannot lack token", func(a *Admission) { a.authority.claimToken = uuid.Nil }},
		{"job cannot replace turn", func(a *Admission) { a.authority.turn++ }},
		{"foreign comment", func(a *Admission) { a.comments[0].SessionID = uuid.New() }},
		{"unknown disposition", func(a *Admission) { a.disposition = "accepted_but_unfenced" }},
		{"unknown original", func(a *Admission) { a.original = MessageAdmissionReplayed }},
		{"missing options", func(a *Admission) { a.options = MessageAdmissionDispatchOptions{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, a := newMessageAdmissionReceiptFixture(t)
			tt.change(&a)
			result, err := newAdmissionResult(a, nil)
			require.Error(t, err, "malformed receipt must not mint a sealed successful result")
			_, _, unpackErr := result.Unpack()
			require.Error(t, unpackErr, "invalid result must not expose any receipt or effects")
		})
	}
}

func TestMessageAdmissionEnvelopeRetainsOriginalServerOptions(t *testing.T) {
	t.Parallel()
	command, a := newMessageAdmissionReceiptFixture(t)
	encoded, err := encodeMessageAdmissionEnvelope(command, a)
	require.NoError(t, err, "accepted envelope must encode all original server options separately from semantic identity")
	// Model JSONB canonical rendering; the embedded semantic string must keep
	// its original exact digest-covered bytes regardless of envelope formatting.
	var value map[string]any
	require.NoError(t, json.Unmarshal(encoded, &value), "server envelope must be a JSON object")
	canonical, err := json.MarshalIndent(value, "", "  ")
	require.NoError(t, err, "JSONB-equivalent reformatting must preserve embedded semantic string identity")
	lane := a.fence.threadID
	messageID := a.message.ID
	sessionEpoch, threadEpoch := a.fence.sessionEpoch, a.fence.threadEpoch
	row := messageAdmissionDispatchRow{id: a.dispatchID, sessionID: a.fence.sessionID, laneID: &lane, sessionEpoch: &sessionEpoch, threadEpoch: &threadEpoch, messageID: &messageID, kind: "message", digest: mustMessageAdmissionDigest(a.ingress), payload: canonical, normalizationVersion: 1}
	envelope, retained, err := decodeMessageAdmissionEnvelope(row, a.orgID, a.fence.sessionID, lane, a.ingress)
	require.NoError(t, err, "exact accepted envelope must survive JSONB formatting without a new semantic identity")
	require.Equal(t, command.Input(), retained.Input(), "replay reconstruction must preserve every original command field including epochs server options and holder")
	require.Equal(t, *a.holderID, *envelope.LeaseHolderID, "replay must retain original server changeset lease holder")
	changed := command.Input()
	changed.Dispatch.RunAt = changed.Dispatch.RunAt.Add(time.Hour)
	*changed.LeaseHolderID = uuid.New()
	changed.Fence.sessionEpoch++
	newCommand, err := NewMessageAdmission(changed)
	require.NoError(t, err, "fresh server time holder and epoch changes must not conflict with client semantic identity")
	require.Equal(t, a.ingress, newCommand.Ingress(), "exact semantic ingress must remain stable across excluded server choices")
	require.NotEqual(t, newCommand.Dispatch(), retained.Dispatch(), "retained original scheduling must not be replaced by fresh request scheduling")
	require.Equal(t, command.Dispatch(), retained.Dispatch(), "the original envelope wins on exact replay")
}

func TestMessageAdmissionEnvelopeRejectsContradictoryIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		edit func(*messageAdmissionEnvelope)
	}{
		{"missing session epoch", func(e *messageAdmissionEnvelope) { e.SessionEpoch = nil }},
		{"changed original epoch", func(e *messageAdmissionEnvelope) { *e.SessionEpoch++ }},
		{"missing lane epoch", func(e *messageAdmissionEnvelope) { e.ThreadEpoch = nil }},
		{"changed message id", func(e *messageAdmissionEnvelope) { e.MessageID++ }},
		{"changed accepted turn", func(e *messageAdmissionEnvelope) { *e.MessageTurn++ }},
		{"job missing claim", func(e *messageAdmissionEnvelope) { e.ClaimToken = uuid.Nil }},
		{"queue cannot retain a job", func(e *messageAdmissionEnvelope) { e.Disposition = MessageAdmissionQueued }},
		{"queue cannot retain a comment command", func(e *messageAdmissionEnvelope) {
			e.Disposition, e.JobID, e.ClaimToken, e.ClaimTurn = MessageAdmissionQueued, uuid.Nil, uuid.Nil, 0
			e.ResolvedComments = nil
		}},
		{"resolved comment outside semantic command", func(e *messageAdmissionEnvelope) { e.ResolvedComments[0].ID = uuid.New() }},
		{"unknown original disposition", func(e *messageAdmissionEnvelope) { e.Disposition = "future" }},
		{"missing original retry budget", func(e *messageAdmissionEnvelope) { e.MaxAttempts = 0 }},
		{"changed original retry budget", func(e *messageAdmissionEnvelope) { e.MaxAttempts++ }},
		{"different semantic payload", func(e *messageAdmissionEnvelope) { e.SemanticPayload = `{"version":1}` }},
		{"missing inbox identity", func(e *messageAdmissionEnvelope) { e.InboxID = uuid.Nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			command, a := newMessageAdmissionReceiptFixture(t)
			raw, err := encodeMessageAdmissionEnvelope(command, a)
			require.NoError(t, err, "freeze a complete accepted envelope before corrupting one retained fact")
			var envelope messageAdmissionEnvelope
			require.NoError(t, json.Unmarshal(raw, &envelope), "decode a fresh independent envelope per parallel case")
			tt.edit(&envelope)
			raw, err = json.Marshal(envelope)
			require.NoError(t, err, "retain a syntactically valid but semantically contradictory envelope")
			row := messageAdmissionDispatchRow{id: a.dispatchID, sessionID: a.fence.sessionID, laneID: cloneMessageAdmissionPointer(&a.fence.threadID), sessionEpoch: cloneMessageAdmissionPointer(&a.fence.sessionEpoch), threadEpoch: cloneMessageAdmissionPointer(&a.fence.threadEpoch), messageID: cloneMessageAdmissionPointer(&a.message.ID), kind: "message", digest: mustMessageAdmissionDigest(a.ingress), payload: raw, normalizationVersion: 1}
			_, retained, err := decodeMessageAdmissionEnvelope(row, a.orgID, a.fence.sessionID, a.fence.threadID, a.ingress)
			require.ErrorIs(t, err, ErrLifecycleConsumerReconciliation, "contradictory retained identity must become explicit reconciliation rather than guessed current data")
			require.Equal(t, MessageAdmission{}, retained, "invalid retained evidence must not reconstruct a usable frozen command")
		})
	}
}

func TestMessageAdmissionInvalidBeforeSQL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		invalid string
	}{{"nil context", "context"}, {"nil transaction", "transaction"}, {"wrong tenant", "tenant"}, {"zero command", "command"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			command, _ := newMessageAdmissionReceiptFixture(t)
			orgID := command.OrgID()
			var tx pgx.Tx = mock
			switch tt.invalid {
			case "context":
				ctx = nil
			case "transaction":
				tx = nil
			case "tenant":
				orgID = uuid.New()
			case "command":
				command = MessageAdmission{}
			}
			result, err := AdmitMessageInTx(ctx, tx, orgID, command)
			require.Error(t, err, "invalid command scope or supplied unit must fail before any query")
			_, _, unpackErr := result.Unpack()
			require.Error(t, unpackErr, "refused admission must return no usable receipt")
			require.NoError(t, mock.ExpectationsWereMet(), "invalid admission must perform no SQL or transaction settlement")
		})
	}
}

func TestMessageAdmissionReplayMissDoesNotCapture(t *testing.T) {
	t.Parallel()
	mock, ctx := newLifecycleScopeMock(t)
	command, _ := newMessageAdmissionReceiptFixture(t)
	expectLifecycleScopePolicy(mock)
	mock.ExpectQuery(messageAdmissionDispatchLookup).WithArgs(command.OrgID(), command.Ingress().Kind(), command.Ingress().Key()).WillReturnError(pgx.ErrNoRows)
	a, found, err := FindMessageAdmissionReplayInTx(ctx, mock, command.OrgID(), command.Fence().sessionID, command.Fence().threadID, command.Ingress())
	require.NoError(t, err, "exact replay miss is an observation not a new eligibility decision")
	require.False(t, found, "a missing exact receipt must not manufacture acceptance")
	require.Equal(t, Admission{}, a, "a miss must expose no partial admission or captured fence")
	require.NoError(t, mock.ExpectationsWereMet(), "receipt miss must take no parent lock capture epoch mutate or settle transaction")
}

func TestMessageAdmissionReplayInterlockPrecedesHigherLocks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		active bool
	}{{"active observation does not rely on service guard", true}, {"historical replay does not route on current legacy mode", false}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			command, a := newMessageAdmissionReceiptFixture(t)
			raw, err := encodeMessageAdmissionEnvelope(command, a)
			require.NoError(t, err, "direct replay fixture must start from a complete accepted envelope")
			expectLifecycleScopePolicy(mock)
			mock.ExpectQuery(messageAdmissionDispatchLookup).WithArgs(a.orgID, a.ingress.Kind(), a.ingress.Key()).WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "thread_id", "session_epoch", "thread_epoch", "message_id", "kind", "digest", "payload", "normalization_version"}).AddRow(a.dispatchID, a.fence.sessionID, &a.fence.threadID, &a.fence.sessionEpoch, &a.fence.threadEpoch, &a.message.ID, "message", mustMessageAdmissionDigest(a.ingress), raw, 1))
			snapshot := legacyLifecycleModeSnapshot()
			if tt.active {
				snapshot = activeLifecycleModeSnapshot()
				snapshot.cutoverOrgID, snapshot.cutoverSessionID = &a.orgID, &a.fence.sessionID
			}
			snapshot.orgID, snapshot.sessionID = a.orgID, a.fence.sessionID
			expectLifecycleScopePolicy(mock)
			mock.ExpectQuery(cutoverSharedSQL).WithArgs("session_lifecycle_cutover:" + a.orgID.String() + ":" + a.fence.sessionID.String()).WillReturnRows(cutoverLockRows().AddRow(true))
			mock.ExpectQuery(lifecycleModeSQL).WithArgs(a.orgID, a.fence.sessionID).WillReturnRows(lifecycleModeSnapshotRows(snapshot))
			pr := uuid.New()
			owners := messageAdmissionOwners{reviewPR: &pr, claimDispatch: &a.dispatchID, claimToken: &a.authority.claimToken, claimJob: &a.jobID}
			expectMessageAdmissionOwners(mock, a.orgID, a.fence.sessionID, a.fence.threadID, owners)
			mock.ExpectQuery(`SELECT bool_and(pg_try_advisory_xact_lock(hashtextextended(key,0))) FROM (SELECT unnest($1::text[]) AS key ORDER BY key) ordered`).WithArgs([]string{"code_review_pr:" + a.orgID.String() + ":" + pr.String()}).WillReturnRows(pgxmock.NewRows([]string{"locked"}).AddRow(true))
			mock.ExpectQuery(`SELECT id FROM session_input_dispatches WHERE org_id=$1 AND id=ANY($2::uuid[]) ORDER BY id FOR NO KEY UPDATE NOWAIT`).WithArgs(a.orgID, []uuid.UUID{a.dispatchID}).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(a.dispatchID)).RowsWillBeClosed()
			mock.ExpectQuery(`SELECT id FROM jobs WHERE org_id=$1 AND id=ANY($2::uuid[]) ORDER BY id FOR NO KEY UPDATE NOWAIT`).WithArgs(a.orgID, []uuid.UUID{a.jobID}).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(a.jobID)).RowsWillBeClosed()
			expectLifecycleScopePolicy(mock)
			parentErr := errors.New("synthetic parent refusal after verified lock ordering")
			mock.ExpectQuery(lifecycleSessionScopeLockQuery).WithArgs(a.orgID, a.fence.sessionID).WillReturnError(parentErr)
			actual, found, err := FindMessageAdmissionReplayInTx(ctx, mock, a.orgID, a.fence.sessionID, a.fence.threadID, a.ingress)
			require.ErrorIs(t, err, parentErr, "both finite current modes must reach the same resource-access lock without a new eligibility route")
			require.False(t, found, "failed resource access must not expose retained acceptance")
			require.Equal(t, Admission{}, actual, "failed direct observation must expose no historical authority or receipt")
			require.NoError(t, mock.ExpectationsWereMet(), "direct helper must acquire shared interlock before domain dispatch job and parent with no mutation or settlement")
		})
	}
}

func TestMessageAdmissionJobOptions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		override bool
	}{{"legacy lane key by default", false}, {"explicit continuation key retained", true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			command, a := newMessageAdmissionReceiptFixture(t)
			if !tt.override {
				input := command.Input()
				input.ContinuationDedupeKeyOverride = nil
				input.Ingress = prepareMessageAdmissionCommandFixture(t, input, "client-key")
				var err error
				command, err = NewMessageAdmission(input)
				require.NoError(t, err, "default-key command must freeze its updated semantic payload")
				a.ingress = command.Ingress()
			}
			binding, opts, err := messageAdmissionJobOptions(command, a, a.jobID)
			require.NoError(t, err, "new ordinary job options must be concrete before the enqueue helper")
			require.Equal(t, []uuid.UUID{a.dispatchID}, binding.DispatchIDs(), "ordinary job must bind only its exact newly admitted input")
			want := EnqueueOpts{Queue: "agent", JobType: "continue_session", Payload: map[string]string{"session_id": a.fence.sessionID.String(), "thread_id": a.fence.threadID.String(), "org_id": a.orgID.String(), "queued_message_id": "19",
				"changeset_id": command.ChangesetID().String(), "changeset_lease_holder_id": command.LeaseHolderID().String(), "post_success_action": "push_pr_changes", "post_success_author_mode": "user"}, Priority: 5,
				DedupeKey: command.ContinuationDedupeKeyOverride(), TargetNodeID: a.TargetNodeID(), RunAt: cloneMessageAdmissionPointer(&a.options.RunAt), MaxAttempts: 3}
			if !tt.override {
				key := ContinueSessionDedupeKey(a.fence.threadID)
				want.DedupeKey = &key
			}
			require.Equal(t, want, opts, "job must retain source continuation payload priority exact dedupe target concrete time attempts and original holder")
		})
	}
}

func expectMessageAdmissionOwners(mock pgxmock.PgxPoolIface, orgID, sessionID, laneID uuid.UUID, owners messageAdmissionOwners) {
	mock.ExpectQuery(messageAdmissionOwnerLookupQuery).WithArgs(orgID, sessionID, laneID).WillReturnRows(pgxmock.NewRows([]string{"review", "automation", "target", "claim_dispatch", "claim_token", "claim_job"}).AddRow(owners.reviewPR, owners.automationGeneration, owners.automationTarget, owners.claimDispatch, owners.claimToken, owners.claimJob))
	rows := pgxmock.NewRows([]string{"id", "pull_request_id"})
	for _, r := range owners.repairs {
		rows.AddRow(r.id, r.prID)
	}
	mock.ExpectQuery(messageAdmissionRepairLookupQuery).WithArgs(orgID, sessionID, defaultLifecycleConsumerLimits().structuredRows+1).WillReturnRows(rows).RowsWillBeClosed()
}

func TestMessageAdmissionCapturePrelocksBeforeParent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		changed bool
	}{{"known domain receipt job rows precede canonical parent lanes", false}, {"changed locator cannot escape as retained command fence", true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			snapshot := activeLifecycleModeSnapshot()
			f := newLifecycleScopeMockFixture()
			f.orgID, f.sessionID = snapshot.orgID, snapshot.sessionID
			f.session.orgID, f.session.id = f.orgID, f.sessionID
			f.first.orgID, f.first.sessionID = f.orgID, f.sessionID
			f.second.orgID, f.second.sessionID = f.orgID, f.sessionID
			f.session.epochs.LifecycleVersion = 1
			f.first.epochs.LifecycleVersion, f.second.epochs.LifecycleVersion = 1, 1
			f.session.status = models.SessionStatusAwaitingInput
			f.first.status = models.ThreadStatusAwaitingInput
			pr, generation, target, dispatch, token, job, repair, repairPR := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			owners := messageAdmissionOwners{reviewPR: &pr, automationGeneration: &generation, automationTarget: &target, claimDispatch: &dispatch, claimToken: &token, claimJob: &job, repairs: []messageAdmissionRepairLocator{{repair, repairPR}}}
			expectLifecycleModeRead(mock, snapshot)
			expectMessageAdmissionOwners(mock, f.orgID, f.sessionID, f.first.id, owners)
			keys := []string{"code_review_pr:" + f.orgID.String() + ":" + pr.String(), "code_review_pr:" + f.orgID.String() + ":" + repairPR.String()}
			slices.Sort(keys)
			mock.ExpectQuery(`SELECT bool_and(pg_try_advisory_xact_lock(hashtextextended(key,0))) FROM (SELECT unnest($1::text[]) AS key ORDER BY key) ordered`).WithArgs(keys).WillReturnRows(pgxmock.NewRows([]string{"locked"}).AddRow(true))
			for _, entry := range []struct {
				query string
				id    uuid.UUID
			}{
				{`SELECT id FROM automation_targets WHERE org_id=$1 AND id=ANY($2::uuid[]) ORDER BY id FOR NO KEY UPDATE NOWAIT`, target},
				{`SELECT id FROM automation_target_sessions WHERE org_id=$1 AND id=ANY($2::uuid[]) ORDER BY id FOR NO KEY UPDATE NOWAIT`, generation},
				{`SELECT id FROM pull_request_repair_runs WHERE org_id=$1 AND id=ANY($2::uuid[]) ORDER BY id FOR NO KEY UPDATE NOWAIT`, repair},
				{`SELECT id FROM session_input_dispatches WHERE org_id=$1 AND id=ANY($2::uuid[]) ORDER BY id FOR NO KEY UPDATE NOWAIT`, dispatch},
				{`SELECT id FROM jobs WHERE org_id=$1 AND id=ANY($2::uuid[]) ORDER BY id FOR NO KEY UPDATE NOWAIT`, job},
			} {
				mock.ExpectQuery(entry.query).WithArgs(f.orgID, []uuid.UUID{entry.id}).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(entry.id)).RowsWillBeClosed()
			}
			expectLifecycleModeRead(mock, snapshot)
			expectThreadSendScopeParent(mock, f)
			expectThreadSendScopeKeys(mock, f, f.first.id, f.second.id)
			mock.ExpectQuery(lifecycleThreadScopeLockQuery).WithArgs(f.orgID, f.sessionID, []uuid.UUID{f.first.id, f.second.id}).WillReturnRows(lifecycleScopeThreadRows(f.first, f.second)).RowsWillBeClosed()
			after := owners
			if tt.changed {
				other := uuid.New()
				after.claimToken = &other
			}
			expectMessageAdmissionOwners(mock, f.orgID, f.sessionID, f.first.id, after)
			actual, err := CaptureOrdinaryMessageFenceInTx(ctx, mock, f.orgID, f.sessionID, f.first.id)
			if tt.changed {
				require.ErrorIs(t, err, ErrMessageAdmissionConflict, "changed owner locator must abort capture without adopting a new identity")
				require.Equal(t, AdmissionFence{}, actual, "failed capture must expose no fence to a later mutation retry loop")
			} else {
				want, constructorErr := NewAdmissionFence(f.sessionID, &f.first.id, f.session.epochs.AdmissionEpoch, &f.first.epochs.AdmissionEpoch)
				require.NoError(t, constructorErr, "synthetic expected fence must be exact explicit and nonnegative")
				require.NoError(t, err, "capture may observe an ineligible owned awaiting-input scope without granting admission")
				require.Equal(t, want, actual, "capture must retain only exact epochs after every known higher-ranked lock")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "the exact SQL sequence must prove interlock domain reservation dispatch job parent and sorted lane acquisition with no mutation or settlement")
		})
	}
}

func TestMessageAdmissionEffectsAreExactAndPostcommitOnly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		kind MessageAdmissionDisposition
		want []LifecycleEffectKind
	}{
		{"new job", MessageAdmissionNewJob, []LifecycleEffectKind{LifecycleEffectInboxChanged, LifecycleEffectThreadChanged, LifecycleEffectSessionChanged, LifecycleEffectWakeJobs}},
		{"queued", MessageAdmissionQueued, []LifecycleEffectKind{LifecycleEffectInboxChanged, LifecycleEffectThreadChanged}},
		{"waiting", MessageAdmissionWaiting, []LifecycleEffectKind{LifecycleEffectInboxChanged, LifecycleEffectThreadChanged}},
		{"replayed", MessageAdmissionReplayed, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, a := newMessageAdmissionReceiptFixture(t)
			a.disposition = tt.kind
			if tt.kind != MessageAdmissionNewJob && tt.kind != MessageAdmissionReplayed {
				a.original = tt.kind
				a.jobID = uuid.Nil
				a.authority = AdmissionAuthority{}
				a.comments = nil
			}
			actual, err := messageAdmissionEffects(a)
			require.NoError(t, err, "a valid accepted receipt may produce only immutable notification data")
			var expected []LifecycleEffect
			for _, kind := range tt.want {
				effect, err := newLifecycleEffect(kind, a.orgID, a.fence.sessionID, &a.fence.threadID)
				require.NoError(t, err, "synthetic expected notification must retain exact receipt scope")
				expected = append(expected, effect)
			}
			require.Equal(t, expected, actual, "queued waiting and replayed outcomes must never manufacture a job wake or duplicate current-state effects")
		})
	}
}
