-- Roll back applications observing this table before removing it. Expiry and
-- application rollback are not proof that lifecycle writers have drained.
SET LOCAL lock_timeout = '5s';

DROP TABLE lifecycle_writer_instances;
