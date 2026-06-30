package models

import "strings"

// OpenCode logical-model registry — the single source of truth for which
// OpenCode models 143 offers and how each is reached. See
// docs/design/115-logical-models-and-route-resolution.md.
//
// A model's *identity* (which weights the user wants) is decoupled from its
// *transport* (how the sandbox reaches it). The user picks a logical model
// (e.g. "glm-5.2"); at session launch the resolver walks that model's routes
// in priority order and picks the first one backed by a runnable credential
// (internal/services/agent/env.go resolveOpenCodeProviderConfig).
//
// Two interchangeable transports may be merged under one logical model ONLY
// when they serve genuinely equivalent weights (same model, context window,
// and quantization). Routes are listed in default priority order: OpenRouter
// first (it pins an audited US-only inference-provider allowlist), then the
// OpenCode-native gateway.

// OpenCodeRoute is one physical way to reach a logical model: a backing
// provider (which credential serves it), the registry route ID, and — for
// OpenRouter routes — the audited US-only inference-provider allowlist pinned
// into the runtime config.
type OpenCodeRoute struct {
	// Backing is the credential's backing provider. OpenCode credentials are
	// all stored under ProviderOpenCode with a backing_provider discriminator;
	// resolution matches a route to a credential whose
	// NormalizedBackingProvider() equals this value.
	Backing ProviderName
	// PhysicalModelID is the route ID tracked by 143 (e.g.
	// "openrouter/z-ai/glm-5.2" or "opencode/glm-5.2"). OpenRouter routes are
	// converted to OpenCode's custom-model CLI form ("openrouter/~...") when
	// building the sandbox runtime config.
	PhysicalModelID string
	// USProviderList is the audited US-only OpenRouter inference-provider
	// allowlist (only/order) pinned in the runtime config. Empty for non-OpenRouter
	// routes, which do not expose per-provider location controls.
	USProviderList []string
}

// IsNativeOpenCode reports whether this route uses the OpenCode-native gateway
// (direct vendor route, no audited US-provider allowlist). Native routes are
// gated behind org policy when auto-routing a logical selection.
func (r OpenCodeRoute) IsNativeOpenCode() bool {
	return r.Backing == ProviderOpenCode
}

// TransportLabel returns the human transport name for this route, e.g.
// "OpenRouter" or "OpenCode native".
func (r OpenCodeRoute) TransportLabel() string {
	return OpenCodeTransportLabel(r.Backing)
}

// OpenCodeTransportLabel returns the human transport name for an OpenCode
// backing provider. Shared by the resolver error messages, the routing log,
// and the models API so the label is defined once.
func OpenCodeTransportLabel(backing ProviderName) string {
	switch backing {
	case ProviderAnthropic:
		return "Anthropic"
	case ProviderOpenAI:
		return "OpenAI"
	case ProviderGemini:
		return "Gemini"
	case ProviderOpenRouter:
		return "OpenRouter"
	case ProviderOpenCode:
		return "OpenCode native"
	default:
		return string(backing)
	}
}

// OpenCodeModel is a single user-facing model with one or more interchangeable
// routes in default priority order.
type OpenCodeModel struct {
	// ID is the logical identifier stored in config (e.g. "glm-5.2"). For
	// first-party single-route models it is the physical ID itself
	// (e.g. "anthropic/claude-haiku-4-5"), which is already unambiguous.
	ID          string
	DisplayName string
	Routes      []OpenCodeRoute
}

// DefaultOpenCodeModel is the logical model used when no OpenCode model is
// configured. GLM 5.2 is the cost-first default and leads the picker.
const DefaultOpenCodeModel = "glm-5.2"

var defaultOpenCodePhysicalModelByBacking = map[ProviderName]string{
	ProviderOpenRouter: OpenCodeModelOpenRouterGLM52,
	ProviderOpenCode:   OpenCodeModelGLM52,
	ProviderOpenAI:     OpenCodeModelGPT54Mini,
	ProviderAnthropic:  OpenCodeModelClaudeHaiku45,
	ProviderGemini:     OpenCodeModelGemini3Flash,
}

