package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/claudecodeauth"
	"github.com/assembledhq/143/internal/services/codexauth"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestNewRouter_EncryptionKeyValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		masterKey string
		expectErr bool
	}{
		{
			name:      "returns error when encryption key is too short",
			masterKey: "short",
			expectErr: true,
		},
		{
			name:      "allows startup when encryption key is unset",
			masterKey: "",
			expectErr: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{EncryptionMasterKey: tt.masterKey}
			codexSvc := codexauth.NewService(nil, zerolog.Nop())
			claudeSvc := claudecodeauth.NewService(nil, zerolog.Nop())
			router, _, _, _, _, err := NewRouter(cfg, nil, zerolog.Nop(), nil, codexSvc, claudeSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			if tt.expectErr {
				require.Error(t, err, "NewRouter should return an error when encryption key is invalid")
				require.Nil(t, router, "NewRouter should not construct a router with an invalid encryption key")
				return
			}

			require.NoError(t, err, "NewRouter should not return an error when encryption key is unset")
			require.NotNil(t, router, "NewRouter should construct a router when encryption key is unset")
		})
	}
}

func TestSessionThreadPatchRouteIsBuilderWorkflowOnly(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile("router.go")
	require.NoError(t, err, "router.go should be readable for route grouping regression test")

	readGroupStart := strings.Index(string(source), `RequireRole("admin", "builder", "member", "viewer")`)
	builderGroupStart := strings.Index(string(source), `RequireRole("admin", "builder", "member")`)
	memberWriteGroupStart := strings.Index(string(source), `RequireRole("admin", "member")`)
	patchRoute := strings.Index(string(source), `r.Patch("/api/v1/sessions/{id}/threads/{tid}", sessionThreadHandler.UpdateThread)`)

	require.NotEqual(t, -1, readGroupStart, "router should still define an all-roles readable route group")
	require.NotEqual(t, -1, builderGroupStart, "router should still define a builder workflow route group")
	require.NotEqual(t, -1, memberWriteGroupStart, "router should still define an admin/member-only route group")
	require.NotEqual(t, -1, patchRoute, "thread update PATCH route should be registered")
	require.Greater(t, patchRoute, builderGroupStart, "thread update PATCH route should live in the builder workflow group")
	require.Less(t, patchRoute, memberWriteGroupStart, "thread update PATCH route should not be widened into the admin/member-only settings group")
	require.Greater(t, builderGroupStart, readGroupStart, "builder workflow route group should follow the all-roles readable group")
}

func TestNewRouter_WiresLinearWebhookSigningSecret(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile("router.go")
	require.NoError(t, err, "router.go should be readable for webhook wiring regression test")

	handlerConstruction := strings.Index(string(source), `handlers.NewIngestionWebhookHandler`)
	require.NotEqual(t, -1, handlerConstruction, "router should construct the ingestion webhook handler")

	setRequireSecret := strings.Index(string(source), `ingestionWebhookHandler.SetRequireSecret(cfg.Env == "production")`)
	setGlobalSecret := strings.Index(string(source), `ingestionWebhookHandler.SetGlobalLinearWebhookSecret(cfg.LinearWebhookSigningSecret)`)

	require.NotEqual(t, -1, setRequireSecret, "router should still wire production signature enforcement")
	require.NotEqual(t, -1, setGlobalSecret, "router should wire LINEAR_WEBHOOK_SIGNING_SECRET into Linear webhook verification")
	require.Greater(t, setGlobalSecret, handlerConstruction, "global Linear webhook secret should be set after handler construction")
	require.Greater(t, setGlobalSecret, setRequireSecret, "global Linear webhook secret wiring should live with the webhook verification setup")
}

func testRouterPrivateKeyPEM(t *testing.T) string {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err, "rsa key generation should not return an error")

	block := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}
	return string(pem.EncodeToMemory(block))
}

