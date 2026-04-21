package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

// ErrLastAdmin is returned by guarded role/removal operations when the
// requested change would leave the organization with no admin members.
var ErrLastAdmin = errors.New("organization would have no admins")

// OrganizationMembershipStore owns reads and writes against the
// organization_memberships join table. Every request-time authorization decision
// in the multi-org world funnels through one of these methods: the middleware
// calls ListByUser / Get to resolve the active membership, and team management
// handlers call Upsert / UpdateRole / Remove to reshape the user-org graph.
type OrganizationMembershipStore struct {
	db   DBTX
	pool TxStarter
}

// NewOrganizationMembershipStore constructs a store. When db is a TxStarter
// (i.e. a pgxpool.Pool), the *Guarded methods can open their own transactions
// to enforce last-admin invariants atomically. Tx-scoped stores constructed
// from a pgx.Tx don't have pool access, so guarded operations on those will
// error out — call them from the pool-backed store at the request boundary.
func NewOrganizationMembershipStore(db DBTX) *OrganizationMembershipStore {
	s := &OrganizationMembershipStore{db: db}
	if pool, ok := db.(TxStarter); ok {
		s.pool = pool
	}
	return s
}

const membershipSelectColumns = `user_id, org_id, role, created_at`

// ListByUser returns every membership the user holds, with the joined org name
// so the UI switcher can render without a second round-trip. Memberships are
// ordered by created_at so the oldest membership consistently wins when the
// middleware falls back to "first available" (keeping pre-multi-org behavior
// stable for single-membership users).
//
// lint:allow-no-orgid reason="user-scoped lookup — memberships are the authoritative org set, not a target-org query"
func (s *OrganizationMembershipStore) ListByUser(ctx context.Context, userID uuid.UUID) ([]models.MembershipSummary, error) {
	query := `
		SELECT m.org_id, o.name AS org_name, m.role
		FROM organization_memberships m
		JOIN organizations o ON o.id = m.org_id
		WHERE m.user_id = @user_id
		ORDER BY m.created_at ASC`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"user_id": userID})
	if err != nil {
		return nil, fmt.Errorf("query memberships: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.MembershipSummary])
}

// Get returns the (user, org) membership row, or pgx.ErrNoRows if the user has
// no membership in that org. Used by the auth middleware to validate that a
// user-asserted active org is actually one they belong to.
func (s *OrganizationMembershipStore) Get(ctx context.Context, userID, orgID uuid.UUID) (models.OrganizationMembership, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM organization_memberships
		WHERE user_id = @user_id AND org_id = @org_id`, membershipSelectColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"user_id": userID,
		"org_id":  orgID,
	})
	if err != nil {
		return models.OrganizationMembership{}, fmt.Errorf("query membership: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.OrganizationMembership])
}

// GrantAtLeast inserts a membership or, if one already exists, upgrades the
// role to the requested level — never downgrades. The name reflects the
// semantics: after this call the user has *at least* the requested role.
//
// This is the correct primitive for invitation acceptance: an idempotent
// re-accept (e.g. a double-clicked link) must not downgrade an admin back to
// the invite's original role, but a pending invite that legitimately upgrades
// an existing member should apply. Privilege ordering is admin > member >
// viewer; a request for a lower-or-equal role is a no-op.
//
// Use UpdateRole / UpdateRoleGuarded for explicit role changes (including
// demotions) — those paths enforce the last-admin invariant, GrantAtLeast
// does not because it can never demote.
func (s *OrganizationMembershipStore) GrantAtLeast(ctx context.Context, userID, orgID uuid.UUID, role string) error {
	if !models.IsValidRole(role) {
		return fmt.Errorf("invalid role %q", role)
	}
	query := `
		INSERT INTO organization_memberships (user_id, org_id, role)
		VALUES (@user_id, @org_id, @role)
		ON CONFLICT (user_id, org_id) DO UPDATE
		SET role = CASE
			WHEN EXCLUDED.role = 'admin' THEN 'admin'
			WHEN EXCLUDED.role = 'member' AND organization_memberships.role = 'viewer' THEN 'member'
			ELSE organization_memberships.role
		END`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"user_id": userID,
		"org_id":  orgID,
		"role":    role,
	})
	return err
}

// UpdateRole changes the user's role within a specific org. Returns
// pgx.ErrNoRows if no membership exists for that (user, org) pair so callers
// can surface a 404.
func (s *OrganizationMembershipStore) UpdateRole(ctx context.Context, userID, orgID uuid.UUID, role string) error {
	if !models.IsValidRole(role) {
		return fmt.Errorf("invalid role %q", role)
	}
	query := `
		UPDATE organization_memberships
		SET role = @role
		WHERE user_id = @user_id AND org_id = @org_id`
	ct, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"user_id": userID,
		"org_id":  orgID,
		"role":    role,
	})
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// Remove deletes the membership row for the (user, org) pair. The user row
// itself is preserved because it may still hold memberships in other orgs.
// Returns pgx.ErrNoRows if no such membership exists.
//
// Remove also clears org-scoped references to the user so removing a member
// cannot leave orphaned rows that mention a no-longer-member: pending
// invitations they sent in the org are revoked, session_questions they
// answered have answered_by nulled, and any auth_sessions hint pointing at
// the now-revoked org is cleared so the user's next request re-resolves to
// an org they're still in rather than bouncing off stale state. Accepted/
// historical invitations are preserved as an audit trail of real actions —
// the invited_by link survives so "who invited this person" is still
// answerable after the inviter leaves.
func (s *OrganizationMembershipStore) Remove(ctx context.Context, userID, orgID uuid.UUID) error {
	// Order matters: deleted_membership runs first and returns whether the
	// membership existed. The follow-on CTEs gate their WHERE clauses on that
	// result so we skip the side effects when nothing was actually removed.
	query := `
		WITH deleted_membership AS (
			DELETE FROM organization_memberships
			WHERE user_id = @user_id AND org_id = @org_id
			RETURNING 1
		),
		cleared_answers AS (
			UPDATE session_questions
			SET answered_by = NULL
			WHERE org_id = @org_id
			  AND answered_by = @user_id
			  AND EXISTS (SELECT 1 FROM deleted_membership)
		),
		revoked_invitations AS (
			UPDATE invitations
			SET status = 'revoked'
			WHERE org_id = @org_id
			  AND invited_by = @user_id
			  AND status = 'pending'
			  AND EXISTS (SELECT 1 FROM deleted_membership)
		),
		cleared_session_hints AS (
			UPDATE auth_sessions
			SET last_org_id = NULL
			WHERE user_id = @user_id
			  AND last_org_id = @org_id
			  AND EXISTS (SELECT 1 FROM deleted_membership)
		)
		SELECT EXISTS (SELECT 1 FROM deleted_membership)`

	var deleted bool
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"user_id": userID,
		"org_id":  orgID,
	}).Scan(&deleted)
	if err != nil {
		return err
	}
	if !deleted {
		return pgx.ErrNoRows
	}
	return nil
}

// CountForUser returns the number of memberships the user currently holds.
// Used after membership removal to decide whether the user still has any
// remaining orgs. A returned count of zero means the user has been
// effectively deactivated from the product — callers typically invalidate
// all their sessions in that case so their browser state doesn't linger
// pointing at orgs they can no longer access.
//
// lint:allow-no-orgid reason="user-scoped aggregate; membership set is the authoritative org list"
func (s *OrganizationMembershipStore) CountForUser(ctx context.Context, userID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM organization_memberships WHERE user_id = @user_id`,
		pgx.NamedArgs{"user_id": userID}).Scan(&count)
	return count, err
}

