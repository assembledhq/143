-- The prompt columns were added (000250) with database defaults purely to
-- backfill existing rows during rollout. All writers now supply both fields
-- explicitly (CodeReviewStore.SavePolicy), so the DB defaults are dead
-- scaffolding and the Go layer is the single source of truth for defaults.
ALTER TABLE code_review_policies
    ALTER COLUMN review_instructions DROP DEFAULT,
    ALTER COLUMN automated_approval_policy DROP DEFAULT;
