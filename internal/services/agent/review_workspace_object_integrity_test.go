package agent

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func copyReviewBaseBlobToLooseObject(t *testing.T, f reviewEvidenceGitFixture) string {
	t.Helper()
	git := func(args ...string) []byte {
		t.Helper()
		out, err := exec.Command("/usr/bin/git", append([]string{"-C", f.workDir}, args...)...).CombinedOutput()
		require.NoError(t, err, "ordinary source object fixture command should succeed: %s", out)
		return out
	}
	oid := strings.TrimSpace(string(git("rev-parse", f.base+":removed.go")))
	body := git("cat-file", "blob", oid)
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	_, err := fmt.Fprintf(writer, "blob %d%c", len(body), 0)
	require.NoError(t, err, "ordinary loose object should retain Git's exact existing blob header")
	_, err = writer.Write(body)
	require.NoError(t, err, "ordinary loose object should copy existing captured bytes without changing its OID")
	require.NoError(t, writer.Close(), "ordinary object compression should finish its checksum")
	object := filepath.Join(f.workDir, ".git/objects", oid[:2], oid[2:])
	require.NoError(t, os.MkdirAll(filepath.Dir(object), 0o700), "ordinary object directory should exist")
	require.NoError(t, os.WriteFile(object, compressed.Bytes(), 0o600), "fixture should create only a valid duplicate of an existing captured object")
	require.Equal(t, body, git("show", f.base+":removed.go"), "valid loose duplicate must resolve the unchanged captured source")
	return object
}

func corruptReviewLooseChecksum(t *testing.T, object string) {
	t.Helper()
	info, err := os.Stat(object)
	require.NoError(t, err, "ordinary loose object mode should be available before the controlled mutation")
	data, err := os.ReadFile(object)
	require.NoError(t, err, "ordinary loose object should be readable before benign checksum corruption")
	data[len(data)-1] ^= 1
	require.NoError(t, os.Chmod(object, 0o600), "test-owned ordinary object should be writable for its checksum flip")
	require.NoError(t, os.WriteFile(object, data, 0o600), "fixture should flip one checksum byte without constructing an OID or index remapping")
	require.NoError(t, os.Chmod(object, info.Mode().Perm()), "controlled checksum corruption should preserve the ordinary object's original permissions")
}

func observeReviewSourcePhases(t *testing.T) (localReviewEvidenceProvider, string) {
	t.Helper()
	root := t.TempDir()
	events, git := filepath.Join(root, "events"), filepath.Join(root, "controlled-git")
	script := "#!/bin/sh\ncase \" $* \" in *' fsck '*) phase=fsck;; *' rev-list '*) phase=closure;; *' read-tree '*) phase=index;; *) phase=;; esac\nif test -n \"$phase\"; then printf '%s\\n' \"$phase\" >> '" + shellEscapeSingleQuote(events) + "'; fi\nexec /usr/bin/git \"$@\"\n"
	require.NoError(t, os.WriteFile(git, []byte(script), 0o700), "phase observer should execute real Git and record only finite source-check names")
	return localReviewEvidenceProvider{rewriteTrustedGit: git}, events
}

