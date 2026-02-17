# Design: Auto-Closing Feedback Loops

This document describes how 143.dev automatically closes its own feedback loops — turning high-signal data (PR review comments, failure classifications, complexity predictions, post-deploy impact) into configuration improvements without requiring human action. Every automated decision is logged, versioned with insert-only snapshots, and rollback-able to any point in time.

## Problem

143.dev has several feedback loops that collect high-signal data:
- **Run debugging** (doc 15) classifies failures and detects cross-run patterns — but nobody acts on them
- **Complexity estimation** (doc 12) predicts issue difficulty — but calibration drift goes uncorrected
- **Codebase context** (doc 14) enriches agent runs — but context gaps persist until someone manually triggers a rebuild
- **Review feedback** (doc 11) captures reviewer preferences as patterns — but promoting them to active conventions requires manual approval

All four loops require manual human action to close. This creates bottlenecks that prevent the system from self-improving. The fix: auto-close these loops with configurable autonomy, defaulting to fully automatic but easily switchable to supervised or manual modes, with full traceability and point-in-time rollback.

## Overview

The system adds:

1. **Tuning autonomy configuration** — a new `tuning_autonomy` setting that controls whether the system auto-applies improvements or waits for approval
2. **Insert-only configuration versioning** — every config change inserts a new immutable version, enabling point-in-time rollback and full audit
3. **Tuning decisions audit table** — every automated decision is recorded whether applied, proposed, or rejected
4. **Four auto-closing loops** — experiment config, complexity calibration, context self-healing, convention auto-apply
5. **Safety mechanisms** — rate limiting, auto-rollback, cost gates, protected categories
6. **Self-improvement dashboard** — config timeline, decision log, approval queue, point-in-time restore

## 1. Tuning Autonomy Configuration

A new `tuning_autonomy` setting in `organizations.settings`, orthogonal to the existing `autonomy_level` (which controls run execution):

```json
{
  "tuning_autonomy": "auto",
  "tuning_config": {
    "rate_limit": {
      "max_changes_per_hour": 5,
      "max_changes_per_day": 20
    },
    "rollback_thresholds": {
      "metric_regression_pct": 10,
      "check_interval_minutes": 60
    },
    "cost_gate": {
      "max_cost_increase_pct": 20
    },
    "never_auto_change": ["security"],
    "min_data_thresholds": {
      "calibration_data_points": 20,
      "context_failures_same_area": 2,
      "failed_runs_for_experiment": 3,
      "experiment_significance": 0.05,
      "pattern_confidence": 0.8
    }
  }
}
```

### Autonomy Levels

| Level | Behavior | Use Case |
|-------|----------|----------|
| `auto` (default) | System self-configures. All loops auto-close. Every decision logged and visible in dashboard. Admins notified but don't need to approve. | Teams that trust the system and want maximum velocity |
| `supervised` | System proposes changes, waits for admin approval before applying. Decisions appear in the approval queue. | Teams ramping up trust or in regulated environments |
| `manual` | System surfaces insights and recommendations only. Admins act manually. | Teams that want full control |

The `tuning_autonomy` level applies globally. Individual loops cannot be set to different levels — this keeps the mental model simple. If a team wants to disable a specific loop, they can set its minimum data threshold impossibly high.

## 2. Insert-Only Configuration Versioning

All tuning-managed configuration uses an insert-only pattern. Instead of mutating config in place, every change inserts a new version and invalidates the previous one. This gives:

- Full history of every config state the system has ever been in
- Point-in-time rollback to any previous configuration (by date or version number)
- Easy "restore to last known good" by re-activating an old version
- Audit trail built into the data model, not bolted on

### `tuning_config_versions` Table

The core versioning table. Each row is an immutable snapshot of a configuration. New configs are inserted; old ones are never mutated (except the `is_active` flag).

```sql
CREATE TABLE tuning_config_versions (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          uuid NOT NULL REFERENCES organizations(id),
    config_scope    text NOT NULL,
    -- 'complexity_calibration', 'agent_defaults', 'conventions',
    -- 'context_package', 'tuning_settings'
    scope_key       text NOT NULL,
    -- scoping key within the scope, e.g. repo name, issue type, or '*' for org-wide
    version         int NOT NULL,
    config_snapshot jsonb NOT NULL,
    -- the full config value at this version (complete, not a delta)
    is_active       boolean NOT NULL DEFAULT true,
    -- only one version per (org_id, config_scope, scope_key) is active at a time
    decision_id     uuid REFERENCES tuning_decisions(id),
    -- which tuning decision created this version (null for initial/manual)
    created_by      text NOT NULL DEFAULT 'system',
    -- 'system' for auto-tuning, 'user:<user_id>' for manual, 'rollback' for rollbacks
    created_at      timestamptz NOT NULL DEFAULT now()
    -- immutable: rows are never updated, only new rows are inserted
);

-- Only one active version per scope
CREATE UNIQUE INDEX idx_config_versions_active
    ON tuning_config_versions (org_id, config_scope, scope_key)
    WHERE is_active = true;

-- Fast point-in-time queries
CREATE INDEX idx_config_versions_history
    ON tuning_config_versions (org_id, config_scope, scope_key, created_at DESC);

-- Version sequence
CREATE UNIQUE INDEX idx_config_versions_seq
    ON tuning_config_versions (org_id, config_scope, scope_key, version);
```

