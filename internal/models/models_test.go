package models

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRepository_IsActive(t *testing.T) {
	t.Parallel()

	cases := []struct {
		status RepositoryStatus
		want   bool
	}{
		{RepositoryStatusActive, true},
		{RepositoryStatusPaused, false},
		{RepositoryStatusDisconnected, false},
		{"", false},
		{"unknown", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(string(tc.status), func(t *testing.T) {
			t.Parallel()
			repo := Repository{Status: tc.status}
			if got := repo.IsActive(); got != tc.want {
				t.Fatalf("IsActive() = %v, want %v (status %q)", got, tc.want, tc.status)
			}
		})
	}
}

func TestSessionListItem_MarshalJSON_PreservesEnrichment(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Round(time.Second)
	repositoryID := uuid.New()
	primaryIssueID := uuid.New()
	prNumber := 42
	item := SessionListItem{
		Session: Session{
			ID:               uuid.New(),
			PrimaryIssueID:   &primaryIssueID,
			OrgID:            uuid.New(),
			Origin:           SessionOriginIssueTrigger,
			InteractionMode:  SessionInteractionModeSingleRun,
			ValidationPolicy: SessionValidationPolicyOnTurnComplete,
			AgentType:        AgentTypeClaudeCode,
			Status:           SessionStatusRunning,
			AutonomyLevel:    "supervised",
			TokenMode:        "low",
			LastActivityAt:   now,
			RepositoryID:     &repositoryID,
			CreatedAt:        now,
		},
		LastViewedAt: &now,
		PRSummary: &PRSummary{
			Status:   "open",
			CIStatus: PullRequestCIStatusSuccess,
			Number:   prNumber,
			URL:      "https://example.com/pr/42",
		},
	}

	raw, err := json.Marshal(item)
	require.NoError(t, err, "marshaling a SessionListItem should succeed")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded), "marshaled SessionListItem should decode into a map")
	require.Contains(t, decoded, "last_viewed_at", "SessionListItem JSON should preserve last_viewed_at from the wrapper")
	require.Contains(t, decoded, "pr_summary", "SessionListItem JSON should preserve pr_summary from the wrapper")
}

func TestSessionPrimaryIssueHelpers(t *testing.T) {
	t.Parallel()

	primaryIssueID := uuid.New()

	tests := []struct {
		name               string
		session            Session
		wantHasPrimary     bool
		wantInteractive    bool
		wantValidateOnTurn bool
		wantValidateOnEnd  bool
	}{
		{
			name: "with primary issue linked",
			session: Session{
				PrimaryIssueID:   &primaryIssueID,
				InteractionMode:  SessionInteractionModeSingleRun,
				ValidationPolicy: SessionValidationPolicyOnTurnComplete,
			},
			wantHasPrimary:     true,
			wantInteractive:    false,
			wantValidateOnTurn: true,
			wantValidateOnEnd:  false,
		},
		{
			name: "interactive session end validation",
			session: Session{
				PrimaryIssueID:   &primaryIssueID,
				InteractionMode:  SessionInteractionModeInteractive,
				ValidationPolicy: SessionValidationPolicyOnSessionEnd,
			},
			wantHasPrimary:     true,
			wantInteractive:    true,
			wantValidateOnTurn: false,
			wantValidateOnEnd:  true,
		},
		{
			name: "zero-issue session is a first-class path",
			session: Session{
				InteractionMode:  SessionInteractionModeSingleRun,
				ValidationPolicy: SessionValidationPolicySkip,
			},
			wantHasPrimary:     false,
			wantInteractive:    false,
			wantValidateOnTurn: false,
			wantValidateOnEnd:  false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.wantHasPrimary, tt.session.HasPrimaryIssue(), "HasPrimaryIssue should match the primary issue state")
			require.Equal(t, tt.wantInteractive, tt.session.IsInteractive(), "IsInteractive should reflect the interaction mode")
			require.Equal(t, tt.wantValidateOnTurn, tt.session.ShouldValidateOnTurnComplete(), "ShouldValidateOnTurnComplete should reflect the validation policy")
			require.Equal(t, tt.wantValidateOnEnd, tt.session.ShouldValidateOnSessionEnd(), "ShouldValidateOnSessionEnd should reflect the validation policy")
		})
	}
}

