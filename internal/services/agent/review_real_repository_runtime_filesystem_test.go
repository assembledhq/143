package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const realReviewFilesystemRelease = "runsc version release-20261005.0"
const realReviewFilesystemStateLimit = 1 << 20 // Observer policy, not an upstream state-file maximum.
const realReviewFilesystemMountLimit = 256

// This allowlist deliberately contains no host paths, OCI spec, environment,
// command, annotations, or raw parser errors. A live state file contains secrets.
type realReviewRuntimeFilesystemProof struct {
	Schema            string                    `json:"schema"`
	RootOverlay       realReviewRootOverlay     `json:"root_overlay_selector,omitempty"`
	RuntimeVersion    string                    `json:"runtime_version,omitempty"`
	ContainerID       string                    `json:"container_id,omitempty"`
	SandboxPID        int                       `json:"sandbox_pid,omitempty"`
	GoferMountCount   int                       `json:"gofer_mount_count"`
	Root              *realReviewGoferRootProof `json:"root,omitempty"`
	Verified          bool                      `json:"verified"`
	CleanupFailed     bool                      `json:"cleanup_failed"`
	UnavailableReason string                    `json:"unavailable_reason,omitempty"`
}

type realReviewGoferRootProof struct {
	Lower              int    `json:"lower"`
	Upper              int    `json:"upper"`
	LowerLabel         string `json:"lower_label"`
	UpperLabel         string `json:"upper_label"`
	SizeClassification string `json:"size_classification"`
}

type realReviewFilesystemProbeRequest struct {
	RuntimeState   string                `json:"runtime_state"`
	DaemonHost     string                `json:"daemon_host"`
	RuntimeVersion string                `json:"runtime_version"`
	ContainerID    string                `json:"container_id"`
	PID            int                   `json:"pid"`
	CallerUID      int                   `json:"caller_uid"`
	RootOverlay    realReviewRootOverlay `json:"root_overlay_selector,omitempty"`
}

const realReviewFilesystemProbePrefix = "REAL_REVIEW_RUNTIME_FILESYSTEM_V1:"

func observeRealReviewRuntimeFilesystem(ctx context.Context, runtimeState, daemonHost, runtimeVersion, containerID string, pid int, rootOverlay realReviewRootOverlay) (realReviewRuntimeFilesystemProof, error) {
	request := realReviewFilesystemProbeRequest{RuntimeState: runtimeState, DaemonHost: daemonHost, RuntimeVersion: runtimeVersion, ContainerID: containerID, PID: pid, CallerUID: os.Geteuid(), RootOverlay: rootOverlay}
	if runtimeVersion != realReviewFilesystemRelease {
		return realReviewFilesystemUnavailable("unsupported_runtime_version")
	}
	if !realReviewFilesystemRequestValid(request) || runtime.GOOS != "linux" {
		return realReviewFilesystemUnavailable("invalid_probe_request")
	}
	if !realReviewFilesystemOwnedDaemon(request) {
		return realReviewFilesystemUnavailable("unsafe_owned_daemon")
	}
	if os.Geteuid() == 0 {
		return readRealReviewRuntimeFilesystem(runtimeState, daemonHost, runtimeVersion, containerID, pid, 0, 0, rootOverlay)
	}
	executable, err := os.Executable()
	if err != nil {
		return realReviewFilesystemUnavailable("probe_executable_unavailable")
	}
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) > 4096 {
		return realReviewFilesystemUnavailable("invalid_probe_request")
	}
	bounded, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	// The child is this same test binary running one read-only, gated test. sudo
	// supplies SUDO_UID; only PATH enters sudo, with no inherited credentials.
	command := realReviewFilesystemProbeCommand(bounded, executable, encoded)
	var output realReviewFilesystemBoundedOutput
	command.Stdout, command.Stderr = &output, io.Discard
	if err := command.Run(); err != nil || output.exceeded {
		return realReviewFilesystemUnavailable("privileged_probe_unavailable")
	}
	proof, ok := realReviewFilesystemDecodeProbeOutput(output.data, request)
	if !ok {
		return realReviewFilesystemUnavailable("invalid_probe_response")
	}
	if !proof.Verified {
		if proof.CleanupFailed {
			return proof, errors.Join(errors.New("runtime filesystem proof unavailable: "+proof.UnavailableReason), errors.New("runtime filesystem metadata cleanup failed"))
		}
		return proof, errors.New("runtime filesystem proof unavailable: " + proof.UnavailableReason)
	}
	return proof, nil
}

