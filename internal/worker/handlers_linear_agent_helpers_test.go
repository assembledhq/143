package worker

import (
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/linear"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// These tests cover the pure helpers in handlers_linear_agent_helpers.go.
// The full created-path closure relies on too many concrete stores to
// unit-test cleanly without a Postgres harness; these tests pin the
// payload-construction logic that drives the agent's first-turn
// experience, since that's what users actually see if the helpers
// produce wrong output.

func TestBuildAgentSession(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	issue := &models.Issue{ID: uuid.New(), OrgID: orgID, ExternalID: "iss_1"}
	fetched := &linear.FetchedIssue{
		ID:          "iss_1",
		Identifier:  "ACS-42",
		Title:       "Fix the thing",
		Description: "It's broken",
		TeamID:      "team_1",
	}
	repo := linear.AgentRepoResolveResult{RepositoryID: repoID, DefaultBranch: "release/2026-05", Source: "team_default_mapping"}

	session := buildAgentSession(orgID, repo, issue, fetched)
	require.Equal(t, orgID, session.OrgID, "session inherits org from caller, not from the issue (defense against cross-org bugs)")
	require.Equal(t, models.SessionOriginIssueTrigger, session.Origin,
		"origin must mark this as an inbound trigger, not a manual session — drives downstream PM/automation behavior")
	require.NotNil(t, session.PrimaryIssueID, "primary issue link is what makes HandleAgentMilestone fire")
	require.Equal(t, issue.ID, *session.PrimaryIssueID)
	require.NotNil(t, session.RepositoryID)
	require.Equal(t, repoID, *session.RepositoryID)
	require.NotNil(t, session.TargetBranch, "mapped default branch should become the session target branch")
	require.Equal(t, "release/2026-05", *session.TargetBranch, "session target branch should honor the team repo mapping override")
	require.NotNil(t, session.LinearIdentifierHint)
	require.Equal(t, "ACS-42", *session.LinearIdentifierHint,
		"identifier hint feeds the branch-naming logic and the PR title prefix")
	require.Equal(t, models.LinearPrepareStateReady, session.LinearPrepareState,
		"agent-triggered sessions skip the prepare-and-link work; PrepareState must already be ready")
	require.NotNil(t, session.Title)
	require.Equal(t, "Fix the thing", *session.Title)
	require.NotNil(t, session.PMApproach, "PMApproach carries the issue context so run_agent doesn't need a fresh Linear fetch")
	require.Contains(t, *session.PMApproach, "ACS-42")
}

func TestBuildAgentSession_FallsBackToIdentifierWhenTitleEmpty(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	issue := &models.Issue{ID: uuid.New()}
	fetched := &linear.FetchedIssue{
		Identifier: "ACS-42",
		// Title intentionally empty
	}
	session := buildAgentSession(orgID, linear.AgentRepoResolveResult{RepositoryID: uuid.New()}, issue, fetched)
	require.NotNil(t, session.Title)
	require.Equal(t, "ACS-42", *session.Title,
		"empty title falls back to identifier so the sessions list never shows a blank row")
}

func TestBuildIssueApproachPrompt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		fetched     *linear.FetchedIssue
		mustHave    []string
		mustNotHave []string
	}{
		{
			name:    "nil fetched returns empty",
			fetched: nil,
		},
		{
			name: "renders title and description",
			fetched: &linear.FetchedIssue{
				Identifier:  "ACS-42",
				Title:       "Fix the thing",
				Description: "It's broken",
			},
			mustHave: []string{"ACS-42", "Fix the thing", "It's broken"},
		},
		{
			name: "includes recent comments when present",
			fetched: &linear.FetchedIssue{
				Identifier:  "ACS-42",
				Title:       "X",
				Description: "Y",
				Comments: []linear.FetchedComment{
					{Author: "alice", Body: "first"},
					{Author: "bob", Body: "second"},
				},
			},
			mustHave: []string{"alice", "first", "bob", "second", "Recent discussion"},
		},
		{
			name: "no Recent discussion section when comments empty",
			fetched: &linear.FetchedIssue{
				Identifier: "ACS-42",
				Title:      "X",
			},
			mustNotHave: []string{"Recent discussion"},
		},
		{
			name: "includes attachments when present",
			fetched: &linear.FetchedIssue{
				Identifier: "ACS-42",
				Title:      "X",
				Attachments: []linear.FetchedAttachment{
					{Title: "Design doc", URL: "https://example.com/doc"},
				},
			},
			mustHave: []string{"Linked references", "Design doc", "https://example.com/doc"},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := buildIssueApproachPrompt(tc.fetched)
			if tc.fetched == nil {
				require.Empty(t, out, "nil fetched must produce empty prompt; downstream callers handle that as 'no PM approach'")
				return
			}
			for _, want := range tc.mustHave {
				require.True(t, strings.Contains(out, want),
					"prompt %q missing required substring %q", out, want)
			}
			for _, must := range tc.mustNotHave {
				require.False(t, strings.Contains(out, must),
					"prompt %q must NOT contain %q", out, must)
			}
		})
	}
}
