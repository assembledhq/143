package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Bypass eligibility errors. These let callers (the API layer) distinguish a
// legitimate policy/eligibility rejection — which should surface as a 409 — from
// an unexpected internal failure, which should surface as a 500 rather than
// masquerading as "not allowed".
var (
	// ErrBypassNotAllowed means policy forbids the bypass (role not permitted,
	// scope missing, or the check is configured non-bypassable).
	ErrBypassNotAllowed = errors.New("PR readiness bypass is not allowed")
	// ErrBypassNotEligible means the run's current state has nothing bypassable
	// (still running, not blocked, no eligible blocking checks, or missing reason).
	ErrBypassNotEligible = errors.New("PR readiness run is not eligible for bypass")
)

type PRReadinessStore struct {
	db DBTX
}

func NewPRReadinessStore(db DBTX) *PRReadinessStore {
	return &PRReadinessStore{db: db}
}

const prReadinessRunColumns = `id, org_id, session_id, changeset_id, repository_id, status,
	evaluated_workspace_revision, evaluated_snapshot_key, evaluated_head_sha, summary, review_packet,
	triggered_by_user_id, started_at, completed_at, created_at, updated_at`

const prReadinessCheckColumns = `id, org_id, run_id, session_id, changeset_id, check_type, status,
	enforcement, title, summary, details, action, created_at,
	check_key, enforcement_builder, enforcement_engineer, enforcement_admin, provenance, source`

func (s *PRReadinessStore) CreateRun(ctx context.Context, run *models.PRReadinessRun) error {
	if err := run.Status.Validate(); err != nil {
		return err
	}
	if run.ChangesetID == uuid.Nil {
		err := s.db.QueryRow(ctx, `INSERT INTO pr_readiness_runs (
			org_id, session_id, repository_id, status, evaluated_workspace_revision,
			evaluated_snapshot_key, summary, review_packet, triggered_by_user_id
		) VALUES (
			@org_id, @session_id, @repository_id, @status, @evaluated_workspace_revision,
			@evaluated_snapshot_key, @summary, @review_packet, @triggered_by_user_id
		) RETURNING id, changeset_id, started_at, created_at, updated_at`, pgx.NamedArgs{
			"org_id": run.OrgID, "session_id": run.SessionID, "repository_id": run.RepositoryID,
			"status": run.Status, "evaluated_workspace_revision": run.EvaluatedWorkspaceRevision,
			"evaluated_snapshot_key": run.EvaluatedSnapshotKey, "summary": run.Summary,
			"review_packet": run.ReviewPacket, "triggered_by_user_id": run.TriggeredByUserID,
		}).Scan(&run.ID, &run.ChangesetID, &run.StartedAt, &run.CreatedAt, &run.UpdatedAt)
		if err != nil {
			return fmt.Errorf("create PR readiness run: %w", err)
		}
		return nil
	}
	query := `
		INSERT INTO pr_readiness_runs (
			org_id, session_id, changeset_id, repository_id, status, evaluated_workspace_revision,
			evaluated_snapshot_key, evaluated_head_sha, summary, review_packet, triggered_by_user_id
		) VALUES (
			@org_id, @session_id, COALESCE(NULLIF(@changeset_id, '00000000-0000-0000-0000-000000000000'::uuid),
				(SELECT id FROM session_changesets WHERE org_id = @org_id AND session_id = @session_id AND is_primary)),
			@repository_id, @status, @evaluated_workspace_revision,
			@evaluated_snapshot_key, @evaluated_head_sha, @summary, @review_packet, @triggered_by_user_id
		)
		RETURNING id, changeset_id, started_at, created_at, updated_at`
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":                       run.OrgID,
		"session_id":                   run.SessionID,
		"changeset_id":                 run.ChangesetID,
		"repository_id":                run.RepositoryID,
		"status":                       run.Status,
		"evaluated_workspace_revision": run.EvaluatedWorkspaceRevision,
		"evaluated_snapshot_key":       run.EvaluatedSnapshotKey,
		"evaluated_head_sha":           run.EvaluatedHeadSHA,
		"summary":                      run.Summary,
		"review_packet":                run.ReviewPacket,
		"triggered_by_user_id":         run.TriggeredByUserID,
	}).Scan(&run.ID, &run.ChangesetID, &run.StartedAt, &run.CreatedAt, &run.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create PR readiness run: %w", err)
	}
	return nil
}

