package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/email"
)

// teamUserStore is the interface the team handler depends on for user operations.
type teamUserStore interface {
	ListByOrg(ctx context.Context, orgID uuid.UUID) ([]models.User, error)
	GetByID(ctx context.Context, orgID, userID uuid.UUID) (models.User, error)
	GetByEmail(ctx context.Context, email string) (models.User, error)
	UpdateRole(ctx context.Context, orgID, userID uuid.UUID, role string) error
	Delete(ctx context.Context, orgID, userID uuid.UUID) error
	CountAdmins(ctx context.Context, orgID uuid.UUID) (int, error)
}

// teamSessionStore is the interface for session invalidation on member removal.
type teamSessionStore interface {
	DeleteByUserID(ctx context.Context, userID uuid.UUID) error
}

// teamInvitationStore is the interface for invitation operations.
type teamInvitationStore interface {
	Create(ctx context.Context, inv *models.Invitation) error
	GetByToken(ctx context.Context, token string) (models.Invitation, error)
	ListPendingByOrgWithInviter(ctx context.Context, orgID uuid.UUID) ([]models.InvitationWithInviter, error)
	Accept(ctx context.Context, id uuid.UUID) error
	Revoke(ctx context.Context, orgID, id uuid.UUID) error
}

// teamOrgStore is the interface for org lookups (needed for accept flow).
type teamOrgStore interface {
	GetByID(ctx context.Context, id uuid.UUID) (models.Organization, error)
}

var validRoles = []string{"admin", "member", "viewer"}

// TeamHandler serves the /api/v1/team/* endpoints.
type TeamHandler struct {
	users       teamUserStore
	sessions    teamSessionStore
	invitations teamInvitationStore
	orgs        teamOrgStore
	emailSender email.Sender
	frontendURL string
	audit       *db.AuditEmitter
}

// SetAuditEmitter injects the audit emitter for logging team events.
func (h *TeamHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

// NewTeamHandler creates a new team management handler.
func NewTeamHandler(
	users teamUserStore,
	sessions teamSessionStore,
	invitations teamInvitationStore,
	orgs teamOrgStore,
	frontendURL string,
	emailSender email.Sender,
) *TeamHandler {
	if emailSender == nil {
		emailSender = email.NewNoopSender()
	}
	return &TeamHandler{
		users:       users,
		sessions:    sessions,
		invitations: invitations,
		orgs:        orgs,
		emailSender: emailSender,
		frontendURL: frontendURL,
	}
}

// ListMembers returns all users in the org.
func (h *TeamHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	users, err := h.users.ListByOrg(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list members", err)
		return
	}
	if users == nil {
		users = []models.User{}
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.User]{Data: users})
}

// ChangeRole updates a member's role.
func (h *TeamHandler) ChangeRole(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	currentUser := middleware.UserFromContext(r.Context())

	memberID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid member ID")
		return
	}

	var body struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}

	if !slices.Contains(validRoles, body.Role) {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "invalid role: must be admin, member, or viewer")
		return
	}

	if currentUser != nil && currentUser.ID == memberID {
		writeError(w, r, http.StatusBadRequest, "CANNOT_CHANGE_OWN_ROLE", "admins cannot change their own role")
		return
	}

	// If demoting from admin, ensure they're not the last admin.
	member, err := h.users.GetByID(r.Context(), orgID, memberID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "MEMBER_NOT_FOUND", "member not found in this organization")
		return
	}

	if member.Role == "admin" && body.Role != "admin" {
		count, err := h.users.CountAdmins(r.Context(), orgID)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "COUNT_FAILED", "failed to check admin count", err)
			return
		}
		if count <= 1 {
			writeError(w, r, http.StatusBadRequest, "LAST_ADMIN", "cannot demote the last admin")
			return
		}
	}

	if err := h.users.UpdateRole(r.Context(), orgID, memberID, body.Role); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update role", err)
		return
	}

	memberIDStr := memberID.String()
	details, marshalErr := json.Marshal(map[string]string{"new_role": body.Role, "previous_role": member.Role})
	if marshalErr != nil {
		zerolog.Ctx(r.Context()).Warn().Err(marshalErr).Msg("failed to marshal audit details for role change")
	}
	emitUserAudit(h.audit, r, models.AuditActionTeamMemberRoleChanged, models.AuditResourceTeamMember, &memberIDStr, details)

	member.Role = body.Role
	writeJSON(w, http.StatusOK, models.SingleResponse[models.User]{Data: member})
}

