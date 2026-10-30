package thread

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type sendPostgresRaceEntry struct {
	winner                  bool
	txID                    int64
	sessionEpoch, laneEpoch int64
}

type sendPostgresRaceUnit struct {
	guards  sendPostgresGuardCounts
	lookups int
	err     error
}

type sendPostgresRaceTransactions struct {
	base         SendTransactions
	fixture      sendPostgresFixture
	winner       bool
	entryReady   chan<- sendPostgresRaceEntry
	entryRelease <-chan struct{}
	loserMiss    chan struct{}
	loserRelease <-chan struct{}
	units        []sendPostgresRaceUnit
}

func (r *sendPostgresRaceTransactions) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	unitNumber := len(r.units) + 1
	// The winner acquires no connection until the loser's first actual
	// mutation lookup has missed. The loser then holds only a shared cutover
	// interlock, not the parent, so the second connection can finish admission.
	if r.winner && unitNumber == 2 {
		barrierCtx, cancel := context.WithTimeout(ctx, db.LifecycleTransactionTimeout)
		defer cancel()
		if err := waitSendPostgresBarrier(barrierCtx, r.loserMiss); err != nil {
			return err
		}
	}
	var unit sendPostgresRaceUnit
	var entry sendPostgresRaceEntry
	err := r.base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		unit = sendPostgresRaceUnit{}
		observed := sendPostgresGuardObservedTx{Tx: tx, counts: &unit.guards}
		wrapped := sendPostgresRaceTx{Tx: observed, trace: &unit}
		if !r.winner && unitNumber == 2 {
			wrapped.missReady, wrapped.release = r.loserMiss, r.loserRelease
		}
		if err := work(unitCtx, wrapped); err != nil {
			return err // Preserve the service's exact known-rollback marker.
		}
		if unitNumber == 1 {
			entry.winner = r.winner
			return tx.QueryRow(unitCtx, `SELECT txid_current(),s.admission_epoch,t.admission_epoch
 FROM sessions s JOIN session_threads t ON t.org_id=s.org_id AND t.session_id=s.id
 WHERE s.org_id=$1 AND s.id=$2 AND t.id=$3`, r.fixture.org, r.fixture.session, r.fixture.lanes[0]).Scan(&entry.txID, &entry.sessionEpoch, &entry.laneEpoch)
		}
		return nil
	})
	unit.err = err
	r.units = append(r.units, unit)
	if err != nil || unitNumber != 1 {
		return err
	}
	// Only known completed entry observations reach this barrier. No parent
	// or lane lock is held while both commands wait for the other's capture.
	r.entryReady <- entry
	barrierCtx, cancel := context.WithTimeout(ctx, db.LifecycleTransactionTimeout)
	defer cancel()
	return waitSendPostgresBarrier(barrierCtx, r.entryRelease)
}

type sendPostgresRaceTx struct {
	pgx.Tx
	trace     *sendPostgresRaceUnit
	missReady chan struct{}
	release   <-chan struct{}
}

func (t sendPostgresRaceTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	row := t.Tx.QueryRow(ctx, query, args...)
	if !strings.Contains(query, "FROM session_input_dispatches WHERE org_id=$1 AND ingress_kind=$2 AND ingress_key=$3") {
		return row
	}
	t.trace.lookups++
	first := t.trace.lookups == 1
	return sendPostgresAfterScanRow{Row: row, after: func(err error) error {
		if first && t.missReady != nil && errors.Is(err, pgx.ErrNoRows) {
			close(t.missReady)
			if barrierErr := waitSendPostgresBarrier(ctx, t.release); barrierErr != nil {
				return barrierErr
			}
		}
		return err
	}}
}

