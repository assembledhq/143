package models

import (
	"encoding/json"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRetainedCallerStartIdentityIsPrivate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value func(uuid.UUID) any
	}{
		{"publication", func(id uuid.UUID) any { return SessionPublication{ReviewStartID: &id} }},
		{"automation", func(id uuid.UUID) any { return AutomationRun{ReviewStartID: &id} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			id := uuid.New()
			raw, err := json.Marshal(tt.value(id))
			require.NoError(t, err, "serialize a populated caller through its real public model")
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &fields), "inspect actual public fields")
			_, exposed := fields["review_start_id"]
			require.False(t, exposed, "private retained correlation must not become an API or export field")
			require.NotContains(t, string(raw), id.String(), "private identity must not leak under another serialized name")
		})
	}
}
