package models

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPreviewInfrastructureOwnerValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		mutate   func(*PreviewInfrastructureOwner)
		expected bool
	}{
		{name: "complete session owner", expected: true},
		{name: "standalone nil session", mutate: func(o *PreviewInfrastructureOwner) { o.SessionID = uuid.Nil }, expected: true},
		{name: "missing org", mutate: func(o *PreviewInfrastructureOwner) { o.OrgID = uuid.Nil }},
		{name: "missing preview", mutate: func(o *PreviewInfrastructureOwner) { o.PreviewID = uuid.Nil }},
		{name: "missing handle", mutate: func(o *PreviewInfrastructureOwner) { o.Handle = "" }},
		{name: "padded handle", mutate: func(o *PreviewInfrastructureOwner) { o.Handle = " handle " }},
		{name: "missing worker generation", mutate: func(o *PreviewInfrastructureOwner) { o.WorkerNodeID = "" }},
		{name: "padded worker generation", mutate: func(o *PreviewInfrastructureOwner) { o.WorkerNodeID = " worker " }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			owner := PreviewInfrastructureOwner{OrgID: uuid.New(), PreviewID: uuid.New(), SessionID: uuid.New(), Handle: "handle", WorkerNodeID: "worker"}
			if tt.mutate != nil {
				tt.mutate(&owner)
			}
			require.Equal(t, tt.expected, owner.Valid(), "only complete durable identities may authorize infrastructure cleanup")
		})
	}
}
