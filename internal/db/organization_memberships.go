package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/assembledhq/143/internal/models"
)

// mapLastAdminViolation returns ErrLastAdmin if err is a PgError raised by the
// enforce_last_admin trigger (migration 000082), otherwise err unchanged. The
// *Guarded variants catch the condition in Go with in-tx admin-row locks, but
// any non-guarded write can still trip the trigger at COMMIT time since it is
// DEFERRABLE INITIALLY DEFERRED; funnelling both paths through this mapper
// lets the handler layer surface a single sentinel regardless of which layer
// actually caught the violation.
func mapLastAdminViolation(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.CheckViolation && pgErr.ConstraintName == "enforce_last_admin" {
		return ErrLastAdmin
	}
	return err
}

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
// ordered by (created_at, org_id) so the oldest membership consistently wins
// when the middleware falls back to "first available" — the org_id tiebreak
// keeps the order deterministic for rows whose created_at is identical (the
// backfill migration inserts one per existing user at now(), so multi-org
// users promoted during backfill would otherwise see switcher order flip
// between pageloads).
//
// lint:allow-no-orgid reason="user-scoped lookup — memberships are the authoritative org set, not a target-org query"
func (s *OrganizationMembershipStore) ListByUser(ctx context.Context, userID uuid.UUID) ([]models.MembershipSummary, error) {
	query := `
		SELECT m.org_id, o.name AS org_name, m.role
		FROM organization_memberships m
		JOIN organizations o ON o.id = m.org_id
		WHERE m.user_id = @user_id
		ORDER BY m.created_at ASC, m.org_id ASC`
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

// Insert adds a new membership row. Unlike GrantAtLeast, this fails loudly on
// a (user_id, org_id) conflict. Use it from signup paths where both the user
// and the org were just inserted in the same transaction: there cannot
// legitimately be a pre-existing membership, so ON CONFLICT DO NOTHING (or
// GrantAtLeast's silent no-op) would mask a real bug. Invitation acceptance
// and role-upgrade paths should use GrantAtLeast instead.
func (s *OrganizationMembershipStore) Insert(ctx context.Context, userID, orgID uuid.UUID, role string) error {
	if !models.IsValidRole(role) {
		return fmt.Errorf("invalid role %q", role)
	}
	_, err := s.db.Exec(ctx,
		`INSERT INTO organization_memberships (user_id, org_id, role)
		 VALUES (@user_id, @org_id, @role)`,
		pgx.NamedArgs{
			"user_id": userID,
			"org_id":  orgID,
			"role":    role,
		})
	return err
}

// GrantAtLeast inserts a membership or, if one already exists, upgrades the
// role to the requested level — never downgrades. The name reflects the
// semantics: after this call the user has *at least* the requested role.
// Returns the effective role after the write so callers can echo the actual
// granted role back to the client (e.g. an admin re-invited as viewer stays
// admin, and the response should say so rather than parroting the invite's
// role).
//
// This is the correct primitive for invitation acceptance: an idempotent
// re-accept (e.g. a double-clicked link) must not downgrade an admin back to
// the invite's original role, but a pending invite that legitimately upgrades
// an existing member should apply. Privilege ordering is admin > member >
// builder > viewer; a request for a lower-or-equal role is a no-op at the DB
// level but the returned role reflects the row that now exists.
//
// Use UpdateRole / UpdateRoleGuarded for explicit role changes (including
// demotions) — those paths enforce the last-admin invariant, GrantAtLeast
// does not because it can never demote.
func (s *OrganizationMembershipStore) GrantAtLeast(ctx context.Context, userID, orgID uuid.UUID, role string) (string, error) {
	if !models.IsValidRole(role) {
		return "", fmt.Errorf("invalid role %q", role)
	}
	query := `
		INSERT INTO organization_memberships (user_id, org_id, role)
		VALUES (@user_id, @org_id, @role)
		ON CONFLICT (user_id, org_id) DO UPDATE
		SET role = CASE
			WHEN EXCLUDED.role = 'admin' THEN 'admin'
			WHEN EXCLUDED.role = 'member' AND organization_memberships.role IN ('builder', 'viewer') THEN 'member'
			WHEN EXCLUDED.role = 'builder' AND organization_memberships.role = 'viewer' THEN 'builder'
			ELSE organization_memberships.role
		END
		RETURNING role`
	var effective string
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"user_id": userID,
		"org_id":  orgID,
		"role":    role,
	}).Scan(&effective)
	if err != nil {
		return "", err
	}
	return effective, nil
}

// UpdateRole changes the user's role within a specific org. Returns
// pgx.ErrNoRows if no membership exists for that (user, org) pair so callers
// can surface a 404. If the change would demote the last admin the
// enforce_last_admin trigger rejects the update; that is translated to
// ErrLastAdmin so callers get the same sentinel they'd see from
// UpdateRoleGuarded.
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
		return mapLastAdminViolation(err)
	}
	if ct.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// Remove deletes the membership row for the (user, org) pair. The user row
