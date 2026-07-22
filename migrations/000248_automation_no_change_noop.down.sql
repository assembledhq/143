-- This migration only corrects historical status classification. Reverting
-- completed no-ops back to failed PR attempts would reintroduce the bad state,
-- so the data cleanup is intentionally irreversible.
SELECT 1;
