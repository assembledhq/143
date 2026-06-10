package db

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/assembledhq/143/internal/models"
)

type EvalDatasetStore struct {
	db DBTX
}

func NewEvalDatasetStore(db DBTX) *EvalDatasetStore {
	return &EvalDatasetStore{db: db}
}

const evalDatasetColumns = `d.id, d.org_id, d.repository_id, d.name, d.dataset_type, d.status,
	d.description, d.source_summary, d.created_by_user_id, d.created_at, d.updated_at,
	COALESCE(COUNT(dt.id), 0)::int AS task_count`

func scanEvalDataset(row pgx.Row) (models.EvalDataset, error) {
	var dataset models.EvalDataset
	err := row.Scan(
		&dataset.ID, &dataset.OrgID, &dataset.RepositoryID, &dataset.Name,
		&dataset.DatasetType, &dataset.Status, &dataset.Description,
		&dataset.SourceSummary, &dataset.CreatedByUserID, &dataset.CreatedAt,
		&dataset.UpdatedAt, &dataset.TaskCount,
	)
	return dataset, err
}

func (s *EvalDatasetStore) ListByOrg(ctx context.Context, orgID uuid.UUID, repositoryID *uuid.UUID) ([]models.EvalDataset, error) {
	args := pgx.NamedArgs{"org_id": orgID}
	where := `d.org_id = @org_id AND d.status = 'active'`
	if repositoryID != nil {
		where += ` AND (d.repository_id = @repository_id OR d.repository_id IS NULL)`
		args["repository_id"] = *repositoryID
	}
	query := fmt.Sprintf(`SELECT %s
		FROM eval_datasets d
		LEFT JOIN eval_dataset_tasks dt ON dt.org_id = d.org_id AND dt.dataset_id = d.id
		WHERE %s
		GROUP BY d.id
		ORDER BY d.dataset_type, d.created_at DESC`, evalDatasetColumns, where)
	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("list eval datasets: %w", err)
	}
	defer rows.Close()
	var datasets []models.EvalDataset
	for rows.Next() {
		dataset, err := scanEvalDataset(rows)
		if err != nil {
			return nil, fmt.Errorf("scan eval dataset: %w", err)
		}
		datasets = append(datasets, dataset)
	}
	return datasets, rows.Err()
}

func (s *EvalDatasetStore) Create(ctx context.Context, dataset *models.EvalDataset) error {
	if dataset.DatasetType == "" {
		dataset.DatasetType = models.EvalDatasetTypeGolden
	}
	if err := dataset.DatasetType.Validate(); err != nil {
		return err
	}
	if dataset.Status == "" {
		dataset.Status = models.EvalDatasetStatusActive
	}
	if err := dataset.Status.Validate(); err != nil {
		return err
	}
	query := fmt.Sprintf(`WITH inserted AS (
		INSERT INTO eval_datasets (
			org_id, repository_id, name, dataset_type, status, description, source_summary, created_by_user_id
		) VALUES (
			@org_id, @repository_id, @name, @dataset_type, @status, @description, @source_summary, @created_by_user_id
		)
		RETURNING *
	)
	SELECT %s
	FROM inserted d
	LEFT JOIN eval_dataset_tasks dt ON false
	GROUP BY d.id, d.org_id, d.repository_id, d.name, d.dataset_type, d.status,
		d.description, d.source_summary, d.created_by_user_id, d.created_at, d.updated_at`, evalDatasetColumns)
	scanned, err := scanEvalDataset(s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":             dataset.OrgID,
		"repository_id":      dataset.RepositoryID,
		"name":               dataset.Name,
		"dataset_type":       dataset.DatasetType,
		"status":             dataset.Status,
		"description":        dataset.Description,
		"source_summary":     dataset.SourceSummary,
		"created_by_user_id": dataset.CreatedByUserID,
	}))
	if err != nil {
		return fmt.Errorf("create eval dataset: %w", err)
	}
	*dataset = scanned
	return nil
}

func (s *EvalDatasetStore) AddTask(ctx context.Context, orgID, datasetID, taskID uuid.UUID, sliceKey string) (models.EvalDatasetTask, error) {
	var task models.EvalDatasetTask
	err := s.db.QueryRow(ctx,
		`INSERT INTO eval_dataset_tasks (org_id, dataset_id, task_id, slice_key)
		 SELECT @org_id, d.id, t.id, @slice_key
		 FROM eval_datasets d
		 JOIN eval_tasks t ON t.org_id = d.org_id AND t.id = @task_id
		 WHERE d.org_id = @org_id AND d.id = @dataset_id AND d.status = 'active'
		 ON CONFLICT (org_id, dataset_id, task_id) DO UPDATE SET slice_key = EXCLUDED.slice_key
		 RETURNING id, org_id, dataset_id, task_id, slice_key, created_at`,
		pgx.NamedArgs{"org_id": orgID, "dataset_id": datasetID, "task_id": taskID, "slice_key": sliceKey},
	).Scan(&task.ID, &task.OrgID, &task.DatasetID, &task.TaskID, &task.SliceKey, &task.CreatedAt)
	if err != nil {
		return task, fmt.Errorf("add eval dataset task: %w", err)
	}
	return task, nil
}

