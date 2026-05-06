// Package handlers — coding_credentials.
//
// API surface for the unified coding-credentials store. Replaces the split
// /settings/credentials/personal + /settings/coding-auths surfaces with one
// scope-aware endpoint. See docs/design/future/65-unified-coding-credentials.md.
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// codingCredentialStore is the narrow surface this handler depends on. Defined
// at the handler layer so tests can wire a fake; production passes the real
// *db.CodingCredentialStore.
type codingCredentialStore interface {
	Get(ctx context.Context, scope models.Scope, id uuid.UUID) (*models.DecryptedCodingCredential, error)
	ListByScope(ctx context.Context, scope models.Scope) ([]models.DecryptedCodingCredential, error)
	ListByProvider(ctx context.Context, scope models.Scope, provider models.ProviderName) ([]models.DecryptedCodingCredential, error)
	ListResolvable(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName) ([]models.DecryptedCodingCredential, error)
	ListResolvableMulti(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, providers []models.ProviderName) (map[models.ProviderName][]models.DecryptedCodingCredential, error)
	Create(ctx context.Context, scope models.Scope, label string, cfg models.ProviderConfig, opts db.CreateOpts) (*uuid.UUID, error)
	Rename(ctx context.Context, scope models.Scope, id uuid.UUID, label string) error
	UpdateStatus(ctx context.Context, scope models.Scope, id uuid.UUID, status string) error
	Disable(ctx context.Context, scope models.Scope, id uuid.UUID) error
	Move(ctx context.Context, scope models.Scope, id uuid.UUID, pos models.MoveCodingCredentialInput) error
	Reorder(ctx context.Context, scope models.Scope, orderedIDs []uuid.UUID) error
}

// CodingCredentialHandler exposes the unified API.
type CodingCredentialHandler struct {
	store       codingCredentialStore
	orgStore    codingAuthOrgStore
	invalidator OrgSettingsInvalidator
}

// NewCodingCredentialHandler constructs the handler.
func NewCodingCredentialHandler(store codingCredentialStore, orgStore codingAuthOrgStore) *CodingCredentialHandler {
	return &CodingCredentialHandler{store: store, orgStore: orgStore}
}

// SetOrgSettingsInvalidator injects the org-settings cache invalidator. When
// org-scoped agent_defaults are updated, Create calls InvalidateOrg after the
// write succeeds so subsequent agent starts see the new defaults immediately.
func (h *CodingCredentialHandler) SetOrgSettingsInvalidator(invalidator OrgSettingsInvalidator) {
	h.invalidator = invalidator
}

// resolveScopeFromQuery decides which scope to operate on based on the `scope`
// query param + the requesting user. "org" requires admin (when requireAdmin
// is true); "personal" always operates on the requester's own user_id;
// "resolved" returns the unified resolver order for the caller.
//
// The orgID is passed in by the caller (via middleware.OrgIDFromContext at
// the handler entry point) so the multi-tenancy lint sees the call inline.
func (h *CodingCredentialHandler) resolveScopeFromQuery(r *http.Request, orgID uuid.UUID, requireAdmin bool) (models.Scope, string, error) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		return models.Scope{}, "", fmt.Errorf("unauthenticated")
	}
	scopeParam := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("scope")))
	if scopeParam == "" {
		scopeParam = models.CodingCredentialScopePersonal
	}
	switch scopeParam {
	case models.CodingCredentialScopeOrg:
		if requireAdmin && middleware.ActiveRoleFromContext(r.Context()) != "admin" {
			return models.Scope{}, "", fmt.Errorf("admin role required for scope=org mutations")
		}
		return models.Scope{OrgID: orgID}, scopeParam, nil
	case "resolved":
		// "resolved" returns the caller's effective ordered list (personal
		// then org). UserID must be set so ListResolvable walks the personal
		// half — without it, the response degrades to org-only and the
		// /settings/account "Effective resolution" line never shows personal
		// rows.
		uid := user.ID
		return models.Scope{OrgID: orgID, UserID: &uid}, scopeParam, nil
	case models.CodingCredentialScopePersonal:
		uid := user.ID
		return models.Scope{OrgID: orgID, UserID: &uid}, scopeParam, nil
	}
	return models.Scope{}, "", fmt.Errorf("invalid scope: %q", scopeParam)
}

