ALTER TABLE code_review_policies
    ADD COLUMN final_review_template text NOT NULL DEFAULT '';
