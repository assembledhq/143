package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type TXTLookupFunc func(ctx context.Context, host string) ([]string, error)

type VerifiedDomainHandler struct {
	store     *db.VerifiedDomainStore
	lookupTXT TXTLookupFunc
}

func NewVerifiedDomainHandler(store *db.VerifiedDomainStore, lookupTXT TXTLookupFunc) *VerifiedDomainHandler {
	if lookupTXT == nil {
		resolver := net.DefaultResolver
		lookupTXT = resolver.LookupTXT
	}
	return &VerifiedDomainHandler{store: store, lookupTXT: lookupTXT}
}

func (h *VerifiedDomainHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	domains, err := h.store.ListByOrg(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list verified domains", err)
		return
	}
	for i := range domains {
		h.decorate(&domains[i])
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.VerifiedDomain]{Data: domains})
}

func (h *VerifiedDomainHandler) Create(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}
	var body struct {
		Domain          string      `json:"domain"`
		AutoJoinEnabled *bool       `json:"auto_join_enabled"`
		AutoJoinRole    models.Role `json:"auto_join_role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	domain, err := db.NormalizeVerifiedDomain(body.Domain)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_DOMAIN", "invalid domain")
		return
	}
	role := body.AutoJoinRole
	if role == "" {
		role = models.RoleMember
	}
	if err := role.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ROLE", "invalid auto-join role")
		return
	}
	autoJoinEnabled := true
	if body.AutoJoinEnabled != nil {
		autoJoinEnabled = *body.AutoJoinEnabled
	}
	token, err := generateRandomString(24)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "TOKEN_FAILED", "failed to generate verification token", err)
		return
	}
	verifiedDomain := &models.VerifiedDomain{
		Domain:            domain,
		VerificationToken: token,
		AutoJoinEnabled:   autoJoinEnabled,
		AutoJoinRole:      role,
		CreatedBy:         user.ID,
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	if err := h.store.Create(r.Context(), orgID, verifiedDomain); err != nil {
		writeError(w, r, http.StatusConflict, "DOMAIN_CREATE_FAILED", "failed to create verified domain", err)
		return
	}
	h.decorate(verifiedDomain)
	writeJSON(w, http.StatusCreated, models.SingleResponse[models.VerifiedDomain]{Data: *verifiedDomain})
}

func (h *VerifiedDomainHandler) Verify(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid domain id")
		return
	}
	domain, err := h.store.GetByID(r.Context(), orgID, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "domain not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "LOOKUP_FAILED", "failed to load domain", err)
		return
	}
	txts, err := h.lookupTXT(r.Context(), verificationHost(domain.Domain))
	if err != nil {
		writeError(w, r, http.StatusConflict, "DOMAIN_NOT_VERIFIED", "verification TXT record was not found")
		return
	}
	if !containsTXT(txts, verificationRecord(domain.VerificationToken)) {
		writeError(w, r, http.StatusConflict, "DOMAIN_NOT_VERIFIED", "verification TXT record was not found")
		return
	}
	domain, err = h.store.MarkVerified(r.Context(), orgID, id)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "VERIFY_FAILED", "failed to mark domain verified", err)
		return
	}
	h.decorate(&domain)
	writeJSON(w, http.StatusOK, models.SingleResponse[models.VerifiedDomain]{Data: domain})
}

func (h *VerifiedDomainHandler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid domain id")
		return
	}
	if err := h.store.Delete(r.Context(), orgID, id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "domain not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "DELETE_FAILED", "failed to delete domain", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *VerifiedDomainHandler) decorate(domain *models.VerifiedDomain) {
	domain.VerificationHost = verificationHost(domain.Domain)
	domain.VerificationRecord = verificationRecord(domain.VerificationToken)
}

func verificationHost(domain string) string {
	return "_143-domain-verification." + domain
}

func verificationRecord(token string) string {
	return "143-domain-verification=" + token
}

func containsTXT(txts []string, want string) bool {
	for _, txt := range txts {
		if strings.TrimSpace(txt) == want {
			return true
		}
	}
	return false
}
