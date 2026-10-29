-- The highest migration has already locked, refused unresolved work, and disabled inserts.
SET LOCAL lock_timeout = '5s';
ALTER TABLE session_review_loops DROP CONSTRAINT fk_srl_startup_pass;
