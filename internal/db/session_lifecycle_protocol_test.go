package db

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// This independent golden catches changes to tenant/parent predicates, optional
// cutover handling, visibility filtering and statement/lock boundaries.
const lifecycleModeSQL = `SELECT s.org_id, s.id, s.lifecycle_version,
	c.org_id, c.session_id, c.version, c.state, c.legacy_input_cutoff_id,
	c.normalization, c.activated_at, c.minimum_writer_version, c.resumability_capability,
	s.agent_type, COALESCE((SELECT jsonb_agg(jsonb_build_object(
		'protocol_version', t.lifecycle_version, 'provider', t.agent_type) ORDER BY t.id)
		FROM session_threads t WHERE t.org_id = s.org_id AND t.session_id = s.id
		AND t.archived_at IS NULL), '[]'::jsonb)
	FROM sessions s LEFT JOIN session_lifecycle_cutovers c
	ON c.org_id = s.org_id AND c.session_id = s.id
	WHERE s.org_id = $1 AND s.id = $2`

func TestResolveLifecycleModeInTxQueryContract(t *testing.T) {
	t.Parallel()
	require.Equal(t, strings.Join(strings.Fields(lifecycleModeSQL), " "), strings.Join(strings.Fields(lifecycleModeObservationQuery), " "),
		"resolver must read the exact parent and optional cutover including hidden parents in a separate statement")
}

