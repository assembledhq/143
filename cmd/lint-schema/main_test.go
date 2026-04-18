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
    ORG_ID UUID NOT NULL
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
