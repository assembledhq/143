package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLinearUserLinkSource_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		source    LinearUserLinkSource
		expectErr bool
	}{
		{name: "observed is valid", source: LinearUserLinkSourceObserved},
		{name: "email match is valid", source: LinearUserLinkSourceEmailMatch},
		{name: "self linked is valid", source: LinearUserLinkSourceSelfLinked},
		{name: "admin linked is valid", source: LinearUserLinkSourceAdminLinked},
		{name: "unknown is invalid", source: LinearUserLinkSource("manual"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.source.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown Linear user link sources")
				return
			}
			require.NoError(t, err, "Validate should accept known Linear user link sources")
		})
	}
}
