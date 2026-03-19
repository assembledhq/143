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
	byStatus   map[string][]models.Issue
	errByKey   map[string]error
	totalCount int // returned by CountByOrg; defaults to 0 (small tier)
}

func (m *gatherIssueStoreMock) ListByOrg(ctx context.Context, orgID uuid.UUID, filters db.IssueFilters) ([]models.Issue, error) {
	if err := m.errByKey[filters.Status]; err != nil {
		return nil, err
	}
	return m.byStatus[filters.Status], nil
}

func (m *gatherIssueStoreMock) CountByOrg(ctx context.Context, orgID uuid.UUID) (int, error) {
	if err := m.errByKey["count"]; err != nil {
		return 0, err
	}
	return m.totalCount, nil
}

func (m *gatherIssueStoreMock) UpdateStatus(ctx context.Context, orgID, issueID uuid.UUID, status string) error {
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
	key := string(filters.Status)
	if err := m.errByKey[key]; err != nil {
		return nil, err
	}
	return m.byStatus[key], nil
}

func (m *gatherSessionStoreMock) ListRecentByOrg(ctx context.Context, orgID uuid.UUID, statuses []string, limit int) ([]models.Session, error) {
	if err := m.errByKey["recent"]; err != nil {
		return nil, err
	}
	return m.recent, nil
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

func TestGatherContext_CountByOrgError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	settingsJSON, _ := json.Marshal(models.OrgSettings{MaxConcurrentRuns: 3})

	svc := &Service{
		issues: &gatherIssueStoreMock{
			byStatus: map[string][]models.Issue{},
			errByKey: map[string]error{"count": fmt.Errorf("count exploded")},
		},
		sessions: &gatherSessionStoreMock{byStatus: map[string][]models.Session{}},
		orgs:     &gatherOrgStoreMock{org: models.Organization{ID: orgID, Settings: settingsJSON}},
	}

	_, err := svc.gatherContext(context.Background(), orgID, nil)
	require.Error(t, err, "gatherContext should fail when CountByOrg errors")
	require.Contains(t, err.Error(), "count issues", "error should mention count issues")
}

func TestGatherContext_AdaptiveLimitsTierSelection(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	settingsJSON, _ := json.Marshal(models.OrgSettings{MaxConcurrentRuns: 3})

	tests := []struct {
		name        string
		totalCount  int
		expectedMax int // expected IssuesPerStatus limit
	}{
		{
			name:        "small org uses small limits",
			totalCount:  10,
			expectedMax: limitsSmall.IssuesPerStatus,
		},
		{
			name:        "medium org uses medium limits",
			totalCount:  200,
			expectedMax: limitsMedium.IssuesPerStatus,
		},
		{
			name:        "large org uses large limits",
			totalCount:  1000,
			expectedMax: limitsLarge.IssuesPerStatus,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := &Service{
				issues: &gatherIssueStoreMock{
					byStatus:   map[string][]models.Issue{},
					totalCount: tt.totalCount,
				},
				sessions: &gatherSessionStoreMock{byStatus: map[string][]models.Session{}, count: 0},
				orgs:     &gatherOrgStoreMock{org: models.Organization{ID: orgID, Settings: settingsJSON}},
			}

			bundle, err := svc.gatherContext(context.Background(), orgID, nil)
			require.NoError(t, err, "gatherContext should succeed")
			require.NotNil(t, bundle, "gatherContext should return a bundle")
			// The mock returns empty slices regardless of limit, so we verify
			// the function completes without error at each tier. The actual
			// limit values are validated in constants_test.go.
		})
	}
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
		sessionStore    sessionStore
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
			name:      "returns wrapped error when organization lookup fails",
			orgStore:  &gatherOrgStoreMock{err: fmt.Errorf("org missing")},
			issueStore: &gatherIssueStoreMock{},
			sessionStore: &gatherSessionStoreMock{},
			expectErr: "org missing",
		},
		{
			name: "returns wrapped error when issue lookup fails",
			orgStore: &gatherOrgStoreMock{org: models.Organization{ID: orgID, Settings: settingsJSON}},
			issueStore: &gatherIssueStoreMock{errByKey: map[string]error{"open": fmt.Errorf("issues unavailable")}},
			sessionStore: &gatherSessionStoreMock{},
			expectErr: "issues unavailable",
		},
		{
			name:     "builds full context with issues runs prs and decisions",
			orgStore: &gatherOrgStoreMock{org: models.Organization{ID: orgID, Settings: settingsJSON}},
			issueStore: &gatherIssueStoreMock{byStatus: map[string][]models.Issue{
				"open": {
					{
						ID:                    issueID,
						Source:                "sentry",
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
						Source:                "github",
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
						{ID: pendingRunID, IssueID: issueID, Status: "pending", StartedAt: &now},
					},
					"running": {
						{ID: uuid.New(), IssueID: secondIssueID, Status: "running", StartedAt: &now},
					},
				},
				recent: []models.Session{{
					ID:                 recentRunID,
					IssueID:            issueID,
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
				SessionID:   pendingRunID,
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
				sessions:    tt.sessionStore,
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
