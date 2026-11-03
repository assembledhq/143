package main

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestInactiveBinaryRollbackPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, mutation, refusal string
		workerURL               bool
		source                  bool
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
		tc := tests[i]
		tc.name = "source revert " + tc.name
		tc.source = true
		if tc.name == "source revert different schema" {
			tc.mutation = "UPDATE schema_migrations SET version=394"
		}
		tc.refusal = strings.ReplaceAll(tc.refusal, "392", "393")
		if strings.Contains(tc.name, "dirty ledger") {
			tc.refusal = "dirty migration ledger"
		}
		if tc.name == "source revert different schema" {
			tc.refusal = "schema through 393 only"
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
			if tt.source {
				schema = 393
			}
			require.NoError(t, m.Migrate(schema), "install the historical inactive schema supported by binary rollback")
			f := seedCallerIntentMigrationLegacy(t, ctx, conn)
			seedCallerDispatchStatusMigrationMetadata(t, ctx, conn, f)
			if tt.mutation != "" {
				_, err := conn.Exec(ctx, tt.mutation)
				require.NoError(t, err, "construct the isolated refusal state")
			}
			snapshot := func() string {
				var value string
				err := conn.QueryRow(ctx, `SELECT jsonb_build_object('ledger',(SELECT jsonb_agg(to_jsonb(x)) FROM schema_migrations x),'control',(SELECT jsonb_agg(to_jsonb(x)) FROM caller_send_intent_control x),'sessions',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM sessions x),'metadata',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM code_review_session_metadata x))::text`).Scan(&value)
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
					err = verifyInactiveRuntimeSchema(ctx, rawURL, 393)
				}
			} else {
				err = verifyInactiveBinaryRollback(ctx, rawURL)
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
