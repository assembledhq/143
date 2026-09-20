package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// automationModelAvailabilityStub records every credential lookup in order so
// the tests can prove which ranks were reached, not just which one was chosen.
// A rank the walk never gets to must cost no lookup at all: availability checks
// hit the credential store, so a chain of five models that always dispatches
// its primary should still only ever pay for one.
type automationModelAvailabilityStub struct {
	unavailable map[string]bool
	checked     []string
}

func (s *automationModelAvailabilityStub) IsAgentAvailable(_ context.Context, _ uuid.UUID, _ *uuid.UUID, agent models.AgentType, model string) (bool, error) {
	key := automationModelAvailabilityKey(string(agent), model)
	s.checked = append(s.checked, key)
	return !s.unavailable[key], nil
}

func automationModelAvailabilityKey(agentType, model string) string {
	return fmt.Sprintf("%s:%s", agentType, model)
}

// automationModelRankForTest is a value-comparable projection of a rank. The
// production type holds pointers, and comparing pointer graphs in a table makes
// failures unreadable while asserting nothing extra: what matters is which
// agent, model and effort a rank resolved to and whether it is a fallback.
type automationModelRankForTest struct {
	AgentType       string
	Model           string
	ReasoningEffort models.ReasoningEffort
	Fallback        bool
}

func automationModelRanksForTest(ranks []models.AutomationModelRank) []automationModelRankForTest {
	out := make([]automationModelRankForTest, 0, len(ranks))
	for _, rank := range ranks {
		projected := automationModelRankForTest{
			AgentType: stringPtrValue(rank.AgentType),
			Model:     stringPtrValue(rank.Model),
			Fallback:  rank.Fallback,
		}
		if rank.ReasoningEffort != nil {
			projected.ReasoningEffort = *rank.ReasoningEffort
		}
		out = append(out, projected)
	}
	return out
}

// automationForModelChainTest builds an automation whose primary is rank 0 and
// whose fallbacks carry explicit agents, so the expectations below exercise the
// candidate walk rather than AgentTypeForModel's name inference.
func automationForModelChainTest(agentType, model string, fallbackAgents, fallbackModels []string) models.Automation {
	automation := models.Automation{
		ID:            uuid.New(),
		AgentType:     stringPtr(agentType),
		ModelOverride: stringPtr(model),
	}
	if len(fallbackModels) > 0 {
		automation.FallbackModels = models.AutomationFallbackModels{
			AgentTypes: fallbackAgents,
			Models:     fallbackModels,
		}
	}
	return automation
}

// automationForEffortChainTest builds a chain whose single fallback re-runs the
// primary's own model at a different reasoning level. That is a real strategy —
// the same model at a cheaper level can succeed where the expensive one hit a
// capacity ceiling — so the collapse must not treat the two ranks as identical.
func automationForEffortChainTest(agentType, model string, primaryEffort, fallbackEffort models.ReasoningEffort) models.Automation {
	effort := primaryEffort
	return models.Automation{
		ID:              uuid.New(),
		AgentType:       stringPtr(agentType),
		ModelOverride:   stringPtr(model),
		ReasoningEffort: &effort,
		FallbackModels: models.AutomationFallbackModels{
			AgentTypes:       []string{agentType},
			Models:           []string{model},
			ReasoningEfforts: []models.ReasoningEffort{fallbackEffort},
		},
	}
}

func automationConfigSnapshotForTest(t *testing.T, automation models.Automation) json.RawMessage {
	t.Helper()
	snapshot, err := automation.BuildConfigSnapshot()
	require.NoError(t, err, "building a run's config snapshot must succeed or the dispatch path has nothing to pin the chain to")
	return snapshot
}

