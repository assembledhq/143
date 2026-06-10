package domains

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeDomain(t *testing.T) {
	t.Parallel()

	require.Equal(t, "example.com", NormalizeDomain("  Example.COM  "))
	require.Equal(t, "example.com", NormalizeDomain("example.com."))
	require.Equal(t, "", NormalizeDomain("   "))
}

func TestValidateDomain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		domain    string
		expectErr bool
	}{
		{name: "plain company domain", domain: "assembledhq.com", expectErr: false},
		{name: "subdomain is allowed", domain: "mail.assembledhq.com", expectErr: false},
		{name: "multi-label TLD", domain: "company.co.uk", expectErr: false},
		{name: "punycode IDN", domain: "xn--bcher-kva.example", expectErr: false},
		{name: "empty", domain: "", expectErr: true},
		{name: "single label", domain: "localhost", expectErr: true},
		{name: "url not domain", domain: "https://example.com", expectErr: true},
		{name: "email not domain", domain: "user@example.com", expectErr: true},
		{name: "ip address", domain: "192.168.1.1", expectErr: true},
		{name: "leading hyphen label", domain: "-bad.example.com", expectErr: true},
		{name: "free provider gmail", domain: "gmail.com", expectErr: true},
		{name: "free provider regional", domain: "yahoo.co.uk", expectErr: true},
		{name: "disposable provider", domain: "mailinator.com", expectErr: true},
		{name: "github noreply relay", domain: "users.noreply.github.com", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateDomain(tt.domain)
			if tt.expectErr {
				require.Error(t, err, "ValidateDomain should reject %q", tt.domain)
				return
			}
			require.NoError(t, err, "ValidateDomain should accept %q", tt.domain)
		})
	}
}

func TestEmailDomain(t *testing.T) {
	t.Parallel()

	require.Equal(t, "assembledhq.com", EmailDomain("john@AssembledHQ.com"))
	require.Equal(t, "example.com", EmailDomain(`"weird@local"@example.com`))
	require.Equal(t, "", EmailDomain("no-at-sign"))
	require.Equal(t, "", EmailDomain("trailing@"))
}

func TestTXTRecordHelpers(t *testing.T) {
	t.Parallel()

	require.Equal(t, "_143-verify.example.com", TXTRecordName("example.com"))
	require.Equal(t, "143-domain-verify=abc123", TXTRecordValue("abc123"))
}

func TestGenerateToken(t *testing.T) {
	t.Parallel()

	tok, err := GenerateToken()
	require.NoError(t, err)
	require.Len(t, tok, 32, "token should be 32 hex chars (128 bits)")

	other, err := GenerateToken()
	require.NoError(t, err)
	require.NotEqual(t, tok, other, "tokens must be unique")
}

// fakeResolver maps lookup names to TXT record sets or errors.
type fakeResolver struct {
	records map[string][]string
	errs    map[string]error
}

func (f *fakeResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	if err, ok := f.errs[name]; ok {
		return nil, err
	}
	if recs, ok := f.records[name]; ok {
		return recs, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func TestVerifierVerify(t *testing.T) {
	t.Parallel()

	const domain = "example.com"
	const token = "abc123"
	want := TXTRecordValue(token)

	t.Run("record at dedicated label", func(t *testing.T) {
		t.Parallel()
		v := NewVerifierWithResolver(&fakeResolver{records: map[string][]string{
			"_143-verify.example.com": {"unrelated", want},
		}})
		ok, err := v.Verify(context.Background(), domain, token)
		require.NoError(t, err)
		require.True(t, ok)
	})

	t.Run("record at apex fallback", func(t *testing.T) {
		t.Parallel()
		v := NewVerifierWithResolver(&fakeResolver{records: map[string][]string{
			"example.com": {"v=spf1 -all", want},
		}})
		ok, err := v.Verify(context.Background(), domain, token)
		require.NoError(t, err)
		require.True(t, ok)
	})

	t.Run("wrong token is not verified", func(t *testing.T) {
		t.Parallel()
		v := NewVerifierWithResolver(&fakeResolver{records: map[string][]string{
			"_143-verify.example.com": {TXTRecordValue("other")},
		}})
		ok, err := v.Verify(context.Background(), domain, token)
		require.NoError(t, err)
		require.False(t, ok)
	})

	t.Run("nxdomain on both names is not an error", func(t *testing.T) {
		t.Parallel()
		v := NewVerifierWithResolver(&fakeResolver{})
		ok, err := v.Verify(context.Background(), domain, token)
		require.NoError(t, err, "missing records mean 'not verified', not lookup failure")
		require.False(t, ok)
	})

	t.Run("hard resolver failure on both names surfaces error", func(t *testing.T) {
		t.Parallel()
		v := NewVerifierWithResolver(&fakeResolver{errs: map[string]error{
			"_143-verify.example.com": &net.DNSError{Err: "server misbehaving", Name: "_143-verify.example.com", IsTemporary: true},
			"example.com":             &net.DNSError{Err: "server misbehaving", Name: "example.com", IsTemporary: true},
		}})
		ok, err := v.Verify(context.Background(), domain, token)
		require.Error(t, err)
		require.False(t, ok)
	})

	t.Run("lookup error on label but match at apex still verifies", func(t *testing.T) {
		t.Parallel()
		v := NewVerifierWithResolver(&fakeResolver{
			errs:    map[string]error{"_143-verify.example.com": &net.DNSError{Err: "server misbehaving", IsTemporary: true}},
			records: map[string][]string{"example.com": {want}},
		})
		ok, err := v.Verify(context.Background(), domain, token)
		require.NoError(t, err)
		require.True(t, ok)
	})
}

func TestIsPublicEmailDomain(t *testing.T) {
	t.Parallel()

	require.True(t, IsPublicEmailDomain("gmail.com"))
	require.True(t, IsPublicEmailDomain("qq.com"))
	require.True(t, IsPublicEmailDomain("privaterelay.appleid.com"))
	require.False(t, IsPublicEmailDomain("assembledhq.com"))
}
