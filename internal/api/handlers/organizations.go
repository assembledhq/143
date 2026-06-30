package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// maxOrgNameLen bounds the display name for a newly-created org. 120 chars
// matches the bound enforced by the frontend CreateOrgDialog and leaves
// plenty of headroom for real team names without allowing paragraph-length
// junk that would break the switcher dropdown layout.
const maxOrgNameLen = 120

// OrganizationsHandler owns routes that act on organizations the authenticated
// user may not yet belong to — most notably POST /api/v1/organizations, which
// is how a signed-in user provisions an additional org for themselves without
// going through the signup flow.
type OrganizationsHandler struct {
	pool  db.TxStarter
	audit *db.AuditEmitter
}

func NewOrganizationsHandler(pool db.TxStarter) *OrganizationsHandler {
	return &OrganizationsHandler{pool: pool}
}

// SetAuditEmitter injects the audit emitter for logging organization events.
func (h *OrganizationsHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

// Create provisions a new organization with the authenticated user as its
// first admin. The org row and the initial admin membership are inserted in
// a single transaction so no org is ever persisted without at least one admin.
//
// This route is deliberately mounted OUTSIDE OrgContext: a zero-membership
// user (removed from their last org) must still be able to create a fresh org
// to escape that state, and OrgContext would 403 them before they ever reach
// this handler. Authentication is still required via UserFromContext.
//
// The response echoes the new org's id, name, the caller's role ("admin"), and
// the server-generated created_at. The frontend uses id to switch into the
// new org by setting the X-Active-Org-ID header on the very next request; no
// server-side session mutation happens here, which keeps the create step
// independent of the switch step.
//
// lint:allow-no-orgid reason="creates a new org; no pre-existing org context to scope against"
func (h *OrganizationsHandler) Create(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}
	if h.pool == nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "organizations handler pool is not configured")
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_NAME", "Name is required.")
		return
	}
	if len([]rune(name)) > maxOrgNameLen {
		writeError(w, r, http.StatusBadRequest, "NAME_TOO_LONG", fmt.Sprintf("Name must be %d characters or fewer.", maxOrgNameLen))
		return
	}

	org, err := h.createOrgWithAdmin(r.Context(), name, user.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "ORG_CREATE_FAILED", "failed to create organization", err)
		return
	}

	h.emitOrgCreated(r, user.ID, org)

	writeJSON(w, http.StatusCreated, map[string]any{
		"data": map[string]any{
			"id":         org.ID,
			"name":       org.Name,
			"role":       models.RoleAdmin,
			"created_at": org.CreatedAt,
		},
	})
}

func (h *OrganizationsHandler) createOrgWithAdmin(ctx context.Context, name string, userID uuid.UUID) (*models.Organization, error) {
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin org tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	org := &models.Organization{Name: name, Settings: models.DefaultNewOrganizationSettings()}
	if err := db.NewOrganizationStore(tx).Create(ctx, org); err != nil {
		return nil, fmt.Errorf("create organization: %w", err)
	}
	// Plain Insert (not GrantAtLeast): the org was just created inside this
	// tx, so a pre-existing membership would indicate a bug. Loud failure is
	// better than GrantAtLeast's silent no-op.
	if err := db.NewOrganizationMembershipStore(tx).Insert(ctx, userID, org.ID, models.RoleAdmin); err != nil {
		return nil, fmt.Errorf("grant admin membership: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit org tx: %w", err)
	}
	return org, nil
}

// emitOrgCreated writes the audit row against the newly-created org. The route
// runs outside OrgContext so OrgIDFromContext is uuid.Nil here — we set
// params.OrgID explicitly to the new org, matching auth.register's attribution
// pattern where the audit row belongs to the org the user just entered.
func (h *OrganizationsHandler) emitOrgCreated(r *http.Request, userID uuid.UUID, org *models.Organization) {
	if h.audit == nil || org == nil {
		return
	}
	resID := org.ID.String()
	params := db.UserActionParams{
		OrgID:        org.ID,
		UserID:       userID,
		Action:       models.AuditActionOrganizationCreated,
		ResourceType: models.AuditResourceOrganization,
		ResourceID:   &resID,
		Details: marshalAuditDetails(*zerolog.Ctx(r.Context()), map[string]any{
			"organization_id": org.ID.String(),
			"name":            org.Name,
			"created_by":      userID.String(),
		}),
	}
	if reqID := chiMiddleware.GetReqID(r.Context()); reqID != "" {
		params.RequestID = &reqID
	}
	if ip := parseClientIP(r); ip != nil {
		params.IPAddress = ip
	}
	if ua := r.UserAgent(); ua != "" {
		params.UserAgent = &ua
	}
	h.audit.EmitUserAction(r.Context(), params)
}
