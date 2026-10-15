package agent_test

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/system"
	"github.com/stretchr/testify/require"
)

func TestRealReviewRootOverlaySelection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, value, state, runtime string
		optedIn                     bool
		expected                    realReviewRootOverlay
		err                         string
	}{
		{name: "ordinary unset stays default", expected: realReviewRootOverlayDefault},
		{name: "opted in default", optedIn: true, state: "/private/tmp/owned-runtime", runtime: "runsc", expected: realReviewRootOverlayDefault},
		{name: "explicit isolated candidate", value: "none", optedIn: true, state: "/private/tmp/owned-runtime", runtime: "runsc", expected: realReviewRootOverlayNone},
		{name: "unknown selector", value: "self", err: "unsupported acceptance root overlay selector"},
		{name: "flag injection", value: "none --unsafe", err: "unsupported acceptance root overlay selector"},
		{name: "candidate requires acceptance opt in", value: "none", state: "/private/tmp/owned-runtime", runtime: "runsc", err: "explicit root overlay requires opted-in owned runsc acceptance"},
		{name: "candidate requires runsc", value: "none", optedIn: true, state: "/private/tmp/owned-runtime", runtime: "runc", err: "explicit root overlay requires opted-in owned runsc acceptance"},
		{name: "candidate requires owned path", value: "none", optedIn: true, runtime: "runsc", err: "explicit root overlay requires opted-in owned runsc acceptance"},
		{name: "candidate rejects relative path", value: "none", optedIn: true, state: "owned-runtime", runtime: "runsc", err: "explicit root overlay requires opted-in owned runsc acceptance"},
		{name: "candidate rejects unowned root", value: "none", optedIn: true, state: "/", runtime: "runsc", err: "explicit root overlay requires opted-in owned runsc acceptance"},
		{name: "candidate rejects traversal", value: "none", optedIn: true, state: "/private/tmp/owned/../runtime", runtime: "runsc", err: "explicit root overlay requires opted-in owned runsc acceptance"},
		{name: "candidate rejects controls", value: "none", optedIn: true, state: "/private/tmp/owned\nruntime", runtime: "runsc", err: "explicit root overlay requires opted-in owned runsc acceptance"},
		{name: "candidate bounds path", value: "none", optedIn: true, state: "/" + strings.Repeat("x", 1024), runtime: "runsc", err: "explicit root overlay requires opted-in owned runsc acceptance"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, err := parseRealReviewRootOverlay(tt.value, tt.optedIn, tt.state, tt.runtime)
			if tt.err != "" {
				require.EqualError(t, err, tt.err, "invalid mode must fail before private inputs or resource creation")
			} else {
				require.NoError(t, err, "supported isolated mode should select its fixed registration")
			}
			require.Equal(t, tt.expected, actual, "invalid selections must never return a usable candidate mode")
		})
	}
}

func TestRealReviewRootOverlayLoadedRegistration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		overlay  realReviewRootOverlay
		args     []string
		change   func(*system.RuntimeWithStatus)
		expected bool
	}{
		{name: "default exact tuple", args: []string{"--host-uds=open"}, expected: true},
		{name: "none exact tuple", overlay: realReviewRootOverlayNone, args: []string{"--host-uds=open", "--overlay2=none"}, expected: true},
		{name: "loaded status is metadata", overlay: realReviewRootOverlayNone, args: []string{"--host-uds=open", "--overlay2=none"}, change: func(r *system.RuntimeWithStatus) { r.Status = map[string]string{"annotation": "available"} }, expected: true},
		{name: "default refuses candidate tuple", args: []string{"--host-uds=open", "--overlay2=none"}},
		{name: "candidate refuses default tuple", overlay: realReviewRootOverlayNone, args: []string{"--host-uds=open"}},
		{name: "candidate refuses reordered tuple", overlay: realReviewRootOverlayNone, args: []string{"--overlay2=none", "--host-uds=open"}},
		{name: "candidate refuses duplicate flag", overlay: realReviewRootOverlayNone, args: []string{"--host-uds=open", "--overlay2=none", "--overlay2=none"}},
		{name: "candidate refuses arbitrary flag", overlay: realReviewRootOverlayNone, args: []string{"--host-uds=open", "--overlay2=none", "--unsafe"}},
		{name: "unknown selector", overlay: realReviewRootOverlay("arbitrary"), args: []string{"--host-uds=open"}},
		{name: "different executable", args: []string{"--host-uds=open"}, change: func(r *system.RuntimeWithStatus) { r.Path = "/other/runsc" }},
		{name: "shim selection", args: []string{"--host-uds=open"}, change: func(r *system.RuntimeWithStatus) { r.Type = "io.containerd.runsc.v1" }},
		{name: "extra shim option", args: []string{"--host-uds=open"}, change: func(r *system.RuntimeWithStatus) { r.Options = map[string]any{"BinaryName": "/other/runsc"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			registration := system.RuntimeWithStatus{Runtime: system.Runtime{Path: "/usr/bin/runsc", Args: tt.args}}
			if tt.change != nil {
				tt.change(&registration)
			}
			require.Equal(t, tt.expected, realReviewLoadedRuntimeMatches(tt.overlay, registration), "running daemon registration must exactly match the selected legacy tuple")
		})
	}
}

