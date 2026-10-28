package thread

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestRunSendUnitUnknownDiagnostics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		settlement   string
		workFails    bool
		joinedWork   bool
		legacy       bool
		wantUnknown  bool
		wantAttempts int
	}{
		{name: "known commit", settlement: "callback", wantAttempts: 1},
		{name: "known callback abort", settlement: "callback", workFails: true, wantAttempts: 1},
		{name: "known joined callback abort", settlement: "callback", workFails: true, joinedWork: true, wantAttempts: 1},
		{name: "known exhausted contention", settlement: "contention", workFails: true, wantAttempts: 1},
		{name: "failure before callback", settlement: "before callback"},
		{name: "lost commit acknowledgement", settlement: "lost acknowledgement", wantUnknown: true, wantAttempts: 1},
		{name: "joined settlement cannot prove rollback", settlement: "joined settlement", workFails: true, wantUnknown: true, wantAttempts: 1},
		{name: "legacy uncertainty has no fabricated ingress", settlement: "lost acknowledgement", legacy: true, wantUnknown: true, wantAttempts: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			diagnostic := sendDiagnostic{id: uuid.New(), clientKey: "synthetic-private-client-key"}
			ingress, err := db.NewLifecycleIngress("synthetic-message", "synthetic-private-ingress-key", db.LifecycleDigest{1})
			require.NoError(t, err, "the fixture must carry an immutable command identity without granting admission authority")
			if tt.legacy {
				ingress = db.LifecycleIngress{}
			}
			ctx := context.WithValue(context.Background(), sendDiagnosticContextKey{}, diagnostic)
			ctx = context.WithValue(ctx, sendIngressContextKey{}, ingress)
			pgCause := &pgconn.PgError{Code: "55P03", Message: "synthetic lock conflict"}
			var workCause error = pgCause
			if tt.joinedWork {
				workCause = errors.Join(pgCause, io.EOF)
			}
			workFailure := &legacySendDomainError{domain: ErrEnqueueFailed, cause: workCause}
			contention := &db.LifecycleContentionError{Attempts: 3, Cause: pgCause}
			var settlementError error
			service := &Service{
				logger: zerolog.New(&logs),
				send: SendDependencies{Transactions: sendTransactionsFunc(func(unitCtx context.Context, work func(context.Context, pgx.Tx) error) error {
					if tt.settlement == "before callback" {
						settlementError = io.EOF
						return settlementError
					}
					callbackError := work(unitCtx, nil)
					switch tt.settlement {
					case "contention":
						settlementError = contention
					case "lost acknowledgement":
						require.NoError(t, callbackError, "lost commit acknowledgement must follow a successful callback")
						settlementError = io.EOF
					case "joined settlement":
						require.Error(t, callbackError, "joined settlement must retain the actual callback failure")
						settlementError = errors.Join(callbackError, io.EOF)
					default:
						settlementError = callbackError
					}
					return settlementError
				})},
			}
			attempts := 0
			err = service.runSendUnit(ctx, func(context.Context, pgx.Tx) error {
				attempts++
				if tt.workFails {
					return workFailure
				}
				return nil
			})
			require.Equal(t, tt.wantAttempts, attempts, "diagnostic logging must not add a callback or retry")
			if !tt.wantUnknown {
				require.Empty(t, logs.String(), "known commit, known abort, contention and pre-callback failures must not log unknown outcomes")
				require.NotErrorIs(t, err, ErrCommandOutcomeUnknown, "logging must not change a proven known outcome into uncertainty")
				switch tt.settlement {
				case "before callback":
					require.Same(t, settlementError, err, "failure before callback must preserve the runner's exact error")
				case "contention":
					require.Same(t, contention, err, "confirmed contention must preserve its typed original error")
				default:
					if tt.workFails {
						require.Same(t, workFailure, err, "exact rollback proof must return the original callback cause")
					} else {
						require.NoError(t, err, "known commit must retain its nil outcome")
					}
				}
				return
			}
			var unknown *CommandOutcomeUnknownError
			require.ErrorAs(t, err, &unknown, "an uncertain settlement must retain the existing typed response")
			require.ErrorIs(t, err, ErrCommandOutcomeUnknown, "the existing unknown sentinel must remain machine recognizable")
			require.Same(t, settlementError, unknown.Cause, "logging must preserve the exact original settlement cause, including a genuine join")
			require.Same(t, settlementError, errors.Unwrap(unknown), "the typed response must unwrap directly to the untouched settlement error")
			require.Equal(t, ingress, unknown.Ingress, "logging must preserve the original ingress without synthesizing a legacy identity")
			require.Equal(t, diagnostic.id, unknown.DiagnosticID, "the warning and response must retain the same diagnostic identity")
			require.Equal(t, diagnostic.clientKey, unknown.ClientMessageID, "the returned correlation key must remain available without being logged")
			require.EqualError(t, err, ErrCommandOutcomeUnknown.Error(), "the response must not expose diagnostic causes or command keys")
			decoder := json.NewDecoder(&logs)
			var actual map[string]string
			require.NoError(t, decoder.Decode(&actual), "a true unknown must emit one structured diagnostic warning")
			require.Equal(t, map[string]string{
				"level":         "warn",
				"error":         settlementError.Error(),
				"diagnostic_id": diagnostic.id.String(),
				"ingress_kind":  ingress.Kind(),
				"message":       "thread message command outcome unknown",
			}, actual, "the warning must contain only the original cause and safe correlation fields, never a client key, ingress key or message payload")
			var extra map[string]string
			require.ErrorIs(t, decoder.Decode(&extra), io.EOF, "each truly unknown unit must emit exactly one diagnostic warning")
		})
	}
}