func realReviewFilesystemProbeCommand(ctx context.Context, executable string, request []byte) *exec.Cmd {
	command := exec.CommandContext(ctx, "/usr/bin/sudo", "-n", "--", "/usr/bin/env", "REVIEW_REAL_RUNTIME_FILESYSTEM_PROBE=1", executable, "-test.run=^TestRealReviewRuntimeFilesystemPrivilegedProbe$", "-test.count=1", "-test.timeout=3s")
	command.Env = []string{"PATH=/usr/bin:/bin"}
	command.Stdin = bytes.NewReader(request)
	// A killed sudo must not leave pipe-copy goroutines waiting on descendants.
	command.WaitDelay = 250 * time.Millisecond
	return command
}

type realReviewFilesystemBoundedOutput struct {
	data     []byte
	exceeded bool
}

func (output *realReviewFilesystemBoundedOutput) Write(data []byte) (int, error) {
	if len(data) > 8192-len(output.data) {
		output.exceeded = true
		return 0, errors.New("probe_output_limit")
	}
	output.data = append(output.data, data...)
	return len(data), nil
}

func realReviewFilesystemRequestValid(request realReviewFilesystemProbeRequest) bool {
	return request.RootOverlay.valid() && len(request.RuntimeState) <= 1024 && filepath.IsAbs(request.RuntimeState) && filepath.Clean(request.RuntimeState) == request.RuntimeState && request.RuntimeState != "/" && !strings.ContainsAny(request.RuntimeState, "\x00\r\n") && request.DaemonHost == "unix://"+request.RuntimeState+"/docker.sock" && request.RuntimeVersion == realReviewFilesystemRelease && regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(request.ContainerID) && request.PID > 0 && request.PID <= 2147483647 && request.CallerUID >= 0 && request.CallerUID <= 2147483647
}

func realReviewFilesystemOwnedDaemon(request realReviewFilesystemProbeRequest) (safe bool) {
	root, err := realReviewFilesystemOpenAbsoluteDirectory(request.RuntimeState)
	if err != nil {
		return false
	}
	defer func() {
		if err := unix.Close(root); err != nil {
			safe = false
		}
	}()
	if !realReviewFilesystemSafeMetadata(root, true, request.CallerUID) {
		return false
	}
	config, err := realReviewFilesystemReadAt(root, "daemon.json", 65536, request.CallerUID)
	return err == nil && realReviewFilesystemDaemonBound(config, request.RuntimeState, request.DaemonHost, request.RootOverlay)
}

func realReviewFilesystemUnavailable(reason string) (realReviewRuntimeFilesystemProof, error) {
	return realReviewRuntimeFilesystemProof{Schema: "runsc_effective_root_v1", UnavailableReason: reason}, errors.New("runtime filesystem proof unavailable: " + reason)
}

