ALTER TABLE code_review_policies
    ADD COLUMN continuation_policy jsonb NOT NULL DEFAULT '{}'::jsonb;
