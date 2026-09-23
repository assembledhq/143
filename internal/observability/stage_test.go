package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestStageTimerEmitsPairedBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		enabled bool
		outcome string
		want    int
	}{
		{name: "enabled", enabled: true, outcome: "succeeded", want: 2},
		{name: "disabled", enabled: false, outcome: "failed", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			logger := zerolog.New(&output).With().Str("session_id", "session-1").Logger()
			stage := BeginStage(tt.enabled, logger, "workspace_hydration")
			stage.End(tt.outcome)
			lines := strings.Split(strings.TrimSpace(output.String()), "\n")
			if !tt.enabled {
				require.Empty(t, output.String(), "disabled stage should emit no logs")
				return
			}
			require.Len(t, lines, tt.want, "enabled stage should emit start and end events")
			var start, end map[string]any
			require.NoError(t, json.Unmarshal([]byte(lines[0]), &start), "start event should be valid JSON")
			require.NoError(t, json.Unmarshal([]byte(lines[1]), &end), "end event should be valid JSON")
			require.Equal(t, "workspace_hydration", start["stage"], "start should name the stage")
			require.Equal(t, start["stage_started_at"], end["stage_started_at"], "boundaries should share an exact start timestamp")
			require.Equal(t, "session-1", end["session_id"], "stage events should retain caller correlation fields")
			require.Equal(t, tt.outcome, end["outcome"], "end should record the supplied outcome")
			require.GreaterOrEqual(t, end["duration_ms"].(float64), float64(0), "elapsed duration should be nonnegative")
		})
	}
}

func TestStageOutcome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		ctx     context.Context
		err     error
		outcome string
	}{
		{name: "success", ctx: context.Background(), outcome: "succeeded"},
		{name: "failure", ctx: context.Background(), err: errors.New("failed"), outcome: "failed"},
		{name: "cancelled", ctx: cancelledStageContext(), err: context.Canceled, outcome: "cancelled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.outcome, StageOutcome(tt.ctx, tt.err), "stage outcome should reflect success, failure, or cancellation")
		})
	}
}

func cancelledStageContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
