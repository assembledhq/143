package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsHexSHA(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		byteLen int
		want    bool
	}{
		{"valid 20-byte sha", "aabbccddee1122334455aabbccddee1122334455", 20, true},
		{"too short", "aabb", 20, false},
		{"too long", "aabbccddee1122334455aabbccddee11223344556677", 20, false},
		{"invalid hex chars", "zzzzzzzzzz1122334455aabbccddee1122334455", 20, false},
		{"wrong length for 20-byte check", "aabbccddee1122334455aabbccddee1122334455aabbccddee1122334455667788", 20, false},
		{"empty", "", 20, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, isHexSHA(tt.input, tt.byteLen))
		})
	}
}

func TestInputManifest_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		im      InputManifest
		wantErr bool
	}{
		{"valid dev SHA", InputManifest{ServerDeploySHA: "dev"}, false},
		{"valid 40-char hex SHA", InputManifest{ServerDeploySHA: "aabbccddee1122334455aabbccddee1122334455"}, false},
		{"empty SHA", InputManifest{ServerDeploySHA: ""}, true},
		{"invalid SHA format", InputManifest{ServerDeploySHA: "not-a-sha"}, true},
		{"short SHA", InputManifest{ServerDeploySHA: "aabb"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.im.Validate()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
