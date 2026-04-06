package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/feedback"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/assembledhq/143/internal/services/pm"
	"github.com/assembledhq/143/internal/services/prioritization"
	"github.com/assembledhq/143/internal/services/validation"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func newTestStores(t *testing.T) (*Stores, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	stores := &Stores{
		Issues:       db.NewIssueStore(mock),
		Sessions:    db.NewSessionStore(mock),
		Jobs:         db.NewJobStore(mock),
		Integrations: db.NewIntegrationStore(mock),
		Webhooks:     db.NewWebhookDeliveryStore(mock),
	}
	return stores, mock
}

func TestIngestWebhookHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		payload   json.RawMessage
		expectErr bool
		errSubstr string
	}{
		{
			name:      "valid payload succeeds",
			payload:   json.RawMessage(`{"webhook_delivery_id":"abc-123","provider":"github"}`),
			expectErr: false,
		},
		{
			name:      "invalid JSON returns unmarshal error",
			payload:   json.RawMessage(`{invalid json}`),
			expectErr: true,
			errSubstr: "unmarshal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stores, mock := newTestStores(t)
			defer mock.Close()
			logger := zerolog.Nop()

			handler := newIngestWebhookHandler(stores, logger)
			err := handler(context.Background(), "ingest_webhook", tt.payload)

			if tt.expectErr {
				require.Error(t, err, "ingest_webhook handler should return an error for invalid input")
				require.Contains(t, err.Error(), tt.errSubstr, "error should contain expected substring")
			} else {
				require.NoError(t, err, "ingest_webhook handler should succeed for valid input")
			}
		})
	}
}

func TestPrioritizeHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		payload   json.RawMessage
		expectErr bool
		errSubstr string
	}{
		{
			name:      "invalid JSON returns unmarshal error",
			payload:   json.RawMessage(`not json at all`),
			expectErr: true,
			errSubstr: "unmarshal",
		},
		{
			name:      "missing org ID returns parse error",
			payload:   json.RawMessage(`{"issue_id":"` + uuid.New().String() + `"}`),
			expectErr: true,
			errSubstr: "parse org ID",
		},
		{
			name:      "invalid issue UUID returns parse error",
			payload:   json.RawMessage(`{"issue_id":"not-a-valid-uuid","org_id":"` + uuid.New().String() + `"}`),
			expectErr: true,
			errSubstr: "parse issue ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stores, mock := newTestStores(t)
			defer mock.Close()
			logger := zerolog.Nop()

			services := &Services{}
			handler := newPrioritizeHandler(stores, services, logger)
			err := handler(context.Background(), "prioritize", tt.payload)

			if tt.expectErr {
				require.Error(t, err, "prioritize handler should return an error for invalid input")
				require.Contains(t, err.Error(), tt.errSubstr, "error should contain expected substring")
			} else {
				require.NoError(t, err, "prioritize handler should succeed for valid input")
			}
		})
	}
}

func TestSyncSentryHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		payload   json.RawMessage
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
		errSubstr string
	}{
		{
			name:      "invalid JSON returns unmarshal error",
			payload:   json.RawMessage(`{invalid json}`),
			setupMock: func(mock pgxmock.PgxPoolIface) {},
			expectErr: true,
			errSubstr: "unmarshal sync_sentry payload",
		},
		{
			name:      "invalid org ID returns parse error",
			payload:   json.RawMessage(`{"org_id":"not-a-uuid"}`),
			setupMock: func(mock pgxmock.PgxPoolIface) {},
			expectErr: true,
			errSubstr: "parse org ID",
		},
		{
			name:    "no integrations returns nil",
			payload: json.RawMessage(`{"org_id":"` + uuid.New().String() + `"}`),
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .* FROM integrations").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}))
			},
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stores, mock := newTestStores(t)
			defer mock.Close()
			logger := zerolog.Nop()

			tt.setupMock(mock)

			handler := newSyncSentryHandler(stores, logger)
			err := handler(context.Background(), "sync_sentry", tt.payload)

			if tt.expectErr {
				require.Error(t, err, "sync_sentry handler should return an error for invalid input")
				require.Contains(t, err.Error(), tt.errSubstr, "error should contain expected substring")
			} else {
				require.NoError(t, err, "sync_sentry handler should succeed")
				require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
			}
		})
	}
}

func TestNewOrgIDJobHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		payload   json.RawMessage
		expectErr bool
		errSubstr string
	}{
		{
			name:      "invalid JSON returns unmarshal error",
			payload:   json.RawMessage(`{invalid json}`),
			expectErr: true,
			errSubstr: "unmarshal pm_bootstrap payload",
		},
		{
			name:      "invalid org ID returns parse error",
			payload:   json.RawMessage(`{"org_id":"not-a-uuid"}`),
			expectErr: true,
			errSubstr: "parse org ID",
		},
		{
			name:      "valid org ID invokes callback",
			payload:   nil,
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			logger := zerolog.Nop()
			expectedOrgID := uuid.New()
			payload := tt.payload
			if payload == nil {
				payload = json.RawMessage(`{"org_id":"` + expectedOrgID.String() + `"}`)
			}

			called := false
			handler := newOrgIDJobHandler("pm_bootstrap", func(ctx context.Context, orgID uuid.UUID) error {
				called = true
				require.Equal(t, expectedOrgID, orgID, "newOrgIDJobHandler should pass the parsed org ID to the callback")
				return nil
			}, logger)

			err := handler(context.Background(), "pm_bootstrap", payload)
			if tt.expectErr {
				require.Error(t, err, "newOrgIDJobHandler should return an error for invalid input")
				require.Contains(t, err.Error(), tt.errSubstr, "error should contain the expected substring")
				require.False(t, called, "newOrgIDJobHandler should not invoke the callback when input is invalid")
				return
			}

			require.NoError(t, err, "newOrgIDJobHandler should succeed for valid input")
			require.True(t, called, "newOrgIDJobHandler should invoke the callback for valid input")
		})
	}
}

func TestParseSlackTimestamp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ts       string
		expected time.Time
	}{
		{
			name:     "valid slack timestamp returns unix seconds",
			ts:       "1678901234.567890",
			expected: time.Unix(1678901234, 0),
		},
		{
			name:     "missing fractional part still parses",
			ts:       "1678901234",
			expected: time.Unix(1678901234, 0),
		},
		{
			name:     "invalid timestamp returns zero time",
			ts:       "not-a-timestamp",
			expected: time.Time{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := parseSlackTimestamp(tt.ts)
			require.Equal(t, tt.expected, actual, "parseSlackTimestamp should return the expected time value")
		})
	}
}

func TestRunAgentHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		payload   json.RawMessage
		expectErr bool
		errSubstr string
	}{
		{
			name:      "invalid JSON returns unmarshal error",
			payload:   json.RawMessage(`{bad json}`),
			expectErr: true,
			errSubstr: "unmarshal run_agent payload",
		},
		{
			name:      "invalid org ID returns parse error",
			payload:   json.RawMessage(`{"session_id":"` + uuid.New().String() + `","org_id":"not-a-uuid"}`),
			expectErr: true,
			errSubstr: "parse org ID",
		},
		{
			name:      "invalid run ID returns parse error",
			payload:   json.RawMessage(`{"session_id":"not-a-uuid","org_id":"` + uuid.New().String() + `"}`),
			expectErr: true,
			errSubstr: "parse agent run ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stores, mock := newTestStores(t)
			defer mock.Close()
			logger := zerolog.Nop()

			handler := newRunAgentHandler(stores, nil, logger)
			err := handler(context.Background(), "run_agent", tt.payload)

			require.Error(t, err, "run_agent handler should return an error for invalid input")
			require.Contains(t, err.Error(), tt.errSubstr, "error should contain expected substring")
		})
	}
}

func TestValidateHandler_InvalidJSON(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()
	logger := zerolog.Nop()

	handler := newValidateHandler(stores, nil, logger)
	payload := json.RawMessage(`{bad json}`)
	err := handler(context.Background(), "validate", payload)

	require.Error(t, err, "validate handler should return an error for invalid JSON")
	require.Contains(t, err.Error(), "unmarshal validate payload", "error should indicate unmarshal failure")
}