func TestSendMessageLegacyAtomicUnitOrderAndUnknownStops(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		unknownAt int
		want      []string
	}{
		{name: "whole send commits before publishing", want: []string{"begin", "commit", "begin", "claim-thread-false", "claim-session-idle", "message", "enqueue", "commit", "claim-effect", "job-effect"}},
		{name: "unknown whole send publishes nothing", unknownAt: 2, want: []string{"begin", "commit", "begin", "claim-thread-false", "claim-session-idle", "message", "enqueue", "unknown"}},
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
				require.ErrorAs(t, err, &unknown, "ambiguous atomic settlement must stop without publishing, retrying or compensating")
				require.NotEqual(t, uuid.Nil, unknown.DiagnosticID, "legacy-invalid-v1 shape still retains one stable correlation identity")
				require.Equal(t, input.ClientMessageID, unknown.ClientMessageID, "unknown retains original client correlation without using it as authority")
				require.Equal(t, db.LifecycleIngress{}, unknown.Ingress, "invalid-v1 legacy shape must not fabricate a canonical ingress")
				require.Nil(t, result, "unknown cannot masquerade as accepted work")
			} else {
				require.NoError(t, err, "legacy-only validation must preserve v0 acceptance")
				require.Equal(t, int64(42), result.Message.ID, "known atomic commit must expose its message")
			}
			require.Equal(t, tt.want, runner.events, "the entry observation and one atomic send attempt must settle before every published effect")
			require.Empty(t, deps.sessionStore.updateCalls, "atomic send never authorizes compensation")
		})
	}
}

func TestSendMessageLegacyDomainErrorPreservesCauseShape(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cause   func() error
		text    string
		pgCode  string
		wantEOF bool
	}{
		{name: "lock unavailable", cause: func() error {
			return &pgconn.PgError{Severity: "ERROR", Message: "synthetic lock conflict", Code: "55P03"}
		}, text: "enqueue failed: ERROR: synthetic lock conflict (SQLSTATE 55P03)", pgCode: "55P03"},
		{name: "deadlock", cause: func() error { return &pgconn.PgError{Severity: "ERROR", Message: "synthetic deadlock", Code: "40P01"} }, text: "enqueue failed: ERROR: synthetic deadlock (SQLSTATE 40P01)", pgCode: "40P01"},
		{name: "no rows", cause: func() error { return pgx.ErrNoRows }, text: "enqueue failed: no rows in result set"},
		{name: "joined EOF remains joined", cause: func() error {
			return errors.Join(&pgconn.PgError{Severity: "ERROR", Message: "synthetic lock conflict", Code: "55P03"}, io.EOF)
		}, text: "enqueue failed: ERROR: synthetic lock conflict (SQLSTATE 55P03)\nEOF", pgCode: "55P03", wantEOF: true},
		{name: "caller canceled", cause: func() error { return context.Canceled }, text: "enqueue failed: context canceled"},
		{name: "deadline exceeded", cause: func() error { return context.DeadlineExceeded }, text: "enqueue failed: context deadline exceeded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cause := tt.cause()
			wrapped := &legacySendDomainError{domain: ErrEnqueueFailed, cause: cause}
			require.EqualError(t, wrapped, tt.text, "handler-facing error text must retain the established domain and original cause text")
			require.ErrorIs(t, wrapped, ErrEnqueueFailed, "handlers must retain the established domain sentinel")
			require.ErrorIs(t, wrapped, cause, "the untouched cause must remain discoverable through the single unwrap")
			require.True(t, cause == errors.Unwrap(wrapped), "the wrapper must return the exact original cause identity or value without inserting another shape")
			require.True(t, wrapped.Is(ErrEnqueueFailed), "the explicit Is method must identify its domain")
			require.False(t, wrapped.Is(cause), "cause matching must come from Unwrap rather than replacing domain matching")
			_, multiple := any(wrapped).(interface{ Unwrap() []error })
			require.False(t, multiple, "a service domain must not fabricate a multi-error unwrap that hides a plain SQLSTATE")
			require.Equal(t, tt.wantEOF, errors.Is(wrapped, io.EOF), "an existing joined EOF must remain visible without being introduced on plain errors")
			var actualPG *pgconn.PgError
			foundPG := errors.As(wrapped, &actualPG)
			require.Equal(t, tt.pgCode != "", foundPG, "SQLSTATE discovery must preserve the actual cause type")
			if tt.pgCode != "" {
				var originalPG *pgconn.PgError
				require.ErrorAs(t, cause, &originalPG, "the fixture's PostgreSQL error must exist inside the original cause")
				require.Same(t, originalPG, actualPG, "errors.As must return the exact original PostgreSQL cause")
				require.Equal(t, tt.pgCode, actualPG.Code, "retry classification must see the original SQLSTATE")
			}
		})
	}
}