func realReviewFilesystemDecodeProbeOutput(raw []byte, request realReviewFilesystemProbeRequest) (realReviewRuntimeFilesystemProof, bool) {
	var proof realReviewRuntimeFilesystemProof
	if !realReviewFilesystemRequestValid(request) || len(raw) > 8192 || !bytes.HasPrefix(raw, []byte(realReviewFilesystemProbePrefix)) || !bytes.HasSuffix(raw, []byte("\nPASS\n")) || bytes.Count(raw, []byte(realReviewFilesystemProbePrefix)) != 1 {
		return proof, false
	}
	data := raw[len(realReviewFilesystemProbePrefix) : len(raw)-len("\nPASS\n")]
	object, valid := realReviewFilesystemDecode(data)
	if !valid || object == nil {
		return proof, false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&proof) != nil || proof.Schema != "runsc_effective_root_v1" || proof.ContainerID != request.ContainerID || proof.SandboxPID != request.PID || proof.RuntimeVersion != realReviewFilesystemRelease || proof.RootOverlay != request.RootOverlay {
		return realReviewRuntimeFilesystemProof{}, false
	}
	if !proof.Verified {
		allowed := []string{"invalid_container_identity", "unsupported_runtime_version", "unbound_owned_daemon", "unsafe_runtime_directory", "unsafe_runtime_metadata", "unsafe_daemon_configuration", "unbound_daemon_configuration", "unsafe_runsc_directory", "lock_missing_or_unsafe", "lock_unavailable", "state_missing_unsafe_or_oversized", "malformed_state", "state_identity_mismatch", "invalid_gofer_mount_configuration", "invalid_gofer_size_configuration", "directory_close_failed", "lock_close_failed", "lock_release_failed", "invalid_root_overlay_selector", "root_overlay_mismatch"}
		for _, reason := range allowed {
			if proof.UnavailableReason == reason && proof.Root == nil && proof.GoferMountCount == 0 {
				return proof, true
			}
		}
		return realReviewRuntimeFilesystemProof{}, false
	}
	root := proof.Root
	if root == nil || proof.CleanupFailed || proof.UnavailableReason != "" || proof.GoferMountCount < 1 || proof.GoferMountCount > realReviewFilesystemMountLimit || root.Lower < 0 || root.Lower > 2 || root.Upper < 0 || root.Upper > 3 || root.Lower == 0 && root.Upper == 0 || root.LowerLabel != []string{"none", "lisafs", "erofs"}[root.Lower] || root.UpperLabel != []string{"none", "memory", "self", "anon"}[root.Upper] || root.SizeClassification != "default" && root.SizeClassification != "custom_size_present" {
		return realReviewRuntimeFilesystemProof{}, false
	}
	if realReviewEffectiveRootMatches(request.RootOverlay, proof) != nil {
		return realReviewRuntimeFilesystemProof{}, false
	}
	return proof, true
}

func realReviewFilesystemDecodeProbeRequest(encoded []byte, callerUID int) (realReviewFilesystemProbeRequest, bool) {
	var request realReviewFilesystemProbeRequest
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	_, uniqueRequest := realReviewFilesystemDecode(encoded)
	var trailing any
	if len(encoded) > 4096 || !uniqueRequest || decoder.Decode(&request) != nil || decoder.Decode(&trailing) != io.EOF || callerUID != request.CallerUID || !realReviewFilesystemRequestValid(request) {
		return realReviewFilesystemProbeRequest{}, false
	}
	return request, true
}

func TestRealReviewRuntimeFilesystemPrivilegedProbe(t *testing.T) {
	t.Parallel()
	if os.Getenv("REVIEW_REAL_RUNTIME_FILESYSTEM_PROBE") != "1" {
		t.Skip("private read-only runtime probe requires explicit isolated invocation")
	}
	encoded, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
	uid, uidErr := strconv.Atoi(os.Getenv("SUDO_UID"))
	request, validRequest := realReviewFilesystemDecodeProbeRequest(encoded, uid)
	if runtime.GOOS != "linux" || os.Geteuid() != 0 || err != nil || uidErr != nil || !validRequest {
		t.Fatal("invalid privileged runtime probe request")
	}
	proof, proofErr := readRealReviewRuntimeFilesystem(request.RuntimeState, request.DaemonHost, request.RuntimeVersion, request.ContainerID, request.PID, 0, request.CallerUID, request.RootOverlay)
	if proofErr != nil && proof.UnavailableReason == "" {
		t.Fatal("runtime proof failure classification missing")
	}
	output, err := json.Marshal(proof)
	if err != nil {
		t.Fatal("runtime proof serialization failed")
	}
	if _, err := os.Stdout.Write(append(append([]byte(realReviewFilesystemProbePrefix), output...), '\n')); err != nil {
		t.Fatal("runtime proof delivery failed")
	}
}

