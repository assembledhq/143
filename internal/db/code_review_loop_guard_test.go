package db

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestStopFullReviewFallbackLoopPostgres(t *testing.T) {
	t.Parallel()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL for review restart guard proof")
	}
	tests := []struct {
		name, setup                                          string
		stopped, pending, newActive, foreignOrg, failEnqueue bool
	}{
		{name: "third automatic attempt stops", stopped: true},
		{name: "comment enqueue failure rolls back cancellation", failEnqueue: true, setup: "ALTER TABLE jobs ADD CONSTRAINT reject_enqueue CHECK (false)"},
		{name: "ancestor admission survived before queued marker", stopped: true, setup: "UPDATE code_review_revision_assessments SET failure_detail='full_review:changed' WHERE n=2"},
		{name: "newer pending request survives", stopped: true, pending: true},
		{name: "newer active session survives", stopped: true, newActive: true},
		{name: "only two attempts", setup: "DELETE FROM code_review_revision_assessments WHERE n=1"},
		{name: "explicit current request resets", setup: "UPDATE sessions SET revision_context='{}' WHERE n=3"},
		{name: "explicit intermediate request resets", setup: "UPDATE sessions SET revision_context='{}' WHERE n=2"},
		{name: "new head resets", setup: "UPDATE code_review_revision_assessments SET head_sha='new-head' WHERE n=2"},
		{name: "new base resets", setup: "UPDATE code_review_revision_assessments SET base_sha='new-base' WHERE n=2"},
		{name: "new base ref resets", setup: "UPDATE code_review_revision_assessments SET base_ref='other-branch' WHERE n=2"},
		{name: "changed code resets", setup: "UPDATE code_review_revision_assessments SET code_digest='changed' WHERE n=2"},
		{name: "changed contract resets", setup: "UPDATE code_review_revision_assessments SET contract_digest='changed' WHERE n=2"},
		{name: "changed intent resets", setup: "UPDATE code_review_revision_assessments SET intent_digest='changed' WHERE n=2"},
		{name: "successful assessment resets", setup: "UPDATE code_review_revision_assessments SET status='completed' WHERE n=2"},
		{name: "evidence continuation resets", setup: "UPDATE code_review_revision_assessments SET review_scope='evidence_only' WHERE n=2"},
		{name: "uncertain current publication protected", setup: "UPDATE code_review_revision_assessments SET publication_state='uncertain' WHERE n=3"},
		{name: "confirmed current publication protected", setup: "UPDATE code_review_revision_assessments SET publication_state='confirmed' WHERE n=3"},
		{name: "receipt protected", setup: "UPDATE code_review_revision_assessments SET publication_receipt='{}' WHERE n=3"},
		{name: "GitHub review protected", setup: "UPDATE code_review_revision_assessments SET github_review_id=42 WHERE n=3"},
		{name: "prior receipt breaks chain", setup: "UPDATE code_review_revision_assessments SET publication_receipt='{}' WHERE n=2"},
		{name: "already queued fallback untouched", setup: "UPDATE code_review_revision_assessments SET failure_detail='full_review_queued:changed' WHERE n=3"},
		{name: "other tenant ancestor ignored", setup: "UPDATE code_review_revision_assessments SET org_id=gen_random_uuid() WHERE n=2"},
		{name: "other PR ancestor ignored", setup: "UPDATE code_review_revision_assessments SET pull_request_id=gen_random_uuid() WHERE n=2"},
		{name: "cycle cannot exhaust budget", setup: "UPDATE code_review_revision_assessments SET previous_assessment_id=id WHERE n=2"},
		{name: "other tenant request rejected", foreignOrg: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			conn, err := pgx.Connect(ctx, databaseURL)
			require.NoError(t, err, "connect disposable PostgreSQL")
			defer func() { require.NoError(t, conn.Close(ctx), "close isolated connection") }()
			tx, err := conn.Begin(ctx)
			require.NoError(t, err, "start isolated schema transaction")
			defer func() { require.NoError(t, tx.Rollback(ctx), "discard isolated guard fixtures") }()
			schema := "review_loop_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			_, err = tx.Exec(ctx, "CREATE SCHEMA "+schema+"; SET LOCAL search_path TO "+schema+",public")
			require.NoError(t, err, "isolate test tables")
			_, err = tx.Exec(ctx, `
CREATE TABLE sessions(n int,id uuid,org_id uuid,revision_context jsonb);
CREATE TABLE code_review_revision_assessments(n int,id uuid,org_id uuid,pull_request_id uuid,session_id uuid,previous_assessment_id uuid,head_sha text,base_sha text,base_ref text,code_digest text,contract_digest text,intent_digest text,review_scope text,status text,failure_detail text,publication_state text,publication_receipt jsonb,github_review_id bigint,completed_at timestamptz);
CREATE TABLE code_review_session_metadata(org_id uuid,pull_request_id uuid,session_id uuid,status text,stale boolean DEFAULT true,phase text,status_code text,status_message text,failure_reason text,retry_at timestamptz,retryable_failure bool,completed_at timestamptz);
CREATE TABLE session_threads(org_id uuid,session_id uuid,status text,cancel_requested_at timestamptz);
CREATE TABLE code_review_requests(org_id uuid,pull_request_id uuid,assessment_id uuid,session_id uuid,status text);
CREATE TABLE jobs(id uuid DEFAULT gen_random_uuid(),org_id uuid,queue text,job_type text,payload jsonb,priority int,dedupe_key text UNIQUE,run_at timestamptz,max_attempts int);`)
			require.NoError(t, err, "create restart guard fixtures")
			org, repo, pr := uuid.New(), uuid.New(), uuid.New()
			var previous *uuid.UUID
			var current models.CodeReviewAssessment
			for n := 1; n <= 3; n++ {
				id, session := uuid.New(), uuid.New()
				source, detail := "assessment_fallback", "full_review_queued:changed"
				if n == 1 {
					source = "ui"
				}
				if n == 3 {
					detail = "full_review:changed"
				}
				_, err = tx.Exec(ctx, `INSERT INTO sessions VALUES($1,$2,$3,jsonb_build_object('request_context',jsonb_build_object('source',$4::text)))`, n, session, org, source)
				require.NoError(t, err, "seed distinct review request provenance")
				_, err = tx.Exec(ctx, `INSERT INTO code_review_revision_assessments(n,id,org_id,pull_request_id,session_id,previous_assessment_id,head_sha,base_sha,base_ref,code_digest,contract_digest,intent_digest,review_scope,status,failure_detail,publication_state) VALUES($1,$2,$3,$4,$5,$6,'head','base','main','code','contract','intent','full','superseded',$7,'not_started')`, n, id, org, pr, session, previous, detail)
				require.NoError(t, err, "seed consecutive unsent fallback")
				previous = &id
				current = models.CodeReviewAssessment{ID: id, OrgID: org, RepositoryID: repo, PullRequestID: pr, SessionID: session}
			}
			_, err = tx.Exec(ctx, `INSERT INTO code_review_session_metadata(org_id,pull_request_id,session_id,status,retryable_failure) VALUES($1,$2,$3,'stale',true);`, org, pr, current.SessionID)
			require.NoError(t, err, "seed terminal metadata")
			_, err = tx.Exec(ctx, `INSERT INTO session_threads VALUES($1,$2,'running',NULL)`, org, current.SessionID)
			require.NoError(t, err, "seed lingering reviewer thread")
			_, err = tx.Exec(ctx, `INSERT INTO code_review_requests VALUES($1,$2,$3,$4,'joined')`, org, pr, current.ID, current.SessionID)
			require.NoError(t, err, "seed bound request")
			if tt.setup != "" {
				_, err = tx.Exec(ctx, tt.setup)
				require.NoError(t, err, "apply guard boundary scenario")
			}
			state := models.CodeReviewPRState{State: models.CodeReviewScheduleRunning, ActiveAssessmentID: &current.ID, ActiveSessionID: &current.SessionID}
			expected := state
			if tt.pending {
				state.PendingInput = json.RawMessage(`{"new":"request"}`)
				expected.PendingInput = state.PendingInput
			}
			if tt.newActive {
				newID := uuid.New()
				state.ActiveSessionID = &newID
				expected.ActiveSessionID = &newID
			}
			if tt.stopped {
				expected.ActiveAssessmentID = nil
				if !tt.newActive {
					expected.ActiveSessionID = nil
				}
				if !tt.pending && !tt.newActive {
					expected.State = models.CodeReviewScheduleIdle
				}
			}
			if tt.foreignOrg {
				current.OrgID = uuid.New()
			}
			workTx := tx
			if tt.failEnqueue {
				workTx, err = tx.Begin(ctx)
				require.NoError(t, err, "isolate failed cancellation transaction")
			}
			got, err := stopFullReviewFallbackLoop(ctx, workTx, current, 3, "restart limit reached", &state)
			if tt.foreignOrg {
				require.ErrorIs(t, err, pgx.ErrNoRows, "another tenant cannot access the assessment")
				return
			}
			if tt.failEnqueue {
				require.ErrorContains(t, err, "enqueue stopped review status comment", "failed enqueue must fail the cancellation transaction")
				require.NoError(t, workTx.Rollback(ctx), "rollback must restore assessment, metadata, request, and thread state")
			} else {
				require.NoError(t, err, "restart guard should evaluate safely")
			}
			require.Equal(t, tt.stopped, got, "only a consecutive automatic restart chain should stop")
			require.Equal(t, expected, state, "newer work must survive stopping an old loop")
			var status, request string
			var cancelled bool
			err = tx.QueryRow(ctx, `SELECT a.status,r.status,t.cancel_requested_at IS NOT NULL FROM code_review_revision_assessments a JOIN code_review_requests r ON r.org_id=a.org_id AND r.assessment_id=a.id JOIN session_threads t ON t.org_id=a.org_id AND t.session_id=a.session_id WHERE a.org_id=$1 AND a.id=$2`, org, current.ID).Scan(&status, &request, &cancelled)
			require.NoError(t, err, "read durable cancellation state")
			expectedStatus, expectedRequest := "superseded", "joined"
			if tt.stopped {
				expectedStatus, expectedRequest = "cancelled", "cancelled"
			}
			require.Equal(t, []any{expectedStatus, expectedRequest, tt.stopped}, []any{status, request, cancelled}, "assessment, request, and runtime cancellation must agree")
			if tt.stopped {
				again, err := stopFullReviewFallbackLoop(ctx, tx, current, 3, "restart limit reached", &state)
				require.NoError(t, err, "replayed stop should succeed")
				require.True(t, again, "replayed stop must never admit another fallback")
				require.Equal(t, expected, state, "replayed stop must preserve newer work")
			}
			var count int
			err = tx.QueryRow(ctx, `SELECT COUNT(*) FROM jobs WHERE org_id=$1`, org).Scan(&count)
			require.NoError(t, err, "read durable comment sync jobs")
			expectedCount := 0
			if tt.stopped {
				expectedCount = 1
			}
			require.Equal(t, expectedCount, count, "only a stopped loop should enqueue one terminal comment even after replay")
			if tt.stopped {
				var queue, jobType, key string
				var payload map[string]string
				var priority, maxAttempts int
				err = tx.QueryRow(ctx, `SELECT queue,job_type,payload,priority,dedupe_key,max_attempts FROM jobs WHERE org_id=$1`, org).Scan(&queue, &jobType, &payload, &priority, &key, &maxAttempts)
				require.NoError(t, err, "read the exact terminal comment dispatch")
				require.Equal(t, []any{"default", models.JobTypeSyncCodeReviewStatusComment, 3, "code_review_status_comment:" + current.SessionID.String() + ":loop_stopped", 3}, []any{queue, jobType, priority, key, maxAttempts}, "terminal comment uses its own dedupe key and the normal status queue")
				require.Equal(t, map[string]string{"org_id": org.String(), "repository_id": repo.String(), "pull_request_id": pr.String(), "session_id": current.SessionID.String()}, payload, "comment payload is scoped to the stopped review")
			}
		})
	}
}
