package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type InvitationStore struct {
	db DBTX
}

func NewInvitationStore(db DBTX) *InvitationStore {
	return &InvitationStore{db: db}
}

// Begin starts a transaction using the underlying DB handle.
// lint:allow-no-orgid reason="transaction helper; org scoping is enforced by the wrapped queries"
func (s *InvitationStore) Begin(ctx context.Context) (pgx.Tx, error) {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return nil, fmt.Errorf("invitation store db does not support transactions")
	}
	return txStarter.Begin(ctx)
}

const invitationSelectColumns = `id, org_id, email, github_username, acceptance_method, role, invited_by, token, status, expires_at, created_at, accepted_at`

// Create inserts a new invitation and populates the generated fields on the input.
func (s *InvitationStore) Create(ctx context.Context, inv *models.Invitation) error {
	if inv.AcceptanceMethod == "" {
		inv.AcceptanceMethod = models.InvitationAcceptanceMethodEither
	}

	query := `INSERT INTO invitations (org_id, email, github_username, acceptance_method, role, invited_by, token, expires_at)
		VALUES (@org_id, @email, @github_username, @acceptance_method, @role, @invited_by, @token, @expires_at)
		RETURNING id, status, created_at`

	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":            inv.OrgID,
		"email":             inv.Email,
		"github_username":   inv.GitHubUsername,
		"acceptance_method": inv.AcceptanceMethod,
		"role":              inv.Role,
		"invited_by":        inv.InvitedBy,
		"token":             inv.Token,
		"expires_at":        inv.ExpiresAt,
	})
	return row.Scan(&inv.ID, &inv.Status, &inv.CreatedAt)
}

