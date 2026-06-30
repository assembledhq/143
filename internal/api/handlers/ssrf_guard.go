package handlers

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// SSRF (server-side request forgery) protection for outbound requests whose
// target URL is supplied by a user. Trusted, hard-coded integration hosts
// (GitHub, Slack, Notion, etc.) do not need this; only paths that fetch a
// caller-provided URL should route through newSSRFSafeHTTPClient.

// blockedIPNetworks are CIDR ranges that server-side requests must never reach:
// loopback, RFC1918 private space, the cloud-metadata link-local range
// (169.254.169.254 lives here), carrier-grade NAT, IPv6 unique-local, and the
// Tailscale CGNAT range used for this fleet's internal mesh.
var blockedIPNetworks = func() []*net.IPNet {
	cidrs := []string{
		"127.0.0.0/8",    // IPv4 loopback
		"10.0.0.0/8",     // RFC1918
		"172.16.0.0/12",  // RFC1918
		"192.168.0.0/16", // RFC1918
		"169.254.0.0/16", // link-local (incl. cloud metadata 169.254.169.254)
		"100.64.0.0/10",  // CGNAT / Tailscale mesh
		"0.0.0.0/8",      // "this host"
		"::1/128",        // IPv6 loopback
		"fe80::/10",      // IPv6 link-local
		"fc00::/7",       // IPv6 unique-local
		"ff00::/8",       // IPv6 multicast
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(fmt.Sprintf("ssrf_guard: bad CIDR %q: %v", c, err))
		}
		nets = append(nets, n)
	}
	return nets
}()

// isBlockedIP reports whether ip falls in a range outbound user-driven requests
// must not reach. A nil/unspecified/multicast address is also blocked.
func isBlockedIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLoopback() {
		return true
	}
	for _, n := range blockedIPNetworks {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// validatePublicHTTPURL parses raw and returns an error unless it is an absolute
// http(s) URL with a host. If the host is an IP literal, it is checked against
// the blocklist immediately so we can reject with a clean 4xx before dialing.
// Hostnames are not resolved here (that happens at dial time, which is DNS-rebind
// safe) — this is a cheap up-front filter, not the primary control.
func validatePublicHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("URL scheme must be http or https")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL must include a host")
	}
	if ip := net.ParseIP(host); ip != nil && isBlockedIP(ip) {
		return fmt.Errorf("URL host is a non-public address")
	}
	return nil
}

// ssrfSafeDialControl is a net.Dialer Control hook. It runs after DNS resolution
// on the concrete address being dialed, so it defeats DNS rebinding and is
// re-applied on every redirect hop.
func ssrfSafeDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("ssrf guard: malformed dial address %q", address)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("ssrf guard: unresolved dial address %q", host)
	}
	if isBlockedIP(ip) {
		return fmt.Errorf("ssrf guard: refusing to connect to non-public address %s", ip)
	}
	return nil
}

// ssrfSafeCheckRedirect re-validates the scheme of each redirect hop and caps
// the chain. The dial-time control already blocks redirects to private targets;
// this additionally refuses a redirect that switches to a non-http(s) scheme.
func ssrfSafeCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("ssrf guard: stopped after 10 redirects")
	}
	if s := strings.ToLower(req.URL.Scheme); s != "http" && s != "https" {
		return fmt.Errorf("ssrf guard: refusing redirect to scheme %q", req.URL.Scheme)
	}
	return nil
}

// newSSRFSafeHTTPClient returns an *http.Client for fetching user-supplied URLs.
// It blocks connections to private/loopback/link-local/metadata addresses at
// dial time (DNS-rebind safe), re-validates each redirect hop's scheme, and
// caps redirects. timeout bounds the whole request.
func newSSRFSafeHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   ssrfSafeDialControl,
	}
	transport := &http.Transport{
		// Proxy is deliberately nil. An HTTP(S)_PROXY would make the dialer
		// connect to the proxy (a public address that passes the dial-time
		// control) while the real, possibly-private target host is sent to the
		// proxy in the request — bypassing the SSRF guard entirely. We must dial
		// the target directly so ssrfSafeDialControl inspects its actual IP.
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &http.Client{
		Timeout:       timeout,
		Transport:     transport,
		CheckRedirect: ssrfSafeCheckRedirect,
	}
}
