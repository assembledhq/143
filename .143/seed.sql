-- Seed data for preview dogfooding.
-- Creates a default organization and admin user so the preview is
-- immediately usable without requiring the registration flow.
--
-- Password: "preview-dogfood" (bcrypt hash below).

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
VALUES (
  '00000000-0000-4000-a000-000000000002'::uuid,
  '00000000-0000-4000-a000-000000000001'::uuid,
  'dogfood@143.dev',
  'Preview Admin',
  'admin',
  -- bcrypt hash of "preview-dogfood" (cost 10)
  '$2a$10$yq0Z0nFAzgJa1IuC.zbMh.RmEdX2dAJk8XQwELbmOA1AztcbCUyVi',
  now()
)
ON CONFLICT (email) DO NOTHING;
