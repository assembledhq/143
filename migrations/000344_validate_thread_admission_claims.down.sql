-- Validation is intentionally not undone: migration 343 down removes the
-- constraints and columns. Do not reacquire a stronger lock just to unvalidate.
SELECT 1;