func TestSendMessageLegacyDomainErrorSettlementProof(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		mode       string
		joinedWork bool
		unknown    bool
		attempts   int
	}{
		{name: "exact callback marker proves known rollback", attempts: 1},
		{name: "exact joined callback cause is a known abort without replay", joinedWork: true, attempts: 1},
		{name: "settlement join cannot reuse callback rollback proof", mode: "joined settlement", unknown: true, attempts: 1},
		{name: "lost commit after successful callback is unknown", mode: "lost commit", unknown: true, attempts: 1},
		{name: "retry then success cannot reuse earlier callback marker after failed commit", mode: "retry then lost commit", unknown: true, attempts: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pgCause := &pgconn.PgError{Severity: "ERROR", Message: "synthetic deadlock", Code: "40P01"}
			var cause error = pgCause
			if tt.joinedWork {
				cause = errors.Join(pgCause, io.EOF)
			}
			workFailure := &legacySendDomainError{domain: ErrEnqueueFailed, cause: cause}
			diagnostic := sendDiagnostic{id: uuid.New(), clientKey: "synthetic-legacy-client-key"}
			ctx := context.WithValue(context.Background(), sendDiagnosticContextKey{}, diagnostic)
			callbacks := 0
			service := &Service{send: SendDependencies{Transactions: sendTransactionsFunc(func(unitCtx context.Context, work func(context.Context, pgx.Tx) error) error {
				first := work(unitCtx, nil)
				switch tt.mode {
				case "joined settlement":
					return errors.Join(first, io.EOF)
				case "lost commit":
					require.NoError(t, first, "a lost commit begins with a successful callback")
					return io.EOF
				case "retry then lost commit":
					var marked *sendWorkError
					require.ErrorAs(t, first, &marked, "the first callback must return its exact work marker before owner-controlled retry")
					require.Same(t, workFailure, marked.cause, "the callback marker must retain its domain wrapper")
					var originalPG *pgconn.PgError
					require.ErrorAs(t, first, &originalPG, "the single unwrap must retain the original deadlock for the transaction owner")
					require.Same(t, pgCause, originalPG, "retry must not substitute a reconstructed PostgreSQL error")
					require.NoError(t, work(unitCtx, nil), "the owner-supplied second callback must succeed before commit fails")
					return io.EOF
				default:
					return first
				}
			})}}
			err := service.runSendUnit(ctx, func(context.Context, pgx.Tx) error {
				callbacks++
				if tt.mode == "lost commit" || callbacks > 1 {
					return nil
				}
				return workFailure
			})
			require.Equal(t, tt.attempts, callbacks, "only the supplied transaction owner may invoke another callback")
			if tt.unknown {
				var unknown *CommandOutcomeUnknownError
				require.ErrorAs(t, err, &unknown, "joined settlement or lost commit must preserve typed uncertainty")
				require.ErrorIs(t, err, io.EOF, "unknown settlement must retain the actual completion cause")
				require.Equal(t, diagnostic.id, unknown.DiagnosticID, "uncertainty must retain the original correlation identity")
				require.Equal(t, diagnostic.clientKey, unknown.ClientMessageID, "uncertainty must retain the original client correlation")
				if tt.mode != "joined settlement" {
					require.NotErrorIs(t, err, ErrEnqueueFailed, "a successful final callback must discard the earlier domain failure before failed commit")
					require.Same(t, io.EOF, unknown.Cause, "a failed final commit must not reuse the first callback's marker or error")
				}
				return
			}
			require.Same(t, workFailure, err, "the exact callback marker after known rollback must return the original domain wrapper")
			require.NotErrorIs(t, err, ErrCommandOutcomeUnknown, "a joined callback cause with exact rollback proof is a known abort")
			require.ErrorIs(t, err, ErrEnqueueFailed, "known rollback must preserve handler-facing domain classification")
		})
	}
}