func TestThreadSendSameKeyLateWinnerPostgres(t *testing.T) {
	t.Parallel()
	f := newSendPostgresFixture(t, true, 1)
	require.Equal(t, int32(2), f.pool.Config().MaxConns, "deterministic race must use only the explicit two-connection baseline, never a spare observer connection")
	winner, winnerEffects := f.service(t, true)
	loser, loserEffects := f.service(t, true)
	ready := make(chan sendPostgresRaceEntry, 2)
	entryRelease, loserMiss, loserRelease := make(chan struct{}), make(chan struct{}), make(chan struct{})
	winnerRunner := &sendPostgresRaceTransactions{base: winner.send.Transactions, fixture: f, winner: true, entryReady: ready, entryRelease: entryRelease, loserMiss: loserMiss}
	loserRunner := &sendPostgresRaceTransactions{base: loser.send.Transactions, fixture: f, entryReady: ready, entryRelease: entryRelease, loserMiss: loserMiss, loserRelease: loserRelease}
	winner.send.Transactions, loser.send.Transactions = winnerRunner, loserRunner
	// Each real runner retains its unchanged two-second unit budget. This
	// outer cancellation only bounds test cleanup, including post-Run barriers.
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	type outcome struct {
		result *SendMessageResult
		err    error
	}
	winnerDone, loserDone := make(chan outcome, 1), make(chan outcome, 1)
	input := f.input(0, "synthetic-service-late-winner")
	go func() { result, err := winner.SendMessage(ctx, input); winnerDone <- outcome{result, err} }()
	go func() { result, err := loser.SendMessage(ctx, input); loserDone <- outcome{result, err} }()
	entries := make(map[bool]sendPostgresRaceEntry, 2)
	for range 2 {
		select {
		case entry := <-ready:
			entries[entry.winner] = entry
		case ended := <-winnerDone:
			t.Fatalf("winner ended before both real entry commits: %v", ended.err)
		case ended := <-loserDone:
			t.Fatalf("loser ended before both real entry commits: %v", ended.err)
		case <-ctx.Done():
			t.Fatal("both real entry observations did not complete before mutation release")
		}
	}
	require.Equal(t, 2, len(entries), "both independent real service observations must have committed before either mutation")
	require.NotEqual(t, entries[true].txID, entries[false].txID, "the commands cannot share an encompassing transaction or captured parent lock")
	for _, entry := range entries {
		require.Equal(t, []int64{3, 5}, []int64{entry.sessionEpoch, entry.laneEpoch}, "each committed entry must retain the original exact epochs before the race")
	}
	close(entryRelease)
	var won outcome
	select {
	case won = <-winnerDone:
	case ended := <-loserDone:
		t.Fatalf("loser ended before winner commit and late-lookup release: %v", ended.err)
	case <-ctx.Done():
		t.Fatal("winner did not finish while loser was paused after a real exact-lookup miss")
	}
	require.NoError(t, won.err, "winner must complete the real accepted graph while loser holds no parent lock")
	require.NotNil(t, won.result, "known committed winner must return its accepted message")
	require.NotNil(t, won.result.Admission, "known committed winner must expose its original canonical receipt")
	require.Equal(t, db.MessageAdmissionNewJob, won.result.Admission.Disposition(), "designated winner must perform the one new admission")
	close(loserRelease)
	var lost outcome
	select {
	case lost = <-loserDone:
	case <-ctx.Done():
		t.Fatal("loser did not settle rollback and fresh exact observation within bounded real runners")
	}
	require.NoError(t, lost.err, "late winner must resolve only through fresh exact observation after the losing mutation rolls back")
	require.NotNil(t, lost.result, "loser must return the existing accepted message rather than unresolved contention")
	require.NotNil(t, lost.result.Admission, "loser must expose only the existing canonical receipt")
	a, b := won.result.Admission, lost.result.Admission
	require.Equal(t, db.MessageAdmissionReplayed, b.Disposition(), "loser cannot masquerade as a second admitted job")
	require.Equal(t, db.MessageAdmissionNewJob, b.OriginalDisposition(), "fresh replay retains the winner's original disposition")
	require.Equal(t, a.Fence(), b.Fence(), "late replay must return original epochs rather than capture another fence")
	laneEpoch := int64(5)
	wantFence, fenceErr := db.NewAdmissionFence(f.session, &f.lanes[0], 3, &laneEpoch)
	require.NoError(t, fenceErr, "construct only an independent expected immutable shape for the original explicit target")
	require.Equal(t, wantFence, a.Fence(), "winner must retain the original captured parent and lane epochs, not a new implicit expectation")
	require.Equal(t, a.Ingress(), b.Ingress(), "same semantic key must resolve the exact original digest")
	require.Equal(t, a.OrgID(), b.OrgID(), "replay cannot expose a receipt from another tenant")
	require.Equal(t, a.DispatchID(), b.DispatchID(), "both outcomes must name one immutable accepted input")
	require.Equal(t, a.DispatchOptions(), b.DispatchOptions(), "loser's once-prepared server schedule cannot replace winner options")
	require.Equal(t, a.TargetNodeID(), b.TargetNodeID(), "replay retains the original concrete target option without inventing provider authority")
	aAuthority, aHasAuthority := a.Authority()
	bAuthority, bHasAuthority := b.Authority()
	require.Equal(t, []bool{true, true}, []bool{aHasAuthority, bHasAuthority}, "the original logical claim receipt remains present without authorizing a second mutation")
	require.Equal(t, aAuthority, bAuthority, "loser returns the exact original logical dispatch/token/turn receipt")
	aHolder, aHasHolder := a.LeaseHolderID()
	bHolder, bHasHolder := b.LeaseHolderID()
	require.Equal(t, []bool{true, true}, []bool{aHasHolder, bHasHolder}, "both receipts must retain the original concrete changeset holder")
	require.Equal(t, aHolder, bHolder, "loser's newly prepared holder cannot replace the committed winner holder")
	aJob, aHasJob := a.JobID()
	bJob, bHasJob := b.JobID()
	require.Equal(t, []bool{true, true}, []bool{aHasJob, bHasJob}, "late replay must retain the one original consumer")
	require.Equal(t, aJob, bJob, "no second job identity may be adopted")
	require.Equal(t, won.result.Message, lost.result.Message, "both callers return the identical complete stored message")
	require.Equal(t, won.result.InboxEntry, lost.result.InboxEntry, "both callers return the identical complete inbox association")
	require.Equal(t, sendPostgresGraph{messages: 1, inbox: 1, dispatches: 1, continueJobs: 1, claims: 1, parentStatus: models.SessionStatusRunning, threadStatus: models.ThreadStatusRunning}, sendPostgresReadGraph(t, f), "two service calls with a deterministic late winner create exactly one durable accepted graph and no execution increments")
	requireSendPostgresClaimLink(t, f, *a, aJob, won.result.Message)
	require.Equal(t, []uuid.UUID{aJob}, winnerEffects.jobs, "only the known committed winner may wake its exact new job")
	require.Equal(t, 1, len(winnerEffects.lifecycle), "winner publishes its committed effect bundle once")
	require.Empty(t, loserEffects.lifecycle, "losing rollback and fresh receipt observation publish no bundle")
	require.Empty(t, loserEffects.jobs, "loser cannot repeat the original wake")
	require.Equal(t, 2, len(winnerRunner.units), "winner has exactly observation plus one mutating unit")
	require.Equal(t, 3, len(loserRunner.units), "loser has observation, one aborted mutation and one fresh nonmutating receipt observation")
	require.ErrorIs(t, loserRunner.units[1].err, db.ErrMessageAdmissionReplayObservationNeeded, "actual late parent-locked lookup must trigger the finite rollback-to-observation disposition")
	require.NotErrorIs(t, loserRunner.units[1].err, ErrCommandOutcomeUnknown, "actual successful whole rollback is known, not a fabricated lost acknowledgement")
	require.Equal(t, []int{1, 2, 2}, []int{loserRunner.units[0].lookups, loserRunner.units[1].lookups, loserRunner.units[2].lookups}, "the loser must actually miss once, discover the parent-locked winner second, then read the exact receipt in a separate unit")
	for _, unit := range append(append([]sendPostgresRaceUnit(nil), winnerRunner.units...), loserRunner.units...) {
		require.Equal(t, 1, unit.guards.parent, "each observation or mutation locks the parent once; mutation must not call a second expected-fence capture")
		require.Equal(t, 1, unit.guards.lanes, "each real unit must consume its one canonical sorted-lane lock query")
	}
}