func TestReviewObjectIntegrityHashesOnceBeforeEachVerification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		expected []string
		missing  bool
	}{
		{name: "cold locally complete", expected: []string{"fsck", "closure", "fsck", "closure", "index", "setup", "fsck", "closure", "index", "verify", "fsck", "closure", "index", "closure", "fsck", "closure", "index"}},
		{name: "cold missing captured source", missing: true, expected: []string{"fsck", "closure", "fsck", "closure", "closure", "closure", "fsck", "closure", "index", "setup", "fsck", "closure", "index", "verify", "fsck", "closure", "index", "closure", "fsck", "closure", "index"}},
		{name: "warm", expected: []string{"fsck", "closure", "fsck", "closure", "index", "verify", "fsck", "closure", "index", "fsck", "closure", "index"}},
		{name: "expired", expected: []string{"fsck", "closure", "fsck", "closure", "index", "fsck", "closure", "index"}},
		{name: "standalone", expected: []string{"fsck", "fsck", "closure"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			config := `{"review":{"commands":["mkdir -p .143/cache/go; printf source > .143/cache/go/api.go; printf 'setup\\n' >> \"$REVIEW_TEST_HASH_EVENTS\""],"verify_commands":["test -f .143/cache/go/api.go; printf 'verify\\n' >> \"$REVIEW_TEST_HASH_EVENTS\""],"evidence":[{"path":".143/cache/go","description":"Go source","category":"go_dependencies"}]}}`
			f := newReviewEvidenceGitFixtureWithConfig(t, config, false)
			o := f.orchestrator()
			if !tt.missing {
				require.NoError(t, o.hydrateCodeReviewTrees(context.Background(), f.session, f.sandbox(), f.remote, "", zerolog.Nop()), "local-complete count fixture should prehydrate captured trees so only intended verification boundaries are observed")
			}
			provider, events := observeReviewSourcePhases(t)
			o.provider = provider
			sandbox := f.sandbox()
			sandbox.Env = map[string]string{"REVIEW_TEST_HASH_EVENTS": events}
			if tt.name == "warm" || tt.name == "expired" {
				require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, sandbox, zerolog.Nop()), "warm count fixture should prepare its initial receipt")
				require.NoError(t, os.WriteFile(events, nil, 0o600), "phase history should isolate the requested warm or expired verification")
			}
			tokens := &reviewConfigGitHubTokens{token: "fake-hash-count-hydration-token"}
			o.github = nil
			if tt.missing {
				o.github = tokens
			}
			if tt.name == "standalone" {
				// The post-hydrate verifier has no worktree checkout requirement.
				payload := reviewWorkspacePreparationPayload{WorkDir: f.workDir, Standalone: true, GuardConfiguration: true, ExpectedRepoURL: f.remote, Prepare: "true", VerifyIntegrity: reviewGitObjectIntegrityScript(f.workDir), VerifyCapturedIntegrity: reviewGitObjectIntegrityScript(f.workDir) + reviewTreeEvidenceScript(f.workDir, f.base, f.head, ""), TimeoutSeconds: 3, CancelPath: filepath.Join(t.TempDir(), "cancel")}
				require.NoError(t, o.execReviewPreparationPayload(context.Background(), sandbox, payload, reviewGitAuthentication{}, zerolog.Nop()), "standalone source checks should hash before captured closure")
			} else {
				ctx := context.Background()
				if tt.name == "expired" {
					ctx = WithCodeReviewWorkspacePreparationDeadline(ctx, time.Now().Add(-time.Second))
				}
				require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(ctx, f.session, sandbox, zerolog.Nop()), "count fixture should accept intact source through its requested lifecycle")
			}
			data, err := os.ReadFile(events)
			require.NoError(t, err, "real Git source-check phases should remain available")
			require.Equal(t, tt.expected, strings.Split(strings.TrimSpace(string(data)), "\n"), "each verification must hash once before captured closure/index while retaining pretoken and post-command boundaries")
			expectedTokens := 0
			if tt.missing {
				expectedTokens = 1
			}
			require.Equal(t, expectedTokens, tokens.calls, "only genuinely missing captured source may authenticate after the distinct first hash pass")
		})
	}
}

func TestReviewObjectIntegrityHashesUnreferencedObjectsBeforeReadersAndToken(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "printf forbidden > permit", false)
	o := f.orchestrator()
	require.NoError(t, o.hydrateCodeReviewTrees(context.Background(), f.session, f.sandbox(), f.remote, "", zerolog.Nop()), "unreferenced-object fixture should have otherwise intact captured source")
	create := exec.Command("/usr/bin/git", "-C", f.workDir, "hash-object", "-w", "--stdin")
	create.Stdin = strings.NewReader("ordinary unattached source object\n")
	out, err := create.CombinedOutput()
	require.NoError(t, err, "Git itself should create the legitimate object outside current refs: %s", out)
	oid := strings.TrimSpace(string(out))
	corruptReviewLooseChecksum(t, filepath.Join(f.workDir, ".git/objects", oid[:2], oid[2:]))
	provider, events := observeReviewSourcePhases(t)
	o.provider = provider
	tokens := &reviewConfigGitHubTokens{token: "fake-unreferenced-source-token"}
	o.github = tokens
	err = o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop())
	require.ErrorIs(t, err, ErrCodeReviewSourceIntegrity, "rootless full fsck must validate corrupt present objects even outside current refs")
	data, err := os.ReadFile(events)
	require.NoError(t, err, "unreferenced corruption phase should be recorded")
	require.Equal(t, "fsck\n", string(data), "required full hashing must reject corruption before any captured graph or index readers")
	require.Equal(t, 0, tokens.calls, "unreferenced corrupt objects must not trigger authenticated repair")
	require.NoFileExists(t, filepath.Join(f.workDir, "permit"), "unreferenced corruption must execute no repository command")
	require.NoFileExists(t, filepath.Join(f.workDir, ".git/143-review-evidence.json"), "rootless integrity failure cannot publish readiness")
}

func TestStandaloneReviewSourceFailureClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		corrupt bool
	}{{name: "missing captured promised source"}, {name: "corrupt present captured source", corrupt: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, "", false)
			o := f.orchestrator()
			if tt.corrupt {
				require.NoError(t, o.hydrateCodeReviewTrees(context.Background(), f.session, f.sandbox(), f.remote, "", zerolog.Nop()), "corruption fixture should first make its captured source present")
				corruptReviewLooseChecksum(t, copyReviewBaseBlobToLooseObject(t, f))
			} else {
				require.NoError(t, o.execReviewEvidence(context.Background(), f.sandbox(), reviewGitObjectIntegrityScript(f.workDir), ""), "full hash validation should accept valid partial source with missing promised captured blobs")
				require.Error(t, o.execReviewEvidence(context.Background(), f.sandbox(), reviewTreeEvidenceScript(f.workDir, f.base, f.head, ""), ""), "missing-source fixture must exercise an actual nonzero captured closure check")
			}
			provider, events := observeReviewSourcePhases(t)
			o.provider = provider
			tokens := &reviewConfigGitHubTokens{token: "fake-standalone-source-token"}
			o.github = tokens
			payload := reviewWorkspacePreparationPayload{WorkDir: f.workDir, Standalone: true, GuardConfiguration: true, ExpectedRepoURL: f.remote, Prepare: "true", VerifyCapturedIntegrity: reviewGitObjectIntegrityScript(f.workDir) + reviewTreeEvidenceScript(f.workDir, f.base, f.head, ""), TimeoutSeconds: 3, CancelPath: filepath.Join(t.TempDir(), "cancel")}
			err := o.execReviewPreparationPayload(context.Background(), f.sandbox(), payload, reviewGitAuthentication{}, zerolog.Nop())
			require.Error(t, err, "standalone invalid captured source must fail instead of authorizing readiness")
			data, readErr := os.ReadFile(events)
			require.NoError(t, readErr, "standalone source-check phases should remain observable")
			if tt.corrupt {
				require.ErrorIs(t, err, ErrCodeReviewSourceIntegrity, "only trusted full-fsck internal125 may classify corruption as integrity failure")
				require.Equal(t, "fsck\n", string(data), "standalone corrupt available source must fail before captured graph readers")
			} else {
				require.NotErrorIs(t, err, ErrCodeReviewSourceIntegrity, "ordinary missing captured source must retain hydration guidance rather than claim corrupt objects")
				require.Equal(t, "Review workspace preparation failed during pinned source hydration. Review did not run.", NewCodeReviewWorkspacePreparationError(CodeReviewPreparationStageHydration, err).Error(), "standalone missing evidence must use the existing operational hydration copy")
				require.Equal(t, "fsck\nclosure\n", string(data), "standalone must complete the hash pass before rejecting missing captured closure")
			}
			require.Equal(t, 0, tokens.calls, "failed standalone acceptance must never request or retry authentication")
			require.NoFileExists(t, filepath.Join(f.workDir, ".git/143-review-evidence.json"), "standalone missing/corrupt sources cannot seal readiness")
		})
	}
}

func TestReviewObjectIntegrityRejectsBaseOnlyCorruptionBeforeToken(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "printf forbidden > permit", false)
	o := f.orchestrator()
	require.NoError(t, o.hydrateCodeReviewTrees(context.Background(), f.session, f.sandbox(), f.remote, "", zerolog.Nop()), "fixture should materialize its captured source before corruption")
	object := copyReviewBaseBlobToLooseObject(t, f)
	corruptReviewLooseChecksum(t, object)
	// Presence and HEAD cleanliness do not hash BASE-only blob bytes. Preserve
	// these controls so the new barrier proves more than the old two checks.
	require.NoError(t, o.execReviewEvidence(context.Background(), f.sandbox(), reviewTreeEvidenceScript(f.workDir, f.base, f.head, ""), ""), "old closure-only check should still pass for a present but corrupt BASE-only object")
	indexOnly := "/usr/bin/python3 -I -S -c '" + shellEscapeSingleQuote(reviewGitWorktreeVerificationPython) + "' '" + f.head + "'"
	require.NoError(t, o.execReviewEvidence(context.Background(), f.sandbox(), indexOnly, ""), "unchanged HEAD worktree control should not discover BASE-only checksum corruption")
	_, readErr := exec.Command("/usr/bin/git", "-C", f.workDir, "show", f.base+":removed.go").CombinedOutput()
	require.Error(t, readErr, "captured deleted-file content should actually be unreadable before the new integrity barrier")
	tokens := &reviewConfigGitHubTokens{token: "fake-corruption-token"}
	o.github = tokens
	err := o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop())
	require.ErrorIs(t, err, ErrCodeReviewSourceIntegrity, "hash failure must be a terminal source integrity failure instead of missing evidence")
	var typed *CodeReviewWorkspacePreparationError
	require.ErrorAs(t, err, &typed, "source validation should retain a typed operational preparation failure")
	require.Equal(t, CodeReviewPreparationStageHydration, typed.Stage, "integrity validation must retain the existing pinned-source stage")
	require.Equal(t, "Review workspace preparation could not verify pinned Git source object integrity. Recreate the review workspace and retry. Review did not run.", err.Error(), "author guidance must describe the actual source failure without object paths or diagnostics")
	require.Equal(t, 0, tokens.calls, "corrupt present objects must fail before requesting any hydration token")
	require.NoFileExists(t, filepath.Join(f.workDir, "permit"), "corruption must execute no repository setup")
	require.NoFileExists(t, filepath.Join(f.workDir, ".git/143-review-evidence.json"), "invalid captured source cannot publish readiness")
}

