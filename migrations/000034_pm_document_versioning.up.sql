-- 000034: PM Document Versioning + Document Set Pins + Input Manifest
--
-- Implements the insert-only versioning pattern (same as memories/review_patterns)
-- for PM documents. Adds document set pinning for eval reproducibility and
-- input_manifest on sessions for full input traceability.

-- 1. Add versioning columns to pm_documents.
ALTER TABLE pm_documents
    ADD COLUMN active       BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN logical_id   UUID NOT NULL DEFAULT gen_random_uuid(),
    ADD COLUMN content_hash TEXT NOT NULL DEFAULT '';

-- Backfill: each existing row becomes its own first version.
-- logical_id = id (the row is the first version of itself).
UPDATE pm_documents SET logical_id = id, content_hash = encode(digest(content::bytea, 'sha256'), 'hex');

-- Ensure only one active version per logical document within an org.
CREATE UNIQUE INDEX idx_pm_documents_active_logical
    ON pm_documents (org_id, logical_id) WHERE active = true;

-- 2. Document set pins: a lightweight snapshot of which document versions
--    were active at a point in time. Referenced by pm_plans and eval_tasks.
CREATE TABLE pm_document_set_pins (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE pm_document_set_pin_members (
    pin_id          UUID NOT NULL REFERENCES pm_document_set_pins(id) ON DELETE CASCADE,
    document_id     UUID NOT NULL REFERENCES pm_documents(id) ON DELETE RESTRICT,
    PRIMARY KEY (pin_id, document_id)
);

CREATE INDEX idx_pm_document_set_pins_org ON pm_document_set_pins(org_id);

-- 3. Input manifest on sessions for full reproducibility.
ALTER TABLE sessions ADD COLUMN input_manifest JSONB;
