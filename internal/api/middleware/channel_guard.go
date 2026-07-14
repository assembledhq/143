package middleware

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// ReleaseChannelLookup resolves an org's release channel. Implemented by
// db.OrganizationStore.
type ReleaseChannelLookup interface {
	GetReleaseChannel(ctx context.Context, id uuid.UUID) (models.ReleaseChannel, error)
}

// channelGuardCacheTTL bounds how long a flipped org can keep using the wrong
// host. Org flips are rare operator actions performed while the org is
// quiescent, so a short positive cache is safe and keeps the guard off the
// DB for repeat requests.
const channelGuardCacheTTL = time.Minute

// channelGuardCacheSweepAt is the map size at which a store first sweeps
// expired entries, so orgs that stopped hitting the canary host (or were
// deleted outright) don't accumulate forever. Live entries always survive a
// sweep; the map can exceed this bound only while that many orgs are actively
// inside their TTL window.
const channelGuardCacheSweepAt = 1024

type channelCacheEntry struct {
	channel    models.ReleaseChannel
	validUntil time.Time
}

// channelGuardCache is a TTL cache of org release channels. now is injectable
// for tests; nil means time.Now.
type channelGuardCache struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[uuid.UUID]channelCacheEntry
}

func newChannelGuardCache(now func() time.Time) *channelGuardCache {
	if now == nil {
		now = time.Now
	}
	return &channelGuardCache{now: now, entries: make(map[uuid.UUID]channelCacheEntry)}
}

func (c *channelGuardCache) get(orgID uuid.UUID) (models.ReleaseChannel, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[orgID]
	if !ok {
		return "", false
	}
	if c.now().After(entry.validUntil) {
		delete(c.entries, orgID)
		return "", false
	}
	return entry.channel, true
}

func (c *channelGuardCache) put(orgID uuid.UUID, channel models.ReleaseChannel) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= channelGuardCacheSweepAt {
		now := c.now()
		for id, entry := range c.entries {
			if now.After(entry.validUntil) {
				delete(c.entries, id)
			}
		}
	}
	c.entries[orgID] = channelCacheEntry{channel: channel, validUntil: c.now().Add(channelGuardCacheTTL)}
}

// RequireCanaryChannelForHost guards the canary hostname: authenticated
// requests whose active org is not on the canary release channel are refused,
// so the canary plane (running latest main) only ever serves dogfood orgs.
// See docs/design/118-canary-stable-release-channels.md.
//
// Scope and non-goals:
//   - Only requests whose Host matches canaryHost are guarded; the stable
//     hostname serves any org (older UI is harmless).
//   - Requests without an org in context (login, OAuth callbacks, health)
//     pass through — dogfood users must be able to sign in on the canary
//     host in the first place.
//   - canaryHost == "" disables the guard entirely (single-plane deployments
//     and local dev).
func RequireCanaryChannelForHost(canaryHost string, lookup ReleaseChannelLookup, logger zerolog.Logger) func(http.Handler) http.Handler {
	canaryHost = normalizeHost(canaryHost)
	cache := newChannelGuardCache(nil)

	return func(next http.Handler) http.Handler {
		if canaryHost == "" {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if normalizeHost(r.Host) != canaryHost {
				next.ServeHTTP(w, r)
				return
			}
			orgID := OrgIDFromContext(r.Context())
			if orgID == uuid.Nil {
				next.ServeHTTP(w, r)
				return
			}

			channel, ok := cache.get(orgID)
			if !ok {
				fetched, err := lookup.GetReleaseChannel(r.Context(), orgID)
				if err != nil {
					// Fail closed: the canary host must not serve an org whose
					// channel we cannot establish.
					zerolog.Ctx(r.Context()).Error().Err(err).Str("org_id", orgID.String()).
						Msg("canary host guard could not resolve org release channel")
					writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "release channel lookup failed")
					return
				}
				channel = fetched
				cache.put(orgID, channel)
			}

			if channel != models.ReleaseChannelCanary {
				logger.Debug().Str("org_id", orgID.String()).Str("host", r.Host).
					Msg("refusing non-canary org on canary host")
				// redirect_origin lets the frontend bounce browser sessions
				// back to the primary domain instead of rendering a shell
				// whose every API call fails. Derivable only under the
				// canary.<primary-domain> convention; omitted otherwise.
				var details any
				if primary := primaryOriginForCanaryHost(r); primary != "" {
					details = map[string]string{"redirect_origin": primary}
				}
				writeErrorDetails(w, http.StatusForbidden, "ORG_NOT_ON_CANARY",
					"this org is not on the canary release channel; use the primary domain", details)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// primaryOriginForCanaryHost derives the stable plane's origin from a
// request to the canary host by stripping the leading "canary." label —
// the naming convention the whole split uses (canary.<primary-domain>).
// Returns "" when the host doesn't follow the convention.
func primaryOriginForCanaryHost(r *http.Request) string {
	host := normalizeHost(r.Host)
	primary, ok := strings.CutPrefix(host, "canary.")
	if !ok || primary == "" {
		return ""
	}
	scheme := "https"
	if !IsRequestSecure(r) {
		scheme = "http"
	}
	return scheme + "://" + primary
}

// normalizeHost lowercases and strips any port so "Canary.143.dev:443"
// matches "canary.143.dev".
func normalizeHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}
