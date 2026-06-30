-- High-impact product surface data for the dogfood preview. Everything here
-- is synthetic and safe to publish, but the shapes mirror real records closely
-- enough for project, automation, autopilot, code review, usage, and provider
-- integration pages to render non-empty workflows.

UPDATE organizations
SET settings = COALESCE(settings, '{}'::jsonb) || '{
  "default_work_repository_id": "00000000-0000-4000-a000-000000000100",
  "product_context": {
    "product": "143",
    "audience": "engineering teams delegating code work to agents",
    "principles": [
      "show agent work with evidence",
      "keep tenant data isolated",
      "make preview and PR readiness obvious"
    ],
    "current_focus": [
      "preview reliability",
      "provider-triggered automations",
      "review quality gates"
    ]
  }
}'::jsonb,
    updated_at = now()
WHERE id = '00000000-0000-4000-a000-000000000001'::uuid;

UPDATE repositories
SET settings = COALESCE(settings, '{}'::jsonb) || '{
  "preview_config": {
    "detected": true,
    "entrypoint": "make preview",
    "health_path": "/healthz",
    "ports": [3000, 8080],
    "last_detected_at": "seeded"
  },
  "branching": {
    "protected_branches": ["main"],
    "agent_branch_prefix": "codex/"
  }
}'::jsonb,
    updated_at = now()
WHERE id = '00000000-0000-4000-a000-000000000100'::uuid;

UPDATE repositories
SET settings = COALESCE(settings, '{}'::jsonb) || '{
  "preview_config": {
    "detected": true,
    "entrypoint": "npm run dev",
    "health_path": "/ready",
    "ports": [5173],
    "last_detected_at": "seeded"
  },
  "branching": {
    "protected_branches": ["main"],
    "agent_branch_prefix": "codex/"
  }
}'::jsonb,
    updated_at = now()
WHERE id = '00000000-0000-4000-a000-000000000101'::uuid;

