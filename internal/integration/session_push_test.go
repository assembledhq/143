//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/handlers"
	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// TestIntegration_SendMessage_EnqueuesContinueSessionJob is the headline test
// for the push-to-session flow. It exercises the same code path a frontend
// chat reply hits in production: real SessionStore, SessionMessageStore, and
// JobStore against a real Postgres, with the handler claiming the idle
// session, persisting the user message, and enqueueing a continue_session
// job — all in a single transaction.
//
// The assertions are structured to catch the exact regressions pgxmock-based
// tests miss:
//   - SQL typos / schema drift (real INSERTs run; bad columns fail loudly)
//   - Transaction-boundary bugs (we read state *after* commit, so a
//     mid-tx failure would leave the assertions to surface inconsistency)
//   - Wrong job_type / queue / payload shape (the worker dispatches by
//     these fields; a refactor that drifts the constants is silently broken
//     in unit tests but caught here)
func TestIntegration_SendMessage_EnqueuesContinueSessionJob(t *testing.T) {
	pool := setup(t)

	orgID := seedOrg(t, pool)
	user := seedUser(t, pool, orgID)
	session := seedSession(t, pool, orgID, sessionOpts{
		Status:      models.SessionStatusIdle,
		CurrentTurn: 1,
	})

	handler := newTestSessionHandler(pool)

	body := strings.NewReader(`{"message":"Please add tests for the new RPC handler."}`)
	req := buildAuthedRequest(http.MethodPost,
		"/api/v1/sessions/"+session.ID.String()+"/messages",
		body, orgID, &user, map[string]string{"id": session.ID.String()})

	rec := httptest.NewRecorder()
	handler.SendMessage(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, "send message should return 201, body=%s", rec.Body.String())

	// Assert the response carries the persisted message back to the client.
	var resp struct {
		Data models.SessionMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "Please add tests for the new RPC handler.", resp.Data.Content)
	require.Equal(t, models.MessageRoleUser, resp.Data.Role)
	require.Equal(t, session.CurrentTurn+1, resp.Data.TurnNumber, "turn number should advance one past the seeded value")
	require.NotNil(t, resp.Data.UserID)
	require.Equal(t, user.ID, *resp.Data.UserID)

	// 1. messages — exactly the one row we just sent.
	msgs, err := db.NewSessionMessageStore(pool).ListBySession(context.Background(), orgID, session.ID)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Equal(t, "Please add tests for the new RPC handler.", msgs[0].Content)
	require.Equal(t, session.CurrentTurn+1, msgs[0].TurnNumber)

	// 2. session — claimed and now running. Status transitions are part of
	//    the contract: a future refactor that, say, leaves status='idle'
	//    after ClaimIdle would produce frozen sessions in production.
	updated, err := db.NewSessionStore(pool).GetByID(context.Background(), orgID, session.ID)
	require.NoError(t, err)
	require.Equal(t, models.SessionStatusRunning, updated.Status,
		"ClaimIdle should have transitioned the session to running")

	// 3. jobs — exactly one continue_session job, queue=agent, payload
	//    references this session. The whole reason we're testing this against
	//    a real DB is that this assertion catches the case where the handler
	//    or the JobStore drift apart on column names / payload shape.
	jobs := listJobs(t, pool, orgID)
	require.Len(t, jobs, 1, "expected exactly one job enqueued")
	job := jobs[0]
	require.Equal(t, "continue_session", job.JobType,
		"job type drift will silently break push — worker dispatches by this field")
	require.Equal(t, "agent", job.Queue)
	require.Equal(t, "pending", job.Status)
	require.Equal(t, 5, job.Priority)
	require.Equal(t, session.ID.String(), payloadField(t, job.Payload, "session_id"))
	require.Equal(t, orgID.String(), payloadField(t, job.Payload, "org_id"))
}

// TestIntegration_SendMessage_RunningSessionSkipsJob covers the second
// branch of SendMessage: when the session is already running, the message is
// buffered into session_messages but no continue_session job is enqueued
// because the live agent will pick it up inline. A refactor that flips the
// branch (enqueueing for running sessions) would cause duplicate runs in
// production.
func TestIntegration_SendMessage_RunningSessionSkipsJob(t *testing.T) {
	pool := setup(t)

	orgID := seedOrg(t, pool)
	user := seedUser(t, pool, orgID)
	session := seedSession(t, pool, orgID, sessionOpts{
		Status:      models.SessionStatusRunning,
		CurrentTurn: 2,
	})

	handler := newTestSessionHandler(pool)

	body := strings.NewReader(`{"message":"quick aside while you work"}`)
	req := buildAuthedRequest(http.MethodPost,
		"/api/v1/sessions/"+session.ID.String()+"/messages",
		body, orgID, &user, map[string]string{"id": session.ID.String()})

	rec := httptest.NewRecorder()
	handler.SendMessage(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, "running-session reply should return 201, body=%s", rec.Body.String())

	msgs, err := db.NewSessionMessageStore(pool).ListBySession(context.Background(), orgID, session.ID)
	require.NoError(t, err)
	require.Len(t, msgs, 1, "message should still be persisted")

	jobs := listJobs(t, pool, orgID)
	require.Empty(t, jobs, "no job should be enqueued for a running session — agent picks the message up inline")
}

