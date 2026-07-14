-- Release channels split execution into a canary plane (latest main, dogfood
-- orgs) and a stable plane (pinned releases, customer orgs) over one shared
-- database. See docs/design/118-canary-stable-release-channels.md.

-- Which plane serves and executes work for this org. Flips are operator
-- actions and should happen while the org has no active session executors or
-- preview runtimes (pinned target_node_id jobs would otherwise take the
-- crash-recovery path onto the other pool).
ALTER TABLE organizations
    ADD COLUMN release_channel text NOT NULL DEFAULT 'stable'
    CONSTRAINT organizations_release_channel_check
    CHECK (release_channel IN ('stable', 'canary'));

-- Stamped at enqueue time from the org's release_channel (snapshot, not a
-- live join): in-flight and pending jobs finish on the channel they were
-- enqueued for. Workers claim only jobs matching their own channel.
ALTER TABLE jobs
    ADD COLUMN channel text NOT NULL DEFAULT 'stable'
    CONSTRAINT jobs_channel_check
    CHECK (channel IN ('stable', 'canary'));

ALTER TABLE nodes
    ADD COLUMN channel text NOT NULL DEFAULT 'stable'
    CONSTRAINT nodes_channel_check
    CHECK (channel IN ('stable', 'canary'));

-- Claim-path index: each channel's worker pool scans only its own pending
-- backlog, in the same order ClaimNextRunnable claims (priority DESC,
-- created_at ASC).
CREATE INDEX idx_jobs_pending_claim_channel
    ON jobs (channel, priority DESC, created_at)
    WHERE status = 'pending';

-- Written by `migrate up` when a gated destructive migration is applied; read
-- by stable promote/rollback preflights (`migrate verify`). Persisted because
-- schema_migrations stores only a bare version integer and an older checkout
-- does not contain the newer migration files whose comments carry the
-- lint:destructive-ok-after annotations.
CREATE TABLE schema_compat_floors ( -- lint:no-org-id reason="schema compatibility metadata, not tenant data"
    migration_version bigint PRIMARY KEY,
    stable_floor      bigint NOT NULL,
    applied_at        timestamptz NOT NULL DEFAULT now()
);
