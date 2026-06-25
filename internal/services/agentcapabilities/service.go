package agentcapabilities

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrPolicyNotFound = errors.New("agent capability policy not found")

// ErrInvalidGrant is the sentinel wrapped by all ValidateGrant failures.
// Handlers can test errors.Is(err, ErrInvalidGrant) to distinguish input
// validation rejections from infrastructure errors.
var ErrInvalidGrant = errors.New("invalid capability grant")

type PolicyReader interface {
	GetSessionDefaultPolicy(ctx context.Context, orgID uuid.UUID) (models.AgentCapabilityPolicy, error)
	GetAutomationPolicy(ctx context.Context, orgID, automationID uuid.UUID) (models.AgentCapabilityPolicy, error)
}

type HumanInputRequestWriter interface {
	Create(ctx context.Context, req *models.HumanInputRequest) error
}

type ApprovedGrantAppender interface {
	AppendApprovedSessionGrant(ctx context.Context, orgID, sessionID uuid.UUID, item models.AgentCapabilitySnapshotItem) ([]models.AgentCapabilitySnapshotItem, error)
}

type Service struct {
	policies       PolicyReader
	requests       HumanInputRequestWriter
	approvedGrants ApprovedGrantAppender
	catalog        map[models.AgentCapabilityID]models.AgentCapabilityDefinition
}

type ResolveInput struct {
	OrgID            uuid.UUID
	RepositoryID     *uuid.UUID
	SessionOrigin    models.SessionOrigin
	AutomationID     *uuid.UUID
	AutomationRunID  *uuid.UUID
	TemplateID       string
	ExistingSnapshot []models.AgentCapabilitySnapshotItem
}

func NewService(policies PolicyReader) *Service {
	defs := catalogDefinitions()
	catalog := make(map[models.AgentCapabilityID]models.AgentCapabilityDefinition, len(defs))
	for _, def := range defs {
		catalog[def.ID] = def
	}
	return &Service{policies: policies, catalog: catalog}
}

func (s *Service) SetHumanInputRequestWriter(requests HumanInputRequestWriter) {
	s.requests = requests
}

func (s *Service) SetApprovedGrantAppender(appender ApprovedGrantAppender) {
	s.approvedGrants = appender
}

func (s *Service) Definitions() []models.AgentCapabilityDefinition {
	defs := make([]models.AgentCapabilityDefinition, 0, len(s.catalog))
	for _, def := range s.catalog {
		defs = append(defs, def)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].ID < defs[j].ID })
	return defs
}

func (s *Service) ResolveAvailability(ctx context.Context, orgID uuid.UUID, repoID *uuid.UUID) ([]models.AgentCapabilityDefinition, error) {
	defs := s.Definitions()
	for i := range defs {
		available := true
		reason := ""
		if defs[i].Scope == models.AgentCapabilityScopeRepository && repoID == nil {
			available = false
			reason = "Requires a repository-scoped session."
		}
		defs[i].Availability = &models.AgentCapabilityAvailability{Available: available, Reason: reason}
	}
	return defs, nil
}

func (s *Service) ValidateGrant(grant models.AgentCapabilityPolicyGrantInput) error {
	if err := grant.CapabilityID.Validate(); err != nil {
		return fmt.Errorf("%w: INVALID_CAPABILITY: %v", ErrInvalidGrant, err)
	}
	if err := grant.AccessLevel.Validate(); err != nil {
		return fmt.Errorf("%w: INVALID_CAPABILITY_ACCESS: %v", ErrInvalidGrant, err)
	}
	def, ok := s.catalog[grant.CapabilityID]
	if !ok {
		return fmt.Errorf("%w: INVALID_CAPABILITY: %q", ErrInvalidGrant, grant.CapabilityID)
	}
	if !accessAllowed(grant.AccessLevel, def.MaxAccessLevel) {
		return fmt.Errorf("%w: INVALID_CAPABILITY_ACCESS: %s exceeds %s", ErrInvalidGrant, grant.AccessLevel, def.MaxAccessLevel)
	}
	if len(grant.Config) > 0 && !json.Valid(grant.Config) {
		return fmt.Errorf("%w: INVALID_CAPABILITY_CONFIG: config must be valid JSON", ErrInvalidGrant)
	}
	if len(grant.Config) > 0 {
		var obj map[string]any
		if err := json.Unmarshal(grant.Config, &obj); err != nil {
			return fmt.Errorf("%w: INVALID_CAPABILITY_CONFIG: config must be a JSON object", ErrInvalidGrant)
		}
	}
	return nil
}

