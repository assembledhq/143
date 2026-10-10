-- Ranked fallback models for automations. Rank 0 stays in the existing
-- agent_type/model_override/reasoning_effort scalar columns; this object holds
-- ranks 1..N as index-aligned arrays, mirroring code_review_policies.agent_roster.
--
-- Model validity is enforced in Go (models.ValidateModelForAgentType) as it is
-- everywhere else in this schema; a SQL allowlist would require a migration
-- every time a model ships. The type guard only pins the container shape.
ALTER TABLE automations
    ADD COLUMN fallback_models jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD CONSTRAINT chk_automations_fallback_models
        CHECK (jsonb_typeof(fallback_models) = 'object');
