package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

const realReviewFilesystemFixtureID = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
const realReviewFilesystemCredentialSentinel = "PRIVATE_CREDENTIAL_SENTINEL_never_emit"

type realReviewFilesystemFixture struct {
	state, host, root, stateFile, lockFile string
	value                                  map[string]any
}

func newRealReviewFilesystemFixture(t *testing.T) realReviewFilesystemFixture {
	t.Helper()
	// /tmp and /var are symlinks on macOS. Fixtures use an already-physical root
	// so the production no-symlink contract remains identical on both hosts.
	state, err := os.MkdirTemp("/private/tmp", "real-review-filesystem-")
	if os.IsNotExist(err) {
		state, err = os.MkdirTemp("/tmp", "real-review-filesystem-")
	}
	require.NoError(t, err, "fixture must allocate an isolated physical runtime directory")
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(state), "fixture must clean only its owned runtime tree") })
	root := filepath.Join(state, "exec/runtime-runc/moby")
	require.NoError(t, os.MkdirAll(root, 0700), "fixture must create owned runsc state directories")
	host := "unix://" + state + "/docker.sock"
	config := map[string]any{"exec-root": state + "/exec", "data-root": state + "/mount/docker", "storage-driver": "overlay2", "hosts": []string{host}, "runtimes": map[string]any{"runsc": map[string]any{"path": "/usr/bin/runsc", "runtimeArgs": []string{"--host-uds=open"}}}}
	encoded, err := json.Marshal(config)
	require.NoError(t, err, "fixture daemon configuration must serialize")
	require.NoError(t, os.WriteFile(state+"/daemon.json", encoded, 0600), "fixture must bind daemon to its exact owned runtime")
	name := realReviewFilesystemFixtureID + "_sandbox:" + realReviewFilesystemFixtureID
	fixture := realReviewFilesystemFixture{state: state, host: host, root: root, stateFile: root + "/" + name + ".state", lockFile: root + "/" + name + ".lock", value: map[string]any{
		"id": realReviewFilesystemFixtureID, "sandbox": map[string]any{"id": realReviewFilesystemFixtureID, "pid": 1234},
		"saver":           map[string]any{"id": map[string]any{"containerId": realReviewFilesystemFixtureID, "sandboxId": realReviewFilesystemFixtureID}, "rootDir": root},
		"goferMountConfs": []any{map[string]any{"lower": 1, "upper": 2}},
		"spec":            map[string]any{"process": map[string]any{"env": []string{realReviewFilesystemCredentialSentinel}, "args": []string{realReviewFilesystemCredentialSentinel}}, "annotations": map[string]any{"private": realReviewFilesystemCredentialSentinel}},
	}}
	require.NoError(t, os.WriteFile(fixture.lockFile, nil, 0600), "fixture must provide the existing upstream sibling lock")
	fixture.write(t)
	return fixture
}

func (fixture realReviewFilesystemFixture) write(t *testing.T) {
	t.Helper()
	encoded, err := json.Marshal(fixture.value)
	require.NoError(t, err, "fixture state must serialize without retaining raw metadata in results")
	require.NoError(t, os.WriteFile(fixture.stateFile, encoded, 0600), "fixture must write an isolated state file")
}

func (fixture realReviewFilesystemFixture) read() (realReviewRuntimeFilesystemProof, error) {
	return readRealReviewRuntimeFilesystem(fixture.state, fixture.host, realReviewFilesystemRelease, realReviewFilesystemFixtureID, 1234, os.Geteuid(), os.Geteuid(), realReviewRootOverlayDefault)
}

func expectedRealReviewFilesystemProof(lower, upper int, size string, count int) realReviewRuntimeFilesystemProof {
	return realReviewRuntimeFilesystemProof{Schema: "runsc_effective_root_v1", RuntimeVersion: realReviewFilesystemRelease, ContainerID: realReviewFilesystemFixtureID, SandboxPID: 1234, GoferMountCount: count, Verified: true, Root: &realReviewGoferRootProof{Lower: lower, Upper: upper, LowerLabel: []string{"none", "lisafs", "erofs"}[lower], UpperLabel: []string{"none", "memory", "self", "anon"}[upper], SizeClassification: size}}
}

