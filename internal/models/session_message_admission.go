package models

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// SessionMessageIngressKind identifies a command receipt, not an execution or
// delivery authority. The key is opaque to clients and must be retained exactly.
type SessionMessageIngressKind string

const (
	SessionMessageIngressClient SessionMessageIngressKind = "thread_client_message"
	SessionMessageIngressServer SessionMessageIngressKind = "thread_server_command"
)

func (v SessionMessageIngressKind) Validate() error {
	switch v {
	case SessionMessageIngressClient, SessionMessageIngressServer:
		return nil
	default:
		return errors.New("invalid session message ingress kind")
	}
}

// SessionMessageAdmissionReceipt exposes committed input acceptance only. Bound
// is not provider launch, and waiting is not delivery, application or an answer.
// No claim token, job authority or provider state belongs in this response.
type SessionMessageAdmissionReceipt struct {
	DispatchID  uuid.UUID                 `json:"dispatch_id"`
	State       LifecycleAdmissionState   `json:"state"`
	IngressKind SessionMessageIngressKind `json:"ingress_kind"`
	IngressKey  string                    `json:"ingress_key"`
	Replayed    bool                      `json:"replayed"`
}

func (v SessionMessageAdmissionReceipt) Validate() error {
	if v.DispatchID == uuid.Nil {
		return errors.New("session message admission requires a dispatch")
	}
	if v.State != LifecycleAdmissionBound && v.State != LifecycleAdmissionWaiting {
		return errors.New("session message admission requires an accepted state")
	}
	if err := v.IngressKind.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(v.IngressKey) == "" || !utf8.ValidString(v.IngressKey) || strings.ContainsRune(v.IngressKey, '\x00') {
		return errors.New("session message admission requires a valid exact command key")
	}
	return nil
}