func TestValidateHandler_UsesJobOrgIDWhenPayloadMissingOrgID(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()
	logger := zerolog.Nop()

	orgID := uuid.New()
	runID := uuid.New()
	mock.ExpectQuery("SELECT .* FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(context.Canceled)

	handler := newValidateHandler(stores, nil, logger)
	payload := json.RawMessage(`{"session_id":"` + runID.String() + `"}`)
	err := handler(withJobOrgID(context.Background(), orgID), "validate", payload)

	require.Error(t, err, "validate handler should return an error when run fetch fails")
	require.Contains(t, err.Error(), "fetch agent run", "validate handler should use org ID from job context before failing run fetch")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestOpenPRHandler_InvalidJSON(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()
	logger := zerolog.Nop()

	handler := newOpenPRHandler(stores, nil, logger)
	payload := json.RawMessage(`{bad json}`)
	err := handler(context.Background(), "open_pr", payload)

	require.Error(t, err, "open_pr handler should return an error for invalid JSON")
	require.Contains(t, err.Error(), "unmarshal open_pr payload", "error should indicate unmarshal failure")
}

func TestOpenPRHandler_UsesJobOrgIDWhenPayloadMissingOrgID(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()
	logger := zerolog.Nop()

	orgID := uuid.New()
	runID := uuid.New()
	mock.ExpectQuery("SELECT .* FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(context.Canceled)

	handler := newOpenPRHandler(stores, nil, logger)
	payload := json.RawMessage(`{"session_id":"` + runID.String() + `"}`)
	err := handler(withJobOrgID(context.Background(), orgID), "open_pr", payload)

	require.Error(t, err, "open_pr handler should return an error when run fetch fails")
	require.Contains(t, err.Error(), "fetch agent run", "open_pr handler should use org ID from job context before failing run fetch")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestAnalyzeFailureHandler_InvalidJSON(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()
	logger := zerolog.Nop()

	handler := newAnalyzeFailureHandler(stores, nil, logger)
	payload := json.RawMessage(`{bad json}`)
	err := handler(context.Background(), "analyze_failure", payload)

	require.Error(t, err, "analyze_failure handler should return an error for invalid JSON")
	require.Contains(t, err.Error(), "unmarshal analyze_failure payload", "error should indicate unmarshal failure")
}

func TestAnalyzeFailureHandler_UsesJobOrgIDWhenPayloadMissingOrgID(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()
	logger := zerolog.Nop()

	orgID := uuid.New()
	runID := uuid.New()
	mock.ExpectQuery("SELECT .* FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(context.Canceled)

	handler := newAnalyzeFailureHandler(stores, nil, logger)
	payload := json.RawMessage(`{"session_id":"` + runID.String() + `"}`)
	err := handler(withJobOrgID(context.Background(), orgID), "analyze_failure", payload)

	require.Error(t, err, "analyze_failure handler should return an error when run fetch fails")
	require.Contains(t, err.Error(), "fetch agent run", "analyze_failure handler should use org ID from job context before failing run fetch")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

type mockPMService struct {
	calledOrgID     uuid.UUID
	calledProjectID uuid.UUID
	trigger         models.PMTrigger
}

func (m *mockPMService) Analyze(ctx context.Context, orgID uuid.UUID, trigger models.PMTrigger, repoID *uuid.UUID) (*pm.Plan, error) {
	m.calledOrgID = orgID
	m.trigger = trigger
	return &pm.Plan{}, nil
}

func (m *mockPMService) AnalyzeProject(ctx context.Context, orgID, projectID uuid.UUID) error {
	m.calledOrgID = orgID
	m.calledProjectID = projectID
	return nil
}

func (m *mockPMService) RunBootstrap(ctx context.Context, orgID uuid.UUID) error {
	m.calledOrgID = orgID
	return nil
}

func (m *mockPMService) RunRefresh(ctx context.Context, orgID uuid.UUID) error {
	m.calledOrgID = orgID
	return nil
}

func TestPMAnalyzeHandler_InvalidJSON(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()
	logger := zerolog.Nop()

	services := &Services{PM: &mockPMService{}}
	handler := newPMAnalyzeHandler(stores, services, logger)

	err := handler(context.Background(), "pm_analyze", json.RawMessage(`{bad`))
	require.Error(t, err, "pm_analyze handler should return error for invalid JSON")
}

func TestPMAnalyzeHandler_UsesJobOrgID(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()
	logger := zerolog.Nop()

	pmSvc := &mockPMService{}
	services := &Services{PM: pmSvc}
	handler := newPMAnalyzeHandler(stores, services, logger)

	orgID := uuid.New()
	ctx := withJobOrgID(context.Background(), orgID)

	err := handler(ctx, "pm_analyze", json.RawMessage(`{"trigger":"cron"}`))
	require.NoError(t, err, "pm_analyze handler should succeed")
	require.Equal(t, orgID, pmSvc.calledOrgID, "should use org ID from job context")
	require.Equal(t, models.PMTriggerCron, pmSvc.trigger, "should pass trigger through")
}

func TestProjectCycleHandler_InvalidJSON(t *testing.T) {
	t.Parallel()

	logger := zerolog.Nop()
	services := &Services{PM: &mockPMService{}}
	handler := newProjectCycleHandler(services, logger)

	err := handler(context.Background(), "project_cycle", json.RawMessage(`{bad`))
	require.Error(t, err, "project_cycle handler should return error for invalid JSON")
	require.Contains(t, err.Error(), "unmarshal")
}

func TestProjectCycleHandler_InvalidProjectID(t *testing.T) {
	t.Parallel()

	logger := zerolog.Nop()
	services := &Services{PM: &mockPMService{}}
	handler := newProjectCycleHandler(services, logger)

	orgID := uuid.New()
	ctx := withJobOrgID(context.Background(), orgID)
	err := handler(ctx, "project_cycle", json.RawMessage(`{"org_id":"`+orgID.String()+`","project_id":"not-a-uuid"}`))
	require.Error(t, err, "project_cycle handler should return error for invalid project ID")
	require.Contains(t, err.Error(), "parse project ID")
}

func TestProjectCycleHandler_Success(t *testing.T) {
	t.Parallel()

	logger := zerolog.Nop()
	pmSvc := &mockPMService{}
	services := &Services{PM: pmSvc}
	handler := newProjectCycleHandler(services, logger)

	orgID := uuid.New()
	projectID := uuid.New()
	ctx := withJobOrgID(context.Background(), orgID)
	payload := json.RawMessage(`{"org_id":"` + orgID.String() + `","project_id":"` + projectID.String() + `"}`)

	err := handler(ctx, "project_cycle", payload)
	require.NoError(t, err, "project_cycle handler should succeed")
	require.Equal(t, orgID, pmSvc.calledOrgID, "should pass org ID to AnalyzeProject")
	require.Equal(t, projectID, pmSvc.calledProjectID, "should pass project ID to AnalyzeProject")
}

func TestRegisterHandlers_AllRegistered(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()
	logger := zerolog.Nop()

	w := New(nil, logger, "test-node")
	RegisterHandlers(w, stores, nil, DataRetentionConfig{}, logger)

	expectedHandlers := []string{
		"ingest_webhook",
		"sync_sentry",
		"sync_slack",
		"data_retention_cleanup",
	}
	for _, name := range expectedHandlers {
		_, ok := w.handlers[name]
		require.True(t, ok, "%s handler should be registered", name)
	}

	// pm_analyze and project_cycle should not be registered without PM service
	unexpectedWithoutPM := []string{
		"pm_analyze",
		"project_cycle",
	}
	for _, name := range unexpectedWithoutPM {
		_, ok := w.handlers[name]
		require.False(t, ok, "%s handler should not be registered without PM service", name)
	}

	// Now test with PM service — pm_analyze and project_cycle should be registered
	w2 := New(nil, logger, "test-node")
	RegisterHandlers(w2, stores, &Services{PM: &mockPMService{}}, DataRetentionConfig{}, logger)
	for _, name := range []string{"pm_analyze", "project_cycle"} {
		_, ok := w2.handlers[name]
		require.True(t, ok, "%s handler should be registered with PM service", name)
	}

	unexpectedHandlers := []string{
		"prioritize",
		"run_agent",
		"validate",
		"open_pr",
		"analyze_failure",
	}
	for _, name := range unexpectedHandlers {
		_, ok := w.handlers[name]
		require.False(t, ok, "%s handler should not be registered without services", name)
	}
}

func TestWorker_Register(t *testing.T) {
	t.Parallel()

	w := New(nil, zerolog.Nop(), "test-node")

	called := false
	handler := func(ctx context.Context, jobType string, payload json.RawMessage) error {
		called = true
		return nil
	}

	w.Register("test_job", handler)

	h, ok := w.handlers["test_job"]
	require.True(t, ok, "handler should be stored in the handlers map")
	require.NotNil(t, h, "handler function should not be nil")

	err := h(context.Background(), "test_job", nil)
	require.NoError(t, err, "handler invocation should succeed")
	require.True(t, called, "handler function should have been called")
}

type testFeedbackCommentStore struct {
	getByIDFn              func(ctx context.Context, orgID, id uuid.UUID) (models.ReviewComment, error)
	updateClassificationFn func(ctx context.Context, orgID, id uuid.UUID, filterStatus string, category *string, actionable, generalizable bool, generalizedRule, summary *string) error
}

func (m *testFeedbackCommentStore) Create(ctx context.Context, c *models.ReviewComment) error {
	return nil
}

func (m *testFeedbackCommentStore) GetByID(ctx context.Context, orgID, id uuid.UUID) (models.ReviewComment, error) {
	if m.getByIDFn != nil {
		return m.getByIDFn(ctx, orgID, id)
	}
	return models.ReviewComment{}, nil
}

func (m *testFeedbackCommentStore) UpdateClassification(ctx context.Context, orgID, id uuid.UUID, filterStatus string, category *string, actionable, generalizable bool, generalizedRule, summary *string) error {
	if m.updateClassificationFn != nil {
		return m.updateClassificationFn(ctx, orgID, id, filterStatus, category, actionable, generalizable, generalizedRule, summary)
	}
	return nil
}

func (m *testFeedbackCommentStore) MarkApplied(ctx context.Context, orgID, id uuid.UUID) error {
	return nil
}

func (m *testFeedbackCommentStore) ListActionableByPullRequest(ctx context.Context, orgID, prID uuid.UUID) ([]models.ReviewComment, error) {
	return nil, nil
}

type testFeedbackMemoryStore struct {
	createCalls int
}

func (m *testFeedbackMemoryStore) Create(ctx context.Context, p *models.Memory) error {
	m.createCalls++
	return nil
}

func (m *testFeedbackMemoryStore) GetByID(ctx context.Context, orgID, id uuid.UUID) (models.Memory, error) {
	return models.Memory{}, nil
}

func (m *testFeedbackMemoryStore) FindMatchingRule(ctx context.Context, orgID uuid.UUID, repo, normalizedRule string) (models.Memory, error) {
	return models.Memory{}, errors.New("not found")
}

func (m *testFeedbackMemoryStore) IncrementOccurrence(ctx context.Context, orgID, memoryID, commentID uuid.UUID) error {
	return nil
}

func (m *testFeedbackMemoryStore) ListActiveByRepo(ctx context.Context, orgID uuid.UUID, repo string) ([]models.Memory, error) {
	return nil, nil
}

func (m *testFeedbackMemoryStore) UpdateMemory(ctx context.Context, orgID, id uuid.UUID, rule *string, status *string) error {
	return nil
}

type testFeedbackJobStore struct{}

func (m *testFeedbackJobStore) Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	return uuid.New(), nil
}

func TestProcessReviewCommentHandler_SkipsPatternUpdateWhenCommentAlreadyProcessed(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	commentID := uuid.New()
	rule := "Always validate required input fields"
	category := "nit"

	commentStore := &testFeedbackCommentStore{
		getByIDFn: func(ctx context.Context, gotOrgID, gotCommentID uuid.UUID) (models.ReviewComment, error) {
			return models.ReviewComment{
				ID:              gotCommentID,
				OrgID:           gotOrgID,
				FilterStatus:    "accepted",
				Generalizable:   true,
				GeneralizedRule: &rule,
				Category:        &category,
			}, nil
		},
	}
	memoryStore := &testFeedbackMemoryStore{}
	feedbackService := feedback.NewService(commentStore, memoryStore, &testFeedbackJobStore{}, nil, zerolog.Nop())

	services := &Services{Feedback: feedbackService}
	handler := newProcessReviewCommentHandler(services, zerolog.Nop())
	payload := json.RawMessage(`{"comment_id":"` + commentID.String() + `","org_id":"` + orgID.String() + `","repo":"org/repo"}`)

	err := handler(context.Background(), "process_review_comment", payload)
	require.NoError(t, err, "process_review_comment handler should succeed for already processed comments")
	require.Equal(t, 0, memoryStore.createCalls, "process_review_comment should not update memories when comment was already processed")
}

// ---------------------------------------------------------------------------
// newUpdateMemoriesHandler tests
// ---------------------------------------------------------------------------

func TestUpdateMemoriesHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		payload   json.RawMessage
		expectErr bool
		errSubstr string
	}{
		{
			name:      "invalid JSON returns unmarshal error",
			payload:   json.RawMessage(`{bad json}`),
			expectErr: true,
			errSubstr: "unmarshal update_memories payload",
		},
		{
			name:      "missing org ID returns parse error",
			payload:   json.RawMessage(`{"comment_id":"` + uuid.New().String() + `","repo":"org/repo","rule":"use gofmt","category":"style"}`),
			expectErr: true,
			errSubstr: "parse org ID",
		},
		{
			name:      "invalid comment ID returns parse error",
			payload:   json.RawMessage(`{"comment_id":"not-a-uuid","org_id":"` + uuid.New().String() + `","repo":"org/repo","rule":"use gofmt","category":"style"}`),
			expectErr: true,
			errSubstr: "parse comment ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			commentStore := &testFeedbackCommentStore{}
			memoryStore := &testFeedbackMemoryStore{}
			feedbackService := feedback.NewService(commentStore, memoryStore, &testFeedbackJobStore{}, nil, zerolog.Nop())
			services := &Services{Feedback: feedbackService}

			handler := newUpdateMemoriesHandler(services, zerolog.Nop())
			err := handler(context.Background(), "update_memories", tt.payload)

			if tt.expectErr {
				require.Error(t, err, "handler should return error")
				require.Contains(t, err.Error(), tt.errSubstr, "error should contain expected substring")
			} else {
				require.NoError(t, err, "handler should succeed")
			}
		})
	}
}

func TestUpdateMemoriesHandler_Success(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	commentID := uuid.New()

	commentStore := &testFeedbackCommentStore{}
	memoryStore := &testFeedbackMemoryStore{}
	feedbackService := feedback.NewService(commentStore, memoryStore, &testFeedbackJobStore{}, nil, zerolog.Nop())

	services := &Services{Feedback: feedbackService}
	handler := newUpdateMemoriesHandler(services, zerolog.Nop())

	payload := json.RawMessage(`{"comment_id":"` + commentID.String() + `","org_id":"` + orgID.String() + `","repo":"org/repo","rule":"always use gofmt","category":"style"}`)
	err := handler(context.Background(), "update_memories", payload)
	require.NoError(t, err, "update_memories handler should succeed with valid payload")
	require.Equal(t, 1, memoryStore.createCalls, "should create a new memory")
}

func TestUpdateMemoriesHandler_UsesJobOrgID(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	commentID := uuid.New()

	commentStore := &testFeedbackCommentStore{}
	memoryStore := &testFeedbackMemoryStore{}
	feedbackService := feedback.NewService(commentStore, memoryStore, &testFeedbackJobStore{}, nil, zerolog.Nop())

	services := &Services{Feedback: feedbackService}
	handler := newUpdateMemoriesHandler(services, zerolog.Nop())

	ctx := withJobOrgID(context.Background(), orgID)
	payload := json.RawMessage(`{"comment_id":"` + commentID.String() + `","repo":"org/repo","rule":"always use gofmt","category":"style"}`)
	err := handler(ctx, "update_memories", payload)
	require.NoError(t, err, "update_memories should succeed using org ID from context")
	require.Equal(t, 1, memoryStore.createCalls, "should create a new memory")
}

// ---------------------------------------------------------------------------
// hasServiceHandlersDependencies tests
// ---------------------------------------------------------------------------

func TestHasServiceHandlersDependencies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		services *Services
		expected bool
	}{
		{
			name:     "nil services returns false",
			services: nil,
			expected: false,
		},
		{
			name:     "empty services returns false",
			services: &Services{},
			expected: false,
		},
		{
			name: "missing Orchestrator returns false",
			services: &Services{
				Validation:      &validation.Service{},
				PR:              &ghservice.PRService{},
				Failure:         &agent.FailureService{},
				SandboxProvider: &stubSandboxProvider{},
			},
			expected: false,
		},
		{
			name: "missing Validation returns false",
			services: &Services{
				Orchestrator:    &agent.Orchestrator{},
				PR:              &ghservice.PRService{},
				Failure:         &agent.FailureService{},
				SandboxProvider: &stubSandboxProvider{},
			},
			expected: false,
		},
		{
			name: "missing PR returns false",
			services: &Services{
				Orchestrator:    &agent.Orchestrator{},
				Validation:      &validation.Service{},
				Failure:         &agent.FailureService{},
				SandboxProvider: &stubSandboxProvider{},
			},
			expected: false,
		},
		{
			name: "missing Failure returns false",
			services: &Services{
				Orchestrator:    &agent.Orchestrator{},
				Validation:      &validation.Service{},
				PR:              &ghservice.PRService{},
				SandboxProvider: &stubSandboxProvider{},
			},
			expected: false,
		},
		{
			name: "missing SandboxProvider returns false",
			services: &Services{
				Orchestrator: &agent.Orchestrator{},
				Validation:   &validation.Service{},
				PR:           &ghservice.PRService{},
				Failure:      &agent.FailureService{},
			},
			expected: false,
		},
		{
			name: "all present returns true",
			services: &Services{
				Orchestrator:    &agent.Orchestrator{},
				Validation:      &validation.Service{},
				PR:              &ghservice.PRService{},
				Failure:         &agent.FailureService{},
				SandboxProvider: &stubSandboxProvider{},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := hasServiceHandlersDependencies(tt.services)
			require.Equal(t, tt.expected, result, "hasServiceHandlersDependencies should return expected result")
		})
	}
}

