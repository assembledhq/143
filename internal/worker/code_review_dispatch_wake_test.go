package worker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func codeReviewDispatchWakeFixture(optional string) (runCodeReviewPayload, json.RawMessage) {
	job := runCodeReviewPayload{
		OrgID:         uuid.MustParse("11111111-1111-4111-8111-111111111111"),
		SessionID:     uuid.MustParse("22222222-2222-4222-8222-222222222222"),
		MetadataID:    uuid.MustParse("33333333-3333-4333-8333-333333333333"),
		RepositoryID:  uuid.MustParse("44444444-4444-4444-8444-444444444444"),
		PullRequestID: uuid.MustParse("55555555-5555-4555-8555-555555555555"),
		PolicyID:      uuid.MustParse("66666666-6666-4666-8666-666666666666"),
		PolicyVersion: 7, HeadSHA: "synthetic-original-head", OutputKey: "synthetic-original-output",
	}
	// Deliberately retain whitespace, integer lexemes beyond float64 precision,
	// a maximal int64, optional unknown data and an exponent's original spelling.
	raw := fmt.Sprintf(`{ "org_id":"%s", "session_id":"%s", "metadata_id":"%s", "repository_id":"%s", "pull_request_id":"%s", "policy_id":"%s", "policy_version":7, "head_sha":"synthetic-original-head", "review_output_key":"synthetic-original-output", "from_fork":false, "future":{"max_int64":9223372036854775807,"precise":9007199254740993,"exponent":1e+09,"nested":[true,null,{"retained":"original"}]}%s }`,
		job.OrgID, job.SessionID, job.MetadataID, job.RepositoryID, job.PullRequestID, job.PolicyID, optional)
	return job, json.RawMessage(raw)
}

func codeReviewDispatchWakeEdit(t *testing.T, raw json.RawMessage, key string, value json.RawMessage) json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields), "decode only this case's independent synthetic payload")
	if value == nil {
		delete(fields, key)
	} else {
		fields[key] = value
	}
	result, err := json.Marshal(fields)
	require.NoError(t, err, "preserve raw field values while constructing a negative routing fixture")
	return result
}