// Audited US-only OpenRouter inference-provider allowlists, audited against
// OpenRouter's endpoint list and provider-company locations on 2026-06-26.
// Keep docs/design/implemented/95-opencode-agent-adapter.md in sync. These
// live on the OpenRouter route of each logical model below.
var (
	usGLM52        = []string{"deepinfra", "together", "fireworks"}
	usGLM51        = []string{"deepinfra", "baseten", "together"}
	usKimiK25      = []string{"digitalocean", "deepinfra"}
	usKimiK26      = []string{"deepinfra", "baseten", "fireworks"}
	usMiniMaxM27   = []string{"deepinfra", "fireworks", "together"}
	usMiniMaxM25   = []string{"deepinfra", "digitalocean", "parasail"}
	usDeepSeekV4Fl = []string{"deepinfra", "cloudflare", "fireworks"}
	usDeepSeekV4Pr = []string{"deepinfra", "together", "fireworks"}
	usGemini35Fl   = []string{"google-ai-studio", "google-vertex/global"}
	usGemini31Pro  = []string{"google-ai-studio", "google-vertex/global"}
	usGPT52        = []string{"openai", "azure"}
	usGPT55        = []string{"openai", "azure"}
	usGPT55Pro     = []string{"openai"} // OpenRouter currently exposes only OpenAI.
)

// openRouterRoute builds an OpenRouter-backed route.
func openRouterRoute(physicalID string, usProviders []string) OpenCodeRoute {
	return OpenCodeRoute{Backing: ProviderOpenRouter, PhysicalModelID: physicalID, USProviderList: usProviders}
}

// nativeRoute builds an OpenCode-native (Zen/Go gateway) route.
func nativeRoute(physicalID string) OpenCodeRoute {
	return OpenCodeRoute{Backing: ProviderOpenCode, PhysicalModelID: physicalID}
}

// firstPartyRoute builds a first-party-backed route (Anthropic/OpenAI/Gemini
// direct). These have a single route and no US allowlist.
func firstPartyRoute(backing ProviderName, physicalID string) OpenCodeRoute {
	return OpenCodeRoute{Backing: backing, PhysicalModelID: physicalID}
}

