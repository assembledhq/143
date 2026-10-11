package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestAuditRetentionHandlerPolicyAndFailures(t *testing.T) {
	t.Parallel()
	failure := errors.New("database unavailable")
	tests := []struct {
		name          string
		settings      string
		payload       string
		contextOrg    bool
		failedStage   string
		expectedDays  int
		expectedError string
	}{
		{name: "configured tenant retention", settings: `{"audit_retention_days":14}`, expectedDays: 14},
		{name: "default retention", settings: `{}`, expectedDays: models.DefaultAuditRetentionDays},
		{name: "negative retention uses default", settings: `{"audit_retention_days":-1}`, expectedDays: models.DefaultAuditRetentionDays},
		{name: "malformed settings use default", settings: `{`, expectedDays: models.DefaultAuditRetentionDays},
		{name: "job context supplies tenant", settings: `{}`, contextOrg: true, expectedDays: models.DefaultAuditRetentionDays},
		{name: "invalid JSON stops before reads", payload: `{`, expectedError: "unmarshal audit_retention_cleanup payload"},
		{name: "invalid tenant stops before reads", payload: `{"org_id":"invalid"}`, expectedError: "parse org ID"},
		{name: "missing tenant stops before reads", payload: `{}`, expectedError: "missing org ID"},
		{name: "organization read failure remains retryable", failedStage: "organization", expectedError: "fetch organization"},
		{name: "cleanup failure remains retryable", settings: `{}`, failedStage: "cleanup", expectedDays: models.DefaultAuditRetentionDays, expectedError: "delete expired audit logs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "retention database fixture should initialize")
			defer mock.Close()
			orgID := uuid.New()
			ctx := context.Background()
			payload := json.RawMessage(`{"org_id":"` + orgID.String() + `"}`)
			if tt.contextOrg {
				ctx = withJobOrgID(ctx, orgID)
				payload = json.RawMessage(`{}`)
			}
			if tt.payload != "" {
				payload = json.RawMessage(tt.payload)
			}
			if tt.payload == "" {
				read := mock.ExpectQuery(`FROM organizations WHERE id = @id`).WithArgs(pgx.NamedArgs{"id": orgID})
				if tt.failedStage == "organization" {
					read.WillReturnError(failure)
				} else {
					now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
					read.WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).AddRow(orgID, "Tenant", []byte(tt.settings), now, now))
					cleanup := mock.ExpectQuery(`SELECT delete_expired_audit_logs`).WithArgs(orgID, tt.expectedDays)
					if tt.failedStage == "cleanup" {
						cleanup.WillReturnError(failure)
					} else {
						cleanup.WillReturnRows(pgxmock.NewRows([]string{"deleted"}).AddRow(int64(7)))
					}
				}
			}
			var logs bytes.Buffer
			handler := newAuditRetentionCleanupHandler(&Stores{Organizations: db.NewOrganizationStore(mock), AuditLogs: db.NewAuditLogStore(mock)}, zerolog.New(&logs))
			err = handler(ctx, "audit_retention_cleanup", payload)
			if tt.expectedError != "" {
				require.ErrorContains(t, err, tt.expectedError, "invalid input and dependency errors should remain observable")
				require.NotContains(t, logs.String(), "audit retention cleanup complete", "failed cleanup must not claim completion")
				if tt.failedStage != "" {
					require.ErrorIs(t, err, failure, "worker retry should retain the original database error")
				}
			} else {
				require.NoError(t, err, "tenant retention should complete with the effective policy")
				require.Contains(t, logs.String(), `"deleted":7`, "completion should report the confirmed deletion count")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "retention must use only the expected tenant-scoped reads and deletes")
		})
	}
}

func TestDataRetentionHandlerPreservesIndependentFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		failedStores []string
	}{
		{name: "all cleanup stages succeed"},
		{name: "queue snapshot failure does not skip later cleanup", failedStores: []string{"snapshots"}},
		{name: "webhook failure does not skip logs or jobs", failedStores: []string{"webhooks"}},
		{name: "all failures retain their causes", failedStores: []string{"snapshots", "webhooks", "logs", "jobs"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "data retention database fixture should initialize")
			defer mock.Close()
			orgID := uuid.New()
			failures := make(map[string]error)
			for _, stage := range tt.failedStores {
				failures[stage] = errors.New(stage + " database failure")
			}
			snapshots := mock.ExpectExec(`DELETE FROM code_review_dispute_queue_snapshots WHERE org_id = @org_id AND expires_at <= now\(\)`).WithArgs(pgx.NamedArgs{"org_id": orgID})
			if cause := failures["snapshots"]; cause != nil {
				snapshots.WillReturnError(cause)
			} else {
				snapshots.WillReturnResult(pgxmock.NewResult("DELETE", 3))
			}
			for _, stage := range []struct {
				name, query string
				days        int
			}{
				{name: "webhooks", query: `SELECT delete_expired_webhook_deliveries`, days: 7},
				{name: "logs", query: `SELECT delete_expired_session_logs`, days: 14},
				{name: "jobs", query: `SELECT delete_expired_completed_jobs`, days: 30},
			} {
				query := mock.ExpectQuery(stage.query).WithArgs(stage.days)
				if cause := failures[stage.name]; cause != nil {
					query.WillReturnError(cause)
				} else {
					query.WillReturnRows(pgxmock.NewRows([]string{"deleted"}).AddRow(int64(2)))
				}
			}
			stores := &Stores{CodeReviewDisputes: db.NewCodeReviewDisputeStore(mock), Webhooks: db.NewWebhookDeliveryStore(mock), SessionLogs: db.NewSessionLogStore(mock), Jobs: db.NewJobStore(mock)}
			handler := newDataRetentionCleanupHandler(stores, DataRetentionConfig{WebhookDays: 7, LogsDays: 14, JobsDays: 30}, zerolog.Nop())
			err = handler(withJobOrgID(context.Background(), orgID), "data_retention_cleanup", json.RawMessage(`{}`))
			if len(failures) == 0 {
				require.NoError(t, err, "independent successful stages should complete retention")
			} else {
				for stage, cause := range failures {
					require.ErrorIs(t, err, cause, "combined error should preserve the %s failure for retry", stage)
				}
			}
			require.NoError(t, mock.ExpectationsWereMet(), "every enabled cleanup must run even after an earlier stage fails")
		})
	}
}

