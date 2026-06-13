package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
)

func newCLIAuthTestHandler(t *testing.T, mock pgxmock.PgxPoolIface) *AuthHandler {
	t.Helper()
	cfg := &config.Config{
		GitHubOAuthClientID:    "client-id",
		BaseURL:                "http://localhost:8080",
		FrontendURL:            "http://frontend.test",
		GitHubOAuthRedirectURI: "http://localhost:8080/api/v1/auth/github/callback",
	}
	var pool db.TxStarter
	var userStore *db.UserStore
	var sessionStore *db.AuthSessionStore
	var authCodes *db.CLIAuthCodeStore
	var cliTokens *db.UserCLITokenStore
	var joinTokens *db.OrgJoinTokenStore
	if mock != nil {
		pool = mock
		userStore = db.NewUserStore(mock)
		sessionStore = db.NewAuthSessionStore(mock)
		authCodes = db.NewCLIAuthCodeStore(mock)
		cliTokens = db.NewUserCLITokenStore(mock)
		joinTokens = db.NewOrgJoinTokenStore(mock)
	}
	h := NewAuthHandler(cfg, pool, userStore, sessionStore, nil, nil)
	h.SetCLIAuthStores(authCodes, cliTokens, joinTokens, nil)
	return h
}

func TestCLIStartValidatesInputsAndSetsCookies(t *testing.T) {
	t.Parallel()
	h := newCLIAuthTestHandler(t, nil)

	challenge := strings.Repeat("ab", 32) // 64 hex chars
	target := "/api/v1/auth/cli/start?port=51234&challenge=" + challenge +
		"&device=my-laptop&join=143j_Ab3x9kQ2mP4r"
	w := httptest.NewRecorder()
	h.CLIStart(w, httptest.NewRequest(http.MethodGet, target, nil))

	require.Equal(t, http.StatusTemporaryRedirect, w.Code)
	require.Contains(t, w.Header().Get("Location"), "github.com/login/oauth/authorize",
		"cli/start should chain into the GitHub OAuth login redirect")

	cookies := map[string]string{}
	for _, c := range w.Result().Cookies() {
		cookies[c.Name] = c.Value
		if c.Name == "cli_port" || c.Name == "cli_challenge" || c.Name == "pending_join" {
			require.True(t, c.HttpOnly, "%s cookie must be HttpOnly", c.Name)
		}
	}
	require.Equal(t, "51234", cookies["cli_port"])
	require.Equal(t, challenge, cookies["cli_challenge"])
	require.Equal(t, "my-laptop", cookies["cli_device"])
	require.Equal(t, "143j_Ab3x9kQ2mP4r", cookies["pending_join"])
}

func TestCLIStartRejectsBadInputs(t *testing.T) {
	t.Parallel()
	h := newCLIAuthTestHandler(t, nil)
	challenge := strings.Repeat("ab", 32)

	cases := []struct {
		name  string
		query string
		code  string
	}{
		{"missing port", "challenge=" + challenge, "INVALID_CLI_PORT"},
		{"privileged port", "port=80&challenge=" + challenge, "INVALID_CLI_PORT"},
		{"port out of range", "port=70000&challenge=" + challenge, "INVALID_CLI_PORT"},
		{"missing challenge", "port=51234", "INVALID_CLI_CHALLENGE"},
		{"short challenge", "port=51234&challenge=abcd", "INVALID_CLI_CHALLENGE"},
		{"non-hex challenge", "port=51234&challenge=" + strings.Repeat("zz", 32), "INVALID_CLI_CHALLENGE"},
		{"malformed join token", "port=51234&challenge=" + challenge + "&join=not-a-token", "INVALID_JOIN_TOKEN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			h.CLIStart(w, httptest.NewRequest(http.MethodGet, "/api/v1/auth/cli/start?"+tc.query, nil))
			require.Equal(t, http.StatusBadRequest, w.Code)
			require.Contains(t, w.Body.String(), tc.code)
		})
	}
}