func (s *PRReadinessStore) MarkRunning(ctx context.Context, orgID, runID uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE pr_readiness_runs
		SET status = 'running', updated_at = now()
		WHERE org_id = @org_id AND id = @id
		  AND status IN ('queued', 'running')`, pgx.NamedArgs{
		"org_id": orgID,
		"id":     runID,
	})
	if err != nil {
		return fmt.Errorf("mark PR readiness running: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *PRReadinessStore) MarkFailed(ctx context.Context, orgID, runID uuid.UUID, summary string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE pr_readiness_runs
		SET status = 'failed',
		    summary = @summary,
		    completed_at = COALESCE(completed_at, now()),
		    updated_at = now()
		WHERE org_id = @org_id
		  AND id = @id
		  AND status IN ('queued', 'running')`, pgx.NamedArgs{
		"org_id":  orgID,
		"id":      runID,
		"summary": summary,
	})
	if err != nil {
		return fmt.Errorf("mark PR readiness failed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *PRReadinessStore) CompleteRunWithChecks(ctx context.Context, orgID uuid.UUID, runID uuid.UUID, result models.PRReadinessRun, checks []models.PRReadinessCheck) error {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return fmt.Errorf("complete PR readiness requires transaction support")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin PR readiness completion tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE pr_readiness_runs
		SET status = @status,
		    summary = @summary,
		    review_packet = @review_packet,
		    completed_at = now(),
		    updated_at = now()
		WHERE org_id = @org_id AND id = @id
		  AND status IN ('queued', 'running')`, pgx.NamedArgs{
		"org_id":        orgID,
		"id":            runID,
		"status":        result.Status,
		"summary":       result.Summary,
		"review_packet": result.ReviewPacket,
	})
	if err != nil {
		return fmt.Errorf("update PR readiness run: %w", err)
	}
	// 0 rows means the run was already terminal (a concurrent completion or a
	// dead-letter MarkFailed won). Bail without clobbering its checks.
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if _, err := tx.Exec(ctx, `DELETE FROM pr_readiness_checks WHERE org_id = @org_id AND run_id = @run_id`, pgx.NamedArgs{"org_id": orgID, "run_id": runID}); err != nil {
		return fmt.Errorf("replace PR readiness checks: %w", err)
	}
	for _, check := range checks {
		if err := check.CheckType.Validate(); err != nil {
			return err
		}
		if err := check.Status.Validate(); err != nil {
			return err
		}
		if err := check.Enforcement.Validate(); err != nil {
			return err
		}
		if check.CheckKey == "" {
			check.CheckKey = string(check.CheckType)
		}
		if check.EnforcementByRole == (models.PRReadinessEnforcementByRole{}) {
			check.EnforcementByRole = models.PRReadinessEnforcementByRole{
				Builder:  firstNonZeroReadinessEnforcement(check.EnforcementBuilder, check.Enforcement),
				Engineer: check.EnforcementEngineer,
				Admin:    check.EnforcementAdmin,
			}
		}
		if check.EnforcementByRole.Builder == "" {
			check.EnforcementByRole.Builder = check.Enforcement
		}
		if err := check.EnforcementByRole.Validate(); err != nil {
			return err
		}
		if check.Enforcement == "" {
			check.Enforcement = check.EnforcementByRole.Builder
		}
		if check.Provenance == "" {
			check.Provenance = models.PRReadinessProvenanceBuiltin
		}
		if err := check.Provenance.Validate(); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO pr_readiness_checks (
				org_id, run_id, session_id, changeset_id, check_key, check_type, status, enforcement,
				enforcement_builder, enforcement_engineer, enforcement_admin,
				provenance, source, title, summary, details, action
			) VALUES (
				@org_id, @run_id, @session_id, @changeset_id, @check_key, @check_type, @status, @enforcement,
				@enforcement_builder, @enforcement_engineer, @enforcement_admin,
				@provenance, @source, @title, @summary, @details, @action
			)`, pgx.NamedArgs{
			"org_id":               orgID,
			"run_id":               runID,
			"session_id":           check.SessionID,
			"changeset_id":         check.ChangesetID,
			"check_key":            check.CheckKey,
			"check_type":           check.CheckType,
			"status":               check.Status,
			"enforcement":          check.Enforcement,
			"enforcement_builder":  check.EnforcementByRole.Builder,
			"enforcement_engineer": check.EnforcementByRole.Engineer,
			"enforcement_admin":    check.EnforcementByRole.Admin,
			"provenance":           check.Provenance,
			"source":               check.Source,
			"title":                check.Title,
			"summary":              check.Summary,
			"details":              check.Details,
			"action":               check.Action,
		})
		if err != nil {
			return fmt.Errorf("insert PR readiness check: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit PR readiness completion tx: %w", err)
	}
	return nil
}

func (s *PRReadinessStore) GetLatestBySession(ctx context.Context, orgID, sessionID uuid.UUID) (*models.PRReadinessRun, error) {
	query := `SELECT ` + prReadinessRunColumns + ` FROM pr_readiness_runs
		WHERE org_id = @org_id AND session_id = @session_id
		  AND changeset_id = (SELECT id FROM session_changesets WHERE org_id = @org_id AND session_id = @session_id AND is_primary)
		ORDER BY created_at DESC, id DESC LIMIT 1`
	return s.getLatestRun(ctx, orgID, query, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID})
}

func (s *PRReadinessStore) GetLatestByChangeset(ctx context.Context, orgID, sessionID, changesetID uuid.UUID) (*models.PRReadinessRun, error) {
	query := `
		SELECT ` + prReadinessRunColumns + `
		FROM pr_readiness_runs
		WHERE org_id = @org_id AND session_id = @session_id AND changeset_id = @changeset_id
		ORDER BY created_at DESC, id DESC
		LIMIT 1`
	return s.getLatestRun(ctx, orgID, query, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID})
}

func (s *PRReadinessStore) getLatestRun(ctx context.Context, orgID uuid.UUID, query string, args pgx.NamedArgs) (*models.PRReadinessRun, error) {
	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query latest PR readiness run: %w", err)
	}
	run, err := pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[models.PRReadinessRun])
	if err != nil {
		return nil, err
	}
	checks, err := s.ListChecksByRun(ctx, orgID, run.ID)
	if err != nil {
		return nil, err
	}
	run.Checks = checks
	bypasses, err := s.ListBypassesByRun(ctx, orgID, run.ID)
	if err != nil {
		return nil, err
	}
	run.Bypasses = bypasses
	run.ReviewPacket = reviewPacketWithBypasses(run.ReviewPacket, bypasses)
	return &run, nil
}

