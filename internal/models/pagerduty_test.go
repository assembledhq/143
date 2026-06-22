package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPagerDutyOAuthModeValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mode      PagerDutyOAuthMode
		expectErr bool
	}{
		{name: "scoped is valid", mode: PagerDutyOAuthModeScoped},
		{name: "classic user is valid", mode: PagerDutyOAuthModeClassicUser},
		{name: "empty is invalid", mode: "", expectErr: true},
		{name: "api key is invalid", mode: PagerDutyOAuthMode("api_key"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.mode.Validate()
			if tt.expectErr {
				require.Error(t, err, "invalid PagerDuty OAuth mode should fail validation")
				return
			}
			require.NoError(t, err, "known PagerDuty OAuth mode should pass validation")
		})
	}
}

func TestPagerDutyIntegrationStatusValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    PagerDutyIntegrationStatus
		expectErr bool
	}{
		{name: "active is valid", status: PagerDutyIntegrationStatusActive},
		{name: "degraded is valid", status: PagerDutyIntegrationStatusDegraded},
		{name: "inactive is valid", status: PagerDutyIntegrationStatusInactive},
		{name: "empty is invalid", status: "", expectErr: true},
		{name: "deleted is invalid", status: PagerDutyIntegrationStatus("deleted"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.status.Validate()
			if tt.expectErr {
				require.Error(t, err, "invalid PagerDuty integration status should fail validation")
				return
			}
			require.NoError(t, err, "known PagerDuty integration status should pass validation")
		})
	}
}

func TestAutomationEventProviderValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		provider  AutomationEventProvider
		expectErr bool
	}{
		{name: "PagerDuty is valid", provider: AutomationEventProviderPagerDuty},
		{name: "GitHub is valid", provider: AutomationEventProviderGitHub},
		{name: "empty is invalid", provider: "", expectErr: true},
		{name: "unknown is invalid", provider: AutomationEventProvider("jira"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.provider.Validate()
			if tt.expectErr {
				require.Error(t, err, "invalid automation event provider should fail validation")
				return
			}
			require.NoError(t, err, "known automation event provider should pass validation")
		})
	}
}

func TestPagerDutyEventTypeValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		eventType PagerDutyEventType
		expectErr bool
	}{
		{name: "triggered is valid", eventType: PagerDutyEventIncidentTriggered},
		{name: "annotated is valid", eventType: PagerDutyEventIncidentAnnotated},
		{name: "resolved is valid", eventType: PagerDutyEventIncidentResolved},
		{name: "empty is invalid", eventType: "", expectErr: true},
		{name: "unknown is invalid", eventType: PagerDutyEventType("incident.foo"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.eventType.Validate()
			if tt.expectErr {
				require.Error(t, err, "invalid PagerDuty event type should fail validation")
				return
			}
			require.NoError(t, err, "known PagerDuty event type should pass validation")
		})
	}
}
