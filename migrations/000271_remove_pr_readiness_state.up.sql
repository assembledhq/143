-- Complete the destructive PR-readiness cleanup after migration 000267 has
-- committed its jobs-only rolling barrier. Keep this migration idempotent so
-- databases that successfully applied the original all-in-one 000267 can move
-- forward alongside production, where that original migration rolled back.

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

DO $$
BEGIN
    IF to_regclass('pr_readiness_runs') IS NOT NULL THEN
        DROP TRIGGER IF EXISTS trg_pr_readiness_assign_primary_changeset ON pr_readiness_runs;
    END IF;
END
$$;

DROP FUNCTION IF EXISTS assign_pr_readiness_primary_changeset();

DROP TABLE IF EXISTS pr_readiness_bypasses;
DROP TABLE IF EXISTS pr_readiness_checks;
DROP TABLE IF EXISTS pr_readiness_runs;
DROP TABLE IF EXISTS pr_readiness_custom_checks;
DROP TABLE IF EXISTS pr_readiness_policies;
DROP TABLE IF EXISTS pr_readiness_contexts;
