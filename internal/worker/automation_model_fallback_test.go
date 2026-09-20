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
		expected   []automationModelRankForTest
		expectErr  bool
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
			candidates, err := automationModelCandidates(run, tt.automation)
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

// TestAutomationModelCandidatesRemaining covers the defect this whole rework
// exists to fix. The chain used to advance by session count, but pre-flight
// availability can skip a rank without ever spawning a session, so the Nth
// session is not the Nth rank: with [opus, codex (no credential), sonnet], the
// run reached sonnet on its second session, and the third dispatch — indexing
// at count 2 — landed back on sonnet and re-ran the model that had just failed.
// Subtracting the (agent, model) pairs that actually ran is what makes that
// impossible: a model is dropped the moment it has a session, so the third
// dispatch can only reach a rank no session has touched.
func TestAutomationModelCandidatesRemaining(t *testing.T) {
	t.Parallel()

	opus := models.AutomationModelRank{
		AgentType: stringPtr(string(models.AgentTypeClaudeCode)),
		Model:     stringPtr(models.ClaudeCodeModelOpus5),
	}
	codex := models.AutomationModelRank{
		AgentType: stringPtr(string(models.AgentTypeCodex)),
		Model:     stringPtr(models.DefaultCodexModel),
		Fallback:  true,
	}
	sonnet := models.AutomationModelRank{
		AgentType: stringPtr(string(models.AgentTypeClaudeCode)),
		Model:     stringPtr(models.ClaudeCodeModelSonnet46),
		Fallback:  true,
	}
	// A rank that names only a model: its agent is resolved downstream by the
	// handler's ladder, so it is stored with no agent at all.
	agentlessOpus := models.AutomationModelRank{Model: stringPtr(models.ClaudeCodeModelOpus5), Fallback: true}

	attempt := func(agentType, model *string) models.AutomationRunAttempt {
		return models.AutomationRunAttempt{SessionID: uuid.New(), AgentType: agentType, Model: model}
	}

	tests := []struct {
		name       string
		candidates []models.AutomationModelRank
		attempts   []models.AutomationRunAttempt
		expected   []models.AutomationModelRank
	}{
		{
			// The first dispatch of a run has nothing to subtract, so the
			// chain must come through untouched down to the primary.
			name:       "returns the chain unchanged when the run has attempted nothing",
			candidates: []models.AutomationModelRank{opus, codex, sonnet},
			expected:   []models.AutomationModelRank{opus, codex, sonnet},
		},
		{
			// The regression itself: codex was skipped pre-flight for want of
			// a credential, so the run's two sessions covered ranks 0 and 2 and
			// the session count (2) lagged the chain position (3). Counting
			// said "resume at rank 2" and re-ran sonnet, the model that had
			// just failed. Subtracting what actually ran leaves only the rank
			// no session ever touched — and never sonnet again.
			name:       "never returns a rank a session already ran, even when a skipped rank made the count lag the chain",
			candidates: []models.AutomationModelRank{opus, codex, sonnet},
			attempts: []models.AutomationRunAttempt{
				// Newest first, the order ListSessionAttempts returns.
				attempt(sonnet.AgentType, sonnet.Model),
				attempt(opus.AgentType, opus.Model),
			},
			expected: []models.AutomationModelRank{codex},
		},
		{
			// Once every rank has a session, nothing remains and the caller
			// fails the run instead of wrapping around to the primary. This is
			// the state the count-based logic could never reach, because its
			// index had already passed the end of a chain it had not spent.
			name:       "returns nothing once every rank in the chain has a session",
			candidates: []models.AutomationModelRank{opus, codex, sonnet},
			attempts: []models.AutomationRunAttempt{
				attempt(sonnet.AgentType, sonnet.Model),
				attempt(codex.AgentType, codex.Model),
				attempt(opus.AgentType, opus.Model),
			},
			expected: []models.AutomationModelRank{},
		},
		{
			// nil and "" are the same value here: an agentless rank is spent
			// by the agentless session it spawned, while the rank that names
			// an agent for the same model is untouched, because the agent is
			// half the key.
			name:       "matches an agentless attempt to the agentless rank only",
			candidates: []models.AutomationModelRank{opus, agentlessOpus},
			attempts:   []models.AutomationRunAttempt{attempt(nil, stringPtr(models.ClaudeCodeModelOpus5))},
			expected:   []models.AutomationModelRank{opus},
		},
		{
			// Matching is by identity, not by index: an attempt on rank 1
			// spends rank 1 and leaves the primary available, and the stored
			// strings are compared trimmed so padding written by an older
			// caller cannot make a spent rank look fresh.
			name:       "spends the rank whose trimmed agent and model match, not the rank at that position",
			candidates: []models.AutomationModelRank{opus, codex, sonnet},
			attempts: []models.AutomationRunAttempt{
				attempt(stringPtr("  "+string(models.AgentTypeCodex)+" "), stringPtr(" "+models.DefaultCodexModel+"  ")),
			},
			expected: []models.AutomationModelRank{opus, sonnet},
		},
		{
			// A -> B -> A is a legal chain, but the run only ever gets one
			// session per model: once A has run, both copies of it are spent,
			// because a second session on A would repeat work already done.
			name:       "spends every copy of a model that appears twice in the chain",
			candidates: []models.AutomationModelRank{opus, codex, opus},
			attempts:   []models.AutomationRunAttempt{attempt(opus.AgentType, opus.Model)},
			expected:   []models.AutomationModelRank{codex},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			remaining := automationModelCandidatesRemaining(tt.candidates, tt.attempts)
			require.Equal(t, automationModelRanksForTest(tt.expected), automationModelRanksForTest(remaining),
				"only ranks this run has not already spent a session on may be dispatched again")
		})
	}
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
	// The second rank leaves its agent unset, which is the common shape for an
	// automation that only overrides the model.
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
			// A rank with no agent has nothing to look up yet: the handler's
			// ladder (org default, then the built-in default) resolves it
			// downstream. Skipping it would strand the most common automation
			// configuration with no candidate at all.
			name:           "selects a rank with an unset agent without a credential lookup",
			candidates:     chainWithUnsetAgent,
			unavailable:    []int{0},
			expectedIndex:  1,
			expectedOK:     true,
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

	// The run is claimed first, so the failure below has to be written from
	// running rather than pending.
	mock.ExpectExec(`UPDATE automation_runs SET status = @to_status.+WHERE id = @id AND org_id = @org_id AND status = @from_status`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// No sessions yet, so nothing is subtracted and the walk considers the
	// whole chain, primary first.
	expectAutomationRunSessionAttempts(mock)

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
