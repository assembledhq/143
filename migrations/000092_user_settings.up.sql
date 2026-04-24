ALTER TABLE users
    ADD COLUMN settings jsonb NOT NULL DEFAULT '{}';
