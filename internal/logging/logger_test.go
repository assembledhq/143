package logging

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestNewLogger(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		level         string
		expectedLevel zerolog.Level
	}{
		{
			name:          "uses parsed level when valid",
			level:         "debug",
			expectedLevel: zerolog.DebugLevel,
		},
		{
			name:          "falls back to info level when invalid",
			level:         "not-a-valid-level",
			expectedLevel: zerolog.InfoLevel,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			logger := NewLogger(tt.level)
			require.Equal(t, tt.expectedLevel, logger.GetLevel(), "NewLogger should set the expected log level")
		})
	}
}