func TestCLIStartRefusesWhenGitHubOAuthUnavailable(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		cfg  *config.Config
	}{
		{"demo mode", &config.Config{DemoMode: true, GitHubOAuthClientID: "client-id"}},
		{"no oauth app", &config.Config{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := NewAuthHandler(tc.cfg, nil, nil, nil, nil, nil)
			w := httptest.NewRecorder()
			h.CLIStart(w, httptest.NewRequest(http.MethodGet, "/api/v1/auth/cli/start?port=51234&challenge="+strings.Repeat("ab", 32), nil))
			require.Equal(t, http.StatusConflict, w.Code)
			require.Contains(t, w.Body.String(), "GITHUB_OAUTH_DISABLED")
		})
	}
}

func TestReadAndClearCLILoginIntentRevalidatesCookieValues(t *testing.T) {
	t.Parallel()

	challenge := strings.Repeat("cd", 32)
	cases := []struct {
		name    string
		cookies map[string]string
		wantNil bool
	}{
		{"valid", map[string]string{"cli_port": "52000", "cli_challenge": challenge, "cli_device": "laptop"}, false},
		{"no cookies", map[string]string{}, true},
		{"port tampered", map[string]string{"cli_port": "80", "cli_challenge": challenge}, true},
		{"challenge tampered", map[string]string{"cli_port": "52000", "cli_challenge": "zz"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodGet, "/callback", nil)
			for name, value := range tc.cookies {
				r.AddCookie(&http.Cookie{Name: name, Value: value})
			}
			w := httptest.NewRecorder()
			intent := readAndClearCLILoginIntent(w, r)
			if tc.wantNil {
				require.Nil(t, intent)
				return
			}
			require.NotNil(t, intent)
			require.Equal(t, 52000, intent.port)
			require.Equal(t, challenge, intent.challenge)
			require.Equal(t, "laptop", intent.device)
			require.Equal(t, "http://127.0.0.1:52000/callback?code=x", intent.loopbackURL("code=x"))
		})
	}
}

func TestCLIExchangeRejectsInvalidBody(t *testing.T) {
	t.Parallel()
	h := newCLIAuthTestHandler(t, newPgxMock(t))

	for _, body := range []string{``, `{}`, `{"code":"x"}`, `{"verifier":"y"}`} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/cli/exchange", strings.NewReader(body))
		h.CLIExchange(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code, "body %q", body)
		require.Contains(t, w.Body.String(), "INVALID_BODY")
	}
}

