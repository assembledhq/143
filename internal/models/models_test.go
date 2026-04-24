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
		{RepositoryStatusDisconnected, false},
		{"", false},
		{"unknown", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(string(tc.status), func(t *testing.T) {
			t.Parallel()
			repo := Repository{Status: string(tc.status)}
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
	prNumber := 42
	item := SessionListItem{
		Session: Session{
			ID:               uuid.New(),
			IssueID:          uuid.New(),
			OrgID:            uuid.New(),
			Origin:           SessionOriginIssueTrigger,
			InteractionMode:  SessionInteractionModeSingleRun,
			ValidationPolicy: SessionValidationPolicyOnTurnComplete,
			AgentType:        AgentTypeClaudeCode,
			Status:           string(SessionStatusRunning),
			AutonomyLevel:    "supervised",
			TokenMode:        "low",
			LastActivityAt:   now,
			RepositoryID:     &repositoryID,
			CreatedAt:        now,
		},
		LastViewedAt: &now,
		PRSummary: &PRSummary{
			Status:   "open",
			CIStatus: "green",
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

	explicitPrimary := uuid.New()
	legacyIssue := uuid.New()

	tests := []struct {
		name               string
		session            Session
		wantPrimary        *uuid.UUID
		wantHasPrimary     bool
		wantInteractive    bool
		wantValidateOnTurn bool
		wantValidateOnEnd  bool
	}{
		{
			name: "prefers explicit primary issue",
			session: Session{
				IssueID:          legacyIssue,
				PrimaryIssueID:   &explicitPrimary,
				InteractionMode:  SessionInteractionModeSingleRun,
				ValidationPolicy: SessionValidationPolicyOnTurnComplete,
			},
			wantPrimary:        &explicitPrimary,
			wantHasPrimary:     true,
			wantInteractive:    false,
			wantValidateOnTurn: true,
			wantValidateOnEnd:  false,
		},
		{
			name: "falls back to legacy issue id",
			session: Session{
				IssueID:          legacyIssue,
				InteractionMode:  SessionInteractionModeInteractive,
				ValidationPolicy: SessionValidationPolicyOnSessionEnd,
			},
			wantPrimary:        &legacyIssue,
			wantHasPrimary:     true,
			wantInteractive:    true,
			wantValidateOnTurn: false,
			wantValidateOnEnd:  true,
		},
		{
			name: "returns nil when no issue is attached",
			session: Session{
				InteractionMode:  SessionInteractionModeSingleRun,
				ValidationPolicy: SessionValidationPolicySkip,
			},
			wantPrimary:        nil,
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

			gotPrimary := tt.session.EffectivePrimaryIssueID()
			require.Equal(t, tt.wantPrimary, gotPrimary, "EffectivePrimaryIssueID should resolve the expected primary issue")
			require.Equal(t, tt.wantHasPrimary, tt.session.HasPrimaryIssue(), "HasPrimaryIssue should match the resolved primary issue state")
			require.Equal(t, tt.wantInteractive, tt.session.IsInteractive(), "IsInteractive should reflect the interaction mode")
			require.Equal(t, tt.wantValidateOnTurn, tt.session.ShouldValidateOnTurnComplete(), "ShouldValidateOnTurnComplete should reflect the validation policy")
			require.Equal(t, tt.wantValidateOnEnd, tt.session.ShouldValidateOnSessionEnd(), "ShouldValidateOnSessionEnd should reflect the validation policy")
		})
	}
}

func TestSession_UnmarshalJSON_BackfillsPrimaryIssueID(t *testing.T) {
	t.Parallel()

	issueID := uuid.New()
	raw := []byte(`{"id":"` + uuid.New().String() + `","issue_id":"` + issueID.String() + `"}`)

	var session Session
	err := json.Unmarshal(raw, &session)
	require.NoError(t, err, "UnmarshalJSON should decode session payloads")
	require.Equal(t, issueID, session.IssueID, "UnmarshalJSON should decode the legacy issue_id field")
	require.NotNil(t, session.PrimaryIssueID, "UnmarshalJSON should backfill PrimaryIssueID from issue_id")
	require.Equal(t, issueID, *session.PrimaryIssueID, "UnmarshalJSON should preserve the primary issue identity")
}

func TestSessionDetail_MarshalJSON_PreservesThreads(t *testing.T) {
	t.Parallel()

	threadID := uuid.New()
	session := SessionDetail{
		Session: Session{
			ID:               uuid.New(),
			IssueID:          uuid.New(),
			PrimaryIssueID:   func() *uuid.UUID { id := uuid.New(); return &id }(),
			OrgID:            uuid.New(),
			Origin:           SessionOriginIssueTrigger,
			InteractionMode:  SessionInteractionModeSingleRun,
			ValidationPolicy: SessionValidationPolicyOnTurnComplete,
			Status:           string(SessionStatusRunning),
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
	require.Contains(t, decoded, "issue_id", "SessionDetail JSON should preserve the effective issue_id field")
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

func TestSession_UnmarshalJSON_InvalidPayload(t *testing.T) {
	t.Parallel()

	var session Session
	err := json.Unmarshal([]byte(`{bad-json`), &session)
	require.Error(t, err, "UnmarshalJSON should reject invalid JSON payloads")
}