// Fixture ownership is explicit; the live entry point always requires root-owned
// runsc metadata. No package-level hooks or runtime commands are used.
func readRealReviewRuntimeFilesystem(runtimeState, daemonHost, runtimeVersion, containerID string, pid, stateUID, configUID int, rootOverlay realReviewRootOverlay) (proof realReviewRuntimeFilesystemProof, resultErr error) {
	proof.Schema = "runsc_effective_root_v1"
	fail := func(reason string) (realReviewRuntimeFilesystemProof, error) {
		proof.Verified, proof.Root, proof.GoferMountCount = false, nil, 0
		proof.UnavailableReason = reason
		return proof, errors.New("runtime filesystem proof unavailable: " + reason)
	}
	cleanupFailed := func(reason string) {
		proof.Verified, proof.Root, proof.GoferMountCount, proof.CleanupFailed = false, nil, 0, true
		if resultErr == nil {
			proof.UnavailableReason = reason
		}
		resultErr = errors.Join(resultErr, errors.New("runtime filesystem metadata cleanup failed: "+reason))
	}
	if !rootOverlay.valid() {
		return fail("invalid_root_overlay_selector")
	}
	proof.RootOverlay = rootOverlay
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(containerID) || pid <= 0 || pid > 2147483647 {
		return fail("invalid_container_identity")
	}
	proof.ContainerID, proof.SandboxPID = containerID, pid
	if runtimeVersion != realReviewFilesystemRelease {
		return fail("unsupported_runtime_version")
	}
	proof.RuntimeVersion = realReviewFilesystemRelease
	if !filepath.IsAbs(runtimeState) || filepath.Clean(runtimeState) != runtimeState || runtimeState == "/" || strings.ContainsAny(runtimeState, "\x00\r\n") || daemonHost != "unix://"+runtimeState+"/docker.sock" {
		return fail("unbound_owned_daemon")
	}
	owned, err := realReviewFilesystemOpenAbsoluteDirectory(runtimeState)
	if err != nil {
		return fail("unsafe_runtime_directory")
	}
	defer func() {
		if err := unix.Close(owned); err != nil {
			cleanupFailed("directory_close_failed")
		}
	}()
	if !realReviewFilesystemSafeMetadata(owned, true, configUID) {
		return fail("unsafe_runtime_metadata")
	}
	config, err := realReviewFilesystemReadAt(owned, "daemon.json", 65536, configUID)
	if err != nil {
		return fail("unsafe_daemon_configuration")
	}
	rootPath := runtimeState + "/exec/runtime-runc/moby"
	if !realReviewFilesystemDaemonBound(config, runtimeState, daemonHost, rootOverlay) {
		return fail("unbound_daemon_configuration")
	}
	root, err := realReviewFilesystemOpenDirectories(owned, []string{"exec", "runtime-runc", "moby"}, stateUID)
	if err != nil {
		return fail("unsafe_runsc_directory")
	}
	defer func() {
		if err := unix.Close(root); err != nil {
			cleanupFailed("directory_close_failed")
		}
	}()
	name := containerID + "_sandbox:" + containerID
	lock, err := unix.Openat(root, name+".lock", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return fail("lock_missing_or_unsafe")
	}
	defer func() {
		if err := unix.Close(lock); err != nil {
			cleanupFailed("lock_close_failed")
		}
	}()
	if !realReviewFilesystemSafeMetadata(lock, false, stateUID) {
		return fail("lock_missing_or_unsafe")
	}
	// One nonblocking shared attempt cannot extend the preparation deadline.
	if err := unix.Flock(lock, unix.LOCK_SH|unix.LOCK_NB); err != nil {
		return fail("lock_unavailable")
	}
	defer func() {
		if err := unix.Flock(lock, unix.LOCK_UN); err != nil {
			cleanupFailed("lock_release_failed")
		}
	}()
	state, err := realReviewFilesystemReadAt(root, name+".state", realReviewFilesystemStateLimit, stateUID)
	if err != nil {
		return fail("state_missing_unsafe_or_oversized")
	}
	decoded, ok := realReviewFilesystemDecode(state)
	if !ok {
		return fail("malformed_state")
	}
	sandbox, sandboxOK := decoded["sandbox"].(map[string]any)
	saver, saverOK := decoded["saver"].(map[string]any)
	savedID, savedIDOK := saver["id"].(map[string]any)
	actualPID, pidOK := realReviewFilesystemInteger(sandbox["pid"])
	if !sandboxOK || !saverOK || !savedIDOK || decoded["id"] != containerID || sandbox["id"] != containerID || savedID["containerId"] != containerID || savedID["sandboxId"] != containerID || saver["rootDir"] != rootPath || !pidOK || actualPID != int64(pid) {
		return fail("state_identity_mismatch")
	}
	mounts, ok := decoded["goferMountConfs"].([]any)
	if !ok || len(mounts) == 0 || len(mounts) > realReviewFilesystemMountLimit {
		return fail("invalid_gofer_mount_configuration")
	}
	var rootProof realReviewGoferRootProof
	for index, raw := range mounts {
		mount, ok := raw.(map[string]any)
		lower, lowerOK := realReviewFilesystemInteger(mount["lower"])
		upper, upperOK := realReviewFilesystemInteger(mount["upper"])
		if !ok || !lowerOK || !upperOK || lower < 0 || lower > 2 || upper < 0 || upper > 3 || lower == 0 && upper == 0 {
			return fail("invalid_gofer_mount_configuration")
		}
		sizeClass := "default"
		if rawSize, exists := mount["size"]; exists {
			size, sizeOK := rawSize.(string)
			if !sizeOK || len(size) > 128 {
				return fail("invalid_gofer_size_configuration")
			}
			if size != "" {
				sizeClass = "custom_size_present"
			}
		}
		if index == 0 {
			rootProof = realReviewGoferRootProof{Lower: int(lower), Upper: int(upper), LowerLabel: []string{"none", "lisafs", "erofs"}[lower], UpperLabel: []string{"none", "memory", "self", "anon"}[upper], SizeClassification: sizeClass}
		}
	}
	proof.GoferMountCount, proof.Root, proof.Verified = len(mounts), &rootProof, true
	if realReviewEffectiveRootMatches(rootOverlay, proof) != nil {
		return fail("root_overlay_mismatch")
	}
	return proof, nil
}

