package thread

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestSendMessageLegacyUnitOrderAndUnknownStops(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		unknownAt int
		want      []string
	}{
		{name: "original separate units and postcommit order", want: []string{"begin", "commit", "begin", "claim-thread-false", "commit", "claim-effect", "begin", "claim-session-idle", "commit", "begin", "message", "commit", "begin", "enqueue", "commit", "job-effect"}},
		{name: "unknown lane claim", unknownAt: 2, want: []string{"begin", "commit", "begin", "claim-thread-false", "unknown"}},
		{name: "unknown parent claim", unknownAt: 3, want: []string{"begin", "commit", "begin", "claim-thread-false", "commit", "claim-effect", "begin", "claim-session-idle", "unknown"}},
		{name: "unknown message write", unknownAt: 4, want: []string{"begin", "commit", "begin", "claim-thread-false", "commit", "claim-effect", "begin", "claim-session-idle", "commit", "begin", "message", "unknown"}},
		{name: "unknown enqueue", unknownAt: 5, want: []string{"begin", "commit", "begin", "claim-thread-false", "commit", "claim-effect", "begin", "claim-session-idle", "commit", "begin", "message", "commit", "begin", "enqueue", "unknown"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service, deps := newTestService(t)
			input := SendMessageInput{OrgID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), ClientMessageID: "synthetic-key", Message: "continue", PostSuccessAction: "legacy-valid-not-v1"}
			deps.threadStore.claimIdleFn = func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.SessionThread, error) {
				return models.SessionThread{OrgID: input.OrgID, SessionID: input.SessionID, ID: input.ThreadID, Status: models.ThreadStatusRunning}, nil
			}
			deps.messageStore.createFn = func(_ context.Context, message *models.SessionMessage) error { message.ID = 42; return nil }
			runner := service.send.Transactions.(*legacyGuardFixtureRunner)
			runner.failCommitAt = tt.unknownAt
			result, err := service.SendMessage(context.Background(), input)
			if tt.unknownAt != 0 {
				var unknown *CommandOutcomeUnknownError
				require.ErrorAs(t, err, &unknown, "ambiguous original unit must stop without compensating any prior accepted unit")
				require.NotEqual(t, uuid.Nil, unknown.DiagnosticID, "legacy-invalid-v1 shape still retains one stable correlation identity")
				require.Equal(t, input.ClientMessageID, unknown.ClientMessageID, "unknown retains original client correlation without using it as authority")
				require.Equal(t, db.LifecycleIngress{}, unknown.Ingress, "invalid-v1 legacy shape must not fabricate a canonical ingress")
				require.Nil(t, result, "unknown cannot masquerade as accepted work")
			} else {
				require.NoError(t, err, "legacy-only validation must not silently change v0 acceptance")
				require.Equal(t, int64(42), result.Message.ID, "known original unit must expose its message")
			}
			require.Equal(t, tt.want, runner.events, "every original mutation retains its own fresh guarded transaction and effects follow only known commit; parent claims remain eventless")
			require.Empty(t, deps.sessionStore.updateCalls, "unknown never authorizes compensation")
		})
	}
}

func TestSendMessageLegacyClaimMissDiagnosticAfterCommit(t *testing.T) {
	t.Parallel()
	service, deps := newTestService(t)
	input := SendMessageInput{OrgID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), Message: "continue"}
	deps.threadStore.claimIdleFn = func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.SessionThread, error) {
		return models.SessionThread{}, pgx.ErrNoRows
	}
	deps.threadStore.getByIDFn = func(context.Context, uuid.UUID, uuid.UUID) (models.SessionThread, error) {
		return models.SessionThread{ID: input.ThreadID, OrgID: input.OrgID, SessionID: input.SessionID, Status: models.ThreadStatusRunning}, nil
	}
	deps.messageStore.createFn = func(_ context.Context, message *models.SessionMessage) error { message.ID = 42; return nil }
	result, err := service.SendMessage(context.Background(), input)
	require.NoError(t, err, "raw claim miss must commit the original unit before post-unit diagnosis and queue fallback")
	require.Equal(t, int64(42), result.Message.ID, "queue path must preserve message acceptance")
	runner := service.send.Transactions.(*legacyGuardFixtureRunner)
	require.Equal(t, []string{"begin", "commit", "begin", "claim-thread-false", "commit", "idle-limit", "begin", "message", "commit", "begin", "pending", "commit", "pending-effect"}, runner.events, "miss must be a known completed unit, diagnostic must be outside its locks, and standalone pending retains current-row event order")
}

