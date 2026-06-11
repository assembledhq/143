package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

var reviewCommentColumns = []string{
	"id", "session_id", "org_id", "user_id", "file_path",
	"line_number", "diff_side", "body", "resolved", "resolved_at", "resolved_by_pass",
	"pass_number", "created_at", "updated_at",
}

func newTestReviewCommentHandler(t *testing.T, mock pgxmock.PgxPoolIface) *SessionReviewCommentHandler {
	t.Helper()
	return NewSessionReviewCommentHandler(
		db.NewSessionReviewCommentStore(mock),
		db.NewSessionStore(mock),
		zerolog.Nop(),
	)
}

func withReviewCommentRoutes(handler *SessionReviewCommentHandler) http.Handler {
	r := chi.NewRouter()
	r.Get("/api/v1/sessions/{id}/review-comments", handler.List)
	r.Post("/api/v1/sessions/{id}/review-comments", handler.Create)
	r.Patch("/api/v1/sessions/{id}/review-comments/{commentId}", handler.Update)
	r.Delete("/api/v1/sessions/{id}/review-comments/{commentId}", handler.Delete)
	r.Post("/api/v1/sessions/{id}/review-comments/send", handler.SendToAgent)
	return r
}

func reviewCommentRow(id, sessionID, orgID, userID uuid.UUID, filePath string, lineNumber int, body string, resolved bool) []interface{} {
	now := time.Now()
	var resolvedAt *time.Time
	var resolvedByPass *int
	if resolved {
		resolvedAt = &now
	}
	return []interface{}{
		id, sessionID, orgID, userID, filePath,
		lineNumber, "new", body, resolved, resolvedAt, resolvedByPass,
		1, now, now,
	}
}