// CountAdmins returns the number of admin memberships for the given org.
// Used to enforce the "cannot remove or demote the last admin" invariant
// during role changes and membership deletions.
func (s *OrganizationMembershipStore) CountAdmins(ctx context.Context, orgID uuid.UUID) (int, error) {
	query := `SELECT COUNT(*) FROM organization_memberships WHERE org_id = @org_id AND role = 'admin'`
	var count int
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{"org_id": orgID}).Scan(&count)
	return count, err
}

// OldestForUser returns the user's earliest-joined membership, or pgx.ErrNoRows
// if the user has no memberships. This is the deterministic fallback used when
// neither the X-Active-Org-ID header nor the session's last_org_id points at a
// valid membership — single-membership users trivially get their only org,
// multi-membership users get a stable default that does not depend on insert
// order for the other memberships.
//
// lint:allow-no-orgid reason="user-scoped bootstrap lookup; picks the target org from the user's membership set"
func (s *OrganizationMembershipStore) OldestForUser(ctx context.Context, userID uuid.UUID) (models.OrganizationMembership, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM organization_memberships
		WHERE user_id = @user_id
		ORDER BY created_at ASC, org_id ASC
		LIMIT 1`, membershipSelectColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"user_id": userID})
	if err != nil {
		return models.OrganizationMembership{}, fmt.Errorf("query oldest membership: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.OrganizationMembership])
}

// lockAdminCount opens a per-org serialization point on the admin set: it
// SELECTs all admin rows for the org with FOR UPDATE so any concurrent tx
// trying to demote, remove, or insert-as-admin one of those rows blocks
// until this tx commits. Returns the locked admin count. Callers must own
// the surrounding transaction; the locks live for its duration.
func (s *OrganizationMembershipStore) lockAdminCount(ctx context.Context, tx pgx.Tx, orgID uuid.UUID) (int, error) {
	rows, err := tx.Query(ctx, `
		SELECT 1
		FROM organization_memberships
		WHERE org_id = @org_id AND role = 'admin'
		FOR UPDATE`, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return 0, fmt.Errorf("lock admin rows: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
	}
	return count, rows.Err()
}

// UpdateRoleGuarded changes a member's role inside a transaction that holds
// a row-level lock on every admin in the org. Two concurrent demotions of
// different admins serialize on those locks, so we cannot lose the last
// admin to a TOCTOU race.
//
// Returns the previous role on success. Returns ErrLastAdmin if the change
// would demote the only remaining admin, or pgx.ErrNoRows if the membership
// does not exist.
func (s *OrganizationMembershipStore) UpdateRoleGuarded(ctx context.Context, userID, orgID uuid.UUID, newRole string) (string, error) {
	if !models.IsValidRole(newRole) {
		return "", fmt.Errorf("invalid role %q", newRole)
	}
	if s.pool == nil {
		return "", fmt.Errorf("UpdateRoleGuarded requires a pool-backed store")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	adminCount, err := s.lockAdminCount(ctx, tx, orgID)
	if err != nil {
		return "", err
	}

	var prevRole string
	err = tx.QueryRow(ctx, `
		SELECT role FROM organization_memberships
		WHERE user_id = @user_id AND org_id = @org_id
		FOR UPDATE`,
		pgx.NamedArgs{"user_id": userID, "org_id": orgID}).Scan(&prevRole)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", pgx.ErrNoRows
		}
		return "", fmt.Errorf("read membership: %w", err)
	}

	if prevRole == "admin" && newRole != "admin" && adminCount <= 1 {
		return prevRole, ErrLastAdmin
	}

	if prevRole != newRole {
		if _, err := tx.Exec(ctx, `
			UPDATE organization_memberships
			SET role = @role
			WHERE user_id = @user_id AND org_id = @org_id`,
			pgx.NamedArgs{"user_id": userID, "org_id": orgID, "role": newRole}); err != nil {
			return "", fmt.Errorf("update role: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit tx: %w", err)
	}
	return prevRole, nil
}

// RemoveGuarded deletes the membership inside a transaction that locks every
// admin in the org, so the same TOCTOU race that UpdateRoleGuarded prevents
// for demotions is also prevented for removals.
//
// Returns the previous role on success. Returns ErrLastAdmin if removing the
// member would leave the org with no admins, or pgx.ErrNoRows if the
// membership does not exist. The org-scoped cleanups (cleared answers,
// deleted invitations) run inside the same tx via the underlying Remove CTE.
func (s *OrganizationMembershipStore) RemoveGuarded(ctx context.Context, userID, orgID uuid.UUID) (string, error) {
	if s.pool == nil {
		return "", fmt.Errorf("RemoveGuarded requires a pool-backed store")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	adminCount, err := s.lockAdminCount(ctx, tx, orgID)
	if err != nil {
		return "", err
	}

	var prevRole string
	err = tx.QueryRow(ctx, `
		SELECT role FROM organization_memberships
		WHERE user_id = @user_id AND org_id = @org_id
		FOR UPDATE`,
		pgx.NamedArgs{"user_id": userID, "org_id": orgID}).Scan(&prevRole)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", pgx.ErrNoRows
		}
		return "", fmt.Errorf("read membership: %w", err)
	}

	if prevRole == "admin" && adminCount <= 1 {
		return prevRole, ErrLastAdmin
	}

	if err := NewOrganizationMembershipStore(tx).Remove(ctx, userID, orgID); err != nil {
		return "", err
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit tx: %w", err)
	}
	return prevRole, nil
}

// ListUserIDsByOrg returns just the user_id set for an org, ordered by when
// each member joined. The team handler joins this against the users table to
// render the member directory. We return raw IDs rather than a joined row
// shape so this store stays focused on the membership graph — user display
// fields belong to the user store.
func (s *OrganizationMembershipStore) ListUserIDsByOrg(ctx context.Context, orgID uuid.UUID) ([]uuid.UUID, error) {
	query := `
		SELECT user_id
		FROM organization_memberships
		WHERE org_id = @org_id
		ORDER BY created_at ASC`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("query org member ids: %w", err)
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan member id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
