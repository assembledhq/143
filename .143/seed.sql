-- Seed data for preview dogfooding.
-- Creates a default organization and four users so the preview is
-- immediately usable without requiring the registration flow, plus a
-- placeholder integration and a couple of repositories/projects so the
-- logged-in UI shows populated screens instead of empty states.
--
-- IMPORTANT: the seeded admin email + password below must match the
-- DEMO_EMAIL / DEMO_PASSWORD defaults in internal/config/config.go, since
-- the login-page banner renders those values and a reviewer copy-pastes
-- them into the sign-in form. If you change either side, regenerate the
-- bcrypt hash below (cost 10) and update the config defaults in lockstep.
--
-- Password for all preview users: "preview" (bcrypt hash below).
--
-- All rows use fixed UUIDs and conflict handlers so the seed is safely
-- re-runnable. Tables with secondary unique indexes (e.g.
-- repositories.idx_repositories_org_github) use the unqualified
-- ON CONFLICT DO NOTHING form so any unique violation — not just on id —
-- no-ops rather than aborting the transaction; identity rows use DO UPDATE
-- so old dogfood credentials converge to the current preview credentials.

INSERT INTO organizations (id, name, settings, created_at, updated_at)
VALUES (
  '00000000-0000-4000-a000-000000000001'::uuid,
  '143 Dogfood',
  '{}'::jsonb,
  now(),
  now()
)
ON CONFLICT (id) DO NOTHING;

INSERT INTO users (id, org_id, email, name, role, password_hash, created_at)
VALUES
  (
    '00000000-0000-4000-a000-000000000002'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'preview-admin@143.dev',
    'Preview Admin',
    'admin',
    -- bcrypt hash of "preview" (cost 10)
    '$2y$10$MtyCwm3KVYgmLvAinVwMHO3c65omeHXqqyIqwlz9JXJ30.5V2fyAe',
    now()
  ),
  (
    '00000000-0000-4000-a000-000000000003'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'preview-member@143.dev',
    'Preview Member',
    'member',
    -- bcrypt hash of "preview" (cost 10)
    '$2y$10$MtyCwm3KVYgmLvAinVwMHO3c65omeHXqqyIqwlz9JXJ30.5V2fyAe',
    now()
  ),
  (
    '00000000-0000-4000-a000-000000000004'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'preview-builder@143.dev',
    'Preview Builder',
    'builder',
    -- bcrypt hash of "preview" (cost 10)
    '$2y$10$MtyCwm3KVYgmLvAinVwMHO3c65omeHXqqyIqwlz9JXJ30.5V2fyAe',
    now()
  ),
  (
    '00000000-0000-4000-a000-000000000005'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'preview-viewer@143.dev',
    'Preview Viewer',
    'viewer',
    -- bcrypt hash of "preview" (cost 10)
    '$2y$10$MtyCwm3KVYgmLvAinVwMHO3c65omeHXqqyIqwlz9JXJ30.5V2fyAe',
    now()
  )
ON CONFLICT (id) DO UPDATE
SET org_id = EXCLUDED.org_id,
    email = EXCLUDED.email,
    name = EXCLUDED.name,
    role = EXCLUDED.role,
    password_hash = EXCLUDED.password_hash;

INSERT INTO organization_memberships (user_id, org_id, role, created_at)
VALUES
  (
    '00000000-0000-4000-a000-000000000002'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'admin',
    now()
  ),
  (
    '00000000-0000-4000-a000-000000000003'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'member',
    now()
  ),
  (
    '00000000-0000-4000-a000-000000000004'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'builder',
    now()
  ),
  (
    '00000000-0000-4000-a000-000000000005'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'viewer',
    now()
  )
ON CONFLICT (user_id, org_id) DO UPDATE
SET role = EXCLUDED.role;

-- Placeholder GitHub integration so repositories have a valid FK target.
-- The preview does not actually talk to GitHub, so the config is empty.
INSERT INTO integrations (id, org_id, provider, config, status, created_at)
VALUES (
  '00000000-0000-4000-a000-000000000010'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  'github',
  '{}'::jsonb,
  'active',
  now()
)
ON CONFLICT (id) DO NOTHING;

