package pm

import (
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestNewService(t *testing.T) {
	t.Parallel()

	issues := &mockIssueStore{}
	agentRuns := &mockAgentRunStore{}
	orgs := &mockOrgStore{}
	jobs := &mockJobStore{}
	plans := &mockPlanStore{}

	svc := NewService(issues, agentRuns, nil, orgs, nil, jobs, plans, nil, nil, nil, nil, zerolog.Nop())
	require.Equal(t, issues, svc.issues, "NewService should store issue dependency")
	require.Equal(t, agentRuns, svc.agentRuns, "NewService should store agent run dependency")
	require.Equal(t, orgs, svc.orgs, "NewService should store org dependency")
	require.Equal(t, jobs, svc.jobs, "NewService should store job dependency")
	require.Equal(t, plans, svc.plans, "NewService should store plan dependency")
}

func TestPMSandboxConfig(t *testing.T) {
	t.Parallel()

	cfg := pmSandboxConfig()
	require.Equal(t, 10*time.Minute, cfg.Timeout, "pmSandboxConfig should set PM timeout")
	require.Equal(t, 1.0, cfg.CPULimit, "pmSandboxConfig should set PM CPU limit")
	require.Equal(t, 2048, cfg.MemoryLimitMB, "pmSandboxConfig should set PM memory limit")
	require.Equal(t, "restricted", cfg.NetworkPolicy, "pmSandboxConfig should set restricted network policy")
}

func TestPlanToModelAndTokenMode(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	planID := uuid.New()
	issueID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)

	plan := &Plan{
		ID:             planID,
		OrgID:          orgID,
		Status:         models.PMPlanStatusExecuting,
		Analysis:       "cluster around webhook retries",
		Tasks:          []Task{{Rank: 1, IssueIDs: []uuid.UUID{issueID}, Title: "Fix retries"}},
		Clusters:       []Cluster{{IssueIDs: []uuid.UUID{issueID}, RootCause: "missing backoff", Strategy: "add retry budget"}},
		SkippedIssues:  []SkipEntry{{IssueID: issueID, Reason: models.PMSkipReasonDuplicate, Detail: "small customer impact"}},
		IssuesReviewed: 3,
		TokenUsage:     []byte(`{"input_tokens":10}`),
		TriggeredBy:    models.PMTriggerCron,
		CreatedAt:      now,
	}
	productContext := &models.ProductContext{Philosophy: "stability", Direction: "incident reduction"}

	model, err := planToModel(plan, productContext)
	require.NoError(t, err, "planToModel should serialize valid plan")
	require.Equal(t, planID, model.ID, "planToModel should copy plan ID")
	require.Equal(t, orgID, model.OrgID, "planToModel should copy org ID")
	require.NotEmpty(t, model.Tasks, "planToModel should serialize tasks")
	require.NotEmpty(t, model.Clusters, "planToModel should serialize clusters")
	require.NotEmpty(t, model.SkippedIssues, "planToModel should serialize skipped issues")
	require.NotEmpty(t, model.ProductContextSnapshot, "planToModel should snapshot product context when present")

	modelWithoutContext, err := planToModel(plan, nil)
	require.NoError(t, err, "planToModel should allow nil product context")
	require.Empty(t, modelWithoutContext.ProductContextSnapshot, "planToModel should leave product context snapshot empty when no context is provided")

	require.Equal(t, "low", tokenModeFromComplexity(models.PMTaskComplexitySimple), "tokenModeFromComplexity should use low tokens for simple tasks")
	require.Equal(t, "high", tokenModeFromComplexity(models.PMTaskComplexityModerate), "tokenModeFromComplexity should use high tokens for moderate tasks")
	require.Equal(t, "high", tokenModeFromComplexity(models.PMTaskComplexityComplex), "tokenModeFromComplexity should use high tokens for complex tasks")
}
