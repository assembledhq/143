package models

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func newPreparationIntentCodecFixture() CodeReviewPreparationIntent {
	return CodeReviewPreparationIntent{
		Version: 1, OrgID: uuid.New(), JobID: uuid.New(), MetadataID: uuid.New(), SessionID: uuid.New(), PullRequestID: uuid.New(),
		HeadSHA: strings.Repeat("a", 40), WorkspaceGeneration: 0, ThreadID: nil, Turn: 1, TurnScope: "session", Recheck: false,
		FailureClass: CodeReviewPreparationFailureSetup, Stage: "readiness",
	}
}

func TestCodeReviewPreparationIntentRoundTrip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*CodeReviewPreparationIntent)
	}{
		{name: "zero generation false recheck and explicit null thread retain provenance", mutate: func(*CodeReviewPreparationIntent) {}},
		{name: "explicit thread retains its own turn", mutate: func(i *CodeReviewPreparationIntent) {
			id := uuid.New()
			i.ThreadID = &id
			i.TurnScope = "thread"
			i.Turn = 9
		}},
		{name: "nonzero observed generation survives serialization", mutate: func(i *CodeReviewPreparationIntent) { i.WorkspaceGeneration = 97 }},
		{name: "cleanup has finite classification", mutate: func(i *CodeReviewPreparationIntent) { i.FailureClass = CodeReviewPreparationFailureCleanup }},
		{name: "source integrity has finite classification", mutate: func(i *CodeReviewPreparationIntent) { i.FailureClass = CodeReviewPreparationFailureIntegrity }},
		{name: "deadline has finite classification", mutate: func(i *CodeReviewPreparationIntent) { i.FailureClass = CodeReviewPreparationFailureDeadline }},
		{name: "clone stage survives serialization", mutate: func(i *CodeReviewPreparationIntent) { i.Stage = "clone" }},
		{name: "hydration stage survives serialization", mutate: func(i *CodeReviewPreparationIntent) { i.Stage = "hydration" }},
		{name: "checkout stage survives serialization", mutate: func(i *CodeReviewPreparationIntent) { i.Stage = "checkout" }},
		{name: "dependency setup stage survives serialization", mutate: func(i *CodeReviewPreparationIntent) { i.Stage = "dependency_setup" }},
		{name: "long supported Git identity survives serialization", mutate: func(i *CodeReviewPreparationIntent) { i.HeadSHA = strings.Repeat("b", 64) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			expected := newPreparationIntentCodecFixture()
			tt.mutate(&expected)
			require.NoError(t, expected.Validate(), "supported intent should validate without inferring omitted provenance")
			raw, err := json.Marshal(expected)
			require.NoError(t, err, "encode complete intent fixture")
			actual, err := DecodeCodeReviewPreparationIntent(raw)
			require.NoError(t, err, "decode complete intent without losing zero or null values")
			require.Equal(t, expected, actual, "round trip should preserve every captured identity and failure field")
		})
	}
}

func TestCodeReviewPreparationIntentRequiresEveryProvenanceMember(t *testing.T) {
	t.Parallel()
	tests := []struct{ name string }{
		{"version"}, {"org_id"}, {"job_id"}, {"metadata_id"}, {"session_id"}, {"pull_request_id"}, {"head_sha"},
		{"workspace_generation"}, {"thread_id"}, {"turn"}, {"turn_scope"}, {"recheck"}, {"failure_class"}, {"stage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newPreparationIntentCodecFixture()
			raw, err := json.Marshal(fixture)
			require.NoError(t, err, "encode isolated provenance fixture")
			var members map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &members), "read fixture members without changing zero values")
			delete(members, tt.name)
			raw, err = json.Marshal(members)
			require.NoError(t, err, "encode missing-member fixture")
			_, err = DecodeCodeReviewPreparationIntent(raw)
			require.Error(t, err, "missing provenance must not be reconstructed from default values or unrelated job state")
		})
	}
}