func TestSendMessageLegacyClaimMissClassifiesInsideAtomicUnit(t *testing.T) {
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
	require.NoError(t, err, "claim miss classification and queue fallback must share one guarded atomic attempt")
	require.Equal(t, int64(42), result.Message.ID, "queue path must preserve message acceptance")
	runner := service.send.Transactions.(*legacyGuardFixtureRunner)
	require.Equal(t, []string{"begin", "commit", "begin", "claim-thread-false", "message", "pending", "commit", "pending-effect"}, runner.events, "queue classification must not settle an independent claim unit or perform a pool-backed diagnostic")
}

func TestSendMessageLegacyFreshModeAtEntryAndAtomicMutation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		at     int
		want   error
		events []string
	}{
		{name: "entry normalization precedes business policy", at: 1, want: ErrSendNormalizing, events: []string{"begin", "rollback"}},
		{name: "atomic mutation rechecks after entry", at: 2, want: db.ErrThreadSendModeChanged, events: []string{"begin", "commit", "begin", "rollback"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service, deps := newTestService(t)
			input := SendMessageInput{OrgID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), Message: "continue"}
			if tt.at == 1 {
				deps.sessionStore.codeReviewErr = errors.New("business policy must not precede a rejected entry mode observation")
			}
			runner := service.send.Transactions.(*legacyGuardFixtureRunner)
			runner.normalizingAt = tt.at
			result, err := service.SendMessage(context.Background(), input)
			require.ErrorIs(t, err, tt.want, "mode changes must stop the fixed legacy branch before business policy or mutations")
			require.Nil(t, result, "a fixed-branch mode mismatch cannot report a successful send")
			require.Equal(t, tt.events, runner.events, "entry and atomic attempt must each obtain their own actual fresh SQL mode observation")
		})
	}
}

func TestSendMessageLegacyKnownAbortPublishesNothing(t *testing.T) {
	t.Parallel()
	failure := errors.New("synthetic database write failure")
	tests := []struct {
		name   string
		at     string
		events []string
	}{
		{name: "message failure", at: "message", events: []string{"begin", "commit", "begin", "claim-thread-false", "claim-session-idle", "message", "rollback"}},
		{name: "enqueue failure", at: "enqueue", events: []string{"begin", "commit", "begin", "claim-thread-false", "claim-session-idle", "message", "enqueue", "rollback"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service, deps := newTestService(t)
			input := SendMessageInput{OrgID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), Message: "continue"}
			deps.threadStore.claimIdleFn = func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.SessionThread, error) {
				return models.SessionThread{ID: input.ThreadID, OrgID: input.OrgID, SessionID: input.SessionID, Status: models.ThreadStatusRunning}, nil
			}
			deps.messageStore.createFn = func(_ context.Context, message *models.SessionMessage) error {
				message.ID = 42
				if tt.at == "message" {
					return failure
				}
				return nil
			}
			deps.jobStore.enqueueWithOptsFn = func(context.Context, uuid.UUID, db.EnqueueOpts) (uuid.UUID, error) { return uuid.Nil, failure }
			result, err := service.SendMessage(context.Background(), input)
			require.ErrorIs(t, err, failure, "known whole-send rollback must retain its original write failure")
			require.Nil(t, result, "aborted send cannot expose a captured message")
			runner := service.send.Transactions.(*legacyGuardFixtureRunner)
			require.Equal(t, tt.events, runner.events, "a known abort must publish no claim/job effects and start no compensating transaction")
			require.Empty(t, deps.sessionStore.updateCalls, "rollback must not run a parent status compensation")
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
		require.Equal(t, originalDiagnostic, ctx.Value(sendDiagnosticContextKey{}).(sendDiagnostic).id, "one correlation identity must survive the whole legacy attempt")
		message.ID = 42
		return nil
	}
	service.send.Transactions.(*legacyGuardFixtureRunner).failCommitAt = 2
	result, err := service.SendMessage(context.Background(), input)
	var unknown *CommandOutcomeUnknownError
	require.ErrorAs(t, err, &unknown, "ambiguous whole-send commit must retain typed uncertainty")
	require.Nil(t, result, "unknown does not establish acceptance")
	require.Equal(t, db.LifecycleIngress{}, unknown.Ingress, "shape-valid v1 preparation cannot become a phantom legacy receipt")
	require.NotEqual(t, uuid.Nil, originalDiagnostic, "legacy diagnostic identity must be real and correlation-only")
	require.Equal(t, originalDiagnostic, unknown.DiagnosticID, "unknown must retain the original diagnostic identity")
	require.Equal(t, input.ClientMessageID, unknown.ClientMessageID, "original legacy client key remains correlation only")
}