func TestCLIExchangeExpiredCodeReturns410(t *testing.T) {
	t.Parallel()
	mock := newPgxMock(t)
	h := newCLIAuthTestHandler(t, mock)

	// Consume's guarded UPDATE matches no rows for unknown/expired/used codes.
	mock.ExpectQuery(`UPDATE cli_auth_codes SET consumed_at = now\(\)`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(cliAuthCodeTestColumns()))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/cli/exchange",
		strings.NewReader(`{"code":"deadbeef","verifier":"v"}`))
	h.CLIExchange(w, req)

	require.Equal(t, http.StatusGone, w.Code)
	require.Contains(t, w.Body.String(), "CLI_CODE_EXPIRED")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCLIExchangeVerifierMismatchReturns400(t *testing.T) {
	t.Parallel()
	mock := newPgxMock(t)
	h := newCLIAuthTestHandler(t, mock)

	rightVerifier := "right-verifier"
	sum := sha256.Sum256([]byte(rightVerifier))
	challenge := hex.EncodeToString(sum[:])

	rows := pgxmock.NewRows(cliAuthCodeTestColumns()).AddRow(
		uuid.New(), "code-hash", challenge, uuid.New(), nil, "laptop",
		time.Now().Add(time.Minute), nil, time.Now(),
	)
	mock.ExpectQuery(`UPDATE cli_auth_codes SET consumed_at = now\(\)`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(rows)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/cli/exchange",
		strings.NewReader(`{"code":"deadbeef","verifier":"wrong-verifier"}`))
	h.CLIExchange(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "CLI_VERIFIER_MISMATCH")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestCallbackCLIBranchRedirectsToLoopbackWithCode runs the OAuth callback
// for an existing GitHub user carrying CLI-intent cookies and asserts the
// browser is sent to the CLI's loopback listener with a one-time code —
// never a credential.
func TestCallbackCLIBranchRedirectsToLoopbackWithCode(t *testing.T) {
	t.Parallel()
	mock := newPgxMock(t)
	h := newCLIAuthTestHandler(t, mock)

	userID := uuid.New()
	orgID := uuid.New()
	now := time.Now()

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			_, _ = w.Write([]byte(`{"access_token":"ghu_token","token_type":"bearer","scope":"user:email"}`))
		case "/user":
			_, _ = w.Write([]byte(`{"id":42,"login":"octocat","name":"Octo Cat","email":"octo@example.com","avatar_url":"https://example.com/a.png"}`))
		case "/user/emails":
			_, _ = w.Write([]byte(`[{"email":"42+octocat@users.noreply.github.com","verified":true}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer github.Close()
	h.SetGitHubURLsForTest(github.URL, github.URL, github.Client())

	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login",
		"github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}

	// 1. Existing-user lookup by GitHub ID.
	mock.ExpectQuery(`(?s)SELECT .+ FROM users\s+WHERE github_id = @github_id`).
		WithArgs(int64(42)).
		WillReturnRows(pgxmock.NewRows(userColumns).AddRow(
			userID, orgID, "octo@example.com", "Octo Cat", "member", ptrTo(int64(42)), ptrTo("octocat"),
			nil, nil, nil, nil, now,
		))
	// 2. Profile refresh upsert.
	mock.ExpectQuery(`INSERT INTO users`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(userID, now))
	// 3. Web session mint (persistSessionTx): last-org hint + session insert.
	expectUserLastOrgLookup(mock, userID, nil)
	mock.ExpectQuery("INSERT INTO auth_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(uuid.New(), now))
	// 4. resolveCLILoginOrg consults the last-org hint again.
	expectUserLastOrgLookup(mock, userID, nil)
	// 5. Auth-code insert (preceded by the opportunistic GC delete).
	mock.ExpectExec(`DELETE FROM cli_auth_codes`).
		WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectQuery(`INSERT INTO cli_auth_codes`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(cliAuthCodeTestColumns()).AddRow(
			uuid.New(), "hash", strings.Repeat("ab", 32), userID, nil, "laptop",
			now.Add(time.Minute), nil, now,
		))

	challenge := strings.Repeat("ab", 32)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/callback?code=gh-code&state=state-1", nil)
	req.AddCookie(&http.Cookie{Name: "github_oauth_state", Value: "state-1"})
	req.AddCookie(&http.Cookie{Name: "cli_port", Value: "52123"})
	req.AddCookie(&http.Cookie{Name: "cli_challenge", Value: challenge})
	req.AddCookie(&http.Cookie{Name: "cli_device", Value: "laptop"})
	w := httptest.NewRecorder()

	h.Callback(w, req)

	require.Equal(t, http.StatusTemporaryRedirect, w.Code, "body: %s", w.Body.String())
	location, err := url.Parse(w.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:52123", location.Host, "must redirect to the hardcoded loopback host")
	require.Equal(t, "/callback", location.Path)
	code := location.Query().Get("code")
	require.NotEmpty(t, code, "loopback redirect must carry the one-time code")
	require.NotContains(t, w.Header().Get("Location"), "143u_", "the browser must never see a real token")

	// The web session cookie is still installed alongside the CLI handoff.
	var sawSession bool
	for _, c := range w.Result().Cookies() {
		if c.Name == "session_token" && c.Value != "" {
			sawSession = true
		}
	}
	require.True(t, sawSession, "CLI login should also sign the user into the web app")
	require.NoError(t, mock.ExpectationsWereMet())
}

func newPgxMock(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	t.Cleanup(mock.Close)
	return mock
}

func ptrTo[T any](v T) *T { return &v }

func cliAuthCodeTestColumns() []string {
	return []string{"id", "code_hash", "challenge", "user_id", "org_id",
		"device_name", "expires_at", "consumed_at", "created_at"}
}

// TestCallbackCLIBranchDomainAutoJoinRedirectsToLoopback pins the merge
// interaction between CLI login and domain capture: a brand-new user at a
// captured-domain org running `143-tools login` as their very first
// sign-in must finish on the CLI loopback handshake, not get bounced to
// the web app while their terminal hangs.
func TestCallbackCLIBranchDomainAutoJoinRedirectsToLoopback(t *testing.T) {
	t.Parallel()
	mock := newPgxMock(t)
	h := newCLIAuthTestHandler(t, mock)
	h.SetOrgDomainStore(db.NewOrganizationDomainStore(mock))

	userID := uuid.New()
	orgID := uuid.New()
	now := time.Now()

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			_, _ = w.Write([]byte(`{"access_token":"ghu_token","token_type":"bearer","scope":"user:email"}`))
		case "/user":
			_, _ = w.Write([]byte(`{"id":42,"login":"alicehub","name":"Alice Hub","email":"alice@assembledhq.com","avatar_url":""}`))
		case "/user/emails":
			_, _ = w.Write([]byte(`[{"email":"alice@assembledhq.com","primary":true,"verified":true}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer github.Close()
	h.SetGitHubURLsForTest(github.URL, github.URL, github.Client())

	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login",
		"github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}

	// Brand-new user: no GitHub-id match, no email match.
	mock.ExpectQuery(`(?s)SELECT .+ FROM users\s+WHERE github_id = @github_id`).
		WithArgs(int64(42)).
		WillReturnRows(pgxmock.NewRows(userColumns))
	mock.ExpectQuery(`(?s)SELECT .+ FROM users WHERE LOWER\(email\) = LOWER\(@email\)`).
		WithArgs("alice@assembledhq.com").
		WillReturnRows(pgxmock.NewRows(userColumns))
	// Domain lookup: once in selectGitHubAutoJoinEmail, once in tryDomainAutoJoin.
	for range 2 {
		mock.ExpectQuery("SELECT d.org_id, o.name AS org_name, d.domain").
			WithArgs("assembledhq.com").
			WillReturnRows(pgxmock.NewRows([]string{"org_id", "org_name", "domain"}).
				AddRow(orgID, "Assembled", "assembledhq.com"))
	}
	// createAutoJoinUser transaction.
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO users .* ON CONFLICT .* RETURNING id, created_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(userID, now))
	mock.ExpectQuery("INSERT INTO organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow("member"))
	mock.ExpectExec("UPDATE users SET email_verified_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE users SET last_org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	expectUserLastOrgLookup(mock, userID, &orgID)
	mock.ExpectQuery("INSERT INTO auth_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(uuid.New(), now))
	mock.ExpectCommit()
	// finishCLILoginWithSession: resolveCLILoginOrg + auth-code mint.
	expectUserLastOrgLookup(mock, userID, &orgID)
	mock.ExpectExec(`DELETE FROM cli_auth_codes`).
		WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectQuery(`INSERT INTO cli_auth_codes`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(cliAuthCodeTestColumns()).AddRow(
			uuid.New(), "hash", strings.Repeat("ab", 32), userID, &orgID, "laptop",
			now.Add(time.Minute), nil, now,
		))

	challenge := strings.Repeat("ab", 32)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/callback?code=gh-code&state=state-1", nil)
	req.AddCookie(&http.Cookie{Name: "github_oauth_state", Value: "state-1"})
	req.AddCookie(&http.Cookie{Name: "cli_port", Value: "52123"})
	req.AddCookie(&http.Cookie{Name: "cli_challenge", Value: challenge})
	req.AddCookie(&http.Cookie{Name: "cli_device", Value: "laptop"})
	w := httptest.NewRecorder()

	h.Callback(w, req)

	require.Equal(t, http.StatusTemporaryRedirect, w.Code, "body: %s", w.Body.String())
	location, err := url.Parse(w.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:52123", location.Host,
		"a CLI-initiated captured-domain signup must finish on the loopback handshake, not the web redirect")
	require.NotEmpty(t, location.Query().Get("code"))
	require.NoError(t, mock.ExpectationsWereMet())
}