// OpenCodeModelRegistry is the ordered set of logical models the picker offers.
// GLM 5.2 stays first as the default; after that, model families are grouped
// with newer versions before older versions. OpenRouter/native pairs collapse
// into one entry each.
var OpenCodeModelRegistry = []OpenCodeModel{
	{ID: "glm-5.2", DisplayName: "GLM 5.2", Routes: []OpenCodeRoute{
		openRouterRoute(OpenCodeModelOpenRouterGLM52, usGLM52),
		nativeRoute(OpenCodeModelGLM52),
	}},
	{ID: "glm-5.1", DisplayName: "GLM 5.1", Routes: []OpenCodeRoute{
		openRouterRoute(OpenCodeModelOpenRouterGLM51, usGLM51),
		nativeRoute(OpenCodeModelGLM51),
	}},
	// GPT-5.5 is NOT collapsed under a bare logical id: that name belongs to the
	// Codex agent (CodexModelGPT55), so a bare "gpt-5.5" must stay invalid for
	// OpenCode. It is offered as explicit physical (pinned) routes instead,
	// which keeps the audited US allowlist in the registry.
	{ID: "gpt-5.5-pro", DisplayName: "GPT-5.5 Pro", Routes: []OpenCodeRoute{
		openRouterRoute(OpenCodeModelOpenRouterGPT55Pro, usGPT55Pro),
		nativeRoute(OpenCodeModelGPT55Pro),
	}},
	{ID: OpenCodeModelOpenRouterGPT55, DisplayName: "GPT-5.5", Routes: []OpenCodeRoute{
		openRouterRoute(OpenCodeModelOpenRouterGPT55, usGPT55),
	}},
	{ID: OpenCodeModelGPT55, DisplayName: "GPT-5.5", Routes: []OpenCodeRoute{
		nativeRoute(OpenCodeModelGPT55),
	}},
	{ID: OpenCodeModelGPT54, DisplayName: "GPT-5.4", Routes: []OpenCodeRoute{
		firstPartyRoute(ProviderOpenAI, OpenCodeModelGPT54),
	}},
	{ID: OpenCodeModelGPT54Mini, DisplayName: "GPT-5.4 Mini", Routes: []OpenCodeRoute{
		firstPartyRoute(ProviderOpenAI, OpenCodeModelGPT54Mini),
	}},
	{ID: OpenCodeModelGPT53CodexSpark, DisplayName: "GPT-5.3 Codex Spark", Routes: []OpenCodeRoute{
		firstPartyRoute(ProviderOpenAI, OpenCodeModelGPT53CodexSpark),
	}},
	{ID: "gpt-5.2", DisplayName: "GPT-5.2", Routes: []OpenCodeRoute{
		openRouterRoute(OpenCodeModelOpenRouterGPT52, usGPT52),
		nativeRoute(OpenCodeModelGPT52),
	}},
	{ID: OpenCodeModelClaudeOpus48, DisplayName: "Claude Opus 4.8", Routes: []OpenCodeRoute{
		firstPartyRoute(ProviderAnthropic, OpenCodeModelClaudeOpus48),
	}},
	{ID: OpenCodeModelClaudeOpus47, DisplayName: "Claude Opus 4.7", Routes: []OpenCodeRoute{
		firstPartyRoute(ProviderAnthropic, OpenCodeModelClaudeOpus47),
	}},
	{ID: OpenCodeModelClaudeOpus46, DisplayName: "Claude Opus 4.6", Routes: []OpenCodeRoute{
		firstPartyRoute(ProviderAnthropic, OpenCodeModelClaudeOpus46),
	}},
	{ID: OpenCodeModelClaudeSonnet46, DisplayName: "Claude Sonnet 4.6", Routes: []OpenCodeRoute{
		firstPartyRoute(ProviderAnthropic, OpenCodeModelClaudeSonnet46),
	}},
	{ID: OpenCodeModelClaudeHaiku45, DisplayName: "Claude Haiku 4.5", Routes: []OpenCodeRoute{
		firstPartyRoute(ProviderAnthropic, OpenCodeModelClaudeHaiku45),
	}},
	{ID: "gemini-3.5-flash", DisplayName: "Gemini 3.5 Flash", Routes: []OpenCodeRoute{
		openRouterRoute(OpenCodeModelOpenRouterGemini35Flash, usGemini35Fl),
		nativeRoute(OpenCodeModelGemini35Flash),
	}},
	{ID: "gemini-3.1-pro", DisplayName: "Gemini 3.1 Pro", Routes: []OpenCodeRoute{
		openRouterRoute(OpenCodeModelOpenRouterGemini31Pro, usGemini31Pro),
		nativeRoute(OpenCodeModelGemini31Pro),
	}},
	{ID: OpenCodeModelGemini3Flash, DisplayName: "Gemini 3 Flash", Routes: []OpenCodeRoute{
		firstPartyRoute(ProviderGemini, OpenCodeModelGemini3Flash),
	}},
	{ID: "minimax-m2.7", DisplayName: "MiniMax M2.7", Routes: []OpenCodeRoute{
		openRouterRoute(OpenCodeModelOpenRouterMiniMaxM27, usMiniMaxM27),
		nativeRoute(OpenCodeModelMiniMaxM27),
	}},
	{ID: "minimax-m2.5", DisplayName: "MiniMax M2.5", Routes: []OpenCodeRoute{
		openRouterRoute(OpenCodeModelOpenRouterMiniMaxM25, usMiniMaxM25),
		nativeRoute(OpenCodeModelMiniMaxM25),
	}},
	{ID: "deepseek-v4-flash", DisplayName: "DeepSeek V4 Flash", Routes: []OpenCodeRoute{
		openRouterRoute(OpenCodeModelOpenRouterDeepSeekV4Flash, usDeepSeekV4Fl),
		nativeRoute(OpenCodeModelDeepSeekV4Flash),
	}},
	{ID: "deepseek-v4-pro", DisplayName: "DeepSeek V4 Pro", Routes: []OpenCodeRoute{
		openRouterRoute(OpenCodeModelOpenRouterDeepSeekV4Pro, usDeepSeekV4Pr),
		nativeRoute(OpenCodeModelDeepSeekV4Pro),
	}},
	{ID: "kimi-k2.6", DisplayName: "Kimi K2.6", Routes: []OpenCodeRoute{
		openRouterRoute(OpenCodeModelOpenRouterKimiK26, usKimiK26),
		nativeRoute(OpenCodeModelKimiK26),
	}},
	{ID: "kimi-k2.5", DisplayName: "Kimi K2.5", Routes: []OpenCodeRoute{
		openRouterRoute(OpenCodeModelOpenRouterKimiK25, usKimiK25),
		nativeRoute(OpenCodeModelKimiK25),
	}},
}

