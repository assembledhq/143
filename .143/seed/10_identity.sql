INSERT INTO organizations (id, name, settings, created_at, updated_at)
VALUES (
  '00000000-0000-4000-a000-000000000001'::uuid,
  '143 Dogfood',
  '{}'::jsonb,
  now(),
  now()
)
ON CONFLICT (id) DO NOTHING;

INSERT INTO users (id, org_id, email, name, role, password_hash, created_at)
VALUES
  (
    '00000000-0000-4000-a000-000000000002'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'ada.lovelace@143.dev',
    'Ada Lovelace',
    'admin',
    -- bcrypt hash of "preview" (cost 10)
    '$2y$10$MtyCwm3KVYgmLvAinVwMHO3c65omeHXqqyIqwlz9JXJ30.5V2fyAe',
    now()
  ),
  (
    '00000000-0000-4000-a000-000000000003'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'grace.hopper@143.dev',
    'Grace Hopper',
    'member',
    -- bcrypt hash of "preview" (cost 10)
    '$2y$10$MtyCwm3KVYgmLvAinVwMHO3c65omeHXqqyIqwlz9JXJ30.5V2fyAe',
    now()
  ),
  (
    '00000000-0000-4000-a000-000000000004'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'alan.turing@143.dev',
    'Alan Turing',
    'builder',
    -- bcrypt hash of "preview" (cost 10)
    '$2y$10$MtyCwm3KVYgmLvAinVwMHO3c65omeHXqqyIqwlz9JXJ30.5V2fyAe',
    now()
  ),
  (
    '00000000-0000-4000-a000-000000000005'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'dennis.ritchie@143.dev',
    'Dennis Ritchie',
    'viewer',
    -- bcrypt hash of "preview" (cost 10)
    '$2y$10$MtyCwm3KVYgmLvAinVwMHO3c65omeHXqqyIqwlz9JXJ30.5V2fyAe',
    now()
  )
ON CONFLICT (id) DO UPDATE
SET org_id = EXCLUDED.org_id,
    email = EXCLUDED.email,
    name = EXCLUDED.name,
    role = EXCLUDED.role,
    password_hash = EXCLUDED.password_hash;

INSERT INTO organization_memberships (user_id, org_id, role, created_at)
VALUES
  (
    '00000000-0000-4000-a000-000000000002'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'admin',
    now()
  ),
  (
    '00000000-0000-4000-a000-000000000003'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'member',
    now()
  ),
  (
    '00000000-0000-4000-a000-000000000004'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'builder',
    now()
  ),
  (
    '00000000-0000-4000-a000-000000000005'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'viewer',
    now()
  )
ON CONFLICT (user_id, org_id) DO UPDATE
SET role = EXCLUDED.role;