func TestResolveLifecycleModeInTxProvenanceMatrix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		prepare func(*lifecycleModeSnapshot)
		want    lifecycleModeObservation
	}{
		{name: "unstamped parent and absent cutover remain legacy", prepare: func(s *lifecycleModeSnapshot) { *s = legacyLifecycleModeSnapshot() }, want: lifecycleModeObservation{mode: lifecycleModeLegacy}},
		{name: "inert shadow without JSON remains legacy", prepare: func(s *lifecycleModeSnapshot) {
			makeInactiveLifecycleModeSnapshot(s, models.LifecycleCutoverStateShadow)
		}, want: lifecycleModeObservation{mode: lifecycleModeLegacy, cutoverVersion: 9}},
		{name: "inert shadow without a cutoff or minimum remains legacy", prepare: func(s *lifecycleModeSnapshot) {
			makeInactiveLifecycleModeSnapshot(s, models.LifecycleCutoverStateShadow)
			s.legacyInputCutoffID, s.minimumWriterVersion = nil, nil
		}, want: lifecycleModeObservation{mode: lifecycleModeLegacy, cutoverVersion: 9}},
		{name: "inert shadow with exact incomplete envelope remains legacy", prepare: func(s *lifecycleModeSnapshot) {
			makeInactiveLifecycleModeSnapshot(s, models.LifecycleCutoverStateShadow)
			s.normalization = incompleteLifecycleNormalization()
		}, want: lifecycleModeObservation{mode: lifecycleModeLegacy, cutoverVersion: 9}},
		{name: "normalizing requires a retained normalization envelope", prepare: func(s *lifecycleModeSnapshot) {
			makeInactiveLifecycleModeSnapshot(s, models.LifecycleCutoverStateNormalizing)
		}},
		{name: "normalizing with exact incomplete envelope stays pending", prepare: func(s *lifecycleModeSnapshot) {
			makeInactiveLifecycleModeSnapshot(s, models.LifecycleCutoverStateNormalizing)
			s.normalization = incompleteLifecycleNormalization()
		}, want: lifecycleModeObservation{mode: lifecycleModeNormalizing, cutoverVersion: 9}},
		{name: "normalizing cannot infer a missing cutoff", prepare: func(s *lifecycleModeSnapshot) {
			makeInactiveLifecycleModeSnapshot(s, models.LifecycleCutoverStateNormalizing)
			s.normalization, s.legacyInputCutoffID = incompleteLifecycleNormalization(), nil
		}},
		{name: "active provenance preserves independent CAS revision", want: lifecycleModeObservation{mode: lifecycleModeActive, protocolVersion: 1, cutoverVersion: 9}},
		{name: "unknown capability provider cannot establish active mode", prepare: func(s *lifecycleModeSnapshot) {
			s.resumabilityCapability = json.RawMessage(strings.Replace(string(s.resumabilityCapability), `"provider":"codex"`, `"provider":"synthetic"`, 1))
		}},
		{name: "actual parent provider must be covered", prepare: func(s *lifecycleModeSnapshot) { s.parentAgentType = models.AgentTypeClaudeCode }},
		{name: "invalid actual parent provider fails closed", prepare: func(s *lifecycleModeSnapshot) { s.parentAgentType = "synthetic" }},
		{name: "visible lane zero stamp cannot escape active parent", prepare: func(s *lifecycleModeSnapshot) {
			s.visibleLanes = json.RawMessage(`[{"protocol_version":0,"provider":"codex"}]`)
		}},
		{name: "visible lane future stamp cannot escape active parent", prepare: func(s *lifecycleModeSnapshot) {
			s.visibleLanes = json.RawMessage(`[{"protocol_version":2,"provider":"codex"}]`)
		}},
		{name: "visible lane provider must be covered", prepare: func(s *lifecycleModeSnapshot) {
			s.visibleLanes = json.RawMessage(`[{"protocol_version":1,"provider":"claude_code"}]`)
		}},
		{name: "visible lane provider must be supported", prepare: func(s *lifecycleModeSnapshot) {
			s.visibleLanes = json.RawMessage(`[{"protocol_version":1,"provider":"synthetic"}]`)
		}},
		{name: "malformed lane observation fails closed", prepare: func(s *lifecycleModeSnapshot) { s.visibleLanes = nil }},
		{name: "duplicate lane fields fail closed", prepare: func(s *lifecycleModeSnapshot) {
			s.visibleLanes = json.RawMessage(`[{"protocol_version":0,"protocol_version":1,"provider":"codex"}]`)
		}},
		{name: "active session-only scope infers no lane", prepare: func(s *lifecycleModeSnapshot) { s.visibleLanes = json.RawMessage(`[]`) }, want: lifecycleModeObservation{mode: lifecycleModeActive, protocolVersion: 1, cutoverVersion: 9}},
		{name: "inactive opaque providers do not change legacy policy", prepare: func(s *lifecycleModeSnapshot) {
			*s = legacyLifecycleModeSnapshot()
			s.parentAgentType, s.visibleLanes = "synthetic", json.RawMessage(`[{"protocol_version":0,"provider":"opaque"}]`)
		}, want: lifecycleModeObservation{mode: lifecycleModeLegacy}},
		{name: "positive lane with absent parent cutover is contradictory", prepare: func(s *lifecycleModeSnapshot) {
			*s = legacyLifecycleModeSnapshot()
			s.visibleLanes = json.RawMessage(`[{"protocol_version":1,"provider":"codex"}]`)
		}},
		{name: "positive lane under shadow parent is contradictory", prepare: func(s *lifecycleModeSnapshot) {
			makeInactiveLifecycleModeSnapshot(s, models.LifecycleCutoverStateShadow)
			s.visibleLanes = json.RawMessage(`[{"protocol_version":1,"provider":"codex"}]`)
		}},
		{name: "active zero CAS revision is distinct from protocol stamp", prepare: func(s *lifecycleModeSnapshot) { s.cutoverVersion = requestLifecycleScopePointer(int64(0)) }, want: lifecycleModeObservation{mode: lifecycleModeActive, protocolVersion: 1}},
		{name: "positive parent and missing cutover cannot downgrade", prepare: func(s *lifecycleModeSnapshot) { *s = legacyLifecycleModeSnapshot(); s.protocolVersion = 1 }},
		{name: "negative retained parent stamp cannot become legacy", prepare: func(s *lifecycleModeSnapshot) { *s = legacyLifecycleModeSnapshot(); s.protocolVersion = -1 }},
		{name: "future retained parent stamp is unsupported", prepare: func(s *lifecycleModeSnapshot) { s.protocolVersion = 2 }},
		{name: "positive parent and shadow are contradictory", prepare: func(s *lifecycleModeSnapshot) {
			makeInactiveLifecycleModeSnapshot(s, models.LifecycleCutoverStateShadow)
			s.protocolVersion = 1
		}},
		{name: "positive parent and normalizing are contradictory", prepare: func(s *lifecycleModeSnapshot) {
			makeInactiveLifecycleModeSnapshot(s, models.LifecycleCutoverStateNormalizing)
			s.protocolVersion = 1
		}},
		{name: "zero parent and active are contradictory", prepare: func(s *lifecycleModeSnapshot) { s.protocolVersion = 0 }},
		{name: "partial cutover scope is not absence", prepare: func(s *lifecycleModeSnapshot) { s.cutoverOrgID = nil }},
		{name: "partial cutover revision is not absence", prepare: func(s *lifecycleModeSnapshot) { s.cutoverVersion = nil }},
		{name: "partial cutover state is not absence", prepare: func(s *lifecycleModeSnapshot) { s.state = nil }},
		{name: "foreign cutover tenant is inconsistent", prepare: func(s *lifecycleModeSnapshot) { s.cutoverOrgID = requestLifecycleScopePointer(uuid.UUID{15: 99}) }},
		{name: "foreign cutover parent is inconsistent", prepare: func(s *lifecycleModeSnapshot) { s.cutoverSessionID = requestLifecycleScopePointer(uuid.UUID{15: 99}) }},
		{name: "negative cutover revision is invalid", prepare: func(s *lifecycleModeSnapshot) { s.cutoverVersion = requestLifecycleScopePointer(int64(-1)) }},
		{name: "unknown cutover state is unsupported", prepare: func(s *lifecycleModeSnapshot) {
			s.state = requestLifecycleScopePointer(models.LifecycleCutoverState("future-sensitive-state"))
		}},
		{name: "active missing cutoff is incomplete", prepare: func(s *lifecycleModeSnapshot) { s.legacyInputCutoffID = nil }},
		{name: "active zero cutoff is invalid", prepare: func(s *lifecycleModeSnapshot) { s.legacyInputCutoffID = requestLifecycleScopePointer(int64(0)) }},
		{name: "active negative cutoff is invalid", prepare: func(s *lifecycleModeSnapshot) { s.legacyInputCutoffID = requestLifecycleScopePointer(int64(-1)) }},
		{name: "active missing activation instant is incomplete", prepare: func(s *lifecycleModeSnapshot) { s.activatedAt = nil }},
		{name: "active zero activation instant is incomplete", prepare: func(s *lifecycleModeSnapshot) { s.activatedAt = requestLifecycleScopePointer(time.Time{}) }},
		{name: "active missing minimum writer is incomplete", prepare: func(s *lifecycleModeSnapshot) { s.minimumWriterVersion = nil }},
		{name: "active zero minimum writer is incompatible", prepare: func(s *lifecycleModeSnapshot) { s.minimumWriterVersion = requestLifecycleScopePointer(0) }},
		{name: "active negative minimum writer is incompatible", prepare: func(s *lifecycleModeSnapshot) { s.minimumWriterVersion = requestLifecycleScopePointer(-1) }},
		{name: "active future minimum writer is incompatible", prepare: func(s *lifecycleModeSnapshot) { s.minimumWriterVersion = requestLifecycleScopePointer(2) }},
		{name: "shadow activation instant implies activation", prepare: func(s *lifecycleModeSnapshot) {
			at := *s.activatedAt
			makeInactiveLifecycleModeSnapshot(s, models.LifecycleCutoverStateShadow)
			s.activatedAt = &at
		}},
		{name: "shadow positive minimum writer implies activation", prepare: func(s *lifecycleModeSnapshot) {
			makeInactiveLifecycleModeSnapshot(s, models.LifecycleCutoverStateShadow)
			s.minimumWriterVersion = requestLifecycleScopePointer(1)
		}},
		{name: "shadow retained capability implies activation", prepare: func(s *lifecycleModeSnapshot) {
			capability := s.resumabilityCapability
			makeInactiveLifecycleModeSnapshot(s, models.LifecycleCutoverStateShadow)
			s.resumabilityCapability = capability
		}},
		{name: "shadow complete normalization implies activation", prepare: func(s *lifecycleModeSnapshot) {
			normalization := s.normalization
			makeInactiveLifecycleModeSnapshot(s, models.LifecycleCutoverStateShadow)
			s.normalization = normalization
		}},
		{name: "shadow arbitrary JSON cannot grant legacy", prepare: func(s *lifecycleModeSnapshot) {
			makeInactiveLifecycleModeSnapshot(s, models.LifecycleCutoverStateShadow)
			s.normalization = json.RawMessage(`{"future":"sensitive-payload-value"}`)
		}},
		{name: "shadow JSON null is not SQL absence", prepare: func(s *lifecycleModeSnapshot) {
			makeInactiveLifecycleModeSnapshot(s, models.LifecycleCutoverStateShadow)
			s.normalization = json.RawMessage(`null`)
		}},
		{name: "normalizing cannot retain active capability", prepare: func(s *lifecycleModeSnapshot) {
			capability := s.resumabilityCapability
			makeInactiveLifecycleModeSnapshot(s, models.LifecycleCutoverStateNormalizing)
			s.resumabilityCapability = capability
		}},
		{name: "absent cutover cannot hide residual metadata", prepare: func(s *lifecycleModeSnapshot) {
			*s = legacyLifecycleModeSnapshot()
			s.normalization = json.RawMessage(`{}`)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			snapshot := activeLifecycleModeSnapshot()
			if tt.prepare != nil {
				tt.prepare(&snapshot)
			}
			expectLifecycleModeRead(mock, snapshot)
			got, err := resolveLifecycleModeInTx(ctx, mock, snapshot.orgID, snapshot.sessionID)
			if tt.want.mode == 0 {
				requireLifecycleProvenanceError(t, err, snapshot)
			} else {
				require.NoError(t, err, "only a supported consistent retained protocol snapshot should resolve")
			}
			require.Equal(t, tt.want, got, "resolver must return the complete typed mode or zero on every failure")
			require.NoError(t, mock.ExpectationsWereMet(), "mode resolution must perform only policy read shared interlock and fresh exact parent lookup")
		})
	}
}

