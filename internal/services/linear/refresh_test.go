package linear

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

// fakeCredentialStore implements both CredentialReader and CredentialWriter
// for the refresh tests. Holds a single LinearConfig per org keyed by orgID;
// captures Upsert calls so tests can assert what was persisted across
// concurrent refreshes.
type fakeCredentialStore struct {
	mu         sync.Mutex
	configs    map[uuid.UUID]models.LinearConfig
	upsertHits []models.LinearConfig
	getErr     error // when set, every Get returns this error
	upsertErr  error // when set, every Upsert returns this error after recording the hit
}

func newFakeCredentialStore() *fakeCredentialStore {
	return &fakeCredentialStore{configs: map[uuid.UUID]models.LinearConfig{}}
}

func (f *fakeCredentialStore) Get(_ context.Context, orgID uuid.UUID, _ models.ProviderName) (*models.DecryptedCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	cfg, ok := f.configs[orgID]
	if !ok {
		return nil, nil
	}
	return &models.DecryptedCredential{
		ID:     uuid.New(),
		OrgID:  orgID,
		Config: cfg,
	}, nil
}

func (f *fakeCredentialStore) Upsert(_ context.Context, orgID uuid.UUID, cfg models.ProviderConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	lc, ok := cfg.(models.LinearConfig)
	if !ok {
		return fmt.Errorf("expected LinearConfig, got %T", cfg)
	}
	// Record the attempt regardless of upsertErr so tests can distinguish
	// "didn't try to write" from "tried, but the store rejected it."
	f.upsertHits = append(f.upsertHits, lc)
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.configs[orgID] = lc
	return nil
}

func (f *fakeCredentialStore) UpdateLinearConfigIfRefreshTokenMatches(_ context.Context, orgID uuid.UUID, expectedRefreshToken string, cfg models.LinearConfig) (models.LinearConfig, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upsertHits = append(f.upsertHits, cfg)
	if f.upsertErr != nil {
		return models.LinearConfig{}, false, f.upsertErr
	}
	current := f.configs[orgID]
	if current.RefreshToken != expectedRefreshToken {
		return current, false, nil
	}
	f.configs[orgID] = cfg
	return cfg, true, nil
}

func (f *fakeCredentialStore) snapshot(orgID uuid.UUID) models.LinearConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.configs[orgID]
}

func (f *fakeCredentialStore) upsertCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.upsertHits)
}

// roundTripFunc implements http.RoundTripper from a closure.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// stubLinearOAuthServer returns an http.Client whose RoundTripper services
// /oauth/token requests with the supplied response builder. callCount tracks
// how many requests landed so tests can assert serialization.
type stubLinearOAuthServer struct {
	calls   atomic.Int64
	respond func(form url.Values) (status int, body string)
}

func (s *stubLinearOAuthServer) httpClient() *http.Client {
	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != linearTokenURL {
			return nil, fmt.Errorf("unexpected URL %s", req.URL.String())
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			return nil, err
		}
		s.calls.Add(1)
		status, respBody := s.respond(form)
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(respBody)),
		}, nil
	})}
}

// newRefreshTestService wires a Service with the minimum surface refresh
// tests need: stub integration + credential stores, a fake HTTP transport
// for /oauth/token, and a logger that drops output.
func newRefreshTestService(t *testing.T, intg *fakeIntegrationStore, creds *fakeCredentialStore, oauthClient *stubLinearOAuthServer) *Service {
	t.Helper()
	svc := &Service{
		logger:             zerolog.Nop(),
		integrations:       intg,
		integrationsWriter: intg,
		credentials:        creds,
		credentialsWriter:  creds,
		oauthClient: OAuthClientCreds{
			ClientID:     "test-client",
			ClientSecret: "test-secret",
		},
	}
	if oauthClient != nil {
		svc.refreshHTTPClient = oauthClient.httpClient()
	} else {
		svc.refreshHTTPClient = &http.Client{}
	}
	return svc
}

func newActiveLinearIntegrationStore(orgID uuid.UUID) *fakeIntegrationStore {
	return &fakeIntegrationStore{
		row: models.Integration{
			ID:     uuid.New(),
			OrgID:  orgID,
			Status: models.IntegrationStatusActive,
			Config: json.RawMessage(`{"workspace_id":"wks-1"}`),
		},
	}
}

// TestGetValidToken_NoRefreshNeeded verifies the fast path: a healthy
// credential whose ExpiresAt is well outside the refresh window is
// returned as-is, no /oauth/token traffic. This is the single most common
// case in production and any regression here means we'd hammer Linear's
// OAuth endpoint on every API call.
func TestGetValidToken_NoRefreshNeeded(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	intg := newActiveLinearIntegrationStore(orgID)
	creds := newFakeCredentialStore()
	creds.configs[orgID] = models.LinearConfig{
		AccessToken:  "lin_at_active",
		RefreshToken: "lin_rt",
		ExpiresAt:    time.Now().Add(2 * time.Hour),
		WorkspaceID:  "wks-1",
	}
	stub := &stubLinearOAuthServer{respond: func(form url.Values) (int, string) {
		t.Fatal("refresh endpoint should not be called for a fresh token")
		return 0, ""
	}}
	svc := newRefreshTestService(t, intg, creds, stub)

	_, token, err := svc.GetValidToken(context.Background(), orgID)
	require.NoError(t, err)
	require.Equal(t, "lin_at_active", token)
	require.Zero(t, stub.calls.Load(), "no refresh expected")
	require.Zero(t, creds.upsertCount(), "credential row must not be rewritten on the fast path")
}