func TestSendMessageLegacyBusinessContentionDoesNotBecomeStatusFallback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		parent bool
		events []string
	}{
		{name: "lane contention", events: []string{"begin", "commit", "begin", "claim-thread-false", "rollback"}},
		{name: "parent contention", parent: true, events: []string{"begin", "commit", "begin", "claim-thread-false", "claim-session-idle", "rollback"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service, deps := newTestService(t)
			input := SendMessageInput{OrgID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), Message: "continue"}
			contention := &pgconn.PgError{Code: "55P03"}
			deps.threadStore.claimIdleFn = func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.SessionThread, error) {
				if !tt.parent {
					return models.SessionThread{}, contention
				}
				return models.SessionThread{ID: input.ThreadID, OrgID: input.OrgID, SessionID: input.SessionID, Status: models.ThreadStatusRunning}, nil
			}
			deps.sessionStore.claimIdleFn = func(context.Context, uuid.UUID, uuid.UUID) (models.Session, error) {
				return models.Session{}, contention
			}
			deps.threadStore.getByIDFn = func(context.Context, uuid.UUID, uuid.UUID) (models.SessionThread, error) {
				t.Fatal("business contention must not become a thread status fallback")
				return models.SessionThread{}, nil
			}
			deps.sessionStore.getByIDFn = func(context.Context, uuid.UUID, uuid.UUID) (models.Session, error) {
				t.Fatal("business contention must not become a parent status fallback")
				return models.Session{}, nil
			}
			runner := service.send.Transactions.(*legacyGuardFixtureRunner)
			runner.contentionAt = 2
			result, err := service.SendMessage(context.Background(), input)
			var exhausted *db.LifecycleContentionError
			require.ErrorAs(t, err, &exhausted, "confirmed-aborted contention disposition must survive the service call")
			require.Equal(t, 3, exhausted.Attempts, "service must retain exhausted-attempt evidence")
			require.Nil(t, result, "contention is unaccepted")
			require.Equal(t, tt.events, runner.events, "whole-send contention must cause no status fallback, notification or compensation")
		})
	}
}

