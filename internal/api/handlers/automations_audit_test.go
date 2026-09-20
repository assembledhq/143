package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

func TestAutomationAuditSnapshot_Interval(t *testing.T) {
	t.Parallel()

	v, u := 3, models.ScheduleUnitDays
	runAt := "09:35"
	a := &models.Automation{
		Name:          "Refresh caches",
		IdentityScope: models.AutomationIdentityScopePersonal,
		ScheduleType:  models.AutomationScheduleInterval,
		IntervalValue: &v,
		IntervalUnit:  &u,
		IntervalRunAt: &runAt,
		Timezone:      "UTC",
	}
	snap := automationAuditSnapshot(a)
	require.Equal(t, "Refresh caches", snap["name"])
	require.Equal(t, models.AutomationIdentityScopePersonal, snap["identity_scope"])
	require.Equal(t, models.AutomationScheduleInterval, snap["schedule_type"])
	require.Equal(t, 3, snap["interval_value"])
	require.Equal(t, "days", snap["interval_unit"])
	require.Equal(t, "09:35", snap["interval_run_at"])
	_, hasCron := snap["cron_expression"]
	require.False(t, hasCron, "interval snapshot must not include cron fields")
}

func TestAutomationAuditSnapshot_Cron(t *testing.T) {
	t.Parallel()

	expr := "0 9 * * 1"
	a := &models.Automation{
		Name:           "Monday briefing",
		IdentityScope:  models.AutomationIdentityScopeOrg,
		ScheduleType:   models.AutomationScheduleCron,
		CronExpression: &expr,
		Timezone:       "America/Los_Angeles",
	}
	snap := automationAuditSnapshot(a)
	require.Equal(t, models.AutomationIdentityScopeOrg, snap["identity_scope"])
	require.Equal(t, "0 9 * * 1", snap["cron_expression"])
	require.Equal(t, "America/Los_Angeles", snap["timezone"])
	_, hasInterval := snap["interval_value"]
	require.False(t, hasInterval, "cron snapshot must not include interval fields")
}

func TestAutomationAuditDiff_OnlyChangedFields(t *testing.T) {
	t.Parallel()

	oldV, newV := 1, 7
	oldU, newU := models.ScheduleUnitDays, models.ScheduleUnitDays
	old := models.Automation{
		Name: "a", Goal: "g", ExecutionMode: "sequential", MaxConcurrent: 1,
		BaseBranch: "main", IdentityScope: models.AutomationIdentityScopeOrg, ScheduleType: models.AutomationScheduleInterval,
		IntervalValue: &oldV, IntervalUnit: &oldU, Timezone: "UTC", Priority: 50,
	}
	new_ := old
	new_.Name = "b"
	new_.IdentityScope = models.AutomationIdentityScopePersonal
	new_.IntervalValue = &newV
	new_.IntervalUnit = &newU // unchanged
	new_.Priority = 75

	changes := automationAuditDiff(&old, &new_)
	require.Len(t, changes, 4, "only name, identity_scope, interval_value, and priority should change")
	require.Contains(t, changes, "name")
	require.Contains(t, changes, "identity_scope")
	require.Contains(t, changes, "interval_value")
	require.Contains(t, changes, "priority")

	nameChange := changes["name"].(map[string]any)
	require.Equal(t, "a", nameChange["before"])
	require.Equal(t, "b", nameChange["after"])

	scopeChange := changes["identity_scope"].(map[string]any)
	require.Equal(t, models.AutomationIdentityScopeOrg, scopeChange["before"])
	require.Equal(t, models.AutomationIdentityScopePersonal, scopeChange["after"])

	intervalChange := changes["interval_value"].(map[string]any)
	require.Equal(t, 1, intervalChange["before"])
	require.Equal(t, 7, intervalChange["after"])
}

