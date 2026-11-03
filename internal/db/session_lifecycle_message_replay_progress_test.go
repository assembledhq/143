package db

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func messageAdmissionReplayProgressFixture() map[string]any {
	return map[string]any{
		"continuation_runtime_id":       "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		"continuation_message_id":       "42",
		"continuation_task_message_ids": []int64{7, 42},
		"execution_purpose":             "checkpoint_recovery",
		"recovery_runtime_id":           "ffffffff-1111-4222-8333-444444444444",
		"recovery_message_ids":          []int64{7, 42, 53},
	}
}

func TestMessageAdmissionReplayProgressShape(t *testing.T) {
	t.Parallel()
	type progressCase struct {
		name   string
		change func(map[string]any)
		valid  bool
	}
	tests := []progressCase{
		{name: "complete continuation and recovery progress", valid: true},
		{name: "all progress absent", change: func(fields map[string]any) { clear(fields) }, valid: true},
		{name: "continuation without recovery", change: func(fields map[string]any) {
			delete(fields, "execution_purpose")
			delete(fields, "recovery_runtime_id")
			delete(fields, "recovery_message_ids")
		}, valid: true},
		{name: "recovery without continuation recording", change: func(fields map[string]any) {
			delete(fields, "continuation_runtime_id")
			delete(fields, "continuation_message_id")
			delete(fields, "continuation_task_message_ids")
		}, valid: true},
		{name: "task set may contain duplicate original identities", change: func(fields map[string]any) {
			fields["continuation_task_message_ids"] = []int64{42, 42}
		}, valid: true},
		{name: "task set may contain largest positive int64", change: func(fields map[string]any) {
			fields["recovery_message_ids"] = json.RawMessage(`[42,9223372036854775807]`)
		}, valid: true},
		{name: "continuation message cannot change original queued identity", change: func(fields map[string]any) { fields["continuation_message_id"] = "43" }},
		{name: "continuation message is an exact canonical string", change: func(fields map[string]any) { fields["continuation_message_id"] = "042" }},
		{name: "continuation message cannot become a JSON number", change: func(fields map[string]any) { fields["continuation_message_id"] = 42 }},
		{name: "retry purpose is not recovery progress", change: func(fields map[string]any) { fields["execution_purpose"] = "checkpoint_retry" }},
		{name: "continue purpose is not recovery progress", change: func(fields map[string]any) { fields["execution_purpose"] = "continue" }},
		{name: "unknown purpose is not recovery progress", change: func(fields map[string]any) { fields["execution_purpose"] = "other" }},
		{name: "empty purpose is not recovery progress", change: func(fields map[string]any) { fields["execution_purpose"] = "" }},
		{name: "purpose cannot become a JSON object", change: func(fields map[string]any) { fields["execution_purpose"] = map[string]any{} }},
	}
	for _, key := range []string{
		"continuation_runtime_id", "continuation_message_id", "continuation_task_message_ids",
		"execution_purpose", "recovery_runtime_id", "recovery_message_ids",
	} {
		tests = append(tests,
			progressCase{name: key + " cannot be missing from a partial triple", change: func(fields map[string]any) { delete(fields, key) }},
			progressCase{name: key + " cannot be JSON null", change: func(fields map[string]any) { fields[key] = nil }},
		)
	}
	for _, key := range []string{"continuation_runtime_id", "recovery_runtime_id"} {
		for _, value := range []struct {
			name  string
			value any
		}{
			{name: "zero UUID", value: "00000000-0000-0000-0000-000000000000"},
			{name: "uppercase UUID", value: "AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE"},
			{name: "UUID without canonical hyphens", value: "aaaaaaaabbbb4ccc8dddeeeeeeeeeeee"},
			{name: "URN UUID", value: "urn:uuid:aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"},
			{name: "malformed UUID", value: "not-a-runtime"},
			{name: "empty UUID", value: ""},
			{name: "non-string UUID", value: 42},
		} {
			tests = append(tests, progressCase{name: key + " rejects " + value.name, change: func(fields map[string]any) { fields[key] = value.value }})
		}
	}
	for _, key := range []string{"continuation_task_message_ids", "recovery_message_ids"} {
		for _, value := range []struct {
			name  string
			value any
		}{
			{name: "empty array", value: []int64{}},
			{name: "missing original input", value: []int64{7, 53}},
			{name: "zero identity", value: []int64{0, 42}},
			{name: "negative identity", value: []int64{-1, 42}},
			{name: "numeric string", value: []string{"42"}},
			{name: "fractional identity", value: json.RawMessage(`[42,1.5]`)},
			{name: "non-integer number representation", value: json.RawMessage(`[42.0]`)},
			{name: "overflowing identity", value: json.RawMessage(`[42,9223372036854775808]`)},
			{name: "null array member", value: json.RawMessage(`[42,null]`)},
			{name: "object array member", value: json.RawMessage(`[42,{}]`)},
			{name: "scalar identity", value: 42},
			{name: "object instead of array", value: map[string]any{"message_id": 42}},
		} {
			tests = append(tests, progressCase{name: key + " rejects " + value.name, change: func(fields map[string]any) { fields[key] = value.value }})
		}
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fields := messageAdmissionReplayProgressFixture()
			if tt.change != nil {
				tt.change(fields)
			}
			payload, err := json.Marshal(fields)
			require.NoError(t, err, "construct an isolated current-progress JSON fixture without changing original receipt data")
			require.Equal(t, tt.valid, validMessageAdmissionReplayProgress(payload, 42), "only complete actual-writer-shaped progress may coexist with an exact historical admission")
		})
	}
}

func TestMessageAdmissionReplayProgressInputBound(t *testing.T) {
	t.Parallel()
	limit := defaultLifecycleConsumerLimits().payloadBytes
	tests := []struct {
		name      string
		payload   []byte
		messageID int64
		valid     bool
	}{
		{name: "no returned payload is incomplete", messageID: 42},
		{name: "JSON null is not a current job object", payload: []byte(`null`), messageID: 42},
		{name: "JSON array is not a current job object", payload: []byte(`[]`), messageID: 42},
		{name: "malformed JSON is incomplete", payload: []byte(`{`), messageID: 42},
		{name: "zero original instruction refuses", payload: []byte(`{}`)},
		{name: "negative original instruction refuses", payload: []byte(`{}`), messageID: -1},
		{name: "exact existing byte limit remains allowed", payload: append([]byte(`{}`), bytes.Repeat([]byte(" "), limit-2)...), messageID: 42, valid: true},
		{name: "existing byte limit overflow refuses", payload: append([]byte(`{}`), bytes.Repeat([]byte(" "), limit-1)...), messageID: 42},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.valid, validMessageAdmissionReplayProgress(tt.payload, tt.messageID), "bounded replay progress validation must never turn absent oversized or malformed job data into clear receipt identity")
		})
	}
}
