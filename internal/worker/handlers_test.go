package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/feedback"
	"github.com/assembledhq/143/internal/services/pm"
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
