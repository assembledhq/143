DROP INDEX IF EXISTS session_changesets_stack_order;
DROP TABLE IF EXISTS session_changeset_leases;
ALTER TABLE session_changesets
    DROP CONSTRAINT IF EXISTS session_changesets_restack_delta_kind_check,
    DROP COLUMN IF EXISTS restack_confirmation_required,
    DROP COLUMN IF EXISTS restack_delta_summary,
    DROP COLUMN IF EXISTS restack_delta_kind;
