package models

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionOrigin_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     SessionOrigin
		expectErr bool
	}{
		{name: "issue trigger", value: SessionOriginIssueTrigger},
		{name: "manual", value: SessionOriginManual},
		{name: "project", value: SessionOriginProject},
		{name: "automation", value: SessionOriginAutomation},
		{name: "revision", value: SessionOriginRevision},
		{name: "external api", value: SessionOriginExternalAPI},
		{name: "eval bootstrap", value: SessionOriginEvalBootstrap},
		{name: "eval run", value: SessionOriginEvalRun},
		{name: "automation goal improvement", value: SessionOriginAutomationGoalImprovement},
		{name: "code review", value: SessionOriginCodeReview},
		{name: "invalid", value: SessionOrigin("bogus"), expectErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown session origins")
				return
			}
			require.NoError(t, err, "Validate should accept known session origins")
		})
	}
}

func TestSessionStatusHelpers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		status          SessionStatus
		expectTerminal  bool
		expectResumable bool
		expectCanAddTab bool
	}{
		{
			name:            "running session",
			status:          SessionStatusRunning,
			expectTerminal:  false,
			expectResumable: false,
			expectCanAddTab: true,
		},
		{
			name:            "completed session",
			status:          SessionStatusCompleted,
			expectTerminal:  true,
			expectResumable: true,
			expectCanAddTab: true,
		},
		{
			name:            "awaiting input session",
			status:          SessionStatusAwaitingInput,
			expectTerminal:  false,
			expectResumable: true,
			expectCanAddTab: true,
		},
		{
			name:            "skipped session",
			status:          SessionStatusSkipped,
			expectTerminal:  true,
			expectResumable: false,
			expectCanAddTab: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expectTerminal, tt.status.IsTerminal(), "IsTerminal should return the expected value")
			require.Equal(t, tt.expectResumable, tt.status.IsResumable(), "IsResumable should return the expected value")
			require.Equal(t, tt.expectCanAddTab, tt.status.CanAddThread(), "CanAddThread should return the expected value")
		})
	}
}

func TestBranchCreationState_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     BranchCreationState
		expectErr bool
	}{
		{name: "idle", value: BranchCreationStateIdle},
		{name: "queued", value: BranchCreationStateQueued},
		{name: "pushing", value: BranchCreationStatePushing},
		{name: "succeeded", value: BranchCreationStateSucceeded},
		{name: "failed", value: BranchCreationStateFailed},
		{name: "invalid", value: BranchCreationState("bogus"), expectErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown branch creation states")
				return
			}
			require.NoError(t, err, "Validate should accept known branch creation states")
		})
	}
}

func TestSessionInteractionMode_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     SessionInteractionMode
		expectErr bool
	}{
		{name: "interactive", value: SessionInteractionModeInteractive},
		{name: "single run", value: SessionInteractionModeSingleRun},
		{name: "invalid", value: SessionInteractionMode("bogus"), expectErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown interaction modes")
				return
			}
			require.NoError(t, err, "Validate should accept known interaction modes")
		})
	}
}

func TestSessionRetryMode_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     SessionRetryMode
		expectErr bool
	}{
		{name: "checkpoint", value: SessionRetryModeCheckpoint},
		{name: "start over", value: SessionRetryModeStartOver},
		{name: "invalid", value: SessionRetryMode("fresh"), expectErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown retry modes")
				return
			}
			require.NoError(t, err, "Validate should accept known retry modes")
		})
	}
}

func TestSessionValidationPolicy_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     SessionValidationPolicy
		expectErr bool
	}{
		{name: "turn complete", value: SessionValidationPolicyOnTurnComplete},
		{name: "session end", value: SessionValidationPolicyOnSessionEnd},
		{name: "skip", value: SessionValidationPolicySkip},
		{name: "invalid", value: SessionValidationPolicy("bogus"), expectErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown validation policies")
				return
			}
			require.NoError(t, err, "Validate should accept known validation policies")
		})
	}
}

func TestLinearPrepareState_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     LinearPrepareState
		expectErr bool
	}{
		{name: "none", value: LinearPrepareStateNone},
		{name: "pending", value: LinearPrepareStatePending},
		{name: "ready", value: LinearPrepareStateReady},
		{name: "failed", value: LinearPrepareStateFailed},
		{name: "invalid", value: LinearPrepareState("bogus"), expectErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown Linear prepare states")
				return
			}
			require.NoError(t, err, "Validate should accept known Linear prepare states")
		})
	}
}

