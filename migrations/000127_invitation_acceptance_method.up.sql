ALTER TABLE invitations
    ADD COLUMN acceptance_method text NOT NULL DEFAULT 'either';

ALTER TABLE invitations
    ADD CONSTRAINT invitations_acceptance_method_valid
    CHECK (acceptance_method IN ('either', 'email', 'github'));
