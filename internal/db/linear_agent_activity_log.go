package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// LinearAgentActivityLog is one row per Linear AgentActivity we've emitted.
// Combined with the (agent_session_row_id, idem_key) UNIQUE constraint this
// gives the writer at-most-once semantics — concurrent milestone fan-outs
// for the same idem_key collide and the loser short-circuits.
type LinearAgentActivityLog struct {
	ID                uuid.UUID                      `db:"id" json:"id"`
	OrgID             uuid.UUID                      `db:"org_id" json:"org_id"`
	AgentSessionRowID uuid.UUID                      `db:"agent_session_row_id" json:"agent_session_row_id"`
	IdemKey           string                         `db:"idem_key" json:"idem_key"`
	ActivityType      models.LinearAgentActivityType `db:"activity_type" json:"activity_type"`
	LinearActivityID  string                         `db:"linear_activity_id" json:"linear_activity_id,omitempty"`
	CreatedAt         time.Time                      `db:"created_at" json:"created_at"`
}

// LinearAgentActivityLogStore reads and writes the per-activity audit log.
type LinearAgentActivityLogStore struct {
	db DBTX
}

func NewLinearAgentActivityLogStore(db DBTX) *LinearAgentActivityLogStore {
	return &LinearAgentActivityLogStore{db: db}
}

// ReserveResult communicates the outcome of a Reserve attempt. The writer
// uses this to decide whether to actually call Linear's GraphQL API:
//   - Reserved=true  → we won the race; emit to Linear, then call Complete.
//   - Reserved=false → another caller already emitted; short-circuit.
type ReserveResult struct {
	Reserved bool
	RowID    uuid.UUID
}

// Reserve atomically claims the (agent_session, idem_key) slot for emission.
// Returns Reserved=true when the row was freshly inserted, Reserved=false
// when it already existed (the previous emit succeeded). The two-phase
// design (Reserve → emit to Linear → Complete) is intentional: a one-phase
// "INSERT with linear_activity_id from the response" would require two
// network round-trips inside the DB transaction, holding the row lock for
// the full Linear API latency. The split keeps the lock window short.
//
// Failure model: if Reserve succeeds but the Linear emit fails, the row
// stays present without a linear_activity_id. The next attempt will see
// Reserved=false (UNIQUE collision) and short-circuit, even though Linear
// never received the activity. This is the right tradeoff for milestone
// activities — duplicate "PR merged" thoughts are far worse than a missing
// one, and milestones are already reported through the durable attachment.
// For elicitation activities (where missing emission is more visible) the
// caller should use ReserveAndComplete instead, which keeps the row only
// when the emit succeeds.
func (s *LinearAgentActivityLogStore) Reserve(ctx context.Context, orgID, agentSessionRowID uuid.UUID, idemKey string, activityType models.LinearAgentActivityType) (ReserveResult, error) {
	if err := activityType.Validate(); err != nil {
		return ReserveResult{}, err
	}
	if idemKey == "" {
		return ReserveResult{}, errors.New("idem_key is required")
	}
	if orgID == uuid.Nil {
		return ReserveResult{}, errors.New("org_id is required")
	}

	var (
		rowID    uuid.UUID
		inserted bool
	)
	err := s.db.QueryRow(ctx, `
		INSERT INTO linear_agent_activity_log
			(org_id, agent_session_row_id, idem_key, activity_type)
		VALUES (@org_id, @agent_session_row_id, @idem_key, @activity_type)
		ON CONFLICT (agent_session_row_id, idem_key) DO UPDATE
		SET idem_key = EXCLUDED.idem_key  -- no-op so RETURNING fires
		WHERE linear_agent_activity_log.org_id = EXCLUDED.org_id
		RETURNING id, (xmax = 0) AS inserted`,
		pgx.NamedArgs{
			"org_id":               orgID,
			"agent_session_row_id": agentSessionRowID,
			"idem_key":             idemKey,
			"activity_type":        string(activityType),
		}).Scan(&rowID, &inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		// The conflicting row's org_id didn't match the caller's — a
		// buggy caller is trying to reserve against another org's
		// agent_session_row_id. FK to linear_agent_sessions(id) already
		// makes this nearly impossible, but defense in depth: refuse
		// loudly rather than silently update someone else's row.
		return ReserveResult{}, fmt.Errorf("reserve linear_agent_activity_log: cross-org collision on (agent_session_row_id=%s, idem_key=%s)", agentSessionRowID, idemKey)
	}
	if err != nil {
		return ReserveResult{}, fmt.Errorf("reserve linear_agent_activity_log: %w", err)
	}
	return ReserveResult{Reserved: inserted, RowID: rowID}, nil
}