type EvalReleaseGateStore struct {
	db DBTX
}

func NewEvalReleaseGateStore(db DBTX) *EvalReleaseGateStore {
	return &EvalReleaseGateStore{db: db}
}

const evalReleaseGateColumns = `id, org_id, gate_name, enabled, dataset_id, min_pass_at_1,
	min_pass_at_k, max_policy_violations, max_regression_delta, canary_stages,
	rollback_rules, updated_by_user_id, active, created_at`

func scanEvalReleaseGate(row pgx.Row) (models.EvalReleaseGate, error) {
	var gate models.EvalReleaseGate
	err := row.Scan(
		&gate.ID, &gate.OrgID, &gate.GateName, &gate.Enabled, &gate.DatasetID,
		&gate.MinPassAt1, &gate.MinPassAtK, &gate.MaxPolicyViolations,
		&gate.MaxRegressionDelta, &gate.CanaryStages, &gate.RollbackRules,
		&gate.UpdatedByUserID, &gate.Active, &gate.CreatedAt,
	)
	return gate, err
}

func (s *EvalReleaseGateStore) ListActive(ctx context.Context, orgID uuid.UUID) ([]models.EvalReleaseGate, error) {
	query := fmt.Sprintf(`SELECT %s FROM eval_release_gates WHERE org_id = @org_id AND active = true ORDER BY gate_name`, evalReleaseGateColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("list eval release gates: %w", err)
	}
	defer rows.Close()
	var gates []models.EvalReleaseGate
	for rows.Next() {
		gate, err := scanEvalReleaseGate(rows)
		if err != nil {
			return nil, fmt.Errorf("scan eval release gate: %w", err)
		}
		gates = append(gates, gate)
	}
	return gates, rows.Err()
}

func (s *EvalReleaseGateStore) Upsert(ctx context.Context, gate *models.EvalReleaseGate) error {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return fmt.Errorf("eval release gate store requires TxStarter, got %T", s.db)
	}
	if len(gate.CanaryStages) == 0 {
		gate.CanaryStages = json.RawMessage(`[10,30,100]`)
	}
	if len(gate.RollbackRules) == 0 {
		gate.RollbackRules = json.RawMessage(`{}`)
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin eval release gate upsert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`UPDATE eval_release_gates SET active = false
		 WHERE org_id = @org_id AND gate_name = @gate_name AND active = true`,
		pgx.NamedArgs{"org_id": gate.OrgID, "gate_name": gate.GateName},
	); err != nil {
		return fmt.Errorf("inactivate eval release gate: %w", err)
	}
	scanned, err := scanEvalReleaseGate(tx.QueryRow(ctx,
		fmt.Sprintf(`INSERT INTO eval_release_gates (
			org_id, gate_name, enabled, dataset_id, min_pass_at_1, min_pass_at_k,
			max_policy_violations, max_regression_delta, canary_stages, rollback_rules, updated_by_user_id
		) VALUES (
			@org_id, @gate_name, @enabled, @dataset_id, @min_pass_at_1, @min_pass_at_k,
			@max_policy_violations, @max_regression_delta, @canary_stages, @rollback_rules, @updated_by_user_id
		) RETURNING %s`, evalReleaseGateColumns),
		pgx.NamedArgs{
			"org_id":                gate.OrgID,
			"gate_name":             gate.GateName,
			"enabled":               gate.Enabled,
			"dataset_id":            gate.DatasetID,
			"min_pass_at_1":         gate.MinPassAt1,
			"min_pass_at_k":         gate.MinPassAtK,
			"max_policy_violations": gate.MaxPolicyViolations,
			"max_regression_delta":  gate.MaxRegressionDelta,
			"canary_stages":         gate.CanaryStages,
			"rollback_rules":        gate.RollbackRules,
			"updated_by_user_id":    gate.UpdatedByUserID,
		},
	))
	if err != nil {
		return fmt.Errorf("insert eval release gate: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit eval release gate upsert: %w", err)
	}
	*gate = scanned
	return nil
}

const evalReleaseGateDecisionColumns = `id, org_id, batch_id, gate_id, status, reason, metrics, created_at`

func scanEvalReleaseGateDecision(row pgx.Row) (models.EvalReleaseGateDecision, error) {
	var decision models.EvalReleaseGateDecision
	err := row.Scan(
		&decision.ID, &decision.OrgID, &decision.BatchID, &decision.GateID,
		&decision.Status, &decision.Reason, &decision.Metrics, &decision.CreatedAt,
	)
	return decision, err
}