// TestIntegration_SendMessage_RejectsDestroyedSnapshot guards the
// SNAPSHOT_EXPIRED branch: a session whose sandbox was reaped after 30 days
// cannot be resumed. The handler must reject before touching the message
// store or the job queue, otherwise we'd accumulate orphan rows pointing at
// runs that can never start.
func TestIntegration_SendMessage_RejectsDestroyedSnapshot(t *testing.T) {
	pool := setup(t)

	orgID := seedOrg(t, pool)
	user := seedUser(t, pool, orgID)
	session := seedSession(t, pool, orgID, sessionOpts{
		Status:      models.SessionStatusCompleted,
		CurrentTurn: 3,
	})
	// Mark the snapshot destroyed (the reaper does this in production).
	_, err := pool.Exec(context.Background(),
		`UPDATE sessions SET sandbox_state = 'destroyed' WHERE id = $1`, session.ID)
	require.NoError(t, err)

	handler := newTestSessionHandler(pool)

	body := strings.NewReader(`{"message":"please resume"}`)
	req := buildAuthedRequest(http.MethodPost,
		"/api/v1/sessions/"+session.ID.String()+"/messages",
		body, orgID, &user, map[string]string{"id": session.ID.String()})

	rec := httptest.NewRecorder()
	handler.SendMessage(rec, req)

	require.Equal(t, http.StatusGone, rec.Code, "destroyed-snapshot should be 410 Gone")
	require.Contains(t, rec.Body.String(), "SNAPSHOT_EXPIRED")

	msgs, err := db.NewSessionMessageStore(pool).ListBySession(context.Background(), orgID, session.ID)
	require.NoError(t, err)
	require.Empty(t, msgs, "rejected request must not persist a message")

	jobs := listJobs(t, pool, orgID)
	require.Empty(t, jobs, "rejected request must not enqueue a job")
}

// newTestSessionHandler wires the production SessionHandler against the live
// pool. Stores irrelevant to the flows under test (validation, PR, threads,
// LLM client, audit emitter) are passed nil — the handlers we exercise check
// for nil before using them, and we want a regression to the contrary to
// surface as a panic during the test rather than be hidden behind a mock.
//
// Mirroring cmd/server/main.go's wiring is deliberate: if a future refactor
// changes which stores SendMessage / CreateManual / RetrySession / EndSession
// reach for, this helper must be updated in the same commit, which is itself
// a useful refactor signal.
func newTestSessionHandler(pool *pgxpool.Pool) *handlers.SessionHandler {
	logger := zerolog.Nop()
	return handlers.NewSessionHandler(
		db.NewSessionStore(pool),
		db.NewSessionLogStore(pool),
		db.NewSessionQuestionStore(pool),
		nil, // pullRequestStore
		nil, // issueStore — set to nil; CreateManual without RepositoryID won't call it
		db.NewRepositoryStore(pool),
		db.NewOrganizationStore(pool),
		db.NewJobStore(pool),
		db.NewSessionMessageStore(pool),
		db.NewSessionThreadStore(pool),
		nil, // llmClient — CreateManual treats nil as "skip title generation"
		logger,
	)
}

// buildAuthedRequest constructs an *http.Request as if it had passed the
// auth + org-context middleware in production: the user is on the request
// context, the active org is on the context, and chi route params are set
// so chi.URLParam(r, "id") returns the right session ID.
func buildAuthedRequest(method, target string, body io.Reader, orgID uuid.UUID, user *models.User, urlParams map[string]string) *http.Request {
	req := httptest.NewRequest(method, target, body)
	rctx := chi.NewRouteContext()
	for k, v := range urlParams {
		rctx.URLParams.Add(k, v)
	}
	ctx := req.Context()
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	if user != nil {
		ctx = middleware.WithUser(ctx, user)
	}
	return req.WithContext(ctx)
}
