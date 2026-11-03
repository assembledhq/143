package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type registrySetupResolver struct {
	metadata                    []models.PackageRegistryCredentialSummary
	resolved                    []models.ResolvedPackageRegistryCredential
	metadataCalls, resolveCalls int
	orgID, repoID               uuid.UUID
}

func (r *registrySetupResolver) Metadata(_ context.Context, orgID, repoID uuid.UUID) ([]models.PackageRegistryCredentialSummary, error) {
	r.metadataCalls++
	if orgID != r.orgID || repoID != r.repoID {
		return nil, errors.New("scope mismatch")
	}
	return r.metadata, nil
}
func (r *registrySetupResolver) Resolve(_ context.Context, orgID, repoID uuid.UUID) ([]models.ResolvedPackageRegistryCredential, error) {
	r.resolveCalls++
	if orgID != r.orgID || repoID != r.repoID {
		return nil, errors.New("scope mismatch")
	}
	return r.resolved, nil
}

type registrySetupProvider struct {
	SandboxProvider
	warm      bool
	calls     int
	revisions []string
}

func (p *registrySetupProvider) PrepareNPMDependencies(ctx context.Context, _ *Sandbox, _ []repoconfig.NPMSetup, revision string, resolve func(context.Context) ([]models.ResolvedPackageRegistryCredential, error)) (string, error) {
	p.calls++
	p.revisions = append(p.revisions, revision)
	if !p.warm {
		if _, err := resolve(ctx); err != nil {
			return "", err
		}
	}
	return "manifest:" + revision, nil
}

type registrySetupTrust struct {
	err           error
	calls         int
	orgID, repoID uuid.UUID
	number        int
	head          string
}

func (t *registrySetupTrust) ValidatePackageSetupPullRequest(_ context.Context, orgID, repoID uuid.UUID, number int, head string) error {
	t.calls++
	t.orgID, t.repoID, t.number, t.head = orgID, repoID, number, head
	return t.err
}

func TestPackageRegistrySetupTrustAndScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                    string
		origin                                  models.SessionOrigin
		revision                                string
		warm, trustError, wrongRepo, verifyOnly bool
		fail                                    bool
		resolves, trustCalls                    int
	}{
		{name: "scheduled base repository automation", origin: models.SessionOriginAutomation, resolves: 1},
		{name: "same repository review", origin: models.SessionOriginCodeReview, revision: `{"from_fork":false,"github_pr_number":7,"head_sha":"pinned"}`, resolves: 1, trustCalls: 1},
		{name: "fork review", origin: models.SessionOriginCodeReview, revision: `{"from_fork":true}`, fail: true},
		{name: "unknown review trust", origin: models.SessionOriginCodeReview, revision: `{}`, fail: true},
		{name: "failed live lookup", origin: models.SessionOriginCodeReview, revision: `{"from_fork":false,"github_pr_number":7,"head_sha":"pinned"}`, trustError: true, fail: true, trustCalls: 1},
		{name: "healthy warm review never decrypts", origin: models.SessionOriginCodeReview, revision: `{"from_fork":false,"github_pr_number":7,"head_sha":"pinned"}`, warm: true},
		{name: "cross tenant metadata", origin: models.SessionOriginAutomation, wrongRepo: true, fail: true},
		{name: "expired setup cannot decrypt or install", origin: models.SessionOriginAutomation, verifyOnly: true, fail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, repoID := uuid.New(), uuid.New()
			summary := models.PackageRegistryCredentialSummary{ID: uuid.New(), RepositoryID: repoID, Revision: uuid.New(), RegistryURL: "https://registry.example/", PackageScope: "@fixture"}
			if tt.wrongRepo {
				summary.RepositoryID = uuid.New()
			}
			resolver := &registrySetupResolver{orgID: orgID, repoID: repoID, metadata: []models.PackageRegistryCredentialSummary{summary}, resolved: []models.ResolvedPackageRegistryCredential{{Metadata: summary, Token: "fake-setup-only"}}}
			provider := &registrySetupProvider{warm: tt.warm}
			trust := &registrySetupTrust{}
			if tt.trustError {
				trust.err = errors.New("provider unavailable fake-setup-only")
			}
			o := &Orchestrator{provider: provider, packageCredentials: resolver, packageSetupTrust: trust}
			ctx := context.Background()
			if tt.verifyOnly {
				ctx = context.WithValue(ctx, packageSetupVerifyOnlyKey{}, true)
			}
			_, err := o.preparePackageRegistryDependencies(ctx, &models.Session{OrgID: orgID, RepositoryID: &repoID, Origin: tt.origin, RevisionContext: json.RawMessage(tt.revision)}, &Sandbox{ID: "main", OrgID: orgID.String()}, repoconfig.Config{Setup: repoconfig.SetupSection{NPM: []repoconfig.NPMSetup{{Directory: "."}}}})
			if tt.fail {
				require.Error(t, err, "unsafe setup must fail before credentials can be delivered")
				require.NotContains(t, err.Error(), "fake-setup-only", "errors must not expose provider credential diagnostics")
			} else {
				require.NoError(t, err, "authorized setup should complete")
			}
			require.Equal(t, tt.resolves, resolver.resolveCalls, "only cold authorized setup may decrypt")
			require.Equal(t, tt.trustCalls, trust.calls, "live PR identity is checked immediately before credential resolution")
			if trust.calls > 0 {
				require.Equal(t, orgID, trust.orgID, "trust lookups must be tenant scoped")
				require.Equal(t, repoID, trust.repoID, "trust lookups must use the exact repository")
				require.Equal(t, 7, trust.number, "trust lookups must use the pinned PR")
				require.Equal(t, "pinned", trust.head, "trust lookups must use the reviewed head")
			}
		})
	}
}

