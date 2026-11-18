package lifecyclecensus

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/stretchr/testify/require"
)

func censusPtr[T any](value T) *T { return &value }

func TestParseLabel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, label string
		role        ObservedRole
		build       string
	}{
		{name: "api development", label: "143:api:dev", role: RoleAPI, build: "dev"},
		{name: "worker full mixed-case SHA", label: "143:worker:0123456789aBcDeF0123456789aBcDeF01234567", role: RoleWorker, build: "0123456789aBcDeF0123456789aBcDeF01234567"},
		{name: "combined unknown build", label: "143:all:unknown", role: RoleAll, build: "unknown"},
		{name: "executor marked fingerprint", label: "143:session_executor:sha256-0123456789abcdef0123456789abcdef", role: RoleSessionExecutor, build: "sha256-0123456789abcdef0123456789abcdef"},
		{name: "operator development", label: "143:operator:dev", role: "operator", build: "dev"},
		{name: "operator exact SHA", label: "143:operator:0123456789aBcDeF0123456789aBcDeF01234567", role: "operator", build: "0123456789aBcDeF0123456789aBcDeF01234567"},
		{name: "operator unknown build", label: "143:operator:unknown", role: "operator", build: "unknown"},
		{name: "operator marked fingerprint", label: "143:operator:sha256-0123456789abcdef0123456789abcdef", role: "operator", build: "sha256-0123456789abcdef0123456789abcdef"},
		{name: "operator malformed build", label: "143:operator:private-build", role: RoleUnknown, build: "unknown"},
		{name: "operator extra segment", label: "143:operator:dev:private", role: RoleUnknown, build: "unknown"},
		{name: "operator spelling is exact", label: "143:Operator:dev", role: RoleUnknown, build: "unknown"},
		{name: "untagged", role: RoleUnknown, build: "unknown"},
		{name: "arbitrary secret", label: "postgres://synthetic:secret@synthetic-host/private", role: RoleUnknown, build: "unknown"},
		{name: "unsupported role", label: "143:future:dev", role: RoleUnknown, build: "unknown"},
		{name: "truncated raw SHA", label: "143:api:0123456789abcdef", role: RoleUnknown, build: "unknown"},
		{name: "unmarked digest", label: "143:api:0123456789abcdef0123456789abcdef", role: RoleUnknown, build: "unknown"},
		{name: "bad hex SHA", label: "143:api:" + strings.Repeat("g", 40), role: RoleUnknown, build: "unknown"},
		{name: "bad fingerprint", label: "143:api:sha256-" + strings.Repeat("z", 32), role: RoleUnknown, build: "unknown"},
		{name: "extra segment", label: "143:api:dev:private", role: RoleUnknown, build: "unknown"},
		{name: "control sequence", label: "143:api:dev\nprivate", role: RoleUnknown, build: "unknown"},
		{name: "prefix is exact", label: "other:api:dev", role: RoleUnknown, build: "unknown"},
		{name: "role is case sensitive", label: "143:API:dev", role: RoleUnknown, build: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			role, build := parseLabel(tt.label)
			require.Equal(t, tt.role, role, "only allowlisted runtime or operator connection labels may identify a configured role")
			require.Equal(t, tt.build, build, "arbitrary application metadata must collapse to unknown rather than escape into output")
		})
	}
}

func TestAssembleOperatorConnectionsAreNotWriterLeases(t *testing.T) {
	t.Parallel()
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name, label string
		expected    ConnectionGroup
	}{
		{name: "connection-only operator", label: "143:operator:dev", expected: ConnectionGroup{Role: "operator", Build: "dev", State: StateUnknown, Count: 1, UnknownBackendType: 1, UnknownTransactionDetails: 1}},
		{name: "malformed operator remains unknown", label: "143:operator:private", expected: ConnectionGroup{Role: RoleUnknown, Build: "unknown", State: StateUnknown, Count: 1, UnknownBackendType: 1, UnknownTransactionDetails: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			writers := db.LifecycleCensusWriterSnapshot{ObservedAt: stamp, Rows: []db.LifecycleCensusWriter{{Role: "operator", BuildSHA: "dev", Version: 0}}}
			connections := db.LifecycleCensusConnectionSnapshot{ObservedAt: stamp.Add(time.Second), Rows: []db.LifecycleCensusConnection{{ApplicationName: tt.label}}}
			report := assemble(writers, connections)
			require.Equal(t, WriterSummary{ObservedAt: stamp, CountsScope: CountsObserved, RowsObserved: 1, Groups: []WriterGroup{{Role: RoleUnknown, Build: "dev", Count: 1}}}, report.Writers, "even a supplied operator lease must remain an unknown writer role")
			require.Equal(t, ConnectionSummary{ObservedAt: stamp.Add(time.Second), Visibility: VisibilityUnknown, CountsScope: CountsObserved, RowsObserved: 1, Groups: []ConnectionGroup{tt.expected}}, report.Connections, "operator connection recognition must retain separate clocks and uncertainty without inventing a lease")
			encoded, err := json.Marshal(report)
			require.NoError(t, err, "the additive connection role must retain the versioned JSON shape")
			require.Equal(t, 1, report.SchemaVersion, "adding a known connection-role value must not change the existing report structure")
			repeated, err := json.Marshal(assemble(writers, connections))
			require.NoError(t, err, "the same observations must remain serializable")
			require.Equal(t, encoded, repeated, "operator summaries must remain deterministic for identical observations")
		})
	}
}

