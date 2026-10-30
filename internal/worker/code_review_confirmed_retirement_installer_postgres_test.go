package worker

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// These are actual storage/transaction schedules, not provider calls, review
// completion, Worker.poll, effect delivery or production retirement chronology.
// Three checkouts serve the two bounded existing owners and warmed observer.
type confirmedInstallerFixture struct {
	original, foreign strandedReviewFixture
	input             db.CodeReviewDispatchPreparation
	pending           uuid.UUID
	children          []terminalDispatchOwnershipChild
	foreignChildren   []terminalDispatchOwnershipChild
}

func confirmedInstallerSeed(t *testing.T, ctx context.Context, pool *pgxpool.Pool, uncertain bool) confirmedInstallerFixture {
	t.Helper()
	f, foreign, children, foreignChildren := terminalDispatchOwnershipFixture(t, ctx, pool, "pending")
	var thread uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM session_threads WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID).Scan(&thread), "reuse the genuinely completed original lane for old histories and the preminted new installer")
	unknown := fullAssessmentDispatchChild(t, ctx, pool, f, thread, "unknown")
	children = append(children, terminalDispatchOwnershipChild{id: unknown, metadata: f.job.MetadataID, state: "unknown", attempts: 1, due: true})
	var pending uuid.UUID
	for _, child := range children {
		if child.metadata == f.job.MetadataID && child.state == "pending" {
			pending = child.id
		}
	}
	require.NotEqual(t, uuid.Nil, pending, "positive retirement must have a real original pending cancellation canary, not only sibling/foreign work")
	proof := confirmedRetirementProofCase{metadataID: "matching", helper: true, pending: true}
	if uncertain {
		proof.publication = "uncertain"
	}
	confirmedRetirementProofStage(t, ctx, pool, f, foreign, proof)
	var payload json.RawMessage
	require.NoError(t, pool.QueryRow(ctx, `SELECT payload FROM jobs WHERE org_id=$1 AND id=$2 AND status='running' AND lock_token=$3 AND dedupe_key=$4`, f.job.OrgID, f.jobID, f.token, "code_review:"+f.job.OutputKey).Scan(&payload), "freeze the entire actual original full-controller payload before either owner starts")
	resultID := uuid.New()
	key := "cr-dispatch:v1:" + resultID.String()
	in := db.CodeReviewDispatchPreparation{ResultID: resultID, SessionID: f.job.SessionID, MetadataID: f.job.MetadataID, ThreadID: thread, AgentProvider: "claude_code", Version: 1, Key: key, Digest: strings.Repeat("a", 64), ReviewOutputKey: f.job.OutputKey,
		RawOutput: new("Synthetic genuinely NEW original installer evidence"), StructuredResult: json.RawMessage(`{"synthetic":"confirmed-installer"}`)}
	in.Input = fullAssessmentDispatchJSON(t, map[string]any{"version": 1, "purpose": db.CallerSendStampCodeReviewDispatch,
		"org_id": f.job.OrgID, "session_id": f.job.SessionID, "thread_id": thread, "client_message_id": key, "message": "Synthetic genuinely NEW original command"})
	in.Wake = fullAssessmentDispatchJSON(t, map[string]any{"job_type": "run_code_review", "queue": "agent", "dedupe_key": "code_review:" + f.job.OutputKey, "payload": payload})
	return confirmedInstallerFixture{original: f, foreign: foreign, input: in, pending: pending, children: children, foreignChildren: foreignChildren}
}

