-- Remove the auto-seeded primary thread rows. We can identify them by the
-- well-known label and the fact that they are the only thread on the
-- session — leaving any sessions where the user has since added more tabs
-- alone, since deleting "Main" out from under them would orphan transcript
-- and file-event rows that point at this thread_id.
--
-- Caveat: the Add-Tab dialog lets users override the auto-generated label,
-- so a user-created tab labeled "Main" on a single-tab session would be
-- swept up by this DELETE. This is a deliberate trade-off for migration
-- simplicity — this down path is only meant for a clean, rapid revert. If
-- the up has been live long enough for users to start manually labeling
-- tabs "Main", coordinate the rollback manually rather than running this.
DELETE FROM session_threads t
WHERE t.label = 'Main'
  AND (SELECT count(*) FROM session_threads x WHERE x.session_id = t.session_id) = 1;
