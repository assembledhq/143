package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type packageLifecycleResolver struct {
	*registrySetupResolver
	events *[]string
}

func (r *packageLifecycleResolver) Metadata(ctx context.Context, orgID, repoID uuid.UUID) ([]models.PackageRegistryCredentialSummary, error) {
	*r.events = append(*r.events, "metadata")
	return r.registrySetupResolver.Metadata(ctx, orgID, repoID)
}

func (r *packageLifecycleResolver) Resolve(ctx context.Context, orgID, repoID uuid.UUID) ([]models.ResolvedPackageRegistryCredential, error) {
	*r.events = append(*r.events, "resolve")
	return r.registrySetupResolver.Resolve(ctx, orgID, repoID)
}

type packageLifecycleTrust struct {
	*registrySetupTrust
	events *[]string
}

func (p *packageLifecycleTrust) ValidatePackageSetupPullRequest(ctx context.Context, orgID, repoID uuid.UUID, number int, head string) error {
	*p.events = append(*p.events, "trust")
	return p.registrySetupTrust.ValidatePackageSetupPullRequest(ctx, orgID, repoID, number, head)
}

type packageLifecycleProvider struct {
	*registrySetupProvider
	events *[]string
}

func (p *packageLifecycleProvider) PrepareNPMDependencies(ctx context.Context, sandbox *Sandbox, setup []repoconfig.NPMSetup, revision string, resolve func(context.Context) ([]models.ResolvedPackageRegistryCredential, error)) (string, error) {
	*p.events = append(*p.events, "setup")
	return p.registrySetupProvider.PrepareNPMDependencies(ctx, sandbox, setup, revision, resolve)
}

func (p *packageLifecycleProvider) Exec(ctx context.Context, sandbox *Sandbox, command string, stdout, stderr io.Writer) (int, error) {
	if strings.Contains(command, "printf ready > bootstrap-ready") {
		*p.events = append(*p.events, "bootstrap")
	}
	return p.SandboxProvider.Exec(ctx, sandbox, command, stdout, stderr)
}

type packageLifecycleHarness struct {
	orchestrator *Orchestrator
	provider     *packageLifecycleProvider
	resolver     *packageLifecycleResolver
	trust        *packageLifecycleTrust
	events       []string
}

func newPackageLifecycleHarness(t *testing.T, directory string, session *models.Session, review *repoconfig.ReviewSection) *packageLifecycleHarness {
	t.Helper()
	configPath := filepath.Join(directory, repoconfig.ConfigPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0o700), "setup configuration directory should exist")
	config, err := json.Marshal(repoconfig.Config{
		Setup:     repoconfig.SetupSection{NPM: []repoconfig.NPMSetup{{Directory: "."}}},
		Bootstrap: repoconfig.CommandSection{Commands: []string{"printf ready > bootstrap-ready"}},
		Review:    review,
	})
	require.NoError(t, err, "fixture repository configuration should encode")
	require.NoError(t, os.WriteFile(configPath, config, 0o600), "fixture should declare platform setup and subsequent repository bootstrap")
	summary := models.PackageRegistryCredentialSummary{ID: uuid.New(), RepositoryID: *session.RepositoryID, Revision: uuid.New(), RegistryURL: "https://registry.example.test/", PackageScope: "@example"}
	h := &packageLifecycleHarness{}
	h.resolver = &packageLifecycleResolver{registrySetupResolver: &registrySetupResolver{orgID: session.OrgID, repoID: *session.RepositoryID, metadata: []models.PackageRegistryCredentialSummary{summary}, resolved: []models.ResolvedPackageRegistryCredential{{Metadata: summary, Token: "synthetic-setup-secret"}}}, events: &h.events}
	h.trust = &packageLifecycleTrust{registrySetupTrust: &registrySetupTrust{}, events: &h.events}
	h.provider = &packageLifecycleProvider{registrySetupProvider: &registrySetupProvider{SandboxProvider: &reviewInputsProvider{}}, events: &h.events}
	h.orchestrator = &Orchestrator{provider: h.provider, packageCredentials: h.resolver, packageSetupTrust: h.trust, repositories: testInternalRepoStore{repo: models.Repository{FullName: "example/repo", CloneURL: directory}}}
	return h
}

