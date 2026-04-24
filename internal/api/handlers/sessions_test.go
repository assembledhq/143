package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/api/sse"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type stubSessionPRCredentialStore struct {
	cred *models.DecryptedUserCredential
	err  error
}

type failingSSEWriter struct {
	header       http.Header
	failOnSubstr string
	failAfter    int
	writes       int
}

func (w *failingSSEWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *failingSSEWriter) WriteHeader(int) {}

func (w *failingSSEWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.failAfter > 0 && w.writes < w.failAfter {
		return len(p), nil
	}
	if w.failOnSubstr == "" || strings.Contains(string(p), w.failOnSubstr) {
		return 0, errors.New("sse write failed")
	}
	return len(p), nil
}

func (w *failingSSEWriter) Flush() {}

func (s *stubSessionPRCredentialStore) GetForUser(_ context.Context, _, _ uuid.UUID, _ models.ProviderName) (*models.DecryptedUserCredential, error) {
	return s.cred, s.err
}

type stubSessionPRAuthCredentialChecker struct {
	hasValidCredentialFunc func(context.Context, uuid.UUID, uuid.UUID) (bool, error)
}

func (s *stubSessionPRAuthCredentialChecker) HasValidCredential(ctx context.Context, orgID, userID uuid.UUID) (bool, error) {
	if s.hasValidCredentialFunc != nil {
		return s.hasValidCredentialFunc(ctx, orgID, userID)
	}
	return false, nil
}

func (s *stubSessionPRCredentialStore) Upsert(_ context.Context, _, _ uuid.UUID, _ models.ProviderConfig, _ bool) error {
	return nil
}

func (s *stubSessionPRCredentialStore) Disable(_ context.Context, _, _ uuid.UUID, _ models.ProviderName) error {
	return nil
}

type stubSessionPRTitleSyncer struct {
	called    bool
	lastTitle string
	err       error
}

func (s *stubSessionPRTitleSyncer) SyncSessionTitle(_ context.Context, session *models.Session) error {
	s.called = true
	if session.Title != nil {
		s.lastTitle = *session.Title
	}
	return s.err
}

type archiveTestSnapshotStore struct {
	deleted []string
	err     error
}

func (s *archiveTestSnapshotStore) Save(context.Context, string, io.Reader) error {
	return nil
}

func (s *archiveTestSnapshotStore) Load(context.Context, string, io.Writer) error {
	return nil
}

func (s *archiveTestSnapshotStore) Delete(_ context.Context, key string) error {
	s.deleted = append(s.deleted, key)
	return s.err
}

func newSessionHandler(t *testing.T, mock pgxmock.PgxPoolIface) *SessionHandler {
	t.Helper()
	return NewSessionHandler(
		db.NewSessionStore(mock),
		db.NewSessionLogStore(mock),
		db.NewSessionQuestionStore(mock),
		db.NewValidationStore(mock),
		db.NewPullRequestStore(mock),
		db.NewIssueStore(mock),
		db.NewRepositoryStore(mock),
		db.NewOrganizationStore(mock),
		db.NewJobStore(mock),
		db.NewSessionMessageStore(mock),
		db.NewSessionThreadStore(mock),
		nil, // llmClient — not needed in unit tests
		zerolog.Nop(),
	)
}

// sessionColumns is the standard column set for sessions queries.
// Must match sessionSelectColumns in session_store.go. Update all inline
// AddRow calls in this file when adding/removing/reordering columns.
var sessionColumns = []string{
	"id", "issue_id", "org_id", "origin", "interaction_mode", "validation_policy", "agent_type", "status", "autonomy_level", "token_mode",
	"complexity_tier", "confidence_score", "confidence_reasoning", "risk_factors",
	"container_id", "worker_node_id", "turn_holding_container", "started_at", "completed_at", "token_usage",
	"failure_explanation", "failure_category", "failure_next_steps", "failure_retry_advised",
	"parent_session_id", "revision_context", "error", "result_summary", "diff",
	"pm_plan_id", "title", "pm_approach", "pm_reasoning",
	"project_task_id", "model_override", "triggered_by_user_id",
	"agent_session_id", "current_turn", "last_activity_at", "sandbox_state", "snapshot_key",
	"target_branch", "working_branch", "base_commit_sha", "repository_id", "diff_stats", "diff_history", "input_manifest", "archived_at", "archived_by_user_id", "automation_run_id", "pr_creation_state", "pr_creation_error", "diff_collected_at", "latest_diff_snapshot_id", "deleted_at", "created_at",
}

func sessionTestRowWithPolicyDefaults(values []interface{}) []interface{} {
	row := make([]interface{}, 0, len(values)+3)
	row = append(row, values[:3]...)
	row = append(
		row,
		"",
		"",
		"",
	)
	row = append(row, values[3:]...)
	return row
}

func sessionTestRow(values ...interface{}) []interface{} {
	switch len(values) {
	case len(sessionColumns) - 3, len(sessionColumns) - 4, len(sessionColumns) - 6, len(sessionColumns) - 7:
		values = sessionTestRowWithPolicyDefaults(values)
	}

	switch len(values) {
	case len(sessionColumns):
		return values
	case len(sessionColumns) - 1:
		row := make([]interface{}, 0, len(sessionColumns))
		row = append(row, values[:15]...)
		row = append(row, nil) // worker_node_id
		row = append(row, values[15:]...)
		return row
	case len(sessionColumns) - 4:
		row := make([]interface{}, 0, len(sessionColumns))
		row = append(row, values[:15]...)
		row = append(row, nil) // worker_node_id
		row = append(row, values[15:42]...)
		row = append(row, nil) // base_commit_sha
		row = append(row, values[42:51]...)
		row = append(row, nil) // diff_collected_at
		row = append(row, nil) // latest_diff_snapshot_id
		row = append(row, values[51:]...)
		return row
	case len(sessionColumns) - 3:
		row := make([]interface{}, 0, len(sessionColumns))
		row = append(row, values[:15]...)
		row = append(row, nil) // worker_node_id
		row = append(row, values[15:43]...)
		row = append(row, nil) // base_commit_sha
		row = append(row, values[43:52]...)
		row = append(row, nil) // diff_collected_at
		row = append(row, nil) // latest_diff_snapshot_id
		row = append(row, values[52:]...)
		return row
	default:
		panic(fmt.Sprintf("sessionTestRow received %d values, want %d, %d, %d, or %d (plus legacy variants without session policy columns)", len(values), len(sessionColumns), len(sessionColumns)-1, len(sessionColumns)-3, len(sessionColumns)-4))
	}
}

func addSessionRow(rows *pgxmock.Rows, values ...interface{}) *pgxmock.Rows {
	return rows.AddRow(sessionTestRow(values...)...)
}

func sessionAnyArgs(count int) []interface{} {
	args := make([]interface{}, count)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}

func expectManualSessionCreate(mock pgxmock.PgxPoolIface, runID uuid.UUID, now time.Time) {
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO sessions").
		WithArgs(sessionAnyArgs(22)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "last_activity_at"}).AddRow(runID, now, now))
	mock.ExpectCommit()
}

func expectIssueSessionCreate(mock pgxmock.PgxPoolIface, runID uuid.UUID, now time.Time) {
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO sessions").
		WithArgs(sessionAnyArgs(22)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "last_activity_at"}).AddRow(runID, now, now))
	mock.ExpectExec("INSERT INTO session_issue_links").
		WithArgs(sessionAnyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
}

func TestSessionHandler_List(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID)
		expectedCode int
		expectedLen  int
	}{
		{
			name: "returns agent runs for org successfully",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				runID := uuid.New()
				issueID := uuid.New()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
							nil, nil, nil, nil,
							nil, false, &now, &now, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil,                      // project_task_id
							nil,                      // model_override
							nil,                      // triggered_by_user_id
							nil, 0, now, "none", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
							nil,      // target_branch
							nil,      // working_branch
							nil,      // repository_id
							nil,      // diff_stats
							nil,      // diff_history
							nil,      // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil,            // automation_run_id
							"idle",         // pr_creation_state
							(*string)(nil), // pr_creation_error
							nil,            // deleted_at
							now,
						),
					)
			},
			expectedCode: http.StatusOK,
			expectedLen:  1,
		},
		{
			name: "returns empty list when no runs exist",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
			},
			expectedCode: http.StatusOK,
			expectedLen:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			orgID := uuid.New()
			handler := newSessionHandler(t, mock)

			tt.setupMock(mock, orgID)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil)
			ctx := middleware.WithOrgID(req.Context(), orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.List(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")

			var resp models.ListResponse[models.Session]
			err = json.Unmarshal(w.Body.Bytes(), &resp)
			require.NoError(t, err, "response body should be valid JSON")
			require.Equal(t, tt.expectedLen, len(resp.Data), "should return expected number of runs")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandler_List_WithRepositoryID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	handler := newSessionHandler(t, mock)

	now := time.Now()
	runID := uuid.New()
	issueID := uuid.New()
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id .+ repository_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,                      // triggered_by_user_id
				nil, 0, now, "none", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions?repository_id="+repoID.String(), nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200 when filtering by repository_id")

	var resp models.ListResponse[models.Session]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, 1, len(resp.Data), "should return filtered sessions")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_List_InvalidRepositoryID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions?repository_id=not-a-uuid", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid repository_id")
	require.Contains(t, w.Body.String(), "INVALID_REPOSITORY_ID", "error code should indicate invalid repository_id")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_List_InvalidStatus(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions?status=bogus", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid status")
	require.Contains(t, w.Body.String(), "INVALID_STATUS", "error code should indicate invalid status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_List_CommaSeparatedStatuses(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	now := time.Now()
	runID := uuid.New()
	issueID := uuid.New()
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id .+ AND status = ANY").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, issueID, orgID, "claude-code", "pending", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil, // triggered_by_user_id
				nil, 0, now, "none", nil,
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions?status=pending,running", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200 for comma-separated statuses")

	var resp models.ListResponse[models.Session]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, 1, len(resp.Data), "should return filtered sessions")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionCursorRoundTrip(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Truncate(time.Nanosecond)
	id := uuid.New()

	encoded := encodeSessionCursor(now, id)
	decodedTime, decodedID, err := decodeSessionCursor(encoded)
	require.NoError(t, err)
	require.True(t, now.Equal(decodedTime), "decoded time should match")
	require.Equal(t, id, decodedID, "decoded ID should match")
}

func TestDecodeSessionCursor_Invalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		cursor string
	}{
		{name: "not base64", cursor: "!!!invalid!!!"},
		{name: "missing comma", cursor: "bm9jb21tYQ=="},                                                 // "nocomma"
		{name: "bad timestamp", cursor: "YmFkdGltZSwwMTIzNDU2Ny04OWFiLWNkZWYtMDEyMy00NTY3ODlhYmNkZWY="}, // "badtime,..."
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := decodeSessionCursor(tt.cursor)
			require.Error(t, err)
		})
	}
}

func TestSessionHandler_List_WithCursor(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	now := time.Now()
	runID := uuid.New()
	issueID := uuid.New()
	cursorTime := now.Add(-time.Hour)
	cursorID := uuid.New()
	cursor := encodeSessionCursor(cursorTime, cursorID)

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id .+ AND \\(last_activity_at, id\\) <").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil, nil,
				nil, 0, now, "none", nil,
				nil, nil, nil, nil, nil, nil, nil, nil, nil, "idle", (*string)(nil), nil, now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions?cursor="+cursor, nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200 with cursor")

	var resp models.ListResponse[models.Session]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, 1, len(resp.Data), "should return sessions after cursor")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// TestSessionHandler_List_EmitsCursorWhenFull exercises the nextCursor emission
// path: when the DB returns exactly `limit` rows, the handler must encode the
// last row's last_activity_at (the MRU sort key) into the returned cursor so
// callers can page from the correct position.
func TestSessionHandler_List_EmitsCursorWhenFull(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	now := time.Now().UTC().Truncate(time.Nanosecond)
	runID := uuid.New()
	issueID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil, nil,
				nil, 0, now, "none", nil,
				nil, nil, nil, nil, nil, nil, nil, nil, nil, "idle", (*string)(nil), nil, now,
			),
		)

	// Request exactly one row so len(runs) == limit and the cursor is emitted.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions?limit=1", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp models.ListResponse[models.SessionListItem]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.Equal(t, 1, len(resp.Data))
	require.NotEmpty(t, resp.Meta.NextCursor, "next cursor must be set when page is full")

	// Cursor must encode (last_activity_at, id) so pagination continues in MRU order.
	decodedTime, decodedID, err := decodeSessionCursor(resp.Meta.NextCursor)
	require.NoError(t, err)
	require.True(t, now.Equal(decodedTime), "cursor time must be last_activity_at of last row")
	require.Equal(t, runID, decodedID, "cursor id must be id of last row")
}

