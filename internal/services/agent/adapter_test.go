package agent

import "testing"

func TestSlugForRepo(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"valid org/repo", "assembledhq/143", "143"},
		{"no slash returns empty", "norepo", ""},
		{"empty input returns empty", "", ""},
		{"empty repo portion returns empty", "org/", ""},
		{"nested slashes collapse to hyphens", "org/repo/sub", "repo-sub"},
		{"leading slash treats left as empty", "/repo", "repo"},
		// Path-traversal guards: even though GitHub's repo-name grammar
		// excludes these, SlugForRepo must never produce a slug that
		// resolves "/home/sandbox/<slug>" to a parent directory.
		{"dot component is rejected", "org/.", ""},
		{"dotdot component is rejected", "org/..", ""},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := SlugForRepo(tc.in)
			if got != tc.want {
				t.Fatalf("SlugForRepo(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