func TestPackageRegistryBuiltinReviewPreparationLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		review       *repoconfig.ReviewSection
		coldEvents   []string
		warmEvents   []string
		resolveCalls int
	}{
		{name: "default source-only review"},
		{
			name:         "explicit npm setup",
			review:       &repoconfig.ReviewSection{InstallNPMDependencies: true},
			coldEvents:   []string{"metadata", "setup", "trust", "resolve"},
			warmEvents:   []string{"metadata", "setup", "trust", "resolve", "metadata", "setup"},
			resolveCalls: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			directory, base, head, _ := newReviewInputsGitFixture(t)
			session := reviewInputsSession(t, base, head)
			revision, err := json.Marshal(map[string]any{"kind": "code_review", "github_repo": "example/repo", "github_pr_number": 42, "base_sha": base, "head_sha": head, "from_fork": false})
			require.NoError(t, err, "same-repository review context should encode pinned identity")
			session.RevisionContext = revision
			h := newPackageLifecycleHarness(t, directory, session, tt.review)
			sandbox := &Sandbox{ID: "review-package-lifecycle", OrgID: session.OrgID.String(), WorkDir: directory}
			expectedRevision, err := packageCredentialRevision(*session.RepositoryID, h.resolver.metadata)
			require.NoError(t, err, "metadata fingerprint should be available before cold review preparation")
			require.NoError(t, h.orchestrator.prepareColdCodeReviewInputs(context.Background(), session, sandbox), "cold builtin review must publish captured inputs")
			require.Equal(t, tt.coldEvents, h.events, "cold package setup must require opt-in and verify live PR trust before resolving credentials")
			if tt.review != nil && tt.review.InstallNPMDependencies {
				require.Equal(t, []string{expectedRevision}, h.provider.revisions, "cold setup must receive the current repository credential revision")
				require.Equal(t, 42, h.trust.number, "cold review trust must use the captured PR number")
				require.Equal(t, head, h.trust.head, "cold review trust must use the exact reviewed head")
			}
			manifestPath, patchPath := CodeReviewInputPaths(session.OrgID, session.ID, *session.RepositoryID, "example/repo", base, head)
			manifest, err := os.ReadFile(filepath.Join(directory, manifestPath))
			require.NoError(t, err, "cold review must publish its immutable input manifest")
			patch, err := os.ReadFile(filepath.Join(directory, patchPath))
			require.NoError(t, err, "cold review must publish its captured patch")
			h.provider.warm = true
			require.NoError(t, h.orchestrator.ensureCodeReviewInputs(context.Background(), session, sandbox), "warm builtin review must verify existing immutable inputs")
			require.Equal(t, tt.warmEvents, h.events, "warm review may recheck opt-in package metadata without resolving an already-valid installation")
			require.Equal(t, tt.resolveCalls, h.resolver.resolveCalls, "warm review must not decrypt credentials")
			require.Equal(t, tt.resolveCalls, h.trust.calls, "warm review must not request new credential delivery")
			if tt.review != nil && tt.review.InstallNPMDependencies {
				require.Equal(t, []string{expectedRevision, expectedRevision}, h.provider.revisions, "warm setup must receive the same current credential revision")
			}
			manifestAfter, err := os.ReadFile(filepath.Join(directory, manifestPath))
			require.NoError(t, err, "warm review manifest must remain readable")
			patchAfter, err := os.ReadFile(filepath.Join(directory, patchPath))
			require.NoError(t, err, "warm review patch must remain readable")
			require.Equal(t, manifest, manifestAfter, "warm package verification must preserve captured review identity")
			require.Equal(t, patch, patchAfter, "warm package verification must preserve captured review changes")
			_, err = os.Stat(filepath.Join(directory, "bootstrap-ready"))
			require.ErrorIs(t, err, os.ErrNotExist, "builtin review dependency setup must not execute arbitrary repository bootstrap")
		})
	}
}

func TestPackageRegistryPRFeedbackPreparationTrustLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		number      int
		head        string
		trustDenied bool
		wantEvents  []string
	}{
		{name: "repair with verified head", number: 7, head: "pinned", wantEvents: []string{"metadata", "setup", "trust", "resolve", "bootstrap"}},
		{name: "feedback with denied trust", number: 7, head: "pinned", trustDenied: true, wantEvents: []string{"metadata", "setup", "trust"}},
		{name: "repair missing PR number", head: "pinned", wantEvents: []string{"metadata", "setup"}},
		{name: "feedback missing head", number: 7, wantEvents: []string{"metadata", "setup"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			repoID := uuid.New()
			session := &models.Session{ID: uuid.New(), OrgID: uuid.New(), RepositoryID: &repoID, Origin: models.SessionOriginManual}
			h := newPackageLifecycleHarness(t, directory, session, nil)
			if tt.trustDenied {
				h.trust.err = errors.New("synthetic trust denial")
			}
			ctx := context.WithValue(context.Background(), packageSetupPRIdentityKey{}, packageSetupPRIdentity{Number: tt.number, Head: tt.head})
			err := h.orchestrator.prepareSandboxRepositoryForSession(ctx, h.provider, session, &Sandbox{ID: "feedback-package-lifecycle", OrgID: session.OrgID.String(), WorkDir: directory}, directory, repositoryPreparationFull, zerolog.Nop())
			if tt.trustDenied || tt.number == 0 || tt.head == "" {
				require.Error(t, err, "unverified PR feedback must stop before credential resolution or repository bootstrap")
				require.Equal(t, 0, h.resolver.resolveCalls, "unverified PR feedback must not resolve credentials")
				_, markerErr := os.Stat(filepath.Join(directory, "bootstrap-ready"))
				require.ErrorIs(t, markerErr, os.ErrNotExist, "denied feedback preparation must not run repository bootstrap")
			} else {
				require.NoError(t, err, "verified PR feedback should finish controlled setup before repository bootstrap")
				marker, markerErr := os.ReadFile(filepath.Join(directory, "bootstrap-ready"))
				require.NoError(t, markerErr, "verified preparation should execute the actual bootstrap command")
				require.Equal(t, "ready", string(marker), "bootstrap should produce the expected repository output")
			}
			require.Equal(t, tt.wantEvents, h.events, "feedback preparation must preserve trust, resolve, and bootstrap ordering")
			if h.trust.calls > 0 {
				require.Equal(t, session.OrgID, h.trust.orgID, "feedback trust must use the current organization")
				require.Equal(t, repoID, h.trust.repoID, "feedback trust must use the current repository")
				require.Equal(t, tt.number, h.trust.number, "feedback trust must use the captured PR number")
				require.Equal(t, tt.head, h.trust.head, "feedback trust must use the captured head")
			}
		})
	}
}

