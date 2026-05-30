package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var sessionSandboxHolderTestColumns = []string{
	"id", "org_id", "session_id", "container_id", "holder_kind", "holder_id",
	"owner_node_id", "lease_token", "status", "heartbeat_at", "expires_at",
	"created_at", "released_at", "updated_at",
}

func TestSessionSandboxHolderStore_CreateActive(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	holderID := uuid.New()
	rowID := uuid.New()
	leaseToken := uuid.New()
	now := time.Now().UTC()

	mock.ExpectQuery("INSERT INTO session_sandbox_holders").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionSandboxHolderTestColumns).AddRow(
			rowID, orgID, sessionID, "container-1", models.SessionSandboxHolderKindThreadRuntime,
			holderID, "worker-1", leaseToken, models.SessionSandboxHolderStatusActive,
			now, now.Add(time.Minute), now, nil, now,
		))

	store := NewSessionSandboxHolderStore(mock)
	holder, err := store.CreateActive(context.Background(), orgID, CreateSessionSandboxHolderParams{
		SessionID:     sessionID,
		ContainerID:   "container-1",
		HolderKind:    models.SessionSandboxHolderKindThreadRuntime,
		HolderID:      holderID,
		OwnerNodeID:   "worker-1",
		LeaseToken:    leaseToken,
		LeaseDuration: time.Minute,
	})

	require.NoError(t, err, "CreateActive should not return an error")
	require.Equal(t, rowID, holder.ID, "CreateActive should return the inserted holder")
	require.Equal(t, models.SessionSandboxHolderStatusActive, holder.Status, "CreateActive should create an active holder")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionSandboxHolderStore_ReleaseWithLease(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	holderID := uuid.New()
	leaseToken := uuid.New()

	mock.ExpectExec("UPDATE session_sandbox_holders").
		WithArgs(orgID, sessionID, models.SessionSandboxHolderKindThreadRuntime, holderID, leaseToken).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	store := NewSessionSandboxHolderStore(mock)
	released, err := store.ReleaseWithLease(context.Background(), orgID, sessionID, models.SessionSandboxHolderKindThreadRuntime, holderID, leaseToken)

	require.NoError(t, err, "ReleaseWithLease should not return an error")
	require.True(t, released, "ReleaseWithLease should report a matching lease release")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionSandboxHolderStore_HeartbeatWithLease(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	holderID := uuid.New()
	leaseToken := uuid.New()

	mock.ExpectExec("UPDATE session_sandbox_holders").
		WithArgs(orgID, sessionID, models.SessionSandboxHolderKindThreadRuntime, holderID, leaseToken, 90).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	store := NewSessionSandboxHolderStore(mock)
	ok, err := store.HeartbeatWithLease(context.Background(), orgID, sessionID, models.SessionSandboxHolderKindThreadRuntime, holderID, leaseToken, 90*time.Second)

	require.NoError(t, err, "HeartbeatWithLease should not return an error")
	require.True(t, ok, "HeartbeatWithLease should report a matching lease heartbeat")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionSandboxHolderStore_CountActiveThreadRuntimesBySession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()

	mock.ExpectQuery("SELECT count").
		WithArgs(orgID, sessionID).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))

	store := NewSessionSandboxHolderStore(mock)
	count, err := store.CountActiveThreadRuntimesBySession(context.Background(), orgID, sessionID)

	require.NoError(t, err, "CountActiveThreadRuntimesBySession should not return an error")
	require.Equal(t, 2, count, "CountActiveThreadRuntimesBySession should only count mutating thread runtime holders")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionSandboxHolderStore_CountActiveThreadRuntimesBySessionExcluding(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	holderID := uuid.New()

	mock.ExpectQuery("holder_id <>").
		WithArgs(orgID, sessionID, holderID).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))

	store := NewSessionSandboxHolderStore(mock)
	count, err := store.CountActiveThreadRuntimesBySessionExcluding(context.Background(), orgID, sessionID, holderID)

	require.NoError(t, err, "CountActiveThreadRuntimesBySessionExcluding should not return an error")
	require.Equal(t, 1, count, "CountActiveThreadRuntimesBySessionExcluding should omit the current runtime holder")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