// resolveScope materialises a Scope from a scope-name string the handler has
// already parsed (from a request body OR the query string). Personal scope is
// always coerced to the caller's own user_id — never trust the client to
// assert which user owns a personal row.
func (h *CodingCredentialHandler) resolveScope(r *http.Request, orgID uuid.UUID, scopeParam string, requireAdmin bool) (models.Scope, error) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		return models.Scope{}, fmt.Errorf("unauthenticated")
	}
	switch scopeParam {
	case models.CodingCredentialScopeOrg:
		if requireAdmin && middleware.ActiveRoleFromContext(r.Context()) != "admin" {
			return models.Scope{}, fmt.Errorf("admin role required")
		}
		return models.Scope{OrgID: orgID}, nil
	case models.CodingCredentialScopePersonal:
		uid := user.ID
		return models.Scope{OrgID: orgID, UserID: &uid}, nil
	default:
		return models.Scope{}, fmt.Errorf("invalid scope: %q", scopeParam)
	}
}

// assertScopeMatchesContext is a defense-in-depth check on every mutation
// path. resolveScope already coerces personal scope to the caller's user_id
// from context, so this is a no-op on the happy path. It exists so that a
// future refactor that ever lets a Scope flow into a mutation without
// re-deriving from context (for example, a body field that smuggles a
// user_id) cannot land silently — the assertion will surface the mismatch
// as a 403 instead of a successful write against another user's stack.
//
// Org scope is unconditionally allowed: org rows are not user-owned and the
// admin gate already ran in resolveScope.
func (h *CodingCredentialHandler) assertScopeMatchesContext(r *http.Request, scope models.Scope) error {
	if scope.UserID == nil {
		return nil
	}
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		return fmt.Errorf("unauthenticated")
	}
	if *scope.UserID != user.ID {
		return fmt.Errorf("personal scope must match the authenticated user")
	}
	return nil
}

// List handles GET /api/v1/coding-credentials?scope=...
//
// scope=org → list every org row (admin or member can read; only admin mutates)
// scope=personal → list the caller's own personal rows
// scope=resolved → ordered list (personal then org) the runtime would resolve
func (h *CodingCredentialHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	scope, scopeParam, err := h.resolveScopeFromQuery(r, orgID, false)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SCOPE", err.Error())
		return
	}

	if scopeParam == "resolved" {
		out, err := h.listResolved(r.Context(), scope)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list resolved credentials", err)
			return
		}
		writeJSON(w, http.StatusOK, models.ListResponse[models.CodingCredentialSummary]{Data: out})
		return
	}

	creds, err := h.store.ListByScope(r.Context(), scope)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list credentials", err)
		return
	}
	out := summariesForScope(creds, scope)
	writeJSON(w, http.StatusOK, models.ListResponse[models.CodingCredentialSummary]{Data: out})
}

