package models

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOrgJoinTokenStatus(t *testing.T) {
	t.Parallel()

	now := time.Now()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	three := 3

	cases := []struct {
		name  string
		token OrgJoinToken
		want  JoinTokenStatus
	}{
		{"fresh unlimited", OrgJoinToken{}, JoinTokenStatusActive},
		{"under max uses", OrgJoinToken{MaxUses: &three, UseCount: 2}, JoinTokenStatusActive},
		{"exhausted", OrgJoinToken{MaxUses: &three, UseCount: 3}, JoinTokenStatusExhausted},
		{"expired", OrgJoinToken{ExpiresAt: &past}, JoinTokenStatusExpired},
		{"not yet expired", OrgJoinToken{ExpiresAt: &future}, JoinTokenStatusActive},
		{"revoked", OrgJoinToken{RevokedAt: &past}, JoinTokenStatusRevoked},
		{"revoked wins over expired", OrgJoinToken{RevokedAt: &past, ExpiresAt: &past}, JoinTokenStatusRevoked},
		{"expired wins over exhausted", OrgJoinToken{ExpiresAt: &past, MaxUses: &three, UseCount: 3}, JoinTokenStatusExpired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, tc.token.Status(now))
		})
	}
}