func TestSessionHandler_List_InvalidCursor(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions?cursor=invalid", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid cursor")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_Counts(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("(?s)SELECT.*all_count.*active_count.*archived_count").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"all_count", "active_count", "archived_count"}).
				AddRow(42, 7, 3),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/counts", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Counts(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200")

	var resp models.SingleResponse[models.SessionCounts]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, 42, resp.Data.All, "all count should pass through")
	require.Equal(t, 7, resp.Data.Active, "active count should pass through")
	require.Equal(t, 3, resp.Data.Archived, "archived count should pass through")
	require.Greater(t, resp.Data.Cap, 0, "cap should be populated so clients can render 99+ correctly")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_Counts_WithScopeFilters(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	userID := uuid.New()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("(?s)SELECT.*repository_id.*triggered_by_user_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"all_count", "active_count", "archived_count"}).
				AddRow(5, 2, 1),
		)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sessions/counts?repository_id="+repoID.String()+"&triggered_by_user_id="+userID.String(), nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Counts(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200 with scope filters")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_Counts_InvalidRepositoryID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/counts?repository_id=not-a-uuid", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Counts(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should reject invalid repository_id")
	require.Contains(t, w.Body.String(), "INVALID_REPOSITORY_ID")
}

func TestSessionHandler_Counts_InvalidUserID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/counts?triggered_by_user_id=bad", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Counts(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should reject invalid triggered_by_user_id")
	require.Contains(t, w.Body.String(), "INVALID_USER_ID")
}

func TestSessionHandler_Get(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		idParam      string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID)
		expectedCode int
		expectedBody string
	}{
		{
			name:    "returns agent run by ID successfully",
			idParam: "", // will be set to a valid UUID in the subtest
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				runID := uuid.New()
				issueID := uuid.New()
				mock.ExpectQuery("SELECT").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							runID, issueID, orgID, "claude-code", "running", "supervised", "standard",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil,                      // project_task_id
							nil,                      // model_override
							nil,                      // triggered_by_user_id
							nil, 0, now, "none", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
							nil,      // target_branch
							nil,      // working_branch
							nil,      // repository_id
							nil,      // diff_stats
							nil,      // diff_history
							nil,      // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil,            // automation_run_id
							"idle",         // pr_creation_state
							(*string)(nil), // pr_creation_error
							nil,            // deleted_at
							now,
						),
					)
			},
			expectedCode: http.StatusOK,
			expectedBody: "running",
		},
		{
			name:         "returns bad request for invalid UUID",
			idParam:      "invalid",
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			orgID := uuid.New()
			handler := newSessionHandler(t, mock)

			tt.setupMock(mock, orgID)

			idParam := tt.idParam
			if idParam == "" {
				idParam = uuid.New().String()
			}

			req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+idParam, nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", idParam)
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.Get(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")
			require.Contains(t, w.Body.String(), tt.expectedBody, "response body should contain expected content")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

// triggerFixIssueMock sets up the common mock for a successful issue lookup,
// agent run creation, and job enqueue for TriggerFix tests.
func triggerFixIssueMock(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
	now := time.Now()
	issueID := uuid.New()
	repoID := uuid.New()

	// Mock issue lookup
	mock.ExpectQuery("SELECT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "external_id", "source", "source_integration_id", "repository_id",
				"title", "description", "raw_data", "status", "first_seen_at", "last_seen_at",
				"occurrence_count", "affected_customer_count", "severity", "tags", "fingerprint",
				"created_at", "updated_at", "deleted_at",
			}).AddRow(
				issueID, orgID, "ISSUE-1", "sentry", nil, []byte(repoID.String()),
				"Test issue", nil, nil, "open", now, now,
				1, 0, "medium", nil, "fp-1",
				now, now, nil,
			),
		)

	runID := uuid.New()
	expectIssueSessionCreate(mock, runID, now)

	// Mock job enqueue (6 named args)
	jobID := uuid.New()
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
}

func triggerFixIssueAndOrgDefaultMock(mock pgxmock.PgxPoolIface, orgID uuid.UUID, defaultAgentType string) {
	issueID := uuid.New()
	now := time.Now()
	repoID := uuid.New()
	settings := fmt.Sprintf(`{"default_agent_type":"%s"}`, defaultAgentType)

	// Mock issue lookup
	mock.ExpectQuery("SELECT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "external_id", "source", "source_integration_id", "repository_id",
				"title", "description", "raw_data", "status", "first_seen_at", "last_seen_at",
				"occurrence_count", "affected_customer_count", "severity", "tags", "fingerprint",
				"created_at", "updated_at", "deleted_at",
			}).AddRow(
				issueID, orgID, "ISSUE-1", "sentry", nil, []byte(repoID.String()),
				"Test issue", nil, nil, "open", now, now,
				1, 0, "medium", nil, "fp-1",
				now, now, nil,
			),
		)

	// Mock org lookup for default agent type.
	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
				AddRow(orgID, "Acme", []byte(settings), now, now),
		)

	runID := uuid.New()
	expectIssueSessionCreate(mock, runID, now)

	// Mock job enqueue (6 named args)
	jobID := uuid.New()
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
}

