package db

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// These plans establish index eligibility with sequential scans discouraged in
// the local transaction. They do not measure natural planner choices or fleet
// performance, and EXPLAIN without ANALYZE does not enqueue any notifications.
func TestCallerSendWakeDueIndexesPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, table, column, index, query string
	}{
		{name: "reviewer dispatch", table: "code_review_agent_results", column: "dispatch_due_at", index: "idx_cr_agent_results_dispatch_due", query: repairReviewerSendWakesSQL},
		{name: "loop startup", table: "session_review_loops", column: "startup_send_due_at", index: "idx_srl_startup_send_due", query: repairLoopStartupWakesSQL},
		{name: "prepared creation", table: "review_loop_start_intents", column: "due_at", index: "idx_review_loop_start_due", query: repairPreparedStartWakesSQL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			pool := testpostgres.NewPool(t, ctx, "../../migrations", 1)
			tx := callerSendSchemaBegin(t, ctx, pool)
			var actual struct {
				name, table, method, definition string
				valid, ready, live, unique      bool
			}
			err := tx.QueryRow(ctx, `SELECT ix.relname,tab.relname,am.amname,pg_get_indexdef(i.indexrelid),i.indisvalid,i.indisready,i.indislive,i.indisunique
 FROM pg_index i JOIN pg_class ix ON ix.oid=i.indexrelid JOIN pg_class tab ON tab.oid=i.indrelid
 JOIN pg_am am ON am.oid=ix.relam WHERE i.indexrelid=to_regclass($1)`, "public."+tt.index).Scan(
				&actual.name, &actual.table, &actual.method, &actual.definition, &actual.valid, &actual.ready, &actual.live, &actual.unique)
			require.NoError(t, err, "the complete migration chain must install the actual named due index")
			expected := actual
			expected.name, expected.table, expected.method = tt.index, tt.table, "btree"
			expected.definition = fmt.Sprintf("CREATE INDEX %s ON public.%s USING btree (%s, id) WHERE (%s IS NOT NULL)", tt.index, tt.table, tt.column, tt.column)
			expected.valid, expected.ready, expected.live, expected.unique = true, true, true, false
			require.Equal(t, expected, actual, "due index identity, full key order, partial predicate and readiness must match the contract")
			_, err = tx.Exec(ctx, `SET LOCAL enable_seqscan=off`)
			require.NoError(t, err, "discourage sequential scans only inside this rolled-back eligibility proof")
			var raw json.RawMessage
			require.NoError(t, tx.QueryRow(ctx, "EXPLAIN (VERBOSE,FORMAT JSON) "+tt.query).Scan(&raw), "explain the exact bounded production repair statement without executing its INSERT")
			var plan any
			require.NoError(t, json.Unmarshal(raw, &plan), "the actual PostgreSQL plan is valid JSON")
			var indexes []string
			callerSendWakePlanIndexes(plan, &indexes)
			require.Contains(t, indexes, tt.index, "the actual due index must be eligible for its production repair selector")
			require.Contains(t, tt.query, "LIMIT 100", "each independent global repair statement must retain its literal one-hundred-owner bound")
			var jobs int
			require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM jobs`).Scan(&jobs), "read the empty isolated job queue after non-executing plans")
			require.Equal(t, 0, jobs, "index eligibility checks must not create notifications or send work")
		})
	}
}

func callerSendWakePlanIndexes(value any, indexes *[]string) {
	switch value := value.(type) {
	case map[string]any:
		if name, ok := value["Index Name"].(string); ok {
			*indexes = append(*indexes, name)
		}
		for _, child := range value {
			callerSendWakePlanIndexes(child, indexes)
		}
	case []any:
		for _, child := range value {
			callerSendWakePlanIndexes(child, indexes)
		}
	}
}

type callerSendWakeCandidateCapture struct {
	pgx.Tx
	query string
	args  []any
}

func (t *callerSendWakeCandidateCapture) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	t.query, t.args = query, append([]any(nil), args...)
	return t.Tx.Query(ctx, query, args...)
}

// The full-schema rows below are legal SQL-shape fixtures. Their private JSON
// is not a complete command codec and is never executed. The captured query
// and verbose plan prove that candidate identities use no frozen-payload
// expressions; this is not a measurement of physical TOAST/cache I/O.
func TestRetainedStartCandidateBoundsPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                              string
		limit, prepared, startup          int
		expectedPrepared, expectedStartup int
	}{
		{name: "later startups survive older prepared backlog", limit: 2, prepared: 5, startup: 3, expectedPrepared: 2, expectedStartup: 2},
		{name: "scope future exclusion and tie ordering", limit: 10, prepared: 3, startup: 2, expectedPrepared: 3, expectedStartup: 2},
		{name: "zero uses bounded default for each category", limit: 0, prepared: 105, startup: 105, expectedPrepared: 100, expectedStartup: 100},
		{name: "oversized request clamps each category", limit: 101, prepared: 105, startup: 105, expectedPrepared: 100, expectedStartup: 100},
		{name: "negative uses bounded default", limit: -1, prepared: 3, startup: 2, expectedPrepared: 3, expectedStartup: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			pool := testpostgres.NewPool(t, ctx, "../../migrations", 2)
			_, err := pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready AND dispatch_status_ready`)
			require.NoError(t, err, "enable retained SQL fixtures only inside this isolated synthetic database")
			tx := callerSendSchemaBegin(t, ctx, pool)
			fixture := callerSendSchemaSeed(t, ctx, tx)
			type expectedDue struct {
				candidate ReviewLoopStartCandidate
				due       time.Time
			}
			expected := make([]expectedDue, 0, tt.expectedPrepared+tt.expectedStartup)
			base := time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC)
			// Reverse insertion makes due/id order independent of fixture order.
			for n := tt.prepared - 1; n >= 0; n-- {
				f := fixture
				f.owner = uuid.MustParse(fmt.Sprintf("00000000-0000-4000-8000-%012x", n+1))
				f.pass = uuid.New()
				f.at = base.Add(time.Duration(n/2) * time.Second)
				require.NoError(t, callerSendSchemaInsertMap(ctx, tx, "review_loop_start_intents", callerSendSchemaStartRow(f, "manual", uuid.Nil)), "insert a complete legal prepared owner with deterministic due/id ordering")
				if n < tt.expectedPrepared {
					expected = append(expected, expectedDue{ReviewLoopStartCandidate{ID: f.owner, SessionID: f.session}, f.at})
				}
			}
			owner := callerSendSchemaOwners()[1]
			for n := tt.startup - 1; n >= 0; n-- {
				f := callerSendWakeIndexSession(t, ctx, tx, fixture.org)
				f.owner = uuid.MustParse(fmt.Sprintf("00000000-0000-4000-8001-%012x", n+1))
				f.pass = uuid.New()
				f.at = base.Add(time.Duration(tt.prepared+n+1) * time.Second)
				shape := callerSendSchemaShape(owner, f, "pending")
				shape["due_at"] = f.at
				require.NoError(t, callerSendSchemaInsertOwner(ctx, tx, owner, f, shape), "insert a genuine startup and its bound start/pass after the entire prepared backlog")
				if n < tt.expectedStartup {
					expected = append(expected, expectedDue{ReviewLoopStartCandidate{ID: f.owner, SessionID: f.session}, f.at})
				}
			}
			future := fixture
			future.owner, future.pass, future.at = uuid.New(), uuid.New(), time.Now().Add(24*time.Hour)
			require.NoError(t, callerSendSchemaInsertMap(ctx, tx, "review_loop_start_intents", callerSendSchemaStartRow(future, "manual", uuid.Nil)), "retain future prepared work that must not be returned early")
			futureLoop := callerSendWakeIndexSession(t, ctx, tx, fixture.org)
			futureLoop.owner, futureLoop.pass, futureLoop.at = uuid.New(), uuid.New(), future.at
			require.NoError(t, callerSendSchemaInsertOwner(ctx, tx, owner, futureLoop, callerSendSchemaShape(owner, futureLoop, "pending")), "retain future startup work that must not be returned early")
			foreign := callerSendSchemaSeed(t, ctx, tx)
			foreign.at = base.Add(-24 * time.Hour)
			require.NoError(t, callerSendSchemaInsertMap(ctx, tx, "review_loop_start_intents", callerSendSchemaStartRow(foreign, "manual", uuid.Nil)), "insert earlier foreign prepared work that must remain outside this tenant")
			foreignLoop := foreign
			foreignLoop.owner, foreignLoop.pass = uuid.New(), uuid.New()
			require.NoError(t, callerSendSchemaInsertOwner(ctx, tx, owner, foreignLoop, callerSendSchemaShape(owner, foreignLoop, "pending")), "insert earlier foreign startup work that must remain outside this tenant")
			capture := &callerSendWakeCandidateCapture{Tx: tx}
			actual, err := ListDueReviewLoopStartsInTx(ctx, fixture.org, capture, tt.limit)
			require.NoError(t, err, "run the production selector against real fully constrained owners")
			sort.Slice(expected, func(i, j int) bool {
				if expected[i].due.Equal(expected[j].due) {
					return expected[i].candidate.ID.String() < expected[j].candidate.ID.String()
				}
				return expected[i].due.Before(expected[j].due)
			})
			wanted := make([]ReviewLoopStartCandidate, len(expected))
			for n, row := range expected {
				wanted[n] = row.candidate
			}
			require.Equal(t, wanted, actual, "each category must make bounded progress in due/id order while excluding future and foreign owners")
			for _, field := range []string{"envelope", "startup_input", "dispatch_wake", "startup_send_input"} {
				require.NotContains(t, strings.ToLower(capture.query), field, "candidate SQL must not read or serialize frozen payload columns")
			}
			var plan json.RawMessage
			require.NoError(t, tx.QueryRow(ctx, "EXPLAIN (VERBOSE,FORMAT JSON) "+capture.query, capture.args...).Scan(&plan), "inspect the exact production candidate projection and bounds")
			for _, field := range []string{"envelope", "startup_input", "dispatch_wake", "startup_send_input"} {
				require.NotContains(t, strings.ToLower(string(plan)), field, "verbose executor expressions must contain only scoped identity/due metadata")
			}
		})
	}
}

func callerSendWakeIndexSession(t *testing.T, ctx context.Context, tx pgx.Tx, orgID uuid.UUID) callerSendSchemaFixture {
	t.Helper()
	session := models.Session{OrgID: orgID, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusRunning, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
	require.NoError(t, NewSessionStore(tx).CreateInTx(ctx, tx, &session), "create a separate real same-tenant session for a legal concurrently running startup")
	require.NotNil(t, session.PrimaryThreadID, "the actual session writer must supply the bound primary thread")
	return callerSendSchemaFixture{org: orgID, session: session.ID, thread: *session.PrimaryThreadID}
}