func TestReviewObjectIntegrityRejectsOrdinaryPackChecksumCorruption(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "printf forbidden > permit", false)
	o := f.orchestrator()
	require.NoError(t, o.hydrateCodeReviewTrees(context.Background(), f.session, f.sandbox(), f.remote, "", zerolog.Nop()), "ordinary captured packs should be valid before benign checksum corruption")
	packs, err := filepath.Glob(filepath.Join(f.workDir, ".git/objects/pack/*.pack"))
	require.NoError(t, err, "ordinary captured packs should be enumerable")
	require.NotEmpty(t, packs, "partial clone fixture must contain real Git pack storage")
	data, err := os.ReadFile(packs[0])
	require.NoError(t, err, "ordinary pack should be readable before corruption")
	data[len(data)-1] ^= 1
	require.NoError(t, os.Chmod(packs[0], 0o600), "test-owned ordinary pack must be writable before its benign checksum mutation")
	require.NoError(t, os.WriteFile(packs[0], data, 0o600), "fixture should flip only an ordinary pack checksum byte without altering its index")
	require.NoError(t, os.Chmod(packs[0], 0o444), "ordinary pack should retain Git's read-only mode after the controlled checksum flip")
	tokens := &reviewConfigGitHubTokens{token: "fake-pack-checksum-token"}
	o.github = tokens
	err = o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop())
	require.ErrorIs(t, err, ErrCodeReviewSourceIntegrity, "full fsck must reject pack checksum corruption before closure or worktree acceptance")
	require.Equal(t, 0, tokens.calls, "corrupt present pack storage must never be treated as missing evidence requiring a token")
	require.NoFileExists(t, filepath.Join(f.workDir, "permit"), "corrupt pack storage must execute no repository preparation")
}

const corruptReviewBaseObjectPython = `
import pathlib, subprocess, zlib
oid = subprocess.check_output(["/usr/bin/git", "rev-parse", "refs/remotes/origin/main:removed.go"]).decode().strip()
body = subprocess.check_output(["/usr/bin/git", "cat-file", "blob", oid])
packed = bytearray(zlib.compress(b"blob " + str(len(body)).encode() + b"\0" + body))
packed[-1] ^= 1
target = pathlib.Path(".git/objects") / oid[:2] / oid[2:]
target.parent.mkdir(parents=True, exist_ok=True)
target.write_bytes(packed)
`

func TestReviewObjectIntegrityRejectsRepositoryCommandCorruption(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		verifier bool
	}{{name: "setup corrupts BASE-only source"}, {name: "warm verifier corrupts BASE-only source", verifier: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			corrupt := "/usr/bin/python3 -I -S -c '" + shellEscapeSingleQuote(corruptReviewBaseObjectPython) + "'"
			setup := "mkdir -p .143/cache/go; printf source > .143/cache/go/api.go; printf '.\\n' >> executions.log"
			verify := "test -f .143/cache/go/api.go"
			if tt.verifier {
				verify += "; if test -f .143/cache/corrupt-now; then " + corrupt + "; fi"
			} else {
				setup += "; " + corrupt
			}
			config := fmt.Sprintf(`{"review":{"commands":[%s],"verify_commands":[%s],"evidence":[{"path":".143/cache/go","description":"Local Go source","category":"go_dependencies"}]}}`, quoteIntegrityJSON(t, setup), quoteIntegrityJSON(t, verify))
			f := newReviewEvidenceGitFixtureWithConfig(t, config, false)
			o := f.orchestrator()
			if tt.verifier {
				require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "healthy first preparation should publish source before verifier corruption")
				require.NoError(t, os.WriteFile(filepath.Join(f.workDir, ".143/cache/corrupt-now"), nil, 0o600), "warm verifier fixture should activate its benign checksum mutation")
			}
			tokens := &reviewConfigGitHubTokens{token: "fake-command-corruption-token"}
			o.github = tokens
			err := o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop())
			require.ErrorIs(t, err, ErrCodeReviewSourceIntegrity, "repository commands must not authorize corrupt local objects through unchanged HEAD or output metadata")
			if tt.verifier {
				require.Equal(t, 0, tokens.calls, "corrupting a warm verifier must never request fresh authentication or source repair")
			} else {
				require.Equal(t, 1, tokens.calls, "fresh healthy Git closure should hydrate once before setup corrupts source")
				require.NoFileExists(t, filepath.Join(f.workDir, ".git/143-review-evidence.json"), "failed setup integrity must not seal readiness")
			}
			runs, err := os.ReadFile(filepath.Join(f.workDir, "executions.log"))
			require.NoError(t, err, "setup invocation history should remain available")
			require.Equal(t, ".\n", string(runs), "source integrity failure must not retry setup in the same workspace")
		})
	}
}

func quoteIntegrityJSON(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err, "integrity recipe fixture should serialize its controlled command")
	return string(encoded)
}

