package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var srcColumns = []string{
	"id", "session_id", "org_id", "user_id", "file_path",
	"line_number", "diff_side", "body", "resolved", "resolved_at", "resolved_by_pass",
	"pass_number", "created_at", "updated_at",
}

func srcIntPtr(i int) *int { return &i }

func newSessionReviewCommentRow(id, sessionID, orgID, userID uuid.UUID, now time.Time) []any {
	return []any{
		id, sessionID, orgID, userID, "main.go",
		42, "right", "Please fix this", false, (*time.Time)(nil), (*int)(nil),
		1, now, now,
	}
}

func TestSessionReviewCommentStore_Create_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewSessionReviewCommentStore(mock)
	now := time.Now()

	c := &models.SessionReviewComment{
		SessionID:  uuid.New(),
		OrgID:      uuid.New(),
		UserID:     uuid.New(),
		FilePath:   "main.go",
		LineNumber: 42,
		DiffSide:   "right",
		Body:       "Please fix this",
		PassNumber: 1,
	}

	generatedID := uuid.New()
	mock.ExpectQuery("INSERT INTO session_review_comments").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(srcColumns).
				AddRow(generatedID, c.SessionID, c.OrgID, c.UserID, c.FilePath,
					c.LineNumber, c.DiffSide, c.Body, false, (*time.Time)(nil), (*int)(nil),
					c.PassNumber, now, now),
		)

	err = store.Create(context.Background(), c)
	require.NoError(t, err, "should create session review comment without error")
	require.Equal(t, generatedID, c.ID, "should set the generated ID on the comment")
	require.Equal(t, now, c.CreatedAt, "should set the created_at timestamp")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionReviewCommentStore_Create_Error(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewSessionReviewCommentStore(mock)

	mock.ExpectQuery("INSERT INTO session_review_comments").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("connection refused"))

	c := &models.SessionReviewComment{
		SessionID: uuid.New(), OrgID: uuid.New(), UserID: uuid.New(),
		FilePath: "main.go", LineNumber: 1, DiffSide: "right", Body: "test", PassNumber: 1,
	}
	err = store.Create(context.Background(), c)
	require.Error(t, err, "should return an error on db failure")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionReviewCommentStore_GetByID(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	id := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "returns comment when found",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM session_review_comments WHERE id .+ AND org_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(srcColumns).
							AddRow(newSessionReviewCommentRow(id, sessionID, orgID, userID, now)...),
					)
			},
			expectErr: false,
		},
		{
			name: "returns error when not found",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM session_review_comments WHERE id .+ AND org_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(srcColumns))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool without error")
			defer mock.Close()

			store := NewSessionReviewCommentStore(mock)
			tt.setupMock(mock)

			result, err := store.GetByID(context.Background(), orgID, id)
			if tt.expectErr {
				require.Error(t, err, "should return an error when comment not found")
			} else {
				require.NoError(t, err, "should retrieve comment by ID without error")
				require.Equal(t, id, result.ID, "should return the correct comment ID")
				require.Equal(t, orgID, result.OrgID, "should return the correct org ID")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionReviewCommentStore_ListBySession(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	id1 := uuid.New()
	id2 := uuid.New()
	now := time.Now()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expected  int
		expectErr bool
	}{
		{
			name: "returns comments for session",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM session_review_comments WHERE session_id .+ AND org_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(srcColumns).
							AddRow(newSessionReviewCommentRow(id1, sessionID, orgID, userID, now)...).
							AddRow(newSessionReviewCommentRow(id2, sessionID, orgID, userID, now)...),
					)
			},
			expected: 2,
		},
		{
			name: "returns empty list when no comments",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM session_review_comments WHERE session_id .+ AND org_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(srcColumns))
			},
			expected: 0,
		},
		{
			name: "returns error on db failure",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM session_review_comments WHERE session_id .+ AND org_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(fmt.Errorf("connection refused"))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool without error")
			defer mock.Close()

			store := NewSessionReviewCommentStore(mock)
			tt.setupMock(mock)

			comments, err := store.ListBySession(context.Background(), orgID, sessionID)
			if tt.expectErr {
				require.Error(t, err, "ListBySession should return an error")
				return
			}
			require.NoError(t, err, "ListBySession should not return an error")
			require.Len(t, comments, tt.expected, "should return expected number of comments")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionReviewCommentStore_Update(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	id := uuid.New()
	now := time.Now()

	bodyStr := "Updated body"
	resolvedTrue := true
	resolvedFalse := false
	resolvedByPass := 3

	tests := []struct {
		name           string
		body           *string
		resolved       *bool
		resolvedByPass *int
		setupMock      func(mock pgxmock.PgxPoolIface)
		expectErr      bool
	}{
		{
			name:     "update body only",
			body:     &bodyStr,
			resolved: nil,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("UPDATE session_review_comments SET .+body.+ WHERE id .+ AND org_id .+ AND session_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(srcColumns).
							AddRow(id, sessionID, orgID, userID, "main.go",
								42, "right", bodyStr, false, (*time.Time)(nil), (*int)(nil),
								1, now, now),
					)
			},
		},
		{
			name:     "update resolved only (resolve)",
			body:     nil,
			resolved: &resolvedTrue,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("UPDATE session_review_comments SET .+resolved.+ WHERE id .+ AND org_id .+ AND session_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(srcColumns).
							AddRow(id, sessionID, orgID, userID, "main.go",
								42, "right", "Please fix this", true, &now, (*int)(nil),
								1, now, now),
					)
			},
		},
		{
			name:     "update resolved only (unresolve)",
			body:     nil,
			resolved: &resolvedFalse,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("UPDATE session_review_comments SET .+resolved.+ WHERE id .+ AND org_id .+ AND session_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(srcColumns).
							AddRow(id, sessionID, orgID, userID, "main.go",
								42, "right", "Please fix this", false, (*time.Time)(nil), (*int)(nil),
								1, now, now),
					)
			},
		},
		{
			name:     "update both body and resolved",
			body:     &bodyStr,
			resolved: &resolvedTrue,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("UPDATE session_review_comments SET .+body.+resolved.+ WHERE id .+ AND org_id .+ AND session_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(srcColumns).
							AddRow(id, sessionID, orgID, userID, "main.go",
								42, "right", bodyStr, true, &now, (*int)(nil),
								1, now, now),
					)
			},
		},
		{
			name:           "update resolved with resolved_by_pass",
			body:           nil,
			resolved:       &resolvedTrue,
			resolvedByPass: &resolvedByPass,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("UPDATE session_review_comments SET .+resolved.+resolved_by_pass.+ WHERE id .+ AND org_id .+ AND session_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(srcColumns).
							AddRow(id, sessionID, orgID, userID, "main.go",
								42, "right", "Please fix this", true, &now, srcIntPtr(3),
								1, now, now),
					)
			},
		},
		{
			name:     "returns error on db failure",
			body:     &bodyStr,
			resolved: nil,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("UPDATE session_review_comments").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(fmt.Errorf("connection refused"))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool without error")
			defer mock.Close()

			store := NewSessionReviewCommentStore(mock)
			tt.setupMock(mock)

			result, err := store.Update(context.Background(), orgID, sessionID, id, tt.body, tt.resolved, tt.resolvedByPass)
			if tt.expectErr {
				require.Error(t, err, "Update should return an error")
				return
			}
			require.NoError(t, err, "Update should not return an error")
			require.Equal(t, id, result.ID, "should return the correct comment ID")
			require.Equal(t, orgID, result.OrgID, "should return the correct org ID")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionReviewCommentStore_Delete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "deletes comment successfully",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("DELETE FROM session_review_comments WHERE id .+ AND org_id .+ AND session_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("DELETE", 1))
			},
		},
		{
			name: "returns error when not found",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("DELETE FROM session_review_comments WHERE id .+ AND org_id .+ AND session_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("DELETE", 0))
			},
			expectErr: true,
		},
		{
			name: "returns error on db failure",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("DELETE FROM session_review_comments WHERE id .+ AND org_id .+ AND session_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(fmt.Errorf("connection refused"))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool without error")
			defer mock.Close()

			store := NewSessionReviewCommentStore(mock)
			tt.setupMock(mock)

			err = store.Delete(context.Background(), uuid.New(), uuid.New(), uuid.New())
			if tt.expectErr {
				require.Error(t, err, "Delete should return an error")
			} else {
				require.NoError(t, err, "Delete should not return an error")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

// TestSessionReviewCommentStore_MultiTenancy verifies that org_id filtering is applied
// to all store methods, ensuring data isolation between organizations.
func TestSessionReviewCommentStore_MultiTenancy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		execute   func(store *SessionReviewCommentStore, orgID uuid.UUID) error
	}{
		{
			name: "Create includes org_id",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("INSERT INTO session_review_comments .+ org_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(srcColumns).
							AddRow(uuid.New(), uuid.New(), uuid.New(), uuid.New(), "main.go",
								1, "right", "test", false, (*time.Time)(nil), (*int)(nil),
								1, time.Now(), time.Now()),
					)
			},
			execute: func(store *SessionReviewCommentStore, orgID uuid.UUID) error {
				c := &models.SessionReviewComment{
					SessionID: uuid.New(), OrgID: orgID, UserID: uuid.New(),
					FilePath: "main.go", LineNumber: 1, DiffSide: "right", Body: "test", PassNumber: 1,
				}
				return store.Create(context.Background(), c)
			},
		},
		{
			name: "GetByID filters by org_id",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM session_review_comments WHERE id .+ AND org_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(srcColumns))
			},
			execute: func(store *SessionReviewCommentStore, orgID uuid.UUID) error {
				_, _ = store.GetByID(context.Background(), orgID, uuid.New())
				return nil
			},
		},
		{
			name: "ListBySession filters by org_id",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM session_review_comments WHERE session_id .+ AND org_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(srcColumns))
			},
			execute: func(store *SessionReviewCommentStore, orgID uuid.UUID) error {
				_, err := store.ListBySession(context.Background(), orgID, uuid.New())
				return err
			},
		},
		{
			name: "Update filters by org_id",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("UPDATE session_review_comments SET .+ WHERE id .+ AND org_id .+ AND session_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(srcColumns))
			},
			execute: func(store *SessionReviewCommentStore, orgID uuid.UUID) error {
				_, _ = store.Update(context.Background(), orgID, uuid.New(), uuid.New(), nil, nil, nil)
				return nil
			},
		},
		{
			name: "Delete filters by org_id",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("DELETE FROM session_review_comments WHERE id .+ AND org_id .+ AND session_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("DELETE", 1))
			},
			execute: func(store *SessionReviewCommentStore, orgID uuid.UUID) error {
				return store.Delete(context.Background(), orgID, uuid.New(), uuid.New())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool without error")
			defer mock.Close()

			store := NewSessionReviewCommentStore(mock)
			tt.setupMock(mock)

			orgID := uuid.New()
			_ = tt.execute(store, orgID)
			require.NoError(t, mock.ExpectationsWereMet(), "query must include org_id filter for multi-tenancy isolation")
		})
	}
}
