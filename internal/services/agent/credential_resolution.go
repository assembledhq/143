package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
)

// resolvedAgentEnv belongs to one execution attempt. Keep both secret-bearing
// fields out of JSON, including when this value is marshalled on its own.
type resolvedAgentEnv struct {
	Env        map[string]string           `json:"-"`
	Credential *runtimeCredentialSelection `json:"-"`

	// An unsuccessful lookup is transient and must not be mistaken for an
	// authoritative empty credential stack or trigger a shared-cache repick.
	credentialAuthoritative bool  `json:"-"`
	credentialLookupErr     error `json:"-"`
}

type runtimeCredentialSelection struct {
	Binding    runtimeCredentialBinding
	Credential models.DecryptedCodingCredential `json:"-"`
}

func captureRuntimeCredential(credential models.DecryptedCodingCredential, agentType models.AgentType, env map[string]string) *runtimeCredentialSelection {
	if credential.UserID != nil {
		owner := *credential.UserID
		credential.UserID = &owner
	}
	// The provider configs otherwise contain values only. Clone the one slice
	// so callers cannot mutate a store-owned config through this snapshot.
	if cfg, ok := credential.Config.(models.AnthropicSubscriptionConfig); ok {
		cfg.Scopes = append([]string(nil), cfg.Scopes...)
		credential.Config = cfg
	}
	binding := runtimeCredentialBindingForPick(credential.Provider, credential.ID, &credential)
	binding.AgentType = agentType
	if binding.CredentialOwnerID != nil {
		owner := *binding.CredentialOwnerID
		binding.CredentialOwnerID = &owner
	}
	if model := env[models.ModelEnvVarForAgentType(agentType)]; model != "" {
		binding.EffectiveModel = model
	}
	return &runtimeCredentialSelection{Binding: binding, Credential: credential}
}

// ShedRateLimitedForCredential attributes a failure to the credential that
// actually executed, regardless of later picks or legacy tracker eviction.
func (e *AgentEnv) ShedRateLimitedForCredential(ctx context.Context, selection *runtimeCredentialSelection, signal CredentialFailureSignal) {
	shedder := e.codingShedder()
	if shedder == nil || selection == nil {
		return
	}
	credential := selection.Credential
	limit := models.CodingCredentialRateLimit{Until: signal.RateLimitedUntil, Message: signal.Message}
	if limit.Until.IsZero() {
		limit.Until = time.Now().Add(75 * time.Second)
	}
	if persistent, ok := shedder.(CodingCredentialPersistentShedder); ok {
		if err := persistent.MarkRateLimitedForScope(ctx, credential.Scope(), credential.ID, limit); err != nil {
			e.logger.Warn().Err(err).
				Str("cred_id", credential.ID.String()).
				Str("provider", string(credential.Provider)).
				Msg("failed to persist coding credential rate-limit marker")
		}
		return
	}
	shedder.MarkRateLimited(credential.ID)
}

// ShedAuthRejectedForCredential uses the execution's actual backing scope,
// which can differ from the requesting user's scope when org fallback won.
func (e *AgentEnv) ShedAuthRejectedForCredential(ctx context.Context, selection *runtimeCredentialSelection) {
	shedder := e.codingShedder()
	if shedder == nil || selection == nil {
		return
	}
	credential := selection.Credential
	if persistent, ok := shedder.(CodingCredentialPersistentShedder); ok {
		if err := persistent.MarkAuthRejectedForScope(ctx, credential.Scope(), credential.ID); err != nil {
			e.logger.Warn().Err(err).
				Str("cred_id", credential.ID.String()).
				Str("provider", string(credential.Provider)).
				Msg("failed to persist coding credential auth rejection")
		}
		return
	}
	shedder.MarkAuthRejected(credential.ID)
}

// InjectCodexAuthForCredential never chooses another credential. A nil
// selection means this resolution found no unified credential, so it must not
// fall back to a shared pick from another execution.
func (e *AgentEnv) InjectCodexAuthForCredential(ctx context.Context, orgID uuid.UUID, sandbox *Sandbox, selection *runtimeCredentialSelection) (bool, error) {
	if selection == nil {
		return false, nil
	}
	credential := selection.Credential
	if credential.OrgID != orgID {
		return false, fmt.Errorf("codex credential belongs to a different organization")
	}
	cfg, ok := codexSubscriptionConfigFromPicked(credential)
	if !ok {
		return false, nil
	}
	if credential.Provider == models.ProviderOpenAISubscription {
		refreshed, err := e.refreshCodexSubscriptionIfNeeded(ctx, credential.Scope(), credential.ID, cfg)
		if err != nil {
			return false, err
		}
		cfg = *refreshed
	}
	return e.writeCodexAuth(ctx, orgID, sandbox, cfg)
}

// RefreshCodexAuthForCredential handles token_expired by asking the auth
// service to reload or refresh the same row and reinjecting its returned
// config. The auth service decides whether the token needs refreshing.
func (e *AgentEnv) RefreshCodexAuthForCredential(ctx context.Context, orgID uuid.UUID, sandbox *Sandbox, selection *runtimeCredentialSelection) (bool, error) {
	if selection == nil {
		return false, nil
	}
	credential := selection.Credential
	if credential.OrgID != orgID {
		return false, fmt.Errorf("codex credential belongs to a different organization")
	}
	if _, ok := codexSubscriptionConfigFromPicked(credential); !ok {
		return false, nil
	}
	refresher, ok := e.codexAuth.(CodexAuthRefresher)
	if !ok || credential.Provider != models.ProviderOpenAISubscription {
		return e.InjectCodexAuthForCredential(ctx, orgID, sandbox, selection)
	}
	cfg, err := refresher.RefreshTokenByID(ctx, credential.Scope(), credential.ID)
	if err != nil {
		return false, maybeWrapCodexAuthInvalid(refresher, fmt.Errorf("refresh codex subscription %s: %w", credential.ID, err))
	}
	if cfg == nil {
		return false, wrapCodexAuthInvalid(fmt.Errorf("refresh codex subscription %s returned no token", credential.ID))
	}
	return e.writeCodexAuth(ctx, orgID, sandbox, *cfg)
}
