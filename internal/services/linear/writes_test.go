package linear

import (
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
)

func TestSubtitleForMilestone(t *testing.T) {
	t.Parallel()
	cases := []struct {
		event MilestoneEvent
		pr    int
		want  string
	}{
		{MilestoneLinked, 0, "Running"},
		{MilestonePROpened, 0, "PR open"},
		{MilestonePROpened, 42, "PR #42 open"},
		{MilestonePRMerged, 7, "PR #7 merged"},
		{MilestoneEndedNoPR, 0, "Ended without PR"},
		{MilestoneFailed, 0, "Failed"},
	}
	for _, c := range cases {
		got := subtitleForMilestone(c.event, c.pr)
		if got != c.want {
			t.Errorf("subtitleForMilestone(%v, %d) = %q, want %q", c.event, c.pr, got, c.want)
		}
	}
}

func TestCommentBodyForMilestone_HasBotPrefix(t *testing.T) {
	t.Parallel()
	cases := []MilestoneEvent{
		MilestoneLinked, MilestonePROpened, MilestonePRMerged,
		MilestoneEndedNoPR, MilestoneFailed,
	}
	for _, e := range cases {
		body := commentBodyForMilestone(e, "ACS-1", "https://143.example/sessions/abc", 5)
		if !strings.HasPrefix(body, botCommentPrefix) {
			t.Errorf("comment for %s missing mandatory bot prefix: %q", e, body)
		}
		if !strings.Contains(body, "ACS-1") {
			t.Errorf("comment for %s missing identifier: %q", e, body)
		}
	}
}

func TestIsForwardMove(t *testing.T) {
	t.Parallel()
	cases := []struct {
		from, to string
		want     bool
	}{
		{"backlog", "started", true},
		{"unstarted", "completed", true},
		{"started", "completed", true},
		{"completed", "started", false}, // forward-only: don't move out of completed
		{"canceled", "started", false},
		{"started", "backlog", false}, // never go backwards
		{"", "started", true},         // unknown current → bootstrap to first state
		{"started", "started", false}, // sideways → no
		{"unknown", "started", false}, // unknown types: refuse
	}
	for _, c := range cases {
		got := isForwardMove(c.from, c.to)
		if got != c.want {
			t.Errorf("isForwardMove(%q, %q) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}

func TestTeamKeyFromIdentifier(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"ACS-1234": "ACS",
		"":         "",
		"NOSEP":    "",
		"-1":       "",
	}
	for in, want := range cases {
		if got := teamKeyFromIdentifier(in); got != want {
			t.Errorf("teamKeyFromIdentifier(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDefaultLinearAutomationSettings_OnByDefault locks the design 62
// contract that fresh orgs have visibility + state-sync enabled. If a
// future migration changes the default-on shape, this test should fail
// loudly so we update the design doc and migration notes together.
func TestDefaultLinearAutomationSettings_OnByDefault(t *testing.T) {
	t.Parallel()
	settings := defaultLinearAutomationSettings()
	if !settings.EffectivePostSessionLinks() {
		t.Error("PostSessionLinks must default to true; design 62 mandates visibility-on")
	}
	if !settings.EffectiveMoveWorkflowStates() {
		t.Error("MoveWorkflowStates must default to true; design 62 mandates state-sync-on")
	}
	if len(settings.ReviewStateNamePreferences) == 0 {
		t.Error("ReviewStateNamePreferences must default non-empty so PR-open transitions resolve")
	}
}

// TestEffectiveAccessorsDistinguishMissingFromExplicitFalse pins the
// pointer-typed flag semantics so a future refactor can't silently
// flip explicit-off back to default-on.
func TestEffectiveAccessorsDistinguishMissingFromExplicitFalse(t *testing.T) {
	t.Parallel()
	missing := models.LinearAutomationSettings{}
	if !missing.EffectivePostSessionLinks() {
		t.Error("nil PostSessionLinks must read as design default (true)")
	}
	if !missing.EffectiveMoveWorkflowStates() {
		t.Error("nil MoveWorkflowStates must read as design default (true)")
	}

	f := false
	explicitOff := models.LinearAutomationSettings{
		PostSessionLinks:   &f,
		MoveWorkflowStates: &f,
	}
	if explicitOff.EffectivePostSessionLinks() {
		t.Error("explicit false PostSessionLinks must NOT be silently flipped on")
	}
	if explicitOff.EffectiveMoveWorkflowStates() {
		t.Error("explicit false MoveWorkflowStates must NOT be silently flipped on")
	}
}