// GetByToken looks up an invitation by token regardless of status.
// The caller inspects the Status and ExpiresAt fields to return the correct error.
// lint:allow-no-orgid reason="invite acceptance is pre-membership; token identifies the target org"
func (s *InvitationStore) GetByToken(ctx context.Context, token string) (models.Invitation, error) {
	query := fmt.Sprintf(`SELECT %s FROM invitations WHERE token = @token`, invitationSelectColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"token": token})
	if err != nil {
		return models.Invitation{}, fmt.Errorf("query invitation: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Invitation])
}

// GetByID looks up an invitation by id regardless of status. The caller is
// responsible for any status / expiry / recipient-match checks; this is the
// primary entry point for the invitee-side accept and decline routes, which
// authenticate the *user* via the session and then re-validate the invite
// against that user's email and github_login.
//
// lint:allow-no-orgid reason="invitation id is globally unique; org context comes from the row itself"
func (s *InvitationStore) GetByID(ctx context.Context, id uuid.UUID) (models.Invitation, error) {
	query := fmt.Sprintf(`SELECT %s FROM invitations WHERE id = @id`, invitationSelectColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"id": id})
	if err != nil {
		return models.Invitation{}, fmt.Errorf("query invitation by id: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Invitation])
}

// ListPendingForUser returns the active invitations addressed to the given
// user — those where the invitation's email or github_username matches and
// the user is not already a member of the target org. Used by the in-app
// pending-invitations surface (org switcher dot + dropdown section).
//
// Match semantics intentionally mirror validateInvitationWithStore in the
// auth handler: case-insensitive equality on either identifier, with empty
// inputs treated as no-match (an unauthenticated identifier cannot satisfy
// either branch). Drift between this query and the claim-time check would
// produce the worst-possible UX: an invitation visible in the dropdown that
// rejects on accept with INVITE_MISMATCH. Any change here needs a matching
// edit there.
//
// Dedupe by org: an admin can technically issue both an email-invite and a
// github-invite that resolve to the same person (the partial unique indexes
// only constrain duplicates of the *same* identifier, not cross-identifier
// duplicates for the same target). DISTINCT ON folds those down to one row
// per org so the UI doesn't show two "Acme [Accept]" rows for one human; the
// outer ORDER BY restores the most-recent-first display order.
//
// lint:allow-no-orgid reason="user-scoped query spanning all orgs the user is invited to"
func (s *InvitationStore) ListPendingForUser(ctx context.Context, userID uuid.UUID, email, githubLogin string) ([]models.PendingInvitationForUserRow, error) {
	query := `
		SELECT id, org_id, org_name, role, invited_by, inviter_name, expires_at, created_at
		FROM (
			SELECT DISTINCT ON (i.org_id)
				i.id, i.org_id, o.name AS org_name, i.role,
				i.invited_by, COALESCE(u.name, '') AS inviter_name,
				i.expires_at, i.created_at
			FROM invitations i
			JOIN organizations o ON o.id = i.org_id
			LEFT JOIN users u ON u.id = i.invited_by
			WHERE i.status = 'pending'
			  AND i.expires_at > now()
			  AND (
			      (i.acceptance_method = 'email'  AND i.email IS NOT NULL          AND @email        <> '' AND lower(i.email)          = lower(@email))
			   OR (i.acceptance_method = 'github' AND i.github_username IS NOT NULL AND @github_login <> '' AND lower(i.github_username) = lower(@github_login))
			   OR (i.acceptance_method = 'either' AND (
			          (i.email IS NOT NULL          AND @email        <> '' AND lower(i.email)          = lower(@email))
			       OR (i.github_username IS NOT NULL AND @github_login <> '' AND lower(i.github_username) = lower(@github_login))
			      ))
			  )
			  AND NOT EXISTS (
			      SELECT 1 FROM organization_memberships m
			      WHERE m.user_id = @user_id AND m.org_id = i.org_id
			  )
			ORDER BY i.org_id, i.created_at DESC
		) deduped
		ORDER BY created_at DESC`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"user_id":      userID,
		"email":        email,
		"github_login": githubLogin,
	})
	if err != nil {
		return nil, fmt.Errorf("query pending invitations for user: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PendingInvitationForUserRow])
}

// ListPendingByOrg returns all pending invitations for the org.
func (s *InvitationStore) ListPendingByOrg(ctx context.Context, orgID uuid.UUID) ([]models.Invitation, error) {
	query := fmt.Sprintf(`SELECT %s FROM invitations WHERE org_id = @org_id AND status = 'pending' ORDER BY created_at DESC`, invitationSelectColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("query invitations: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.Invitation])
}

// ListPendingByOrgWithInviter returns pending invitations with the inviter's name via JOIN.
func (s *InvitationStore) ListPendingByOrgWithInviter(ctx context.Context, orgID uuid.UUID) ([]models.InvitationWithInviter, error) {
	query := `SELECT i.id, i.org_id, i.email, i.github_username, i.acceptance_method, i.role, i.invited_by, i.token, i.status, i.expires_at, i.created_at, i.accepted_at,
		COALESCE(u.name, '') AS inviter_name
		FROM invitations i
		LEFT JOIN users u ON u.id = i.invited_by
		WHERE i.org_id = @org_id AND i.status = 'pending'
		ORDER BY i.created_at DESC`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("query invitations with inviter: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.InvitationWithInviter])
}

// Accept marks the invitation as accepted.
// lint:allow-no-orgid reason="token-based acceptance; invitation id is globally unique and already token-validated"
func (s *InvitationStore) Accept(ctx context.Context, id uuid.UUID) error {
	query := `UPDATE invitations SET status = 'accepted', accepted_at = now() WHERE id = @id AND status = 'pending'`
	ct, err := s.db.Exec(ctx, query, pgx.NamedArgs{"id": id})
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// Revoke marks the invitation as revoked. Returns pgx.ErrNoRows if not found or not pending.
func (s *InvitationStore) Revoke(ctx context.Context, orgID, id uuid.UUID) error {
	query := `UPDATE invitations SET status = 'revoked' WHERE id = @id AND org_id = @org_id AND status = 'pending'`
	ct, err := s.db.Exec(ctx, query, pgx.NamedArgs{"id": id, "org_id": orgID})
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