// TestGetValidToken_LegacyNoRefreshToken asserts that legacy credentials
// with no refresh token and zero ExpiresAt keep working — they return the
// cached token, never attempt refresh, and never error. This is the migration
// path: existing connections stay live until the user reconnects, at which
// point they get the new fields.
func TestGetValidToken_LegacyNoRefreshToken(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	intg := newActiveLinearIntegrationStore(orgID)
	creds := newFakeCredentialStore()
	creds.configs[orgID] = models.LinearConfig{
		AccessToken: "lin_at_legacy",
		// no RefreshToken, no ExpiresAt — the legacy shape.
	}
	stub := &stubLinearOAuthServer{respond: func(form url.Values) (int, string) {
		t.Fatal("legacy credential must never trigger a refresh request")
		return 0, ""
	}}
	svc := newRefreshTestService(t, intg, creds, stub)

	_, token, err := svc.GetValidToken(context.Background(), orgID)
	require.NoError(t, err)
	require.Equal(t, "lin_at_legacy", token)
	require.Zero(t, stub.calls.Load())
}

// TestGetValidToken_KnownExpiryNoRefreshTokenRequiresReconnect covers rows
// that know the access token expiry but lack a refresh token. Once they are
// inside the refresh window, returning the cached token would knowingly hand
// callers a stale or soon-stale credential. The correct behavior is to force
// the reconnect path through ErrNoRefreshToken.
func TestGetValidToken_KnownExpiryNoRefreshTokenRequiresReconnect(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	intg := newActiveLinearIntegrationStore(orgID)
	creds := newFakeCredentialStore()
	creds.configs[orgID] = models.LinearConfig{
		AccessToken: "lin_at_expiring",
		ExpiresAt:   time.Now().Add(2 * time.Minute),
	}
	stub := &stubLinearOAuthServer{respond: func(form url.Values) (int, string) {
		t.Fatal("refresh endpoint should not be called without a refresh token")
		return 0, ""
	}}
	svc := newRefreshTestService(t, intg, creds, stub)

	_, token, err := svc.GetValidToken(context.Background(), orgID)
	require.ErrorIs(t, err, ErrNoRefreshToken, "known-expiring credentials without refresh token should force reconnect")
	require.Empty(t, token, "caller must not receive a known-expiring cached token")
	require.Zero(t, stub.calls.Load(), "no refresh request is possible without a refresh token")
}

// TestGetValidToken_RefreshSucceeds covers the canonical happy path:
// token is within the refresh window, refresh-token grant is exchanged for
// a new access/refresh pair, the rotated row is persisted, and the new
// access token is returned to the caller.
func TestGetValidToken_RefreshSucceeds(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	intg := newActiveLinearIntegrationStore(orgID)
	creds := newFakeCredentialStore()
	creds.configs[orgID] = models.LinearConfig{
		AccessToken:   "lin_at_old",
		RefreshToken:  "lin_rt_old",
		ExpiresAt:     time.Now().Add(2 * time.Minute), // within 5min refresh window
		WorkspaceID:   "wks-1",
		WorkspaceName: "Acme",
	}
	stub := &stubLinearOAuthServer{respond: func(form url.Values) (int, string) {
		require.Equal(t, "refresh_token", form.Get("grant_type"))
		require.Equal(t, "lin_rt_old", form.Get("refresh_token"))
		require.Equal(t, "test-client", form.Get("client_id"))
		require.Equal(t, "test-secret", form.Get("client_secret"))
		return http.StatusOK, `{"access_token":"lin_at_new","refresh_token":"lin_rt_new","token_type":"Bearer","scope":"read,write","expires_in":7200}`
	}}
	svc := newRefreshTestService(t, intg, creds, stub)

	_, token, err := svc.GetValidToken(context.Background(), orgID)
	require.NoError(t, err)
	require.Equal(t, "lin_at_new", token, "caller should receive the rotated access token")

	persisted := creds.snapshot(orgID)
	require.Equal(t, "lin_at_new", persisted.AccessToken)
	require.Equal(t, "lin_rt_new", persisted.RefreshToken, "rotated refresh token must be persisted")
	require.Equal(t, "wks-1", persisted.WorkspaceID, "workspace metadata must be preserved across refresh")
	require.Equal(t, "Acme", persisted.WorkspaceName)
	require.True(t, persisted.ExpiresAt.After(time.Now().Add(time.Hour)), "expires_at should be far in the future")
	require.EqualValues(t, 1, stub.calls.Load(), "exactly one refresh request expected")
}

// TestGetValidToken_RefreshConcurrentSerializesPerOrg asserts the
// per-org mutex actually prevents N concurrent goroutines from each
// burning a refresh-token redemption against Linear. After the first
// goroutine refreshes, the others should observe the rotated row,
// double-check inside the lock, and skip the refresh entirely.
func TestGetValidToken_RefreshConcurrentSerializesPerOrg(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	intg := newActiveLinearIntegrationStore(orgID)
	creds := newFakeCredentialStore()
	creds.configs[orgID] = models.LinearConfig{
		AccessToken:  "lin_at_old",
		RefreshToken: "lin_rt_old",
		ExpiresAt:    time.Now().Add(1 * time.Minute),
		WorkspaceID:  "wks-1",
	}
	respond := func(form url.Values) (int, string) {
		return http.StatusOK, `{"access_token":"lin_at_new","refresh_token":"lin_rt_new","token_type":"Bearer","expires_in":7200}`
	}
	stub := &stubLinearOAuthServer{respond: respond}
	svc := newRefreshTestService(t, intg, creds, stub)

	const concurrency = 16
	var wg sync.WaitGroup
	tokens := make([]string, concurrency)
	errs := make([]error, concurrency)
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func(i int) {
			defer wg.Done()
			_, tok, err := svc.GetValidToken(context.Background(), orgID)
			tokens[i] = tok
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		require.NoErrorf(t, err, "goroutine %d failed", i)
		require.Equalf(t, "lin_at_new", tokens[i], "goroutine %d got stale token", i)
	}
	require.EqualValues(t, 1, stub.calls.Load(), "concurrent callers must coalesce onto a single refresh request")
}