// Complete records the Linear-returned activity id on a previously-reserved
// row. Callers invoke this after the GraphQL emit succeeds. If the emit
// failed, callers should leave the row in place (Reserved-but-incomplete)
// — see Reserve's failure-model comment for the rationale.
func (s *LinearAgentActivityLogStore) Complete(ctx context.Context, orgID, rowID uuid.UUID, linearActivityID string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE linear_agent_activity_log
		SET linear_activity_id = @linear_activity_id
		WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{
			"id":                 rowID,
			"org_id":             orgID,
			"linear_activity_id": linearActivityID,
		})
	if err != nil {
		return fmt.Errorf("complete linear_agent_activity_log: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errors.New("complete: linear_agent_activity_log row not found")
	}
	return nil
}

// Discard removes a reserved-but-not-completed row. Used by elicitation
// emits that want stricter semantics than milestone emits: if the Linear
// call fails, the slot is freed so the next replay actually re-emits
// rather than short-circuiting on the corpse row.
//
// Zero rows affected is normal: it means either (a) the row was already
// completed (linear_activity_id NOT NULL — Linear actually succeeded
// after all), (b) a concurrent Discard freed the slot first, or (c) the
// row was for a different org. None of those are caller bugs the writer
// can recover from, and signaling them as errors would force callers to
// special-case the benign races. Callers that genuinely need to know
// whether *they* freed the slot should use DiscardByIdemKey and check
// rows-affected themselves at the call site.
func (s *LinearAgentActivityLogStore) Discard(ctx context.Context, orgID, rowID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `
		DELETE FROM linear_agent_activity_log
		WHERE id = @id AND org_id = @org_id AND linear_activity_id IS NULL`,
		pgx.NamedArgs{"id": rowID, "org_id": orgID})
	if err != nil {
		return fmt.Errorf("discard linear_agent_activity_log: %w", err)
	}
	return nil
}

// DiscardByIdemKey removes a reserved-but-not-completed row addressed by
// (agent_session_row, idem_key) — the same UNIQUE that Reserve writes
// against. Lets the writer's strict-semantics path free a slot without
// first listing every activity on the session. Race-safe: a concurrent
// successful Complete sets linear_activity_id non-NULL and the predicate
// turns this into a no-op.
func (s *LinearAgentActivityLogStore) DiscardByIdemKey(ctx context.Context, orgID, agentSessionRowID uuid.UUID, idemKey string) error {
	if idemKey == "" {
		return errors.New("idem_key is required")
	}
	_, err := s.db.Exec(ctx, `
		DELETE FROM linear_agent_activity_log
		WHERE org_id = @org_id
		  AND agent_session_row_id = @agent_session_row_id
		  AND idem_key = @idem_key
		  AND linear_activity_id IS NULL`,
		pgx.NamedArgs{
			"org_id":               orgID,
			"agent_session_row_id": agentSessionRowID,
			"idem_key":             idemKey,
		})
	if err != nil {
		return fmt.Errorf("discard linear_agent_activity_log by idem_key: %w", err)
	}
	return nil
}

// ListForAgentSession returns activities for an AgentSession in chronological
// order. Used by the operator debug surface and by replay-on-reconnect logic
// that wants to confirm what's already been emitted.
func (s *LinearAgentActivityLogStore) ListForAgentSession(ctx context.Context, orgID, agentSessionRowID uuid.UUID) ([]LinearAgentActivityLog, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, org_id, agent_session_row_id, idem_key, activity_type,
		       COALESCE(linear_activity_id, '') AS linear_activity_id, created_at
		FROM linear_agent_activity_log
		WHERE org_id = @org_id
		  AND agent_session_row_id = @agent_session_row_id
		ORDER BY created_at ASC`,
		pgx.NamedArgs{
			"org_id":               orgID,
			"agent_session_row_id": agentSessionRowID,
		})
	if err != nil {
		return nil, fmt.Errorf("list linear_agent_activity_log: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[LinearAgentActivityLog])
}
