package agent

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type reviewConfigGitHubTokens struct {
	calls   int
	token   string
	onToken func()
}

func (p *reviewConfigGitHubTokens) GetInstallationToken(context.Context, int64) (string, error) {
	p.calls++
	if p.onToken != nil {
		p.onToken()
	}
	return p.token, nil
}

func setReviewFixtureGitConfig(t *testing.T, f reviewEvidenceGitFixture, key, value string) {
	t.Helper()
	out, err := exec.Command("/usr/bin/git", "-C", f.workDir, "config", key, value).CombinedOutput()
	require.NoError(t, err, "isolated adversarial Git configuration should be installed: %s", out)
}

func TestStandaloneReviewHydrationBindsConfigurationBeforeFetch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, proxy string
		omitBinding bool
	}{
		{name: "standalone rejects planted proxy", proxy: "http://127.0.0.1:1"},
		{name: "shared executor supplies omitted binding", proxy: "http://127.0.0.1:1", omitBinding: true},
		{name: "safe standalone hydrates missing pinned sources"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, "", false)
			marker := filepath.Join(t.TempDir(), "fetch-started")
			git := filepath.Join(t.TempDir(), "controlled-git")
			script := "#!/bin/sh\ncase \" $* \" in *' fetch '*) printf started > '" + shellEscapeSingleQuote(marker) + "';; esac\nexec /usr/bin/git \"$@\"\n"
			require.NoError(t, os.WriteFile(git, []byte(script), 0o700), "controlled Git fixture should mark only real attempted fetches")
			if tt.proxy != "" {
				setReviewFixtureGitConfig(t, f, "http."+f.remote+".proxy", tt.proxy)
			}
			o := f.orchestrator()
			o.provider = localReviewEvidenceProvider{rewriteTrustedGit: git}
			const token = "fake-standalone-hydration-token"
			var err error
			if tt.omitBinding {
				// The shared executor's auth-derived binding must stay effective
				// even if another standalone caller omits the initializer field.
				payload := reviewWorkspacePreparationPayload{WorkDir: f.workDir, Standalone: true, AuthenticateGit: true, Prepare: reviewTreeEvidenceScript(f.workDir, f.base, f.head, f.remote), TimeoutSeconds: 2, CancelPath: filepath.Join(t.TempDir(), "cancel")}
				err = o.execReviewPreparationPayload(context.Background(), f.sandbox(), payload, reviewGitAuthentication{f.remote, token}, zerolog.Nop())
			} else {
				err = o.hydrateCodeReviewTrees(context.Background(), f.session, f.sandbox(), f.remote, token, zerolog.Nop())
			}
			if tt.proxy != "" {
				require.Error(t, err, "standalone authenticated hydration must reject planted transport settings before running any fetch")
				require.NoFileExists(t, marker, "auth-derived repository binding must run the finite guard before a Git fetch starts")
			} else {
				require.NoError(t, err, "safe standalone hydration must fetch the fixture's genuinely missing pinned source objects")
				require.FileExists(t, marker, "safe control must exercise a real fetch so rejection cannot pass merely because no hydration was needed")
			}
			require.NoFileExists(t, filepath.Join(f.workDir, ".git/143-review-evidence.json"), "standalone source hydration alone must not publish workspace readiness")
		})
	}
}

