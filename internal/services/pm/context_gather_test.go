package pm

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type gatherIssueStoreMock struct {
	byStatus map[string][]models.Issue
	errByKey map[string]error
}

func (m *gatherIssueStoreMock) GetByID(ctx context.Context, orgID, issueID uuid.UUID) (models.Issue, error) {
	return models.Issue{}, nil
}

func (m *gatherIssueStoreMock) ListByOrg(ctx context.Context, orgID uuid.UUID, filters db.IssueFilters) ([]models.Issue, error) {
	if err := m.errByKey[string(filters.Status)]; err != nil {
		return nil, err
	}
	return m.byStatus[string(filters.Status)], nil
}

func (m *gatherIssueStoreMock) UpdateStatus(ctx context.Context, orgID, issueID uuid.UUID, status models.IssueStatus) error {
	return nil
}

type gatherSessionStoreMock struct {
	byStatus map[string][]models.Session
	recent   []models.Session
	count    int
	errByKey map[string]error
}

func (m *gatherSessionStoreMock) CountRunningByOrg(ctx context.Context, orgID uuid.UUID) (int, error) {
	if err := m.errByKey["count_running"]; err != nil {
		return 0, err
	}
	return m.count, nil
}

func (m *gatherSessionStoreMock) Create(ctx context.Context, run *models.Session) error {
	return nil
}

func (m *gatherSessionStoreMock) ListByOrg(ctx context.Context, orgID uuid.UUID, filters db.SessionFilters) ([]models.Session, error) {
	var result []models.Session
	for _, s := range filters.Statuses {
		key := string(s)
		if err := m.errByKey[key]; err != nil {
			return nil, err
		}
		result = append(result, m.byStatus[key]...)
	}
	if len(filters.Statuses) == 0 {
		if err := m.errByKey[""]; err != nil {
			return nil, err
		}
		return m.byStatus[""], nil
	}
	return result, nil
}

func (m *gatherSessionStoreMock) ListRecentByOrg(ctx context.Context, orgID uuid.UUID, statuses []string, limit int) ([]models.Session, error) {
	if err := m.errByKey["recent"]; err != nil {
		return nil, err
	}
	return m.recent, nil
}

func (m *gatherSessionStoreMock) UpdateResult(ctx context.Context, orgID, runID uuid.UUID, status models.SessionStatus, result *models.SessionResult) error {
	return nil
}

func (m *gatherSessionStoreMock) UpdatePMPlanID(ctx context.Context, orgID, runID, planID uuid.UUID) error {
	return nil
}

type gatherOrgStoreMock struct {
	org models.Organization
	err error
}

func (m *gatherOrgStoreMock) GetByID(ctx context.Context, id uuid.UUID) (models.Organization, error) {
	if m.err != nil {
		return models.Organization{}, m.err
	}
	return m.org, nil
}

type gatherPRStoreMock struct {
	prs []models.PullRequest
	err error
}

func (m *gatherPRStoreMock) ListByOrg(ctx context.Context, orgID uuid.UUID, filters db.PullRequestFilters) ([]models.PullRequest, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.prs, nil
}

type gatherDecisionStoreMock struct {
	entries []models.PMDecisionLogEntry
	err     error
}

func (m *gatherDecisionStoreMock) Create(ctx context.Context, entry *models.PMDecisionLogEntry) error {
	return nil
}

func (m *gatherDecisionStoreMock) ListRecentByOrg(ctx context.Context, orgID uuid.UUID, limit int) ([]models.PMDecisionLogEntry, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.entries, nil
}

func (m *gatherDecisionStoreMock) UpdateOutcome(ctx context.Context, orgID, planID, issueID uuid.UUID, outcome models.PMDecisionOutcome) error {
	return nil
}

