package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

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
	RegisterHandlers(w, stores, nil, logger)

	expectedHandlers := []string{
		"ingest_webhook",
		"sync_sentry",
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
	RegisterHandlers(w2, stores, &Services{PM: &mockPMService{}}, logger)
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

type testFeedbackPatternStore struct {
	createCalls int
}

func (m *testFeedbackPatternStore) Create(ctx context.Context, p *models.ReviewPattern) error {
	m.createCalls++
	return nil
}

func (m *testFeedbackPatternStore) GetByID(ctx context.Context, orgID, id uuid.UUID) (models.ReviewPattern, error) {
	return models.ReviewPattern{}, nil
}

func (m *testFeedbackPatternStore) FindMatchingRule(ctx context.Context, orgID uuid.UUID, repo, normalizedRule string) (models.ReviewPattern, error) {
	return models.ReviewPattern{}, errors.New("not found")
}

func (m *testFeedbackPatternStore) IncrementOccurrence(ctx context.Context, orgID, patternID, commentID uuid.UUID) error {
	return nil
}

func (m *testFeedbackPatternStore) ListActiveByRepo(ctx context.Context, orgID uuid.UUID, repo string) ([]models.ReviewPattern, error) {
	return nil, nil
}

func (m *testFeedbackPatternStore) UpdatePattern(ctx context.Context, orgID, id uuid.UUID, rule *string, status *string) error {
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
	patternStore := &testFeedbackPatternStore{}
	feedbackService := feedback.NewService(commentStore, patternStore, &testFeedbackJobStore{}, nil, zerolog.Nop())

	services := &Services{Feedback: feedbackService}
	handler := newProcessReviewCommentHandler(services, zerolog.Nop())
	payload := json.RawMessage(`{"comment_id":"` + commentID.String() + `","org_id":"` + orgID.String() + `","repo":"org/repo"}`)

	err := handler(context.Background(), "process_review_comment", payload)
	require.NoError(t, err, "process_review_comment handler should succeed for already processed comments")
	require.Equal(t, 0, patternStore.createCalls, "process_review_comment should not update patterns when comment was already processed")
}

// ---------------------------------------------------------------------------
// newUpdateReviewPatternsHandler tests
// ---------------------------------------------------------------------------

func TestUpdateReviewPatternsHandler(t *testing.T) {
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
			errSubstr: "unmarshal update_review_patterns payload",
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
			patternStore := &testFeedbackPatternStore{}
			feedbackService := feedback.NewService(commentStore, patternStore, &testFeedbackJobStore{}, nil, zerolog.Nop())
			services := &Services{Feedback: feedbackService}

			handler := newUpdateReviewPatternsHandler(services, zerolog.Nop())
			err := handler(context.Background(), "update_review_patterns", tt.payload)

			if tt.expectErr {
				require.Error(t, err, "handler should return error")
				require.Contains(t, err.Error(), tt.errSubstr, "error should contain expected substring")
			} else {
				require.NoError(t, err, "handler should succeed")
			}
		})
	}
}

func TestUpdateReviewPatternsHandler_Success(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	commentID := uuid.New()

	commentStore := &testFeedbackCommentStore{}
	patternStore := &testFeedbackPatternStore{}
	feedbackService := feedback.NewService(commentStore, patternStore, &testFeedbackJobStore{}, nil, zerolog.Nop())

	services := &Services{Feedback: feedbackService}
	handler := newUpdateReviewPatternsHandler(services, zerolog.Nop())

	payload := json.RawMessage(`{"comment_id":"` + commentID.String() + `","org_id":"` + orgID.String() + `","repo":"org/repo","rule":"always use gofmt","category":"style"}`)
	err := handler(context.Background(), "update_review_patterns", payload)
	require.NoError(t, err, "update_review_patterns handler should succeed with valid payload")
	require.Equal(t, 1, patternStore.createCalls, "should create a new pattern")
}