type GrantRequestInput struct {
	OrgID       uuid.UUID
	SessionID   uuid.UUID
	ThreadID    *uuid.UUID
	TurnNumber  int
	AgentType   models.AgentType
	Capability  models.AgentCapabilityID
	AccessLevel models.AgentCapabilityAccessLevel
	Reason      string
}

type ApprovedGrantInput struct {
	OrgID               uuid.UUID
	SessionID           uuid.UUID
	HumanInputRequestID uuid.UUID
	Capability          models.AgentCapabilityID
	AccessLevel         models.AgentCapabilityAccessLevel
}

func (s *Service) RequestGrant(ctx context.Context, in GrantRequestInput) (models.HumanInputRequest, error) {
	if s.requests == nil {
		return models.HumanInputRequest{}, fmt.Errorf("capability request store is not configured")
	}
	if err := s.ValidateGrant(models.AgentCapabilityPolicyGrantInput{
		CapabilityID: in.Capability,
		AccessLevel:  in.AccessLevel,
		Enabled:      true,
		Config:       json.RawMessage(`{}`),
	}); err != nil {
		return models.HumanInputRequest{}, err
	}
	def := s.catalog[in.Capability]
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		reason = "The agent requested this capability to continue the current task."
	}
	kind := models.HumanInputRequestKindActionChoice
	if in.AccessLevel == models.AgentCapabilityAccessWrite || in.AccessLevel == models.AgentCapabilityAccessPublish {
		kind = models.HumanInputRequestKindToolApproval
	}
	payload, err := json.Marshal(map[string]any{
		"type":          "agent_capability_request",
		"capability_id": string(in.Capability),
		"access_level":  string(in.AccessLevel),
		"reason":        reason,
	})
	if err != nil {
		return models.HumanInputRequest{}, fmt.Errorf("marshal capability request payload: %w", err)
	}
	req := models.HumanInputRequest{
		OrgID:            in.OrgID,
		SessionID:        in.SessionID,
		ThreadID:         in.ThreadID,
		TurnNumber:       in.TurnNumber,
		AgentType:        in.AgentType,
		Kind:             kind,
		Status:           models.HumanInputRequestStatusPending,
		Title:            "Approve agent capability",
		Body:             fmt.Sprintf("The agent requested %s (%s access).\n\nReason: %s", def.DisplayName, in.AccessLevel, reason),
		Sensitivity:      models.HumanInputSensitivityTeam,
		PreferredChannel: models.HumanInputPreferredChannelWeb,
		Choices: []models.HumanInputChoice{
			{ID: "approve", Label: "Approve", Description: "Grant this capability for the current session only."},
			{ID: "deny", Label: "Deny", Description: "Resume without this capability.", Destructive: true},
		},
		ProviderPayload: payload,
	}
	if err := s.requests.Create(ctx, &req); err != nil {
		return models.HumanInputRequest{}, err
	}
	return req, nil
}

func (s *Service) ApplyApprovedGrant(ctx context.Context, in ApprovedGrantInput) ([]models.AgentCapabilitySnapshotItem, error) {
	if s.approvedGrants == nil {
		return nil, fmt.Errorf("approved capability grant store is not configured")
	}
	if err := s.ValidateGrant(models.AgentCapabilityPolicyGrantInput{
		CapabilityID: in.Capability,
		AccessLevel:  in.AccessLevel,
		Enabled:      true,
		Config:       json.RawMessage(`{}`),
	}); err != nil {
		return nil, err
	}
	def := s.catalog[in.Capability]
	return s.approvedGrants.AppendApprovedSessionGrant(ctx, in.OrgID, in.SessionID, models.AgentCapabilitySnapshotItem{
		ID:                  in.Capability,
		DisplayName:         def.DisplayName,
		AccessLevel:         in.AccessLevel,
		Risk:                def.Risk,
		Scope:               def.Scope,
		Config:              json.RawMessage(`{}`),
		Source:              models.AgentCapabilityGrantSourceUserApproved,
		GrantedAt:           time.Now().UTC(),
		HumanInputRequestID: &in.HumanInputRequestID,
	})
}