func TestReviewObjectIntegrityRejectsCorruptionDespiteRewrittenReceipt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		expired bool
	}{{name: "warm reuse"}, {name: "expired verification", expired: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, "", false)
			o := f.orchestrator()
			require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "healthy source should create a cache receipt")
			object := copyReviewBaseBlobToLooseObject(t, f)
			corruptReviewLooseChecksum(t, object)
			receipt := filepath.Join(f.workDir, ".git/143-review-evidence.json")
			data, err := os.ReadFile(receipt)
			require.NoError(t, err, "existing readiness receipt should remain readable")
			var rewritten map[string]any
			require.NoError(t, json.Unmarshal(data, &rewritten), "valid receipt fixture should decode")
			rewritten["source_integrity"] = "verified"
			data, err = json.Marshal(rewritten)
			require.NoError(t, err, "self-attested receipt fixture should serialize without changing its accepted revision")
			require.NoError(t, os.WriteFile(receipt, data, 0o600), "fixture should rewrite only its own source receipt")
			o.github = nil
			ctx := context.Background()
			if tt.expired {
				ctx = WithCodeReviewWorkspacePreparationDeadline(ctx, time.Now().Add(-time.Second))
			}
			err = o.ensureCodeReviewWorkspaceEvidence(ctx, f.session, f.sandbox(), zerolog.Nop())
			require.ErrorIs(t, err, ErrCodeReviewSourceIntegrity, "a rewritten receipt cannot attest corrupt bytes, including after setup expiry")
			require.NotErrorIs(t, err, context.DeadlineExceeded, "expired source integrity failure should retain its concrete recreate-workspace guidance")
		})
	}
}

func TestReviewObjectIntegrityAcceptsPromisedHistoryAndOrdinaryRepacking(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "", false)
	o := f.orchestrator()
	require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "ordinary partial source should hydrate under the full hash barrier")
	cmd := exec.Command("/usr/bin/git", "-C", f.workDir, "cat-file", "-e", f.historyBlob)
	cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	require.Error(t, cmd.Run(), "unrelated promised historical blob should remain absent after captured hydration")
	for _, args := range [][]string{{"-c", "core.commitGraph=false", "repack", "-ad"}, {"multi-pack-index", "write"}} {
		out, err := exec.Command("/usr/bin/git", append([]string{"-C", f.workDir}, args...)...).CombinedOutput()
		require.NoError(t, err, "ordinary no-commitGraph repacking and MIDX creation should remain supported: %s", out)
	}
	require.FileExists(t, filepath.Join(f.workDir, ".git/objects/pack/multi-pack-index"), "positive repack control must exercise a real MIDX while verification remains enabled")
	require.NoError(t, os.Rename(strings.TrimPrefix(f.remote, "file://"), strings.TrimPrefix(f.remote, "file://")+"-offline"), "source remote should be unavailable for offline full validation")
	o.github = nil
	require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "full fsck plus closure must accept legitimate repacking and absent noncomparison promised history offline")
	ctx := WithCodeReviewWorkspacePreparationDeadline(context.Background(), time.Now().Add(-time.Second))
	require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(ctx, f.session, f.sandbox(), zerolog.Nop()), "expired verification should accept intact repacked sources without authentication or repair")
}

func TestReviewObjectIntegrityKeepsMissingCapturedBlobRepair(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "", false)
	o := f.orchestrator()
	require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "fixture should first publish complete captured sources")
	out, err := exec.Command("/usr/bin/git", "-C", f.workDir, "rev-parse", f.head+":changed.go").CombinedOutput()
	require.NoError(t, err, "fixture should identify a captured blob before removing its storage: %s", out)
	blob := strings.TrimSpace(string(out))
	indexes, err := filepath.Glob(filepath.Join(f.workDir, ".git/objects/pack/*.idx"))
	require.NoError(t, err, "ordinary blob pack indexes should be enumerable")
	removed := false
	for _, index := range indexes {
		out, err := exec.Command("/usr/bin/git", "verify-pack", "-v", index).CombinedOutput()
		require.NoError(t, err, "fixture should inspect ordinary pack contents without remapping them: %s", out)
		if strings.Contains(string(out), blob+" ") {
			for _, suffix := range []string{".pack", ".idx", ".promisor"} {
				if err := os.Remove(strings.TrimSuffix(index, ".idx") + suffix); err != nil && !os.IsNotExist(err) {
					require.NoError(t, err, "fixture should remove the complete existing captured blob pack")
				}
			}
			removed = true
		}
	}
	require.True(t, removed, "missing-capture control must remove a real pinned blob pack")
	require.NoError(t, o.execReviewEvidence(context.Background(), f.sandbox(), reviewGitObjectIntegrityScript(f.workDir), ""), "full fsck alone must permit absent promised objects in an otherwise valid partial clone")
	require.Error(t, o.execReviewEvidence(context.Background(), f.sandbox(), reviewTreeEvidenceScript(f.workDir, f.base, f.head, ""), ""), "independent captured closure must detect the required missing blob")
	tokens := &reviewConfigGitHubTokens{token: "fake-missing-capture-token"}
	o.github = tokens
	require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "genuinely missing promised captured content must remain repairable within the original budget")
	require.Equal(t, 1, tokens.calls, "missing captured evidence should authenticate exactly once after the required full hash pass succeeds")
}

