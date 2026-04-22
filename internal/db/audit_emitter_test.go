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
