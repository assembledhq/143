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
  '00000000-0000-4000-a000-000000000302'::uuid,
  '00000000-0000-4000-a000-000000000303'::uuid,
  '00000000-0000-4000-a000-000000000304'::uuid
);
INSERT INTO session_messages (session_id, org_id, user_id, turn_number, role, content, thread_id, created_at)
VALUES
  (
    '00000000-0000-4000-a000-000000000300'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    1, 'user',
    'Please wire the preview recycler up to pull_request.closed so we stop paying for previews after a merge.',
    '00000000-0000-4000-a000-000000000700'::uuid,
    now() - interval '35 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000300'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    NULL,
    1, 'assistant',
    'Plan: inject preview manager into PRService, call StopPreview from the closed branch, mark pr_preview_state.status = ''closed''. Opened PR with a regression test.',
    '00000000-0000-4000-a000-000000000700'::uuid,
    now() - interval '34 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000300'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    2, 'user',
    'Looks good. Can you also make sure we do not blow up if the preview manager is not wired (self-hosted path)?',
    '00000000-0000-4000-a000-000000000700'::uuid,
    now() - interval '6 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000301'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    1, 'user',
    'Webhook deliveries are failing intermittently when GitHub returns a 502. Add a retry with backoff.',
    '00000000-0000-4000-a000-000000000701'::uuid,
    now() - interval '2 hours'
  ),
  (
    '00000000-0000-4000-a000-000000000301'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    NULL,
    1, 'assistant',
    'Added exponential backoff retry (3 attempts, 500ms/1s/2s) around the signature verification call. Tests cover 502, 503, and network timeouts.',
    '00000000-0000-4000-a000-000000000701'::uuid,
    now() - interval '1 hour' - interval '10 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000302'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    1, 'user',
    'Preview cold start is 90s+ on the dogfood env. Where is the time actually going?',
    '00000000-0000-4000-a000-000000000702'::uuid,
    now() - interval '3 days'
  ),
  (
    '00000000-0000-4000-a000-000000000303'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000003'::uuid,
    1, 'user',
    'Please keep archived sessions discoverable, but I am not sure whether they should show in the default list.',
    '00000000-0000-4000-a000-000000000703'::uuid,
    now() - interval '55 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000303'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    NULL,
    2, 'assistant',
    'I found two viable filter behaviors and paused for a product decision before changing the default.',
    '00000000-0000-4000-a000-000000000703'::uuid,
    now() - interval '18 minutes'
  ),
  (
    '00000000-0000-4000-a000-000000000304'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    '00000000-0000-4000-a000-000000000002'::uuid,
    1, 'user',
    'Normalize replay cursor ordering so retried webhook deliveries stay stable.',
    '00000000-0000-4000-a000-000000000704'::uuid,
    now() - interval '7 hours'
  ),
  (
    '00000000-0000-4000-a000-000000000304'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    NULL,
    2, 'assistant',
    'The synthetic replay-ordering test is still failing, so I stopped with a focused next-step summary.',
    '00000000-0000-4000-a000-000000000704'::uuid,
    now() - interval '6 hours'
  )
ON CONFLICT DO NOTHING;

-- A few log lines per session so the log stream UI has something to show.
-- DELETE first for the same idempotency reason as session_messages above.
DELETE FROM session_logs WHERE session_id IN (
  '00000000-0000-4000-a000-000000000300'::uuid,
  '00000000-0000-4000-a000-000000000301'::uuid,
  '00000000-0000-4000-a000-000000000302'::uuid,
  '00000000-0000-4000-a000-000000000303'::uuid,
  '00000000-0000-4000-a000-000000000304'::uuid
);
INSERT INTO session_logs (session_id, org_id, timestamp, level, message, turn_number, thread_id)
VALUES
  ('00000000-0000-4000-a000-000000000300'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, now() - interval '34 minutes', 'info', 'sandbox provisioned', 1, '00000000-0000-4000-a000-000000000700'::uuid),
  ('00000000-0000-4000-a000-000000000300'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, now() - interval '30 minutes', 'info', 'pushed branch feat/preview-teardown', 1, '00000000-0000-4000-a000-000000000700'::uuid),
  ('00000000-0000-4000-a000-000000000300'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, now() - interval '28 minutes', 'info', 'opened pull request #42', 1, '00000000-0000-4000-a000-000000000700'::uuid),
  ('00000000-0000-4000-a000-000000000301'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, now() - interval '1 hour' - interval '5 minutes', 'info', 'session completed successfully', 1, '00000000-0000-4000-a000-000000000701'::uuid),
  ('00000000-0000-4000-a000-000000000303'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, now() - interval '18 minutes', 'question', 'waiting for product decision on archive filter behavior', 2, '00000000-0000-4000-a000-000000000703'::uuid),
  ('00000000-0000-4000-a000-000000000304'::uuid, '00000000-0000-4000-a000-000000000001'::uuid, now() - interval '6 hours', 'error', 'synthetic replay-ordering regression remained failing', 2, '00000000-0000-4000-a000-000000000704'::uuid)
ON CONFLICT DO NOTHING;