func TestReviewObjectIntegrityOwnsPlatformPathAndPreservesNestedRecipeGit(t *testing.T) {
	t.Parallel()
	config := `{"review":{"commands":["mkdir -p .143/cache/go; printf source > .143/cache/go/api.go; git -C .143/cache/nested-repo rev-parse HEAD > .143/cache/nested-head"],"verify_commands":["test -f .143/cache/go/api.go; git -C .143/cache/nested-repo rev-parse HEAD >/dev/null"],"evidence":[{"path":".143/cache/go","description":"Local Go source","category":"go_dependencies"}]}}`
	f := newReviewEvidenceGitFixtureWithConfig(t, config, false)
	nested := filepath.Join(f.workDir, ".143/cache/nested-repo")
	for _, args := range [][]string{{"init", "-q", nested}, {"-C", nested, "-c", "user.name=Nested fixture", "-c", "user.email=nested@example.com", "commit", "-q", "--allow-empty", "-m", "nested source"}} {
		out, err := exec.Command("/usr/bin/git", args...).CombinedOutput()
		require.NoError(t, err, "ordinary nested Git fixture should be independently initialized: %s", out)
	}
	expected, err := exec.Command("/usr/bin/git", "-C", nested, "rev-parse", "HEAD").CombinedOutput()
	require.NoError(t, err, "independent nested source identity should resolve")
	root := t.TempDir()
	marker := filepath.Join(root, "ordinary-git")
	git := "#!/bin/sh\nprintf . >> '" + shellEscapeSingleQuote(marker) + "'\nexec /usr/bin/git \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "git"), []byte(git), 0o700), "ordinary recipe PATH fixture should mark nested Git calls without changing their behavior")
	sandbox := f.sandbox()
	sandbox.Env = map[string]string{"PATH": root + ":/usr/bin:/bin"}
	o := f.orchestrator()
	payload := reviewWorkspacePreparationPayload{WorkDir: f.workDir, Standalone: true, PlatformCommand: true, GuardConfiguration: true, ExpectedRepoURL: f.remote, Prepare: `test "$PATH" = /usr/bin:/bin`, TimeoutSeconds: 3, CancelPath: filepath.Join(root, "cancel")}
	require.NoError(t, o.execReviewPreparationPayload(context.Background(), sandbox, payload, reviewGitAuthentication{}, zerolog.Nop()), "platform stages must use the owned system PATH even when recipe tooling overrides it")
	require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, sandbox, zerolog.Nop()), "source preparation must preserve ordinary recipe PATH and clear platform-owned repository context for nested Git")
	out, err := exec.Command("/usr/bin/git", "-C", f.workDir, "multi-pack-index", "write").CombinedOutput()
	require.NoError(t, err, "warm full fsck should exercise an ordinary MIDX while recipe PATH is overridden: %s", out)
	o.github = nil
	require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, sandbox, zerolog.Nop()), "warm platform checks must stay independent of writable recipe Git while nested verifiers remain compatible")
	actual, err := os.ReadFile(filepath.Join(f.workDir, ".143/cache/nested-head"))
	require.NoError(t, err, "nested Git command should record its independent source identity")
	require.Equal(t, string(expected), string(actual), "ordinary recipe Git must resolve the nested checkout rather than inherited platform Git context")
	called, err := os.ReadFile(marker)
	require.NoError(t, err, "ordinary recipe invocation count should be available")
	require.Equal(t, "...", string(called), "only setup and two repository verifiers may use the ordinary recipe Git PATH")
}

func TestReviewObjectIntegrityRejectsCommitGraphMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		directory bool
	}{{name: "commit-graph"}, {name: "commit-graphs", directory: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, "printf forbidden > permit", false)
			target := filepath.Join(f.workDir, ".git/objects/info", tt.name)
			require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o700), "graph metadata parent should exist")
			if tt.directory {
				require.NoError(t, os.Mkdir(target, 0o700), "fixture should create only unneeded split commit-graph metadata")
			} else {
				require.NoError(t, os.WriteFile(target, []byte("unneeded graph metadata"), 0o600), "fixture should create only unneeded graph metadata")
			}
			tokens := &reviewConfigGitHubTokens{token: "fake-graph-token"}
			o := f.orchestrator()
			o.github = tokens
			require.Error(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "mutable commit-graph accelerators must fail the finite repository policy")
			require.Equal(t, 0, tokens.calls, "rejected graph metadata must never request installation authentication")
			require.NoFileExists(t, filepath.Join(f.workDir, "permit"), "rejected graph metadata must execute no repository recipe")
		})
	}
}

