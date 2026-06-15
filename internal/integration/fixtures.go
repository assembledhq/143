//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/cluster"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// seedOrg inserts a minimal Organization row and returns its ID. Settings is
// an empty JSON object so OrgSettings parsing inside handlers (CreateManual)
// gets sensible defaults instead of a NULL/parse error.
func seedOrg(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	store := db.NewOrganizationStore(pool)
	org := &models.Organization{
		Name:     "integration-org-" + uuid.NewString()[:8],
		Settings: json.RawMessage(`{}`),
	}
	if err := store.Create(context.Background(), org); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	return org.ID
}

// seedUser inserts a member-role user belonging to the given org. The user's
// ID is what handlers like SendMessage stamp onto messages and audit events
// via middleware.UserFromContext, so most tests want a real user even when
// they don't authenticate. Inserts via raw SQL: UserStore.UpsertFromGitHub
// requires a GitHub ID we don't want to fabricate, and Email-based register
// flows go through the handler we'd rather not depend on transitively.
func seedUser(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) models.User {
	t.Helper()
	user := models.User{
		OrgID: orgID,
		Email: fmt.Sprintf("integration-%s@test.local", uuid.NewString()[:8]),
		Name:  "Integration Test User",
		Role:  "member",
	}
	err := pool.QueryRow(context.Background(), `
		INSERT INTO users (org_id, email, name, role)
		VALUES ($1, $2, $3, $4)
		RETURNING id, created_at
	`, user.OrgID, user.Email, user.Name, user.Role).Scan(&user.ID, &user.CreatedAt)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return user
}

// seedWorkerNode registers the worker node precondition required by the real
// claim path. Production workers are wrapped by NodeManager heartbeat; these
// integration tests construct Worker directly, so they must provide the active
// nodes row explicitly.
func seedWorkerNode(t *testing.T, pool *pgxpool.Pool, nodeID string) {
	t.Helper()
	manager := cluster.NewNodeManager(pool, zerolog.Nop(), nodeID, "worker")
	if err := manager.Register(context.Background(), "integration-test-host"); err != nil {
		t.Fatalf("seed worker node: %v", err)
	}
}

// setNodeStatus forces a seeded node into a status (e.g. 'draining') while
// leaving its heartbeat fresh — the state a rolling deploy leaves behind.
func setNodeStatus(t *testing.T, pool *pgxpool.Pool, nodeID, status string) {
	t.Helper()
	tag, err := pool.Exec(context.Background(),
		`UPDATE nodes SET status = $1 WHERE id = $2`, status, nodeID)
	if err != nil {
		t.Fatalf("set node %s status=%s: %v", nodeID, status, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("set node status: expected to update 1 row, updated %d (node %s seeded?)", tag.RowsAffected(), nodeID)
	}
}

// sessionOpts tunes which fields seedSession overrides on the default session
// row. Zero-valued fields fall back to sensible defaults that make the
// resulting row a plausible target for the handlers under test (manual
// session, idle, ready to receive a follow-up).
type sessionOpts struct {
	Status       models.SessionStatus
	CurrentTurn  int
	Origin       models.SessionOrigin
	Interaction  models.SessionInteractionMode
	Validation   models.SessionValidationPolicy
	AgentType    models.AgentType
	RepositoryID *uuid.UUID
}

// seedSession creates a session via the real SessionStore so any future
// invariants enforced by Create (default origin, interaction mode, link
// inserts) are honored. Returns the freshly persisted session — callers
// should treat the returned struct as authoritative for ID / CreatedAt.
func seedSession(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, opts sessionOpts) models.Session {
	t.Helper()
	if opts.Status == "" {
		opts.Status = models.SessionStatusIdle
	}
	if opts.Origin == "" {
		opts.Origin = models.SessionOriginManual
	}
	if opts.Interaction == "" {
		opts.Interaction = models.SessionInteractionModeInteractive
	}
	if opts.Validation == "" {
		opts.Validation = models.SessionValidationPolicyOnSessionEnd
	}
	if opts.AgentType == "" {
		opts.AgentType = models.DefaultDefaultAgentType
	}

	title := "integration test session"
	session := &models.Session{
		OrgID:            orgID,
		AgentType:        opts.AgentType,
		Status:           opts.Status,
		AutonomyLevel:    models.DefaultSessionAutonomy,
		TokenMode:        models.SessionTokenModeLow,
		Origin:           opts.Origin,
		InteractionMode:  opts.Interaction,
		ValidationPolicy: opts.Validation,
		Title:            &title,
		PMApproach:       &title,
		RepositoryID:     opts.RepositoryID,
	}
	store := db.NewSessionStore(pool)
	if err := store.Create(context.Background(), session); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	// SessionStore.Create always inserts as 'pending' implicitly via the
	// caller-supplied Status field — callers asking for non-pending need a
	// follow-up update. CurrentTurn gets bumped by the orchestrator at
	// runtime, so tests that need a specific turn (e.g. "send message to
	// session that's already had two turns") set it via UPDATE here.
	if session.Status != opts.Status || session.CurrentTurn != opts.CurrentTurn {
		_, err := pool.Exec(context.Background(), `
			UPDATE sessions
			SET status = $1, current_turn = $2
			WHERE id = $3 AND org_id = $4
		`, opts.Status, opts.CurrentTurn, session.ID, orgID)
		if err != nil {
			t.Fatalf("seed session status update: %v", err)
		}
		session.Status = opts.Status
		session.CurrentTurn = opts.CurrentTurn
	}

	return *session
}

// jobRow is the subset of the jobs row that integration tests assert against.
// Reading the full models.Job pulls in lease/owner fields irrelevant to the
// handler→DB seam we are testing.
type jobRow struct {
	ID       uuid.UUID
	OrgID    uuid.UUID
	Queue    string
	JobType  string
	Status   string
	Priority int
	Payload  json.RawMessage
}

// listJobs returns every job row for the given org, ordered oldest-first.
// Tests assert exact counts and types; ordering keeps assertions deterministic.
func listJobs(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) []jobRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT id, org_id, queue, job_type, status, priority, payload
		FROM jobs
		WHERE org_id = $1
		ORDER BY created_at ASC, id ASC
	`, orgID)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	defer rows.Close()

	var out []jobRow
	for rows.Next() {
		var j jobRow
		if err := rows.Scan(&j.ID, &j.OrgID, &j.Queue, &j.JobType, &j.Status, &j.Priority, &j.Payload); err != nil {
			t.Fatalf("scan job row: %v", err)
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate jobs: %v", err)
	}
	return out
}

// payloadField extracts a single string field from a job's JSON payload.
// Fails the test if the field is missing or not a string — the handlers
// under test always write string fields for session_id / org_id, so a missing
// or wrong-typed field is a regression worth surfacing loudly.
func payloadField(t *testing.T, payload json.RawMessage, key string) string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal(payload, &m); err != nil {
		t.Fatalf("unmarshal job payload: %v (raw=%s)", err, string(payload))
	}
	val, ok := m[key]
	if !ok {
		t.Fatalf("job payload missing %q (raw=%s)", key, string(payload))
	}
	return val
}