func realReviewFilesystemSafeMetadata(fd int, directory bool, owner int) bool {
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Uid != uint32(owner) || stat.Mode&0022 != 0 {
		return false
	}
	if directory {
		return stat.Mode&unix.S_IFMT == unix.S_IFDIR
	}
	return stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Nlink == 1
}

// Every lookup is relative to the already-open parent descriptor. O_NOFOLLOW
// applies to every component, so no EvalSymlinks/open time-of-check gap exists.
func realReviewFilesystemOpenAbsoluteDirectory(absolute string) (int, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, errors.New("directory_open_failed")
	}
	for _, component := range strings.Split(strings.TrimPrefix(absolute, "/"), "/") {
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		closeErr := unix.Close(fd)
		if openErr != nil || closeErr != nil {
			if openErr == nil {
				if err := unix.Close(next); err != nil {
					return -1, errors.New("directory_close_failed")
				}
			}
			return -1, errors.New("directory_open_failed")
		}
		fd = next
	}
	return fd, nil
}

func realReviewFilesystemOpenDirectories(parent int, components []string, owner int) (int, error) {
	fd, err := unix.Dup(parent)
	if err != nil {
		return -1, errors.New("directory_open_failed")
	}
	for _, component := range components {
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		closeErr := unix.Close(fd)
		if openErr != nil || closeErr != nil {
			if openErr == nil {
				if err := unix.Close(next); err != nil {
					return -1, errors.New("directory_close_failed")
				}
			}
			return -1, errors.New("directory_open_failed")
		}
		if !realReviewFilesystemSafeMetadata(next, true, owner) {
			if err := unix.Close(next); err != nil {
				return -1, errors.New("directory_close_failed")
			}
			return -1, errors.New("directory_metadata_unsafe")
		}
		fd = next
	}
	return fd, nil
}