func TestServiceGatherContext(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	issueID := uuid.New()
	secondIssueID := uuid.New()
	pendingRunID := uuid.New()
	recentRunID := uuid.New()
	prID := uuid.New()
	planID := uuid.New()
	decisionID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	desc := "timeout while syncing subscription state"
	confidence := 0.91
	failureCategory := "timeout"
	failureReason := "network flake"

	settings := models.OrgSettings{
		MaxConcurrentRuns: 7,
		ProductContext: &models.ProductContext{
			Philosophy: "ship reliability first",
			Direction:  "payments hardening",
			FocusAreas: []string{"slo", "incident prevention"},
			AvoidAreas: []string{"new ui"},
		},
	}
	settingsJSON, err := json.Marshal(settings)
	require.NoError(t, err, "test setup should marshal org settings")

	tests := []struct {
		name             string
		orgStore         orgStore
		issueStore       issueStore
		sessionStore     sessionStore
		pullRequests     prStore
		decisionLog      decisionLogStore
		expectErr        string
		expectedOpen     int
		expectedInFlight int
		expectedRecent   int
		expectedPRs      int
		expectedDecision int
	}{
		{
			name:         "returns wrapped error when organization lookup fails",
			orgStore:     &gatherOrgStoreMock{err: fmt.Errorf("org missing")},
			issueStore:   &gatherIssueStoreMock{},
			sessionStore: &gatherSessionStoreMock{},
			expectErr:    "org missing",
		},
		{
			name:         "returns wrapped error when issue lookup fails",
			orgStore:     &gatherOrgStoreMock{org: models.Organization{ID: orgID, Settings: settingsJSON}},
			issueStore:   &gatherIssueStoreMock{errByKey: map[string]error{"open": fmt.Errorf("issues unavailable")}},
			sessionStore: &gatherSessionStoreMock{},
			expectErr:    "issues unavailable",
		},
		{
			name:     "builds full context with issues runs prs and decisions",
			orgStore: &gatherOrgStoreMock{org: models.Organization{ID: orgID, Settings: settingsJSON}},
			issueStore: &gatherIssueStoreMock{byStatus: map[string][]models.Issue{
				"open": {
					{
						ID:                    issueID,
						Source:                models.IssueSourceSentry,
						Title:                 "payment request panic",
						Description:           &desc,
						Severity:              "high",
						OccurrenceCount:       4,
						AffectedCustomerCount: 2,
						FirstSeenAt:           now.Add(-2 * time.Hour),
						LastSeenAt:            now,
						Tags:                  []string{"payments"},
						RawData:               json.RawMessage(`{"stacktrace":"line"}`),
					},
				},
				"triaged": {
					{
						ID:                    secondIssueID,
						Source:                models.IssueSource("github"),
						Title:                 "retry policy bug",
						Severity:              "medium",
						OccurrenceCount:       3,
						AffectedCustomerCount: 1,
						FirstSeenAt:           now.Add(-4 * time.Hour),
						LastSeenAt:            now.Add(-1 * time.Hour),
					},
				},
			}},
			sessionStore: &gatherSessionStoreMock{
				byStatus: map[string][]models.Session{
					"pending": {
						{ID: pendingRunID, PrimaryIssueID: &issueID, Status: "pending", StartedAt: &now},
					},
					"running": {
						{ID: uuid.New(), PrimaryIssueID: &secondIssueID, Status: "running", StartedAt: &now},
					},
				},
				recent: []models.Session{{
					ID:                 recentRunID,
					PrimaryIssueID:     &issueID,
					Status:             "completed",
					ConfidenceScore:    &confidence,
					FailureCategory:    &failureCategory,
					FailureExplanation: &failureReason,
					CompletedAt:        &now,
				}},
				count: 2,
			},
			pullRequests: &gatherPRStoreMock{prs: []models.PullRequest{{
				ID:           prID,
				SessionID:    &pendingRunID,
				Title:        "Fix payment panic",
				Status:       "open",
				ReviewStatus: "pending",
			}}},
			decisionLog: &gatherDecisionStoreMock{entries: []models.PMDecisionLogEntry{{
				ID:        decisionID,
				PlanID:    planID,
				IssueID:   &issueID,
				Decision:  models.PMDecisionTypeDelegate,
				Reasoning: "highest impact",
				Outcome:   models.PMDecisionOutcomeSucceeded,
				CreatedAt: now,
			}}},
			expectedOpen:     2,
			expectedInFlight: 2,
			expectedRecent:   1,
			expectedPRs:      1,
			expectedDecision: 1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := &Service{
				issues:       tt.issueStore,
				sessions:     tt.sessionStore,
				pullRequests: tt.pullRequests,
				orgs:         tt.orgStore,
				decisionLog:  tt.decisionLog,
			}

			bundle, err := svc.gatherContext(context.Background(), orgID, nil)
			if tt.expectErr != "" {
				require.Error(t, err, "gatherContext should return an error")
				require.Contains(t, err.Error(), tt.expectErr, "gatherContext should return the expected error")
				return
			}

			require.NoError(t, err, "gatherContext should not return an error")
			require.NotNil(t, bundle, "gatherContext should return a context bundle")
			require.NotNil(t, bundle.pmContext, "gatherContext should include PM context")
			require.Len(t, bundle.pmContext.OpenIssues, tt.expectedOpen, "gatherContext should include expected open and triaged issues")
			require.Len(t, bundle.pmContext.InFlightRuns, tt.expectedInFlight, "gatherContext should include expected in-flight runs")
			require.Len(t, bundle.pmContext.RecentOutcomes, tt.expectedRecent, "gatherContext should include expected recent outcomes")
			require.Len(t, bundle.pmContext.RecentPRs, tt.expectedPRs, "gatherContext should include expected pull requests")
			require.Len(t, bundle.pmContext.PreviousDecisions, tt.expectedDecision, "gatherContext should include expected decision log entries")
			require.Equal(t, settings.MaxConcurrentRuns, bundle.pmContext.MaxConcurrentRuns, "gatherContext should carry max concurrent runs from org settings")
			require.Equal(t, 2, bundle.pmContext.CurrentRunCount, "gatherContext should carry current run count")
			require.Equal(t, settings.ProductContext, bundle.productContext, "gatherContext should carry parsed product context")
		})
	}
}