### Store Functions (`internal/db/tuning_config.go`)

```go
// internal/db/tuning_config.go

type TuningConfigStore struct {
    db DBTX
}

func NewTuningConfigStore(db DBTX) *TuningConfigStore {
    return &TuningConfigStore{db: db}
}

type TuningConfigVersion struct {
    ID             uuid.UUID  `db:"id"`
    OrgID          uuid.UUID  `db:"org_id"`
    ConfigScope    string     `db:"config_scope"`
    ScopeKey       string     `db:"scope_key"`
    Version        int32      `db:"version"`
    ConfigSnapshot []byte     `db:"config_snapshot"`
    IsActive       bool       `db:"is_active"`
    DecisionID     *uuid.UUID `db:"decision_id"`
    CreatedBy      string     `db:"created_by"`
    CreatedAt      time.Time  `db:"created_at"`
}

func (s *TuningConfigStore) DeactivateConfigVersion(ctx context.Context, orgID uuid.UUID, configScope, scopeKey string) error {
    _, err := s.db.Exec(ctx, `
        UPDATE tuning_config_versions
        SET is_active = false
        WHERE org_id = $1 AND config_scope = $2 AND scope_key = $3 AND is_active = true`,
        orgID, configScope, scopeKey,
    )
    return err
}

func (s *TuningConfigStore) GetNextConfigVersionNumber(ctx context.Context, orgID uuid.UUID, configScope, scopeKey string) (int32, error) {
    var nextVersion int32
    err := s.db.QueryRow(ctx, `
        SELECT COALESCE(MAX(version), 0) + 1 AS next_version
        FROM tuning_config_versions
        WHERE org_id = $1 AND config_scope = $2 AND scope_key = $3`,
        orgID, configScope, scopeKey,
    ).Scan(&nextVersion)
    return nextVersion, err
}

func (s *TuningConfigStore) InsertConfigVersion(ctx context.Context, v TuningConfigVersion) (TuningConfigVersion, error) {
    rows, _ := s.db.Query(ctx, `
        INSERT INTO tuning_config_versions (
            org_id, config_scope, scope_key, version, config_snapshot,
            is_active, decision_id, created_by
        ) VALUES ($1, $2, $3, $4, $5, true, $6, $7)
        RETURNING *`,
        v.OrgID, v.ConfigScope, v.ScopeKey, v.Version, v.ConfigSnapshot,
        v.DecisionID, v.CreatedBy,
    )
    return pgx.CollectOneRow(rows, pgx.RowToStructByName[TuningConfigVersion])
}

func (s *TuningConfigStore) GetActiveConfigVersion(ctx context.Context, orgID uuid.UUID, configScope, scopeKey string) (TuningConfigVersion, error) {
    rows, _ := s.db.Query(ctx, `
        SELECT * FROM tuning_config_versions
        WHERE org_id = $1 AND config_scope = $2 AND scope_key = $3 AND is_active = true`,
        orgID, configScope, scopeKey,
    )
    return pgx.CollectOneRow(rows, pgx.RowToStructByName[TuningConfigVersion])
}

func (s *TuningConfigStore) GetConfigVersion(ctx context.Context, orgID uuid.UUID, configScope, scopeKey string, version int32) (TuningConfigVersion, error) {
    rows, _ := s.db.Query(ctx, `
        SELECT * FROM tuning_config_versions
        WHERE org_id = $1 AND config_scope = $2 AND scope_key = $3 AND version = $4`,
        orgID, configScope, scopeKey, version,
    )
    return pgx.CollectOneRow(rows, pgx.RowToStructByName[TuningConfigVersion])
}

func (s *TuningConfigStore) GetConfigVersionAsOf(ctx context.Context, orgID uuid.UUID, configScope, scopeKey string, asOf time.Time) (TuningConfigVersion, error) {
    rows, _ := s.db.Query(ctx, `
        SELECT * FROM tuning_config_versions
        WHERE org_id = $1 AND config_scope = $2 AND scope_key = $3
          AND created_at <= $4
        ORDER BY version DESC
        LIMIT 1`,
        orgID, configScope, scopeKey, asOf,
    )
    return pgx.CollectOneRow(rows, pgx.RowToStructByName[TuningConfigVersion])
}

func (s *TuningConfigStore) ListConfigVersionHistory(ctx context.Context, orgID uuid.UUID, configScope, scopeKey string, limit, offset int) ([]TuningConfigVersion, error) {
    rows, _ := s.db.Query(ctx, `
        SELECT * FROM tuning_config_versions
        WHERE org_id = $1 AND config_scope = $2 AND scope_key = $3
        ORDER BY version DESC
        LIMIT $4 OFFSET $5`,
        orgID, configScope, scopeKey, limit, offset,
    )
    return pgx.CollectRows(rows, pgx.RowToStructByName[TuningConfigVersion])
}

func (s *TuningConfigStore) CountRecentAppliedDecisions(ctx context.Context, orgID uuid.UUID, since time.Time) (int64, error) {
    var count int64
    err := s.db.QueryRow(ctx, `
        SELECT COUNT(*) FROM tuning_decisions
        WHERE org_id = $1 AND status = 'applied' AND applied_at > $2`,
        orgID, since,
    ).Scan(&count)
    return count, err
}
```