// stubSandboxProvider satisfies the agent.SandboxProvider interface for testing hasServiceHandlersDependencies.
type stubSandboxProvider struct{}

func (s *stubSandboxProvider) Name() string { return "stub" }
func (s *stubSandboxProvider) Create(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
	return nil, nil
}
func (s *stubSandboxProvider) CloneRepo(ctx context.Context, sb *agent.Sandbox, repoURL, branch, token string) error {
	return nil
}
func (s *stubSandboxProvider) Exec(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
	return 0, nil
}
func (s *stubSandboxProvider) ReadFile(ctx context.Context, sb *agent.Sandbox, path string) ([]byte, error) {
	return nil, nil
}
func (s *stubSandboxProvider) WriteFile(ctx context.Context, sb *agent.Sandbox, path string, data []byte) error {
	return nil
}
func (s *stubSandboxProvider) Destroy(ctx context.Context, sb *agent.Sandbox) error {
	return nil
}
func (s *stubSandboxProvider) ConnectionInfo(ctx context.Context, sb *agent.Sandbox) (*agent.SandboxConnectionInfo, error) {
	return nil, nil
}
func (s *stubSandboxProvider) Snapshot(ctx context.Context, sb *agent.Sandbox) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}
func (s *stubSandboxProvider) Restore(ctx context.Context, sb *agent.Sandbox, reader io.Reader) error {
	return nil
}
func (s *stubSandboxProvider) ExecStream(ctx context.Context, sb *agent.Sandbox, cmd string, onLine func(line []byte), stderr io.Writer) (int, error) {
	return 0, nil
}

