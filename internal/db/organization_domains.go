package db

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

// OrganizationDomainStore manages the verified-email-domain rows used for
// domain capture / auto-join.
type OrganizationDomainStore struct {
	db DBTX
}

func NewOrganizationDomainStore(db DBTX) *OrganizationDomainStore {
	return &OrganizationDomainStore{db: db}
}

const orgDomainSelectColumns = `id, org_id, domain, verification_token, status, auto_join_enabled, created_by, created_at, verified_at, last_checked_at, failed_checks`

// Create inserts a pending domain claim. Conflicts on (org_id, domain)
// bubble up as unique-violation errors for the handler to translate.
func (s *OrganizationDomainStore) Create(ctx context.Context, d *models.OrganizationDomain) error {
	query := `
		INSERT INTO organization_domains (org_id, domain, verification_token, created_by)
		VALUES (@org_id, @domain, @verification_token, @created_by)
		RETURNING id, status, auto_join_enabled, created_at`
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":             d.OrgID,
		"domain":             d.Domain,
		"verification_token": d.VerificationToken,
		"created_by":         d.CreatedBy,
	})
	return row.Scan(&d.ID, &d.Status, &d.AutoJoinEnabled, &d.CreatedAt)
}

// ListByOrg returns all domain claims for the org, verified first, then by
// recency, so the settings page renders the load-bearing rows on top.
func (s *OrganizationDomainStore) ListByOrg(ctx context.Context, orgID uuid.UUID) ([]models.OrganizationDomain, error) {
	query := fmt.Sprintf(`
		SELECT %s FROM organization_domains
		WHERE org_id = @org_id
		ORDER BY (status = 'verified') DESC, created_at DESC`, orgDomainSelectColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("query organization domains: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.OrganizationDomain])
}

// GetByID returns a single org-scoped domain row.
func (s *OrganizationDomainStore) GetByID(ctx context.Context, orgID, id uuid.UUID) (models.OrganizationDomain, error) {
	query := fmt.Sprintf(`
		SELECT %s FROM organization_domains
		WHERE id = @id AND org_id = @org_id`, orgDomainSelectColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"id": id, "org_id": orgID})
	if err != nil {
		return models.OrganizationDomain{}, fmt.Errorf("query organization domain: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.OrganizationDomain])
}

// MarkVerified flips a pending row to verified and stamps the timestamps.
// The partial unique index on verified domains makes this fail with a
// unique violation when another org already verified the same domain.
// Returns pgx.ErrNoRows when the row doesn't exist in this org.
// failed_checks resets: a manual verify is by definition a successful check.
func (s *OrganizationDomainStore) MarkVerified(ctx context.Context, orgID, id uuid.UUID) (models.OrganizationDomain, error) {
	query := fmt.Sprintf(`
		UPDATE organization_domains
		SET status = 'verified',
		    verified_at = COALESCE(verified_at, now()),
		    last_checked_at = now(),
		    failed_checks = 0
		WHERE id = @id AND org_id = @org_id
		RETURNING %s`, orgDomainSelectColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"id": id, "org_id": orgID})
	if err != nil {
		return models.OrganizationDomain{}, fmt.Errorf("mark domain verified: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.OrganizationDomain])
}

// TouchChecked records a verification attempt that did not succeed, so the
// admin UI can show "last checked" even for still-pending rows.
func (s *OrganizationDomainStore) TouchChecked(ctx context.Context, orgID, id uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`UPDATE organization_domains SET last_checked_at = now() WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": id, "org_id": orgID})
	return err
}

// SetAutoJoin toggles auto-join for a domain. Returns pgx.ErrNoRows when the
// row doesn't exist in this org.
func (s *OrganizationDomainStore) SetAutoJoin(ctx context.Context, orgID, id uuid.UUID, enabled bool) (models.OrganizationDomain, error) {
	query := fmt.Sprintf(`
		UPDATE organization_domains
		SET auto_join_enabled = @enabled
		WHERE id = @id AND org_id = @org_id
		RETURNING %s`, orgDomainSelectColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"id": id, "org_id": orgID, "enabled": enabled})
	if err != nil {
		return models.OrganizationDomain{}, fmt.Errorf("set domain auto-join: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.OrganizationDomain])
}

// Delete removes a domain claim. Returns pgx.ErrNoRows when absent.
func (s *OrganizationDomainStore) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	ct, err := s.db.Exec(ctx,
		`DELETE FROM organization_domains WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": id, "org_id": orgID})
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// FindAutoJoinOrgByDomain returns the org that has verified the given email
// domain with auto-join enabled, or pgx.ErrNoRows. At most one row can
// match thanks to the partial unique index on verified domains.
//
// lint:allow-no-orgid reason="pre-membership signup lookup; the result IS the org"
func (s *OrganizationDomainStore) FindAutoJoinOrgByDomain(ctx context.Context, domain string) (models.JoinableOrganization, error) {
	query := `
		SELECT d.org_id, o.name AS org_name, d.domain
		FROM organization_domains d
		JOIN organizations o ON o.id = d.org_id
		WHERE d.domain = @domain
		  AND d.status = 'verified'
		  AND d.auto_join_enabled`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"domain": domain})
	if err != nil {
		return models.JoinableOrganization{}, fmt.Errorf("query auto-join org for domain: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.JoinableOrganization])
}

// CountByOrg returns how many domain claims (any status) the org holds.
func (s *OrganizationDomainStore) CountByOrg(ctx context.Context, orgID uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM organization_domains WHERE org_id = @org_id`,
		pgx.NamedArgs{"org_id": orgID}).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count organization domains: %w", err)
	}
	return n, nil
}

// AutoJoinDomainExists reports whether ANY org has verified the given
// domain with auto-join enabled, without revealing which. Used to tell an
// unverified-email user "verify your email to join your team" without
// leaking the org's identity before they prove address ownership.
//
// lint:allow-no-orgid reason="pre-membership existence probe; deliberately org-blind"
func (s *OrganizationDomainStore) AutoJoinDomainExists(ctx context.Context, domain string) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM organization_domains
			WHERE domain = @domain AND status = 'verified' AND auto_join_enabled
		)`, pgx.NamedArgs{"domain": domain}).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check auto-join domain existence: %w", err)
	}
	return exists, nil
}

// VerifiedDomainExists reports whether ANY org has verified the given
// domain (regardless of the auto-join toggle). Used for identity
// stickiness: an account email on a company-verified domain is treated as
// the user's canonical identity and survives profile-email churn.
//
// lint:allow-no-orgid reason="org-blind existence probe over verified company domains"
func (s *OrganizationDomainStore) VerifiedDomainExists(ctx context.Context, domain string) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM organization_domains
			WHERE domain = @domain AND status = 'verified'
		)`, pgx.NamedArgs{"domain": domain}).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check verified domain existence: %w", err)
	}
	return exists, nil
}

// ListVerifiedDueForRecheck returns up to limit verified domains whose last
// check is older than the cutoff (or never ran), oldest-checked first, for
// the daily DNS re-check sweep. The limit bounds DNS work per scheduler
// tick (each check is up to two lookups with 5s timeouts); the
// last_checked_at watermark drains any backlog across subsequent ticks.
//
// lint:allow-no-orgid reason="cross-org system sweep run by the leader-elected scheduler"
func (s *OrganizationDomainStore) ListVerifiedDueForRecheck(ctx context.Context, checkedBefore time.Time, limit int) ([]models.OrganizationDomain, error) {
	query := fmt.Sprintf(`
		SELECT %s FROM organization_domains
		WHERE status = 'verified'
		  AND (last_checked_at IS NULL OR last_checked_at < @checked_before)
		ORDER BY last_checked_at ASC NULLS FIRST
		LIMIT @limit`, orgDomainSelectColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"checked_before": checkedBefore, "limit": limit})
	if err != nil {
		return nil, fmt.Errorf("query domains due for recheck: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.OrganizationDomain])
}

// RecordRecheckSuccess stamps a passing sweep check and clears the failure
// streak. Deliberately does NOT re-enable auto-join: if the sweep turned it
// off, turning it back on is an admin decision, not a DNS observation.
//
// lint:allow-no-orgid reason="system sweep write keyed by globally unique row id"
func (s *OrganizationDomainStore) RecordRecheckSuccess(ctx context.Context, id uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`UPDATE organization_domains SET last_checked_at = now(), failed_checks = 0 WHERE id = @id`,
		pgx.NamedArgs{"id": id})
	return err
}

// RecordRecheckFailure increments the failure streak and, when it reaches
// maxFailures, atomically disables auto-join. Returns the new streak and
// whether THIS call performed the disable (so the caller emits exactly one
// audit event per trip, not one per subsequent failure).
//
// lint:allow-no-orgid reason="system sweep write keyed by globally unique row id"
func (s *OrganizationDomainStore) RecordRecheckFailure(ctx context.Context, id uuid.UUID, maxFailures int) (failedChecks int, disabled bool, err error) {
	// The `before` CTE reads the statement snapshot (pre-update values), so
	// `disabled` is true only when THIS update flipped auto_join_enabled —
	// a row an admin already disabled never reports a fresh trip.
	query := `
		WITH before AS (
			SELECT auto_join_enabled FROM organization_domains WHERE id = @id
		), updated AS (
			UPDATE organization_domains d
			SET last_checked_at = now(),
			    failed_checks = d.failed_checks + 1,
			    auto_join_enabled = d.auto_join_enabled AND (d.failed_checks + 1 < @max_failures)
			WHERE d.id = @id
			RETURNING d.failed_checks, d.auto_join_enabled
		)
		SELECT u.failed_checks, (b.auto_join_enabled AND NOT u.auto_join_enabled)
		FROM updated u, before b`
	err = s.db.QueryRow(ctx, query, pgx.NamedArgs{"id": id, "max_failures": maxFailures}).
		Scan(&failedChecks, &disabled)
	if err != nil {
		return 0, false, fmt.Errorf("record domain recheck failure: %w", err)
	}
	return failedChecks, disabled, nil
}

// ListJoinableForUser returns orgs the user could join via a verified
// auto-join domain matching their email domain, excluding orgs they already
// belong to. Callers must have already established that the user's email is
// provider-verified; this query only does the domain/membership math.
//
// lint:allow-no-orgid reason="user-scoped cross-org discovery query"
func (s *OrganizationDomainStore) ListJoinableForUser(ctx context.Context, userID uuid.UUID, emailDomain string) ([]models.JoinableOrganization, error) {
	query := `
		SELECT d.org_id, o.name AS org_name, d.domain
		FROM organization_domains d
		JOIN organizations o ON o.id = d.org_id
		WHERE d.domain = @domain
		  AND d.status = 'verified'
		  AND d.auto_join_enabled
		  AND NOT EXISTS (
		      SELECT 1 FROM organization_memberships m
		      WHERE m.user_id = @user_id AND m.org_id = d.org_id
		  )`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"user_id": userID, "domain": emailDomain})
	if err != nil {
		return nil, fmt.Errorf("query joinable orgs for user: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.JoinableOrganization])
}
