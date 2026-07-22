-- Persist the first observation of a bounded retry condition so the retry
-- window survives worker restarts and is not measured from job creation.
ALTER TABLE jobs ADD COLUMN retry_window_started_at timestamptz;
