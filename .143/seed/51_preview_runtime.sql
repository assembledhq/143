-- A seeded "ready" preview instance pointing at session 300.
-- worker_node_id = 'seeded' keeps the real recycler from touching this row.
INSERT INTO preview_instances (
  id, session_id, org_id, user_id, profile_name, name, status, provider,
  worker_node_id, preview_handle, primary_service, port, preview_target_id,
  config_digest, base_commit_sha, current_phase, last_path, expires_at,
  ready_at, created_at, updated_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000400'::uuid,
    '00000000-0000-4000-a000-000000000300'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    'manado',
    'Ship PR preview auto-teardown',
    'ready',
    'seeded',
    'seeded',
    '',
    'frontend',
    3000,
    '00000000-0000-4000-a000-000000000431'::uuid,
    'seeded-config-digest-preview-teardown',
    '1111111111111111111111111111111111111111',
    'ready',
    '/sessions/00000000-0000-4000-a000-000000000300',
    now() + interval '24 hours',
    now() - interval '29 minutes',
    now() - interval '30 minutes',
    now() - interval '2 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000401'::uuid,
    '00000000-0000-4000-a000-000000000304'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    'manado',
    'Normalize webhook replay cursor',
    'failed',
    'seeded',
    'seeded',
    '',
    'frontend',
    3000,
    NULL,
    'seeded-config-digest-webhook-replay',
    '3333333333333333333333333333333333333333',
    'failed',
    '/',
    now() - interval '5 hours',
    NULL,
    now() - interval '7 hours',
    now() - interval '6 hours'
  )
ON CONFLICT (id) DO UPDATE
SET status = EXCLUDED.status,
    preview_target_id = EXCLUDED.preview_target_id,
    config_digest = EXCLUDED.config_digest,
    base_commit_sha = EXCLUDED.base_commit_sha,
    current_phase = EXCLUDED.current_phase,
    last_path = EXCLUDED.last_path,
    ready_at = EXCLUDED.ready_at,
    updated_at = EXCLUDED.updated_at;

INSERT INTO preview_services (
  id, preview_instance_id, service_name, role, status, port, error, created_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000410'::uuid,
    '00000000-0000-4000-a000-000000000400'::uuid,
    'frontend', 'primary', 'ready', 3000,
    '',
    now() - interval '30 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000411'::uuid,
    '00000000-0000-4000-a000-000000000400'::uuid,
    'server', 'support', 'ready', 8080,
    '',
    now() - interval '30 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000412'::uuid,
    '00000000-0000-4000-a000-000000000401'::uuid,
    'frontend', 'primary', 'failed', 3000,
    'Synthetic startup check failed before the app became reachable.',
    now() - interval '30 minutes'
  )
ON CONFLICT (id) DO UPDATE
SET status = EXCLUDED.status,
    error = EXCLUDED.error;

INSERT INTO preview_infrastructure (
  id, preview_instance_id, infra_name, template, status, error, created_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000420'::uuid,
    '00000000-0000-4000-a000-000000000400'::uuid,
    'db', 'postgres-17', 'healthy', '',
    now() - interval '30 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000421'::uuid,
    '00000000-0000-4000-a000-000000000401'::uuid,
    'db', 'postgres-17', 'healthy', '',
    now() - interval '7 hours'
  )
ON CONFLICT (id) DO UPDATE
SET status = EXCLUDED.status,
    error = EXCLUDED.error;

INSERT INTO preview_runtimes (
  id, org_id, preview_instance_id, runtime_epoch, worker_node_id, endpoint_url,
  preview_handle, primary_port, status, lease_expires_at, last_heartbeat_at,
  stopped_at, error, created_at, updated_at, unavailable_reason
)
VALUES (
  '00000000-0000-4000-a000-000000000433'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000401'::uuid,
  1,
  'seeded',
  '',
  '',
  3000,
  'failed',
  now() - interval '6 hours',
  now() - interval '6 hours',
  now() - interval '6 hours',
  'Synthetic runtime exited before health check passed.',
  now() - interval '7 hours',
  now() - interval '6 hours',
  ''
)
ON CONFLICT (preview_instance_id, runtime_epoch) DO UPDATE
SET status = EXCLUDED.status,
    stopped_at = EXCLUDED.stopped_at,
    error = EXCLUDED.error,
    updated_at = EXCLUDED.updated_at;

INSERT INTO preview_snapshots (
  id, preview_instance_id, trigger, url_path, blob_ref, viewport_width,
  viewport_height, console_errors, file_changes, created_at
)
VALUES (
  '00000000-0000-4000-a000-000000000434'::uuid,
  '00000000-0000-4000-a000-000000000400'::uuid,
  'baseline',
  '/',
  'seeded/previews/00000000-0000-4000-a000-000000000400/baseline.png',
  1440,
  900,
  '[]'::jsonb,
  '[{"path":"internal/services/preview/recycler.go","status":"modified"}]'::jsonb,
  now() - interval '28 minutes'
)
ON CONFLICT (id) DO UPDATE
SET blob_ref = EXCLUDED.blob_ref,
    console_errors = EXCLUDED.console_errors,
    file_changes = EXCLUDED.file_changes;

INSERT INTO preview_logs (
  id, preview_instance_id, org_id, level, step, message, metadata, created_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000435'::uuid,
    '00000000-0000-4000-a000-000000000400'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'info',
    'ready',
    'Seeded preview marked ready for demo browsing.',
    '{"service":"frontend","duration_ms":4200}'::jsonb,
    now() - interval '29 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000436'::uuid,
    '00000000-0000-4000-a000-000000000401'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'error',
    'health_check',
    'Synthetic preview failed before the app became reachable.',
    '{"service":"frontend","attempt":3}'::jsonb,
    now() - interval '6 hours'
  )
ON CONFLICT (id) DO UPDATE
SET level = EXCLUDED.level,
    step = EXCLUDED.step,
    message = EXCLUDED.message,
    metadata = EXCLUDED.metadata;
