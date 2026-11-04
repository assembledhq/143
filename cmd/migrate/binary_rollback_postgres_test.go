package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/crypto"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/services/packagecredentials"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestInactiveBinaryRollbackPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, mutation, refusal string
		workerURL               bool
		source                  bool
		schema                  uint
	}{
		{name: "inactive schema"},
		{name: "worker pool settings", workerURL: true},
		{name: "worker pool settings dirty ledger", workerURL: true, mutation: "UPDATE schema_migrations SET dirty=true", refusal: "expected clean migration 392"},
		{name: "worker pool settings enabled participation", workerURL: true, mutation: "UPDATE caller_send_intent_control SET intents_enabled=true", refusal: "caller participation"},
		{name: "dirty ledger", mutation: "UPDATE schema_migrations SET dirty=true", refusal: "expected clean migration 392"},
		{name: "different schema", mutation: "UPDATE schema_migrations SET version=393", refusal: "expected clean migration 392"},
		{name: "missing control", mutation: "DELETE FROM caller_send_intent_control", refusal: "caller participation"},
		{name: "enabled participation", mutation: "UPDATE caller_send_intent_control SET intents_enabled=true", refusal: "caller participation"},
		{name: "incomplete readiness", mutation: "UPDATE caller_send_intent_control SET schema_ready=false", refusal: "caller participation"},
		{name: "retained unsupported status", mutation: "UPDATE code_review_session_metadata SET status_code='code_reviewer_dispatch_unresolved'", refusal: "retained caller history"},
		{name: "retained lifecycle stamp", mutation: "ALTER TABLE sessions DISABLE TRIGGER USER; UPDATE sessions SET lifecycle_version=1", refusal: "lifecycle activation"},
	}
	original := len(tests)
	for i := 0; i < original; i++ {
		binary393 := tests[i]
		binary393.name = "binary schema393 " + binary393.name
		binary393.schema = 393
		binary393.refusal = strings.ReplaceAll(binary393.refusal, "392", "393")
		if binary393.name == "binary schema393 different schema" {
			binary393.mutation = "UPDATE schema_migrations SET version=392"
		}
		tests = append(tests, binary393)
		tc := tests[i]
		tc.name = "source revert " + tc.name
		tc.source = true
		if tc.name == "source revert different schema" {
			tc.mutation = "UPDATE schema_migrations SET version=397"
		}
		tc.refusal = strings.ReplaceAll(tc.refusal, "392", "393")
		if strings.Contains(tc.name, "dirty ledger") {
			tc.refusal = "dirty migration ledger"
		}
		if tc.name == "source revert different schema" {
			tc.refusal = "schema through 396 only"
		}
		tests = append(tests, tc)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, conn := newContentMigrationPostgres(t, false)
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			schema := uint(392)
			if tt.schema != 0 {
				schema = tt.schema
			}
			if tt.source {
				schema = inactiveRuntimeSchemaVersion
			}
			require.NoError(t, m.Migrate(schema), "install the historical inactive schema supported by binary rollback")
			f := seedCallerIntentMigrationLegacy(t, ctx, conn)
			seedCallerDispatchStatusMigrationMetadata(t, ctx, conn, f)
			if schema >= 393 {
				seedInactiveRollbackCredential(t, ctx, conn, f.org)
			}
			if tt.mutation != "" {
				_, err := conn.Exec(ctx, tt.mutation)
				require.NoError(t, err, "construct the isolated refusal state")
			}
			snapshot := func() string {
				var value string
				query := `SELECT jsonb_build_object('ledger',(SELECT jsonb_agg(to_jsonb(x)) FROM schema_migrations x),'control',(SELECT jsonb_agg(to_jsonb(x)) FROM caller_send_intent_control x),'sessions',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM sessions x),'metadata',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM code_review_session_metadata x))`
				if schema >= 393 {
					query += ` || jsonb_build_object('credentials',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM package_registry_credentials x))`
				}
				err := conn.QueryRow(ctx, "SELECT ("+query+")::text").Scan(&value)
				require.NoError(t, err, "capture exact ledger, control and business state")
				return value
			}
			before := snapshot()
			rawURL := conn.Config().ConnString()
			if tt.workerURL {
				parsed, err := url.Parse(rawURL)
				require.NoError(t, err, "parse fixture URL for worker pool setting")
				query := parsed.Query()
				query.Set("pool_max_conns", "4")
				parsed.RawQuery = query.Encode()
				rawURL = parsed.String()
			}
			var err error
			if tt.source {
				err = verifyRuntimeRevertBeforeUp(ctx, m, rawURL)
				if err == nil {
					err = verifyInactiveRuntimeSchema(ctx, rawURL, inactiveRuntimeSchemaVersion)
				}
			} else {
				err = verifyInactiveBinaryRollback(ctx, rawURL, schema)
			}
			if tt.refusal == "" {
				require.NoError(t, err, "clean inactive schema should permit binary-only rollback")
			} else {
				require.ErrorContains(t, err, tt.refusal, "unsafe rollback state must be refused")
			}
			require.Equal(t, before, snapshot(), "inspection must preserve exact ledger, activation and business data")
		})
	}
}

func seedInactiveRollbackCredential(t *testing.T, ctx context.Context, conn *pgx.Conn, org uuid.UUID) {
	t.Helper()
	integration, repository := uuid.New(), uuid.New()
	_, err := conn.Exec(ctx, `INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, integration, org)
	require.NoError(t, err, "seed the synthetic credential repository integration")
	_, err = conn.Exec(ctx, `INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id) VALUES($1,$2,$3,(hashtextextended($1::uuid::text,0) & 9223372036854775807),'synthetic/rollback','https://example.invalid/rollback.git',1)`, repository, org, integration)
	require.NoError(t, err, "seed the synthetic credential repository")
	encryption, err := crypto.NewService(strings.Repeat("synthetic-key", 4))
	require.NoError(t, err, "initialize synthetic envelope encryption")
	service := packagecredentials.NewService(db.NewPackageRegistryCredentialStore(conn), encryption)
	_, err = service.Create(ctx, org, repository, "https://registry.example.test", "@synthetic", "synthetic-registry-token")
	require.NoError(t, err, "create a real encrypted credential whose exact row must survive both successful and refused inspections")
}

func TestInactiveBinaryRollbackRejectsUnreviewedSchema(t *testing.T) {
	t.Parallel()
	for _, schema := range []uint{0, 391, 394, 395, 396} {
		t.Run(fmt.Sprint(schema), func(t *testing.T) {
			t.Parallel()
			err := verifyInactiveBinaryRollback(context.Background(), "invalid://must-not-connect", schema)
			require.EqualError(t, err, "inactive binary rollback supports only explicitly pinned schema 392 or 393", "unreviewed schema must be refused before connecting")
		})
	}
}
