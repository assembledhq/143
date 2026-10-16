package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type heartbeatPresenceCounter struct {
	ids []string
	err error
}

func (c *heartbeatPresenceCounter) CountLiveSandboxes(context.Context) (int, error) {
	return len(c.ids), c.err
}
func (c *heartbeatPresenceCounter) LiveSandboxContainerIDs(context.Context) ([]string, error) {
	return c.ids, c.err
}

type heartbeatPresenceReferenceStore struct {
	hostID string
	refs   []models.SandboxContainerReference
	called []string
}

func (s *heartbeatPresenceReferenceStore) ListHostSandboxReferences(_ context.Context, hostID string) ([]models.SandboxContainerReference, error) {
	s.called = append(s.called, hostID)
	if hostID != s.hostID {
		return []models.SandboxContainerReference{}, nil
	}
	return s.refs, nil
}

type heartbeatMissingInspector struct {
	missing map[string]bool
	checked []string
}

func (i *heartbeatMissingInspector) ConfirmSandboxContainerMissing(_ context.Context, id string) (bool, error) {
	i.checked = append(i.checked, id)
	return i.missing[id], nil
}

func sandboxPresenceHostCases() []struct{ name, input, canonical string } {
	return []struct{ name, input, canonical string }{
		{"canonical IPv4", "192.0.2.1", "192.0.2.1"},
		{"padded IPv4", " 192.0.2.1 \t", "192.0.2.1"},
		{"canonical IPv6", "2001:db8::1", "2001:db8::1"},
		{"expanded uppercase IPv6", "2001:0DB8:0000:0000:0000:0000:0000:0001", "2001:db8::1"},
		{"IPv4 mapped IPv6", "::ffff:192.0.2.1", "192.0.2.1"},
	}
}

func TestWorkerSandboxPresenceUsesCanonicalHostIdentity(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"worker", "all"} {
		for _, tt := range sandboxPresenceHostCases() {
			t.Run(mode+"/"+tt.name, func(t *testing.T) {
				t.Parallel()
				cfg := &config.Config{Mode: mode, WorkerPrivateIP: tt.input}
				id := strings.Repeat("a", 64)
				refs := []models.SandboxContainerReference{{ContainerID: id, OwnerNodeID: "old-worker"}}
				store := &heartbeatPresenceReferenceStore{hostID: tt.canonical, refs: refs}
				inspector := &heartbeatMissingInspector{missing: map[string]bool{id: true}}
				gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{Counter: &heartbeatPresenceCounter{ids: []string{}}, MaxActive: 2, Logger: zerolog.Nop()})
				installSandboxReferenceObserver(cfg, gate, inspector, store)
				metadata := buildWorkerMetadataProvider(nil, false, "", "", nil, gate, agent.StaticEgressRuntimeConfig{}, cfg.SandboxHostID())()
				require.Equal(t, []string{tt.canonical}, store.called, "startup observer must query the literal canonical physical host advertised by its heartbeat")
				require.Equal(t, tt.canonical, metadata["sandbox_host_id"], "heartbeat and observer must share one canonical host identity")
				require.Equal(t, refs, metadata["missing_sandbox_references"], "canonical startup wiring must preserve exact missing container and historical owner evidence")
				require.Equal(t, []string{id}, inspector.checked, "host-owned full container identity must receive one definitive absence probe")
				require.Equal(t, "", metadata["missing_sandbox_reference_error"], "valid equivalent host configurations must collect successful absence evidence")
			})
		}
	}
}

type heartbeatPresenceObserver struct {
	refs []models.SandboxContainerReference
	err  error
}

func (o *heartbeatPresenceObserver) ObserveMissingSandboxReferences(context.Context, []string) ([]models.SandboxContainerReference, error) {
	return o.refs, o.err
}

func TestWorkerHeartbeatClearsFailedSandboxPresenceEvidence(t *testing.T) {
	t.Parallel()
	id := strings.Repeat("a", 64)
	tests := []struct {
		name                                       string
		countFailure                               bool
		expectedCountError, expectedReferenceError string
	}{
		{name: "inspection error clears previous proof", expectedReferenceError: "inspection unavailable"},
		{name: "inventory error clears previous proof", countFailure: true, expectedCountError: "inventory unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			counter := &heartbeatPresenceCounter{ids: []string{}}
			observer := &heartbeatPresenceObserver{refs: []models.SandboxContainerReference{{ContainerID: id, OwnerNodeID: "old-worker"}}}
			gate := agent.NewSandboxCapacityGate(agent.SandboxCapacityGateConfig{Counter: counter, MaxActive: 2, Logger: zerolog.Nop()})
			gate.SetReferenceObserver(observer)
			provider := buildWorkerMetadataProvider(nil, false, "", "", nil, gate, agent.StaticEgressRuntimeConfig{}, "192.0.2.1")
			first := provider()
			firstRaw, err := json.Marshal(first)
			require.NoError(t, err, "serialize successful heartbeat evidence")
			var firstDecoded map[string]any
			require.NoError(t, json.Unmarshal(firstRaw, &firstDecoded), "decode proof in its actual wire shape")
			require.Equal(t, []any{map[string]any{"container_id": id, "owner_node_id": "old-worker"}}, firstDecoded["missing_sandbox_references"], "heartbeat must carry the exact owner-bound immutable ID proof")
			if tt.countFailure {
				counter.err = errors.New(tt.expectedCountError)
			} else {
				observer.err = errors.New(tt.expectedReferenceError)
			}
			failed := provider()
			raw, err := json.Marshal(failed)
			require.NoError(t, err, "serialize failed proof heartbeat")
			var decoded map[string]any
			require.NoError(t, json.Unmarshal(raw, &decoded), "decode explicit null evidence for metadata merging")
			value, present := decoded["missing_sandbox_references"]
			require.True(t, present, "failed collection must explicitly replace old merged evidence")
			require.Nil(t, value, "failed collection must publish no reusable absence proof")
			require.Equal(t, tt.expectedCountError, decoded["live_sandbox_count_error"], "count uncertainty must remain distinguishable from physical absence")
			require.Equal(t, tt.expectedReferenceError, decoded["missing_sandbox_reference_error"], "failed presence probes must keep the raw reference floor")
			counter.err = nil
			observer.err = nil
			recovered := provider()
			require.Equal(t, "", recovered["live_sandbox_count_error"], "successful collection must explicitly clear prior merged count error")
			require.Equal(t, "", recovered["missing_sandbox_reference_error"], "successful collection must explicitly clear prior merged observer error")
		})
	}
}