// listResolved walks the personal-then-org resolved list across every coding
// provider and returns the merged set tagged with scope. Used by the personal
// settings page to render the "effective resolution" block under the personal
// stack and the read-only org fallback section. Backed by ListResolvableMulti
// so a cold cache costs two round trips total (one per scope half) regardless
// of how many providers are checked.
func (h *CodingCredentialHandler) listResolved(ctx context.Context, scope models.Scope) ([]models.CodingCredentialSummary, error) {
	resolved, err := h.store.ListResolvableMulti(ctx, scope.OrgID, scope.UserID, models.CodingAgentProviders)
	if err != nil {
		return nil, err
	}
	seen := map[uuid.UUID]struct{}{}
	rows := make([]models.DecryptedCodingCredential, 0)
	for _, provider := range models.CodingAgentProviders {
		rows = append(rows, resolved[provider]...)
	}
	sortResolvedCodingRows(rows)

	out := make([]models.CodingCredentialSummary, 0, len(rows))
	for _, cred := range rows {
		if _, dup := seen[cred.ID]; dup {
			continue
		}
		seen[cred.ID] = struct{}{}
		out = append(out, summaryFromDecryptedCoding(cred))
	}
	// Mark the first runnable in each (scope, agent) tier as default. The
	// resolver picks tier-by-tier; the API hint mirrors that.
	markDefaults(out)
	return out, nil
}

func sortResolvedCodingRows(rows []models.DecryptedCodingCredential) {
	sort.SliceStable(rows, func(i, j int) bool {
		leftPersonal := rows[i].UserID != nil
		rightPersonal := rows[j].UserID != nil
		if leftPersonal != rightPersonal {
			return leftPersonal
		}
		if rows[i].Priority != rows[j].Priority {
			return rows[i].Priority < rows[j].Priority
		}
		if !rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].CreatedAt.Before(rows[j].CreatedAt)
		}
		return false
	})
}

// Create handles POST /api/v1/coding-credentials.
//
// API-key flows go through this endpoint. Subscription flows live behind the
// dedicated provider-specific OAuth endpoints (codex-auth, claude-code-auth)
// since they need device-code / PKCE state machines.
func (h *CodingCredentialHandler) Create(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	var input models.CreateCodingCredentialInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	if err := input.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_INPUT", err.Error())
		return
	}
	scope, err := h.resolveScope(r, orgID, input.Scope, true)
	if err != nil {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	if err := h.assertScopeMatchesContext(r, scope); err != nil {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}

	cfg, _, err := codingCredentialConfigFromInput(input)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_INPUT", err.Error())
		return
	}

	user := middleware.UserFromContext(r.Context())
	var createdBy *uuid.UUID
	if user != nil {
		uid := user.ID
		createdBy = &uid
	}
	label := input.Label
	if label == "" {
		label = defaultLabelFor(input.Agent, input.AuthType)
	}

	id, err := h.store.Create(r.Context(), scope, label, cfg, db.CreateOpts{CreatedBy: createdBy})
	if err != nil {
		var taken *db.ErrCodingCredentialLabelTaken
		if errors.As(err, &taken) {
			writeError(w, r, http.StatusConflict, "LABEL_TAKEN", err.Error())
			return
		}
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create credential", err)
		return
	}

	// Apply optional agent_config overrides at the org level (Amp/Pi default
	// model). Skipped for personal scope — there is no per-user agent_config.
	if scope.IsOrg() && len(input.AgentDefaults) > 0 && h.orgStore != nil {
		if err := h.orgStore.MergeCodingAgentDefaults(r.Context(), scope.OrgID, input.Agent, input.AgentDefaults); err != nil {
			if rollbackErr := h.store.Disable(r.Context(), scope, *id); rollbackErr != nil {
				err = fmt.Errorf("merge agent defaults: %w; rollback credential disable: %v", err, rollbackErr)
			}
			writeError(w, r, http.StatusInternalServerError, "AGENT_DEFAULTS_FAILED", "failed to merge agent defaults", err)
			return
		}
		if h.invalidator != nil {
			h.invalidator.InvalidateOrg(scope.OrgID)
		}
	}

	cred, err := h.store.Get(r.Context(), scope, *id)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "READ_BACK_FAILED", "credential created but read-back failed", err)
		return
	}
	writeJSON(w, http.StatusCreated, summaryFromDecryptedCoding(*cred))
}