func TestFreezeCodeReviewReviewerDispatchWakePreservesOriginal(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, optional string }{
		{name: "omitted optional fields"},
		{name: "explicit null fields", optional: `, "previous_review_body":null, "existing_github_review_id":null, "request_context":null`},
		{name: "present pointers and full nested fields", optional: `, "previous_review_body":"Synthetic original retained body", "existing_github_review_id":9223372036854775807, "previous_review_decided_at":"2026-01-02T03:04:05.123456Z", "request_context":{"source":"synthetic","body":"retain original","future_context":{"value":9007199254740993}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			job, original := codeReviewDispatchWakeFixture(tt.optional)
			// These mutable non-routing typed values must never replace the original.
			current := "Synthetic current value that was not in the retained payload"
			job.PreviousReviewBody = &current
			job.PullRequestAuthorTeams = []string{"synthetic-current-team"}
			actual, err := freezeCodeReviewReviewerDispatchWake(job, original)
			require.NoError(t, err, "exact independent routing identity should freeze its complete original payload")
			expected := `{"job_type":"run_code_review","queue":"agent","dedupe_key":"code_review:synthetic-original-output","payload":` + string(original) + `}`
			require.Equal(t, expected, string(actual), "the full fixed envelope must contain the exact original bytes, including optional and unknown fields")
			var envelope struct {
				JobType   string          `json:"job_type"`
				Queue     string          `json:"queue"`
				DedupeKey string          `json:"dedupe_key"`
				Payload   json.RawMessage `json:"payload"`
			}
			require.NoError(t, json.Unmarshal(actual, &envelope), "decode only the finite envelope while retaining its raw payload")
			require.Equal(t, []string{"run_code_review", "agent", "code_review:synthetic-original-output"}, []string{envelope.JobType, envelope.Queue, envelope.DedupeKey}, "only the existing controller type queue and original dedupe namespace may be installed")
			require.Equal(t, string(original), string(envelope.Payload), "payload extraction must retain the original interior whitespace and complete raw numeric lexemes")
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(envelope.Payload, &fields), "inspect synthetic raw values without float64 reconstruction")
			var future map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(fields["future"], &future), "read unknown future fields as raw values")
			require.Equal(t, "9223372036854775807", string(future["max_int64"]), "the maximal int64 lexeme must survive without floating point rounding")
			require.Equal(t, "9007199254740993", string(future["precise"]), "an integer immediately beyond float64 precision must survive exactly")
			require.Equal(t, "1e+09", string(future["exponent"]), "unknown numeric spelling must not be normalized through a numeric reconstruction")
			var exact int64
			require.NoError(t, json.Unmarshal(future["max_int64"], &exact), "decode the retained maximal integer into its exact integral type")
			require.Equal(t, int64(9223372036854775807), exact, "exact integer preservation requires more than a floating point JSON comparison")
			before := bytes.Clone(actual)
			sourceBefore := bytes.Clone(original)
			original[0] = '['
			require.True(t, bytes.Equal(before, actual), "mutating the input buffer after freeze must not mutate the returned envelope")
			actual[0] = '['
			require.True(t, bytes.Equal(sourceBefore[1:], original[1:]), "mutating the returned envelope must not modify any input payload bytes")
			repeated, repeatedErr := freezeCodeReviewReviewerDispatchWake(job, sourceBefore)
			require.NoError(t, repeatedErr, "a separate freeze must use only its independently supplied original bytes")
			require.True(t, bytes.Equal(before, repeated), "separate results must not share mutable output storage")
		})
	}
}

func TestFreezeCodeReviewReviewerDispatchWakeScopeRefusals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		edit func(*runCodeReviewPayload)
	}{
		{name: "organization", edit: func(j *runCodeReviewPayload) { j.OrgID = uuid.New() }},
		{name: "session", edit: func(j *runCodeReviewPayload) { j.SessionID = uuid.New() }},
		{name: "metadata", edit: func(j *runCodeReviewPayload) { j.MetadataID = uuid.New() }},
		{name: "repository", edit: func(j *runCodeReviewPayload) { j.RepositoryID = uuid.New() }},
		{name: "pull request", edit: func(j *runCodeReviewPayload) { j.PullRequestID = uuid.New() }},
		{name: "policy", edit: func(j *runCodeReviewPayload) { j.PolicyID = uuid.New() }},
		{name: "policy version", edit: func(j *runCodeReviewPayload) { j.PolicyVersion++ }},
		{name: "head", edit: func(j *runCodeReviewPayload) { j.HeadSHA = "synthetic-other-head" }},
		{name: "output key", edit: func(j *runCodeReviewPayload) { j.OutputKey = "synthetic-other-output" }},
		{name: "missing organization", edit: func(j *runCodeReviewPayload) { j.OrgID = uuid.Nil }},
		{name: "missing session", edit: func(j *runCodeReviewPayload) { j.SessionID = uuid.Nil }},
		{name: "missing metadata", edit: func(j *runCodeReviewPayload) { j.MetadataID = uuid.Nil }},
		{name: "missing repository", edit: func(j *runCodeReviewPayload) { j.RepositoryID = uuid.Nil }},
		{name: "missing pull request", edit: func(j *runCodeReviewPayload) { j.PullRequestID = uuid.Nil }},
		{name: "missing policy", edit: func(j *runCodeReviewPayload) { j.PolicyID = uuid.Nil }},
		{name: "missing head", edit: func(j *runCodeReviewPayload) { j.HeadSHA = "" }},
		{name: "missing output key", edit: func(j *runCodeReviewPayload) { j.OutputKey = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			job, original := codeReviewDispatchWakeFixture("")
			tt.edit(&job)
			actual, err := freezeCodeReviewReviewerDispatchWake(job, original)
			require.ErrorIs(t, err, errCodeReviewReviewerDispatchWakeInvalid, "a mismatched or missing independent routing value must refuse before intent installation")
			require.EqualError(t, err, "invalid original reviewer dispatch wake payload", "scope refusal must not echo frozen payload or identifiers")
			require.Nil(t, actual, "a failed correlation must not return a replacement wake or fallback")
		})
	}
}

func TestFreezeCodeReviewReviewerDispatchWakeInvalidOriginal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		edit func(*testing.T, json.RawMessage) json.RawMessage
	}{
		{name: "missing original", edit: func(_ *testing.T, _ json.RawMessage) json.RawMessage { return nil }},
		{name: "empty original", edit: func(_ *testing.T, _ json.RawMessage) json.RawMessage { return json.RawMessage{} }},
		{name: "whitespace only", edit: func(_ *testing.T, _ json.RawMessage) json.RawMessage { return json.RawMessage("  \n") }},
		{name: "array", edit: func(_ *testing.T, _ json.RawMessage) json.RawMessage { return json.RawMessage(`[]`) }},
		{name: "null", edit: func(_ *testing.T, _ json.RawMessage) json.RawMessage { return json.RawMessage(`null`) }},
		{name: "malformed", edit: func(_ *testing.T, _ json.RawMessage) json.RawMessage { return json.RawMessage(`{"org_id":`) }},
		{name: "trailing object", edit: func(_ *testing.T, r json.RawMessage) json.RawMessage { return append(r, []byte(` {}`)...) }},
		{name: "trailing garbage", edit: func(_ *testing.T, r json.RawMessage) json.RawMessage { return append(r, 'x') }},
		{name: "invalid UTF8", edit: func(_ *testing.T, r json.RawMessage) json.RawMessage {
			return append(r[:len(r)-1], []byte{',', '"', 'x', '"', ':', '"', 255, '"', '}'}...)
		}},
		{name: "duplicate routing key", edit: func(_ *testing.T, r json.RawMessage) json.RawMessage {
			return append([]byte(`{"org_id":"11111111-1111-4111-8111-111111111111",`), r[1:]...)
		}},
		{name: "escaped duplicate routing key", edit: func(_ *testing.T, r json.RawMessage) json.RawMessage {
			return append([]byte(`{"\u006frg_id":"11111111-1111-4111-8111-111111111111",`), r[1:]...)
		}},
		{name: "case aliased routing key", edit: func(t *testing.T, r json.RawMessage) json.RawMessage {
			return codeReviewDispatchWakeEdit(t, r, "Org_ID", json.RawMessage(`"11111111-1111-4111-8111-111111111111"`))
		}},
		{name: "case aliased policy version", edit: func(t *testing.T, r json.RawMessage) json.RawMessage {
			return codeReviewDispatchWakeEdit(t, r, "POLICY_VERSION", json.RawMessage(`7`))
		}},
		{name: "duplicate nested unknown key", edit: func(t *testing.T, r json.RawMessage) json.RawMessage {
			return codeReviewDispatchWakeEdit(t, r, "future", json.RawMessage(`{"nested":[{"same":1,"same":2}]}`))
		}},
		{name: "null identity", edit: func(t *testing.T, r json.RawMessage) json.RawMessage {
			return codeReviewDispatchWakeEdit(t, r, "metadata_id", json.RawMessage(`null`))
		}},
		{name: "numeric identity", edit: func(t *testing.T, r json.RawMessage) json.RawMessage {
			return codeReviewDispatchWakeEdit(t, r, "org_id", json.RawMessage(`1`))
		}},
		{name: "noncanonical identity", edit: func(t *testing.T, r json.RawMessage) json.RawMessage {
			return codeReviewDispatchWakeEdit(t, r, "org_id", json.RawMessage(`"{11111111-1111-4111-8111-111111111111}"`))
		}},
		{name: "null policy version", edit: func(t *testing.T, r json.RawMessage) json.RawMessage {
			return codeReviewDispatchWakeEdit(t, r, "policy_version", json.RawMessage(`null`))
		}},
		{name: "string policy version", edit: func(t *testing.T, r json.RawMessage) json.RawMessage {
			return codeReviewDispatchWakeEdit(t, r, "policy_version", json.RawMessage(`"7"`))
		}},
		{name: "fractional policy version", edit: func(t *testing.T, r json.RawMessage) json.RawMessage {
			return codeReviewDispatchWakeEdit(t, r, "policy_version", json.RawMessage(`7.0`))
		}},
		{name: "overflowing policy version", edit: func(t *testing.T, r json.RawMessage) json.RawMessage {
			return codeReviewDispatchWakeEdit(t, r, "policy_version", json.RawMessage(`99999999999999999999999999`))
		}},
	}
	for _, key := range []string{"org_id", "session_id", "metadata_id", "repository_id", "pull_request_id", "policy_id", "policy_version", "head_sha", "review_output_key"} {
		tests = append(tests, struct {
			name string
			edit func(*testing.T, json.RawMessage) json.RawMessage
		}{name: "missing " + key, edit: func(t *testing.T, r json.RawMessage) json.RawMessage {
			return codeReviewDispatchWakeEdit(t, r, key, nil)
		}})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			job, original := codeReviewDispatchWakeFixture("")
			actual, err := freezeCodeReviewReviewerDispatchWake(job, tt.edit(t, original))
			require.ErrorIs(t, err, errCodeReviewReviewerDispatchWakeInvalid, "invalid ambiguous or missing original correlation must be a safe pre-intent refusal")
			require.EqualError(t, err, "invalid original reviewer dispatch wake payload", "validation errors must not include raw original content or parse errors")
			require.Nil(t, actual, "invalid original data must never produce a remarshal of the current typed job")
		})
	}
}

func codeReviewDispatchWakePad(t *testing.T, raw json.RawMessage, target int) json.RawMessage {
	t.Helper()
	const prefix = `,"synthetic_padding":"`
	const suffix = `"}`
	count := target - (len(raw) - 1 + len(prefix) + len(suffix))
	require.GreaterOrEqual(t, count, 0, "the synthetic boundary target must leave room for a complete JSON object")
	result := make(json.RawMessage, 0, target)
	result = append(result, raw[:len(raw)-1]...)
	result = append(result, prefix...)
	result = append(result, strings.Repeat("x", count)...)
	result = append(result, suffix...)
	return result
}

func TestFreezeCodeReviewReviewerDispatchWakeBounds(t *testing.T) {
	t.Parallel()
	const expectedPrefix = `{"job_type":"run_code_review","queue":"agent","dedupe_key":"code_review:synthetic-original-output","payload":`
	tests := []struct {
		name   string
		target int
		valid  bool
	}{
		{name: "exact returned envelope bound", target: codeReviewReviewerDispatchWakeMaxBytes - len(expectedPrefix) - 1, valid: true},
		{name: "raw fits but envelope exceeds bound", target: codeReviewReviewerDispatchWakeMaxBytes},
		{name: "raw exceeds bound", target: codeReviewReviewerDispatchWakeMaxBytes + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			job, raw := codeReviewDispatchWakeFixture("")
			original := codeReviewDispatchWakePad(t, raw, tt.target)
			require.Equal(t, tt.target, len(original), "the synthetic fixture must hit the requested raw-byte boundary exactly")
			actual, err := freezeCodeReviewReviewerDispatchWake(job, original)
			if !tt.valid {
				require.ErrorIs(t, err, errCodeReviewReviewerDispatchWakeTooLarge, "raw or envelope oversize must refuse before any intent without truncation")
				require.Nil(t, actual, "oversize must not return a shortened or reconstructed wake")
				return
			}
			require.NoError(t, err, "the exact finite raw envelope limit should remain usable")
			require.Equal(t, codeReviewReviewerDispatchWakeMaxBytes, len(actual), "accepted envelope must meet the exact inclusive raw bound")
			expected := append([]byte(expectedPrefix), original...)
			expected = append(expected, '}')
			require.True(t, bytes.Equal(expected, actual), "even at the limit the entire original payload must remain byte-exact without printing a large artifact")
			var compact bytes.Buffer
			require.NoError(t, json.Compact(&compact, actual), "the retained exact-bound envelope must also be valid canonical compact JSON")
			require.LessOrEqual(t, compact.Len(), codeReviewReviewerDispatchWakeMaxBytes, "raw bounding must also bound compact canonical JSON, not PostgreSQL JSONB presentation")
		})
	}
}
