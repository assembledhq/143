package models

import "fmt"

// LifecycleDispatchKind identifies one exact input, not a job dedupe family.
type LifecycleDispatchKind string

const (
	LifecycleDispatchMessage  LifecycleDispatchKind = "message"
	LifecycleDispatchAnswer   LifecycleDispatchKind = "answer"
	LifecycleDispatchDenial   LifecycleDispatchKind = "denial"
	LifecycleDispatchInitial  LifecycleDispatchKind = "initial"
	LifecycleDispatchRetry    LifecycleDispatchKind = "retry"
	LifecycleDispatchRecovery LifecycleDispatchKind = "recovery"
)

func (v LifecycleDispatchKind) Validate() error {
	switch v {
	case LifecycleDispatchMessage, LifecycleDispatchAnswer, LifecycleDispatchDenial,
		LifecycleDispatchInitial, LifecycleDispatchRetry, LifecycleDispatchRecovery:
		return nil
	default:
		return fmt.Errorf("invalid LifecycleDispatchKind: %q", v)
	}
}

// LifecycleAdmissionState records admission only. Delivery, application and
// uncertainty remain owned by the inbox and must not be copied into this state.
type LifecycleAdmissionState string

const (
	LifecycleAdmissionWaiting    LifecycleAdmissionState = "waiting"
	LifecycleAdmissionBound      LifecycleAdmissionState = "bound"
	LifecycleAdmissionTerminal   LifecycleAdmissionState = "terminal"
	LifecycleAdmissionHistorical LifecycleAdmissionState = "historical"
)

func (v LifecycleAdmissionState) Validate() error {
	switch v {
	case LifecycleAdmissionWaiting, LifecycleAdmissionBound, LifecycleAdmissionTerminal, LifecycleAdmissionHistorical:
		return nil
	default:
		return fmt.Errorf("invalid LifecycleAdmissionState: %q", v)
	}
}

// LifecycleConsumerKind distinguishes logical input ownership from execution
// leases. A job and its proven allocated runtime are one consumer, not two.
type LifecycleConsumerKind string

const (
	LifecycleConsumerNone    LifecycleConsumerKind = "none"
	LifecycleConsumerJob     LifecycleConsumerKind = "job"
	LifecycleConsumerRuntime LifecycleConsumerKind = "runtime"
)

func (v LifecycleConsumerKind) Validate() error {
	switch v {
	case LifecycleConsumerNone, LifecycleConsumerJob, LifecycleConsumerRuntime:
		return nil
	default:
		return fmt.Errorf("invalid LifecycleConsumerKind: %q", v)
	}
}