func TestAutomationModelCandidates(t *testing.T) {
	t.Parallel()

	// The automation as it looked when the run was created. Every "edited
	// later" case below mutates a copy of this and asserts the run still runs
	// the chain it was snapshotted with.
	snapshotted := automationForModelChainTest(
		string(models.AgentTypeCodex), models.DefaultCodexModel,
		[]string{string(models.AgentTypeClaudeCode)}, []string{models.DefaultClaudeCodeModel},
	)

	tests := []struct {
		name       string
		snapshot   json.RawMessage
		automation models.Automation
		// defaultAgentType is the org default the handler resolves once per
		// dispatch. The zero value exercises the bottom of the ladder, where
		// models.DefaultDefaultAgentType is the last resort.
		defaultAgentType models.AgentType
		expected         []automationModelRankForTest
		expectErr        bool
	}{
		{
			// The snapshot is the contract the run was dispatched under, so it
			// wins even when the live row currently says something else.
			name:     "prefers the run config snapshot over the live automation row",
			snapshot: automationConfigSnapshotForTest(t, snapshotted),
			automation: automationForModelChainTest(
				string(models.AgentTypeOpenCode), models.OpenCodeModelGPT55, nil, nil,
			),
			expected: []automationModelRankForTest{
				{AgentType: string(models.AgentTypeCodex), Model: models.DefaultCodexModel},
				{AgentType: string(models.AgentTypeClaudeCode), Model: models.DefaultClaudeCodeModel, Fallback: true},
			},
		},
		{
			// Runs created before fallback models shipped have no
			// fallback_models key at all. Reading the live row is the only way
			// those runs get a chain; treating the missing key as an empty
			// chain would silently strip a primary the user has configured.
			name:     "falls back to the live automation row when the snapshot predates the feature",
			snapshot: json.RawMessage(`{"agent_type":"opencode","model_override":"stale-snapshot-model"}`),
			automation: automationForModelChainTest(
				string(models.AgentTypeCodex), models.DefaultCodexModel,
				[]string{string(models.AgentTypeClaudeCode)}, []string{models.DefaultClaudeCodeModel},
			),
			expected: []automationModelRankForTest{
				{AgentType: string(models.AgentTypeCodex), Model: models.DefaultCodexModel},
				{AgentType: string(models.AgentTypeClaudeCode), Model: models.DefaultClaudeCodeModel, Fallback: true},
			},
		},
		{
			// An in-flight run must not be re-ranked underneath itself: an edit
			// that appends a rank would otherwise shift the attempt index, so
			// a re-dispatch after a capacity failure could re-run the model
			// that just failed or skip one entirely.
			name:     "an automation edited after the run was created cannot re-rank it",
			snapshot: automationConfigSnapshotForTest(t, snapshotted),
			automation: automationForModelChainTest(
				string(models.AgentTypeOpenCode), models.OpenCodeModelGPT55,
				[]string{string(models.AgentTypeCodex), string(models.AgentTypeClaudeCode)},
				[]string{models.DefaultCodexModel, models.DefaultClaudeCodeModel},
			),
			expected: []automationModelRankForTest{
				{AgentType: string(models.AgentTypeCodex), Model: models.DefaultCodexModel},
				{AgentType: string(models.AgentTypeClaudeCode), Model: models.DefaultClaudeCodeModel, Fallback: true},
			},
		},
		{
			// Re-running the model that just failed burns an attempt for
			// nothing, so a fallback identical to the rank before it collapses.
			name: "collapses a fallback that duplicates the rank immediately before it",
			automation: automationForModelChainTest(
				string(models.AgentTypeCodex), models.DefaultCodexModel,
				[]string{string(models.AgentTypeCodex)}, []string{models.DefaultCodexModel},
			),
			expected: []automationModelRankForTest{
				{AgentType: string(models.AgentTypeCodex), Model: models.DefaultCodexModel},
			},
		},
		{
			// Identity includes reasoning effort. The same model at a cheaper
			// level is a different attempt with a different chance of
			// succeeding, so collapsing on (agent, model) alone would silently
			// delete the fallback the user deliberately configured.
			name: "keeps an adjacent rank that re-tries the same model at a different reasoning level",
			automation: automationForEffortChainTest(
				string(models.AgentTypeCodex), models.DefaultCodexModel,
				models.ReasoningEffortXHigh, models.ReasoningEffortLow,
			),
			expected: []automationModelRankForTest{
				{AgentType: string(models.AgentTypeCodex), Model: models.DefaultCodexModel, ReasoningEffort: models.ReasoningEffortXHigh},
				{AgentType: string(models.AgentTypeCodex), Model: models.DefaultCodexModel, ReasoningEffort: models.ReasoningEffortLow, Fallback: true},
			},
		},
		{
			// A -> B -> A is a deliberate retry strategy: coming back to the
			// first model after trying another one is not a duplicate, because
			// the intervening attempt gave it time to recover capacity.
			name: "keeps a non-adjacent repeat of an earlier rank",
			automation: automationForModelChainTest(
				string(models.AgentTypeCodex), models.DefaultCodexModel,
				[]string{string(models.AgentTypeClaudeCode), string(models.AgentTypeCodex)},
				[]string{models.DefaultClaudeCodeModel, models.DefaultCodexModel},
			),
			expected: []automationModelRankForTest{
				{AgentType: string(models.AgentTypeCodex), Model: models.DefaultCodexModel},
				{AgentType: string(models.AgentTypeClaudeCode), Model: models.DefaultClaudeCodeModel, Fallback: true},
				{AgentType: string(models.AgentTypeCodex), Model: models.DefaultCodexModel, Fallback: true},
			},
		},
		{
			// Resolution has to happen BEFORE the collapse, not after. These
			// two ranks end up naming the same agent and the same model; the
			// only difference is that the primary inherits its agent while the
			// fallback spells it out. Comparing the raw pointers first sees
			// "" against "codex", keeps both, and spends a whole extra attempt
			// re-running the model that just failed.
			name: "collapses a rank that inherits its agent into the adjacent rank that names it",
			automation: models.Automation{
				ID:            uuid.New(),
				ModelOverride: stringPtr(models.DefaultCodexModel),
				FallbackModels: models.AutomationFallbackModels{
					AgentTypes: []string{string(models.AgentTypeCodex)},
					Models:     []string{models.DefaultCodexModel},
				},
			},
			defaultAgentType: models.AgentTypeCodex,
			expected: []automationModelRankForTest{
				{AgentType: string(models.AgentTypeCodex), Model: models.DefaultCodexModel},
			},
		},
		{
			// An automation that overrides only the model is the common shape,
			// and its primary reaches here with no agent at all. The org
			// default the handler resolved must be stamped onto it, because
			// everything downstream — the availability lookup, the session
			// row, the attempt ledger — deals in concrete agents.
			name: "stamps the org default onto a primary that names only a model",
			automation: models.Automation{
				ID:            uuid.New(),
				ModelOverride: stringPtr(models.DefaultCodexModel),
			},
			defaultAgentType: models.AgentTypeOpenCode,
			expected: []automationModelRankForTest{
				{AgentType: string(models.AgentTypeOpenCode), Model: models.DefaultCodexModel},
			},
		},
		{
			// A snapshot we cannot parse must surface as an error rather than
			// quietly degrading to the live row: the caller fails the run so a
			// human sees the corruption instead of a run that silently ignored
			// the chain it was created with.
			name:       "surfaces a malformed config snapshot as an error",
			snapshot:   json.RawMessage(`{"fallback_models":`),
			automation: automationForModelChainTest(string(models.AgentTypeCodex), models.DefaultCodexModel, nil, nil),
			expectErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			run := models.AutomationRun{ID: uuid.New(), ConfigSnapshot: tt.snapshot}
			candidates, err := automationModelCandidates(run, tt.automation, tt.defaultAgentType)
			if tt.expectErr {
				require.Error(t, err, "an unparseable snapshot must not be mistaken for a run that simply has no chain")
				return
			}
			require.NoError(t, err, "a well-formed chain should resolve without error")
			require.Equal(t, tt.expected, automationModelRanksForTest(candidates),
				"the dispatch chain must come from the snapshot the run was created with, minus adjacent repeats")
		})
	}
}

