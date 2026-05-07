package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

// This file holds request-validation helpers used by automations.go. They were
// extracted so the main handler file stays focused on HTTP-level routing and
// response shaping — mixing field-level validation in kept that file creeping
// past the 800-line mark where handler files in this codebase start to become
// hard to scan.

// automationNameMaxLength and automationGoalMaxLength mirror the
// chk_automations_name_length and chk_automations_goal_length CHECK
// constraints. Keeping them at this layer surfaces a 10MB body as a 400 rather
// than a Postgres constraint violation — the user-legible error.
const (
	automationNameMaxLength = 200
	automationGoalMaxLength = 64000
)

func validateAutomationNameAndGoal(name, goal string) error {
	if len(name) > automationNameMaxLength {
		return fmt.Errorf("name must be at most %d characters", automationNameMaxLength)
	}
	if len(goal) > automationGoalMaxLength {
		return fmt.Errorf("goal must be at most %d characters", automationGoalMaxLength)
	}
	return nil
}

// validateBaseBranch rejects branch names that obviously can't be refs:
// empty/whitespace, path traversal, or embedded whitespace. Intentionally
// conservative — libgit2 has stricter rules but applying them here would
// duplicate logic we'd have to keep in sync with git's rules. The callsite
// (repo checkout) will fail loudly on anything we let through.
func validateBaseBranch(b string) error {
	trimmed := strings.TrimSpace(b)
	if trimmed == "" {
		return fmt.Errorf("base_branch must not be empty")
	}
	if trimmed != b {
		return fmt.Errorf("base_branch must not contain leading/trailing whitespace")
	}
	if strings.ContainsAny(b, " \t\n\r") {
		return fmt.Errorf("base_branch must not contain whitespace")
	}
	if strings.Contains(b, "..") {
		return fmt.Errorf("base_branch must not contain '..'")
	}
	return nil
}

// validateTimezone rejects strings that time.LoadLocation can't parse. Without
// this, a malformed timezone would be silently stored and later fail at
// schedule evaluation time — far from the user's write.
func validateTimezone(tz string) error {
	if _, err := time.LoadLocation(tz); err != nil {
		return fmt.Errorf("invalid timezone %q", tz)
	}
	return nil
}

// resolveRepositoryID parses a repository_id from a request and verifies it
// belongs to orgID and is still active. Returns nil + nil for empty input.
// Errors are user-safe and can be returned directly from handlers; the
// errRepoDisconnected sentinel (defined in repo_active.go) lets handlers
// distinguish disconnected repos so they can return REPO_DISCONNECTED.
func (h *AutomationHandler) resolveRepositoryID(ctx context.Context, orgID uuid.UUID, raw string) (*uuid.UUID, error) {
	if raw == "" {
		return nil, nil
	}
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid repository_id")
	}
	if _, err := requireActiveRepo(ctx, h.repoStore, orgID, parsed); err != nil {
		switch {
		case errors.Is(err, errRepoDisconnected):
			return nil, errRepoDisconnected
		case errors.Is(err, errRepoStoreUnconfigured):
			return nil, fmt.Errorf("repository lookup not configured")
		default:
			return nil, fmt.Errorf("repository not found in this org")
		}
	}
	return &parsed, nil
}

func cloneOptionalString(v *string) *string {
	if v == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*v)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func resolveAutomationAgentAndModel(currentAgentType, currentModel, reqAgentType, reqModel *string) (*string, *string, error) {
	effectiveAgentType := cloneOptionalString(currentAgentType)
	effectiveModel := cloneOptionalString(currentModel)

	if reqAgentType != nil {
		effectiveAgentType = cloneOptionalString(reqAgentType)
		if effectiveAgentType != nil {
			if err := models.AgentType(*effectiveAgentType).Validate(); err != nil {
				return nil, nil, err
			}
		}
	}

	if reqModel != nil {
		effectiveModel = cloneOptionalString(reqModel)
	}

	if effectiveModel == nil {
		return effectiveAgentType, nil, nil
	}

	if effectiveAgentType == nil {
		inferred := models.AgentTypeForModel(*effectiveModel)
		if inferred == "" {
			return nil, nil, fmt.Errorf("model %q is not recognized", *effectiveModel)
		}
		s := string(inferred)
		effectiveAgentType = &s
	}

	if err := models.ValidateModelForAgentType(models.AgentType(*effectiveAgentType), *effectiveModel); err != nil {
		return nil, nil, err
	}

	return effectiveAgentType, effectiveModel, nil
}

func (h *AutomationHandler) validateAutomationModelAvailability(ctx context.Context, orgID uuid.UUID, agentType *string, modelOverride *string) error {
	if agentType == nil || modelOverride == nil {
		return nil
	}

	available, err := h.isAutomationAgentAvailable(ctx, orgID, models.AgentType(*agentType))
	if err != nil {
		return err
	}
	if available {
		return nil
	}

	return fmt.Errorf("model %q is not available for agent %q; configure a team-usable credential first", *modelOverride, *agentType)
}

