-- Serialize the fresh preflight and guard removal with both evidence writers.
SET LOCAL lock_timeout = '5s';
LOCK TABLE public.sessions, public.session_lifecycle_cutovers IN ACCESS EXCLUSIVE MODE;
SELECT lifecycle143_assert_protocol_rollback_safe();
DROP TRIGGER trg_sessions_retain_lifecycle_protocol_truncate ON sessions;
DROP TRIGGER trg_sessions_retain_lifecycle_protocol ON sessions;