func TestRealReviewRuntimeFilesystemValidModes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		lower, upper int
		size         any
		includeSize  bool
		expectedSize string
	}{
		{"lisafs none", 1, 0, nil, false, "default"}, {"lisafs memory", 1, 1, "", true, "default"}, {"lisafs self", 1, 2, nil, false, "default"}, {"lisafs anon", 1, 3, "64m", true, "custom_size_present"},
		{"erofs none", 2, 0, nil, false, "default"}, {"erofs memory", 2, 1, nil, false, "default"}, {"erofs self", 2, 2, nil, false, "default"}, {"erofs anon", 2, 3, nil, false, "default"},
		{"tmpfs memory", 0, 1, nil, false, "default"}, {"tmpfs self", 0, 2, nil, false, "default"}, {"tmpfs anon", 0, 3, realReviewFilesystemCredentialSentinel, true, "custom_size_present"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newRealReviewFilesystemFixture(t)
			mount := map[string]any{"lower": tt.lower, "upper": tt.upper}
			if tt.includeSize {
				mount["size"] = tt.size
			}
			fixture.value["goferMountConfs"] = []any{mount, map[string]any{"lower": 0, "upper": 1}}
			fixture.write(t)
			proof, err := fixture.read()
			require.NoError(t, err, "valid persisted numeric modes must establish exact root proof")
			require.Equal(t, expectedRealReviewFilesystemProof(tt.lower, tt.upper, tt.expectedSize, 2), proof, "proof must preserve numeric modes and fixed labels with root first")
			encoded, err := json.Marshal(proof)
			require.NoError(t, err, "sanitized proof must serialize")
			require.NotContains(t, string(encoded), realReviewFilesystemCredentialSentinel, "ignored OCI credentials and custom size bytes must never enter proof")
		})
	}
}

