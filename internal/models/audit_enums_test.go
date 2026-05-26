package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAuditActorType_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     AuditActorType
		expectErr bool
	}{
		{name: "user is valid", value: AuditActorUser},
		{name: "agent is valid", value: AuditActorAgent},
		{name: "system is valid", value: AuditActorSystem},
		{name: "webhook is valid", value: AuditActorWebhook},
		{name: "empty is invalid", value: "", expectErr: true},
		{name: "unknown is invalid", value: "robot", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.value.Validate()
			if tt.expectErr {
				require.Error(t, err, "should reject invalid actor type")
				require.Contains(t, err.Error(), "invalid AuditActorType", "error message should mention invalid AuditActorType")
			} else {
				require.NoError(t, err, "should accept valid actor type")
			}
		})
	}
}

func TestAuditAction_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     AuditAction
		expectErr bool
	}{
		{name: "session.created is valid", value: AuditActionSessionCreated},
		{name: "session.branch_requested is valid", value: AuditActionSessionBranchRequested},
		{name: "session.preview_lifetime_set is valid", value: AuditActionSessionPreviewLifetimeSet},
		{name: "project.started is valid", value: AuditActionProjectStarted},
		{name: "auth.login is valid", value: AuditActionAuthLogin},
		{name: "team.member_invited is valid", value: AuditActionTeamMemberInvited},
		{name: "credential.updated is valid", value: AuditActionCredentialUpdated},
		{name: "pm.plan_created is valid", value: AuditActionPMPlanCreated},
		{name: "issue.created is valid", value: AuditActionIssueCreated},
		{name: "integration.connected is valid", value: AuditActionIntegrationConnected},
		{name: "preview_secret_bundle.updated is valid", value: AuditActionPreviewSecretBundleUpdated},
		{name: "preview_secret_bundle.resolved is valid", value: AuditActionPreviewSecretBundleResolved},
		{name: "empty is invalid", value: "", expectErr: true},
		{name: "unknown is invalid", value: "foo.bar", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.value.Validate()
			if tt.expectErr {
				require.Error(t, err, "should reject invalid audit action")
				require.Contains(t, err.Error(), "invalid AuditAction", "error message should mention invalid AuditAction")
			} else {
				require.NoError(t, err, "should accept valid audit action")
			}
		})
	}
}

func TestAuditResourceType_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     AuditResourceType
		expectErr bool
	}{
		{name: "session is valid", value: AuditResourceSession},
		{name: "project is valid", value: AuditResourceProject},
		{name: "project_task is valid", value: AuditResourceProjectTask},
		{name: "user is valid", value: AuditResourceUser},
		{name: "settings is valid", value: AuditResourceSettings},
		{name: "credential is valid", value: AuditResourceCredential},
		{name: "preview_secret_bundle is valid", value: AuditResourcePreviewSecretBundle},
		{name: "empty is invalid", value: "", expectErr: true},
		{name: "unknown is invalid", value: "foobar", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.value.Validate()
			if tt.expectErr {
				require.Error(t, err, "should reject invalid resource type")
				require.Contains(t, err.Error(), "invalid AuditResourceType", "error message should mention invalid AuditResourceType")
			} else {
				require.NoError(t, err, "should accept valid resource type")
			}
		})
	}
}
