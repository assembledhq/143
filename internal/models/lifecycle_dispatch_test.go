package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLifecycleDispatchEnums(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		value   interface{ Validate() error }
		invalid bool
	}{
		{name: "message", value: LifecycleDispatchMessage},
		{name: "answer", value: LifecycleDispatchAnswer},
		{name: "denial", value: LifecycleDispatchDenial},
		{name: "initial", value: LifecycleDispatchInitial},
		{name: "retry", value: LifecycleDispatchRetry},
		{name: "recovery", value: LifecycleDispatchRecovery},
		{name: "zero kind", value: LifecycleDispatchKind(""), invalid: true},
		{name: "future kind", value: LifecycleDispatchKind("future"), invalid: true},
		{name: "waiting", value: LifecycleAdmissionWaiting},
		{name: "bound", value: LifecycleAdmissionBound},
		{name: "terminal", value: LifecycleAdmissionTerminal},
		{name: "historical", value: LifecycleAdmissionHistorical},
		{name: "zero admission state", value: LifecycleAdmissionState(""), invalid: true},
		{name: "delivery does not belong in admission state", value: LifecycleAdmissionState("delivered"), invalid: true},
		{name: "uncertainty does not belong in admission state", value: LifecycleAdmissionState("uncertain"), invalid: true},
		{name: "no consumer", value: LifecycleConsumerNone},
		{name: "job", value: LifecycleConsumerJob},
		{name: "runtime", value: LifecycleConsumerRuntime},
		{name: "zero consumer", value: LifecycleConsumerKind(""), invalid: true},
		{name: "unknown consumer", value: LifecycleConsumerKind("future"), invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.value.Validate()
			if tt.invalid {
				require.Error(t, err, "unknown or cross-state-machine enum values must fail closed")
			} else {
				require.NoError(t, err, "each explicitly named persisted lifecycle enum must be recognized")
			}
		})
	}
}