func TestReviewObjectIntegrityClassificationAndContextPrecedence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		cause error
		copy  string
	}{
		{name: "integrity", cause: ErrCodeReviewSourceIntegrity, copy: "Review workspace preparation could not verify pinned Git source object integrity. Recreate the review workspace and retry. Review did not run."},
		{name: "deadline", cause: errors.Join(ErrCodeReviewSourceIntegrity, context.DeadlineExceeded), copy: "Review workspace preparation exceeded its total eight-minute budget during pinned source hydration. Review did not run."},
		{name: "cancellation", cause: errors.Join(ErrCodeReviewSourceIntegrity, context.Canceled), copy: "Review workspace preparation was cancelled during pinned source hydration. Review did not run."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := NewCodeReviewWorkspacePreparationError(CodeReviewPreparationStageHydration, tt.cause)
			require.Equal(t, tt.copy, err.Error(), "source-integrity guidance must preserve total-budget and cancellation outcome priority")
		})
	}
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o700), "recipe failure fixture should contain literal metadata")
	payload := reviewWorkspacePreparationPayload{WorkDir: root, Receipt: json.RawMessage(`{"version":1}`), VerifyTrees: "true", VerifyGit: "true", VerifyTools: "true", Prepare: "exit 125", TimeoutSeconds: 1, CancelPath: filepath.Join(root, "cancel")}
	o := &Orchestrator{provider: localReviewEvidenceProvider{}}
	err := o.execReviewPreparationPayload(context.Background(), &Sandbox{WorkDir: root}, payload, reviewGitAuthentication{}, zerolog.Nop())
	require.Error(t, err, "ordinary repository exit125 should remain a setup command failure")
	require.NotErrorIs(t, err, ErrCodeReviewSourceIntegrity, "only trusted object validation may produce the internal integrity outcome")
}

type reviewZeroSourceReader struct{}

func (reviewZeroSourceReader) Read(value []byte) (int, error) { clear(value); return len(value), nil }

// Record only finite controller phases/remaining time for the fixed-deadline
// fixture. No command, authentication input, or inherited environment is logged.
type observedReviewIntegrityProvider struct {
	disconnectedReviewEvidenceProvider
}

func (p observedReviewIntegrityProvider) ExecWithStdin(ctx context.Context, sandbox *Sandbox, command string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	observed := strings.Replace(reviewWorkspacePreparationPython, "def capture(args):", `
def observe_integrity_phase(stage):
    with open(os.environ["REVIEW_TEST_INTEGRITY_PHASES"], "a") as saved:
        saved.write(json.dumps({"stage": stage, "remaining_seconds": deadline - time.monotonic(), "controller_pid": os.getpid(), "controller_pgid": os.getpgrp(), "active_pid": active.pid if active else None, "cancel_marker_present": bool(cancel_path and os.path.exists(cancel_path)), "completion_marker_present": bool(cancel_path and os.path.exists(cancel_path + ".done"))}) + "\n")
def capture(args):
    observe_integrity_phase("capture-enter")`, 1)
	observed = strings.Replace(observed, "    return data\n", "    observe_integrity_phase(\"capture-complete\")\n    return data\n", 1)
	observed = strings.Replace(observed, "def verify_object_integrity(script):", "def verify_object_integrity(script):\n    observe_integrity_phase(\"integrity-enter\")", 1)
	observed = strings.Replace(observed, "    child_directory =", "    if script == p.get(\"verify_integrity\"):\n        observe_integrity_phase(\"integrity-observed-script\" if \"controlled-git\" in script else \"integrity-unobserved-script\")\n    child_directory =", 1)
	observed = strings.Replace(observed, "    while True:\n        check_cancelled()", "    if script == p.get(\"verify_integrity\"):\n        observe_integrity_phase(\"integrity-popen\")\n    while True:\n        check_cancelled()", 1)
	observed = strings.Replace(observed, "def read_preparation_input():", "def read_preparation_input():\n    observe_integrity_phase(\"input-enter\")", 1)
	observed = strings.Replace(observed, "    message = read_preparation_input()", "    message = read_preparation_input()\n    observe_integrity_phase(\"input-complete\")", 1)
	command = strings.Replace(command, shellEscapeSingleQuote(reviewWorkspacePreparationPython), shellEscapeSingleQuote(observed), 1)
	command += " 2> '" + shellEscapeSingleQuote(sandbox.Env["REVIEW_TEST_INTEGRITY_PHASES"]+".stderr") + "'"
	return p.disconnectedReviewEvidenceProvider.ExecWithStdin(ctx, sandbox, command, stdin, stdout, stderr)
}

