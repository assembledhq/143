package db

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionIssueLinksMigration_PrioritizesIssueBackedSessionsOverUserID(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "migrations", "000091_session_issue_links.up.sql")
	src, err := os.ReadFile(path)
	require.NoError(t, err, "reading the session issue links migration should succeed")

	text := string(src)

	require.Regexp(
		t,
		regexp.MustCompile(`(?s)origin = CASE.*WHEN issue_id IS NOT NULL THEN 'issue_trigger'.*WHEN triggered_by_user_id IS NOT NULL THEN 'manual'`),
		text,
		"migration should classify issue-backed sessions as issue-triggered before the manual fallback",
	)
	require.Regexp(
		t,
		regexp.MustCompile(`(?s)interaction_mode = CASE.*WHEN issue_id IS NOT NULL THEN 'single_run'.*WHEN triggered_by_user_id IS NOT NULL THEN 'interactive'`),
		text,
		"migration should keep issue-backed sessions on the single-run path before the interactive manual fallback",
	)
	require.Regexp(
		t,
		regexp.MustCompile(`(?s)validation_policy = CASE.*WHEN issue_id IS NOT NULL THEN 'on_turn_complete'.*WHEN triggered_by_user_id IS NOT NULL THEN 'on_session_end'`),
		text,
		"migration should keep issue-backed sessions on turn-complete validation before the manual fallback",
	)
}
