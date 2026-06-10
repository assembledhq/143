package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

var orgDomainColumns = []string{
	"id", "org_id", "domain", "verification_token", "status", "auto_join_enabled",
	"created_by", "created_at", "verified_at", "last_checked_at", "failed_checks",
}

func TestOrganizationDomainStore_Create(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationDomainStore(mock)
	orgID := uuid.New()
	createdBy := uuid.New()
	generatedID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("INSERT INTO organization_domains").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "status", "auto_join_enabled", "created_at"}).
			AddRow(generatedID, "pending", true, now))

	d := &models.OrganizationDomain{
		OrgID:             orgID,
		Domain:            "assembledhq.com",
		VerificationToken: "abc123",
		CreatedBy:         &createdBy,
	}
	require.NoError(t, store.Create(context.Background(), d))
	require.Equal(t, generatedID, d.ID)
	require.Equal(t, models.OrgDomainStatusPending, d.Status)
	require.True(t, d.AutoJoinEnabled)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationDomainStore_MarkVerified_ConflictPropagates(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationDomainStore(mock)

	mock.ExpectQuery("UPDATE organization_domains").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(&pgconn.PgError{Code: "23505", ConstraintName: "idx_org_domains_verified_domain"})

	_, err = store.MarkVerified(context.Background(), uuid.New(), uuid.New())
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "unique violation must propagate so the handler can return DOMAIN_CLAIMED")
	require.Equal(t, "23505", pgErr.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationDomainStore_FindAutoJoinOrgByDomain(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationDomainStore(mock)
	orgID := uuid.New()

	mock.ExpectQuery("SELECT d.org_id, o.name AS org_name, d.domain").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"org_id", "org_name", "domain"}).
			AddRow(orgID, "Assembled", "assembledhq.com"))

	got, err := store.FindAutoJoinOrgByDomain(context.Background(), "assembledhq.com")
	require.NoError(t, err)
	require.Equal(t, orgID, got.OrgID)
	require.Equal(t, "Assembled", got.OrgName)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationDomainStore_FindAutoJoinOrgByDomain_NoMatch(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationDomainStore(mock)

	mock.ExpectQuery("SELECT d.org_id, o.name AS org_name, d.domain").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"org_id", "org_name", "domain"}))

	_, err = store.FindAutoJoinOrgByDomain(context.Background(), "nobody.example")
	require.ErrorIs(t, err, pgx.ErrNoRows)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOrganizationDomainStore_ListByOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewOrganizationDomainStore(mock)
	orgID := uuid.New()
	now := time.Now()
	verifiedAt := now.Add(-time.Hour)

	mock.ExpectQuery("SELECT (.+) FROM organization_domains").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(orgDomainColumns).
			AddRow(uuid.New(), orgID, "assembledhq.com", "tok1", "verified", true, nil, now, &verifiedAt, &verifiedAt, 0).
			AddRow(uuid.New(), orgID, "pending.example", "tok2", "pending", true, nil, now, nil, nil, 0))

	rows, err := store.ListByOrg(context.Background(), orgID)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, models.OrgDomainStatusVerified, rows[0].Status)
	require.Equal(t, "assembledhq.com", rows[0].Domain)
	require.Nil(t, rows[1].VerifiedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}
