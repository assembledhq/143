//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
)

// insertUserWithEmails inserts a user with an explicit primary email, optional
// secondary_emails, and an explicit created_at so GetByOrgAndEmail tie-break
// ordering is deterministic and observable. Raw SQL (rather than a store
// helper) because nothing in UserStore lets a test backdate created_at or seed
// a secondary email without going through the OAuth/invite handlers.
func insertUserWithEmails(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, primary string, secondary []string, createdAt time.Time) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(context.Background(), `
		INSERT INTO users (org_id, email, name, role, secondary_emails, created_at)
		VALUES ($1, $2, $3, 'member', $4, $5)
		RETURNING id
	`, orgID, primary, "Test "+primary, secondary, createdAt).Scan(&id)
	require.NoError(t, err, "insert user with emails")
	return id
}

// TestGetByOrgAndEmail_PrimaryMatchWinsOverSecondaryCollision proves the
// priority tie-break in GetByOrgAndEmail against a real Postgres: nothing
// enforces that an address is unique across users within an org, so a lookup
// address can be one user's primary email and an *older* user's secondary
// email simultaneously. A plain `created_at ASC` would pick the older
// secondary-only match; the priority ordering must prefer the primary match.
func TestGetByOrgAndEmail_PrimaryMatchWinsOverSecondaryCollision(t *testing.T) {
	pool := setup(t)
	orgID := seedOrg(t, pool)

	base := time.Now().UTC().Truncate(time.Second)
	// Created earlier, matches only via its secondary_emails array.
	secondaryUser := insertUserWithEmails(t, pool, orgID,
		"personal@example.com", []string{"shared@example.com"}, base.Add(-time.Hour))
	// Created later, owns the address as its primary email.
	primaryUser := insertUserWithEmails(t, pool, orgID,
		"shared@example.com", nil, base)

	store := db.NewUserStore(pool)
	got, err := store.GetByOrgAndEmail(context.Background(), orgID, "Shared@Example.com")
	require.NoError(t, err, "lookup should resolve a matching org user")
	require.Equal(t, primaryUser, got.ID,
		"a primary-email match must win over an older user that only matches on a secondary email")
	require.NotEqual(t, secondaryUser, got.ID)
}

// TestGetByOrgAndEmail_DeterministicOnEqualPriority proves that when two users
// match at the same priority tier (both via a secondary-email collision), the
// resolution is stable across repeated lookups — the older row always wins —
// so creator attribution can never flap between two candidate users.
func TestGetByOrgAndEmail_DeterministicOnEqualPriority(t *testing.T) {
	pool := setup(t)
	orgID := seedOrg(t, pool)

	base := time.Now().UTC().Truncate(time.Second)
	older := insertUserWithEmails(t, pool, orgID,
		"a@example.com", []string{"shared@example.com"}, base.Add(-time.Hour))
	newer := insertUserWithEmails(t, pool, orgID,
		"b@example.com", []string{"shared@example.com"}, base)

	store := db.NewUserStore(pool)
	for i := 0; i < 3; i++ {
		got, err := store.GetByOrgAndEmail(context.Background(), orgID, "shared@example.com")
		require.NoError(t, err, "lookup should resolve a matching org user")
		require.Equal(t, older, got.ID,
			"equal-priority secondary collisions must tie-break on created_at so lookups are deterministic")
		require.NotEqual(t, newer, got.ID)
	}
}
