package models

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSessionMessageIngressKindValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		kind  SessionMessageIngressKind
		valid bool
	}{
		{name: "client command", kind: SessionMessageIngressClient, valid: true},
		{name: "server command", kind: SessionMessageIngressServer, valid: true},
		{name: "zero refuses", kind: ""},
		{name: "delivery is not ingress", kind: "delivered"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.valid, tt.kind.Validate() == nil, "only recognized thread message command kinds should validate")
		})
	}
}

func TestSessionMessageAdmissionReceiptValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*SessionMessageAdmissionReceipt)
		valid  bool
	}{
		{name: "bound", valid: true},
		{name: "waiting replay", valid: true, change: func(v *SessionMessageAdmissionReceipt) { v.State, v.Replayed = LifecycleAdmissionWaiting, true }},
		{name: "missing dispatch", change: func(v *SessionMessageAdmissionReceipt) { v.DispatchID = uuid.Nil }},
		{name: "terminal is not accepted message state", change: func(v *SessionMessageAdmissionReceipt) { v.State = LifecycleAdmissionTerminal }},
		{name: "historical is not accepted message state", change: func(v *SessionMessageAdmissionReceipt) { v.State = LifecycleAdmissionHistorical }},
		{name: "unknown ingress", change: func(v *SessionMessageAdmissionReceipt) { v.IngressKind = "provider_launch" }},
		{name: "empty key", change: func(v *SessionMessageAdmissionReceipt) { v.IngressKey = "  " }},
		{name: "NUL key", change: func(v *SessionMessageAdmissionReceipt) { v.IngressKey = "synthetic\x00key" }},
		{name: "invalid UTF8 key", change: func(v *SessionMessageAdmissionReceipt) { v.IngressKey = string([]byte{0xff}) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			receipt := SessionMessageAdmissionReceipt{DispatchID: uuid.New(), State: LifecycleAdmissionBound, IngressKind: SessionMessageIngressClient, IngressKey: "synthetic-command"}
			if tt.change != nil {
				tt.change(&receipt)
			}
			require.Equal(t, tt.valid, receipt.Validate() == nil, "receipt validation should require exact accepted command metadata without delivery authority")
		})
	}
}

func TestSessionMessageAdmissionReceiptJSON(t *testing.T) {
	t.Parallel()
	receipt := SessionMessageAdmissionReceipt{DispatchID: uuid.MustParse("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"), State: LifecycleAdmissionWaiting, IngressKind: SessionMessageIngressServer, IngressKey: "synthetic-command", Replayed: true}
	encoded, err := json.Marshal(receipt)
	require.NoError(t, err, "safe admission receipt should serialize")
	require.JSONEq(t, `{"dispatch_id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","state":"waiting","ingress_kind":"thread_server_command","ingress_key":"synthetic-command","replayed":true}`, string(encoded), "public receipt should expose only acceptance identity and typed state")
}
