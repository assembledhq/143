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

func TestSessionSandboxHolderStore_AcquireCodeReview(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		found bool
	}{
		{name: "eligible active review", found: true},
		{name: "ineligible or stale review"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgx mock should be created")
			defer mock.Close()
			orgID := uuid.New()
			sessionID := uuid.New()
			threadID := uuid.New()
			reviewID := uuid.New()
			leaseToken := uuid.New()
			now := time.Now().UTC()
			rows := pgxmock.NewRows(sessionSandboxHolderTestColumns)
			if tt.found {
				rows.AddRow(uuid.New(), orgID, sessionID, "container-1", models.SessionSandboxHolderKindCodeReview,
					reviewID, "worker-1", leaseToken, models.SessionSandboxHolderStatusActive,
					now, now.Add(time.Minute), now, nil, now)
			}
			mock.ExpectQuery(`WITH eligible AS MATERIALIZED[\s\S]+JOIN nodes n[\s\S]+n\.last_heartbeat_at >= now\(\) - interval '90 seconds'[\s\S]+FOR UPDATE OF m, s[\s\S]+ON CONFLICT[\s\S]+created_at = now\(\)[\s\S]+h\.container_id = EXCLUDED\.container_id[\s\S]+h\.status = 'active'`).
				WithArgs(orgID, sessionID, "container-1", "worker-1", threadID.String(), leaseToken, 60).
				WillReturnRows(rows)
			store := NewSessionSandboxHolderStore(mock)
			holder, found, err := store.AcquireCodeReview(context.Background(), orgID, AcquireCodeReviewSandboxHolderParams{
				SessionID: sessionID, ThreadID: threadID, ContainerID: "container-1",
				OwnerNodeID: "worker-1", LeaseToken: leaseToken, LeaseDuration: time.Minute,
			})
			require.NoError(t, err, "review holder acquisition should handle both eligible and rejected reviews")
			require.Equal(t, tt.found, found, "only a trusted active review may retain its workspace")
			if tt.found {
				require.Equal(t, reviewID, holder.HolderID, "holder should be keyed by the active review metadata id")
				require.Equal(t, leaseToken, holder.LeaseToken, "new holder should retain its fencing token")
			} else {
				require.Equal(t, models.SessionSandboxHolder{}, holder, "a rejected review should not expose a holder")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionSandboxHolderStore_RenewAndReleaseCodeReview(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		rows     int64
		wantDone bool
	}{
		{name: "matching review holder", rows: 1, wantDone: true},
		{name: "multiple matching review holders", rows: 2, wantDone: true},
		{name: "missing or fenced holder"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgx mock should be created")
			defer mock.Close()
			orgID, sessionID := uuid.New(), uuid.New()
			mock.ExpectExec(`WITH eligible AS MATERIALIZED[\s\S]+JOIN nodes n[\s\S]+m\.status IN \('queued', 'running'\)[\s\S]+n\.last_heartbeat_at >= now\(\) - interval '90 seconds'[\s\S]+h\.expires_at > now\(\)[\s\S]+h\.heartbeat_at <= now\(\) - interval '20 seconds'`).
				WithArgs(orgID, sessionID, 60).WillReturnResult(pgxmock.NewResult("UPDATE", tt.rows))
			mock.ExpectExec(`WITH terminal AS MATERIALIZED[\s\S]+status NOT IN \('queued', 'running'\)[\s\S]+h\.holder_kind = 'code_review'`).
				WithArgs(orgID, sessionID).WillReturnResult(pgxmock.NewResult("UPDATE", tt.rows))
			store := NewSessionSandboxHolderStore(mock)
			renewed, err := store.RenewCodeReview(context.Background(), orgID, sessionID, time.Minute)
			require.NoError(t, err, "review holder renewal should execute with active-review fencing")
			require.Equal(t, tt.wantDone, renewed, "renewal should report only a matching live holder")
			released, err := store.ReleaseTerminalCodeReview(context.Background(), orgID, sessionID)
			require.NoError(t, err, "terminal review holder release should execute")
			require.Equal(t, tt.wantDone, released, "release should report only a terminal review holder")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionSandboxHolderStore_ExpireCodeReviewHolders(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should be created")
	defer mock.Close()
	mock.ExpectExec(`WITH candidates AS MATERIALIZED[\s\S]+h\.holder_kind = 'code_review'[\s\S]+h\.expires_at <= now\(\)[\s\S]+FOR UPDATE OF h SKIP LOCKED`).
		WithArgs(100).WillReturnResult(pgxmock.NewResult("UPDATE", 3))
	store := NewSessionSandboxHolderStore(mock)
	expired, err := store.ExpireCodeReviewHolders(context.Background(), 200)
	require.NoError(t, err, "system cleanup should expire detached or overdue review holders")
	require.Equal(t, int64(3), expired, "cleanup should report the number of expired review holders")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionSandboxHolderStore_ReleaseCodeReviewHoldersByOwner(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should be created")
	defer mock.Close()
	mock.ExpectExec(`UPDATE session_sandbox_holders h[\s\S]+n\.status = 'draining'[\s\S]+h\.owner_node_id = n\.id[\s\S]+h\.holder_kind = 'code_review'`).
		WithArgs("worker-1").WillReturnResult(pgxmock.NewResult("UPDATE", 2))
	released, err := NewSessionSandboxHolderStore(mock).ReleaseCodeReviewHoldersByOwner(context.Background(), "worker-1")
	require.NoError(t, err, "draining worker should release its review holders")
	require.Equal(t, int64(2), released, "drain cleanup should report every released holder")
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
