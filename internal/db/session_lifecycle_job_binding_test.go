package db

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSameLifecycleJobBinding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*LifecycleJobBinding)
		want   bool
	}{
		{name: "same complete input set", want: true},
		{name: "input order is not identity", change: func(b *LifecycleJobBinding) { b.dispatchIDs[0], b.dispatchIDs[1] = b.dispatchIDs[1], b.dispatchIDs[0] }, want: true},
		{name: "other tenant", change: func(b *LifecycleJobBinding) { b.orgID = uuid.New() }},
		{name: "other job", change: func(b *LifecycleJobBinding) { b.jobID = uuid.New() }},
		{name: "other session", change: func(b *LifecycleJobBinding) { b.fence.sessionID = uuid.New() }},
		{name: "other lane", change: func(b *LifecycleJobBinding) { b.fence.threadID = uuid.New() }},
		{name: "other session epoch", change: func(b *LifecycleJobBinding) { b.fence.sessionEpoch++ }},
		{name: "other lane epoch", change: func(b *LifecycleJobBinding) { b.fence.threadEpoch++ }},
		{name: "other input", change: func(b *LifecycleJobBinding) { b.dispatchIDs[0] = uuid.New() }},
		{name: "missing input", change: func(b *LifecycleJobBinding) { b.dispatchIDs = b.dispatchIDs[:1] }},
		{name: "duplicate input", change: func(b *LifecycleJobBinding) { b.dispatchIDs[1] = b.dispatchIDs[0] }},
		{name: "invalid zero job", change: func(b *LifecycleJobBinding) { b.jobID = uuid.Nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			left := lifecycleJobPayloadTestBinding(t, models.LifecycleDispatchMessage, true)
			right := left
			right.dispatchIDs = left.DispatchIDs()
			if tt.change != nil {
				tt.change(&right)
			}
			beforeLeft, beforeRight := left.DispatchIDs(), right.DispatchIDs()
			require.Equal(t, tt.want, sameLifecycleJobBinding(left, right), "only complete validated immutable identity and the same exact input set may compare equal")
			require.Equal(t, beforeLeft, left.DispatchIDs(), "comparison must not sort the caller's left input buffer")
			require.Equal(t, beforeRight, right.DispatchIDs(), "comparison must not sort the caller's right input buffer")
		})
	}
}

func TestLifecycleJobBindingPayloadRoundTrip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		kind models.LifecycleDispatchKind
	}{
		{name: "ordinary batch", kind: models.LifecycleDispatchMessage},
		{name: "exact answer", kind: models.LifecycleDispatchAnswer},
		{name: "exact denial", kind: models.LifecycleDispatchDenial},
		{name: "initial input", kind: models.LifecycleDispatchInitial},
		{name: "retry input", kind: models.LifecycleDispatchRetry},
		{name: "recovery input", kind: models.LifecycleDispatchRecovery},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, scope := range []struct {
				name     string
				threaded bool
			}{{name: "lane", threaded: true}, {name: "session-wide"}} {
				t.Run(scope.name, func(t *testing.T) {
					t.Parallel()
					binding := lifecycleJobPayloadTestBinding(t, tt.kind, scope.threaded)
					originalIDs := binding.DispatchIDs()
					base := json.RawMessage(`{"domain":{"large_id":900719925474099312345,"action":"keep"},"target":"original"}`)
					payload, err := encodeLifecycleJobBindingPayload(base, binding)
					require.NoError(t, err, "a new exact input set must encode without replacing domain identities")
					var actual map[string]json.RawMessage
					require.NoError(t, json.Unmarshal(payload, &actual), "read every persisted field without converting large numeric identities to float")
					want := map[string]json.RawMessage{
						"org_id": json.RawMessage(`"11111111-1111-4111-8111-111111111111"`), "session_id": json.RawMessage(`"22222222-2222-4222-8222-222222222222"`),
						"lifecycle_binding_version": json.RawMessage(`1`), "admission_session_epoch": json.RawMessage(`7`),
						"input_dispatch_ids":      json.RawMessage(`["77777777-7777-4777-8777-777777777777"]`),
						"lifecycle_dispatch_kind": json.RawMessage(fmt.Sprintf(`%q`, tt.kind)),
						"domain":                  json.RawMessage(`{"large_id":900719925474099312345,"action":"keep"}`), "target": json.RawMessage(`"original"`),
					}
					if tt.kind == models.LifecycleDispatchMessage {
						want["input_dispatch_ids"] = json.RawMessage(`["77777777-7777-4777-8777-777777777777","88888888-8888-4888-8888-888888888888"]`)
					}
					if scope.threaded {
						want["thread_id"], want["admission_thread_epoch"] = json.RawMessage(`"44444444-4444-4444-8444-444444444444"`), json.RawMessage(`11`)
					}
					if tt.kind == models.LifecycleDispatchAnswer || tt.kind == models.LifecycleDispatchDenial {
						want["lifecycle_request_id"] = json.RawMessage(`"55555555-5555-4555-8555-555555555555"`)
					}
					require.Equal(t, want, actual, "the complete envelope must preserve domain values and only the exact explicit binding")
					require.Equal(t, originalIDs, binding.DispatchIDs(), "canonical encoding must not reorder a caller-owned binding")
					decoded, err := decodeLifecycleJobBindingPayload(binding.OrgID(), binding.JobID(), payload)
					require.NoError(t, err, "the actual job row must recover the supported immutable envelope")
					expected := binding
					expected.dispatchIDs = []uuid.UUID{uuid.MustParse("77777777-7777-4777-8777-777777777777")}
					if tt.kind == models.LifecycleDispatchMessage {
						expected.dispatchIDs = append(expected.dispatchIDs, uuid.MustParse("88888888-8888-4888-8888-888888888888"))
					}
					require.Equal(t, expected, decoded, "round trip must reconstruct every field and canonical set, not just matching IDs")
					inputCopy := decoded.DispatchIDs()
					inputCopy[0] = uuid.New()
					require.Equal(t, expected, decoded, "returned input slices must not mutate the retained decoded binding")
				})
			}
		})
	}
}

