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

// TestIntegration_EndSession_EnqueuesOpenPRJob covers the "I'm done — open
// the PR" terminal action. The default validation policy is "on session
// end", so EndSession should transition the session to completed and
// enqueue an `open_pr` job whose dedupe key keeps double-clicks from
// producing duplicate PRs.
//
// A regression here would mean completed sessions never produce a PR — the
// user finishes their work, hits End, and watches the result get silently
// dropped on the floor.
func TestIntegration_EndSession_EnqueuesOpenPRJob(t *testing.T) {
	pool := setup(t)

	orgID := seedOrg(t, pool)
	user := seedUser(t, pool, orgID)

	session := seedSession(t, pool, orgID, sessionOpts{
		Status:      string(models.SessionStatusIdle),
		CurrentTurn: 2,
		Validation:  models.SessionValidationPolicyOnSessionEnd,
	})

	handler := newTestSessionHandler(pool)
	req := buildAuthedRequest(http.MethodPost,
		"/api/v1/sessions/"+session.ID.String()+"/end",
		nil, orgID, &user, map[string]string{"id": session.ID.String()})

	rec := httptest.NewRecorder()
	handler.EndSession(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "end session should return 200, body=%s", rec.Body.String())

	// 1. Session transitioned to completed; pr_creation_state queued.
	updated, err := db.NewSessionStore(pool).GetByID(context.Background(), orgID, session.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", updated.Status)
	require.Equal(t, models.PRCreationStateQueued, updated.PRCreationState,
		"end-session must mark PR creation as queued so the UI shows the right state immediately")

	// 2. open_pr job enqueued with the dedupe key the handler builds.
	//    Dedupe is the only thing that prevents a double-click from
	//    producing two PRs; the test asserts both type and dedupe behavior.
	jobs := listJobs(t, pool, orgID)
	require.Len(t, jobs, 1)
	job := jobs[0]
	require.Equal(t, "open_pr", job.JobType,
		"validation policy `on_session_end` must enqueue open_pr (not validate)")
	require.Equal(t, "default", job.Queue, "open_pr lives on the default queue (not agent), so non-sandbox workers can pick it up")
	require.Equal(t, "pending", job.Status)
	require.Equal(t, session.ID.String(), payloadField(t, job.Payload, "session_id"))
	require.Equal(t, orgID.String(), payloadField(t, job.Payload, "org_id"))

	// Dedupe assertion: re-end the same (now completed) session should be
	// rejected because it's no longer idle, so we won't actually exercise
	// dedupe here — but the dedupe_key column should be populated. Verify
	// it via a direct query so a future refactor that drops the dedupe
	// column from the INSERT is caught.
	var dedupeKey *string
	err = pool.QueryRow(context.Background(),
		`SELECT dedupe_key FROM jobs WHERE id = $1`, job.ID).Scan(&dedupeKey)
	require.NoError(t, err)
	require.NotNil(t, dedupeKey, "open_pr enqueue must set a dedupe_key to suppress double-clicks")
	require.Equal(t, "open_pr:"+session.ID.String(), *dedupeKey)
}

// TestIntegration_EndSession_RejectsNonIdleSession asserts the precondition:
// only idle sessions can be ended. Ending a running or already-completed
// session should 409 without enqueuing or transitioning anything.
func TestIntegration_EndSession_RejectsNonIdleSession(t *testing.T) {
	pool := setup(t)

	orgID := seedOrg(t, pool)
	user := seedUser(t, pool, orgID)
	session := seedSession(t, pool, orgID, sessionOpts{
		Status:      string(models.SessionStatusRunning),
		CurrentTurn: 1,
	})

	handler := newTestSessionHandler(pool)
	req := buildAuthedRequest(http.MethodPost,
		"/api/v1/sessions/"+session.ID.String()+"/end",
		nil, orgID, &user, map[string]string{"id": session.ID.String()})

	rec := httptest.NewRecorder()
	handler.EndSession(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "NOT_IDLE")

	stored, err := db.NewSessionStore(pool).GetByID(context.Background(), orgID, session.ID)
	require.NoError(t, err)
	require.Equal(t, "running", stored.Status, "rejected end-session must not mutate status")

	jobs := listJobs(t, pool, orgID)
	require.Empty(t, jobs)
}