// ---------------------------------------------------------------------------
// RegisterHandlers with full services tests
// ---------------------------------------------------------------------------

func TestRegisterHandlers_WithAllServices(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()
	logger := zerolog.Nop()

	services := &Services{
		Orchestrator:    &agent.Orchestrator{},
		Validation:      &validation.Service{},
		PR:              &ghservice.PRService{},
		Failure:         &agent.FailureService{},
		SandboxProvider: &stubSandboxProvider{},
		Prioritization:  &prioritization.Service{},
		Feedback:        feedback.NewService(&testFeedbackCommentStore{}, &testFeedbackMemoryStore{}, &testFeedbackJobStore{}, nil, zerolog.Nop()),
		PM:              &mockPMService{},
	}

	w := New(nil, logger, "test-node")
	RegisterHandlers(w, stores, services, DataRetentionConfig{}, logger)

	allExpected := []string{
		"ingest_webhook",
		"sync_sentry",
		"sync_slack",
		"prioritize",
		"pm_analyze",
		"run_agent",
		"validate",
		"open_pr",
		"analyze_failure",
		"process_review_comment",
		"update_memories",
		"data_retention_cleanup",
	}
	for _, name := range allExpected {
		_, ok := w.handlers[name]
		require.True(t, ok, "%s handler should be registered when all services are provided", name)
	}
}

func TestRegisterHandlers_WithOnlyPrioritization(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()
	logger := zerolog.Nop()

	services := &Services{
		Prioritization: &prioritization.Service{},
	}

	w := New(nil, logger, "test-node")
	RegisterHandlers(w, stores, services, DataRetentionConfig{}, logger)

	_, ok := w.handlers["prioritize"]
	require.True(t, ok, "prioritize handler should be registered")
	_, ok = w.handlers["run_agent"]
	require.False(t, ok, "run_agent handler should not be registered without orchestrator dependencies")
	_, ok = w.handlers["process_review_comment"]
	require.False(t, ok, "process_review_comment handler should not be registered without feedback service")
}

func TestRegisterHandlers_WithOnlyFeedback(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()
	logger := zerolog.Nop()

	feedbackService := feedback.NewService(&testFeedbackCommentStore{}, &testFeedbackMemoryStore{}, &testFeedbackJobStore{}, nil, zerolog.Nop())
	services := &Services{
		Feedback: feedbackService,
	}

	w := New(nil, logger, "test-node")
	RegisterHandlers(w, stores, services, DataRetentionConfig{}, logger)

	_, ok := w.handlers["process_review_comment"]
	require.True(t, ok, "process_review_comment handler should be registered")
	_, ok = w.handlers["update_memories"]
	require.True(t, ok, "update_memories handler should be registered")
	_, ok = w.handlers["prioritize"]
	require.False(t, ok, "prioritize handler should not be registered without prioritization service")
}

func TestRegisterHandlers_WithOnlyPM(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()
	logger := zerolog.Nop()

	services := &Services{
		PM: &mockPMService{},
	}

	w := New(nil, logger, "test-node")
	RegisterHandlers(w, stores, services, DataRetentionConfig{}, logger)

	_, ok := w.handlers["pm_analyze"]
	require.True(t, ok, "pm_analyze handler should be registered")
	_, ok = w.handlers["prioritize"]
	require.False(t, ok, "prioritize handler should not be registered without prioritization service")
}

// ---------------------------------------------------------------------------
// Additional PMAnalyze handler tests
// ---------------------------------------------------------------------------

func TestPMAnalyzeHandler_InvalidTrigger(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()
	logger := zerolog.Nop()

	pmSvc := &mockPMService{}
	services := &Services{PM: pmSvc}
	handler := newPMAnalyzeHandler(stores, services, logger)

	orgID := uuid.New()
	payload := json.RawMessage(`{"org_id":"` + orgID.String() + `","trigger":"invalid_trigger"}`)
	err := handler(context.Background(), "pm_analyze", payload)
	require.Error(t, err, "pm_analyze handler should return error for invalid trigger")
	require.Contains(t, err.Error(), "invalid trigger", "error should mention invalid trigger")
}

func TestPMAnalyzeHandler_WithRepoID(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()
	logger := zerolog.Nop()

	pmSvc := &mockPMService{}
	services := &Services{PM: pmSvc}
	handler := newPMAnalyzeHandler(stores, services, logger)

	orgID := uuid.New()
	repoID := uuid.New()
	payload := json.RawMessage(`{"org_id":"` + orgID.String() + `","trigger":"manual","repo_id":"` + repoID.String() + `"}`)
	err := handler(context.Background(), "pm_analyze", payload)
	require.NoError(t, err, "pm_analyze handler should succeed with repo ID")
	require.Equal(t, orgID, pmSvc.calledOrgID, "should pass org ID through")
	require.Equal(t, models.PMTriggerManual, pmSvc.trigger, "should pass manual trigger through")
}

func TestPMAnalyzeHandler_InvalidRepoID(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()
	logger := zerolog.Nop()

	pmSvc := &mockPMService{}
	services := &Services{PM: pmSvc}
	handler := newPMAnalyzeHandler(stores, services, logger)

	orgID := uuid.New()
	payload := json.RawMessage(`{"org_id":"` + orgID.String() + `","trigger":"cron","repo_id":"not-a-uuid"}`)
	err := handler(context.Background(), "pm_analyze", payload)
	require.Error(t, err, "pm_analyze handler should return error for invalid repo ID")
	require.Contains(t, err.Error(), "parse repo ID", "error should mention repo ID")
}

func TestPMAnalyzeHandler_DefaultTrigger(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()
	logger := zerolog.Nop()

	pmSvc := &mockPMService{}
	services := &Services{PM: pmSvc}
	handler := newPMAnalyzeHandler(stores, services, logger)

	orgID := uuid.New()
	payload := json.RawMessage(`{"org_id":"` + orgID.String() + `"}`)
	err := handler(context.Background(), "pm_analyze", payload)
	require.NoError(t, err, "pm_analyze handler should succeed with default trigger")
	require.Equal(t, models.PMTriggerCron, pmSvc.trigger, "empty trigger should default to cron")
}

func TestPMAnalyzeHandler_MissingOrgID(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()
	logger := zerolog.Nop()

	pmSvc := &mockPMService{}
	services := &Services{PM: pmSvc}
	handler := newPMAnalyzeHandler(stores, services, logger)

	payload := json.RawMessage(`{"trigger":"cron"}`)
	err := handler(context.Background(), "pm_analyze", payload)
	require.Error(t, err, "pm_analyze handler should return error when org ID is missing")
	require.Contains(t, err.Error(), "parse org ID", "error should mention org ID")
}