func TestResolveLifecycleModeInTxStrictActiveEnvelopes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, field string
		transform   func(string) string
	}{
		{name: "SQL missing normalization", field: "normalization", transform: func(string) string { return "" }},
		{name: "normalization JSON null", field: "normalization", transform: func(string) string { return "null" }},
		{name: "normalization array", field: "normalization", transform: func(string) string { return "[]" }},
		{name: "normalization malformed JSON", field: "normalization", transform: func(raw string) string { return raw[:len(raw)-1] }},
		{name: "normalization future schema", field: "normalization", transform: func(raw string) string { return strings.Replace(raw, `"schema_version":1`, `"schema_version":2`, 1) }},
		{name: "normalization missing field", field: "normalization", transform: func(raw string) string { return strings.Replace(raw, `"complete":true`, `"future":true`, 1) }},
		{name: "normalization extra field", field: "normalization", transform: func(raw string) string {
			return strings.Replace(raw, `"complete":true`, `"complete":true,"future":true`, 1)
		}},
		{name: "normalization case folded field", field: "normalization", transform: func(raw string) string { return strings.Replace(raw, `"complete"`, `"Complete"`, 1) }},
		{name: "normalization duplicate field", field: "normalization", transform: func(raw string) string {
			return strings.Replace(raw, `"complete":true`, `"complete":false,"complete":true`, 1)
		}},
		{name: "normalization null field", field: "normalization", transform: func(raw string) string { return strings.Replace(raw, `"complete":true`, `"complete":null`, 1) }},
		{name: "normalization false completion", field: "normalization", transform: func(raw string) string { return strings.Replace(raw, `"complete":true`, `"complete":false`, 1) }},
		{name: "normalization wrong cutoff", field: "normalization", transform: func(raw string) string { return strings.Replace(raw, `"cutoff_id":1`, `"cutoff_id":2`, 1) }},
		{name: "normalization numeric string cutoff", field: "normalization", transform: func(raw string) string { return strings.Replace(raw, `"cutoff_id":1`, `"cutoff_id":"1"`, 1) }},
		{name: "normalization missing receipt", field: "normalization", transform: func(raw string) string {
			return strings.Replace(raw, `"receipt_id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"`, `"receipt_id":"00000000-0000-0000-0000-000000000000"`, 1)
		}},
		{name: "normalization malformed floor receipt", field: "normalization", transform: func(raw string) string {
			return strings.Replace(raw, `"floor_receipt_id":"ffffffff-1111-4222-8333-444444444444"`, `"floor_receipt_id":"sensitive-payload-value"`, 1)
		}},
		{name: "normalization trailing document", field: "normalization", transform: func(raw string) string { return raw + `{}` }},
		{name: "SQL missing capability", field: "capability", transform: func(string) string { return "" }},
		{name: "capability JSON null", field: "capability", transform: func(string) string { return "null" }},
		{name: "capability future schema", field: "capability", transform: func(raw string) string { return strings.Replace(raw, `"schema_version":1`, `"schema_version":2`, 1) }},
		{name: "capability zero release receipt", field: "capability", transform: func(raw string) string {
			return strings.Replace(raw, `"release_receipt_id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"`, `"release_receipt_id":"00000000-0000-0000-0000-000000000000"`, 1)
		}},
		{name: "capability empty tuples", field: "capability", transform: func(raw string) string { return raw[:strings.Index(raw, `"tuples":`)] + `"tuples":[]}` }},
		{name: "capability null tuples", field: "capability", transform: func(raw string) string { return raw[:strings.Index(raw, `"tuples":`)] + `"tuples":null}` }},
		{name: "capability null tuple", field: "capability", transform: func(raw string) string { return raw[:strings.Index(raw, `"tuples":`)] + `"tuples":[null]}` }},
		{name: "capability tuple missing image", field: "capability", transform: func(raw string) string { return strings.Replace(raw, `"image_build":"synthetic-image-v1",`, "", 1) }},
		{name: "capability tuple future field", field: "capability", transform: func(raw string) string {
			return strings.Replace(raw, `"provider":"codex"`, `"provider":"codex","future":true`, 1)
		}},
		{name: "capability tuple duplicate field", field: "capability", transform: func(raw string) string {
			return strings.Replace(raw, `"provider":"codex"`, `"provider":"unknown","provider":"codex"`, 1)
		}},
		{name: "capability tuple unknown resume kind", field: "capability", transform: func(raw string) string {
			return strings.Replace(raw, `"resume_identity":"native_conversation"`, `"resume_identity":"future-sensitive-kind"`, 1)
		}},
		{name: "capability tuple empty provider", field: "capability", transform: func(raw string) string { return strings.Replace(raw, `"provider":"codex"`, `"provider":""`, 1) }},
		{name: "capability tuple wildcard build", field: "capability", transform: func(raw string) string {
			return strings.Replace(raw, `"cli_build":"synthetic-cli-v1"`, `"cli_build":"synthetic-*"`, 1)
		}},
		{name: "capability tuple whitespace image", field: "capability", transform: func(raw string) string {
			return strings.Replace(raw, `"image_build":"synthetic-image-v1"`, `"image_build":" synthetic-image-v1"`, 1)
		}},
		{name: "capability tuple control protocol", field: "capability", transform: func(raw string) string {
			return strings.Replace(raw, `"verification_protocol":"synthetic-verify-v1"`, `"verification_protocol":"synthetic\nverify"`, 1)
		}},
		{name: "capability tuple zero conformance receipt", field: "capability", transform: func(raw string) string {
			return strings.Replace(raw, `"conformance_receipt_id":"ffffffff-1111-4222-8333-444444444444"`, `"conformance_receipt_id":"00000000-0000-0000-0000-000000000000"`, 1)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			snapshot := activeLifecycleModeSnapshot()
			if tt.field == "normalization" {
				snapshot.normalization = json.RawMessage(tt.transform(string(snapshot.normalization)))
			} else {
				snapshot.resumabilityCapability = json.RawMessage(tt.transform(string(snapshot.resumabilityCapability)))
			}
			expectLifecycleModeRead(mock, snapshot)
			got, err := resolveLifecycleModeInTx(ctx, mock, snapshot.orgID, snapshot.sessionID)
			requireLifecycleProvenanceError(t, err, snapshot)
			require.Equal(t, lifecycleModeObservation{}, got, "unsupported or ambiguous provenance must never expose even a partial active mode")
			require.NoError(t, mock.ExpectationsWereMet(), "invalid persisted envelopes must be rejected after the exact fresh lookup without writes or settlement")
		})
	}
}

