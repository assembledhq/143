package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// These two schedules prove actual retirement/installation storage composition,
// not Worker.poll, provider delivery, real Send, effects, ACK ambiguity or
// activation. Each parallel leaf owns a separate full-schema database. Exactly
// three checkouts serve the two existing bounded transaction owners and one
// independent observer warmed before either unit; no policy or budget changes.
type terminalInstallerOrderEntry struct {
	ctx   context.Context
	pid   int32
	clock time.Time
}

type terminalInstallerOrderHeld struct {
	terminalInstallerOrderEntry
	graph    json.RawMessage
	prepared db.PreparedCodeReviewDispatch
}

func terminalInstallerOrderEntryInTx(ctx context.Context, tx pgx.Tx) (terminalInstallerOrderEntry, error) {
	entry := terminalInstallerOrderEntry{ctx: ctx}
	err := tx.QueryRow(ctx, `SELECT pg_backend_pid(),transaction_timestamp()`).Scan(&entry.pid, &entry.clock)
	return entry, err
}

// This transparent supplied-capability wrapper calls the original callback
// unchanged and delegates all acquisition, policy, retries and settlement to
// the real existing runner. The optional after-work barrier holds only the
// actual callback before that runner's commit, never fabricating an outcome.
type terminalInstallerOrderTerminalRun struct {
	base    db.CodeReviewMetadataTransactions
	fixture strandedReviewFixture
	entered chan<- terminalInstallerOrderEntry
	held    chan<- terminalInstallerOrderHeld
	release <-chan struct{}
}

func (r *terminalInstallerOrderTerminalRun) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	return r.base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		entry, err := terminalInstallerOrderEntryInTx(unitCtx, tx)
		if err != nil {
			return err
		}
		select {
		case r.entered <- entry:
		case <-unitCtx.Done():
			return unitCtx.Err()
		}
		if err := work(unitCtx, tx); err != nil {
			return err
		}
		if r.held == nil {
			return nil
		}
		graph, err := terminalContentionSnapshot(unitCtx, tx, r.fixture)
		if err != nil {
			return err
		}
		select {
		case r.held <- terminalInstallerOrderHeld{terminalInstallerOrderEntry: entry, graph: graph}:
		case <-unitCtx.Done():
			return unitCtx.Err()
		}
		select {
		case <-r.release:
			return nil
		case <-unitCtx.Done():
			return unitCtx.Err()
		}
	})
}

func terminalInstallerOrderInstall(ctx context.Context, runner db.CodeReviewMetadataTransactions, f strandedReviewFixture, in db.CodeReviewDispatchPreparation, entered chan<- terminalInstallerOrderEntry, held chan<- terminalInstallerOrderHeld, release <-chan struct{}) (db.PreparedCodeReviewDispatch, error) {
	var prepared db.PreparedCodeReviewDispatch
	err := runner.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		prepared = db.PreparedCodeReviewDispatch{}
		entry, err := terminalInstallerOrderEntryInTx(unitCtx, tx)
		if err != nil {
			return err
		}
		select {
		case entered <- entry:
		case <-unitCtx.Done():
			return unitCtx.Err()
		}
		prepared, err = db.PrepareCodeReviewDispatchInTx(unitCtx, f.job.OrgID, tx, in)
		if err != nil {
			return err
		}
		if held == nil {
			return nil
		}
		graph, err := terminalContentionSnapshot(unitCtx, tx, f)
		if err != nil {
			return err
		}
		select {
		case held <- terminalInstallerOrderHeld{terminalInstallerOrderEntry: entry, graph: graph, prepared: prepared}:
		case <-unitCtx.Done():
			return unitCtx.Err()
		}
		select {
		case <-release:
			return nil
		case <-unitCtx.Done():
			return unitCtx.Err()
		}
	})
	if err != nil {
		return db.PreparedCodeReviewDispatch{}, err
	}
	return prepared, nil
}

