package pagerduty

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type fakeCredReader struct {
	cred *models.DecryptedCredential
	err  error
	hits int
}

func (f *fakeCredReader) GetByID(ctx context.Context, orgID, id uuid.UUID) (*models.DecryptedCredential, error) {
	f.hits++
	return f.cred, f.err
}

type fakeCASWriter struct {
	persisted models.PagerDutyConfig
	calls     int
	updated   bool
}

func (f *fakeCASWriter) UpdatePagerDutyConfigByIDIfRefreshTokenMatches(ctx context.Context, orgID, credentialID uuid.UUID, expectedRefreshToken string, cfg models.PagerDutyConfig) (models.PagerDutyConfig, bool, error) {
	f.calls++
	f.persisted = cfg
	f.updated = true
	return cfg, true, nil
}

func newTestTokenService(t *testing.T, handler http.HandlerFunc, reader pagerDutyCredentialByIDReader, writer pagerDutyCredentialCASWriter) (*TokenService, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	svc := NewTokenService(reader, writer, PagerDutyOAuthClientCreds{ClientID: "cid", ClientSecret: "secret"}, zerolog.Nop())
	svc.tokenURL = srv.URL
	return svc, srv
}

func TestEnsureFresh_NoRefreshWhenTokenHealthy(t *testing.T) {
	t.Parallel()
	reader := &fakeCredReader{}
	writer := &fakeCASWriter{}
	called := false
	svc, _ := newTestTokenService(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}, reader, writer)

	observed := models.PagerDutyConfig{AccessToken: "tok", RefreshToken: "ref", ExpiresAt: time.Now().Add(time.Hour)}
	got, err := svc.EnsureFresh(context.Background(), uuid.New(), uuid.New(), observed)
	require.NoError(t, err)
	require.Equal(t, "tok", got.AccessToken, "healthy token should be returned unchanged")
	require.False(t, called, "no token endpoint call should be made for a healthy token")
	require.Equal(t, 0, writer.calls, "no persistence should happen")
}

func TestEnsureFresh_RefreshesAndPersists(t *testing.T) {
	t.Parallel()
	credID := uuid.New()
	orgID := uuid.New()
	observed := models.PagerDutyConfig{AccessToken: "old", RefreshToken: "ref-old", ServiceRegion: "eu", ExpiresAt: time.Now().Add(time.Minute)}
	reader := &fakeCredReader{cred: &models.DecryptedCredential{Config: observed}}
	writer := &fakeCASWriter{}
	svc, _ := newTestTokenService(t, func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		require.Equal(t, "refresh_token", r.Form.Get("grant_type"))
		require.Equal(t, "ref-old", r.Form.Get("refresh_token"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new","refresh_token":"ref-new","token_type":"Bearer","expires_in":86400}`))
	}, reader, writer)

	got, err := svc.EnsureFresh(context.Background(), orgID, credID, observed)
	require.NoError(t, err)
	require.Equal(t, "new", got.AccessToken, "rotated access token should be returned")
	require.Equal(t, "ref-new", got.RefreshToken, "rotated refresh token should be returned")
	require.Equal(t, "eu", got.ServiceRegion, "region must be preserved across refresh")
	require.Equal(t, 1, writer.calls, "refreshed token should be persisted once")
	require.Equal(t, "new", writer.persisted.AccessToken)
	require.False(t, got.NeedsRefresh(pagerDutyRefreshWindow), "refreshed token should no longer be near expiry")
}

func TestEnsureFresh_RevokedRefreshToken(t *testing.T) {
	t.Parallel()
	observed := models.PagerDutyConfig{AccessToken: "old", RefreshToken: "ref-old", ExpiresAt: time.Now().Add(time.Minute)}
	reader := &fakeCredReader{cred: &models.DecryptedCredential{Config: observed}}
	writer := &fakeCASWriter{}
	svc, _ := newTestTokenService(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}, reader, writer)

	_, err := svc.EnsureFresh(context.Background(), uuid.New(), uuid.New(), observed)
	require.ErrorIs(t, err, ErrPagerDutyRefreshTokenRevoked)
	require.Equal(t, 0, writer.calls, "a revoked refresh token must not persist anything")
}

func TestEnsureFresh_NoRefreshTokenIsNoop(t *testing.T) {
	t.Parallel()
	observed := models.PagerDutyConfig{AccessToken: "old", ExpiresAt: time.Now().Add(time.Minute)}
	svc, _ := newTestTokenService(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("token endpoint should not be called without a refresh token")
	}, &fakeCredReader{}, &fakeCASWriter{})

	got, err := svc.EnsureFresh(context.Background(), uuid.New(), uuid.New(), observed)
	require.NoError(t, err)
	require.Equal(t, "old", got.AccessToken, "without a refresh token the observed token is returned as-is")
}