// TestAutomationRankAgentType pins the ladder every rank is resolved through
// before anything compares, checks or dispatches it. A rank that leaves this
// function without a concrete agent is the infinite-re-dispatch bug: the
// session records the agent it actually ran, so a rank still carrying nil can
// never match its own attempt and the run re-dispatches that model until the
// reaper kills it.
func TestAutomationRankAgentType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		rank             models.AutomationModelRank
		defaultAgentType models.AgentType
		expected         models.AgentType
	}{
		{
			// A rank that names its own agent is the whole point of a
			// heterogeneous chain, so it outranks anything the org says.
			name:             "a rank's own agent wins over the org default",
			rank:             models.AutomationModelRank{AgentType: stringPtr(string(models.AgentTypeClaudeCode)), Model: stringPtr(models.DefaultClaudeCodeModel)},
			defaultAgentType: models.AgentTypeCodex,
			expected:         models.AgentTypeClaudeCode,
		},
		{
			// An agent that is not on the roster — a typo, or one retired
			// since the automation was saved — would fail every availability
			// lookup, so it falls through rather than stranding the rank.
			name:             "an explicit agent that is not a real agent falls through to the org default",
			rank:             models.AutomationModelRank{AgentType: stringPtr("retired_agent"), Model: stringPtr(models.DefaultCodexModel)},
			defaultAgentType: models.AgentTypeClaudeCode,
			expected:         models.AgentTypeClaudeCode,
		},
		{
			// Whitespace is not an agent. An entry padded by an older writer
			// must take the default instead of being looked up as " ".
			name:             "a whitespace-only agent takes the org default",
			rank:             models.AutomationModelRank{AgentType: stringPtr("   "), Model: stringPtr(models.DefaultCodexModel)},
			defaultAgentType: models.AgentTypeOpenCode,
			expected:         models.AgentTypeOpenCode,
		},
		{
			// The common automation shape: only a model is overridden, and the
			// org's configured default supplies the agent.
			name:             "a rank with no agent takes the org default",
			rank:             models.AutomationModelRank{Model: stringPtr(models.DefaultClaudeCodeModel)},
			defaultAgentType: models.AgentTypeClaudeCode,
			expected:         models.AgentTypeClaudeCode,
		},
		{
			// Orgs that never set a default still have to dispatch, so the
			// bottom of the ladder is the built-in default rather than "".
			name:     "an empty org default falls to the built-in default",
			rank:     models.AutomationModelRank{Model: stringPtr(models.DefaultCodexModel)},
			expected: models.DefaultDefaultAgentType,
		},
		{
			// Org settings are user-writable, so an unparseable default must
			// not leak into a rank and fail the availability lookup there.
			name:             "an invalid org default falls to the built-in default",
			rank:             models.AutomationModelRank{Model: stringPtr(models.DefaultCodexModel)},
			defaultAgentType: models.AgentType("retired_agent"),
			expected:         models.DefaultDefaultAgentType,
		},
		{
			// Both rungs above are unusable, so even a rank naming nothing at
			// all comes out dispatchable.
			name:             "a rank with neither agent nor a usable default still resolves",
			rank:             models.AutomationModelRank{},
			defaultAgentType: models.AgentType(" "),
			expected:         models.DefaultDefaultAgentType,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resolved := automationRankAgentType(tt.rank, tt.defaultAgentType)
			require.Equal(t, tt.expected, resolved,
				"the rank agent ladder decides which credential is checked and which agent the session records")
			require.NoError(t, resolved.Validate(),
				"every rank must leave the ladder on a real agent; an unusable one fails availability and never matches its own attempt")
		})
	}
}

