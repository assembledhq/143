-- Org-scoped identity key on users, so tenant-scoped tables can carry a
-- composite FOREIGN KEY (user_id, org_id) REFERENCES users(id, org_id) rather
-- than a bare users(id) reference that a cross-tenant write could satisfy.
-- Migration 000267 is the first consumer (session_publications.initiated_by_user_id).
--
-- Note on locking: unlike a CHECK or FOREIGN KEY constraint, a UNIQUE
-- constraint CANNOT be added NOT VALID — Postgres must build the backing
-- unique index up front. That build scans all of `users` while holding
-- ACCESS EXCLUSIVE, blocking reads and writes (i.e. every authenticated
-- request) for its duration. We accept the brief lock rather than
-- CREATE UNIQUE INDEX CONCURRENTLY + ADD CONSTRAINT ... USING INDEX because
-- our migrator wraps each file in a single transaction and CONCURRENTLY
-- cannot run inside one — the same tradeoff documented in migration 000134.
--
-- `users` is one row per human per org (thousands at SaaS scale, not
-- millions), so the build completes in milliseconds. It lives in its own
-- migration specifically so this lock is held for the index build alone and
-- is not extended across the ALTERs in 000267. The 5s lock_timeout is the
-- safety bound: if the table is far larger than expected, or another session
-- holds a conflicting lock, the migration fails fast instead of stalling
-- every login behind it.
SET LOCAL lock_timeout = '5s';

ALTER TABLE users
    ADD CONSTRAINT users_id_org_id_key UNIQUE (id, org_id);