// TestGetValidToken_RefreshTokenRevoked covers the hard-failure path:
// Linear returns 400 invalid_grant, refresh.go must zero the persisted
// refresh token (so future calls don't loop), flip the integration row
// to errored (so the UI shows reconnect), and return ErrRefreshTokenRevoked.
func TestGetValidToken_RefreshTokenRevoked(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	intg := newActiveLinearIntegrationStore(orgID)
	creds := newFakeCredentialStore()
	creds.configs[orgID] = models.LinearConfig{
		AccessToken:  "lin_at_old",
		RefreshToken: "lin_rt_revoked",
		ExpiresAt:    time.Now().Add(1 * time.Minute),
		WorkspaceID:  "wks-1",
	}
	stub := &stubLinearOAuthServer{respond: func(form url.Values) (int, string) {
		return http.StatusBadRequest, `{"error":"invalid_grant","error_description":"refresh token has been revoked"}`
	}}
	svc := newRefreshTestService(t, intg, creds, stub)

	_, _, err := svc.GetValidToken(context.Background(), orgID)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrRefreshTokenRevoked)

	persisted := creds.snapshot(orgID)
	require.Empty(t, persisted.RefreshToken, "refresh token must be zeroed so future calls fall through to the no-refresh-token branch")
	require.Equal(t, "wks-1", persisted.WorkspaceID, "workspace metadata must survive the revocation write")

	require.NotEmpty(t, intg.statusCfgCalls, "MarkIntegrationUnauthorized should fire so the UI shows the reconnect banner")
	require.Equal(t, models.IntegrationStatusError, intg.statusCfgCalls[0].status)

	_, token, err := svc.GetValidToken(context.Background(), orgID)
	require.ErrorIs(t, err, ErrNoRefreshToken, "subsequent calls after revocation should stay in the reconnect path")
	require.Empty(t, token, "subsequent calls after revocation must not return the stale access token")
}

// TestGetValidToken_RefreshFailureWithValidCachedTokenFallsBack asserts
// that a transient refresh failure (e.g. Linear 5xx) does NOT take down
// the calling code path when the cached token is still inside its
// validity window. The next call will retry; meanwhile the worker's read
// completes with the cached token.
func TestGetValidToken_RefreshFailureWithValidCachedTokenFallsBack(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	intg := newActiveLinearIntegrationStore(orgID)
	creds := newFakeCredentialStore()
	// Inside the 5-minute refresh window but still valid, so fall-back
	// should kick in on a transient refresh failure.
	creds.configs[orgID] = models.LinearConfig{
		AccessToken:  "lin_at_old",
		RefreshToken: "lin_rt",
		ExpiresAt:    time.Now().Add(2 * time.Minute),
	}
	stub := &stubLinearOAuthServer{respond: func(form url.Values) (int, string) {
		return http.StatusInternalServerError, `{"error":"upstream"}`
	}}
	svc := newRefreshTestService(t, intg, creds, stub)

	_, token, err := svc.GetValidToken(context.Background(), orgID)
	require.NoError(t, err, "transient 5xx on refresh should fall back to the still-valid cached token")
	require.Equal(t, "lin_at_old", token)
}

// TestGetValidToken_RefreshFailureWithExpiredCachedTokenSurfacesError
// pins the harder side of the same edge case: if the cached token has
// already expired (we entered the refresh window late, or the server
// has been down longer than the validity tail), there's no safe
// fallback and the error must propagate so the caller can react.
func TestGetValidToken_RefreshFailureWithExpiredCachedTokenSurfacesError(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	intg := newActiveLinearIntegrationStore(orgID)
	creds := newFakeCredentialStore()
	creds.configs[orgID] = models.LinearConfig{
		AccessToken:  "lin_at_dead",
		RefreshToken: "lin_rt",
		ExpiresAt:    time.Now().Add(-1 * time.Minute), // already expired
	}
	stub := &stubLinearOAuthServer{respond: func(form url.Values) (int, string) {
		return http.StatusInternalServerError, `{"error":"upstream"}`
	}}
	svc := newRefreshTestService(t, intg, creds, stub)

	_, _, err := svc.GetValidToken(context.Background(), orgID)
	require.Error(t, err, "cannot fall back to an already-expired token")
	require.NotErrorIs(t, err, ErrRefreshTokenRevoked, "5xx is transient, not revocation")
}

// TestGetValidToken_OAuthClientNotConfigured asserts that a service built
// without OAuth client credentials surfaces ErrOAuthClientNotConfigured
// rather than panicking or silently returning a stale token. Triggered
// only when the credential needs refresh; healthy tokens still work.
func TestGetValidToken_OAuthClientNotConfigured(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	intg := newActiveLinearIntegrationStore(orgID)
	creds := newFakeCredentialStore()
	creds.configs[orgID] = models.LinearConfig{
		AccessToken:  "lin_at",
		RefreshToken: "lin_rt",
		ExpiresAt:    time.Now().Add(1 * time.Minute),
	}
	svc := &Service{
		logger:             zerolog.Nop(),
		integrations:       intg,
		integrationsWriter: intg,
		credentials:        creds,
		credentialsWriter:  creds,
		oauthClient:        OAuthClientCreds{}, // intentionally empty
		refreshHTTPClient:  &http.Client{},
	}

	_, _, err := svc.GetValidToken(context.Background(), orgID)
	require.ErrorIs(t, err, ErrOAuthClientNotConfigured)
}

