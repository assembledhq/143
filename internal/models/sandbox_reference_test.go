package models

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSandboxContainerReferenceValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, id, owner string
		want            bool
	}{
		{name: "complete immutable Docker identity", id: strings.Repeat("a", 64), owner: "old-worker", want: true},
		{name: "reusable container name", id: "sandbox", owner: "worker"},
		{name: "ambiguous short ID", id: strings.Repeat("a", 12), owner: "worker"},
		{name: "noncanonical uppercase ID", id: strings.Repeat("A", 64), owner: "worker"},
		{name: "nonhex ID", id: strings.Repeat("x", 64), owner: "worker"},
		{name: "missing owner", id: strings.Repeat("a", 64)},
		{name: "trimmed owner could change identity", id: strings.Repeat("a", 64), owner: " worker"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := (SandboxContainerReference{ContainerID: tt.id, OwnerNodeID: tt.owner}).Validate()
			require.Equal(t, tt.want, err == nil, "only canonical complete IDs with an exact owner may be used as presence proof")
		})
	}
}
