CREATE TABLE pm_documents (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    title           TEXT NOT NULL,
    content         TEXT NOT NULL DEFAULT '',
    doc_type        TEXT NOT NULL DEFAULT 'roadmap',
    sort_order      INT NOT NULL DEFAULT 0,

    -- Source provenance: where this document came from.
    -- source_type: 'manual' (pasted in UI), 'url' (fetched from link),
    --              'notion', 'google_docs', 'confluence', 'file_upload', etc.
    source_type     TEXT NOT NULL DEFAULT 'manual',
    -- source_url: the canonical URL for this document in the external system.
    source_url      TEXT,
    -- source_id: the external system's ID for this document (e.g. Notion page ID).
    source_id       TEXT,
    -- source_meta: arbitrary JSON for integration-specific metadata
    -- (e.g. Notion workspace, last sync cursor, Google Doc revision).
    source_meta     JSONB,
    -- last_synced_at: when the content was last pulled from the external source.
    last_synced_at  TIMESTAMPTZ,

    created_by      UUID REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_pm_documents_org ON pm_documents(org_id);
CREATE INDEX idx_pm_documents_source ON pm_documents(org_id, source_type, source_id) WHERE source_id IS NOT NULL;
