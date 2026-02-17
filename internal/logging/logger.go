package logging

import (
	"os"
	"time"

	"github.com/rs/zerolog"
)

// NewLogger creates a zerolog logger with the given level.
func NewLogger(level string) zerolog.Logger {
	lvl, err := zerolog.ParseLevel(level)
	if err != nil {
		lvl = zerolog.InfoLevel
	}

	output := zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}
	return zerolog.New(output).Level(lvl).With().Timestamp().Logger()
}
