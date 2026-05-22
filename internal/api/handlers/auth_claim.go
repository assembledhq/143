package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// acceptOptions controls the side-effects of acceptValidatedInvitation that
// are required by some entry points but harmful at others.
type acceptOptions struct {
	// updateLastOrgID, when true, persists the claimed org as the user's
	// default for future logins. Set on the token-based claim path (user is
	// acting on the email link / OAuth invite cookie, so landing in the new
	// org next session is expected). Not set on the in-app id-based accept
	// path: the user is mid-flow inside their current org and the frontend
	// offers an explicit "Switch to it" affordance after acceptance.
	updateLastOrgID bool
}

// acceptValidatedInvitation runs the write-side of an invitation claim:
// mark the row accepted, grant a membership at GrantAtLeast semantics, and
// optionally persist the claimed org as the user's default. Caller must
// have already validated the invitation (status == "pending", not expired,
// recipient identifier matches the user) — this helper trusts the row
// passed in and only re-checks the status race via Accept's WHERE clause.
//
// The accept and grant always commit together; the last_org_id update
// joins them when opts.updateLastOrgID is set. The Accept WHERE
// status='pending' guard converts a concurrent Revoke into a clean
// INVITE_INVALID error, rolling the would-be membership grant back with
// the rest of the tx.
//
// Returns the effective role after the grant — different from inv.Role
// whenever the user already held a higher role (GrantAtLeast never
// downgrades; admin re-invited as viewer stays admin).
func (h *AuthHandler) acceptValidatedInvitation(
	ctx context.Context,
	inv *models.Invitation,
	userID uuid.UUID,
	role models.Role,
	opts acceptOptions,
) (string, *invitationError, error) {
	if h.pool == nil {
		return "", nil, fmt.Errorf("auth handler pool is not configured")
	}
	if inv == nil {
		return "", nil, fmt.Errorf("invitation is required")
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("begin accept transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := db.NewInvitationStore(tx).Accept(ctx, inv.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", &invitationError{
				status:  http.StatusGone,
				code:    "INVITE_INVALID",
				message: "this invitation is no longer valid",
			}, nil
		}
		return "", nil, fmt.Errorf("accept invitation: %w", err)
	}

	effectiveRole, err := db.NewOrganizationMembershipStore(tx).GrantAtLeast(ctx, userID, inv.OrgID, role)
	if err != nil {
		return "", nil, fmt.Errorf("grant membership: %w", err)
	}
	if opts.updateLastOrgID {
		if err := db.NewUserStore(tx).UpdateLastOrgID(ctx, userID, &inv.OrgID); err != nil {
			return "", nil, fmt.Errorf("update user last org: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", nil, fmt.Errorf("commit accept transaction: %w", err)
	}
	return effectiveRole, nil, nil
}

// claimInvitationForExistingUser validates a pending invitation by token
// and grants the authenticated user a membership in the inviting org. The
// caller's email (and GitHub login when present) must match the invitation
// per the same rules as the signup flow.
//
// The user's persisted last_org_id is updated to the claimed org: the only
// callers are the email-link claim endpoint and the OAuth-callback pending-
// invite path, both of which represent the user expressing "I want to land
// in this org next time."
//
// The returned *models.Invitation is non-nil whenever the invitation row
// was loaded — including failure cases past the initial GetByToken lookup —
// so the caller can emit org-scoped audit events for failed claims.
func (h *AuthHandler) claimInvitationForExistingUser(
	ctx context.Context,
	token, userEmail, githubLogin string,
	userID uuid.UUID,
) (*models.Invitation, string, *invitationError, error) {
	// Fail fast on a misconfigured handler. validateInvitation would otherwise
	// nil-deref on the invitation store before reaching acceptValidatedInvitation's
	// own pool check — this preserves the top-level contract that a nil pool
	// returns an error rather than panicking.
	if h.pool == nil {
		return nil, "", nil, fmt.Errorf("auth handler pool is not configured")
	}
	inv, _, role, invErr := h.validateInvitation(ctx, token, userEmail, githubLogin)
	if invErr != nil {
		return invitationOrNil(inv), "", invErr, nil
	}
	effectiveRole, acceptErr, err := h.acceptValidatedInvitation(ctx, &inv, userID, role, acceptOptions{updateLastOrgID: true})
	if err != nil {
		return &inv, "", nil, err
	}
	if acceptErr != nil {
		return &inv, "", acceptErr, nil
	}
	return &inv, effectiveRole, nil, nil
}

// invitationOrNil returns &inv only when the invitation row was actually
// populated (id non-zero). validateInvitationWithStore returns a zero-value
// Invitation when the GetByToken lookup itself failed, which is not useful
// for org-scoped audit emission.
func invitationOrNil(inv models.Invitation) *models.Invitation {
	if inv.ID == uuid.Nil {
		return nil
	}
	return &inv
}

// claimPendingInvitationForExistingUser is the best-effort adaptor used by
// OAuth callback paths: when an existing user logs in with a pending
// invitation cookie, we try to grant them membership in the inviting org.
// Failures are logged but do not abort the auth flow — the user can still
// sign into their existing org and retry the invite later.
//
// Both success and failure are audited (when the invitation row was loaded
// far enough to identify the org), so security review can spot patterns of
// failed claims — e.g. a leaked token being tried by the wrong account.
func (h *AuthHandler) claimPendingInvitationForExistingUser(
	r *http.Request,
	token, userEmail, githubLogin string,
	userID uuid.UUID,
) {
	if token == "" {
		return
	}
	inv, _, invErr, err := h.claimInvitationForExistingUser(r.Context(), token, userEmail, githubLogin, userID)
	if err != nil {
		// The wrapped error can contain raw SQL or filesystem details
		// (e.g. pgx error messages, store-layer wrap strings). Those belong
		// in the structured log where ops can correlate them, not in an
		// audit-log JSON column that is served back to admin UIs and kept
		// forever. Persist a generic code + fixed message instead.
		zerolog.Ctx(r.Context()).Warn().Err(err).Str("user_id", userID.String()).Msg("failed to claim pending invitation")
		h.emitInvitationClaimFailed(r, userID, inv, "INTERNAL_ERROR", "internal error during invitation claim")
		return
	}
	if invErr != nil {
		zerolog.Ctx(r.Context()).Info().
			Str("code", invErr.code).
			Str("user_id", userID.String()).
			Msg("pending invitation claim rejected during oauth login")
		h.emitInvitationClaimFailed(r, userID, inv, invErr.code, invErr.message)
		return
	}
	if inv != nil {
		h.emitInvitationAccepted(r, userID, inv)
	}
}

// emitInvitationClaimFailed records a failed invitation claim attempt for
// security observability. No-op when the audit emitter is nil or the
// invitation could not be identified (org context required for audit row).
func (h *AuthHandler) emitInvitationClaimFailed(
	r *http.Request,
	userID uuid.UUID,
	inv *models.Invitation,
	code, message string,
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
		"status":          inv.Status,
		"invited_by":      inv.InvitedBy.String(),
		"code":            code,
		"message":         message,
	})
	h.audit.EmitUserAction(r.Context(), db.UserActionParams{
		OrgID:        inv.OrgID,
		UserID:       userID,
		Action:       models.AuditActionTeamInvitationClaimFailed,
		ResourceType: models.AuditResourceInvitation,
		ResourceID:   &invIDStr,
		Details:      details,
	})
}