func TestLifecycleJobBindingPayloadRejectsInvalidDecode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		edit func(map[string]json.RawMessage)
		raw  func(json.RawMessage) json.RawMessage
	}{
		{name: "unsupported version", edit: func(v map[string]json.RawMessage) { v["lifecycle_binding_version"] = json.RawMessage(`2`) }},
		{name: "fractional version", edit: func(v map[string]json.RawMessage) { v["lifecycle_binding_version"] = json.RawMessage(`1.0`) }},
		{name: "missing version", edit: func(v map[string]json.RawMessage) { delete(v, "lifecycle_binding_version") }},
		{name: "foreign tenant", edit: func(v map[string]json.RawMessage) {
			v["org_id"] = json.RawMessage(`"99999999-9999-4999-8999-999999999999"`)
		}},
		{name: "missing tenant", edit: func(v map[string]json.RawMessage) { delete(v, "org_id") }},
		{name: "null session", edit: func(v map[string]json.RawMessage) { v["session_id"] = json.RawMessage(`null`) }},
		{name: "zero session", edit: func(v map[string]json.RawMessage) {
			v["session_id"] = json.RawMessage(`"00000000-0000-0000-0000-000000000000"`)
		}},
		{name: "negative epoch", edit: func(v map[string]json.RawMessage) { v["admission_session_epoch"] = json.RawMessage(`-1`) }},
		{name: "missing epoch", edit: func(v map[string]json.RawMessage) { delete(v, "admission_session_epoch") }},
		{name: "null lane", edit: func(v map[string]json.RawMessage) { v["thread_id"] = json.RawMessage(`null`) }},
		{name: "missing lane epoch", edit: func(v map[string]json.RawMessage) { delete(v, "admission_thread_epoch") }},
		{name: "orphan lane epoch", edit: func(v map[string]json.RawMessage) { delete(v, "thread_id") }},
		{name: "missing input set", edit: func(v map[string]json.RawMessage) { delete(v, "input_dispatch_ids") }},
		{name: "null input set", edit: func(v map[string]json.RawMessage) { v["input_dispatch_ids"] = json.RawMessage(`null`) }},
		{name: "empty input set", edit: func(v map[string]json.RawMessage) { v["input_dispatch_ids"] = json.RawMessage(`[]`) }},
		{name: "duplicate input", edit: func(v map[string]json.RawMessage) {
			v["input_dispatch_ids"] = json.RawMessage(`["77777777-7777-4777-8777-777777777777","77777777-7777-4777-8777-777777777777"]`)
		}},
		{name: "zero input", edit: func(v map[string]json.RawMessage) {
			v["input_dispatch_ids"] = json.RawMessage(`["00000000-0000-0000-0000-000000000000"]`)
		}},
		{name: "unsupported kind", edit: func(v map[string]json.RawMessage) { v["lifecycle_dispatch_kind"] = json.RawMessage(`"future"`) }},
		{name: "answer lacks request", edit: func(v map[string]json.RawMessage) { v["lifecycle_dispatch_kind"] = json.RawMessage(`"answer"`) }},
		{name: "ordinary input cannot gain a request", edit: func(v map[string]json.RawMessage) {
			v["lifecycle_request_id"] = json.RawMessage(`"55555555-5555-4555-8555-555555555555"`)
		}},
		{name: "duplicate protected key", raw: func(v json.RawMessage) json.RawMessage {
			return append([]byte(`{"session_id":"22222222-2222-4222-8222-222222222222",`), v[1:]...)
		}},
		{name: "folded alias", raw: func(v json.RawMessage) json.RawMessage {
			return append([]byte(`{"SESSION_ID":"22222222-2222-4222-8222-222222222222",`), v[1:]...)
		}},
		{name: "unicode folded alias", raw: func(v json.RawMessage) json.RawMessage {
			return append([]byte(`{"ſession_id":"22222222-2222-4222-8222-222222222222",`), v[1:]...)
		}},
		{name: "nested duplicate domain key", raw: func(v json.RawMessage) json.RawMessage { return append([]byte(`{"domain":{"id":1,"id":2},`), v[1:]...) }},
		{name: "trailing value", raw: func(v json.RawMessage) json.RawMessage { return append(v, []byte(` {}`)...) }},
		{name: "nonobject", raw: func(json.RawMessage) json.RawMessage { return json.RawMessage(`[]`) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			binding := lifecycleJobPayloadTestBinding(t, models.LifecycleDispatchMessage, true)
			payload, err := encodeLifecycleJobBindingPayload(nil, binding)
			require.NoError(t, err, "construct an independent valid baseline envelope for the selected corruption")
			if tt.edit != nil {
				var fields map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(payload, &fields), "decode fixture fields without changing production parser behavior")
				tt.edit(fields)
				payload, err = json.Marshal(fields)
				require.NoError(t, err, "persist the selected synthetic invalid field")
			} else {
				payload = tt.raw(payload)
			}
			actual, err := decodeLifecycleJobBindingPayload(binding.OrgID(), binding.JobID(), payload)
			require.Error(t, err, "malformed or unsupported input cannot become a reusable job binding")
			require.Equal(t, LifecycleJobBinding{}, actual, "every failed parse must expose no partial authority-shaped value")
		})
	}
}

