package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agentcapabilities"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type AgentCapabilitiesHandler struct {
	store *db.AgentCapabilityPolicyStore
	svc   *agentcapabilities.Service
}

type InternalAgentCapabilitiesHandler struct {
	svc           *agentcapabilities.Service
	sessionStore  *db.SessionStore
	signingSecret string
}

type internalCapabilityResponse struct {
	Snapshot     []models.AgentCapabilitySnapshotItem `json:"snapshot"`
	Catalog      []models.AgentCapabilityDefinition   `json:"catalog"`
	SessionID    uuid.UUID                            `json:"session_id"`
	RepositoryID uuid.UUID                            `json:"repository_id"`
}

func NewInternalAgentCapabilitiesHandler(svc *agentcapabilities.Service, sessionStore *db.SessionStore, signingSecret string) *InternalAgentCapabilitiesHandler {
	return &InternalAgentCapabilitiesHandler{svc: svc, sessionStore: sessionStore, signingSecret: signingSecret}
}

func (h *InternalAgentCapabilitiesHandler) Effective(w http.ResponseWriter, r *http.Request) {
	claims, session, ok := h.authorize(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[internalCapabilityResponse]{Data: internalCapabilityResponse{
		Snapshot:     session.CapabilitySnapshot,
		Catalog:      h.svc.Definitions(),
		SessionID:    *claims.SessionID,
		RepositoryID: claims.RepoID,
	}})
}

func (h *InternalAgentCapabilitiesHandler) Request(w http.ResponseWriter, r *http.Request) {
	claims, session, ok := h.authorize(w, r)
	if !ok {
		return
	}
	var body struct {
		CapabilityID models.AgentCapabilityID          `json:"capability_id"`
		AccessLevel  models.AgentCapabilityAccessLevel `json:"access_level"`
		Reason       string                            `json:"reason"`
		ThreadID     *uuid.UUID                        `json:"thread_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}
	threadID := claims.ThreadID
	if body.ThreadID != nil {
		threadID = body.ThreadID
	}
	req, err := h.svc.RequestGrant(r.Context(), agentcapabilities.GrantRequestInput{
		OrgID:       claims.OrgID,
		SessionID:   *claims.SessionID,
		ThreadID:    threadID,
		TurnNumber:  session.CurrentTurn,
		AgentType:   session.AgentType,
		Capability:  body.CapabilityID,
		AccessLevel: body.AccessLevel,
		Reason:      body.Reason,
	})
	if err != nil {
		if errors.Is(err, agentcapabilities.ErrInvalidGrant) {
			writeError(w, r, http.StatusBadRequest, "INVALID_CAPABILITY", err.Error(), err)
		} else {
			writeError(w, r, http.StatusInternalServerError, "CAPABILITY_REQUEST_FAILED", "failed to create capability request", err)
		}
		return
	}
	if err := h.sessionStore.UpdateStatus(r.Context(), claims.OrgID, *claims.SessionID, models.SessionStatusAwaitingInput); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_SESSION_FAILED", "failed to pause session for capability approval", err)
		return
	}
	writeJSON(w, http.StatusAccepted, models.SingleResponse[models.HumanInputRequest]{Data: req})
}

func (h *InternalAgentCapabilitiesHandler) authorize(w http.ResponseWriter, r *http.Request) (*auth.InternalTokenClaims, models.Session, bool) {
	return authorizeInternalSession(w, r, h.signingSecret, h.sessionStore)
}

type capabilityPolicyRequest struct {
	Capabilities []models.AgentCapabilityPolicyGrantInput `json:"capabilities"`
}

type capabilityPolicyResponse struct {
	Policy       *models.AgentCapabilityPolicy `json:"policy,omitempty"`
	Capabilities []models.AgentCapabilityGrant `json:"capabilities"`
}

func NewAgentCapabilitiesHandler(store *db.AgentCapabilityPolicyStore, svc *agentcapabilities.Service) *AgentCapabilitiesHandler {
	return &AgentCapabilitiesHandler{store: store, svc: svc}
}

func (h *AgentCapabilitiesHandler) Catalog(w http.ResponseWriter, r *http.Request) {
	defs := h.svc.Definitions()
	writeJSON(w, http.StatusOK, models.ListResponse[models.AgentCapabilityDefinition]{Data: defs})
}

func (h *AgentCapabilitiesHandler) GetSessionDefault(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	policy, err := h.store.GetSessionDefaultPolicy(r.Context(), orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, models.SingleResponse[capabilityPolicyResponse]{Data: capabilityPolicyResponse{Capabilities: nil}})
			return
		}
		writeError(w, r, http.StatusInternalServerError, "GET_CAPABILITIES_FAILED", "failed to load default capabilities", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[capabilityPolicyResponse]{Data: capabilityPolicyResponse{Policy: &policy, Capabilities: policy.Grants}})
}

func (h *AgentCapabilitiesHandler) PatchSessionDefault(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	var userID *uuid.UUID
	if user := middleware.UserFromContext(r.Context()); user != nil {
		userID = &user.ID
	}
	var body capabilityPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "invalid request body", err)
		return
	}
	if err := h.validateGrants(body.Capabilities); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_CAPABILITY", err.Error(), err)
		return
	}
	if _, err := h.store.UpdateSessionDefaultPolicy(r.Context(), orgID, userID, body.Capabilities); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_CAPABILITIES_FAILED", "failed to update default capabilities", err)
		return
	}
	h.GetSessionDefault(w, r)
}

func (h *AgentCapabilitiesHandler) GetAutomationPolicy(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	automationID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid automation id", err)
		return
	}
	policy, err := h.store.GetAutomationPolicy(r.Context(), orgID, automationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, models.SingleResponse[capabilityPolicyResponse]{Data: capabilityPolicyResponse{Capabilities: nil}})
			return
		}
		writeError(w, r, http.StatusInternalServerError, "GET_CAPABILITIES_FAILED", "failed to load automation capabilities", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[capabilityPolicyResponse]{Data: capabilityPolicyResponse{Policy: &policy, Capabilities: policy.Grants}})
}

func (h *AgentCapabilitiesHandler) PatchAutomationPolicy(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	var userID *uuid.UUID
	if user := middleware.UserFromContext(r.Context()); user != nil {
		userID = &user.ID
	}
	automationID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid automation id", err)
		return
	}
	var body capabilityPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "invalid request body", err)
		return
	}
	if err := h.validateGrants(body.Capabilities); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_CAPABILITY", err.Error(), err)
		return
	}
	if _, err := h.store.ReplaceAutomationPolicy(r.Context(), orgID, automationID, userID, body.Capabilities); err != nil {
		if errors.Is(err, db.ErrAutomationNotFound) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "automation not found", err)
		} else {
			writeError(w, r, http.StatusInternalServerError, "UPDATE_CAPABILITIES_FAILED", "failed to update automation capabilities", err)
		}
		return
	}
	h.GetAutomationPolicy(w, r)
}

func (h *AgentCapabilitiesHandler) validateGrants(grants []models.AgentCapabilityPolicyGrantInput) error {
	seen := make(map[models.AgentCapabilityID]bool, len(grants))
	for _, grant := range grants {
		if seen[grant.CapabilityID] {
			return errors.New("duplicate capability grant")
		}
		seen[grant.CapabilityID] = true
		if err := h.svc.ValidateGrant(grant); err != nil {
			return err
		}
	}
	return nil
}