// TestGetValidToken_NoIntegration verifies that when an org has never
// installed Linear, GetValidToken returns ErrIntegrationNotFound. The
// integration store reports pgx.ErrNoRows; loadLinearCredential maps that
// to the typed sentinel callers can branch on.
func TestGetValidToken_NoIntegration(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	intg := &fakeIntegrationStore{notFoundErr: pgx.ErrNoRows}
	creds := newFakeCredentialStore()
	svc := newRefreshTestService(t, intg, creds, nil)

	_, _, err := svc.GetValidToken(context.Background(), orgID)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrIntegrationNotFound)
}

// TestGetValidAccessToken_MissingIntegrationReturnsEmptyNilForEnvInjection
// pins the contract that env.go relies on: the convenience wrapper must
// suppress the "no integration" sentinel so callers can branch on `token == ""`
// without importing linear-specific errors.
func TestGetValidAccessToken_MissingIntegrationReturnsEmptyNilForEnvInjection(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	intg := &fakeIntegrationStore{notFoundErr: pgx.ErrNoRows}
	creds := newFakeCredentialStore()
	svc := newRefreshTestService(t, intg, creds, nil)

	token, err := svc.GetValidAccessToken(context.Background(), orgID)
	require.NoError(t, err)
	require.Empty(t, token, `"" + nil signals "no Linear installed" so env.go skips the env var`)
}

// TestGetValidAccessToken_InactiveIntegrationReturnsEmptyNilForEnvInjection
// covers the disconnect path: DisconnectIntegration leaves the credential row
// in place and flips the integration row to inactive. Env injection must treat
// that exactly like "not installed" so a user-initiated disconnect cannot keep
// refreshing and injecting LINEAR_ACCESS_TOKEN into new sandboxes.
func TestGetValidAccessToken_InactiveIntegrationReturnsEmptyNilForEnvInjection(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	intg := &fakeIntegrationStore{
		row: models.Integration{
			ID:     uuid.New(),
			OrgID:  orgID,
			Status: models.IntegrationStatusInactive,
		},
	}
	creds := newFakeCredentialStore()
	creds.configs[orgID] = models.LinearConfig{
		AccessToken:  "lin_at_disconnected",
		RefreshToken: "lin_rt_disconnected",
		ExpiresAt:    time.Now().Add(1 * time.Minute),
	}
	stub := &stubLinearOAuthServer{respond: func(form url.Values) (int, string) {
		t.Fatal("inactive integrations must not refresh or inject Linear tokens")
		return 0, ""
	}}
	svc := newRefreshTestService(t, intg, creds, stub)

	token, err := svc.GetValidAccessToken(context.Background(), orgID)
	require.NoError(t, err, "inactive integration should be suppressed for env injection")
	require.Empty(t, token, "inactive integration should not produce LINEAR_ACCESS_TOKEN")
	require.Zero(t, stub.calls.Load(), "inactive integration should not call Linear's refresh endpoint")
}

// TestGetValidToken_RaceLossesUseRotatedToken simulates the multi-node
// race: this process's refresh attempt 401s because a peer node already
// rotated, but the rotated token is now in the row. refreshLinearToken's
// post-rejection re-read should pick that up and return the peer's
// freshly-persisted token instead of declaring revocation.
//
// Note: in the current implementation the race-recovery branch only
// re-reads from the same store, so we simulate a peer-node rotation by
// having the OAuth stub mutate the store mid-flight. This is the closest
// we can get to a multi-process race in a single-process unit test.
func TestGetValidToken_RaceLossesUseRotatedToken(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	intg := newActiveLinearIntegrationStore(orgID)
	creds := newFakeCredentialStore()
	creds.configs[orgID] = models.LinearConfig{
		AccessToken:  "lin_at_old",
		RefreshToken: "lin_rt_old",
		ExpiresAt:    time.Now().Add(1 * time.Minute),
	}
	stub := &stubLinearOAuthServer{respond: func(form url.Values) (int, string) {
		// Simulate: by the time our request lands at Linear, a peer node
		// already rotated and persisted a new token. Mutate the store to
		// reflect that, then return the 4xx that would arrive for our
		// now-stale refresh token.
		creds.mu.Lock()
		creds.configs[orgID] = models.LinearConfig{
			AccessToken:  "lin_at_peer_rotated",
			RefreshToken: "lin_rt_peer_rotated",
			ExpiresAt:    time.Now().Add(2 * time.Hour),
		}
		creds.mu.Unlock()
		return http.StatusBadRequest, `{"error":"invalid_grant"}`
	}}
	svc := newRefreshTestService(t, intg, creds, stub)

	_, token, err := svc.GetValidToken(context.Background(), orgID)
	require.NoError(t, err, "race recovery should pick up the peer-rotated token")
	require.Equal(t, "lin_at_peer_rotated", token)

	require.Empty(t, intg.statusCfgCalls, "MarkIntegrationUnauthorized must NOT fire when the peer already recovered")
}

