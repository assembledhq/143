package agent

import (
	"context"

	"github.com/assembledhq/143/internal/observability"
	"github.com/rs/zerolog"
)

// reviewStartupTiming is local to one continuation. Keeping the active timer
// here ensures early returns close the current phase exactly once, without
// changing the continuation's errors or extending its deadline.
type reviewStartupTiming struct {
	log     zerolog.Logger
	stage   observability.StageTimer
	enabled bool
	active  bool
}

func beginReviewStartupTiming(enabled bool, log zerolog.Logger) *reviewStartupTiming {
	timing := &reviewStartupTiming{log: log, enabled: enabled}
	timing.advance("review_turn_context")
	return timing
}

func (t *reviewStartupTiming) advance(name string) {
	if !t.enabled {
		return
	}
	if t.active {
		t.stage.End("succeeded")
	}
	t.stage = observability.BeginStage(true, t.log, name)
	t.active = true
}

func (t *reviewStartupTiming) finish(ctx context.Context, err error) {
	if !t.active {
		return
	}
	t.stage.End(observability.StageOutcome(ctx, err))
	t.active = false
}