func TestRealReviewRuntimeFilesystemRejectsMalformedState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, reason string
		mutate       func(map[string]any)
	}{
		{"wrong top id", "state_identity_mismatch", func(v map[string]any) { v["id"] = realReviewFilesystemCredentialSentinel }},
		{"missing sandbox", "state_identity_mismatch", func(v map[string]any) { delete(v, "sandbox") }},
		{"null sandbox", "state_identity_mismatch", func(v map[string]any) { v["sandbox"] = nil }},
		{"wrong sandbox id", "state_identity_mismatch", func(v map[string]any) { v["sandbox"].(map[string]any)["id"] = "other" }},
		{"missing pid", "state_identity_mismatch", func(v map[string]any) { delete(v["sandbox"].(map[string]any), "pid") }},
		{"pid mismatch", "state_identity_mismatch", func(v map[string]any) { v["sandbox"].(map[string]any)["pid"] = 1235 }},
		{"pid string", "state_identity_mismatch", func(v map[string]any) { v["sandbox"].(map[string]any)["pid"] = "1234" }},
		{"pid fraction", "state_identity_mismatch", func(v map[string]any) { v["sandbox"].(map[string]any)["pid"] = 1234.5 }},
		{"missing saver", "state_identity_mismatch", func(v map[string]any) { delete(v, "saver") }},
		{"wrong saver container", "state_identity_mismatch", func(v map[string]any) { v["saver"].(map[string]any)["id"].(map[string]any)["containerId"] = "other" }},
		{"wrong saver sandbox", "state_identity_mismatch", func(v map[string]any) { v["saver"].(map[string]any)["id"].(map[string]any)["sandboxId"] = "other" }},
		{"wrong saver root", "state_identity_mismatch", func(v map[string]any) {
			v["saver"].(map[string]any)["rootDir"] = realReviewFilesystemCredentialSentinel
		}},
		{"null mounts", "invalid_gofer_mount_configuration", func(v map[string]any) { v["goferMountConfs"] = nil }},
		{"empty mounts", "invalid_gofer_mount_configuration", func(v map[string]any) { v["goferMountConfs"] = []any{} }},
		{"missing mounts", "invalid_gofer_mount_configuration", func(v map[string]any) { delete(v, "goferMountConfs") }},
		{"null mount", "invalid_gofer_mount_configuration", func(v map[string]any) { v["goferMountConfs"] = []any{nil} }},
		{"too many mounts", "invalid_gofer_mount_configuration", func(v map[string]any) {
			mounts := make([]any, realReviewFilesystemMountLimit+1)
			for i := range mounts {
				mounts[i] = map[string]any{"lower": 1, "upper": 2}
			}
			v["goferMountConfs"] = mounts
		}},
		{"missing lower", "invalid_gofer_mount_configuration", func(v map[string]any) { v["goferMountConfs"] = []any{map[string]any{"upper": 2}} }},
		{"missing upper", "invalid_gofer_mount_configuration", func(v map[string]any) { v["goferMountConfs"] = []any{map[string]any{"lower": 1}} }},
		{"null lower", "invalid_gofer_mount_configuration", func(v map[string]any) { v["goferMountConfs"] = []any{map[string]any{"lower": nil, "upper": 2}} }},
		{"boolean upper", "invalid_gofer_mount_configuration", func(v map[string]any) { v["goferMountConfs"] = []any{map[string]any{"lower": 1, "upper": true}} }},
		{"string upper", "invalid_gofer_mount_configuration", func(v map[string]any) { v["goferMountConfs"] = []any{map[string]any{"lower": 1, "upper": "self"}} }},
		{"negative lower", "invalid_gofer_mount_configuration", func(v map[string]any) { v["goferMountConfs"] = []any{map[string]any{"lower": -1, "upper": 2}} }},
		{"unknown lower", "invalid_gofer_mount_configuration", func(v map[string]any) { v["goferMountConfs"] = []any{map[string]any{"lower": 3, "upper": 2}} }},
		{"unknown upper", "invalid_gofer_mount_configuration", func(v map[string]any) { v["goferMountConfs"] = []any{map[string]any{"lower": 1, "upper": 4}} }},
		{"fraction upper", "invalid_gofer_mount_configuration", func(v map[string]any) { v["goferMountConfs"] = []any{map[string]any{"lower": 1, "upper": 2.5}} }},
		{"no lower or upper", "invalid_gofer_mount_configuration", func(v map[string]any) { v["goferMountConfs"] = []any{map[string]any{"lower": 0, "upper": 0}} }},
		{"null size", "invalid_gofer_size_configuration", func(v map[string]any) {
			v["goferMountConfs"] = []any{map[string]any{"lower": 1, "upper": 2, "size": nil}}
		}},
		{"oversized size", "invalid_gofer_size_configuration", func(v map[string]any) {
			v["goferMountConfs"] = []any{map[string]any{"lower": 1, "upper": 2, "size": strings.Repeat("x", 129)}}
		}},
		{"invalid second mount", "invalid_gofer_mount_configuration", func(v map[string]any) {
			v["goferMountConfs"] = []any{map[string]any{"lower": 1, "upper": 2}, map[string]any{"lower": 0, "upper": 0}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newRealReviewFilesystemFixture(t)
			tt.mutate(fixture.value)
			fixture.write(t)
			proof, err := fixture.read()
			require.EqualError(t, err, "runtime filesystem proof unavailable: "+tt.reason, "invalid state must return a static classified reason")
			expected := expectedRealReviewFilesystemProof(1, 2, "default", 1)
			expected.Verified = false
			expected.Root = nil
			expected.GoferMountCount = 0
			expected.UnavailableReason = tt.reason
			require.Equal(t, expected, proof, "rejected state must never retain an effective configuration")
			encoded, marshalErr := json.Marshal(proof)
			require.NoError(t, marshalErr, "classified failure must serialize")
			require.NotContains(t, string(encoded)+err.Error(), realReviewFilesystemCredentialSentinel, "failed state must never leak raw credentials or paths")
		})
	}
}