func TestSendMessageLegacyAtomicReplayDiscardsAbortedCapture(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		rejectRetry bool
	}{
		{name: "retry success returns only committed attempt"},
		{name: "retry guard rejection cannot expose earlier result", rejectRetry: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service, deps := newTestService(t)
			input := SendMessageInput{OrgID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), Message: "continue"}
			deps.threadStore.claimIdleFn = func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.SessionThread, error) {
				return models.SessionThread{ID: input.ThreadID, OrgID: input.OrgID, SessionID: input.SessionID, Status: models.ThreadStatusRunning}, nil
			}
			messages := 0
			deps.messageStore.createFn = func(_ context.Context, message *models.SessionMessage) error {
				messages++
				message.ID = int64(40 + messages)
				return nil
			}
			firstJob, committedJob := uuid.New(), uuid.New()
			jobs := 0
			deps.jobStore.enqueueWithOptsFn = func(context.Context, uuid.UUID, db.EnqueueOpts) (uuid.UUID, error) {
				jobs++
				if jobs == 1 {
					return firstJob, nil
				}
				return committedJob, nil
			}
			runner := service.send.Transactions.(*legacyGuardFixtureRunner)
			runner.retryAt, runner.rejectRetry = 2, tt.rejectRetry
			result, err := service.SendMessage(context.Background(), input)
			prefix := []string{"begin", "commit", "begin", "claim-thread-false", "claim-session-idle", "message", "enqueue", "rollback", "begin"}
			if tt.rejectRetry {
				require.ErrorIs(t, err, db.ErrThreadSendModeChanged, "fresh retry guard rejection must stop the attempt")
				require.Nil(t, result, "the rejected retry cannot expose the aborted attempt's message")
				require.Equal(t, append(prefix, "rollback"), runner.events, "a guard rejection must publish none of the aborted attempt's effects")
				require.Empty(t, runner.jobIDs, "no aborted job ID may escape through notifications")
			} else {
				require.NoError(t, err, "confirmed rollback permits the transaction owner's fresh attempt")
				require.Equal(t, int64(42), result.Message.ID, "only the successful attempt's inserted message may be returned")
				require.Equal(t, []uuid.UUID{committedJob}, runner.jobIDs, "only the successful attempt's job may be notified")
				require.Equal(t, append(prefix, "claim-thread-false", "claim-session-idle", "message", "enqueue", "commit", "claim-effect", "job-effect"), runner.events, "the retried attempt must publish exactly once after its own known commit")
			}
			require.Empty(t, deps.sessionStore.updateCalls, "transaction replay must never compensate an aborted capture")
		})
	}
}

func TestSendMessageLegacyOwnerLossFollowsKnownQueuedCommit(t *testing.T) {
	t.Parallel()
	failure := errors.New("synthetic queued message failure")
	tests := []struct {
		name    string
		queued  bool
		unknown bool
		abort   bool
		calls   int
	}{
		{name: "normal send does not recover"},
		{name: "known queue commit recovers with separate budget", queued: true, calls: 1},
		{name: "unknown queue settlement does not recover", queued: true, unknown: true},
		{name: "known queue abort does not recover", queued: true, abort: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service, deps := newTestService(t)
			input := SendMessageInput{OrgID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), Message: "continue"}
			thread := models.SessionThread{ID: input.ThreadID, OrgID: input.OrgID, SessionID: input.SessionID, Status: models.ThreadStatusRunning}
			deps.threadStore.claimIdleFn = func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.SessionThread, error) {
				if tt.queued {
					return models.SessionThread{}, pgx.ErrNoRows
				}
				return thread, nil
			}
			deps.threadStore.getByIDFn = func(context.Context, uuid.UUID, uuid.UUID) (models.SessionThread, error) { return thread, nil }
			deps.messageStore.createFn = func(_ context.Context, message *models.SessionMessage) error {
				if tt.abort {
					return failure
				}
				message.ID = 42
				return nil
			}
			runner := service.send.Transactions.(*legacyGuardFixtureRunner)
			if tt.unknown {
				runner.failCommitAt = 2
			}
			recovery := &mockOwnerLossRecovery{check: func(ctx context.Context) {
				require.True(t, runner.settled, "owner-loss recovery must run only after the whole queue transaction settles")
				require.Equal(t, "pending-effect", runner.events[len(runner.events)-1], "queue mutation effects must follow commit before recovery starts")
				require.ErrorIs(t, runner.activeCtx.Err(), context.Canceled, "the completed atomic callback context must already be released")
				require.NoError(t, ctx.Err(), "recovery must use its own postcommit context")
				deadline, ok := ctx.Deadline()
				require.True(t, ok, "owner-loss recovery must retain its existing bounded context")
				require.Positive(t, time.Until(deadline), "the separate recovery budget must be live after commit")
				require.LessOrEqual(t, time.Until(deadline), 5*time.Second, "owner-loss recovery must retain the existing five-second cap")
			}}
			service.SetOwnerLossOrchestrator(recovery)
			result, err := service.SendMessage(context.Background(), input)
			switch {
			case tt.unknown:
				require.ErrorIs(t, err, ErrCommandOutcomeUnknown, "unknown queue settlement must stop before recovery")
				require.Nil(t, result, "unknown queue settlement cannot expose accepted input")
			case tt.abort:
				require.ErrorIs(t, err, failure, "known queue abort must retain the write failure")
				require.Nil(t, result, "known queue abort cannot expose accepted input")
			default:
				require.NoError(t, err, "known queue or normal commit must retain its result")
				require.NotNil(t, result, "known commit must expose accepted input")
			}
			require.Len(t, recovery.calls, tt.calls, "only a known queue-only commit may start owner-loss recovery")
		})
	}
}