func TestResolveLifecycleModeInTxKnownResumeKinds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		kind ResumeIdentityKind
	}{
		{name: "native conversation", kind: ResumeNativeConversation},
		{name: "workspace continuation", kind: ResumeWorkspaceContinuation},
		{name: "initial turn", kind: ResumeInitialTurn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			snapshot := activeLifecycleModeSnapshot()
			snapshot.resumabilityCapability = json.RawMessage(strings.Replace(string(snapshot.resumabilityCapability), string(ResumeNativeConversation), string(tt.kind), 1))
			expectLifecycleModeRead(mock, snapshot)
			got, err := resolveLifecycleModeInTx(ctx, mock, snapshot.orgID, snapshot.sessionID)
			require.NoError(t, err, "a known exact resume variant should pass shape validation without claiming provider conformance")
			require.Equal(t, lifecycleModeObservation{mode: lifecycleModeActive, protocolVersion: 1, cutoverVersion: 9}, got,
				"all supported resume identities should preserve the same retained mode and distinct revision")
			require.NoError(t, mock.ExpectationsWereMet(), "provider tuple validation must not call providers or install protocol settings")
		})
	}
}

func TestResolveLifecycleModeInTxRejectsInvalidInputs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, wantText string
		wantErr        error
	}{
		{name: "nil context", wantText: "nil context"},
		{name: "nil transaction", wantText: "nil transaction"},
		{name: "typed nil transaction", wantText: "nil transaction"},
		{name: "cancelled context", wantErr: context.Canceled},
		{name: "expired context", wantErr: context.DeadlineExceeded},
		{name: "missing deadline", wantText: "deadline"},
		{name: "overlong deadline", wantText: "deadline"},
		{name: "zero organization", wantText: "organization"},
		{name: "zero session", wantText: "session"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			snapshot := activeLifecycleModeSnapshot()
			var tx pgx.Tx = mock
			switch tt.name {
			case "nil context":
				ctx = nil
			case "nil transaction":
				tx = nil
			case "typed nil transaction":
				var typedNil *pgxpool.Tx
				tx = typedNil
			case "cancelled context":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			case "expired context":
				expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				t.Cleanup(cancel)
				ctx = expired
			case "missing deadline":
				ctx = context.Background()
			case "overlong deadline":
				overlong, cancel := context.WithTimeout(context.Background(), 10*LifecycleTransactionTimeout)
				t.Cleanup(cancel)
				ctx = overlong
			case "zero organization":
				snapshot.orgID = uuid.Nil
			case "zero session":
				snapshot.sessionID = uuid.Nil
			}
			got, err := resolveLifecycleModeInTx(ctx, tx, snapshot.orgID, snapshot.sessionID)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr, "invalid unit context must preserve its cancellation cause")
			} else {
				require.ErrorContains(t, err, tt.wantText, "invalid resolver input must fail before any SQL")
			}
			require.Equal(t, lifecycleModeObservation{}, got, "invalid inputs must never expose a mode")
			require.NoError(t, mock.ExpectationsWereMet(), "input rejection must not install settings acquire locks or settle the caller transaction")
		})
	}
}