func TestRealReviewRuntimeFilesystemSafeReads(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, reason string
		change       func(*testing.T, realReviewFilesystemFixture)
	}{
		{"missing lock", "lock_missing_or_unsafe", func(t *testing.T, f realReviewFilesystemFixture) {
			require.NoError(t, os.Remove(f.lockFile), "fixture removes existing lock")
		}},
		{"lock held", "lock_unavailable", func(t *testing.T, f realReviewFilesystemFixture) {
			fd, err := unix.Open(f.lockFile, unix.O_RDONLY, 0)
			require.NoError(t, err, "fixture opens existing lock")
			require.NoError(t, unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB), "fixture exclusively holds metadata lock")
			t.Cleanup(func() {
				require.NoError(t, unix.Flock(fd, unix.LOCK_UN), "fixture releases metadata lock")
				require.NoError(t, unix.Close(fd), "fixture closes lock descriptor")
			})
		}},
		{"state symlink", "state_missing_unsafe_or_oversized", func(t *testing.T, f realReviewFilesystemFixture) {
			require.NoError(t, os.Remove(f.stateFile), "fixture removes regular state")
			require.NoError(t, os.Symlink(f.state+"/daemon.json", f.stateFile), "fixture redirects final state component")
		}},
		{"lock symlink", "lock_missing_or_unsafe", func(t *testing.T, f realReviewFilesystemFixture) {
			require.NoError(t, os.Remove(f.lockFile), "fixture removes regular lock")
			require.NoError(t, os.Symlink(f.stateFile, f.lockFile), "fixture redirects final lock component")
		}},
		{"exec symlink", "unsafe_runsc_directory", func(t *testing.T, f realReviewFilesystemFixture) {
			require.NoError(t, os.Rename(f.state+"/exec", f.state+"/hidden"), "fixture moves owned state")
			require.NoError(t, os.Symlink("hidden", f.state+"/exec"), "fixture redirects intermediate component")
		}},
		{"runtime symlink", "unsafe_runsc_directory", func(t *testing.T, f realReviewFilesystemFixture) {
			require.NoError(t, os.Rename(f.state+"/exec/runtime-runc", f.state+"/exec/hidden"), "fixture moves runtime directory")
			require.NoError(t, os.Symlink("hidden", f.state+"/exec/runtime-runc"), "fixture redirects runtime component")
		}},
		{"namespace symlink", "unsafe_runsc_directory", func(t *testing.T, f realReviewFilesystemFixture) {
			require.NoError(t, os.Rename(f.root, f.state+"/exec/runtime-runc/hidden"), "fixture moves namespace directory")
			require.NoError(t, os.Symlink("hidden", f.root), "fixture redirects namespace component")
		}},
		{"fifo lock", "lock_missing_or_unsafe", func(t *testing.T, f realReviewFilesystemFixture) {
			require.NoError(t, os.Remove(f.lockFile), "fixture removes regular lock")
			require.NoError(t, unix.Mkfifo(f.lockFile, 0600), "fixture creates nonblocking FIFO lock")
		}},
		{"directory lock", "lock_missing_or_unsafe", func(t *testing.T, f realReviewFilesystemFixture) {
			require.NoError(t, os.Remove(f.lockFile), "fixture removes regular lock")
			require.NoError(t, os.Mkdir(f.lockFile, 0700), "fixture creates nonregular lock")
		}},
		{"hardlinked lock", "lock_missing_or_unsafe", func(t *testing.T, f realReviewFilesystemFixture) {
			require.NoError(t, os.Link(f.lockFile, f.state+"/lock-alias"), "fixture creates forbidden lock hardlink")
		}},
		{"writable lock", "lock_missing_or_unsafe", func(t *testing.T, f realReviewFilesystemFixture) {
			require.NoError(t, os.Chmod(f.lockFile, 0620), "fixture makes lock group writable")
		}},
		{"fifo state", "state_missing_unsafe_or_oversized", func(t *testing.T, f realReviewFilesystemFixture) {
			require.NoError(t, os.Remove(f.stateFile), "fixture removes regular state")
			require.NoError(t, unix.Mkfifo(f.stateFile, 0600), "fixture creates an unopened FIFO without a producer")
		}},
		{"directory state", "state_missing_unsafe_or_oversized", func(t *testing.T, f realReviewFilesystemFixture) {
			require.NoError(t, os.Remove(f.stateFile), "fixture removes regular state")
			require.NoError(t, os.Mkdir(f.stateFile, 0700), "fixture creates nonregular state")
		}},
		{"hard linked state", "state_missing_unsafe_or_oversized", func(t *testing.T, f realReviewFilesystemFixture) {
			require.NoError(t, os.Link(f.stateFile, f.state+"/alias"), "fixture adds forbidden state hardlink")
		}},
		{"writable state", "state_missing_unsafe_or_oversized", func(t *testing.T, f realReviewFilesystemFixture) {
			require.NoError(t, os.Chmod(f.stateFile, 0620), "fixture makes state group writable")
		}},
		{"writable runtime directory", "unsafe_runtime_metadata", func(t *testing.T, f realReviewFilesystemFixture) {
			require.NoError(t, os.Chmod(f.state, 0720), "fixture makes owned root group writable")
		}},
		{"oversized state", "state_missing_unsafe_or_oversized", func(t *testing.T, f realReviewFilesystemFixture) {
			require.NoError(t, os.WriteFile(f.stateFile, bytes.Repeat([]byte("x"), realReviewFilesystemStateLimit+1), 0600), "fixture exceeds explicit state read cap")
		}},
		{"trailing json", "malformed_state", func(t *testing.T, f realReviewFilesystemFixture) {
			data, err := os.ReadFile(f.stateFile)
			require.NoError(t, err, "fixture reads own synthetic state")
			require.NoError(t, os.WriteFile(f.stateFile, append(data, []byte(` {"secret":"`+realReviewFilesystemCredentialSentinel+`"}`)...), 0600), "fixture appends forbidden JSON value")
		}},
		{"duplicate critical key", "malformed_state", func(t *testing.T, f realReviewFilesystemFixture) {
			data, err := os.ReadFile(f.stateFile)
			require.NoError(t, err, "fixture reads own synthetic state")
			require.NoError(t, os.WriteFile(f.stateFile, append([]byte(`{"id":"`+realReviewFilesystemCredentialSentinel+`",`), data[1:]...), 0600), "fixture repeats critical identity key")
		}},
		{"truncated credential", "malformed_state", func(t *testing.T, f realReviewFilesystemFixture) {
			require.NoError(t, os.WriteFile(f.stateFile, []byte(`{"private":"`+realReviewFilesystemCredentialSentinel), 0600), "fixture truncates secret-bearing JSON")
		}},
		{"deep nesting", "malformed_state", func(t *testing.T, f realReviewFilesystemFixture) {
			require.NoError(t, os.WriteFile(f.stateFile, []byte(`{"private":`+strings.Repeat("[", 50)+"0"+strings.Repeat("]", 50)+"}"), 0600), "fixture exceeds decoder nesting cap")
		}},
		{"daemon wrong registration", "unbound_daemon_configuration", func(t *testing.T, f realReviewFilesystemFixture) {
			require.NoError(t, os.WriteFile(f.state+"/daemon.json", []byte(`{"exec-root":"`+realReviewFilesystemCredentialSentinel+`"}`), 0600), "fixture breaks exact owned daemon binding")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newRealReviewFilesystemFixture(t)
			tt.change(t, fixture)
			started := time.Now()
			proof, err := fixture.read()
			require.EqualError(t, err, "runtime filesystem proof unavailable: "+tt.reason, "unsafe or unavailable metadata must have a static failure classification")
			require.Less(t, time.Since(started), time.Second, "busy locks and FIFOs must never block metadata observation")
			require.False(t, proof.Verified, "unavailable runtime metadata must never substitute compiled defaults")
			require.Nil(t, proof.Root, "unavailable runtime metadata must retain no root mode")
			encoded, marshalErr := json.Marshal(proof)
			require.NoError(t, marshalErr, "sanitized failure proof must serialize")
			require.NotContains(t, string(encoded)+err.Error(), realReviewFilesystemCredentialSentinel, "all metadata errors must redact raw payload and paths")
		})
	}
}