func configureRealReviewOverlayFixture(t *testing.T, fixture realReviewFilesystemFixture, overlay realReviewRootOverlay) {
	t.Helper()
	config := map[string]any{"exec-root": fixture.state + "/exec", "data-root": fixture.state + "/mount/docker", "storage-driver": "overlay2", "hosts": []string{fixture.host}, "runtimes": map[string]any{"runsc": map[string]any{"path": "/usr/bin/runsc", "runtimeArgs": overlay.runscArgs()}}}
	encoded, err := json.Marshal(config)
	require.NoError(t, err, "fixture selected registration should encode")
	require.NoError(t, os.WriteFile(fixture.state+"/daemon.json", encoded, 0600), "fixture must bind selected mode to its owned daemon")
}

func TestRealReviewRootOverlayOwnedDaemonAndChild(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		requested    realReviewRootOverlay
		configured   realReviewRootOverlay
		lower, upper int
		size         string
		wantOwned    bool
		wantReason   string
	}{
		{name: "absent mode preserves default", lower: 1, upper: 2, wantOwned: true},
		{name: "candidate exact mode", requested: realReviewRootOverlayNone, configured: realReviewRootOverlayNone, lower: 1, upper: 0, wantOwned: true},
		{name: "candidate refuses default daemon", requested: realReviewRootOverlayNone, lower: 1, upper: 0, wantReason: "unbound_daemon_configuration"},
		{name: "default refuses candidate daemon", configured: realReviewRootOverlayNone, lower: 1, upper: 2, wantReason: "unbound_daemon_configuration"},
		{name: "unknown request refuses both boundaries", requested: realReviewRootOverlay("arbitrary"), lower: 1, upper: 2, wantReason: "invalid_root_overlay_selector"},
		{name: "candidate refuses root hint self", requested: realReviewRootOverlayNone, configured: realReviewRootOverlayNone, lower: 1, upper: 2, wantOwned: true, wantReason: "root_overlay_mismatch"},
		{name: "candidate refuses other lower", requested: realReviewRootOverlayNone, configured: realReviewRootOverlayNone, lower: 2, upper: 0, wantOwned: true, wantReason: "root_overlay_mismatch"},
		{name: "candidate refuses custom size", requested: realReviewRootOverlayNone, configured: realReviewRootOverlayNone, lower: 1, upper: 0, size: "64m", wantOwned: true, wantReason: "root_overlay_mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newRealReviewFilesystemFixture(t)
			configureRealReviewOverlayFixture(t, fixture, tt.configured)
			fixture.value["goferMountConfs"] = []any{map[string]any{"lower": tt.lower, "upper": tt.upper, "size": tt.size}}
			fixture.write(t)
			request := realReviewFilesystemProbeRequest{RuntimeState: fixture.state, DaemonHost: fixture.host, RuntimeVersion: realReviewFilesystemRelease, ContainerID: realReviewFilesystemFixtureID, PID: 1234, CallerUID: os.Geteuid(), RootOverlay: tt.requested}
			require.Equal(t, tt.wantOwned, realReviewFilesystemOwnedDaemon(request), "parent must validate the one selected daemon tuple before elevation")
			proof, err := readRealReviewRuntimeFilesystem(request.RuntimeState, request.DaemonHost, request.RuntimeVersion, request.ContainerID, request.PID, os.Geteuid(), os.Geteuid(), request.RootOverlay)
			if tt.wantReason != "" {
				require.EqualError(t, err, "runtime filesystem proof unavailable: "+tt.wantReason, "child must reject wrong configuration and effective modes with a static reason")
				require.False(t, proof.Verified, "rejected child reads must never prove the runtime")
				require.Nil(t, proof.Root, "rejected child reads must retain no raw or partial mount state")
			} else {
				require.NoError(t, err, "exact owned daemon and observed mode should prove selected runtime")
				expected := expectedRealReviewFilesystemProof(tt.lower, tt.upper, "default", 1)
				expected.RootOverlay = tt.requested
				require.Equal(t, expected, proof, "child must record exact selector and sanitized effective root")
			}
		})
	}
}

