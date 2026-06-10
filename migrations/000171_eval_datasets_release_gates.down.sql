DROP INDEX IF EXISTS idx_eval_release_gates_recent;
DROP INDEX IF EXISTS uq_eval_release_gates_active;
DROP TABLE IF EXISTS eval_release_gates;

DROP INDEX IF EXISTS idx_eval_dataset_tasks_dataset;
DROP TABLE IF EXISTS eval_dataset_tasks;

DROP INDEX IF EXISTS idx_eval_datasets_repo_created;
DROP INDEX IF EXISTS idx_eval_datasets_type_status;
DROP TABLE IF EXISTS eval_datasets;