func TestAutomationAuditDiff_NoChanges(t *testing.T) {
	t.Parallel()

	a := models.Automation{
		Name: "a", Goal: "g", ExecutionMode: "sequential", MaxConcurrent: 1,
		BaseBranch: "main", ScheduleType: models.AutomationScheduleInterval,
		Timezone: "UTC", Priority: 50,
	}
	changes := automationAuditDiff(&a, &a)
	require.Empty(t, changes, "identical automations must yield an empty diff")
}

// TestAutomationAuditDiff_OptionalFieldsTriState pins the nil-vs-empty
// distinction: clearing an optional string (nil → "" or vice versa) must
// show up as a change so the audit timeline doesn't silently collapse the
// transition. Earlier, derefString-based diff treated nil and "" identically.
func TestAutomationAuditDiff_OptionalFieldsTriState(t *testing.T) {
	t.Parallel()

	t.Run("nil to empty string counts as change", func(t *testing.T) {
		t.Parallel()
		empty := ""
		old := models.Automation{Scope: nil}
		new_ := models.Automation{Scope: &empty}
		changes := automationAuditDiff(&old, &new_)
		require.Contains(t, changes, "scope")
		scope := changes["scope"].(map[string]any)
		require.Nil(t, scope["before"])
		require.Equal(t, "", scope["after"])
	})

	t.Run("set to nil counts as change", func(t *testing.T) {
		t.Parallel()
		expr := "0 9 * * 1"
		old := models.Automation{CronExpression: &expr}
		new_ := models.Automation{CronExpression: nil}
		changes := automationAuditDiff(&old, &new_)
		require.Contains(t, changes, "cron_expression")
		c := changes["cron_expression"].(map[string]any)
		require.Equal(t, "0 9 * * 1", c["before"])
		require.Nil(t, c["after"])
	})

	t.Run("nil interval to zero counts as change", func(t *testing.T) {
		t.Parallel()
		zero := 0
		old := models.Automation{IntervalValue: nil}
		new_ := models.Automation{IntervalValue: &zero}
		changes := automationAuditDiff(&old, &new_)
		require.Contains(t, changes, "interval_value")
		c := changes["interval_value"].(map[string]any)
		require.Nil(t, c["before"])
		require.Equal(t, 0, c["after"])
	})

	t.Run("both nil does not surface a change", func(t *testing.T) {
		t.Parallel()
		old := models.Automation{Scope: nil}
		new_ := models.Automation{Scope: nil}
		changes := automationAuditDiff(&old, &new_)
		require.NotContains(t, changes, "scope")
	})
}

func TestAutomationAuditDiff_RepositoryIDTransitions(t *testing.T) {
	t.Parallel()

	repoA := uuid.New()
	repoB := uuid.New()

	t.Run("nil to set", func(t *testing.T) {
		t.Parallel()
		old := models.Automation{RepositoryID: nil}
		new_ := models.Automation{RepositoryID: &repoA}
		changes := automationAuditDiff(&old, &new_)
		require.Contains(t, changes, "repository_id")
	})

	t.Run("set to different", func(t *testing.T) {
		t.Parallel()
		old := models.Automation{RepositoryID: &repoA}
		new_ := models.Automation{RepositoryID: &repoB}
		changes := automationAuditDiff(&old, &new_)
		require.Contains(t, changes, "repository_id")
	})

	t.Run("same value", func(t *testing.T) {
		t.Parallel()
		old := models.Automation{RepositoryID: &repoA}
		new_ := models.Automation{RepositoryID: &repoA}
		changes := automationAuditDiff(&old, &new_)
		require.NotContains(t, changes, "repository_id")
	})
}