func TestReviewObjectIntegrityUsesOriginalDeadlineAndCancellation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		cancel bool
	}{{name: "absolute deadline"}, {name: "disconnected cancellation", cancel: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, "", false)
			// A legitimate large loose object gives the observer time to identify
			// and pause a real hash process. This proves process cleanup, rather
			// than benchmarking continuously active native hashing throughput.
			// Input streams zeros; neither memory nor the compressed file grows
			// to a GiB. No corrupted index, forged OID, or exploit is constructed.
			create := exec.Command("/usr/bin/git", "-C", f.workDir, "hash-object", "-w", "--stdin")
			create.Stdin = io.LimitReader(reviewZeroSourceReader{}, 1024*1024*1024)
			out, err := create.CombinedOutput()
			require.NoError(t, err, "large ordinary object fixture should be created by Git itself: %s", out)
			root := t.TempDir()
			marker, git := filepath.Join(root, "fsck-pid"), filepath.Join(root, "controlled-git")
			phases := filepath.Join(root, "phases.jsonl")
			observer := `import json, os, subprocess, sys
if "fsck" not in sys.argv[1:]:
    os.execv("/usr/bin/git", ["/usr/bin/git"] + sys.argv[1:])
child = subprocess.Popen(["/usr/bin/git"] + sys.argv[1:], stdin=subprocess.DEVNULL, close_fds=True)
with open(` + strconv.Quote(marker+".json") + `, "w") as saved:
    json.dump({"observer_pid": os.getpid(), "observer_pgid": os.getpgrp(), "git_pid": child.pid, "git_pgid": os.getpgid(child.pid)}, saved)
with open(` + strconv.Quote(marker) + `, "w") as saved:
    saved.write(str(child.pid))
sys.exit(child.wait())
`
			script := "#!/bin/sh\nexec /usr/bin/python3 -I -S -c '" + shellEscapeSingleQuote(observer) + "' \"$@\"\n"
			require.NoError(t, os.WriteFile(git, []byte(script), 0o700), "observer must record the PID returned after spawning the real trusted Git")
			o := f.orchestrator()
			o.provider = observedReviewIntegrityProvider{disconnectedReviewEvidenceProvider{localReviewEvidenceProvider{rewriteTrustedGit: git}}}
			sandbox := f.sandbox()
			sandbox.Env = map[string]string{"REVIEW_TEST_INTEGRITY_PHASES": phases}
			ctx := WithCodeReviewWorkspacePreparationDeadline(context.Background(), time.Now().Add(2*time.Second))
			cancel := func() {}
			if tt.cancel {
				ctx, cancel = context.WithCancel(ctx)
			}
			defer cancel()
			started := time.Now()
			completed := make(chan error, 1)
			var completedFailure error
			completedObserved := false
			go func() { completed <- o.ensureCodeReviewWorkspaceEvidence(ctx, f.session, sandbox, zerolog.Nop()) }()
			t.Cleanup(func() {
				if t.Failed() {
					if data, err := os.ReadFile(phases); err == nil {
						t.Logf("controlled integrity controller phases: %s", data)
					} else {
						t.Logf("controlled integrity controller phase read: %v", err)
					}
					if data, err := os.ReadFile(phases + ".stderr"); err == nil {
						t.Logf("controlled integrity wrapper stderr: %s", data)
					}
					if data, err := os.ReadFile(marker + ".json"); err == nil {
						t.Logf("controlled integrity observer process identities: %s", data)
					} else {
						t.Logf("controlled integrity observer identity read: %v", err)
					}
					failure, observed := completedFailure, completedObserved
					if !observed {
						select {
						case failure = <-completed:
							observed = true
						default:
							t.Log("controlled fixture output has not returned")
						}
					}
					if observed {
						for errors.Unwrap(failure) != nil {
							failure = errors.Unwrap(failure)
						}
						t.Logf("deepest controlled fixture failure: %T %#v", failure, failure)
					}
				}
			})
			var pid int
			require.Eventually(t, func() bool {
				data, err := os.ReadFile(marker)
				if err != nil {
					return false
				}
				pid, err = strconv.Atoi(string(data))
				return err == nil
			}, time.Second, time.Millisecond, "observer must expose a real child PID within the original fixed preparation allowance")
			process, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").CombinedOutput()
			require.NoError(t, err, "test-owned fsck process identity must be inspected without command arguments or environment: %s", process)
			require.Equal(t, "git", filepath.Base(strings.TrimSpace(string(process))), "observed child must be the real Git executable rather than its controller")
			require.NoError(t, syscall.Kill(pid, syscall.SIGSTOP), "the identified real Git process must still exist when the test pauses it")
			if tt.cancel {
				cancel()
			}
			select {
			case err = <-completed:
				completedFailure, completedObserved = err, true
			case <-time.After(4 * time.Second):
				t.Fatal("paused real fsck must terminate within the original two-second budget plus bounded cleanup allowance")
			}
			if tt.cancel {
				require.ErrorIs(t, err, context.Canceled, "disconnected fsck attachment must preserve cancellation after process cleanup")
			} else {
				require.ErrorIs(t, err, context.DeadlineExceeded, "full fsck must consume the existing absolute setup budget")
			}
			require.Less(t, time.Since(started), 4*time.Second, "fsck must use the original two-second budget plus bounded process cleanup, without renewing preparation time")
			terminationErr := syscall.Kill(pid, 0)
			if !errors.Is(terminationErr, syscall.ESRCH) {
				stateCtx, stateCancel := context.WithTimeout(context.Background(), time.Second)
				state, stateErr := exec.CommandContext(stateCtx, "ps", "-p", strconv.Itoa(pid), "-o", "pid=,ppid=,pgid=,stat=,comm=").CombinedOutput()
				stateCancel()
				t.Logf("controlled real Git state after failed preparation returned (read error=%v): %s", stateErr, state)
			}
			require.ErrorIs(t, terminationErr, syscall.ESRCH, "the identified paused real Git process must be gone before failed preparation returns")
		})
	}
}
