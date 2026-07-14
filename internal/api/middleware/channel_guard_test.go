package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type fakeChannelLookup struct {
	channel models.ReleaseChannel
	err     error
	calls   int
}

func (f *fakeChannelLookup) GetReleaseChannel(_ context.Context, _ uuid.UUID) (models.ReleaseChannel, error) {
	f.calls++
	return f.channel, f.err
}

func channelGuardRequest(t *testing.T, guard func(http.Handler) http.Handler, host string, orgID uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "http://"+host+"/api/v1/sessions", nil)
	req.Host = host
	if orgID != uuid.Nil {
		req = req.WithContext(WithOrgID(req.Context(), orgID))
	}
	rr := httptest.NewRecorder()
	guard(next).ServeHTTP(rr, req)
	return rr
}

func TestRequireCanaryChannelForHost(t *testing.T) {
	t.Parallel()

	t.Run("empty canary host disables the guard", func(t *testing.T) {
		t.Parallel()
		lookup := &fakeChannelLookup{channel: models.ReleaseChannelStable}
		guard := RequireCanaryChannelForHost("", lookup, zerolog.Nop())
		rr := channelGuardRequest(t, guard, "canary.143.dev", uuid.New())
		require.Equal(t, http.StatusOK, rr.Code, "single-plane deployments must be unaffected")
		require.Zero(t, lookup.calls, "the disabled guard must not hit the database")
	})

	t.Run("non-canary hosts pass any org", func(t *testing.T) {
		t.Parallel()
		lookup := &fakeChannelLookup{channel: models.ReleaseChannelStable}
		guard := RequireCanaryChannelForHost("canary.143.dev", lookup, zerolog.Nop())
		rr := channelGuardRequest(t, guard, "143.dev", uuid.New())
		require.Equal(t, http.StatusOK, rr.Code, "the stable hostname serves every org")
		require.Zero(t, lookup.calls, "stable-host requests must not pay a channel lookup")
	})

	t.Run("canary host refuses stable-channel orgs with a redirect target", func(t *testing.T) {
		t.Parallel()
		lookup := &fakeChannelLookup{channel: models.ReleaseChannelStable}
		guard := RequireCanaryChannelForHost("canary.143.dev", lookup, zerolog.Nop())
		rr := channelGuardRequest(t, guard, "canary.143.dev", uuid.New())
		require.Equal(t, http.StatusForbidden, rr.Code, "a stable org must not be served canary code")
		require.Contains(t, rr.Body.String(), "ORG_NOT_ON_CANARY")
		require.Contains(t, rr.Body.String(), `"redirect_origin":"http://143.dev"`,
			"the 403 must carry the primary origin so browser sessions can bounce off the canary host")
	})

	t.Run("canary host serves canary-channel orgs", func(t *testing.T) {
		t.Parallel()
		lookup := &fakeChannelLookup{channel: models.ReleaseChannelCanary}
		guard := RequireCanaryChannelForHost("canary.143.dev", lookup, zerolog.Nop())
		rr := channelGuardRequest(t, guard, "canary.143.dev", uuid.New())
		require.Equal(t, http.StatusOK, rr.Code)
	})

	t.Run("host matching ignores port and case", func(t *testing.T) {
		t.Parallel()
		lookup := &fakeChannelLookup{channel: models.ReleaseChannelCanary}
		guard := RequireCanaryChannelForHost("Canary.143.dev", lookup, zerolog.Nop())
		rr := channelGuardRequest(t, guard, "canary.143.dev:443", uuid.New())
		require.Equal(t, http.StatusOK, rr.Code)
		require.Equal(t, 1, lookup.calls, "the guarded host must be recognized despite port/case differences")
	})

	t.Run("unauthenticated requests pass so login works on the canary host", func(t *testing.T) {
		t.Parallel()
		lookup := &fakeChannelLookup{channel: models.ReleaseChannelStable}
		guard := RequireCanaryChannelForHost("canary.143.dev", lookup, zerolog.Nop())
		rr := channelGuardRequest(t, guard, "canary.143.dev", uuid.Nil)
		require.Equal(t, http.StatusOK, rr.Code)
		require.Zero(t, lookup.calls)
	})

	t.Run("lookup failures fail closed", func(t *testing.T) {
		t.Parallel()
		lookup := &fakeChannelLookup{err: errors.New("db down")}
		guard := RequireCanaryChannelForHost("canary.143.dev", lookup, zerolog.Nop())
		rr := channelGuardRequest(t, guard, "canary.143.dev", uuid.New())
		require.Equal(t, http.StatusServiceUnavailable, rr.Code,
			"the canary host must not serve an org whose channel cannot be established")
	})

	t.Run("channel decisions are cached per org", func(t *testing.T) {
		t.Parallel()
		lookup := &fakeChannelLookup{channel: models.ReleaseChannelCanary}
		guard := RequireCanaryChannelForHost("canary.143.dev", lookup, zerolog.Nop())
		orgID := uuid.New()
		for range 3 {
			rr := channelGuardRequest(t, guard, "canary.143.dev", orgID)
			require.Equal(t, http.StatusOK, rr.Code)
		}
		require.Equal(t, 1, lookup.calls, "repeat requests within the TTL must reuse the cached channel")
	})
}