func TestAutomationProductTriggerSummary(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		events []models.AutomationGitHubEvent
		want   []string
	}{
		{
			name:   "nil events returns nil",
			events: nil,
			want:   nil,
		},
		{
			name:   "empty events returns nil",
			events: []models.AutomationGitHubEvent{},
			want:   nil,
		},
		{
			name:   "pr.opened maps to github.pr.opened",
			events: []models.AutomationGitHubEvent{models.AutomationGitHubEventPullRequestOpened},
			want:   []string{string(models.AutomationProductTriggerPROpened)},
		},
		{
			name: "all three feedback events emit one github.pr.feedback label",
			events: []models.AutomationGitHubEvent{
				models.AutomationGitHubEventIssueCommentCreated,
				models.AutomationGitHubEventPullRequestReviewSubmitted,
				models.AutomationGitHubEventPullRequestReviewCommentCreated,
			},
			want: []string{string(models.AutomationProductTriggerPRFeedback)},
		},
		{
			name:   "single feedback event still emits one github.pr.feedback label",
			events: []models.AutomationGitHubEvent{models.AutomationGitHubEventPullRequestReviewSubmitted},
			want:   []string{string(models.AutomationProductTriggerPRFeedback)},
		},
		{
			name:   "check_suite.completed maps to github.checks.completed",
			events: []models.AutomationGitHubEvent{models.AutomationGitHubEventCheckSuiteCompleted},
			want:   []string{string(models.AutomationProductTriggerChecksCompleted)},
		},
		{
			name:   "legacy check_run.completed also maps to github.checks.completed",
			events: []models.AutomationGitHubEvent{models.AutomationGitHubEventCheckRunCompleted},
			want:   []string{string(models.AutomationProductTriggerChecksCompleted)},
		},
		{
			name: "check_suite and check_run together emit one github.checks.completed label",
			events: []models.AutomationGitHubEvent{
				models.AutomationGitHubEventCheckSuiteCompleted,
				models.AutomationGitHubEventCheckRunCompleted,
			},
			want: []string{string(models.AutomationProductTriggerChecksCompleted)},
		},
		{
			name: "full set of product triggers maps in order",
			events: []models.AutomationGitHubEvent{
				models.AutomationGitHubEventPullRequestOpened,
				models.AutomationGitHubEventPullRequestUpdated,
				models.AutomationGitHubEventPullRequestReadyForReview,
				models.AutomationGitHubEventIssueCommentCreated,
				models.AutomationGitHubEventPullRequestReviewSubmitted,
				models.AutomationGitHubEventPullRequestReviewCommentCreated,
				models.AutomationGitHubEventCheckSuiteCompleted,
				models.AutomationGitHubEventPullRequestMerged,
			},
			want: []string{
				string(models.AutomationProductTriggerPROpened),
				string(models.AutomationProductTriggerPRUpdated),
				string(models.AutomationProductTriggerPRReadyForReview),
				string(models.AutomationProductTriggerPRFeedback),
				string(models.AutomationProductTriggerChecksCompleted),
				string(models.AutomationProductTriggerPRMerged),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := automationProductTriggerSummary(tc.events)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestMarshalAuditDetails(t *testing.T) {
	t.Parallel()

	logger := zerolog.Nop()

	t.Run("empty map returns nil", func(t *testing.T) {
		t.Parallel()
		require.Nil(t, marshalAuditDetails(logger, map[string]any{}))
		require.Nil(t, marshalAuditDetails(logger, nil))
	})

	t.Run("valid payload round-trips", func(t *testing.T) {
		t.Parallel()
		got := marshalAuditDetails(logger, map[string]any{"name": "x", "count": 3})
		require.NotNil(t, got)
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(got, &decoded))
		require.Equal(t, "x", decoded["name"])
		require.Equal(t, float64(3), decoded["count"])
	})

	t.Run("unmarshalable payload returns nil and logs", func(t *testing.T) {
		t.Parallel()
		// Channels are not JSON-marshalable, so this exercises the error
		// branch. We verify both that the return is nil (so the audit row
		// stores SQL NULL rather than corrupt bytes) and that a log entry
		// was written so silent data loss is observable.
		var buf bytes.Buffer
		bufLogger := zerolog.New(&buf)
		details := map[string]any{"unencodable": make(chan int)}
		require.Nil(t, marshalAuditDetails(bufLogger, details))
		require.Contains(t, buf.String(), "marshal audit details")
	})
}

// TestAutomationAuditDiff_FallbackModels pins what the timeline reports for the
// ranked chain. The diff renders each side through automationFallbackModelsSummary
// first: track compares with reflect.DeepEqual, which calls nil and an empty
// slice different, so comparing the structs directly would file an audit row
// every time a client re-sent the chain it already had.
func TestAutomationAuditDiff_FallbackModels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		old        models.AutomationFallbackModels
		new_       models.AutomationFallbackModels
		wantChange bool
	}{
		{
			name:       "configuring a chain is a change",
			old:        models.AutomationFallbackModels{},
			new_:       models.AutomationFallbackModels{Models: []string{models.CodexModelGPT55}},
			wantChange: true,
		},
		{
			name:       "clearing a chain is a change",
			old:        models.AutomationFallbackModels{Models: []string{models.CodexModelGPT55}},
			new_:       models.AutomationFallbackModels{},
			wantChange: true,
		},
		{
			// Rank order is the failover order, so a reorder is a real edit
			// even though the set of models is identical.
			name:       "reordering the ranks is a change",
			old:        models.AutomationFallbackModels{Models: []string{models.CodexModelGPT55, models.ClaudeCodeModelSonnet46}},
			new_:       models.AutomationFallbackModels{Models: []string{models.ClaudeCodeModelSonnet46, models.CodexModelGPT55}},
			wantChange: true,
		},
		{
			name: "adding a per-rank override is a change",
			old:  models.AutomationFallbackModels{Models: []string{models.CodexModelGPT55}},
			new_: models.AutomationFallbackModels{
				Models:           []string{models.CodexModelGPT55},
				ReasoningEfforts: []models.ReasoningEffort{models.ReasoningEffortHigh},
			},
			wantChange: true,
		},
		{
			name:       "re-sending the same chain is not a change",
			old:        models.AutomationFallbackModels{Models: []string{models.CodexModelGPT55}},
			new_:       models.AutomationFallbackModels{Models: []string{models.CodexModelGPT55}},
			wantChange: false,
		},
		{
			// What a form-driven client actually posts back: padded whitespace
			// and parallel arrays it never filled in.
			name: "a redundantly encoded copy of the same chain is not a change",
			old:  models.AutomationFallbackModels{Models: []string{models.CodexModelGPT55}},
			new_: models.AutomationFallbackModels{
				AgentTypes:       []string{""},
				Models:           []string{"  " + models.CodexModelGPT55 + "  "},
				ReasoningEfforts: []models.ReasoningEffort{""},
			},
			wantChange: false,
		},
		{
			name:       "two automations without a chain are not a change",
			old:        models.AutomationFallbackModels{},
			new_:       models.AutomationFallbackModels{AgentTypes: []string{}, Models: []string{}},
			wantChange: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			old := models.Automation{Name: "a", FallbackModels: tt.old}
			new_ := models.Automation{Name: "a", FallbackModels: tt.new_}
			changes := automationAuditDiff(&old, &new_)
			if tt.wantChange {
				require.Contains(t, changes, "fallback_models", "a chain edit must be visible in the audit timeline")
				return
			}
			require.NotContains(t, changes, "fallback_models", "an unchanged chain must not file an audit row")
		})
	}
}

