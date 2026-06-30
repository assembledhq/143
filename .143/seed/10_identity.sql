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
    'preview-admin@143.dev',
    'Ada Lovelace',
    'admin',
    NULL,
    now()
  ),
  (
    '00000000-0000-4000-a000-000000000003'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'preview-member@143.dev',
    'Grace Hopper',
    'member',
    NULL,
    now()
  ),
  (
    '00000000-0000-4000-a000-000000000004'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'preview-builder@143.dev',
    'Alan Turing',
    'builder',
    NULL,
    now()
  ),
  (
    '00000000-0000-4000-a000-000000000005'::uuid,
    '00000000-0000-4000-a000-000000000001'::uuid,
    'preview-viewer@143.dev',
    'Dennis Ritchie',
    'viewer',
    NULL,
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
