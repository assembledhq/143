package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/assembledhq/143/internal/services/agent"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type unusedMemoryReinforcer struct{ calls int }

func (s *unusedMemoryReinforcer) GetContextMemories(context.Context, agent.MemoryContextRequest) (*agent.MemoryContextResult, error) {
	s.calls++
	return &agent.MemoryContextResult{MemoryIDs: []uuid.UUID{uuid.New()}}, nil
}
func (s *unusedMemoryReinforcer) ReinforceMemories(context.Context, uuid.UUID, []uuid.UUID) error {
	s.calls++
	return nil
}

func TestLegacyMemoryReinforcementSkipsUnprovenUsage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		payload          string
		contextOrg, fail bool
	}{
		{name: "approval payload explicitly skipped", payload: `{"org_id":"%s","repo":"example/repo"}`},
		{name: "legacy payload uses durable owner", payload: `{"repo":"example/repo"}`, contextOrg: true},
		{name: "malformed payload", payload: `{`, fail: true},
		{name: "missing owner", payload: `{"repo":"example/repo"}`, fail: true},
		{name: "missing repo", payload: `{"org_id":"%s"}`, fail: true},
		{name: "zero owner", payload: `{"org_id":"00000000-0000-0000-0000-000000000000","repo":"example/repo"}`, fail: true},
		{name: "mismatched owner", payload: `{"org_id":"00000000-0000-0000-0000-000000000001","repo":"example/repo"}`, contextOrg: true, fail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID := uuid.New()
			payload := tt.payload
			if tt.name == "approval payload explicitly skipped" || tt.name == "missing repo" {
				payload = fmt.Sprintf(payload, orgID)
			}
			ctx := context.Background()
			if tt.contextOrg {
				ctx = withJobOrgID(ctx, orgID)
			}
			var logs bytes.Buffer
			err := newObsoleteMemoryReinforcementHandler(zerolog.New(&logs))(ctx, "reinforce_memories", json.RawMessage(payload))
			if tt.fail {
				var fatal *FatalError
				require.ErrorAs(t, err, &fatal, "invalid legacy work should terminate without pretending to reinforce memories")
				require.Empty(t, logs.String(), "invalid work must not log a successful skip")
				return
			}
			require.NoError(t, err, "queued approval work should retire without inventing consumed memory identities")
			require.Contains(t, logs.String(), "exact consumed memory identities are unavailable", "compatibility handling must explain the intentional no-op")
		})
	}
}

func TestRegisterHandlersNeverReinforcesUnconsumedMemories(t *testing.T) {
	t.Parallel()
	memories := &unusedMemoryReinforcer{}
	w := &Worker{handlers: make(map[string]JobHandler)}
	RegisterHandlers(w, &Stores{}, &Services{Memory: memories}, DataRetentionConfig{}, zerolog.Nop())
	handler, ok := w.handlers["reinforce_memories"]
	require.True(t, ok, "legacy queued reinforcement requires explicit compatibility consumption")
	payload, err := json.Marshal(map[string]string{"org_id": uuid.NewString(), "repo": "example/repo"})
	require.NoError(t, err, "legacy payload should encode")
	require.NoError(t, handler(context.Background(), "reinforce_memories", payload), "legacy registration must deliberately skip unproven reinforcement")
	require.Equal(t, 0, memories.calls, "approval compatibility must never retrieve or reinforce currently active memories")
}
