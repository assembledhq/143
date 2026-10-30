package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type lifecyclePredicateFixture struct {
	org, session, lane, sibling uuid.UUID
	fence                       AdmissionFence
	own                         *lifecycleOwnConsumer
}

func TestLifecycleConsumerPredicatePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		setup    func(*testing.T, *pgxpool.Pool, *lifecyclePredicateFixture)
		limits   func(*lifecycleConsumerLimits)
		expected lifecycleConsumerTestResult
	}{
		{name: "empty scope"},
		{name: "same lane legacy input with different dedupe", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedPredicateLegacyJob(t, p, *f, fmt.Sprintf(`{"session_id":%q,"thread_id":%q}`, f.session, f.lane))
		}, expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "healthy sibling legacy consumer", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedPredicateLegacyJob(t, p, *f, fmt.Sprintf(`{"session_id":%q,"thread_id":%q}`, f.session, f.sibling))
		}},
		{name: "threadless legacy consumer spans lanes", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedPredicateLegacyJob(t, p, *f, fmt.Sprintf(`{"session_id":%q}`, f.session))
		}, expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "session-wide request sees sibling", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			f.fence = AdmissionFence{sessionID: f.session}
			seedPredicateLegacyJob(t, p, *f, fmt.Sprintf(`{"session_id":%q,"thread_id":%q}`, f.session, f.sibling))
		}, expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "UUID raw hex and braced route", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedPredicateLegacyJob(t, p, *f, fmt.Sprintf(`{"session_id":%q,"thread_id":%q}`, strings.ReplaceAll(f.session.String(), "-", ""), "{"+f.lane.String()+"}"))
		}, expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "URN route", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedPredicateLegacyJob(t, p, *f, fmt.Sprintf(`{"session_id":%q,"thread_id":%q}`, f.session.URN(), f.lane.URN()))
		}, expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "Unicode folded route is scoped uncertainty", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedPredicateLegacyJob(t, p, *f, fmt.Sprintf(`{"ſeſſion_id":%q}`, f.session))
		}, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "malformed thread cannot erase session", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedPredicateLegacyJob(t, p, *f, fmt.Sprintf(`{"session_id":%q,"thread_id":7}`, f.session))
		}, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "malformed unrelated required route does not block tenant", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedPredicateLegacyJob(t, p, *f, `{"session_id":"broken","thread_id":false}`)
		}},
		{name: "scalar unrelated payload cannot dispatch", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedPredicateLegacyJob(t, p, *f, `42`)
		}},
		{name: "foreign job tenant is not queried", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			otherOrg, _, _ := seedRuntimeSeedThread(t, p)
			other := *f
			other.org = otherOrg
			seedPredicateLegacyJob(t, p, other, fmt.Sprintf(`{"session_id":%q}`, f.session))
		}},
		{name: "payload org mismatch within scoped job", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedPredicateLegacyJob(t, p, *f, fmt.Sprintf(`{"org_id":%q,"session_id":%q}`, uuid.New(), f.session))
		}, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "payload session B still anchors thread A", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			other := seedLifecycleConsumerSession(t, p, f.org)
			seedPredicateLegacyJob(t, p, *f, fmt.Sprintf(`{"session_id":%q,"thread_id":%q}`, other.ID, f.lane))
		}, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "runtime stored session B still anchors thread A", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			other := seedLifecycleConsumerSession(t, p, f.org)
			createSeedRuntime(t, p, f.org, other.ID, f.lane)
		}, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "archived actual lane retains contradictory runtime", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			other := seedLifecycleConsumerSession(t, p, f.org)
			createSeedRuntime(t, p, f.org, other.ID, f.lane)
			predicateExec(t, p, "UPDATE session_threads SET archived_at=now() WHERE org_id=$1 AND id=$2", f.org, f.lane)
		}, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "hidden actual parent retains contradictory runtime", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			other := seedLifecycleConsumerSession(t, p, f.org)
			createSeedRuntime(t, p, f.org, other.ID, f.lane)
			predicateExec(t, p, "UPDATE sessions SET archived_at=now(),deleted_at=now() WHERE org_id=$1 AND id=$2", f.org, f.session)
		}, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "live runtime lease expired", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			r := createSeedRuntime(t, p, f.org, f.session, f.lane)
			predicateExec(t, p, `UPDATE thread_runtimes SET lease_expires_at=now()-interval '1 day' WHERE org_id=$1 AND id=$2`, f.org, r.ID)
		}, expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "compatible live sibling runtime", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			createSeedRuntime(t, p, f.org, f.session, f.sibling)
		}},
		{name: "terminal runtime JSON is not proof", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			r := createSeedRuntime(t, p, f.org, f.session, f.lane)
			predicateExec(t, p, `UPDATE thread_runtimes SET status='closed',closed_at=now(),drain_proof='{"claimed":"drained"}' WHERE org_id=$1 AND id=$2`, f.org, r.ID)
		}, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "unthreaded executor spans lanes", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedPredicateExecutor(t, p, *f, nil, "running")
		}, expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "terminal executor lacks drain proof", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedPredicateExecutor(t, p, *f, &f.lane, "completed")
		}, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "legacy executor foreign job is a missing scoped reference", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			otherOrg, _, _ := seedRuntimeSeedThread(t, p)
			foreignJob := seedLifecycleConsumerJob(t, p, otherOrg, models.JobStatusPending)
			seedPredicateExecutor(t, p, *f, &f.lane, "running")
			predicateExec(t, p, `UPDATE session_executors SET job_id=$3 WHERE org_id=$1 AND session_id=$2`, f.org, f.session, foreignJob)
		}, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "pending inbox is waiting input", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedRuntimeInbox(t, p, f.org, f.session, f.lane, 1, "pending", nil)
		}},
		{name: "pending inbox retains prior delivery uncertainty", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedPredicatePreviouslyDeliveredInbox(t, p, *f, models.ThreadInboxDeliveryStatePending)
		}, expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "dead letter retains prior delivery uncertainty", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedPredicatePreviouslyDeliveredInbox(t, p, *f, models.ThreadInboxDeliveryStateDeadLetter)
		}, expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "applied pending delivery is no longer uncertain", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedPredicatePreviouslyDeliveredInbox(t, p, *f, models.ThreadInboxDeliveryStatePending)
			predicateExec(t, p, `UPDATE thread_inbox_entries SET applied_at=now() WHERE org_id=$1 AND thread_id=$2`, f.org, f.lane)
		}},
		{name: "applied dead letter delivery is no longer uncertain", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedPredicatePreviouslyDeliveredInbox(t, p, *f, models.ThreadInboxDeliveryStateDeadLetter)
			predicateExec(t, p, `UPDATE thread_inbox_entries SET applied_at=now() WHERE org_id=$1 AND thread_id=$2`, f.org, f.lane)
		}},
		{name: "unknown inbox survives absent runtime", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedRuntimeInbox(t, p, f.org, f.session, f.lane, 1, "unknown_delivery", nil)
		}, expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "acked inbox is not applied", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedRuntimeInbox(t, p, f.org, f.session, f.lane, 1, "acked", nil)
		}, expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "positive application clears inbox ownership", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedRuntimeInbox(t, p, f.org, f.session, f.lane, 1, "acked", nil)
			predicateExec(t, p, `UPDATE thread_inbox_entries SET applied_at=now() WHERE org_id=$1 AND thread_id=$2`, f.org, f.lane)
		}},
		{name: "open preparation past every deadline", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			job := seedLifecycleConsumerJob(t, p, f.org, models.JobStatusPending)
			seedLifecyclePreparation(t, p, f.org, f.session, &f.lane, job, "authorized", "none")
		}, expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "closed unknown preparation spans sibling", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			job := seedLifecycleConsumerJob(t, p, f.org, models.JobStatusFailed)
			seedLifecyclePreparation(t, p, f.org, f.session, &f.sibling, job, "closed", "unknown")
		}, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "settled-looking preparation requires B validator", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			job := seedLifecycleConsumerJob(t, p, f.org, models.JobStatusSucceeded)
			seedLifecyclePreparation(t, p, f.org, f.session, &f.sibling, job, "closed", "settled")
		}, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "closed preparation preserves malformed active job anchor", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			job := seedPredicateLegacyJob(t, p, *f, `{}`)
			seedLifecyclePreparation(t, p, f.org, f.session, &f.lane, job, "closed", "none")
		}, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "retained recovery ignores expired due time", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedLifecycleWorkspaceRecovery(t, p, f.org, f.session)
		}, expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "ordinary preview snapshot operator review pins coexist", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			for _, kind := range []string{"preview", "snapshot", "operator", "code_review"} {
				seedPredicateHolder(t, p, *f, kind, uuid.New(), uuid.New(), "synthetic-container", "synthetic-owner", "active")
			}
		}},
		{name: "expired runtime holder with unknown owner reconciles", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedPredicateHolder(t, p, *f, "thread_runtime", uuid.New(), uuid.New(), "synthetic-container", "synthetic-owner", "expired")
		}, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "runtime holder resolves compatible sibling", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			r := createSeedRuntime(t, p, f.org, f.session, f.sibling)
			predicateExec(t, p, `UPDATE thread_runtimes SET container_id='synthetic-container' WHERE org_id=$1 AND id=$2`, f.org, r.ID)
			seedPredicateHolder(t, p, *f, "thread_runtime", r.ID, r.LeaseToken, "synthetic-container", r.OwnerNodeID, "active")
		}},
		{name: "historical dispatch retains malformed active job anchor", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			job := seedPredicateLegacyJob(t, p, *f, `{}`)
			id, err := insertLifecycleConsumerDispatch(p, f.org, f.session, f.lane, job, nil, nil)
			require.NoError(t, err, "seed retained reverse input association")
			predicateExec(t, p, `UPDATE session_input_dispatches SET admission_state='historical' WHERE org_id=$1 AND id=$2`, f.org, id)
		}, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "parked control remains beyond deadlines", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			job := seedLifecycleConsumerJob(t, p, f.org, models.JobStatusAwaitingStartControl)
			id, err := insertLifecycleConsumerDispatch(p, f.org, f.session, f.lane, job, nil, nil)
			require.NoError(t, err, "bind parked control to exact tenant and input")
			predicateExec(t, p, `UPDATE session_input_dispatches SET control_receipt_id=gen_random_uuid(),control_version=1,control_state='awaiting_control',control_snapshot='{}',control_next_attempt_at=now()-interval '1 day',deadline_at=now()-interval '2 days' WHERE org_id=$1 AND id=$2`, f.org, id)
		}, expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "legacy job row cap refuses partial clear", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedPredicateLegacyJob(t, p, *f, `{}`)
			seedPredicateLegacyJob(t, p, *f, `{}`)
		}, limits: func(v *lifecycleConsumerLimits) { v.legacyJobs = 1 }, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerDiscoveryIncomplete}},
		{name: "legacy aggregate byte cap refuses partial clear", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedPredicateLegacyJob(t, p, *f, `{"domain":"some synthetic text"}`)
		}, limits: func(v *lifecycleConsumerLimits) { v.payloadBytes = 8 }, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerDiscoveryIncomplete}},
		{name: "single oversized legacy envelope refuses partial clear", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedPredicateLegacyJob(t, p, *f, fmt.Sprintf(`{"domain":%q}`, strings.Repeat("x", legacyJobRouteMaxBytes)))
		}, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerDiscoveryIncomplete}},
		{name: "lane-anchor budget refuses partial clear", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedPredicateLegacyJob(t, p, *f, fmt.Sprintf(`{"session_id":%q,"thread_id":%q}`, f.session, f.lane))
			seedPredicateLegacyJob(t, p, *f, fmt.Sprintf(`{"session_id":%q,"thread_id":%q}`, f.session, f.sibling))
		}, limits: func(v *lifecycleConsumerLimits) { v.laneAnchors = 1 }, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerDiscoveryIncomplete}},
		{name: "structured row cap refuses partial clear", setup: func(t *testing.T, p *pgxpool.Pool, f *lifecyclePredicateFixture) {
			seedRuntimeInbox(t, p, f.org, f.session, f.lane, 1, "acked", nil)
			seedRuntimeInbox(t, p, f.org, f.session, f.lane, 2, "acked", nil)
		}, limits: func(v *lifecycleConsumerLimits) { v.structuredRows = 1 }, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerDiscoveryIncomplete}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			f := newLifecyclePredicateFixture(t, pool)
			if tt.setup != nil {
				tt.setup(t, pool, &f)
			}
			limits := defaultLifecycleConsumerLimits()
			if tt.limits != nil {
				tt.limits(&limits)
			}
			actual := runLifecycleConsumerPredicate(t, pool, f, limits)
			require.Equal(t, tt.expected, actual, "the canonical supplied-transaction predicate must preserve scoped logical/physical consumers without blocking proven compatible siblings")
		})
	}
}

func TestLifecycleConsumerOwnPredicatePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		runtime  bool
		change   string
		expected lifecycleConsumerTestResult
	}{
		{name: "exact own job has no competing consumer"},
		{name: "exact own job and canonical runtime are one consumer", runtime: true},
		{name: "null job lease cannot establish current allocation", runtime: true, change: "null_job_lease", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "expired job lease cannot establish current allocation", runtime: true, change: "expired_job_lease", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "null runtime lease cannot establish current allocation", runtime: true, change: "null_runtime_lease", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "expired runtime lease cannot establish current allocation", runtime: true, change: "expired_runtime_lease", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "another key for same input scope still competes", change: "other_job", expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "extra immutable dispatch reference rejects own exception", change: "extra_dispatch", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "own job cannot hide sibling runtime", change: "sibling_runtime", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "own job cannot hide another session runtime", change: "other_session_runtime", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "older allocation of same job remains uncertain", runtime: true, change: "older_runtime", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "runtime token is exact", runtime: true, change: "runtime_token", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "allocation digest is exact", runtime: true, change: "digest", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "expected turn is durable allocation identity", runtime: true, change: "turn", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "current runtime uncertain receipt is not ignored", runtime: true, change: "uncertainty", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "uninterpreted launch marker is not own authority", runtime: true, change: "launch_marker", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "own job does not exclude unknown delivery", runtime: true, change: "inbox", expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "own job does not exclude open preparation", runtime: true, change: "preparation", expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "own job payload cannot acquire another input", change: "payload_set", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "parked own job requires exact control proof", change: "parked", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "own job cannot hide sibling automation association", change: "automation_sibling", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "binding cannot make controller job an own input consumer", change: "controller_type", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "binding cannot substitute a maintenance queue", change: "other_queue", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			f := newLifecyclePredicateFixture(t, pool)
			seedPredicateOwnConsumer(t, pool, &f, tt.runtime)
			job := f.own.binding.jobID
			switch tt.change {
			case "null_job_lease":
				predicateExec(t, pool, `UPDATE jobs SET lease_expires_at=NULL WHERE org_id=$1 AND id=$2`, f.org, job)
			case "expired_job_lease":
				predicateExec(t, pool, `UPDATE jobs SET lease_expires_at=statement_timestamp()-interval '1 second' WHERE org_id=$1 AND id=$2`, f.org, job)
			case "null_runtime_lease":
				predicateExec(t, pool, `UPDATE thread_runtimes SET lease_expires_at=NULL WHERE org_id=$1 AND id=$2`, f.org, f.own.attempt.runtimeID)
			case "expired_runtime_lease":
				predicateExec(t, pool, `UPDATE thread_runtimes SET lease_expires_at=statement_timestamp()-interval '1 second' WHERE org_id=$1 AND id=$2`, f.org, f.own.attempt.runtimeID)
			case "other_job":
				otherJob := uuid.New()
				otherBinding, err := NewLifecycleJobBinding(f.org, f.fence, otherJob, f.own.binding.dispatchIDs, models.LifecycleDispatchMessage, nil)
				require.NoError(t, err, "construct a second immutable envelope naming the same input but a different job")
				payload, err := encodeLifecycleJobBindingPayload(nil, otherBinding)
				require.NoError(t, err, "encode a competing same-input job without sharing the own job identity")
				predicateExec(t, pool, `INSERT INTO jobs(id,org_id,queue,job_type,status,payload,dedupe_key) VALUES($1,$2,'agent','continue_session','pending',$3,$1::uuid::text)`, otherJob, f.org, payload)
			case "extra_dispatch":
				_, err := insertLifecycleConsumerDispatch(pool, f.org, f.session, f.lane, job, nil, nil)
				require.NoError(t, err, "retain an additional input that the proposed own binding did not contain")
			case "sibling_runtime", "other_session_runtime":
				session, lane := f.session, f.sibling
				if tt.change == "other_session_runtime" {
					other := seedLifecycleConsumerSession(t, pool, f.org)
					session, lane = other.ID, *other.PrimaryThreadID
				}
				r := createSeedRuntime(t, pool, f.org, session, lane)
				seedPredicateRuntimeAllocation(t, pool, f.org, r.ID, f.own.binding, uuid.New(), r.LeaseToken, 0)
			case "older_runtime":
				r := f.own.attempt.runtimeID
				predicateExec(t, pool, `UPDATE thread_runtimes SET status='lost' WHERE org_id=$1 AND id=$2`, f.org, r)
				newRuntime := createSeedRuntime(t, pool, f.org, f.session, f.lane)
				seedPredicateRuntimeAllocation(t, pool, f.org, newRuntime.ID, f.own.binding, f.own.attempt.jobToken, newRuntime.LeaseToken, f.own.attempt.expectedTurn)
				// Keep the declared current allocation exact; the older same-job
				// row must still prevent any broad same-job exception.
				oldAttempt := f.own.attempt
				f.own.attempt = &AttemptFence{binding: f.own.binding, jobToken: oldAttempt.jobToken, runtimeID: newRuntime.ID, runtimeToken: newRuntime.LeaseToken, allocationDigest: oldAttempt.allocationDigest, expectedTurn: oldAttempt.expectedTurn}
				predicateExec(t, pool, `UPDATE session_input_dispatches SET runtime_id=$3,runtime_lease_token=$4 WHERE org_id=$1 AND job_id=$2`, f.org, job, newRuntime.ID, newRuntime.LeaseToken)
			case "runtime_token":
				predicateExec(t, pool, `UPDATE thread_runtimes SET lease_token=gen_random_uuid() WHERE org_id=$1 AND id=$2`, f.org, f.own.attempt.runtimeID)
			case "digest":
				predicateExec(t, pool, `UPDATE thread_runtimes SET allocation_digest=repeat('b',64) WHERE org_id=$1 AND id=$2`, f.org, f.own.attempt.runtimeID)
			case "turn":
				predicateExec(t, pool, `UPDATE thread_runtimes SET allocation_expected_turn=allocation_expected_turn+1 WHERE org_id=$1 AND id=$2`, f.org, f.own.attempt.runtimeID)
			case "uncertainty":
				predicateExec(t, pool, `UPDATE thread_runtimes SET lease_loss_receipt='{"unknown":true}' WHERE org_id=$1 AND id=$2`, f.org, f.own.attempt.runtimeID)
			case "launch_marker":
				predicateExec(t, pool, `UPDATE thread_runtimes SET first_provider_launch_marker='{"uninterpreted":true}' WHERE org_id=$1 AND id=$2`, f.org, f.own.attempt.runtimeID)
			case "inbox":
				seedRuntimeInbox(t, pool, f.org, f.session, f.lane, 1, "unknown_delivery", &f.own.attempt.runtimeID)
			case "preparation":
				seedLifecyclePreparation(t, pool, f.org, f.session, &f.lane, job, "authorized", "none")
			case "payload_set":
				predicateExec(t, pool, `UPDATE jobs SET payload=jsonb_set(payload,'{input_dispatch_ids}',jsonb_build_array(gen_random_uuid()::text)) WHERE org_id=$1 AND id=$2`, f.org, job)
			case "parked":
				predicateExec(t, pool, `UPDATE jobs SET status='awaiting_start_control' WHERE org_id=$1 AND id=$2`, f.org, job)
			case "automation_sibling":
				other := f
				other.lane = f.sibling
				seedPredicateAutomation(t, pool, other, "direct", job)
			case "controller_type":
				predicateExec(t, pool, `UPDATE jobs SET job_type='run_code_review_recheck' WHERE org_id=$1 AND id=$2`, f.org, job)
			case "other_queue":
				predicateExec(t, pool, `UPDATE jobs SET queue='maintenance' WHERE org_id=$1 AND id=$2`, f.org, job)
			}
			require.Equal(t, tt.expected, runLifecycleConsumerPredicate(t, pool, f, defaultLifecycleConsumerLimits()), "own exclusion must prove the actual immutable job, all references and exact current allocation without hiding another input, predecessor or physical uncertainty")
		})
	}
}