func (s *PRReadinessStore) GetRunByID(ctx context.Context, orgID, runID uuid.UUID) (models.PRReadinessRun, error) {
	query := `
		SELECT ` + prReadinessRunColumns + `
		FROM pr_readiness_runs
		WHERE org_id = @org_id AND id = @id`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "id": runID})
	if err != nil {
		return models.PRReadinessRun{}, fmt.Errorf("query PR readiness run: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[models.PRReadinessRun])
}

func (s *PRReadinessStore) ListChecksByRun(ctx context.Context, orgID, runID uuid.UUID) ([]models.PRReadinessCheck, error) {
	query := `
		SELECT ` + prReadinessCheckColumns + `
		FROM pr_readiness_checks
		WHERE org_id = @org_id AND run_id = @run_id
		ORDER BY created_at ASC, id ASC`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "run_id": runID})
	if err != nil {
		return nil, fmt.Errorf("query PR readiness checks: %w", err)
	}
	checks, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[models.PRReadinessCheck])
	if err != nil {
		return nil, err
	}
	for i := range checks {
		checks[i].EnforcementByRole = models.PRReadinessEnforcementByRole{
			Builder:  firstNonZeroReadinessEnforcement(checks[i].EnforcementBuilder, checks[i].Enforcement),
			Engineer: checks[i].EnforcementEngineer,
			Admin:    checks[i].EnforcementAdmin,
		}
		if checks[i].CheckKey == "" {
			checks[i].CheckKey = string(checks[i].CheckType)
		}
	}
	return checks, nil
}

func (s *PRReadinessStore) ResolvePolicy(ctx context.Context, orgID uuid.UUID, repositoryID *uuid.UUID) (models.PRReadinessResolvedPolicy, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, org_id, repository_id, config, active, created_by_user_id, created_at
		FROM pr_readiness_policies
		WHERE org_id = @org_id
		  AND active = true
		  AND (repository_id IS NULL OR repository_id = @repository_id)
		ORDER BY CASE WHEN repository_id = @repository_id THEN 0 ELSE 1 END, created_at DESC, id DESC
		LIMIT 1`, pgx.NamedArgs{"org_id": orgID, "repository_id": repositoryID})
	if err != nil {
		return models.PRReadinessResolvedPolicy{}, fmt.Errorf("query PR readiness policy: %w", err)
	}
	record, err := collectOnePRReadinessPolicy(rows)
	if err != nil {
		if err == pgx.ErrNoRows {
			return models.PRReadinessResolvedPolicy{
				Config: models.ResolvePRReadinessPolicyConfig(nil, nil),
				Source: "default",
			}, nil
		}
		return models.PRReadinessResolvedPolicy{}, err
	}
	source := "organization"
	if record.RepositoryID != nil {
		source = "repository"
	}
	return models.PRReadinessResolvedPolicy{
		Config: record.Config,
		Source: source,
		Policy: &record,
	}, nil
}

func (s *PRReadinessStore) SavePolicy(ctx context.Context, orgID uuid.UUID, repositoryID *uuid.UUID, config models.PRReadinessPolicyConfig, createdByUserID *uuid.UUID) (models.PRReadinessPolicyRecord, error) {
	config = models.ResolvePRReadinessPolicyConfig(&config, nil)
	if err := config.Validate(); err != nil {
		return models.PRReadinessPolicyRecord{}, err
	}
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return models.PRReadinessPolicyRecord{}, fmt.Errorf("save PR readiness policy requires transaction support")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return models.PRReadinessPolicyRecord{}, fmt.Errorf("begin PR readiness policy tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		UPDATE pr_readiness_policies
		SET active = false
		WHERE org_id = @org_id
		  AND active = true
		  AND repository_id IS NOT DISTINCT FROM @repository_id`, pgx.NamedArgs{
		"org_id":        orgID,
		"repository_id": repositoryID,
	}); err != nil {
		return models.PRReadinessPolicyRecord{}, fmt.Errorf("inactivate PR readiness policy: %w", err)
	}
	configBytes, err := json.Marshal(config)
	if err != nil {
		return models.PRReadinessPolicyRecord{}, fmt.Errorf("marshal PR readiness policy: %w", err)
	}
	rows, err := tx.Query(ctx, `
		INSERT INTO pr_readiness_policies (org_id, repository_id, config, created_by_user_id)
		VALUES (@org_id, @repository_id, @config, @created_by_user_id)
		RETURNING id, org_id, repository_id, config, active, created_by_user_id, created_at`, pgx.NamedArgs{
		"org_id":             orgID,
		"repository_id":      repositoryID,
		"config":             configBytes,
		"created_by_user_id": createdByUserID,
	})
	if err != nil {
		return models.PRReadinessPolicyRecord{}, fmt.Errorf("insert PR readiness policy: %w", err)
	}
	record, err := collectOnePRReadinessPolicy(rows)
	if err != nil {
		return models.PRReadinessPolicyRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return models.PRReadinessPolicyRecord{}, fmt.Errorf("commit PR readiness policy tx: %w", err)
	}
	return record, nil
}