type mockPMServiceError struct{}

func (m *mockPMServiceError) Analyze(ctx context.Context, orgID uuid.UUID, trigger models.PMTrigger, repoID *uuid.UUID) (*pm.Plan, error) {
	return nil, errors.New("pm analysis failed")
}

func (m *mockPMServiceError) AnalyzeProject(ctx context.Context, orgID, projectID uuid.UUID) error {
	return errors.New("project analysis failed")
}

func (m *mockPMServiceError) RunBootstrap(ctx context.Context, orgID uuid.UUID) error {
	return errors.New("bootstrap failed")
}

func (m *mockPMServiceError) RunRefresh(ctx context.Context, orgID uuid.UUID) error {
	return errors.New("refresh failed")
}

func TestPMAnalyzeHandler_ServiceError(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()
	logger := zerolog.Nop()

	services := &Services{PM: &mockPMServiceError{}}
	handler := newPMAnalyzeHandler(stores, services, logger)

	orgID := uuid.New()
	payload := json.RawMessage(`{"org_id":"` + orgID.String() + `","trigger":"cron"}`)
	err := handler(context.Background(), "pm_analyze", payload)
	require.Error(t, err, "pm_analyze handler should return error when service fails")
	require.Contains(t, err.Error(), "pm analysis failed", "error should contain service error message")
}

// ---------------------------------------------------------------------------
// Additional ProcessReviewComment handler tests
// ---------------------------------------------------------------------------

func TestProcessReviewCommentHandler_InvalidJSON(t *testing.T) {
	t.Parallel()

	feedbackService := feedback.NewService(&testFeedbackCommentStore{}, &testFeedbackMemoryStore{}, &testFeedbackJobStore{}, nil, zerolog.Nop())
	services := &Services{Feedback: feedbackService}

	handler := newProcessReviewCommentHandler(services, zerolog.Nop())
	err := handler(context.Background(), "process_review_comment", json.RawMessage(`{bad`))
	require.Error(t, err, "should return error for invalid JSON")
	require.Contains(t, err.Error(), "unmarshal process_review_comment payload", "error should indicate unmarshal failure")
}

func TestProcessReviewCommentHandler_InvalidOrgID(t *testing.T) {
	t.Parallel()

	feedbackService := feedback.NewService(&testFeedbackCommentStore{}, &testFeedbackMemoryStore{}, &testFeedbackJobStore{}, nil, zerolog.Nop())
	services := &Services{Feedback: feedbackService}

	handler := newProcessReviewCommentHandler(services, zerolog.Nop())
	payload := json.RawMessage(`{"comment_id":"` + uuid.New().String() + `","org_id":"not-a-uuid"}`)
	err := handler(context.Background(), "process_review_comment", payload)
	require.Error(t, err, "should return error for invalid org ID")
	require.Contains(t, err.Error(), "parse org ID", "error should mention org ID")
}

func TestProcessReviewCommentHandler_InvalidCommentID(t *testing.T) {
	t.Parallel()

	feedbackService := feedback.NewService(&testFeedbackCommentStore{}, &testFeedbackMemoryStore{}, &testFeedbackJobStore{}, nil, zerolog.Nop())
	services := &Services{Feedback: feedbackService}

	handler := newProcessReviewCommentHandler(services, zerolog.Nop())
	payload := json.RawMessage(`{"comment_id":"not-a-uuid","org_id":"` + uuid.New().String() + `"}`)
	err := handler(context.Background(), "process_review_comment", payload)
	require.Error(t, err, "should return error for invalid comment ID")
	require.Contains(t, err.Error(), "parse comment ID", "error should mention comment ID")
}

func TestProcessReviewCommentHandler_WithPendingComment(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	commentID := uuid.New()
	rule := "Always validate required input fields"
	category := "nit"

	callCount := 0
	commentStore := &testFeedbackCommentStore{
		getByIDFn: func(ctx context.Context, gotOrgID, gotCommentID uuid.UUID) (models.ReviewComment, error) {
			callCount++
			return models.ReviewComment{
				ID:              gotCommentID,
				OrgID:           gotOrgID,
				FilterStatus:    "pending",
				Generalizable:   true,
				GeneralizedRule: &rule,
				Category:        &category,
			}, nil
		},
	}
	memoryStore := &testFeedbackMemoryStore{}
	feedbackService := feedback.NewService(commentStore, memoryStore, &testFeedbackJobStore{}, nil, zerolog.Nop())

	services := &Services{Feedback: feedbackService}
	handler := newProcessReviewCommentHandler(services, zerolog.Nop())
	payload := json.RawMessage(`{"comment_id":"` + commentID.String() + `","org_id":"` + orgID.String() + `","repo":"org/repo"}`)

	err := handler(context.Background(), "process_review_comment", payload)
	require.NoError(t, err, "handler should succeed for pending comment")
	require.Equal(t, 1, memoryStore.createCalls, "should create a new memory for pending generalizable comment")
}

func TestProcessReviewCommentHandler_NoRepoSkipsPatterns(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	commentID := uuid.New()

	commentStore := &testFeedbackCommentStore{
		getByIDFn: func(ctx context.Context, gotOrgID, gotCommentID uuid.UUID) (models.ReviewComment, error) {
			return models.ReviewComment{
				ID:           gotCommentID,
				OrgID:        gotOrgID,
				FilterStatus: "pending",
			}, nil
		},
	}
	memoryStore := &testFeedbackMemoryStore{}
	feedbackService := feedback.NewService(commentStore, memoryStore, &testFeedbackJobStore{}, nil, zerolog.Nop())

	services := &Services{Feedback: feedbackService}
	handler := newProcessReviewCommentHandler(services, zerolog.Nop())
	payload := json.RawMessage(`{"comment_id":"` + commentID.String() + `","org_id":"` + orgID.String() + `"}`)

	err := handler(context.Background(), "process_review_comment", payload)
	require.NoError(t, err, "handler should succeed without repo")
	require.Equal(t, 0, memoryStore.createCalls, "should not create memories when no repo is provided")
}

func TestProcessReviewCommentHandler_GetCommentError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	commentID := uuid.New()

	commentStore := &testFeedbackCommentStore{
		getByIDFn: func(ctx context.Context, gotOrgID, gotCommentID uuid.UUID) (models.ReviewComment, error) {
			return models.ReviewComment{}, errors.New("db connection lost")
		},
	}
	memoryStore := &testFeedbackMemoryStore{}
	feedbackService := feedback.NewService(commentStore, memoryStore, &testFeedbackJobStore{}, nil, zerolog.Nop())

	services := &Services{Feedback: feedbackService}
	handler := newProcessReviewCommentHandler(services, zerolog.Nop())
	payload := json.RawMessage(`{"comment_id":"` + commentID.String() + `","org_id":"` + orgID.String() + `","repo":"org/repo"}`)

	err := handler(context.Background(), "process_review_comment", payload)
	require.Error(t, err, "handler should return error when get comment fails")
}

// ---------------------------------------------------------------------------
// Additional validate, open_pr, analyze_failure, run_agent handler tests
// ---------------------------------------------------------------------------

