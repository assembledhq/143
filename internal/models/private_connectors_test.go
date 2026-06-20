package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrivateConnectorEnumsValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		err     error
		wantErr bool
	}{
		{name: "valid group status", err: PrivateConnectorStatusOnline.Validate()},
		{name: "invalid group status", err: PrivateConnectorStatus("paused").Validate(), wantErr: true},
		{name: "valid instance status", err: PrivateConnectorInstanceStatusOffline.Validate()},
		{name: "invalid instance status", err: PrivateConnectorInstanceStatus("paused").Validate(), wantErr: true},
		{name: "valid token preset", err: PrivateConnectorTokenPresetInteractive.Validate()},
		{name: "invalid token preset", err: PrivateConnectorTokenPreset("manual").Validate(), wantErr: true},
		{name: "valid resource type", err: PrivateConnectorResourceTypeVictoriaLogs.Validate()},
		{name: "invalid resource type", err: PrivateConnectorResourceType("redis").Validate(), wantErr: true},
		{name: "valid resource mode", err: PrivateConnectorResourceModePreviewRuntime.Validate()},
		{name: "invalid resource mode", err: PrivateConnectorResourceMode("production_write").Validate(), wantErr: true},
		{name: "valid config source", err: PrivateConnectorConfigSourceUI.Validate()},
		{name: "invalid config source", err: PrivateConnectorConfigSource("merged").Validate(), wantErr: true},
		{name: "valid protocol", err: PrivateConnectorProtocolWebSocket.Validate()},
		{name: "invalid protocol", err: PrivateConnectorProtocol("ssh").Validate(), wantErr: true},
		{name: "valid action status", err: PrivateConnectorActionStatusSucceeded.Validate()},
		{name: "invalid action status", err: PrivateConnectorActionStatus("queued").Validate(), wantErr: true},
		{name: "valid runtime lease status", err: PrivateConnectorRuntimeLeaseStatusActive.Validate()},
		{name: "invalid runtime lease status", err: PrivateConnectorRuntimeLeaseStatus("paused").Validate(), wantErr: true},
		{name: "valid runtime access mode", err: PrivateConnectorRuntimeAccessModePostgresTCP.Validate()},
		{name: "invalid runtime access mode", err: PrivateConnectorRuntimeAccessMode("ssh").Validate(), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if tt.wantErr {
				require.Error(t, tt.err, "Validate should reject invalid private connector enum values")
				return
			}
			require.NoError(t, tt.err, "Validate should accept valid private connector enum values")
		})
	}
}
