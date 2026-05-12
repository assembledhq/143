package models

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLinearAgentSessionStateMigrationVocabularyMatchesGoEnum pins the
// chk_linear_agent_sessions_state CHECK constraint in the migration to
// AllLinearAgentSessionStates. Drift on either side fails here at compile
// time of the test suite rather than at runtime as a constraint violation.
func TestLinearAgentSessionStateMigrationVocabularyMatchesGoEnum(t *testing.T) {
	t.Parallel()

	const migrationFile = "000122_linear_agent.up.sql"
	path := filepath.Join("..", "..", "migrations", migrationFile)
	contents, err := os.ReadFile(path)
	require.NoError(t, err, "test should read the linear_agent migration")

	re := regexp.MustCompile(`(?s)CONSTRAINT\s+chk_linear_agent_sessions_state\s+` +
		`CHECK\s*\(\s*state\s+IN\s*\(([^)]*)\)\s*\)`)
	match := re.FindStringSubmatch(string(contents))
	require.Len(t, match, 2, "migration must declare chk_linear_agent_sessions_state with an IN-list")

	migrationValues := parseLinearPrepareStateList(t, match[1])
	goValues := make([]string, 0, len(AllLinearAgentSessionStates()))
	for _, s := range AllLinearAgentSessionStates() {
		goValues = append(goValues, string(s))
	}
	sort.Strings(migrationValues)
	sort.Strings(goValues)
	require.Equal(t, goValues, migrationValues,
		"chk_linear_agent_sessions_state values must match AllLinearAgentSessionStates; "+
			"add the missing value to whichever side is behind")
}

// TestLinearAgentActivityTypeMigrationVocabularyMatchesGoEnum pins the
// chk_linear_agent_activity_log_type CHECK constraint to
// AllLinearAgentActivityTypes.
func TestLinearAgentActivityTypeMigrationVocabularyMatchesGoEnum(t *testing.T) {
	t.Parallel()

	const migrationFile = "000122_linear_agent.up.sql"
	path := filepath.Join("..", "..", "migrations", migrationFile)
	contents, err := os.ReadFile(path)
	require.NoError(t, err, "test should read the linear_agent migration")

	re := regexp.MustCompile(`(?s)CONSTRAINT\s+chk_linear_agent_activity_log_type\s+` +
		`CHECK\s*\(\s*activity_type\s+IN\s*\(([^)]*)\)\s*\)`)
	match := re.FindStringSubmatch(string(contents))
	require.Len(t, match, 2, "migration must declare chk_linear_agent_activity_log_type with an IN-list")

	migrationValues := parseLinearPrepareStateList(t, match[1])
	goValues := make([]string, 0, len(AllLinearAgentActivityTypes()))
	for _, a := range AllLinearAgentActivityTypes() {
		goValues = append(goValues, string(a))
	}
	sort.Strings(migrationValues)
	sort.Strings(goValues)
	require.Equal(t, goValues, migrationValues,
		"chk_linear_agent_activity_log_type values must match AllLinearAgentActivityTypes; "+
			"add the missing value to whichever side is behind")
}