func TestValidateHandler_SessionFetchError(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	mock.ExpectQuery("SELECT .* FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("session not found"))

	handler := newValidateHandler(stores, nil, zerolog.Nop())
	payload := json.RawMessage(`{"session_id":"` + runID.String() + `","org_id":"` + orgID.String() + `"}`)
	err := handler(context.Background(), "validate", payload)
	require.Error(t, err, "validate handler should return error when session fetch fails")
	require.Contains(t, err.Error(), "fetch agent run", "error should mention run fetch")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestValidateHandler_InvalidOrgID(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()

	handler := newValidateHandler(stores, nil, zerolog.Nop())
	payload := json.RawMessage(`{"session_id":"` + uuid.New().String() + `","org_id":"not-a-uuid"}`)
	err := handler(context.Background(), "validate", payload)
	require.Error(t, err, "validate handler should return error for invalid org ID")
	require.Contains(t, err.Error(), "parse org ID", "error should mention org ID")
}

func TestValidateHandler_InvalidRunID(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()

	handler := newValidateHandler(stores, nil, zerolog.Nop())
	payload := json.RawMessage(`{"session_id":"not-a-uuid","org_id":"` + uuid.New().String() + `"}`)
	err := handler(context.Background(), "validate", payload)
	require.Error(t, err, "validate handler should return error for invalid run ID")
	require.Contains(t, err.Error(), "parse agent run ID", "error should mention run ID")
}

func TestValidateHandler_MissingOrgID(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()

	handler := newValidateHandler(stores, nil, zerolog.Nop())
	payload := json.RawMessage(`{"session_id":"` + uuid.New().String() + `"}`)
	err := handler(context.Background(), "validate", payload)
	require.Error(t, err, "validate handler should return error when org ID is missing from payload and context")
	require.Contains(t, err.Error(), "parse org ID", "error should mention org ID")
}

func TestOpenPRHandler_InvalidOrgID(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()

	handler := newOpenPRHandler(stores, nil, zerolog.Nop())
	payload := json.RawMessage(`{"session_id":"` + uuid.New().String() + `","org_id":"not-a-uuid"}`)
	err := handler(context.Background(), "open_pr", payload)
	require.Error(t, err, "open_pr handler should return error for invalid org ID")
	require.Contains(t, err.Error(), "parse org ID", "error should mention org ID")
}

func TestOpenPRHandler_InvalidRunID(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()

	handler := newOpenPRHandler(stores, nil, zerolog.Nop())
	payload := json.RawMessage(`{"session_id":"not-a-uuid","org_id":"` + uuid.New().String() + `"}`)
	err := handler(context.Background(), "open_pr", payload)
	require.Error(t, err, "open_pr handler should return error for invalid run ID")
	require.Contains(t, err.Error(), "parse agent run ID", "error should mention run ID")
}

func TestAnalyzeFailureHandler_InvalidOrgID(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()

	handler := newAnalyzeFailureHandler(stores, nil, zerolog.Nop())
	payload := json.RawMessage(`{"session_id":"` + uuid.New().String() + `","org_id":"not-a-uuid"}`)
	err := handler(context.Background(), "analyze_failure", payload)
	require.Error(t, err, "analyze_failure handler should return error for invalid org ID")
	require.Contains(t, err.Error(), "parse org ID", "error should mention org ID")
}

func TestAnalyzeFailureHandler_InvalidRunID(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()

	handler := newAnalyzeFailureHandler(stores, nil, zerolog.Nop())
	payload := json.RawMessage(`{"session_id":"not-a-uuid","org_id":"` + uuid.New().String() + `"}`)
	err := handler(context.Background(), "analyze_failure", payload)
	require.Error(t, err, "analyze_failure handler should return error for invalid run ID")
	require.Contains(t, err.Error(), "parse agent run ID", "error should mention run ID")
}

func TestRunAgentHandler_MissingOrgID(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()

	handler := newRunAgentHandler(stores, nil, zerolog.Nop())
	payload := json.RawMessage(`{"session_id":"` + uuid.New().String() + `"}`)
	err := handler(context.Background(), "run_agent", payload)
	require.Error(t, err, "run_agent handler should return error when org ID is missing")
	require.Contains(t, err.Error(), "parse org ID", "error should mention org ID")
}

func TestRunAgentHandler_FetchRunError(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	mock.ExpectQuery("SELECT .* FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("session not found"))

	handler := newRunAgentHandler(stores, nil, zerolog.Nop())
	payload := json.RawMessage(`{"session_id":"` + runID.String() + `","org_id":"` + orgID.String() + `"}`)
	err := handler(context.Background(), "run_agent", payload)
	require.Error(t, err, "run_agent handler should return error when session fetch fails")
	require.Contains(t, err.Error(), "fetch agent run", "error should mention run fetch")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// ---------------------------------------------------------------------------
// parseOrgID additional tests
// ---------------------------------------------------------------------------

func TestParseOrgID_FromPayload(t *testing.T) {
	t.Parallel()

	expected := uuid.New()
	got, err := parseOrgID(expected.String(), context.Background())
	require.NoError(t, err, "parseOrgID should succeed with valid UUID")
	require.Equal(t, expected, got, "should return parsed UUID")
}

func TestParseOrgID_InvalidPayloadUUID(t *testing.T) {
	t.Parallel()

	_, err := parseOrgID("not-a-uuid", context.Background())
	require.Error(t, err, "parseOrgID should fail for invalid UUID")
}

func TestParseOrgID_FromContext(t *testing.T) {
	t.Parallel()

	expected := uuid.New()
	ctx := withJobOrgID(context.Background(), expected)
	got, err := parseOrgID("", ctx)
	require.NoError(t, err, "parseOrgID should succeed with org ID in context")
	require.Equal(t, expected, got, "should return org ID from context")
}

func TestParseOrgID_MissingEverywhere(t *testing.T) {
	t.Parallel()

	_, err := parseOrgID("", context.Background())
	require.Error(t, err, "parseOrgID should fail when org ID is missing from both payload and context")
	require.Contains(t, err.Error(), "missing org ID", "error should indicate missing org ID")
}

// ---------------------------------------------------------------------------
// Sync sentry handler: list integrations DB error
// ---------------------------------------------------------------------------

func TestSyncSentryHandler_ListIntegrationsError(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()
	logger := zerolog.Nop()

	orgID := uuid.New()
	mock.ExpectQuery("SELECT .* FROM integrations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("db error"))

	handler := newSyncSentryHandler(stores, logger)
	payload := json.RawMessage(`{"org_id":"` + orgID.String() + `"}`)
	err := handler(context.Background(), "sync_sentry", payload)
	require.Error(t, err, "sync_sentry handler should return error when list integrations fails")
	require.Contains(t, err.Error(), "list sentry integrations", "error should mention listing integrations")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// ---------------------------------------------------------------------------
// Prioritize handler: uses org ID from context
// ---------------------------------------------------------------------------

func TestPrioritizeHandler_MissingOrgID(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()

	issueID := uuid.New()
	handler := newPrioritizeHandler(stores, &Services{}, zerolog.Nop())

	payload := json.RawMessage(`{"issue_id":"` + issueID.String() + `"}`)
	err := handler(context.Background(), "prioritize", payload)
	require.Error(t, err, "prioritize handler should fail when org ID is missing")
	require.Contains(t, err.Error(), "parse org ID", "error should mention org ID")
}

func TestPrioritizeHandler_InvalidIssueID(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()

	orgID := uuid.New()
	handler := newPrioritizeHandler(stores, &Services{}, zerolog.Nop())

	payload := json.RawMessage(`{"issue_id":"not-a-uuid","org_id":"` + orgID.String() + `"}`)
	err := handler(context.Background(), "prioritize", payload)
	require.Error(t, err, "prioritize handler should fail for invalid issue ID")
	require.Contains(t, err.Error(), "parse issue ID", "error should mention issue ID")
}

// ---------------------------------------------------------------------------
// Data retention cleanup handler tests
// ---------------------------------------------------------------------------

func newRetentionTestStores(t *testing.T) (*Stores, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	stores := &Stores{
		Webhooks:    db.NewWebhookDeliveryStore(mock),
		SessionLogs: db.NewSessionLogStore(mock),
		Jobs:        db.NewJobStore(mock),
	}
	return stores, mock
}

func TestDataRetentionHandler_AllStoresSucceed(t *testing.T) {
	t.Parallel()

	stores, mock := newRetentionTestStores(t)
	defer mock.Close()

	cfg := DataRetentionConfig{WebhookDays: 30, LogsDays: 90, JobsDays: 30}

	mock.ExpectQuery("SELECT delete_expired_webhook_deliveries").
		WithArgs(30).
		WillReturnRows(pgxmock.NewRows([]string{"delete_expired_webhook_deliveries"}).AddRow(int64(5)))
	mock.ExpectQuery("SELECT delete_expired_session_logs").
		WithArgs(90).
		WillReturnRows(pgxmock.NewRows([]string{"delete_expired_session_logs"}).AddRow(int64(10)))
	mock.ExpectQuery("SELECT delete_expired_completed_jobs").
		WithArgs(30).
		WillReturnRows(pgxmock.NewRows([]string{"delete_expired_completed_jobs"}).AddRow(int64(3)))

	handler := newDataRetentionCleanupHandler(stores, cfg, zerolog.Nop())
	err := handler(context.Background(), "data_retention_cleanup", nil)
	require.NoError(t, err, "handler should succeed when all stores succeed")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDataRetentionHandler_ReturnsErrorOnFailure(t *testing.T) {
	t.Parallel()

	stores, mock := newRetentionTestStores(t)
	defer mock.Close()

	cfg := DataRetentionConfig{WebhookDays: 30, LogsDays: 90, JobsDays: 30}

	mock.ExpectQuery("SELECT delete_expired_webhook_deliveries").
		WithArgs(30).
		WillReturnError(errors.New("db connection lost"))
	mock.ExpectQuery("SELECT delete_expired_session_logs").
		WithArgs(90).
		WillReturnRows(pgxmock.NewRows([]string{"delete_expired_session_logs"}).AddRow(int64(0)))
	mock.ExpectQuery("SELECT delete_expired_completed_jobs").
		WithArgs(30).
		WillReturnRows(pgxmock.NewRows([]string{"delete_expired_completed_jobs"}).AddRow(int64(0)))

	handler := newDataRetentionCleanupHandler(stores, cfg, zerolog.Nop())
	err := handler(context.Background(), "data_retention_cleanup", nil)
	require.Error(t, err, "handler should return error when a store fails")
	require.Contains(t, err.Error(), "delete expired webhook deliveries")
}

func TestDataRetentionHandler_SkipsNilStores(t *testing.T) {
	t.Parallel()

	stores := &Stores{} // all nil
	cfg := DataRetentionConfig{WebhookDays: 30, LogsDays: 90, JobsDays: 30}

	handler := newDataRetentionCleanupHandler(stores, cfg, zerolog.Nop())
	err := handler(context.Background(), "data_retention_cleanup", nil)
	require.NoError(t, err, "handler should succeed with nil stores")
}

func TestDataRetentionHandler_SkipsZeroRetentionDays(t *testing.T) {
	t.Parallel()

	stores, mock := newRetentionTestStores(t)
	defer mock.Close()

	cfg := DataRetentionConfig{WebhookDays: 0, LogsDays: 0, JobsDays: 0}

	handler := newDataRetentionCleanupHandler(stores, cfg, zerolog.Nop())
	err := handler(context.Background(), "data_retention_cleanup", nil)
	require.NoError(t, err, "handler should skip cleanup when retention days are 0")
	require.NoError(t, mock.ExpectationsWereMet(), "no DB calls should be made")
}

// --- Eval handler tests ---

var evalRunTestCols = []string{
	"id", "task_id", "org_id", "batch_id",
	"input_manifest", "model", "server_deploy_sha", "pm_document_set_pin_id",
	"config_ref", "context_overrides",
	"agent_diff", "agent_trace", "token_usage",
	"criterion_results", "final_score", "passed",
	"status", "duration_seconds", "sandbox_id",
	"started_at", "completed_at", "error_message", "created_at",
}

var evalTaskTestCols = []string{
	"id", "org_id", "repo_id", "name", "description",
	"base_commit_sha", "solution_commit_sha", "solution_diff",
	"issue_description", "issue_context",
	"server_deploy_sha", "pm_document_set_pin_id", "org_settings_version_id",
	"memory_snapshot", "sandbox_image_digest", "context_overrides",
	"scoring_criteria", "pass_threshold",
	"source", "source_pr_number", "complexity", "tags",
	"created_by", "created_at", "updated_at", "archived_at",
}

func newEvalTestStores(t *testing.T) (*Stores, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	return &Stores{
		EvalTasks:      db.NewEvalTaskStore(mock),
		EvalRuns:       db.NewEvalRunStore(mock),
		EvalBatches:    db.NewEvalBatchStore(mock),
		EvalBootstraps: db.NewEvalBootstrapStore(mock),
		Repositories:   db.NewRepositoryStore(mock),
	}, mock
}

func evalRunRow(runID, taskID, orgID uuid.UUID, now time.Time) []interface{} {
	return []interface{}{
		runID, taskID, orgID, nil,
		nil, "claude-sonnet-4-6", nil, nil,
		nil, json.RawMessage(`{}`),
		nil, nil, nil,
		nil, nil, nil,
		"pending", nil, nil,
		nil, nil, nil, now,
	}
}

func evalTaskRow(taskID, orgID uuid.UUID, now time.Time, criteria json.RawMessage) []interface{} {
	return []interface{}{
		taskID, orgID, uuid.New(), "Test Task", "desc",
		"abc123", nil, nil,
		"Fix the bug", json.RawMessage(`{}`),
		nil, nil, nil,
		nil, nil, json.RawMessage(`{}`),
		criteria, 0.7,
		"manual", nil, "moderate", []string{"test"},
		nil, now, now, nil,
	}
}

func TestExecuteEvalRun(t *testing.T) {
	t.Parallel()

	t.Run("returns failed with placeholder message for valid criteria", func(t *testing.T) {
		t.Parallel()

		run := &models.EvalRun{Model: "claude-sonnet-4-6"}
		task := &models.EvalTask{
			ScoringCriteria: json.RawMessage(`[{"name":"tests_pass","grader_type":"code_check","weight":1.0}]`),
		}
		logger := zerolog.Nop()

		result := executeEvalRun(context.Background(), &Stores{}, &Services{}, run, task, logger)
		require.Equal(t, models.EvalRunStatusFailed, result.Status)
		require.NotNil(t, result.ErrorMessage)
		require.Contains(t, *result.ErrorMessage, "not yet implemented")
	})

	t.Run("returns failed on invalid scoring criteria JSON", func(t *testing.T) {
		t.Parallel()

		run := &models.EvalRun{Model: "claude-sonnet-4-6"}
		task := &models.EvalTask{
			ScoringCriteria: json.RawMessage(`not valid json`),
		}
		logger := zerolog.Nop()

		result := executeEvalRun(context.Background(), &Stores{}, &Services{}, run, task, logger)
		require.Equal(t, models.EvalRunStatusFailed, result.Status)
		require.NotNil(t, result.ErrorMessage)
		require.Contains(t, *result.ErrorMessage, "failed to parse scoring criteria")
	})

	t.Run("returns failed when repository store is nil", func(t *testing.T) {
		t.Parallel()

		run := &models.EvalRun{Model: "claude-sonnet-4-6"}
		task := &models.EvalTask{
			ScoringCriteria: json.RawMessage(`[]`),
		}
		logger := zerolog.Nop()

		result := executeEvalRun(context.Background(), &Stores{}, &Services{}, run, task, logger)
		require.Equal(t, models.EvalRunStatusFailed, result.Status)
		require.NotNil(t, result.ErrorMessage)
		require.Contains(t, *result.ErrorMessage, "repository store not configured")
	})

	t.Run("returns failed when sandbox provider is nil", func(t *testing.T) {
		t.Parallel()

		stores, mock := newEvalTestStores(t)
		defer mock.Close()

		orgID := uuid.New()
		repoID := uuid.New()
		run := &models.EvalRun{Model: "claude-sonnet-4-6"}
		task := &models.EvalTask{
			OrgID:           orgID,
			RepoID:          repoID,
			ScoringCriteria: json.RawMessage(`[]`),
		}
		logger := zerolog.Nop()

		// Mock repository lookup
		mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "integration_id", "github_id", "full_name", "default_branch", "private", "language", "description", "clone_url", "installation_id", "status", "last_synced_at", "context_quality", "settings", "created_at", "updated_at"}).
				AddRow(repoID, orgID, uuid.New(), int64(123), "org/repo", "main", false, nil, nil, "https://github.com/org/repo.git", int64(456), "active", nil, nil, json.RawMessage(`{}`), time.Now(), time.Now()))

		result := executeEvalRun(context.Background(), stores, &Services{}, run, task, logger)
		require.Equal(t, models.EvalRunStatusFailed, result.Status)
		require.NotNil(t, result.ErrorMessage)
		require.Contains(t, *result.ErrorMessage, "sandbox provider not configured")
	})
}

func TestRunEvalHandler(t *testing.T) {
	t.Parallel()

	t.Run("invalid JSON payload returns error", func(t *testing.T) {
		t.Parallel()

		stores, mock := newEvalTestStores(t)
		defer mock.Close()

		handler := newRunEvalHandler(stores, &Services{}, zerolog.Nop())
		err := handler(context.Background(), "run_eval", json.RawMessage(`{invalid`))
		require.Error(t, err)
		require.Contains(t, err.Error(), "unmarshal run_eval payload")
	})

	t.Run("missing org ID returns error", func(t *testing.T) {
		t.Parallel()

		stores, mock := newEvalTestStores(t)
		defer mock.Close()

		payload := json.RawMessage(`{"eval_run_id":"` + uuid.New().String() + `"}`)
		handler := newRunEvalHandler(stores, &Services{}, zerolog.Nop())
		err := handler(context.Background(), "run_eval", payload)
		require.Error(t, err)
		require.Contains(t, err.Error(), "parse org ID")
	})

	t.Run("invalid eval run ID returns error", func(t *testing.T) {
		t.Parallel()

		stores, mock := newEvalTestStores(t)
		defer mock.Close()

		payload := json.RawMessage(`{"eval_run_id":"not-a-uuid","org_id":"` + uuid.New().String() + `"}`)
		handler := newRunEvalHandler(stores, &Services{}, zerolog.Nop())
		err := handler(context.Background(), "run_eval", payload)
		require.Error(t, err)
		require.Contains(t, err.Error(), "parse eval run ID")
	})

	t.Run("successful run executes full lifecycle", func(t *testing.T) {
		t.Parallel()

		stores, mock := newEvalTestStores(t)
		defer mock.Close()

		orgID := uuid.New()
		runID := uuid.New()
		taskID := uuid.New()
		now := time.Now()

		// GetByID for run
		mock.ExpectQuery("SELECT .+ FROM eval_runs WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(evalRunTestCols).AddRow(evalRunRow(runID, taskID, orgID, now)...))

		// GetByID for task
		mock.ExpectQuery("SELECT .+ FROM eval_tasks WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(evalTaskTestCols).AddRow(
				evalTaskRow(taskID, orgID, now, json.RawMessage(`[]`))...))

		// UpdateStatus to running
		mock.ExpectExec("UPDATE eval_runs SET status").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		// UpdateResult
		mock.ExpectExec("UPDATE eval_runs SET").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		payload, _ := json.Marshal(map[string]string{
			"eval_run_id": runID.String(),
			"org_id":      orgID.String(),
		})

		handler := newRunEvalHandler(stores, &Services{}, zerolog.Nop())
		err := handler(context.Background(), "run_eval", payload)
		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("batch run completes batch when all runs done", func(t *testing.T) {
		t.Parallel()

		stores, mock := newEvalTestStores(t)
		defer mock.Close()

		orgID := uuid.New()
		runID := uuid.New()
		taskID := uuid.New()
		batchID := uuid.New()
		now := time.Now()

		// GetByID for run — this time with a batch_id set
		runRow := evalRunRow(runID, taskID, orgID, now)
		runRow[3] = &batchID // batch_id field
		mock.ExpectQuery("SELECT .+ FROM eval_runs WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(evalRunTestCols).AddRow(runRow...))

		// GetByID for task
		mock.ExpectQuery("SELECT .+ FROM eval_tasks WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(evalTaskTestCols).AddRow(
				evalTaskRow(taskID, orgID, now, json.RawMessage(`[]`))...))

		// UpdateStatus to running
		mock.ExpectExec("UPDATE eval_runs SET status").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		// UpdateResult
		mock.ExpectExec("UPDATE eval_runs SET").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		// CompleteBatchIfDone
		mock.ExpectExec("UPDATE eval_batches SET status").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		payload, _ := json.Marshal(map[string]string{
			"eval_run_id": runID.String(),
			"org_id":      orgID.String(),
			"batch_id":    batchID.String(),
		})

		handler := newRunEvalHandler(stores, &Services{}, zerolog.Nop())
		err := handler(context.Background(), "run_eval", payload)
		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("fetch run failure returns error", func(t *testing.T) {
		t.Parallel()

		stores, mock := newEvalTestStores(t)
		defer mock.Close()

		orgID := uuid.New()
		runID := uuid.New()

		mock.ExpectQuery("SELECT .+ FROM eval_runs WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(evalRunTestCols))

		payload, _ := json.Marshal(map[string]string{
			"eval_run_id": runID.String(),
			"org_id":      orgID.String(),
		})

		handler := newRunEvalHandler(stores, &Services{}, zerolog.Nop())
		err := handler(context.Background(), "run_eval", payload)
		require.Error(t, err)
		require.Contains(t, err.Error(), "fetch eval run")
	})

	t.Run("update status failure returns error", func(t *testing.T) {
		t.Parallel()

		stores, mock := newEvalTestStores(t)
		defer mock.Close()

		orgID := uuid.New()
		runID := uuid.New()
		taskID := uuid.New()
		now := time.Now()

		mock.ExpectQuery("SELECT .+ FROM eval_runs WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(evalRunTestCols).AddRow(evalRunRow(runID, taskID, orgID, now)...))

		mock.ExpectQuery("SELECT .+ FROM eval_tasks WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(evalTaskTestCols).AddRow(
				evalTaskRow(taskID, orgID, now, json.RawMessage(`[]`))...))

		mock.ExpectExec("UPDATE eval_runs SET status").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(errors.New("db connection lost"))

		payload, _ := json.Marshal(map[string]string{
			"eval_run_id": runID.String(),
			"org_id":      orgID.String(),
		})

		handler := newRunEvalHandler(stores, &Services{}, zerolog.Nop())
		err := handler(context.Background(), "run_eval", payload)
		require.Error(t, err)
		require.Contains(t, err.Error(), "update eval run status to running")
	})
}

func TestComputeWeightedScore(t *testing.T) {
	t.Parallel()

	t.Run("simple pass", func(t *testing.T) {
		criteria := []models.ScoringCriterion{
			{Name: "tests", Weight: 1.0, Required: false},
			{Name: "quality", Weight: 1.0, Required: false},
		}
		results := []models.CriterionResult{
			{Name: "tests", Score: 1.0, Pass: true},
			{Name: "quality", Score: 0.8, Pass: true},
		}
		score, passed := computeWeightedScore(criteria, results, 0.7)
		require.InDelta(t, 0.9, score, 0.01)
		require.True(t, passed)
	})

	t.Run("required criterion fails overall", func(t *testing.T) {
		criteria := []models.ScoringCriterion{
			{Name: "tests", Weight: 1.0, Required: true},
			{Name: "quality", Weight: 1.0, Required: false},
		}
		results := []models.CriterionResult{
			{Name: "tests", Score: 0.0, Pass: false},
			{Name: "quality", Score: 1.0, Pass: true},
		}
		score, passed := computeWeightedScore(criteria, results, 0.3)
		require.InDelta(t, 0.5, score, 0.01) // weighted avg is 0.5
		require.False(t, passed)              // but fails due to required criterion
	})

	t.Run("below threshold fails", func(t *testing.T) {
		criteria := []models.ScoringCriterion{
			{Name: "tests", Weight: 1.0},
		}
		results := []models.CriterionResult{
			{Name: "tests", Score: 0.5, Pass: true},
		}
		_, passed := computeWeightedScore(criteria, results, 0.7)
		require.False(t, passed)
	})

	t.Run("empty results return zero", func(t *testing.T) {
		score, passed := computeWeightedScore(nil, nil, 0.5)
		require.Equal(t, 0.0, score)
		require.False(t, passed)
	})

	t.Run("unequal weights", func(t *testing.T) {
		criteria := []models.ScoringCriterion{
			{Name: "tests", Weight: 3.0},
			{Name: "quality", Weight: 1.0},
		}
		results := []models.CriterionResult{
			{Name: "tests", Score: 1.0, Pass: true},
			{Name: "quality", Score: 0.0, Pass: false},
		}
		score, _ := computeWeightedScore(criteria, results, 0.5)
		require.InDelta(t, 0.75, score, 0.01) // (3*1.0 + 1*0.0) / 4
	})
}

func TestExtractJSON(t *testing.T) {
	t.Parallel()

	t.Run("extracts from markdown fences", func(t *testing.T) {
		input := "Here is the result:\n```json\n{\"pass\": true}\n```"
		result := extractJSON(input)
		require.Equal(t, "{\"pass\": true}", result)
	})

	t.Run("plain JSON passthrough", func(t *testing.T) {
		input := `{"pass": false, "reasoning": "bad"}`
		result := extractJSON(input)
		require.Equal(t, input, result)
	})

	t.Run("no JSON returns input", func(t *testing.T) {
		input := "no json here"
		result := extractJSON(input)
		require.Equal(t, input, result)
	})
}

func TestTruncateString(t *testing.T) {
	t.Parallel()

	t.Run("short string unchanged", func(t *testing.T) {
		require.Equal(t, "hello", truncateString("hello", 10))
	})

	t.Run("long string truncated", func(t *testing.T) {
		result := truncateString("hello world", 5)
		require.Equal(t, "hello...(truncated)", result)
	})
}

func TestEvalFailed(t *testing.T) {
	t.Parallel()

	result := evalFailed("test error: %v", "details")
	require.Equal(t, models.EvalRunStatusFailed, result.Status)
	require.NotNil(t, result.ErrorMessage)
	require.Equal(t, "test error: details", *result.ErrorMessage)
}

func TestBuildEvalManifest(t *testing.T) {
	t.Parallel()

	pinID := uuid.New()
	settingsID := uuid.New()
	digest := "sha256:abc123"
	task := &models.EvalTask{
		BaseCommitSHA:        "abc123",
		PMDocumentSetPinID:   &pinID,
		OrgSettingsVersionID: &settingsID,
		SandboxImageDigest:   &digest,
	}
	run := &models.EvalRun{Model: "claude-sonnet-4-6"}

	manifest := buildEvalManifest(task, run)
	require.Equal(t, "abc123", manifest.RepoBaseCommitSHA)
	require.Equal(t, "claude-sonnet-4-6", manifest.Model)
	require.Equal(t, &pinID, manifest.PMDocumentSetPinID)
	require.Equal(t, &settingsID, manifest.OrgSettingsVersionID)
	require.Equal(t, "sha256:abc123", manifest.SandboxImageDigest)
}
