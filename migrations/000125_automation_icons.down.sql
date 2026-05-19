ALTER TABLE automations
    DROP CONSTRAINT IF EXISTS chk_automations_icon_value_length;

ALTER TABLE automations
    DROP CONSTRAINT IF EXISTS chk_automations_icon_type;

ALTER TABLE automations
    DROP COLUMN IF EXISTS icon_value,
    DROP COLUMN IF EXISTS icon_type;
