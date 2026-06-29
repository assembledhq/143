ALTER TABLE code_review_policies
    ADD COLUMN IF NOT EXISTS final_review_template text NOT NULL DEFAULT '';