func TestResolveLifecycleModeInTxRequiresExistingPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		policy []any
	}{
		{name: "repeatable read", policy: []any{"repeatable read", "1", "250ms", "1500ms"}},
		{name: "serializable", policy: []any{"serializable", "1", "250ms", "1500ms"}},
		{name: "missing writer marker", policy: []any{"read committed", "", "250ms", "1500ms"}},
		{name: "older writer marker", policy: []any{"read committed", "0", "250ms", "1500ms"}},
		{name: "wrong lock timeout", policy: []any{"read committed", "1", "0", "1500ms"}},
		{name: "wrong statement timeout", policy: []any{"read committed", "1", "250ms", "0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			snapshot := activeLifecycleModeSnapshot()
			mock.ExpectQuery(lifecycleTransactionPolicyQuery).WillReturnRows(lifecycleScopePolicyRows().AddRow(tt.policy...))
			got, err := resolveLifecycleModeInTx(ctx, mock, snapshot.orgID, snapshot.sessionID)
			require.Error(t, err, "resolver requires the caller's existing exact READ COMMITTED transaction policy")
			require.Equal(t, lifecycleModeObservation{}, got, "unverified transaction policy must return no mode")
			require.NoError(t, mock.ExpectationsWereMet(), "policy rejection must occur before interlock acquisition or parent lookup")
		})
	}
}

