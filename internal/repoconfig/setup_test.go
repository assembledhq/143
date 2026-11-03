package repoconfig

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNPMSetupConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, raw string
		want      []NPMSetup
		fail      bool
	}{
		{name: "root and standalone", raw: `{"setup":{"npm":[{"directory":"."},{"directory":"tools/widget","legacy_peer_deps":true}]}}`, want: []NPMSetup{{Directory: "."}, {Directory: "tools/widget", LegacyPeerDeps: true}}},
		{name: "traversal", raw: `{"setup":{"npm":[{"directory":"../other"}]}}`, fail: true},
		{name: "alias", raw: `{"setup":{"npm":[{"directory":"tools/../widget"}]}}`, fail: true},
		{name: "absolute", raw: `{"setup":{"npm":[{"directory":"/other"}]}}`, fail: true},
		{name: "managed", raw: `{"setup":{"npm":[{"directory":"packages/node_modules/widget"}]}}`, fail: true},
		{name: "duplicate", raw: `{"setup":{"npm":[{"directory":"."},{"directory":"."}]}}`, fail: true},
		{name: "empty", raw: `{"setup":{"npm":[{}]}}`, fail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := Parse([]byte(tt.raw))
			if tt.fail {
				require.Error(t, err, "unsafe setup declarations must fail validation")
				return
			}
			require.NoError(t, err, "typed npm setup should parse")
			require.Equal(t, tt.want, cfg.Setup.NPM, "root and separate package setup should preserve order and peer policy")
		})
	}
}
