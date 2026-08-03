-- Drops whichever table this installation carries: a fresh database has the
-- records table this migration created, while a database that was expanded and
-- then contracted by migration 275 carries the legacy artifacts table.
DROP TABLE IF EXISTS code_review_prompt_records;
DROP TABLE IF EXISTS code_review_prompt_artifacts;
