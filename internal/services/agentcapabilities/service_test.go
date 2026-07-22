package agentcapabilities

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestServiceValidateGrantRejectsUnknownCapabilityAndExcessAccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		grant models.AgentCapabilityPolicyGrantInput
	}{
		{
			name: "unknown capability",
			grant: models.AgentCapabilityPolicyGrantInput{
				CapabilityID: models.AgentCapabilityID("unknown"),
				AccessLevel:  models.AgentCapabilityAccessRead,
				Enabled:      true,
				Config:       json.RawMessage(`{}`),
			},
		},
		{
			name: "access above catalog maximum",
			grant: models.AgentCapabilityPolicyGrantInput{
				CapabilityID: models.AgentCapabilitySessionHistory,
				AccessLevel:  models.AgentCapabilityAccessWrite,
				Enabled:      true,
				Config:       json.RawMessage(`{}`),
			},
		},
	}

	svc := NewService(nil)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := svc.ValidateGrant(tt.grant)
			require.Error(t, err, "invalid capability grant should be rejected")
		})
	}
}

func TestServiceResolveForSessionUsesRecommendedDefaultsWhenNoPolicyExists(t *testing.T) {
	t.Parallel()

	svc := NewService(&memoryPolicyStore{})
	repoID := uuid.New()

	snapshot, err := svc.ResolveForSession(context.Background(), ResolveInput{
		OrgID:         uuid.New(),
		RepositoryID:  &repoID,
		SessionOrigin: models.SessionOriginManual,
	})
	require.NoError(t, err, "manual repository session should resolve default snapshot")
	require.Equal(t, []models.AgentCapabilityID{
		models.AgentCapabilityRepoContext,
		models.AgentCapabilityPRHistory,
		models.AgentCapabilityReviewFeedback,
		models.AgentCapabilityCIHistory,
		models.AgentCapabilityIssueSources,
		models.AgentCapabilityProductionDiagnostics,
		models.AgentCapabilitySlackNotifications,
		models.AgentCapabilityAutomationManagement,
		models.AgentCapabilityPublishing,
	}, snapshotIDs(snapshot), "manual repository sessions should get recommended commonly-used defaults")
}

func TestServiceResolveForSessionAddsIssueSourcesForTriggeredSessions(t *testing.T) {
	t.Parallel()

	svc := NewService(&memoryPolicyStore{})
	repoID := uuid.New()

	snapshot, err := svc.ResolveForSession(context.Background(), ResolveInput{
		OrgID:         uuid.New(),
		RepositoryID:  &repoID,
		SessionOrigin: models.SessionOriginIssueTrigger,
	})
	require.NoError(t, err, "triggered repository session should resolve default snapshot")
	require.Contains(t, snapshotIDs(snapshot), models.AgentCapabilityIssueSources,
		"issue-triggered sessions should get issue sources scoped to that origin")
}

func TestServiceDefinitionsIncludeCodeReviewPolicyManagement(t *testing.T) {
	t.Parallel()

	svc := NewService(nil)
	var found *models.AgentCapabilityDefinition
	for _, def := range svc.Definitions() {
		if def.ID == models.AgentCapabilityCodeReviewPolicy {
			capability := def
			found = &capability
			break
		}
	}
	require.NotNil(t, found, "capability catalog should include code review policy management")
	require.Equal(t, models.AgentCapabilityAccessWrite, found.MaxAccessLevel, "policy management should cap at write access")
	require.Equal(t, models.AgentCapabilityRiskHigh, found.Risk, "changing the org review policy is a high-risk action")
	require.Equal(t, models.AgentCapabilityScopeOrg, found.Scope, "the review policy is org-wide")
	require.False(t, recommendedDefaultEnabledCapabilities[models.AgentCapabilityCodeReviewPolicy],
		"policy management must stay out of the recommended defaults so it is granted only explicitly")
}

