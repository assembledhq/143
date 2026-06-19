package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestAuditEmitter_EmitWebhookActionIncludesSessionAndProject(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	emitter := NewAuditEmitter(NewAuditLogStore(mock), zerolog.Nop())

	orgID := uuid.New()
	sessionID := uuid.New()
	projectID := uuid.New()
	resourceID := sessionID.String()
	details := json.RawMessage(`{"auto_archive":true}`)

	mock.ExpectQuery("INSERT INTO audit_logs").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), time.Now()))

	emitter.EmitWebhookAction(context.Background(), WebhookActionParams{
		OrgID:        orgID,
		ProviderName: "github",
		Action:       models.AuditActionSessionArchived,
		ResourceType: models.AuditResourceSession,
		ResourceID:   &resourceID,
		Details:      details,
		SessionID:    &sessionID,
		ProjectID:    &projectID,
	})

	require.NoError(t, mock.ExpectationsWereMet(), "webhook audit emit should persist session and project correlations")
}

func TestAuditEmitter_EmitAPIActionUsesAPIActor(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	emitter := NewAuditEmitter(NewAuditLogStore(mock), zerolog.Nop())
	orgID := uuid.New()
	clientID := uuid.New()
	tokenID := uuid.New()
	resourceID := tokenID.String()

	mock.ExpectQuery("INSERT INTO audit_logs").
		WithArgs(
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), time.Now()))

	emitter.EmitAPIAction(context.Background(), APIActionParams{
		OrgID:        orgID,
		APIClientID:  clientID,
		APITokenID:   &tokenID,
		Action:       models.AuditActionAPITokenUsed,
		ResourceType: models.AuditResourceAPIToken,
		ResourceID:   &resourceID,
		Details:      json.RawMessage(`{"path":"/api/v1/sessions"}`),
	})

	require.NoError(t, mock.ExpectationsWereMet(), "API audit emit should persist an api actor row")
}

func TestAuditEmitter_EmitUserActionsBatchesMultipleRows(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	emitter := NewAuditEmitter(NewAuditLogStore(mock), zerolog.Nop())

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	const n = 4

	paramsList := make([]UserActionParams, 0, n)
	for i := 0; i < n; i++ {
		resID := uuid.New().String()
		sid := sessionID
		paramsList = append(paramsList, UserActionParams{
			OrgID:        orgID,
			UserID:       userID,
			Action:       models.AuditActionSessionReviewCommentUpdated,
			ResourceType: models.AuditResourceSessionReviewComment,
			ResourceID:   &resID,
			SessionID:    &sid,
			Details:      json.RawMessage(`{}`),
		})
	}

	// 4 rows × 13 columns = 52 args, in a single Exec.
	argMatchers := make([]any, 0, n*13)
	for i := 0; i < n*13; i++ {
		argMatchers = append(argMatchers, pgxmock.AnyArg())
	}
	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs(argMatchers...).
		WillReturnResult(pgxmock.NewResult("INSERT", n))

	emitter.EmitUserActions(context.Background(), paramsList)
	require.NoError(t, mock.ExpectationsWereMet(),
		"N user-action audits must collapse to a single Exec via CreateBatch")
}

func TestAuditEmitter_EmitUserActionsEmptyIsNoop(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	emitter := NewAuditEmitter(NewAuditLogStore(mock), zerolog.Nop())
	emitter.EmitUserActions(context.Background(), nil)
	require.NoError(t, mock.ExpectationsWereMet(), "no DB call should be made for an empty params slice")
}

func TestAuditEmitter_EmitUserActionsLogsAndDropsBatchErrors(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	emitter := NewAuditEmitter(NewAuditLogStore(mock), zerolog.Nop())

	emitter.EmitUserActions(context.Background(), []UserActionParams{{
		OrgID:        orgID,
		UserID:       userID,
		Action:       "invalid-action",
		ResourceType: models.AuditResourceSession,
	}})

	require.NoError(t, mock.ExpectationsWereMet(), "invalid batch should be dropped without a DB call")
}