// Update handles PATCH /api/v1/coding-credentials/{id}.
func (h *CodingCredentialHandler) Update(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "id must be a valid UUID")
		return
	}
	var input models.UpdateCodingCredentialInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	if err := input.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_INPUT", err.Error())
		return
	}
	scope, err := h.resolveScope(r, orgID, input.Scope, true)
	if err != nil {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	if err := h.assertScopeMatchesContext(r, scope); err != nil {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}

	if input.Status != nil && !isAllowedHandlerStatus(*input.Status) {
		writeError(w, r, http.StatusBadRequest, "INVALID_STATUS",
			"status must be one of: disabled, invalid (active and pending_auth are set by verification flows)")
		return
	}

	if input.Label != nil {
		if err := h.store.Rename(r.Context(), scope, id, *input.Label); err != nil {
			h.handleStoreError(w, r, err, "RENAME_FAILED")
			return
		}
	}
	if input.Status != nil {
		// The handler whitelist deliberately excludes active and pending_auth.
		// Activation belongs to provider-specific verification/completion
		// flows, never a generic PATCH, otherwise a user could mark an invalid
		// or PKCE-only credential runnable without proving the secret works.
		if err := h.store.UpdateStatus(r.Context(), scope, id, *input.Status); err != nil {
			h.handleStoreError(w, r, err, "STATUS_FAILED")
			return
		}
	}

	cred, err := h.store.Get(r.Context(), scope, id)
	if err != nil {
		h.handleStoreError(w, r, err, "READ_BACK_FAILED")
		return
	}
	writeJSON(w, http.StatusOK, summaryFromDecryptedCoding(*cred))
}