func TestResolveLifecycleModeInTxAcquisitionFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{name: "actual busy cause"},
		{name: "query failure"},
		{name: "missing lock row"},
		{name: "lock scan failure"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			fixture := newCutoverUnitFixture()
			expectLifecycleScopePolicy(mock)
			query := mock.ExpectQuery(cutoverSharedSQL).WithArgs(fixture.key)
			switch tt.name {
			case "actual busy cause":
				query.WillReturnRows(cutoverLockRows().AddRow(false))
			case "query failure":
				query.WillReturnError(io.ErrUnexpectedEOF)
			case "missing lock row":
				query.WillReturnRows(cutoverLockRows())
			case "lock scan failure":
				query.WillReturnRows(cutoverLockRows().AddRow("not-a-bool"))
			}
			got, err := resolveLifecycleModeInTx(ctx, mock, fixture.orgID, fixture.sessionID)
			require.Error(t, err, "failed interlock acquisition must prevent all mode observations")
			switch tt.name {
			case "actual busy cause":
				requireCutoverPrivateBusy(t, err)
			case "query failure":
				require.ErrorIs(t, err, io.ErrUnexpectedEOF, "resolver must preserve the original interlock query cause")
			case "missing lock row":
				require.ErrorIs(t, err, pgx.ErrNoRows, "missing acquisition output must remain a database error")
			}
			require.NotErrorIs(t, err, ErrLifecycleGuardProvenanceRequired, "acquisition failures are not manufactured provenance failures")
			require.Equal(t, lifecycleModeObservation{}, got, "failed acquisition must return the zero observation")
			require.NoError(t, mock.ExpectationsWereMet(), "failed shared acquisition must not read metadata retry or settle the transaction")
		})
	}
}

func TestResolveLifecycleModeInTxDiscardsFailedSnapshots(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		wantErr error
	}{
		{name: "missing parent", wantErr: pgx.ErrNoRows},
		{name: "query failure", wantErr: io.ErrUnexpectedEOF},
		{name: "parent stamp scan failure"},
		{name: "last capability scan failure"},
		{name: "foreign parent tenant", wantErr: pgx.ErrNoRows},
		{name: "foreign parent session", wantErr: pgx.ErrNoRows},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, ctx := newLifecycleScopeMock(t)
			snapshot := activeLifecycleModeSnapshot()
			expectCutoverSharedAcquisition(mock, newCutoverUnitFixture())
			query := mock.ExpectQuery(lifecycleModeSQL).WithArgs(snapshot.orgID, snapshot.sessionID)
			values := lifecycleModeSnapshotValues(snapshot)
			switch tt.name {
			case "missing parent":
				query.WillReturnRows(lifecycleModeSnapshotRows())
			case "query failure":
				query.WillReturnError(io.ErrUnexpectedEOF)
			default:
				switch tt.name {
				case "parent stamp scan failure":
					values[2] = "not-a-stamp"
				case "last capability scan failure":
					values[11] = struct{}{}
				case "foreign parent tenant":
					values[0] = uuid.UUID{15: 99}
				case "foreign parent session":
					values[1] = uuid.UUID{15: 99}
				}
				query.WillReturnRows(lifecycleModeSnapshotRows().AddRow(values...))
			}
			got, err := resolveLifecycleModeInTx(ctx, mock, snapshot.orgID, snapshot.sessionID)
			require.Error(t, err, "failed exact parent lookup must return no usable mode")
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr, "late lookup errors and scope mismatch must preserve their database cause")
			}
			require.NotErrorIs(t, err, ErrLifecycleGuardProvenanceRequired, "a failed parent read cannot invent retained active provenance")
			require.Equal(t, lifecycleModeObservation{}, got, "all partially scanned snapshots must be discarded on error")
			require.NoError(t, mock.ExpectationsWereMet(), "failed fresh lookup must not issue any transaction effects or retries")
		})
	}
}