// openCodeModelsByID and openCodeModelsByPhysicalID index the registry for O(1)
// lookups. Built once at init; the registry is read-only at runtime.
var (
	openCodeModelsByID         = map[string]OpenCodeModel{}
	openCodeRouteByPhysicalID  = map[string]OpenCodeRoute{}
	openCodeModelByPhysicalID  = map[string]OpenCodeModel{}
	openCodePhysicalModelIDSet = map[string]struct{}{}
)

func init() {
	for _, m := range OpenCodeModelRegistry {
		openCodeModelsByID[m.ID] = m
		for _, route := range m.Routes {
			openCodeRouteByPhysicalID[route.PhysicalModelID] = route
			openCodeModelByPhysicalID[route.PhysicalModelID] = m
			openCodePhysicalModelIDSet[route.PhysicalModelID] = struct{}{}
		}
	}
}

// LookupOpenCodeModel returns the logical model for an id, or false.
func LookupOpenCodeModel(id string) (OpenCodeModel, bool) {
	m, ok := openCodeModelsByID[strings.TrimSpace(id)]
	return m, ok
}

// IsOpenCodeLogicalModel reports whether id names a registry logical model.
func IsOpenCodeLogicalModel(id string) bool {
	_, ok := openCodeModelsByID[strings.TrimSpace(id)]
	return ok
}

// IsKnownOpenCodePhysicalModel reports whether id is a curated physical route
// ID (used to recognize pinned selections that predate logical models).
func IsKnownOpenCodePhysicalModel(id string) bool {
	_, ok := openCodePhysicalModelIDSet[normalizeOpenCodePhysicalModelID(id)]
	return ok
}

// OpenCodeRouteForPhysicalModel returns the registry route + owning logical
// model for a physical model ID. Used to resolve pinned selections and to look
// up the audited US-provider allowlist for a resolved route.
func OpenCodeRouteForPhysicalModel(physicalID string) (OpenCodeModel, OpenCodeRoute, bool) {
	id := normalizeOpenCodePhysicalModelID(physicalID)
	route, ok := openCodeRouteByPhysicalID[id]
	if !ok {
		return OpenCodeModel{}, OpenCodeRoute{}, false
	}
	return openCodeModelByPhysicalID[id], route, true
}

// OpenCodeUSProviderList returns the audited US-only OpenRouter provider
// allowlist for a physical model ID, or nil when the model has none (native or
// first-party routes, or uncurated custom slugs).
func OpenCodeUSProviderList(physicalID string) []string {
	route, ok := openCodeRouteByPhysicalID[normalizeOpenCodePhysicalModelID(physicalID)]
	if !ok {
		return nil
	}
	return route.USProviderList
}