// TestAutomationModelCandidates_ResolvesEveryRankAgent is the regression guard
// for the infinite re-dispatch loop. A rank used to keep whatever agent the
// automation stored — nil for an automation that overrides only the model —
// while the session it spawned recorded the agent the ladder resolved
// downstream. The frozen rank and its own attempt therefore never matched, so
// models.AutomationModelRanksRemaining handed the same rank back on every
// dispatch and the run re-ran one model until the stuck-run reaper failed it.
func TestAutomationModelCandidates_ResolvesEveryRankAgent(t *testing.T) {
	t.Parallel()

	// No agent_type anywhere: neither the primary nor the fallback names one,
	// which is exactly the shape that used to come back nil.
	automation := models.Automation{
		ID:            uuid.New(),
		ModelOverride: stringPtr(models.DefaultCodexModel),
		FallbackModels: models.AutomationFallbackModels{
			Models: []string{models.DefaultClaudeCodeModel},
		},
	}
	run := models.AutomationRun{ID: uuid.New()}

	candidates, err := automationModelCandidates(run, automation, models.AgentTypeOpenCode)
	require.NoError(t, err, "an automation that overrides only its model still has a chain to dispatch")
	require.Len(t, candidates, 2, "the primary and its one fallback are distinct models and must both survive the collapse")

	for idx, candidate := range candidates {
		require.NotNil(t, candidate.AgentType,
			"rank %d must carry a concrete agent: a nil agent could never match its own recorded attempt, so the run would re-dispatch this model forever", idx)
		require.NoError(t, models.AgentType(*candidate.AgentType).Validate(),
			"rank %d must resolve to a real agent, since availability is looked up per (agent, model)", idx)
	}

	require.Equal(t, string(models.AgentTypeOpenCode), stringPtrValue(candidates[0].AgentType),
		"rank 0 of an automation with no agent_type must carry the org default, not nil — the session it dispatches records that same agent")

	// The proof that the ledger now closes the loop: the attempt a dispatch of
	// rank 0 would record spends rank 0, leaving only the fallback. Before the
	// fix this returned the full chain again, forever.
	attempt := models.AutomationRunAttempt{
		SessionID: uuid.New(),
		AgentType: candidates[0].AgentType,
		Model:     candidates[0].Model,
	}
	remaining := models.AutomationModelRanksRemaining(candidates, []models.AutomationRunAttempt{attempt})
	require.Equal(t, automationModelRanksForTest(candidates[1:]), automationModelRanksForTest(remaining),
		"the attempt a resolved rank produces must spend that rank, or the chain never advances past its primary")
}