// TestForceRefreshToken_AlwaysAttemptsRefresh exercises the post-401
// recovery path: even when the cached token is well outside the refresh
// window, ForceRefreshToken still POSTs to Linear (because the worker
// just observed a 401, which means our window-based proactive logic is
// wrong about this token).
func TestForceRefreshToken_AlwaysAttemptsRefresh(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	intg := newActiveLinearIntegrationStore(orgID)
	creds := newFakeCredentialStore()
	// Token claims 2 hours of validity but the call site got a 401, so
	// force-refresh must fire regardless.
	creds.configs[orgID] = models.LinearConfig{
		AccessToken:  "lin_at_dead_despite_clock",
		RefreshToken: "lin_rt",
		ExpiresAt:    time.Now().Add(2 * time.Hour),
	}
	stub := &stubLinearOAuthServer{respond: func(form url.Values) (int, string) {
		require.Equal(t, "refresh_token", form.Get("grant_type"))
		return http.StatusOK, `{"access_token":"lin_at_forced","refresh_token":"lin_rt","expires_in":7200}`
	}}
	svc := newRefreshTestService(t, intg, creds, stub)

	token, err := svc.ForceRefreshToken(context.Background(), orgID)
	require.NoError(t, err)
	require.Equal(t, "lin_at_forced", token)
	require.EqualValues(t, 1, stub.calls.Load())
}

// TestForceRefreshToken_SkipsPOSTWhenPeerAlreadyRotated covers the
// efficiency fix: if the in-lock re-read shows the access token already
// changed under us (peer node rotated between our 401 and our lock
// acquisition), we should use the peer's token instead of POSTing our
// stale refresh token. POSTing would consume Linear's freshly-issued
// refresh token for a redundant third rotation.
func TestForceRefreshToken_SkipsPOSTWhenPeerAlreadyRotated(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	intg := newActiveLinearIntegrationStore(orgID)
	creds := newFakeCredentialStore()
	// The caller observed AccessToken "lin_at_caller_saw" before getting
	// a 401. By the time refreshLinearToken acquires the per-org lock and
	// re-reads, the store reflects a peer-node rotation to "lin_at_peer".
	creds.configs[orgID] = models.LinearConfig{
		AccessToken:  "lin_at_peer",
		RefreshToken: "lin_rt_peer",
		ExpiresAt:    time.Now().Add(2 * time.Hour),
	}
	stub := &stubLinearOAuthServer{respond: func(form url.Values) (int, string) {
		t.Fatal("must not POST when the in-lock re-read shows a peer-rotated token")
		return 0, ""
	}}
	svc := newRefreshTestService(t, intg, creds, stub)

	// Drive refreshLinearToken directly through ForceRefreshToken, but
	// pre-load `observed` with the caller's pre-rotation view so the
	// in-lock re-read genuinely diverges. ForceRefreshToken's first
	// loadLinearCredential currently sees the post-rotation row, so
	// observed.AccessToken == latest.AccessToken and the new branch
	// wouldn't fire — to actually exercise the new path we drop one
	// level and call refreshLinearToken with skipFreshCheck=true and
	// the older `observed` config.
	older := models.LinearConfig{
		AccessToken:  "lin_at_caller_saw",
		RefreshToken: "lin_rt_caller_saw",
		ExpiresAt:    time.Now().Add(-1 * time.Minute),
	}
	got, err := svc.refreshLinearToken(context.Background(), orgID, older, true /* skipFreshCheck */)
	require.NoError(t, err)
	require.Equal(t, "lin_at_peer", got.AccessToken, "must use the peer-rotated token, not POST our stale refresh token")
	require.Zero(t, stub.calls.Load(), "no /oauth/token request expected when peer already rotated")
}

// TestWithRefreshableClient_RetriesOnce_SucceedsOnSecondTry is the
// integration test for the on-401 retry loop in withRefreshableClient.
// First call returns ErrUnauthorized, force-refresh runs, second call
// succeeds. Asserts that fn() is called exactly twice and the new token
// flows through to the second client build.
func TestWithRefreshableClient_RetriesOnce_SucceedsOnSecondTry(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	intg := newActiveLinearIntegrationStore(orgID)
	creds := newFakeCredentialStore()
	creds.configs[orgID] = models.LinearConfig{
		AccessToken:  "lin_at_stale",
		RefreshToken: "lin_rt",
		ExpiresAt:    time.Now().Add(2 * time.Hour), // not in refresh window
	}
	stub := &stubLinearOAuthServer{respond: func(form url.Values) (int, string) {
		return http.StatusOK, `{"access_token":"lin_at_recovered","refresh_token":"lin_rt","expires_in":7200}`
	}}
	svc := newRefreshTestService(t, intg, creds, stub)

	tokensSeen := make([]string, 0, 2)
	svc.clientFactory = func(_ context.Context, token string) (Client, error) {
		tokensSeen = append(tokensSeen, token)
		return &fakeRetryClient{
			fail401On: len(tokensSeen) == 1, // 401 the first build only
		}, nil
	}

	calls := 0
	err := svc.withRefreshableClient(context.Background(), orgID, func(c Client) error {
		calls++
		// Trigger the same 401 path the real graphQLClient uses.
		_, err := c.ListTeamKeys(context.Background())
		return err
	})
	require.NoError(t, err, "retry should succeed")
	require.Equal(t, 2, calls, "fn must be invoked exactly twice (first 401, then success)")
	require.Equal(t, []string{"lin_at_stale", "lin_at_recovered"}, tokensSeen, "second client build must use the refreshed token")
}