func (s *PRReadinessStore) CreateBypass(ctx context.Context, orgID, runID, userID uuid.UUID, reason string) (models.PRReadinessBypass, error) {
	return s.createBypass(ctx, orgID, runID, userID, reason, models.RoleBuilder, models.DefaultPRReadinessPolicyConfig())
}

func (s *PRReadinessStore) CreateBypassWithPolicy(ctx context.Context, orgID, runID, userID uuid.UUID, reason string, role models.Role, policy models.PRReadinessPolicyConfig) (models.PRReadinessBypass, error) {
	return s.createBypass(ctx, orgID, runID, userID, reason, role, policy)
}

func (s *PRReadinessStore) createBypass(ctx context.Context, orgID, runID, userID uuid.UUID, reason string, role models.Role, policy models.PRReadinessPolicyConfig) (models.PRReadinessBypass, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return models.PRReadinessBypass{}, fmt.Errorf("%w: reason is required", ErrBypassNotEligible)
	}
	if !policy.BypassAllowedFor(role) {
		return models.PRReadinessBypass{}, fmt.Errorf("%w: not enabled for %s", ErrBypassNotAllowed, role)
	}
	run, err := s.GetRunByID(ctx, orgID, runID)
	if err != nil {
		return models.PRReadinessBypass{}, err
	}
	if run.Status == models.PRReadinessRunStatusQueued || run.Status == models.PRReadinessRunStatusRunning {
		return models.PRReadinessBypass{}, fmt.Errorf("%w: readiness is still running", ErrBypassNotEligible)
	}
	if run.Status != models.PRReadinessRunStatusBlocked {
		return models.PRReadinessBypass{}, fmt.Errorf("%w: only completed blocking checks can be bypassed", ErrBypassNotEligible)
	}
	checks, err := s.ListChecksByRun(ctx, orgID, runID)
	if err != nil {
		return models.PRReadinessBypass{}, err
	}
	bypassedChecks := make([]string, 0)
	for _, check := range checks {
		if check.CheckKey == "" {
			check.CheckKey = string(check.CheckType)
		}
		enforcement := check.EnforcementByRole.EnforcementFor(role)
		if enforcement == models.PRReadinessEnforcementOff && role == models.RoleBuilder {
			enforcement = check.Enforcement
		}
		if enforcement == models.PRReadinessEnforcementBlocking &&
			(check.Status == models.PRReadinessCheckStatusFailed || check.Status == models.PRReadinessCheckStatusError) {
			if policy.IsCheckNonBypassable(check.CheckKey, check.CheckType) {
				return models.PRReadinessBypass{}, fmt.Errorf("%w: check %s is non-bypassable by policy", ErrBypassNotAllowed, check.CheckKey)
			}
			bypassedChecks = append(bypassedChecks, check.CheckKey)
		}
	}
	if len(bypassedChecks) == 0 {
		return models.PRReadinessBypass{}, fmt.Errorf("%w: no completed blocking checks are eligible", ErrBypassNotEligible)
	}
	checkBytes, err := json.Marshal(bypassedChecks)
	if err != nil {
		return models.PRReadinessBypass{}, fmt.Errorf("marshal PR readiness bypass checks: %w", err)
	}
	return s.insertBypass(ctx, orgID, run, userID, reason, checkBytes)
}

func (s *PRReadinessStore) insertBypass(ctx context.Context, orgID uuid.UUID, run models.PRReadinessRun, userID uuid.UUID, reason string, checkBytes []byte) (models.PRReadinessBypass, error) {
	var bypass models.PRReadinessBypass
	var bypassedChecks []byte
	if run.ChangesetID == uuid.Nil {
		err := s.db.QueryRow(ctx, `INSERT INTO pr_readiness_bypasses (
			org_id, readiness_run_id, session_id, repository_id, bypassed_by_user_id, reason, bypassed_checks
		) VALUES (@org_id, @readiness_run_id, @session_id, @repository_id, @bypassed_by_user_id, @reason, @bypassed_checks)
		RETURNING id, org_id, readiness_run_id, session_id, repository_id, pull_request_id, bypassed_by_user_id, reason, bypassed_checks, created_at`, pgx.NamedArgs{
			"org_id": orgID, "readiness_run_id": run.ID, "session_id": run.SessionID, "repository_id": run.RepositoryID,
			"bypassed_by_user_id": userID, "reason": reason, "bypassed_checks": checkBytes,
		}).Scan(&bypass.ID, &bypass.OrgID, &bypass.ReadinessRunID, &bypass.SessionID, &bypass.RepositoryID, &bypass.PullRequestID, &bypass.BypassedByUserID, &bypass.Reason, &bypassedChecks, &bypass.CreatedAt)
		if err != nil {
			return models.PRReadinessBypass{}, fmt.Errorf("insert PR readiness bypass: %w", err)
		}
		if err := json.Unmarshal(bypassedChecks, &bypass.BypassedChecks); err != nil {
			return models.PRReadinessBypass{}, fmt.Errorf("decode PR readiness bypass checks: %w", err)
		}
		return bypass, nil
	}
	err := s.db.QueryRow(ctx, `
		INSERT INTO pr_readiness_bypasses (
			org_id, readiness_run_id, session_id, changeset_id, repository_id, bypassed_by_user_id, reason, bypassed_checks
		) VALUES (
			@org_id, @readiness_run_id, @session_id, @changeset_id, @repository_id, @bypassed_by_user_id, @reason, @bypassed_checks
		)
		RETURNING id, org_id, readiness_run_id, session_id, changeset_id, repository_id, pull_request_id, bypassed_by_user_id, reason, bypassed_checks, created_at`, pgx.NamedArgs{
		"org_id":              orgID,
		"readiness_run_id":    run.ID,
		"session_id":          run.SessionID,
		"changeset_id":        run.ChangesetID,
		"repository_id":       run.RepositoryID,
		"bypassed_by_user_id": userID,
		"reason":              reason,
		"bypassed_checks":     checkBytes,
	}).Scan(&bypass.ID, &bypass.OrgID, &bypass.ReadinessRunID, &bypass.SessionID, &bypass.ChangesetID, &bypass.RepositoryID, &bypass.PullRequestID, &bypass.BypassedByUserID, &bypass.Reason, &bypassedChecks, &bypass.CreatedAt)
	if err != nil {
		return models.PRReadinessBypass{}, fmt.Errorf("insert PR readiness bypass: %w", err)
	}
	if err := json.Unmarshal(bypassedChecks, &bypass.BypassedChecks); err != nil {
		return models.PRReadinessBypass{}, fmt.Errorf("decode PR readiness bypass checks: %w", err)
	}
	return bypass, nil
}

