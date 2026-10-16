-- PostgreSQL cannot mark a validated CHECK constraint NOT VALID again. The 314
-- down migration removes the constraint together with its column.
SELECT 1;