func terminalInstallerOrderWait(t *testing.T, observer *pgxpool.Conn, holder, contender terminalInstallerOrderEntry, fragment string, finished <-chan struct{}) {
	t.Helper()
	require.NotEqual(t, holder.pid, contender.pid, "the actual installer and retirement owner must use independent existing transaction checkouts")
	for {
		select {
		case <-finished:
			t.Fatal("the contender ended before its actual metadata statement was positively observed waiting on this exact holder")
		case <-holder.ctx.Done():
			t.Fatal("the unchanged held owner budget expired before the actual holder-specific metadata wait was proven")
		case <-contender.ctx.Done():
			t.Fatal("the unchanged contender budget expired before its actual metadata wait was proven")
		default:
		}
		_, err := observer.Exec(holder.ctx, `SELECT pg_stat_clear_snapshot()`)
		require.NoError(t, err, "discard only observer catalog caching before reading exact live transaction/PID/blocker/query evidence")
		var waiting bool
		err = observer.QueryRow(holder.ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity a
			WHERE a.datname=current_database() AND a.pid=$1::integer AND a.xact_start=$2::timestamptz
			AND a.state='active' AND a.wait_event_type='Lock' AND pg_blocking_pids(a.pid)=ARRAY[$3::integer]
			AND position('code_review_session_metadata' IN a.query)>0 AND position($4::text IN a.query)>0)`, contender.pid, contender.clock, holder.pid, fragment).Scan(&waiting)
		require.NoError(t, err, "read the exact original metadata query, actual transaction start and sole independently retained holder before permitting commit")
		if waiting {
			return
		}
	}
}

func terminalInstallerOrderFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (strandedReviewFixture, db.CodeReviewDispatchPreparation) {
	t.Helper()
	f := terminalOwnerPointerPositiveFixture(t, pool, false)
	var ledger int
	var dirty bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT version,dirty FROM schema_migrations`).Scan(&ledger, &dirty), "read the actual appended schema ledger before synthetic intent enablement")
	require.Equal(t, 392, ledger, "the new unresolved marker must use the fully migrated appended status schema")
	require.False(t, dirty, "the installer-order proof cannot use a dirty schema ledger")
	tag, err := pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready AND dispatch_status_ready`)
	require.NoError(t, err, "enable only this disposable synthetic database behind both real readiness gates")
	require.Equal(t, int64(1), tag.RowsAffected(), "both readiness gates must genuinely admit the fixture's real Prepare path")
	thread := uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO session_threads(id,org_id,session_id,agent_type,label,status,completed_at)
		VALUES($1,$2,$3,'claude_code','Synthetic installer-order lane','completed',statement_timestamp())`, thread, f.job.OrgID, f.job.SessionID)
	require.NoError(t, err, "seed the actual tenant/session/provider/model lane used by Prepare's existing correlation query")
	sibling := f
	require.NoError(t, pool.QueryRow(ctx, `SELECT id,head_sha,review_output_key FROM code_review_session_metadata WHERE org_id=$1 AND session_id=$2 AND id<>$3`, f.job.OrgID, f.job.SessionID, f.job.MetadataID).
		Scan(&sibling.job.MetadataID, &sibling.job.HeadSHA, &sibling.job.OutputKey), "retain the actual distinct same-session owner before creating a nonempty sibling pending canary")
	fullAssessmentDispatchChild(t, ctx, pool, sibling, thread, "pending")
	// The real current producer always uses this non-NULL original key. The
	// pending/running partial unique index means actual Prepare must dedupe its
	// original wake against this retained running controller, not invent another
	// wake job. The genuinely NEW object in the installer-first gate is its child.
	var payload json.RawMessage
	require.NoError(t, pool.QueryRow(ctx, `SELECT payload FROM jobs WHERE org_id=$1 AND id=$2 AND queue='agent' AND job_type='run_code_review' AND status='running' AND dedupe_key=$3 AND lock_token=$4`, f.job.OrgID, f.jobID, "code_review:"+f.job.OutputKey, f.token).Scan(&payload), "retain the real producer's non-NULL original running key and freeze its entire payload without rerendering or dropping provenance")
	result := uuid.New()
	key := "cr-dispatch:v1:" + result.String()
	in := db.CodeReviewDispatchPreparation{ResultID: result, SessionID: f.job.SessionID, MetadataID: f.job.MetadataID, ThreadID: thread,
		AgentProvider: "claude_code", Version: 1, Key: key, Digest: strings.Repeat("a", 64), ReviewOutputKey: f.job.OutputKey,
		RawOutput: new("Synthetic original installer-order evidence"), StructuredResult: json.RawMessage(`{"synthetic":"installer-order"}`)}
	in.Input = fullAssessmentDispatchJSON(t, map[string]any{"version": 1, "purpose": db.CallerSendStampCodeReviewDispatch,
		"org_id": f.job.OrgID, "session_id": f.job.SessionID, "thread_id": thread, "client_message_id": key, "message": "Synthetic original installer command"})
	in.Wake = fullAssessmentDispatchJSON(t, map[string]any{"job_type": "run_code_review", "queue": "agent", "dedupe_key": "code_review:" + f.job.OutputKey, "payload": payload})
	return f, in
}