func TestSessionHandler_TriggerFix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		idParam      string
		body         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID)
		expectedCode int
		expectedBody string
	}{
		{
			name:    "triggers fix with org default agent type when request omits agent_type",
			idParam: "",
			body:    "",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				triggerFixIssueAndOrgDefaultMock(mock, orgID, "gemini_cli")
			},
			expectedCode: http.StatusCreated,
			expectedBody: "gemini_cli",
		},
		{
			name:    "falls back to codex when org default agent type is missing",
			idParam: "",
			body:    "",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				triggerFixIssueAndOrgDefaultMock(mock, orgID, "")
			},
			expectedCode: http.StatusCreated,
			expectedBody: "codex",
		},
		{
			name:         "triggers fix with gemini_cli agent type",
			idParam:      "",
			body:         `{"agent_type":"gemini_cli"}`,
			setupMock:    triggerFixIssueMock,
			expectedCode: http.StatusCreated,
			expectedBody: "gemini_cli",
		},
		{
			name:         "triggers fix with codex agent type",
			idParam:      "",
			body:         `{"agent_type":"codex"}`,
			setupMock:    triggerFixIssueMock,
			expectedCode: http.StatusCreated,
			expectedBody: "codex",
		},
		{
			name:         "triggers fix with high token mode",
			idParam:      "",
			body:         `{"agent_type":"codex","token_mode":"high"}`,
			setupMock:    triggerFixIssueMock,
			expectedCode: http.StatusCreated,
			expectedBody: "high",
		},
		{
			name:    "rejects invalid agent type",
			idParam: "",
			body:    `{"agent_type":"invalid_agent"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				issueID := uuid.New()
				mock.ExpectQuery("SELECT").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{
							"id", "org_id", "external_id", "source", "source_integration_id", "repository_id",
							"title", "description", "raw_data", "status", "first_seen_at", "last_seen_at",
							"occurrence_count", "affected_customer_count", "severity", "tags", "fingerprint",
							"created_at", "updated_at", "deleted_at",
						}).AddRow(
							issueID, orgID, "ISSUE-1", "sentry", nil, nil,
							"Test issue", nil, nil, "open", now, now,
							1, 0, "medium", nil, "fp-1",
							now, now, nil,
						),
					)
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_AGENT_TYPE",
		},
		{
			name:    "rejects invalid token mode",
			idParam: "",
			body:    `{"agent_type":"codex","token_mode":"extreme"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				issueID := uuid.New()
				mock.ExpectQuery("SELECT").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{
							"id", "org_id", "external_id", "source", "source_integration_id", "repository_id",
							"title", "description", "raw_data", "status", "first_seen_at", "last_seen_at",
							"occurrence_count", "affected_customer_count", "severity", "tags", "fingerprint",
							"created_at", "updated_at", "deleted_at",
						}).AddRow(
							issueID, orgID, "ISSUE-1", "sentry", nil, nil,
							"Test issue", nil, nil, "open", now, now,
							1, 0, "medium", nil, "fp-1",
							now, now, nil,
						),
					)
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_TOKEN_MODE",
		},
		{
			name:         "returns bad request for invalid issue ID",
			idParam:      "bad-id",
			body:         "",
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			orgID := uuid.New()
			handler := newSessionHandler(t, mock)

			tt.setupMock(mock, orgID)

			idParam := tt.idParam
			if idParam == "" {
				idParam = uuid.New().String()
			}

			var bodyReader *strings.Reader
			if tt.body != "" {
				bodyReader = strings.NewReader(tt.body)
			} else {
				bodyReader = strings.NewReader("")
			}

			req := httptest.NewRequest(http.MethodPost, "/api/v1/issues/"+idParam+"/fix", bodyReader)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", idParam)
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.TriggerFix(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")
			require.Contains(t, w.Body.String(), tt.expectedBody, "response body should contain expected content")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandler_GetValidation_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	validationID := uuid.New()
	now := time.Now()

	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM validations WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "session_id", "org_id", "status",
				"direction_check", "correctness_check", "quality_check", "security_scan",
				"regression_test_check", "coverage_delta", "ci_check", "details",
				"started_at", "completed_at", "created_at",
			}).AddRow(
				validationID, runID, orgID, "passed",
				"pass", "pass", "pass", "pass",
				"skipped", nil, "pass", nil,
				&now, &now, now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+runID.String()+"/validation", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GetValidation(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200 OK for validation lookup")

	var resp models.SingleResponse[models.Validation]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, "passed", resp.Data.Status, "should return the validation with passed status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_ListQuestions_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	qID := uuid.New()
	now := time.Now()

	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM session_questions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "session_id", "org_id", "question_text", "options", "context",
				"blocks_phase", "answer_text", "answered_by", "answered_at", "status", "created_at",
			}).AddRow(
				qID, runID, orgID, "Which fix approach?", nil, nil,
				nil, nil, nil, nil, "pending", now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+runID.String()+"/questions", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ListQuestions(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200 OK for questions list")

	var resp models.ListResponse[models.SessionQuestion]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, 1, len(resp.Data), "should return one question for the run")
	require.Equal(t, "Which fix approach?", resp.Data[0].QuestionText, "should return the question text")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_AnswerQuestion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		body         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID, runID uuid.UUID, qID uuid.UUID, userID uuid.UUID)
		expectedCode int
		expectedBody string
	}{
		{
			name: "answers question successfully",
			body: `{"answer": "Option A"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID, runID uuid.UUID, qID uuid.UUID, userID uuid.UUID) {
				now := time.Now()

				// Mock answer update
				mock.ExpectExec("UPDATE session_questions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))

				// Mock get by ID after answer
				mock.ExpectQuery("SELECT .+ FROM session_questions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{
							"id", "session_id", "org_id", "question_text", "options", "context",
							"blocks_phase", "answer_text", "answered_by", "answered_at", "status", "created_at",
						}).AddRow(
							qID, runID, orgID, "Which fix?", nil, nil,
							nil, stringPtr("Option A"), &userID, &now, "answered", now,
						),
					)
			},
			expectedCode: http.StatusOK,
			expectedBody: "answered",
		},
		{
			name:         "returns bad request when answer is empty",
			body:         `{"answer": ""}`,
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID, runID uuid.UUID, qID uuid.UUID, userID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "MISSING_ANSWER",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			orgID := uuid.New()
			runID := uuid.New()
			qID := uuid.New()
			userID := uuid.New()

			handler := newSessionHandler(t, mock)
			tt.setupMock(mock, orgID, runID, qID, userID)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+runID.String()+"/questions/"+qID.String()+"/answer", strings.NewReader(tt.body))
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", runID.String())
			rctx.URLParams.Add("qid", qID.String())
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: "member"})
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.AnswerQuestion(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")
			require.Contains(t, w.Body.String(), tt.expectedBody, "response body should contain expected content")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandler_GetPullRequest_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	prID := uuid.New()
	now := time.Now()

	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "session_id", "org_id", "github_pr_number", "github_pr_url",
				"github_repo", "title", "body", "status", "review_status",
				"authored_by", "ci_status", "merged_at", "created_at", "updated_at",
			}).AddRow(
				prID, &runID, orgID, 42, "https://github.com/org/repo/pull/42",
				"org/repo", "Fix bug", nil, "open", "pending",
				"app", "", nil, now, now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+runID.String()+"/pull-request", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GetPullRequest(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200 OK")

	var resp models.SingleResponse[models.PullRequest]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, 42, resp.Data.GitHubPRNumber, "should return the PR number")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_GetPullRequest_NoPR_Returns200Null(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()

	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "session_id", "org_id", "github_pr_number", "github_pr_url",
			"github_repo", "title", "body", "status", "review_status",
			"authored_by", "ci_status", "merged_at", "created_at", "updated_at",
		}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+runID.String()+"/pull-request", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GetPullRequest(w, req)
	require.Equal(t, http.StatusOK, w.Code, "empty state should be 200, not 404")
	require.JSONEq(t, `{"data":null}`, w.Body.String(), "body should be data:null")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_GetPullRequest_DBError_Returns500(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()

	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("db exploded"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+runID.String()+"/pull-request", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GetPullRequest(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code, "real DB errors should 500, not 200")
	require.Contains(t, w.Body.String(), "INTERNAL_ERROR", "error code should surface")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_GetPullRequest_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/bad-id/pull-request", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bad-id")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GetPullRequest(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid ID")
	require.Contains(t, w.Body.String(), "INVALID_ID")
}

func TestSessionHandler_GetValidation_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/bad-id/validation", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bad-id")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GetValidation(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid ID")
	require.Contains(t, w.Body.String(), "INVALID_ID")
}

func TestSessionHandler_ListQuestions_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/bad-id/questions", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bad-id")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ListQuestions(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid ID")
	require.Contains(t, w.Body.String(), "INVALID_ID")
}

func TestSessionHandler_AnswerQuestion_InvalidQID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+runID.String()+"/questions/bad-id/answer", strings.NewReader(`{"answer":"yes"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	rctx.URLParams.Add("qid", "bad-id")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.AnswerQuestion(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid question ID")
	require.Contains(t, w.Body.String(), "INVALID_ID")
}

func TestSessionHandler_AnswerQuestion_NoUser(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	qID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+runID.String()+"/questions/"+qID.String()+"/answer", strings.NewReader(`{"answer":"yes"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	rctx.URLParams.Add("qid", qID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	// No user set in context
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.AnswerQuestion(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code, "should return 401 when no user in context")
	require.Contains(t, w.Body.String(), "UNAUTHORIZED")
}

func TestSessionHandler_TriggerFix_InvalidAutonomyLevel(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)

	now := time.Now()
	mock.ExpectQuery("SELECT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "external_id", "source", "source_integration_id", "repository_id",
				"title", "description", "raw_data", "status", "first_seen_at", "last_seen_at",
				"occurrence_count", "affected_customer_count", "severity", "tags", "fingerprint",
				"created_at", "updated_at", "deleted_at",
			}).AddRow(
				issueID, orgID, "ISSUE-1", "sentry", nil, nil,
				"Test issue", nil, nil, "open", now, now,
				1, 0, "medium", nil, "fp-1",
				now, now, nil,
			),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/issues/"+issueID.String()+"/fix", strings.NewReader(`{"agent_type":"codex","autonomy_level":"chaos"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", issueID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.TriggerFix(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid autonomy level")
	require.Contains(t, w.Body.String(), "INVALID_AUTONOMY_LEVEL")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_GetLogs_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	now := time.Now()

	handler := newSessionHandler(t, mock)

	// Mock session lookup.
	issueID := uuid.New()
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,                      // triggered_by_user_id
				nil, 0, now, "none", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	// Mock log listing.
	mock.ExpectQuery("SELECT .+ FROM session_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}).
				AddRow(int64(1), runID, orgID, nil, now, "info", "Starting agent", nil, nil).
				AddRow(int64(2), runID, orgID, nil, now, "info", "Agent completed", nil, nil),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+runID.String()+"/logs", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GetLogs(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp models.ListResponse[models.SessionLog]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.Equal(t, 2, len(resp.Data))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_GetLogs_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/bad-id/logs", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bad-id")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GetLogs(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_ID")
}

func TestSessionHandler_GetLogs_EmptyLogs(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	now := time.Now()

	handler := newSessionHandler(t, mock)

	issueID := uuid.New()
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,                      // triggered_by_user_id
				nil, 0, now, "none", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	mock.ExpectQuery("SELECT .+ FROM session_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+runID.String()+"/logs", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GetLogs(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp models.ListResponse[models.SessionLog]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.Equal(t, 0, len(resp.Data))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_GetTimeline_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(
				pgxmock.NewRows(sessionColumns),
				sessionID, uuid.New(), orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,                             // triggered_by_user_id
				nil, 1, now, "snapshotted", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)
	mock.ExpectQuery("SELECT .+ FROM session_messages WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(messageColumns).
				AddRow(int64(1), sessionID, orgID, nil, nil, 1, "assistant", "Done fixing", nil, nil, nil, now),
		)
	mock.ExpectQuery("SELECT .+ FROM session_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}).
				AddRow(int64(10), sessionID, orgID, nil, now.Add(-time.Minute), "output", "Done fixing", nil, 1),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sessionID.String()+"/timeline", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GetTimeline(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return expected status code")

	var resp models.ListResponse[models.SessionTimelineEntry]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Len(t, resp.Data, 1, "duplicate final output log should be suppressed from fetched timeline")
	require.Equal(t, models.SessionTimelineKindMessage, resp.Data[0].Kind, "assistant transcript should remain visible")
	require.NotNil(t, resp.Data[0].Message, "timeline message entry should include message payload")
	require.Equal(t, "Done fixing", resp.Data[0].Message.Content, "timeline should return the persisted assistant transcript")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_GetTimeline_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		sessionID    string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID)
		expectedCode int
	}{
		{
			name:         "invalid session id",
			sessionID:    "not-a-uuid",
			expectedCode: http.StatusBadRequest,
		},
		{
			name:      "session not found",
			sessionID: uuid.New().String(),
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(pgx.ErrNoRows)
			},
			expectedCode: http.StatusNotFound,
		},
		{
			name:      "message listing failure",
			sessionID: uuid.New().String(),
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(
							pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "completed", "supervised", "standard",
							nil, nil, nil, nil,
							nil, false, &now, &now, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 1, now, "snapshotted", nil,
							nil, nil, nil, nil, nil, nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectQuery("SELECT .+ FROM session_messages WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(errors.New("boom"))
			},
			expectedCode: http.StatusInternalServerError,
		},
		{
			name:      "log listing failure",
			sessionID: uuid.New().String(),
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(
							pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "completed", "supervised", "standard",
							nil, nil, nil, nil,
							nil, false, &now, &now, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 1, now, "snapshotted", nil,
							nil, nil, nil, nil, nil, nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectQuery("SELECT .+ FROM session_messages WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(messageColumns))
				mock.ExpectQuery("SELECT .+ FROM session_logs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(errors.New("boom"))
			},
			expectedCode: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			orgID := uuid.New()
			sessionUUID := uuid.New()
			if tt.sessionID != "not-a-uuid" {
				sessionUUID = uuid.MustParse(tt.sessionID)
			}
			handler := newSessionHandler(t, mock)
			if tt.setupMock != nil {
				tt.setupMock(mock, orgID, sessionUUID)
			}

			req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+tt.sessionID+"/timeline", nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", tt.sessionID)
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.GetTimeline(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "GetTimeline should return the expected status code")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandler_StreamLogs_TerminalRun(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	now := time.Now()
	issueID := uuid.New()

	handler := newSessionHandler(t, mock)

	// Mock session lookup — terminal status triggers GetLogs fallback.
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,                      // triggered_by_user_id
				nil, 0, now, "none", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	// GetLogs path: second session lookup + log listing.
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,                      // triggered_by_user_id
				nil, 0, now, "none", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	mock.ExpectQuery("SELECT .+ FROM session_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}).
				AddRow(int64(1), runID, orgID, nil, now, "info", "Done", nil, nil),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+runID.String()+"/logs/stream", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.StreamLogs(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_StreamLogs_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/bad-id/logs/stream", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bad-id")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.StreamLogs(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_ID")
}

// TestSessionHandler_StreamLogs_ShutdownSignal verifies that the SSE loop
// returns promptly when shutdownCh is closed, instead of blocking
// Server.Shutdown until its deadline expires.
func TestSessionHandler_StreamLogs_ShutdownSignal(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	now := time.Now()
	issueID := uuid.New()

	handler := newSessionHandler(t, mock)
	shutdownCh := make(chan struct{})
	handler.SetShutdownSignal(shutdownCh)

	// Non-terminal ("running") status triggers the SSE streaming path.
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, issueID, orgID, "claude-code", "running", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,                      // triggered_by_user_id
				nil, 0, now, "none", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	// Empty logs list so the initial write loop is a no-op.
	mock.ExpectQuery("SELECT .+ FROM session_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+runID.String()+"/logs/stream", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.StreamLogs(w, req)
	}()

	// Close the shutdown channel; whether the handler has reached its select
	// yet or not, the for-select will pick the shutdownCh case on its first
	// iteration and return.
	close(shutdownCh)

	select {
	case <-done:
		// Expected: handler exited within deadline.
	case <-time.After(2 * time.Second):
		t.Fatal("StreamLogs did not return within 2s of shutdownCh close")
	}

	// A heartbeat is written on the shutdown path so the browser sees a
	// flush before EOF; check for its SSE comment marker.
	require.Contains(t, w.Body.String(), ": ping", "expected heartbeat on shutdown")
	require.NoError(t, mock.ExpectationsWereMet())
}

func newSessionTestStreams(t *testing.T) (*cache.SessionStreams, *miniredis.Miniredis) {
	t.Helper()

	mr := miniredis.RunT(t)
	client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + mr.Addr()}, zerolog.Nop(), nil)
	require.NotNil(t, client, "Redis client should initialize for session handler tests")
	return cache.NewSessionStreams(client, zerolog.Nop(), nil), mr
}

func TestSessionHandler_CatchUpLogs_UsesRedisRangeAndFallbacks(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	handler := newSessionHandler(t, mock)
	streams, _ := newSessionTestStreams(t)
	handler.SetStreams(streams)

	sessionID := uuid.New()
	orgID := uuid.New()
	require.NoError(t, streams.PublishLog(context.Background(), &models.SessionLog{ID: 1, SessionID: sessionID, OrgID: orgID, Level: "info", Message: "one", Timestamp: time.Now()}), "first log publish should succeed")
	require.NoError(t, streams.PublishLog(context.Background(), &models.SessionLog{ID: 2, SessionID: sessionID, OrgID: orgID, Level: "info", Message: "two", Timestamp: time.Now()}), "second log publish should succeed")

	logs, err := handler.catchUpLogs(context.Background(), orgID, sessionID, cache.SessionLogStreamID(1))
	require.NoError(t, err, "Redis XRANGE catch-up should succeed")
	require.Len(t, logs, 1, "Redis XRANGE catch-up should only return newer logs")
	require.Equal(t, int64(2), logs[0].ID, "Redis XRANGE catch-up should return the later log")

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))

	logs, err = handler.catchUpLogs(context.Background(), orgID, sessionID, "bad-id")
	require.NoError(t, err, "invalid Last-Event-ID should fall back to a full Postgres replay")
	require.Empty(t, logs, "fallback full replay should return the mocked empty result set")
}

func TestShouldSkipRedisLog(t *testing.T) {
	t.Parallel()

	seen, skip := shouldSkipRedisLog(context.Background(), cache.SessionLogStreamID(3), cache.SessionLogStreamID(4), uuid.New())
	require.True(t, skip, "older log IDs should be skipped")
	require.Equal(t, cache.SessionLogStreamID(4), seen, "skip helper should preserve the last delivered ID")

	seen, skip = shouldSkipRedisLog(context.Background(), cache.SessionLogStreamID(5), cache.SessionLogStreamID(4), uuid.New())
	require.False(t, skip, "newer log IDs should not be skipped")
	require.Equal(t, "", seen, "non-skipped entries should not override the last delivered ID")

	seen, skip = shouldSkipRedisLog(context.Background(), "bad-stream-id", cache.SessionLogStreamID(4), uuid.New())
	require.False(t, skip, "invalid current stream IDs should not be skipped")
	require.Equal(t, "", seen, "invalid current stream IDs should not preserve the last delivered ID")

	seen, skip = shouldSkipRedisLog(context.Background(), cache.SessionLogStreamID(5), "bad-last-id", uuid.New())
	require.False(t, skip, "invalid last delivered stream IDs should not skip newer entries")
	require.Equal(t, "", seen, "invalid last delivered stream IDs should not be preserved")
}

func TestSessionHandler_StreamLogsViaRedis_FallsBackWhenRedisUnavailable(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	handler := newSessionHandler(t, mock)

	orgID := uuid.New()
	runID := uuid.New()
	run := models.Session{ID: runID, OrgID: orgID, IssueID: uuid.New(), Status: string(models.SessionStatusRunning)}

	rec := httptest.NewRecorder()
	sw := sse.NewWriter(rec)
	require.NotNil(t, sw, "SSE writer should initialize")

	require.False(t, handler.streamLogsViaRedis(context.Background(), sw, orgID, run, ""), "Redis stream helper should fall back when Redis subscriptions are unavailable")
	require.Empty(t, rec.Body.String(), "fallback path should not emit partial SSE output")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_StreamLogsViaRedis_ContextCanceledAfterSetup(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	handler := newSessionHandler(t, mock)
	streams, _ := newSessionTestStreams(t)
	handler.SetStreams(streams)

	orgID := uuid.New()
	runID := uuid.New()
	run := models.Session{ID: runID, OrgID: orgID, IssueID: uuid.New(), Status: string(models.SessionStatusRunning)}

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))

	rec := httptest.NewRecorder()
	sw := sse.NewWriter(rec)
	require.NotNil(t, sw, "SSE writer should initialize")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() {
		done <- handler.streamLogsViaRedis(ctx, sw, orgID, run, "")
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case ok := <-done:
		require.True(t, ok, "canceled contexts should exit the Redis stream helper cleanly after setup")
	case <-time.After(2 * time.Second):
		t.Fatal("Redis stream helper did not return after context cancellation")
	}
	require.Contains(t, rec.Body.String(), "event: status", "Redis stream helper should still emit the initial status event before honoring cancellation")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_StreamLogsViaRedis_ShutdownSignalAfterSetup(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	handler := newSessionHandler(t, mock)
	streams, _ := newSessionTestStreams(t)
	handler.SetStreams(streams)
	shutdownCh := make(chan struct{})
	handler.SetShutdownSignal(shutdownCh)

	orgID := uuid.New()
	runID := uuid.New()
	run := models.Session{ID: runID, OrgID: orgID, IssueID: uuid.New(), Status: string(models.SessionStatusRunning)}

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))

	rec := httptest.NewRecorder()
	sw := sse.NewWriter(rec)
	require.NotNil(t, sw, "SSE writer should initialize")
	close(shutdownCh)

	require.True(t, handler.streamLogsViaRedis(context.Background(), sw, orgID, run, ""), "shutdown signals should exit the Redis stream helper cleanly")
	require.Contains(t, rec.Body.String(), ": ping", "shutdown handling should emit a heartbeat before returning")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_StreamLogsViaRedis_ReplayAndStatusWriteFailures(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	handler := newSessionHandler(t, mock)
	streams, _ := newSessionTestStreams(t)
	handler.SetStreams(streams)

	orgID := uuid.New()
	runID := uuid.New()
	run := models.Session{ID: runID, OrgID: orgID, IssueID: uuid.New(), Status: string(models.SessionStatusRunning)}
	require.NoError(t, streams.PublishLog(context.Background(), &models.SessionLog{ID: 11, SessionID: runID, OrgID: orgID, Level: "info", Message: "hello", Timestamp: time.Now()}), "test should seed Redis replay logs")

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}).
				AddRow(int64(11), runID, orgID, nil, time.Now(), "info", "hello", nil, nil),
		)

	logFailWriter := &failingSSEWriter{}
	logFailSW := sse.NewWriter(logFailWriter)
	require.NotNil(t, logFailSW, "SSE writer should initialize")
	require.False(t, handler.streamLogsViaRedis(context.Background(), logFailSW, orgID, run, ""), "replay write failures should abort the Redis stream helper")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))

	statusFailSW := sse.NewWriter(&failingSSEWriter{failOnSubstr: "event: status"})
	require.NotNil(t, statusFailSW, "SSE writer should initialize")
	require.False(t, handler.streamLogsViaRedis(context.Background(), statusFailSW, orgID, run, ""), "initial status write failures should abort the Redis stream helper")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_StreamLogsViaRedis_SubscriptionClosureWritesRetryEvent(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	handler := newSessionHandler(t, mock)
	streams, mr := newSessionTestStreams(t)
	handler.SetStreams(streams)

	orgID := uuid.New()
	runID := uuid.New()
	run := models.Session{ID: runID, OrgID: orgID, IssueID: uuid.New(), Status: string(models.SessionStatusRunning)}

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))

	rec := httptest.NewRecorder()
	sw := sse.NewWriter(rec)
	require.NotNil(t, sw, "SSE writer should initialize")

	done := make(chan bool, 1)
	go func() {
		done <- handler.streamLogsViaRedis(context.Background(), sw, orgID, run, "")
	}()

	time.Sleep(20 * time.Millisecond)
	mr.Close()

	select {
	case ok := <-done:
		require.True(t, ok, "subscription closures should end the Redis stream helper cleanly")
	case <-time.After(2 * time.Second):
		t.Fatal("Redis stream helper did not finish after Redis subscription closure")
	}

	body := rec.Body.String()
	require.Contains(t, body, "event: error", "subscription closures should tell the client to reconnect")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_StreamLogsViaPolling_ReplaysAndFinishes(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	handler := newSessionHandler(t, mock)
	orgID := uuid.New()
	runID := uuid.New()
	issueID := uuid.New()
	now := time.Now()
	run := models.Session{ID: runID, OrgID: orgID, IssueID: issueID, Status: string(models.SessionStatusRunning)}

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, now, "none", nil,
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)
	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id .+ sl.id >").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}).
				AddRow(int64(9), runID, orgID, nil, now, "info", "done", nil, nil),
		)

	rec := httptest.NewRecorder()
	sw := sse.NewWriter(rec)
	require.NotNil(t, sw, "SSE writer should initialize")

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.streamLogsViaPolling(context.Background(), sw, orgID, run, "")
	}()

	select {
	case <-done:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("polling stream helper did not finish after terminal status")
	}

	body := rec.Body.String()
	require.Contains(t, body, `event: done`, "polling stream should emit a done event for terminal statuses")
	require.Contains(t, body, `id: 9-0`, "polling stream should emit incremental log events")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_StreamLogsViaPolling_InvalidLastEventIDFallsBackAndWriteFails(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	handler := newSessionHandler(t, mock)
	orgID := uuid.New()
	runID := uuid.New()
	run := models.Session{ID: runID, OrgID: orgID, IssueID: uuid.New(), Status: string(models.SessionStatusRunning)}
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}).
				AddRow(int64(3), runID, orgID, nil, now, "info", "hello", nil, nil),
		)

	sw := sse.NewWriter(&failingSSEWriter{})
	require.NotNil(t, sw, "SSE writer should initialize")

	handler.streamLogsViaPolling(context.Background(), sw, orgID, run, "bad-id")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_StreamLogsViaPolling_InitialLoadFailureReturns(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	handler := newSessionHandler(t, mock)
	orgID := uuid.New()
	runID := uuid.New()
	run := models.Session{ID: runID, OrgID: orgID, IssueID: uuid.New(), Status: string(models.SessionStatusRunning)}

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id .+ sl.id >").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), int64(2)).
		WillReturnError(context.DeadlineExceeded)

	rec := httptest.NewRecorder()
	sw := sse.NewWriter(rec)
	require.NotNil(t, sw, "SSE writer should initialize")

	handler.streamLogsViaPolling(context.Background(), sw, orgID, run, cache.SessionLogStreamID(2))
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_StreamLogsViaPolling_ReloadFailureReturns(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	handler := newSessionHandler(t, mock)
	orgID := uuid.New()
	runID := uuid.New()
	run := models.Session{ID: runID, OrgID: orgID, IssueID: uuid.New(), Status: string(models.SessionStatusRunning)}

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(context.DeadlineExceeded)

	rec := httptest.NewRecorder()
	sw := sse.NewWriter(rec)
	require.NotNil(t, sw, "SSE writer should initialize")

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.streamLogsViaPolling(context.Background(), sw, orgID, run, "")
	}()

	select {
	case <-done:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("polling stream helper did not return after reload failure")
	}
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_StreamLogsViaPolling_StatusAndDoneWriteFailures(t *testing.T) {
	t.Parallel()

	t.Run("initial status write failure returns immediately", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool without error")
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		orgID := uuid.New()
		runID := uuid.New()
		run := models.Session{ID: runID, OrgID: orgID, IssueID: uuid.New(), Status: string(models.SessionStatusRunning)}

		mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))

		sw := sse.NewWriter(&failingSSEWriter{failOnSubstr: "event: status"})
		require.NotNil(t, sw, "SSE writer should initialize")

		handler.streamLogsViaPolling(context.Background(), sw, orgID, run, "")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("shutdown heartbeat failure still returns", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool without error")
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		shutdownCh := make(chan struct{})
		handler.SetShutdownSignal(shutdownCh)

		orgID := uuid.New()
		runID := uuid.New()
		run := models.Session{ID: runID, OrgID: orgID, IssueID: uuid.New(), Status: string(models.SessionStatusRunning)}

		mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))

		close(shutdownCh)
		sw := sse.NewWriter(&failingSSEWriter{failOnSubstr: ": ping", failAfter: 2})
		require.NotNil(t, sw, "SSE writer should initialize")

		handler.streamLogsViaPolling(context.Background(), sw, orgID, run, "")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("status and done write failures during reload return", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name         string
			status       string
			failOnSubstr string
			failAfter    int
		}{
			{name: "status event failure", status: string(models.SessionStatusCompleted), failOnSubstr: "event: status", failAfter: 2},
			{name: "done event failure", status: string(models.SessionStatusCompleted), failOnSubstr: "event: done", failAfter: 3},
		}

		for _, tt := range tests {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				mock, err := pgxmock.NewPool()
				require.NoError(t, err, "should create mock pool without error")
				defer mock.Close()

				handler := newSessionHandler(t, mock)
				orgID := uuid.New()
				runID := uuid.New()
				issueID := uuid.New()
				now := time.Now()
				run := models.Session{ID: runID, OrgID: orgID, IssueID: issueID, Status: string(models.SessionStatusRunning)}

				mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							runID, issueID, orgID, "claude-code", tt.status, "supervised", "standard",
							nil, nil, nil, nil,
							nil, false, &now, &now, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 0, now, "none", nil,
							nil, nil, nil, nil, nil, nil, nil, nil, nil,
							"idle", (*string)(nil), nil, now,
						),
					)
				mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id .+ sl.id >").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))

				sw := sse.NewWriter(&failingSSEWriter{failOnSubstr: tt.failOnSubstr, failAfter: tt.failAfter})
				require.NotNil(t, sw, "SSE writer should initialize")

				done := make(chan struct{})
				go func() {
					defer close(done)
					handler.streamLogsViaPolling(context.Background(), sw, orgID, run, "")
				}()

				select {
				case <-done:
				case <-time.After(1500 * time.Millisecond):
					t.Fatal("polling stream helper did not return after a status/done write failure")
				}

				require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
			})
		}
	})
}

