package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestEmitUserAuditsWithSession(t *testing.T) {
	t.Parallel()

	t.Run("no-ops without emitter", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodPost, "/", nil)
		emitUserAuditsWithSession(nil, req, []userAuditEntry{{Action: models.AuditActionSessionCreated}})
	})

	t.Run("no-ops without entries", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should initialize")
		defer mock.Close()

		req := httptest.NewRequest(http.MethodPost, "/", nil)
		emitter := db.NewAuditEmitter(db.NewAuditLogStore(mock), zerolog.Nop())
		emitUserAuditsWithSession(emitter, req, nil)
		require.NoError(t, mock.ExpectationsWereMet(), "empty audit entry list should not write audit rows")
	})

	t.Run("no-ops without user context", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should initialize")
		defer mock.Close()

		orgID := uuid.New()
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
		emitter := db.NewAuditEmitter(db.NewAuditLogStore(mock), zerolog.Nop())
		emitUserAuditsWithSession(emitter, req, []userAuditEntry{{Action: models.AuditActionSessionCreated}})
		require.NoError(t, mock.ExpectationsWereMet(), "missing user context should not write audit rows")
	})

	t.Run("includes request metadata on emitted rows", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should initialize")
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		sessionID := uuid.New()
		resourceID := sessionID.String()

		mock.ExpectQuery("INSERT INTO audit_logs").
			WithArgs(
				orgID,
				models.AuditActorUser,
				userID.String(),
				&userID,
				models.AuditActionSessionReviewCommentUpdated,
				models.AuditResourceSessionReviewComment,
				&resourceID,
				json.RawMessage(`{"source":"test"}`),
				pgxmock.AnyArg(),
				pgxmock.AnyArg(),
				pgxmock.AnyArg(),
				&sessionID,
				pgxmock.AnyArg(),
			).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), time.Now()))

		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set("X-Forwarded-For", "203.0.113.10")
		req.Header.Set("User-Agent", "unit-test-agent")
		ctx := middleware.WithOrgID(req.Context(), orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: "member"})
		ctx = context.WithValue(ctx, chiMiddleware.RequestIDKey, "req-123")
		req = req.WithContext(ctx)

		emitter := db.NewAuditEmitter(db.NewAuditLogStore(mock), zerolog.Nop())
		emitUserAuditsWithSession(emitter, req, []userAuditEntry{{
			Action:       models.AuditActionSessionReviewCommentUpdated,
			ResourceType: models.AuditResourceSessionReviewComment,
			ResourceID:   &resourceID,
			SessionID:    &sessionID,
			Details:      json.RawMessage(`{"source":"test"}`),
		}})

		require.NoError(t, mock.ExpectationsWereMet(), "batched helper should emit one enriched audit row")
	})
}