func TestAssembleTransactionUncertainty(t *testing.T) {
	t.Parallel()
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name       string
		row        db.LifecycleCensusConnection
		visibility *bool
		expected   ConnectionGroup
		wantVis    Visibility
	}{
		{name: "visible open transaction", row: db.LifecycleCensusConnection{ApplicationName: "143:worker:dev", BackendType: censusPtr("client backend"), State: censusPtr("idle in transaction"), XactStart: &stamp, DetailsVisible: true}, visibility: censusPtr(true), wantVis: VisibilityFull, expected: ConnectionGroup{Role: RoleWorker, Build: "dev", State: StateIdleInTransaction, Count: 1, ObservedOpenTransactions: 1, OldestObservedTransactionAt: &stamp}},
		{name: "masked transaction retained", row: db.LifecycleCensusConnection{ApplicationName: "143:worker:dev"}, visibility: censusPtr(false), wantVis: VisibilityRestricted, expected: ConnectionGroup{Role: RoleWorker, Build: "dev", State: StateUnknown, Count: 1, UnknownBackendType: 1, UnknownTransactionDetails: 1}},
		{name: "null transaction is unknown even if idle", row: db.LifecycleCensusConnection{ApplicationName: "143:worker:dev", BackendType: censusPtr("client backend"), State: censusPtr("idle"), DetailsVisible: true}, visibility: censusPtr(true), wantVis: VisibilityFull, expected: ConnectionGroup{Role: RoleWorker, Build: "dev", State: StateIdle, Count: 1, UnknownTransactionDetails: 1}},
		{name: "disabled activity is not no transaction", row: db.LifecycleCensusConnection{ApplicationName: "143:worker:dev", BackendType: censusPtr("client backend"), State: censusPtr("disabled"), XactStart: &stamp, DetailsVisible: true}, visibility: censusPtr(true), wantVis: VisibilityFull, expected: ConnectionGroup{Role: RoleWorker, Build: "dev", State: StateDisabled, Count: 1, UnknownTransactionDetails: 1}},
		{name: "future state never leaks", row: db.LifecycleCensusConnection{ApplicationName: "secret", BackendType: censusPtr("client backend"), State: censusPtr("secret"), XactStart: &stamp, DetailsVisible: true}, wantVis: VisibilityUnknown, expected: ConnectionGroup{Role: RoleUnknown, Build: "unknown", State: StateUnknown, Count: 1, UnknownTransactionDetails: 1}},
		{name: "visibility masks supplied details defensively", row: db.LifecycleCensusConnection{ApplicationName: "143:worker:dev", BackendType: censusPtr("client backend"), State: censusPtr("active"), XactStart: &stamp}, visibility: censusPtr(false), wantVis: VisibilityRestricted, expected: ConnectionGroup{Role: RoleWorker, Build: "dev", State: StateUnknown, Count: 1, UnknownTransactionDetails: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			report := assemble(db.LifecycleCensusWriterSnapshot{ObservedAt: stamp}, db.LifecycleCensusConnectionSnapshot{ObservedAt: stamp, FullVisibility: tt.visibility, Rows: []db.LifecycleCensusConnection{tt.row}})
			require.Equal(t, ConnectionSummary{ObservedAt: stamp, Visibility: tt.wantVis, CountsScope: CountsObserved, RowsObserved: 1, Groups: []ConnectionGroup{tt.expected}}, report.Connections, "visible, hidden, disabled and null details must retain distinct observational meaning")
		})
	}
}