func TestResolveAutomationModelSelection(t *testing.T) {
	t.Parallel()

	// A heterogeneous chain: ranks may name a different agent than the primary,
	// so availability has to be checked per (agent, model) pair rather than once
	// for the automation.
	rankedChain := []models.AutomationModelRank{
		{AgentType: stringPtr(string(models.AgentTypeCodex)), Model: stringPtr(models.DefaultCodexModel)},
		{AgentType: stringPtr(string(models.AgentTypeClaudeCode)), Model: stringPtr(models.DefaultClaudeCodeModel), Fallback: true},
		{AgentType: stringPtr(string(models.AgentTypeOpenCode)), Model: stringPtr(models.OpenCodeModelGPT55), Fallback: true},
	}
	// The shape automationModelCandidates actually produces for an automation
	// that only overrides the model: the second rank named no agent, and the
	// ladder stamped the org default onto it before this walk ever sees it.
	// Nothing here is special-cased for it — it is checked like any other rank.
	chainWithInheritedAgent := []models.AutomationModelRank{
		rankedChain[0],
		{AgentType: stringPtr(string(models.AgentTypeClaudeCode)), Model: stringPtr(models.DefaultClaudeCodeModel), Fallback: true},
	}
	// A rank that reached the walk with its agent still blank. This cannot
	// happen through automationModelCandidates any more, so it means something
	// upstream failed to resolve — and there is no credential to look up for
	// "", so dispatching it would be an unchecked dispatch.
	chainWithUnsetAgent := []models.AutomationModelRank{
		rankedChain[0],
		{Model: stringPtr(models.DefaultClaudeCodeModel), Fallback: true},
	}

	tests := []struct {
		name        string
		candidates  []models.AutomationModelRank
		unavailable []int
		nilService  bool
		// expectedIndex is a position in candidates, which is the list the
		// caller already trimmed to what is still unspent.
		expectedIndex int
		expectedOK    bool
		// expectedChecks are indexes into candidates, in the exact order the
		// credential lookups must happen.
		expectedChecks []int
	}{
		{
			// The whole point of lazy checking: a configured but unreached
			// chain costs exactly one lookup.
			name:           "dispatches the primary when it is available",
			candidates:     rankedChain,
			expectedIndex:  0,
			expectedOK:     true,
			expectedChecks: []int{0},
		},
		{
			// Pre-flight failover: dispatching into a model with no usable
			// credential only buys a failed session.
			name:           "skips an unavailable primary and dispatches rank 1",
			candidates:     rankedChain,
			unavailable:    []int{0},
			expectedIndex:  1,
			expectedOK:     true,
			expectedChecks: []int{0, 1},
		},
		{
			// A re-dispatch hands over only what is left of the chain, and the
			// walk starts at the front of that list. Ranks already spent are
			// not in it at all, so they can never cost a second lookup — they
			// failed for their own reason, not for availability.
			name:           "walks from the front of the remaining chain it was handed",
			candidates:     rankedChain[2:],
			expectedIndex:  0,
			expectedOK:     true,
			expectedChecks: []int{0},
		},
		{
			// Everything still to try is unusable, so the caller must fail the
			// run rather than dispatch into a model it knows will not run.
			name:           "reports no selection when every remaining rank is unavailable",
			candidates:     rankedChain[1:],
			unavailable:    []int{0, 1},
			expectedOK:     false,
			expectedChecks: []int{0, 1},
		},
		{
			// A run that has already spent every rank is handed an empty list.
			// It must report exhaustion rather than wrapping around to the
			// model it started with.
			name:       "reports no selection when the remaining chain is empty",
			candidates: nil,
			expectedOK: false,
		},
		{
			// Workers assembled without the availability service must still
			// dispatch; failing every automation run for want of an optional
			// dependency would be far worse than dispatching optimistically.
			name:          "assumes availability when no coding-agent service is wired",
			candidates:    rankedChain[1:],
			nilService:    true,
			expectedIndex: 0,
			expectedOK:    true,
		},
		{
			// A rank that inherited its agent is not privileged. It used to be
			// returned unchecked, which meant the most common automation shape
			// dispatched into a credential nobody had verified; now the lookup
			// happens against the agent the ladder resolved, which is also the
			// agent the session will record.
			name:           "checks a rank that inherited its agent, using the resolved agent",
			candidates:     chainWithInheritedAgent,
			unavailable:    []int{0},
			expectedIndex:  1,
			expectedOK:     true,
			expectedChecks: []int{0, 1},
		},
		{
			// A blank agent has no credential to look up, so "assume it works"
			// was really "dispatch unchecked". Treat it as unusable instead and
			// keep walking; a later rank may still be runnable.
			name:           "skips a rank whose agent is still blank rather than dispatching it unchecked",
			candidates:     chainWithUnsetAgent,
			unavailable:    []int{0},
			expectedOK:     false,
			expectedChecks: []int{0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			key := func(index int) string {
				candidate := tt.candidates[index]
				return automationModelAvailabilityKey(stringPtrValue(candidate.AgentType), stringPtrValue(candidate.Model))
			}
			availability := &automationModelAvailabilityStub{unavailable: make(map[string]bool)}
			for _, index := range tt.unavailable {
				availability.unavailable[key(index)] = true
			}
			var expectedChecks []string
			for _, index := range tt.expectedChecks {
				expectedChecks = append(expectedChecks, key(index))
			}

			services := &Services{CodingAgents: availability}
			if tt.nilService {
				services = nil
			}

			rank, index, ok, err := resolveAutomationModelSelection(
				context.Background(), services, uuid.New(), nil, tt.candidates,
			)
			require.NoError(t, err, "a stub that answers every lookup should never fail the walk")
			require.Equal(t, tt.expectedOK, ok, "the chain is usable only while a rank remains that can actually run")
			if tt.expectedOK {
				require.Equal(t, tt.expectedIndex, index,
					"the returned index must name the rank actually chosen within the remaining chain")
				require.Equal(t, tt.candidates[tt.expectedIndex], rank,
					"the dispatched rank carries the agent, model and effort the session is created with")
			}
			if tt.nilService {
				return
			}
			require.Equal(t, expectedChecks, availability.checked,
				"only the ranks the walk actually reaches may cost a credential lookup")
		})
	}
}