func TestRealReviewRuntimeFilesystemInputsAndProtocol(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		raw   func(realReviewFilesystemFixture, realReviewRuntimeFilesystemProof) []byte
		valid bool
	}{
		{"valid", func(_ realReviewFilesystemFixture, p realReviewRuntimeFilesystemProof) []byte {
			b, err := json.Marshal(p)
			if err != nil {
				panic(err)
			}
			return []byte(realReviewFilesystemProbePrefix + string(b) + "\nPASS\n")
		}, true},
		{"duplicate frame", func(_ realReviewFilesystemFixture, _ realReviewRuntimeFilesystemProof) []byte {
			return []byte(realReviewFilesystemProbePrefix + "{}\n" + realReviewFilesystemProbePrefix + "{}\nPASS\n")
		}, false},
		{"truncated frame", func(_ realReviewFilesystemFixture, _ realReviewRuntimeFilesystemProof) []byte {
			return []byte(realReviewFilesystemProbePrefix + `{"private":"` + realReviewFilesystemCredentialSentinel)
		}, false},
		{"raw leaked field", func(_ realReviewFilesystemFixture, p realReviewRuntimeFilesystemProof) []byte {
			b, err := json.Marshal(p)
			if err != nil {
				panic(err)
			}
			return []byte(realReviewFilesystemProbePrefix + `{"private":"` + realReviewFilesystemCredentialSentinel + `",` + string(b[1:]) + "\nPASS\n")
		}, false},
		{"oversized", func(_ realReviewFilesystemFixture, _ realReviewRuntimeFilesystemProof) []byte {
			return bytes.Repeat([]byte("x"), 8193)
		}, false},
		{"inconsistent label", func(_ realReviewFilesystemFixture, p realReviewRuntimeFilesystemProof) []byte {
			p.Root.UpperLabel = realReviewFilesystemCredentialSentinel
			b, err := json.Marshal(p)
			if err != nil {
				panic(err)
			}
			return []byte(realReviewFilesystemProbePrefix + string(b) + "\nPASS\n")
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newRealReviewFilesystemFixture(t)
			expected := expectedRealReviewFilesystemProof(1, 2, "default", 1)
			request := realReviewFilesystemProbeRequest{fixture.state, fixture.host, realReviewFilesystemRelease, realReviewFilesystemFixtureID, 1234, os.Geteuid(), realReviewRootOverlayDefault}
			actual, ok := realReviewFilesystemDecodeProbeOutput(tt.raw(fixture, expected), request)
			require.Equal(t, tt.valid, ok, "privileged result framing must reject duplicate, secret-bearing, and incomplete output")
			if tt.valid {
				require.Equal(t, expected, actual, "privileged protocol must preserve only the exact sanitized proof")
			} else {
				require.Equal(t, realReviewRuntimeFilesystemProof{}, actual, "invalid privileged output must retain no parsed fields")
			}
		})
	}
}