func TestReviewAuthenticationRejectsUnsafeLocalConfigBeforeToken(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, key, value string }{
		{name: "unscoped proxy", key: "http.proxy", value: "http://127.0.0.1:1"},
		{name: "URL proxy", key: "http.https://example.com/repo.git.proxy", value: "http://127.0.0.1:1"},
		{name: "URL TLS disabled", key: "http.https://example.com/repo.git.sslVerify", value: "false"},
		{name: "URL CA", key: "http.https://example.com/repo.git.sslCAInfo", value: "/fixture/ca"},
		{name: "URL curl resolution", key: "http.https://example.com/repo.git.curloptResolve", value: "example.com:443:127.0.0.1"},
		{name: "URL rewrite", key: "url.https://attacker.invalid/.insteadOf", value: "https://example.com/"},
		{name: "include", key: "include.path", value: "/fixture/included-config"},
		{name: "conditional include", key: "includeIf.gitdir:/fixture/.path", value: "/fixture/included-config"},
		{name: "worktree config extension", key: "extensions.worktreeConfig", value: "true"},
		{name: "foreign worktree", key: "core.worktree", value: "/fixture/foreign"},
		{name: "Git proxy command", key: "core.gitProxy", value: "fixture-command"},
		{name: "SSH command", key: "core.sshCommand", value: "fixture-command"},
		{name: "fsmonitor", key: "core.fsmonitor", value: "fixture-command"},
		{name: "custom hooks directory", key: "core.hooksPath", value: "/fixture/hooks"},
		{name: "altered LFS clean helper", key: "filter.lfs.clean", value: "fixture-command"},
		{name: "altered LFS smudge helper", key: "filter.lfs.smudge", value: "fixture-command"},
		{name: "altered LFS process helper", key: "filter.lfs.process", value: "fixture-command"},
		{name: "remote helper", key: "remote.origin.vcs", value: "fixture-helper"},
		{name: "remote upload command", key: "remote.origin.uploadpack", value: "fixture-command"},
		{name: "unowned credential helper", key: "credential.helper", value: "fixture-command"},
		{name: "unknown core key", key: "core.futureHelper", value: "fixture-command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, "printf forbidden >> executions.log", false)
			setReviewFixtureGitConfig(t, f, tt.key, tt.value)
			tokens := &reviewConfigGitHubTokens{token: "fake-config-rejected-installation-token"}
			provider := &recordingReviewCredentialProvider{}
			o := f.orchestrator()
			o.provider, o.github = provider, tokens
			err := o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop())
			require.Error(t, err, "unsafe local configuration must be an operational preparation failure")
			require.Equal(t, 0, tokens.calls, "local transport overrides must fail before installation-token lookup")
			require.Equal(t, 0, provider.inputs, "unsafe source configuration must receive no authenticated transport input")
			require.NoFileExists(t, filepath.Join(f.workDir, "executions.log"), "unsafe configuration must run no repository preparation")
			require.NoFileExists(t, filepath.Join(f.workDir, ".git/143-review-evidence.json"), "unsafe configuration cannot publish readiness")
		})
	}
}

func TestReviewAuthenticationRechecksLocalConfigAfterTokenLookup(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "printf forbidden >> executions.log", false)
	tokens := &reviewConfigGitHubTokens{token: "fake-after-lookup-token", onToken: func() { setReviewFixtureGitConfig(t, f, "http."+f.remote+".proxy", "http://127.0.0.1:1") }}
	provider := &recordingReviewCredentialProvider{}
	o := f.orchestrator()
	o.provider, o.github = provider, tokens
	err := o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop())
	require.Error(t, err, "configuration changed after the first proof must fail again inside the authenticated wrapper")
	require.Equal(t, 1, tokens.calls, "fixture should mutate configuration precisely after one approved lookup")
	require.Equal(t, 1, provider.inputs, "private input may reach only the trusted wrapper before its repeated guard rejects the mutation")
	require.NoFileExists(t, filepath.Join(f.workDir, "executions.log"), "late unsafe configuration must run no repository recipe")
	require.NoFileExists(t, filepath.Join(f.workDir, ".git/143-review-evidence.json"), "late unsafe configuration must not seal readiness")
}

func TestReviewGitConfigAcceptsExactPlatformBootstrapProfile(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "", false)
	for _, entry := range []struct{ key, value string }{
		{"user.name", "143 Fixture"}, {"user.email", "fixture@example.com"}, {"push.autoSetupRemote", "true"}, {"credential.helper", "!143-tools git-credential"},
	} {
		setReviewFixtureGitConfig(t, f, entry.key, entry.value)
	}
	o := f.orchestrator()
	require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "real partial clone and exact git-bootstrap configuration must hydrate and publish evidence")
	o.github = nil
	require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "known platform profile must reuse evidence without token lookup")
	expired := WithCodeReviewWorkspacePreparationDeadline(context.Background(), time.Now().Add(-time.Minute))
	require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(expired, f.session, f.sandbox(), zerolog.Nop()), "known platform profile must pass expired local-only verification")
}

func TestReviewGitConfigRejectsIncludeFIFOWithoutReadingIt(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "printf forbidden >> executions.log", false)
	fifo := filepath.Join(t.TempDir(), "included-config")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600), "fixture should create an unopened blocking include")
	setReviewFixtureGitConfig(t, f, "include.path", fifo)
	tokens := &reviewConfigGitHubTokens{token: "fake-fifo-token"}
	o := f.orchestrator()
	o.github = tokens
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	require.Error(t, o.ensureCodeReviewWorkspaceEvidence(ctx, f.session, f.sandbox(), zerolog.Nop()), "literal no-includes guard must reject an include before Git discovery can open it")
	require.Less(t, time.Since(started), time.Second, "unsafe includes must fail promptly rather than spending the deadline blocked on a FIFO")
	require.Equal(t, 0, tokens.calls, "blocking include must not trigger token lookup")
}

