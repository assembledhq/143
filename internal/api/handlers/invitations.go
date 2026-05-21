package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// ListPendingInvitations returns the pending invitations addressed to the
// authenticated user — surfaces the in-app "you have invitations" affordance
// in the org switcher without forcing the user to open the email link.
//
// Endpoint is intentionally not org-scoped: invitations span organizations,
// and a user with zero memberships still needs to see the invites that would
// give them access. Filtering and dedupe happen in the store query (see
// InvitationStore.ListPendingForUser); this handler only shapes the response.
//
// lint:allow-no-orgid reason="user-scoped query spanning all orgs the user is invited to"
func (h *AuthHandler) ListPendingInvitations(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}

	githubLogin := ""
	if user.GitHubLogin != nil {
		githubLogin = *user.GitHubLogin
	}

	rows, err := h.invitationStore.ListPendingForUser(r.Context(), user.ID, user.Email, githubLogin)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list pending invitations", err)
		return
	}

	responses := make([]models.PendingInvitationForUser, 0, len(rows))
	for _, row := range rows {
		responses = append(responses, models.PendingInvitationForUser{
			ID:      row.ID,
			OrgID:   row.OrgID,
			OrgName: row.OrgName,
			Role:    row.Role,
			InvitedBy: models.UserBrief{
				ID:   row.InvitedBy,
				Name: row.InviterName,
			},
			ExpiresAt: row.ExpiresAt,
			CreatedAt: row.CreatedAt,
		})
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.PendingInvitationForUser]{Data: responses})
}

// AcceptInvitationByID is the in-app counterpart to ClaimInvitation: the user
// is already authenticated, sees the invitation in their org switcher, and
// accepts it inline. Distinct from the token route because (a) the token is
// never returned by the API once the user is in-app, and (b) this path does
// not move the user's persisted active-org preference — the frontend offers
// an explicit "Switch to it" affordance after acceptance so the user is not
// teleported out of whatever org they were working in.
//
// Recipient match is re-checked server-side against the session even though
// the listing already filtered to claimable invites: the URL-named id could
// otherwise be probed by an authenticated user against any invitation row.
//
// lint:allow-no-orgid reason="invitee-scoped accept; org context is the invitation's own org_id"
func (h *AuthHandler) AcceptInvitationByID(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}

	invID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid invitation id")
		return
	}

	githubLogin := ""
	if user.GitHubLogin != nil {
		githubLogin = *user.GitHubLogin
	}

	inv, invErr, err := h.loadInvitationForRecipient(r, user, invID, githubLogin, false)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INVITE_LOOKUP_FAILED", "failed to look up invitation", err)
		return
	}
	if invErr != nil {
		writeError(w, r, invErr.status, invErr.code, invErr.message)
		return
	}

	effectiveRole, acceptErr, err := h.acceptValidatedInvitation(r.Context(), &inv, user.ID, string(inv.Role), acceptOptions{updateLastOrgID: false})
	if err != nil {
		zerolog.Ctx(r.Context()).Warn().Err(err).Str("user_id", user.ID.String()).Msg("failed to accept invitation by id")
		h.emitInvitationClaimFailed(r, user.ID, &inv, "INTERNAL_ERROR", "internal error during invitation accept")
		writeError(w, r, http.StatusInternalServerError, "ACCEPT_FAILED", "failed to accept invitation", err)
		return
	}
	if acceptErr != nil {
		h.emitInvitationClaimFailed(r, user.ID, &inv, acceptErr.code, acceptErr.message)
		writeError(w, r, acceptErr.status, acceptErr.code, acceptErr.message)
		return
	}

	h.emitInvitationAccepted(r, user.ID, &inv)

	// Echo the *effective* role rather than inv.Role: GrantAtLeast never
	// downgrades, so a user who already held a higher membership in this org
	// keeps it. Returning inv.Role here would mislead the org switcher into
	// rendering a role the user does not actually hold.
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"org_id": inv.OrgID,
			"role":   effectiveRole,
		},
	})
}