type retentionCutoffArgument struct {
	first    *time.Time
	earliest time.Time
	latest   time.Time
}

func (a retentionCutoffArgument) Match(value any) bool {
	cutoff, ok := value.(time.Time)
	if !ok || cutoff.Before(a.earliest) || cutoff.After(a.latest) {
		return false
	}
	if a.first.IsZero() {
		*a.first = cutoff
	}
	return a.first.Equal(cutoff)
}

func TestSlackPayloadRetentionBatchesAndTenantFailures(t *testing.T) {
	t.Parallel()
	failure := errors.New("Slack retention database failure")
	tests := []struct {
		name          string
		batch         int
		failedStage   string
		expectedBatch int
	}{
		{name: "configured batches continue until short batch", batch: 2, expectedBatch: 2},
		{name: "default batch remains bounded", expectedBatch: 1000},
		{name: "failed organization does not block next tenant", batch: 2, expectedBatch: 2, failedStage: "redact"},
		{name: "organization scan failure remains retryable", batch: 2, expectedBatch: 2, failedStage: "organizations"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "Slack retention fixture should initialize")
			defer mock.Close()
			firstOrg, secondOrg := uuid.New(), uuid.New()
			query := mock.ExpectQuery(`SELECT id FROM organizations ORDER BY created_at ASC`)
			var logs bytes.Buffer
			var expectedTotal int64
			if tt.failedStage == "organizations" {
				query.WillReturnError(failure)
			} else {
				query.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(firstOrg).AddRow(secondOrg))
				var firstCutoff time.Time
				cutoff := retentionCutoffArgument{first: &firstCutoff, earliest: time.Now().AddDate(0, 0, -7).Add(-time.Minute), latest: time.Now().AddDate(0, 0, -7).Add(time.Minute)}
				args := func(orgID uuid.UUID) pgx.NamedArgs {
					return pgx.NamedArgs{"org_id": orgID, "cutoff": cutoff, "limit": tt.expectedBatch}
				}
				const redactSQL = `(?s)UPDATE slack_inbound_events.*WHERE org_id = @org_id.*received_at < @cutoff.*LIMIT @limit`
				if tt.failedStage == "redact" {
					mock.ExpectExec(redactSQL).WithArgs(args(firstOrg)).WillReturnError(failure)
				} else {
					mock.ExpectExec(redactSQL).WithArgs(args(firstOrg)).WillReturnResult(pgxmock.NewResult("UPDATE", int64(tt.expectedBatch)))
					mock.ExpectExec(redactSQL).WithArgs(args(firstOrg)).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
					expectedTotal = int64(tt.expectedBatch + 1)
				}
				mock.ExpectExec(redactSQL).WithArgs(args(secondOrg)).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				expectedTotal++
			}
			stores := &Stores{Organizations: db.NewOrganizationStore(mock), SlackInboundEvents: db.NewSlackInboundEventStore(mock)}
			handler := newDataRetentionCleanupHandler(stores, DataRetentionConfig{SlackInboundPayloadDays: 7, SlackInboundPayloadBatch: tt.batch}, zerolog.New(&logs))
			err = handler(context.Background(), "data_retention_cleanup", nil)
			if tt.failedStage != "" {
				require.ErrorIs(t, err, failure, "tenant cleanup failure should remain visible for worker retry")
			} else {
				require.NoError(t, err, "short batches should finish successful bounded redaction")
			}
			if tt.failedStage != "organizations" {
				var completion struct {
					Redacted int64 `json:"redacted"`
				}
				for _, line := range bytes.Split(logs.Bytes(), []byte{'\n'}) {
					if bytes.Contains(line, []byte("Slack inbound payload cleanup complete")) {
						require.NoError(t, json.Unmarshal(line, &completion), "confirmed redaction progress should be structured")
					}
				}
				require.Equal(t, expectedTotal, completion.Redacted, "progress should count only successful batches across both tenants")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "redaction should stop at short batches, retain a fixed cutoff and scope every batch to its tenant")
		})
	}
}