func TestReviewGitConfigCapsExpandedListingBeforeAllocation(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "", false)
	// A small literal config can expand to 150MiB when Git repeats one long
	// subsection per short key. The guard must stop its stdout at 1MiB.
	text := "[remote \"" + strings.Repeat("x", 30000) + "\"]\n" + strings.Repeat("a=1\n", 5000)
	require.Less(t, len(text), 65536, "expansion fixture must fit the literal configuration bound")
	require.NoError(t, os.WriteFile(filepath.Join(f.workDir, ".git/config"), []byte(text), 0o600), "fixture should write only isolated oversized-subsection metadata")
	settings, err := json.Marshal(map[string]string{"work_dir": f.workDir, "repo_url": f.remote})
	require.NoError(t, err, "literal guard fixture should serialize")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/python3", "-I", "-S", "-c", reviewGitAuthenticationConfigPython, string(settings)).CombinedOutput()
	require.Error(t, err, "expanded listing beyond the finite cap must be rejected")
	require.NoError(t, ctx.Err(), "guard must kill the listing writer before exhausting the original deadline")
	require.Less(t, len(out), 4096, "guard rejection must not echo expanded configuration output")
}

func TestReviewGitConfigRejectsDuplicateOrMismatchedOrigin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
	}{
		{name: "duplicate owned helper", args: []string{"config", "--add", "credential.helper", "!143-tools git-credential"}},
		{name: "different origin", args: []string{"remote", "set-url", "origin", "https://attacker.invalid/repo.git"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, "", false)
			if strings.Contains(tt.name, "duplicate") {
				setReviewFixtureGitConfig(t, f, "credential.helper", "!143-tools git-credential")
			}
			out, err := exec.Command("/usr/bin/git", append([]string{"-C", f.workDir}, tt.args...)...).CombinedOutput()
			require.NoError(t, err, "isolated config mutation should succeed: %s", out)
			o := f.orchestrator()
			o.github = nil
			require.Error(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "duplicate or unbound metadata must fail without any token capability")
		})
	}
}

func TestReviewGitConfigRejectsUnsafeMetadataTopology(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, path string
		link       bool
	}{
		{name: "Git pointer file", path: ".git"},
		{name: "Git directory symlink", path: ".git", link: true},
		{name: "config symlink", path: ".git/config", link: true},
		{name: "objects symlink", path: ".git/objects", link: true},
		{name: "objects info symlink", path: ".git/objects/info", link: true},
		{name: "objects pack symlink", path: ".git/objects/pack", link: true},
		{name: "refs symlink", path: ".git/refs", link: true},
		{name: "info symlink", path: ".git/info", link: true},
		{name: "common directory pointer", path: ".git/commondir"},
		{name: "worktree configuration", path: ".git/config.worktree"},
		{name: "alternate objects", path: ".git/objects/info/alternates"},
		{name: "alternate HTTP objects", path: ".git/objects/info/http-alternates"},
		{name: "graft metadata", path: ".git/info/grafts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, "printf forbidden >> executions.log", false)
			target := filepath.Join(f.workDir, tt.path)
			if tt.link || tt.path == ".git" {
				saved := filepath.Join(t.TempDir(), "saved-metadata")
				require.NoError(t, os.Rename(target, saved), "fixture should move only isolated metadata")
				if tt.link {
					require.NoError(t, os.Symlink(saved, target), "fixture should plant a linked metadata component")
				} else {
					require.NoError(t, os.WriteFile(target, []byte("gitdir: "+saved+"\n"), 0o600), "fixture should plant an unsupported Git pointer layout")
				}
			} else {
				require.NoError(t, os.WriteFile(target, []byte("/fixture/foreign-metadata\n"), 0o600), "fixture should plant unsupported sidecar metadata")
			}
			tokens := &reviewConfigGitHubTokens{token: "fake-topology-token"}
			o := f.orchestrator()
			o.github = tokens
			require.Error(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "linked, pointer, or alternate metadata must fail before any credential acquisition")
			require.Equal(t, 0, tokens.calls, "unsafe metadata topology must never cause token lookup")
			require.NoFileExists(t, filepath.Join(f.workDir, "executions.log"), "unsafe topology must execute no repository recipe")
		})
	}
}