-- The github_id, installation_id, and clone_url values are placeholders.
-- The dogfood preview has no GitHub App configured and will not actually
-- call the GitHub API, so any code path that tries to hit GitHub from
-- this seeded data will fail — that is expected. If you need real GitHub
-- integration in the preview, register a throwaway App and replace these.
INSERT INTO repositories (
  id, org_id, integration_id, github_id, full_name, default_branch,
  private, language, description, clone_url, installation_id, status,
  settings, created_at, updated_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000100'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000010'::uuid,
    1001,
    'assembledhq/143',
    'main',
    true,
    'Go',
    'The 143 agent platform itself (dogfood).',
    'https://github.com/assembledhq/143.git',
    99999,
    'active',
    '{}'::jsonb,
    now(),
    now()
  ),
  (
    '00000000-0000-4000-a000-000000000101'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000010'::uuid,
    1002,
    'assembledhq/example-service',
    'main',
    true,
    'TypeScript',
    'Example service used for walkthroughs in the dogfood preview.',
    'https://github.com/assembledhq/example-service.git',
    99999,
    'active',
    '{}'::jsonb,
    now(),
    now()
  )
ON CONFLICT DO NOTHING;

INSERT INTO projects (
  id, org_id, repository_id, title, goal, scope, status, priority,
  execution_mode, max_concurrent, auto_merge, base_branch, created_by,
  created_at, updated_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000200'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000100'::uuid,
    'Ship preview environments',
    'Add preview deploys so every session shows a live app.',
    'Preview provider, gateway routing, and UI integration.',
    'active',
    50,
    'sequential',
    2,
    false,
    'main',
    '00000000-0000-4000-a000-000000000002'::uuid,
    now(),
    now()
  ),
  (
    '00000000-0000-4000-a000-000000000201'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000101'::uuid,
    'Webhook ingestion',
    'Wire example-service webhooks through the ingestion pipeline.',
    'Signature verification, idempotent delivery, replay.',
    'completed',
    50,
    'sequential',
    2,
    false,
    'main',
    '00000000-0000-4000-a000-000000000002'::uuid,
    now(),
    now()
  )
ON CONFLICT (id) DO NOTHING;

-- =============================================================================
-- Illusory session + preview rows.
--
-- The dogfood preview runs as OS processes inside a sandbox container and has
-- no access to a Docker socket, so it cannot actually spawn sessions or
-- previews. These rows exist so that the sessions list, session detail, and
-- preview panels render populated state for a reviewer clicking around.
--
-- preview_instances.worker_node_id is set to the sentinel 'seeded' so the
-- real RecycleWorker (which scans WHERE worker_node_id = <this worker>) never
-- touches these rows. expires_at is far in the future for the same reason.
--
-- Idempotency: rows with fixed PKs use ON CONFLICT (id) DO NOTHING. Rows in
-- tables where we don't own an id (session_messages, session_logs) have no
-- unique constraint on the seeded columns, so ON CONFLICT alone cannot
-- deduplicate them. To stay idempotent across repeated seed runs (e.g. a
-- sandbox restart against a persistent Postgres volume) we DELETE the seed
-- rows by their fixed session_ids before re-INSERTing. The session_ids
-- 00000000-0000-4000-a000-00000000030x are owned by this seed and cannot
-- collide with real sessions, which use gen_random_uuid().
-- =============================================================================

-- Three sessions: one "active PR", one "completed", one "idle" -- spread
-- across the two seeded repos and ordered by last_activity_at DESC so the
-- sessions list has a natural MRU shape.
INSERT INTO sessions (
  id, org_id, repository_id, triggered_by_user_id, title, working_branch,
  target_branch, agent_type, status, autonomy_level, token_mode,
  sandbox_state, current_turn, last_activity_at, started_at, completed_at,
  created_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000300'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000100'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    'Ship PR preview auto-teardown',
    'feat/preview-teardown',
    'main',
    'codex',
    'pr_created',
    'semi',
    'low',
    'snapshotted',
    4,
    now() - interval '2 minutes',
    now() - interval '35 minutes',
    now() - interval '3 minutes',
    now() - interval '35 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000301'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000101'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    'Retry webhook signature on transient GitHub 5xx',
    'fix/webhook-retry',
    'main',
    'codex',
    'completed',
    'semi',
    'low',
    'snapshotted',
    3,
    now() - interval '1 hour',
    now() - interval '2 hours',
    now() - interval '1 hour' - interval '5 minutes',
    now() - interval '2 hours'
  ),
  (
    '00000000-0000-4000-a000-000000000302'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000100'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    'Investigate preview cold-start latency',
    NULL,
    'main',
    'codex',
    'idle',
    'semi',
    'low',
    'none',
    1,
    now() - interval '3 days',
    now() - interval '3 days' - interval '10 minutes',
    NULL,
    now() - interval '3 days' - interval '10 minutes'
  )
