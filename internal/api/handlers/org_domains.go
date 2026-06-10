package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/domains"
)

// orgDomainVerifier is the DNS-check seam so handler tests don't do real
// DNS lookups.
type orgDomainVerifier interface {
	Verify(ctx context.Context, domain, token string) (bool, error)
}

// maxDomainsPerOrg bounds domain claims per organization.
const maxDomainsPerOrg = 10

// OrgDomainsHandler serves the verified-domain management endpoints
// (/api/v1/team/domains/*, admin-only) and the user-facing joinable-org
// discovery endpoints (/api/v1/orgs/joinable, /api/v1/orgs/{id}/join).
type OrgDomainsHandler struct {
	store       *db.OrganizationDomainStore
	memberships *db.OrganizationMembershipStore
	users       *db.UserStore
	verifier    orgDomainVerifier
	audit       *db.AuditEmitter
}

func NewOrgDomainsHandler(
	store *db.OrganizationDomainStore,
	memberships *db.OrganizationMembershipStore,
	users *db.UserStore,
	verifier orgDomainVerifier,
) *OrgDomainsHandler {
	return &OrgDomainsHandler{
		store:       store,
		memberships: memberships,
		users:       users,
		verifier:    verifier,
	}
}

// SetAuditEmitter injects the audit emitter for logging domain events.
func (h *OrgDomainsHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

// orgDomainResponse decorates the DB row with the exact DNS record the
// admin needs to publish, so the frontend never reimplements the format.
type orgDomainResponse struct {
	models.OrganizationDomain
	DNSRecordName  string `json:"dns_record_name"`
	DNSRecordValue string `json:"dns_record_value"`
}

func toOrgDomainResponse(d models.OrganizationDomain) orgDomainResponse {
	return orgDomainResponse{
		OrganizationDomain: d,
		DNSRecordName:      domains.TXTRecordName(d.Domain),
		DNSRecordValue:     domains.TXTRecordValue(d.VerificationToken),
	}
}

// List returns all domain claims for the active org.
// GET /api/v1/team/domains (admin)
func (h *OrgDomainsHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	rows, err := h.store.ListByOrg(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list domains", err)
		return
	}
	out := make([]orgDomainResponse, 0, len(rows))
	for _, d := range rows {
		out = append(out, toOrgDomainResponse(d))
	}
	writeJSON(w, http.StatusOK, models.ListResponse[orgDomainResponse]{Data: out})
}

// Create claims a domain for the active org and returns the TXT record the
// admin must publish before verifying.
// POST /api/v1/team/domains (admin)
func (h *OrgDomainsHandler) Create(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())

	var body struct {
		Domain string `json:"domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}

	domain := domains.NormalizeDomain(body.Domain)
	if err := domains.ValidateDomain(domain); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_DOMAIN", err.Error())
		return
	}

	// Cap claims per org (Clerk uses the same limit). Real orgs verify a
	// handful of domains; an unbounded list is only room for clutter and
	// scripted abuse of the verify endpoint's DNS lookups.
	if count, err := h.store.CountByOrg(r.Context(), orgID); err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to count domains", err)
		return
	} else if count >= maxDomainsPerOrg {
		writeError(w, r, http.StatusBadRequest, "DOMAIN_LIMIT_REACHED",
			fmt.Sprintf("An organization can have at most %d domains. Remove one to add another.", maxDomainsPerOrg))
		return
	}

	token, err := domains.GenerateToken()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate verification token", err)
		return
	}

	row := &models.OrganizationDomain{
		OrgID:             orgID,
		Domain:            domain,
		VerificationToken: token,
	}
	if user != nil {
		row.CreatedBy = &user.ID
	}
	if err := h.store.Create(r.Context(), row); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeError(w, r, http.StatusConflict, "DOMAIN_EXISTS", "This domain has already been added to this organization.")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to add domain", err)
		return
	}

	idStr := row.ID.String()
	details, _ := json.Marshal(map[string]string{"domain": domain})
	emitUserAudit(h.audit, r, models.AuditActionTeamDomainAdded, models.AuditResourceOrgDomain, &idStr, details)

	writeJSON(w, http.StatusCreated, models.SingleResponse[orgDomainResponse]{Data: toOrgDomainResponse(*row)})
}

// Verify performs the DNS TXT lookup for a pending domain and flips it to
// verified when the record is present.
// POST /api/v1/team/domains/{id}/verify (admin)
func (h *OrgDomainsHandler) Verify(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid domain id")
		return
	}

	row, err := h.store.GetByID(r.Context(), orgID, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "DOMAIN_NOT_FOUND", "domain not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load domain", err)
		return
	}

	if row.Status != models.OrgDomainStatusVerified {
		ok, verr := h.verifier.Verify(r.Context(), row.Domain, row.VerificationToken)
		if verr != nil {
			_ = h.store.TouchChecked(r.Context(), orgID, id)
			writeError(w, r, http.StatusBadGateway, "DNS_LOOKUP_FAILED",
				"DNS lookup failed — try again in a moment.", verr)
			return
		}
		if !ok {
			_ = h.store.TouchChecked(r.Context(), orgID, id)
			writeError(w, r, http.StatusUnprocessableEntity, "DOMAIN_NOT_VERIFIED",
				fmt.Sprintf("TXT record not found. Publish %q at %q and try again — DNS changes can take a few minutes to propagate.",
					domains.TXTRecordValue(row.VerificationToken), domains.TXTRecordName(row.Domain)))
			return
		}

		row, err = h.store.MarkVerified(r.Context(), orgID, id)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				writeError(w, r, http.StatusConflict, "DOMAIN_CLAIMED",
					"This domain has already been verified by another organization.")
				return
			}
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to mark domain verified", err)
			return
		}

		idStr := row.ID.String()
		details, _ := json.Marshal(map[string]string{"domain": row.Domain})
		emitUserAudit(h.audit, r, models.AuditActionTeamDomainVerified, models.AuditResourceOrgDomain, &idStr, details)
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[orgDomainResponse]{Data: toOrgDomainResponse(row)})
}

// Update toggles auto-join for a domain.
// PATCH /api/v1/team/domains/{id} (admin)
func (h *OrgDomainsHandler) Update(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid domain id")
		return
	}

	var body struct {
		AutoJoinEnabled *bool `json:"auto_join_enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AutoJoinEnabled == nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "auto_join_enabled is required")
		return
	}

	row, err := h.store.SetAutoJoin(r.Context(), orgID, id, *body.AutoJoinEnabled)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "DOMAIN_NOT_FOUND", "domain not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to update domain", err)
		return
	}

	idStr := row.ID.String()
	details, _ := json.Marshal(map[string]any{"domain": row.Domain, "auto_join_enabled": row.AutoJoinEnabled})
	emitUserAudit(h.audit, r, models.AuditActionTeamDomainUpdated, models.AuditResourceOrgDomain, &idStr, details)

	writeJSON(w, http.StatusOK, models.SingleResponse[orgDomainResponse]{Data: toOrgDomainResponse(row)})
}