func (h *AutomationHandler) defaultAutomationAgentType(ctx context.Context, orgID uuid.UUID, explicit *string) (models.AgentType, error) {
	if explicit != nil && strings.TrimSpace(*explicit) != "" {
		return models.AgentType(strings.TrimSpace(*explicit)), nil
	}
	if h.orgStore != nil {
		org, err := h.orgStore.GetByID(ctx, orgID)
		if err != nil {
			return "", fmt.Errorf("load organization settings: %w", err)
		}
		settings, err := models.ParseOrgSettings(org.Settings)
		if err != nil {
			h.logger.Warn().Err(err).Str("org_id", orgID.String()).Msg("failed to parse org settings for automation default agent; falling back to default")
			return models.DefaultDefaultAgentType, nil
		}
		if settings.DefaultAgentType != "" {
			return settings.DefaultAgentType, nil
		}
	}
	return models.DefaultDefaultAgentType, nil
}

func (h *AutomationHandler) isAutomationAgentAvailable(ctx context.Context, orgID uuid.UUID, agentType models.AgentType) (bool, error) {
	if agentType == "" {
		return false, nil
	}

	checkedAvailabilitySource := false

	if h.orgStore != nil {
		checkedAvailabilitySource = true
		org, err := h.orgStore.GetByID(ctx, orgID)
		if err != nil {
			return false, fmt.Errorf("load organization settings: %w", err)
		}
		settings, err := models.ParseOrgSettings(org.Settings)
		if err != nil {
			return false, fmt.Errorf("parse organization settings: %w", err)
		}
		if hasAutomationAgentConfigKey(settings.AgentConfig, agentType) {
			return true, nil
		}
	}

	if h.codingAuthStore != nil {
		checkedAvailabilitySource = true
		rows, err := h.codingAuthStore.ListCodingAuths(ctx, orgID)
		if err != nil {
			return false, fmt.Errorf("list coding auths: %w", err)
		}
		for _, row := range rows {
			if row.Agent == agentType && (row.Status == models.CodingAuthStatusHealthy || row.Status == models.CodingAuthStatusRateLimited) {
				return true, nil
			}
		}
	}

	if h.codingCredentialStore != nil {
		checkedAvailabilitySource = true
		rows, err := h.codingCredentialStore.ListByScope(ctx, models.Scope{OrgID: orgID})
		if err != nil {
			return false, fmt.Errorf("list org coding credentials: %w", err)
		}
		for _, row := range rows {
			if row.Status != models.CodingCredentialStatusActive {
				continue
			}
			if codingCredentialAgentType(row.Provider) == agentType {
				return true, nil
			}
		}
	}

	provider := automationProviderForAgent(agentType)
	if provider == "" {
		return false, nil
	}

	if h.userCredentialStore != nil {
		checkedAvailabilitySource = true
		rows, err := h.userCredentialStore.ListTeamDefaults(ctx, orgID)
		if err != nil {
			return false, fmt.Errorf("list team default credentials: %w", err)
		}
		for _, row := range rows {
			if row.Provider == provider {
				return true, nil
			}
		}
	}

	if h.orgCredentialStore != nil {
		checkedAvailabilitySource = true
		if creds, err := h.orgCredentialStore.ListByProvider(ctx, orgID, provider); err == nil {
			for _, cred := range creds {
				if cred.Config.MaskedSummary().MaskedKey != "" {
					return true, nil
				}
			}
		}
		if cred, err := h.orgCredentialStore.Get(ctx, orgID, provider); err == nil && cred != nil && cred.Config.MaskedSummary().MaskedKey != "" {
			return true, nil
		}
	}

	if !checkedAvailabilitySource {
		return true, nil
	}

	return false, nil
}

func hasAutomationAgentConfigKey(cfg models.AgentEnvConfig, agentType models.AgentType) bool {
	if cfg == nil {
		return false
	}
	keys, ok := cfg[string(agentType)]
	if !ok {
		return false
	}
	keyName := automationAgentConfigSecretKey(agentType)
	if keyName == "" {
		return false
	}
	return strings.TrimSpace(keys[keyName]) != ""
}

func automationAgentConfigSecretKey(agentType models.AgentType) string {
	switch agentType {
	case models.AgentTypeCodex:
		return "OPENAI_API_KEY"
	case models.AgentTypeClaudeCode:
		return "ANTHROPIC_API_KEY"
	case models.AgentTypeGeminiCLI:
		return "GEMINI_API_KEY"
	case models.AgentTypeAmp:
		return "AMP_API_KEY"
	case models.AgentTypePi:
		return "PI_API_KEY"
	default:
		return ""
	}
}

func automationProviderForAgent(agentType models.AgentType) models.ProviderName {
	switch agentType {
	case models.AgentTypeClaudeCode:
		return models.ProviderAnthropic
	case models.AgentTypeCodex:
		return models.ProviderOpenAI
	case models.AgentTypeGeminiCLI:
		return models.ProviderGemini
	case models.AgentTypeAmp:
		return models.ProviderAmp
	case models.AgentTypePi:
		return models.ProviderPi
	default:
		return ""
	}
}

func codingCredentialAgentType(provider models.ProviderName) models.AgentType {
	switch provider {
	case models.ProviderAnthropic, models.ProviderAnthropicSubscription:
		return models.AgentTypeClaudeCode
	case models.ProviderOpenAI, models.ProviderOpenAIChatGPT, models.ProviderOpenAISubscription:
		return models.AgentTypeCodex
	case models.ProviderGemini:
		return models.AgentTypeGeminiCLI
	case models.ProviderAmp:
		return models.AgentTypeAmp
	case models.ProviderPi:
		return models.AgentTypePi
	default:
		return ""
	}
}