func TestSessionHandler_StreamLogs_RedisFallbackToPolling(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	issueID := uuid.New()
	now := time.Now()
	handler := newSessionHandler(t, mock)
	streams, _ := newSessionTestStreams(t)
	handler.SetStreams(streams)
	handler.SetShutdownSignal(make(chan struct{}))

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, issueID, orgID, "claude-code", "running", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, now, "none", nil,
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)
	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(context.DeadlineExceeded)
	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+runID.String()+"/logs/stream", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.StreamLogs(w, req)
	}()

	select {
	case <-done:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("StreamLogs did not finish after falling back to polling")
	}
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreateManual(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		body         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID)
		expectedCode int
		expectedBody string
	}{
		{
			name: "creates manual session successfully",
			body: `{"message":"Fix the login bug","agent_type":"claude_code","references":[{"kind":"file","token":"@internal/api/handlers/sessions.go","path":"internal/api/handlers/sessions.go","display":"internal/api/handlers/sessions.go"}]}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				runID := uuid.New()
				messageID := int64(1)
				jobID := uuid.New()

				// Mock org settings lookup
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
						AddRow(orgID, "test-org", nil, now, now))

				expectManualSessionCreate(mock, runID, now)

				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(messageID, now))

				// Mock concurrency check
				mock.ExpectQuery("SELECT count").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

				// Mock job enqueue (6 named args)
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
			},
			expectedCode: http.StatusCreated,
			expectedBody: "claude_code",
		},
		{
			name:         "returns bad request for invalid reference kind",
			body:         `{"message":"Fix bug","references":[{"kind":"unknown","display":"Unknown"}]}`,
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_REFERENCES",
		},
		{
			name: "uses org default agent type when not specified",
			body: `{"message":"Fix the login bug"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				runID := uuid.New()
				messageID := int64(1)
				jobID := uuid.New()

				// Mock org lookup for default agent type.
				mock.ExpectQuery("SELECT .+ FROM organizations").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
							AddRow(orgID, "Acme", []byte(`{"default_agent_type":"gemini_cli"}`), now, now),
					)

				expectManualSessionCreate(mock, runID, now)

				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(messageID, now))

				// Mock concurrency check
				mock.ExpectQuery("SELECT count").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

				// Mock job enqueue
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
			},
			expectedCode: http.StatusCreated,
			expectedBody: "gemini_cli",
		},
		{
			name:         "returns bad request for empty message",
			body:         `{"message":"  ","agent_type":"claude_code"}`,
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "MISSING_MESSAGE",
		},
		{
			name:         "returns bad request for invalid body",
			body:         `{invalid`,
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_BODY",
		},
		{
			name: "returns bad request for invalid agent type",
			body: `{"message":"Fix bug","agent_type":"invalid"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
						AddRow(orgID, "test-org", nil, now, now))
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_AGENT_TYPE",
		},
		{
			name: "returns bad request for invalid autonomy level",
			body: `{"message":"Fix bug","agent_type":"claude_code","autonomy_level":"chaos"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
						AddRow(orgID, "test-org", nil, now, now))
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_AUTONOMY_LEVEL",
		},
		{
			name: "returns bad request for invalid token mode",
			body: `{"message":"Fix bug","agent_type":"claude_code","token_mode":"extreme"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
						AddRow(orgID, "test-org", nil, now, now))
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_TOKEN_MODE",
		},
		{
			name:         "returns bad request for invalid branch characters",
			body:         `{"message":"Fix bug","agent_type":"claude_code","branch":"main..exploit"}`,
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_BRANCH",
		},
		{
			name:         "returns bad request for invalid repository_id format",
			body:         `{"message":"Fix bug","agent_type":"claude_code","repository_id":"not-a-uuid"}`,
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_REPOSITORY_ID",
		},
		{
			name: "returns not found for non-existent repository",
			body: `{"message":"Fix bug","agent_type":"claude_code","repository_id":"` + uuid.New().String() + `"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				mock.ExpectQuery("SELECT").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{
						"id", "org_id", "platform", "platform_id", "full_name",
						"default_branch", "installed_at", "created_at", "updated_at",
					}))
			},
			expectedCode: http.StatusNotFound,
			expectedBody: "REPOSITORY_NOT_FOUND",
		},
		{
			name: "rejects creation against a disconnected repository",
			body: `{"message":"Fix bug","agent_type":"claude_code","repository_id":"` + uuid.New().String() + `"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				cols := []string{
					"id", "org_id", "integration_id", "github_id", "full_name", "default_branch",
					"private", "language", "description", "clone_url", "installation_id", "status",
					"last_synced_at", "context_quality", "settings", "created_at", "updated_at",
				}
				mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(cols).AddRow(
						uuid.New(), orgID, uuid.New(), int64(1), "org/repo", "main",
						false, nil, nil, "https://github.com/org/repo.git", int64(1),
						"disconnected", nil, nil, json.RawMessage(`{}`), now, now,
					))
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "REPO_DISCONNECTED",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			orgID := uuid.New()
			handler := newSessionHandler(t, mock)

			tt.setupMock(mock, orgID)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/manual", strings.NewReader(tt.body))
			ctx := middleware.WithOrgID(req.Context(), orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.CreateManual(w, req)
			require.Equal(t, tt.expectedCode, w.Code)
			require.Contains(t, w.Body.String(), tt.expectedBody)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestSessionHandler_EndSession_EnqueuesValidation(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	jobID := uuid.New()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "idle", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil, // triggered_by_user_id
				nil, 1, now, "snapshotted", stringPtr("snapshots/test.tar"),
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)
	mock.ExpectQuery("UPDATE sessions SET status = @status, completed_at = now\\(\\), last_activity_at = now\\(\\) WHERE id = @id AND org_id = @org_id .+ RETURNING").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 1, now, "snapshotted", stringPtr("snapshots/test.tar"),
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/end", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.EndSession(w, req)

	require.Equal(t, http.StatusOK, w.Code, "ending an idle non-manual session should enqueue validation")
	require.Contains(t, w.Body.String(), `"status":"completed"`, "response should return the completed session")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_EndSession_ManualSkipsValidation(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	userID := uuid.New()
	jobID := uuid.New()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "idle", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				&userID, // triggered_by_user_id
				nil, 1, now, "snapshotted", stringPtr("snapshots/test.tar"),
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)
	mock.ExpectQuery("UPDATE sessions SET status = @status, completed_at = now\\(\\), last_activity_at = now\\(\\) WHERE id = @id AND org_id = @org_id .+ RETURNING").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				&userID,
				nil, 1, now, "snapshotted", stringPtr("snapshots/test.tar"),
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)
	// Expect open_pr job instead of validate.
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
	mock.ExpectExec("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/end", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.EndSession(w, req)

	require.Equal(t, http.StatusOK, w.Code, "ending a manual session should skip validation and enqueue open_pr")
	require.Contains(t, w.Body.String(), `"status":"completed"`, "response should return the completed session")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBuildManualSessionDescription(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		message  string
		images   []string
		expected string
	}{
		{
			name:     "message only",
			message:  "Fix the bug",
			images:   nil,
			expected: "Fix the bug",
		},
		{
			name:     "message with images",
			message:  "Fix the bug",
			images:   []string{"https://example.com/img1.png", "https://example.com/img2.png"},
			expected: "Fix the bug\n\n### Attached images\n- https://example.com/img1.png\n- https://example.com/img2.png",
		},
		{
			name:     "empty images slice",
			message:  "Fix the bug",
			images:   []string{},
			expected: "Fix the bug",
		},
		{
			name:     "blank image URLs filtered",
			message:  "Fix the bug",
			images:   []string{"  ", "https://example.com/img.png"},
			expected: "Fix the bug\n\n### Attached images\n- https://example.com/img.png",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := buildManualSessionDescription(tt.message, tt.images)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestManualSessionTitle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		message  string
		expected string
	}{
		{
			name:     "short message",
			message:  "Fix the login bug",
			expected: "Fix the login bug",
		},
		{
			name:     "empty message",
			message:  "",
			expected: "Manual Session",
		},
		{
			name:     "whitespace only",
			message:  "   ",
			expected: "Manual Session",
		},
		{
			name:     "multiline uses first line",
			message:  "Fix the login bug\nMore details here",
			expected: "Fix the login bug",
		},
		{
			name:     "long message truncated",
			message:  strings.Repeat("a", 200),
			expected: strings.Repeat("a", 120) + "...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := manualSessionTitle(tt.message)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestIsValidGitRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		ref   string
		valid bool
	}{
		{"main", true},
		{"feature/add-auth", true},
		{"fix-123", true},
		{"refs/heads/main", true},
		{"", false},
		{"main..develop", false},
		{"branch~1", false},
		{"branch^2", false},
		{"branch:file", false},
		{"branch name", false},
		{"branch\\path", false},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.valid, isValidGitRef(tt.ref))
		})
	}
}

// messageColumns is the standard column set for session_messages queries.
var messageColumns = []string{
	"id", "session_id", "org_id", "thread_id", "user_id", "turn_number", "role", "content", "attachments", "references", "token_usage", "created_at",
}

func TestSessionHandler_ListMessages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID)
		expectedCode int
		expectedLen  int
	}{
		{
			name: "returns messages for session",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				now := time.Now()
				// Session lookup.
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "idle", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 1, now, "snapshotted", nil,
							nil,      // target_branch
							nil,      // working_branch
							nil,      // repository_id
							nil,      // diff_stats
							nil,      // diff_history
							nil,      // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil,            // automation_run_id
							"idle",         // pr_creation_state
							(*string)(nil), // pr_creation_error
							nil,            // deleted_at
							now,
						),
					)
				// Messages query.
				userID := uuid.New()
				mock.ExpectQuery("SELECT .+ FROM session_messages WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(messageColumns).
							AddRow(int64(1), sessionID, orgID, nil, &userID, 1, "user", "Hello", nil, nil, nil, now).
							AddRow(int64(2), sessionID, orgID, nil, nil, 1, "assistant", "Hi there", nil, nil, nil, now),
					)
			},
			expectedCode: http.StatusOK,
			expectedLen:  2,
		},
		{
			name: "returns empty list for session with no messages",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "completed", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, &now, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 0, now, "none", nil,
							nil,      // target_branch
							nil,      // working_branch
							nil,      // repository_id
							nil,      // diff_stats
							nil,      // diff_history
							nil,      // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil,            // automation_run_id
							"idle",         // pr_creation_state
							(*string)(nil), // pr_creation_error
							nil,            // deleted_at
							now,
						),
					)
				mock.ExpectQuery("SELECT .+ FROM session_messages WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(messageColumns))
			},
			expectedCode: http.StatusOK,
			expectedLen:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			orgID := uuid.New()
			sessionID := uuid.New()
			handler := newSessionHandler(t, mock)

			tt.setupMock(mock, orgID, sessionID)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sessionID.String()+"/messages", nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", sessionID.String())
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.ListMessages(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")

			var resp models.ListResponse[models.SessionMessage]
			err = json.Unmarshal(w.Body.Bytes(), &resp)
			require.NoError(t, err, "response body should be valid JSON")
			require.Equal(t, tt.expectedLen, len(resp.Data), "should return expected number of messages")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandler_SendMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		body         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID)
		expectedCode int
		expectedBody string
	}{
		{
			name: "sends message and enqueues continue_session job",
			body: `{"message":"Please add tests"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				// GetByID — session is idle.
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "idle", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 1, now, "snapshotted", stringPtr("snapshots/test"),
							nil,      // target_branch
							nil,      // working_branch
							nil,      // repository_id
							nil,      // diff_stats
							nil,      // diff_history
							nil,      // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil,            // automation_run_id
							"idle",         // pr_creation_state
							(*string)(nil), // pr_creation_error
							nil,            // deleted_at
							now,
						),
					)
				mock.ExpectBegin()
				// ClaimIdle succeeds.
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 1, now, "snapshotted", stringPtr("snapshots/test"),
							nil,      // target_branch
							nil,      // working_branch
							nil,      // repository_id
							nil,      // diff_stats
							nil,      // diff_history
							nil,      // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil,            // automation_run_id
							"idle",         // pr_creation_state
							(*string)(nil), // pr_creation_error
							nil,            // deleted_at
							now,
						),
					)
				// Create message.
				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
				// Enqueue job.
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
				mock.ExpectCommit()
			},
			expectedCode: http.StatusCreated,
			expectedBody: "Please add tests",
		},
		{
			name: "sends message to running session without enqueuing job",
			body: `{"message":"Quick note while you work"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				// GetByID — session is running.
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 2, now, "running", nil,
							nil,      // target_branch
							nil,      // working_branch
							nil,      // repository_id
							nil,      // diff_stats
							nil,      // diff_history
							nil,      // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil,            // automation_run_id
							"idle",         // pr_creation_state
							(*string)(nil), // pr_creation_error
							nil,            // deleted_at
							now,
						),
					)
				// Create message — no ClaimIdle, no ClaimForResume, no Enqueue.
				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
			},
			expectedCode: http.StatusCreated,
			expectedBody: "Quick note while you work",
		},
		{
			name: "rejects empty message",
			body: `{"message":""}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "MISSING_MESSAGE",
		},
		{
			name: "rejects when session is not idle or resumable",
			body: `{"message":"More work"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				// GetByID — session is pending (not running, not idle, not terminal).
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "pending", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 0, now, "none", nil,
							nil,      // target_branch
							nil,      // working_branch
							nil,      // repository_id
							nil,      // diff_stats
							nil,      // diff_history
							nil,      // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil,            // automation_run_id
							"idle",         // pr_creation_state
							(*string)(nil), // pr_creation_error
							nil,            // deleted_at
							now,
						),
					)
				mock.ExpectBegin()
				// ClaimIdle fails (no row returned).
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
				// ClaimForResume also fails (no row returned).
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
				mock.ExpectRollback()
			},
			expectedCode: http.StatusConflict,
			expectedBody: "NOT_RESUMABLE",
		},
		{
			name: "returns error when transaction begin fails",
			body: `{"message":"Please continue"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "idle", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 1, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectBegin().WillReturnError(fmt.Errorf("cannot begin tx"))
			},
			expectedCode: http.StatusInternalServerError,
			expectedBody: "TX_BEGIN_FAILED",
		},
		{
			name: "logs rollback error when transaction rollback fails",
			body: `{"message":"More work"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "pending", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 0, now, "none", nil,
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectBegin()
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
				mock.ExpectRollback().WillReturnError(fmt.Errorf("rollback failed"))
			},
			expectedCode: http.StatusConflict,
			expectedBody: "NOT_RESUMABLE",
		},
		{
			name: "rejects message to completed session with destroyed sandbox snapshot",
			body: `{"message":"Continue please"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "completed", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 3, now, "destroyed", nil,
							nil, nil, nil, nil, nil,
							nil,      // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil,            // automation_run_id
							"idle",         // pr_creation_state
							(*string)(nil), // pr_creation_error
							nil,            // deleted_at
							now,
						),
					)
			},
			expectedCode: http.StatusGone,
			expectedBody: "SNAPSHOT_EXPIRED",
		},
		{
			name: "rejects message to idle session with destroyed sandbox snapshot",
			body: `{"message":"Continue please"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "idle", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 2, now, "destroyed", nil,
							nil, nil, nil, nil, nil,
							nil,      // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil,            // automation_run_id
							"idle",         // pr_creation_state
							(*string)(nil), // pr_creation_error
							nil,            // deleted_at
							now,
						),
					)
			},
			expectedCode: http.StatusGone,
			expectedBody: "SNAPSHOT_EXPIRED",
		},
		{
			name: "sends message to completed session via ClaimForResume",
			body: `{"message":"Continue working on this"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				// GetByID — session is completed.
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "completed", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 1, now, "snapshotted", stringPtr("snapshots/test"),
							nil,      // target_branch
							nil,      // working_branch
							nil,      // repository_id
							nil,      // diff_stats
							nil,      // diff_history
							nil,      // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil,            // automation_run_id
							"idle",         // pr_creation_state
							(*string)(nil), // pr_creation_error
							nil,            // deleted_at
							now,
						),
					)
				mock.ExpectBegin()
				// ClaimIdle fails (no row returned).
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
				// ClaimForResume succeeds.
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 1, now, "snapshotted", stringPtr("snapshots/test"),
							nil,      // target_branch
							nil,      // working_branch
							nil,      // repository_id
							nil,      // diff_stats
							nil,      // diff_history
							nil,      // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil,            // automation_run_id
							"idle",         // pr_creation_state
							(*string)(nil), // pr_creation_error
							nil,            // deleted_at
							now,
						),
					)
				// Create message.
				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
				// Enqueue job.
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
				mock.ExpectCommit()
			},
			expectedCode: http.StatusCreated,
			expectedBody: "Continue working on this",
		},
		{
			name: "returns error when message creation fails in transaction",
			body: `{"message":"Please continue"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "idle", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 1, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectBegin()
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 1, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(fmt.Errorf("insert failed"))
				mock.ExpectRollback()
			},
			expectedCode: http.StatusInternalServerError,
			expectedBody: "CREATE_FAILED",
		},
		{
			name: "sends message to awaiting input session via ClaimForResume",
			body: `{"message":"Here is the clarification"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				answer := "Here is the clarification"
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "awaiting_input", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 2, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectBegin()
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
				mock.ExpectQuery("UPDATE sessions\\s+SET status = 'running', completed_at = NULL, last_activity_at = now\\(\\)\\s+WHERE id = @id AND org_id = @org_id AND status IN \\('completed', 'pr_created', 'failed', 'cancelled', 'awaiting_input', 'needs_human_guidance'\\)\\s+AND sandbox_state != 'destroyed'\\s+RETURNING").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 2, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
				mock.ExpectQuery("UPDATE session_questions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{
							"id", "session_id", "org_id", "question_text", "options", "context",
							"blocks_phase", "answer_text", "answered_by", "answered_at", "status", "created_at",
						}).AddRow(
							uuid.New(), sessionID, orgID, "Which fix?", nil, nil,
							stringPtr("implementation"), &answer, &userID, &now, "answered", now,
						),
					)
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
				mock.ExpectCommit()
				mock.ExpectQuery("INSERT INTO audit_logs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
			},
			expectedCode: http.StatusCreated,
			expectedBody: "Here is the clarification",
		},
		{
			name: "returns error when awaiting input answer update fails",
			body: `{"message":"Here is the clarification"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "awaiting_input", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 2, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectBegin()
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
				mock.ExpectQuery("UPDATE sessions\\s+SET status = 'running', completed_at = NULL, last_activity_at = now\\(\\)\\s+WHERE id = @id AND org_id = @org_id AND status IN \\('completed', 'pr_created', 'failed', 'cancelled', 'awaiting_input', 'needs_human_guidance'\\)\\s+AND sandbox_state != 'destroyed'\\s+RETURNING").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 2, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
				mock.ExpectQuery("UPDATE session_questions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(fmt.Errorf("update failed"))
				mock.ExpectRollback()
			},
			expectedCode: http.StatusInternalServerError,
			expectedBody: "ANSWER_FAILED",
		},
		{
			name: "sends message to needs human guidance session via ClaimForResume",
			body: `{"message":"Please refine the fix"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "needs_human_guidance", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 2, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectBegin()
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
				mock.ExpectQuery("UPDATE sessions\\s+SET status = 'running', completed_at = NULL, last_activity_at = now\\(\\)\\s+WHERE id = @id AND org_id = @org_id AND status IN \\('completed', 'pr_created', 'failed', 'cancelled', 'awaiting_input', 'needs_human_guidance'\\)\\s+AND sandbox_state != 'destroyed'\\s+RETURNING").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 2, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
				mock.ExpectCommit()
			},
			expectedCode: http.StatusCreated,
			expectedBody: "Please refine the fix",
		},
		{
			name: "sends message to awaiting input session without pending question",
			body: `{"message":"Continuing without a stored question"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "awaiting_input", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 2, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectBegin()
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
				mock.ExpectQuery("UPDATE sessions\\s+SET status = 'running', completed_at = NULL, last_activity_at = now\\(\\)\\s+WHERE id = @id AND org_id = @org_id AND status IN \\('completed', 'pr_created', 'failed', 'cancelled', 'awaiting_input', 'needs_human_guidance'\\)\\s+AND sandbox_state != 'destroyed'\\s+RETURNING").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 2, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
				mock.ExpectQuery("UPDATE session_questions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{
						"id", "session_id", "org_id", "question_text", "options", "context",
						"blocks_phase", "answer_text", "answered_by", "answered_at", "status", "created_at",
					}))
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
				mock.ExpectCommit()
			},
			expectedCode: http.StatusCreated,
			expectedBody: "Continuing without a stored question",
		},
		{
			name: "rejects awaiting input follow-up without text answer",
			body: `{"images":["https://example.com/image.png"]}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "awaiting_input", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 2, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "MISSING_ANSWER",
		},
		{
			name: "rolls back message creation when enqueue fails",
			body: `{"message":"Please continue"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "idle", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 1, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectBegin()
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 1, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(fmt.Errorf("queue unavailable"))
				mock.ExpectRollback()
			},
			expectedCode: http.StatusInternalServerError,
			expectedBody: "ENQUEUE_FAILED",
		},
		{
			name: "returns error when commit fails",
			body: `{"message":"Please continue"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "idle", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 1, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectBegin()
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 1, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
				mock.ExpectCommit().WillReturnError(fmt.Errorf("commit failed"))
				mock.ExpectRollback()
			},
			expectedCode: http.StatusInternalServerError,
			expectedBody: "TX_COMMIT_FAILED",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			orgID := uuid.New()
			sessionID := uuid.New()
			userID := uuid.New()
			handler := newSessionHandler(t, mock)
			handler.audit = db.NewAuditEmitter(db.NewAuditLogStore(mock), zerolog.Nop())

			tt.setupMock(mock, orgID, sessionID, userID)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/messages", strings.NewReader(tt.body))
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", sessionID.String())
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: "member"})
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.SendMessage(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")
			require.Contains(t, w.Body.String(), tt.expectedBody, "response body should contain expected content")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandler_ListMessages_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/bad-id/messages", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bad-id")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ListMessages(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid ID")
	require.Contains(t, w.Body.String(), "INVALID_ID")
}