func TestUpdateReviewPatternsHandler_UsesJobOrgID(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	commentID := uuid.New()

	commentStore := &testFeedbackCommentStore{}
	patternStore := &testFeedbackPatternStore{}
	feedbackService := feedback.NewService(commentStore, patternStore, &testFeedbackJobStore{}, nil, zerolog.Nop())

	services := &Services{Feedback: feedbackService}
	handler := newUpdateReviewPatternsHandler(services, zerolog.Nop())

	ctx := withJobOrgID(context.Background(), orgID)
	payload := json.RawMessage(`{"comment_id":"` + commentID.String() + `","repo":"org/repo","rule":"always use gofmt","category":"style"}`)
	err := handler(ctx, "update_review_patterns", payload)
	require.NoError(t, err, "update_review_patterns should succeed using org ID from context")
	require.Equal(t, 1, patternStore.createCalls, "should create a new pattern")
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
		Feedback:        feedback.NewService(&testFeedbackCommentStore{}, &testFeedbackPatternStore{}, &testFeedbackJobStore{}, nil, zerolog.Nop()),
		PM:              &mockPMService{},
	}

	w := New(nil, logger, "test-node")
	RegisterHandlers(w, stores, services, logger)

	allExpected := []string{
		"ingest_webhook",
		"sync_sentry",
		"prioritize",
		"pm_analyze",
		"run_agent",
		"validate",
		"open_pr",
		"analyze_failure",
		"process_review_comment",
		"update_review_patterns",
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
	RegisterHandlers(w, stores, services, logger)

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

	feedbackService := feedback.NewService(&testFeedbackCommentStore{}, &testFeedbackPatternStore{}, &testFeedbackJobStore{}, nil, zerolog.Nop())
	services := &Services{
		Feedback: feedbackService,
	}

	w := New(nil, logger, "test-node")
	RegisterHandlers(w, stores, services, logger)

	_, ok := w.handlers["process_review_comment"]
	require.True(t, ok, "process_review_comment handler should be registered")
	_, ok = w.handlers["update_review_patterns"]
	require.True(t, ok, "update_review_patterns handler should be registered")
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
	RegisterHandlers(w, stores, services, logger)

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

	feedbackService := feedback.NewService(&testFeedbackCommentStore{}, &testFeedbackPatternStore{}, &testFeedbackJobStore{}, nil, zerolog.Nop())
	services := &Services{Feedback: feedbackService}

	handler := newProcessReviewCommentHandler(services, zerolog.Nop())
	err := handler(context.Background(), "process_review_comment", json.RawMessage(`{bad`))
	require.Error(t, err, "should return error for invalid JSON")
	require.Contains(t, err.Error(), "unmarshal process_review_comment payload", "error should indicate unmarshal failure")
}

func TestProcessReviewCommentHandler_InvalidOrgID(t *testing.T) {
	t.Parallel()

	feedbackService := feedback.NewService(&testFeedbackCommentStore{}, &testFeedbackPatternStore{}, &testFeedbackJobStore{}, nil, zerolog.Nop())
	services := &Services{Feedback: feedbackService}

	handler := newProcessReviewCommentHandler(services, zerolog.Nop())
	payload := json.RawMessage(`{"comment_id":"` + uuid.New().String() + `","org_id":"not-a-uuid"}`)
	err := handler(context.Background(), "process_review_comment", payload)
	require.Error(t, err, "should return error for invalid org ID")
	require.Contains(t, err.Error(), "parse org ID", "error should mention org ID")
}

func TestProcessReviewCommentHandler_InvalidCommentID(t *testing.T) {
	t.Parallel()

	feedbackService := feedback.NewService(&testFeedbackCommentStore{}, &testFeedbackPatternStore{}, &testFeedbackJobStore{}, nil, zerolog.Nop())
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
	patternStore := &testFeedbackPatternStore{}
	feedbackService := feedback.NewService(commentStore, patternStore, &testFeedbackJobStore{}, nil, zerolog.Nop())

	services := &Services{Feedback: feedbackService}
	handler := newProcessReviewCommentHandler(services, zerolog.Nop())
	payload := json.RawMessage(`{"comment_id":"` + commentID.String() + `","org_id":"` + orgID.String() + `","repo":"org/repo"}`)

	err := handler(context.Background(), "process_review_comment", payload)
	require.NoError(t, err, "handler should succeed for pending comment")
	require.Equal(t, 1, patternStore.createCalls, "should create a new pattern for pending generalizable comment")
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
	patternStore := &testFeedbackPatternStore{}
	feedbackService := feedback.NewService(commentStore, patternStore, &testFeedbackJobStore{}, nil, zerolog.Nop())

	services := &Services{Feedback: feedbackService}
	handler := newProcessReviewCommentHandler(services, zerolog.Nop())
	payload := json.RawMessage(`{"comment_id":"` + commentID.String() + `","org_id":"` + orgID.String() + `"}`)

	err := handler(context.Background(), "process_review_comment", payload)
	require.NoError(t, err, "handler should succeed without repo")
	require.Equal(t, 0, patternStore.createCalls, "should not create patterns when no repo is provided")
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
	patternStore := &testFeedbackPatternStore{}
	feedbackService := feedback.NewService(commentStore, patternStore, &testFeedbackJobStore{}, nil, zerolog.Nop())

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
