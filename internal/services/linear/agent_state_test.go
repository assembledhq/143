package linear

import (
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
)

func TestMilestoneActivity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		event     MilestoneEvent
		prNumber  int
		wantOK    bool
		wantType  models.LinearAgentActivityType
		wantState string
		bodyHas   string
	}{
		{
			name:   "linked is suppressed (dispatcher already emitted bootstrap)",
			event:  MilestoneLinked,
			wantOK: false,
		},
		{
			name:      "started emits action with state pin",
			event:     MilestoneStarted,
			wantOK:    true,
			wantType:  models.LinearAgentActivityAction,
			wantState: "active",
		},
		{
			name:     "pr_opened emits response with PR number",
			event:    MilestonePROpened,
			prNumber: 42,
			wantOK:   true,
			wantType: models.LinearAgentActivityResponse,
			bodyHas:  "PR #42",
		},
		{
			name:      "pr_merged pins state=complete",
			event:     MilestonePRMerged,
			prNumber:  42,
			wantOK:    true,
			wantType:  models.LinearAgentActivityAction,
			wantState: "complete",
			bodyHas:   "PR #42",
		},
		{
			name:      "ended_no_pr is response, state=complete",
			event:     MilestoneEndedNoPR,
			wantOK:    true,
			wantType:  models.LinearAgentActivityResponse,
			wantState: "complete",
		},
		{
			name:      "failed is error activity, state=error",
			event:     MilestoneFailed,
			wantOK:    true,
			wantType:  models.LinearAgentActivityError,
			wantState: "error",
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			activity, ok := MilestoneActivity(tc.event, tc.prNumber)
			if ok != tc.wantOK {
				t.Fatalf("MilestoneActivity ok=%v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if activity.Type != tc.wantType {
				t.Errorf("Type=%q want %q", activity.Type, tc.wantType)
			}
			if activity.PinSessionState != tc.wantState {
				t.Errorf("PinSessionState=%q want %q", activity.PinSessionState, tc.wantState)
			}
			if tc.bodyHas != "" && !strings.Contains(activity.Body, tc.bodyHas) {
				t.Errorf("Body=%q want substring %q", activity.Body, tc.bodyHas)
			}
			// idem_key must be stable + match the milestone:<event> shape so
			// concurrent emits collide on the activity-log UNIQUE.
			wantKey := "milestone:" + string(tc.event)
			if activity.IdemKey != wantKey {
				t.Errorf("IdemKey=%q want %q", activity.IdemKey, wantKey)
			}
		})
	}
}

func TestBootstrapActivity(t *testing.T) {
	t.Parallel()
	a := BootstrapActivity("ACS-1234")
	if a.Type != models.LinearAgentActivityThought {
		t.Errorf("Type=%q want thought", a.Type)
	}
	if !a.Ephemeral {
		t.Errorf("BootstrapActivity should be ephemeral so it scrolls out of the activity feed")
	}
	if a.IdemKey != "bootstrap:opened" {
		t.Errorf("IdemKey=%q want bootstrap:opened", a.IdemKey)
	}
	if !strings.Contains(a.Body, "ACS-1234") {
		t.Errorf("Body=%q should reference the issue identifier", a.Body)
	}
}

func TestBootstrapActivity_NoIdentifier(t *testing.T) {
	t.Parallel()
	a := BootstrapActivity("")
	if strings.Contains(a.Body, "<nil>") || strings.Contains(a.Body, "%!") {
		t.Errorf("Body=%q must not contain stray formatting placeholders when identifier is empty", a.Body)
	}
}

func TestUnmappedRepoActivity(t *testing.T) {
	t.Parallel()
	a := UnmappedRepoActivity("Backend")
	if a.Type != models.LinearAgentActivityResponse {
		t.Errorf("Type=%q want response", a.Type)
	}
	if a.PinSessionState != "complete" {
		t.Errorf("PinSessionState=%q want complete (benign user state, not an error)", a.PinSessionState)
	}
	if !strings.Contains(a.Body, "Backend") {
		t.Errorf("Body=%q should mention the team name", a.Body)
	}
}
