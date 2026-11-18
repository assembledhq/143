-- Passive representation only. No row is created, normalized or activated by
-- this migration; stored version/state/JSON never establishes authority.
-- Install the hot session FK in its own following transaction.
SET LOCAL lock_timeout = '5s';

CREATE TABLE session_lifecycle_cutovers (
    org_id uuid NOT NULL REFERENCES organizations(id),
    session_id uuid NOT NULL,
    version bigint NOT NULL,
    state text NOT NULL,
    -- A retained message watermark, not a message reference or drain proof.
    -- NULL alone does not mean the historical input set was proved empty.
    legacy_input_cutoff_id bigint,
    normalization jsonb,
    activated_at timestamptz,
    minimum_writer_version integer,
    -- The tested multi-lane/provider/build envelope must be specified and
    -- verified before activation. Arbitrary stored JSON is not conformance.
    resumability_capability jsonb,
    CONSTRAINT session_lifecycle_cutovers_pkey PRIMARY KEY (org_id, session_id),
    CONSTRAINT session_lifecycle_cutovers_version_check CHECK (version >= 0),
    CONSTRAINT session_lifecycle_cutovers_state_check
        CHECK (state IN ('shadow', 'normalizing', 'active')),
    CONSTRAINT session_lifecycle_cutovers_cutoff_check
        CHECK (legacy_input_cutoff_id IS NULL OR legacy_input_cutoff_id > 0),
    CONSTRAINT session_lifecycle_cutovers_minimum_writer_check
        CHECK (minimum_writer_version IS NULL OR minimum_writer_version >= 0)
);
