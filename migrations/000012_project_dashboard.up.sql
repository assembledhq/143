-- Project attachments: screenshots, design files, mockups linked to a project.
CREATE TABLE project_attachments (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    org_id          UUID NOT NULL REFERENCES organizations(id),

    -- File metadata
    file_name       TEXT NOT NULL,
    file_url        TEXT NOT NULL,
    file_type       TEXT NOT NULL DEFAULT 'image',  -- image, design, document
    thumbnail_url   TEXT,
    file_size       INT,

    -- Categorization
    category        TEXT NOT NULL DEFAULT 'screenshot',  -- screenshot, mockup, wireframe, reference
    caption         TEXT,
    sort_order      INT NOT NULL DEFAULT 0,

    uploaded_by     UUID REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_project_attachments_project ON project_attachments(project_id, sort_order);

-- Project specs: product requirement documents in markdown.
CREATE TABLE project_specs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    org_id          UUID NOT NULL REFERENCES organizations(id),

    title           TEXT NOT NULL,
    content         TEXT NOT NULL DEFAULT '',
    spec_type       TEXT NOT NULL DEFAULT 'prd',  -- prd, technical, design, user_story
    sort_order      INT NOT NULL DEFAULT 0,
    version         INT NOT NULL DEFAULT 1,

    created_by      UUID REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_project_specs_project ON project_specs(project_id, sort_order);
