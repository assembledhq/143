package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var verifiedDomainColumns = []string{
	"id", "org_id", "domain", "status", "verification_token", "verified_at",
	"auto_join_enabled", "auto_join_role", "created_by", "created_at", "updated_at",
}

func TestVerifiedDomainStore_Create(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	userID := uuid.New()
	domainID := uuid.New()
	domain := &models.VerifiedDomain{
		OrgID:             orgID,
		Domain:            "example.com",
		VerificationToken: "token",
		AutoJoinEnabled:   true,
		AutoJoinRole:      models.RoleMember,
		CreatedBy:         userID,
	}

	mock.ExpectQuery("INSERT INTO org_verified_domains").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "status", "created_at", "updated_at"}).
			AddRow(domainID, models.VerifiedDomainStatusPending, now, now))

	err = NewVerifiedDomainStore(mock).Create(context.Background(), orgID, domain)
	require.NoError(t, err, "Create should persist a pending verified-domain row")
	require.Equal(t, domainID, domain.ID, "Create should set generated id")
	require.Equal(t, models.VerifiedDomainStatusPending, domain.Status, "Create should set pending status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestNormalizeVerifiedDomain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		expected  string
		expectErr bool
	}{
		{name: "normalizes case and trailing dot", input: "Example.COM.", expected: "example.com"},
		{name: "accepts registrable subdomain", input: "eng.example.co.uk", expected: "eng.example.co.uk"},
		{name: "rejects public suffix", input: "co.uk", expectErr: true},
		{name: "rejects single label", input: "localhost", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual, err := NormalizeVerifiedDomain(tt.input)
			if tt.expectErr {
				require.Error(t, err, "NormalizeVerifiedDomain should reject invalid domains")
				return
			}
			require.NoError(t, err, "NormalizeVerifiedDomain should accept registrable domains")
			require.Equal(t, tt.expected, actual, "NormalizeVerifiedDomain should return canonical domain")
		})
	}
}

func TestVerifiedDomainStore_FindVerifiedAutoJoinByEmailDomain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		email     string
		setupMock func(mock pgxmock.PgxPoolIface, now time.Time)
		expectErr bool
	}{
		{
			name:  "normalizes email domain and returns verified row",
			email: "New.User@Example.COM",
			setupMock: func(mock pgxmock.PgxPoolIface, now time.Time) {
				mock.ExpectQuery("SELECT .+ FROM org_verified_domains").
					WithArgs("example.com").
					WillReturnRows(pgxmock.NewRows(verifiedDomainColumns).
						AddRow(uuid.New(), uuid.New(), "example.com", models.VerifiedDomainStatusVerified, "token", &now, true, models.RoleMember, uuid.New(), now, now))
			},
		},
		{
			name:      "rejects malformed email",
			email:     "not-an-email",
			setupMock: func(mock pgxmock.PgxPoolIface, now time.Time) {},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should initialize")
			defer mock.Close()

			now := time.Now()
			tt.setupMock(mock, now)
			domain, err := NewVerifiedDomainStore(mock).FindVerifiedAutoJoinByEmailDomain(context.Background(), tt.email)
			if tt.expectErr {
				require.Error(t, err, "FindVerifiedAutoJoinByEmailDomain should reject malformed email")
				require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
				return
			}
			require.NoError(t, err, "FindVerifiedAutoJoinByEmailDomain should return a verified row")
			require.Equal(t, "example.com", domain.Domain, "lookup should use lower-cased email domain")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestVerifiedDomainStore_MarkVerifiedFiltersByOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	domainID := uuid.New()
	now := time.Now()
	mock.ExpectQuery("UPDATE org_verified_domains").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(verifiedDomainColumns).
			AddRow(domainID, orgID, "example.com", models.VerifiedDomainStatusVerified, "token", &now, true, models.RoleMember, uuid.New(), now, now))

	domain, err := NewVerifiedDomainStore(mock).MarkVerified(context.Background(), orgID, domainID)
	require.NoError(t, err, "MarkVerified should update a row scoped to the org")
	require.Equal(t, orgID, domain.OrgID, "MarkVerified should return the org-scoped row")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