func TestResolveLifecycleModeInTxCancellationAfterScan(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, query string
	}{
		{name: "policy scan cancellation", query: lifecycleTransactionPolicyQuery},
		{name: "shared acquisition scan cancellation", query: lifecycleCutoverSharedLockQuery},
		{name: "complete mode scan cancellation", query: lifecycleModeObservationQuery},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, budgetCtx := newLifecycleScopeMock(t)
			ctx, cancel := context.WithCancel(budgetCtx)
			t.Cleanup(cancel)
			snapshot := activeLifecycleModeSnapshot()
			expectLifecycleScopePolicy(mock)
			if tt.query != lifecycleTransactionPolicyQuery {
				mock.ExpectQuery(cutoverSharedSQL).WithArgs(newCutoverUnitFixture().key).WillReturnRows(cutoverLockRows().AddRow(true))
			}
			if tt.query == lifecycleModeObservationQuery {
				mock.ExpectQuery(lifecycleModeSQL).WithArgs(snapshot.orgID, snapshot.sessionID).WillReturnRows(lifecycleModeSnapshotRows(snapshot))
			}
			tx := &lifecycleScopeCancellingTx{Tx: mock, query: tt.query, cancel: cancel}
			got, err := resolveLifecycleModeInTx(ctx, tx, snapshot.orgID, snapshot.sessionID)
			require.ErrorIs(t, err, context.Canceled, "cancellation after a successful scan must override any mode result")
			require.Equal(t, lifecycleModeObservation{}, got, "late cancellation must discard the complete observed metadata")
			require.NoError(t, mock.ExpectationsWereMet(), "cancellation must prevent subsequent SQL without retry or transaction settlement")
		})
	}
}

func activeLifecycleModeSnapshot() lifecycleModeSnapshot {
	fixture := newCutoverUnitFixture()
	return lifecycleModeSnapshot{
		orgID: fixture.orgID, sessionID: fixture.sessionID, protocolVersion: 1,
		parentAgentType: models.AgentTypeCodex, visibleLanes: json.RawMessage(`[{"protocol_version":1,"provider":"codex"}]`),
		cutoverOrgID: requestLifecycleScopePointer(fixture.orgID), cutoverSessionID: requestLifecycleScopePointer(fixture.sessionID),
		cutoverVersion: requestLifecycleScopePointer(int64(9)), state: requestLifecycleScopePointer(models.LifecycleCutoverStateActive),
		legacyInputCutoffID: requestLifecycleScopePointer(int64(1)), activatedAt: requestLifecycleScopePointer(time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)), minimumWriterVersion: requestLifecycleScopePointer(1),
		normalization:          json.RawMessage(`{"schema_version":1,"receipt_id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","floor_receipt_id":"ffffffff-1111-4222-8333-444444444444","cutoff_id":1,"complete":true}`),
		resumabilityCapability: json.RawMessage(`{"schema_version":1,"release_receipt_id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","tuples":[{"provider":"codex","cli_build":"synthetic-cli-v1","image_build":"synthetic-image-v1","resume_identity":"native_conversation","verification_protocol":"synthetic-verify-v1","conformance_receipt_id":"ffffffff-1111-4222-8333-444444444444"}]}`),
	}
}

func legacyLifecycleModeSnapshot() lifecycleModeSnapshot {
	fixture := newCutoverUnitFixture()
	return lifecycleModeSnapshot{orgID: fixture.orgID, sessionID: fixture.sessionID, parentAgentType: models.AgentTypeCodex, visibleLanes: json.RawMessage(`[{"protocol_version":0,"provider":"codex"}]`)}
}