func TestSessionReviewCommentHandler_List(t *testing.T) {
	t.Parallel()

	t.Run("returns empty list", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		handler := newTestReviewCommentHandler(t, mock)

		mock.ExpectQuery("SELECT .+ FROM session_review_comments WHERE session_id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(reviewCommentColumns))

		url := fmt.Sprintf("/api/v1/sessions/%s/review-comments", sessionID)
		req := httptest.NewRequest(http.MethodGet, url, nil)
		ctx := middleware.WithOrgID(req.Context(), orgID)
		req = req.WithContext(ctx)

		w := httptest.NewRecorder()
		withReviewCommentRoutes(handler).ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var resp models.ListResponse[models.SessionReviewComment]
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Len(t, resp.Data, 0)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns comments", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()
		commentID := uuid.New()
		handler := newTestReviewCommentHandler(t, mock)

		mock.ExpectQuery("SELECT .+ FROM session_review_comments WHERE session_id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(reviewCommentColumns).
					AddRow(reviewCommentRow(commentID, sessionID, orgID, userID, "src/app.ts", 10, "Fix this", false)...),
			)

		url := fmt.Sprintf("/api/v1/sessions/%s/review-comments", sessionID)
		req := httptest.NewRequest(http.MethodGet, url, nil)
		ctx := middleware.WithOrgID(req.Context(), orgID)
		req = req.WithContext(ctx)

		w := httptest.NewRecorder()
		withReviewCommentRoutes(handler).ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var resp models.ListResponse[models.SessionReviewComment]
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Len(t, resp.Data, 1)
		require.Equal(t, "Fix this", resp.Data[0].Body)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestSessionReviewCommentHandler_Create(t *testing.T) {
	t.Parallel()

	t.Run("creates comment successfully", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()
		commentID := uuid.New()
		handler := newTestReviewCommentHandler(t, mock)
		handler.SetAuditEmitter(newAuditEmitterForTest(mock))
		handler.SetAuditEmitter(newAuditEmitterForTest(mock))

		// GetByID for session lookup (to get current_turn)
		setupSessionMock(mock, orgID, sessionID, nil)

		// INSERT returning the new comment
		mock.ExpectQuery("INSERT INTO session_review_comments").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(reviewCommentColumns).
					AddRow(reviewCommentRow(commentID, sessionID, orgID, userID, "src/app.ts", 10, "Add tests", false)...),
			)
		expectAuditInsert(mock)

		body := `{"file_path":"src/app.ts","line_number":10,"body":"Add tests"}`
		url := fmt.Sprintf("/api/v1/sessions/%s/review-comments", sessionID)
		req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		ctx := middleware.WithOrgID(req.Context(), orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
		req = req.WithContext(ctx)

		w := httptest.NewRecorder()
		withReviewCommentRoutes(handler).ServeHTTP(w, req)

		require.Equal(t, http.StatusCreated, w.Code)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("rejects missing fields", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()
		handler := newTestReviewCommentHandler(t, mock)

		body := `{"file_path":"","body":""}`
		url := fmt.Sprintf("/api/v1/sessions/%s/review-comments", sessionID)
		req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		ctx := middleware.WithOrgID(req.Context(), orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
		req = req.WithContext(ctx)

		w := httptest.NewRecorder()
		withReviewCommentRoutes(handler).ServeHTTP(w, req)

		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("rejects zero line_number", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()
		handler := newTestReviewCommentHandler(t, mock)

		body := `{"file_path":"src/app.ts","line_number":0,"body":"test"}`
		url := fmt.Sprintf("/api/v1/sessions/%s/review-comments", sessionID)
		req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		ctx := middleware.WithOrgID(req.Context(), orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
		req = req.WithContext(ctx)

		w := httptest.NewRecorder()
		withReviewCommentRoutes(handler).ServeHTTP(w, req)

		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("rejects oversized body", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()
		handler := newTestReviewCommentHandler(t, mock)

		longBody := make([]byte, 10241)
		for i := range longBody {
			longBody[i] = 'a'
		}
		reqBody := fmt.Sprintf(`{"file_path":"src/app.ts","line_number":1,"body":"%s"}`, string(longBody))
		url := fmt.Sprintf("/api/v1/sessions/%s/review-comments", sessionID)
		req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(reqBody)))
		req.Header.Set("Content-Type", "application/json")
		ctx := middleware.WithOrgID(req.Context(), orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
		req = req.WithContext(ctx)

		w := httptest.NewRecorder()
		withReviewCommentRoutes(handler).ServeHTTP(w, req)

		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("rejects invalid side", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()
		handler := newTestReviewCommentHandler(t, mock)

		body := `{"file_path":"src/app.ts","line_number":1,"body":"test","side":"both"}`
		url := fmt.Sprintf("/api/v1/sessions/%s/review-comments", sessionID)
		req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		ctx := middleware.WithOrgID(req.Context(), orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
		req = req.WithContext(ctx)

		w := httptest.NewRecorder()
		withReviewCommentRoutes(handler).ServeHTTP(w, req)

		require.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestSessionReviewCommentHandler_Delete(t *testing.T) {
	t.Parallel()

	t.Run("deletes successfully with ownership check", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()
		commentID := uuid.New()
		handler := newTestReviewCommentHandler(t, mock)
		handler.SetAuditEmitter(newAuditEmitterForTest(mock))

		// Ownership check: GetByID returns the comment owned by the requesting user
		mock.ExpectQuery("SELECT .+ FROM session_review_comments WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(reviewCommentColumns).
					AddRow(reviewCommentRow(commentID, sessionID, orgID, userID, "src/app.ts", 10, "Fix this", false)...),
			)

		// Expect DELETE with session_id in WHERE clause
		mock.ExpectExec("DELETE FROM session_review_comments WHERE id = .+ AND org_id = .+ AND session_id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("DELETE", 1))
		expectAuditInsert(mock)

		url := fmt.Sprintf("/api/v1/sessions/%s/review-comments/%s", sessionID, commentID)
		req := httptest.NewRequest(http.MethodDelete, url, nil)
		ctx := middleware.WithOrgID(req.Context(), orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
		req = req.WithContext(ctx)

		w := httptest.NewRecorder()
		withReviewCommentRoutes(handler).ServeHTTP(w, req)

		require.Equal(t, http.StatusNoContent, w.Code)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("rejects delete by non-owner", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		ownerID := uuid.New()
		otherUserID := uuid.New()
		commentID := uuid.New()
		handler := newTestReviewCommentHandler(t, mock)

		// Ownership check: comment is owned by a different user
		mock.ExpectQuery("SELECT .+ FROM session_review_comments WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(reviewCommentColumns).
					AddRow(reviewCommentRow(commentID, sessionID, orgID, ownerID, "src/app.ts", 10, "Fix this", false)...),
			)

		url := fmt.Sprintf("/api/v1/sessions/%s/review-comments/%s", sessionID, commentID)
		req := httptest.NewRequest(http.MethodDelete, url, nil)
		ctx := middleware.WithOrgID(req.Context(), orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: otherUserID, OrgID: orgID})
		req = req.WithContext(ctx)

		w := httptest.NewRecorder()
		withReviewCommentRoutes(handler).ServeHTTP(w, req)

		require.Equal(t, http.StatusForbidden, w.Code)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns not found when comment doesn't exist", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()
		commentID := uuid.New()
		handler := newTestReviewCommentHandler(t, mock)

		// Ownership check: comment not found
		mock.ExpectQuery("SELECT .+ FROM session_review_comments WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(reviewCommentColumns))

		url := fmt.Sprintf("/api/v1/sessions/%s/review-comments/%s", sessionID, commentID)
		req := httptest.NewRequest(http.MethodDelete, url, nil)
		ctx := middleware.WithOrgID(req.Context(), orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
		req = req.WithContext(ctx)

		w := httptest.NewRecorder()
		withReviewCommentRoutes(handler).ServeHTTP(w, req)

		require.Equal(t, http.StatusNotFound, w.Code)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestSessionReviewCommentHandler_Update(t *testing.T) {
	t.Parallel()

	t.Run("updates body text", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()
		commentID := uuid.New()
		handler := newTestReviewCommentHandler(t, mock)
		handler.SetAuditEmitter(newAuditEmitterForTest(mock))

		// Ownership check: GetByID returns the comment owned by the requesting user
		mock.ExpectQuery("SELECT .+ FROM session_review_comments WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(reviewCommentColumns).
					AddRow(reviewCommentRow(commentID, sessionID, orgID, userID, "src/app.ts", 10, "Fix this", false)...),
			)

		mock.ExpectQuery("UPDATE session_review_comments").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(reviewCommentColumns).
					AddRow(reviewCommentRow(commentID, sessionID, orgID, userID, "src/app.ts", 10, "Updated text", false)...),
			)
		expectAuditInsert(mock)

		body := `{"body":"Updated text"}`
		url := fmt.Sprintf("/api/v1/sessions/%s/review-comments/%s", sessionID, commentID)
		req := httptest.NewRequest(http.MethodPatch, url, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		ctx := middleware.WithOrgID(req.Context(), orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
		req = req.WithContext(ctx)

		w := httptest.NewRecorder()
		withReviewCommentRoutes(handler).ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var resp models.SingleResponse[models.SessionReviewComment]
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Equal(t, "Updated text", resp.Data.Body)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("resolves comment with pass number", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()
		commentID := uuid.New()
		handler := newTestReviewCommentHandler(t, mock)

		// Ownership check
		mock.ExpectQuery("SELECT .+ FROM session_review_comments WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(reviewCommentColumns).
					AddRow(reviewCommentRow(commentID, sessionID, orgID, userID, "src/app.ts", 10, "Fix this", false)...),
			)

		// GetByID for session lookup (to get current_turn for resolved_by_pass)
		setupSessionMock(mock, orgID, sessionID, nil)

		now := time.Now()
		resolvedByPass := 1
		mock.ExpectQuery("UPDATE session_review_comments").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(reviewCommentColumns).
					AddRow(
						commentID, sessionID, orgID, userID, "src/app.ts",
						10, "new", "Fix this", true, &now, &resolvedByPass,
						1, now, now,
					),
			)

		body := `{"resolved":true}`
		url := fmt.Sprintf("/api/v1/sessions/%s/review-comments/%s", sessionID, commentID)
		req := httptest.NewRequest(http.MethodPatch, url, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		ctx := middleware.WithOrgID(req.Context(), orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
		req = req.WithContext(ctx)

		w := httptest.NewRecorder()
		withReviewCommentRoutes(handler).ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var resp models.SingleResponse[models.SessionReviewComment]
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.True(t, resp.Data.Resolved)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("rejects update by non-owner", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		ownerID := uuid.New()
		otherUserID := uuid.New()
		commentID := uuid.New()
		handler := newTestReviewCommentHandler(t, mock)

		// Ownership check: comment is owned by a different user
		mock.ExpectQuery("SELECT .+ FROM session_review_comments WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(reviewCommentColumns).
					AddRow(reviewCommentRow(commentID, sessionID, orgID, ownerID, "src/app.ts", 10, "Fix this", false)...),
			)

		body := `{"body":"Updated text"}`
		url := fmt.Sprintf("/api/v1/sessions/%s/review-comments/%s", sessionID, commentID)
		req := httptest.NewRequest(http.MethodPatch, url, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		ctx := middleware.WithOrgID(req.Context(), orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: otherUserID, OrgID: orgID})
		req = req.WithContext(ctx)

		w := httptest.NewRecorder()
		withReviewCommentRoutes(handler).ServeHTTP(w, req)

		require.Equal(t, http.StatusForbidden, w.Code)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns not found for missing comment", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()
		commentID := uuid.New()
		handler := newTestReviewCommentHandler(t, mock)

		// Ownership check: comment not found
		mock.ExpectQuery("SELECT .+ FROM session_review_comments WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(reviewCommentColumns))

		body := `{"body":"Updated text"}`
		url := fmt.Sprintf("/api/v1/sessions/%s/review-comments/%s", sessionID, commentID)
		req := httptest.NewRequest(http.MethodPatch, url, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		ctx := middleware.WithOrgID(req.Context(), orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
		req = req.WithContext(ctx)

		w := httptest.NewRecorder()
		withReviewCommentRoutes(handler).ServeHTTP(w, req)

		require.Equal(t, http.StatusNotFound, w.Code)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestSessionReviewCommentHandler_SendToAgent(t *testing.T) {
	t.Parallel()

	t.Run("formats open comments as message", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()
		handler := newTestReviewCommentHandler(t, mock)

		mock.ExpectQuery("SELECT .+ FROM session_review_comments WHERE session_id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(reviewCommentColumns).
					AddRow(reviewCommentRow(uuid.New(), sessionID, orgID, userID, "src/app.ts", 10, "Fix error handling", false)...).
					AddRow(reviewCommentRow(uuid.New(), sessionID, orgID, userID, "src/utils.ts", 5, "Already fixed", true)...),
			)

		url := fmt.Sprintf("/api/v1/sessions/%s/review-comments/send", sessionID)
		req := httptest.NewRequest(http.MethodPost, url, nil)
		ctx := middleware.WithOrgID(req.Context(), orgID)
		req = req.WithContext(ctx)

		w := httptest.NewRecorder()
		withReviewCommentRoutes(handler).ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)

		var resp models.SingleResponse[struct {
			Message string `json:"message"`
		}]
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Contains(t, resp.Data.Message, "Fix error handling")
		require.Contains(t, resp.Data.Message, "src/app.ts:10")
		require.Contains(t, resp.Data.Message, "Target line: `src/app.ts:10` (new side)")
		require.Contains(t, resp.Data.Message, "Requested change: \"Fix error handling\"")
		// Resolved comment should not be included
		require.NotContains(t, resp.Data.Message, "Already fixed")
	})

	t.Run("returns error when no open comments", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()
		handler := newTestReviewCommentHandler(t, mock)

		// All comments are resolved
		mock.ExpectQuery("SELECT .+ FROM session_review_comments WHERE session_id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(reviewCommentColumns).
					AddRow(reviewCommentRow(uuid.New(), sessionID, orgID, userID, "src/app.ts", 10, "Done", true)...),
			)

		url := fmt.Sprintf("/api/v1/sessions/%s/review-comments/send", sessionID)
		req := httptest.NewRequest(http.MethodPost, url, nil)
		ctx := middleware.WithOrgID(req.Context(), orgID)
		req = req.WithContext(ctx)

		w := httptest.NewRecorder()
		withReviewCommentRoutes(handler).ServeHTTP(w, req)

		require.Equal(t, http.StatusBadRequest, w.Code)
	})
}