func TestLifecycleConsumerOwnRequestIdentityPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                 string
		kind                 models.LifecycleDispatchKind
		runtime, sessionWide bool
		requestLane          string
		expected             lifecycleConsumerTestResult
	}{
		{name: "pending answer exact lane", kind: models.LifecycleDispatchAnswer, requestLane: "same"},
		{name: "running answer exact lane", kind: models.LifecycleDispatchAnswer, runtime: true, requestLane: "same"},
		{name: "pending denial exact lane", kind: models.LifecycleDispatchDenial, requestLane: "same"},
		{name: "running denial exact lane", kind: models.LifecycleDispatchDenial, runtime: true, requestLane: "same"},
		{name: "pending answer cannot use sibling request", kind: models.LifecycleDispatchAnswer, requestLane: "sibling", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "running answer cannot use sibling request", kind: models.LifecycleDispatchAnswer, runtime: true, requestLane: "sibling", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "pending denial cannot use sibling request", kind: models.LifecycleDispatchDenial, requestLane: "sibling", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "running denial cannot use sibling request", kind: models.LifecycleDispatchDenial, runtime: true, requestLane: "sibling", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "threaded answer cannot use null request lane", kind: models.LifecycleDispatchAnswer, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "running denial cannot use null request lane", kind: models.LifecycleDispatchDenial, runtime: true, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "session-wide answer matches null request lane", kind: models.LifecycleDispatchAnswer, sessionWide: true},
		{name: "session-wide denial matches null request lane", kind: models.LifecycleDispatchDenial, sessionWide: true},
		{name: "session-wide answer cannot use threaded request", kind: models.LifecycleDispatchAnswer, sessionWide: true, requestLane: "same", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "session-wide denial cannot use threaded request", kind: models.LifecycleDispatchDenial, sessionWide: true, requestLane: "same", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			f := newLifecyclePredicateFixture(t, pool)
			if tt.sessionWide {
				f.fence = AdmissionFence{sessionID: f.session}
			}
			var requestLane *uuid.UUID
			if tt.requestLane == "same" {
				requestLane = &f.lane
			} else if tt.requestLane == "sibling" {
				requestLane = &f.sibling
			}
			request := uuid.New()
			predicateExec(t, pool, `INSERT INTO session_human_input_requests(id,org_id,session_id,thread_id,agent_type,request_kind,title,body)
			 VALUES($1,$2,$3,$4,$5,'free_text','Synthetic identity request','Synthetic identity body')`, request, f.org, f.session, requestLane, models.AgentTypeCodex)
			seedPredicateOwnConsumerWithRequest(t, pool, &f, tt.runtime, tt.kind, &request)
			require.Equal(t, tt.expected, runLifecycleConsumerPredicate(t, pool, f, defaultLifecycleConsumerLimits()), "own proof must match the actual request's tenant, session and nullable lane, without inferring request readiness or actor authority")
		})
	}
}