func TestServiceDefinitionsIncludesSlackNotificationsCapability(t *testing.T) {
	t.Parallel()

	svc := NewService(nil)
	var found *models.AgentCapabilityDefinition
	for _, def := range svc.Definitions() {
		if def.ID == models.AgentCapabilitySlackNotifications {
			copy := def
			found = &copy
			break
		}
	}

	require.NotNil(t, found, "catalog should expose a Slack notifications capability")
	require.Equal(t, models.AgentCapabilityAccessWrite, found.MaxAccessLevel, "Slack notification sending should require write access")
	require.Equal(t, models.AgentCapabilityScopeIntegration, found.Scope, "Slack notification sending should be integration-scoped")
}

func TestServiceDefinitionsIncludesAutomationManagementCapability(t *testing.T) {
	t.Parallel()

	svc := NewService(nil)
	var found *models.AgentCapabilityDefinition
	for _, def := range svc.Definitions() {
		if def.ID == models.AgentCapabilityAutomationManagement {
			copy := def
			found = &copy
			break
		}
	}

	require.NotNil(t, found, "catalog should expose an automation management capability")
	require.Equal(t, models.AgentCapabilityAccessWrite, found.MaxAccessLevel, "automation management should require write access")
	require.Equal(t, models.AgentCapabilityRiskHigh, found.Risk, "automation management should be high risk")
	require.Equal(t, models.AgentCapabilityScopeRepository, found.Scope, "automation management should be repository-scoped")
}

func TestServiceRequestGrantCreatesHumanInputApproval(t *testing.T) {
	t.Parallel()

	requests := &recordingRequestWriter{}
	svc := NewService(nil)
	svc.SetHumanInputRequestWriter(requests)

	sessionID := uuid.New()
	threadID := uuid.New()
	req, err := svc.RequestGrant(context.Background(), GrantRequestInput{
		OrgID:       uuid.New(),
		SessionID:   sessionID,
		ThreadID:    &threadID,
		AgentType:   models.DefaultDefaultAgentType,
		Capability:  models.AgentCapabilitySessionHistory,
		AccessLevel: models.AgentCapabilityAccessRead,
		Reason:      "Compare this run against prior sessions.",
	})
	require.NoError(t, err, "valid capability request should create a human input approval")
	require.Equal(t, req, requests.created, "request writer should receive the created request")
	require.Equal(t, models.HumanInputRequestKindActionChoice, req.Kind, "read-only context capabilities should use action_choice")
	require.Equal(t, sessionID, req.SessionID, "approval should be scoped to the current session")
	require.Equal(t, &threadID, req.ThreadID, "approval should preserve current thread scope")
	require.JSONEq(t, `{"type":"agent_capability_request","capability_id":"session_history","access_level":"read","reason":"Compare this run against prior sessions."}`, string(req.ProviderPayload), "provider payload should carry capability approval metadata")
}

func TestServiceApplyApprovedGrantAppendsUserApprovedSnapshot(t *testing.T) {
	t.Parallel()

	appender := &recordingApprovedGrantAppender{}
	svc := NewService(nil)
	svc.SetApprovedGrantAppender(appender)

	orgID := uuid.New()
	sessionID := uuid.New()
	requestID := uuid.New()
	snapshot, err := svc.ApplyApprovedGrant(context.Background(), ApprovedGrantInput{
		OrgID:               orgID,
		SessionID:           sessionID,
		HumanInputRequestID: requestID,
		Capability:          models.AgentCapabilitySessionHistory,
		AccessLevel:         models.AgentCapabilityAccessRead,
	})
	require.NoError(t, err, "approved grant should append to the session snapshot")
	require.Equal(t, appender.snapshot, snapshot, "service should return the appender snapshot")
	require.Equal(t, orgID, appender.orgID, "append should be scoped by org")
	require.Equal(t, sessionID, appender.sessionID, "append should target the current session")
	require.Equal(t, models.AgentCapabilitySessionHistory, appender.item.ID, "snapshot item should use requested capability")
	require.Equal(t, models.AgentCapabilityGrantSourceUserApproved, appender.item.Source, "snapshot item should be marked user approved")
	require.Equal(t, &requestID, appender.item.HumanInputRequestID, "snapshot item should record approval request id")
}