func TestAutomationModelCandidateLabels(t *testing.T) {
	t.Parallel()

	candidates := []models.AutomationModelRank{
		{AgentType: stringPtr(string(models.AgentTypeCodex)), Model: stringPtr(models.DefaultCodexModel)},
		{AgentType: stringPtr(string(models.AgentTypeClaudeCode)), Model: stringPtr(models.DefaultClaudeCodeModel), Fallback: true},
		// No model: the agent name is the only thing meaningful to show.
		{AgentType: stringPtr(string(models.AgentTypeOpenCode)), Fallback: true},
		// Neither agent nor model: resolved downstream by the handler's ladder.
		{Fallback: true},
	}

	tests := []struct {
		name       string
		candidates []models.AutomationModelRank
		expected   string
	}{
		{
			name:       "names every rank when the run has not attempted anything yet",
			candidates: candidates,
			expected:   fmt.Sprintf("%s, %s, %s, default model", models.DefaultCodexModel, models.DefaultClaudeCodeModel, models.AgentTypeOpenCode),
		},
		{
			// The summary explains why this dispatch found nothing to run.
			// Ranks already attempted are not in the remaining chain, and
			// naming them as unavailable would tell the user something untrue.
			name:       "names only the ranks left in the remaining chain",
			candidates: candidates[2:],
			expected:   fmt.Sprintf("%s, default model", models.AgentTypeOpenCode),
		},
		{
			// The caller prints a different sentence entirely for this case,
			// which it detects by the empty string.
			name:       "is empty once the chain is fully spent",
			candidates: nil,
			expected:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, automationModelCandidateLabels(tt.candidates),
				"the exhausted-chain summary must name exactly the ranks this dispatch considered")
		})
	}
}