func TestLifecycleConsumerAutomationReverseAnchorPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, mode string }{
		{name: "direct active association", mode: "direct"},
		{name: "protected generation with missing direct route", mode: "generation"},
		{name: "protected generation contradicts direct route", mode: "contradictory_generation"},
		{name: "legacy per-run association", mode: "per_run"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			f := newLifecyclePredicateFixture(t, pool)
			seedPredicateAutomation(t, pool, f, tt.mode, uuid.Nil)
			require.Equal(t, lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}, runLifecycleConsumerPredicate(t, pool, f, defaultLifecycleConsumerLimits()), "automation direct, protected-generation and per-run associations must retain scoped jobs despite unrouteable payloads")
		})
	}
}

func TestLifecycleConsumerRecheckReverseAnchorPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	c := seedRecheckPersistenceCompletion(t, context.Background(), pool, "")
	predicateExec(t, pool, `UPDATE jobs SET payload='{}' WHERE org_id=$1 AND id=$2`, c.OrgID, c.JobID)
	f := lifecyclePredicateFixture{org: c.OrgID, session: c.SessionID, lane: c.ThreadID, fence: AdmissionFence{sessionID: c.SessionID, threadID: c.ThreadID, threaded: true}}
	require.Equal(t, lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}, runLifecycleConsumerPredicate(t, pool, f, defaultLifecycleConsumerLimits()), "a direct recheck receipt must retain its job scope even when legacy payload routing is destroyed")
}

