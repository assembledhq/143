package codereview

import (
	"context"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type confirmedCaptureTransactionOwner struct {
	*pgxpool.Pool
	begins, commits, rollbacks, nested, observations, metadataLocks int
}

func (o *confirmedCaptureTransactionOwner) Begin(ctx context.Context) (pgx.Tx, error) {
	o.begins++
	tx, err := o.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &confirmedCaptureTransaction{Tx: tx, owner: o}, nil
}

type confirmedCaptureTransaction struct {
	pgx.Tx
	owner *confirmedCaptureTransactionOwner
}

func (tx *confirmedCaptureTransaction) Begin(ctx context.Context) (pgx.Tx, error) {
	tx.owner.nested++
	return tx.Tx.Begin(ctx)
}

func (tx *confirmedCaptureTransaction) Commit(ctx context.Context) error {
	tx.owner.commits++
	return tx.Tx.Commit(ctx)
}

func (tx *confirmedCaptureTransaction) Rollback(ctx context.Context) error {
	tx.owner.rollbacks++
	return tx.Tx.Rollback(ctx)
}

func (tx *confirmedCaptureTransaction) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if strings.HasPrefix(sql, "SELECT a.id,COALESCE(a.xmin::text,'')") {
		tx.owner.observations++
	}
	if strings.HasPrefix(sql, "SELECT id FROM code_review_session_metadata WHERE org_id=$1 AND session_id=$2 AND pull_request_id=$3 AND id=$4 FOR UPDATE") {
		tx.owner.metadataLocks++
	}
	return tx.Tx.QueryRow(ctx, sql, args...)
}

// These are the real package-local scheduling paths, deliberately not public
// call-through races. Public positive readers have separate fast-path coverage.
func TestConfirmedCaptureTransactionPreservesOriginalPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		starter       bool
		metadataLocks int
	}{
		{name: "refresh rechecks the locked source and rolls back the wrapper", metadataLocks: 1},
		{name: "queued starter observes its supplied transaction without metadata locking", starter: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := newScheduledSessionPostgresPool(t)
			require.Equal(t, int32(2), pool.Config().MaxConns, "the observer and real scheduling owner must exhaust the literal two-slot pool")
			f := confirmedReadersSeed(t, ctx, pool, false)
			foreign := confirmedReadersSeed(t, ctx, pool, false)
			observer, err := pool.Acquire(ctx)
			require.NoError(t, err, "reserve the only independent observer before actual scheduling begins")
			defer observer.Release()
			before, err := confirmedReadersGraph(ctx, observer, f.input.OrgID)
			require.NoError(t, err, "retain the complete nonempty original graph including schedule timestamps and publication evidence")
			foreignBefore, err := confirmedReadersGraph(ctx, observer, foreign.input.OrgID)
			require.NoError(t, err, "retain an independent tenant's complete confirmed owner and admission graph")
			store := confirmedReadersStore(t, pool)
			service := NewService(store, store, db.NewSessionStore(pool), db.NewJobStore(pool), zerolog.Nop(), Config{})
			owner := &confirmedCaptureTransactionOwner{Pool: pool}
			provider := &confirmedReadersProviderTrap{}
			service.SetScheduling(db.NewCodeReviewScheduleStore(owner), provider)
			mr := miniredis.RunT(t)
			client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + mr.Addr()}, zerolog.Nop(), nil)
			require.NotNil(t, client, "use only an isolated cache client for real scheduler effects")
			t.Cleanup(func() {
				require.NoError(t, client.Close(), "close the isolated cache client after its real scheduling operation")
			})
			streams := cache.NewCodeReviewStreams(client, zerolog.Nop())
			require.NoError(t, streams.PublishUpdated(ctx, f.input.OrgID, models.CodeReviewUpdatedEvent{OrgID: f.input.OrgID}), "prime the isolated cache connection before measuring forbidden post-commit effects")
			service.SetSchedulingStreams(streams)
			commands := mr.CommandCount()
			acquisitions := pool.Stat().AcquireCount()
			input := ReviewChangedInput{OrgID: f.input.OrgID, RepositoryID: f.input.RepositoryID, PullRequestID: f.input.PullRequestID,
				PriorSessionID: f.metadata.SessionID, PriorMetadataID: &f.metadata.ID, GitHubRepo: f.input.GitHubRepo,
				GitHubPRNumber: f.input.GitHubPRNumber, GitHubPRURL: f.input.GitHubPRURL, PullRequestTitle: f.input.PullRequestTitle,
				BaseSHA: f.input.BaseSHA, HeadSHA: f.input.HeadSHA, TriggerSource: f.metadata.TriggerSource,
				ChangeReason: captureIdentityChangeReason, ExplicitRequest: true}
			var result ReviewRequestedResult
			if tt.starter {
				result, err = service.handleSerializedReviewChanged(ctx, input)
			} else {
				result, err = service.refreshSchedule(ctx, input, models.CodeReviewReviewNow, true, nil)
			}
			require.NoError(t, err, "the real inner positive must translate its rollback sentinel to source preservation, not replacement or failure")
			require.Equal(t, ReviewRequestedResult{Processed: true, Reused: true, SessionID: f.metadata.SessionID,
				MetadataID: f.metadata.ID, TriggerSource: f.metadata.TriggerSource, IgnoredReason: CaptureIdentitySourceCompletionPendingReason}, result,
				"the supplied transaction must return only the original confirmed owner with no dispatch or new handoff authority")
			require.Equal(t, [6]int{1, 0, 1, 0, 1, tt.metadataLocks}, [6]int{owner.begins, owner.commits, owner.rollbacks, owner.nested, owner.observations, owner.metadataLocks},
				"positive evidence must come from exactly one real supplied transaction that rolls back the whole wrapper before settlement effects")
			require.Equal(t, int64(1), pool.Stat().AcquireCount()-acquisitions, "all transaction-local reads must stay on the sole owner checkout rather than escape to its saturated pool")
			require.Equal(t, [3]int{1, 0, 0}, [3]int{provider.prepared, provider.reads, provider.syncs}, "reader preparation may occur before the owner, but confirmed preservation must never invoke provider work")
			require.Equal(t, commands, mr.CommandCount(), "rollback must suppress the actual configured scheduling publication")
			after, err := confirmedReadersGraph(ctx, observer, f.input.OrgID)
			require.NoError(t, err, "read every original row after the actual scheduling owner has released its checkout")
			foreignAfter, err := confirmedReadersGraph(ctx, observer, foreign.input.OrgID)
			require.NoError(t, err, "read every independent tenant row after rollback")
			require.Equal(t, before, after, "the sentinel must preserve the full schedule, request, job, session, receipt, outcome, dispute and message graph including timestamps")
			require.Equal(t, foreignBefore, foreignAfter, "positive exact source preservation must neither borrow nor mutate foreign tenant evidence")
		})
	}
}
