ALTER TABLE automations
    ADD COLUMN icon_type TEXT NOT NULL DEFAULT 'emoji',
    ADD COLUMN icon_value TEXT NOT NULL DEFAULT '⚙️';

ALTER TABLE automations
    ADD CONSTRAINT chk_automations_icon_type CHECK (icon_type IN ('emoji'));

ALTER TABLE automations
    ADD CONSTRAINT chk_automations_icon_value_length CHECK (char_length(icon_value) BETWEEN 1 AND 16);