// TestWithRefreshableClient_DoesNotRetryNon401 prevents accidental
// retries on errors that aren't ErrUnauthorized. A retry on, say, a 5xx
// would double a Linear API call that may already have partially
// succeeded — for reads it's harmless, but the contract is "only retry
// on 401" so the helper stays predictable for any future mutation that
// might opt in.
func TestWithRefreshableClient_DoesNotRetryNon401(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	intg := newActiveLinearIntegrationStore(orgID)
	creds := newFakeCredentialStore()
	creds.configs[orgID] = models.LinearConfig{
		AccessToken: "lin_at",
		ExpiresAt:   time.Now().Add(2 * time.Hour),
	}
	svc := newRefreshTestService(t, intg, creds, nil)

	calls := 0
	svc.clientFactory = func(_ context.Context, _ string) (Client, error) {
		return &fakeRetryClient{returnGenericErr: true}, nil
	}

	err := svc.withRefreshableClient(context.Background(), orgID, func(c Client) error {
		calls++
		_, err := c.ListTeamKeys(context.Background())
		return err
	})
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrUnauthorized)
	require.Equal(t, 1, calls, "non-401 errors must NOT trigger a retry")
}

// fakeRetryClient is the smallest possible Client surface for the retry
// tests: ListTeamKeys honors the test-controlled flags and every other
// method panics so the tests that exercise it stay focused.
type fakeRetryClient struct {
	fail401On        bool
	returnGenericErr bool
}

func (f *fakeRetryClient) FetchIssue(context.Context, string) (*FetchedIssue, error) {
	panic("not used in retry tests")
}
func (f *fakeRetryClient) FetchUser(context.Context, string) (*FetchedUser, error) {
	panic("not used in retry tests")
}
func (f *fakeRetryClient) ListTeamKeys(context.Context) ([]TeamKeyInfo, error) {
	if f.fail401On {
		return nil, ErrUnauthorized
	}
	if f.returnGenericErr {
		return nil, errors.New("graphql validation failed")
	}
	return []TeamKeyInfo{{TeamID: "t1", Key: "ABC"}}, nil
}
func (f *fakeRetryClient) CreateOrUpdateAttachment(context.Context, AttachmentWriteInput) (AttachmentResult, error) {
	panic("not used in retry tests")
}
func (f *fakeRetryClient) CreateComment(context.Context, string, string) (string, error) {
	panic("not used in retry tests")
}
func (f *fakeRetryClient) UpdateComment(context.Context, string, string) error {
	panic("not used in retry tests")
}
func (f *fakeRetryClient) FindRecentBotCommentByURL(context.Context, string, string) (string, error) {
	panic("not used in retry tests")
}
func (f *fakeRetryClient) WorkflowStateForType(context.Context, string, []string, string) (*WorkflowState, error) {
	panic("not used in retry tests")
}
func (f *fakeRetryClient) UpdateIssueState(context.Context, string, string) error {
	panic("not used in retry tests")
}
func (f *fakeRetryClient) IssueRecentHumanEdits(context.Context, string, time.Time) (bool, error) {
	panic("not used in retry tests")
}
func (f *fakeRetryClient) HasGitHubIntegrationAttachment(context.Context, string) (bool, error) {
	panic("not used in retry tests")
}
func (f *fakeRetryClient) AgentActivityCreate(context.Context, AgentActivityInput) (AgentActivityResult, error) {
	panic("not used in retry tests")
}
func (f *fakeRetryClient) AgentSessionUpdate(context.Context, AgentSessionUpdateInput) error {
	panic("not used in retry tests")
}
func (f *fakeRetryClient) AgentSessionGet(context.Context, string) (*FetchedAgentSession, error) {
	panic("not used in retry tests")
}
func (f *fakeRetryClient) FetchComment(context.Context, string) (*FetchedComment, error) {
	panic("not used in retry tests")
}

// TestMergeRefreshedConfig_PreservesMetadataAndRotatesAuth pins the
// merge contract that the rest of the refresh flow depends on:
// workspace metadata is sticky (Linear's refresh response doesn't echo
// it back), but the auth fields rotate from the response. Without this
// test, a future refactor could accidentally persist a config that
// loses workspace_id and breaks every detection that relied on it.
func TestMergeRefreshedConfig_PreservesMetadataAndRotatesAuth(t *testing.T) {
	t.Parallel()
	prior := models.LinearConfig{
		WebhookSecret: "secret",
		AccessToken:   "lin_at_old",
		RefreshToken:  "lin_rt_old",
		TokenType:     "Bearer",
		Scope:         "read,write",
		ExpiresAt:     time.Now().Add(-1 * time.Hour),
		WorkspaceID:   "wks-1",
		WorkspaceName: "Acme",
	}
	resp := linearTokenRefreshResponse{
		AccessToken:  "lin_at_new",
		RefreshToken: "lin_rt_new",
		TokenType:    "Bearer",
		Scope:        "read,write",
		ExpiresIn:    7200,
	}
	merged := mergeRefreshedConfig(prior, resp, zerolog.Nop(), uuid.New())

	require.Equal(t, "lin_at_new", merged.AccessToken)
	require.Equal(t, "lin_rt_new", merged.RefreshToken)
	require.Equal(t, "secret", merged.WebhookSecret, "webhook secret must survive the rotation")
	require.Equal(t, "wks-1", merged.WorkspaceID)
	require.Equal(t, "Acme", merged.WorkspaceName)
	require.True(t, merged.ExpiresAt.After(time.Now().Add(time.Hour)), "ExpiresAt should be ~2h in the future")
}

