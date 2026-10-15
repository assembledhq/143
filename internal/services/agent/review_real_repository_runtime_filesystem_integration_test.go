package agent_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/require"
)

// A state-file read alone is insufficient: the same resource-bounded Docker
// sandbox must still be live after the observation. Never retain raw inspect errors.
func confirmRealReviewRuntimeFilesystem(proof *realReviewRuntimeFilesystemProof, before, after container.InspectResponse, inspectErr error) error {
	if proof == nil {
		return errors.New("runtime filesystem confirmation requires an observed proof")
	}
	reject := func() error {
		proof.Verified, proof.UnavailableReason = false, "sandbox_identity_changed"
		return errors.New("sandbox identity or resource configuration changed during runtime filesystem observation")
	}
	if inspectErr != nil || !proof.Verified || before.ContainerJSONBase == nil || after.ContainerJSONBase == nil ||
		before.State == nil || after.State == nil || before.HostConfig == nil || after.HostConfig == nil ||
		!before.State.Running || !after.State.Running || before.State.Pid <= 0 ||
		proof.ContainerID != before.ID || proof.SandboxPID != before.State.Pid ||
		proof.RuntimeVersion != realReviewFilesystemRelease || after.ID != before.ID || after.State.Pid != before.State.Pid {
		return reject()
	}
	if after.HostConfig.Runtime != "runsc" || after.HostConfig.Runtime != before.HostConfig.Runtime ||
		after.HostConfig.Memory != before.HostConfig.Memory || after.HostConfig.NanoCPUs != before.HostConfig.NanoCPUs ||
		after.HostConfig.StorageOpt["size"] != before.HostConfig.StorageOpt["size"] ||
		after.HostConfig.CgroupParent != before.HostConfig.CgroupParent {
		return reject()
	}
	return nil
}

func TestRealReviewRuntimeFilesystemPostReadIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*realReviewRuntimeFilesystemProof, *container.InspectResponse, *container.InspectResponse)
		err    error
		fail   bool
	}{
		{name: "same live bounded sandbox remains verified"},
		{name: "inspect failure is sanitized", err: errors.New("private-inspect-credential-marker"), fail: true},
		{name: "missing initial container", mutate: func(_ *realReviewRuntimeFilesystemProof, before, _ *container.InspectResponse) {
			before.ContainerJSONBase = nil
		}, fail: true},
		{name: "missing final container", mutate: func(_ *realReviewRuntimeFilesystemProof, _, after *container.InspectResponse) {
			after.ContainerJSONBase = nil
		}, fail: true},
		{name: "missing final state", mutate: func(_ *realReviewRuntimeFilesystemProof, _, after *container.InspectResponse) { after.State = nil }, fail: true},
		{name: "missing final resource configuration", mutate: func(_ *realReviewRuntimeFilesystemProof, _, after *container.InspectResponse) { after.HostConfig = nil }, fail: true},
		{name: "stopped during observation", mutate: func(_ *realReviewRuntimeFilesystemProof, _, after *container.InspectResponse) {
			after.State.Running = false
		}, fail: true},
		{name: "different container", mutate: func(_ *realReviewRuntimeFilesystemProof, _, after *container.InspectResponse) {
			after.ID = strings.Repeat("b", 64)
		}, fail: true},
		{name: "different live process", mutate: func(_ *realReviewRuntimeFilesystemProof, _, after *container.InspectResponse) { after.State.Pid++ }, fail: true},
		{name: "memory changed", mutate: func(_ *realReviewRuntimeFilesystemProof, _, after *container.InspectResponse) {
			after.HostConfig.Memory++
		}, fail: true},
		{name: "CPU changed", mutate: func(_ *realReviewRuntimeFilesystemProof, _, after *container.InspectResponse) {
			after.HostConfig.NanoCPUs++
		}, fail: true},
		{name: "disk quota changed", mutate: func(_ *realReviewRuntimeFilesystemProof, _, after *container.InspectResponse) {
			after.HostConfig.StorageOpt["size"] = "20G"
		}, fail: true},
		{name: "runtime changed", mutate: func(_ *realReviewRuntimeFilesystemProof, _, after *container.InspectResponse) {
			after.HostConfig.Runtime = "runc"
		}, fail: true},
		{name: "cgroup configuration changed", mutate: func(_ *realReviewRuntimeFilesystemProof, _, after *container.InspectResponse) {
			after.HostConfig.CgroupParent = "/shared"
		}, fail: true},
		{name: "unverified state cannot become proof", mutate: func(proof *realReviewRuntimeFilesystemProof, _, _ *container.InspectResponse) { proof.Verified = false }, fail: true},
		{name: "state belongs to another container", mutate: func(proof *realReviewRuntimeFilesystemProof, _, _ *container.InspectResponse) {
			proof.ContainerID = strings.Repeat("b", 64)
		}, fail: true},
		{name: "state belongs to another process", mutate: func(proof *realReviewRuntimeFilesystemProof, _, _ *container.InspectResponse) { proof.SandboxPID++ }, fail: true},
		{name: "unsupported state schema cannot be verified", mutate: func(proof *realReviewRuntimeFilesystemProof, _, _ *container.InspectResponse) {
			proof.RuntimeVersion = "unknown"
		}, fail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := func() container.InspectResponse {
				return container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{
					ID: strings.Repeat("a", 64), State: &container.State{Running: true, Pid: 41},
					HostConfig: &container.HostConfig{Runtime: "runsc", StorageOpt: map[string]string{"size": "10G"}, Resources: container.Resources{Memory: 3221225472, NanoCPUs: 2000000000}},
				}}
			}
			before, after := fixture(), fixture()
			proof := realReviewRuntimeFilesystemProof{Schema: "runsc_effective_root_v1", RuntimeVersion: realReviewFilesystemRelease, ContainerID: before.ID, SandboxPID: 41, Verified: true}
			if tt.mutate != nil {
				tt.mutate(&proof, &before, &after)
			}
			err := confirmRealReviewRuntimeFilesystem(&proof, before, after, tt.err)
			expected := proof
			expected.Verified, expected.UnavailableReason = true, ""
			if tt.fail {
				expected.Verified, expected.UnavailableReason = false, "sandbox_identity_changed"
				require.EqualError(t, err, "sandbox identity or resource configuration changed during runtime filesystem observation", "post-read mismatches must reject the proof with one sanitized material reason")
			} else {
				require.NoError(t, err, "the same live sandbox should keep its effective state proof")
			}
			require.Equal(t, expected, proof, "post-read confirmation must update the full proof's verification state without changing its observed fields")
		})
	}
}