func TestRealReviewRootOverlayPrivilegedResponseBinding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		request  realReviewRootOverlay
		change   func(*realReviewRuntimeFilesystemProof)
		expected bool
	}{
		{name: "candidate bound response", request: realReviewRootOverlayNone, expected: true},
		{name: "candidate refuses absent selector", request: realReviewRootOverlayNone, change: func(p *realReviewRuntimeFilesystemProof) { p.RootOverlay = realReviewRootOverlayDefault }},
		{name: "default refuses candidate response"},
		{name: "candidate refuses unknown selector", request: realReviewRootOverlayNone, change: func(p *realReviewRuntimeFilesystemProof) { p.RootOverlay = "arbitrary" }},
		{name: "candidate refuses inconsistent effective mode", request: realReviewRootOverlayNone, change: func(p *realReviewRuntimeFilesystemProof) { p.Root.Upper, p.Root.UpperLabel = 2, "self" }},
		{name: "candidate refuses custom size", request: realReviewRootOverlayNone, change: func(p *realReviewRuntimeFilesystemProof) { p.Root.SizeClassification = "custom_size_present" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newRealReviewFilesystemFixture(t)
			request := realReviewFilesystemProbeRequest{RuntimeState: fixture.state, DaemonHost: fixture.host, RuntimeVersion: realReviewFilesystemRelease, ContainerID: realReviewFilesystemFixtureID, PID: 1234, CallerUID: os.Geteuid(), RootOverlay: tt.request}
			proof := expectedRealReviewFilesystemProof(1, 0, "default", 1)
			proof.RootOverlay = realReviewRootOverlayNone
			if tt.change != nil {
				tt.change(&proof)
			}
			encoded, err := json.Marshal(proof)
			require.NoError(t, err, "fixture response should encode")
			decoded, ok := realReviewFilesystemDecodeProbeOutput([]byte(realReviewFilesystemProbePrefix+string(encoded)+"\nPASS\n"), request)
			require.Equal(t, tt.expected, ok, "privileged response must be bound to requested selector and its effective mode")
			if tt.expected {
				require.Equal(t, proof, decoded, "valid response must retain exact sanitized proof")
			} else {
				require.Equal(t, realReviewRuntimeFilesystemProof{}, decoded, "rejected mode response must retain no parsed proof")
			}
		})
	}
}

