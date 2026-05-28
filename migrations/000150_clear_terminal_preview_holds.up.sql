UPDATE preview_instances
SET preview_holding_container = FALSE,
    updated_at = now()
WHERE preview_holding_container = TRUE
  AND status IN ('stopped', 'failed', 'expired', 'unavailable');