func TestSendMessageLegacyFreshModeAtEveryUnit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		at     int
		want   error
		events []string
	}{
		{name: "entry normalization precedes old prechecks", at: 1, want: ErrSendNormalizing, events: []string{"begin", "rollback"}},
		{name: "lane claim rechecks after entry", at: 2, want: db.ErrThreadSendModeChanged, events: []string{"begin", "commit", "begin", "rollback"}},
		{name: "parent claim rechecks after lane commit", at: 3, want: db.ErrThreadSendModeChanged, events: []string{"begin", "commit", "begin", "claim-thread-false", "commit", "claim-effect", "begin", "rollback"}},
		{name: "message rechecks after parent commit", at: 4, want: db.ErrThreadSendModeChanged, events: []string{"begin", "commit", "begin", "claim-thread-false", "commit", "claim-effect", "begin", "claim-session-idle", "commit", "begin", "rollback"}},
		{name: "enqueue rechecks after message commit", at: 5, want: db.ErrThreadSendModeChanged, events: []string{"begin", "commit", "begin", "claim-thread-false", "commit", "claim-effect", "begin", "claim-session-idle", "commit", "begin", "message", "commit", "begin", "rollback"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service, deps := newTestService(t)
			input := SendMessageInput{OrgID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), Message: "continue"}
			deps.threadStore.claimIdleFn = func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.SessionThread, error) {
				return models.SessionThread{ID: input.ThreadID, OrgID: input.OrgID, SessionID: input.SessionID, Status: models.ThreadStatusRunning}, nil
			}
			deps.messageStore.createFn = func(_ context.Context, message *models.SessionMessage) error { message.ID = 42; return nil }
			if tt.at == 1 {
				deps.sessionStore.codeReviewErr = errors.New("legacy precheck must not run before entry router")
			}
			runner := service.send.Transactions.(*legacyGuardFixtureRunner)
			runner.normalizingAt = tt.at
			result, err := service.SendMessage(context.Background(), input)
			require.ErrorIs(t, err, tt.want, "mode changes must stop the fixed legacy branch without fallback or compensation")
			require.Nil(t, result, "a fixed-branch mode mismatch cannot be reported as successful send")
			require.Equal(t, tt.events, runner.events, "each original boundary must obtain its own actual fresh SQL mode observation")
		})
	}
}

func TestSendMessageLegacyCompensationUnitsStopOnUnknown(t *testing.T) {
	t.Parallel()
	failure := errors.New("synthetic message write failure")
	tests := []struct {
		name      string
		unknownAt int
		want      []string
	}{
		{name: "known abort restores parent then lane separately", want: []string{"begin", "commit", "begin", "claim-thread-false", "commit", "claim-effect", "begin", "claim-session-idle", "commit", "begin", "message", "rollback", "begin", "session-status", "commit", "session-effect", "begin", "thread-status", "commit", "thread-effect"}},
		{name: "unknown parent restore stops lane compensation", unknownAt: 5, want: []string{"begin", "commit", "begin", "claim-thread-false", "commit", "claim-effect", "begin", "claim-session-idle", "commit", "begin", "message", "rollback", "begin", "session-status", "unknown"}},
		{name: "unknown lane restore cannot publish", unknownAt: 6, want: []string{"begin", "commit", "begin", "claim-thread-false", "commit", "claim-effect", "begin", "claim-session-idle", "commit", "begin", "message", "rollback", "begin", "session-status", "commit", "session-effect", "begin", "thread-status", "unknown"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service, deps := newTestService(t)
			input := SendMessageInput{OrgID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), Message: "continue"}
			deps.threadStore.claimIdleFn = func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.SessionThread, error) {
				return models.SessionThread{ID: input.ThreadID, OrgID: input.OrgID, SessionID: input.SessionID, Status: models.ThreadStatusRunning}, nil
			}
			deps.messageStore.createFn = func(context.Context, *models.SessionMessage) error { return failure }
			runner := service.send.Transactions.(*legacyGuardFixtureRunner)
			runner.failCommitAt = tt.unknownAt
			result, err := service.SendMessage(context.Background(), input)
			if tt.unknownAt != 0 {
				require.ErrorIs(t, err, ErrCommandOutcomeUnknown, "an ambiguous compensation must replace the primary error and stop later writes")
			} else {
				require.ErrorIs(t, err, failure, "known compensation preserves the original message failure")
			}
			require.Nil(t, result, "failed message unit cannot expose a successful result")
			require.Equal(t, tt.want, runner.events, "original independent compensation units and snapshot/current-row publishers must preserve order and known-commit ownership")
		})
	}
}

