-- Irreversible data repair. The up migration recovers delivery state that was
-- already lost: it cannot distinguish an entry it repaired from one that
-- reached the same state through the normal runtime path, so reverting would
-- corrupt correctly-delivered entries rather than restore anything. Rolling
-- back migration 000264 drops applied_at outright, which is the only complete
-- revert available.
SELECT 1;
