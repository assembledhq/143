-- Passive identity foundation only: no producer, consumer, claim or runnable
-- admission state exists yet. Stored JSON and epochs supply no authority.
-- Install each remaining hot-parent FK in its own subsequent transaction.
SET LOCAL lock_timeout = '5s';

CREATE TABLE session_input_dispatches (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    session_id uuid NOT NULL,
    thread_id uuid,
    session_epoch bigint,
    thread_epoch bigint,
    -- Partitioned messages lack a tenant-scoped unique referenced key. This
    -- identity needs future canonical transaction-side ownership validation.
    message_id bigint,
    request_id uuid,
    kind text NOT NULL,
    ingress_kind text NOT NULL,
    ingress_key text NOT NULL,
    payload_digest text NOT NULL,
    validated_payload jsonb NOT NULL,
    normalization_version integer NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT session_input_dispatches_kind_check
        CHECK (kind IN ('message', 'answer', 'denial', 'initial', 'retry', 'recovery')),
    CONSTRAINT session_input_dispatches_ingress_kind_check
        CHECK (btrim(ingress_kind) <> ''),
    CONSTRAINT session_input_dispatches_ingress_key_check
        CHECK (btrim(ingress_key) <> ''),
    CONSTRAINT session_input_dispatches_payload_digest_check
        CHECK (btrim(payload_digest) <> ''),
    CONSTRAINT session_input_dispatches_normalization_version_check
        CHECK (normalization_version >= 0),
    CONSTRAINT session_input_dispatches_session_epoch_check
        CHECK (session_epoch IS NULL OR session_epoch >= 0),
    CONSTRAINT session_input_dispatches_thread_epoch_check
        CHECK (thread_epoch IS NULL OR thread_epoch >= 0),
    CONSTRAINT session_input_dispatches_thread_epoch_lane_check
        CHECK (thread_epoch IS NULL OR thread_id IS NOT NULL),
    CONSTRAINT session_input_dispatches_message_id_check
        CHECK (message_id IS NULL OR message_id > 0),
    CONSTRAINT session_input_dispatches_request_kind_check
        CHECK (kind NOT IN ('answer', 'denial') OR request_id IS NOT NULL),
    CONSTRAINT session_input_dispatches_ingress_key
        UNIQUE (org_id, ingress_kind, ingress_key),
    -- Future logical claim pointers can reference exact parent membership.
    CONSTRAINT session_input_dispatches_scope_key
        UNIQUE (org_id, session_id, id)
);

CREATE UNIQUE INDEX idx_session_input_dispatches_message
    ON session_input_dispatches (org_id, message_id)
    WHERE message_id IS NOT NULL;
CREATE INDEX idx_session_input_dispatches_lane
    ON session_input_dispatches (org_id, session_id, thread_id);
CREATE INDEX idx_session_input_dispatches_request
    ON session_input_dispatches (org_id, session_id, request_id)
    WHERE request_id IS NOT NULL;
