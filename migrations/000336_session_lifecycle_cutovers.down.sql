-- Retire future users and dependent guards before removing this representation.
-- No CASCADE: an unexpected dependency must fail visibly.
SET LOCAL lock_timeout = '5s';
DROP TABLE session_lifecycle_cutovers;