func newLifecyclePredicateFixture(t *testing.T, pool *pgxpool.Pool) lifecyclePredicateFixture {
	t.Helper()
	org, session, lane := seedRuntimeSeedThread(t, pool)
	sibling := models.SessionThread{OrgID: org, SessionID: session, Label: "Synthetic independent lane", AgentType: models.DefaultDefaultAgentType, Status: models.ThreadStatusIdle}
	require.NoError(t, NewSessionThreadStore(pool).Create(context.Background(), &sibling, 10), "create a separate explicitly named compatible lane")
	return lifecyclePredicateFixture{org: org, session: session, lane: lane, sibling: sibling.ID, fence: AdmissionFence{sessionID: session, threadID: lane, threaded: true}}
}

func runLifecycleConsumerPredicate(t *testing.T, pool *pgxpool.Pool, f lifecyclePredicateFixture, limits lifecycleConsumerLimits) lifecycleConsumerTestResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
	defer cancel()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	require.NoError(t, err, "caller must own the sole bounded transaction")
	defer func() { _ = tx.Rollback(context.Background()) }()
	require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, tx), "caller must configure its transaction before acquiring lifecycle scope")
	if f.own != nil {
		var job uuid.UUID
		require.NoError(t, tx.QueryRow(ctx, `SELECT id FROM jobs WHERE org_id=$1 AND id=$2 FOR NO KEY UPDATE`, f.org, f.own.binding.jobID).Scan(&job), "an own-consumer caller must hold its known higher-rank job before parent and lane acquisition")
	}
	var lanes []uuid.UUID
	if f.fence.threaded {
		lanes = []uuid.UUID{f.fence.threadID}
	}
	_, err = lockLifecycleScopeInTx(ctx, tx, f.org, lifecycleScope{sessionID: f.session, threadIDs: lanes})
	require.NoError(t, err, "caller must hold the canonical parent and explicit lane before reading consumers")
	blocked, readErr := hasCompetingInputConsumerInTx(ctx, tx, f.org, f.fence, f.own, limits)
	var stillOpen int
	require.NoError(t, tx.QueryRow(ctx, "SELECT 1").Scan(&stillOpen), "typed reconciliation and discovery results must leave the caller's transaction open")
	require.Equal(t, 1, stillOpen, "the consumer reader must not settle or replace the supplied transaction")
	require.NoError(t, tx.Rollback(ctx), "only the transaction owner may settle the read unit")
	return lifecycleConsumerResult(blocked, readErr)
}