func TestCodeReviewPreparationIntentRejectsInvalidAuthority(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*CodeReviewPreparationIntent)
	}{
		{"unknown schema version", func(i *CodeReviewPreparationIntent) { i.Version = 2 }},
		{"missing organization", func(i *CodeReviewPreparationIntent) { i.OrgID = uuid.Nil }},
		{"missing job", func(i *CodeReviewPreparationIntent) { i.JobID = uuid.Nil }},
		{"missing metadata", func(i *CodeReviewPreparationIntent) { i.MetadataID = uuid.Nil }},
		{"missing session", func(i *CodeReviewPreparationIntent) { i.SessionID = uuid.Nil }},
		{"missing pull request", func(i *CodeReviewPreparationIntent) { i.PullRequestID = uuid.Nil }},
		{"empty head identity", func(i *CodeReviewPreparationIntent) { i.HeadSHA = "" }},
		{"unbounded head identity", func(i *CodeReviewPreparationIntent) { i.HeadSHA = strings.Repeat("a", 65) }},
		{"head identity contains control data", func(i *CodeReviewPreparationIntent) { i.HeadSHA = "head\nsecret" }},
		{"negative generation", func(i *CodeReviewPreparationIntent) { i.WorkspaceGeneration = -1 }},
		{"zero turn", func(i *CodeReviewPreparationIntent) { i.Turn = 0 }},
		{"negative turn", func(i *CodeReviewPreparationIntent) { i.Turn = -1 }},
		{"recheck cannot acquire baseline authority", func(i *CodeReviewPreparationIntent) { i.Recheck = true }},
		{"unknown failure class", func(i *CodeReviewPreparationIntent) { i.FailureClass = "raw provider error with secret" }},
		{"unknown stage", func(i *CodeReviewPreparationIntent) { i.Stage = "raw command output" }},
		{"unknown turn scope", func(i *CodeReviewPreparationIntent) { i.TurnScope = "latest_message" }},
		{"inferred thread cannot claim thread turn", func(i *CodeReviewPreparationIntent) { i.TurnScope = "thread" }},
		{"explicit nil uuid has no thread authority", func(i *CodeReviewPreparationIntent) { id := uuid.Nil; i.ThreadID = &id; i.TurnScope = "thread" }},
		{"explicit thread cannot claim session turn", func(i *CodeReviewPreparationIntent) { id := uuid.New(); i.ThreadID = &id }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := newPreparationIntentCodecFixture()
			tt.mutate(&fixture)
			require.Error(t, fixture.Validate(), "invalid identity or finite classification must not acquire terminal projection authority")
			raw, err := json.Marshal(fixture)
			require.NoError(t, err, "encode invalid semantic fixture without syntax errors")
			_, err = DecodeCodeReviewPreparationIntent(raw)
			require.Error(t, err, "persisted invalid provenance must be rejected before projection")
		})
	}
}

func TestCodeReviewPreparationIntentRejectsMalformedRecords(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		transform func([]byte) []byte
	}{
		{"empty record", func([]byte) []byte { return nil }},
		{"null record", func([]byte) []byte { return []byte("null") }},
		{"array record", func([]byte) []byte { return []byte("[]") }},
		{"malformed JSON", func(raw []byte) []byte { return raw[:len(raw)-1] }},
		{"trailing second object", func(raw []byte) []byte { return append(raw, []byte(" {}")...) }},
		{"unknown extension", func(raw []byte) []byte {
			return append(append([]byte{}, raw[:len(raw)-1]...), []byte(",\"provider_stderr\":\"secret\"}")...)
		}},
		{"ambiguous duplicate generation", func(raw []byte) []byte {
			return append(append([]byte{}, raw[:len(raw)-1]...), []byte(",\"workspace_generation\":99}")...)
		}},
		{"ambiguous duplicate thread", func(raw []byte) []byte {
			return append(append([]byte{}, raw[:len(raw)-1]...), []byte(",\"thread_id\":null}")...)
		}},
		{"over bound including whitespace", func(raw []byte) []byte { return append(raw, []byte(strings.Repeat(" ", 2049-len(raw)))...) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(newPreparationIntentCodecFixture())
			require.NoError(t, err, "encode complete input for syntax-boundary fixture")
			_, err = DecodeCodeReviewPreparationIntent(tt.transform(raw))
			require.Error(t, err, "malformed or ambiguous stored record must never authorize projection")
		})
	}
}

func TestCodeReviewPreparationIntentRejectsNullRequiredMembers(t *testing.T) {
	t.Parallel()
	tests := []struct{ name string }{
		{"version"}, {"org_id"}, {"job_id"}, {"metadata_id"}, {"session_id"}, {"pull_request_id"}, {"head_sha"},
		{"workspace_generation"}, {"turn"}, {"turn_scope"}, {"recheck"}, {"failure_class"}, {"stage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(newPreparationIntentCodecFixture())
			require.NoError(t, err, "encode complete input for null-member fixture")
			var members map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &members), "read isolated null-member fixture")
			members[tt.name] = json.RawMessage("null")
			raw, err = json.Marshal(members)
			require.NoError(t, err, "encode explicit null required member")
			_, err = DecodeCodeReviewPreparationIntent(raw)
			require.Error(t, err, "explicit null cannot substitute for required immutable provenance")
		})
	}
}

func TestCodeReviewPreparationIntentAcceptsExactSizeBound(t *testing.T) {
	t.Parallel()
	expected := newPreparationIntentCodecFixture()
	raw, err := json.Marshal(expected)
	require.NoError(t, err, "encode valid bounded fixture")
	raw = append(raw, []byte(strings.Repeat(" ", 2048-len(raw)))...)
	actual, err := DecodeCodeReviewPreparationIntent(raw)
	require.NoError(t, err, "complete record at the documented size bound should remain valid")
	require.Equal(t, expected, actual, "size boundary must preserve every captured value")
}
