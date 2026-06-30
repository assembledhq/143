package handlers

import (
	"net"
	"net/http"
	"net/url"
	"testing"
)

func TestIsBlockedIP(t *testing.T) {
	t.Parallel()

	cases := []struct {
		ip      string
		blocked bool
	}{
		// Public addresses are allowed.
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"93.184.216.34", false}, // example.com
		{"2606:4700:4700::1111", false},

		// Loopback.
		{"127.0.0.1", true},
		{"127.0.0.53", true},
		{"::1", true},

		// Cloud metadata / link-local.
		{"169.254.169.254", true},
		{"169.254.0.1", true},
		{"fe80::1", true},

		// RFC1918 private.
		{"10.0.0.3", true},
		{"172.16.5.4", true},
		{"192.168.1.1", true},

		// CGNAT / Tailscale mesh.
		{"100.64.0.1", true},
		{"100.127.255.255", true},

		// Unspecified / "this host".
		{"0.0.0.0", true},
		{"::", true},

		// IPv6 unique-local and multicast.
		{"fc00::1", true},
		{"fd12:3456::1", true},
		{"ff02::1", true},

		// IPv4-mapped IPv6 must not bypass the IPv4 blocklist.
		{"::ffff:127.0.0.1", true},
		{"::ffff:169.254.169.254", true},
		{"::ffff:10.0.0.1", true},
		{"::ffff:8.8.8.8", false},
	}
	for _, tc := range cases {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("test setup: could not parse IP %q", tc.ip)
		}
		if got := isBlockedIP(ip); got != tc.blocked {
			t.Errorf("isBlockedIP(%s) = %v, want %v", tc.ip, got, tc.blocked)
		}
	}
	if !isBlockedIP(nil) {
		t.Errorf("isBlockedIP(nil) should be true")
	}
}

func TestSSRFSafeDialControl(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		address string
		wantErr bool
	}{
		{"public ipv4", "8.8.8.8:443", false},
		{"public ipv6 bracketed", "[2606:4700:4700::1111]:443", false},
		{"loopback", "127.0.0.1:80", true},
		{"loopback ipv6 bracketed", "[::1]:80", true},
		{"metadata endpoint", "169.254.169.254:80", true},
		{"rfc1918", "10.0.0.3:5432", true},
		{"v4-mapped loopback", "[::ffff:127.0.0.1]:80", true},
		{"missing port", "8.8.8.8", true},                // SplitHostPort fails
		{"unresolved hostname", "example.com:443", true}, // ParseIP returns nil
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ssrfSafeDialControl("tcp", tc.address, nil)
			if (err != nil) != tc.wantErr {
				t.Errorf("ssrfSafeDialControl(%q) err = %v, wantErr %v", tc.address, err, tc.wantErr)
			}
		})
	}
}

func TestSSRFSafeCheckRedirect(t *testing.T) {
	t.Parallel()

	mkReq := func(rawurl string) *http.Request {
		u, err := url.Parse(rawurl)
		if err != nil {
			t.Fatalf("test setup: bad url %q: %v", rawurl, err)
		}
		return &http.Request{URL: u}
	}

	if err := ssrfSafeCheckRedirect(mkReq("https://example.com/a"), nil); err != nil {
		t.Errorf("https redirect should be allowed, got %v", err)
	}
	if err := ssrfSafeCheckRedirect(mkReq("http://example.com/a"), nil); err != nil {
		t.Errorf("http redirect should be allowed, got %v", err)
	}
	if err := ssrfSafeCheckRedirect(mkReq("file:///etc/passwd"), nil); err == nil {
		t.Errorf("file:// redirect should be refused")
	}
	if err := ssrfSafeCheckRedirect(mkReq("gopher://169.254.169.254"), nil); err == nil {
		t.Errorf("gopher:// redirect should be refused")
	}

	// Redirect chain cap.
	via := make([]*http.Request, 10)
	if err := ssrfSafeCheckRedirect(mkReq("https://example.com/loop"), via); err == nil {
		t.Errorf("redirect chain of 10 should be stopped")
	}
}

func TestValidatePublicHTTPURL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		raw     string
		wantErr bool
	}{
		{"https://api.mezmo.com", false},
		{"http://logs.example.com:8080/path", false},
		{"https://8.8.8.8", false},
		{"", true},
		{"not-a-url", true},
		{"ftp://example.com", true},
		{"file:///etc/passwd", true},
		{"gopher://169.254.169.254", true},
		{"https://", true},                      // no host
		{"http://169.254.169.254/latest", true}, // metadata literal
		{"http://127.0.0.1:8080", true},         // loopback literal
		{"http://10.0.0.3:5432", true},          // rfc1918 literal
		{"http://[::1]:80", true},               // ipv6 loopback literal
		{"http://[::ffff:127.0.0.1]:80", true},  // v4-mapped loopback literal
	}
	for _, tc := range cases {
		if err := validatePublicHTTPURL(tc.raw); (err != nil) != tc.wantErr {
			t.Errorf("validatePublicHTTPURL(%q) err = %v, wantErr %v", tc.raw, err, tc.wantErr)
		}
	}
}
