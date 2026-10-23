-- Roll back readers/producers first. Do not discard unknown dependencies.
SET LOCAL lock_timeout = '5s';
DROP TABLE session_input_workspace_compatibility;
