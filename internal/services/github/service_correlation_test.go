package github

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	githubtelemetry "github.com/assembledhq/143/internal/services/github/telemetry"
)

func TestService_IssuedCredentialCorrelation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		action     string
		profile    githubtelemetry.CredentialProfile
		repository string
	}{
		{name: "installation cache and replacement", profile: githubtelemetry.CredentialProfileInstallation},
		{name: "sandbox API cache and replacement", action: "api", profile: githubtelemetry.CredentialProfileSandboxAPI, repository: "example/fixture"},
		{name: "sandbox push cache and replacement", action: "push", profile: githubtelemetry.CredentialProfileSandboxPush, repository: "example/fixture"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			service, err := NewService(42, testPrivateKeyPEM(t), zerolog.New(&logs))
			require.NoError(t, err, "fixture issuer should initialize")
			registry := githubtelemetry.NewCredentialRegistry()
			service.credentialRegistry = registry
			tokens := []string{"fixture-" + uuid.NewString(), "fixture-" + uuid.NewString()}
			expires := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
			var exchanges atomic.Int32
			service.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				index := int(exchanges.Add(1)) - 1
				body, marshalErr := json.Marshal(map[string]any{"token": tokens[index], "expires_at": expires})
				require.NoError(t, marshalErr, "synthetic token response should encode")
				return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
			})}
			ctx := githubtelemetry.WithRequestMetadata(context.Background(), githubtelemetry.RequestMetadata{Repository: "example/fixture"})
			getToken := func() string {
				var token string
				var tokenErr error
				if tt.action == "" {
					token, tokenErr = service.GetInstallationToken(ctx, 99)
				} else {
					token, tokenErr = service.GetSandboxInstallationToken(ctx, 99, 123, tt.action)
				}
				require.NoError(t, tokenErr, "credential correlation must preserve successful issuance")
				return token
			}
			require.Equal(t, tokens[0], getToken(), "first request should return the issued token")
			first, known := registry.Resolve(tokens[0])
			require.True(t, known, "successfully decoded credentials should be registered")
			_, err = uuid.Parse(first.GenerationID)
			require.NoError(t, err, "generation should be an opaque UUID")
			require.Equal(t, githubtelemetry.CredentialIdentity{GenerationID: first.GenerationID, Profile: tt.profile, AppID: 42, InstallationID: 99, Repository: tt.repository, IssuedAt: first.IssuedAt, ExpiresAt: expires}, first, "registration should retain issuing scope without binding unrestricted tokens to one repository")
			require.Equal(t, tokens[0], getToken(), "cache reuse should return the same actual credential")
			cached, known := registry.Resolve(tokens[0])
			require.True(t, known, "cache reuse should remain correlated")
			require.Equal(t, first, cached, "cache hits should preserve the complete generation identity")
			require.Equal(t, int32(1), exchanges.Load(), "cache hits must not mint a second token")
			service.mu.Lock()
			if tt.action == "" {
				service.cache[99].ExpiresAt = time.Now().Add(-time.Minute)
			} else {
				service.sandboxCache[sandboxTokenCacheKey{InstallationID: 99, RepositoryID: 123, Action: tt.action}].ExpiresAt = time.Now().Add(-time.Minute)
			}
			service.mu.Unlock()
			require.Equal(t, tokens[1], getToken(), "cache replacement should return the newly issued credential")
			replaced, known := registry.Resolve(tokens[1])
			require.True(t, known, "replacement credential should be registered")
			require.NotEqual(t, first.GenerationID, replaced.GenerationID, "different actual credentials must have different generations")
			retained, known := registry.Resolve(tokens[0])
			require.True(t, known, "cache replacement must retain still-valid in-flight credential attribution")
			require.Equal(t, first, retained, "old in-flight requests should retain original generation metadata")
			require.Equal(t, 2, strings.Count(logs.String(), `"message":"github installation token issued"`), "only successful exchanges should emit issuance events")
			decoder := json.NewDecoder(bytes.NewReader(logs.Bytes()))
			issuances := 0
			for {
				var issuance map[string]any
				decodeErr := decoder.Decode(&issuance)
				if decodeErr == io.EOF {
					break
				}
				require.NoError(t, decodeErr, "issuer events should remain valid JSON")
				if issuance["message"] != "github installation token issued" {
					continue
				}
				issuances++
				if tt.repository == "" {
					require.NotContains(t, issuance, "github_token_repository", "unrestricted installation credentials must not gain a repository restriction")
				} else {
					require.Equal(t, tt.repository, issuance["github_token_repository"], "scoped issuance and request events must retain the same repository provenance")
				}
			}
			require.Equal(t, 2, issuances, "both successful exchanges should retain their issuance provenance")
			for _, token := range tokens {
				digest := sha256.Sum256([]byte(token))
				require.NotContains(t, logs.String(), token, "issuance telemetry must never contain a token")
				require.NotContains(t, logs.String(), hex.EncodeToString(digest[:]), "issuance telemetry must never expose its internal correlation key")
			}
		})
	}
}

func TestService_FailedTokenExchangeDoesNotRegisterCredential(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "provider failure", status: http.StatusForbidden, body: `{"message":"denied"}`},
		{name: "incomplete response", status: http.StatusCreated, body: `{"token":"fixture-incomplete",`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			service, err := NewService(42, testPrivateKeyPEM(t), zerolog.New(&logs))
			require.NoError(t, err, "fixture issuer should initialize")
			service.credentialRegistry = githubtelemetry.NewCredentialRegistry()
			service.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tt.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tt.body))}, nil
			})}
			_, _, err = service.exchangeForInstallationToken(context.Background(), "fixture-jwt", 99)
			require.Error(t, err, "failed or malformed exchanges must retain their original errors")
			_, known := service.credentialRegistry.Resolve("fixture-incomplete")
			require.False(t, known, "failed exchanges must not register a credential")
			require.NotContains(t, logs.String(), "github installation token issued", "failed exchanges must not emit successful issuance telemetry")
		})
	}
}