func TestLifecycleJobBindingPayloadRejectsRebinding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		base json.RawMessage
	}{
		{name: "existing version", base: json.RawMessage(`{"lifecycle_binding_version":1}`)},
		{name: "existing epoch", base: json.RawMessage(`{"admission_session_epoch":7}`)},
		{name: "existing lane epoch", base: json.RawMessage(`{"admission_thread_epoch":11}`)},
		{name: "existing input set", base: json.RawMessage(`{"input_dispatch_ids":[]}`)},
		{name: "existing kind", base: json.RawMessage(`{"lifecycle_dispatch_kind":"message"}`)},
		{name: "existing request", base: json.RawMessage(`{"lifecycle_request_id":null}`)},
		{name: "mismatched session", base: json.RawMessage(`{"session_id":"99999999-9999-4999-8999-999999999999"}`)},
		{name: "null route", base: json.RawMessage(`{"thread_id":null}`)},
		{name: "folded binding key", base: json.RawMessage(`{"LIFECYCLE_BINDING_VERSION":1}`)},
		{name: "nested duplicate", base: json.RawMessage(`{"domain":{"id":1,"id":2}}`)},
		{name: "nonobject", base: json.RawMessage(`true`)},
		{name: "null object", base: json.RawMessage(`null`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			binding := lifecycleJobPayloadTestBinding(t, models.LifecycleDispatchMessage, true)
			before := append(json.RawMessage(nil), tt.base...)
			actual, err := encodeLifecycleJobBindingPayload(tt.base, binding)
			require.Error(t, err, "the new-job codec cannot rewrite an existing binding or adopt conflicting routing")
			require.Nil(t, actual, "an invalid base must return no partially encoded envelope")
			require.Equal(t, before, tt.base, "refusal cannot mutate the caller's original domain payload")
		})
	}
}

func lifecycleJobPayloadTestBinding(t *testing.T, kind models.LifecycleDispatchKind, threaded bool) LifecycleJobBinding {
	t.Helper()
	var lane *uuid.UUID
	var epoch *int64
	if threaded {
		id, value := uuid.MustParse("44444444-4444-4444-8444-444444444444"), int64(11)
		lane, epoch = &id, &value
	}
	fence, err := NewAdmissionFence(uuid.MustParse("22222222-2222-4222-8222-222222222222"), lane, 7, epoch)
	require.NoError(t, err, "construct the explicit expected fixture scope without current-row inference")
	ids := []uuid.UUID{uuid.MustParse("77777777-7777-4777-8777-777777777777")}
	if kind == models.LifecycleDispatchMessage {
		ids = append([]uuid.UUID{uuid.MustParse("88888888-8888-4888-8888-888888888888")}, ids...)
	}
	var request *uuid.UUID
	if kind == models.LifecycleDispatchAnswer || kind == models.LifecycleDispatchDenial {
		id := uuid.MustParse("55555555-5555-4555-8555-555555555555")
		request = &id
	}
	binding, err := NewLifecycleJobBinding(uuid.MustParse("11111111-1111-4111-8111-111111111111"), fence, uuid.MustParse("33333333-3333-4333-8333-333333333333"), ids, kind, request)
	require.NoError(t, err, "construct the exact immutable job/input fixture")
	return binding
}
