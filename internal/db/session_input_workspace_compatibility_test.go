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

func TestSessionHumanInputRequestStore_GetWorkspaceCompatibility(t *testing.T) {
	t.Parallel()
	const expectedSQL = `
		SELECT state, anchor_digest, context_scope_digest, workspace_generation,
			workspace_revision, snapshot_key, workspace_state, last_authority_kind,
			last_authority_receipt_id, reason, next_revalidation_at
		FROM session_input_workspace_compatibility
		WHERE org_id = $1 AND session_id = $2 AND request_id = $3`
	type testCase struct {
		name          string
		fixture       func() InputWorkspaceCompatibilityMetadata
		missing       bool
		queryError    bool
		scanError     bool
		invalidColumn int
	}
	tests := []testCase{
		{name: "SQL NULL fields all remain independently unknown", fixture: func() InputWorkspaceCompatibilityMetadata {
			return InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStateUnprovable}
		}},
		{name: "anchor digest remains independent", fixture: func() InputWorkspaceCompatibilityMetadata {
			digest := "synthetic-anchor-digest"
			return InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStateUnprovable, AnchorDigest: &digest}
		}},
		{name: "context scope digest remains independent", fixture: func() InputWorkspaceCompatibilityMetadata {
			digest := "synthetic-context-scope"
			return InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStatePending, ContextScopeDigest: &digest}
		}},
		{name: "generation zero remains present without a revision", fixture: func() InputWorkspaceCompatibilityMetadata {
			generation := int64(0)
			return InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStatePending, WorkspaceGeneration: &generation}
		}},
		{name: "revision zero remains present without a generation", fixture: func() InputWorkspaceCompatibilityMetadata {
			revision := int64(0)
			return InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStatePending, WorkspaceRevision: &revision}
		}},
		{name: "present empty snapshot key is not defaulted to NULL", fixture: func() InputWorkspaceCompatibilityMetadata {
			key := ""
			return InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStateUnavailable, SnapshotKey: &key}
		}},
		{name: "JSON null stays distinct from SQL NULL", fixture: func() InputWorkspaceCompatibilityMetadata {
			return InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStateUnprovable, WorkspaceState: json.RawMessage(`null`)}
		}},
		{name: "future opaque JSON arrays remain raw", fixture: func() InputWorkspaceCompatibilityMetadata {
			return InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStatePending, WorkspaceState: json.RawMessage(`["future",null,{"descriptor":3}]`)}
		}},
		{name: "opaque scalar JSON is not interpreted as proof", fixture: func() InputWorkspaceCompatibilityMetadata {
			return InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStateCertified, WorkspaceState: json.RawMessage(`0`)}
		}},
		{name: "future authority kind is returned without validation or receipt inference", fixture: func() InputWorkspaceCompatibilityMetadata {
			kind := "future-operation-kind"
			return InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStatePending, LastAuthorityKind: &kind}
		}},
		{name: "receipt remains independently present without kind", fixture: func() InputWorkspaceCompatibilityMetadata {
			receipt := uuid.New()
			return InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStateUnprovable, LastAuthorityReceiptID: &receipt}
		}},
		{name: "present zero receipt identity remains distinct from SQL NULL", fixture: func() InputWorkspaceCompatibilityMetadata {
			receipt := uuid.Nil
			return InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStateUnprovable, LastAuthorityReceiptID: &receipt}
		}},
		{name: "present empty reason stays distinct from absent reason", fixture: func() InputWorkspaceCompatibilityMetadata {
			reason := ""
			return InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStateUnavailable, Reason: &reason}
		}},
		{name: "past due revalidation is not hidden or interpreted", fixture: func() InputWorkspaceCompatibilityMetadata {
			due := time.Date(2000, time.January, 2, 3, 4, 5, 0, time.UTC)
			return InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStatePending, NextRevalidationAt: &due}
		}},
		{name: "maximum bigint watermarks are returned unchanged", fixture: func() InputWorkspaceCompatibilityMetadata {
			generation, revision := int64(math.MaxInt64), int64(math.MaxInt64-1)
			return InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStatePending, WorkspaceGeneration: &generation, WorkspaceRevision: &revision}
		}},
		{name: "minimum bigint values are raw observations not eligibility", fixture: func() InputWorkspaceCompatibilityMetadata {
			generation, revision := int64(math.MinInt64), int64(math.MinInt64+1)
			return InputWorkspaceCompatibilityMetadata{State: models.InputWorkspaceCompatibilityStateUnprovable, WorkspaceGeneration: &generation, WorkspaceRevision: &revision}
		}},
		{name: "missing scoped row is not inferred unprovable", missing: true},
		{name: "query failure preserves its cause", queryError: true},
		{name: "state scan error returns a completely zero result", fixture: func() InputWorkspaceCompatibilityMetadata {
			return completeInputWorkspaceCompatibilityFixture(models.InputWorkspaceCompatibilityStateCertified)
		}, scanError: true, invalidColumn: 0},
		{name: "late receipt scan error discards earlier populated fields", fixture: func() InputWorkspaceCompatibilityMetadata {
			return completeInputWorkspaceCompatibilityFixture(models.InputWorkspaceCompatibilityStateCertified)
		}, scanError: true, invalidColumn: 8},
		{name: "last field scan error discards every earlier populated field", fixture: func() InputWorkspaceCompatibilityMetadata {
			return completeInputWorkspaceCompatibilityFixture(models.InputWorkspaceCompatibilityStateCertified)
		}, scanError: true, invalidColumn: 10},
	}
	states := []models.InputWorkspaceCompatibilityState{
		models.InputWorkspaceCompatibilityStateCertified,
		models.InputWorkspaceCompatibilityStatePending,
		models.InputWorkspaceCompatibilityStateUnavailable,
		models.InputWorkspaceCompatibilityStateUnprovable,
		"future_state",
		"",
	}
	for _, state := range states {
		tests = append(tests, testCase{
			name: "all metadata with raw state " + string(state),
			fixture: func() InputWorkspaceCompatibilityMetadata {
				return completeInputWorkspaceCompatibilityFixture(state)
			},
		})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
			require.NoError(t, err, "each passive compatibility case should use a fresh exact-query mock")
			defer mock.Close()
			orgID, sessionID, requestID := uuid.New(), uuid.New(), uuid.New()
			queryErr := errors.New("compatibility metadata unavailable")
			query := mock.ExpectQuery(expectedSQL).WithArgs(orgID, sessionID, requestID)
			want := InputWorkspaceCompatibilityMetadata{}
			if tt.queryError {
				query.WillReturnError(queryErr)
			} else {
				rows := inputWorkspaceCompatibilityRows()
				if !tt.missing {
					want = tt.fixture()
					values := inputWorkspaceCompatibilityValues(want)
					if tt.scanError {
						values[tt.invalidColumn] = struct{}{}
						want = InputWorkspaceCompatibilityMetadata{}
					}
					rows.AddRow(values...)
				}
				query.WillReturnRows(rows)
			}
			got, err := NewSessionHumanInputRequestStore(mock).GetWorkspaceCompatibility(context.Background(), orgID, sessionID, requestID)
			switch {
			case tt.missing:
				require.ErrorIs(t, err, pgx.ErrNoRows, "absent exact metadata must preserve not-found instead of inferring any compatibility state")
			case tt.queryError:
				require.ErrorIs(t, err, queryErr, "passive compatibility query failures must preserve their cause")
			case tt.scanError:
				require.Error(t, err, "malformed column data must fail the entire observation")
			default:
				require.NoError(t, err, "raw compatibility metadata must be returned without validation or eligibility inference")
			}
			require.Equal(t, want, got, "reader must preserve all independent nullable fields or return a completely zero result on error")
			require.NoError(t, mock.ExpectationsWereMet(), "the single read must enforce exact tenant/session/request scope without joins, status filters, locks or writes")
		})
	}
}

