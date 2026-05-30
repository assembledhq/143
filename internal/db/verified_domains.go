package db

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/net/publicsuffix"
)

type VerifiedDomainStore struct {
	db DBTX
}

func NewVerifiedDomainStore(db DBTX) *VerifiedDomainStore {
	return &VerifiedDomainStore{db: db}
}

const verifiedDomainSelectColumns = `id, org_id, domain, status, verification_token, verified_at, auto_join_enabled, auto_join_role, created_by, created_at, updated_at`

func NormalizeVerifiedDomain(domain string) (string, error) {
	normalized := strings.Trim(strings.ToLower(strings.TrimSpace(domain)), ".")
	if normalized == "" || strings.Contains(normalized, "@") || strings.Contains(normalized, "..") {
		return "", fmt.Errorf("invalid domain")
	}
	parts := strings.Split(normalized, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("domain must include a registrable suffix")
	}
	suffix, _ := publicsuffix.PublicSuffix(normalized)
	if suffix == normalized {
		return "", fmt.Errorf("domain must be registrable")
	}
	for _, part := range parts {
		if part == "" || len(part) > 63 || strings.HasPrefix(part, "-") || strings.HasSuffix(part, "-") {
			return "", fmt.Errorf("invalid domain label")
		}
	}
	return normalized, nil
}

func EmailDomain(email string) (string, error) {
	addr, err := mail.ParseAddress(strings.TrimSpace(email))
	if err != nil {
		return "", err
	}
	parts := strings.Split(addr.Address, "@")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid email")
	}
	return NormalizeVerifiedDomain(parts[1])
}

func (s *VerifiedDomainStore) Create(ctx context.Context, orgID uuid.UUID, domain *models.VerifiedDomain) error {
	if domain == nil {
		return fmt.Errorf("domain is required")
	}
	if err := domain.AutoJoinRole.Validate(); err != nil {
		return err
	}
	query := `
		INSERT INTO org_verified_domains (org_id, domain, verification_token, auto_join_enabled, auto_join_role, created_by)
		VALUES (@org_id, @domain, @verification_token, @auto_join_enabled, @auto_join_role, @created_by)
		RETURNING id, status, created_at, updated_at`
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":             orgID,
		"domain":             domain.Domain,
		"verification_token": domain.VerificationToken,
		"auto_join_enabled":  domain.AutoJoinEnabled,
		"auto_join_role":     domain.AutoJoinRole,
		"created_by":         domain.CreatedBy,
	}).Scan(&domain.ID, &domain.Status, &domain.CreatedAt, &domain.UpdatedAt)
	if err != nil {
		return err
	}
	domain.OrgID = orgID
	return nil
}

func (s *VerifiedDomainStore) ListByOrg(ctx context.Context, orgID uuid.UUID) ([]models.VerifiedDomain, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM org_verified_domains
		WHERE org_id = @org_id
		ORDER BY created_at DESC`, verifiedDomainSelectColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("query verified domains: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.VerifiedDomain])
}

func (s *VerifiedDomainStore) GetByID(ctx context.Context, orgID, id uuid.UUID) (models.VerifiedDomain, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM org_verified_domains
		WHERE org_id = @org_id AND id = @id`, verifiedDomainSelectColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "id": id})
	if err != nil {
		return models.VerifiedDomain{}, fmt.Errorf("query verified domain: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.VerifiedDomain])
}

func (s *VerifiedDomainStore) MarkVerified(ctx context.Context, orgID, id uuid.UUID) (models.VerifiedDomain, error) {
	query := fmt.Sprintf(`
		UPDATE org_verified_domains
		SET status = 'verified', verified_at = COALESCE(verified_at, now()), updated_at = now()
		WHERE org_id = @org_id AND id = @id
		RETURNING %s`, verifiedDomainSelectColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "id": id})
	if err != nil {
		return models.VerifiedDomain{}, fmt.Errorf("mark verified domain: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.VerifiedDomain])
}

func (s *VerifiedDomainStore) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM org_verified_domains WHERE org_id = @org_id AND id = @id`, pgx.NamedArgs{"org_id": orgID, "id": id})
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// FindVerifiedAutoJoinByEmailDomain resolves a globally verified domain from
// a provider-verified email address. The domain row's org_id is the tenant
// scope for the membership grant.
//
// lint:allow-no-orgid reason="pre-membership auto-join lookup by verified email domain"
func (s *VerifiedDomainStore) FindVerifiedAutoJoinByEmailDomain(ctx context.Context, email string) (models.VerifiedDomain, error) {
	domain, err := EmailDomain(email)
	if err != nil {
		return models.VerifiedDomain{}, err
	}
	query := fmt.Sprintf(`
		SELECT %s
		FROM org_verified_domains
		WHERE domain = @domain
		  AND status = 'verified'
		  AND auto_join_enabled = true`, verifiedDomainSelectColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"domain": domain})
	if err != nil {
		return models.VerifiedDomain{}, fmt.Errorf("query verified auto-join domain: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.VerifiedDomain])
}

func IsNoVerifiedDomainMatch(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

// GrantDomainMembership grants userID membership in the verified domain's org
// and persists that org as the user's next-login default. This is the write
// primitive used by OAuth login-time auto-join after the provider has proved
// the user owns the email address.
//
// lint:allow-no-orgid reason="org_id comes from a verified domain row selected by provider-verified email domain"
func (s *VerifiedDomainStore) GrantDomainMembership(ctx context.Context, userID uuid.UUID, domain models.VerifiedDomain) error {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return fmt.Errorf("verified domain store db does not support transactions")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin domain auto-join transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := NewOrganizationMembershipStore(tx).GrantAtLeast(ctx, userID, domain.OrgID, domain.AutoJoinRole); err != nil {
		return fmt.Errorf("grant domain membership: %w", err)
	}
	if err := NewUserStore(tx).UpdateLastOrgID(ctx, userID, &domain.OrgID); err != nil {
		return fmt.Errorf("update last org for domain membership: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit domain auto-join transaction: %w", err)
	}
	return nil
}
