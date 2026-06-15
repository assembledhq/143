ALTER TABLE users ADD COLUMN secondary_emails text[];

UPDATE users
SET secondary_emails = '{}'
WHERE secondary_emails IS NULL;

ALTER TABLE users ALTER COLUMN secondary_emails SET DEFAULT '{}';
ALTER TABLE users ALTER COLUMN secondary_emails SET NOT NULL;
