-- Export payloads freeze retained evidence, including mutable legacy records.
CREATE TABLE code_review_exports (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL REFERENCES organizations(id),
    repository_id uuid NOT NULL,
    slot smallint NOT NULL CHECK (slot BETWEEN 1 AND 3),
    schema_version integer NOT NULL CHECK (schema_version = 1),
    filters jsonb NOT NULL,
    acquired_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    counts jsonb NOT NULL,
    UNIQUE (org_id, id),
    UNIQUE (org_id, slot),
    FOREIGN KEY (org_id, repository_id) REFERENCES repositories(org_id, id)
);
CREATE INDEX code_review_exports_expiry ON code_review_exports(org_id, expires_at);
CREATE TABLE code_review_export_rows (
    org_id uuid NOT NULL REFERENCES organizations(id),
    export_id uuid NOT NULL,
    position bigint NOT NULL CHECK (position > 0),
    payload jsonb NOT NULL,
    PRIMARY KEY (org_id, export_id, position),
    FOREIGN KEY (org_id, export_id) REFERENCES code_review_exports(org_id, id) ON DELETE CASCADE
);
