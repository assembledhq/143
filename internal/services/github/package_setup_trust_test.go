package github

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPackageSetupPullRequestTrustResponse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, raw string
		fail      bool
	}{
		{name: "same repository pinned head", raw: `{"head":{"sha":"pinned","repo":{"id":42,"full_name":"Acme/Web"}},"base":{"repo":{"id":42,"full_name":"acme/web"}}}`},
		{name: "fork", raw: `{"head":{"sha":"pinned","repo":{"id":99,"full_name":"outsider/web"}},"base":{"repo":{"id":42,"full_name":"acme/web"}}}`, fail: true},
		{name: "stale head", raw: `{"head":{"sha":"new","repo":{"id":42,"full_name":"acme/web"}},"base":{"repo":{"id":42,"full_name":"acme/web"}}}`, fail: true},
		{name: "unknown head repository", raw: `{"head":{"sha":"pinned"},"base":{"repo":{"id":42,"full_name":"acme/web"}}}`, fail: true},
		{name: "wrong scoped base", raw: `{"head":{"sha":"pinned","repo":{"id":42,"full_name":"acme/other"}},"base":{"repo":{"id":42,"full_name":"acme/other"}}}`, fail: true},
		{name: "malformed response", raw: `{`, fail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validatePackageSetupPullRequestResponse([]byte(tt.raw), "acme/web", "pinned")
			if tt.fail {
				require.Error(t, err, "unknown, forked or stale provider identity must deny package credentials")
			} else {
				require.NoError(t, err, "authoritative matching head and base repository should permit setup")
			}
		})
	}
}