func TestServiceResolveForSessionSkipsGrantsInvalidatedByCatalogChange(t *testing.T) {
	t.Parallel()

	repoID := uuid.New()
	// Policy contains a grant for a capability ID that no longer exists in the
	// catalog (simulates a catalog change after the grant was stored).
	staleGrants := []models.AgentCapabilityGrant{
		{CapabilityID: "nonexistent_capability", AccessLevel: models.AgentCapabilityAccessRead, Enabled: true, Config: json.RawMessage(`{}`)},
		{CapabilityID: models.AgentCapabilityRepoContext, AccessLevel: models.AgentCapabilityAccessRead, Enabled: true, Config: json.RawMessage(`{}`)},
	}
	store := &fixedPolicyStore{grants: staleGrants}
	svc := NewService(store)

	snapshot, err := svc.ResolveForSession(context.Background(), ResolveInput{
		OrgID:         uuid.New(),
		RepositoryID:  &repoID,
		SessionOrigin: models.SessionOriginManual,
	})
	require.NoError(t, err, "a stale grant should not cause resolution to fail")
	require.Equal(t, []models.AgentCapabilityID{models.AgentCapabilityRepoContext}, snapshotIDs(snapshot),
		"the stale grant should be skipped and the valid grant should be included")
}

type memoryPolicyStore struct{}

type fixedPolicyStore struct {
	grants []models.AgentCapabilityGrant
}

func (f *fixedPolicyStore) GetSessionDefaultPolicy(ctx context.Context, orgID uuid.UUID) (models.AgentCapabilityPolicy, error) {
	return models.AgentCapabilityPolicy{Grants: f.grants}, nil
}

func (f *fixedPolicyStore) GetAutomationPolicy(ctx context.Context, orgID, automationID uuid.UUID) (models.AgentCapabilityPolicy, error) {
	return models.AgentCapabilityPolicy{}, ErrPolicyNotFound
}

func (m *memoryPolicyStore) GetSessionDefaultPolicy(ctx context.Context, orgID uuid.UUID) (models.AgentCapabilityPolicy, error) {
	return models.AgentCapabilityPolicy{}, ErrPolicyNotFound
}

type recordingRequestWriter struct {
	created models.HumanInputRequest
}

func (w *recordingRequestWriter) Create(ctx context.Context, req *models.HumanInputRequest) error {
	w.created = *req
	return nil
}

type recordingApprovedGrantAppender struct {
	orgID     uuid.UUID
	sessionID uuid.UUID
	item      models.AgentCapabilitySnapshotItem
	snapshot  []models.AgentCapabilitySnapshotItem
}

func (a *recordingApprovedGrantAppender) AppendApprovedSessionGrant(ctx context.Context, orgID, sessionID uuid.UUID, item models.AgentCapabilitySnapshotItem) ([]models.AgentCapabilitySnapshotItem, error) {
	a.orgID = orgID
	a.sessionID = sessionID
	a.item = item
	a.snapshot = []models.AgentCapabilitySnapshotItem{item}
	return a.snapshot, nil
}

func (m *memoryPolicyStore) GetAutomationPolicy(ctx context.Context, orgID, automationID uuid.UUID) (models.AgentCapabilityPolicy, error) {
	return models.AgentCapabilityPolicy{}, ErrPolicyNotFound
}

func snapshotIDs(snapshot []models.AgentCapabilitySnapshotItem) []models.AgentCapabilityID {
	out := make([]models.AgentCapabilityID, 0, len(snapshot))
	for _, item := range snapshot {
		out = append(out, item.ID)
	}
	return out
}