// Delete removes a domain claim (and with it, auto-join for that domain).
// DELETE /api/v1/team/domains/{id} (admin)
func (h *OrgDomainsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid domain id")
		return
	}

	// Load first so the audit entry can record which domain was removed.
	row, err := h.store.GetByID(r.Context(), orgID, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "DOMAIN_NOT_FOUND", "domain not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load domain", err)
		return
	}

	if err := h.store.Delete(r.Context(), orgID, id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "DOMAIN_NOT_FOUND", "domain not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to delete domain", err)
		return
	}

	idStr := id.String()
	details, _ := json.Marshal(map[string]string{"domain": row.Domain})
	emitUserAudit(h.audit, r, models.AuditActionTeamDomainRemoved, models.AuditResourceOrgDomain, &idStr, details)

	w.WriteHeader(http.StatusNoContent)
}

// joinableOrgsResponse extends the joinable list with the hint that the
// user's domain IS captured but their email isn't verified yet — the
// frontend turns that into a "verify your email to join your team" prompt.
// Deliberately existence-only: the org's identity stays hidden until the
// user proves they own the address.
type joinableOrgsResponse struct {
	Data                      []models.JoinableOrganization `json:"data"`
	EmailVerificationRequired bool                          `json:"email_verification_required"`
}

// ListJoinable returns orgs the signed-in user can join because their
// provider-verified email domain matches a verified auto-join domain.
// Registered outside the OrgContext block (like /invitations/pending)
// because it spans orgs and must work for zero-membership users.
// GET /api/v1/orgs/joinable
func (h *OrgDomainsHandler) ListJoinable(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}

	joinable, needsVerification, err := h.joinableForUser(r.Context(), user)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list joinable organizations", err)
		return
	}
	writeJSON(w, http.StatusOK, joinableOrgsResponse{Data: joinable, EmailVerificationRequired: needsVerification})
}