func TestRealReviewRootOverlayEveryCreate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		overlay  realReviewRootOverlay
		profile  bool
		observed int
	}{
		{name: "ordinary default remains unobserved"},
		{name: "default profiling independently requires proof", profile: true, observed: 2},
		{name: "candidate normal and restore require proof without profiling", overlay: realReviewRootOverlayNone, observed: 2},
		{name: "candidate diagnostic and restore require proof", overlay: realReviewRootOverlayNone, profile: true, observed: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			observations, confirmations := 0, 0
			// Both lifecycle paths invoke the common Create verifier. Proof is never
			// reused across the cold allocation and the new restore allocation.
			for _, id := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64)} {
				proof, err := verifyRealReviewCreatedFilesystem(tt.overlay, tt.profile, func() (realReviewRuntimeFilesystemProof, error) {
					observations++
					upper := 2
					if tt.overlay == realReviewRootOverlayNone {
						upper = 0
					}
					p := expectedRealReviewFilesystemProof(1, upper, "default", 1)
					p.RootOverlay, p.ContainerID = tt.overlay, id
					return p, nil
				}, func(p *realReviewRuntimeFilesystemProof) error {
					confirmations++
					require.Equal(t, id, p.ContainerID, "post-read confirmation must use this newly created container")
					return nil
				})
				require.NoError(t, err, "correct runtime mode should permit private work only after required confirmation")
				if tt.observed == 0 {
					require.Nil(t, proof, "ordinary unset acceptance must preserve its existing observation path")
				} else {
					require.True(t, proof.Verified, "each candidate allocation must hold its own effective mode proof")
				}
			}
			require.Equal(t, tt.observed, observations, "required proof must run for both cold and restored allocations")
			require.Equal(t, tt.observed, confirmations, "each observed allocation must recheck its live identity and resources")
		})
	}
}

func TestRealReviewRootOverlayCreateStopsBeforePrivateWork(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		change       func(*realReviewRuntimeFilesystemProof)
		observeErr   error
		confirmErr   error
		confirmCalls int
	}{
		{name: "unavailable proof", observeErr: errors.New("private raw observer error")},
		{name: "unverified proof", change: func(p *realReviewRuntimeFilesystemProof) { p.Verified = false }},
		{name: "wrong mode", change: func(p *realReviewRuntimeFilesystemProof) { p.Root.Upper, p.Root.UpperLabel = 2, "self" }},
		{name: "missing proof", change: func(p *realReviewRuntimeFilesystemProof) { p.Root = nil }},
		{name: "wrong selector", change: func(p *realReviewRuntimeFilesystemProof) { p.RootOverlay = realReviewRootOverlayDefault }},
		{name: "identity changes after read", confirmErr: errors.New("sandbox identity changed"), confirmCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			confirmations := 0
			proof, err := verifyRealReviewCreatedFilesystem(realReviewRootOverlayNone, false, func() (realReviewRuntimeFilesystemProof, error) {
				p := expectedRealReviewFilesystemProof(1, 0, "default", 1)
				p.RootOverlay = realReviewRootOverlayNone
				if tt.change != nil {
					tt.change(&p)
				}
				return p, tt.observeErr
			}, func(p *realReviewRuntimeFilesystemProof) error {
				confirmations++
				p.Verified = false
				return tt.confirmErr
			})
			require.Error(t, err, "candidate failure must stop Create before private repository preparation")
			require.NotContains(t, err.Error(), "private raw", "observer errors must never expose untrusted host details")
			require.NotNil(t, proof, "sanitized proof must remain available for failed diagnostic evidence")
			require.Equal(t, tt.confirmCalls, confirmations, "invalid initial proof must stop before further host observation")
		})
	}
}

func realReviewOverlayCreationResource(id string) map[string]any {
	proof := expectedRealReviewFilesystemProof(1, 0, "default", 1)
	proof.ContainerID, proof.RootOverlay = id, realReviewRootOverlayNone
	return map[string]any{"container_id": id, "runtime": "runsc", "kernel_cgroup_proof": realReviewKernelResourceProof{ContainerID: id, SandboxPID: proof.SandboxPID, Verified: true}, "effective_runtime_filesystem": &proof}
}

func TestRealReviewRootOverlayResultContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		change   func(*[]map[string]any)
		created  int64
		expected bool
	}{
		{name: "cold and restore proved", created: 2, expected: true},
		{name: "no created evidence", change: func(p *[]map[string]any) { *p = nil }},
		{name: "missing restored record", created: 2, change: func(p *[]map[string]any) { *p = (*p)[:1] }},
		{name: "unverified restored mode", created: 2, change: func(p *[]map[string]any) {
			(*p)[1]["effective_runtime_filesystem"].(*realReviewRuntimeFilesystemProof).Verified = false
		}},
		{name: "wrong restored mode", created: 2, change: func(p *[]map[string]any) {
			(*p)[1]["effective_runtime_filesystem"].(*realReviewRuntimeFilesystemProof).Root.Upper = 2
		}},
		{name: "wrong restored selector", created: 2, change: func(p *[]map[string]any) {
			(*p)[1]["effective_runtime_filesystem"].(*realReviewRuntimeFilesystemProof).RootOverlay = realReviewRootOverlayDefault
		}},
		{name: "missing restored proof", created: 2, change: func(p *[]map[string]any) { delete((*p)[1], "effective_runtime_filesystem") }},
		{name: "duplicate creation", created: 2, change: func(p *[]map[string]any) { (*p)[1] = (*p)[0] }},
		{name: "kernel process mismatch", created: 2, change: func(p *[]map[string]any) {
			kernel := (*p)[1]["kernel_cgroup_proof"].(realReviewKernelResourceProof)
			kernel.SandboxPID++
			(*p)[1]["kernel_cgroup_proof"] = kernel
		}},
		{name: "unverified kernel", created: 2, change: func(p *[]map[string]any) {
			kernel := (*p)[1]["kernel_cgroup_proof"].(realReviewKernelResourceProof)
			kernel.Verified = false
			(*p)[1]["kernel_cgroup_proof"] = kernel
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resources := []map[string]any{realReviewOverlayCreationResource(strings.Repeat("a", 64)), realReviewOverlayCreationResource(strings.Repeat("b", 64))}
			if tt.change != nil {
				tt.change(&resources)
			}
			actual := realReviewAcceptedRuntimeContract(realReviewRootOverlayNone, false, tt.created, resources)
			expected := realReviewRuntimeContract{RootOverlaySelector: "none", RunscPath: "/usr/bin/runsc", RunscArgs: []string{"--host-uds=open", "--overlay2=none"}, EffectiveRootProofRequired: true, AllCreationsVerified: tt.expected}
			require.Equal(t, expected, actual, "normal result eligibility must require identical selected runtime and proof for every creation")
		})
	}
}

func TestRealReviewRootOverlayPrivilegedRequest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		selector    string
		appendField string
		callerDelta int
		expected    bool
	}{
		{name: "legacy absent selector", expected: true},
		{name: "explicit empty selector", selector: `,"root_overlay_selector":""`, expected: true},
		{name: "candidate selector", selector: `,"root_overlay_selector":"none"`, expected: true},
		{name: "unknown selector", selector: `,"root_overlay_selector":"self"`},
		{name: "null is not absence", selector: `,"root_overlay_selector":null`},
		{name: "boolean is not selector", selector: `,"root_overlay_selector":true`},
		{name: "array cannot supply arbitrary args", selector: `,"root_overlay_selector":["none"]`},
		{name: "arbitrary args rejected", appendField: `,"runtime_args":["--unsafe"]`},
		{name: "duplicate selector rejected", selector: `,"root_overlay_selector":"none","root_overlay_selector":""`},
		{name: "sudo caller mismatch", callerDelta: 1},
		{name: "request bound", appendField: `,"unknown":"` + strings.Repeat("x", 4097) + `"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newRealReviewFilesystemFixture(t)
			request := realReviewFilesystemProbeRequest{RuntimeState: fixture.state, DaemonHost: fixture.host, RuntimeVersion: realReviewFilesystemRelease, ContainerID: realReviewFilesystemFixtureID, PID: 1234, CallerUID: os.Geteuid()}
			encoded, err := json.Marshal(request)
			require.NoError(t, err, "fixture request should serialize without the absent selector")
			raw := string(encoded[:len(encoded)-1]) + tt.selector + tt.appendField + "}"
			actual, valid := realReviewFilesystemDecodeProbeRequest([]byte(raw), os.Geteuid()+tt.callerDelta)
			require.Equal(t, tt.expected, valid, "child must accept only a bounded unique enum request bound to the sudo caller")
			if tt.expected {
				if strings.Contains(tt.selector, `"none"`) {
					request.RootOverlay = realReviewRootOverlayNone
				}
				require.Equal(t, request, actual, "valid request must preserve exact owned inputs and the selected mode")
			} else {
				require.Equal(t, realReviewFilesystemProbeRequest{}, actual, "invalid request must retain no hostile input")
			}
		})
	}
}
