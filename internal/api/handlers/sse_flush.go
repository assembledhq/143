package handlers

import (
	"github.com/assembledhq/143/internal/api/sse"
	"github.com/rs/zerolog"
)

func flushSSE(sw *sse.Writer, logger *zerolog.Event) {
	if err := sw.Flush(); err != nil {
		logger.Err(err).Msg("failed to flush SSE stream")
	}
}