func seedPredicateLegacyJob(t *testing.T, pool *pgxpool.Pool, f lifecyclePredicateFixture, payload string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	predicateExec(t, pool, `INSERT INTO jobs(id,org_id,queue,job_type,payload,status,dedupe_key) VALUES($1,$2,'agent','continue_session',$3,'pending',$1::uuid::text)`, id, f.org, json.RawMessage(payload))
	return id
}

func seedPredicateOwnConsumer(t *testing.T, pool *pgxpool.Pool, f *lifecyclePredicateFixture, runtime bool) {
	t.Helper()
	seedPredicateOwnConsumerWithRequest(t, pool, f, runtime, models.LifecycleDispatchMessage, nil)
}

func seedPredicateOwnConsumerWithRequest(t *testing.T, pool *pgxpool.Pool, f *lifecyclePredicateFixture, runtime bool, kind models.LifecycleDispatchKind, request *uuid.UUID) {
	t.Helper()
	job, dispatch := uuid.New(), uuid.New()
	binding, err := NewLifecycleJobBinding(f.org, f.fence, job, []uuid.UUID{dispatch}, kind, request)
	require.NoError(t, err, "construct exact proposed own identity")
	payload, err := encodeLifecycleJobBindingPayload(nil, binding)
	require.NoError(t, err, "encode the exact immutable binding stored on the actual job")
	predicateExec(t, pool, `INSERT INTO jobs(id,org_id,queue,job_type,payload,status) VALUES($1,$2,'agent','continue_session',$3,'pending')`, job, f.org, payload)
	f.own = &lifecycleOwnConsumer{binding: binding}
	var runtimeID, runtimeToken, jobToken *uuid.UUID
	consumer := "job"
	if runtime {
		r := createSeedRuntime(t, pool, f.org, f.session, f.lane)
		token := uuid.New()
		turn := 7
		seedPredicateRuntimeAllocation(t, pool, f.org, r.ID, binding, token, r.LeaseToken, turn)
		predicateExec(t, pool, `UPDATE jobs SET status='running',lock_token=$3,lease_expires_at=now()+interval '1 hour' WHERE org_id=$1 AND id=$2`, f.org, job, token)
		digest, err := ParseLifecycleDigest(strings.Repeat("a", 64))
		require.NoError(t, err, "decode the persisted immutable allocation digest")
		attempt, err := NewAttemptFence(binding, token, r.ID, r.LeaseToken, digest, turn)
		require.NoError(t, err, "declare exact current runtime identity for durable proof")
		f.own.attempt = &attempt
		runtimeID, runtimeToken, jobToken = &r.ID, &r.LeaseToken, &token
		consumer = "runtime"
	}
	var lane *uuid.UUID
	var threadEpoch *int64
	if f.fence.threaded {
		lane, threadEpoch = &f.fence.threadID, &f.fence.threadEpoch
	}
	predicateExec(t, pool, `INSERT INTO session_input_dispatches(id,org_id,session_id,thread_id,session_epoch,thread_epoch,kind,request_id,ingress_kind,ingress_key,payload_digest,validated_payload,normalization_version,admission_state,consumer_kind,job_id,runtime_id,job_lock_token,runtime_lease_token)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,'synthetic_predicate',$1::uuid::text,repeat('a',64),'{}',1,'bound',$9,$10,$11,$12,$13)`, dispatch, f.org, f.session, lane, f.fence.sessionEpoch, threadEpoch, kind, request, consumer, job, runtimeID, jobToken, runtimeToken)
}