func TestNewRouter_GitHubAppConfigBuildsRouter(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		GitHubAppID:         143,
		GitHubAppPrivateKey: testRouterPrivateKeyPEM(t),
	}
	codexSvc := codexauth.NewService(nil, zerolog.Nop())
	claudeSvc := claudecodeauth.NewService(nil, zerolog.Nop())

	router, _, _, _, _, err := NewRouter(cfg, nil, zerolog.Nop(), nil, codexSvc, claudeSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err, "NewRouter should build successfully when GitHub App credentials are valid")
	require.NotNil(t, router, "NewRouter should construct a router when GitHub App credentials are valid")
}

func TestNewRouter_WithRedisWiringBuildsRouter(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	codexSvc := codexauth.NewService(nil, zerolog.Nop())
	claudeSvc := claudecodeauth.NewService(nil, zerolog.Nop())

	router, _, _, _, _, err := NewRouter(cfg, nil, zerolog.Nop(), nil, codexSvc, claudeSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, &cache.Client{}, &cache.SessionStreams{})
	require.NoError(t, err, "router construction should accept optional Redis dependencies")
	require.NotNil(t, router, "router should still be constructed with Redis wiring enabled")
}

func TestResolveRouterCodingCredentialStoreUsesSharedStore(t *testing.T) {
	t.Parallel()

	shared := db.NewCodingCredentialStore(nil, nil)
	got := resolveRouterCodingCredentialStore(nil, nil, shared)

	require.Same(t, shared, got, "router should reuse the process-level coding credential store when one is supplied")
}

func TestResolveRouterCodingCredentialStoreCreatesFallback(t *testing.T) {
	t.Parallel()

	got := resolveRouterCodingCredentialStore(nil, nil, nil)

	require.NotNil(t, got, "router should create a coding credential store when no shared store is supplied")
}

func TestNewRouter_BuildsWithoutOptionalReviewWiring(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	codexSvc := codexauth.NewService(nil, zerolog.Nop())
	claudeSvc := claudecodeauth.NewService(nil, zerolog.Nop())

	router, _, _, _, _, err := NewRouter(cfg, nil, zerolog.Nop(), nil, codexSvc, claudeSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err, "NewRouter should still construct the router without session-review wiring")
	require.NotNil(t, router, "NewRouter should still construct the router without session-review wiring")
}

func TestNewRouter_InternalPreviewRoutesSkipGlobalBodyLimit(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Mode:          "worker",
		NodeID:        "worker-a",
		SessionSecret: "test-session-secret",
	}
	codexSvc := codexauth.NewService(nil, zerolog.Nop())
	claudeSvc := claudecodeauth.NewService(nil, zerolog.Nop())

	router, _, _, _, _, err := NewRouter(cfg, nil, zerolog.Nop(), nil, codexSvc, claudeSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err, "NewRouter should build successfully for worker preview route tests")
	require.NotNil(t, router, "NewRouter should construct a router for worker preview route tests")

	req := httptest.NewRequest(http.MethodPost, "/internal/preview/start", bytes.NewReader(bytes.Repeat([]byte("a"), 2<<20)))
	req.RemoteAddr = "10.0.0.10:1234"
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code, "internal preview routes should reach preview auth instead of the global body limit")
}

func TestNewRouter_InternalPreviewRoutesSkipGlobalRateLimit(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Mode:          "worker",
		NodeID:        "worker-a",
		SessionSecret: "test-session-secret",
	}
	codexSvc := codexauth.NewService(nil, zerolog.Nop())
	claudeSvc := claudecodeauth.NewService(nil, zerolog.Nop())

	router, _, _, _, _, err := NewRouter(cfg, nil, zerolog.Nop(), nil, codexSvc, claudeSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err, "NewRouter should build successfully for worker preview route tests")
	require.NotNil(t, router, "NewRouter should construct a router for worker preview route tests")

	for i := 0; i < 25; i++ {
		req := httptest.NewRequest(http.MethodPost, "/internal/preview/start", http.NoBody)
		req.RemoteAddr = "10.0.0.10:1234"
		rr := httptest.NewRecorder()

		router.ServeHTTP(rr, req)

		require.Equal(t, http.StatusUnauthorized, rr.Code, "internal preview routes should bypass the global IP limiter on every request")
	}
}