func makeInactiveLifecycleModeSnapshot(snapshot *lifecycleModeSnapshot, state models.LifecycleCutoverState) {
	snapshot.protocolVersion = 0
	snapshot.visibleLanes = json.RawMessage(`[{"protocol_version":0,"provider":"codex"}]`)
	snapshot.state = requestLifecycleScopePointer(state)
	snapshot.normalization = nil
	snapshot.activatedAt = nil
	snapshot.minimumWriterVersion = requestLifecycleScopePointer(0)
	snapshot.resumabilityCapability = nil
}

func incompleteLifecycleNormalization() json.RawMessage {
	return json.RawMessage(strings.Replace(string(activeLifecycleModeSnapshot().normalization), `"complete":true`, `"complete":false`, 1))
}

func expectLifecycleModeRead(mock pgxmock.PgxPoolIface, snapshot lifecycleModeSnapshot) {
	expectCutoverSharedAcquisition(mock, newCutoverUnitFixture())
	mock.ExpectQuery(lifecycleModeSQL).WithArgs(snapshot.orgID, snapshot.sessionID).WillReturnRows(lifecycleModeSnapshotRows(snapshot))
}

func lifecycleModeSnapshotRows(snapshots ...lifecycleModeSnapshot) *pgxmock.Rows {
	rows := pgxmock.NewRows([]string{"parent_org_id", "parent_id", "lifecycle_version", "cutover_org_id", "cutover_session_id", "version", "state", "legacy_input_cutoff_id", "normalization", "activated_at", "minimum_writer_version", "resumability_capability", "parent_agent_type", "visible_lanes"})
	for _, snapshot := range snapshots {
		rows.AddRow(lifecycleModeSnapshotValues(snapshot)...)
	}
	return rows
}

func lifecycleModeSnapshotValues(snapshot lifecycleModeSnapshot) []any {
	return []any{snapshot.orgID, snapshot.sessionID, snapshot.protocolVersion, snapshot.cutoverOrgID, snapshot.cutoverSessionID, snapshot.cutoverVersion,
		snapshot.state, snapshot.legacyInputCutoffID, snapshot.normalization, snapshot.activatedAt, snapshot.minimumWriterVersion, snapshot.resumabilityCapability, snapshot.parentAgentType, snapshot.visibleLanes}
}

func requireLifecycleProvenanceError(t *testing.T, err error, snapshot lifecycleModeSnapshot) {
	t.Helper()
	require.ErrorIs(t, err, ErrLifecycleGuardProvenanceRequired, "malformed missing inconsistent or unsupported retained provenance must fail closed")
	var provenanceErr *LifecycleGuardProvenanceError
	require.ErrorAs(t, err, &provenanceErr, "provenance rejection must preserve its explicit typed protocol error")
	require.NotNil(t, provenanceErr, "provenance rejection must retain a real typed cause")
	require.False(t, retryableLifecycleTransaction(err), "invalid provenance must not acquire contention retry semantics")
	require.NotErrorIs(t, err, pgx.ErrNoRows, "missing active cutover provenance must not masquerade as missing exact parent")
	for _, sensitive := range []string{snapshot.orgID.String(), snapshot.sessionID.String(), "sensitive-payload-value", "future-sensitive-state", "future-sensitive-kind"} {
		require.NotContains(t, err.Error(), sensitive, "provenance error text must not contain persisted values or tenant identifiers")
	}
}

func TestResolveLifecycleModeInTxMixedProviderEnvelope(t *testing.T) {
	t.Parallel()
	snapshot := activeLifecycleModeSnapshot()
	var envelope struct {
		SchemaVersion    int               `json:"schema_version"`
		ReleaseReceiptID uuid.UUID         `json:"release_receipt_id"`
		Tuples           []json.RawMessage `json:"tuples"`
	}
	require.NoError(t, json.Unmarshal(snapshot.resumabilityCapability, &envelope), "reuse the complete versioned synthetic capability envelope")
	additional := json.RawMessage(strings.Replace(string(envelope.Tuples[0]), `"provider":"codex"`, `"provider":"claude_code"`, 1))
	envelope.Tuples = append(envelope.Tuples, additional)
	var err error
	snapshot.resumabilityCapability, err = json.Marshal(envelope)
	require.NoError(t, err, "encode separate exact tested tuples for both actual providers")
	snapshot.visibleLanes = json.RawMessage(`[{"protocol_version":1,"provider":"codex"},{"protocol_version":1,"provider":"claude_code"}]`)
	actual, err := snapshot.resolve()
	require.NoError(t, err, "a covered mixed-provider visible cohort is mode-compatible without claiming real provider conformance")
	require.Equal(t, lifecycleModeObservation{mode: lifecycleModeActive, protocolVersion: 1, cutoverVersion: 9}, actual, "all supported tuple providers must be considered rather than selecting only the first")
}