// TestMergeRefreshedConfig_RefreshTokenOmittedKeepsExisting covers the
// OAuth-spec edge case where a refresh response omits refresh_token to
// indicate "unchanged." We must keep the prior refresh token, not zero
// it out — otherwise the next call would lose its ability to refresh.
func TestMergeRefreshedConfig_RefreshTokenOmittedKeepsExisting(t *testing.T) {
	t.Parallel()
	prior := models.LinearConfig{
		AccessToken:  "lin_at_old",
		RefreshToken: "lin_rt_keep",
	}
	resp := linearTokenRefreshResponse{
		AccessToken: "lin_at_new",
		ExpiresIn:   7200,
		// RefreshToken intentionally empty
	}
	merged := mergeRefreshedConfig(prior, resp, zerolog.Nop(), uuid.New())
	require.Equal(t, "lin_rt_keep", merged.RefreshToken, "omitted refresh_token must NOT zero the prior one")
}

// TestMergeRefreshedConfig_ExpiresInOmittedPreservesPriorExpiry pins the
// fix for a subtle bug: an earlier version zeroed ExpiresAt when the
// refresh response omitted expires_in, silently demoting the row to
// "use until 401" mode forever (NeedsRefresh returns false on a zero
// ExpiresAt, so future proactive refreshes never fire). Now we preserve
// the prior expiry so the next refresh-window check still triggers
// correctly.
func TestMergeRefreshedConfig_ExpiresInOmittedPreservesPriorExpiry(t *testing.T) {
	t.Parallel()
	priorExpiry := time.Now().Add(2 * time.Minute) // inside refresh window
	prior := models.LinearConfig{
		AccessToken:  "lin_at_old",
		RefreshToken: "lin_rt",
		ExpiresAt:    priorExpiry,
	}
	resp := linearTokenRefreshResponse{
		AccessToken: "lin_at_new",
		// ExpiresIn intentionally zero
	}
	merged := mergeRefreshedConfig(prior, resp, zerolog.Nop(), uuid.New())
	require.True(t, priorExpiry.Equal(merged.ExpiresAt), "prior ExpiresAt must be preserved when refresh response omits expires_in")
	require.True(t, merged.NeedsRefresh(refreshWindow), "preserved-expiry row must still report NeedsRefresh so next call retries")
}

// TestGetValidToken_RefreshOAuthClientMisconfigured pins the
// most important non-blocker from review: an invalid_client response
// from /oauth/token (e.g. CLIENT_SECRET rotated upstream) MUST NOT
// zero the refresh token. The earlier any-4xx-is-revocation logic
// would have produced self-inflicted mass revocation across every
// connected org — a deploy with a bad secret bricks all installs.
func TestGetValidToken_RefreshOAuthClientMisconfigured(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	intg := newActiveLinearIntegrationStore(orgID)
	creds := newFakeCredentialStore()
	creds.configs[orgID] = models.LinearConfig{
		AccessToken:  "lin_at_old",
		RefreshToken: "lin_rt_should_survive",
		ExpiresAt:    time.Now().Add(1 * time.Minute),
	}
	stub := &stubLinearOAuthServer{respond: func(form url.Values) (int, string) {
		// Linear's response for wrong client_id/secret per RFC 6749 §5.2.
		return http.StatusUnauthorized, `{"error":"invalid_client","error_description":"client authentication failed"}`
	}}
	svc := newRefreshTestService(t, intg, creds, stub)

	_, _, err := svc.GetValidToken(context.Background(), orgID)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrOAuthClientNotConfigured, "invalid_client must surface as a config error, not a revocation")
	require.NotErrorIs(t, err, ErrRefreshTokenRevoked, "user's refresh token must NOT be classified as revoked when our own client creds are wrong")

	persisted := creds.snapshot(orgID)
	require.Equal(t, "lin_rt_should_survive", persisted.RefreshToken, "refresh token must survive an invalid_client response so the next deploy with correct creds can recover")
	require.Empty(t, intg.statusCfgCalls, "MarkIntegrationUnauthorized must NOT fire — the user did nothing wrong")
}

// TestGetValidToken_UnrecognizedOAuthErrorDoesNotRevoke covers the
// safe-default branch: a 4xx with an OAuth error code we don't know
// (e.g. invalid_request, or a Linear-specific extension) gets
// classified as a generic transient error, not revocation. The cached
// token is used as fallback if still valid — exactly the same behavior
// as a 5xx blip, which is the right cautious posture.
func TestGetValidToken_UnrecognizedOAuthErrorDoesNotRevoke(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	intg := newActiveLinearIntegrationStore(orgID)
	creds := newFakeCredentialStore()
	creds.configs[orgID] = models.LinearConfig{
		AccessToken:  "lin_at_old",
		RefreshToken: "lin_rt_should_survive",
		ExpiresAt:    time.Now().Add(2 * time.Minute), // still valid → fallback path
	}
	stub := &stubLinearOAuthServer{respond: func(form url.Values) (int, string) {
		return http.StatusBadRequest, `{"error":"unsupported_grant_type"}`
	}}
	svc := newRefreshTestService(t, intg, creds, stub)

	_, token, err := svc.GetValidToken(context.Background(), orgID)
	require.NoError(t, err, "unrecognized 4xx with cached token still valid should fall back, not error out")
	require.Equal(t, "lin_at_old", token)

	persisted := creds.snapshot(orgID)
	require.Equal(t, "lin_rt_should_survive", persisted.RefreshToken, "refresh token must survive unrecognized OAuth errors")
	require.Empty(t, intg.statusCfgCalls, "no revocation flip on unrecognized errors")
}