func (s *EvalReleaseGateStore) ListDecisionsByBatch(ctx context.Context, orgID, batchID uuid.UUID) ([]models.EvalReleaseGateDecision, error) {
	query := fmt.Sprintf(`SELECT %s FROM eval_release_gate_decisions WHERE org_id = @org_id AND batch_id = @batch_id ORDER BY created_at DESC`, evalReleaseGateDecisionColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "batch_id": batchID})
	if err != nil {
		return nil, fmt.Errorf("list eval release gate decisions: %w", err)
	}
	defer rows.Close()
	var decisions []models.EvalReleaseGateDecision
	for rows.Next() {
		decision, err := scanEvalReleaseGateDecision(rows)
		if err != nil {
			return nil, fmt.Errorf("scan eval release gate decision: %w", err)
		}
		decisions = append(decisions, decision)
	}
	return decisions, rows.Err()
}

func (s *EvalReleaseGateStore) EvaluateBatch(ctx context.Context, orgID, batchID uuid.UUID) ([]models.EvalReleaseGateDecision, error) {
	gates, err := s.ListActive(ctx, orgID)
	if err != nil {
		return nil, err
	}
	decisions := make([]models.EvalReleaseGateDecision, 0, len(gates))
	for _, gate := range gates {
		decision, err := s.evaluateGateForBatch(ctx, orgID, batchID, gate)
		if err != nil {
			return nil, err
		}
		decisions = append(decisions, decision)
	}
	return decisions, nil
}

func (s *EvalReleaseGateStore) evaluateGateForBatch(ctx context.Context, orgID, batchID uuid.UUID, gate models.EvalReleaseGate) (models.EvalReleaseGateDecision, error) {
	args := pgx.NamedArgs{"org_id": orgID, "batch_id": batchID, "gate_id": gate.ID}
	query := `SELECT
			COUNT(*)::int,
			COUNT(*) FILTER (WHERE passed = true)::int,
			AVG(final_score)
		FROM eval_runs
		WHERE eval_runs.org_id = @org_id AND eval_runs.batch_id = @batch_id AND eval_runs.status = 'completed'`
	if gate.DatasetID != nil {
		query = `SELECT
			COUNT(*)::int,
			COUNT(*) FILTER (WHERE passed = true)::int,
			AVG(final_score)
		FROM eval_runs
		WHERE eval_runs.org_id = @org_id AND eval_runs.batch_id = @batch_id AND eval_runs.status = 'completed'
			AND EXISTS (
			SELECT 1 FROM eval_dataset_tasks dt
			WHERE dt.org_id = eval_runs.org_id AND dt.dataset_id = @dataset_id AND dt.task_id = eval_runs.task_id
		)`
		args["dataset_id"] = *gate.DatasetID
	}
	var total int
	var passed int
	var avgScore *float64
	err := s.db.QueryRow(ctx, query, args).Scan(&total, &passed, &avgScore)
	if err != nil {
		return models.EvalReleaseGateDecision{}, fmt.Errorf("compute eval release gate metrics: %w", err)
	}
	passRate := 0.0
	if total > 0 {
		passRate = float64(passed) / float64(total)
	}
	status := models.EvalReleaseGateDecisionPassed
	reason := "release gate passed"
	if total == 0 {
		status = models.EvalReleaseGateDecisionNoData
		reason = "no completed eval runs matched this gate"
	} else if !gate.Enabled {
		status = models.EvalReleaseGateDecisionNoData
		reason = "release gate is disabled"
	} else if passRate < gate.MinPassAt1 {
		status = models.EvalReleaseGateDecisionFailed
		reason = fmt.Sprintf("pass rate %.2f is below required pass@1 %.2f", passRate, gate.MinPassAt1)
	}
	avg := 0.0
	if avgScore != nil {
		avg = *avgScore
	}
	metrics, err := json.Marshal(map[string]any{
		"total_runs":    total,
		"passed_runs":   passed,
		"pass_rate":     passRate,
		"average_score": avg,
		"min_pass_at_1": gate.MinPassAt1,
	})
	if err != nil {
		return models.EvalReleaseGateDecision{}, fmt.Errorf("marshal eval release gate metrics: %w", err)
	}
	query = fmt.Sprintf(`INSERT INTO eval_release_gate_decisions (
			org_id, batch_id, gate_id, status, reason, metrics
		) VALUES (
			@org_id, @batch_id, @gate_id, @status, @reason, @metrics
		)
		ON CONFLICT (org_id, batch_id, gate_id) DO UPDATE
		SET status = EXCLUDED.status,
			reason = EXCLUDED.reason,
			metrics = EXCLUDED.metrics,
			created_at = now()
		RETURNING %s`, evalReleaseGateDecisionColumns)
	decision, err := scanEvalReleaseGateDecision(s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"batch_id": batchID,
		"gate_id":  gate.ID,
		"status":   status,
		"reason":   reason,
		"metrics":  metrics,
	}))
	if err != nil {
		return decision, fmt.Errorf("upsert eval release gate decision: %w", err)
	}
	return decision, nil
}

var _ TxStarter = (*pgxpool.Pool)(nil)
