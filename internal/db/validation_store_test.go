package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var validationColumns = []string{
	"id", "session_id", "org_id", "status",
	"direction_check", "correctness_check", "quality_check", "security_scan",
	"regression_test_check", "coverage_delta", "ci_check", "details",
	"started_at", "completed_at", "created_at",
}

func newValidationRow(id, sessionID, orgID uuid.UUID, now time.Time) []any {
	return []any{
		id, sessionID, orgID, "pending",
		"skip", "skip", "skip", "skip",
		"skip", json.RawMessage(`{}`), "skip", json.RawMessage(`{}`),
		nil, nil, now,
	}
}

func TestValidationStore_Create_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewValidationStore(mock)
	now := time.Now()
	generatedID := uuid.New()

	v := &models.Validation{
		SessionID: uuid.New(),
		OrgID:     uuid.New(),
		Status:    "pending",
	}

	mock.ExpectQuery("INSERT INTO validations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(generatedID, now),
		)

	err = store.Create(context.Background(), v)
	require.NoError(t, err, "should create validation without error")
	require.Equal(t, generatedID, v.ID, "should set the generated ID on the validation")
	require.Equal(t, now, v.CreatedAt, "should set the created_at timestamp on the validation")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestValidationStore_GetByID_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewValidationStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM validations WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(validationColumns).
				AddRow(newValidationRow(id, sessionID, orgID, now)...),
		)

	v, err := store.GetByID(context.Background(), orgID, id)
	require.NoError(t, err, "should retrieve validation by ID without error")
	require.Equal(t, id, v.ID, "should return the correct validation ID")
	require.Equal(t, sessionID, v.SessionID, "should return the correct agent run ID")
	require.Equal(t, orgID, v.OrgID, "should return the correct org ID")
	require.Equal(t, "pending", v.Status, "should return the correct status")
	require.Equal(t, "skip", v.DirectionCheck, "should return the correct direction check")
	require.Equal(t, "skip", v.CorrectnessCheck, "should return the correct correctness check")
	require.Equal(t, "skip", v.QualityCheck, "should return the correct quality check")
	require.Equal(t, "skip", v.SecurityScan, "should return the correct security scan")
	require.Equal(t, "skip", v.RegressionTestCheck, "should return the correct regression test check")
	require.Equal(t, "skip", v.CICheck, "should return the correct CI check")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestValidationStore_GetByID_NotFound(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewValidationStore(mock)

	mock.ExpectQuery("SELECT .+ FROM validations WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(validationColumns))

	_, err = store.GetByID(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err, "should return an error when validation is not found")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestValidationStore_GetBySessionID_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewValidationStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM validations WHERE session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(validationColumns).
				AddRow(newValidationRow(id, sessionID, orgID, now)...),
		)

	v, err := store.GetBySessionID(context.Background(), orgID, sessionID)
	require.NoError(t, err, "should retrieve validation by agent run ID without error")
	require.Equal(t, id, v.ID, "should return the correct validation ID")
	require.Equal(t, sessionID, v.SessionID, "should return the correct agent run ID")
	require.Equal(t, orgID, v.OrgID, "should return the correct org ID")
	require.Equal(t, "pending", v.Status, "should return the correct status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestValidationStore_UpdateCheck_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewValidationStore(mock)
	orgID := uuid.New()
	id := uuid.New()

	mock.ExpectExec("UPDATE validations SET direction_check").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateCheck(context.Background(), orgID, id, "direction_check", "passed", nil)
	require.NoError(t, err, "should update validation check without error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestValidationStore_UpdateCheck_InvalidName(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewValidationStore(mock)

	err = store.UpdateCheck(context.Background(), uuid.New(), uuid.New(), "invalid_check", "passed", nil)
	require.Error(t, err, "should return an error for invalid check name")
	require.Contains(t, err.Error(), "invalid check name", "error message should mention invalid check name")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestValidationStore_UpdateStatus_Running(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewValidationStore(mock)
	orgID := uuid.New()
	id := uuid.New()

	mock.ExpectExec("UPDATE validations SET status .+ started_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateStatus(context.Background(), orgID, id, "running")
	require.NoError(t, err, "should update validation status to running without error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestValidationStore_UpdateStatus_Passed(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewValidationStore(mock)
	orgID := uuid.New()
	id := uuid.New()

	mock.ExpectExec("UPDATE validations SET status .+ completed_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateStatus(context.Background(), orgID, id, "passed")
	require.NoError(t, err, "should update validation status to passed without error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
