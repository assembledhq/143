ALTER TABLE code_review_prompt_records DROP COLUMN assessment_id;
ALTER TABLE code_review_findings DROP COLUMN assessment_id;
ALTER TABLE code_review_agent_results DROP COLUMN assessment_id;
ALTER TABLE code_review_session_metadata DROP COLUMN assessment_id;
DROP TABLE code_review_revision_assessments;
DROP INDEX code_review_metadata_identity;
DROP INDEX code_review_policies_org_id;
