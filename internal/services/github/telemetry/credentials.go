package telemetry

import (
	"crypto/sha256"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
)

// CredentialProfile identifies an issuer-owned token permission profile. It is
// diagnostic metadata only and never participates in authorization or metrics.
type CredentialProfile string

const (
	CredentialProfileInstallation CredentialProfile = "installation"
	CredentialProfileSandboxAPI   CredentialProfile = "sandbox_api"
	CredentialProfileSandboxPush  CredentialProfile = "sandbox_push"

	maxCredentialRegistryEntries = 4096
	credentialExpiryGrace        = 10 * time.Minute
	maxCredentialLifetime        = 2 * time.Hour
	credentialIssuanceClockSkew  = 5 * time.Minute
	maxCredentialTokenBytes      = 8192
)

// CredentialRegistration is supplied only after a successful token exchange.
// Tokens themselves are retained only as process-local, non-exported digests.
type CredentialRegistration struct {
	Profile        CredentialProfile
	AppID          int64
	InstallationID int64
	Repository     string
	IssuedAt       time.Time
	ExpiresAt      time.Time
}

// CredentialIdentity is an opaque process-local correlation identity, not a
// credential or an authentication decision. It contains no token fingerprint.
type CredentialIdentity struct {
	GenerationID   string
	Profile        CredentialProfile
	AppID          int64
	InstallationID int64
	Repository     string
	IssuedAt       time.Time
	ExpiresAt      time.Time
}

type credentialEntry struct {
	identity   CredentialIdentity
	conflicted bool
}

// CredentialRegistry correlates only tokens registered by a local issuer. It
// deliberately refuses new registrations at capacity rather than evicting a
// still-live generation and falsely splitting its quota history. Expired tokens
// remain available for a ten-minute grace period to identify in-flight requests.
// Issuance is accepted at most five minutes ahead and lifetimes at most two hours;
// malformed provider timestamps cannot pin an entry indefinitely.
type CredentialRegistry struct {
	mu      sync.Mutex
	entries map[[sha256.Size]byte]credentialEntry
	now     func() time.Time
	limit   int
	grace   time.Duration
}

var defaultCredentialRegistry = NewCredentialRegistry()

// NewCredentialRegistry creates an isolated registry with production bounds.
func NewCredentialRegistry() *CredentialRegistry {
	return &CredentialRegistry{
		entries: make(map[[sha256.Size]byte]credentialEntry),
		now:     time.Now,
		limit:   maxCredentialRegistryEntries,
		grace:   credentialExpiryGrace,
	}
}

// DefaultCredentialRegistry is shared by local token issuers and transports.
func DefaultCredentialRegistry() *CredentialRegistry { return defaultCredentialRegistry }

// RegisterCredential registers a token in the shared process-local registry.
func RegisterCredential(token string, registration CredentialRegistration) (CredentialIdentity, bool) {
	return defaultCredentialRegistry.Register(token, registration)
}

// Register returns the original identity for repeated registration of the same
// token and issuer provenance. Conflicting provenance makes that token unknown
// until expiry; registration failures never prevent use of the credential.
func (r *CredentialRegistry) Register(token string, registration CredentialRegistration) (CredentialIdentity, bool) {
	if r == nil || !validCredentialToken(token) || !validCredentialRegistration(registration) {
		return CredentialIdentity{}, false
	}
	registration.Repository = strings.ToLower(registration.Repository)
	digest := sha256.Sum256([]byte(token))
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if registration.ExpiresAt.Sub(registration.IssuedAt) > maxCredentialLifetime || registration.IssuedAt.After(now.Add(credentialIssuanceClockSkew)) {
		return CredentialIdentity{}, false
	}
	r.pruneLocked(now)
	if existing, ok := r.entries[digest]; ok {
		if existing.conflicted || !sameCredentialProvenance(existing.identity, registration) {
			existing.conflicted = true
			r.entries[digest] = existing
			return CredentialIdentity{}, false
		}
		return existing.identity, true
	}
	if len(r.entries) >= r.limit || !now.Before(registration.ExpiresAt.Add(r.grace)) {
		return CredentialIdentity{}, false
	}
	generation, err := uuid.NewRandom()
	if err != nil {
		return CredentialIdentity{}, false
	}
	identity := CredentialIdentity{
		GenerationID: generation.String(), Profile: registration.Profile,
		AppID: registration.AppID, InstallationID: registration.InstallationID,
		Repository: registration.Repository,
		IssuedAt:   registration.IssuedAt.UTC(), ExpiresAt: registration.ExpiresAt.UTC(),
	}
	r.entries[digest] = credentialEntry{identity: identity}
	return identity, true
}

// Resolve never registers an unknown token. Callers may log the returned opaque
// identity, but must continue to derive authentication/control scope separately.
func (r *CredentialRegistry) Resolve(token string) (CredentialIdentity, bool) {
	if r == nil || !validCredentialToken(token) {
		return CredentialIdentity{}, false
	}
	digest := sha256.Sum256([]byte(token))
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(r.now())
	entry, ok := r.entries[digest]
	if !ok || entry.conflicted {
		return CredentialIdentity{}, false
	}
	return entry.identity, true
}

func (r *CredentialRegistry) pruneLocked(now time.Time) {
	for digest, entry := range r.entries {
		if !now.Before(entry.identity.ExpiresAt.Add(r.grace)) {
			delete(r.entries, digest)
		}
	}
}

func validCredentialToken(token string) bool {
	return token != "" && len(token) <= maxCredentialTokenBytes && strings.IndexFunc(token, unicode.IsSpace) < 0
}

func validCredentialRegistration(registration CredentialRegistration) bool {
	switch registration.Profile {
	case CredentialProfileInstallation, CredentialProfileSandboxAPI, CredentialProfileSandboxPush:
	default:
		return false
	}
	if registration.AppID <= 0 || registration.InstallationID <= 0 || registration.IssuedAt.IsZero() || !registration.ExpiresAt.After(registration.IssuedAt) {
		return false
	}
	if registration.Repository == "" {
		return true
	}
	if len(registration.Repository) > 256 || strings.Count(registration.Repository, "/") != 1 {
		return false
	}
	for _, part := range strings.Split(registration.Repository, "/") {
		if part == "" {
			return false
		}
		for _, ch := range part {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-' || ch == '.') {
				return false
			}
		}
	}
	return true
}

func sameCredentialProvenance(identity CredentialIdentity, registration CredentialRegistration) bool {
	return identity.Profile == registration.Profile && identity.AppID == registration.AppID &&
		identity.InstallationID == registration.InstallationID && identity.Repository == registration.Repository
}

// credentialFromAuthorization accepts GitHub's bearer and token schemes while
// refusing ambiguous duplicate Authorization values. Neither the header nor its
// digest escapes this lookup.
func (r *CredentialRegistry) credentialFromAuthorization(header http.Header) (CredentialIdentity, bool) {
	var values []string
	for name, candidate := range header {
		if strings.EqualFold(name, "Authorization") {
			values = append(values, candidate...)
		}
	}
	if len(values) != 1 {
		return CredentialIdentity{}, false
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !(strings.EqualFold(parts[0], "bearer") || strings.EqualFold(parts[0], "token")) {
		return CredentialIdentity{}, false
	}
	return r.Resolve(parts[1])
}
