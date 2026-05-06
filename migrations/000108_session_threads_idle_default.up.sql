ALTER TABLE session_threads ALTER COLUMN status SET DEFAULT 'idle';

-- Promote any threads created under the old default that never started running
-- (current_turn = 0, no agent_session_id) to the new 'idle' state. Threads that
-- already advanced are left alone so we do not stomp on real runtime status.
UPDATE session_threads
SET status = 'idle'
WHERE status = 'pending'
  AND current_turn = 0
  AND agent_session_id IS NULL;
