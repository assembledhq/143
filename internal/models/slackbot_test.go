package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionAttributionSourceValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		source    SessionAttributionSource
		expectErr bool
	}{
		{name: "slack is valid", source: SessionAttributionSourceSlack},
		{name: "external api is valid", source: SessionAttributionSourceExternalAPI},
		{name: "unknown is invalid", source: SessionAttributionSource("email"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.source.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown attribution sources")
				return
			}
			require.NoError(t, err, "Validate should accept known attribution sources")
		})
	}
}

func TestSlackNotificationKindValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		kind      SlackNotificationKind
		expectErr bool
	}{
		{name: "auto repair attention is valid", kind: SlackNotificationPRAutoRepairAttention},
		{name: "readiness attention is valid", kind: SlackNotificationPRReadinessAttention},
		{name: "unknown is invalid", kind: SlackNotificationKind("pr.auto_repair_done"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.kind.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown Slack notification kinds")
				return
			}
			require.NoError(t, err, "Validate should accept known Slack notification kinds")
		})
	}
}
