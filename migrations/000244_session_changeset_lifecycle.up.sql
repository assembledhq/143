ALTER TABLE session_changesets
    ADD COLUMN restack_delta_kind text,
    ADD COLUMN restack_delta_summary text,
    ADD COLUMN restack_confirmation_required boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT session_changesets_restack_delta_kind_check CHECK (
        restack_delta_kind IS NULL OR restack_delta_kind IN ('clean_replay', 'mechanical_fallout', 'semantic_change')
    );

CREATE TABLE session_changeset_leases (
    changeset_id uuid PRIMARY KEY,
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    session_id uuid NOT NULL,
    holder_id uuid NOT NULL,
    holder_type text NOT NULL CHECK (holder_type IN ('agent_turn', 'materialize', 'publish', 'restack', 'readiness', 'preview')),
    holder_label text NOT NULL DEFAULT '',
    acquired_at timestamptz NOT NULL DEFAULT now(),
    heartbeat_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    FOREIGN KEY (changeset_id, org_id, session_id)
        REFERENCES session_changesets(id, org_id, session_id) ON DELETE CASCADE,
    CHECK (expires_at > acquired_at)
);

CREATE INDEX session_changeset_leases_session
    ON session_changeset_leases (org_id, session_id, expires_at);

CREATE INDEX session_changesets_stack_order
    ON session_changesets (org_id, session_id, stacked_on_changeset_id, order_index)
    WHERE status <> 'abandoned';
