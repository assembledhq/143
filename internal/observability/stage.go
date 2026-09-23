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
	log.Info().Str("stage", name).Time("stage_started_at", started).Msg("stage started")
	return StageTimer{log: log, name: name, started: started, enabled: true}
}

// End records elapsed wall time, including external waits. outcome is a
// bounded value such as succeeded, failed, waiting, cancelled, attempted, or skipped;
// attempted means a best-effort operation reports no success/failure result.
// Do not put provider errors, prompts, or credentials into stage fields.
func (t StageTimer) End(outcome string) {
	if !t.enabled {
		return
	}
	t.log.Info().
		Str("stage", t.name).
		Str("outcome", outcome).
		Time("stage_started_at", t.started).
		Float64("duration_ms", DurationMillis(time.Since(t.started))).
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
