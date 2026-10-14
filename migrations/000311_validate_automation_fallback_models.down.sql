-- PostgreSQL cannot mark a validated CHECK constraint NOT VALID again. The 310
-- down migration removes the constraint together with its column.
SELECT 1;
