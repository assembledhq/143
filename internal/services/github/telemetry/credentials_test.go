package telemetry

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func credentialFixture(now time.Time) CredentialRegistration {
	return CredentialRegistration{
		Profile: CredentialProfileInstallation, AppID: 21, InstallationID: 42,
		IssuedAt: now, ExpiresAt: now.Add(time.Hour),
	}
}

func TestCredentialRegistryStableOpaqueIdentity(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.October, 14, 20, 0, 0, 123, time.UTC)
	profiles := []CredentialProfile{CredentialProfileInstallation, CredentialProfileSandboxAPI, CredentialProfileSandboxPush}
	for _, profile := range profiles {
		t.Run(string(profile), func(t *testing.T) {
			t.Parallel()
			registry := NewCredentialRegistry()
			registry.now = func() time.Time { return now }
			registration := credentialFixture(now)
			registration.Profile = profile
			registration.Repository = "Octo/Demo"
			identity, ok := registry.Register("synthetic-token", registration)
			require.True(t, ok, "a successful issuer exchange should be correlatable")
			generation, err := uuid.Parse(identity.GenerationID)
			require.NoError(t, err, "generation should be an opaque random UUID")
			require.Equal(t, uuid.Version(4), generation.Version(), "generation should not be derived from credential bytes")
			expected := CredentialIdentity{
				GenerationID: identity.GenerationID, Profile: profile, AppID: 21, InstallationID: 42,
				Repository: "octo/demo", IssuedAt: now, ExpiresAt: now.Add(time.Hour),
			}
			require.Equal(t, expected, identity, "identity should retain exact issuer provenance")
			registration.IssuedAt = now.Add(time.Minute)
			registration.ExpiresAt = now.Add(2 * time.Hour)
			repeated, repeatedOK := registry.Register("synthetic-token", registration)
			require.True(t, repeatedOK, "repeated registration should preserve the original generation")
			require.Equal(t, expected, repeated, "repeat registration must not rewrite issuance clocks or extend retention")
			resolved, found := registry.Resolve("synthetic-token")
			require.True(t, found, "registered token should resolve independently of context labels")
			require.Equal(t, expected, resolved, "lookup should return the immutable original identity")
			other, otherOK := registry.Register("another-synthetic-token", credentialFixture(now))
			require.True(t, otherOK, "a distinct token should obtain its own generation")
			require.NotEqual(t, identity.GenerationID, other.GenerationID, "distinct tokens should never intentionally share a generation")
		})
	}
}

func TestCredentialRegistryConflictingProvenanceIsUncorrelated(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.October, 14, 20, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		change func(*CredentialRegistration)
	}{
		{name: "app", change: func(r *CredentialRegistration) { r.AppID++ }},
		{name: "installation", change: func(r *CredentialRegistration) { r.InstallationID++ }},
		{name: "profile", change: func(r *CredentialRegistration) { r.Profile = CredentialProfileSandboxAPI }},
		{name: "repository", change: func(r *CredentialRegistration) { r.Repository = "octo/other" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			registry := NewCredentialRegistry()
			registry.now = func() time.Time { return now }
			registration := credentialFixture(now)
			_, ok := registry.Register("synthetic-token", registration)
			require.True(t, ok, "initial issuer provenance should register")
			changed := registration
			tt.change(&changed)
			identity, accepted := registry.Register("synthetic-token", changed)
			require.False(t, accepted, "conflicting provenance should not select a convenient identity")
			require.Equal(t, CredentialIdentity{}, identity, "conflicting registration should return no identity")
			resolved, found := registry.Resolve("synthetic-token")
			require.False(t, found, "conflicting issuer provenance should remain explicitly uncorrelated")
			require.Equal(t, CredentialIdentity{}, resolved, "ambiguous provenance should not escape lookup")
			_, recovered := registry.Register("synthetic-token", registration)
			require.False(t, recovered, "subsequent original provenance must not erase a conflict")
		})
	}
}