func realReviewFilesystemReadAt(parent int, name string, limit int64, owner int) (data []byte, resultErr error) {
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("file_open_failed")
	}
	file := os.NewFile(uintptr(fd), "bounded_runtime_metadata")
	defer func() {
		if err := file.Close(); err != nil {
			data, resultErr = nil, errors.Join(resultErr, errors.New("file_close_failed"))
		}
	}()
	var stat unix.Stat_t
	if !realReviewFilesystemSafeMetadata(fd, false, owner) || unix.Fstat(fd, &stat) != nil || stat.Size < 0 || stat.Size > limit {
		return nil, errors.New("file_metadata_unsafe")
	}
	data, err = io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit || int64(len(data)) != stat.Size {
		return nil, errors.New("file_read_incomplete_or_oversized")
	}
	return data, nil
}

func realReviewFilesystemDaemonBound(raw []byte, state, host string, rootOverlay realReviewRootOverlay) bool {
	if !rootOverlay.valid() {
		return false
	}
	config, ok := realReviewFilesystemDecode(raw)
	if !ok || config["exec-root"] != state+"/exec" || config["data-root"] != state+"/mount/docker" || config["storage-driver"] != "overlay2" {
		return false
	}
	hosts, hostsOK := config["hosts"].([]any)
	runtimes, runtimesOK := config["runtimes"].(map[string]any)
	runsc, runscOK := runtimes["runsc"].(map[string]any)
	args, argsOK := runsc["runtimeArgs"].([]any)
	if !hostsOK || len(hosts) != 1 || hosts[0] != host || !runtimesOK || !runscOK || len(runsc) != 2 || runsc["path"] != "/usr/bin/runsc" || !argsOK || len(args) != len(rootOverlay.runscArgs()) {
		return false
	}
	for index, expected := range rootOverlay.runscArgs() {
		if args[index] != expected {
			return false
		}
	}
	return true
}

func realReviewFilesystemInteger(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	integer, err := strconv.ParseInt(string(number), 10, 64)
	return integer, err == nil
}

// Token decoding rejects duplicate keys everywhere, including ignored OCI data,
// and limits nesting before Go's general JSON decoder can allocate a deep tree.
func realReviewFilesystemDecode(raw []byte) (map[string]any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	remaining := 65536
	value, ok := realReviewFilesystemJSONValue(decoder, 0, &remaining)
	if !ok {
		return nil, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}
	object, ok := value.(map[string]any)
	return object, ok
}

func realReviewFilesystemJSONValue(decoder *json.Decoder, depth int, remaining *int) (any, bool) {
	if depth > 48 || *remaining <= 0 {
		return nil, false
	}
	*remaining--
	token, err := decoder.Token()
	if err != nil {
		return nil, false
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return token, true
	}
	switch delimiter {
	case '{':
		object := map[string]any{}
		for decoder.More() {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok {
				return nil, false
			}
			if _, duplicate := object[name]; duplicate {
				return nil, false
			}
			value, ok := realReviewFilesystemJSONValue(decoder, depth+1, remaining)
			if !ok {
				return nil, false
			}
			object[name] = value
		}
		closing, err := decoder.Token()
		return object, err == nil && closing == json.Delim('}')
	case '[':
		array := []any{}
		for decoder.More() {
			value, ok := realReviewFilesystemJSONValue(decoder, depth+1, remaining)
			if !ok {
				return nil, false
			}
			array = append(array, value)
		}
		closing, err := decoder.Token()
		return array, err == nil && closing == json.Delim(']')
	default:
		return nil, false
	}
}