// OpenCodePhysicalModelForBacking maps a selection (logical id or physical id)
// to a physical CLI model id for a given backing provider. A physical id is
// returned unchanged; a logical id resolves to its route matching the backing
// (or its first route); an uncurated slug is returned unchanged.
func OpenCodePhysicalModelForBacking(idOrPhysical string, backing ProviderName) string {
	id := strings.TrimSpace(idOrPhysical)
	if IsKnownOpenCodePhysicalModel(id) {
		return id
	}
	if m, ok := openCodeModelsByID[id]; ok {
		for _, route := range m.Routes {
			if route.Backing == backing {
				return route.PhysicalModelID
			}
		}
		if len(m.Routes) > 0 {
			return m.Routes[0].PhysicalModelID
		}
	}
	return id
}

// OpenCodeModelAPI is the wire shape served by GET /api/v1/settings/opencode-models.
// It is the frontend's source of truth for the picker list and per-model routes
// (which transports a model can run on), so the route data is not hand-synced.
type OpenCodeModelAPI struct {
	ID          string             `json:"id"`
	DisplayName string             `json:"display_name"`
	Routes      []OpenCodeRouteAPI `json:"routes"`
}

// OpenCodeRouteAPI is the wire shape for one route. The US provider allowlist is
// intentionally omitted — it is a backend-only concern.
type OpenCodeRouteAPI struct {
	Backing         string `json:"backing"`
	TransportLabel  string `json:"transport_label"`
	PhysicalModelID string `json:"physical_model_id"`
}

// OpenCodeModelsForAPI projects the registry into the read-API wire shape.
func OpenCodeModelsForAPI() []OpenCodeModelAPI {
	out := make([]OpenCodeModelAPI, 0, len(OpenCodeModelRegistry))
	for _, m := range OpenCodeModelRegistry {
		routes := make([]OpenCodeRouteAPI, 0, len(m.Routes))
		for _, route := range m.Routes {
			routes = append(routes, OpenCodeRouteAPI{
				Backing:         string(route.Backing),
				TransportLabel:  route.TransportLabel(),
				PhysicalModelID: route.PhysicalModelID,
			})
		}
		out = append(out, OpenCodeModelAPI{ID: m.ID, DisplayName: m.DisplayName, Routes: routes})
	}
	return out
}

// DefaultOpenCodePhysicalModelForBacking returns a physical CLI model id for a
// backing when no model is configured. These fallbacks are explicit so picker
// ordering changes do not silently alter the model used by existing orgs with
// backing-specific OpenCode credentials.
func DefaultOpenCodePhysicalModelForBacking(backing ProviderName) string {
	if model, ok := defaultOpenCodePhysicalModelByBacking[backing]; ok {
		return model
	}
	for _, m := range OpenCodeModelRegistry {
		for _, route := range m.Routes {
			if route.Backing == backing {
				return route.PhysicalModelID
			}
		}
	}
	if def, ok := openCodeModelsByID[DefaultOpenCodeModel]; ok && len(def.Routes) > 0 {
		return def.Routes[0].PhysicalModelID
	}
	return DefaultOpenCodeModel
}

// OpenCodeDisplayName returns a human label for a logical id or physical model
// ID. Falls back to the raw value for uncurated custom slugs.
func OpenCodeDisplayName(idOrPhysical string) string {
	id := strings.TrimSpace(idOrPhysical)
	if m, ok := openCodeModelsByID[id]; ok {
		return m.DisplayName
	}
	if m, ok := openCodeModelByPhysicalID[normalizeOpenCodePhysicalModelID(id)]; ok {
		return m.DisplayName
	}
	return id
}

func normalizeOpenCodePhysicalModelID(id string) string {
	id = strings.TrimSpace(id)
	const openRouterCustomPrefix = "openrouter/~"
	if upstreamModel, ok := strings.CutPrefix(id, openRouterCustomPrefix); ok {
		return "openrouter/" + upstreamModel
	}
	return id
}