func TestCredentialRegistryBoundsAndExpiry(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.October, 14, 20, 0, 0, 0, time.UTC)
	registry := NewCredentialRegistry()
	registry.now = func() time.Time { return now }
	registration := credentialFixture(now)
	for index := range maxCredentialRegistryEntries {
		_, ok := registry.Register(fmt.Sprintf("synthetic-%d", index), registration)
		require.True(t, ok, "all entries within the fixed capacity should register")
	}
	identity, full := registry.Register("overflow", registration)
	require.False(t, full, "capacity pressure should leave new tokens uncorrelated")
	require.Equal(t, CredentialIdentity{}, identity, "capacity pressure should not invent an unstable generation")
	_, existing := registry.Resolve("synthetic-0")
	require.True(t, existing, "capacity pressure must not evict an active generation")
	_, unknown := registry.Resolve("not-issued-here")
	require.False(t, unknown, "unknown lookups must not consume registry capacity")
	require.Equal(t, maxCredentialRegistryEntries, len(registry.entries), "unknown lookups should leave the bounded registry unchanged")
	now = registration.ExpiresAt.Add(credentialExpiryGrace - time.Nanosecond)
	_, inFlight := registry.Resolve("synthetic-0")
	require.True(t, inFlight, "expiry grace should retain identity for finishing requests")
	now = registration.ExpiresAt.Add(credentialExpiryGrace)
	resolved, expired := registry.Resolve("synthetic-0")
	require.False(t, expired, "retention must stop at the exact bounded grace deadline")
	require.Equal(t, CredentialIdentity{}, resolved, "expired registry entries should become explicitly unknown")
	require.Empty(t, registry.entries, "lazy expiry should release all expired entries without a background goroutine")
	_, replaced := registry.Register("replacement", credentialFixture(now))
	require.True(t, replaced, "expired entries should release capacity for newly issued credentials")
}

func TestCredentialRegistryConcurrentRegistration(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.October, 14, 20, 0, 0, 0, time.UTC)
	registry := NewCredentialRegistry()
	registry.now = func() time.Time { return now }
	type result struct {
		identity CredentialIdentity
		ok       bool
	}
	results := make(chan result, 64)
	var workers sync.WaitGroup
	for range 64 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			identity, ok := registry.Register("concurrent-synthetic-token", credentialFixture(now))
			results <- result{identity: identity, ok: ok}
		}()
	}
	workers.Wait()
	close(results)
	var expected CredentialIdentity
	for actual := range results {
		require.True(t, actual.ok, "concurrent issuer registration should preserve correlation")
		if expected.GenerationID == "" {
			expected = actual.identity
		}
		require.Equal(t, expected, actual.identity, "all concurrent registrations must return one generation")
	}
	require.Equal(t, 1, len(registry.entries), "concurrent duplicates should consume only one bounded entry")
}

func TestCredentialRegistryRejectsInvalidRegistration(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.October, 14, 20, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		token  string
		change func(*CredentialRegistration)
	}{
		{name: "empty credential", token: ""},
		{name: "whitespace credential", token: "synthetic token"},
		{name: "oversized credential", token: strings.Repeat("s", maxCredentialTokenBytes+1)},
		{name: "unbounded lifetime", token: "synthetic", change: func(r *CredentialRegistration) { r.ExpiresAt = r.IssuedAt.Add(maxCredentialLifetime + time.Nanosecond) }},
		{name: "future issuance", token: "synthetic", change: func(r *CredentialRegistration) {
			r.IssuedAt = now.Add(credentialIssuanceClockSkew + time.Nanosecond)
			r.ExpiresAt = r.IssuedAt.Add(time.Hour)
		}},
		{name: "unknown profile", token: "synthetic", change: func(r *CredentialRegistration) { r.Profile = "unknown" }},
		{name: "missing installation", token: "synthetic", change: func(r *CredentialRegistration) { r.InstallationID = 0 }},
		{name: "missing app", token: "synthetic", change: func(r *CredentialRegistration) { r.AppID = 0 }},
		{name: "missing expiry", token: "synthetic", change: func(r *CredentialRegistration) { r.ExpiresAt = time.Time{} }},
		{name: "malformed repository", token: "synthetic", change: func(r *CredentialRegistration) { r.Repository = "not/a/repository" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			registry := NewCredentialRegistry()
			registry.now = func() time.Time { return now }
			registration := credentialFixture(now)
			if tt.change != nil {
				tt.change(&registration)
			}
			identity, ok := registry.Register(tt.token, registration)
			require.False(t, ok, "invalid issuer evidence should remain uncorrelated")
			require.Equal(t, CredentialIdentity{}, identity, "invalid registration must not expose partial provenance")
			require.Empty(t, registry.entries, "invalid registrations must not consume registry capacity")
		})
	}
}