func terminalInstallerOrderAbsent(t *testing.T, ctx context.Context, observer *pgxpool.Conn, f strandedReviewFixture, in db.CodeReviewDispatchPreparation) {
	t.Helper()
	var counts [2]int
	require.NoError(t, observer.QueryRow(ctx, `SELECT
		(SELECT count(*)::int FROM code_review_agent_results WHERE org_id=$1 AND session_id=$2 AND id=$3),
		(SELECT count(*)::int FROM jobs WHERE org_id=$1 AND dedupe_key=$4)`, f.job.OrgID, f.job.SessionID, in.ResultID, "code_review:"+in.ReviewOutputKey).Scan(&counts[0], &counts[1]),
		"independently inspect the exact NEW preminted result and retained original wake key, not merely all-session cardinality")
	require.Equal(t, [2]int{0, 1}, counts, "before installer commit or after terminal-first refusal there must be no NEW original child and exactly the retained original-key controller, never a replacement wake")
}

func terminalInstallerOrderAssertNewChild(t *testing.T, before, staged json.RawMessage, in db.CodeReviewDispatchPreparation) {
	t.Helper()
	expected, actual := terminalOwnerPointerPositiveDecode(t, before), terminalOwnerPointerPositiveDecode(t, staged)
	kept := make([]map[string]json.RawMessage, 0, len(actual.rows["results"]))
	removed := 0
	for _, row := range actual.rows["results"] {
		if string(row["id"]) == string(fullAssessmentDispatchJSON(t, in.ResultID)) {
			removed++
			continue
		}
		kept = append(kept, row)
	}
	require.Equal(t, 1, removed, "actual Prepare must insert exactly the NEW preminted original child, not merely update a visible result")
	actual.rows["results"] = kept
	require.Equal(t, expected, actual, "before its first commit actual Prepare may add only the one NEW original child; it must preserve every complete original wake, sibling, metadata, evidence and unrelated graph field")
}

func terminalInstallerOrderAssertGraph(t *testing.T, before, after json.RawMessage, f strandedReviewFixture, in db.CodeReviewDispatchPreparation, failure db.CodeReviewTerminalFailure, unresolved bool, stamp, afterClock time.Time) {
	t.Helper()
	if !unresolved {
		terminalOwnerPointerPositiveAssertGraph(t, before, after, f, failure, false, stamp)
		return
	}
	expected, actual := terminalOwnerPointerPositiveDecode(t, before), terminalOwnerPointerPositiveDecode(t, after)
	job, actualJob := terminalOwnerPointerPositiveRow(t, expected, "jobs", f.jobID), terminalOwnerPointerPositiveRow(t, actual, "jobs", f.jobID)
	job["status"], job["last_error"], job["owner_kind"] = fullAssessmentDispatchJSON(t, "dead_letter"), fullAssessmentDispatchJSON(t, failure.JobError), fullAssessmentDispatchJSON(t, "worker")
	for _, field := range []string{"locked_by_node_id", "run_owner_id", "lock_token", "locked_at", "lease_expires_at"} {
		job[field] = json.RawMessage(`null`)
	}
	terminalOwnerPointerPositiveClock(t, job, actualJob, "completed_at", stamp)
	terminalOwnerPointerPositiveClock(t, job, actualJob, "updated_at", stamp)
	metadata, actualMetadata := terminalOwnerPointerPositiveRow(t, expected, "metadata", f.job.MetadataID), terminalOwnerPointerPositiveRow(t, actual, "metadata", f.job.MetadataID)
	metadata["status"] = fullAssessmentDispatchJSON(t, "failed")
	for _, field := range []string{"phase", "decision", "acceptable", "retry_at"} {
		metadata[field] = json.RawMessage(`null`)
	}
	metadata["failure_reason"], metadata["status_code"], metadata["status_message"] = fullAssessmentDispatchJSON(t, strings.TrimSpace(failure.Reason)), fullAssessmentDispatchJSON(t, models.CodeReviewStatusCodeDispatchUnresolved), fullAssessmentDispatchJSON(t, "The review stopped with a reviewer dispatch still unresolved. Its original intent is retained for reconciliation; do not resend that command.")
	metadata["retryable_failure"] = fullAssessmentDispatchJSON(t, false)
	terminalOwnerPointerPositiveClock(t, metadata, actualMetadata, "last_error_at", stamp)
	terminalOwnerPointerPositiveClock(t, metadata, actualMetadata, "completed_at", stamp)
	assessment, actualAssessment := terminalOwnerPointerPositiveRow(t, expected, "assessments", f.assessment), terminalOwnerPointerPositiveRow(t, actual, "assessments", f.assessment)
	assessment["status"], assessment["failure_detail"] = fullAssessmentDispatchJSON(t, "failed"), fullAssessmentDispatchJSON(t, "dispatch_unresolved:"+strings.TrimSpace(failure.Reason))
	terminalOwnerPointerPositiveClock(t, assessment, actualAssessment, "completed_at", stamp)
	child, actualChild := terminalOwnerPointerPositiveRow(t, expected, "results", in.ResultID), terminalOwnerPointerPositiveRow(t, actual, "results", in.ResultID)
	require.Equal(t, json.RawMessage(`"pending"`), child["dispatch_state"], "the installer must have inserted a genuinely NEW pending child before retirement's parent-lock wait")
	require.Equal(t, json.RawMessage(`0`), child["dispatch_writer_seq"], "the newly inserted child must have no preexisting cancellation transition")
	require.Equal(t, json.RawMessage(`0`), child["dispatch_attempt_seq"], "the fresh child must have no prior or fabricated original send attempt")
	require.NotEqual(t, json.RawMessage(`null`), child["dispatch_due_at"], "the exact new original child must genuinely be unresolved before pending cancellation")
	child["dispatch_state"], child["dispatch_resolution"], child["dispatch_due_at"], child["dispatch_writer_seq"] = fullAssessmentDispatchJSON(t, "cancelled"), fullAssessmentDispatchJSON(t, "owner_cancelled"), json.RawMessage(`null`), json.RawMessage(`1`)
	var updated time.Time
	require.NoError(t, json.Unmarshal(actualChild["dispatch_updated_at"], &updated), "read the actual later cancellation statement's clock rather than inventing a current time")
	require.True(t, !updated.Before(stamp) && !updated.After(afterClock), "the separate cancellation must use a real later statement clock inside the retained unit and before independent postcommit observation")
	child["dispatch_updated_at"] = actualChild["dispatch_updated_at"]
	require.Equal(t, expected, actual, "one known retirement commit may change only its original job/metadata/assessment plus NEW original pending child; complete original wake, same-session sibling canary, immutable input, attempted history, messages, evidence and every unrelated field remain exact")
}