func TestRealReviewRuntimeFilesystemRejectsInputs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, reason string
		change       func(*realReviewFilesystemProbeRequest)
	}{
		{"unsupported release", "unsupported_runtime_version", func(r *realReviewFilesystemProbeRequest) { r.RuntimeVersion = "runsc version release-other" }},
		{"nonfull id", "invalid_container_identity", func(r *realReviewFilesystemProbeRequest) { r.ContainerID = realReviewFilesystemCredentialSentinel }},
		{"zero pid", "invalid_container_identity", func(r *realReviewFilesystemProbeRequest) { r.PID = 0 }},
		{"wrong daemon", "unbound_owned_daemon", func(r *realReviewFilesystemProbeRequest) { r.DaemonHost = "unix:///var/run/docker.sock" }},
		{"escaping root", "unbound_owned_daemon", func(r *realReviewFilesystemProbeRequest) { r.RuntimeState += "/child/../" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newRealReviewFilesystemFixture(t)
			request := realReviewFilesystemProbeRequest{f.state, f.host, realReviewFilesystemRelease, realReviewFilesystemFixtureID, 1234, os.Geteuid(), realReviewRootOverlayDefault}
			tt.change(&request)
			proof, err := readRealReviewRuntimeFilesystem(request.RuntimeState, request.DaemonHost, request.RuntimeVersion, request.ContainerID, request.PID, os.Geteuid(), os.Geteuid(), request.RootOverlay)
			require.EqualError(t, err, "runtime filesystem proof unavailable: "+tt.reason, "input rejection must not reach privileged metadata reads")
			encoded, marshalErr := json.Marshal(proof)
			require.NoError(t, marshalErr, "input rejection must serialize")
			require.NotContains(t, string(encoded)+err.Error(), realReviewFilesystemCredentialSentinel, "untrusted identity inputs must never be reflected into failures")
		})
	}
}

func TestRealReviewRuntimeFilesystemBoundedOutput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		size        int
		expectedErr bool
	}{{"bounded", 8192, false}, {"over cap", 8193, true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var output realReviewFilesystemBoundedOutput
			n, err := output.Write(bytes.Repeat([]byte("x"), tt.size))
			if tt.expectedErr {
				require.EqualError(t, err, "probe_output_limit", "privileged output must stop at a fixed byte cap")
				require.Equal(t, 0, n, "oversized output must retain none of the rejected write")
				require.True(t, output.exceeded, "overflow must remain explicit")
				require.Empty(t, output.data, "overflow chunk must never be buffered")
			} else {
				require.NoError(t, err, "exact cap must be accepted")
				require.Equal(t, tt.size, n, "accepted bytes must be fully counted")
				require.Equal(t, tt.size, len(output.data), "output retention must equal accepted bytes")
			}
		})
	}
}

func TestRealReviewRuntimeFilesystemJSONRejectsAmbiguity(t *testing.T) {
	t.Parallel()
	tests := []string{`{"a":null,"a":0}`, `{"ignored":{"private":1,"private":2}}`, `{"a":1} 0`, `{"a":1.0,"a":1}`, `[]`, `null`, `{"a":`}
	for index, raw := range tests {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			t.Parallel()
			decoded, ok := realReviewFilesystemDecode([]byte(raw))
			require.False(t, ok, "ambiguous or nonobject JSON must fail before identity decoding")
			require.Nil(t, decoded, "rejected JSON must not return raw credential-bearing content")
		})
	}
}