func TestSession_UnmarshalJSON_FallsBackToLegacyIssueID(t *testing.T) {
	t.Parallel()

	primaryID := uuid.New()
	legacyID := uuid.New()

	t.Run("primary_issue_id wins when present", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{"id":"` + uuid.New().String() + `","primary_issue_id":"` + primaryID.String() + `","issue_id":"` + legacyID.String() + `"}`)
		var s Session
		require.NoError(t, json.Unmarshal(raw, &s), "decoder should accept canonical Phase 2 payloads")
		require.NotNil(t, s.PrimaryIssueID, "PrimaryIssueID should be populated from primary_issue_id")
		require.Equal(t, primaryID, *s.PrimaryIssueID, "primary_issue_id must take precedence over legacy issue_id")
	})

	t.Run("legacy issue_id backfills when primary_issue_id missing", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{"id":"` + uuid.New().String() + `","issue_id":"` + legacyID.String() + `"}`)
		var s Session
		require.NoError(t, json.Unmarshal(raw, &s), "decoder should accept legacy Phase 1 payloads")
		require.NotNil(t, s.PrimaryIssueID, "PrimaryIssueID should be backfilled from legacy issue_id")
		require.Equal(t, legacyID, *s.PrimaryIssueID, "decoder should preserve the legacy issue identity")
	})

	t.Run("legacy uuid.Nil does not backfill", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{"id":"` + uuid.New().String() + `","issue_id":"00000000-0000-0000-0000-000000000000"}`)
		var s Session
		require.NoError(t, json.Unmarshal(raw, &s), "decoder should accept zero-uuid legacy issue_id")
		require.Nil(t, s.PrimaryIssueID, "zero-uuid legacy issue_id should not backfill PrimaryIssueID")
	})
}

func TestSessionDetail_MarshalJSON_PreservesThreads(t *testing.T) {
	t.Parallel()

	threadID := uuid.New()
	primaryIssueID := uuid.New()
	session := SessionDetail{
		Session: Session{
			ID:               uuid.New(),
			PrimaryIssueID:   &primaryIssueID,
			OrgID:            uuid.New(),
			Origin:           SessionOriginIssueTrigger,
			InteractionMode:  SessionInteractionModeSingleRun,
			ValidationPolicy: SessionValidationPolicyOnTurnComplete,
			Status:           SessionStatusRunning,
			LastActivityAt:   time.Now().UTC(),
			CreatedAt:        time.Now().UTC(),
		},
		Threads: []SessionThread{{ID: threadID, Label: "main"}},
	}

	raw, err := json.Marshal(session)
	require.NoError(t, err, "MarshalJSON should encode SessionDetail values")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded), "marshaled SessionDetail should decode into a map")
	require.Contains(t, decoded, "threads", "SessionDetail JSON should preserve the wrapper threads field")
	require.Contains(t, decoded, "primary_issue_id", "SessionDetail JSON should expose the primary_issue_id field")
	require.NotContains(t, decoded, "issue_id", "SessionDetail JSON should not expose the legacy issue_id alias")
}

func TestSessionJSONHelpers_SurfaceEncodingErrors(t *testing.T) {
	t.Parallel()

	badJSON := json.RawMessage(`{bad-json`)
	session := Session{DiffStats: badJSON}

	_, err := session.marshalJSONMap()
	require.Error(t, err, "marshalJSONMap should fail when embedded raw JSON is invalid")

	_, err = json.Marshal(SessionDetail{Session: session})
	require.Error(t, err, "SessionDetail MarshalJSON should propagate session JSON encoding failures")

	_, err = json.Marshal(SessionListItem{Session: session})
	require.Error(t, err, "SessionListItem MarshalJSON should propagate session JSON encoding failures")
}
