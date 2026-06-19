-- Store a retrievable encrypted copy of newly created CLI org join tokens so
-- admins can copy an active install link again. The hash remains the lookup
-- and validation key; this encrypted value is only for authenticated admin
-- display.
ALTER TABLE org_join_tokens
    ADD COLUMN raw_token_encrypted BYTEA;