func TestSessionHandler_SendMessage_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/bad-id/messages", strings.NewReader(`{"message":"test"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bad-id")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.SendMessage(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid ID")
	require.Contains(t, w.Body.String(), "INVALID_ID")
}

// mockLLMClient is a test double for llm.Client.
// The WaitGroup lets the test verify that the handler waits for the LLM call
// to finish before returning a response (i.e. the call is synchronous).
type mockLLMClient struct {
	response string
	err      error
	wg       sync.WaitGroup
}

func (m *mockLLMClient) Complete(_ context.Context, _, _ string) (string, error) {
	defer m.wg.Done()
	return m.response, m.err
}

func newMockLLMClient(response string, err error) *mockLLMClient {
	m := &mockLLMClient{response: response, err: err}
	m.wg.Add(1)
	return m
}

func TestSessionHandler_CreateManual_WithLLMTitle(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()

	llmClient := newMockLLMClient("Fix authentication login flow", nil)
	handler := NewSessionHandler(
		db.NewSessionStore(mock),
		db.NewSessionLogStore(mock),
		db.NewSessionQuestionStore(mock),
		db.NewValidationStore(mock),
		db.NewPullRequestStore(mock),
		db.NewIssueStore(mock),
		db.NewRepositoryStore(mock),
		db.NewOrganizationStore(mock),
		db.NewJobStore(mock),
		db.NewSessionMessageStore(mock),
		db.NewSessionThreadStore(mock),
		llmClient,
		zerolog.Nop(),
	)

	now := time.Now()
	runID := uuid.New()
	jobID := uuid.New()

	// Mock org settings lookup
	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "test-org", nil, now, now))

	expectManualSessionCreate(mock, runID, now)

	// Mock concurrency check
	mock.ExpectQuery("SELECT count").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

	// Mock job enqueue
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))

	// Mock UpdateTitle call
	mock.ExpectExec("UPDATE sessions SET title").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/manual",
		strings.NewReader(`{"message":"The login page throws a 500 error when users try to authenticate with SSO","agent_type":"claude_code"}`))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreateManual(w, req)

	// WaitGroup confirms the LLM was called synchronously before the response.
	llmClient.wg.Wait()

	require.Equal(t, http.StatusCreated, w.Code)

	var resp models.SingleResponse[models.Session]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.NotNil(t, resp.Data.Title)
	require.Equal(t, "Fix authentication login flow", *resp.Data.Title)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_CreateManual_LLMError_Returns500(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()

	llmClient := newMockLLMClient("", fmt.Errorf("rate limited"))
	handler := NewSessionHandler(
		db.NewSessionStore(mock),
		db.NewSessionLogStore(mock),
		db.NewSessionQuestionStore(mock),
		db.NewValidationStore(mock),
		db.NewPullRequestStore(mock),
		db.NewIssueStore(mock),
		db.NewRepositoryStore(mock),
		db.NewOrganizationStore(mock),
		db.NewJobStore(mock),
		db.NewSessionMessageStore(mock),
		db.NewSessionThreadStore(mock),
		llmClient,
		zerolog.Nop(),
	)

	now := time.Now()
	runID := uuid.New()
	jobID := uuid.New()

	// Mock org settings lookup
	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "test-org", nil, now, now))

	expectManualSessionCreate(mock, runID, now)

	// Mock concurrency check
	mock.ExpectQuery("SELECT count").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

	// Mock job enqueue
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))

	// No UpdateTitle mock — the LLM error means it should never be called.

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/manual",
		strings.NewReader(`{"message":"Fix the login bug","agent_type":"claude_code"}`))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreateManual(w, req)

	// WaitGroup confirms the LLM was called synchronously.
	llmClient.wg.Wait()

	// LLM failure should propagate as a 500 error.
	require.Equal(t, http.StatusInternalServerError, w.Code, "LLM title generation failure should return 500")

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_CreatePR_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_Success"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	jobID := uuid.New()
	handler := newSessionHandler(t, mock)

	diff := "--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-old\n+new"

	// Mock session lookup — session has a diff.
	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, &diff,
				nil, nil, nil, nil,
				nil, nil,
				nil,                               // triggered_by_user_id
				nil, 0, now, "none", &snapshotKey, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	// Mock PR lookup — no existing PR (returns empty result).
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
			"title", "body", "status", "review_status", "authored_by", "ci_status", "merged_at", "created_at", "updated_at",
		}))

	// Mock job enqueue.
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
	mock.ExpectExec("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, "should return 202 Accepted")
	require.Contains(t, w.Body.String(), `"status":"queued"`, "response should indicate job was queued")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_DedupeConflict(t *testing.T) {
	t.Parallel()

	// Regression: an in-flight open_pr job for the same session must not cause
	// a 500 ENQUEUE_FAILED response. The dedupe conflict means a PR job is
	// already queued, so the request is effectively a success.
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_DedupeConflict"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)

	diff := "--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-old\n+new"

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, &diff,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,
				nil, nil,
				nil,
				"idle", (*string)(nil),
				nil,
				now,
			),
		)

	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
			"title", "body", "status", "review_status", "authored_by", "ci_status", "merged_at", "created_at", "updated_at",
		}))

	// ON CONFLICT DO NOTHING fires because the dedupe_key already matches a
	// pending job — pgx surfaces this as ErrNoRows.
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectExec("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, "dedupe conflict should be a success — the existing job satisfies the request")
	require.Contains(t, w.Body.String(), `"status":"queued"`, "response should still indicate queued status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_ReturnsAuthInterceptWhenUserCredentialMissing(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_ReturnsAuthInterceptWhenUserCredentialMissing"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	userID := uuid.New()
	handler := newSessionHandler(t, mock)
	handler.SetPRCredentialStore(&stubSessionPRCredentialStore{err: pgx.ErrNoRows})
	handler.SetPRAuthCredentialChecker(&stubSessionPRAuthCredentialChecker{
		hasValidCredentialFunc: func(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
			return false, nil
		},
	})
	handler.SetPRAuthFlow("test-signing-key-32bytes-minimum-length", "https://app.143.dev")

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(sessionTestRow(
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				&userID,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,
				nil, nil,
				nil,
				"idle", (*string)(nil),
				nil,
				now,
			)...),
		)
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
			"title", "body", "status", "review_status", "authored_by", "ci_status", "merged_at", "created_at", "updated_at",
		}))
	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "Acme", json.RawMessage(`{"pr_authorship":"user_preferred"}`), now, now))

	body := strings.NewReader(`{"author_mode":"auto"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusConflict, w.Code, "missing user credential should trigger PR authorship auth intercept")
	var resp models.ErrorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "response should decode")
	require.Equal(t, "GITHUB_PR_AUTHORSHIP_REQUIRED", resp.Error.Code, "response should carry auth-required code")
	details, ok := resp.Error.Details.(map[string]any)
	require.True(t, ok, "error details should be present")
	require.Equal(t, sessionID.String(), details["session_id"], "details should name the session being resumed")
	require.NotEmpty(t, details["resume_token"], "details should include a resume token")
	require.Equal(t, true, details["can_fallback_to_app"], "user_preferred should allow app fallback")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_InvalidAuthorMode(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_InvalidAuthorMode"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	userID := uuid.New()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(sessionTestRow(
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				&userID,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,
				nil, nil,
				nil,
				"idle", (*string)(nil),
				nil,
				now,
			)...),
		)
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
			"title", "body", "status", "review_status", "authored_by", "ci_status", "merged_at", "created_at", "updated_at",
		}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", strings.NewReader(`{"author_mode":"bogus"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "CreatePR should reject invalid author modes")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_PRAuthCheckerError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_PRAuthCheckerError"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	userID := uuid.New()
	handler := newSessionHandler(t, mock)
	handler.SetPRAuthCredentialChecker(&stubSessionPRAuthCredentialChecker{
		hasValidCredentialFunc: func(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
			return false, context.DeadlineExceeded
		},
	})
	handler.SetPRAuthFlow("test-signing-key-32bytes-minimum-length", "https://app.143.dev")

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(sessionTestRow(
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				&userID,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,
				nil, nil,
				nil,
				"idle", (*string)(nil),
				nil,
				now,
			)...),
		)
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
			"title", "body", "status", "review_status", "authored_by", "ci_status", "merged_at", "created_at", "updated_at",
		}))
	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "Acme", json.RawMessage(`{"pr_authorship":"user_preferred"}`), now, now))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", strings.NewReader(`{"author_mode":"auto"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code, "CreatePR should surface PR auth checker failures")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_ResumeTokenRequiresSigningKey(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_ResumeTokenRequiresSigningKey"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	userID := uuid.New()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(sessionTestRow(
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				&userID,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,
				nil, nil,
				nil,
				"idle", (*string)(nil),
				nil,
				now,
			)...),
		)
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
			"title", "body", "status", "review_status", "authored_by", "ci_status", "merged_at", "created_at", "updated_at",
		}))
	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "Acme", json.RawMessage(`{"pr_authorship":"user_preferred"}`), now, now))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", strings.NewReader(`{"author_mode":"auto","resume_token":"resume"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code, "CreatePR should reject resume tokens when signing is not configured")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_AppAuthorModeBypassesAuthIntercept(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_AppAuthorModeBypassesAuthIntercept"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	userID := uuid.New()
	jobID := uuid.New()
	handler := newSessionHandler(t, mock)
	handler.SetPRCredentialStore(&stubSessionPRCredentialStore{err: pgx.ErrNoRows})
	handler.SetPRAuthFlow("test-signing-key-32bytes-minimum-length", "https://app.143.dev")

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(sessionTestRow(
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				&userID,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,
				nil, nil,
				nil,
				"idle", (*string)(nil),
				nil,
				now,
			)...),
		)
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
			"title", "body", "status", "review_status", "authored_by", "ci_status", "merged_at", "created_at", "updated_at",
		}))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
	mock.ExpectExec("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	body := strings.NewReader(`{"author_mode":"app"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, "explicit app author mode should enqueue PR creation without auth intercept")
	require.Contains(t, w.Body.String(), `"status":"queued"`, "response should indicate job was queued")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_InvalidStoredCredentialTriggersAuthIntercept(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_InvalidStoredCredentialTriggersAuthIntercept"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	userID := uuid.New()
	handler := newSessionHandler(t, mock)
	handler.SetPRCredentialStore(&stubSessionPRCredentialStore{
		cred: &models.DecryptedUserCredential{
			Config: models.GitHubAppUserConfig{
				AccessToken:           "ghu_stale",
				TokenType:             "bearer",
				ExpiresAt:             now.Add(-time.Minute),
				RefreshToken:          "ghr_test",
				RefreshTokenExpiresAt: now.Add(30 * 24 * time.Hour),
			},
		},
	})
	handler.SetPRAuthCredentialChecker(&stubSessionPRAuthCredentialChecker{
		hasValidCredentialFunc: func(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
			return false, nil
		},
	})
	handler.SetPRAuthFlow("test-signing-key-32bytes-minimum-length", "https://app.143.dev")

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(sessionTestRow(
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				&userID,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,
				nil, nil,
				nil,
				"idle", (*string)(nil),
				nil,
				now,
			)...),
		)
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
			"title", "body", "status", "review_status", "authored_by", "ci_status", "merged_at", "created_at", "updated_at",
		}))
	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "Acme", json.RawMessage(`{"pr_authorship":"user_preferred"}`), now, now))

	body := strings.NewReader(`{"author_mode":"auto"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusConflict, w.Code, "stale user credential should trigger reconnect instead of enqueueing PR creation")
	var resp models.ErrorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "response should decode")
	require.Equal(t, "GITHUB_PR_AUTHORSHIP_REQUIRED", resp.Error.Code, "response should request GitHub reauthorization")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_UserPreferredWithoutGitHubAppUserAuthFallsBackToApp(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_UserPreferredWithoutGitHubAppUserAuthFallsBackToApp"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	userID := uuid.New()
	jobID := uuid.New()
	handler := newSessionHandler(t, mock)
	handler.SetPRAuthFlow("test-signing-key-32bytes-minimum-length", "https://app.143.dev")

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(sessionTestRow(
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				&userID,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,
				nil, nil,
				nil,
				"idle", (*string)(nil),
				nil,
				now,
			)...),
		)
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
			"title", "body", "status", "review_status", "authored_by", "ci_status", "merged_at", "created_at", "updated_at",
		}))
	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "Acme", json.RawMessage(`{"pr_authorship":"user_preferred"}`), now, now))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
	mock.ExpectExec("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", strings.NewReader(`{"author_mode":"auto"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, "user_preferred should fall back to app auth when GitHub App user auth is unavailable")
	require.Contains(t, w.Body.String(), `"status":"queued"`, "response should enqueue PR creation")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_UserRequiredWithoutGitHubAppUserAuthFailsFast(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_UserRequiredWithoutGitHubAppUserAuthFailsFast"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	userID := uuid.New()
	handler := newSessionHandler(t, mock)
	handler.SetPRAuthFlow("test-signing-key-32bytes-minimum-length", "https://app.143.dev")

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(sessionTestRow(
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				&userID,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,
				nil, nil,
				nil,
				"idle", (*string)(nil),
				nil,
				now,
			)...),
		)
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
			"title", "body", "status", "review_status", "authored_by", "ci_status", "merged_at", "created_at", "updated_at",
		}))
	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "Acme", json.RawMessage(`{"pr_authorship":"user_required"}`), now, now))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", strings.NewReader(`{"author_mode":"auto"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code, "user_required should fail fast when GitHub App user auth is unavailable")
	require.Contains(t, w.Body.String(), "GITHUB_APP_USER_AUTH_NOT_CONFIGURED", "response should explain the missing GitHub App user auth configuration")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_UserRequiredWithoutCheckerIgnoresStoredGitHubAppUserCredential(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_UserRequiredWithoutCheckerIgnoresStoredGitHubAppUserCredential"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	userID := uuid.New()
	handler := newSessionHandler(t, mock)
	handler.SetPRCredentialStore(&stubSessionPRCredentialStore{
		cred: &models.DecryptedUserCredential{
			Config: models.GitHubAppUserConfig{
				AccessToken:           "ghu_present_but_unusable",
				TokenType:             "bearer",
				ExpiresAt:             now.Add(time.Hour),
				RefreshToken:          "ghr_test",
				RefreshTokenExpiresAt: now.Add(30 * 24 * time.Hour),
			},
		},
	})
	handler.SetPRAuthFlow("test-signing-key-32bytes-minimum-length", "https://app.143.dev")

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(sessionTestRow(
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				&userID,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,
				nil, nil,
				nil,
				"idle", (*string)(nil),
				nil,
				now,
			)...),
		)
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
			"title", "body", "status", "review_status", "authored_by", "ci_status", "merged_at", "created_at", "updated_at",
		}))
	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "Acme", json.RawMessage(`{"pr_authorship":"user_required"}`), now, now))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", strings.NewReader(`{"author_mode":"auto"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code, "user_required should fail fast even if a stored github_app_user row exists when the resolver is unwired")
	require.Contains(t, w.Body.String(), "GITHUB_APP_USER_AUTH_NOT_CONFIGURED", "response should explain the missing GitHub App user auth configuration")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_SnapshotExpired(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)

	// Mock session lookup — session has no snapshot_key, simulating an
	// expired sandbox that can no longer be restored for push.
	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, now, "none", nil, // snapshot_key is nil
				nil, nil, nil, nil, nil,
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 when snapshot has expired")
	require.Contains(t, w.Body.String(), "SNAPSHOT_EXPIRED", "error code should indicate snapshot expiry")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_InFlightRejectsDuplicateSubmit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state string
	}{
		{name: "queued state rejects duplicate", state: "queued"},
		{name: "pushing state rejects duplicate", state: "pushing"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should be created")
			defer mock.Close()

			now := time.Now()
			snapshotKey := "snap-" + tt.state
			orgID := uuid.New()
			sessionID := uuid.New()
			issueID := uuid.New()
			handler := newSessionHandler(t, mock)

			mock.ExpectQuery("SELECT .+ FROM sessions").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(
					addSessionRow(pgxmock.NewRows(sessionColumns),
						sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
						nil, nil, nil, nil,
						nil, false, &now, &now, nil,
						nil, nil, nil, false,
						nil, nil, nil, nil, nil,
						nil, nil, nil, nil,
						nil, nil,
						nil,
						nil, 0, now, "none", &snapshotKey,
						nil, nil, nil, nil, nil,
						nil,      // input_manifest
						nil, nil, // archived_at, archived_by_user_id
						nil,            // automation_run_id
						tt.state,       // pr_creation_state
						(*string)(nil), // pr_creation_error
						nil,            // deleted_at
						now,
					),
				)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", sessionID.String())
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.CreatePR(w, req)

			require.Equal(t, http.StatusConflict, w.Code, "in-flight PR creation should reject duplicate submits")
			require.Contains(t, w.Body.String(), "PR_IN_FLIGHT", "error code should indicate an in-flight PR creation")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandler_CreatePR_UpdateStateErrorStillAccepted(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_UpdateStateErrorStillAccepted"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	jobID := uuid.New()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
			"title", "body", "status", "review_status", "authored_by", "ci_status", "merged_at", "created_at", "updated_at",
		}))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
	mock.ExpectExec("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("write failed"))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, "CreatePR should still accept the request when the best-effort state update fails")
	require.Contains(t, w.Body.String(), `"status":"queued"`, "response should still indicate queued status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_AlreadyExists(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_AlreadyExists"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	prID := uuid.New()
	handler := newSessionHandler(t, mock)

	diff := "--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-old\n+new"

	// Mock session lookup — session has a diff.
	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, &diff,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	// Mock PR lookup - PR already exists.
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
				"title", "body", "status", "review_status", "authored_by", "ci_status", "merged_at", "created_at", "updated_at",
			}).AddRow(
				prID, &sessionID, orgID, 42, "https://github.com/org/repo/pull/42", "org/repo",
				"Fix bug", (*string)(nil), "open", "pending", "app", "", (*time.Time)(nil), now, now,
			),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusConflict, w.Code, "should return 409 when PR already exists")
	require.Contains(t, w.Body.String(), "PR_EXISTS", "error code should indicate PR already exists")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_SucceededWithoutStoredPRRejectsRetry(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_SucceededWithoutStoredPRRejectsRetry"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)

	diff := "--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-old\n+new"

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, &diff,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"succeeded",    // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
			"title", "body", "status", "review_status", "authored_by", "ci_status", "merged_at", "created_at", "updated_at",
		}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusConflict, w.Code, "should reject retries after PR creation already succeeded without re-enqueueing")
	require.Contains(t, w.Body.String(), "PR_ALREADY_CREATED", "error code should indicate the terminal PR state")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_SessionNotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	handler := newSessionHandler(t, mock)

	// Mock session lookup — not found.
	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionColumns))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusNotFound, w.Code, "should return 404 when session not found")
	require.Contains(t, w.Body.String(), "NOT_FOUND", "error code should indicate session not found")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_PRLookupDBError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_PRLookupDBError"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)

	diff := "--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-old\n+new"

	// Mock session lookup — session has a diff.
	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, &diff,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	// Mock PR lookup — returns a database error.
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("connection refused"))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code, "should return 500 on PR lookup DB error")
	require.Contains(t, w.Body.String(), "INTERNAL_ERROR", "error code should indicate internal error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// mockCanceller implements SessionCanceller for testing.
type mockCanceller struct {
	called    bool
	sessionID uuid.UUID
	result    bool
}

func (m *mockCanceller) CancelSession(sessionID uuid.UUID) bool {
	m.called = true
	m.sessionID = sessionID
	return m.result
}

func TestSessionHandler_CancelSession_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)
	canceller := &mockCanceller{result: true}
	handler.SetCanceller(canceller)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "running", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil, // triggered_by_user_id
				nil, 1, now, "running", nil,
				nil, nil, nil, nil, nil,
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/cancel", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CancelSession(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, "cancel should return 202 Accepted")
	require.Contains(t, w.Body.String(), `"status":"running"`, "response should still show running status")
	require.True(t, canceller.called, "canceller should have been called")
	require.Equal(t, sessionID, canceller.sessionID, "canceller should receive correct session ID")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_Update(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	existingTitle := "Original title"
	handler := newSessionHandler(t, mock)
	titleSyncer := &stubSessionPRTitleSyncer{}
	handler.SetPRTitleSyncer(titleSyncer)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, &existingTitle, nil, nil,
				nil, nil,
				nil, // triggered_by_user_id
				nil, 1, now, "none", nil,
				nil, nil, nil, nil, nil,
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	mock.ExpectExec("UPDATE sessions SET title").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/sessions/"+sessionID.String(), strings.NewReader(`{"title":"Updated session title"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Update(w, req)

	require.Equal(t, http.StatusOK, w.Code, "update should return 200 OK")

	var resp models.SingleResponse[models.Session]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response should decode")
	require.NotNil(t, resp.Data.Title, "updated session should include title")
	require.Equal(t, "Updated session title", *resp.Data.Title, "response should include the updated title")
	require.True(t, titleSyncer.called, "PR title syncer should be invoked")
	require.Equal(t, "Updated session title", titleSyncer.lastTitle, "PR title syncer should receive the updated title")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_UpdateTitle_SyncFailureStillSucceeds(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	existingTitle := "Original title"
	handler := newSessionHandler(t, mock)
	titleSyncer := &stubSessionPRTitleSyncer{err: errors.New("github unavailable")}
	handler.SetPRTitleSyncer(titleSyncer)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, &existingTitle, nil, nil,
				nil, nil,
				nil,
				nil, 1, now, "none", nil,
				nil, nil, nil, nil, nil,
				nil,
				nil, nil,
				nil,
				"idle",
				(*string)(nil),
				nil,
				now,
			),
		)

	mock.ExpectExec("UPDATE sessions SET title").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/sessions/"+sessionID.String(), strings.NewReader(`{"title":"Updated session title"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Update(w, req)

	require.Equal(t, http.StatusOK, w.Code, "update should still return 200 when PR sync fails")

	var resp models.SingleResponse[models.Session]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response should decode")
	require.NotNil(t, resp.Data.Title, "updated session should include title")
	require.Equal(t, "Updated session title", *resp.Data.Title, "response should include the updated title")
	require.True(t, titleSyncer.called, "PR title syncer should still be invoked")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_UpdateTitle_ErrorPaths(t *testing.T) {
	t.Parallel()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	existingTitle := "Original title"

	tests := []struct {
		name           string
		sessionParam   string
		body           string
		setupMock      func(mock pgxmock.PgxPoolIface)
		expectedStatus int
		expectedCode   string
		expectSync     bool
		expectedTitle  *string
	}{
		{
			name:           "returns bad request for invalid session id",
			sessionParam:   "not-a-uuid",
			body:           `{"title":"Updated session title"}`,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "INVALID_ID",
		},
		{
			name:           "returns bad request for invalid json body",
			sessionParam:   sessionID.String(),
			body:           `{"title":`,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "INVALID_BODY",
		},
		{
			name:           "returns bad request when title is missing",
			sessionParam:   sessionID.String(),
			body:           `{}`,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "INVALID_BODY",
		},
		{
			name:           "returns bad request for invalid title",
			sessionParam:   sessionID.String(),
			body:           `{"title":"   "}`,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "INVALID_TITLE",
		},
		{
			name:         "returns not found when session does not exist",
			sessionParam: sessionID.String(),
			body:         `{"title":"Updated session title"}`,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM sessions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
			},
			expectedStatus: http.StatusNotFound,
			expectedCode:   "NOT_FOUND",
		},
		{
			name:         "returns existing session when title is unchanged",
			sessionParam: sessionID.String(),
			body:         `{"title":"Original title"}`,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM sessions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, &now, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, &existingTitle, nil, nil,
							nil, nil,
							nil,
							nil, 1, now, "none", nil,
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
			},
			expectedStatus: http.StatusOK,
			expectedTitle:  &existingTitle,
		},
		{
			name:         "returns internal error when update fails",
			sessionParam: sessionID.String(),
			body:         `{"title":"Updated session title"}`,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM sessions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, &now, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, &existingTitle, nil, nil,
							nil, nil,
							nil,
							nil, 1, now, "none", nil,
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectExec("UPDATE sessions SET title").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(errors.New("write failed"))
			},
			expectedStatus: http.StatusInternalServerError,
			expectedCode:   "UPDATE_FAILED",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool without error")
			defer mock.Close()

			handler := newSessionHandler(t, mock)
			titleSyncer := &stubSessionPRTitleSyncer{}
			handler.SetPRTitleSyncer(titleSyncer)

			if tt.setupMock != nil {
				tt.setupMock(mock)
			}

			req := httptest.NewRequest(http.MethodPatch, "/api/v1/sessions/"+tt.sessionParam, strings.NewReader(tt.body))
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", tt.sessionParam)
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.Update(w, req)

			require.Equal(t, tt.expectedStatus, w.Code, "update should return the expected status code")

			if tt.expectedTitle != nil {
				var resp models.SingleResponse[models.Session]
				err = json.Unmarshal(w.Body.Bytes(), &resp)
				require.NoError(t, err, "response should decode")
				require.NotNil(t, resp.Data.Title, "response should include the current title")
				require.Equal(t, *tt.expectedTitle, *resp.Data.Title, "response should preserve the existing title")
			} else if tt.expectedCode != "" {
				var resp models.ErrorResponse
				err = json.Unmarshal(w.Body.Bytes(), &resp)
				require.NoError(t, err, "error response should decode")
				require.Equal(t, tt.expectedCode, resp.Error.Code, "error response should include the expected code")
			}

			require.Equal(t, tt.expectSync, titleSyncer.called, "PR title syncer should only run for successful updates")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandler_CancelSession_NotRunning(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)
	canceller := &mockCanceller{result: true}
	handler.SetCanceller(canceller)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "idle", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil, // triggered_by_user_id
				nil, 1, now, "snapshotted", stringPtr("snapshots/test.tar"),
				nil, nil, nil, nil, nil,
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/cancel", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CancelSession(w, req)

	require.Equal(t, http.StatusConflict, w.Code, "cancelling non-running session should return 409")
	require.Contains(t, w.Body.String(), "NOT_RUNNING")
	require.False(t, canceller.called, "canceller should not be called for non-running sessions")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_CancelSession_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := newSessionHandler(t, mock)
	canceller := &mockCanceller{result: true}
	handler.SetCanceller(canceller)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/not-a-uuid/cancel", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "not-a-uuid")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, uuid.New())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CancelSession(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_ID")
	require.False(t, canceller.called)
}

