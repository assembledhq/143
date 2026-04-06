-- Reverse 000035: Preview System Tables
-- Drop in reverse dependency order.

DROP TABLE IF EXISTS pr_preview_state CASCADE;
DROP TABLE IF EXISTS preview_startup_cache CASCADE;
DROP TABLE IF EXISTS preview_access_sessions CASCADE;
DROP TABLE IF EXISTS preview_logs CASCADE;
DROP TABLE IF EXISTS preview_snapshots CASCADE;
DROP TABLE IF EXISTS preview_infrastructure CASCADE;
DROP TABLE IF EXISTS preview_services CASCADE;
DROP TABLE IF EXISTS preview_instances CASCADE;
