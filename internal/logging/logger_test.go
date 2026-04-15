package logging

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestNewLogger(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		level         string
		env           string
		expectedLevel zerolog.Level
	}{
		{
			name:          "uses parsed level when valid",
			level:         "debug",
			env:           "development",
			expectedLevel: zerolog.DebugLevel,
		},
		{
			name:          "falls back to info level when invalid",
			level:         "not-a-valid-level",
			env:           "development",
			expectedLevel: zerolog.InfoLevel,
		},
		{
			name:          "production env uses JSON output",
			level:         "info",
			env:           "production",
			expectedLevel: zerolog.InfoLevel,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			logger := NewLogger(tt.level, tt.env)
			require.Equal(t, tt.expectedLevel, logger.GetLevel(), "NewLogger should set the expected log level")
		})
	}
}

func TestNewLogger_ProductionOutputIsJSON(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := newLogger("info", "production", &buf)
	logger.Info().Msg("test message")

	require.True(t, json.Valid(buf.Bytes()), "production logger should output valid JSON, got: %s", buf.String())

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	require.Equal(t, "test message", entry["message"])
	require.Equal(t, "info", entry["level"])
	require.Contains(t, entry, "time", "JSON output should include a timestamp")
}

func TestNewLogger_DevelopmentOutputIsHumanReadable(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := newLogger("info", "development", &buf)
	logger.Info().Msg("hello world")

	// ConsoleWriter output is not valid JSON — it's human-readable text
	require.False(t, json.Valid(buf.Bytes()), "development logger should not output JSON")
	require.Contains(t, buf.String(), "hello world")
}
