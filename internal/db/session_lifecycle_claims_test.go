package db

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestLifecycleEpochStore_GetThreadClaim(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		kind string
	}{
		{name: "unknown pair stays absent", kind: "null"},
		{name: "exact UUID pair is raw metadata", kind: "present"},
		{name: "present zero UUIDs are not SQL NULL", kind: "zero"},
		{name: "missing exact scope preserves no rows", kind: "missing"},
		{name: "query cause is wrapped", kind: "error"},
		{name: "partial scan is discarded", kind: "scan"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
			require.NoError(t, err, "create a fresh exact-query claim observation fixture")
			defer mock.Close()
			org, session, lane := uuid.New(), uuid.New(), uuid.New()
			cause := errors.New("synthetic database failure")
			expect := mock.ExpectQuery(`SELECT admission_claim_dispatch_id, admission_claim_token FROM session_threads WHERE org_id = $1 AND session_id = $2 AND id = $3`).WithArgs(org, session, lane)
			want := LifecycleAdmissionClaimState{}
			rows := pgxmock.NewRows([]string{"admission_claim_dispatch_id", "admission_claim_token"})
			switch tt.kind {
			case "error":
				expect.WillReturnError(cause)
			case "present", "zero":
				dispatch, token := uuid.Nil, uuid.Nil
				if tt.kind == "present" {
					dispatch, token = uuid.New(), uuid.New()
				}
				want = LifecycleAdmissionClaimState{DispatchID: &dispatch, Token: &token}
				rows.AddRow(&dispatch, &token)
			case "null":
				rows.AddRow(nil, nil)
			case "scan":
				dispatch := uuid.New()
				rows.AddRow(&dispatch, "invalid-uuid")
			}
			if tt.kind != "error" {
				expect.WillReturnRows(rows)
			}
			actual, err := NewLifecycleEpochStore(mock).GetThreadClaim(context.Background(), org, session, lane)
			switch tt.kind {
			case "error":
				require.ErrorIs(t, err, cause, "retain the underlying database cause for callers")
			case "missing":
				require.ErrorIs(t, err, pgx.ErrNoRows, "missing tenant/session/lane must remain a wrapped missing-row result")
			case "scan":
				require.Error(t, err, "malformed second column must not expose the first partially scanned UUID")
			default:
				require.NoError(t, err, "return raw nullable claim metadata without eligibility checks")
			}
			require.Equal(t, want, actual, "observe the exact pair or discard the complete partially read state on error")
			require.NoError(t, mock.ExpectationsWereMet(), "filter all three exact scope IDs without locks or visibility predicates")
		})
	}
}
