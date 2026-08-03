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

// TestIntegration_EndSession_CompletesWithoutPublishing covers the process
// lifecycle boundary introduced by agent-initiated publication. Ending a
// session only stops further agent work; publication requires a separate
// agent-tool or explicit user action.
//
// A regression here would let an ordinary End click bypass the durable
// publication coordinator and its review policy.
func TestIntegration_EndSession_CompletesWithoutPublishing(t *testing.T) {
	pool := setup(t)

	orgID := seedOrg(t, pool)
	user := seedUser(t, pool, orgID)

	session := seedSession(t, pool, orgID, sessionOpts{
		Status:      models.SessionStatusIdle,
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

	// The process is completed without mutating publication state.
	updated, err := db.NewSessionStore(pool).GetByID(context.Background(), orgID, session.ID)
	require.NoError(t, err, "completed session should remain readable")
	require.Equal(t, models.SessionStatusCompleted, updated.Status, "end session should complete the process lifecycle")
	require.Equal(t, models.PRCreationStateIdle, updated.PRCreationState,
		"end session should not imply pull request publication intent")

	// Publication requires a separate durable intent, so End must not enqueue
	// any worker action even when the legacy validation policy says session end.
	jobs := listJobs(t, pool, orgID)
	require.Empty(t, jobs, "end session should not enqueue an open_pr job")
}

// TestIntegration_EndSession_RejectsNonIdleSession asserts the precondition:
// only idle sessions can be ended. Ending a running or already-completed
// session should 409 without enqueuing or transitioning anything.
func TestIntegration_EndSession_RejectsNonIdleSession(t *testing.T) {
	pool := setup(t)

	orgID := seedOrg(t, pool)
	user := seedUser(t, pool, orgID)
	session := seedSession(t, pool, orgID, sessionOpts{
		Status:      models.SessionStatusRunning,
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
	require.Equal(t, models.SessionStatusRunning, stored.Status, "rejected end-session must not mutate status")

	jobs := listJobs(t, pool, orgID)
	require.Empty(t, jobs)
}