func (s *Service) ResolveForSession(ctx context.Context, in ResolveInput) ([]models.AgentCapabilitySnapshotItem, error) {
	grants, source, err := s.resolvePolicyGrants(ctx, in)
	if err != nil {
		return nil, err
	}
	if len(grants) == 0 {
		grants = recommendedDefaultGrants(in)
		source = models.AgentCapabilityGrantSourceSessionDefault
	}
	now := time.Now().UTC()
	snapshot := make([]models.AgentCapabilitySnapshotItem, 0, len(grants)+len(in.ExistingSnapshot))
	seen := make(map[models.AgentCapabilityID]bool)
	for _, existing := range in.ExistingSnapshot {
		snapshot = append(snapshot, existing)
		seen[existing.ID] = true
	}
	for _, grant := range grants {
		if !grant.Enabled || seen[grant.CapabilityID] {
			continue
		}
		if err := s.ValidateGrant(models.AgentCapabilityPolicyGrantInput{
			CapabilityID: grant.CapabilityID,
			AccessLevel:  grant.AccessLevel,
			Enabled:      grant.Enabled,
			Config:       grant.Config,
		}); err != nil {
			// Grant was valid when stored but is now rejected by the catalog
			// (e.g. capability removed or MaxAccessLevel lowered). Skip it so a
			// catalog update does not break existing sessions.
			continue
		}
		def := s.catalog[grant.CapabilityID]
		if def.Scope == models.AgentCapabilityScopeRepository && in.RepositoryID == nil {
			continue
		}
		config := grant.Config
		if len(config) == 0 {
			config = json.RawMessage(`{}`)
		}
		snapshot = append(snapshot, models.AgentCapabilitySnapshotItem{
			ID:          grant.CapabilityID,
			DisplayName: def.DisplayName,
			AccessLevel: grant.AccessLevel,
			Risk:        def.Risk,
			Scope:       def.Scope,
			Config:      config,
			Source:      source,
			GrantedAt:   now,
		})
		seen[grant.CapabilityID] = true
	}
	return snapshot, nil
}

func (s *Service) resolvePolicyGrants(ctx context.Context, in ResolveInput) ([]models.AgentCapabilityGrant, models.AgentCapabilityGrantSource, error) {
	if s.policies == nil {
		return nil, models.AgentCapabilityGrantSourceSessionDefault, nil
	}
	if in.AutomationID != nil {
		policy, err := s.policies.GetAutomationPolicy(ctx, in.OrgID, *in.AutomationID)
		if err == nil {
			return policy.Grants, models.AgentCapabilityGrantSourceAutomation, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) && !errors.Is(err, ErrPolicyNotFound) {
			return nil, "", err
		}
	}
	policy, err := s.policies.GetSessionDefaultPolicy(ctx, in.OrgID)
	if err == nil {
		return policy.Grants, models.AgentCapabilityGrantSourceSessionDefault, nil
	}
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ErrPolicyNotFound) {
		return nil, models.AgentCapabilityGrantSourceSessionDefault, nil
	}
	return nil, "", err
}

// recommendedDefaultEnabledCapabilities is the set of capabilities that ship
// enabled by default when an org has not configured a session-default policy.
// These cover the broadly-useful, commonly-needed capabilities (code/PR/test
// context plus branch & PR publishing); higher-risk write and production-data
// capabilities stay opt-in. Keep in sync with RECOMMENDED_DEFAULT_CAPABILITY_IDS
// in frontend/src/components/automation-capabilities-editor.tsx. Note that
// issue_sources is intentionally excluded here and instead added only for
// triggered sessions below.
var recommendedDefaultEnabledCapabilities = map[models.AgentCapabilityID]bool{
	models.AgentCapabilityRepoContext:    true,
	models.AgentCapabilityPRHistory:      true,
	models.AgentCapabilityReviewFeedback: true,
	models.AgentCapabilityCIHistory:      true,
	models.AgentCapabilityPublishing:     true,
}

func recommendedDefaultGrants(in ResolveInput) []models.AgentCapabilityGrant {
	defs := catalogDefinitions()
	grants := make([]models.AgentCapabilityGrant, 0, len(recommendedDefaultEnabledCapabilities)+1)
	for _, def := range defs {
		if !recommendedDefaultEnabledCapabilities[def.ID] {
			continue
		}
		grants = append(grants, models.AgentCapabilityGrant{
			CapabilityID: def.ID,
			AccessLevel:  def.MaxAccessLevel,
			Enabled:      true,
			Config:       json.RawMessage(`{}`),
		})
	}
	// Issue context (Linear/Sentry/support) is only relevant when a session was
	// triggered by an external issue source, so scope it to those origins
	// rather than enabling it for every manual session.
	if in.SessionOrigin == models.SessionOriginIssueTrigger || in.SessionOrigin == models.SessionOriginSlack || in.SessionOrigin == models.SessionOriginExternalAPI {
		grants = append(grants, models.AgentCapabilityGrant{CapabilityID: models.AgentCapabilityIssueSources, AccessLevel: models.AgentCapabilityAccessRead, Enabled: true, Config: json.RawMessage(`{}`)})
	}
	return grants
}