func seedPredicateRuntimeAllocation(t *testing.T, pool *pgxpool.Pool, org, runtime uuid.UUID, binding LifecycleJobBinding, jobToken, runtimeToken uuid.UUID, turn int) {
	t.Helper()
	predicateExec(t, pool, `UPDATE thread_runtimes SET job_id=$3,job_attempt_token=$4,lease_token=$5,allocation_digest=repeat('a',64),input_dispatch_ids=$6,
	 admission_session_epoch=$7,admission_thread_epoch=$8,allocation_expected_turn=$9,provider_invocation_ordinal=0 WHERE org_id=$1 AND id=$2`, org, runtime, binding.jobID, jobToken, runtimeToken, binding.dispatchIDs, binding.fence.sessionEpoch, binding.fence.threadEpoch, turn)
}

func seedPredicateExecutor(t *testing.T, pool *pgxpool.Pool, f lifecyclePredicateFixture, lane *uuid.UUID, status string) {
	t.Helper()
	node := "synthetic-" + uuid.NewString()
	job := seedLifecycleConsumerJob(t, pool, f.org, models.JobStatusPending)
	predicateExec(t, pool, `INSERT INTO nodes(id) VALUES($1)`, node)
	predicateExec(t, pool, `INSERT INTO session_executors(org_id,session_id,thread_id,job_id,job_type,host_node_id,owner_id,lock_token,status,lease_expires_at,completed_at,exit_code)
	 VALUES($1,$2,$3,$4,'continue_session',$5,'synthetic-owner',gen_random_uuid(),$6,now()-interval '1 day',CASE WHEN $6::text='completed' THEN now() END,CASE WHEN $6::text='completed' THEN 0 END)`, f.org, f.session, lane, job, node, status)
}

