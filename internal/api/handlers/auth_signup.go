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

// signupUserCreateFunc is the per-provider callback used by createSignupOrg to
// persist the user row inside the signup transaction. Each OAuth flow passes a
// closure that calls the appropriate UserStore method (CreateWithPassword /
// UpsertFromGitHub / UpsertFromGoogle) against the tx-scoped store.
type signupUserCreateFunc func(ctx context.Context, userStore *db.UserStore, user *models.User) error

// createSignupOrg atomically creates a fresh org, creates the signup user, and
// grants them an admin membership in one transaction. This is the primitive
// used by every "brand new user, no invitation" path (email register, GitHub
// first login, Google first login).
//
// Either all three rows are inserted or none are: if user creation fails, the
// org row rolls back rather than leaving an orphan. The caller provides the
// user-creation step as a closure so the same helper serves all three flows
// without branching on provider inside.
func (h *AuthHandler) createSignupOrg(
	ctx context.Context,
	orgName string,
	user *models.User,
	createUser signupUserCreateFunc,
) error {
	if h.pool == nil {
		return fmt.Errorf("auth handler pool is not configured")
	}
	if user == nil {
		return fmt.Errorf("user is required")
	}
	if createUser == nil {
		return fmt.Errorf("createUser callback is required")
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin signup transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	org := &models.Organization{
		Name:     orgName,
		Settings: json.RawMessage(`{}`),
	}
	if err := db.NewOrganizationStore(tx).Create(ctx, org); err != nil {
		return fmt.Errorf("create organization: %w", err)
	}

	user.OrgID = org.ID
	user.Role = models.RoleAdmin
	if err := createUser(ctx, db.NewUserStore(tx), user); err != nil {
		return fmt.Errorf("create signup user: %w", err)
	}

	if err := db.NewOrganizationMembershipStore(tx).Upsert(ctx, user.ID, org.ID, models.RoleAdmin); err != nil {
		return fmt.Errorf("grant admin membership: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit signup transaction: %w", err)
	}
	return nil
}

// claimInvitationForExistingUser validates a pending invitation and grants the
// user a membership in the inviting org. Unlike the signup claim helpers,
// there is no user creation: the caller is already authenticated and this
// transaction only accepts the invitation and inserts/updates the membership.
//
// The (email, githubLogin) pair must match the invitation per the same rules
// as the signup flow; a mismatch returns an invitationError so handlers can
// map to an HTTP status. The invitation is accepted inside the same tx as the
// membership upsert, so a race on status will roll the membership back.
func (h *AuthHandler) claimInvitationForExistingUser(
	ctx context.Context,
	token, userEmail, githubLogin string,
	userID uuid.UUID,
) (*models.Invitation, *invitationError, error) {
	if h.pool == nil {
		return nil, nil, fmt.Errorf("auth handler pool is not configured")
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin claim transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txInvitations := db.NewInvitationStore(tx)
	inv, _, role, invErr := h.validateInvitationWithStore(ctx, txInvitations, token, userEmail, githubLogin)
	if invErr != nil {
		return nil, invErr, nil
	}

	if err := txInvitations.Accept(ctx, inv.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &invitationError{
				status:  http.StatusGone,
				code:    "INVITE_INVALID",
				message: "this invitation is no longer valid",
			}, nil
		}
		return nil, nil, fmt.Errorf("accept invitation: %w", err)
	}

	if err := db.NewOrganizationMembershipStore(tx).Upsert(ctx, userID, inv.OrgID, role); err != nil {
		return nil, nil, fmt.Errorf("grant membership: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("commit claim transaction: %w", err)
	}
	return &inv, nil, nil
}

// claimPendingInvitationForExistingUser is the best-effort adaptor used by
// OAuth callback paths: when an existing user logs in with a pending
// invitation cookie, we try to grant them membership in the inviting org.
// Failures are logged but do not abort the auth flow — the user can still
// sign into their existing org and retry the invite later.
func (h *AuthHandler) claimPendingInvitationForExistingUser(
	r *http.Request,
	token, userEmail, githubLogin string,
	userID uuid.UUID,
) {
	if token == "" {
		return
	}
	_, invErr, err := h.claimInvitationForExistingUser(r.Context(), token, userEmail, githubLogin, userID)
	if err != nil {
		zerolog.Ctx(r.Context()).Warn().Err(err).Str("user_id", userID.String()).Msg("failed to claim pending invitation")
		return
	}
	if invErr != nil {
		zerolog.Ctx(r.Context()).Info().
			Str("code", invErr.code).
			Str("user_id", userID.String()).
			Msg("pending invitation claim rejected during oauth login")
	}
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