func accessAllowed(requested, max models.AgentCapabilityAccessLevel) bool {
	rank := map[models.AgentCapabilityAccessLevel]int{
		models.AgentCapabilityAccessRead:    1,
		models.AgentCapabilityAccessWrite:   2,
		models.AgentCapabilityAccessPublish: 3,
	}
	return rank[requested] <= rank[max]
}

func catalogDefinitions() []models.AgentCapabilityDefinition {
	return []models.AgentCapabilityDefinition{
		def(models.AgentCapabilityRepoContext, "Repository context", "Code, docs, and repository-local facts.", "Context", models.AgentCapabilityAccessRead, models.AgentCapabilityRiskLow, models.AgentCapabilityScopeRepository),
		def(models.AgentCapabilityPRHistory, "PR history", "Recent pull requests, reviews, and repository conventions.", "Context", models.AgentCapabilityAccessRead, models.AgentCapabilityRiskLow, models.AgentCapabilityScopeRepository),
		def(models.AgentCapabilitySessionHistory, "Session history", "Prior 143 sessions for this org and repository.", "Context", models.AgentCapabilityAccessRead, models.AgentCapabilityRiskMedium, models.AgentCapabilityScopeRepository),
		def(models.AgentCapabilityReviewFeedback, "Review feedback", "Review comments and learned feedback patterns.", "Context", models.AgentCapabilityAccessRead, models.AgentCapabilityRiskMedium, models.AgentCapabilityScopeRepository),
		def(models.AgentCapabilityCIHistory, "CI/test history", "Test failures and flaky-test evidence.", "Diagnostics", models.AgentCapabilityAccessRead, models.AgentCapabilityRiskMedium, models.AgentCapabilityScopeRepository),
		def(models.AgentCapabilityIssueSources, "Issue sources", "Linear, Sentry, and support-derived issue context.", "Context", models.AgentCapabilityAccessRead, models.AgentCapabilityRiskMedium, models.AgentCapabilityScopeIntegration),
		def(models.AgentCapabilityTeamDocs, "Team docs/messages", "Notion, Slack, architecture, and product context.", "Context", models.AgentCapabilityAccessRead, models.AgentCapabilityRiskMedium, models.AgentCapabilityScopeIntegration),
		def(models.AgentCapabilityProductionDiagnostics, "Production diagnostics", "Bounded read-only production logs and error-tracker evidence.", "Diagnostics", models.AgentCapabilityAccessRead, models.AgentCapabilityRiskHigh, models.AgentCapabilityScopeIntegration),
		def(models.AgentCapabilityExternalComments, "External comments", "Linear and Slack comments or status updates.", "Actions", models.AgentCapabilityAccessWrite, models.AgentCapabilityRiskMedium, models.AgentCapabilityScopeIntegration),
		def(models.AgentCapabilitySlackNotifications, "Slack notifications", "Send a Slack completion or status message through the connected 143 Slack app.", "Actions", models.AgentCapabilityAccessWrite, models.AgentCapabilityRiskMedium, models.AgentCapabilityScopeIntegration),
		def(models.AgentCapabilityAutomationManagement, "Automation management", "Create and manage repo-scoped automations through 143 workflows.", "Actions", models.AgentCapabilityAccessWrite, models.AgentCapabilityRiskHigh, models.AgentCapabilityScopeRepository),
		def(models.AgentCapabilityProjectProposals, "Project proposals", "Planning and project proposal creation.", "Planning", models.AgentCapabilityAccessWrite, models.AgentCapabilityRiskMedium, models.AgentCapabilityScopeOrg),
		def(models.AgentCapabilityEvalAuthoring, "Eval authoring", "Eval candidate creation.", "Planning", models.AgentCapabilityAccessWrite, models.AgentCapabilityRiskHigh, models.AgentCapabilityScopeOrg),
		def(models.AgentCapabilityPublishing, "Branch and PR publishing", "Branch and pull request publication through 143 workflows.", "Actions", models.AgentCapabilityAccessPublish, models.AgentCapabilityRiskHigh, models.AgentCapabilityScopeRepository),
	}
}

func def(id models.AgentCapabilityID, displayName, description, category string, max models.AgentCapabilityAccessLevel, risk models.AgentCapabilityRisk, scope models.AgentCapabilityScope) models.AgentCapabilityDefinition {
	return models.AgentCapabilityDefinition{
		ID:             id,
		DisplayName:    displayName,
		Description:    description,
		Category:       category,
		MaxAccessLevel: max,
		Risk:           risk,
		Scope:          scope,
		DefaultConfig:  json.RawMessage(`{}`),
		Availability:   &models.AgentCapabilityAvailability{Available: true},
	}
}
