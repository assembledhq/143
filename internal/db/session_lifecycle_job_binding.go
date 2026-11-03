package db

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

var lifecycleJobBindingKeys = [...]string{
	"org_id", "session_id", "thread_id", "lifecycle_binding_version",
	"admission_session_epoch", "admission_thread_epoch", "input_dispatch_ids",
	"lifecycle_dispatch_kind", "lifecycle_request_id",
}

// sameLifecycleJobBinding compares complete immutable identities, including the
// exact input set. It validates shape only; callers must still read and verify
// durable consumer rows under the canonical transaction's locks.
func sameLifecycleJobBinding(left, right LifecycleJobBinding) bool {
	if left.Validate() != nil || right.Validate() != nil || left.orgID != right.orgID ||
		left.jobID != right.jobID || left.fence != right.fence || left.kind != right.kind ||
		left.requestID != right.requestID || len(left.dispatchIDs) != len(right.dispatchIDs) {
		return false
	}
	leftIDs, rightIDs := left.DispatchIDs(), right.DispatchIDs()
	compare := func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) }
	slices.SortFunc(leftIDs, compare)
	slices.SortFunc(rightIDs, compare)
	return slices.Equal(leftIDs, rightIDs)
}

// encodeLifecycleJobBindingPayload constructs only a NEW job's immutable input
// envelope. It cannot add inputs to a previously versioned payload. Preserving
// domain fields is not proof of their eligibility or of durable input ownership.
func encodeLifecycleJobBindingPayload(base json.RawMessage, binding LifecycleJobBinding) (json.RawMessage, error) {
	if err := binding.Validate(); err != nil {
		return nil, err
	}
	if len(base) == 0 {
		base = json.RawMessage(`{}`)
	}
	fields, err := lifecycleJobPayloadObject(base)
	if err != nil {
		return nil, err
	}
	for key := range fields {
		if slices.Contains(lifecycleJobBindingKeys[3:], key) {
			return nil, errors.New("cannot replace an existing lifecycle job binding")
		}
	}
	routes := map[string]uuid.UUID{"org_id": binding.orgID, "session_id": binding.fence.sessionID}
	if binding.fence.threaded {
		routes["thread_id"] = binding.fence.threadID
	}
	for _, key := range lifecycleJobBindingKeys[:3] {
		if raw, exists := fields[key]; exists {
			var value uuid.UUID
			if err := decodeLifecycleJobRequired(raw, &value); err != nil || value == uuid.Nil || routes[key] != value {
				return nil, fmt.Errorf("lifecycle job route does not match explicit binding: %s", key)
			}
		}
	}
	ids := binding.DispatchIDs()
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	values := map[string]any{
		"org_id": binding.orgID, "session_id": binding.fence.sessionID,
		"lifecycle_binding_version": 1, "admission_session_epoch": binding.fence.sessionEpoch,
		"input_dispatch_ids": ids, "lifecycle_dispatch_kind": binding.kind,
	}
	if binding.fence.threaded {
		values["thread_id"], values["admission_thread_epoch"] = binding.fence.threadID, binding.fence.threadEpoch
	}
	if binding.requestID != uuid.Nil {
		values["lifecycle_request_id"] = binding.requestID
	}
	for key, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encode lifecycle job field %s: %w", key, err)
		}
		fields[key] = raw
	}
	payload, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("encode lifecycle job envelope: %w", err)
	}
	return payload, nil
}

// decodeLifecycleJobBindingPayload checks shape against the actual job row's
// tenant and identity. It is not a canonical consumer lookup or execution permit.
func decodeLifecycleJobBindingPayload(orgID, jobID uuid.UUID, payload json.RawMessage) (LifecycleJobBinding, error) {
	fields, err := lifecycleJobPayloadObject(payload)
	if err != nil {
		return LifecycleJobBinding{}, err
	}
	var version int
	var storedOrg, sessionID uuid.UUID
	var sessionEpoch int64
	var ids []uuid.UUID
	var kind models.LifecycleDispatchKind
	for _, field := range []struct {
		key   string
		value any
	}{
		{"lifecycle_binding_version", &version}, {"org_id", &storedOrg}, {"session_id", &sessionID},
		{"admission_session_epoch", &sessionEpoch}, {"input_dispatch_ids", &ids}, {"lifecycle_dispatch_kind", &kind},
	} {
		if err := decodeLifecycleJobRequired(fields[field.key], field.value); err != nil {
			return LifecycleJobBinding{}, fmt.Errorf("invalid lifecycle job field %s: %w", field.key, err)
		}
	}
	if version != 1 || storedOrg != orgID {
		return LifecycleJobBinding{}, errors.New("unsupported or foreign lifecycle job binding")
	}
	var threadID *uuid.UUID
	var threadEpoch *int64
	if _, exists := fields["thread_id"]; exists {
		threadID = new(uuid.UUID)
		if err := decodeLifecycleJobRequired(fields["thread_id"], threadID); err != nil {
			return LifecycleJobBinding{}, err
		}
	}
	if _, exists := fields["admission_thread_epoch"]; exists {
		threadEpoch = new(int64)
		if err := decodeLifecycleJobRequired(fields["admission_thread_epoch"], threadEpoch); err != nil {
			return LifecycleJobBinding{}, err
		}
	}
	var requestID *uuid.UUID
	if _, exists := fields["lifecycle_request_id"]; exists {
		requestID = new(uuid.UUID)
		if err := decodeLifecycleJobRequired(fields["lifecycle_request_id"], requestID); err != nil {
			return LifecycleJobBinding{}, err
		}
	}
	fence, err := NewAdmissionFence(sessionID, threadID, sessionEpoch, threadEpoch)
	if err != nil {
		return LifecycleJobBinding{}, err
	}
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return NewLifecycleJobBinding(orgID, fence, jobID, ids, kind, requestID)
}

func decodeLifecycleJobRequired(raw json.RawMessage, target any) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("missing or null lifecycle job value")
	}
	return json.Unmarshal(raw, target)
}

func lifecycleJobPayloadObject(payload json.RawMessage) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := validateLifecycleJobJSONKeys(decoder); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("lifecycle job payload requires exactly one JSON value")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || fields == nil {
		return nil, errors.New("lifecycle job payload requires a JSON object")
	}
	for key := range fields {
		for _, protected := range lifecycleJobBindingKeys {
			if key != protected && strings.EqualFold(key, protected) {
				return nil, errors.New("lifecycle job payload contains an aliased protected key")
			}
		}
	}
	return fields, nil
}

// Keep duplicate domain keys from acquiring last-key-wins semantics when the
// new envelope is encoded. UseNumber preserves large numeric domain identities.
func validateLifecycleJobJSONKeys(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	if delim != '{' && delim != '[' {
		return errors.New("invalid lifecycle job JSON delimiter")
	}
	seen := make(map[string]bool)
	for decoder.More() {
		if delim == '{' {
			token, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok || seen[key] {
				return errors.New("lifecycle job payload contains a duplicate or invalid key")
			}
			seen[key] = true
		}
		if err := validateLifecycleJobJSONKeys(decoder); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}