// itself is preserved because it may still hold memberships in other orgs.
// Returns pgx.ErrNoRows if no such membership exists, or ErrLastAdmin if the
// enforce_last_admin trigger rejects the delete (non-guarded callers should
// prefer RemoveGuarded for a readable pre-check, but the sentinel is the same).
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
//
// The legacy auth_sessions.org_id column is intentionally NOT touched: it is
// NOT NULL in the schema (migration 000001) so we cannot clear it in place,
// and the middleware no longer reads it for authorization — request-time org
// resolution flows through last_org_id / header / oldest-membership. The
// column is scheduled for drop shortly after the sunset window (see the
// TODO at auth.go persistSessionTx) so carrying a stale value for the
// handful of requests between removal and session touch is harmless;
// deleting the session instead would regress the "graceful fallback to
// another membership" behaviour this PR adds.
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
		),
		cleared_user_hint AS (
			UPDATE users
			SET last_org_id = NULL
			WHERE id = @user_id
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
		return mapLastAdminViolation(err)
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
//
// The CTE shape is deliberate: FOR UPDATE must run against the base table so
// the row locks are taken, and COUNT(*) OVER () on the CTE gives us the count
// in the same round trip. A plain `SELECT COUNT(*) ... FOR UPDATE` is not
// valid (FOR UPDATE with aggregates is rejected), and reading rows back into
// Go just to count them wastes a round-trip. LIMIT 1 on the outer select
// keeps the result set a single row regardless of admin fan-out.
func (s *OrganizationMembershipStore) lockAdminCount(ctx context.Context, tx pgx.Tx, orgID uuid.UUID) (int, error) {
	var count int
	err := tx.QueryRow(ctx, `
		WITH locked AS (
			SELECT 1 AS hit
			FROM organization_memberships
			WHERE org_id = @org_id AND role = 'admin'
			FOR UPDATE
		)
		SELECT COALESCE((SELECT COUNT(*) FROM locked), 0)`,
		pgx.NamedArgs{"org_id": orgID}).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("lock admin rows: %w", err)
	}
	return count, nil
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

	// Commit can still surface the enforce_last_admin trigger: it is DEFERRABLE
	// INITIALLY DEFERRED so the check runs at COMMIT. The in-tx lockAdminCount
	// guard should make this unreachable, but mapping keeps the sentinel
	// consistent if anything ever slips past the application check.
	if err := tx.Commit(ctx); err != nil {
		return "", mapLastAdminViolation(fmt.Errorf("commit tx: %w", err))
	}
	return prevRole, nil
}

// RemoveGuarded deletes the membership inside a transaction that locks every
// admin in the org, so the same TOCTOU race that UpdateRoleGuarded prevents
// for demotions is also prevented for removals.
//
// Returns the previous role and the number of pending invitations the removal
// revoked (the underlying Remove CTE flips status='pending' → 'revoked' for
// any invitations the user authored in this org, and the team handler
// surfaces that count in the audit event so the status flips are
// reconstructible from audit alone). Returns ErrLastAdmin if removing the
// member would leave the org with no admins, or pgx.ErrNoRows if the
// membership does not exist.
func (s *OrganizationMembershipStore) RemoveGuarded(ctx context.Context, userID, orgID uuid.UUID) (string, int, error) {
	if s.pool == nil {
		return "", 0, fmt.Errorf("RemoveGuarded requires a pool-backed store")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	adminCount, err := s.lockAdminCount(ctx, tx, orgID)
	if err != nil {
		return "", 0, err
	}

	var prevRole string
	err = tx.QueryRow(ctx, `
		SELECT role FROM organization_memberships
		WHERE user_id = @user_id AND org_id = @org_id
		FOR UPDATE`,
		pgx.NamedArgs{"user_id": userID, "org_id": orgID}).Scan(&prevRole)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", 0, pgx.ErrNoRows
		}
		return "", 0, fmt.Errorf("read membership: %w", err)
	}

	if prevRole == "admin" && adminCount <= 1 {
		return prevRole, 0, ErrLastAdmin
	}

	// Snapshot the pending-invitations-authored-by-this-user count inside
	// the same tx. The Remove CTE below revokes exactly this set. A concurrent
	// CreateInvitation by the same user could theoretically race, but every
	// mutating team path (including CreateInvitation) requires admin role and
	// would have to come from *this* same user — who is about to be removed;
	// the count is an audit-quality snapshot, not a load-bearing invariant.
	var revokedInvitationCount int
	err = tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM invitations
		WHERE org_id = @org_id AND invited_by = @user_id AND status = 'pending'`,
		pgx.NamedArgs{"user_id": userID, "org_id": orgID}).Scan(&revokedInvitationCount)
	if err != nil {
		return "", 0, fmt.Errorf("count pending invitations: %w", err)
	}

	if err := NewOrganizationMembershipStore(tx).Remove(ctx, userID, orgID); err != nil {
		return "", 0, err
	}

	// See UpdateRoleGuarded for why commit errors are mapped: the trigger is
	// DEFERRED and fires at COMMIT, so a last-admin violation that the in-tx
	// check somehow misses would otherwise surface here as a raw PgError.
	if err := tx.Commit(ctx); err != nil {
		return "", 0, mapLastAdminViolation(fmt.Errorf("commit tx: %w", err))
	}
	return prevRole, revokedInvitationCount, nil
}

// ListUserIDsByOrg returns just the user_id set for an org, ordered by when
// each member joined. The team handler joins this against the users table to
// render the member directory. We return raw IDs rather than a joined row
// shape so this store stays focused on the membership graph — user display
// fields belong to the user store.
//
// user_id is the deterministic tiebreak: the backfill migration inserts every
// pre-existing row at now(), so a large org's member list would otherwise flip
// order between requests for any rows sharing a created_at. The resulting page
// flicker on the team page is user-visible, and tests that assert a stable
// order would fail intermittently.
func (s *OrganizationMembershipStore) ListUserIDsByOrg(ctx context.Context, orgID uuid.UUID) ([]uuid.UUID, error) {
	query := `
		SELECT user_id
		FROM organization_memberships
		WHERE org_id = @org_id
		ORDER BY created_at ASC, user_id ASC`
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
