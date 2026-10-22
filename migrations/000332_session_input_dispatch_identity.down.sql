-- Roll back future readers/writers first. Do not cascade dependent claims or
-- treat this schema rollback as a policy for deleting retained input evidence.
SET LOCAL lock_timeout = '5s';
DROP TABLE session_input_dispatches;