func TestSessionHandler_CancelSession_NoCanceller(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)
	// Don't set canceller — leave it nil.

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "running", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil, // triggered_by_user_id
				nil, 1, now, "running", nil,
				nil, nil, nil, nil, nil,
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/cancel", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CancelSession(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "CANCEL_UNAVAILABLE")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_ArchiveSession(t *testing.T) {
	t.Parallel()

	t.Run("archives session successfully", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()

		mock.ExpectExec("UPDATE sessions SET archived_at").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/archive", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", sessionID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = middleware.WithOrgID(ctx, orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.ArchiveSession(w, req)

		require.Equal(t, http.StatusOK, w.Code, "archive should return 200")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns 401 when user is not authenticated", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		sessionID := uuid.New()

		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/archive", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", sessionID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = middleware.WithOrgID(ctx, uuid.New())
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.ArchiveSession(w, req)

		require.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("returns 404 when session not found", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()

		mock.ExpectExec("UPDATE sessions SET archived_at").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 0))

		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/archive", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", sessionID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = middleware.WithOrgID(ctx, orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.ArchiveSession(w, req)

		require.Equal(t, http.StatusNotFound, w.Code)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("cleans up snapshot when archiving session with snapshot key", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgx mock pool")
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		snapshotStore := &archiveTestSnapshotStore{}
		handler.SetSnapshotStore(snapshotStore)

		orgID := uuid.New()
		sessionID := uuid.New()
		issueID := uuid.New()
		userID := uuid.New()
		now := time.Now()
		snapshotKey := "snapshots/session.tar.zst"

		mock.ExpectQuery("SELECT .+ FROM sessions").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				addSessionRow(pgxmock.NewRows(sessionColumns),
					sessionID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
					nil, nil, nil, nil,
					nil, false, &now, &now, nil,
					nil, nil, nil, false,
					nil, nil, nil, nil, nil,
					nil, nil, nil, nil,
					nil, nil, nil,
					nil, 0, now, "saved", &snapshotKey,
					nil, nil, nil, nil, nil, nil,
					nil, nil,
					nil,
					"idle",
					(*string)(nil),
					nil,
					now,
				),
			)
		mock.ExpectExec("UPDATE sessions SET archived_at").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		mock.ExpectExec("UPDATE sessions\\s+SET snapshot_key = NULL, sandbox_state = 'destroyed'").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/archive", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", sessionID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = middleware.WithOrgID(ctx, orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.ArchiveSession(w, req)

		require.Equal(t, http.StatusOK, w.Code, "archive should return 200 after snapshot cleanup")
		require.Equal(t, []string{snapshotKey}, snapshotStore.deleted, "archive should delete the stored snapshot exactly once")
		require.NoError(t, mock.ExpectationsWereMet(), "archive should satisfy all database expectations")
	})

	t.Run("still archives when preload lookup fails for audit", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgx mock pool")
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		handler.SetAuditEmitter(db.NewAuditEmitter(db.NewAuditLogStore(mock), zerolog.Nop()))

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()

		mock.ExpectQuery("SELECT .+ FROM sessions").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(errors.New("db down"))
		mock.ExpectExec("UPDATE sessions SET archived_at").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		mock.ExpectQuery("INSERT INTO audit_logs").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), time.Now()))

		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/archive", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", sessionID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = middleware.WithOrgID(ctx, orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.ArchiveSession(w, req)

		require.Equal(t, http.StatusOK, w.Code, "archive should still succeed when the preload lookup fails")
		require.NoError(t, mock.ExpectationsWereMet(), "archive should satisfy all database expectations")
	})

	t.Run("ignores snapshot cleanup failure after archive succeeds", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgx mock pool")
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		snapshotStore := &archiveTestSnapshotStore{err: errors.New("delete failed")}
		handler.SetSnapshotStore(snapshotStore)

		orgID := uuid.New()
		sessionID := uuid.New()
		issueID := uuid.New()
		userID := uuid.New()
		now := time.Now()
		snapshotKey := "snapshots/session.tar.zst"

		mock.ExpectQuery("SELECT .+ FROM sessions").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				addSessionRow(pgxmock.NewRows(sessionColumns),
					sessionID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
					nil, nil, nil, nil,
					nil, false, &now, &now, nil,
					nil, nil, nil, false,
					nil, nil, nil, nil, nil,
					nil, nil, nil, nil,
					nil, nil, nil,
					nil, 0, now, "saved", &snapshotKey,
					nil, nil, nil, nil, nil, nil,
					nil, nil,
					nil,
					"idle",
					(*string)(nil),
					nil,
					now,
				),
			)
		mock.ExpectExec("UPDATE sessions SET archived_at").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/archive", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", sessionID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = middleware.WithOrgID(ctx, orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.ArchiveSession(w, req)

		require.Equal(t, http.StatusOK, w.Code, "archive should still succeed when snapshot cleanup fails")
		require.Equal(t, []string{snapshotKey}, snapshotStore.deleted, "archive should still attempt snapshot cleanup")
		require.NoError(t, mock.ExpectationsWereMet(), "archive should satisfy all database expectations")
	})
}

