package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompareCLIVersions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		a, b       string
		want       int
		comparable bool
	}{
		{"1.4.0", "1.4.0", 0, true},
		{"1.3.9", "1.4.0", -1, true},
		{"2.0", "1.9.9", 1, true},
		{"v1.4", "1.4.0", 0, true},
		{"1.4.1", "1.4", 1, true},
		{"dev", "1.0.0", 0, false},
		{"a1b2c3d", "1.0.0", 0, false}, // git SHA — never orderable
		{"1.0.0", "", 0, false},
	}
	for _, tc := range cases {
		got, comparable := compareCLIVersions(tc.a, tc.b)
		require.Equal(t, tc.comparable, comparable, "comparable(%q, %q)", tc.a, tc.b)
		if comparable {
			require.Equal(t, tc.want, got, "compare(%q, %q)", tc.a, tc.b)
		}
	}
}

func TestCLIVersionGate(t *testing.T) {
	t.Parallel()

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	cases := []struct {
		name         string
		minSupported string
		userAgent    string
		wantStatus   int
	}{
		{"stale CLI blocked", "1.4.0", "143-tools/1.3.0", http.StatusUpgradeRequired},
		{"current CLI passes", "1.4.0", "143-tools/1.4.0", http.StatusOK},
		{"newer CLI passes", "1.4.0", "143-tools/2.0.0", http.StatusOK},
		{"sha-versioned CLI never blocked", "1.4.0", "143-tools/a1b2c3d", http.StatusOK},
		{"dev CLI never blocked", "1.4.0", "143-tools/dev", http.StatusOK},
		{"non-CLI user agent untouched", "1.4.0", "Mozilla/5.0", http.StatusOK},
		{"enforcement off by default", "", "143-tools/0.0.1", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
			req.Header.Set("User-Agent", tc.userAgent)
			CLIVersionGate(tc.minSupported)(next).ServeHTTP(rec, req)
			require.Equal(t, tc.wantStatus, rec.Code)
			if tc.wantStatus == http.StatusUpgradeRequired {
				require.Contains(t, rec.Body.String(), CLIUpdateRequiredCode)
				require.Contains(t, rec.Body.String(), "143-tools update", "the 426 must name the fix")
			}
		})
	}
}