func TestReviewGitConfigRejectsReplacementRefsWithoutChangingPinnedSource(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		packed bool
	}{{name: "loose replacement"}, {name: "packed replacement", packed: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, "", false)
			o := f.orchestrator()
			require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "replacement fixture should begin with exact complete source")
			out, err := exec.Command("/usr/bin/git", "-C", f.workDir, "replace", f.head, f.base).CombinedOutput()
			require.NoError(t, err, "ordinary Git should install the actual replacement attack: %s", out)
			out, err = exec.Command("/usr/bin/git", "-C", f.workDir, "show", f.head+":changed.go").CombinedOutput()
			require.NoError(t, err, "ordinary control should resolve replacement content: %s", out)
			require.Equal(t, "pinned target implementation\n", string(out), "replacement must change source resolution despite unchanged literal HEAD")
			if tt.packed {
				out, err = exec.Command("/usr/bin/git", "-C", f.workDir, "pack-refs", "--all", "--prune").CombinedOutput()
				require.NoError(t, err, "fixture should place replacement identity into packed refs: %s", out)
				require.NoError(t, os.RemoveAll(filepath.Join(f.workDir, ".git/refs/replace")), "packed case must exercise packed refs independently of the loose directory")
			}
			require.NoError(t, o.execReviewEvidence(context.Background(), f.sandbox(), reviewPinnedCheckoutVerificationScript(f.workDir, f.base, f.head), ""), "platform Git must disable replacement source resolution even outside the strict guard")
			o.github = nil
			require.Error(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "loose and packed replacement metadata must invalidate readiness without token lookup")
		})
	}
}

func TestReviewGitConfigRejectsPoisonedRecipeBeforeReceipt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		poisonPrepare bool
	}{
		{name: "prepare poisons config", poisonPrepare: true},
		{name: "warm verifier poisons config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			prepare := "mkdir -p node_modules/pkg; printf source > node_modules/pkg/api.txt"
			verify := "test -f node_modules/pkg/api.txt"
			poison := "/usr/bin/git config http.proxy http://127.0.0.1:1"
			if tt.poisonPrepare {
				prepare += "; " + poison
			} else {
				verify += "; if test -f permit-poison; then " + poison + "; fi"
			}
			config, err := json.Marshal(map[string]any{"review": map[string]any{"commands": []string{prepare}, "verify_commands": []string{verify}, "evidence": []map[string]string{{"path": "node_modules/pkg", "description": "source", "category": "javascript_dependencies"}}}})
			require.NoError(t, err, "poisoning recipe should serialize")
			f := newReviewEvidenceGitFixtureWithConfig(t, string(config), false)
			o := f.orchestrator()
			if !tt.poisonPrepare {
				require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "warm poisoning fixture must first publish healthy evidence")
				require.NoError(t, os.WriteFile(filepath.Join(f.workDir, "permit-poison"), []byte("poison"), 0o600), "fixture should enable later verifier poisoning")
			}
			require.Error(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "recipe success cannot seal or reuse poisoned Git configuration")
			if tt.poisonPrepare {
				require.NoFileExists(t, filepath.Join(f.workDir, ".git/143-review-evidence.json"), "poisoned first setup cannot publish any receipt")
			}
		})
	}
}

// Verify module auth is actually released, not merely shadowed in a local.
func TestReviewAuthenticationStateIsReleasedBeforeRepositoryCommands(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "printf checked >> executions.log", false)
	provider := authStateReviewProvider{localReviewEvidenceProvider{}}
	o := f.orchestrator()
	o.provider = provider
	o.github = &reviewConfigGitHubTokens{token: "fake-module-auth-token"}
	require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "runtime guard must observe module auth None before repository preparation")
	require.Equal(t, "checked", readCredentialFixtureFile(t, filepath.Join(f.workDir, "executions.log")), "actual untrusted preparation must run only after private state is released")
}

type authStateReviewProvider struct{ localReviewEvidenceProvider }

func (p authStateReviewProvider) ExecWithStdin(ctx context.Context, sandbox *Sandbox, command string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	guarded := strings.Replace(reviewWorkspacePreparationPython, `auth = None  # No private authentication state remains for recipes.`, `assert "auth" in globals(); auth = None; assert globals()["auth"] is None`, 1)
	command = strings.Replace(command, shellEscapeSingleQuote(reviewWorkspacePreparationPython), shellEscapeSingleQuote(guarded), 1)
	return p.localReviewEvidenceProvider.ExecWithStdin(ctx, sandbox, command, stdin, stdout, stderr)
}
