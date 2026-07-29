ALTER TABLE thread_inbox_entries
    ADD COLUMN applied_at timestamptz;

CREATE TABLE thread_inbox_delivery_batches (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    thread_id uuid NOT NULL REFERENCES session_threads(id) ON DELETE CASCADE,
    runtime_id uuid NOT NULL REFERENCES thread_runtimes(id) ON DELETE RESTRICT,
    sequence_start bigint NOT NULL,
    sequence_end bigint NOT NULL,
    status text NOT NULL,
    acknowledged_at timestamptz NOT NULL,
    started_at timestamptz,
    abandoned_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_thread_inbox_delivery_batches_range CHECK (sequence_start > 0 AND sequence_end >= sequence_start),
    CONSTRAINT chk_thread_inbox_delivery_batches_status CHECK (status IN ('acknowledged', 'started', 'abandoned')),
    CONSTRAINT chk_thread_inbox_delivery_batches_lifecycle CHECK (
        (status = 'acknowledged' AND started_at IS NULL AND abandoned_at IS NULL)
        OR (status = 'started' AND started_at IS NOT NULL AND abandoned_at IS NULL)
        OR (status = 'abandoned' AND started_at IS NULL AND abandoned_at IS NOT NULL)
    ),
    CONSTRAINT uq_thread_inbox_delivery_batches_range UNIQUE (org_id, thread_id, sequence_start, sequence_end)
);

CREATE TABLE session_activity_phases (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    thread_id uuid NOT NULL REFERENCES session_threads(id) ON DELETE CASCADE,
    turn_number integer NOT NULL,
    phase_number integer NOT NULL,
    status text NOT NULL,
    boundary_reason text,
    started_at timestamptz NOT NULL,
    completed_at timestamptz,
    runtime_id uuid REFERENCES thread_runtimes(id) ON DELETE SET NULL,
    trigger_kind text NOT NULL,
    trigger_batch_id uuid REFERENCES thread_inbox_delivery_batches(id),
    trigger_sequence_start bigint,
    trigger_sequence_end bigint,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_session_activity_phases_turn_nonnegative CHECK (turn_number >= 0),
    CONSTRAINT chk_session_activity_phases_phase_positive CHECK (phase_number > 0),
    CONSTRAINT chk_session_activity_phases_status CHECK (status IN ('running', 'completed', 'failed', 'cancelled', 'interrupted')),
    CONSTRAINT chk_session_activity_phases_trigger_kind CHECK (trigger_kind IN ('initial', 'inbox_batch', 'recovery')),
    CONSTRAINT chk_session_activity_phases_lifecycle CHECK (
        (status = 'running' AND completed_at IS NULL AND boundary_reason IS NULL)
        OR (status <> 'running' AND completed_at IS NOT NULL AND boundary_reason IS NOT NULL)
    ),
    CONSTRAINT chk_session_activity_phases_time_order CHECK (completed_at IS NULL OR completed_at >= started_at),
    CONSTRAINT chk_session_activity_phases_status_reason CHECK (
        (status = 'running' AND boundary_reason IS NULL)
        OR (status = 'completed' AND boundary_reason IN ('final_response', 'human_input', 'approval', 'plan_approval', 'steered'))
        OR (status = 'failed' AND boundary_reason = 'error')
        OR (status = 'cancelled' AND boundary_reason IN ('stopped', 'cancelled'))
        OR (status = 'interrupted' AND boundary_reason IN ('maintenance', 'runtime_lost', 'capacity_suspended', 'interrupted'))
    ),
    CONSTRAINT chk_session_activity_phases_trigger_range CHECK (
        (trigger_kind = 'inbox_batch' AND trigger_batch_id IS NOT NULL
            AND trigger_sequence_start IS NOT NULL AND trigger_sequence_end IS NOT NULL
            AND trigger_sequence_start > 0 AND trigger_sequence_end >= trigger_sequence_start)
        OR (trigger_kind <> 'inbox_batch' AND trigger_batch_id IS NULL AND trigger_sequence_start IS NULL AND trigger_sequence_end IS NULL)
    ),
    CONSTRAINT uq_session_activity_phase_number UNIQUE (org_id, thread_id, turn_number, phase_number)
);

CREATE UNIQUE INDEX idx_session_activity_phases_one_running
    ON session_activity_phases (org_id, thread_id) WHERE status = 'running';
CREATE UNIQUE INDEX idx_session_activity_phases_trigger_batch
    ON session_activity_phases (org_id, trigger_batch_id) WHERE trigger_batch_id IS NOT NULL;
CREATE INDEX idx_session_activity_phases_transcript
    ON session_activity_phases (org_id, thread_id, turn_number, phase_number);
CREATE INDEX idx_session_activity_phases_runtime_running
    ON session_activity_phases (org_id, runtime_id) WHERE status = 'running';
CREATE INDEX idx_thread_inbox_delivery_batches_runtime
    ON thread_inbox_delivery_batches (org_id, runtime_id, status);
