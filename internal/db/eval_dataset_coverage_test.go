package db

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func evalDatasetTestRows(datasets ...models.EvalDataset) *pgxmock.Rows {
	rows := pgxmock.NewRows([]string{"id", "org_id", "repository_id", "name", "dataset_type", "status", "description", "source_summary", "created_by_user_id", "created_at", "updated_at", "task_count"})
	for _, d := range datasets {
		rows.AddRow(d.ID, d.OrgID, d.RepositoryID, d.Name, d.DatasetType, d.Status, d.Description, d.SourceSummary, d.CreatedByUserID, d.CreatedAt, d.UpdatedAt, d.TaskCount)
	}
	return rows
}

func evalGateTestRows(gates ...models.EvalReleaseGate) *pgxmock.Rows {
	rows := pgxmock.NewRows([]string{"id", "org_id", "gate_name", "enabled", "dataset_id", "min_pass_at_1", "min_pass_at_k", "max_policy_violations", "max_regression_delta", "canary_stages", "rollback_rules", "updated_by_user_id", "active", "created_at"})
	for _, g := range gates {
		rows.AddRow(g.ID, g.OrgID, g.GateName, g.Enabled, g.DatasetID, g.MinPassAt1, g.MinPassAtK, g.MaxPolicyViolations, g.MaxRegressionDelta, g.CanaryStages, g.RollbackRules, g.UpdatedByUserID, g.Active, g.CreatedAt)
	}
	return rows
}

func evalDecisionTestRows(decisions ...models.EvalReleaseGateDecision) *pgxmock.Rows {
	rows := pgxmock.NewRows([]string{"id", "org_id", "batch_id", "gate_id", "status", "reason", "metrics", "created_at"})
	for _, d := range decisions {
		rows.AddRow(d.ID, d.OrgID, d.BatchID, d.GateID, d.Status, d.Reason, d.Metrics, d.CreatedAt)
	}
	return rows
}

func TestEvalDatasetListAndLookupContracts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                   string
		repository, get, empty bool
		failure                string
	}{
		{name: "all tenant datasets"}, {name: "repository and shared datasets", repository: true},
		{name: "empty list", empty: true}, {name: "list query failure", failure: "query"},
		{name: "list scan failure", failure: "scan"}, {name: "list iteration failure", failure: "iteration"},
		{name: "get exact dataset", get: true}, {name: "get missing dataset", get: true, empty: true},
		{name: "get query failure", get: true, failure: "query"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "dataset test should create an isolated database mock")
			t.Cleanup(mock.Close)
			orgID, datasetID, repositoryID, userID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			expected := models.EvalDataset{ID: datasetID, OrgID: orgID, RepositoryID: &repositoryID, Name: "Golden API", DatasetType: models.EvalDatasetTypeGolden, Status: models.EvalDatasetStatusActive, Description: "Regression corpus", SourceSummary: "Verified examples", CreatedByUserID: &userID, CreatedAt: stamp, UpdatedAt: stamp, TaskCount: 2}
			args := pgx.NamedArgs{"org_id": orgID}
			pattern := `SELECT .*FROM eval_datasets d.*dt.org_id = d.org_id.*WHERE d.org_id = @org_id AND d.status = 'active'`
			if tt.get {
				args["id"] = datasetID
				pattern = `SELECT .*FROM eval_datasets d.*dt.org_id = d.org_id.*WHERE d.org_id = @org_id AND d.id = @id`
			}
			var repo *uuid.UUID
			if tt.repository {
				repo = &repositoryID
				args["repository_id"] = repositoryID
				pattern += ` AND \(d.repository_id = @repository_id OR d.repository_id IS NULL\)`
			}
			query := mock.ExpectQuery(pattern).WithArgs(args)
			fault := errors.New("dataset connection interrupted")
			switch tt.failure {
			case "query":
				query.WillReturnError(fault)
			case "scan":
				query.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("bad-id"))
			case "iteration":
				query.WillReturnRows(evalDatasetTestRows(expected).RowError(0, fault))
			default:
				if tt.empty {
					query.WillReturnRows(evalDatasetTestRows())
				} else {
					query.WillReturnRows(evalDatasetTestRows(expected))
				}
			}
			store := NewEvalDatasetStore(mock)
			if tt.get {
				actual, err := store.GetByID(t.Context(), orgID, datasetID)
				if tt.empty {
					require.ErrorIs(t, err, pgx.ErrNoRows, "missing or foreign-tenant IDs should be reported as absent")
					require.Equal(t, models.EvalDataset{}, actual, "absent dataset should not disclose a model")
				} else if tt.failure != "" {
					require.ErrorIs(t, err, fault, "get should preserve the database cause")
				} else {
					require.NoError(t, err, "get should return the selected dataset")
					require.Equal(t, expected, actual, "lookup should preserve all dataset fields and task count")
				}
			} else {
				actual, err := store.ListByOrg(t.Context(), orgID, repo)
				switch {
				case tt.failure == "scan":
					require.ErrorContains(t, err, "scan eval dataset", "malformed rows should fail the complete list")
				case tt.failure != "":
					require.ErrorIs(t, err, fault, "list should preserve query and iteration errors")
				case tt.empty:
					require.NoError(t, err, "empty tenant should list successfully")
					require.Equal(t, []models.EvalDataset(nil), actual, "empty store result should not invent dataset rows")
				default:
					require.NoError(t, err, "tenant list should succeed")
					require.Equal(t, []models.EvalDataset{expected}, actual, "list should return complete dataset models")
				}
			}
			require.NoError(t, mock.ExpectationsWereMet(), "dataset reads should apply exact tenant and optional repository filters")
		})
	}
}

func TestEvalDatasetCreateDefaultsAndValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                string
		kind                models.EvalDatasetType
		status              models.EvalDatasetStatus
		failure, validation string
	}{
		{name: "golden active defaults"}, {name: "explicit shadow archive", kind: models.EvalDatasetTypeShadow, status: models.EvalDatasetStatusArchived},
		{name: "reject invalid type", kind: "unknown", validation: "invalid EvalDatasetType"},
		{name: "reject invalid status", kind: models.EvalDatasetTypeGolden, status: "unknown", validation: "invalid EvalDatasetStatus"},
		{name: "database failure", kind: models.EvalDatasetTypeAdversarial, status: models.EvalDatasetStatusActive, failure: "query"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create test should create an isolated database mock")
			t.Cleanup(mock.Close)
			orgID, repoID, userID := uuid.New(), uuid.New(), uuid.New()
			input := models.EvalDataset{OrgID: orgID, RepositoryID: &repoID, Name: "API corpus", DatasetType: tt.kind, Status: tt.status, Description: "Known failures", SourceSummary: "Reviewed examples", CreatedByUserID: &userID}
			expected := input
			if expected.DatasetType == "" {
				expected.DatasetType = models.EvalDatasetTypeGolden
			}
			if expected.Status == "" {
				expected.Status = models.EvalDatasetStatusActive
			}
			fault := errors.New("insert rejected")
			if tt.validation == "" {
				query := mock.ExpectQuery(`WITH inserted AS .*INSERT INTO eval_datasets`).WithArgs(pgx.NamedArgs{"org_id": orgID, "repository_id": &repoID, "name": input.Name, "dataset_type": expected.DatasetType, "status": expected.Status, "description": input.Description, "source_summary": input.SourceSummary, "created_by_user_id": &userID})
				if tt.failure != "" {
					query.WillReturnError(fault)
				} else {
					expected.ID = uuid.New()
					expected.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
					expected.UpdatedAt = expected.CreatedAt
					query.WillReturnRows(evalDatasetTestRows(expected))
				}
			}
			err = NewEvalDatasetStore(mock).Create(t.Context(), &input)
			switch {
			case tt.validation != "":
				require.ErrorContains(t, err, tt.validation, "invalid enums should be rejected before issuing SQL")
			case tt.failure != "":
				require.ErrorIs(t, err, fault, "create should preserve the insert error")
				require.Equal(t, expected, input, "failed insert should not overwrite the input with a partial row")
			default:
				require.NoError(t, err, "valid dataset should be inserted")
				require.Equal(t, expected, input, "create should return all generated fields and applied defaults")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "create should pass exact tenant and provenance values")
		})
	}
}