// repoRowCols is the column list LinearAgent bootstrap tests use when mocking
// the repositories table. Kept in one place so reordering models.Repository
// doesn't silently desync the test fixtures.
var repoRowCols = []string{
	"id", "org_id", "integration_id", "github_id", "full_name", "default_branch",
	"private", "language", "description", "clone_url", "installation_id", "status",
	"last_synced_at", "context_quality", "settings", "created_at", "updated_at",
}

func repoRow(id, orgID uuid.UUID, fullName string) []any {
	now := time.Now().UTC()
	return []any{
		id, orgID, uuid.New(), int64(1), fullName, "main",
		false, nil, nil, "https://github.com/" + fullName + ".git", int64(1), "active",
		nil, nil, json.RawMessage(`{}`), now, now,
	}
}

// TestLinearAgentBootstrapAdapter_AutoEnablesWhenUnset pins the convenience
// default: on a fresh install (Enabled == nil) the adapter flips it to true.
// Re-auth that hits this path again is idempotent — the second pass sees
// Enabled = &true and leaves it alone.
func TestLinearAgentBootstrapAdapter_AutoEnablesWhenUnset(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	now := time.Now().UTC()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	// First call: settings have no LinearAgent at all. Expect a SELECT, a
	// repo list returning 0 rows (so DefaultRepoID stays nil), and an
	// UPDATE that flips Enabled to true.
	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "Acme", json.RawMessage(`{}`), now, now))
	mock.ExpectQuery("SELECT .+ FROM repositories").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(repoRowCols))
	var capturedSettings []byte
	mock.ExpectExec("UPDATE organizations SET settings").
		WithArgs(capturingArgRouter(&capturedSettings), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	adapter := linearAgentBootstrapAdapter{
		orgs:   db.NewOrganizationStore(mock),
		repos:  db.NewRepositoryStore(mock),
		logger: zerolog.Nop(),
	}
	require.NoError(t, adapter.Bootstrap(context.Background(), orgID))

	var got models.OrgSettings
	require.NoError(t, json.Unmarshal(capturedSettings, &got))
	require.NotNil(t, got.LinearAgent.Enabled, "Enabled must be set so future calls treat the choice as explicit and idempotent")
	require.True(t, *got.LinearAgent.Enabled, "fresh install should auto-enable the inbound agent")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestLinearAgentBootstrapAdapter_PreservesExplicitDisable proves that an
// admin who turned the agent off does not get retro-enabled by a later
// re-authorize. The pointer-vs-zero distinction is load-bearing here: nil
// means "no opinion", &false means "the admin said no".
func TestLinearAgentBootstrapAdapter_PreservesExplicitDisable(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	now := time.Now().UTC()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	priorSettings := json.RawMessage(`{"linear_agent":{"enabled":false}}`)
	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "Acme", priorSettings, now, now))
	mock.ExpectQuery("SELECT .+ FROM repositories").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(repoRowCols))
	// No UPDATE expected — nothing mutated, so no DB write should fire.

	adapter := linearAgentBootstrapAdapter{
		orgs:   db.NewOrganizationStore(mock),
		repos:  db.NewRepositoryStore(mock),
		logger: zerolog.Nop(),
	}
	require.NoError(t, adapter.Bootstrap(context.Background(), orgID))
	require.NoError(t, mock.ExpectationsWereMet(), "no UPDATE should fire when neither Enabled nor DefaultRepoID changed")
}