ON CONFLICT (id) DO NOTHING;

-- Populate a small safe demo diff for the PR-created session so the Changes
-- view renders a real review surface in screenshot/demo mode.
UPDATE sessions
SET
  diff = $diff$diff --git a/internal/services/preview/recycler.go b/internal/services/preview/recycler.go
index 4b825dc..a15f4be 100644
--- a/internal/services/preview/recycler.go
+++ b/internal/services/preview/recycler.go
@@ -42,6 +42,17 @@ func (s *Service) HandlePullRequestClosed(ctx context.Context, event PullRequest
  if event.Repository == "" || event.Number == 0 {
    return nil
  }
+
+	preview, err := s.previewStore.GetByPullRequest(ctx, event.OrgID, event.Repository, event.Number)
+	if errors.Is(err, db.ErrNotFound) {
+		return nil
+	}
+	if err != nil {
+		return fmt.Errorf("lookup pr preview: %w", err)
+	}
+	if preview.Status == models.PreviewStatusReady {
+		return s.previewManager.StopPreview(ctx, event.OrgID, preview.ID)
+	}
  return nil
 }

diff --git a/internal/services/preview/recycler_test.go b/internal/services/preview/recycler_test.go
index 02f3a91..fb49d28 100644
--- a/internal/services/preview/recycler_test.go
+++ b/internal/services/preview/recycler_test.go
@@ -18,6 +18,24 @@ func TestHandlePullRequestClosed(t *testing.T) {
  t.Parallel()

  tests := []struct {
+		name          string
+		previewStatus models.PreviewStatus
+		expectStop    bool
+	}{
+		{name: "stops ready preview", previewStatus: models.PreviewStatusReady, expectStop: true},
+		{name: "ignores closed preview", previewStatus: models.PreviewStatusStopped, expectStop: false},
+	}
+
+	for _, tt := range tests {
+		t.Run(tt.name, func(t *testing.T) {
+			t.Parallel()
+			// preview manager expectations omitted for brevity
+		})
+	}
+
+	legacyCases := []struct {
    name string
  }{
$diff$,
  diff_stats = '{"files_changed":2,"added":30,"removed":2}'::jsonb,
  diff_history = '[{"pass":1,"diff_stats":{"files_changed":2,"added":30,"removed":2},"summary":"Stopped ready PR previews when the pull request closes.","created_at":"2026-05-26T20:00:00Z"}]'::jsonb,
  diff_collected_at = now() - interval '4 minutes'
WHERE id = '00000000-0000-4000-a000-000000000300'::uuid
  AND org_id = '00000000-0000-4000-a000-000000000001'::uuid;

-- A few chat messages per session so the detail pages render a conversation.
-- DELETE first to keep reseeds idempotent (see note at top of the illusory
-- section — session_messages has no unique constraint on the seeded cols).
DELETE FROM session_messages WHERE session_id IN (
  '00000000-0000-4000-a000-000000000300'::uuid,
  '00000000-0000-4000-a000-000000000301'::uuid,
  '00000000-0000-4000-a000-000000000302'::uuid
);
INSERT INTO session_messages (session_id, org_id, user_id, turn_number, role, content, created_at)
VALUES
  (
    '00000000-0000-4000-a000-000000000300'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    1, 'user',
    'Please wire the preview recycler up to pull_request.closed so we stop paying for previews after a merge.',
    now() - interval '35 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000300'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    NULL,
    1, 'assistant',
    'Plan: inject preview manager into PRService, call StopPreview from the closed branch, mark pr_preview_state.status = ''closed''. Opened PR with a regression test.',
    now() - interval '34 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000300'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    2, 'user',
    'Looks good. Can you also make sure we do not blow up if the preview manager is not wired (self-hosted path)?',
    now() - interval '6 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000301'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    1, 'user',
    'Webhook deliveries are failing intermittently when GitHub returns a 502. Add a retry with backoff.',
    now() - interval '2 hours'
  ),
  (
    '00000000-0000-4000-a000-000000000301'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    NULL,
    1, 'assistant',
    'Added exponential backoff retry (3 attempts, 500ms/1s/2s) around the signature verification call. Tests cover 502, 503, and network timeouts.',
    now() - interval '1 hour' - interval '10 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000302'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    1, 'user',
    'Preview cold start is 90s+ on the dogfood env. Where is the time actually going?',
    now() - interval '3 days'
  )
ON CONFLICT DO NOTHING;

-- A few log lines per session so the log stream UI has something to show.
-- DELETE first for the same idempotency reason as session_messages above.
DELETE FROM session_logs WHERE session_id IN (
  '00000000-0000-4000-a000-000000000300'::uuid,
  '00000000-0000-4000-a000-000000000301'::uuid,
  '00000000-0000-4000-a000-000000000302'::uuid
);
INSERT INTO session_logs (session_id, org_id, timestamp, level, message, turn_number)
VALUES
  ('00000000-0000-4000-a000-000000000300'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, now() - interval '34 minutes', 'info', 'sandbox provisioned', 1),
  ('00000000-0000-4000-a000-000000000300'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, now() - interval '30 minutes', 'info', 'pushed branch feat/preview-teardown', 1),
  ('00000000-0000-4000-a000-000000000300'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, now() - interval '28 minutes', 'info', 'opened pull request #42', 1),
  ('00000000-0000-4000-a000-000000000301'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, now() - interval '1 hour' - interval '5 minutes', 'info', 'session completed successfully', 1)
ON CONFLICT DO NOTHING;

-- A seeded "ready" preview instance pointing at session 300.
-- worker_node_id = 'seeded' keeps the real recycler from touching this row.
INSERT INTO preview_instances (
  id, session_id, org_id, user_id, profile_name, name, status, provider,
  worker_node_id, preview_handle, primary_service, port, expires_at, created_at, updated_at
)
VALUES (
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
  now() + interval '24 hours',
  now() - interval '30 minutes',
  now() - interval '2 minutes'
)
ON CONFLICT (id) DO NOTHING;

INSERT INTO preview_services (
  id, preview_instance_id, service_name, role, status, port, created_at
)
VALUES
  (
    '00000000-0000-4000-a000-000000000410'::uuid,
    '00000000-0000-4000-a000-000000000400'::uuid,
    'frontend', 'primary', 'ready', 3000,
    now() - interval '30 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000411'::uuid,
    '00000000-0000-4000-a000-000000000400'::uuid,
    'server', 'support', 'ready', 8080,
    now() - interval '30 minutes'
  )
ON CONFLICT (id) DO NOTHING;

INSERT INTO preview_infrastructure (
  id, preview_instance_id, infra_name, template, status, created_at
)
VALUES (
  '00000000-0000-4000-a000-000000000420'::uuid,
  '00000000-0000-4000-a000-000000000400'::uuid,
  'db', 'postgres-17', 'healthy',
  now() - interval '30 minutes'
)
ON CONFLICT (id) DO NOTHING;

-- Pull request row backing the pr_preview_state below. Any UI that joins
-- pr_preview_state to pull_requests (by org_id + github_repo + pr_number)
-- needs a pull_requests row to render a working link — without this, the
-- PR preview panel renders a broken "PR #42" link in the dogfood.
-- Note: github_pr_url points at a real PR on the public repo so the link
-- resolves; nothing in the dogfood actually calls the GitHub API.
INSERT INTO pull_requests (
  id, session_id, org_id, github_pr_number, github_pr_url, github_repo,
  title, body, status, review_status, authored_by, created_at, updated_at
)
VALUES (
  '00000000-0000-4000-a000-000000000501'::uuid,
  '00000000-0000-4000-a000-000000000300'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  42,
  'https://github.com/assembledhq/143/pull/42',
  'assembledhq/143',
  'Ship PR preview auto-teardown',
  'Wire preview teardown into pull_request.closed / merged.',
  'open',
  'pending',
  'app',
  now() - interval '30 minutes',
  now() - interval '2 minutes'
)
ON CONFLICT (id) DO NOTHING;

-- PR-preview tracking for the "pr_created" session, backed by the seeded
-- pull_requests row above.
INSERT INTO pr_preview_state (
  id, org_id, repo_id, pr_number, last_preview_instance_id, status, created_at, updated_at
)
VALUES (
  '00000000-0000-4000-a000-000000000500'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  '00000000-0000-4000-a000-000000000100'::uuid,
  42,
  '00000000-0000-4000-a000-000000000400'::uuid,
  'running',
  now() - interval '30 minutes',
  now() - interval '2 minutes'
)
ON CONFLICT DO NOTHING;
