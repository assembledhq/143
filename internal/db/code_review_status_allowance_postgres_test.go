package db

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// Each parallel leaf owns its synthetic tenant and complete metadata row. Every
// attempted enum mutation is rolled back, including accepted values; no retained
// classification is erased to make a later migration or test succeed.
func TestCodeReviewDispatchUnresolvedStatusAllowancePostgres(t *testing.T) {
	t.Parallel()
	setupCtx, setupCancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(setupCancel)
	pool := testpostgres.NewPool(t, setupCtx, "../../migrations", 2)
	// This existing fixture assigns repository IDs with max()+1. Serialize only
	// seeding; the actual status attempts use independent tenant rows/transactions.
	var seedMu sync.Mutex
	tests := []struct {
		name           string
		code           any
		wantConstraint string
	}{
		{name: "adopted unresolved dispatch", code: "code_reviewer_dispatch_unresolved"},
		{name: "NULL", code: nil},
		{name: "GitHub rate limited", code: "github_rate_limited"},
		{name: "GitHub unavailable", code: "github_unavailable"},
		{name: "reviewer failed", code: "reviewer_failed"},
		{name: "worker failed", code: "worker_failed"},
		{name: "review loop detected", code: "review_loop_detected"},
		{name: "unknown code rejected", code: "synthetic_unknown_status", wantConstraint: "code_review_session_metadata_status_code_v2_check"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Start the operation budget after this parallel leaf is scheduled.
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			org, in := func() (uuid.UUID, CodeReviewDispatchPreparation) {
				seedMu.Lock()
				defer seedMu.Unlock()
				return codeReviewDispatchPreparePGFixture(t, ctx, pool)
			}()
			var before json.RawMessage
			require.NoError(t, pool.QueryRow(ctx, `SELECT to_jsonb(m) FROM code_review_session_metadata m WHERE org_id=$1 AND id=$2`, org, in.MetadataID).Scan(&before), "retain the complete original owned metadata row before the enum attempt")
			tx, err := pool.Begin(ctx)
			require.NoError(t, err, "own one isolated transaction for exactly one status allowance attempt")
			defer func() { _ = tx.Rollback(context.Background()) }()
			var actual json.RawMessage
			err = tx.QueryRow(ctx, `UPDATE code_review_session_metadata m SET status_code=$3 WHERE org_id=$1 AND id=$2 RETURNING to_jsonb(m)`, org, in.MetadataID, tt.code).Scan(&actual)
			if tt.wantConstraint != "" {
				var pgErr *pgconn.PgError
				require.ErrorAs(t, err, &pgErr, "an unknown status must fail the actual database CHECK, not a Go validator")
				require.Equal(t, "23514", pgErr.Code, "unknown status classification must remain a real PostgreSQL check violation")
				require.Equal(t, tt.wantConstraint, pgErr.ConstraintName, "the rejection must come from the distinct widened status allowance")
			} else {
				require.NoError(t, err, "the adopted new status, every old status and NULL must be accepted by the actual schema")
				var expected map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(before, &expected), "retain every unrelated metadata column as raw JSON without float conversion")
				value, marshalErr := json.Marshal(tt.code)
				require.NoError(t, marshalErr, "encode the exact expected finite enum or NULL value")
				expected["status_code"] = value
				row, marshalErr := json.Marshal(expected)
				require.NoError(t, marshalErr, "encode the complete expected metadata row after only its enum field changed")
				require.JSONEq(t, string(row), string(actual), "acceptance must change only the exact owned row's status_code field")
			}
			require.NoError(t, tx.Rollback(ctx), "roll back the isolated enum attempt rather than rewriting retained history")
			var after json.RawMessage
			require.NoError(t, pool.QueryRow(ctx, `SELECT to_jsonb(m) FROM code_review_session_metadata m WHERE org_id=$1 AND id=$2`, org, in.MetadataID).Scan(&after), "independently read the complete committed row after settlement")
			require.JSONEq(t, string(before), string(after), "both accepted and rejected fixture attempts must leave the complete committed metadata unchanged")
		})
	}
}
