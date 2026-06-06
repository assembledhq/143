package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestThreadCreatedBySourceValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   ThreadCreatedBySource
		wantErr bool
	}{
		{name: "empty is valid for old rows", value: ""},
		{name: "user", value: ThreadCreatedBySourceUser},
		{name: "agent tool", value: ThreadCreatedBySourceAgentTool},
		{name: "system", value: ThreadCreatedBySourceSystem},
		{name: "invalid", value: ThreadCreatedBySource("bot"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.value.Validate()
			if tt.wantErr {
				require.Error(t, err, "Validate should reject unknown thread provenance")
				return
			}
			require.NoError(t, err, "Validate should accept known thread provenance")
		})
	}
}

func TestSessionMessageSourceValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   SessionMessageSource
		wantErr bool
	}{
		{name: "empty is valid for normal messages", value: ""},
		{name: "agent tool", value: SessionMessageSourceAgentTool},
		{name: "invalid", value: SessionMessageSource("bot"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.value.Validate()
			if tt.wantErr {
				require.Error(t, err, "Validate should reject unknown message source")
				return
			}
			require.NoError(t, err, "Validate should accept known message source")
		})
	}
}