// capturedArg records the value pgxmock matched at its position. The audit
// details payload is argument 8 of 13 in the audit_logs INSERT, so this is what
// lets a test assert on what was recorded rather than merely that something was.
type capturedArg struct{ value *any }

func (c capturedArg) Match(v any) bool {
	*c.value = v
	return true
}

// TestAutomationHandler_Update_FallbackModelsAudit covers the two ends of audit
// behavior for the chain through the real handler: a fallback-only edit is
// recorded, and a client that re-sends the chain it already has records nothing.
func TestAutomationHandler_Update_FallbackModelsAudit(t *testing.T) {
	t.Parallel()

	storedAutomation := func(id, orgID uuid.UUID, chain models.AutomationFallbackModels) models.Automation {
		now := time.Now()
		iv := 1
		unit := models.ScheduleUnitDays
		return models.Automation{
			ID: id, OrgID: orgID, Name: "a", Goal: "g",
			FallbackModels: chain,
			ExecutionMode:  "sequential", BaseBranch: "main", ScheduleType: "interval",
			Timezone: "UTC", Enabled: true, IntervalValue: &iv, IntervalUnit: &unit,
			CreatedAt: now, UpdatedAt: now,
		}
	}

	t.Run("a fallback-only PATCH records a fallback_models change", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		orgID, id := uuid.New(), uuid.New()
		mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
			WithArgs(testAnyArgs(2)...).
			WillReturnRows(newAutomationRow(mock, storedAutomation(id, orgID, models.AutomationFallbackModels{})))
		mock.ExpectExec("UPDATE automations SET").
			WithArgs(testAnyArgs(32)...).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		var details any
		auditArgs := testAnyArgs(13)
		auditArgs[7] = capturedArg{value: &details}
		mock.ExpectQuery("INSERT INTO audit_logs").
			WithArgs(auditArgs...).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), time.Now()))

		h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
		h.SetAuditEmitter(newAuditEmitterForTest(mock))

		body := map[string]any{"fallback_models": map[string]any{"models": []string{models.CodexModelGPT55}}}
		req := newAutomationRequest(t, http.MethodPatch, "/api/v1/automations/"+id.String(), body, orgID, uuid.New(), map[string]string{"id": id.String()})
		rr := httptest.NewRecorder()
		h.Update(rr, req)
		require.Equal(t, http.StatusOK, rr.Code, "configuring a chain should succeed")
		require.NoError(t, mock.ExpectationsWereMet(), "a chain edit should write an audit row")

		raw, ok := details.(json.RawMessage)
		require.True(t, ok, "the audit details column should carry the encoded payload")
		var payload struct {
			Changes map[string]struct {
				Before any `json:"before"`
				After  any `json:"after"`
			} `json:"changes"`
		}
		require.NoError(t, json.Unmarshal(raw, &payload), "the audit details should be valid JSON")
		change, tracked := payload.Changes["fallback_models"]
		require.True(t, tracked, "the audit row must name fallback_models as the field that moved")
		require.Nil(t, change.Before, "an automation that had no chain should record a nil before")
		require.NotNil(t, change.After, "the new chain should be recorded")
	})

	t.Run("re-sending the stored chain records nothing", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		orgID, id := uuid.New(), uuid.New()
		stored := models.AutomationFallbackModels{Models: []string{models.CodexModelGPT55}}
		mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").
			WithArgs(testAnyArgs(2)...).
			WillReturnRows(newAutomationRow(mock, storedAutomation(id, orgID, stored)))
		mock.ExpectExec("UPDATE automations SET").
			WithArgs(testAnyArgs(32)...).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		// Deliberately no audit expectation. pgxmock only reports UNMET
		// expectations, so an unexpected INSERT would not fail
		// ExpectationsWereMet on its own — the mock rejects the call and the
		// emitter swallows the error into its logger. Reading that logger is
		// what turns a stray audit write into a failure here.
		var auditLog bytes.Buffer
		h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
		h.SetAuditEmitter(db.NewAuditEmitter(db.NewAuditLogStore(mock), zerolog.New(&auditLog)))

		body := map[string]any{"fallback_models": map[string]any{
			// The encoding a round-trip through the fallback editor produces:
			// same ranks, extra empty parallel arrays.
			"models":      []string{models.CodexModelGPT55},
			"agent_types": []string{""},
		}}
		req := newAutomationRequest(t, http.MethodPatch, "/api/v1/automations/"+id.String(), body, orgID, uuid.New(), map[string]string{"id": id.String()})
		rr := httptest.NewRecorder()
		h.Update(rr, req)
		require.Equal(t, http.StatusOK, rr.Code, "re-sending the stored chain should still succeed")
		require.Empty(t, auditLog.String(), "a no-op chain PATCH must not attempt an audit write")
		require.NoError(t, mock.ExpectationsWereMet(), "the PATCH should still read and write the automation")
	})
}
