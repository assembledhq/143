package db

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestSessionHumanInputRequestStore_GetLifecycleReadiness(t *testing.T) {
	t.Parallel()

	const expectedSQL = `
		SELECT legacy_request, proof_policy_version, enforcement_version,
			classification_version, readiness_state, readiness_deadline_at, readiness_proof
		FROM session_human_input_requests
		WHERE org_id = $1 AND session_id = $2 AND id = $3`
	type testCase struct {
		name       string
		fixture    func() ([]any, HumanInputReadinessMetadata)
		missing    bool
		queryError bool
		scanError  bool
	}
	tests := []testCase{
		{
			name: "SQL null fields all remain unknown",
			fixture: func() ([]any, HumanInputReadinessMetadata) {
				return []any{nil, nil, nil, nil, nil, nil, nil}, HumanInputReadinessMetadata{}
			},
		},
		{
			name: "explicit false does not imply versions or classification",
			fixture: func() ([]any, HumanInputReadinessMetadata) {
				legacy := false
				return []any{&legacy, nil, nil, nil, nil, nil, nil}, HumanInputReadinessMetadata{LegacyRequest: &legacy}
			},
		},
		{
			name: "proof policy zero remains present independently",
			fixture: func() ([]any, HumanInputReadinessMetadata) {
				version := int32(0)
				return []any{nil, &version, nil, nil, nil, nil, nil}, HumanInputReadinessMetadata{ProofPolicyVersion: &version}
			},
		},
		{
			name: "enforcement zero remains present independently",
			fixture: func() ([]any, HumanInputReadinessMetadata) {
				version := int32(0)
				return []any{nil, nil, &version, nil, nil, nil, nil}, HumanInputReadinessMetadata{EnforcementVersion: &version}
			},
		},
		{
			name: "classification zero remains present independently",
			fixture: func() ([]any, HumanInputReadinessMetadata) {
				version := int32(0)
				return []any{nil, nil, nil, &version, nil, nil, nil}, HumanInputReadinessMetadata{ClassificationVersion: &version}
			},
		},
		{
			name: "state can be present without versions proof or deadline",
			fixture: func() ([]any, HumanInputReadinessMetadata) {
				state := models.HumanInputReadinessStateReady
				return []any{nil, nil, nil, nil, &state, nil, nil}, HumanInputReadinessMetadata{ReadinessState: &state}
			},
		},
		{
			name: "deadline can be present without other metadata",
			fixture: func() ([]any, HumanInputReadinessMetadata) {
				deadline := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
				return []any{nil, nil, nil, nil, nil, &deadline, nil}, HumanInputReadinessMetadata{ReadinessDeadlineAt: &deadline}
			},
		},
		{
			name: "JSON null remains different from SQL null",
			fixture: func() ([]any, HumanInputReadinessMetadata) {
				return []any{nil, nil, nil, nil, nil, nil, json.RawMessage(`null`)}, HumanInputReadinessMetadata{ReadinessProof: json.RawMessage(`null`)}
			},
		},
		{
			name: "proof is returned without inferring a classification",
			fixture: func() ([]any, HumanInputReadinessMetadata) {
				return []any{nil, nil, nil, nil, nil, nil, json.RawMessage(`{"synthetic":true}`)}, HumanInputReadinessMetadata{ReadinessProof: json.RawMessage(`{"synthetic":true}`)}
			},
		},
		{name: "missing scoped request", missing: true},
		{name: "query failure preserves its cause", queryError: true},
		{
			name: "late scan failure discards every earlier populated field",
			fixture: func() ([]any, HumanInputReadinessMetadata) {
				row, _ := completeHumanInputReadinessFixture(models.HumanInputReadinessStateReady)
				row[6] = map[string]string{"invalid": "proof representation"}
				return row, HumanInputReadinessMetadata{}
			},
			scanError: true,
		},
	}
	states := []models.HumanInputReadinessState{
		models.HumanInputReadinessStateUnclassified,
		models.HumanInputReadinessStateReady,
		models.HumanInputReadinessStateWaiting,
		models.HumanInputReadinessStateRecoveryRequired,
		"future_state",
	}
	for _, state := range states {
		tests = append(tests, testCase{
			name: "all metadata with " + string(state),
			fixture: func() ([]any, HumanInputReadinessMetadata) {
				return completeHumanInputReadinessFixture(state)
			},
		})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
			require.NoError(t, err, "create an independent passive readiness read fixture")
			defer mock.Close()

			orgID, sessionID, requestID := uuid.New(), uuid.New(), uuid.New()
			databaseErr := errors.New("database unavailable")
			expectation := mock.ExpectQuery(expectedSQL).WithArgs(orgID, sessionID, requestID)
			want := HumanInputReadinessMetadata{}
			if tt.queryError {
				expectation.WillReturnError(databaseErr)
			} else {
				rows := pgxmock.NewRows([]string{"legacy_request", "proof_policy_version", "enforcement_version", "classification_version", "readiness_state", "readiness_deadline_at", "readiness_proof"})
				if !tt.missing {
					var row []any
					row, want = tt.fixture()
					rows.AddRow(row...)
				}
				expectation.WillReturnRows(rows)
			}

			actual, err := NewSessionHumanInputRequestStore(mock).GetLifecycleReadiness(context.Background(), orgID, sessionID, requestID)
			switch {
			case tt.missing:
				require.ErrorIs(t, err, pgx.ErrNoRows, "missing readiness metadata must preserve the scoped no-row error")
			case tt.queryError:
				require.ErrorIs(t, err, databaseErr, "passive readiness reads must preserve the database failure")
			case tt.scanError:
				require.Error(t, err, "invalid column data must fail the entire passive readiness read")
			default:
				require.NoError(t, err, "passive reads must return metadata without validating or inferring classifications")
			}
			require.Equal(t, want, actual, "passive reads must preserve exact independently nullable values or return an empty result on error")
			require.NoError(t, mock.ExpectationsWereMet(), "the sole query must enforce tenant session and request scope without locks inference or writes")
		})
	}
}

func completeHumanInputReadinessFixture(state models.HumanInputReadinessState) ([]any, HumanInputReadinessMetadata) {
	legacy := true
	policy, enforcement, classification := int32(math.MaxInt32), int32(math.MaxInt32-1), int32(math.MaxInt32-2)
	deadline := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	proof := json.RawMessage(`{"synthetic":true}`)
	return []any{&legacy, &policy, &enforcement, &classification, &state, &deadline, proof}, HumanInputReadinessMetadata{
		LegacyRequest: &legacy, ProofPolicyVersion: &policy, EnforcementVersion: &enforcement,
		ClassificationVersion: &classification, ReadinessState: &state, ReadinessDeadlineAt: &deadline, ReadinessProof: proof,
	}
}