func TestAssembleSortedPrivateAggregates(t *testing.T) {
	t.Parallel()
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("synthetic", 3600))
	older := stamp.Add(-time.Minute).UTC()
	newer := stamp.UTC()
	secret := "postgres://synthetic:secret@synthetic-host/private\n"
	// Fingerprinting arbitrary builds reuses the existing runtime policy.
	fingerprint := strings.TrimPrefix(db.LifecycleConnectionApplicationName("api", secret), "143:api:")
	writers := db.LifecycleCensusWriterSnapshot{ObservedAt: stamp, Truncated: true, Rows: []db.LifecycleCensusWriter{
		{Role: "worker", BuildSHA: "dev", Version: 0}, {Role: "worker", BuildSHA: "dev", Version: 0},
		{Role: "api", BuildSHA: secret, Version: 7}, {Role: secret, BuildSHA: "", Version: 0},
	}}
	connections := db.LifecycleCensusConnectionSnapshot{ObservedAt: stamp.Add(time.Second), FullVisibility: censusPtr(true), Truncated: true, Rows: []db.LifecycleCensusConnection{
		{ApplicationName: "143:worker:dev", BackendType: censusPtr("client backend"), State: censusPtr("active"), XactStart: &newer, DetailsVisible: true},
		{ApplicationName: "143:worker:dev", BackendType: censusPtr("client backend"), State: censusPtr("active"), XactStart: &older, DetailsVisible: true},
		{ApplicationName: secret, State: censusPtr(secret)},
	}}
	report := assemble(writers, connections)
	require.Equal(t, 1, report.SchemaVersion, "report must have a stable explicit schema version")
	require.Equal(t, WriterSummary{ObservedAt: stamp.UTC(), Truncated: true, CountsScope: CountsSample, RowsObserved: 4, Groups: []WriterGroup{{Role: RoleAPI, Build: fingerprint, Version: 7, Count: 1}, {Role: RoleUnknown, Build: "unknown", Version: 0, Count: 1}, {Role: RoleWorker, Build: "dev", Version: 0, Count: 2}}}, report.Writers, "writer groups must be sorted, sampled and sanitized without interpreting future versions")
	require.Equal(t, ConnectionSummary{ObservedAt: stamp.Add(time.Second).UTC(), Visibility: VisibilityFull, Truncated: true, CountsScope: CountsSample, RowsObserved: 3, Groups: []ConnectionGroup{{Role: RoleUnknown, Build: "unknown", State: StateUnknown, Count: 1, UnknownBackendType: 1, UnknownTransactionDetails: 1}, {Role: RoleWorker, Build: "dev", State: StateActive, Count: 2, ObservedOpenTransactions: 2, OldestObservedTransactionAt: &older}}}, report.Connections, "connection aggregates must have exact sampled counts and oldest observed transaction, never raw identifiers")
	encoded, err := json.Marshal(report)
	require.NoError(t, err, "allowlisted aggregate report should encode")
	require.NotContains(t, string(encoded), secret, "arbitrary metadata must not be serialized")
	require.NotContains(t, string(encoded), "synthetic-host", "neither raw build metadata nor connection labels may disclose hosts")
	require.Equal(t, []string{
		"passive partial observations; not a compatibility, liveness, drain or rollout verdict",
		"writer leases and activity have separate statement clocks; not an atomic snapshot or instance-to-backend join",
		"role/build labels are self-reported and spoofable; transaction-pooling proxies may not preserve them",
		"current database only; collector excluded; masked backend types are retained as unknown",
		"null, disabled or hidden activity/transaction details remain unknown, not evidence of no transaction",
		"at most 1000 rows per source; truncated counts describe only the retained sample, not source totals",
	}, report.Limitations, "every report must carry its non-authority and incompleteness contract")
}

func TestAssembleEmptyDoesNotClaimFloor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		full     *bool
		expected Visibility
	}{
		{name: "full privileges", full: censusPtr(true), expected: VisibilityFull},
		{name: "restricted privileges", full: censusPtr(false), expected: VisibilityRestricted},
		{name: "unknown privileges", expected: VisibilityUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			report := assemble(db.LifecycleCensusWriterSnapshot{}, db.LifecycleCensusConnectionSnapshot{FullVisibility: tt.full})
			require.Equal(t, WriterSummary{CountsScope: CountsObserved, Groups: []WriterGroup{}}, report.Writers, "no observed lease does not become compatibility evidence")
			require.Equal(t, ConnectionSummary{Visibility: tt.expected, CountsScope: CountsObserved, Groups: []ConnectionGroup{}}, report.Connections, "zero rows must retain visibility without inventing a floor-ready verdict")
		})
	}
}

func TestCensusEnumValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		validate func() error
		valid    bool
	}{}
	for _, value := range []ObservedRole{RoleUnknown, RoleAPI, RoleWorker, RoleAll, RoleSessionExecutor, "operator", "", "future"} {
		tests = append(tests, struct {
			name     string
			validate func() error
			valid    bool
		}{"role " + string(value), value.Validate, value != "" && value != "future"})
	}
	for _, value := range []Visibility{VisibilityFull, VisibilityRestricted, VisibilityUnknown, "", "future"} {
		tests = append(tests, struct {
			name     string
			validate func() error
			valid    bool
		}{"visibility " + string(value), value.Validate, value != "" && value != "future"})
	}
	for _, value := range []CountsScope{CountsObserved, CountsSample, "", "future"} {
		tests = append(tests, struct {
			name     string
			validate func() error
			valid    bool
		}{"scope " + string(value), value.Validate, value != "" && value != "future"})
	}
	for _, value := range []ActivityState{StateUnknown, StateDisabled, StateActive, StateIdle, StateIdleInTransaction, StateIdleInAbortedTransaction, StateFastpath, "", "future"} {
		tests = append(tests, struct {
			name     string
			validate func() error
			valid    bool
		}{"state " + string(value), value.Validate, value != "" && value != "future"})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.valid {
				require.NoError(t, tt.validate(), "documented report enum value must validate")
			} else {
				require.Error(t, tt.validate(), "unknown or unset enum must not pass validation")
			}
		})
	}
}