// TestLinearAgentBootstrapAdapter_AutoPicksSoleRepoAsDefault pins the
// "easy setup" affordance for the team→repo mapping: when the org has
// exactly one connected GitHub repo, that repo becomes DefaultRepoID so
// the agent works on the very first assignment without any manual
// mapping configuration.
func TestLinearAgentBootstrapAdapter_AutoPicksSoleRepoAsDefault(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	now := time.Now().UTC()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "Acme", json.RawMessage(`{}`), now, now))
	rows := pgxmock.NewRows(repoRowCols).AddRow(repoRow(repoID, orgID, "acme/web")...)
	mock.ExpectQuery("SELECT .+ FROM repositories").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(rows)
	var capturedSettings []byte
	mock.ExpectExec("UPDATE organizations SET settings").
		WithArgs(capturingArgRouter(&capturedSettings), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	adapter := linearAgentBootstrapAdapter{
		orgs:   db.NewOrganizationStore(mock),
		repos:  db.NewRepositoryStore(mock),
		logger: zerolog.Nop(),
	}
	require.NoError(t, adapter.Bootstrap(context.Background(), orgID))

	var got models.OrgSettings
	require.NoError(t, json.Unmarshal(capturedSettings, &got))
	require.NotNil(t, got.LinearAgent.DefaultRepoID, "single-repo org should auto-set DefaultRepoID")
	require.Equal(t, repoID, *got.LinearAgent.DefaultRepoID, "DefaultRepoID must point at the lone connected repo")
	require.NotNil(t, got.LinearAgent.Enabled, "Enabled flip should still happen alongside the default-repo set")
	require.True(t, *got.LinearAgent.Enabled)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestLinearAgentBootstrapAdapter_SkipsDefaultRepoWhenMultipleRepos asserts
// the no-surprise rule: with 2+ repos, the adapter refuses to guess and
// leaves DefaultRepoID nil. Admins pick explicitly in that case.
func TestLinearAgentBootstrapAdapter_SkipsDefaultRepoWhenMultipleRepos(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	now := time.Now().UTC()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "Acme", json.RawMessage(`{}`), now, now))
	rows := pgxmock.NewRows(repoRowCols).
		AddRow(repoRow(uuid.New(), orgID, "acme/web")...).
		AddRow(repoRow(uuid.New(), orgID, "acme/api")...)
	mock.ExpectQuery("SELECT .+ FROM repositories").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(rows)
	var capturedSettings []byte
	mock.ExpectExec("UPDATE organizations SET settings").
		WithArgs(capturingArgRouter(&capturedSettings), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	adapter := linearAgentBootstrapAdapter{
		orgs:   db.NewOrganizationStore(mock),
		repos:  db.NewRepositoryStore(mock),
		logger: zerolog.Nop(),
	}
	require.NoError(t, adapter.Bootstrap(context.Background(), orgID))

	var got models.OrgSettings
	require.NoError(t, json.Unmarshal(capturedSettings, &got))
	require.Nil(t, got.LinearAgent.DefaultRepoID, "multi-repo org must NOT have a guessed default — admin picks explicitly")
	require.NotNil(t, got.LinearAgent.Enabled, "Enabled flip is independent of repo-count guard")
	require.True(t, *got.LinearAgent.Enabled)
	require.NoError(t, mock.ExpectationsWereMet())
}

// capturingArgRouter mirrors handlers.capturingArg for use inside router_test.go.
// Captures the matched pgxmock arg into the target so assertions can inspect
// the post-merge settings JSON written by the bootstrap adapter.
func capturingArgRouter(dst *[]byte) pgxmock.Argument {
	return capturingArgRouterImpl{dst: dst}
}

type capturingArgRouterImpl struct{ dst *[]byte }

func (c capturingArgRouterImpl) Match(v any) bool {
	switch b := v.(type) {
	case []byte:
		*c.dst = append((*c.dst)[:0], b...)
	case json.RawMessage:
		*c.dst = append((*c.dst)[:0], b...)
	case string:
		*c.dst = append((*c.dst)[:0], b...)
	default:
		return false
	}
	return true
}
