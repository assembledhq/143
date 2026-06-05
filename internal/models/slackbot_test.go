package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionAttributionSourceValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		source    SessionAttributionSource
		expectErr bool
	}{
		{name: "slack is valid", source: SessionAttributionSourceSlack},
		{name: "unknown is invalid", source: SessionAttributionSource("email"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.source.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown attribution sources")
				return
			}
			require.NoError(t, err, "Validate should accept known attribution sources")
		})
	}
}