func TestCodeReviewTerminalInstallerOrdersPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		installerFirst bool
	}{
		{name: "new installer child commits while actual retirement waits", installerFirst: true},
		{name: "actual retirement commit makes the waiting installer refuse"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			pool := fullRecoveryPostgresPoolWithMaxConns(t, ctx, 3)
			require.Equal(t, int32(3), pool.Config().MaxConns, "holder, actual contender and independent observer must fit exactly three real checkouts without hidden pool headroom")
			f, in := terminalInstallerOrderFixture(t, ctx, pool)
			observer, err := pool.Acquire(ctx)
			require.NoError(t, err, "reserve the independent observer before either unchanged short owner budget starts")
			t.Cleanup(observer.Release)
			var observerPID int32
			require.NoError(t, observer.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&observerPID), "warm the actual independent catalog backend before starting either owner")
			baseline, err := terminalContentionSnapshot(ctx, observer, f)
			require.NoError(t, err, "retain the complete real original/sibling/evidence graph before either transaction changes it")
			terminalInstallerOrderAbsent(t, ctx, observer, f, in)
			terminalEntered, installerEntered := make(chan terminalInstallerOrderEntry, 1), make(chan terminalInstallerOrderEntry, 1)
			terminalHeld, installerHeld := make(chan terminalInstallerOrderHeld, 1), make(chan terminalInstallerOrderHeld, 1)
			releaseTerminal, releaseInstaller := make(chan struct{}), make(chan struct{})
			terminalObserver := &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}
			installerObserver := &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}
			terminalRun := &terminalInstallerOrderTerminalRun{base: terminalObserver, fixture: f, entered: terminalEntered}
			if !tt.installerFirst {
				terminalRun.held, terminalRun.release = terminalHeld, releaseTerminal
			}
			jobs, err := db.NewJobStoreWithCodeReviewTerminalTransactions(pool, terminalRun)
			require.NoError(t, err, "explicitly compose the real retirement owner with the same actual runner plus transparent test-only coordination")
			failure := db.CodeReviewTerminalFailure{JobError: "Synthetic installer-order controller failure", Reason: "Synthetic original installer-order failure", Code: models.CodeReviewStatusCodeWorkerFailed, Message: "Synthetic ordinary installer-order failure copy", Retryable: true}
			type terminalResult struct {
				outcome db.CodeReviewTerminalOutcome
				err     error
			}
			type installerResult struct {
				prepared db.PreparedCodeReviewDispatch
				err      error
			}
			terminalDone, installerDone := make(chan terminalResult, 1), make(chan installerResult, 1)
			terminalFinished, installerFinished := make(chan struct{}), make(chan struct{})
			startTerminal := func() {
				go func() {
					defer close(terminalFinished)
					outcome, err := jobs.DeadLetterCodeReviewWithLease(ctx, f.job.OrgID, f.jobID, f.token, failure)
					terminalDone <- terminalResult{outcome: outcome, err: err}
				}()
				t.Cleanup(func() { cancel(); <-terminalFinished })
			}
			startInstaller := func() {
				go func() {
					defer close(installerFinished)
					var held chan<- terminalInstallerOrderHeld
					var release <-chan struct{}
					if tt.installerFirst {
						held, release = installerHeld, releaseInstaller
					}
					prepared, err := terminalInstallerOrderInstall(ctx, installerObserver, f, in, installerEntered, held, release)
					installerDone <- installerResult{prepared: prepared, err: err}
				}()
				t.Cleanup(func() { cancel(); <-installerFinished })
			}
			var comparison json.RawMessage
			var stagedTerminal json.RawMessage
			if tt.installerFirst {
				startInstaller()
				var installer terminalInstallerOrderHeld
				select {
				case installer = <-installerHeld:
				case actual := <-installerDone:
					require.FailNow(t, "the actual installer must stage a NEW pending child and truthfully dedupe its original wake before retirement starts", "installer outcome: %v", actual.err)
				}
				require.False(t, installer.prepared.WakeInserted(), "the actual current producer's running original key must truthfully dedupe rather than create a replacement wake")
				require.Equal(t, uuid.Nil, installer.prepared.WakeJobID(), "ordinary Prepare dedupe must expose no newly inserted or adopted original wake identity")
				require.Equal(t, in.ResultID, installer.prepared.Result().ID, "the actual NEW child must use the independently preminted exact original result")
				terminalInstallerOrderAssertNewChild(t, baseline, installer.graph, in)
				staged := terminalOwnerPointerPositiveDecode(t, installer.graph)
				child := terminalOwnerPointerPositiveRow(t, staged, "results", in.ResultID)
				require.Equal(t, json.RawMessage(`"pending"`), child["dispatch_state"], "the newly staged result must really be pending before the terminalizer's original metadata statement snapshot")
				wake := terminalOwnerPointerPositiveRow(t, staged, "jobs", f.jobID)
				originalWake := terminalOwnerPointerPositiveRow(t, terminalOwnerPointerPositiveDecode(t, baseline), "jobs", f.jobID)
				require.Equal(t, originalWake, wake, "actual Prepare dedupe must preserve the complete original running wake, payload, lease and non-NULL key before retirement")
				require.Equal(t, fullAssessmentDispatchJSON(t, "code_review:"+in.ReviewOutputKey), wake["dedupe_key"], "the retained original wake must use the exact current producer's non-NULL controller key")
				require.JSONEq(t, string(in.Input), string(child["dispatch_input"]), "the NEW child must retain its entire independently frozen original command before any terminalizer statement")
				require.JSONEq(t, string(in.Wake), string(child["dispatch_wake"]), "the NEW child must retain the entire frozen original wake envelope even when its enqueue truthfully dedupes")
				terminalInstallerOrderAbsent(t, installer.ctx, observer, f, in)
				startTerminal()
				var terminal terminalInstallerOrderEntry
				select {
				case terminal = <-terminalEntered:
				case actual := <-terminalDone:
					require.FailNow(t, "actual retirement must enter its original supplied Run before observation", "retirement outcome: %v", actual.err)
				}
				require.NotEqual(t, observerPID, terminal.pid, "retirement cannot borrow the independently reserved observer backend")
				require.NotEqual(t, observerPID, installer.pid, "the installer cannot borrow the independently reserved observer backend")
				terminalInstallerOrderWait(t, observer, installer.terminalInstallerOrderEntry, terminal, "ORDER BY id FOR UPDATE", terminalFinished)
				outside, err := terminalContentionSnapshot(installer.ctx, observer, f)
				require.NoError(t, err, "read the actual outside graph after the exact metadata wait has been proven")
				require.Equal(t, baseline, outside, "the actual NEW child and blocked retirement must remain wholly uncommitted while the original deduped wake stays exact before installer release")
				comparison = installer.graph
				close(releaseInstaller)
			} else {
				startTerminal()
				var terminal terminalInstallerOrderHeld
				select {
				case terminal = <-terminalHeld:
				case actual := <-terminalDone:
					require.FailNow(t, "actual retirement must stage its complete terminal graph before original runner commit", "retirement outcome: %v", actual.err)
				}
				stagedTerminal = terminal.graph
				startInstaller()
				var installer terminalInstallerOrderEntry
				select {
				case installer = <-installerEntered:
				case actual := <-installerDone:
					require.FailNow(t, "the actual installer must enter its real supplied Run before observation", "installer outcome: %v", actual.err)
				}
				require.NotEqual(t, observerPID, terminal.pid, "retirement cannot use the independent observer's reserved backend")
				require.NotEqual(t, observerPID, installer.pid, "the actual waiting installer cannot use the independent observer's backend")
				terminalInstallerOrderWait(t, observer, terminal.terminalInstallerOrderEntry, installer, "FOR SHARE OF m", installerFinished)
				outside, err := terminalContentionSnapshot(terminal.ctx, observer, f)
				require.NoError(t, err, "read the actual outside graph after the exact installer metadata wait is proven")
				require.Equal(t, baseline, outside, "neither the held terminal graph nor the positively blocked new installer may leak before actual retirement commit")
				comparison = baseline
				close(releaseTerminal)
			}
			terminalActual := <-terminalDone
			installerActual := <-installerDone
			require.NoError(t, terminalActual.err, "the original real runner must commit the complete exact retirement graph after positive holder-specific coordination")
			fullAssessmentDispatchAssertRun(t, terminalObserver)
			fullAssessmentDispatchAssertRun(t, installerObserver)
			require.True(t, terminalActual.outcome.Matches(f.job.OrgID, f.jobID, f.token), "only the original real Run's known commit may release the sealed exact job/tenant/token retirement outcome")
			require.Equal(t, tt.installerFirst, terminalActual.outcome.UnresolvedDispatch(), "only the committed original NEW child may create unresolved status; the live same-session sibling must never confer original-owner uncertainty")
			if tt.installerFirst {
				require.NoError(t, installerActual.err, "the original actual installer must commit its NEW pending child with its retained original command/wake before retirement's later child statement")
				require.False(t, installerActual.prepared.WakeInserted(), "known installer commit must not retroactively call ordinary original-wake dedupe a new insertion")
				require.Equal(t, uuid.Nil, installerActual.prepared.WakeJobID(), "the committed dedupe carrier must not adopt the existing original controller identity")
			} else {
				require.ErrorIs(t, installerActual.err, db.ErrCodeReviewDispatchPreparationIneligible, "the actual waiting Prepare must recheck terminal metadata and refuse after retirement commit, not insert from its old statement snapshot")
				require.Equal(t, db.PreparedCodeReviewDispatch{}, installerActual.prepared, "a post-wait ineligible installer cannot expose a new child, original command or wake")
				terminalInstallerOrderAbsent(t, ctx, observer, f, in)
			}
			after, err := terminalContentionSnapshot(ctx, observer, f)
			require.NoError(t, err, "independently read all complete committed original and sibling graph rows after both actual owners have joined")
			var afterClock time.Time
			require.NoError(t, observer.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&afterClock), "bound the actual separate cancellation statement clock by an independent postcommit database observation")
			terminalInstallerOrderAssertGraph(t, comparison, after, f, in, failure, tt.installerFirst, terminalObserver.clock, afterClock)
			if !tt.installerFirst {
				require.Equal(t, stagedTerminal, after, "after known retirement commit the refused installer must leave the entire exact staged terminal graph byte-identical")
			}
			metadata, metadataChanged := terminalActual.outcome.Metadata()
			require.True(t, metadataChanged, "successful actual retirement must return only its real changed original metadata descriptor")
			storedMetadata, err := f.stores.CodeReviews.GetByOutputKey(ctx, f.job.OrgID, f.job.OutputKey)
			require.NoError(t, err, "read the independently retained exact original metadata, not a latest same-session sibling")
			require.Equal(t, storedMetadata, metadata, "the sealed original descriptor must equal actual committed ordinary or unresolved metadata in its entirety")
		})
	}
}
