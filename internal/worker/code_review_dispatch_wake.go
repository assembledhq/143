package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const codeReviewReviewerDispatchWakeMaxBytes = 8 << 20

var (
	errCodeReviewReviewerDispatchWakeInvalid  = errors.New("invalid original reviewer dispatch wake payload")
	errCodeReviewReviewerDispatchWakeTooLarge = errors.New("original reviewer dispatch wake exceeds encoded limit")
)

// freezeCodeReviewReviewerDispatchWake retains the ORIGINAL controller payload,
// not a remarshal of its current typed projection. Independent routing identity
// must match before any intent is installed. This pure correlation operation
// grants no job-lease, admission, send, owner, settlement or retry authority.
// Missing/invalid original bytes are a pre-intent refusal, never permission to
// regenerate payload, render current inputs, or select a legacy fallback.
func freezeCodeReviewReviewerDispatchWake(job runCodeReviewPayload, original json.RawMessage) (json.RawMessage, error) {
	if len(original) > codeReviewReviewerDispatchWakeMaxBytes {
		return nil, errCodeReviewReviewerDispatchWakeTooLarge
	}
	if job.OrgID == uuid.Nil || job.SessionID == uuid.Nil || job.MetadataID == uuid.Nil ||
		job.RepositoryID == uuid.Nil || job.PullRequestID == uuid.Nil || job.PolicyID == uuid.Nil ||
		job.HeadSHA == "" || job.OutputKey == "" || !utf8.ValidString(job.HeadSHA) || !utf8.ValidString(job.OutputKey) ||
		strings.ContainsRune(job.HeadSHA, 0) || strings.ContainsRune(job.OutputKey, 0) {
		return nil, errCodeReviewReviewerDispatchWakeInvalid
	}
	// Snapshot only bounded bytes. The returned envelope never aliases original.
	payload := bytes.Clone(original)
	if len(payload) == 0 || !utf8.Valid(payload) || !json.Valid(payload) {
		return nil, errCodeReviewReviewerDispatchWakeInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if !codeReviewDispatchWakeUniqueJSON(decoder) {
		return nil, errCodeReviewReviewerDispatchWakeInvalid
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errCodeReviewReviewerDispatchWakeInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(payload, &fields) != nil || fields == nil {
		return nil, errCodeReviewReviewerDispatchWakeInvalid
	}
	expected := []struct{ key, value string }{
		{"org_id", job.OrgID.String()}, {"session_id", job.SessionID.String()},
		{"metadata_id", job.MetadataID.String()}, {"repository_id", job.RepositoryID.String()},
		{"pull_request_id", job.PullRequestID.String()}, {"policy_id", job.PolicyID.String()},
		{"head_sha", job.HeadSHA}, {"review_output_key", job.OutputKey},
	}
	for key := range fields {
		if key != "policy_version" && strings.EqualFold(key, "policy_version") {
			return nil, errCodeReviewReviewerDispatchWakeInvalid
		}
		for _, field := range expected {
			if key != field.key && strings.EqualFold(key, field.key) {
				return nil, errCodeReviewReviewerDispatchWakeInvalid
			}
		}
	}
	for _, field := range expected {
		var actual string
		if json.Unmarshal(fields[field.key], &actual) != nil || actual != field.value {
			return nil, errCodeReviewReviewerDispatchWakeInvalid
		}
	}
	var policyVersion *int
	if json.Unmarshal(fields["policy_version"], &policyVersion) != nil || policyVersion == nil || *policyVersion != job.PolicyVersion {
		return nil, errCodeReviewReviewerDispatchWakeInvalid
	}
	const prefix = `{"job_type":"run_code_review","queue":"agent","dedupe_key":`
	const payloadPrefix = `,"payload":`
	// This lower bound refuses oversized keys before escaping allocates them.
	if len(prefix)+len("code_review:")+len(job.OutputKey)+2+len(payloadPrefix)+len(payload)+1 > codeReviewReviewerDispatchWakeMaxBytes {
		return nil, errCodeReviewReviewerDispatchWakeTooLarge
	}
	key, err := json.Marshal("code_review:" + job.OutputKey)
	if err != nil {
		return nil, errCodeReviewReviewerDispatchWakeInvalid
	}
	size := len(prefix) + len(key) + len(payloadPrefix) + len(payload) + 1
	if size > codeReviewReviewerDispatchWakeMaxBytes {
		return nil, errCodeReviewReviewerDispatchWakeTooLarge
	}
	wake := make(json.RawMessage, 0, size)
	wake = append(wake, prefix...)
	wake = append(wake, key...)
	wake = append(wake, payloadPrefix...)
	wake = append(wake, payload...)
	wake = append(wake, '}')
	// Embedding raw bytes preserves all numeric lexemes and optional fields.
	// Both raw payload and returned envelope fit; compact canonical JSON cannot
	// exceed this raw envelope. PostgreSQL JSONB presentation is checked later,
	// atomically by PrepareCodeReviewDispatchInTx, not inferred or measured here.
	return wake, nil
}

// Decode tokens with UseNumber and reject duplicate decoded object keys at
// every level. No numbers are converted to float64, and tokens are never used
// to reconstruct the retained payload. Root routing aliases are checked above.
func codeReviewDispatchWakeUniqueJSON(decoder *json.Decoder) bool {
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delim, composite := token.(json.Delim)
	if !composite {
		return true
	}
	if delim != '{' && delim != '[' {
		return false
	}
	seen := make(map[string]bool)
	for decoder.More() {
		if delim == '{' {
			token, err := decoder.Token()
			if err != nil {
				return false
			}
			key, ok := token.(string)
			if !ok || seen[key] {
				return false
			}
			seen[key] = true
		}
		if !codeReviewDispatchWakeUniqueJSON(decoder) {
			return false
		}
	}
	_, err = decoder.Token()
	return err == nil
}