func TestRealReviewRuntimeFilesystemProbeCommand(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := []byte(`{"container_id":"fixture"}`)
	command := realReviewFilesystemProbeCommand(ctx, "/private/tmp/isolated-agent.test", request)
	require.Equal(t, "/usr/bin/sudo", command.Path, "privileged probe must select the fixed sudo executable without shell evaluation")
	require.Equal(t, []string{"/usr/bin/sudo", "-n", "--", "/usr/bin/env", "REVIEW_REAL_RUNTIME_FILESYSTEM_PROBE=1", "/private/tmp/isolated-agent.test", "-test.run=^TestRealReviewRuntimeFilesystemPrivilegedProbe$", "-test.count=1", "-test.timeout=3s"}, command.Args, "privilege must select only the gated observer test with its own bounded lifetime")
	require.Equal(t, []string{"PATH=/usr/bin:/bin"}, command.Env, "credential-bearing parent environment must not enter sudo")
	require.Equal(t, 250*time.Millisecond, command.WaitDelay, "pipe-copy shutdown must remain bounded if sudo exits before its child")
	encoded, err := io.ReadAll(command.Stdin)
	require.NoError(t, err, "explicit bounded request must be available only on stdin")
	require.Equal(t, request, encoded, "source paths and identities must not be exposed through command-line arguments")
}

func TestRealReviewRuntimeFilesystemOwnedDaemonAndMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		change   func(*testing.T, realReviewFilesystemFixture)
		expected bool
	}{
		{"exact owned daemon", func(_ *testing.T, _ realReviewFilesystemFixture) {}, true},
		{"symlink owned root", func(t *testing.T, f realReviewFilesystemFixture) {
			require.NoError(t, os.Rename(f.state, f.state+"-moved"), "fixture moves owned runtime root")
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(f.state+"-moved"), "fixture removes relocated runtime root") })
			require.NoError(t, os.Symlink(f.state+"-moved", f.state), "fixture redirects owned root")
		}, false},
		{"symlink daemon config", func(t *testing.T, f realReviewFilesystemFixture) {
			require.NoError(t, os.Rename(f.state+"/daemon.json", f.state+"/moved.json"), "fixture moves daemon config")
			require.NoError(t, os.Symlink("moved.json", f.state+"/daemon.json"), "fixture redirects daemon configuration")
		}, false},
		{"writable daemon config", func(t *testing.T, f realReviewFilesystemFixture) {
			require.NoError(t, os.Chmod(f.state+"/daemon.json", 0660), "fixture makes daemon config group writable")
		}, false},
		{"hardlinked daemon config", func(t *testing.T, f realReviewFilesystemFixture) {
			require.NoError(t, os.Link(f.state+"/daemon.json", f.state+"/other.json"), "fixture creates forbidden daemon config hardlink")
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newRealReviewFilesystemFixture(t)
			tt.change(t, f)
			request := realReviewFilesystemProbeRequest{f.state, f.host, realReviewFilesystemRelease, realReviewFilesystemFixtureID, 1234, os.Geteuid(), realReviewRootOverlayDefault}
			require.Equal(t, tt.expected, realReviewFilesystemOwnedDaemon(request), "privileged execution must require a safely owned and exactly bound daemon config before sudo")
		})
	}
}

func TestRealReviewRuntimeFilesystemRejectsForeignOwner(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                string
		stateUID, configUID int
		reason              string
	}{{"foreign state", os.Geteuid() + 1, os.Geteuid(), "unsafe_runsc_directory"}, {"foreign configuration", os.Geteuid(), os.Geteuid() + 1, "unsafe_runtime_metadata"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newRealReviewFilesystemFixture(t)
			proof, err := readRealReviewRuntimeFilesystem(f.state, f.host, realReviewFilesystemRelease, realReviewFilesystemFixtureID, 1234, tt.stateUID, tt.configUID, realReviewRootOverlayDefault)
			require.EqualError(t, err, "runtime filesystem proof unavailable: "+tt.reason, "metadata from an unexpected owner must fail closed")
			require.False(t, proof.Verified, "foreign ownership cannot establish effective runtime proof")
		})
	}
}