// RemoveMember removes a user from the org and invalidates their sessions.
func (h *TeamHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	currentUser := middleware.UserFromContext(r.Context())

	memberID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid member ID")
		return
	}

	if currentUser != nil && currentUser.ID == memberID {
		writeError(w, r, http.StatusBadRequest, "CANNOT_REMOVE_SELF", "admins cannot remove themselves")
		return
	}

	// Check that the member exists and isn't the last admin.
	member, err := h.users.GetByID(r.Context(), orgID, memberID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "MEMBER_NOT_FOUND", "member not found in this organization")
		return
	}

	if member.Role == "admin" {
		count, err := h.users.CountAdmins(r.Context(), orgID)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "COUNT_FAILED", "failed to check admin count", err)
			return
		}
		if count <= 1 {
			writeError(w, r, http.StatusBadRequest, "LAST_ADMIN", "cannot remove the last admin")
			return
		}
	}

	if err := h.users.Delete(r.Context(), orgID, memberID); err != nil {
		writeError(w, r, http.StatusInternalServerError, "DELETE_FAILED", "failed to remove member", err)
		return
	}

	removedIDStr := memberID.String()
	details, marshalErr := json.Marshal(map[string]string{"removed_email": member.Email})
	if marshalErr != nil {
		zerolog.Ctx(r.Context()).Warn().Err(marshalErr).Msg("failed to marshal audit details for member removal")
	}
	emitUserAudit(h.audit, r, models.AuditActionTeamMemberRemoved, models.AuditResourceTeamMember, &removedIDStr, details)

	// Invalidate all sessions for the removed user.
	if err := h.sessions.DeleteByUserID(r.Context(), memberID); err != nil {
		zerolog.Ctx(r.Context()).Warn().Err(err).Msg("failed to delete sessions for removed user")
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Invitation endpoints ---

// CreateInvitation creates a new invitation and logs the accept link to console.
func (h *TeamHandler) CreateInvitation(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	currentUser := middleware.UserFromContext(r.Context())

	var body struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}

	body.Email = strings.TrimSpace(strings.ToLower(body.Email))

	if _, err := mail.ParseAddress(body.Email); err != nil {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "invalid email address")
		return
	}

	if body.Role == "" {
		body.Role = "member"
	}
	if !slices.Contains(validRoles, body.Role) {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "invalid role: must be admin, member, or viewer")
		return
	}

	// Check if email is already a member of this org.
	if existing, err := h.users.GetByEmail(r.Context(), body.Email); err == nil && existing.OrgID == orgID {
		writeError(w, r, http.StatusConflict, "ALREADY_MEMBER", "this email is already a member of the organization")
		return
	}

	token, err := generateRandomString(32)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "TOKEN_ERROR", "failed to generate invitation token", err)
		return
	}

	inv := &models.Invitation{
		OrgID:     orgID,
		Email:     body.Email,
		Role:      body.Role,
		InvitedBy: currentUser.ID,
		Token:     token,
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
	}

	if err := h.invitations.Create(r.Context(), inv); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeError(w, r, http.StatusConflict, "INVITE_EXISTS", "a pending invitation already exists for this email")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create invitation", err)
		return
	}

	invIDStr := inv.ID.String()
	invDetails, marshalErr := json.Marshal(map[string]string{"email": body.Email, "role": body.Role})
	if marshalErr != nil {
		zerolog.Ctx(r.Context()).Warn().Err(marshalErr).Msg("failed to marshal audit details for invitation")
	}
	emitUserAudit(h.audit, r, models.AuditActionTeamMemberInvited, models.AuditResourceInvitation, &invIDStr, invDetails)

	acceptURL := h.frontendURL + "/invite/accept?token=" + token

	// Look up org name for the email.
	orgName := ""
	if org, orgErr := h.orgs.GetByID(r.Context(), orgID); orgErr == nil {
		orgName = org.Name
	}

	// Send invitation email. If delivery fails, log the error but don't fail
	// the request — the invitation record is valid and the admin can share
	// the link manually.
	if err := h.emailSender.SendInvitation(r.Context(), body.Email, currentUser.Name, orgName, acceptURL); err != nil {
		zerolog.Ctx(r.Context()).Warn().Err(err).
			Str("email", body.Email).
			Msg("failed to send invitation email — invitation is still valid")
	}

	zerolog.Ctx(r.Context()).Info().
		Str("email", body.Email).
		Str("role", body.Role).
		Str("accept_url", acceptURL).
		Msg("invitation created")

	resp := models.InvitationResponse{
		ID:     inv.ID,
		Email:  inv.Email,
		Role:   inv.Role,
		Status: inv.Status,
		InvitedBy: models.UserBrief{
			ID:   currentUser.ID,
			Name: currentUser.Name,
		},
		ExpiresAt: inv.ExpiresAt,
		CreatedAt: inv.CreatedAt,
	}

	writeJSON(w, http.StatusCreated, models.SingleResponse[models.InvitationResponse]{Data: resp})
}

