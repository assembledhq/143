package db

import (
	"bytes"
	"encoding/json"
	"strconv"

	"github.com/google/uuid"
)

var messageAdmissionReplayProgressKeys = [...]string{
	"continuation_runtime_id", "continuation_message_id", "continuation_task_message_ids",
	"execution_purpose", "recovery_runtime_id", "recovery_message_ids",
}

// This is a historical-identity shape check, not proof that any runtime exists,
// owns a lease or may execute/settle work. The caller separately compares the
// complete original payload remainder, so unknown keys and all A1-written keys
// remain exact. None of these current progress values enter the returned receipt.
func validMessageAdmissionReplayProgress(payload []byte, messageID int64) bool {
	if messageID <= 0 || len(payload) == 0 || len(payload) > defaultLifecycleConsumerLimits().payloadBytes {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(payload, &fields) != nil || fields == nil {
		return false
	}
	runtime, hasRuntime := fields["continuation_runtime_id"]
	message, hasMessage := fields["continuation_message_id"]
	tasks, hasTasks := fields["continuation_task_message_ids"]
	if hasRuntime || hasMessage || hasTasks {
		var original string
		if !hasRuntime || !hasMessage || !hasTasks ||
			!validMessageAdmissionProgressRuntime(runtime) ||
			json.Unmarshal(message, &original) != nil || original != strconv.FormatInt(messageID, 10) ||
			!validMessageAdmissionProgressMessages(tasks, messageID) {
			return false
		}
	}
	purpose, hasPurpose := fields["execution_purpose"]
	recovery, hasRecovery := fields["recovery_runtime_id"]
	inputs, hasInputs := fields["recovery_message_ids"]
	if hasPurpose || hasRecovery || hasInputs {
		var value string
		if !hasPurpose || !hasRecovery || !hasInputs ||
			json.Unmarshal(purpose, &value) != nil || value != "checkpoint_recovery" ||
			!validMessageAdmissionProgressRuntime(recovery) ||
			!validMessageAdmissionProgressMessages(inputs, messageID) {
			return false
		}
	}
	return true
}

func validMessageAdmissionProgressRuntime(raw json.RawMessage) bool {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func validMessageAdmissionProgressMessages(raw json.RawMessage, original int64) bool {
	value := bytes.TrimSpace(raw)
	if len(value) == 0 || value[0] != '[' {
		return false
	}
	var ids []int64
	if json.Unmarshal(value, &ids) != nil {
		return false
	}
	found := false
	for _, id := range ids {
		if id <= 0 {
			return false
		}
		found = found || id == original
	}
	return found
}
