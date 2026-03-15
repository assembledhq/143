package db

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var auditLogColumns = []string{
	"id", "org_id", "actor_type", "actor_id", "user_id",
	"action", "resource_type", "resource_id",
	"details", "request_id", "ip_address", "user_agent",
	"session_id", "project_id", "created_at",
}

func TestAuditLogStore_Create(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	tests := []struct {
		name      string
		entry     *models.AuditLog
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "creates user audit log entry",
			entry: &models.AuditLog{
				OrgID:        orgID,
				ActorType:    models.AuditActorUser,
				ActorID:      userID.String(),
				UserID:       &userID,
				Action:       models.AuditActionSessionCreated,
				ResourceType: models.AuditResourceSession,
			},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("INSERT INTO audit_logs").
					WithArgs(
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(),
					).
					WillReturnRows(
						pgxmock.NewRows([]string{"id", "created_at"}).
							AddRow(int64(1), now),
					)
			},
		},
		{
			name: "creates system audit log entry",
			entry: &models.AuditLog{
				OrgID:        orgID,
				ActorType:    models.AuditActorSystem,
				ActorID:      "pm_agent",
				Action:       models.AuditActionPMPlanCreated,
				ResourceType: models.AuditResourcePMPlan,
			},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("INSERT INTO audit_logs").
					WithArgs(
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(),
					).
					WillReturnRows(
						pgxmock.NewRows([]string{"id", "created_at"}).
							AddRow(int64(2), now),
					)
			},
		},
		{
			name: "rejects invalid actor type",
			entry: &models.AuditLog{
				OrgID:        orgID,
				ActorType:    "invalid",
				ActorID:      "test",
				Action:       models.AuditActionAuthLogin,
				ResourceType: models.AuditResourceUser,
			},
			setupMock: func(mock pgxmock.PgxPoolIface) {},
			expectErr: true,
		},
		{
			name: "rejects invalid action",
			entry: &models.AuditLog{
				OrgID:        orgID,
				ActorType:    models.AuditActorUser,
				ActorID:      userID.String(),
				Action:       "bad.action",
				ResourceType: models.AuditResourceUser,
			},
			setupMock: func(mock pgxmock.PgxPoolIface) {},
			expectErr: true,
		},
		{
			name: "rejects invalid resource type",
			entry: &models.AuditLog{
				OrgID:        orgID,
				ActorType:    models.AuditActorUser,
				ActorID:      userID.String(),
				Action:       models.AuditActionAuthLogin,
				ResourceType: "bad_type",
			},
			setupMock: func(mock pgxmock.PgxPoolIface) {},
			expectErr: true,
		},
		{
			name: "returns error on database failure",
			entry: &models.AuditLog{
				OrgID:        orgID,
				ActorType:    models.AuditActorUser,
				ActorID:      userID.String(),
				Action:       models.AuditActionAuthLogin,
				ResourceType: models.AuditResourceUser,
			},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("INSERT INTO audit_logs").
					WithArgs(
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(),
					).
					WillReturnError(fmt.Errorf("connection refused"))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should initialize mock pool without error")
			defer mock.Close()

			store := NewAuditLogStore(mock)
			tt.setupMock(mock)

			err = store.Create(context.Background(), tt.entry)
			if tt.expectErr {
				require.Error(t, err, "should return an error for invalid input or database failure")
				return
			}
			require.NoError(t, err, "should create audit log entry without error")
			require.NotZero(t, tt.entry.ID, "should populate entry ID after creation")
			require.NotZero(t, tt.entry.CreatedAt, "should populate entry CreatedAt after creation")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestAuditLogStore_List(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	tests := []struct {
		name      string
		filters   AuditLogFilters
		setupMock func(mock pgxmock.PgxPoolIface)
		expected  int
		expectErr bool
	}{
		{
			name:    "returns entries for org with no filters",
			filters: AuditLogFilters{},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("(?s)SELECT .+ FROM audit_logs WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(auditLogColumns).
							AddRow(int64(1), orgID, "user", userID.String(), &userID,
								"session.created", "session", nil,
								json.RawMessage(`{}`), nil, nil, nil,
								nil, nil, now).
							AddRow(int64(2), orgID, "system", "pm_agent", nil,
								"pm.plan_created", "pm_plan", nil,
								nil, nil, nil, nil,
								nil, nil, now),
					)
			},
			expected: 2,
		},
		{
			name:    "filters by actor_type",
			filters: AuditLogFilters{ActorType: models.AuditActorUser},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("(?s)SELECT .+ FROM audit_logs WHERE org_id .+ AND actor_type").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(auditLogColumns).
							AddRow(int64(1), orgID, "user", userID.String(), &userID,
								"session.created", "session", nil,
								nil, nil, nil, nil,
								nil, nil, now),
					)
			},
			expected: 1,
		},
		{
			name:    "filters by action",
			filters: AuditLogFilters{Action: models.AuditActionAuthLogin},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("(?s)SELECT .+ FROM audit_logs WHERE org_id .+ AND action").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(auditLogColumns))
			},
			expected: 0,
		},
		{
			name:    "filters by action_prefix",
			filters: AuditLogFilters{ActionPrefix: "session."},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("(?s)SELECT .+ FROM audit_logs WHERE org_id .+ AND action LIKE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(auditLogColumns))
			},
			expected: 0,
		},
		{
			name:    "filters by user_id",
			filters: AuditLogFilters{UserID: &userID},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("(?s)SELECT .+ FROM audit_logs WHERE org_id .+ AND user_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(auditLogColumns))
			},
			expected: 0,
		},
		{
			name:    "returns empty when no entries exist",
			filters: AuditLogFilters{},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("(?s)SELECT .+ FROM audit_logs WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(auditLogColumns))
			},
			expected: 0,
		},
		{
			name:    "returns error on database failure",
			filters: AuditLogFilters{},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("(?s)SELECT .+ FROM audit_logs WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnError(fmt.Errorf("connection refused"))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should initialize mock pool without error")
			defer mock.Close()

			store := NewAuditLogStore(mock)
			tt.setupMock(mock)

			entries, err := store.List(context.Background(), orgID, tt.filters)
			if tt.expectErr {
				require.Error(t, err, "should return an error for database failure")
				return
			}
			require.NoError(t, err, "should list audit log entries without error")
			require.Len(t, entries, tt.expected, "should return the expected number of entries")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestAuditLogStore_DeleteExpired(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()

	tests := []struct {
		name          string
		orgID         uuid.UUID
		retentionDays int
		setupMock     func(mock pgxmock.PgxPoolIface)
		expected      int64
		expectErr     bool
	}{
		{
			name:          "deletes expired entries for org",
			orgID:         orgID,
			retentionDays: 90,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT delete_expired_audit_logs").
					WithArgs(orgID, 90).
					WillReturnRows(pgxmock.NewRows([]string{"delete_expired_audit_logs"}).AddRow(int64(5)))
			},
			expected: 5,
		},
		{
			name:          "returns zero when no expired entries exist",
			orgID:         orgID,
			retentionDays: 90,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT delete_expired_audit_logs").
					WithArgs(orgID, 90).
					WillReturnRows(pgxmock.NewRows([]string{"delete_expired_audit_logs"}).AddRow(int64(0)))
			},
			expected: 0,
		},
		{
			name:          "passes org_id to database function for tenant isolation",
			orgID:         orgID,
			retentionDays: 30,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT delete_expired_audit_logs").
					WithArgs(orgID, 30).
					WillReturnRows(pgxmock.NewRows([]string{"delete_expired_audit_logs"}).AddRow(int64(3)))
			},
			expected: 3,
		},
		{
			name:          "returns error on database failure",
			orgID:         orgID,
			retentionDays: 90,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT delete_expired_audit_logs").
					WithArgs(orgID, 90).
					WillReturnError(fmt.Errorf("connection refused"))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should initialize mock pool without error")
			defer mock.Close()

			store := NewAuditLogStore(mock)
			tt.setupMock(mock)

			deleted, err := store.DeleteExpired(context.Background(), tt.orgID, tt.retentionDays)
			if tt.expectErr {
				require.Error(t, err, "should return an error for database failure")
				return
			}
			require.NoError(t, err, "should delete expired audit logs without error")
			require.Equal(t, tt.expected, deleted, "should return the expected number of deleted entries")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestAuditLogStore_GetByID(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	now := time.Now()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "returns entry when found",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("(?s)SELECT .+ FROM audit_logs WHERE org_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(auditLogColumns).
							AddRow(int64(42), orgID, "user", "actor-1", nil,
								"auth.login", "user", nil,
								nil, nil, nil, nil,
								nil, nil, now),
					)
			},
		},
		{
			name: "returns error when not found",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("(?s)SELECT .+ FROM audit_logs WHERE org_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(auditLogColumns))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should initialize mock pool without error")
			defer mock.Close()

			store := NewAuditLogStore(mock)
			tt.setupMock(mock)

			entry, err := store.GetByID(context.Background(), orgID, 42)
			if tt.expectErr {
				require.Error(t, err, "should return an error when entry is not found")
				return
			}
			require.NoError(t, err, "should retrieve audit log entry without error")
			require.Equal(t, int64(42), entry.ID, "should return the entry with the requested ID")
			require.Equal(t, models.AuditActorType("user"), entry.ActorType, "should return the correct actor type")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}