// ListInvitations returns all pending invitations for the org.
func (h *TeamHandler) ListInvitations(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	invitations, err := h.invitations.ListPendingByOrgWithInviter(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list invitations", err)
		return
	}

	responses := make([]models.InvitationResponse, 0, len(invitations))
	for _, inv := range invitations {
		responses = append(responses, models.InvitationResponse{
			ID:     inv.ID,
			Email:  inv.Email,
			Role:   inv.Role,
			Status: inv.Status,
			InvitedBy: models.UserBrief{
				ID:   inv.InvitedBy,
				Name: inv.InviterName,
			},
			ExpiresAt: inv.ExpiresAt,
			CreatedAt: inv.CreatedAt,
		})
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.InvitationResponse]{Data: responses})
}

// RevokeInvitation revokes a pending invitation.
func (h *TeamHandler) RevokeInvitation(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	invID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid invitation ID")
		return
	}

	if err := h.invitations.Revoke(r.Context(), orgID, invID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "INVITE_NOT_FOUND", "invitation not found or already revoked")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "REVOKE_FAILED", "failed to revoke invitation", err)
		return
	}

	revokedIDStr := invID.String()
	emitUserAudit(h.audit, r, models.AuditActionTeamInvitationRevoked, models.AuditResourceInvitation, &revokedIDStr, nil)
	w.WriteHeader(http.StatusNoContent)
}

// AcceptInvitation validates the token and returns the appropriate action.
// This endpoint is public (no auth required).
func (h *TeamHandler) AcceptInvitation(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Token == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "token is required")
		return
	}

	inv, err := h.invitations.GetByToken(r.Context(), body.Token)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "INVITE_NOT_FOUND", "invitation not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "LOOKUP_FAILED", "failed to look up invitation", err)
		return
	}

	// Check status-specific errors.
	switch inv.Status {
	case "accepted":
		writeError(w, r, http.StatusGone, "INVITE_ALREADY_USED", "this invitation has already been accepted")
		return
	case "revoked":
		writeError(w, r, http.StatusGone, "INVITE_REVOKED", "this invitation has been revoked")
		return
	}

	if inv.Status != "pending" {
		writeError(w, r, http.StatusGone, "INVITE_EXPIRED", "this invitation is no longer valid")
		return
	}

	if time.Now().After(inv.ExpiresAt) {
		writeError(w, r, http.StatusGone, "INVITE_EXPIRED", "this invitation has expired")
		return
	}

	// Look up the org name for the response.
	org, err := h.orgs.GetByID(r.Context(), inv.OrgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "ORG_LOOKUP_FAILED", "failed to look up organization", err)
		return
	}

	// Check if the invited email already has an account.
	if _, err := h.users.GetByEmail(r.Context(), inv.Email); err == nil {
		// User exists — they should sign in to claim the invitation.
		writeJSON(w, http.StatusOK, models.SingleResponse[map[string]string]{
			Data: map[string]string{
				"action":   "login",
				"email":    inv.Email,
				"org_name": org.Name,
			},
		})
		return
	}

	// User does not exist — redirect to register with invitation token.
	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]string]{
		Data: map[string]string{
			"action":   "register",
			"email":    inv.Email,
			"org_name": org.Name,
		},
	})
}