// DeclineInvitationByID lets the invitee dismiss an invitation from the
// in-app surface. The row is marked 'revoked' (rather than a separate
// 'declined' status) so the partial unique indexes free up immediately and
// an admin can re-invite without schema-aware logic. Decline-vs-revoke is
// distinguished in the audit stream (AuditActionTeamInvitationDeclined),
// which is where downstream analytics belong anyway.
//
// Audit policy — deliberately asymmetric from the token-claim path:
//
//   - Success → AuditActionTeamInvitationDeclined row against inv.OrgID.
//   - Malformed id / 404 / 403 non-recipient probe → no audit row.
//     Unlike the token-claim path, which emits AuditActionTeamInvitationClaimFailed
//     for wrong-account attempts, we do NOT persist a claim-failed row for
//     decline probes: the user is trying to make an invitation go away, not
//     acquire membership, so there is no privilege-escalation signal in the
//     attempt that admins of the target org need to see. The zerolog warn
//     emitted from loadInvitationForRecipient and the ClaimRateLimit's
//     per-IP ceiling still cover the ops/security-monitoring angle.
//   - Concurrent revoke race (410) → no audit row: the real action was
//     whichever write landed first, and it already logged its own row.
//
// lint:allow-no-orgid reason="invitee-scoped decline; org context is the invitation's own org_id"
func (h *AuthHandler) DeclineInvitationByID(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}

	invID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid invitation id")
		return
	}

	githubLogin := ""
	if user.GitHubLogin != nil {
		githubLogin = *user.GitHubLogin
	}

	// allowExpired=true on decline: if the dropdown was stale and the invite
	// expired while the user stared at it, surfacing INVITE_EXPIRED (410) here
	// would trap them with a row they can't dismiss. The Revoke WHERE
	// status='pending' still guards the real concurrency races.
	inv, invErr, err := h.loadInvitationForRecipient(r, user, invID, githubLogin, true)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INVITE_LOOKUP_FAILED", "failed to look up invitation", err)
		return
	}
	if invErr != nil {
		writeError(w, r, invErr.status, invErr.code, invErr.message)
		return
	}

	if err := h.invitationStore.Revoke(r.Context(), inv.OrgID, inv.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Concurrent revoke / accept landed first. Idempotent from the
			// invitee's perspective: the invitation is gone, which is what
			// they asked for. Surface a 410 so the frontend can refresh.
			writeError(w, r, http.StatusGone, "INVITE_INVALID", "this invitation is no longer valid")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "DECLINE_FAILED", "failed to decline invitation", err)
		return
	}

	h.emitInvitationDeclined(r, user.ID, &inv)
	w.WriteHeader(http.StatusNoContent)
}

// loadInvitationForRecipient is the lookup-and-validate prelude shared by
// the id-based accept and decline handlers. It returns:
//   - (inv, nil, nil) on success
//   - (zero, *invitationError, nil) for client-visible failures (404, 410, 403)
//   - (zero, nil, error) for unexpected DB failures the caller should log+500
//
// INVITE_MISMATCH is remapped to 403 here (vs the token route's 400): the
// id is part of the URL, so the request *names* a specific invitation the
// user has no claim to — that's an authorization failure, not malformed
// input. The mismatch is also logged so security monitoring can spot
// probing patterns the same way it does for the token-claim path. A
// lookup miss (ErrNoRows) is logged for the same reason: authenticated
// users walking random UUIDs is worth a breadcrumb even though the
// ClaimRateLimit caps the blast radius.
//
// allowExpired=true lets callers (the decline path) treat an expired
// invitation as a valid target — the recipient should still be able to
// dismiss it from their UI. Status and recipient-match checks are
// unconditional; the Revoke / Accept WHERE clauses remain the authoritative
// concurrency guard.
func (h *AuthHandler) loadInvitationForRecipient(r *http.Request, user *models.User, invID uuid.UUID, githubLogin string, allowExpired bool) (models.Invitation, *invitationError, error) {
	inv, err := h.invitationStore.GetByID(r.Context(), invID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			zerolog.Ctx(r.Context()).Warn().
				Str("user_id", user.ID.String()).
				Str("invitation_id", invID.String()).
				Msg("invitation accept/decline attempted against unknown id")
			return models.Invitation{}, &invitationError{http.StatusNotFound, "INVITE_NOT_FOUND", "invitation not found"}, nil
		}
		return models.Invitation{}, nil, err
	}

	invErr := validateInvitationForRecipient(inv, user.Email, githubLogin)
	if invErr != nil {
		if allowExpired && invErr.code == "INVITE_EXPIRED" {
			return inv, nil, nil
		}
		if invErr.code == "INVITE_MISMATCH" {
			// Defensive copy rather than mutating in place: a future refactor
			// of validateInvitationForRecipient that returns a package-level
			// sentinel (a common cleanup) would otherwise leak this 403
			// status into the token-claim path that wants the original 400.
			invErr = &invitationError{
				status:  http.StatusForbidden,
				code:    invErr.code,
				message: invErr.message,
			}
			zerolog.Ctx(r.Context()).Warn().
				Str("user_id", user.ID.String()).
				Str("invitation_id", inv.ID.String()).
				Msg("invitation accept/decline attempted by non-recipient")
		}
		return inv, invErr, nil
	}

	return inv, nil, nil
}

// emitInvitationDeclined records a decline action against the invitation's
// org. Mirrors emitInvitationAccepted so the audit stream carries a clean
// declined-vs-revoked distinction (the row's status column collapses both
// to 'revoked' to keep the partial unique indexes simple, but downstream
// analytics that care about the difference can read it from this action).
func (h *AuthHandler) emitInvitationDeclined(
	r *http.Request,
	userID uuid.UUID,
	inv *models.Invitation,
) {
	if h.audit == nil || inv == nil {
		return
	}
	invIDStr := inv.ID.String()
	details, _ := json.Marshal(map[string]any{
		"invitation_id":   inv.ID.String(),
		"email":           optString(inv.Email),
		"github_username": optString(inv.GitHubUsername),
		"role":            inv.Role,
		"invited_by":      inv.InvitedBy.String(),
		"changes": map[string]any{
			"status": auditChange("pending", "revoked"),
		},
	})
	h.audit.EmitUserAction(r.Context(), db.UserActionParams{
		OrgID:        inv.OrgID,
		UserID:       userID,
		Action:       models.AuditActionTeamInvitationDeclined,
		ResourceType: models.AuditResourceInvitation,
		ResourceID:   &invIDStr,
		Details:      details,
	})
}
