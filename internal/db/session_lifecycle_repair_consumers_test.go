package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestLegacyJobRouteRetainsRepairScopeUncertainty(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, extra string
		expected    bool
	}{
		{name: "ordinary route"},
		{name: "repair ID", extra: `,"repair_run_id":"55555555-5555-4555-8555-555555555555"`, expected: true},
		{name: "null metadata remains unknown", extra: `,"repair_run_id":null`, expected: true},
		{name: "empty metadata remains unknown", extra: `,"repair_run_id":""`, expected: true},
		{name: "wrong type remains unknown", extra: `,"repair_run_id":false`, expected: true},
		{name: "folded worker field", extra: `,"REPAIR_RUN_ID":"55555555-5555-4555-8555-555555555555"`, expected: true},
		{name: "escaped worker field", extra: `,"repair_\u0072un_id":"55555555-5555-4555-8555-555555555555"`, expected: true},
		{name: "duplicate empty cannot erase metadata", extra: `,"repair_run_id":"55555555-5555-4555-8555-555555555555","repair_run_id":""`, expected: true},
		{name: "worker repair command without reservation ID", extra: `,"command_type":"resolve_conflicts"`, expected: true},
		{name: "null repair command remains unknown", extra: `,"command_type":null`, expected: true},
		{name: "empty repair command remains unknown", extra: `,"command_type":""`, expected: true},
		{name: "folded repair command field", extra: `,"COMMAND_TYPE":"resolve_conflicts"`, expected: true},
		{name: "escaped repair command field", extra: `,"command_\u0074ype":"resolve_conflicts"`, expected: true},
		{name: "duplicate empty cannot erase repair command", extra: `,"command_type":"resolve_conflicts","command_type":""`, expected: true},
		{name: "nested display metadata is not worker scope", extra: `,"metadata":{"repair_run_id":"55555555-5555-4555-8555-555555555555"}`},
		{name: "nested display command is not worker scope", extra: `,"metadata":{"command_type":"resolve_conflicts"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, session, lane := legacyJobRouteTestIDs()
			payload := json.RawMessage(fmt.Sprintf(`{"org_id":%q,"session_id":%q,"thread_id":%q%s}`, org, session, lane, tt.extra))
			expected := legacyJobRouteTestObservation()
			expected.repairScopeUnproved = tt.expected
			require.Equal(t, expected, decodeLegacyJobRoute(org, payload), "repair metadata must retain uncertainty without replacing the exact independent route observations")
		})
	}
}

func TestLifecycleConsumerRetainedAndLegacyReadsSharePayloadBudget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		budget   int
		expected lifecycleConsumerTestResult
	}{
		{name: "two complete reads fit exactly", budget: 4},
		{name: "second reader gets no new allowance", budget: 3, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerDiscoveryIncomplete}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create an isolated canonical consumer-query boundary")
			t.Cleanup(mock.Close)
			org, session, lane := legacyJobRouteTestIDs()
			sibling, runtime, job, otherJob := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			limits := defaultLifecycleConsumerLimits()
			limits.payloadBytes = tt.budget
			mock.ExpectQuery(regexp.QuoteMeta(lifecycleTransactionPolicyQuery)).WillReturnRows(pgxmock.NewRows([]string{"isolation", "marker", "lock", "statement"}).AddRow("read committed", "1", "250ms", "1500ms"))
			mock.ExpectQuery("SELECT workspace_recovery_id IS NOT NULL").WithArgs(org, session, true, lane).WillReturnRows(pgxmock.NewRows([]string{"recovery", "member"}).AddRow(false, true))
			mock.ExpectQuery(regexp.QuoteMeta(lifecycleConsumerLaneKeysQuery)).WithArgs(pgx.QueryExecModeExec, org, session, 4097).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(lane).AddRow(sibling))
			mock.ExpectQuery(regexp.QuoteMeta(lifecycleConsumerGenerationKeysQuery)).WithArgs(pgx.QueryExecModeExec, org, session, 33).WillReturnRows(pgxmock.NewRows([]string{"target", "generation"}))
			mock.ExpectQuery(regexp.QuoteMeta(lifecycleConsumerRuntimeKeysQuery)).WithArgs(pgx.QueryExecModeExec, org, session, lifecycleConsumerUUIDArray([]uuid.UUID{lane, sibling}), 513).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(runtime))
			mock.ExpectQuery(regexp.QuoteMeta(lifecycleConsumerEvidenceQuery)).WithArgs(pgx.QueryExecModeExec, org, session, 513, lifecycleConsumerUUIDArray([]uuid.UUID{lane, sibling}), lifecycleConsumerUUIDArray([]uuid.UUID{runtime})).WillReturnRows(pgxmock.NewRows([]string{"source", "id", "session", "thread", "parent", "job", "runtime", "state", "control", "whole", "physical", "owner", "found_job", "job_status"}).AddRow(lifecycleConsumerRuntime, runtime, session, &sibling, &session, &job, &runtime, "live", "", false, true, true, &job, "failed"))
			mock.ExpectQuery(regexp.QuoteMeta(lifecycleConsumerRetainedJobRoutesQuery)).WithArgs(pgx.QueryExecModeExec, org, lifecycleConsumerUUIDArray([]uuid.UUID{job}), legacyJobRouteMaxBytes).WillReturnRows(pgxmock.NewRows([]string{"id", "bytes", "payload"}).AddRow(job, 2, json.RawMessage(`{}`)))
			mock.ExpectQuery(regexp.QuoteMeta(lifecycleConsumerLegacyJobsQuery)).WithArgs(org, 513, legacyJobRouteMaxBytes).WillReturnRows(pgxmock.NewRows([]string{"id", "bytes", "payload"}).AddRow(otherJob, 2, json.RawMessage(`{}`)))
			blocked, err := hasCompetingInputConsumerInTx(ctx, mock, org, AdmissionFence{sessionID: session, threadID: lane, threaded: true}, nil, limits)
			require.Equal(t, tt.expected, lifecycleConsumerResult(blocked, err), "the actual canonical predicate must account for both payload reads against one unchanged aggregate cap")
			require.NoError(t, mock.ExpectationsWereMet(), "bounded repair discovery must compose the original canonical transaction without writes, settlement or hidden lookups")
		})
	}
}

func TestLifecycleConsumerLegacyRepairCannotBorrowSiblingScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                      string
		repair, commandOnly, sameLane, other, own bool
		expected                                  lifecycleConsumerTestResult
	}{
		{name: "ordinary sibling remains independent"},
		{name: "repair sibling is unresolved", repair: true, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "repair same lane is unresolved", repair: true, sameLane: true, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "own binding cannot supply missing repair scope", repair: true, own: true, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "another session repair is not tenant-wide exclusion", repair: true, other: true},
		{name: "worker repair command without reservation ID cannot borrow sibling scope", commandOnly: true, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "another session command is not tenant-wide exclusion", commandOnly: true, other: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, session, lane := legacyJobRouteTestIDs()
			job, parent, target := uuid.New(), session, uuid.New()
			if tt.sameLane {
				target = lane
			}
			if tt.other {
				parent = uuid.New()
			}
			extra := ""
			if tt.repair {
				extra = `,"repair_run_id":"55555555-5555-4555-8555-555555555555"`
			}
			if tt.commandOnly {
				extra = `,"command_type":"resolve_conflicts"`
			}
			payload := json.RawMessage(fmt.Sprintf(`{"org_id":%q,"session_id":%q,"thread_id":%q%s}`, org, parent, target, extra))
			proof := lifecycleOwnConsumerProof{}
			if tt.own {
				proof.binding = &LifecycleJobBinding{jobID: job}
			}
			blocked, err := classifyLegacyLifecycleConsumer(lifecycleLegacyConsumerJob{job, decodeLegacyJobRoute(org, payload)}, AdmissionFence{sessionID: session, threadID: lane, threaded: true}, map[uuid.UUID]uuid.UUID{target: parent}, false, proof)
			require.Equal(t, tt.expected, lifecycleConsumerResult(blocked, err), "only actual scoped repair work must reconcile before any ordinary sibling or own-consumer exception")
			if (tt.repair || tt.commandOnly) && !tt.other {
				require.ErrorIs(t, err, errLifecycleRepairConsumerReconciliation, "ordinary admission needs the specific repair waiting reason while generic callers retain canonical reconciliation")
			}
		})
	}
}

func TestLifecycleConsumerRetainedRepairJobRoutes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, scenario string
		expected       map[uuid.UUID]bool
		cause          error
	}{
		{name: "no retained references", scenario: "empty"},
		{name: "ordinary retained job", scenario: "ordinary", expected: map[uuid.UUID]bool{}},
		{name: "terminal repair identity is still observed", scenario: "repair"},
		{name: "terminal repair command without reservation identity is still observed", scenario: "command"},
		{name: "duplicate references are queried once", scenario: "duplicate references"},
		{name: "missing job cannot clear a prior association", scenario: "missing", cause: ErrLifecycleConsumerReconciliation},
		{name: "unexpected job cannot expand scope", scenario: "unexpected", cause: ErrLifecycleConsumerReconciliation},
		{name: "duplicate returned row cannot prove completeness", scenario: "duplicate row", cause: ErrLifecycleConsumerReconciliation},
		{name: "structured row cap", scenario: "row cap", cause: ErrLifecycleConsumerDiscoveryIncomplete},
		{name: "single payload cap", scenario: "large payload", cause: ErrLifecycleConsumerDiscoveryIncomplete},
		{name: "aggregate payload cap", scenario: "byte cap", cause: ErrLifecycleConsumerDiscoveryIncomplete},
		{name: "negative size cannot bypass byte accounting", scenario: "negative", cause: ErrLifecycleConsumerDiscoveryIncomplete},
		{name: "query failure", scenario: "query", cause: errors.New("synthetic route read failure")},
		{name: "scan failure", scenario: "scan"},
		{name: "row failure", scenario: "row", cause: errors.New("synthetic route row failure")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "construct an isolated read-only retained-job route probe")
			t.Cleanup(mock.Close)
			org, session, lane := legacyJobRouteTestIDs()
			job := uuid.New()
			limits := defaultLifecycleConsumerLimits()
			evidence := []lifecycleConsumerEvidence{{source: lifecycleConsumerRuntime, id: uuid.New(), sessionID: session, threadID: &lane, jobID: &job, foundJobID: &job, state: "live", jobStatus: "failed"}}
			if tt.scenario == "empty" {
				evidence = nil
			} else {
				if tt.scenario == "duplicate references" {
					evidence = append(evidence, evidence[0])
				}
				query := mock.ExpectQuery(regexp.QuoteMeta(lifecycleConsumerRetainedJobRoutesQuery)).WithArgs(pgx.QueryExecModeExec, org, lifecycleConsumerUUIDArray([]uuid.UUID{job}), legacyJobRouteMaxBytes)
				rows := pgxmock.NewRows([]string{"id", "bytes", "payload"})
				payload := json.RawMessage(`{"repair_run_id":"55555555-5555-4555-8555-555555555555"}`)
				if tt.scenario == "ordinary" {
					payload = json.RawMessage(`{}`)
				}
				if tt.scenario == "command" {
					payload = json.RawMessage(`{"command_type":"resolve_conflicts"}`)
				}
				size, returnedID := len(payload), job
				switch tt.scenario {
				case "query":
					query.WillReturnError(tt.cause)
				case "missing":
					query.WillReturnRows(rows)
				case "scan":
					query.WillReturnRows(rows.AddRow(job, "not an integer", payload))
				default:
					switch tt.scenario {
					case "unexpected":
						returnedID = uuid.New()
					case "row cap":
						limits.structuredRows = 1
					case "large payload":
						size, payload = legacyJobRouteMaxBytes+1, nil
					case "byte cap":
						limits.payloadBytes = size - 1
					case "negative":
						size = -1
					}
					rows.AddRow(returnedID, size, payload)
					if tt.scenario == "duplicate row" || tt.scenario == "row cap" {
						rows.AddRow(returnedID, size, payload)
					}
					if tt.scenario == "row" {
						rows.RowError(0, tt.cause)
					}
					query.WillReturnRows(rows)
				}
			}
			actual, payloadBytes, err := readRetainedLifecycleRepairJobsInTx(context.Background(), mock, org, evidence, limits)
			if tt.cause != nil {
				require.ErrorIs(t, err, tt.cause, "a failed or incomplete scoped read must not yield a partial negative repair observation")
				require.Nil(t, actual, "failed repair discovery must expose no reusable partial map")
				require.Zero(t, payloadBytes, "failed discovery must expose no reusable partial byte accounting")
			} else if tt.scenario == "scan" {
				require.Error(t, err, "a row decoding failure must reach the owning bounded transaction")
				require.Nil(t, actual, "a decoding failure cannot establish ordinary sibling scope")
				require.Zero(t, payloadBytes, "a decoding failure returns no completed byte observation")
			} else {
				require.NoError(t, err, "complete exact job references may be observed without acquiring authority or filtering terminal status")
				expected := tt.expected
				if tt.scenario == "repair" || tt.scenario == "command" || tt.scenario == "duplicate references" {
					expected = map[uuid.UUID]bool{job: true}
				}
				require.Equal(t, expected, actual, "only exact retained jobs carrying repair metadata may supply unresolved repair evidence")
				expectedBytes := len(`{"repair_run_id":"55555555-5555-4555-8555-555555555555"}`)
				if tt.scenario == "empty" {
					expectedBytes = 0
				} else if tt.scenario == "ordinary" {
					expectedBytes = len(`{}`)
				} else if tt.scenario == "command" {
					expectedBytes = len(`{"command_type":"resolve_conflicts"}`)
				}
				require.Equal(t, expectedBytes, payloadBytes, "exact retained-job payload reads must consume the same aggregate discovery budget as subsequent legacy reads")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "the scoped reader must issue one exact tenant-qualified ID probe with no history scan, status filter, writes or transaction settlement")
		})
	}
}
