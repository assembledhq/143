package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestInactiveBinaryRollbackPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, mutation, refusal string }{
		{name: "inactive schema"},
		{name: "dirty ledger", mutation: "UPDATE schema_migrations SET dirty=true", refusal: "expected clean migration 392"},
		{name: "different schema", mutation: "UPDATE schema_migrations SET version=393", refusal: "expected clean migration 392"},
		{name: "missing control", mutation: "DELETE FROM caller_send_intent_control", refusal: "caller participation"},
		{name: "enabled participation", mutation: "UPDATE caller_send_intent_control SET intents_enabled=true", refusal: "caller participation"},
		{name: "incomplete readiness", mutation: "UPDATE caller_send_intent_control SET schema_ready=false", refusal: "caller participation"},
		{name: "retained unsupported status", mutation: "UPDATE code_review_session_metadata SET status_code='code_reviewer_dispatch_unresolved'", refusal: "retained caller history"},
		{name: "retained lifecycle stamp", mutation: "ALTER TABLE sessions DISABLE TRIGGER USER; UPDATE sessions SET lifecycle_version=1", refusal: "lifecycle activation"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, conn := newContentMigrationPostgres(t, false)
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			require.NoError(t, m.Up(), "install the complete schema before rollback inspection")
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
			err := verifyInactiveBinaryRollback(ctx, conn.Config().ConnString())
			if tt.refusal == "" {
				require.NoError(t, err, "clean inactive schema should permit binary-only rollback")
			} else {
				require.ErrorContains(t, err, tt.refusal, "unsafe rollback state must be refused")
			}
			require.Equal(t, before, snapshot(), "inspection must preserve exact ledger, activation and business data")
		})
	}
}