// Join adds the signed-in user to an org whose verified auto-join domain
// matches their provider-verified email domain. The eligibility re-check
// here (not just at list time) is the security boundary: the org id is
// caller-supplied.
// POST /api/v1/orgs/{id}/join
func (h *OrgDomainsHandler) Join(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}
	orgID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid organization id")
		return
	}

	joinable, _, err := h.joinableForUser(r.Context(), user)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to check eligibility", err)
		return
	}
	var target *models.JoinableOrganization
	for i := range joinable {
		if joinable[i].OrgID == orgID {
			target = &joinable[i]
			break
		}
	}
	if target == nil {
		writeError(w, r, http.StatusForbidden, "NOT_ELIGIBLE",
			"Your email domain does not grant access to this organization.")
		return
	}

	// GrantAtLeast (not Insert): tolerates a concurrent join/invite-accept
	// racing this request, and never downgrades a role granted elsewhere.
	effectiveRole, err := h.memberships.GrantAtLeast(r.Context(), user.ID, orgID, models.RoleMember)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to join organization", err)
		return
	}

	// Built by hand rather than via emitUserAudit: this route runs outside
	// OrgContext (it spans orgs by design), so OrgIDFromContext would be
	// uuid.Nil — the target org must be set explicitly. ResourceID is the
	// joining user (the team_member the event is about), same shape as the
	// signup-time auto-join emit in the auth handler.
	if h.audit != nil {
		userIDStr := user.ID.String()
		details, _ := json.Marshal(map[string]string{"domain": target.Domain, "role": effectiveRole, "email": user.Email})
		params := db.UserActionParams{
			OrgID:        orgID,
			UserID:       user.ID,
			Action:       models.AuditActionTeamMemberAutoJoined,
			ResourceType: models.AuditResourceTeamMember,
			ResourceID:   &userIDStr,
			Details:      details,
		}
		if reqID := chiMiddleware.GetReqID(r.Context()); reqID != "" {
			params.RequestID = &reqID
		}
		if ua := r.UserAgent(); ua != "" {
			params.UserAgent = &ua
		}
		if ip := parseClientIP(r); ip != nil {
			params.IPAddress = ip
		}
		h.audit.EmitUserAction(r.Context(), params)
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.MembershipSummary]{Data: models.MembershipSummary{
		OrgID:   orgID,
		OrgName: target.OrgName,
		Role:    models.Role(effectiveRole),
	}})
}

// joinableForUser computes the joinable-org set for a user. An empty
// (non-nil) slice means "no domain-based access", not error. The second
// return is true when the user's email is unverified but its domain IS
// captured — i.e. verifying would unlock a join.
func (h *OrgDomainsHandler) joinableForUser(ctx context.Context, user *models.User) ([]models.JoinableOrganization, bool, error) {
	none := []models.JoinableOrganization{}

	emailDomain := domains.EmailDomain(user.Email)
	if emailDomain == "" || domains.IsPublicEmailDomain(emailDomain) {
		return none, false, nil
	}

	// Only attested emails count: a password account claiming an address
	// it never proved must not see (or join) the matching org.
	verifiedAt, err := h.users.GetEmailVerifiedAt(ctx, user.ID)
	if err != nil {
		return nil, false, fmt.Errorf("load email verification: %w", err)
	}
	if verifiedAt == nil {
		exists, err := h.store.AutoJoinDomainExists(ctx, emailDomain)
		if err != nil {
			return nil, false, fmt.Errorf("check capturable domain: %w", err)
		}
		return none, exists, nil
	}

	joinable, err := h.store.ListJoinableForUser(ctx, user.ID, emailDomain)
	if err != nil {
		return nil, false, err
	}
	if joinable == nil {
		joinable = none
	}
	return joinable, false, nil
}
