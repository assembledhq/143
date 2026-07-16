package gateway

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeAppOrigins(t *testing.T) {
	t.Parallel()

	origins := normalizeAppOrigins("https://143.dev/", []string{
		"https://canary.143.dev",
		"https://143.dev", // duplicate of primary after trim
		"",
		"  ",
	})
	require.Equal(t, []string{"https://143.dev", "https://canary.143.dev"}, origins,
		"should keep primary first, trim trailing slashes, drop empties and duplicates")
}

func TestNewGateway_CSPFrameAncestorsIncludesAllAppOrigins(t *testing.T) {
	t.Parallel()

	gw := NewGateway(GatewayConfig{
		AppOrigin:            "https://143.dev",
		AdditionalAppOrigins: []string{"https://canary.143.dev"},
	})
	require.Contains(t, gw.cspHeader, "frame-ancestors https://143.dev https://canary.143.dev",
		"both planes' frontends must be allowed to embed previews")
}

func TestServeBootstrapPage_CarriesFullOriginAllowList(t *testing.T) {
	t.Parallel()

	gw := NewGateway(GatewayConfig{
		AppOrigin:            "https://143.dev",
		AdditionalAppOrigins: []string{"https://canary.143.dev"},
	})

	rr := httptest.NewRecorder()
	gw.serveBootstrapPage(rr, httptest.NewRequest("GET", "/", nil))

	body := rr.Body.String()
	require.Contains(t, body, `["https://143.dev","https://canary.143.dev"]`,
		"the bootstrap page must accept the postMessage token handshake from either plane's frontend")
	require.Contains(t, body, "allowedAppOrigins.indexOf(event.origin)",
		"token messages must be origin-checked against the allow-list")
	require.Contains(t, body, "event.origin);",
		"replies must target the origin that actually sent the token, not a fixed origin")
}

func TestResolvedAppOrigin_PrimaryUnchangedByAdditionalOrigins(t *testing.T) {
	t.Parallel()

	gw := NewGateway(GatewayConfig{
		AppOrigin:            "https://143.dev",
		AdditionalAppOrigins: []string{"https://canary.143.dev"},
	})
	require.Equal(t, "https://143.dev", gw.resolvedAppOrigin(),
		"open-in-app links keep pointing at the primary (stable) origin")
}