// TestGetValidToken_RefreshSucceeded_PersistFails_FallsBackToCachedToken
// covers the painful path described in the refreshLinearToken docstring:
// Linear rotated the tokens at their side, but our DB write fails, so
// the new refresh token is now lost server-side. Two assertions matter:
//
//  1. The caller does NOT get an error: the cached access token is
//     still valid (we only refresh inside refreshWindow), so failing
//     the call would needlessly break a session over a transient DB
//     blip.
//  2. The Upsert WAS attempted (records a hit), so a future audit
//     could correlate the metric against the failed write.
//
// The next call's behavior is documented but not tested here: it'll
// try the consumed refresh token, get invalid_grant, and the
// revocation path takes over — the system self-heals into the
// reconnect flow if the DB issue persists.
func TestGetValidToken_RefreshSucceeded_PersistFails_FallsBackToCachedToken(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	intg := newActiveLinearIntegrationStore(orgID)
	creds := newFakeCredentialStore()
	creds.upsertErr = errors.New("db connection lost")
	creds.configs[orgID] = models.LinearConfig{
		AccessToken:  "lin_at_old",
		RefreshToken: "lin_rt_old",
		ExpiresAt:    time.Now().Add(1 * time.Minute),
	}
	stub := &stubLinearOAuthServer{respond: func(form url.Values) (int, string) {
		return http.StatusOK, `{"access_token":"lin_at_new","refresh_token":"lin_rt_new","expires_in":7200}`
	}}
	svc := newRefreshTestService(t, intg, creds, stub)

	_, token, err := svc.GetValidToken(context.Background(), orgID)
	require.NoError(t, err, "transient persist failure with valid cached token should fall back, not propagate")
	require.Equal(t, "lin_at_old", token, "cached token returned because the rotated one couldn't be persisted")
	require.Equal(t, 1, creds.upsertCount(), "exactly one persist attempt expected (so the failure is observable in metrics/logs)")
}

// TestGetValidToken_RefreshSucceeded_DoesNotOverwriteReconnectedCredential
// pins the compare-and-swap requirement for refresh persistence. If a user
// reconnects Linear while this goroutine is waiting on /oauth/token, the
// refreshed pair derived from the old refresh token must not overwrite the
// newer reconnect credential.
func TestGetValidToken_RefreshSucceeded_DoesNotOverwriteReconnectedCredential(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	intg := newActiveLinearIntegrationStore(orgID)
	creds := newFakeCredentialStore()
	creds.configs[orgID] = models.LinearConfig{
		AccessToken:   "lin_at_old",
		RefreshToken:  "lin_rt_old",
		ExpiresAt:     time.Now().Add(1 * time.Minute),
		WorkspaceID:   "wks-old",
		WorkspaceName: "Old Workspace",
	}
	stub := &stubLinearOAuthServer{respond: func(form url.Values) (int, string) {
		require.Equal(t, "lin_rt_old", form.Get("refresh_token"), "refresh should start from the originally observed refresh token")
		creds.mu.Lock()
		creds.configs[orgID] = models.LinearConfig{
			AccessToken:   "lin_at_reconnected",
			RefreshToken:  "lin_rt_reconnected",
			ExpiresAt:     time.Now().Add(2 * time.Hour),
			WorkspaceID:   "wks-new",
			WorkspaceName: "Reconnected Workspace",
		}
		creds.mu.Unlock()
		return http.StatusOK, `{"access_token":"lin_at_from_old_chain","refresh_token":"lin_rt_from_old_chain","expires_in":7200}`
	}}
	svc := newRefreshTestService(t, intg, creds, stub)

	_, token, err := svc.GetValidToken(context.Background(), orgID)
	require.NoError(t, err, "refresh race with reconnect should recover by using the current row")
	require.Equal(t, "lin_at_reconnected", token, "caller should receive the newer reconnected token, not the old token chain")

	persisted := creds.snapshot(orgID)
	require.Equal(t, "lin_at_reconnected", persisted.AccessToken, "refresh must not overwrite a reconnected access token")
	require.Equal(t, "lin_rt_reconnected", persisted.RefreshToken, "refresh must not overwrite a reconnected refresh token")
	require.Equal(t, "wks-new", persisted.WorkspaceID, "refresh must not restore stale workspace metadata")
	require.Equal(t, 1, creds.upsertCount(), "refresh should attempt one guarded persistence")
}

// TestMarkRefreshTokenRevoked_PersistFailureStillFlipsIntegration
// covers the operational concern that even a flaky credentials writer
// must not block the integration row from being flipped to errored —
// otherwise the UI would show "active" indefinitely while every API
// call 401'd.
func TestMarkRefreshTokenRevoked_PersistFailureStillFlipsIntegration(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	intg := newActiveLinearIntegrationStore(orgID)
	creds := newFakeCredentialStore()
	creds.upsertErr = errors.New("db write failed")
	creds.configs[orgID] = models.LinearConfig{
		AccessToken:  "lin_at_old",
		RefreshToken: "lin_rt_revoked",
		ExpiresAt:    time.Now().Add(1 * time.Minute),
	}
	stub := &stubLinearOAuthServer{respond: func(form url.Values) (int, string) {
		return http.StatusBadRequest, `{"error":"invalid_grant"}`
	}}
	svc := newRefreshTestService(t, intg, creds, stub)

	_, _, err := svc.GetValidToken(context.Background(), orgID)
	require.ErrorIs(t, err, ErrRefreshTokenRevoked, "revocation classification must survive the credential write failure")

	require.NotEmpty(t, intg.statusCfgCalls, "integration row must be flipped to errored even when zeroing the refresh token failed — otherwise the UI banner never shows")
	require.Equal(t, models.IntegrationStatusError, intg.statusCfgCalls[0].status)
}
