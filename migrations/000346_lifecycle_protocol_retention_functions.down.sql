-- Removing protocol guards after activation is not a compatible rollback.
SET LOCAL lock_timeout = '5s';
LOCK TABLE public.sessions, public.session_lifecycle_cutovers IN ACCESS EXCLUSIVE MODE;
SELECT lifecycle143_assert_protocol_rollback_safe();
DROP FUNCTION lifecycle143_reject_active_protocol_truncate();
DROP FUNCTION lifecycle143_retain_cutover_protocol();
DROP FUNCTION lifecycle143_retain_session_protocol();
DROP FUNCTION lifecycle143_protocol_shared_interlock(uuid, uuid);
DROP FUNCTION lifecycle143_assert_protocol_rollback_safe();
