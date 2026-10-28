-- The highest migration has already locked, refused unresolved work, and disabled inserts.
ALTER TABLE session_review_loops DROP CONSTRAINT fk_srl_startup_pass;