// Delete handles DELETE /api/v1/coding-credentials/{id}. Soft-delete (status='disabled').
func (h *CodingCredentialHandler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "id must be a valid UUID")
		return
	}
	scopeParam := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("scope")))
	if scopeParam == "" {
		scopeParam = models.CodingCredentialScopePersonal
	}
	scope, err := h.resolveScope(r, orgID, scopeParam, true)
	if err != nil {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	if err := h.assertScopeMatchesContext(r, scope); err != nil {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	if err := h.store.Disable(r.Context(), scope, id); err != nil {
		h.handleStoreError(w, r, err, "DELETE_FAILED")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Move handles PATCH /api/v1/coding-credentials/{id}/move (drag-drop hot path).
func (h *CodingCredentialHandler) Move(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "id must be a valid UUID")
		return
	}
	var input models.MoveCodingCredentialInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	if err := input.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_INPUT", err.Error())
		return
	}
	scope, err := h.resolveScope(r, orgID, input.Scope, true)
	if err != nil {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	if err := h.assertScopeMatchesContext(r, scope); err != nil {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	if err := h.store.Move(r.Context(), scope, id, input); err != nil {
		h.handleStoreError(w, r, err, "MOVE_FAILED")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Reorder handles PATCH /api/v1/coding-credentials/reorder (bulk reset).
func (h *CodingCredentialHandler) Reorder(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	var input models.ReorderCodingCredentialsInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	if err := input.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_INPUT", err.Error())
		return
	}
	scope, err := h.resolveScope(r, orgID, input.Scope, true)
	if err != nil {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	if err := h.assertScopeMatchesContext(r, scope); err != nil {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	if err := h.store.Reorder(r.Context(), scope, input.OrderedIDs); err != nil {
		h.handleStoreError(w, r, err, "REORDER_FAILED")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *CodingCredentialHandler) handleStoreError(w http.ResponseWriter, r *http.Request, err error, code string) {
	if errors.Is(err, db.ErrCodingCredentialNotFound) {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "credential not found")
		return
	}
	var taken *db.ErrCodingCredentialLabelTaken
	if errors.As(err, &taken) {
		writeError(w, r, http.StatusConflict, "LABEL_TAKEN", err.Error())
		return
	}
	writeError(w, r, http.StatusInternalServerError, code, "store error", err)
}

// summariesForScope builds CodingCredentialSummary objects from a scope-bounded
// list, marking the first runnable per (agent, auth_type) tier as default.
func summariesForScope(creds []models.DecryptedCodingCredential, scope models.Scope) []models.CodingCredentialSummary {
	out := make([]models.CodingCredentialSummary, 0, len(creds))
	for _, cred := range creds {
		if cred.UserID == nil && scope.UserID != nil {
			continue // safety belt
		}
		if cred.UserID != nil && scope.UserID == nil {
			continue
		}
		out = append(out, summaryFromDecryptedCoding(cred))
	}
	markDefaults(out)
	return out
}

// markDefaults flips IsDefault=true on the first runnable row in each
// (scope, agent) tier. Mirrors the resolver's pick semantics for the UI.
//
// On a scope=resolved response that mixes personal and org rows, both the
// top personal row and the top org row for the same agent get the badge —
// that's intentional: the personal row is "the one that runs first," the
// org row is "the one that would run if the personal rows are exhausted."
// The /settings/account page shows them in two visually separate cards, so
// the dual badge reads naturally as "default within each section." Single-
// scope responses get exactly one default per agent.
func markDefaults(rows []models.CodingCredentialSummary) {
	defaulted := map[string]bool{}
	for i := range rows {
		row := &rows[i]
		key := row.Scope + "|" + string(row.Agent)
		if defaulted[key] {
			continue
		}
		if !isRunnableCodingStatus(row.Status) {
			continue
		}
		row.IsDefault = true
		defaulted[key] = true
	}
}

func isRunnableCodingStatus(s models.CodingAuthStatus) bool {
	return s == models.CodingAuthStatusHealthy
}

// isAllowedHandlerStatus enumerates the status values the API surface accepts
// from a client. active and pending_auth are intentionally excluded — those
// states are set by provider-specific verification/OAuth flows. Generic PATCH
// can only remove a row from the runnable set or mark it invalid.
func isAllowedHandlerStatus(s string) bool {
	switch s {
	case models.CodingCredentialStatusDisabled,
		models.CodingCredentialStatusInvalid:
		return true
	}
	return false
}

func summaryFromDecryptedCoding(cred models.DecryptedCodingCredential) models.CodingCredentialSummary {
	agent := codingAgentForProvider(cred.Provider)
	authType := authTypeForProvider(cred.Provider, cred.Config)
	scope := models.CodingCredentialScopeOrg
	if cred.UserID != nil {
		scope = models.CodingCredentialScopePersonal
	}
	return models.CodingCredentialSummary{
		ID:             cred.ID,
		OrgID:          cred.OrgID,
		UserID:         cred.UserID,
		Scope:          scope,
		Priority:       cred.Priority,
		Agent:          agent,
		AuthType:       authType,
		Provider:       cred.Provider,
		Label:          coalesce(cred.Label, defaultLabelFor(agent, authType)),
		Status:         codingStatusFor(cred),
		UsageNote:      usageNoteFor(cred),
		LastVerifiedAt: cred.LastVerifiedAt,
		CreatedBy:      cred.CreatedBy,
		CreatedAt:      cred.CreatedAt,
		UpdatedAt:      cred.UpdatedAt,
	}
}

func codingAgentForProvider(p models.ProviderName) models.AgentType {
	switch p {
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

func authTypeForProvider(p models.ProviderName, cfg models.ProviderConfig) models.CodingAuthType {
	switch p {
	case models.ProviderAnthropicSubscription, models.ProviderOpenAISubscription, models.ProviderOpenAIChatGPT:
		return models.CodingAuthTypeSubscription
	}
	// During the dual-write window a legacy mirrored row can land here with
	// provider=anthropic and a non-nil Subscription embedded (the post-step
	// migration is what later rewrites it to anthropic_subscription). Mirror
	// the type-switch logic in usageNoteFor so the auth_type response field
	// agrees with the usage note for those transitional rows.
	if anth, ok := cfg.(models.AnthropicConfig); ok && anth.Subscription != nil {
		return models.CodingAuthTypeSubscription
	}
	return models.CodingAuthTypeAPIKey
}

func codingStatusFor(cred models.DecryptedCodingCredential) models.CodingAuthStatus {
	switch cred.Status {
	case models.CodingCredentialStatusInvalid:
		return models.CodingAuthStatusInvalid
	case models.CodingCredentialStatusPendingAuth:
		return models.CodingAuthStatusNeedsReauth
	case models.CodingCredentialStatusActive:
		return models.CodingAuthStatusHealthy
	default:
		return models.CodingAuthStatusNeedsReauth
	}
}

func usageNoteFor(cred models.DecryptedCodingCredential) string {
	switch cfg := cred.Config.(type) {
	case models.AnthropicSubscriptionConfig:
		return coalesce(cfg.AccountType, "Claude subscription")
	case models.OpenAISubscriptionConfig:
		return coalesce(cfg.AccountType, "ChatGPT subscription")
	case models.OpenAIChatGPTConfig:
		return coalesce(cfg.AccountType, "ChatGPT subscription")
	case models.AnthropicConfig:
		if cfg.Subscription != nil {
			return coalesce(cfg.Subscription.AccountType, "Claude subscription")
		}
		return cfg.MaskedSummary().MaskedKey
	case models.OpenAIConfig:
		return cfg.MaskedSummary().MaskedKey
	case models.GeminiConfig, models.AmpConfig, models.PiConfig, models.OpenRouterConfig:
		return cred.Config.MaskedSummary().MaskedKey
	}
	return ""
}

func defaultLabelFor(agent models.AgentType, authType models.CodingAuthType) string {
	switch {
	case agent == models.AgentTypeCodex && authType == models.CodingAuthTypeSubscription:
		return "Codex subscription"
	case agent == models.AgentTypeCodex && authType == models.CodingAuthTypeAPIKey:
		return "Codex API key"
	case agent == models.AgentTypeClaudeCode && authType == models.CodingAuthTypeSubscription:
		return "Claude Code subscription"
	case agent == models.AgentTypeClaudeCode && authType == models.CodingAuthTypeAPIKey:
		return "Claude Code API key"
	case agent == models.AgentTypeGeminiCLI:
		return "Gemini CLI API key"
	case agent == models.AgentTypeAmp:
		return "Amp API key"
	case agent == models.AgentTypePi:
		return "Pi API key"
	}
	return "Coding auth"
}

// codingCredentialConfigFromInput maps a CreateCodingCredentialInput to a
// strongly-typed ProviderConfig + provider name for the unified store. Only
// API-key auth lands here; subscription credentials come in via dedicated
// /codex-auth and /claude-code-auth endpoints.
func codingCredentialConfigFromInput(input models.CreateCodingCredentialInput) (models.ProviderConfig, models.ProviderName, error) {
	switch input.Agent {
	case models.AgentTypeCodex:
		return models.OpenAIConfig{
			APIKey:  input.APIKey,
			BaseURL: input.BaseURL,
			APIType: coalesce(input.APIType, "responses"),
		}, models.ProviderOpenAI, nil
	case models.AgentTypeClaudeCode:
		return models.AnthropicConfig{
			APIKey:  input.APIKey,
			BaseURL: input.BaseURL,
		}, models.ProviderAnthropic, nil
	case models.AgentTypeGeminiCLI:
		return models.GeminiConfig{
			APIKey: input.APIKey,
			Model:  coalesce(input.APIType, models.GeminiCLIModelGemini25Pro),
		}, models.ProviderGemini, nil
	case models.AgentTypeAmp:
		return models.AmpConfig{APIKey: input.APIKey}, models.ProviderAmp, nil
	case models.AgentTypePi:
		return models.PiConfig{APIKey: input.APIKey}, models.ProviderPi, nil
	}
	return nil, "", fmt.Errorf("unsupported agent: %s", input.Agent)
}

func coalesce(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