// logInvitationClaimUnknownToken records an attempt by an authenticated user
// to redeem a token that does not match any invitation row. Unlike
// emitInvitationClaimFailed, there is no invitation ID to anchor an audit
// entry to, so the signal goes to structured logs instead — alerting on a
// sustained rate of these events is the primary way to catch someone hammering
// the claim endpoint to guess valid tokens.
//
// The token itself is never logged. A 12-character prefix is included so ops
// can correlate bursts (all the same prefix = scripted enumeration, varied
// prefixes = reused credential stuffing list) without persisting material
// that could let a log reader redeem a leaked-but-not-yet-claimed token.
func logInvitationClaimUnknownToken(r *http.Request, userID uuid.UUID, token, code string) {
	prefix := token
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	ip := ""
	if p := parseClientIP(r); p != nil {
		ip = p.Addr().String()
	}
	zerolog.Ctx(r.Context()).Warn().
		Str("user_id", userID.String()).
		Str("ip", ip).
		Str("token_prefix", prefix).
		Str("code", code).
		Msg("invitation claim attempted with unknown token")
}

// emitInvitationAccepted records a successful invitation claim. Mirrors the
// failure path so OAuth-driven joins are visible in audit alongside direct
// ClaimInvitation calls.
func (h *AuthHandler) emitInvitationAccepted(
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
			"status": auditChange("pending", "accepted"),
		},
	})
	h.audit.EmitUserAction(r.Context(), db.UserActionParams{
		OrgID:        inv.OrgID,
		UserID:       userID,
		Action:       models.AuditActionTeamInvitationAccepted,
		ResourceType: models.AuditResourceInvitation,
		ResourceID:   &invIDStr,
		Details:      details,
	})
}

// readAndClearPendingInvitationCookie reads and clears the pending_invitation
// cookie set during the OAuth login redirect. Returns empty string if no
// cookie is present. Clearing is unconditional so a stale cookie from a
// previous aborted flow doesn't leak into the next signup attempt.
func readAndClearPendingInvitationCookie(w http.ResponseWriter, r *http.Request) string {
	cookie, err := r.Cookie("pending_invitation")
	if err != nil || cookie.Value == "" {
		return ""
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "pending_invitation",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
	return cookie.Value
}
