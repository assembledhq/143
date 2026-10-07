package db

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRestoreRejectedPublicationPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		setup      string
		foreignOrg bool
		restored   bool
	}{
		{name: "owned rejected send", restored: true},
		{name: "other tenant", foreignOrg: true},
		{name: "different generation", setup: "generation=2"},
		{name: "different inputs", setup: "input_digest='other'"},
		{name: "different commit", setup: "submitted_commit_sha='other'"},
		{name: "terminal assessment", setup: "status='failed'"},
		{name: "confirmed publication", setup: "publication_state='confirmed'"},
		{name: "stored receipt", setup: "publication_receipt='{}'"},
		{name: "stored review ID", setup: "github_review_id=123"},
		{name: "no staged outcome", setup: "result_origin=NULL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool, org, repo, pr := newSchedulingPostgres(t)
			_, err := pool.Exec(ctx, `ALTER TABLE code_review_revision_assessments ADD COLUMN input_digest text, ADD COLUMN submitted_commit_sha text`)
			require.NoError(t, err, "add publication fence columns to isolated fixture")
			id := uuid.New()
			_, err = pool.Exec(ctx, `INSERT INTO code_review_revision_assessments(id,org_id,repository_id,pull_request_id,status,publication_state,result_origin,head_sha,submitted_commit_sha,input_digest) VALUES($1,$2,$3,$4,'publishing','uncertain','executed','head','head','inputs')`, id, org, repo, pr)
			require.NoError(t, err, "seed attempted publication")
			if tt.setup != "" {
				_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET `+tt.setup+` WHERE org_id=$1 AND id=$2`, org, id)
				require.NoError(t, err, "apply publication protection scenario")
			}
			var before string
			require.NoError(t, pool.QueryRow(ctx, `SELECT row_to_json(a)::text FROM code_review_revision_assessments a WHERE org_id=$1 AND id=$2`, org, id).Scan(&before), "capture all fields before recovery")
			queryOrg := org
			if tt.foreignOrg {
				queryOrg = uuid.New()
			}
			err = NewCodeReviewAssessmentStore(pool).RestoreRejectedPublication(ctx, queryOrg, id, 1, "inputs", "GitHub rejected publication")
			if !tt.restored {
				require.ErrorIs(t, err, ErrCodeReviewAssessmentState, "rejection recovery must honor every ownership and receipt fence")
				var after string
				require.NoError(t, pool.QueryRow(ctx, `SELECT row_to_json(a)::text FROM code_review_revision_assessments a WHERE org_id=$1 AND id=$2`, org, id).Scan(&after), "read protected assessment")
				require.Equal(t, before, after, "a failed fence must leave the entire assessment unchanged")
				return
			}
			require.NoError(t, err, "definitive rejection should clear this send's uncertainty")
			var state, detail string
			require.NoError(t, pool.QueryRow(ctx, `SELECT publication_state,failure_detail FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, org, id).Scan(&state, &detail), "read rejected publication state")
			require.Equal(t, []string{"reserved", "GitHub rejected publication"}, []string{state, detail}, "retain the rejection for terminal reconciliation")
		})
	}
}