func TestEvalDatasetAddTaskOwnershipAndFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		missing, failure bool
	}{{name: "add or update slice"}, {name: "foreign task or archived dataset", missing: true}, {name: "dependency failure", failure: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "membership test should create its own database mock")
			t.Cleanup(mock.Close)
			orgID, datasetID, taskID := uuid.New(), uuid.New(), uuid.New()
			expected := models.EvalDatasetTask{ID: uuid.New(), OrgID: orgID, DatasetID: datasetID, TaskID: taskID, SliceKey: "database", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
			query := mock.ExpectQuery(`INSERT INTO eval_dataset_tasks .*JOIN eval_tasks t ON t.org_id = d.org_id AND t.id = @task_id.*WHERE d.org_id = @org_id AND d.id = @dataset_id AND d.status = 'active'.*ON CONFLICT`).WithArgs(pgx.NamedArgs{"org_id": orgID, "dataset_id": datasetID, "task_id": taskID, "slice_key": "database"})
			fault := errors.New("membership write failed")
			rows := pgxmock.NewRows([]string{"id", "org_id", "dataset_id", "task_id", "slice_key", "created_at"})
			if tt.failure {
				query.WillReturnError(fault)
			} else {
				if !tt.missing {
					rows.AddRow(expected.ID, orgID, datasetID, taskID, expected.SliceKey, expected.CreatedAt)
				}
				query.WillReturnRows(rows)
			}
			actual, err := NewEvalDatasetStore(mock).AddTask(t.Context(), orgID, datasetID, taskID, "database")
			switch {
			case tt.missing:
				require.ErrorIs(t, err, pgx.ErrNoRows, "cross-tenant membership and inactive datasets should fail closed")
			case tt.failure:
				require.ErrorIs(t, err, fault, "membership errors should remain retryable by callers")
			default:
				require.NoError(t, err, "valid task membership should succeed")
				require.Equal(t, expected, actual, "membership should include all ownership and slice fields")
			}
			if err != nil {
				require.Equal(t, models.EvalDatasetTask{}, actual, "failed membership should not return a partial task")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "membership SQL should verify task and dataset tenant ownership")
		})
	}
}

func TestEvalReleaseGateUpsertTransactionBoundaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, failAt string
		custom       bool
	}{
		{name: "default canary and rollback rules"}, {name: "custom rules preserved", custom: true},
		{name: "begin failure", failAt: "begin"}, {name: "inactivation failure", failAt: "update"},
		{name: "replacement insert failure", failAt: "insert"}, {name: "commit failure", failAt: "commit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "release gate test should create an isolated transaction mock")
			t.Cleanup(mock.Close)
			datasetID, userID := uuid.New(), uuid.New()
			gate := models.EvalReleaseGate{OrgID: uuid.New(), GateName: "API release", Enabled: true, DatasetID: &datasetID, MinPassAt1: .8, MinPassAtK: .9, MaxPolicyViolations: 1, MaxRegressionDelta: .05, UpdatedByUserID: &userID}
			if tt.custom {
				gate.CanaryStages = json.RawMessage(`[5,50,100]`)
				gate.RollbackRules = json.RawMessage(`{"timeout":true}`)
			}
			expected := gate
			if !tt.custom {
				expected.CanaryStages = json.RawMessage(`[10,30,100]`)
				expected.RollbackRules = json.RawMessage(`{}`)
			}
			fault := errors.New("gate transaction failed")
			begin := mock.ExpectBegin()
			if tt.failAt == "begin" {
				begin.WillReturnError(fault)
			} else {
				update := mock.ExpectExec(`UPDATE eval_release_gates SET active = false WHERE org_id = @org_id AND gate_name = @gate_name AND active = true`).WithArgs(pgx.NamedArgs{"org_id": gate.OrgID, "gate_name": gate.GateName})
				if tt.failAt == "update" {
					update.WillReturnError(fault)
				} else {
					update.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
					insert := mock.ExpectQuery(`INSERT INTO eval_release_gates`).WithArgs(pgx.NamedArgs{"org_id": gate.OrgID, "gate_name": gate.GateName, "enabled": true, "dataset_id": &datasetID, "min_pass_at_1": .8, "min_pass_at_k": .9, "max_policy_violations": 1, "max_regression_delta": .05, "canary_stages": expected.CanaryStages, "rollback_rules": expected.RollbackRules, "updated_by_user_id": &userID})
					if tt.failAt == "insert" {
						insert.WillReturnError(fault)
					} else {
						persisted := expected
						persisted.ID = uuid.New()
						persisted.Active = true
						persisted.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
						insert.WillReturnRows(evalGateTestRows(persisted))
						commit := mock.ExpectCommit()
						if tt.failAt == "commit" {
							commit.WillReturnError(fault)
						} else {
							expected = persisted
						}
					}
				}
				if tt.failAt != "" {
					mock.ExpectRollback()
				}
			}
			err = NewEvalReleaseGateStore(mock).Upsert(t.Context(), &gate)
			if tt.failAt != "" {
				require.ErrorIs(t, err, fault, "transaction errors should preserve their original cause")
			} else {
				require.NoError(t, err, "gate replacement should commit atomically")
			}
			require.Equal(t, expected, gate, "only committed gate versions should replace the caller's model")
			require.NoError(t, mock.ExpectationsWereMet(), "failed replacements should roll back after tenant-scoped inactivation")
		})
	}
}

