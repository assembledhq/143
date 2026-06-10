package handlers

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// signupUserCreateFunc is the per-provider callback used by createSignupOrg to
// persist the user row inside the signup transaction. Each OAuth flow passes a
// closure that calls the appropriate UserStore method (CreateWithPassword /
// UpsertFromGitHub / UpsertFromGoogle) against the tx-scoped store.
type signupUserCreateFunc func(ctx context.Context, userStore *db.UserStore, user *models.User) error

// createSignupOrg atomically creates a fresh org, creates the signup user,
// grants them an admin membership, and issues their initial session token —
// all in one transaction. This is the primitive used by every "brand new
// user, no invitation" path (email register, GitHub first login, Google
// first login).
//
// Either all four rows are inserted or none are: if user or session creation
// fails, the org row rolls back rather than leaving an orphan or a user with
// no way to log in. The caller provides the user-creation step as a closure
// so the same helper serves all three flows without branching on provider.
// The returned sessionToken should be installed into the response cookies.
//
// orgName is treated as a display string, not a uniqueness key. Two users
// named "Alice" who sign up concurrently produce two orgs both named
// "Alice's Org"; the primary key (UUID) still disambiguates them in every
// downstream lookup, and the org switcher renders the (possibly duplicate)
// name alongside the id. Renaming is a user action post-signup rather than a
// constraint we try to enforce at creation time.
func (h *AuthHandler) createSignupOrg(
	ctx context.Context,
	orgName string,
	user *models.User,
	createUser signupUserCreateFunc,
) (string, error) {
	if h.pool == nil {
		return "", fmt.Errorf("auth handler pool is not configured")
	}
	if user == nil {
		return "", fmt.Errorf("user is required")
	}
	if createUser == nil {
		return "", fmt.Errorf("createUser callback is required")
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin signup transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	org := &models.Organization{
		Name:     orgName,
		Settings: json.RawMessage(`{}`),
	}
	if err := db.NewOrganizationStore(tx).Create(ctx, org); err != nil {
		return "", fmt.Errorf("create organization: %w", err)
	}

	// TODO(2026-04-25): drop user.OrgID / user.Role assignments once the
	// legacy single-org columns are removed from the users table. The
	// Insert call below is the authoritative record of this user's
	// membership; the users.org_id / users.role fields are only set here to
	// keep code that still reads them during the sunset period working.
	user.OrgID = org.ID
	user.Role = models.RoleAdmin
	if err := createUser(ctx, db.NewUserStore(tx), user); err != nil {
		return "", fmt.Errorf("create signup user: %w", err)
	}

	// Plain Insert (not GrantAtLeast): both the user and the org were just
	// created in this tx, so a conflict means something is wrong — we want a
	// loud failure rather than the silent no-op GrantAtLeast would return.
	if err := db.NewOrganizationMembershipStore(tx).Insert(ctx, user.ID, org.ID, models.RoleAdmin); err != nil {
		return "", fmt.Errorf("grant admin membership: %w", err)
	}

	sessionToken, err := h.persistSessionTx(ctx, tx, user)
	if err != nil {
		return "", fmt.Errorf("create signup session: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit signup transaction: %w", err)
	}
	return sessionToken, nil
}

// createAutoJoinUser atomically creates an OAuth signup user as a member of
// an existing org (matched via a verified auto-join email domain), records
// the provider's email attestation, and issues the initial session token.
// The domain-capture sibling of createSignupOrg: instead of minting a fresh
// org, the new user lands directly in their team's workspace.
//
// Callers must have already established BOTH halves of the trust chain:
// the org has DNS-verified the domain with auto-join enabled, and the OAuth
// provider attests the user's ownership of the email (so MarkEmailVerified
// inside the transaction is recording a fact, not granting one).
//
// The caller presets user.OrgID to the target org and user.Role to the
// auto-join role (legacy users-table columns, same sunset note as
// createSignupOrg); the GrantAtLeast below is the authoritative membership
// write.
func (h *AuthHandler) createAutoJoinUser(
	ctx context.Context,
	user *models.User,
	createUser signupUserCreateFunc,
) (string, error) {
	if h.pool == nil {
		return "", fmt.Errorf("auth handler pool is not configured")
	}
	if user == nil {
		return "", fmt.Errorf("user is required")
	}
	if createUser == nil {
		return "", fmt.Errorf("createUser callback is required")
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin auto-join signup transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txUserStore := db.NewUserStore(tx)
	if err := createUser(ctx, txUserStore, user); err != nil {
		return "", fmt.Errorf("create auto-join user: %w", err)
	}

	// GrantAtLeast (not Insert): the user row is an upsert for OAuth
	// providers, so an interrupted earlier signup attempt may already hold
	// a membership row — never downgrade it.
	effectiveRole, err := db.NewOrganizationMembershipStore(tx).GrantAtLeast(ctx, user.ID, user.OrgID, user.Role)
	if err != nil {
		return "", fmt.Errorf("grant auto-join membership: %w", err)
	}
	user.Role = models.Role(effectiveRole)

	if err := txUserStore.SetEmailVerification(ctx, user.ID, user.Email, true); err != nil {
		return "", fmt.Errorf("mark auto-join email verified: %w", err)
	}

	// Pin the joined org as the user's active-org preference before the
	// session row is written (persistSessionTx reads users.last_org_id).
	// Without this the first session would lean on the oldest-membership
	// fallback — correct for a single-membership user, but explicit is
	// deterministic regardless of what memberships the upsert path found.
	if err := txUserStore.UpdateLastOrgID(ctx, user.ID, &user.OrgID); err != nil {
		return "", fmt.Errorf("set auto-join last org: %w", err)
	}

	sessionToken, err := h.persistSessionTx(ctx, tx, user)
	if err != nil {
		return "", fmt.Errorf("create auto-join session: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit auto-join signup transaction: %w", err)
	}
	return sessionToken, nil
}
