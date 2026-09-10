package codereview

import (
	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestCodeReviewScheduleAdmission(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                        string
		mode                                        models.CodeReviewRequestMode
		explicit, approved, draft, paused, disabled bool
		reason                                      models.CodeReviewWaitReason
		state                                       models.CodeReviewScheduleState
		dueMinutes                                  int
	}{
		{name: "automatic waits for cadence", mode: models.CodeReviewEnsureCurrent, reason: models.CodeReviewWaitInterval, state: models.CodeReviewScheduleWaiting, dueMinutes: 15},
		{name: "review now bypasses timers", mode: models.CodeReviewReviewNow, explicit: true, state: models.CodeReviewScheduleWaiting, dueMinutes: 3},
		{name: "review now does not bypass draft", mode: models.CodeReviewReviewNow, explicit: true, draft: true, reason: models.CodeReviewWaitDraft, state: models.CodeReviewSchedulePaused},
		{name: "manual bypasses automatic pause", mode: models.CodeReviewReviewNow, explicit: true, paused: true, state: models.CodeReviewScheduleWaiting, dueMinutes: 3},
		{name: "automatic respects pause", mode: models.CodeReviewEnsureCurrent, paused: true, reason: models.CodeReviewWaitPaused, state: models.CodeReviewSchedulePaused},
		{name: "manual respects disabled policy", mode: models.CodeReviewReviewNow, explicit: true, disabled: true, reason: models.CodeReviewWaitPolicy, state: models.CodeReviewSchedulePaused},
		{name: "automatic stops after approval", mode: models.CodeReviewEnsureCurrent, approved: true, reason: models.CodeReviewWaitApproved, state: models.CodeReviewSchedulePaused},
		{name: "explicit allowed after approval", mode: models.CodeReviewReviewNow, explicit: true, approved: true, state: models.CodeReviewScheduleWaiting, dueMinutes: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			start := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
			now := start.Add(3 * time.Minute)
			quiet, interval := 300, 900
			policy := models.DefaultCodeReviewPolicyConfig()
			policy.Enabled = !tt.disabled
			policy.SchedulingPolicy = &models.CodeReviewSchedulingPolicy{QuietPeriodSeconds: &quiet, MinimumIntervalSeconds: &interval}
			state := models.CodeReviewPRState{IsDraft: tt.draft, AutomaticPaused: tt.paused, LastMaterialChangeAt: &now, LastAgentStartAt: &start}
			applyScheduleWait(&state, policy, tt.explicit, tt.approved, tt.mode, now)
			require.Equal(t, tt.reason, state.WaitReason, "admission explains the governing wait")
			require.Equal(t, tt.state, state.State, "held work is never approval")
			if tt.dueMinutes > 0 {
				require.Equal(t, start.Add(time.Duration(tt.dueMinutes)*time.Minute), *state.EligibleAt, "deadline uses both timers or explicit bypass")
				require.Nil(t, state.RetryAt, "eligible work has no held-state poll")
			} else {
				require.Nil(t, state.EligibleAt, "held work promises no execution time")
				require.Equal(t, now.Add(5*time.Minute), *state.RetryAt, "held work has a bounded reconciliation wake")
			}
		})
	}
}
