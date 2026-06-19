package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		sql      string
		wantLen  int
		wantName string
	}{
		{
			name: "flags table missing org_id",
			sql: `CREATE TABLE widgets (
    id   uuid PRIMARY KEY,
    name text NOT NULL
);`,
			wantLen:  1,
			wantName: "widgets",
		},
		{
			name: "accepts table with inline org_id",
			sql: `CREATE TABLE widgets (
    id     uuid PRIMARY KEY,
    org_id uuid NOT NULL REFERENCES organizations(id),
    name   text NOT NULL
);`,
			wantLen: 0,
		},
		{
			name: "flags nullable org_id",
			sql: `CREATE TABLE widgets (
    id     uuid PRIMARY KEY,
    org_id uuid REFERENCES organizations(id),
    name   text NOT NULL
);`,
			wantLen:  1,
			wantName: "widgets",
		},
		{
			name: "flags org_id NOT NULL without FK or reviewed hot-table exception marker",
			sql: `CREATE TABLE hot_events (
    id     uuid PRIMARY KEY,
    org_id uuid NOT NULL,
    name   text NOT NULL
);`,
			wantLen:  1,
			wantName: "hot_events",
		},
		{
			name: "accepts org_id NOT NULL without FK when reviewed hot-table exception marker is present",
			sql: `CREATE TABLE hot_events (
    -- lint:allow-hot-table-no-fk reason="append-only runtime events; session ownership checked before insert"
    id     uuid PRIMARY KEY,
    org_id uuid NOT NULL,
    name   text NOT NULL
);`,
			wantLen: 0,
		},
		{
			name: "accepts allowlisted table",
			sql: `CREATE TABLE nodes (
    id   uuid PRIMARY KEY,
    host text NOT NULL
);`,
			wantLen: 0,
		},
		{
			name: "accepts inline escape hatch with reason",
			sql: `CREATE TABLE registry ( -- lint:no-org-id reason="global cache"
    key   text PRIMARY KEY,
    value jsonb
);`,
			wantLen: 0,
		},
		{
			name: "rejects bare lint:no-org-id without reason clause",
			sql: `CREATE TABLE registry ( -- lint:no-org-id
    key   text PRIMARY KEY,
    value jsonb
);`,
			wantLen:  1,
			wantName: "registry",
		},
		{
			name: "skips PARTITION OF children",
			sql: `CREATE TABLE logs_q1 PARTITION OF logs
    FOR VALUES FROM ('2025-01-01') TO ('2025-04-01');`,
			wantLen: 0,
		},
		{
			name: "skips CREATE TABLE AS SELECT",
			sql: `CREATE TABLE IF NOT EXISTS _backup AS
    SELECT * FROM widgets;`,
			wantLen: 0,
		},
		{
			name: "skips underscore-prefixed temp tables",
			sql: `CREATE TABLE _widgets_duplicates (
    id uuid PRIMARY KEY
);`,
			wantLen: 0,
		},
		{
			name: "matches org_id regardless of case",
			sql: `CREATE TABLE widgets (
    id     UUID PRIMARY KEY,
    ORG_ID UUID NOT NULL REFERENCES organizations(id)
);`,
			wantLen: 0,
		},
		{
			name: "does NOT accept a column named org_id_something",
			sql: `CREATE TABLE widgets (
    id            uuid PRIMARY KEY,
    org_id_legacy text
);`,
			wantLen:  1,
			wantName: "widgets",
		},
		{
			name: "flags schema-qualified table missing org_id",
			sql: `CREATE TABLE public.widgets (
    id   uuid PRIMARY KEY,
    name text NOT NULL
);`,
			wantLen:  1,
			wantName: "widgets",
		},
		{
			name: "accepts schema-qualified table with org_id",
			sql: `CREATE TABLE public.widgets (
    id     uuid PRIMARY KEY,
    org_id uuid NOT NULL REFERENCES organizations(id),
    name   text NOT NULL
);`,
			wantLen: 0,
		},
		{
			name: "normalizes schema-qualified name against allowlist",
			sql: `CREATE TABLE public.nodes (
    id   uuid PRIMARY KEY,
    host text NOT NULL
);`,
			wantLen: 0,
		},
		{
			name: "flags double-quoted table missing org_id",
			sql: `CREATE TABLE "widgets" (
    id   uuid PRIMARY KEY,
    name text NOT NULL
);`,
			wantLen:  1,
			wantName: "widgets",
		},
		{
			name: "flags schema-qualified and quoted name missing org_id",
			sql: `CREATE TABLE public."widgets" (
    id uuid PRIMARY KEY
);`,
			wantLen:  1,
			wantName: "widgets",
		},
		{
			name: "accepts in-body escape hatch on its own line",
			sql: `CREATE TABLE registry (
    -- lint:no-org-id reason="global cache"
    key   text PRIMARY KEY,
    value jsonb
);`,
			wantLen: 0,
		},
		{
			name: "accepts in-body escape hatch after a column",
			sql: `CREATE TABLE registry (
    key   text PRIMARY KEY,
    value jsonb -- lint:no-org-id reason="global cache"
);`,
			wantLen: 0,
		},
		{
			name: "still rejects in-body escape without reason clause",
			sql: `CREATE TABLE registry (
    -- lint:no-org-id
    key   text PRIMARY KEY,
    value jsonb
);`,
			wantLen:  1,
			wantName: "registry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := scan("test.sql", tt.sql)
			require.Len(t, got, tt.wantLen, "unexpected violation count")
			if tt.wantLen == 1 {
				require.Equal(t, tt.wantName, got[0].table, "violation table name mismatch")
			}
		})
	}
}
