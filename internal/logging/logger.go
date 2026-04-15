package logging

import (
	"io"
	"os"
	"time"

	"github.com/rs/zerolog"
)

// NewLogger creates a zerolog logger with the given level.
// In production (env == "production"), it outputs structured JSON to stdout
// for consumption by log collectors like Vector. In all other environments,
// it uses a human-readable console format.
func NewLogger(level, env string) zerolog.Logger {
	return newLogger(level, env, os.Stdout)
}

func newLogger(level, env string, w io.Writer) zerolog.Logger {
	lvl, err := zerolog.ParseLevel(level)
	if err != nil {
		lvl = zerolog.InfoLevel
	}

	var output io.Writer
	if env == "production" {
		output = w
	} else {
		output = zerolog.ConsoleWriter{Out: w, TimeFormat: time.RFC3339}
	}
	return zerolog.New(output).Level(lvl).With().Timestamp().Logger()
}