func completeInputWorkspaceCompatibilityFixture(state models.InputWorkspaceCompatibilityState) InputWorkspaceCompatibilityMetadata {
	anchor, contextScope := "synthetic-anchor-digest", "synthetic-context-scope-digest"
	generation, revision := int64(23), int64(31)
	key, kind, reason := "synthetic-workspace-key", "synthetic-authority-kind", "synthetic-observation"
	receipt := uuid.New()
	due := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	return InputWorkspaceCompatibilityMetadata{
		State: state, AnchorDigest: &anchor, ContextScopeDigest: &contextScope,
		WorkspaceGeneration: &generation, WorkspaceRevision: &revision, SnapshotKey: &key,
		WorkspaceState:    json.RawMessage(`{"synthetic":true,"future_descriptor":[1,null]}`),
		LastAuthorityKind: &kind, LastAuthorityReceiptID: &receipt, Reason: &reason, NextRevalidationAt: &due,
	}
}

func inputWorkspaceCompatibilityRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"state", "anchor_digest", "context_scope_digest", "workspace_generation", "workspace_revision", "snapshot_key", "workspace_state", "last_authority_kind", "last_authority_receipt_id", "reason", "next_revalidation_at"})
}

func inputWorkspaceCompatibilityValues(metadata InputWorkspaceCompatibilityMetadata) []any {
	return []any{metadata.State, metadata.AnchorDigest, metadata.ContextScopeDigest,
		metadata.WorkspaceGeneration, metadata.WorkspaceRevision, metadata.SnapshotKey,
		metadata.WorkspaceState, metadata.LastAuthorityKind, metadata.LastAuthorityReceiptID,
		metadata.Reason, metadata.NextRevalidationAt}
}
