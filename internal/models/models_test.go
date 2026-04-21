package models

import "testing"

func TestRepository_IsActive(t *testing.T) {
	t.Parallel()

	cases := []struct {
		status string
		want   bool
	}{
		{RepositoryStatusActive, true},
		{RepositoryStatusDisconnected, false},
		{"", false},
		{"unknown", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.status, func(t *testing.T) {
			t.Parallel()
			repo := Repository{Status: tc.status}
			if got := repo.IsActive(); got != tc.want {
				t.Fatalf("IsActive() = %v, want %v (status %q)", got, tc.want, tc.status)
			}
		})
	}
}