func confirmedInstallerAssertPending(t *testing.T, ctx context.Context, q db.DBTX, f strandedReviewFixture) {
	t.Helper()
	var state struct {
		metadata models.CodeReviewSessionStatus
		job      models.JobStatus
		activeA  *uuid.UUID
		activeS  *uuid.UUID
	}
	require.NoError(t, q.QueryRow(ctx, `SELECT m.status,j.status,p.active_assessment_id,p.active_session_id
	 FROM code_review_session_metadata m JOIN jobs j ON j.org_id=m.org_id AND j.id=$3
	 JOIN code_review_pr_state p ON p.org_id=m.org_id AND p.pull_request_id=m.pull_request_id
	 WHERE m.org_id=$1 AND m.id=$2`, f.job.OrgID, f.job.MetadataID, f.jobID).Scan(&state.metadata, &state.job, &state.activeA, &state.activeS), "independently read the complete original metadata/job/reservation disposition")
	require.Equal(t, models.CodeReviewSessionStatusRunning, state.metadata, "a positively confirmed retired controller must leave durable metadata active")
	require.Equal(t, models.JobStatusDeadLetter, state.job, "only the exact original queue attempt must retire")
	require.Equal(t, &f.assessment, state.activeA, "pending local completion must keep its original assessment reservation")
	require.Equal(t, &f.job.SessionID, state.activeS, "pending local completion must keep its original session reservation")
	assessment, err := db.NewCodeReviewAssessmentStore(q).GetByID(ctx, f.job.OrgID, f.assessment)
	require.NoError(t, err, "read actual retained original publication facts")
	metadata, err := db.NewCodeReviewStore(q).GetByOutputKey(ctx, f.job.OrgID, f.job.OutputKey)
	require.NoError(t, err, "read exact metadata rather than its newer live same-session sibling")
	require.Equal(t, models.CodeReviewPublicationConfirmed, assessment.PublicationState, "a legacy metadata ID alone cannot stand in for positively confirmed publication")
	require.True(t, confirmedFullAssessmentReceiptMatches(assessment, metadata), "the actual retained same-owner receipt must remain usable after retirement")
}

func confirmedInstallerAssertConfirmationOnly(t *testing.T, before, after json.RawMessage, f strandedReviewFixture, receipt json.RawMessage) {
	t.Helper()
	expected, actual := terminalOwnerPointerPositiveDecode(t, before), terminalOwnerPointerPositiveDecode(t, after)
	assessment := terminalOwnerPointerPositiveRow(t, expected, "assessments", f.assessment)
	actualAssessment := terminalOwnerPointerPositiveRow(t, actual, "assessments", f.assessment)
	require.Equal(t, fullAssessmentDispatchJSON(t, "uncertain"), assessment["publication_state"], "the old insertion snapshot must genuinely precede confirmed publication")
	require.JSONEq(t, `null`, string(assessment["publication_receipt"]), "the old insertion snapshot must not contain a valid confirmed receipt")
	require.JSONEq(t, string(receipt), string(actualAssessment["publication_receipt"]), "real RecordPublication must stage every field of the exact valid original receipt")
	assessment["publication_state"], assessment["publication_receipt"] = fullAssessmentDispatchJSON(t, "confirmed"), actualAssessment["publication_receipt"]
	assessment["github_review_id"], assessment["github_review_url"], assessment["failure_detail"] = fullAssessmentDispatchJSON(t, int64(77)), fullAssessmentDispatchJSON(t, "https://example.invalid/review/77"), json.RawMessage(`null`)
	require.Equal(t, expected, actual, "the bounded fixture writer may change only actual RecordPublication fields of original A; metadata itself and every old pending/unknown/sibling/foreign/queue/evidence field must remain exact")
}

// The production prelock statement is intentionally checked after the actual
// refusal assertion. A test-only prelock-removal control can therefore follow
// the SAME live-blocker choreography through its older waiting insertion CTE
// and fail for an actual forbidden insertion, not merely a changed query name.
func confirmedInstallerWait(t *testing.T, observer *pgxpool.Conn, holder, installer terminalInstallerOrderEntry, finished <-chan struct{}) string {
	t.Helper()
	require.NotEqual(t, holder.pid, installer.pid, "actual writer/retirement and installer owners must use distinct real backends")
	for {
		select {
		case <-finished:
			t.Fatal("the real installer ended before its genuine metadata SHARE wait was observed")
		case <-holder.ctx.Done():
			t.Fatal("the original held owner budget expired before actual metadata-wait evidence")
		case <-installer.ctx.Done():
			t.Fatal("the original installer budget expired before actual metadata-wait evidence")
		default:
		}
		_, err := observer.Exec(holder.ctx, `SELECT pg_stat_clear_snapshot()`)
		require.NoError(t, err, "discard only the observer's catalog cache before bounded live metadata-wait readback")
		var waiting bool
		var query string
		err = observer.QueryRow(holder.ctx, `SELECT COALESCE(a.state='active' AND a.wait_event_type='Lock'
	 AND a.xact_start=$2::timestamptz AND pg_blocking_pids(a.pid)=ARRAY[$3::integer]
	 AND position('code_review_session_metadata' IN a.query)>0 AND position('FOR SHARE OF m' IN a.query)>0,false),a.query
	 FROM pg_stat_activity a WHERE a.datname=current_database() AND a.pid=$1::integer`, installer.pid, installer.clock, holder.pid).Scan(&waiting, &query)
		require.NoError(t, err, "observe actual installer PID, original transaction start, query and sole retained metadata holder")
		if waiting {
			return query
		}
	}
}