func TestEvalReleaseGateEvaluateBatchDecisionContracts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		total, passed    int
		enabled, dataset bool
		average          *float64
		status           models.EvalReleaseGateDecisionStatus
		reason, failure  string
	}{
		{name: "threshold equality passes", total: 10, passed: 8, enabled: true, average: float64Ptr(.9), status: models.EvalReleaseGateDecisionPassed, reason: "release gate passed"},
		{name: "below threshold fails", total: 10, passed: 7, enabled: true, dataset: true, average: float64Ptr(.7), status: models.EvalReleaseGateDecisionFailed, reason: "pass rate 0.70 is below required pass@1 0.80"},
		{name: "no completed data", enabled: true, dataset: true, status: models.EvalReleaseGateDecisionNoData, reason: "no completed eval runs matched this gate"},
		{name: "disabled gate cannot pass", total: 10, passed: 10, status: models.EvalReleaseGateDecisionNoData, reason: "release gate is disabled"},
		{name: "missing average uses zero", total: 1, passed: 1, enabled: true, status: models.EvalReleaseGateDecisionPassed, reason: "release gate passed"},
		{name: "gate query failure", failure: "gates"}, {name: "metrics failure", enabled: true, failure: "metrics"},
		{name: "decision persistence failure", total: 1, passed: 1, enabled: true, status: models.EvalReleaseGateDecisionPassed, reason: "release gate passed", failure: "decision"},
		{name: "no active gates", failure: "empty"},
		{name: "nonfinite metric cannot publish", total: 1, passed: 1, enabled: true, average: float64Ptr(math.NaN()), failure: "marshal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "decision test should create its own database mock")
			t.Cleanup(mock.Close)
			orgID, batchID, datasetID := uuid.New(), uuid.New(), uuid.New()
			gate := models.EvalReleaseGate{ID: uuid.New(), OrgID: orgID, GateName: "API release", Enabled: tt.enabled, Active: true, MinPassAt1: .8, CanaryStages: json.RawMessage(`[10,30,100]`), RollbackRules: json.RawMessage(`{}`), CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
			if tt.dataset {
				gate.DatasetID = &datasetID
			}
			fault := errors.New("evaluation database failed")
			list := mock.ExpectQuery(`SELECT .*FROM eval_release_gates WHERE org_id = @org_id AND active = true ORDER BY gate_name`).WithArgs(pgx.NamedArgs{"org_id": orgID})
			var expected []models.EvalReleaseGateDecision
			switch tt.failure {
			case "gates":
				list.WillReturnError(fault)
			case "empty":
				list.WillReturnRows(evalGateTestRows())
				expected = []models.EvalReleaseGateDecision{}
			default:
				list.WillReturnRows(evalGateTestRows(gate))
				args := pgx.NamedArgs{"org_id": orgID, "batch_id": batchID, "gate_id": gate.ID}
				pattern := `SELECT COUNT\(\*\)::int, COUNT\(\*\) FILTER \(WHERE passed = true\)::int, AVG\(final_score\) FROM eval_runs WHERE eval_runs.org_id = @org_id AND eval_runs.batch_id = @batch_id AND eval_runs.status = 'completed'`
				if tt.dataset {
					args["dataset_id"] = datasetID
					pattern += ` AND EXISTS .*dt.org_id = eval_runs.org_id AND dt.dataset_id = @dataset_id AND dt.task_id = eval_runs.task_id`
				}
				metricsQuery := mock.ExpectQuery(pattern).WithArgs(args)
				if tt.failure == "metrics" {
					metricsQuery.WillReturnError(fault)
				} else {
					metricsQuery.WillReturnRows(pgxmock.NewRows([]string{"total", "passed", "average"}).AddRow(tt.total, tt.passed, tt.average))
					if tt.failure != "marshal" {
						rate, avg := 0.0, 0.0
						if tt.total > 0 {
							rate = float64(tt.passed) / float64(tt.total)
						}
						if tt.average != nil {
							avg = *tt.average
						}
						metrics, err := json.Marshal(map[string]any{"total_runs": tt.total, "passed_runs": tt.passed, "pass_rate": rate, "average_score": avg, "min_pass_at_1": .8})
						require.NoError(t, err, "expected gate metrics should serialize")
						decision := models.EvalReleaseGateDecision{ID: uuid.New(), OrgID: orgID, BatchID: batchID, GateID: gate.ID, Status: tt.status, Reason: tt.reason, Metrics: metrics, CreatedAt: gate.CreatedAt}
						insert := mock.ExpectQuery(`INSERT INTO eval_release_gate_decisions .*ON CONFLICT \(org_id, batch_id, gate_id\) DO UPDATE`).WithArgs(pgx.NamedArgs{"org_id": orgID, "batch_id": batchID, "gate_id": gate.ID, "status": tt.status, "reason": tt.reason, "metrics": []byte(metrics)})
						if tt.failure == "decision" {
							insert.WillReturnError(fault)
						} else {
							insert.WillReturnRows(evalDecisionTestRows(decision))
							expected = []models.EvalReleaseGateDecision{decision}
						}
					}
				}
			}
			actual, err := NewEvalReleaseGateStore(mock).EvaluateBatch(t.Context(), orgID, batchID)
			switch tt.failure {
			case "gates", "metrics", "decision":
				require.ErrorIs(t, err, fault, "failed evaluation should preserve the dependency cause")
				require.Nil(t, actual, "failed evaluation should not claim a completed decision list")
			case "marshal":
				require.ErrorContains(t, err, "marshal eval release gate metrics", "invalid metrics should not produce a successful decision")
				require.Nil(t, actual, "unserializable metrics should fail closed")
			default:
				require.NoError(t, err, "complete metrics should produce durable gate decisions")
				require.Equal(t, expected, actual, "gate outcome, reason, and complete metrics should match the threshold contract")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "evaluation should scope both metrics and durable decisions to the same tenant and batch")
		})
	}
}

func TestEvalReleaseGateDecisionListFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, failure string
		empty         bool
	}{{name: "durable decision"}, {name: "no decisions", empty: true}, {name: "query error", failure: "query"}, {name: "scan error", failure: "scan"}, {name: "iteration error", failure: "iteration"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "decision list should have an isolated mock")
			t.Cleanup(mock.Close)
			orgID, batchID := uuid.New(), uuid.New()
			decision := models.EvalReleaseGateDecision{ID: uuid.New(), OrgID: orgID, BatchID: batchID, GateID: uuid.New(), Status: models.EvalReleaseGateDecisionFailed, Reason: "below threshold", Metrics: json.RawMessage(`{"pass_rate":0.5}`), CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
			query := mock.ExpectQuery(`SELECT .*FROM eval_release_gate_decisions WHERE org_id = @org_id AND batch_id = @batch_id ORDER BY created_at DESC`).WithArgs(pgx.NamedArgs{"org_id": orgID, "batch_id": batchID})
			fault := errors.New("decision stream failed")
			switch tt.failure {
			case "query":
				query.WillReturnError(fault)
			case "scan":
				query.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("invalid"))
			case "iteration":
				query.WillReturnRows(evalDecisionTestRows(decision).RowError(0, fault))
			default:
				if tt.empty {
					query.WillReturnRows(evalDecisionTestRows())
				} else {
					query.WillReturnRows(evalDecisionTestRows(decision))
				}
			}
			actual, err := NewEvalReleaseGateStore(mock).ListDecisionsByBatch(context.Background(), orgID, batchID)
			switch {
			case tt.failure == "scan":
				require.ErrorContains(t, err, "scan eval release gate decision", "malformed decisions should not be returned")
			case tt.failure != "":
				require.ErrorIs(t, err, fault, "decision query and iteration errors should propagate")
			case tt.empty:
				require.NoError(t, err, "missing decisions should list successfully")
				require.Equal(t, []models.EvalReleaseGateDecision(nil), actual, "empty store list should contain no decisions")
			default:
				require.NoError(t, err, "durable decisions should load")
				require.Equal(t, []models.EvalReleaseGateDecision{decision}, actual, "list should preserve the complete stored decision")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "decision list should be tenant and batch scoped")
		})
	}
}

func float64Ptr(value float64) *float64 { return &value }
