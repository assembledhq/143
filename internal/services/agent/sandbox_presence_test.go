package agent_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/stretchr/testify/require"
)

type presenceReferenceStore struct {
	refs []models.SandboxContainerReference
	err  error
	host string
}

func (s *presenceReferenceStore) ListHostSandboxReferences(_ context.Context, host string) ([]models.SandboxContainerReference, error) {
	s.host = host
	return s.refs, s.err
}

type presenceInspector struct {
	missing map[string]bool
	err     error
	checked []string
}

func (p *presenceInspector) ConfirmSandboxContainerMissing(_ context.Context, id string) (bool, error) {
	p.checked = append(p.checked, id)
	return p.missing[id], p.err
}

func TestSandboxReferenceObserverOnlyReportsDefinitiveMissingIdentities(t *testing.T) {
	t.Parallel()
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	tests := []struct {
		name                 string
		refs                 []models.SandboxContainerReference
		live                 []string
		missing              map[string]bool
		storeErr, inspectErr error
		want                 []models.SandboxContainerReference
		checked              []string
		expectErr            bool
	}{
		{name: "absent container with old generation owner", refs: []models.SandboxContainerReference{{ContainerID: a, OwnerNodeID: "old-worker"}}, missing: map[string]bool{a: true}, want: []models.SandboxContainerReference{{ContainerID: a, OwnerNodeID: "old-worker"}}, checked: []string{a}},
		{name: "live physical inventory wins over contradictory probe", refs: []models.SandboxContainerReference{{ContainerID: a, OwnerNodeID: "worker"}}, live: []string{a}, missing: map[string]bool{a: true}, want: []models.SandboxContainerReference{}},
		{name: "stopped existing container is retained", refs: []models.SandboxContainerReference{{ContainerID: a, OwnerNodeID: "worker"}}, want: []models.SandboxContainerReference{}, checked: []string{a}},
		{name: "same missing immutable ID with multiple owners", refs: []models.SandboxContainerReference{{ContainerID: a, OwnerNodeID: "old-worker"}, {ContainerID: a, OwnerNodeID: "new-worker"}}, missing: map[string]bool{a: true}, want: []models.SandboxContainerReference{{ContainerID: a, OwnerNodeID: "old-worker"}, {ContainerID: a, OwnerNodeID: "new-worker"}}, checked: []string{a, a}},
		{name: "unrelated live container remains in physical sample", refs: []models.SandboxContainerReference{{ContainerID: a, OwnerNodeID: "old-worker"}}, live: []string{b}, missing: map[string]bool{a: true}, want: []models.SandboxContainerReference{{ContainerID: a, OwnerNodeID: "old-worker"}}, checked: []string{a}},
		{name: "reference store failure returns no proof", storeErr: errors.New("database unavailable"), expectErr: true},
		{name: "inspection failure returns no partial proof", refs: []models.SandboxContainerReference{{ContainerID: a, OwnerNodeID: "worker"}}, inspectErr: errors.New("daemon unavailable"), expectErr: true, checked: []string{a}},
		{name: "reusable name cannot be proven missing", refs: []models.SandboxContainerReference{{ContainerID: "sandbox", OwnerNodeID: "worker"}}, expectErr: true},
		{name: "incomplete physical inventory fails closed", live: []string{"short-id"}, expectErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := &presenceReferenceStore{refs: tt.refs, err: tt.storeErr}
			inspector := &presenceInspector{missing: tt.missing, err: tt.inspectErr}
			observer := agent.NewSandboxReferenceObserver(inspector, store, "192.0.2.1")
			got, err := observer.ObserveMissingSandboxReferences(context.Background(), tt.live)
			require.Equal(t, tt.expectErr, err != nil, "unknown physical presence must return an error rather than evidence of absence")
			require.Equal(t, tt.want, got, "physical absence observations must preserve exact container and owner pairs")
			require.Equal(t, tt.checked, inspector.checked, "only references absent from the same live inventory may be inspected")
			if len(tt.live) == 0 || models.IsFullDockerContainerID(tt.live[0]) {
				require.Equal(t, "192.0.2.1", store.host, "reference observation must be scoped to the actual physical host")
			}
		})
	}
}
