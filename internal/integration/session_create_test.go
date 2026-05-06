//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// TestIntegration_CreateManual_EnqueuesRunAgentJob covers the "user starts a
// new manual session" entry point. A regression here means the entire
// platform stops being able to create new sessions — every dashboard click
// would 500 silently in unit tests but fail to commit a row here.
//
// Notably, this test deliberately does *not* pass a repository_id. Repos
// require an upstream Integration row (FK), and exercising that wiring is
// out of scope for the v1 integration suite. The orchestrator will choose to
// run sandbox-less for repo-less manual sessions; that path still touches
// every line we care about: org settings parse, session insert, message
// insert, concurrency check, job enqueue.
func TestIntegration_CreateManual_EnqueuesRunAgentJob(t *testing.T) {
	pool := setup(t)

	orgID := seedOrg(t, pool)
	user := seedUser(t, pool, orgID)

	handler := newTestSessionHandler(pool)

	body := strings.NewReader(`{"message":"Refactor the cache key derivation in pkg/cache"}`)
	req := buildAuthedRequest(http.MethodPost, "/api/v1/sessions/manual", body, orgID, &user, nil)

	rec := httptest.NewRecorder()
	handler.CreateManual(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, "create manual should return 201, body=%s", rec.Body.String())

	var resp struct {
		Data models.Session `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	createdID := resp.Data.ID
	require.NotEqual(t, "", createdID.String())
	require.Equal(t, "pending", resp.Data.Status, "newly-created manual session must be pending so the worker picks it up")
	require.Equal(t, models.SessionOriginManual, resp.Data.Origin)
	require.Equal(t, models.SessionInteractionModeInteractive, resp.Data.InteractionMode)
	require.NotNil(t, resp.Data.TriggeredByUserID)
	require.Equal(t, user.ID, *resp.Data.TriggeredByUserID)

	// 1. Session row exists in DB and matches the response.
	stored, err := db.NewSessionStore(pool).GetByID(context.Background(), orgID, createdID)
	require.NoError(t, err)
	require.Equal(t, "pending", stored.Status)
	require.Equal(t, models.SessionOriginManual, stored.Origin)

	// 2. Initial turn-0 user message persisted (so attachments / commands
	//    show up in the timeline alongside the prompt). A future refactor
	//    that drops this on the floor would turn the chat history into a
	//    list that starts at turn 1 — UX regression with no test signal
	//    today.
	msgs, err := db.NewSessionMessageStore(pool).ListBySession(context.Background(), orgID, createdID)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Equal(t, 0, msgs[0].TurnNumber)
	require.Equal(t, models.MessageRoleUser, msgs[0].Role)
	require.Equal(t, "Refactor the cache key derivation in pkg/cache", msgs[0].Content)

	// 3. run_agent job in queue. The worker picks up by job_type — drift
	//    here means the session sits in pending forever in production.
	jobs := listJobs(t, pool, orgID)
	require.Len(t, jobs, 1)
	job := jobs[0]
	require.Equal(t, "run_agent", job.JobType,
		"job type drift will silently break new-session start")
	require.Equal(t, "agent", job.Queue)
	require.Equal(t, "pending", job.Status)
	require.Equal(t, createdID.String(), payloadField(t, job.Payload, "session_id"))
	require.Equal(t, orgID.String(), payloadField(t, job.Payload, "org_id"))
}

// TestIntegration_CreateManual_RejectsEmptyMessage anchors the input
// validation contract. The handler trims whitespace then checks emptiness;
// we intentionally exercise the "all whitespace" case rather than the empty
// string because that's the more interesting failure mode (a frontend bug
// that submits stray newlines should surface as MISSING_MESSAGE, not as a
// session with no real prompt).
func TestIntegration_CreateManual_RejectsEmptyMessage(t *testing.T) {
	pool := setup(t)

	orgID := seedOrg(t, pool)
	user := seedUser(t, pool, orgID)

	handler := newTestSessionHandler(pool)

	body := strings.NewReader(`{"message":"   \n   "}`)
	req := buildAuthedRequest(http.MethodPost, "/api/v1/sessions/manual", body, orgID, &user, nil)

	rec := httptest.NewRecorder()
	handler.CreateManual(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "MISSING_MESSAGE")

	// Negative-space assertion: a rejected request must not commit anything.
	jobs := listJobs(t, pool, orgID)
	require.Empty(t, jobs)
}

func TestIntegration_CreateManual_AllowsImageOnlyStart(t *testing.T) {
	pool := setup(t)

	orgID := seedOrg(t, pool)
	user := seedUser(t, pool, orgID)

	handler := newTestSessionHandler(pool)

	body := strings.NewReader(`{"message":"   ","images":["https://example.com/uploaded-shot.png"]}`)
	req := buildAuthedRequest(http.MethodPost, "/api/v1/sessions/manual", body, orgID, &user, nil)

	rec := httptest.NewRecorder()
	handler.CreateManual(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, "image-only manual create should return 201, body=%s", rec.Body.String())

	var resp struct {
		Data models.Session `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "image-only create should return a valid session response")
	require.NotNil(t, resp.Data.Title, "image-only create should apply the placeholder title")
	require.Equal(t, "Manual Session", *resp.Data.Title, "image-only create should use the placeholder title until text arrives")

	msgs, err := db.NewSessionMessageStore(pool).ListBySession(context.Background(), orgID, resp.Data.ID)
	require.NoError(t, err, "image-only create should persist the turn-0 user message")
	require.Len(t, msgs, 1, "image-only create should persist exactly one initial user message")
	require.Equal(t, "", msgs[0].Content, "image-only create should keep the initial message body empty")
	require.Equal(t, []string{"https://example.com/uploaded-shot.png"}, msgs[0].Attachments, "image-only create should persist the uploaded image on the initial message")

	jobs := listJobs(t, pool, orgID)
	require.Len(t, jobs, 1, "image-only create should still enqueue a run_agent job")
	require.Equal(t, resp.Data.ID.String(), payloadField(t, jobs[0].Payload, "session_id"), "run_agent payload should target the created image-only session")
}
