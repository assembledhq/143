package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestReviewStartupTiming(t *testing.T) {
	t.Parallel()
	type boundary struct {
		Stage   string `json:"stage"`
		Message string `json:"message"`
		Outcome string `json:"outcome"`
	}
	tests := []struct {
		name     string
		enabled  bool
		advance  bool
		cancel   bool
		err      error
		expected []boundary
	}{
		{name: "ordinary sessions do not log review setup", advance: true},
		{
			name: "successful transition closes each phase once", enabled: true, advance: true,
			expected: []boundary{
				{Stage: "review_turn_context", Message: "stage started"},
				{Stage: "review_turn_context", Message: "stage ended", Outcome: "succeeded"},
				{Stage: "review_workspace_liveness", Message: "stage started"},
				{Stage: "review_workspace_liveness", Message: "stage ended", Outcome: "succeeded"},
			},
		},
		{
			name: "early failure closes the unfinished phase", enabled: true, err: errors.New("synthetic failure"),
			expected: []boundary{
				{Stage: "review_turn_context", Message: "stage started"},
				{Stage: "review_turn_context", Message: "stage ended", Outcome: "failed"},
			},
		},
		{
			name: "cancellation stays distinguishable", enabled: true, cancel: true, err: context.Canceled,
			expected: []boundary{
				{Stage: "review_turn_context", Message: "stage started"},
				{Stage: "review_turn_context", Message: "stage ended", Outcome: "cancelled"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var output bytes.Buffer
			timing := beginReviewStartupTiming(tt.enabled, zerolog.New(&output))
			if tt.advance {
				timing.advance("review_workspace_liveness")
			}
			if tt.cancel {
				cancel()
			}
			timing.finish(ctx, tt.err)
			// Mirrors explicit completion followed by the continuation's defer.
			timing.finish(ctx, tt.err)
			var actual []boundary
			decoder := json.NewDecoder(&output)
			for decoder.More() {
				var entry boundary
				require.NoError(t, decoder.Decode(&entry), "startup boundaries should be structured JSON")
				actual = append(actual, entry)
			}
			require.Equal(t, tt.expected, actual, "only enabled startup phases should emit one matching end with the correct outcome")
		})
	}
}
