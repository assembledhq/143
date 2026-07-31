-- PR readiness is a removed product subsystem. Block rolling deploys while an
-- old worker is executing a readiness job, then prevent old API/worker
-- processes from enqueueing new work after this migration commits.
LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM jobs
        WHERE status = 'running'
          AND job_type = 'run_pr_readiness'
    ) THEN
        RAISE EXCEPTION
            'PR readiness removal requires all running readiness jobs to be drained before migration';
    END IF;
END
$$;

CREATE FUNCTION reject_removed_pr_readiness_jobs()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.job_type = 'run_pr_readiness' THEN
        RAISE EXCEPTION 'job type % was removed with the PR readiness subsystem', NEW.job_type
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER trg_reject_removed_pr_readiness_jobs
BEFORE INSERT OR UPDATE OF job_type ON jobs
FOR EACH ROW
EXECUTE FUNCTION reject_removed_pr_readiness_jobs();

-- Cancel queued work instead of deleting it: historical job rows stay readable
-- for operators (matching the PM shutdown in 000259), and a DELETE over the
-- whole table has no usable index on job_type alone, so it would hold the jobs
-- lock above for a full sequential scan. Cancelled rows are not covered by
-- delete_expired_completed_jobs, so they persist; the queued readiness backlog
-- is small and bounded, exactly as the PM shutdown left its cancelled rows.
UPDATE jobs
SET status = 'cancelled',
    updated_at = now(),
    last_error = 'cancelled: PR readiness subsystem removed'
WHERE status = 'pending'
  AND job_type = 'run_pr_readiness';

DELETE FROM session_changeset_leases
WHERE holder_type = 'readiness';

-- ParseUserSettings decodes with DisallowUnknownFields, so a retired user
-- preference is a hard decode failure once the new version ships. Org settings
-- decode leniently; strip them too so the stored documents stay truthful.
UPDATE organizations
SET settings = settings
    #- '{session_automation,automatic_follow_through,readiness_after_review_loop}'
    #- '{session_automation,automatic_follow_through,readiness_after_review_loop_states}'
WHERE (settings #> '{session_automation,automatic_follow_through}') ?| ARRAY[
    'readiness_after_review_loop',
    'readiness_after_review_loop_states'
];

UPDATE users
SET settings = settings
    #- '{automatic_pr_follow_through,readiness_after_review_loop}'
WHERE (settings #> '{automatic_pr_follow_through}') ? 'readiness_after_review_loop';

-- Remove the retired event from custom Slack subscriptions. If it was the
-- only explicit event, pin the preset to custom before producing an empty
-- array so subscription matching does not fall back to a broader preset.
UPDATE slack_bot_settings
SET notification_preset = 'custom'
WHERE notification_subscriptions->'events' @> '["pr.readiness_attention"]'::jsonb
  AND NOT EXISTS (
      SELECT 1
      FROM jsonb_array_elements(notification_subscriptions->'events') AS item(value)
      WHERE item.value <> '"pr.readiness_attention"'::jsonb
  );

UPDATE slack_channel_settings
SET notification_preset = 'custom'
WHERE notification_subscriptions->'events' @> '["pr.readiness_attention"]'::jsonb
  AND NOT EXISTS (
      SELECT 1
      FROM jsonb_array_elements(notification_subscriptions->'events') AS item(value)
      WHERE item.value <> '"pr.readiness_attention"'::jsonb
  );

UPDATE slack_bot_settings
SET notification_subscriptions = jsonb_set(
    notification_subscriptions,
    '{events}',
    COALESCE((
        SELECT jsonb_agg(item.value)
        FROM jsonb_array_elements(notification_subscriptions->'events') AS item(value)
        WHERE item.value <> '"pr.readiness_attention"'::jsonb
    ), '[]'::jsonb)
)
WHERE notification_subscriptions->'events' @> '["pr.readiness_attention"]'::jsonb;

UPDATE slack_channel_settings
SET notification_subscriptions = jsonb_set(
    notification_subscriptions,
    '{events}',
    COALESCE((
        SELECT jsonb_agg(item.value)
        FROM jsonb_array_elements(notification_subscriptions->'events') AS item(value)
        WHERE item.value <> '"pr.readiness_attention"'::jsonb
    ), '[]'::jsonb)
)
WHERE notification_subscriptions->'events' @> '["pr.readiness_attention"]'::jsonb;

ALTER TABLE session_changeset_leases
    DROP CONSTRAINT session_changeset_leases_holder_type_check,
    ADD CONSTRAINT session_changeset_leases_holder_type_check
        CHECK (holder_type IN ('agent_turn', 'materialize', 'publish', 'restack', 'preview'));

DROP TRIGGER IF EXISTS trg_pr_readiness_assign_primary_changeset ON pr_readiness_runs;
DROP FUNCTION IF EXISTS assign_pr_readiness_primary_changeset();

DROP TABLE pr_readiness_bypasses;
DROP TABLE pr_readiness_checks;
DROP TABLE pr_readiness_runs;
DROP TABLE pr_readiness_custom_checks;
DROP TABLE pr_readiness_policies;
DROP TABLE pr_readiness_contexts;
