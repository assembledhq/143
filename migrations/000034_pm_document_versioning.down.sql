-- Reverse 000034: PM Document Versioning

ALTER TABLE sessions DROP COLUMN IF EXISTS input_manifest;

DROP TABLE IF EXISTS pm_document_set_pin_members;
DROP TABLE IF EXISTS pm_document_set_pins;

DROP INDEX IF EXISTS idx_pm_documents_active_logical;

ALTER TABLE pm_documents
    DROP COLUMN IF EXISTS content_hash,
    DROP COLUMN IF EXISTS logical_id,
    DROP COLUMN IF EXISTS active;
