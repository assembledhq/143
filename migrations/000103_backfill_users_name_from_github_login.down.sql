-- No-op. The up migration heals empty users.name values by copying
-- github_login into name; reverting would require knowing which rows
-- were originally empty, which we don't track. Leaving names populated
-- is the safer state to leave behind.
SELECT 1;