func TestPackageRegistryRevisionRotation(t *testing.T) {
	t.Parallel()
	repo := uuid.New()
	one := models.PackageRegistryCredentialSummary{ID: uuid.New(), RepositoryID: repo, Revision: uuid.New(), RegistryURL: "https://registry.example/", PackageScope: "@fixture"}
	first, err := packageCredentialRevision(repo, []models.PackageRegistryCredentialSummary{one})
	require.NoError(t, err, "initial metadata should fingerprint without plaintext")
	one.Revision = uuid.New()
	rotated, err := packageCredentialRevision(repo, []models.PackageRegistryCredentialSummary{one})
	require.NoError(t, err, "rotation metadata should fingerprint without plaintext")
	deleted, err := packageCredentialRevision(repo, nil)
	require.NoError(t, err, "deletion should produce a distinct empty binding")
	require.NotEqual(t, first, rotated, "rotation must invalidate setup reuse")
	require.NotEqual(t, rotated, deleted, "deletion must invalidate setup reuse")
}

type registrySetupConfigProvider struct {
	SandboxProvider
	data []byte
	err  error
}

func (p registrySetupConfigProvider) ReadFile(context.Context, *Sandbox, string) ([]byte, error) {
	return p.data, p.err
}

func TestPackageRegistrySetupConfigMissingFiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		data string
		fail bool
	}{
		{name: "missing sentinel", err: os.ErrNotExist},
		{name: "wrapped missing sentinel", err: fmt.Errorf("read setup: %w", os.ErrNotExist)},
		{name: "legacy provider missing text", err: errors.New("no such file")},
		{name: "read denial stays fatal", err: os.ErrPermission, fail: true},
		{name: "explicit malformed setup stays fatal", data: `{"setup":{"npm":[{"directory":"../outside"}]}}`, fail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := &Orchestrator{provider: registrySetupConfigProvider{data: []byte(tt.data), err: tt.err}}
			cfg, err := o.readPackageSetupConfig(context.Background(), &Sandbox{}, "/workspace")
			if tt.fail {
				require.Error(t, err, "unavailable or explicitly invalid setup must fail closed")
			} else {
				require.NoError(t, err, "absent repository setup should preserve existing preparation behavior")
				require.Equal(t, repoconfig.Config{}, cfg, "missing setup config must not declare dependency installation")
			}
		})
	}
}
