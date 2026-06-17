package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrCapabilityAlreadyGranted is returned by AppendApprovedSessionGrant when
// the requested capability is already present in the session snapshot.
var ErrCapabilityAlreadyGranted = errors.New("capability already granted")

// ErrAutomationNotFound is returned when a capability policy write targets an
// automation UUID that does not exist in the given org.
var ErrAutomationNotFound = errors.New("automation not found")

type AgentCapabilityPolicyStore struct {
	db TxStarter
}

func NewAgentCapabilityPolicyStore(db TxStarter) *AgentCapabilityPolicyStore {
	return &AgentCapabilityPolicyStore{db: db}
}

const agentCapabilityPolicyColumns = `id, org_id, policy_type, automation_id, name, active, created_by, created_at`
const agentCapabilityGrantColumns = `id, org_id, policy_id, capability_id, access_level, enabled, config, created_by, created_at`

func (s *AgentCapabilityPolicyStore) GetSessionDefaultPolicy(ctx context.Context, orgID uuid.UUID) (models.AgentCapabilityPolicy, error) {
	policy, err := s.getPolicy(ctx, `
		SELECT `+agentCapabilityPolicyColumns+`
		FROM agent_capability_policies
		WHERE org_id = @org_id
		  AND policy_type = 'session_default'
		  AND active = true`,
		pgx.NamedArgs{"org_id": orgID},
	)
	if err != nil {
		return models.AgentCapabilityPolicy{}, err
	}
	return s.withGrants(ctx, policy)
}

func (s *AgentCapabilityPolicyStore) GetAutomationPolicy(ctx context.Context, orgID, automationID uuid.UUID) (models.AgentCapabilityPolicy, error) {
	policy, err := s.getPolicy(ctx, `
		SELECT p.id, p.org_id, p.policy_type, p.automation_id, p.name, p.active, p.created_by, p.created_at
		FROM agent_capability_policies p
		JOIN automations a ON a.id = p.automation_id AND a.org_id = p.org_id
		WHERE p.org_id = @org_id
		  AND p.automation_id = @automation_id
		  AND p.policy_type = 'automation'
		  AND p.active = true
		  AND a.deleted_at IS NULL`,
		pgx.NamedArgs{"org_id": orgID, "automation_id": automationID},
	)
	if err != nil {
		return models.AgentCapabilityPolicy{}, err
	}
	return s.withGrants(ctx, policy)
}

func (s *AgentCapabilityPolicyStore) getPolicy(ctx context.Context, query string, args pgx.NamedArgs) (models.AgentCapabilityPolicy, error) {
	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return models.AgentCapabilityPolicy{}, err
	}
	policy, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.AgentCapabilityPolicy])
	if err != nil {
		return models.AgentCapabilityPolicy{}, err
	}
	return policy, nil
}

func (s *AgentCapabilityPolicyStore) withGrants(ctx context.Context, policy models.AgentCapabilityPolicy) (models.AgentCapabilityPolicy, error) {
	grants, err := s.ListGrantsByPolicy(ctx, policy.OrgID, policy.ID)
	if err != nil {
		return models.AgentCapabilityPolicy{}, err
	}
	policy.Grants = grants
	return policy, nil
}