func (s *PRReadinessStore) ListBypassesByRun(ctx context.Context, orgID, runID uuid.UUID) ([]models.PRReadinessBypass, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, org_id, readiness_run_id, session_id, repository_id, pull_request_id, bypassed_by_user_id, reason, bypassed_checks, created_at
		FROM pr_readiness_bypasses
		WHERE org_id = @org_id AND readiness_run_id = @run_id
		ORDER BY created_at ASC, id ASC`, pgx.NamedArgs{"org_id": orgID, "run_id": runID})
	if err != nil {
		return nil, fmt.Errorf("query PR readiness bypasses: %w", err)
	}
	defer rows.Close()
	bypasses := make([]models.PRReadinessBypass, 0)
	for rows.Next() {
		var bypass models.PRReadinessBypass
		var checks []byte
		if err := rows.Scan(&bypass.ID, &bypass.OrgID, &bypass.ReadinessRunID, &bypass.SessionID, &bypass.RepositoryID, &bypass.PullRequestID, &bypass.BypassedByUserID, &bypass.Reason, &checks, &bypass.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(checks, &bypass.BypassedChecks); err != nil {
			return nil, err
		}
		bypasses = append(bypasses, bypass)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return bypasses, nil
}

func (s *PRReadinessStore) AttachBypassesToPullRequest(ctx context.Context, orgID, runID, pullRequestID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `
		UPDATE pr_readiness_bypasses
		SET pull_request_id = @pull_request_id
		WHERE org_id = @org_id AND readiness_run_id = @run_id`, pgx.NamedArgs{
		"org_id":          orgID,
		"run_id":          runID,
		"pull_request_id": pullRequestID,
	})
	if err != nil {
		return fmt.Errorf("attach PR readiness bypasses to pull request: %w", err)
	}
	return nil
}

func (s *PRReadinessStore) ListBypassCounts(ctx context.Context, orgID uuid.UUID, repositoryID *uuid.UUID) (models.PRReadinessBypassCounts, error) {
	args := pgx.NamedArgs{"org_id": orgID, "repository_id": repositoryID}
	where := `WHERE org_id = @org_id AND (@repository_id::uuid IS NULL OR repository_id = @repository_id)`
	var counts models.PRReadinessBypassCounts
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM pr_readiness_bypasses `+where, args).Scan(&counts.Total); err != nil {
		return models.PRReadinessBypassCounts{}, fmt.Errorf("count PR readiness bypasses: %w", err)
	}
	byRepo, err := s.collectBypassCountRows(ctx, `
		SELECT COALESCE(repository_id::text, 'organization'), count(*)
		FROM pr_readiness_bypasses
		`+where+`
		GROUP BY repository_id
		ORDER BY count(*) DESC, COALESCE(repository_id::text, 'organization') ASC`, args)
	if err != nil {
		return models.PRReadinessBypassCounts{}, fmt.Errorf("count PR readiness bypasses by repository: %w", err)
	}
	byUser, err := s.collectBypassCountRows(ctx, `
		SELECT bypassed_by_user_id::text, count(*)
		FROM pr_readiness_bypasses
		`+where+`
		GROUP BY bypassed_by_user_id
		ORDER BY count(*) DESC, bypassed_by_user_id::text ASC`, args)
	if err != nil {
		return models.PRReadinessBypassCounts{}, fmt.Errorf("count PR readiness bypasses by user: %w", err)
	}
	byCheck, err := s.collectBypassCountRows(ctx, `
		SELECT check_key, count(*)
		FROM pr_readiness_bypasses b
		CROSS JOIN LATERAL jsonb_array_elements_text(b.bypassed_checks) AS check_key
		`+where+`
		GROUP BY check_key
		ORDER BY count(*) DESC, check_key ASC`, args)
	if err != nil {
		return models.PRReadinessBypassCounts{}, fmt.Errorf("count PR readiness bypasses by check: %w", err)
	}
	counts.ByRepository = byRepo
	counts.ByUser = byUser
	counts.ByCheck = byCheck
	return counts, nil
}