func TestSendMessageLegacyValidShapeUnknownHasCorrelationOnly(t *testing.T) {
	t.Parallel()
	service, deps := newTestService(t)
	input := SendMessageInput{OrgID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), ClientMessageID: "synthetic-client-key", Message: "continue"}
	prepared, _, err := prepareSendMessage(input)
	require.NoError(t, err, "this legacy input deliberately also has valid v1 shape")
	require.NoError(t, prepared.Ingress.Validate(), "v1 preparation would otherwise carry a valid ingress")
	var originalDiagnostic uuid.UUID
	deps.threadStore.claimIdleFn = func(ctx context.Context, _, _, _ uuid.UUID) (models.SessionThread, error) {
		originalDiagnostic = ctx.Value(sendDiagnosticContextKey{}).(sendDiagnostic).id
		return models.SessionThread{ID: input.ThreadID, OrgID: input.OrgID, SessionID: input.SessionID, Status: models.ThreadStatusRunning}, nil
	}
	deps.messageStore.createFn = func(ctx context.Context, message *models.SessionMessage) error {
		require.Equal(t, originalDiagnostic, ctx.Value(sendDiagnosticContextKey{}).(sendDiagnostic).id, "one correlation identity must survive all original legacy units")
		message.ID = 42
		return nil
	}
	service.send.Transactions.(*legacyGuardFixtureRunner).failCommitAt = 4
	result, err := service.SendMessage(context.Background(), input)
	var unknown *CommandOutcomeUnknownError
	require.ErrorAs(t, err, &unknown, "ambiguous legacy message commit must retain typed uncertainty")
	require.Nil(t, result, "unknown does not establish acceptance")
	require.Equal(t, db.LifecycleIngress{}, unknown.Ingress, "shape-valid v1 preparation cannot become a phantom legacy receipt")
	require.NotEqual(t, uuid.Nil, originalDiagnostic, "legacy diagnostic identity must be real and correlation-only")
	require.Equal(t, originalDiagnostic, unknown.DiagnosticID, "unknown must retain the original diagnostic identity rather than minting another")
	require.Equal(t, input.ClientMessageID, unknown.ClientMessageID, "original legacy client key remains correlation only")
}

func TestSendMessageLegacyBusinessContentionDoesNotBecomeStatusFallback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		at     int
		events []string
	}{
		{name: "lane business contention stops before lookup", at: 2, events: []string{"begin", "commit", "begin", "claim-thread-false", "rollback"}},
		{name: "known-aborted parent contention may release prior lane", at: 3, events: []string{"begin", "commit", "begin", "claim-thread-false", "commit", "claim-effect", "begin", "claim-session-idle", "rollback", "begin", "thread-status", "commit", "thread-effect"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service, deps := newTestService(t)
			input := SendMessageInput{OrgID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), Message: "continue"}
			contention := &pgconn.PgError{Code: "55P03"}
			deps.threadStore.claimIdleFn = func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.SessionThread, error) {
				if tt.at == 2 {
					return models.SessionThread{}, contention
				}
				return models.SessionThread{ID: input.ThreadID, OrgID: input.OrgID, SessionID: input.SessionID, Status: models.ThreadStatusRunning}, nil
			}
			deps.sessionStore.claimIdleFn = func(context.Context, uuid.UUID, uuid.UUID) (models.Session, error) {
				return models.Session{}, contention
			}
			deps.threadStore.getByIDFn = func(context.Context, uuid.UUID, uuid.UUID) (models.SessionThread, error) {
				t.Fatal("confirmed business contention must not be reinterpreted by a status fallback read")
				return models.SessionThread{}, nil
			}
			deps.sessionStore.getByIDFn = func(context.Context, uuid.UUID, uuid.UUID) (models.Session, error) {
				t.Fatal("confirmed parent business contention must not become a status fallback")
				return models.Session{}, nil
			}
			runner := service.send.Transactions.(*legacyGuardFixtureRunner)
			runner.contentionAt = tt.at
			result, err := service.SendMessage(context.Background(), input)
			var exhausted *db.LifecycleContentionError
			require.ErrorAs(t, err, &exhausted, "the concrete runner's confirmed-aborted contention disposition must survive the actual service call-through")
			require.Equal(t, 3, exhausted.Attempts, "service must retain the runner's exhausted-attempt evidence, not infer status")
			require.Nil(t, result, "contention is unaccepted, not a successful queued send")
			require.Equal(t, tt.events, runner.events, "only prior confirmed legacy lane work may receive separate fresh-guard compensation; no new send unit follows contention")
		})
	}
}