func (s *AgentCapabilityPolicyStore) ListGrantsByPolicy(ctx context.Context, orgID, policyID uuid.UUID) ([]models.AgentCapabilityGrant, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+agentCapabilityGrantColumns+`
		FROM agent_capability_policy_grants
		WHERE org_id = @org_id AND policy_id = @policy_id
		ORDER BY capability_id ASC`,
		pgx.NamedArgs{"org_id": orgID, "policy_id": policyID},
	)
	if err != nil {
		return nil, fmt.Errorf("query agent capability grants: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.AgentCapabilityGrant])
}

func (s *AgentCapabilityPolicyStore) UpdateSessionDefaultPolicy(ctx context.Context, orgID uuid.UUID, createdBy *uuid.UUID, grants []models.AgentCapabilityPolicyGrantInput) (uuid.UUID, error) {
	return s.replacePolicy(ctx, orgID, nil, models.AgentCapabilityPolicyTypeSessionDefault, createdBy, grants)
}

func (s *AgentCapabilityPolicyStore) ReplaceAutomationPolicy(ctx context.Context, orgID, automationID uuid.UUID, createdBy *uuid.UUID, grants []models.AgentCapabilityPolicyGrantInput) (uuid.UUID, error) {
	var exists bool
	if err := s.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM automations
			WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL
		)`,
		pgx.NamedArgs{"id": automationID, "org_id": orgID},
	).Scan(&exists); err != nil {
		return uuid.Nil, fmt.Errorf("check automation ownership: %w", err)
	}
	if !exists {
		return uuid.Nil, ErrAutomationNotFound
	}
	return s.replacePolicy(ctx, orgID, &automationID, models.AgentCapabilityPolicyTypeAutomation, createdBy, grants)
}

func (s *AgentCapabilityPolicyStore) replacePolicy(ctx context.Context, orgID uuid.UUID, automationID *uuid.UUID, policyType models.AgentCapabilityPolicyType, createdBy *uuid.UUID, grants []models.AgentCapabilityPolicyGrantInput) (uuid.UUID, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("begin capability policy tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	args := pgx.NamedArgs{"org_id": orgID, "policy_type": string(policyType)}
	deactivateSQL := `UPDATE agent_capability_policies SET active = false
		WHERE org_id = @org_id AND policy_type = @policy_type AND active = true`
	if policyType == models.AgentCapabilityPolicyTypeAutomation {
		deactivateSQL = `UPDATE agent_capability_policies SET active = false
			WHERE org_id = @org_id AND automation_id = @automation_id AND policy_type = @policy_type AND active = true`
		args["automation_id"] = *automationID
	}
	if _, err := tx.Exec(ctx, deactivateSQL, args); err != nil {
		return uuid.Nil, fmt.Errorf("deactivate capability policy: %w", err)
	}

	var policyID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent_capability_policies (org_id, policy_type, automation_id, created_by)
		VALUES (@org_id, @policy_type, @automation_id, @created_by)
		RETURNING id`,
		pgx.NamedArgs{
			"org_id":        orgID,
			"policy_type":   policyType,
			"automation_id": automationID,
			"created_by":    createdBy,
		},
	).Scan(&policyID); err != nil {
		return uuid.Nil, fmt.Errorf("insert capability policy: %w", err)
	}

	if len(grants) > 0 {
		for _, grant := range grants {
			config := grant.Config
			if len(config) == 0 {
				config = json.RawMessage(`{}`)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO agent_capability_policy_grants (
					org_id, policy_id, capability_id, access_level, enabled, config, created_by
				) VALUES (
					@org_id, @policy_id, @capability_id, @access_level, @enabled, @config, @created_by
				)`,
				pgx.NamedArgs{
					"org_id":        orgID,
					"policy_id":     policyID,
					"capability_id": grant.CapabilityID,
					"access_level":  grant.AccessLevel,
					"enabled":       grant.Enabled,
					"config":        config,
					"created_by":    createdBy,
				}); err != nil {
				return uuid.Nil, fmt.Errorf("insert capability grant: %w", err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, fmt.Errorf("commit capability policy tx: %w", err)
	}
	return policyID, nil
}

func (s *AgentCapabilityPolicyStore) AppendApprovedSessionGrant(ctx context.Context, orgID, sessionID uuid.UUID, item models.AgentCapabilitySnapshotItem) ([]models.AgentCapabilitySnapshotItem, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin append capability grant tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var snapshot []models.AgentCapabilitySnapshotItem
	var automationRunID *uuid.UUID
	rows, err := tx.Query(ctx, `
		SELECT capability_snapshot, automation_run_id
		FROM sessions
		WHERE org_id = @org_id AND id = @session_id AND deleted_at IS NULL
		FOR UPDATE`,
		pgx.NamedArgs{"org_id": orgID, "session_id": sessionID},
	)
	if err != nil {
		return nil, fmt.Errorf("lock session capability snapshot: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByPos[struct {
		Snapshot        []models.AgentCapabilitySnapshotItem
		AutomationRunID *uuid.UUID
	}])
	if err != nil {
		return nil, err
	}
	snapshot = row.Snapshot
	automationRunID = row.AutomationRunID

	for _, existing := range snapshot {
		if existing.ID == item.ID {
			return snapshot, fmt.Errorf("%w: %q", ErrCapabilityAlreadyGranted, item.ID)
		}
	}
	snapshot = append(snapshot, item)
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("marshal capability snapshot: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE sessions
		SET capability_snapshot = @snapshot
		WHERE org_id = @org_id AND id = @session_id`,
		pgx.NamedArgs{"snapshot": raw, "org_id": orgID, "session_id": sessionID},
	); err != nil {
		return nil, fmt.Errorf("update session capability snapshot: %w", err)
	}
	if automationRunID != nil {
		if _, err := tx.Exec(ctx, `
			UPDATE automation_runs
			SET capability_snapshot = @snapshot
			WHERE org_id = @org_id AND id = @run_id`,
			pgx.NamedArgs{"snapshot": raw, "org_id": orgID, "run_id": *automationRunID},
		); err != nil {
			return nil, fmt.Errorf("update automation run capability snapshot: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit append capability grant tx: %w", err)
	}
	return snapshot, nil
}