### What Gets Versioned

| config_scope | scope_key | What's in config_snapshot |
|---|---|---|
| `complexity_calibration` | `{repo}/{issue_type}` | `{"tier_offset": 0.5, "token_multiplier": 1.3, "data_points": 45}` |
| `agent_defaults` | `*` (org-wide) or `{repo}` | Agent config overrides from winning experiments |
| `conventions` | `{repo}` | Full list of active review patterns / convention rules |
| `context_package` | `{repo}` | Context enrichment metadata (which dirs were enriched, when) |
| `tuning_settings` | `*` | The `tuning_autonomy` + `tuning_config` block itself |

## 3. `tuning_decisions` Audit Table

Every automated decision — whether applied, proposed, or rejected — is recorded. References a `tuning_config_versions` row when applied.

```sql
CREATE TABLE tuning_decisions (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id            uuid NOT NULL REFERENCES organizations(id),
    loop_type         text NOT NULL,
    -- 'experiment_config', 'complexity_calibration',
    -- 'context_enrichment', 'convention_apply'
    trigger_type      text NOT NULL,
    -- 'failure_pattern', 'experiment_result', 'calibration_drift',
    -- 'context_failure', 'review_pattern_promotion', 'regression_rollback'
    trigger_ref       jsonb NOT NULL,
    -- reference to what triggered this decision
    decision          text NOT NULL,
    -- human-readable summary
    config_scope      text NOT NULL,
    scope_key         text NOT NULL,
    before_version    int,
    -- version number before this change (null for first version)
    after_version     int,
    -- version number after this change (null if not yet applied)
    before_snapshot   jsonb NOT NULL,
    after_snapshot    jsonb NOT NULL,
    confidence        float NOT NULL,
    status            text NOT NULL DEFAULT 'pending',
    -- 'pending', 'approved', 'applied', 'rejected', 'rolled_back', 'superseded'
    approved_by       uuid REFERENCES users(id),
    applied_at        timestamptz,
    cost_impact       jsonb,
    created_at        timestamptz NOT NULL DEFAULT now()
    -- immutable: rows are never updated except for status transitions
);
```

**Indexes:**
- `(org_id, loop_type, created_at DESC)` — decisions per loop
- `(org_id, status, created_at DESC)` — pending approval queue
- `(org_id, applied_at DESC)` where `status = 'applied'` — recent changes for regression checking

### Store Functions (`internal/db/tuning_decisions.go`)

