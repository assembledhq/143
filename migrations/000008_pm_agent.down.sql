ALTER TABLE agent_runs
    DROP COLUMN IF EXISTS pm_plan_id,
    DROP COLUMN IF EXISTS pm_approach,
    DROP COLUMN IF EXISTS pm_reasoning;

DROP TABLE IF EXISTS pm_decision_log;
DROP TABLE IF EXISTS pm_plans;
