-- Usage rollups for the preview dashboard.

DELETE FROM usage_hourly
WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid
  AND hour_utc >= date_trunc('day', now()) - interval '13 days'
  AND hour_utc < date_trunc('day', now()) + interval '1 day'
  AND (user_id IS NULL OR user_id IN (
    '00000000-0000-4000-a000-000000000002'::uuid,
    '00000000-0000-4000-a000-000000000003'::uuid,
    '00000000-0000-4000-a000-000000000004'::uuid
  ))
  AND (capacity_tier IS NULL OR capacity_tier IN (
    '2cpu_4096mb_10240diskmb',
    '4cpu_8192mb_20480diskmb'
  ));

WITH days AS (
  SELECT generate_series(0, 13) AS day_offset
),
buckets AS (
  SELECT
    date_trunc('day', now()) - make_interval(days => day_offset) + interval '10 hours' AS hour_utc,
    day_offset
  FROM days
),
rows AS (
  SELECT
    hour_utc,
    NULL::uuid AS user_id,
    NULL::text AS capacity_tier,
    130 + (day_offset * 4) AS container_minutes,
    8 + (day_offset % 3) AS sessions,
    10 + (day_offset % 4) AS starts,
    3 + (day_offset % 2) AS peak,
    980 + (day_offset * 12) AS avg_duration,
    1800 + (day_offset * 18) AS p95_duration,
    52000 + (day_offset * 900) AS input_tokens,
    11000 + (day_offset * 240) AS output_tokens,
    3.10 + (day_offset * 0.08) AS cost_usd
  FROM buckets
  UNION ALL
  SELECT
    hour_utc,
    '00000000-0000-4000-a000-000000000002'::uuid,
    '2cpu_4096mb_10240diskmb',
    46 + (day_offset * 2),
    3 + (day_offset % 2),
    4 + (day_offset % 2),
    2,
    840 + (day_offset * 8),
    1460 + (day_offset * 10),
    21000 + (day_offset * 420),
    4800 + (day_offset * 110),
    1.20 + (day_offset * 0.04)
  FROM buckets
  UNION ALL
  SELECT
    hour_utc,
    '00000000-0000-4000-a000-000000000004'::uuid,
    '4cpu_8192mb_20480diskmb',
    74 + (day_offset * 3),
    4 + (day_offset % 2),
    5 + (day_offset % 3),
    2 + (day_offset % 2),
    1120 + (day_offset * 9),
    2060 + (day_offset * 14),
    30000 + (day_offset * 520),
    6300 + (day_offset * 130),
    1.78 + (day_offset * 0.05)
  FROM buckets
)
INSERT INTO usage_hourly (
  id, org_id, hour_utc, user_id, capacity_tier,
  total_container_minutes, total_sessions, total_container_starts,
  peak_concurrent, avg_duration_sec, p95_duration_sec,
  total_input_tokens, total_output_tokens, total_llm_cost_usd,
  created_at, updated_at
)
SELECT
  gen_random_uuid(),
  '00000000-0000-4000-a000-000000000001'::uuid,
  hour_utc,
  user_id,
  capacity_tier,
  container_minutes,
  sessions,
  starts,
  peak,
  avg_duration,
  p95_duration,
  input_tokens,
  output_tokens,
  cost_usd,
  now(),
  now()
FROM rows;

DELETE FROM usage_hourly_execution
WHERE org_id = '00000000-0000-4000-a000-000000000001'::uuid
  AND hour_utc >= date_trunc('day', now()) - interval '13 days'
  AND hour_utc < date_trunc('day', now()) + interval '1 day'
  AND agent_type IN ('codex','claude_code','opencode')
  AND capacity_key IN ('2cpu_4096mb_10240diskmb','4cpu_8192mb_20480diskmb');

WITH days AS (
  SELECT generate_series(0, 13) AS day_offset
),
buckets AS (
  SELECT
    date_trunc('day', now()) - make_interval(days => day_offset) + interval '10 hours' AS hour_utc,
    day_offset
  FROM days
),
rows AS (
  SELECT
    hour_utc,
    'codex'::text AS agent_type,
    'gpt-5.1-codex-max'::text AS model_used,
    'medium'::text AS reasoning_effort,
    '2cpu_4096mb_10240diskmb'::text AS capacity_key,
    54 + (day_offset * 2) AS container_minutes,
    3 + (day_offset % 2) AS sessions,
    4 + (day_offset % 2) AS starts,
    2 AS peak,
    23000 + (day_offset * 410) AS input_tokens,
    5200 + (day_offset * 115) AS output_tokens,
    1.36 + (day_offset * 0.03) AS cost_usd
  FROM buckets
  UNION ALL
  SELECT
    hour_utc,
    'claude_code',
    'claude-opus-4-5',
    'high',
    '4cpu_8192mb_20480diskmb',
    48 + (day_offset * 2),
    2 + (day_offset % 2),
    3 + (day_offset % 2),
    2,
    18000 + (day_offset * 360),
    3900 + (day_offset * 95),
    1.02 + (day_offset * 0.03)
  FROM buckets
  UNION ALL
  SELECT
    hour_utc,
    'opencode',
    'gpt-5.5',
    'medium',
    '2cpu_4096mb_10240diskmb',
    28 + day_offset,
    2,
    2,
    1,
    11000 + (day_offset * 220),
    1900 + (day_offset * 55),
    0.72 + (day_offset * 0.02)
  FROM buckets
)
INSERT INTO usage_hourly_execution (
  org_id, hour_utc, agent_type, model_used, reasoning_effort, capacity_key,
  total_container_minutes, total_sessions, total_container_starts,
  peak_concurrent, total_input_tokens, total_output_tokens, total_tokens,
  total_llm_cost_usd, created_at, updated_at
)
SELECT
  '00000000-0000-4000-a000-000000000001'::uuid,
  hour_utc,
  agent_type,
  model_used,
  reasoning_effort,
  capacity_key,
  container_minutes,
  sessions,
  starts,
  peak,
  input_tokens,
  output_tokens,
  input_tokens + output_tokens,
  cost_usd,
  now(),
  now()
FROM rows;
