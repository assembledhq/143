package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
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

// teamUserStore is the interface the team handler depends on for user lookups.
// Membership-scoped operations (role updates, removal, admin counts) live on
// teamMembershipStore instead — users own identity, memberships own access.
type teamUserStore interface {
	ListByOrgViaMemberships(ctx context.Context, orgID uuid.UUID, filters db.MembershipPageFilters) ([]models.User, time.Time, error)
	GetByIDGlobal(ctx context.Context, userID uuid.UUID) (models.User, error)
	GetByEmail(ctx context.Context, email string) (models.User, error)
	IsGitHubLoginMemberOfOrg(ctx context.Context, githubLogin string, orgID uuid.UUID) (bool, error)
}

// teamMembershipStore is the authoritative source for per-org role and access.
// Every decision that was "does this user belong to this org, and at what
// level" routes through here — the users table is only consulted for display
// fields (name, email, avatar, github login).
//
// UpdateRoleGuarded / RemoveGuarded are the atomic variants used by mutating
// handlers so the last-admin invariant cannot be violated by two concurrent
// admin demotions. The non-guarded Get / CountForUser remain for read-only
// lookups that don't participate in the invariant (member directory, post-
// removal session cleanup decision).
type teamMembershipStore interface {
	Get(ctx context.Context, userID, orgID uuid.UUID) (models.OrganizationMembership, error)
	UpdateRoleGuarded(ctx context.Context, userID, orgID uuid.UUID, newRole string) (string, error)
	RemoveGuarded(ctx context.Context, userID, orgID uuid.UUID) (prevRole string, revokedInvitations int, err error)
	CountForUser(ctx context.Context, userID uuid.UUID) (int, error)
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

// teamIntegrationStore looks up GitHub App installations for the org.
type teamIntegrationStore interface {
	ListByOrgAndProvider(ctx context.Context, orgID uuid.UUID, provider string) ([]models.Integration, error)
}

// teamGitHubService issues installation tokens for GitHub App installations.
type teamGitHubService interface {
	GetInstallationToken(ctx context.Context, installationID int64) (string, error)
}

// teamRepositoryStore supplies repo-scoped fallbacks for GitHub App metadata.
// Some orgs predate integration.config.installation_id but already have active
// repo rows populated from a prior install sync; those repos still carry the
// installation id we need for invite search.
type teamRepositoryStore interface {
	GetAnyInstallationIDByOrg(ctx context.Context, orgID uuid.UUID) (int64, error)
}

// TeamHandler serves the /api/v1/team/* endpoints.
type TeamHandler struct {
	users        teamUserStore
	memberships  teamMembershipStore
	sessions     teamSessionStore
	invitations  teamInvitationStore
	orgs         teamOrgStore
	repositories teamRepositoryStore
	integrations teamIntegrationStore
	githubSvc    teamGitHubService
	httpClient   *http.Client
	emailSender  email.Sender
	frontendURL  string
	audit        *db.AuditEmitter
}

// SetGitHubIntegration wires the integration store and GitHub App service so
// the team handler can answer /api/v1/team/github/* lookups (autocomplete,
// connection status). Both must be non-nil to enable GitHub user search.
func (h *TeamHandler) SetGitHubIntegration(integrations teamIntegrationStore, ghSvc teamGitHubService) {
	h.integrations = integrations
	h.githubSvc = ghSvc
}

func (h *TeamHandler) SetRepositoryStore(repositories teamRepositoryStore) {
	h.repositories = repositories
}

// SetAuditEmitter injects the audit emitter for logging team events.
func (h *TeamHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

// NewTeamHandler creates a new team management handler.
func NewTeamHandler(
	users teamUserStore,
	memberships teamMembershipStore,
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
		memberships: memberships,
		sessions:    sessions,
		invitations: invitations,
		orgs:        orgs,
		emailSender: emailSender,
		frontendURL: frontendURL,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
	}
}

// ListMembers returns a cursor-paginated page of users holding a membership
// in the active org, regardless of whether the org is their legacy primary.
// The directory is keyed on organization_memberships (joined with users for
// display fields) rather than users.org_id, so multi-org members (e.g.
// someone who joined via ClaimInvitation) appear exactly where the admin
// expects to manage them.
//
// Accepts ?limit (1..500, default 100) and ?cursor (opaque, from prior
// page's meta.next_cursor). Without pagination a large org's member list
// would produce an unbounded response and a single slow query; with cursor
// pagination the frontend pages as the admin scrolls.
func (h *TeamHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	limit := clampListLimit(queryInt(r, "limit", 100), 100, 500)
	filters := db.MembershipPageFilters{Limit: limit}

	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		cursorTime, rawID, err := decodeCursor(cursor)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "invalid cursor")
			return
		}
		cursorUserID, err := uuid.Parse(rawID)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "invalid cursor")
			return
		}
		filters.CursorCreatedAt = &cursorTime
		filters.CursorUserID = &cursorUserID
	}

	users, lastMembershipTime, err := h.users.ListByOrgViaMemberships(r.Context(), orgID, filters)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list members", err)
		return
	}
	if users == nil {
		users = []models.User{}
	}

	var nextCursor string
	if len(users) == limit {
		nextCursor = encodeCursor(lastMembershipTime, users[len(users)-1].ID.String())
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.User]{
		Data: users,
		Meta: models.PaginationMeta{NextCursor: nextCursor},
	})
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

	if !models.IsValidRole(body.Role) {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "invalid role: must be admin, builder, member, or viewer")
		return
	}

	if currentUser != nil && currentUser.ID == memberID {
		writeError(w, r, http.StatusBadRequest, "CANNOT_CHANGE_OWN_ROLE", "admins cannot change their own role")
		return
	}

	// UpdateRoleGuarded is atomic: it locks the org's admin rows for the
	// duration of the tx so two concurrent demotions can't both pass the
	// "more than one admin" check and leave the org with zero admins.
	prevRole, err := h.memberships.UpdateRoleGuarded(r.Context(), memberID, orgID, body.Role)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "MEMBER_NOT_FOUND", "member not found in this organization")
			return
		}
		if errors.Is(err, db.ErrLastAdmin) {
			writeError(w, r, http.StatusBadRequest, "LAST_ADMIN", "cannot demote the last admin")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update role", err)
		return
	}

	memberIDStr := memberID.String()
	details, marshalErr := json.Marshal(map[string]string{"new_role": body.Role, "previous_role": prevRole})
	if marshalErr != nil {
		zerolog.Ctx(r.Context()).Warn().Err(marshalErr).Msg("failed to marshal audit details for role change")
	}
	emitUserAudit(h.audit, r, models.AuditActionTeamMemberRoleChanged, models.AuditResourceTeamMember, &memberIDStr, details)

	// Role has already been updated in the DB. The follow-up display-row
	// lookup is best-effort: if it fails, we still return 200 with a minimal
	// payload so the client treats the mutation as succeeded (because it did).
	// Returning 500 would suggest the role change failed and mislead retries.
	member, err := h.users.GetByIDGlobal(r.Context(), memberID)
	if err != nil {
		zerolog.Ctx(r.Context()).Warn().Err(err).Str("member_id", memberIDStr).Msg("team: role updated but display lookup failed; returning minimal payload")
		writeJSON(w, http.StatusOK, models.SingleResponse[models.User]{Data: models.User{ID: memberID, Role: models.Role(body.Role)}})
		return
	}

	member.Role = models.Role(body.Role)
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

	// Fetch identity for audit details. A missing user row is a true 404
	// (the requested member ID doesn't exist at all); any other error is a
	// lookup failure and should surface as 500 rather than be hidden as 404.
	member, err := h.users.GetByIDGlobal(r.Context(), memberID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "MEMBER_NOT_FOUND", "member not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "LOOKUP_FAILED", "failed to look up member", err)
		return
	}

	// RemoveGuarded does the membership lookup, last-admin check, and the
	// delete (plus the org-scoped cleanups in the underlying Remove CTE) all
	// inside a transaction that locks the org's admin rows. Two concurrent
	// admin removals can't both pass the count > 1 check.
	prevRole, revokedInvitations, err := h.memberships.RemoveGuarded(r.Context(), memberID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "MEMBER_NOT_FOUND", "member not found in this organization")
			return
		}
		if errors.Is(err, db.ErrLastAdmin) {
			writeError(w, r, http.StatusBadRequest, "LAST_ADMIN", "cannot remove the last admin")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "DELETE_FAILED", "failed to remove member", err)
		return
	}
	removedIDStr := memberID.String()
	// revoked_invitations is the number of pending invitations this member
	// authored in this org that Remove flipped to 'revoked' as part of
	// cleanup. Including it keeps the member-removed audit row self-
	// describing — an operator reading only audit history can reconcile
	// invitation status flips to this removal without cross-referencing
	// timestamps.
	details, marshalErr := json.Marshal(map[string]any{
		"removed_email":       member.Email,
		"previous_role":       prevRole,
		"revoked_invitations": revokedInvitations,
	})
	if marshalErr != nil {
		zerolog.Ctx(r.Context()).Warn().Err(marshalErr).Msg("failed to marshal audit details for member removal")
	}
	emitUserAudit(h.audit, r, models.AuditActionTeamMemberRemoved, models.AuditResourceTeamMember, &removedIDStr, details)

	// Only invalidate the user's sessions if they have no remaining memberships
	// — otherwise they'd be logged out of orgs they still belong to. A failure
	// here is non-fatal: the removal itself succeeded and stale sessions will
	// fail their next middleware check (membership lookup returns ErrNoRows)
	// and get rejected there.
	remaining, countErr := h.memberships.CountForUser(r.Context(), memberID)
	if countErr != nil {
		zerolog.Ctx(r.Context()).Warn().Err(countErr).Msg("failed to count remaining memberships after removal")
	} else if remaining == 0 {
		if err := h.sessions.DeleteByUserID(r.Context(), memberID); err != nil {
			zerolog.Ctx(r.Context()).Warn().Err(err).Msg("failed to delete sessions for removed user")
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Invitation endpoints ---

// CreateInvitation creates a new invitation and sends an email notification
// when a durable contact email is present. GitHub-addressed invites keep that
// email for notifications, but acceptance is locked to the GitHub username.
func (h *TeamHandler) CreateInvitation(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	currentUser := middleware.UserFromContext(r.Context())

	var body struct {
		Email            string                            `json:"email"`
		GitHubUsername   string                            `json:"github_username"`
		AcceptanceMethod models.InvitationAcceptanceMethod `json:"acceptance_method"`
		Role             string                            `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}

	body.Email = strings.TrimSpace(strings.ToLower(body.Email))
	body.GitHubUsername = strings.TrimSpace(body.GitHubUsername)
	body.GitHubUsername = strings.TrimPrefix(body.GitHubUsername, "@")

	if body.Email == "" && body.GitHubUsername == "" {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "email or github_username is required")
		return
	}

	if body.Email != "" {
		if _, err := mail.ParseAddress(body.Email); err != nil {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "invalid email address")
			return
		}
	}

	if body.GitHubUsername != "" && !isValidGitHubUsername(body.GitHubUsername) {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "invalid github username")
		return
	}
	if body.AcceptanceMethod == "" {
		if body.GitHubUsername != "" {
			body.AcceptanceMethod = models.InvitationAcceptanceMethodGitHub
		} else {
			body.AcceptanceMethod = models.InvitationAcceptanceMethodEmail
		}
	}
	if err := body.AcceptanceMethod.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "invalid acceptance method: must be email, github, or either")
		return
	}
	if body.AcceptanceMethod == models.InvitationAcceptanceMethodEmail && body.Email == "" {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "email is required for email acceptance")
		return
	}
	if body.AcceptanceMethod == models.InvitationAcceptanceMethodGitHub && body.GitHubUsername == "" {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "github_username is required for github acceptance")
		return
	}

	if body.Role == "" {
		body.Role = string(models.RoleMember)
	}
	if !models.IsValidRole(body.Role) {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "invalid role: must be admin, builder, member, or viewer")
		return
	}

	// Check if the invitee is already a member of this org. With multi-org
	// memberships the users row alone no longer tells us — a user may exist
	// globally and be a member of *other* orgs without being a member here.
	// The authoritative check is memberships.Get(userID, orgID).
	if body.Email != "" {
		if existing, err := h.users.GetByEmail(r.Context(), body.Email); err == nil {
			if _, memErr := h.memberships.Get(r.Context(), existing.ID, orgID); memErr == nil {
				writeError(w, r, http.StatusConflict, "ALREADY_MEMBER", "this email is already a member of the organization")
				return
			} else if !errors.Is(memErr, pgx.ErrNoRows) {
				writeError(w, r, http.StatusInternalServerError, "LOOKUP_FAILED", "failed to check existing membership", memErr)
				return
			}
		}
	}
	if body.GitHubUsername != "" {
		// IsGitHubLoginMemberOfOrg joins users to organization_memberships so
		// the dedup catches members of this org regardless of which org's
		// row is their legacy primary. The previous ListByOrg approach
		// missed users whose only membership in this org was non-primary,
		// allowing duplicate invitations against actual members.
		isMember, err := h.users.IsGitHubLoginMemberOfOrg(r.Context(), body.GitHubUsername, orgID)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "LOOKUP_FAILED", "failed to check existing membership", err)
			return
		}
		if isMember {
			writeError(w, r, http.StatusConflict, "ALREADY_MEMBER", "this github user is already a member of the organization")
			return
		}
	}

	token, err := generateRandomString(32)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "TOKEN_ERROR", "failed to generate invitation token", err)
		return
	}

	inv := &models.Invitation{
		OrgID:            orgID,
		AcceptanceMethod: body.AcceptanceMethod,
		Role:             models.Role(body.Role),
		InvitedBy:        currentUser.ID,
		Token:            token,
		ExpiresAt:        time.Now().Add(7 * 24 * time.Hour),
	}
	if body.Email != "" {
		email := body.Email
		inv.Email = &email
	}
	if body.GitHubUsername != "" {
		gh := body.GitHubUsername
		inv.GitHubUsername = &gh
	}

	if err := h.invitations.Create(r.Context(), inv); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeError(w, r, http.StatusConflict, "INVITE_EXISTS", "a pending invitation already exists for this recipient")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create invitation", err)
		return
	}

	invIDStr := inv.ID.String()
	auditPayload := map[string]string{"role": body.Role, "acceptance_method": string(body.AcceptanceMethod)}
	if body.Email != "" {
		auditPayload["email"] = body.Email
	}
	if body.GitHubUsername != "" {
		auditPayload["github_username"] = body.GitHubUsername
	}
	invDetails, marshalErr := json.Marshal(auditPayload)
	if marshalErr != nil {
		zerolog.Ctx(r.Context()).Warn().Err(marshalErr).Msg("failed to marshal audit details for invitation")
	}
	emitUserAudit(h.audit, r, models.AuditActionTeamMemberInvited, models.AuditResourceInvitation, &invIDStr, invDetails)

	// generateRandomString returns a hex-encoded string, so `token` is always
	// in [0-9a-f] and safe to concatenate into the query string without
	// url.QueryEscape. If the generator ever moves to base64 (or any
	// encoding that can produce `+`, `/`, `=`, or reserved URL chars) this
	// concat must be swapped for url.Values / url.QueryEscape to avoid
	// producing malformed accept links.
	acceptURL := h.frontendURL + "/invite/accept?token=" + token

	// Look up org name for the email.
	orgName := ""
	if org, orgErr := h.orgs.GetByID(r.Context(), orgID); orgErr == nil {
		orgName = org.Name
	}

	// Send invitation email only if we have an email. GitHub-only invites
	// rely on the user signing in via GitHub OAuth to claim their seat, or
	// on the admin sharing the accept link manually.
	if body.Email != "" {
		if err := h.emailSender.SendInvitation(r.Context(), body.Email, currentUser.Name, orgName, acceptURL); err != nil {
			zerolog.Ctx(r.Context()).Warn().Err(err).
				Str("email", body.Email).
				Msg("failed to send invitation email — invitation is still valid")
		}
	}

	zerolog.Ctx(r.Context()).Info().
		Str("email", body.Email).
		Str("github_username", body.GitHubUsername).
		Str("role", body.Role).
		Str("accept_url", acceptURL).
		Msg("invitation created")

	resp := models.InvitationResponse{
		ID:               inv.ID,
		Email:            inv.Email,
		GitHubUsername:   inv.GitHubUsername,
		AcceptanceMethod: inv.AcceptanceMethod,
		Role:             inv.Role,
		Status:           inv.Status,
		InvitedBy: models.UserBrief{
			ID:   currentUser.ID,
			Name: currentUser.Name,
		},
		ExpiresAt: inv.ExpiresAt,
		CreatedAt: inv.CreatedAt,
	}

	writeJSON(w, http.StatusCreated, models.SingleResponse[models.InvitationResponse]{Data: resp})
}

// isValidGitHubUsername validates a GitHub username per GitHub's rules:
// alphanumerics and single hyphens, no leading/trailing hyphen, max 39 chars.
func isValidGitHubUsername(s string) bool {
	if s == "" || len(s) > 39 {
		return false
	}
	if s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		isAlnum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !isAlnum && c != '-' {
			return false
		}
		if c == '-' && i+1 < len(s) && s[i+1] == '-' {
			return false
		}
	}
	return true
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
			ID:               inv.ID,
			Email:            inv.Email,
			GitHubUsername:   inv.GitHubUsername,
			AcceptanceMethod: inv.AcceptanceMethod,
			Role:             inv.Role,
			Status:           inv.Status,
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
	emitUserAudit(h.audit, r, models.AuditActionTeamInvitationRevoked, models.AuditResourceInvitation, &revokedIDStr,
		marshalAuditDetails(*zerolog.Ctx(r.Context()), map[string]any{
			"invitation_id": invID.String(),
			"changes": map[string]any{
				"status": auditChange("pending", "revoked"),
			},
		}))
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

	payload := map[string]string{"org_name": org.Name}
	if inv.Email != nil {
		payload["email"] = *inv.Email
	}
	if inv.GitHubUsername != nil {
		payload["github_username"] = *inv.GitHubUsername
	}
	if inv.AcceptanceMethod != "" {
		payload["acceptance_method"] = string(inv.AcceptanceMethod)
	}

	if inv.AcceptanceMethod == models.InvitationAcceptanceMethodGitHub {
		payload["action"] = "login"
		writeJSON(w, http.StatusOK, models.SingleResponse[map[string]string]{Data: payload})
		return
	}

	// If the invitation is bound to an email, and that email already has an
	// account, direct the invitee to sign in. GitHub-only invitations always
	// route through the register flow; the GitHub OAuth callback then claims
	// the invitation by matching the github_username.
	if inv.Email != nil {
		if _, err := h.users.GetByEmail(r.Context(), *inv.Email); err == nil {
			payload["action"] = "login"
			writeJSON(w, http.StatusOK, models.SingleResponse[map[string]string]{Data: payload})
			return
		}
	}

	payload["action"] = "register"
	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]string]{Data: payload})
}

// --- GitHub user autocomplete for invitations ---

// GitHubInviteStatus reports whether the org has a connected GitHub App
// installation usable for invitation autocomplete.
type GitHubInviteStatus struct {
	Connected bool `json:"connected"`
}

// GitHubUserSuggestion is a single autocomplete result for the invite modal.
type GitHubUserSuggestion struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url,omitempty"`
}

// GitHubInviteStatus returns whether GitHub autocomplete is available for
// this org. Admins call this on modal open to decide whether to show the
// GitHub username input.
func (h *TeamHandler) GitHubInviteStatus(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	installationID, _ := h.getGitHubInstallationID(r.Context(), orgID)
	writeJSON(w, http.StatusOK, models.SingleResponse[GitHubInviteStatus]{
		Data: GitHubInviteStatus{Connected: installationID > 0 && h.githubSvc != nil},
	})
}

// SearchGitHubUsers proxies GitHub's user search API, scoped by the org's
// installed GitHub App so the frontend can autocomplete usernames in the
// invite dialog. Requires ?q= with at least one character.
func (h *TeamHandler) SearchGitHubUsers(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	q = strings.TrimPrefix(q, "@")
	if q == "" {
		writeJSON(w, http.StatusOK, models.ListResponse[GitHubUserSuggestion]{Data: []GitHubUserSuggestion{}})
		return
	}
	if !isValidGitHubUsernamePrefix(q) {
		writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "invalid search query")
		return
	}

	installationID, err := h.getGitHubInstallationID(r.Context(), orgID)
	if err != nil || installationID == 0 || h.githubSvc == nil {
		writeError(w, r, http.StatusServiceUnavailable, "GITHUB_NOT_CONNECTED", "github is not connected for this organization")
		return
	}

	token, err := h.githubSvc.GetInstallationToken(r.Context(), installationID)
	if err != nil {
		zerolog.Ctx(r.Context()).Warn().Err(err).Msg("failed to get github installation token")
		writeError(w, r, http.StatusBadGateway, "GITHUB_TOKEN_FAILED", "failed to authenticate with github")
		return
	}

	users, err := h.searchGitHubUsers(r.Context(), token, q)
	if err != nil {
		zerolog.Ctx(r.Context()).Warn().Err(err).Str("query", q).Msg("failed to search github users")
		writeError(w, r, http.StatusBadGateway, "GITHUB_SEARCH_FAILED", "failed to search github users")
		return
	}

	writeJSON(w, http.StatusOK, models.ListResponse[GitHubUserSuggestion]{Data: users})
}

// getGitHubInstallationID returns the installation_id for the org's active
// GitHub integration, or 0 if no usable integration exists.
func (h *TeamHandler) getGitHubInstallationID(ctx context.Context, orgID uuid.UUID) (int64, error) {
	if h.integrations == nil {
		return h.fallbackGitHubInstallationID(ctx, orgID)
	}
	integrations, err := h.integrations.ListByOrgAndProvider(ctx, orgID, string(models.IntegrationProviderGitHub))
	if err != nil {
		return 0, err
	}
	for _, integration := range integrations {
		if integration.Config == nil {
			continue
		}
		var cfg struct {
			InstallationID int64 `json:"installation_id"`
		}
		if unmarshalErr := json.Unmarshal(integration.Config, &cfg); unmarshalErr != nil {
			continue
		}
		if cfg.InstallationID > 0 {
			return cfg.InstallationID, nil
		}
	}
	return h.fallbackGitHubInstallationID(ctx, orgID)
}

func (h *TeamHandler) fallbackGitHubInstallationID(ctx context.Context, orgID uuid.UUID) (int64, error) {
	if h.repositories == nil {
		return 0, nil
	}
	installationID, err := h.repositories.GetAnyInstallationIDByOrg(ctx, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return installationID, nil
}

// searchGitHubUsers calls GitHub's REST user search and returns up to 10
// login+avatar pairs. The token is an installation access token; GitHub's
// search endpoint is available to installation tokens.
func (h *TeamHandler) searchGitHubUsers(ctx context.Context, token, query string) ([]GitHubUserSuggestion, error) {
	params := url.Values{
		"q":        {query + " in:login type:user"},
		"per_page": {"10"},
	}
	reqURL := "https://api.github.com/search/users?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	client := h.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("github search returned %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Items []struct {
			Login     string `json:"login"`
			AvatarURL string `json:"avatar_url"`
			Type      string `json:"type"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	suggestions := make([]GitHubUserSuggestion, 0, len(result.Items))
	for _, item := range result.Items {
		if item.Type != "" && item.Type != "User" {
			continue
		}
		suggestions = append(suggestions, GitHubUserSuggestion{
			Login:     item.Login,
			AvatarURL: item.AvatarURL,
		})
	}
	return suggestions, nil
}

// isValidGitHubUsernamePrefix allows the same character set as a full GitHub
// username but doesn't require the end-of-name constraints (a user typing
// mid-name may still end with a hyphen).
func isValidGitHubUsernamePrefix(s string) bool {
	if s == "" || len(s) > 39 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		isAlnum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !isAlnum && c != '-' {
			return false
		}
	}
	return true
}