// automationRunSummaryCaptureArg records the result_summary a transition wrote
// so the test can assert on its content without pinning the exact sentence.
type automationRunSummaryCaptureArg struct {
	captured *string
}

func (a automationRunSummaryCaptureArg) Match(v interface{}) bool {
	got, ok := v.(*string)
	if !ok || got == nil {
		return false
	}
	*a.captured = *got
	return true
}

// TestAutomationRunHandler_FailsRunWhenNoModelRankIsAvailable exercises the
// whole dispatch path with a chain that has no usable credential anywhere.
// The run must end terminally — a row left running can never be reclaimed,
// because the pending -> running CAS that guards dispatch can no longer
// succeed — and the summary must name every model that was tried, since the
// user's only fix is to authenticate one of them.
func TestAutomationRunHandler_FailsRunWhenNoModelRankIsAvailable(t *testing.T) {
	t.Parallel()

	stores, mock := newTestStores(t)
	defer mock.Close()
	stores.Automations = db.NewAutomationStore(mock)
	stores.AutomationRuns = db.NewAutomationRunStore(mock)

	orgID := uuid.New()
	automationID := uuid.New()
	runID := uuid.New()
	now := time.Now()
	agentType := string(models.AgentTypeCodex)
	repoID := uuid.New()
	fallbacks := models.AutomationFallbackModels{
		AgentTypes: []string{string(models.AgentTypeClaudeCode)},
		Models:     []string{models.DefaultClaudeCodeModel},
	}

	payload, err := json.Marshal(map[string]string{
		"org_id":            orgID.String(),
		"automation_id":     automationID.String(),
		"automation_run_id": runID.String(),
	})
	require.NoError(t, err, "marshal payload should succeed")

	// The run carries an empty config snapshot, which predates fallback models,
	// so the chain comes from the live automation row below.
	mock.ExpectQuery(`SELECT .+ FROM automation_runs\s+WHERE id = @id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(automationRunRowColumns()).AddRow(
			runID, automationID, orgID, now, models.AutomationTriggeredBySchedule,
			nil, nil, nil, nil, nil, []byte("{}"), "goal", []byte("{}"),
			models.AutomationRunStatusPending, nil, nil, nil, now, now,
		))
	mock.ExpectQuery(`SELECT .+ FROM automations WHERE id = @id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(automationRowColumns()).AddRow(
			automationID, orgID, &repoID, "nightly", "cleanup", nil,
			models.AutomationIconTypeEmoji, "⚙️",
			&agentType, stringPtr(models.DefaultCodexModel), nil, fallbacks, "sequential", 1, "main", models.AutomationIdentityScopeOrg, models.AutomationPublishPolicyPullRequest, 0,
			models.AutomationScheduleInterval, nil, nil, nil, nil, "UTC",
			[]string{}, []byte("{}"),
			nil, nil, true, nil, nil, nil,
			50, []byte("{}"), now, now, nil,
		))

	// No sessions yet, so nothing is subtracted and the walk considers the
	// whole chain, primary first.
	expectAutomationRunSessionAttempts(mock)

	// The run is claimed first, so the failure below has to be written from
	// running rather than pending.
	mock.ExpectExec(`UPDATE automation_runs SET status = @to_status.+WHERE id = @id AND org_id = @org_id AND status = @from_status`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// TransitionStatusIf's named arguments expand in order of first appearance
	// in the SQL: to_status, completed_at, result_summary, id, org_id,
	// from_status.
	var summary string
	mock.ExpectExec(`UPDATE automation_runs SET status = @to_status.+WHERE id = @id AND org_id = @org_id AND status = @from_status`).
		WithArgs(
			models.AutomationRunStatusFailed,
			pgxmock.AnyArg(),
			automationRunSummaryCaptureArg{captured: &summary},
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			models.AutomationRunStatusRunning,
		).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	availability := &automationModelAvailabilityStub{unavailable: map[string]bool{
		automationModelAvailabilityKey(string(models.AgentTypeCodex), models.DefaultCodexModel):           true,
		automationModelAvailabilityKey(string(models.AgentTypeClaudeCode), models.DefaultClaudeCodeModel): true,
	}}

	handler := newAutomationRunHandler(stores, &Services{CodingAgents: availability}, zerolog.Nop())
	err = handler(context.Background(), models.JobTypeAutomationRun, payload)

	// Returning an error would requeue a job that can only reach the same
	// verdict, against a row the CAS no longer lets it claim.
	require.NoError(t, err, "an exhausted model chain is a terminal outcome for the run, not a retryable job failure")
	require.Contains(t, summary, models.DefaultCodexModel,
		"the failure summary must name the primary model so the user knows which credential to fix")
	require.Contains(t, summary, models.DefaultClaudeCodeModel,
		"the failure summary must name every fallback tried, not just the primary")
	require.Equal(t,
		[]string{
			automationModelAvailabilityKey(string(models.AgentTypeCodex), models.DefaultCodexModel),
			automationModelAvailabilityKey(string(models.AgentTypeClaudeCode), models.DefaultClaudeCodeModel),
		},
		availability.checked,
		"the handler must try every rank before giving up on the run")
	require.NoError(t, mock.ExpectationsWereMet(),
		"no session may be created and no run_agent job enqueued when the chain is exhausted")
}
