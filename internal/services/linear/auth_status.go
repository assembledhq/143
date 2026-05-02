package linear

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
)

// authErrorReasonUnauthorized is the human-readable reason stamped into the
// integrations.config jsonb when Linear rejects the access token. The
// integrations settings UI renders this verbatim above the Reconnect CTA, so
// keep it terse and operator-actionable rather than dropping the raw error.
const authErrorReasonUnauthorized = "Linear rejected the access token (HTTP 401). Reconnect to continue syncing."

// configAuthErrorKey / configAuthErrorAtKey are the jsonb keys we stamp on
// integrations.config. We keep them inline (rather than introducing a new
// column) per design call: zero migration, and the only consumer is the
// integrations settings page which already deserializes config to display
// workspace metadata.
const (
	configAuthErrorKey   = "last_auth_error"
	configAuthErrorAtKey = "last_auth_error_at"
)

// MarkIntegrationUnauthorized flips the org's Linear integration row to
// IntegrationStatusError and stamps an auth-error reason + timestamp into
// the existing config jsonb. Best-effort: every step logs and swallows on
// failure because this is a side-channel signal — the caller is in the
// middle of handling the original ErrUnauthorized and shouldn't have its
// retry/dead-letter contract perturbed by a status-write hiccup.
//
// The function is the missing "integration health check" from the comment
// at client.go:664 turned inside-out: instead of a periodic probe, every
// 401 anywhere becomes the probe. Combined with ClearIntegrationUnauthorized
// from a successful call, the credential status reflects reality within
// one Linear API round-trip.
//
// Nil-safe in three layers: nil receiver, nil writer (tests / api-only mode
// without write surface), and "integration row not found" (org never
// installed Linear) all fall through silently.
func (s *Service) MarkIntegrationUnauthorized(ctx context.Context, orgID uuid.UUID) {
	if s == nil || s.integrationsWriter == nil || s.integrations == nil {
		return
	}
	integration, err := s.integrations.GetByOrgAndProvider(ctx, orgID, string(models.IntegrationProviderLinear))
	if err != nil {
		s.logger.Debug().Err(err).
			Str("org_id", orgID.String()).
			Msg("MarkIntegrationUnauthorized: integration lookup failed; skipping status flip")
		return
	}
	cfg := decodeIntegrationConfig(integration.Config)
	// Skip the writes if we're already in error state with a fresh-enough
	// reason: re-stamping every retry would create churn on the integrations
	// row updated_at and noise in any future audit trail.
	if integration.Status == models.IntegrationStatusError && hasRecentAuthError(cfg) {
		return
	}
	cfg[configAuthErrorKey] = authErrorReasonUnauthorized
	cfg[configAuthErrorAtKey] = time.Now().UTC().Format(time.RFC3339)
	raw, err := json.Marshal(cfg)
	if err != nil {
		s.logger.Warn().Err(err).
			Str("org_id", orgID.String()).
			Msg("MarkIntegrationUnauthorized: marshal config failed; status flip skipped")
		return
	}
	if err := s.integrationsWriter.UpdateConfig(ctx, orgID, integration.ID, raw); err != nil {
		s.logger.Warn().Err(err).
			Str("org_id", orgID.String()).
			Str("integration_id", integration.ID.String()).
			Msg("MarkIntegrationUnauthorized: persist config patch failed")
	}
	if err := s.integrationsWriter.UpdateStatus(ctx, orgID, integration.ID, string(models.IntegrationStatusError)); err != nil {
		s.logger.Warn().Err(err).
			Str("org_id", orgID.String()).
			Str("integration_id", integration.ID.String()).
			Msg("MarkIntegrationUnauthorized: persist status flip failed")
		return
	}
	s.logger.Warn().
		Str("org_id", orgID.String()).
		Str("integration_id", integration.ID.String()).
		Msg("Linear integration marked errored after 401 — user must reconnect")
}

// ClearIntegrationUnauthorized clears the auth-error markers and flips the
// status back to active when the row was previously in error state. Called
// from any successful Linear API path so a reconnect (or a transient blip
// resolving on its own) un-sticks the banner without operator intervention.
//
// No-op when the row is already active and free of stale markers.
func (s *Service) ClearIntegrationUnauthorized(ctx context.Context, orgID uuid.UUID) {
	if s == nil || s.integrationsWriter == nil || s.integrations == nil {
		return
	}
	integration, err := s.integrations.GetByOrgAndProvider(ctx, orgID, string(models.IntegrationProviderLinear))
	if err != nil {
		return
	}
	cfg := decodeIntegrationConfig(integration.Config)
	hadError := configHasAuthError(cfg)
	statusErrored := integration.Status == models.IntegrationStatusError
	if !hadError && !statusErrored {
		return
	}
	if hadError {
		delete(cfg, configAuthErrorKey)
		delete(cfg, configAuthErrorAtKey)
		raw, err := json.Marshal(cfg)
		if err == nil {
			if err := s.integrationsWriter.UpdateConfig(ctx, orgID, integration.ID, raw); err != nil {
				s.logger.Warn().Err(err).
					Str("org_id", orgID.String()).
					Str("integration_id", integration.ID.String()).
					Msg("ClearIntegrationUnauthorized: clear config failed; banner may persist")
			}
		}
	}
	if statusErrored {
		if err := s.integrationsWriter.UpdateStatus(ctx, orgID, integration.ID, string(models.IntegrationStatusActive)); err != nil {
			s.logger.Warn().Err(err).
				Str("org_id", orgID.String()).
				Str("integration_id", integration.ID.String()).
				Msg("ClearIntegrationUnauthorized: status flip back to active failed")
			return
		}
		s.logger.Info().
			Str("org_id", orgID.String()).
			Str("integration_id", integration.ID.String()).
			Msg("Linear integration recovered — flipped back to active after successful API call")
	}
}

func decodeIntegrationConfig(raw json.RawMessage) map[string]any {
	cfg := map[string]any{}
	if len(raw) == 0 {
		return cfg
	}
	_ = json.Unmarshal(raw, &cfg)
	return cfg
}

func configHasAuthError(cfg map[string]any) bool {
	_, ok := cfg[configAuthErrorKey]
	return ok
}

// hasRecentAuthError treats a stamp younger than recentAuthErrorWindow as
// "fresh enough that we don't need to overwrite it." Older stamps get
// re-stamped so the timestamp surfaced in the UI tracks the most recent
// failure rather than the first one in a long error streak.
func hasRecentAuthError(cfg map[string]any) bool {
	v, ok := cfg[configAuthErrorAtKey]
	if !ok {
		return false
	}
	str, ok := v.(string)
	if !ok || str == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, str)
	if err != nil {
		return false
	}
	return time.Since(t) < recentAuthErrorWindow
}

const recentAuthErrorWindow = 5 * time.Minute
