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

func newAuditLogColumns() []string {
	return []string{
		"id", "org_id", "actor_type", "actor_id", "user_id",
		"action", "resource_type", "resource_id",
		"details", "request_id", "ip_address", "user_agent",
		"session_id", "project_id", "created_at",
	}
}

func TestAuditLogStore_Create(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		entry     func(orgID, userID uuid.UUID) *models.AuditLog
		setupMock func(mock pgxmock.PgxPoolIface, now time.Time, expectedID int64)
		expectErr bool
	}{
		{
			name: "creates user audit log entry",
			entry: func(orgID, userID uuid.UUID) *models.AuditLog {
				return &models.AuditLog{
					OrgID:        orgID,
					ActorType:    models.AuditActorUser,
					ActorID:      userID.String(),
					UserID:       &userID,
					Action:       models.AuditActionSessionCreated,
					ResourceType: models.AuditResourceSession,
				}
			},
			setupMock: func(mock pgxmock.PgxPoolIface, now time.Time, expectedID int64) {
				mock.ExpectQuery("INSERT INTO audit_logs").
					WithArgs(
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(),
					).
					WillReturnRows(
						pgxmock.NewRows([]string{"id", "created_at"}).
							AddRow(expectedID, now),
					)
			},
		},
		{
			name: "creates system audit log entry",
			entry: func(orgID, _ uuid.UUID) *models.AuditLog {
				return &models.AuditLog{
					OrgID:        orgID,
					ActorType:    models.AuditActorSystem,
					ActorID:      "pm_agent",
					Action:       models.AuditActionPMPlanCreated,
					ResourceType: models.AuditResourcePMPlan,
				}
			},
			setupMock: func(mock pgxmock.PgxPoolIface, now time.Time, expectedID int64) {
				mock.ExpectQuery("INSERT INTO audit_logs").
					WithArgs(
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(),
					).
					WillReturnRows(
						pgxmock.NewRows([]string{"id", "created_at"}).
							AddRow(expectedID, now),
					)
			},
		},
		{
			name: "rejects invalid actor type",
			entry: func(orgID, _ uuid.UUID) *models.AuditLog {
				return &models.AuditLog{
					OrgID:        orgID,
					ActorType:    "invalid",
					ActorID:      "test",
					Action:       models.AuditActionAuthLogin,
					ResourceType: models.AuditResourceUser,
				}
			},
			setupMock: func(mock pgxmock.PgxPoolIface, _ time.Time, _ int64) {},
			expectErr: true,
		},
		{
			name: "rejects invalid action",
			entry: func(orgID, userID uuid.UUID) *models.AuditLog {
				return &models.AuditLog{
					OrgID:        orgID,
					ActorType:    models.AuditActorUser,
					ActorID:      userID.String(),
					Action:       "bad.action",
					ResourceType: models.AuditResourceUser,
				}
			},
			setupMock: func(mock pgxmock.PgxPoolIface, _ time.Time, _ int64) {},
			expectErr: true,
		},
		{
			name: "rejects invalid resource type",
			entry: func(orgID, userID uuid.UUID) *models.AuditLog {
				return &models.AuditLog{
					OrgID:        orgID,
					ActorType:    models.AuditActorUser,
					ActorID:      userID.String(),
					Action:       models.AuditActionAuthLogin,
					ResourceType: "bad_type",
				}
			},
			setupMock: func(mock pgxmock.PgxPoolIface, _ time.Time, _ int64) {},
			expectErr: true,
		},
		{
			name: "returns error on database failure",
			entry: func(orgID, userID uuid.UUID) *models.AuditLog {
				return &models.AuditLog{
					OrgID:        orgID,
					ActorType:    models.AuditActorUser,
					ActorID:      userID.String(),
					Action:       models.AuditActionAuthLogin,
					ResourceType: models.AuditResourceUser,
				}
			},
			setupMock: func(mock pgxmock.PgxPoolIface, _ time.Time, _ int64) {
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

			orgID := uuid.New()
			userID := uuid.New()
			now := time.Now()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should initialize mock pool without error")
			defer mock.Close()

			store := NewAuditLogStore(mock)
			tt.setupMock(mock, now, int64(1))

			entry := tt.entry(orgID, userID)
			err = store.Create(context.Background(), entry)
			if tt.expectErr {
				require.Error(t, err, "should return an error for invalid input or database failure")
				return
			}
			require.NoError(t, err, "should create audit log entry without error")
			require.NotZero(t, entry.ID, "should populate entry ID after creation")
			require.NotZero(t, entry.CreatedAt, "should populate entry CreatedAt after creation")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestAuditLogStore_List(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		filters   AuditLogFilters
		setupMock func(mock pgxmock.PgxPoolIface, orgID, userID uuid.UUID, now time.Time)
		expected  func(orgID, userID uuid.UUID, now time.Time) []models.AuditLog
		expectErr bool
	}{
		{
			name:    "returns entries for org with no filters",
			filters: AuditLogFilters{},
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, userID uuid.UUID, now time.Time) {
				mock.ExpectQuery("(?s)SELECT .+ FROM audit_logs WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(newAuditLogColumns()).
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
			expected: func(orgID, userID uuid.UUID, now time.Time) []models.AuditLog {
				return []models.AuditLog{
					{
						ID: 1, OrgID: orgID, ActorType: "user",
						ActorID: userID.String(), UserID: &userID,
						Action: "session.created", ResourceType: "session",
						Details: json.RawMessage(`{}`), CreatedAt: now,
					},
					{
						ID: 2, OrgID: orgID, ActorType: "system",
						ActorID: "pm_agent",
						Action:  "pm.plan_created", ResourceType: "pm_plan",
						CreatedAt: now,
					},
				}
			},
		},
		{
			name:    "filters by actor_type",
			filters: AuditLogFilters{ActorType: models.AuditActorUser},
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, userID uuid.UUID, now time.Time) {
				mock.ExpectQuery("(?s)SELECT .+ FROM audit_logs WHERE org_id .+ AND actor_type").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(newAuditLogColumns()).
							AddRow(int64(1), orgID, "user", userID.String(), &userID,
								"session.created", "session", nil,
								nil, nil, nil, nil,
								nil, nil, now),
					)
			},
			expected: func(orgID, userID uuid.UUID, now time.Time) []models.AuditLog {
				return []models.AuditLog{
					{
						ID: 1, OrgID: orgID, ActorType: "user",
						ActorID: userID.String(), UserID: &userID,
						Action: "session.created", ResourceType: "session",
						CreatedAt: now,
					},
				}
			},
		},
		{
			name:    "returns empty when no entries exist",
			filters: AuditLogFilters{},
			setupMock: func(mock pgxmock.PgxPoolIface, _, _ uuid.UUID, _ time.Time) {
				mock.ExpectQuery("(?s)SELECT .+ FROM audit_logs WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(newAuditLogColumns()))
			},
			expected: func(_, _ uuid.UUID, _ time.Time) []models.AuditLog {
				return []models.AuditLog{}
			},
		},
		{
			name:    "returns error on database failure",
			filters: AuditLogFilters{},
			setupMock: func(mock pgxmock.PgxPoolIface, _, _ uuid.UUID, _ time.Time) {
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

			orgID := uuid.New()
			userID := uuid.New()
			now := time.Now()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should initialize mock pool without error")
			defer mock.Close()

			store := NewAuditLogStore(mock)
			tt.setupMock(mock, orgID, userID, now)

			entries, err := store.List(context.Background(), orgID, tt.filters)
			if tt.expectErr {
				require.Error(t, err, "should return an error for database failure")
				return
			}
			require.NoError(t, err, "should list audit log entries without error")
			require.Equal(t, tt.expected(orgID, userID, now), entries, "should return the expected audit log entries")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestAuditLogStore_DeleteExpired(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		retentionDays int
		setupMock     func(mock pgxmock.PgxPoolIface, orgID uuid.UUID, retentionDays int)
		expected      int64
		expectErr     bool
	}{
		{
			name:          "deletes expired entries for org",
			retentionDays: 90,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID, retentionDays int) {
				mock.ExpectQuery("SELECT delete_expired_audit_logs").
					WithArgs(orgID, retentionDays).
					WillReturnRows(pgxmock.NewRows([]string{"delete_expired_audit_logs"}).AddRow(int64(5)))
			},
			expected: 5,
		},
		{
			name:          "returns zero when no expired entries exist",
			retentionDays: 90,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID, retentionDays int) {
				mock.ExpectQuery("SELECT delete_expired_audit_logs").
					WithArgs(orgID, retentionDays).
					WillReturnRows(pgxmock.NewRows([]string{"delete_expired_audit_logs"}).AddRow(int64(0)))
			},
			expected: 0,
		},
		{
			name:          "passes org_id to database function for tenant isolation",
			retentionDays: 30,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID, retentionDays int) {
				mock.ExpectQuery("SELECT delete_expired_audit_logs").
					WithArgs(orgID, retentionDays).
					WillReturnRows(pgxmock.NewRows([]string{"delete_expired_audit_logs"}).AddRow(int64(3)))
			},
			expected: 3,
		},
		{
			name:          "returns error on database failure",
			retentionDays: 90,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID, retentionDays int) {
				mock.ExpectQuery("SELECT delete_expired_audit_logs").
					WithArgs(orgID, retentionDays).
					WillReturnError(fmt.Errorf("connection refused"))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := uuid.New()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should initialize mock pool without error")
			defer mock.Close()

			store := NewAuditLogStore(mock)
			tt.setupMock(mock, orgID, tt.retentionDays)

			deleted, err := store.DeleteExpired(context.Background(), orgID, tt.retentionDays)
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

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, orgID uuid.UUID, now time.Time)
		expected  func(orgID uuid.UUID, now time.Time) *models.AuditLog
		expectErr bool
	}{
		{
			name: "returns entry when found",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID, now time.Time) {
				mock.ExpectQuery("(?s)SELECT .+ FROM audit_logs WHERE org_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(newAuditLogColumns()).
							AddRow(int64(42), orgID, "user", "actor-1", nil,
								"auth.login", "user", nil,
								nil, nil, nil, nil,
								nil, nil, now),
					)
			},
			expected: func(orgID uuid.UUID, now time.Time) *models.AuditLog {
				return &models.AuditLog{
					ID: 42, OrgID: orgID, ActorType: "user",
					ActorID: "actor-1", Action: "auth.login",
					ResourceType: "user", CreatedAt: now,
				}
			},
		},
		{
			name: "returns error when not found",
			setupMock: func(mock pgxmock.PgxPoolIface, _ uuid.UUID, _ time.Time) {
				mock.ExpectQuery("(?s)SELECT .+ FROM audit_logs WHERE org_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(newAuditLogColumns()))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := uuid.New()
			now := time.Now()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should initialize mock pool without error")
			defer mock.Close()

			store := NewAuditLogStore(mock)
			tt.setupMock(mock, orgID, now)

			entry, err := store.GetByID(context.Background(), orgID, 42)
			if tt.expectErr {
				require.Error(t, err, "should return an error when entry is not found")
				return
			}
			require.NoError(t, err, "should retrieve audit log entry without error")
			require.Equal(t, tt.expected(orgID, now), entry, "should return the expected audit log entry")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}