func TestSessionHandler_UnarchiveSession(t *testing.T) {
	t.Parallel()

	t.Run("unarchives session successfully", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		orgID := uuid.New()
		sessionID := uuid.New()

		mock.ExpectExec("UPDATE sessions SET archived_at = NULL").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/unarchive", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", sessionID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = middleware.WithOrgID(ctx, orgID)
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.UnarchiveSession(w, req)

		require.Equal(t, http.StatusOK, w.Code, "unarchive should return 200")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns 404 when session not found or not archived", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		orgID := uuid.New()
		sessionID := uuid.New()

		mock.ExpectExec("UPDATE sessions SET archived_at = NULL").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 0))

		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/unarchive", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", sessionID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = middleware.WithOrgID(ctx, orgID)
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.UnarchiveSession(w, req)

		require.Equal(t, http.StatusNotFound, w.Code)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns 400 for invalid session ID", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := newSessionHandler(t, mock)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/not-a-uuid/unarchive", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", "not-a-uuid")
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = middleware.WithOrgID(ctx, uuid.New())
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.UnarchiveSession(w, req)

		require.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestSessionHandler_UpdateTitle(t *testing.T) {
	t.Parallel()

	t.Run("updates the title and returns the refreshed session", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		now := time.Now()
		orgID := uuid.New()
		sessionID := uuid.New()
		issueID := uuid.New()
		title := "Renamed session"

		mock.ExpectExec("UPDATE sessions SET title").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		mock.ExpectQuery("SELECT .+ FROM sessions").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(sessionColumns).AddRow(sessionTestRow(
					sessionID, issueID, orgID, "claude_code", "idle", "full", "standard",
					nil, nil, nil, nil,
					nil, false, &now, &now, nil,
					nil, nil, nil, false,
					nil, nil, nil, nil, nil,
					nil, &title, nil, nil,
					nil, nil, nil,
					nil, 1, now, "snapshotted", nil,
					nil, nil, nil, nil, nil,
					nil, nil, nil,
					nil,
					"idle", (*string)(nil),
					nil,
					now,
				)...),
			)

		req := httptest.NewRequest(http.MethodPatch, "/api/v1/sessions/"+sessionID.String(), strings.NewReader(`{"title":"  Renamed session  "}`))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", sessionID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = middleware.WithOrgID(ctx, orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: uuid.New(), OrgID: orgID, Role: "member"})
		req = req.WithContext(ctx)

		w := httptest.NewRecorder()
		handler.UpdateTitle(w, req)

		require.Equal(t, http.StatusOK, w.Code, "UpdateTitle should return 200")

		var resp models.SingleResponse[models.Session]
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "UpdateTitle should return valid JSON")
		require.NotNil(t, resp.Data.Title, "UpdateTitle should return the updated title")
		require.Equal(t, title, *resp.Data.Title, "UpdateTitle should return the normalized title")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("rejects malformed request bodies", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		orgID := uuid.New()
		sessionID := uuid.New()

		req := httptest.NewRequest(http.MethodPatch, "/api/v1/sessions/"+sessionID.String(), strings.NewReader(`{`))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", sessionID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = middleware.WithOrgID(ctx, orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: uuid.New(), OrgID: orgID, Role: "member"})
		req = req.WithContext(ctx)

		w := httptest.NewRecorder()
		handler.UpdateTitle(w, req)

		require.Equal(t, http.StatusBadRequest, w.Code, "UpdateTitle should reject malformed JSON")
		require.NoError(t, mock.ExpectationsWereMet(), "malformed requests should not hit the database")
	})
}

func stringPtr(s string) *string {
	return &s
}