func (s *PRReadinessStore) collectBypassCountRows(ctx context.Context, query string, args pgx.NamedArgs) ([]models.PRReadinessBypassCount, error) {
	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make([]models.PRReadinessBypassCount, 0)
	for rows.Next() {
		var count models.PRReadinessBypassCount
		if err := rows.Scan(&count.Key, &count.Count); err != nil {
			return nil, err
		}
		counts = append(counts, count)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return counts, nil
}

func reviewPacketWithBypasses(packet json.RawMessage, bypasses []models.PRReadinessBypass) json.RawMessage {
	if len(packet) == 0 || string(packet) == "null" {
		packet = json.RawMessage(`{}`)
	}
	var data map[string]any
	if err := json.Unmarshal(packet, &data); err != nil {
		return packet
	}
	data["bypasses"] = bypasses
	updated, err := json.Marshal(data)
	if err != nil {
		return packet
	}
	return updated
}

func (s *PRReadinessStore) UpsertContext(ctx context.Context, orgID, sessionID uuid.UUID, issueLessReason string, userID uuid.UUID) (models.PRReadinessContext, error) {
	issueLessReason = strings.TrimSpace(issueLessReason)
	var contextValue models.PRReadinessContext
	err := s.db.QueryRow(ctx, `
		INSERT INTO pr_readiness_contexts (org_id, session_id, issue_less_reason, created_by_user_id, updated_by_user_id)
		VALUES (@org_id, @session_id, @issue_less_reason, @user_id, @user_id)
		ON CONFLICT (org_id, session_id) DO UPDATE
		SET issue_less_reason = EXCLUDED.issue_less_reason,
		    updated_by_user_id = EXCLUDED.updated_by_user_id,
		    updated_at = now()
		RETURNING org_id, session_id, issue_less_reason, created_by_user_id, updated_by_user_id, created_at, updated_at`, pgx.NamedArgs{
		"org_id":            orgID,
		"session_id":        sessionID,
		"issue_less_reason": issueLessReason,
		"user_id":           userID,
	}).Scan(&contextValue.OrgID, &contextValue.SessionID, &contextValue.IssueLessReason, &contextValue.CreatedByUserID, &contextValue.UpdatedByUserID, &contextValue.CreatedAt, &contextValue.UpdatedAt)
	if err != nil {
		return models.PRReadinessContext{}, fmt.Errorf("upsert PR readiness context: %w", err)
	}
	return contextValue, nil
}

func (s *PRReadinessStore) GetContext(ctx context.Context, orgID, sessionID uuid.UUID) (models.PRReadinessContext, error) {
	rows, err := s.db.Query(ctx, `
		SELECT org_id, session_id, issue_less_reason, created_by_user_id, updated_by_user_id, created_at, updated_at
		FROM pr_readiness_contexts
		WHERE org_id = @org_id AND session_id = @session_id`, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID})
	if err != nil {
		return models.PRReadinessContext{}, fmt.Errorf("query PR readiness context: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PRReadinessContext])
}

func (s *PRReadinessStore) ListCustomChecks(ctx context.Context, orgID uuid.UUID, repositoryID *uuid.UUID) ([]models.PRReadinessCustomCheck, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, org_id, repository_id, check_key, name, prompt, path_filters, enforcement, source, active, created_by_user_id, created_at
		FROM pr_readiness_custom_checks
		WHERE org_id = @org_id
		  AND active = true
		  AND (repository_id IS NULL OR repository_id = @repository_id)
		ORDER BY repository_id NULLS FIRST, created_at ASC, id ASC`, pgx.NamedArgs{"org_id": orgID, "repository_id": repositoryID})
	if err != nil {
		return nil, fmt.Errorf("query PR readiness custom checks: %w", err)
	}
	return collectPRReadinessCustomChecks(rows)
}

func (s *PRReadinessStore) SaveCustomCheck(ctx context.Context, orgID uuid.UUID, check models.PRReadinessCustomCheck, userID *uuid.UUID) (models.PRReadinessCustomCheck, error) {
	check.CheckKey = strings.TrimSpace(check.CheckKey)
	check.Name = strings.TrimSpace(check.Name)
	check.Prompt = strings.TrimSpace(check.Prompt)
	if check.CheckKey == "" || check.Name == "" || check.Prompt == "" {
		return models.PRReadinessCustomCheck{}, fmt.Errorf("custom readiness check key, name, and prompt are required")
	}
	if check.Source == "" {
		check.Source = models.PRReadinessCustomCheckSourceOrgSettings
	}
	if err := check.Enforcement.Validate(); err != nil {
		return models.PRReadinessCustomCheck{}, err
	}
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return models.PRReadinessCustomCheck{}, fmt.Errorf("save PR readiness custom check requires transaction support")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return models.PRReadinessCustomCheck{}, fmt.Errorf("begin PR readiness custom check tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		UPDATE pr_readiness_custom_checks
		SET active = false
		WHERE org_id = @org_id
		  AND active = true
		  AND repository_id IS NOT DISTINCT FROM @repository_id
		  AND check_key = @check_key`, pgx.NamedArgs{
		"org_id":        orgID,
		"repository_id": check.RepositoryID,
		"check_key":     check.CheckKey,
	}); err != nil {
		return models.PRReadinessCustomCheck{}, fmt.Errorf("inactivate PR readiness custom check: %w", err)
	}
	pathBytes, err := json.Marshal(check.PathFilters)
	if err != nil {
		return models.PRReadinessCustomCheck{}, fmt.Errorf("marshal PR readiness custom check paths: %w", err)
	}
	enforcementBytes, err := json.Marshal(check.Enforcement)
	if err != nil {
		return models.PRReadinessCustomCheck{}, fmt.Errorf("marshal PR readiness custom check enforcement: %w", err)
	}
	rows, err := tx.Query(ctx, `
		INSERT INTO pr_readiness_custom_checks (
			org_id, repository_id, check_key, name, prompt, path_filters, enforcement, source, created_by_user_id
		) VALUES (
			@org_id, @repository_id, @check_key, @name, @prompt, @path_filters, @enforcement, @source, @created_by_user_id
		)
		RETURNING id, org_id, repository_id, check_key, name, prompt, path_filters, enforcement, source, active, created_by_user_id, created_at`, pgx.NamedArgs{
		"org_id":             orgID,
		"repository_id":      check.RepositoryID,
		"check_key":          check.CheckKey,
		"name":               check.Name,
		"prompt":             check.Prompt,
		"path_filters":       pathBytes,
		"enforcement":        enforcementBytes,
		"source":             check.Source,
		"created_by_user_id": userID,
	})
	if err != nil {
		return models.PRReadinessCustomCheck{}, fmt.Errorf("insert PR readiness custom check: %w", err)
	}
	inserted, err := collectOnePRReadinessCustomCheck(rows)
	if err != nil {
		return models.PRReadinessCustomCheck{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return models.PRReadinessCustomCheck{}, fmt.Errorf("commit PR readiness custom check tx: %w", err)
	}
	return inserted, nil
}

func (s *PRReadinessStore) UpdateCustomCheck(ctx context.Context, orgID, id uuid.UUID, check models.PRReadinessCustomCheck, userID *uuid.UUID) (models.PRReadinessCustomCheck, error) {
	check.CheckKey = strings.TrimSpace(check.CheckKey)
	check.Name = strings.TrimSpace(check.Name)
	check.Prompt = strings.TrimSpace(check.Prompt)
	if check.CheckKey == "" || check.Name == "" || check.Prompt == "" {
		return models.PRReadinessCustomCheck{}, fmt.Errorf("custom readiness check key, name, and prompt are required")
	}
	if check.Source == "" {
		check.Source = models.PRReadinessCustomCheckSourceOrgSettings
	}
	if err := check.Enforcement.Validate(); err != nil {
		return models.PRReadinessCustomCheck{}, err
	}
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return models.PRReadinessCustomCheck{}, fmt.Errorf("update PR readiness custom check requires transaction support")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return models.PRReadinessCustomCheck{}, fmt.Errorf("begin PR readiness custom check update tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var repositoryID *uuid.UUID
	rows, err := tx.Query(ctx, `
		UPDATE pr_readiness_custom_checks
		SET active = false
		WHERE org_id = @org_id
		  AND id = @id
		  AND active = true
		  AND source = 'org_settings'
		RETURNING repository_id`, pgx.NamedArgs{"org_id": orgID, "id": id})
	if err != nil {
		return models.PRReadinessCustomCheck{}, fmt.Errorf("inactivate PR readiness custom check by id: %w", err)
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			rows.Close()
			return models.PRReadinessCustomCheck{}, err
		}
		rows.Close()
		return models.PRReadinessCustomCheck{}, pgx.ErrNoRows
	}
	if err := rows.Scan(&repositoryID); err != nil {
		rows.Close()
		return models.PRReadinessCustomCheck{}, err
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return models.PRReadinessCustomCheck{}, err
	}
	rows.Close()
	check.RepositoryID = repositoryID
	return s.insertCustomCheckInTx(ctx, tx, orgID, check, userID)
}

func (s *PRReadinessStore) insertCustomCheckInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, check models.PRReadinessCustomCheck, userID *uuid.UUID) (models.PRReadinessCustomCheck, error) {
	pathBytes, err := json.Marshal(check.PathFilters)
	if err != nil {
		return models.PRReadinessCustomCheck{}, fmt.Errorf("marshal PR readiness custom check paths: %w", err)
	}
	enforcementBytes, err := json.Marshal(check.Enforcement)
	if err != nil {
		return models.PRReadinessCustomCheck{}, fmt.Errorf("marshal PR readiness custom check enforcement: %w", err)
	}
	rows, err := tx.Query(ctx, `
		INSERT INTO pr_readiness_custom_checks (
			org_id, repository_id, check_key, name, prompt, path_filters, enforcement, source, created_by_user_id
		) VALUES (
			@org_id, @repository_id, @check_key, @name, @prompt, @path_filters, @enforcement, @source, @created_by_user_id
		)
		RETURNING id, org_id, repository_id, check_key, name, prompt, path_filters, enforcement, source, active, created_by_user_id, created_at`, pgx.NamedArgs{
		"org_id":             orgID,
		"repository_id":      check.RepositoryID,
		"check_key":          check.CheckKey,
		"name":               check.Name,
		"prompt":             check.Prompt,
		"path_filters":       pathBytes,
		"enforcement":        enforcementBytes,
		"source":             check.Source,
		"created_by_user_id": userID,
	})
	if err != nil {
		return models.PRReadinessCustomCheck{}, fmt.Errorf("insert PR readiness custom check: %w", err)
	}
	inserted, err := collectOnePRReadinessCustomCheck(rows)
	if err != nil {
		return models.PRReadinessCustomCheck{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return models.PRReadinessCustomCheck{}, fmt.Errorf("commit PR readiness custom check update tx: %w", err)
	}
	return inserted, nil
}

func (s *PRReadinessStore) DeleteCustomCheck(ctx context.Context, orgID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE pr_readiness_custom_checks
		SET active = false
		WHERE org_id = @org_id
		  AND id = @id
		  AND active = true
		  AND source = 'org_settings'`, pgx.NamedArgs{"org_id": orgID, "id": id})
	if err != nil {
		return fmt.Errorf("delete PR readiness custom check: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *PRReadinessStore) MaterializeRepoConfigChecks(ctx context.Context, orgID, repositoryID uuid.UUID, checks []models.PRReadinessCustomCheck) error {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return fmt.Errorf("materialize repo readiness checks requires transaction support")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin repo readiness check materialization tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		UPDATE pr_readiness_custom_checks
		SET active = false
		WHERE org_id = @org_id
		  AND repository_id = @repository_id
		  AND source = 'repo_config'
		  AND active = true`, pgx.NamedArgs{"org_id": orgID, "repository_id": repositoryID}); err != nil {
		return fmt.Errorf("inactivate repo readiness checks: %w", err)
	}
	for _, check := range checks {
		check.OrgID = orgID
		check.RepositoryID = &repositoryID
		check.Source = models.PRReadinessCustomCheckSourceRepoConfig
		pathBytes, err := json.Marshal(check.PathFilters)
		if err != nil {
			return fmt.Errorf("marshal repo readiness check paths: %w", err)
		}
		enforcementBytes, err := json.Marshal(check.Enforcement)
		if err != nil {
			return fmt.Errorf("marshal repo readiness check enforcement: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO pr_readiness_custom_checks (
				org_id, repository_id, check_key, name, prompt, path_filters, enforcement, source, created_by_user_id
			) VALUES (
				@org_id, @repository_id, @check_key, @name, @prompt, @path_filters, @enforcement, 'repo_config', NULL
			)`, pgx.NamedArgs{
			"org_id":        orgID,
			"repository_id": repositoryID,
			"check_key":     strings.TrimSpace(check.CheckKey),
			"name":          strings.TrimSpace(check.Name),
			"prompt":        strings.TrimSpace(check.Prompt),
			"path_filters":  pathBytes,
			"enforcement":   enforcementBytes,
		}); err != nil {
			return fmt.Errorf("insert repo readiness check: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit repo readiness check materialization tx: %w", err)
	}
	return nil
}

func collectOnePRReadinessPolicy(rows pgx.Rows) (models.PRReadinessPolicyRecord, error) {
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return models.PRReadinessPolicyRecord{}, err
		}
		return models.PRReadinessPolicyRecord{}, pgx.ErrNoRows
	}
	var record models.PRReadinessPolicyRecord
	var configBytes []byte
	if err := rows.Scan(&record.ID, &record.OrgID, &record.RepositoryID, &configBytes, &record.Active, &record.CreatedByUserID, &record.CreatedAt); err != nil {
		return models.PRReadinessPolicyRecord{}, err
	}
	if err := json.Unmarshal(configBytes, &record.Config); err != nil {
		return models.PRReadinessPolicyRecord{}, fmt.Errorf("decode PR readiness policy config: %w", err)
	}
	record.Config = models.ResolvePRReadinessPolicyConfig(&record.Config, nil)
	return record, nil
}

func collectPRReadinessCustomChecks(rows pgx.Rows) ([]models.PRReadinessCustomCheck, error) {
	defer rows.Close()
	checks := make([]models.PRReadinessCustomCheck, 0)
	for rows.Next() {
		check, err := scanPRReadinessCustomCheck(rows)
		if err != nil {
			return nil, err
		}
		checks = append(checks, check)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return checks, nil
}

func collectOnePRReadinessCustomCheck(rows pgx.Rows) (models.PRReadinessCustomCheck, error) {
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return models.PRReadinessCustomCheck{}, err
		}
		return models.PRReadinessCustomCheck{}, pgx.ErrNoRows
	}
	return scanPRReadinessCustomCheck(rows)
}

func scanPRReadinessCustomCheck(rows pgx.Rows) (models.PRReadinessCustomCheck, error) {
	var check models.PRReadinessCustomCheck
	var pathBytes []byte
	var enforcementBytes []byte
	if err := rows.Scan(&check.ID, &check.OrgID, &check.RepositoryID, &check.CheckKey, &check.Name, &check.Prompt, &pathBytes, &enforcementBytes, &check.Source, &check.Active, &check.CreatedByUserID, &check.CreatedAt); err != nil {
		return models.PRReadinessCustomCheck{}, err
	}
	if len(pathBytes) > 0 {
		if err := json.Unmarshal(pathBytes, &check.PathFilters); err != nil {
			return models.PRReadinessCustomCheck{}, fmt.Errorf("decode PR readiness custom check paths: %w", err)
		}
	}
	if len(enforcementBytes) > 0 {
		if err := json.Unmarshal(enforcementBytes, &check.Enforcement); err != nil {
			return models.PRReadinessCustomCheck{}, fmt.Errorf("decode PR readiness custom check enforcement: %w", err)
		}
	}
	return check, nil
}

func firstNonZeroReadinessEnforcement(values ...models.PRReadinessEnforcement) models.PRReadinessEnforcement {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return models.PRReadinessEnforcementOff
}
