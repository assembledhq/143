package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var validationColumns = []string{
	"id", "agent_run_id", "org_id", "status",
	"direction_check", "correctness_check", "quality_check", "security_scan",
	"regression_test_check", "coverage_delta", "ci_check", "details",
	"started_at", "completed_at", "created_at",
}

func newValidationRow(id, agentRunID, orgID uuid.UUID, now time.Time) []any {
	return []any{
		id, agentRunID, orgID, "pending",
		"skip", "skip", "skip", "skip",
		"skip", json.RawMessage(`{}`), "skip", json.RawMessage(`{}`),
		nil, nil, now,
	}
}

func TestValidationStore_Create_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewValidationStore(mock)
	now := time.Now()
	generatedID := uuid.New()

	v := &models.Validation{
		AgentRunID: uuid.New(),
		OrgID:      uuid.New(),
		Status:     "pending",
	}

	mock.ExpectQuery("INSERT INTO validations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(generatedID, now),
		)

	err = store.Create(context.Background(), v)
	require.NoError(t, err)
	assert.Equal(t, generatedID, v.ID)
	assert.Equal(t, now, v.CreatedAt)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestValidationStore_GetByID_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewValidationStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	agentRunID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM validations WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(validationColumns).
				AddRow(newValidationRow(id, agentRunID, orgID, now)...),
		)

	v, err := store.GetByID(context.Background(), orgID, id)
	require.NoError(t, err)
	assert.Equal(t, id, v.ID)
	assert.Equal(t, agentRunID, v.AgentRunID)
	assert.Equal(t, "pending", v.Status)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestValidationStore_GetByID_NotFound(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewValidationStore(mock)

	mock.ExpectQuery("SELECT .+ FROM validations WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(validationColumns))

	_, err = store.GetByID(context.Background(), uuid.New(), uuid.New())
	assert.Error(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestValidationStore_GetByAgentRunID_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewValidationStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	agentRunID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM validations WHERE agent_run_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(validationColumns).
				AddRow(newValidationRow(id, agentRunID, orgID, now)...),
		)

	v, err := store.GetByAgentRunID(context.Background(), orgID, agentRunID)
	require.NoError(t, err)
	assert.Equal(t, id, v.ID)
	assert.Equal(t, agentRunID, v.AgentRunID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestValidationStore_UpdateCheck_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewValidationStore(mock)
	orgID := uuid.New()
	id := uuid.New()

	mock.ExpectExec("UPDATE validations SET direction_check").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateCheck(context.Background(), orgID, id, "direction_check", "passed", nil)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestValidationStore_UpdateCheck_InvalidName(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewValidationStore(mock)

	err = store.UpdateCheck(context.Background(), uuid.New(), uuid.New(), "invalid_check", "passed", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid check name")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestValidationStore_UpdateStatus_Running(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewValidationStore(mock)
	orgID := uuid.New()
	id := uuid.New()

	mock.ExpectExec("UPDATE validations SET status .+ started_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateStatus(context.Background(), orgID, id, "running")
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestValidationStore_UpdateStatus_Passed(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewValidationStore(mock)
	orgID := uuid.New()
	id := uuid.New()

	mock.ExpectExec("UPDATE validations SET status .+ completed_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateStatus(context.Background(), orgID, id, "passed")
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}
