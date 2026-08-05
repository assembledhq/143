-- PostgreSQL cannot mark a validated CHECK constraint NOT VALID again. The 281
-- down migration removes the constraint together with its guarded columns.
SELECT 1;