// TestLinearPrepareStateMigrationVocabularyMatchesGoEnum pins the
// chk_sessions_linear_prepare_state CHECK constraint in the migration to
// AllLinearPrepareStates. Adding a state in Go without updating the
// migration would otherwise blow up at runtime with a constraint violation
// and vice-versa; failing here flags the drift before merge.
func TestLinearPrepareStateMigrationVocabularyMatchesGoEnum(t *testing.T) {
	t.Parallel()

	const migrationFile = "000105_linear_session_linking.up.sql"
	path := filepath.Join("..", "..", "migrations", migrationFile)
	contents, err := os.ReadFile(path)
	require.NoError(t, err, "migration file %s should be readable", migrationFile)

	// Pull the literal value list out of the CHECK constraint. The pattern
	// is anchored on the constraint name so unrelated CHECKs in the file
	// can't accidentally match.
	re := regexp.MustCompile(`(?s)CONSTRAINT\s+chk_sessions_linear_prepare_state\s*` +
		`CHECK\s*\(\s*linear_prepare_state\s+IN\s*\(([^)]*)\)\s*\)`)
	match := re.FindStringSubmatch(string(contents))
	require.Len(t, match, 2, "migration must declare chk_sessions_linear_prepare_state with an IN-list")

	migrationStates := parseLinearPrepareStateList(t, match[1])
	goStates := make([]string, 0, len(AllLinearPrepareStates()))
	for _, s := range AllLinearPrepareStates() {
		goStates = append(goStates, string(s))
	}
	sort.Strings(migrationStates)
	sort.Strings(goStates)
	require.Equal(t, goStates, migrationStates,
		"chk_sessions_linear_prepare_state values must match AllLinearPrepareStates; "+
			"add the missing value to whichever side is behind")
}

func parseLinearPrepareStateList(t *testing.T, raw string) []string {
	t.Helper()
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		v := strings.TrimSpace(p)
		v = strings.Trim(v, "'\"")
		if v == "" {
			continue
		}
		out = append(out, v)
	}
	return out
}

func TestSessionAutonomy_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     SessionAutonomy
		expectErr bool
	}{
		{name: "full", value: SessionAutonomyFull},
		{name: "semi", value: SessionAutonomySemi},
		{name: "supervised", value: SessionAutonomySupervised},
		{name: "invalid", value: SessionAutonomy("auto_all"), expectErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown session autonomies")
				return
			}
			require.NoError(t, err, "Validate should accept known session autonomies")
		})
	}
}

// TestSessionAutonomyMigrationVocabularyMatchesGoEnum pins the
// chk_sessions_autonomy_level CHECK constraint in the migration to
// AllSessionAutonomies. Adding a value in Go without updating the migration
// would otherwise blow up at runtime with a constraint violation and
// vice-versa; failing here flags the drift before merge.
func TestSessionAutonomyMigrationVocabularyMatchesGoEnum(t *testing.T) {
	t.Parallel()

	const migrationFile = "000035_check_constraints.up.sql"
	path := filepath.Join("..", "..", "migrations", migrationFile)
	contents, err := os.ReadFile(path)
	require.NoError(t, err, "migration file %s should be readable", migrationFile)

	re := regexp.MustCompile(`(?s)CONSTRAINT\s+chk_sessions_autonomy_level\s+` +
		`CHECK\s*\(\s*autonomy_level\s+IN\s*\(([^)]*)\)\s*\)`)
	match := re.FindStringSubmatch(string(contents))
	require.Len(t, match, 2, "migration must declare chk_sessions_autonomy_level with an IN-list")

	migrationValues := parseLinearPrepareStateList(t, match[1])
	goValues := make([]string, 0, len(AllSessionAutonomies()))
	for _, a := range AllSessionAutonomies() {
		goValues = append(goValues, string(a))
	}
	sort.Strings(migrationValues)
	sort.Strings(goValues)
	require.Equal(t, goValues, migrationValues,
		"chk_sessions_autonomy_level values must match AllSessionAutonomies; "+
			"add the missing value to whichever side is behind")
}

func TestSessionIssueLinkRole_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     SessionIssueLinkRole
		expectErr bool
	}{
		{name: "primary", value: SessionIssueLinkRolePrimary},
		{name: "related", value: SessionIssueLinkRoleRelated},
		{name: "invalid", value: SessionIssueLinkRole("bogus"), expectErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown session issue link roles")
				return
			}
			require.NoError(t, err, "Validate should accept known session issue link roles")
		})
	}
}
