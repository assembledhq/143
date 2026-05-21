//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// TestIntegration_RetrySession_ResetsAndReenqueues verifies the failure
// recovery path: a user-clicked Retry on a failed session must (a) reset the
// session row to pending, (b) clear the failure_* metadata, and (c) enqueue
// a fresh run_agent job. A regression in any of these steps strands the
// session: pending-with-no-job is the worst failure mode because it looks
// fine in the UI but never makes progress.
func TestIntegration_RetrySession_ResetsAndReenqueues(t *testing.T) {
	pool := setup(t)

	orgID := seedOrg(t, pool)
	user := seedUser(t, pool, orgID)

	session := seedSession(t, pool, orgID, sessionOpts{
		Status:      models.SessionStatusFailed,
		CurrentTurn: 1,
	})
	// Stamp realistic failure metadata so the test verifies the reset
	// actually clears it. A future refactor that drops a column from
	// ResetForRetry's UPDATE list would surface here as leftover data.
	expl := "agent ran out of context"
	cat := "context_overflow"
	_, err := pool.Exec(context.Background(), `
		UPDATE sessions
		SET failure_explanation = $1,
		    failure_category = $2,
		    failure_retry_advised = true,
		    error = 'something went wrong'
		WHERE id = $3
	`, expl, cat, session.ID)
	require.NoError(t, err)

	handler := newTestSessionHandler(pool)

	req := buildAuthedRequest(http.MethodPost,
		"/api/v1/sessions/"+session.ID.String()+"/retry",
		nil, orgID, &user, map[string]string{"id": session.ID.String()})

	rec := httptest.NewRecorder()
	handler.RetrySession(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "retry should return 200, body=%s", rec.Body.String())

	// 1. Session reset to pending with failure metadata cleared.
	updated, err := db.NewSessionStore(pool).GetByID(context.Background(), orgID, session.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", updated.Status)
	require.Nil(t, updated.FailureExplanation, "failure_explanation must be cleared on retry")
	require.Nil(t, updated.FailureCategory, "failure_category must be cleared on retry")
	require.False(t, updated.FailureRetryAdvised, "failure_retry_advised must be reset on retry")
	require.Nil(t, updated.Error, "error must be cleared on retry")
	require.Nil(t, updated.StartedAt, "started_at must be cleared so the next run computes duration from a fresh start")
	require.Nil(t, updated.CompletedAt, "completed_at must be cleared")

	// 2. New run_agent job in queue.
	jobs := listJobs(t, pool, orgID)
	require.Len(t, jobs, 1, "retry must enqueue exactly one run_agent job")
	job := jobs[0]
	require.Equal(t, "run_agent", job.JobType)
	require.Equal(t, "agent", job.Queue)
	require.Equal(t, "pending", job.Status)
	require.Equal(t, session.ID.String(), payloadField(t, job.Payload, "session_id"))
	require.Equal(t, orgID.String(), payloadField(t, job.Payload, "org_id"))

	// 3. The dedupe_key must be set so that a duplicate retry (or any other
	// run_agent enqueue path) is collapsed by the partial unique index on
	// (queue, dedupe_key) WHERE status IN ('pending','running'). Verified via
	// a direct query because listJobs hides this column. Regression guard:
	// the prod incident at session f996f0e1 was caused by retry passing
	// dedupe_key=NULL, letting a stale pending run_agent and the retry's new
	// run_agent both fire and race AcquireTurnHold.
	var dedupeKey *string
	err = pool.QueryRow(context.Background(),
		`SELECT dedupe_key FROM jobs WHERE id = $1`, job.ID).Scan(&dedupeKey)
	require.NoError(t, err)
	require.NotNil(t, dedupeKey, "retry's run_agent enqueue must set a dedupe_key to suppress duplicate jobs")
	require.Equal(t, "run_agent:"+session.ID.String(), *dedupeKey, "dedupe key must follow the canonical run_agent:<sessionID> format from db.RunAgentDedupeKey")
}

// TestIntegration_RetrySession_RejectsNonFailedSession guards the inverse:
// retrying an already-running or already-pending session must 409 without
// touching the queue. Otherwise a double-click on Retry would enqueue two
// jobs that race for the same session.
func TestIntegration_RetrySession_RejectsNonFailedSession(t *testing.T) {
	pool := setup(t)

	orgID := seedOrg(t, pool)
	user := seedUser(t, pool, orgID)
	session := seedSession(t, pool, orgID, sessionOpts{
		Status:      models.SessionStatusRunning,
		CurrentTurn: 1,
	})

	handler := newTestSessionHandler(pool)
	req := buildAuthedRequest(http.MethodPost,
		"/api/v1/sessions/"+session.ID.String()+"/retry",
		nil, orgID, &user, map[string]string{"id": session.ID.String()})

	rec := httptest.NewRecorder()
	handler.RetrySession(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "NOT_FAILED")

	jobs := listJobs(t, pool, orgID)
	require.Empty(t, jobs, "rejected retry must not enqueue any job")
}