func confirmedInstallerAssertPrelock(t *testing.T, query string) {
	t.Helper()
	require.Equal(t, `SELECT m.id FROM code_review_session_metadata m WHERE m.org_id=$1 AND m.session_id=$2 AND m.id=$3 AND m.status IN ('queued','running') AND NOT m.stale AND m.review_output_key=$4 FOR SHARE OF m`, strings.Join(strings.Fields(query), " "), "corrected Prepare must positively wait in its separate exact eligible metadata SHARE prelock before taking the later insertion snapshot")
}

type confirmedInstallerResult struct {
	prepared db.PreparedCodeReviewDispatch
	err      error
}

type confirmedInstallerTerminalResult struct {
	outcome db.CodeReviewTerminalOutcome
	err     error
}

type confirmedInstallerPublicationWriter struct {
	base        db.CodeReviewMetadataTransactions
	fixture     strandedReviewFixture
	entered     chan<- terminalInstallerOrderEntry
	locked      chan<- terminalInstallerOrderHeld
	confirm     <-chan struct{}
	confirmed   chan<- terminalInstallerOrderHeld
	release     <-chan struct{}
	receipt     json.RawMessage
	inputDigest string
	generation  int64
}

// This bounded FIXTURE writer is not production publication composition. It
// holds exact metadata unchanged, then confirms A through the real tx-backed
// RecordPublication only after a different real installer is observed waiting.
// An A-only commit tests the snapshot hazard that already-visible confirmation
// plus terminalizer-first retirement alone cannot demonstrate.
func (w *confirmedInstallerPublicationWriter) Run(ctx context.Context) error {
	return w.base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		entry, err := terminalInstallerOrderEntryInTx(unitCtx, tx)
		if err != nil {
			return err
		}
		select {
		case w.entered <- entry:
		case <-unitCtx.Done():
			return unitCtx.Err()
		}
		var metadataID uuid.UUID
		if err := tx.QueryRow(unitCtx, `SELECT id FROM code_review_session_metadata WHERE org_id=$1 AND id=$2 FOR UPDATE`, w.fixture.job.OrgID, w.fixture.job.MetadataID).Scan(&metadataID); err != nil {
			return err
		}
		if metadataID != w.fixture.job.MetadataID {
			return db.ErrCodeReviewTerminalScope
		}
		graph, err := terminalContentionSnapshot(unitCtx, tx, w.fixture)
		if err != nil {
			return err
		}
		select {
		case w.locked <- terminalInstallerOrderHeld{terminalInstallerOrderEntry: entry, graph: graph}:
		case <-unitCtx.Done():
			return unitCtx.Err()
		}
		select {
		case <-w.confirm:
		case <-unitCtx.Done():
			return unitCtx.Err()
		}
		if err := db.NewCodeReviewAssessmentStore(tx).RecordPublication(unitCtx, w.fixture.job.OrgID, w.fixture.assessment, w.generation, w.inputDigest,
			models.CodeReviewPublicationConfirmed, w.receipt, new(int64(77)), new("https://example.invalid/review/77")); err != nil {
			return err
		}
		graph, err = terminalContentionSnapshot(unitCtx, tx, w.fixture)
		if err != nil {
			return err
		}
		select {
		case w.confirmed <- terminalInstallerOrderHeld{terminalInstallerOrderEntry: entry, graph: graph}:
		case <-unitCtx.Done():
			return unitCtx.Err()
		}
		select {
		case <-w.release:
			return nil
		case <-unitCtx.Done():
			return unitCtx.Err()
		}
	})
}

func TestConfirmedPublicationInstallerRefusalPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		concurrent bool
		lateProof  bool
	}{
		{name: "known confirmed retirement refuses a genuinely new original result and wake"},
		{name: "actual terminalizer-first commit keeps metadata active but the waiting installer still refuses", concurrent: true},
		{name: "later fresh insertion snapshot sees an A-only confirmation committed after the metadata SHARE wait began", concurrent: true, lateProof: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // Separate full-schema database per leaf; root uses -parallel=1 to bound aggregate PostgreSQL capacity.
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			pool := fullRecoveryPostgresPoolWithMaxConns(t, ctx, 3)
			require.Equal(t, int32(3), pool.Config().MaxConns, "two actual bounded owners plus a warmed observer must fit literally three checkouts")
			fixture := confirmedInstallerSeed(t, ctx, pool, tt.lateProof)
			f, in := fixture.original, fixture.input
			observer, err := pool.Acquire(ctx)
			require.NoError(t, err, "reserve the actual independent observer before either original short unit budget starts")
			t.Cleanup(observer.Release)
			var observerPID int32
			require.NoError(t, observer.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&observerPID), "warm and identify the real independent PostgreSQL observer")
			baseline, err := terminalContentionSnapshot(ctx, observer, f)
			require.NoError(t, err, "retain every original, live sibling, old pending/unknown intent, evidence and queue field before the new installer")
			foreignBaseline, err := terminalContentionSnapshot(ctx, observer, fixture.foreign)
			require.NoError(t, err, "retain the complete real foreign tenant and its live original pending wake")
			terminalDispatchOwnershipAssertChildren(t, terminalOwnerPointerPositiveDecode(t, baseline), fixture.children)
			terminalDispatchOwnershipAssertChildren(t, terminalOwnerPointerPositiveDecode(t, foreignBaseline), fixture.foreignChildren)
			terminalInstallerOrderAbsent(t, ctx, observer, f, in)
			terminalObserver := &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}
			installerObserver := &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}
			failure := db.CodeReviewTerminalFailure{JobError: "Synthetic exhausted local completion", Reason: "Synthetic exhausted local completion", Code: models.CodeReviewStatusCodeWorkerFailed, Message: "Synthetic ordinary worker failure", Retryable: true}
			entered, held := make(chan terminalInstallerOrderEntry, 1), make(chan terminalInstallerOrderHeld, 1)
			confirmedHeld := make(chan terminalInstallerOrderHeld, 1)
			installerEntered := make(chan terminalInstallerOrderEntry, 1)
			release, confirm := make(chan struct{}), make(chan struct{})
			var releaseOnce, confirmOnce sync.Once
			terminalDone := make(chan confirmedInstallerTerminalResult, 1)
			installerDone := make(chan confirmedInstallerResult, 1)
			terminalFinished, installerFinished := make(chan struct{}), make(chan struct{})
			terminalStarted, installerStarted := false, false
			t.Cleanup(func() {
				cancel()
				confirmOnce.Do(func() { close(confirm) })
				releaseOnce.Do(func() { close(release) })
				if terminalStarted {
					<-terminalFinished
				}
				if installerStarted {
					<-installerFinished
				}
			})
			var expectedFinal, exactReceipt json.RawMessage
			var terminalActual confirmedInstallerTerminalResult
			var observedQuery string
			if !tt.concurrent {
				jobs, setupErr := db.NewJobStoreWithCodeReviewTerminalTransactions(pool, terminalObserver)
				require.NoError(t, setupErr, "compose the actual aware retirement owner without a custom semantic store")
				terminalActual.outcome, terminalActual.err = jobs.DeadLetterCodeReviewWithLease(ctx, f.job.OrgID, f.jobID, f.token, failure)
				require.NoError(t, terminalActual.err, "commit the actual positively confirmed exact-lease retirement before invoking any new installer")
				fullAssessmentDispatchAssertRun(t, terminalObserver)
				expectedFinal, err = terminalContentionSnapshot(ctx, observer, f)
				require.NoError(t, err, "retain the complete known-committed confirmed-pending graph before fresh installation")
				confirmedInstallerAssertPending(t, ctx, observer, f)
			} else if !tt.lateProof {
				runner := &terminalInstallerOrderTerminalRun{base: terminalObserver, fixture: f, entered: entered, held: held, release: release}
				jobs, setupErr := db.NewJobStoreWithCodeReviewTerminalTransactions(pool, runner)
				require.NoError(t, setupErr, "compose actual retirement with only transparent precommit observation")
				terminalStarted = true
				go func() {
					defer close(terminalFinished)
					outcome, runErr := jobs.DeadLetterCodeReviewWithLease(ctx, f.job.OrgID, f.jobID, f.token, failure)
					terminalDone <- confirmedInstallerTerminalResult{outcome: outcome, err: runErr}
				}()
			} else {
				assessment, loadErr := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
				require.NoError(t, loadErr, "read actual preexisting staged uncertain A before starting the bounded fixture writer")
				require.Equal(t, models.CodeReviewPublicationUncertain, assessment.PublicationState, "the snapshot hazard must begin without a visible confirmed assessment")
				require.Nil(t, assessment.PublicationReceipt, "legacy metadata identity cannot preseed the positive receipt in the older installer snapshot")
				receipt, receiptErr := fullAssessmentPublicationReceipt(f.assessment, f.manifest.InputDigest, f.job.HeadSHA, new(int64(77)), new("https://example.invalid/review/77"), nil, nil)
				require.NoError(t, receiptErr, "freeze the whole exact valid original receipt before actual RecordPublication")
				exactReceipt = receipt
				writer := &confirmedInstallerPublicationWriter{base: terminalObserver, fixture: f, entered: entered, locked: held, confirm: confirm, confirmed: confirmedHeld, release: release, receipt: receipt, generation: assessment.Generation, inputDigest: assessment.InputDigest}
				terminalStarted = true
				go func() {
					defer close(terminalFinished)
					terminalDone <- confirmedInstallerTerminalResult{err: writer.Run(ctx)}
				}()
			}
			var holder terminalInstallerOrderHeld
			if tt.concurrent {
				select {
				case holder = <-held:
				case actual := <-terminalDone:
					require.FailNow(t, "actual owner must hold exact metadata before the new installer starts", "owner outcome: %v", actual.err)
				}
				require.NotEqual(t, observerPID, holder.pid, "the actual metadata holder cannot borrow the reserved observer checkout")
				if !tt.lateProof {
					expectedFinal = holder.graph
					staged := terminalOwnerPointerPositiveDecode(t, holder.graph)
					require.Equal(t, fullAssessmentDispatchJSON(t, "dead_letter"), terminalOwnerPointerPositiveRow(t, staged, "jobs", f.jobID)["status"], "the real retirement callback must already have staged exact queue retirement before installer entry")
					require.Equal(t, fullAssessmentDispatchJSON(t, "running"), terminalOwnerPointerPositiveRow(t, staged, "metadata", f.job.MetadataID)["status"], "the held real retirement graph must preserve active original metadata rather than rely on a terminal-status producer refusal")
				} else {
					require.Equal(t, baseline, holder.graph, "the deliberate fixture writer must initially lock metadata without changing any row or inventing a publication fact")
				}
			}
			installerStarted = true
			go func() {
				defer close(installerFinished)
				prepared, runErr := terminalInstallerOrderInstall(ctx, installerObserver, f, in, installerEntered, nil, nil)
				installerDone <- confirmedInstallerResult{prepared: prepared, err: runErr}
			}()
			if tt.concurrent {
				var installer terminalInstallerOrderEntry
				select {
				case installer = <-installerEntered:
				case actual := <-installerDone:
					require.FailNow(t, "real installer must enter the actual supplied Run before its wait is observed", "installer outcome: %v", actual.err)
				}
				require.NotEqual(t, observerPID, installer.pid, "the actual installer cannot borrow the reserved observer checkout")
				observedQuery = confirmedInstallerWait(t, observer, holder.terminalInstallerOrderEntry, installer, installerFinished)
				outside, readErr := terminalContentionSnapshot(holder.ctx, observer, f)
				require.NoError(t, readErr, "inspect every independently visible row after exact metadata-wait proof")
				require.Equal(t, baseline, outside, "neither the held original mutation nor the positively blocked NEW installer may leak before commit")
				if tt.lateProof {
					confirmOnce.Do(func() { close(confirm) })
					// Read staged confirmation from its own transaction, never
					// manufacture a graph from an outside old snapshot.
					var stagedConfirmation terminalInstallerOrderHeld
					select {
					case stagedConfirmation = <-confirmedHeld:
					case actual := <-terminalDone:
						require.FailNow(t, "the bounded fixture writer must stage actual RecordPublication before commit", "writer outcome: %v", actual.err)
					}
					require.Equal(t, holder.pid, stagedConfirmation.pid, "real confirmation must occur on the same actual bounded holder backend")
					require.Equal(t, holder.clock, stagedConfirmation.clock, "real confirmation must remain in the same original holder transaction rather than a new snapshot owner")
					expectedFinal = stagedConfirmation.graph
					confirmedInstallerAssertConfirmationOnly(t, baseline, expectedFinal, f, exactReceipt)
					outside, readErr = terminalContentionSnapshot(holder.ctx, observer, f)
					require.NoError(t, readErr, "read actual outside visibility after A-only confirmation is staged but before its commit")
					require.Equal(t, baseline, outside, "staged real receipt confirmation must remain invisible while the metadata prelock still waits")
				}
				releaseOnce.Do(func() { close(release) })
				terminalActual = <-terminalDone
				require.NoError(t, terminalActual.err, "the actual held owner must commit through its unchanged bounded runner")
				<-terminalFinished
				fullAssessmentDispatchAssertRun(t, terminalObserver)
			}
			installerActual := <-installerDone
			<-installerFinished
			require.ErrorIs(t, installerActual.err, db.ErrCodeReviewDispatchPreparationIneligible, "actual new installation must refuse the exact positively confirmed original publication, including after an older metadata wait")
			require.Equal(t, db.PreparedCodeReviewDispatch{}, installerActual.prepared, "refused fresh installation must expose no child, command or newly inserted/adopted wake")
			fullAssessmentDispatchAssertRun(t, installerObserver)
			if tt.concurrent {
				confirmedInstallerAssertPrelock(t, observedQuery)
			}
			terminalInstallerOrderAbsent(t, ctx, observer, f, in)
			after, readErr := terminalContentionSnapshot(ctx, observer, f)
			require.NoError(t, readErr, "inspect the complete final graph after both actual owners joined")
			if tt.lateProof {
				require.Equal(t, expectedFinal, after, "the refused post-wait installer must preserve every byte of the real A-only confirmed commit and never add a new child or wake from its older statement snapshot")
				confirmedInstallerAssertConfirmationOnly(t, baseline, after, f, exactReceipt)
				assessment, loadErr := db.NewCodeReviewAssessmentStore(observer).GetByID(ctx, f.job.OrgID, f.assessment)
				require.NoError(t, loadErr, "read actually committed exact A after the bounded fixture writer and installer both join")
				metadata, loadErr := db.NewCodeReviewStore(observer).GetByOutputKey(ctx, f.job.OrgID, f.job.OutputKey)
				require.NoError(t, loadErr, "read exact original metadata after the A-only fixture confirmation")
				require.Equal(t, models.CodeReviewPublicationConfirmed, assessment.PublicationState, "the later actual insertion snapshot must observe real confirmed state, not a legacy review ID")
				require.True(t, confirmedFullAssessmentReceiptMatches(assessment, metadata), "the committed original receipt must satisfy the existing real Go validator")
			} else {
				require.True(t, terminalActual.outcome.Matches(f.job.OrgID, f.jobID, f.token), "only real known-commit retirement may release the original job/tenant/lease disposition")
				require.True(t, terminalActual.outcome.CompletionRecoveryPending(), "actual known-commit retirement must positively retain the exact confirmed local-completion disposition")
				confirmedInstallerAssertPending(t, ctx, observer, f)
				require.Equal(t, expectedFinal, after, "after actual confirmed retirement, a refused new installer must preserve every complete original/sibling graph field")
				var afterClock time.Time
				require.NoError(t, observer.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&afterClock), "bound actual original pending settlement by independent postcommit database time")
				confirmedRetirementProofAssertGraph(t, baseline, after, f, confirmedRetirementProofCase{pending: true}, failure, fixture.pending, terminalObserver.clock, afterClock)
			}
			foreignAfter, foreignErr := terminalContentionSnapshot(ctx, observer, fixture.foreign)
			require.NoError(t, foreignErr, "read every actual foreign tenant row after both original owners finish")
			require.Equal(t, foreignBaseline, foreignAfter, "no original installer/refusal/publication/retirement may mutate or borrow a genuine foreign pending/input/wake/attempt or owner")
		})
	}
}