func seedPredicateHolder(t *testing.T, pool *pgxpool.Pool, f lifecyclePredicateFixture, kind string, owner, token uuid.UUID, container, node, status string) {
	t.Helper()
	predicateExec(t, pool, `INSERT INTO session_sandbox_holders(org_id,session_id,container_id,holder_kind,holder_id,owner_node_id,lease_token,status,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,now()-interval '1 day')`, f.org, f.session, container, kind, owner, node, token, status)
}

func seedPredicateAutomation(t *testing.T, pool *pgxpool.Pool, f lifecyclePredicateFixture, mode string, job uuid.UUID) {
	t.Helper()
	if job == uuid.Nil {
		job = seedPredicateLegacyJob(t, pool, f, `{}`)
	}
	automation, run := uuid.New(), uuid.New()
	predicateExec(t, pool, `INSERT INTO automations(id,org_id,name,goal) VALUES($1,$2,'Synthetic consumer','Observe a synthetic target')`, automation, f.org)
	var session, thread, target *uuid.UUID
	var generation *int
	if mode == "direct" {
		session, thread = &f.session, &f.lane
	}
	if mode == "generation" || mode == "contradictory_generation" {
		integration, repository, targetID := uuid.New(), uuid.New(), uuid.New()
		predicateExec(t, pool, `INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, integration, f.org)
		predicateExec(t, pool, `INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id) VALUES($1,$2,$3,(hashtextextended($1::uuid::text,0)&9223372036854775807),'synthetic/consumer','https://example.invalid/consumer.git',1)`, repository, f.org, integration)
		predicateExec(t, pool, `INSERT INTO automation_targets(id,org_id,automation_id,repository_id,target_kind,target_key) VALUES($1,$2,$3,$4,'github_pull_request','1')`, targetID, f.org, automation, repository)
		predicateExec(t, pool, `INSERT INTO automation_target_sessions(org_id,target_id,generation,session_id) VALUES($1,$2,1,$3)`, f.org, targetID, f.session)
		target = &targetID
		generation = lifecycleConsumerTestPtr(1)
		if mode == "contradictory_generation" {
			other := seedLifecycleConsumerSession(t, pool, f.org)
			session, thread = &other.ID, other.PrimaryThreadID
		}
	}
	predicateExec(t, pool, `INSERT INTO automation_runs(id,org_id,automation_id,goal_snapshot,status,session_id,thread_id,job_id,target_id,target_generation)
	 VALUES($1,$2,$3,'Synthetic consumer','running',$4,$5,$6,$7,$8)`, run, f.org, automation, session, thread, job, target, generation)
	if mode == "per_run" {
		predicateExec(t, pool, `INSERT INTO session_automation_links(org_id,session_id,automation_run_id) VALUES($1,$2,$3)`, f.org, f.session, run)
	}
}

func predicateExec(t *testing.T, pool *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	_, err := pool.Exec(context.Background(), query, args...)
	require.NoError(t, err, "prepare synthetic full-schema consumer evidence")
}

func seedPredicatePreviouslyDeliveredInbox(t *testing.T, pool *pgxpool.Pool, f lifecyclePredicateFixture, state models.ThreadInboxDeliveryState) {
	t.Helper()
	seedRuntimeInbox(t, pool, f.org, f.session, f.lane, 1, "unknown_delivery", nil)
	predicateExec(t, pool, `UPDATE thread_inbox_entries SET delivered_at=now() WHERE org_id=$1 AND thread_id=$2`, f.org, f.lane)
	var id uuid.UUID
	require.NoError(t, pool.QueryRow(context.Background(), "SELECT id FROM thread_inbox_entries WHERE org_id=$1 AND thread_id=$2", f.org, f.lane).Scan(&id), "read the exact synthetic uncertain-delivery entry")
	var entry models.ThreadInboxEntry
	var err error
	if state == models.ThreadInboxDeliveryStatePending {
		entry, err = NewThreadInboxStore(pool).RetryRecoverable(context.Background(), f.org, f.lane, id, true)
	} else {
		entry, err = NewThreadInboxStore(pool).MarkDeadLetter(context.Background(), f.org, f.lane, id, "synthetic retained delivery")
	}
	require.NoError(t, err, "exercise the actual legacy transition that preserves a previous unknown delivery timestamp")
	require.Equal(t, state, entry.DeliveryState, "the legacy transition must genuinely reach the pending or dead-letter state the predicate observes")
	require.NotNil(t, entry.DeliveredAt, "the actual legacy transition must retain earlier delivery uncertainty independently of the new state label")
}
