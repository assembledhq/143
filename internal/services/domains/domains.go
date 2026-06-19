// Package domains implements email-domain ownership verification for
// organization auto-join ("domain capture").
//
// The flow mirrors the pattern used by Google Workspace, Figma, and
// ChatGPT/Claude Enterprise: an admin claims a domain, we hand them an
// unguessable token to publish as a DNS TXT record, and a verify call
// checks DNS for it. DNS-level proof (rather than "an admin has an email
// on the domain") is required because verification unlocks *automatic*
// membership for every future signup on the domain.
package domains

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
)

// TXTRecordPrefix is the well-known prefix of the verification TXT record
// value, so multiple tools' records can coexist at the same name.
const TXTRecordPrefix = "143-domain-verify="

// TXTRecordLabel is the underscore-prefixed label the record is published
// under (e.g. _143-verify.example.com). The underscore form keeps
// verification records out of the hostname namespace (RFC 8552 convention).
const TXTRecordLabel = "_143-verify"

// lookupTimeout bounds each DNS query so a black-holed resolver can't hang
// the verify endpoint.
const lookupTimeout = 5 * time.Second

// hostnameRE matches a plausible DNS domain: dot-separated labels of
// letters/digits/hyphens, no leading/trailing hyphen per label, at least
// two labels, alphabetic TLD. Deliberately rejects IPs, ports, schemes,
// paths, wildcards, and internationalized domains in raw (non-punycode)
// form — admins with IDN domains enter the xn-- form.
var hostnameRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// NormalizeDomain lowercases and trims a user-entered domain, stripping a
// single trailing dot (FQDN form). It does not validate.
func NormalizeDomain(domain string) string {
	d := strings.ToLower(strings.TrimSpace(domain))
	d = strings.TrimSuffix(d, ".")
	return d
}

// ValidateDomain checks that a normalized domain is a plausible
// organization-owned email domain: syntactically a hostname and not a
// public/free email provider.
func ValidateDomain(domain string) error {
	if domain == "" {
		return fmt.Errorf("domain is required")
	}
	if len(domain) > 253 {
		return fmt.Errorf("domain is too long")
	}
	if strings.ContainsAny(domain, "/@:") {
		return fmt.Errorf("enter a bare domain like example.com, not a URL or email address")
	}
	if !hostnameRE.MatchString(domain) {
		return fmt.Errorf("%q is not a valid domain name", domain)
	}
	if IsPublicEmailDomain(domain) {
		return fmt.Errorf("%q is a public email provider and cannot be claimed by an organization", domain)
	}
	return nil
}

// EmailDomain extracts the lowercase domain part of an email address, or ""
// when the address has no usable domain.
func EmailDomain(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return ""
	}
	return NormalizeDomain(email[at+1:])
}

// GenerateToken returns the random value the admin publishes in DNS.
// 32 hex chars ≈ 128 bits — unguessable, and hex keeps it copy-paste safe.
func GenerateToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate domain verification token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// TXTRecordName returns the fully-qualified name the TXT record should be
// published at for the given domain.
func TXTRecordName(domain string) string {
	return TXTRecordLabel + "." + domain
}

// TXTRecordValue returns the exact TXT record value for a token.
func TXTRecordValue(token string) string {
	return TXTRecordPrefix + token
}

// txtResolver is the seam for DNS lookups so tests don't hit real DNS.
type txtResolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// Verifier checks domain-ownership TXT records via DNS.
type Verifier struct {
	resolver txtResolver
}

// NewVerifier returns a Verifier backed by the system resolver.
func NewVerifier() *Verifier {
	return &Verifier{resolver: net.DefaultResolver}
}

// NewVerifierWithResolver returns a Verifier with a custom resolver (tests).
func NewVerifierWithResolver(r txtResolver) *Verifier {
	return &Verifier{resolver: r}
}

// Verify reports whether the expected TXT record for token is published for
// domain. It checks the dedicated _143-verify label first, then falls back
// to the zone apex (some DNS hosts make underscore labels awkward). A
// lookup error on one name is not fatal as long as the other matches; the
// returned error is the apex lookup error only when neither name resolved.
func (v *Verifier) Verify(ctx context.Context, domain, token string) (bool, error) {
	want := TXTRecordValue(token)

	labelCtx, cancel := context.WithTimeout(ctx, lookupTimeout)
	records, labelErr := v.resolver.LookupTXT(labelCtx, TXTRecordName(domain))
	cancel()
	if labelErr == nil && containsRecord(records, want) {
		return true, nil
	}

	apexCtx, cancel := context.WithTimeout(ctx, lookupTimeout)
	records, apexErr := v.resolver.LookupTXT(apexCtx, domain)
	cancel()
	if apexErr == nil && containsRecord(records, want) {
		return true, nil
	}

	// NXDOMAIN / no-such-record errors mean "not verified", not "lookup
	// broken" — only surface an error when both lookups failed for
	// non-NotFound reasons, so transient resolver trouble is reported
	// distinctly from a missing record.
	if labelErr != nil && apexErr != nil && !isNotFound(labelErr) && !isNotFound(apexErr) {
		return false, fmt.Errorf("dns lookup failed: %w", apexErr)
	}
	return false, nil
}

func containsRecord(records []string, want string) bool {
	for _, r := range records {
		if strings.TrimSpace(r) == want {
			return true
		}
	}
	return false
}

func isNotFound(err error) bool {
	if dnsErr, ok := errors.AsType[*net.DNSError](err); ok {
		return dnsErr.IsNotFound
	}
	return false
}
