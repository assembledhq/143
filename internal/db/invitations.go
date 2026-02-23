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
func (s *InvitationStore) Begin(ctx context.Context) (pgx.Tx, error) {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return nil, fmt.Errorf("invitation store db does not support transactions")
	}
	return txStarter.Begin(ctx)
}

const invitationSelectColumns = `id, org_id, email, role, invited_by, token, status, expires_at, created_at, accepted_at`

// Create inserts a new invitation and populates the generated fields on the input.
func (s *InvitationStore) Create(ctx context.Context, inv *models.Invitation) error {
	query := `INSERT INTO invitations (org_id, email, role, invited_by, token, expires_at)
		VALUES (@org_id, @email, @role, @invited_by, @token, @expires_at)
		RETURNING id, status, created_at`

	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":     inv.OrgID,
		"email":      inv.Email,
		"role":       inv.Role,
		"invited_by": inv.InvitedBy,
		"token":      inv.Token,
		"expires_at": inv.ExpiresAt,
	})
	return row.Scan(&inv.ID, &inv.Status, &inv.CreatedAt)
}

// GetByToken looks up an invitation by token regardless of status.
// The caller inspects the Status and ExpiresAt fields to return the correct error.
func (s *InvitationStore) GetByToken(ctx context.Context, token string) (models.Invitation, error) {
	query := fmt.Sprintf(`SELECT %s FROM invitations WHERE token = @token`, invitationSelectColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"token": token})
	if err != nil {
		return models.Invitation{}, fmt.Errorf("query invitation: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Invitation])
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
	query := `SELECT i.id, i.org_id, i.email, i.role, i.invited_by, i.token, i.status, i.expires_at, i.created_at, i.accepted_at,
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
