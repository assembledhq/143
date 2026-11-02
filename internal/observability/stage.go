package observability

import (
	"context"
	"errors"
	"time"

	"github.com/rs/zerolog"
)

// StageTimer emits paired, structured boundaries for a single attempt at a
// stage. The caller supplies an already-scoped logger so identifiers remain in
// logs rather than becoming high-cardinality metric labels.
type StageTimer struct {
	log     zerolog.Logger
	name    string
	started time.Time
	enabled bool
}

func BeginStage(enabled bool, log zerolog.Logger, name string) StageTimer {
	if !enabled {
		return StageTimer{}
	}
	started := time.Now().UTC()
	// Keep boundaries precise independently of the logger's global timestamp
	// format so adjacent stages can be correlated without rounding overlap.
	log.Info().Str("stage", name).Str("stage_started_at", started.Format(time.RFC3339Nano)).Msg("stage started")
	return StageTimer{log: log, name: name, started: started, enabled: true}
}

// End records elapsed wall time, including external waits. outcome is a
// bounded value such as succeeded, failed, incomplete, waiting, cancelled, attempted, or skipped;
// attempted means a best-effort operation reports no success/failure result.
// Do not put provider errors, prompts, or credentials into stage fields.
func (t StageTimer) End(outcome string) {
	if !t.enabled {
		return
	}
	ended := time.Now().UTC()
	t.log.Info().
		Str("stage", t.name).
		Str("outcome", outcome).
		Str("stage_started_at", t.started.Format(time.RFC3339Nano)).
		Str("stage_ended_at", ended.Format(time.RFC3339Nano)).
		Float64("duration_ms", DurationMillis(ended.Sub(t.started))).
		Msg("stage ended")
}

func StageOutcome(ctx context.Context, err error) string {
	if err == nil {
		return "succeeded"
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return "cancelled"
	}
	return "failed"
}
