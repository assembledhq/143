ALTER TABLE invitations
    DROP CONSTRAINT IF EXISTS invitations_acceptance_method_valid;

ALTER TABLE invitations
    DROP COLUMN IF EXISTS acceptance_method;
