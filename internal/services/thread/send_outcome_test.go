package thread

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

type sendTransactionsFunc func(context.Context, func(context.Context, pgx.Tx) error) error

func (f sendTransactionsFunc) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	return f(ctx, work)
}

func TestSendUnitOutcomeProof(t *testing.T) {
	t.Parallel()
	failure, rollbackFailure := errors.New("synthetic work failure"), errors.New("synthetic rollback failure")
	tests := []struct {
		name     string
		run      func(context.Context, func(context.Context, pgx.Tx) error) error
		work     func(int) error
		want     error
		unknown  bool
		attempts int
	}{
		{name: "known commit", run: func(ctx context.Context, w func(context.Context, pgx.Tx) error) error { return w(ctx, nil) }, work: func(int) error { return nil }, attempts: 1},
		{name: "exact callback abort", run: func(ctx context.Context, w func(context.Context, pgx.Tx) error) error { return w(ctx, nil) }, work: func(int) error { return failure }, want: failure, attempts: 1},
		{name: "joined rollback is unknown", run: func(ctx context.Context, w func(context.Context, pgx.Tx) error) error {
			return errors.Join(w(ctx, nil), rollbackFailure)
		}, work: func(int) error { return failure }, unknown: true, attempts: 1},
		{name: "budget after callback is unknown", run: func(ctx context.Context, w func(context.Context, pgx.Tx) error) error {
			return errors.Join(w(ctx, nil), context.DeadlineExceeded)
		}, work: func(int) error { return failure }, unknown: true, attempts: 1},
		{name: "success then commit rollback remains unknown", run: func(ctx context.Context, w func(context.Context, pgx.Tx) error) error {
			if err := w(ctx, nil); err != nil {
				return err
			}
			return pgx.ErrTxCommitRollback
		}, work: func(int) error { return nil }, unknown: true, attempts: 1},
		{name: "transient callback then commit failure", run: func(ctx context.Context, w func(context.Context, pgx.Tx) error) error {
			first := w(ctx, nil)
			var pg *pgconn.PgError
			if !errors.As(first, &pg) || pg.Code != "40P01" {
				return errors.New("callback marker lost retry SQLSTATE")
			}
			if err := w(ctx, nil); err != nil {
				return err
			}
			return rollbackFailure
		}, work: func(attempt int) error {
			if attempt == 1 {
				return &pgconn.PgError{Code: "40P01"}
			}
			return nil
		}, unknown: true, attempts: 2},
		{name: "later setup failure cannot reuse previous abort", run: func(ctx context.Context, w func(context.Context, pgx.Tx) error) error {
			if err := w(ctx, nil); err == nil {
				return errors.New("expected callback failure")
			}
			return rollbackFailure
		}, work: func(int) error { return failure }, unknown: true, attempts: 1},
		{name: "callback never entered", run: func(context.Context, func(context.Context, pgx.Tx) error) error { return failure }, work: func(int) error { return nil }, want: failure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			diagnostic := sendDiagnostic{id: uuid.New(), clientKey: "synthetic-key"}
			ctx := context.WithValue(context.Background(), sendDiagnosticContextKey{}, diagnostic)
			ingress, ingressErr := db.NewLifecycleIngress("synthetic-test", "original-key", db.LifecycleDigest{1})
			require.NoError(t, ingressErr, "construct an immutable shape-only ingress for outcome retention, not admission authority")
			ctx = context.WithValue(ctx, sendIngressContextKey{}, ingress)
			service := &Service{send: SendDependencies{Transactions: sendTransactionsFunc(tt.run)}}
			attempts := 0
			err := service.runSendUnit(ctx, func(context.Context, pgx.Tx) error { attempts++; return tt.work(attempts) })
			require.Equal(t, tt.attempts, attempts, "the outcome wrapper must neither retry nor replay work on its own")
			if tt.unknown {
				var unknown *CommandOutcomeUnknownError
				require.ErrorAs(t, err, &unknown, "lack of exact rollback proof must preserve a typed unknown outcome")
				require.Equal(t, diagnostic.id, unknown.DiagnosticID, "unknown outcome must retain the original stable diagnostic identity")
				require.Equal(t, diagnostic.clientKey, unknown.ClientMessageID, "unknown outcome retains the original client correlation key")
				require.Equal(t, ingress, unknown.Ingress, "unknown must retain the original full ingress across aborted retry attempts")
				require.ErrorIs(t, err, ErrCommandOutcomeUnknown, "unknown must remain machine recognizable")
				require.Equal(t, ErrCommandOutcomeUnknown.Error(), err.Error(), "unknown error text must not leak command keys")
				return
			}
			if tt.want != nil {
				require.ErrorIs(t, err, tt.want, "exact known abort must retain its original work cause")
			} else {
				require.NoError(t, err, "nil Run result is a known completed unit")
			}
		})
	}
}

func TestPrepareSendMessageFreezesSemanticIdentity(t *testing.T) {
	t.Parallel()
	org, session, lane, actor, changeset := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	input := SendMessageInput{OrgID: org, SessionID: session, ThreadID: lane, UserID: &actor, ChangesetID: &changeset, ClientMessageID: "synthetic-client-key", Message: "continue", Images: []string{"synthetic-image"}}
	first, holder, err := prepareSendMessage(input)
	require.NoError(t, err, "ordinary valid input must prepare without database reads")
	second, otherHolder, err := prepareSendMessage(input)
	require.NoError(t, err, "a new call may choose fresh server values")
	require.Equal(t, first.Ingress, second.Ingress, "client replay identity must exclude server time and newly minted holder")
	require.NotEqual(t, *holder, *otherHolder, "independent calls retain separate candidate lease holders before replay resolves the original")
	epoch := int64(7)
	first.Fence, err = db.NewAdmissionFence(session, &lane, 5, &epoch)
	require.NoError(t, err, "freeze an explicit observation outside mutating attempts")
	command, err := db.NewMessageAdmission(first)
	require.NoError(t, err, "epoch capture must not change the prepared semantic digest")
	first.Images[0] = "changed-after-freeze"
	require.Equal(t, []string{"synthetic-image"}, command.Input().Images, "command must own an immutable copy across transaction retries")
	noKey := input
	noKey.ClientMessageID = ""
	a, _, err := prepareSendMessage(noKey)
	require.NoError(t, err, "keyless call must receive one stable command identity")
	b, _, err := prepareSendMessage(noKey)
	require.NoError(t, err, "repeated keyless text is a distinct call")
	require.NotEqual(t, a.Ingress.Key(), b.Ingress.Key(), "identical keyless content must not become synthetic replay deduplication")
}