type packageLifecycleAutomationStore struct {
	AutomationTurnStore
	events *[]string
}

func (s *packageLifecycleAutomationStore) RecordTurnWorkspace(_ context.Context, _, _, _ uuid.UUID, _ models.AutomationTurnWorkspace) (bool, error) {
	*s.events = append(*s.events, "record-workspace")
	return true, nil
}

func TestPackageRegistryAutomationPreparationTrustLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		number      int
		head        string
		trustDenied bool
		wantEvents  []string
	}{
		{name: "verified automation head", number: 7, head: "pinned", wantEvents: []string{"metadata", "setup", "trust", "resolve", "bootstrap", "record-workspace"}},
		{name: "denied automation head", number: 7, head: "pinned", trustDenied: true, wantEvents: []string{"metadata", "setup", "trust"}},
		{name: "automation missing PR number", head: "pinned", wantEvents: []string{"metadata", "setup"}},
		{name: "automation missing head", number: 7, wantEvents: []string{"metadata", "setup"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			directory, _, _, _ := newReviewInputsGitFixture(t)
			repoID := uuid.New()
			session := &models.Session{ID: uuid.New(), OrgID: uuid.New(), RepositoryID: &repoID, Origin: models.SessionOriginAutomation}
			h := newPackageLifecycleHarness(t, directory, session, nil)
			h.orchestrator.automationTurns = &packageLifecycleAutomationStore{events: &h.events}
			if tt.trustDenied {
				h.trust.err = errors.New("synthetic trust denial")
			}
			state := &automationTurnState{run: models.AutomationRun{ID: uuid.New(), OrgID: session.OrgID}, repositoryID: repoID, pullRequestNumber: tt.number, headSHA: tt.head, startedAt: time.Now()}
			ctx := withAutomationTurnState(context.Background(), state)
			err := h.orchestrator.prepareAutomationTurnRepository(ctx, &Sandbox{ID: "automation-package-lifecycle", OrgID: session.OrgID.String(), WorkDir: directory}, SandboxConfig{WorkDir: directory, Image: "synthetic-setup-image"}, state, zerolog.Nop())
			if tt.trustDenied || tt.number == 0 || tt.head == "" {
				require.Error(t, err, "unverified automation preparation must stop before resolving credentials")
				require.Equal(t, 0, h.resolver.resolveCalls, "unverified automation must not decrypt package credentials")
				_, markerErr := os.Stat(filepath.Join(directory, "bootstrap-ready"))
				require.ErrorIs(t, markerErr, os.ErrNotExist, "denied automation preparation must not run repository bootstrap")
			} else {
				require.NoError(t, err, "verified automation should complete setup, bootstrap, and workspace recording")
				require.NotNil(t, state.fingerprint, "automation must fingerprint its prepared dependencies")
				require.Contains(t, *state.fingerprint, ":npm:manifest:"+h.provider.revisions[0], "automation reuse fingerprint must include the package credential revision")
				require.Equal(t, session.OrgID, h.trust.orgID, "automation trust must use the current organization")
				require.Equal(t, repoID, h.trust.repoID, "automation trust must use the current repository")
				require.Equal(t, tt.number, h.trust.number, "automation trust must use the current target PR")
				require.Equal(t, tt.head, h.trust.head, "automation trust must use the current target head")
			}
			require.Equal(t, tt.wantEvents, h.events, "automation preparation must validate trust before resolution and bootstrap")
		})
	}
}
