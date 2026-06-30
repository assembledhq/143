package preview

import "testing"

func TestSameOrigin(t *testing.T) {
	t.Parallel()

	const preview = "https://abc.preview.143.dev/"

	cases := []struct {
		name      string
		rawURL    string
		expected  string
		wantMatch bool
	}{
		{"exact origin root", "https://abc.preview.143.dev/", preview, true},
		{"same origin with path", "https://abc.preview.143.dev/dashboard?q=1", preview, true},
		{"same origin no trailing slash", "https://abc.preview.143.dev", preview, true},

		// The bypasses a prefix check would have allowed.
		{"suffix attack subdomain", "https://abc.preview.143.dev.evil.com/", preview, false},
		{"userinfo attack", "https://abc.preview.143.dev@evil.com/", preview, false},

		// Other mismatches.
		{"different host", "https://evil.com/", preview, false},
		{"different scheme", "http://abc.preview.143.dev/", preview, false},
		{"different port", "https://abc.preview.143.dev:8443/", preview, false},
		{"unparseable target", "://not a url", preview, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := sameOrigin(tc.rawURL, tc.expected); got != tc.wantMatch {
				t.Errorf("sameOrigin(%q, %q) = %v, want %v", tc.rawURL, tc.expected, got, tc.wantMatch)
			}
		})
	}
}
