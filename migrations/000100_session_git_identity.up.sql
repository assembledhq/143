-- Records which GitHub identity the agent used for git pushes during this
-- session. Stamped at session-start by the orchestrator after the identity
-- resolver picks the right token, so post-hoc auditing can answer
-- "did agent X ever push as user Y?" without walking the credential-helper
-- log.
--
-- Source values: "user" | "app" — kept as TEXT (not an enum) so we can add
-- new identity sources (e.g. "machine_user") without a migration.
ALTER TABLE sessions
    ADD COLUMN git_identity_source TEXT,
    ADD COLUMN git_identity_user_id UUID REFERENCES users(id) ON DELETE SET NULL;