```go
// internal/db/tuning_decisions.go

type TuningDecisionStore struct {
    db DBTX
}

func NewTuningDecisionStore(db DBTX) *TuningDecisionStore {
    return &TuningDecisionStore{db: db}
}

type TuningDecision struct {
    ID              uuid.UUID  `db:"id"`
    OrgID           uuid.UUID  `db:"org_id"`
    LoopType        string     `db:"loop_type"`
    TriggerType     string     `db:"trigger_type"`
    TriggerRef      []byte     `db:"trigger_ref"`
    Decision        string     `db:"decision"`
    ConfigScope     string     `db:"config_scope"`
    ScopeKey        string     `db:"scope_key"`
    BeforeVersion   *int32     `db:"before_version"`
    AfterVersion    *int32     `db:"after_version"`
    BeforeSnapshot  []byte     `db:"before_snapshot"`
    AfterSnapshot   []byte     `db:"after_snapshot"`
    Confidence      float64    `db:"confidence"`
    Status          string     `db:"status"`
    ApprovedBy      *uuid.UUID `db:"approved_by"`
    AppliedAt       *time.Time `db:"applied_at"`
    CostImpact      []byte     `db:"cost_impact"`
    CreatedAt       time.Time  `db:"created_at"`
}

func (s *TuningDecisionStore) Insert(ctx context.Context, d TuningDecision) (TuningDecision, error) {
    rows, _ := s.db.Query(ctx, `
        INSERT INTO tuning_decisions (
            org_id, loop_type, trigger_type, trigger_ref, decision,
            config_scope, scope_key, before_version, after_version,
            before_snapshot, after_snapshot, confidence, status, cost_impact
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
        RETURNING *`,
        d.OrgID, d.LoopType, d.TriggerType, d.TriggerRef, d.Decision,
        d.ConfigScope, d.ScopeKey, d.BeforeVersion, d.AfterVersion,
        d.BeforeSnapshot, d.AfterSnapshot, d.Confidence, d.Status, d.CostImpact,
    )
    return pgx.CollectOneRow(rows, pgx.RowToStructByName[TuningDecision])
}

func (s *TuningDecisionStore) UpdateStatus(ctx context.Context, id uuid.UUID, status string, appliedAt *time.Time, approvedBy *uuid.UUID) error {
    _, err := s.db.Exec(ctx, `
        UPDATE tuning_decisions
        SET status = $2, applied_at = $3, approved_by = $4
        WHERE id = $1`,
        id, status, appliedAt, approvedBy,
    )
    return err
}

func (s *TuningDecisionStore) GetByID(ctx context.Context, id uuid.UUID) (TuningDecision, error) {
    rows, _ := s.db.Query(ctx, `
        SELECT * FROM tuning_decisions WHERE id = $1`,
        id,
    )
    return pgx.CollectOneRow(rows, pgx.RowToStructByName[TuningDecision])
}

func (s *TuningDecisionStore) List(ctx context.Context, orgID uuid.UUID, loopType, status *string, limit, offset int) ([]TuningDecision, error) {
    rows, _ := s.db.Query(ctx, `
        SELECT * FROM tuning_decisions
        WHERE org_id = $1
          AND ($2::text IS NULL OR loop_type = $2)
          AND ($3::text IS NULL OR status = $3)
        ORDER BY created_at DESC
        LIMIT $4 OFFSET $5`,
        orgID, loopType, status, limit, offset,
    )
    return pgx.CollectRows(rows, pgx.RowToStructByName[TuningDecision])
}

func (s *TuningDecisionStore) ListRecentApplied(ctx context.Context, orgID uuid.UUID, since time.Time) ([]TuningDecision, error) {
    rows, _ := s.db.Query(ctx, `
        SELECT * FROM tuning_decisions
        WHERE org_id = $1 AND status = 'applied' AND applied_at > $2
        ORDER BY applied_at DESC`,
        orgID, since,
    )
    return pgx.CollectRows(rows, pgx.RowToStructByName[TuningDecision])
}

func (s *TuningDecisionStore) GetPendingCount(ctx context.Context, orgID uuid.UUID) (int64, error) {
    var count int64
    err := s.db.QueryRow(ctx, `
        SELECT COUNT(*) FROM tuning_decisions
        WHERE org_id = $1 AND status = 'pending'`,
        orgID,
    ).Scan(&count)
    return count, err
}
```

## 4. TuningService Core

The `TuningService` is the central coordinator for all auto-closing loops. It provides `ProposeChange` (creates a decision, checks autonomy level), `ApplyConfigChange` (transactionally inserts a new config version), and rollback APIs.

### How Config Changes Work

```go
func (t *TuningService) ApplyConfigChange(ctx context.Context, decision *TuningDecision) error {
    return pgx.BeginFunc(ctx, t.pool, func(tx pgx.Tx) error {
        configStore := db.NewTuningConfigStore(tx)
        decisionStore := db.NewTuningDecisionStore(tx)

        // 1. Deactivate current active version
        err := configStore.DeactivateConfigVersion(ctx, decision.OrgID, decision.ConfigScope, decision.ScopeKey)
        if err != nil {
            return fmt.Errorf("deactivate current version: %w", err)
        }

        // 2. Get next version number
        nextVersion, err := configStore.GetNextConfigVersionNumber(ctx, decision.OrgID, decision.ConfigScope, decision.ScopeKey)
        if err != nil {
            return fmt.Errorf("get next version: %w", err)
        }

        // 3. Insert new version (immutable, never updated after this)
        version, err := configStore.InsertConfigVersion(ctx, db.TuningConfigVersion{
            OrgID:          decision.OrgID,
            ConfigScope:    decision.ConfigScope,
            ScopeKey:       decision.ScopeKey,
            Version:        nextVersion,
            ConfigSnapshot: decision.AfterSnapshot,
            DecisionID:     &decision.ID,
            CreatedBy:      "system",
        })
        if err != nil {
            return fmt.Errorf("insert config version: %w", err)
        }

        // 4. Update the decision with the version reference
        decision.AfterVersion = &version.Version
        now := time.Now()
        return decisionStore.UpdateStatus(ctx, decision.ID, "applied", &now, decision.ApprovedBy)
    })
}
```

### ProposeChange Flow

```go
func (t *TuningService) ProposeChange(ctx context.Context, decision *TuningDecision) error {
    configStore := db.NewTuningConfigStore(t.pool)
    decisionStore := db.NewTuningDecisionStore(t.pool)

    // 1. Check rate limits
    recentCount, err := configStore.CountRecentAppliedDecisions(ctx, decision.OrgID, time.Now().Add(-1*time.Hour))
    if err != nil {
        return fmt.Errorf("check rate limit: %w", err)
    }
    if recentCount >= t.getMaxChangesPerHour(ctx, decision.OrgID) {
        decision.Status = "pending" // rate-limited, will be retried
        _, err = decisionStore.Insert(ctx, *decision)
        return err
    }

    // 2. Check cost gate
    if decision.CostImpact != nil && decision.CostImpact.CostIncreasePct > t.getCostGateThreshold(ctx, decision.OrgID) {
        decision.Status = "pending" // requires manual approval regardless of autonomy level
        _, err = decisionStore.Insert(ctx, *decision)
        return err
    }

    // 3. Check protected categories
    if t.isProtectedCategory(ctx, decision) {
        decision.Status = "pending"
        _, err = decisionStore.Insert(ctx, *decision)
        return err
    }

    // 4. Check autonomy level
    autonomy := t.getTuningAutonomy(ctx, decision.OrgID)
    switch autonomy {
    case "auto":
        // Insert decision and apply immediately
        inserted, err := decisionStore.Insert(ctx, *decision)
        if err != nil {
            return err
        }
        decision.ID = inserted.ID
        return t.ApplyConfigChange(ctx, decision)

    case "supervised":
        // Insert as pending, notify admins
        decision.Status = "pending"
        _, err = decisionStore.Insert(ctx, *decision)
        if err != nil {
            return err
        }
        return t.notifyAdmins(ctx, decision)

    case "manual":
        // Insert as a recommendation only
        decision.Status = "pending"
        _, err = decisionStore.Insert(ctx, *decision)
        return err
    }
    return nil
}
```

### Point-in-Time Rollback

```go
// Rollback to a specific version
func (t *TuningService) RollbackToVersion(ctx context.Context, orgID uuid.UUID,
    scope, scopeKey string, targetVersion int) error {
    // Read the old version's snapshot (immutable, always available)
    oldVersion, err := t.queries.GetConfigVersion(ctx, db.GetConfigVersionParams{
        OrgID:       orgID,
        ConfigScope: scope,
        ScopeKey:    scopeKey,
        Version:     int32(targetVersion),
    })
    if err != nil {
        return fmt.Errorf("get target version: %w", err)
    }

    // Insert a NEW row with the old snapshot (insert-only: we never mutate)
    return t.ApplyConfigChange(ctx, &TuningDecision{
        OrgID:         orgID,
        LoopType:      "rollback",
        TriggerType:   "manual_rollback",
        TriggerRef:    jsonb{"target_version": targetVersion},
        Decision:      fmt.Sprintf("Rollback to version %d", targetVersion),
        ConfigScope:   scope,
        ScopeKey:      scopeKey,
        AfterSnapshot: oldVersion.ConfigSnapshot,
        Confidence:    1.0,
    })
}

// Rollback to config as of a specific timestamp
func (t *TuningService) RollbackToTimestamp(ctx context.Context, orgID uuid.UUID,
    scope, scopeKey string, asOf time.Time) error {
    // Find the version that was active at that timestamp
    version, err := t.queries.GetConfigVersionAsOf(ctx, db.GetConfigVersionAsOfParams{
        OrgID:       orgID,
        ConfigScope: scope,
        ScopeKey:    scopeKey,
        CreatedAt:   asOf,
    })
    if err != nil {
        return fmt.Errorf("get version as of %s: %w", asOf, err)
    }
    return t.RollbackToVersion(ctx, orgID, scope, scopeKey, int(version.Version))
}
```

## 5. Four Auto-Closing Loops

### Loop A — Self-Experimenting Agent Config

**Trigger**: `run_patterns` (doc 15) detect a failure pattern with high confidence (>=0.7) and at least 3 supporting failed runs.

**Decision logic**:

1. The `pattern_to_experiment` job runs hourly, scanning `run_patterns` with `status = 'detected'` and sufficient supporting runs
2. For each qualifying pattern, it generates an experiment hypothesis: "Changing config X might fix pattern Y"
3. It creates an `agent_config_experiments` row with two variants: current config (control) and proposed config (treatment)
4. The experiment runs until `min_runs_per_variant` is reached
5. When the experiment completes with a statistically significant winner (p<0.05), the system calls `TuningService.ProposeChange` with `config_scope='agent_defaults'` and the winning variant's config as the `after_snapshot`
6. Before org-wide apply, the winning config is validated against the golden eval set (doc 16) — if pass@1 drops, the decision is rejected

**Data flow**:
```
run_patterns (doc 15)
  → pattern_to_experiment job
  → agent_config_experiments (doc 15)
  → experiment completes with winner
  → eval validation (doc 16)
  → TuningService.ProposeChange
  → tuning_decisions + tuning_config_versions
```

**Job type**: `tune_experiment_config` in the `tuning` queue.

### Loop B — Self-Calibrating Complexity

**Trigger**: Weekly calibration job per repo detects accuracy drift.

**Decision logic**:

1. After every completed agent run, a `record_calibration_point` job records a data point: `{predicted_tier, actual_outcome, tokens_used, issue_type, repo}`
2. The `calibrate_complexity` weekly job per repo computes:
   - **Tier accuracy**: % of predictions within 1 tier of actual outcome
   - **Tier bias**: mean signed error (positive = overestimates, negative = underestimates)
   - **Token prediction accuracy**: ratio of actual vs. predicted tokens
3. When accuracy drops below 70% or bias exceeds 0.5 tiers (configurable in `tuning_config.min_data_thresholds`), and at least 20 calibration data points exist, the job computes updated tier offsets and token multipliers
4. Calls `TuningService.ProposeChange` with `config_scope='complexity_calibration'` and `scope_key='{repo}/{issue_type}'`
5. The `estimate_complexity` job (doc 12) reads the active `tuning_config_versions` row for `complexity_calibration` to apply offsets

**Data flow**:
```
agent_runs (completed)
  → record_calibration_point job
  → calibration data (stored in config_snapshot as historical points)
  → calibrate_complexity weekly job
  → TuningService.ProposeChange
  → tuning_decisions + tuning_config_versions
  → estimate_complexity reads active version (doc 12)
```

**Job types**: `record_calibration_point` (per-run, `tuning` queue), `calibrate_complexity` (weekly cron, `tuning` queue).

### Loop C — Self-Healing Context

**Trigger**: Failure classifier (doc 15) reports `context` category failures, and >=2 failures occur in the same codebase area within 30 days.

**Decision logic**:

1. The `evaluate_context_failures` job runs daily, scanning recent `agent_runs` with `failure_category = 'context'`
2. It groups failures by repository and affected file area (extracted from `agent_run_traces` — which files the agent tried to read but couldn't find useful context for)
3. When >=2 failures occur in the same area within 30 days, the job identifies the context gap
4. Calls `TuningService.ProposeChange` with `config_scope='context_package'` and `scope_key='{repo}'`
5. When applied, enqueues a targeted context rebuild via the existing pipeline (doc 14) — specifically, a `rebuild_context` job scoped to the affected directories
6. The `config_snapshot` records what was enriched and when, so the system doesn't re-trigger for the same area

**Data flow**:
```
agent_runs (failure_category = 'context')
  + agent_run_traces (which files were missing context)
  → evaluate_context_failures daily job
  → TuningService.ProposeChange
  → tuning_decisions + tuning_config_versions
  → rebuild_context job (doc 14)
```

**Job type**: `evaluate_context_failures` (daily cron, `tuning` queue).

### Loop D — Self-Improving Conventions

**Trigger**: A `review_pattern` (doc 11) reaches `active` status with confidence >=0.8 and at least one maintainer-tier reviewer in its provenance.

**Decision logic**:

1. The `promote_conventions` job runs hourly, scanning `review_patterns` with `status = 'active'`, `confidence >= 0.8`, and verified reviewer trust
2. It checks reviewer provenance: at least one `source_comment_id` must trace back to a reviewer with `trust_tier = 'maintainer'` in `reviewer_trust`
3. For each qualifying pattern, it builds an updated convention set by reading the current active `tuning_config_versions` row for `conventions/{repo}` and adding the new rule
4. Calls `TuningService.ProposeChange` with `config_scope='conventions'` and `scope_key='{repo}'`
5. When applied, triggers `regenerate_conventions_doc` (doc 11) to update `.143/learned-conventions.md` in the repo, using an `auto_merge` flag that creates and merges a PR automatically (vs. the normal flow that opens a PR for review)

**Data flow**:
```
review_patterns (doc 11, status = 'active', confidence >= 0.8)
  + reviewer_trust (doc 11, trust_tier = 'maintainer')
  → promote_conventions hourly job
  → TuningService.ProposeChange
  → tuning_decisions + tuning_config_versions
  → regenerate_conventions_doc with auto_merge (doc 11)
```

**Job type**: `promote_conventions` (hourly cron, `tuning` queue).

## 6. Safety Mechanisms

### Rate Limiting

Maximum 5 config changes per hour, 20 per day (configurable via `tuning_config.rate_limit`). When rate-limited, the decision is recorded with `status = 'pending'` and retried on the next job cycle.

The rate limit is checked in `ProposeChange` before applying any change:

```go
configStore := db.NewTuningConfigStore(t.pool)
recentCount, _ := configStore.CountRecentAppliedDecisions(ctx, orgID, time.Now().Add(-1*time.Hour))
```

### Auto-Rollback

An hourly `check_tuning_regressions` job checks key metrics for recently applied decisions:

1. Query `ListRecentAppliedDecisions` for decisions applied in the last hour
2. For each decision, compare the relevant metric before and after:
   - `agent_defaults`: agent run success rate
   - `complexity_calibration`: tier prediction accuracy
   - `conventions`: PR approval rate
   - `context_package`: context-related failure rate
3. If regression exceeds the threshold (default 10%, configurable via `tuning_config.rollback_thresholds.metric_regression_pct`), insert a **new** config version that restores the previous snapshot (insert-only rollback — the bad version is preserved for forensics, a new version with the old config is inserted)
4. The decision's status is updated to `rolled_back`

```go
func (t *TuningService) AutoRollback(ctx context.Context, decision *TuningDecision) error {
    // The bad version remains in history for forensic analysis.
    // We insert a NEW version with the old config — no mutations.
    return t.ApplyConfigChange(ctx, &TuningDecision{
        OrgID:         decision.OrgID,
        LoopType:      "rollback",
        TriggerType:   "regression_rollback",
        TriggerRef:    jsonb{"rolled_back_decision_id": decision.ID},
        Decision:      fmt.Sprintf("Auto-rollback: %s regressed by %.1f%%", decision.Decision, regressionPct),
        ConfigScope:   decision.ConfigScope,
        ScopeKey:      decision.ScopeKey,
        BeforeVersion: decision.AfterVersion,
        AfterSnapshot: decision.BeforeSnapshot,
        Confidence:    1.0,
    })
}
```

**Job type**: `check_tuning_regressions` (hourly cron, `tuning` queue).

### Cost Gate

Changes projected to increase token cost by >20% require approval regardless of autonomy level. The cost impact is estimated by `ProposeChange` and stored in `tuning_decisions.cost_impact`:

```json
{
  "estimated_token_delta": 15000,
  "cost_increase_pct": 25.0,
  "reasoning": "New agent config uses larger context window"
}
```

Cost estimation uses the budget tracking from doc 17 — the `TuningService` reads recent `agent_runs.token_usage` to project the impact of config changes.

### Protected Categories

The `never_auto_change` list (default: `["security"]`) ensures that changes related to protected categories always require human approval. This is checked against the `review_pattern.category` or the experiment's target area.

### Minimum Data Thresholds

Each loop has minimum data requirements before it can trigger:

| Loop | Minimum Threshold |
|------|------------------|
| Loop A (experiments) | 3 failed runs matching the pattern, experiment p<0.05 |
| Loop B (calibration) | 20 calibration data points per repo/issue_type |
| Loop C (context) | 2 context failures in the same area within 30 days |
| Loop D (conventions) | Pattern confidence >=0.8, at least 1 maintainer-tier reviewer |

### Point-in-Time Restore

Admins can restore all tuning configs to any historical timestamp via the dashboard. This creates new versions (insert-only) with the old snapshots — the current configs are preserved in history, and new rows are inserted with the restored values.

## 7. Dashboard: Self-Improvement Page

### Autonomy Status Banner

Displayed at the top of the self-improvement page. Shows the current `tuning_autonomy` level with a dropdown to change it. Changing the level itself is a tuning config change (recorded in `tuning_config_versions` with `config_scope='tuning_settings'`).

### Config Version Timeline

Visual history of all config changes across all scopes. Each entry shows:
- Version number and config scope
- What changed (before/after diff of `config_snapshot`)
- Who or what triggered it (`created_by` + `decision_id` link)
- A "Restore this version" button that calls `RollbackToVersion`

### Tuning Decision Timeline

Filterable by loop type, status, and date range. Each decision is expandable to show:
- Trigger details (which pattern, experiment, or failure)
- Before/after config snapshots with diff highlighting
- Confidence score and cost impact
- Current status and any status transitions

### Loop Health Cards

One card per loop showing key metrics:
- **Loop A**: Experiments created, experiments completed, configs applied, success rate improvement
- **Loop B**: Calibration accuracy trend, bias trend, calibrations applied
- **Loop C**: Context failures detected, enrichments triggered, failure rate in enriched areas
- **Loop D**: Patterns promoted, conventions applied, PR approval rate trend

### Rollback History

List of all rollbacks (manual and automatic) with:
- Reason for rollback
- Metric regression amount
- Link to the bad version (preserved in history for forensic analysis)
- Link to the restored version

### Approval Queue (Supervised Mode)

For `supervised` autonomy level: list of pending decisions with:
- Before/after config diff
- Confidence score and cost impact
- Approve / Reject buttons
- Batch approve for low-risk changes

### Point-in-Time Restore

Date picker that shows the active config across all scopes at that timestamp. "Restore to this point" button creates new versions for all scopes that differ from current.

## 8. Integration with Existing Docs

| Doc | Integration Point |
|-----|------------------|
| Doc 15 (Run Debugging) | Loop A consumes `run_patterns`, creates `agent_config_experiments` |
| Doc 12 (Smart Routing) | Loop B adds calibration via `tuning_config_versions`; `estimate_complexity` reads active calibration version |
| Doc 11 (Review Feedback) | Loop D extends `regenerate_conventions_doc` with `auto_merge` flag |
| Doc 14 (Codebase Context) | Loop C enqueues targeted context rebuilds via existing `rebuild_context` pipeline |
| Doc 17 (Cost Intelligence) | Budget-aware gating in `ProposeChange` — reads recent token usage to estimate cost impact |
| Doc 16 (Eval System) | Winning experiment configs validated against golden eval set before org-wide apply |

## 9. Database Changes Summary

### New Tables

| Table | Purpose |
|-------|---------|
| `tuning_config_versions` | Insert-only config versioning with point-in-time rollback |
| `tuning_decisions` | Audit log of every automated tuning decision |

### Modified Settings

| Table | Change |
|-------|--------|
| `organizations.settings` | Add `tuning_autonomy` and `tuning_config` JSONB keys |

### New Job Types

| Job Type | Queue | Schedule |
|----------|-------|----------|
| `pattern_to_experiment` | `tuning` | Hourly cron |
| `record_calibration_point` | `tuning` | Per completed agent run |
| `calibrate_complexity` | `tuning` | Weekly cron per repo |
| `evaluate_context_failures` | `tuning` | Daily cron |
| `promote_conventions` | `tuning` | Hourly cron |
| `check_tuning_regressions` | `tuning` | Hourly cron |

### Entity Relationships

```
organizations
  └── tuning_config_versions
        └── tuning_decisions.decision_id (nullable)
  └── tuning_decisions
        └── tuning_config_versions.decision_id (nullable)
```

Note: `tuning_config_versions` and `tuning_decisions` have a bidirectional nullable FK — a decision can reference the config version it created, and a config version can reference the decision that triggered it. Both FKs are nullable because initial/manual versions have no decision, and pending decisions have no applied version yet.

## 10. Build Order (Phase 11)

Phase 11 depends on Phases 3 (complexity estimation), 7 (observability), 8 (review feedback), and 9 (run debugging).

1. **`tuning_config_versions` + `tuning_decisions` tables + `TuningService` core** — `ProposeChange`, `ApplyConfigChange`, rollback APIs, rate limiting, cost gating
2. **Loop D (conventions auto-apply)** — simplest loop, good first test of the insert-only versioning and autonomy flow
3. **Loop B (complexity calibration)** — calibration data recording, weekly job, version-based calibration reads in `estimate_complexity`
4. **Loop C (context self-healing)** — context failure evaluation, targeted rebuild enqueueing
5. **Loop A (self-experimenting)** — pattern-to-experiment pipeline, eval validation gate, winner auto-apply
6. **Safety hardening** — regression checking job, auto-rollback via new version insertion, budget gating integration with doc 17
7. **Self-improvement dashboard** — config version timeline, decision log, approval queue, loop health cards, point-in-time restore
