package models

import "testing"

func TestPreviewStatus_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status  PreviewStatus
		wantErr bool
	}{
		{PreviewStatusStarting, false},
		{PreviewStatusReady, false},
		{PreviewStatusPartiallyReady, false},
		{PreviewStatusUnhealthy, false},
		{PreviewStatusStopped, false},
		{PreviewStatusFailed, false},
		{PreviewStatusExpired, false},
		{"bogus", true},
		{"", true},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			t.Parallel()
			err := tt.status.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPreviewStatus_IsActive(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status PreviewStatus
		want   bool
	}{
		{PreviewStatusStarting, true},
		{PreviewStatusReady, true},
		{PreviewStatusPartiallyReady, true},
		{PreviewStatusUnhealthy, true},
		{PreviewStatusStopped, false},
		{PreviewStatusFailed, false},
		{PreviewStatusExpired, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			t.Parallel()
			if got := tt.status.IsActive(); got != tt.want {
				t.Errorf("IsActive() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPreviewStatus_IsTerminal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status PreviewStatus
		want   bool
	}{
		{PreviewStatusStarting, false},
		{PreviewStatusReady, false},
		{PreviewStatusPartiallyReady, false},
		{PreviewStatusUnhealthy, false},
		{PreviewStatusStopped, true},
		{PreviewStatusFailed, true},
		{PreviewStatusExpired, true},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			t.Parallel()
			if got := tt.status.IsTerminal(); got != tt.want {
				t.Errorf("IsTerminal() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPreviewServiceRole_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		role    PreviewServiceRole
		wantErr bool
	}{
		{PreviewServiceRolePrimary, false},
		{PreviewServiceRoleSupport, false},
		{"bogus", true},
	}
	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			t.Parallel()
			if err := tt.role.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPreviewServiceStatus_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status  PreviewServiceStatus
		wantErr bool
	}{
		{PreviewServiceStatusStarting, false},
		{PreviewServiceStatusReady, false},
		{PreviewServiceStatusStopped, false},
		{PreviewServiceStatusFailed, false},
		{"bogus", true},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			t.Parallel()
			if err := tt.status.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPreviewInfraStatus_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status  PreviewInfraStatus
		wantErr bool
	}{
		{PreviewInfraStatusProvisioning, false},
		{PreviewInfraStatusHealthy, false},
		{PreviewInfraStatusUnhealthy, false},
		{PreviewInfraStatusStopped, false},
		{PreviewInfraStatusFailed, false},
		{"bogus", true},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			t.Parallel()
			if err := tt.status.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPreviewSnapshotTrigger_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		trigger PreviewSnapshotTrigger
		wantErr bool
	}{
		{PreviewSnapshotTriggerBaseline, false},
		{PreviewSnapshotTriggerAgentChange, false},
		{PreviewSnapshotTriggerAgentExplicit, false},
		{PreviewSnapshotTriggerUserRequest, false},
		{PreviewSnapshotTriggerDesignMode, false},
		{"bogus", true},
	}
	for _, tt := range tests {
		t.Run(string(tt.trigger), func(t *testing.T) {
			t.Parallel()
			if err := tt.trigger.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPreviewLogStep_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		step    PreviewLogStep
		wantErr bool
	}{
		{PreviewLogStepBuild, false},
		{PreviewLogStepInstall, false},
		{PreviewLogStepInit, false},
		{PreviewLogStepStart, false},
		{PreviewLogStepProxy, false},
		{PreviewLogStepCleanup, false},
		{"bogus", true},
	}
	for _, tt := range tests {
		t.Run(string(tt.step), func(t *testing.T) {
			t.Parallel()
			if err := tt.step.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPreviewTrustTier_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		tier    PreviewTrustTier
		wantErr bool
	}{
		{PreviewTrustTierRestricted, false},
		{PreviewTrustTierTrustedInternal, false},
		{"bogus", true},
	}
	for _, tt := range tests {
		t.Run(string(tt.tier), func(t *testing.T) {
			t.Parallel()
			if err := tt.tier.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPRPreviewStatus_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status  PRPreviewStatus
		wantErr bool
	}{
		{PRPreviewStatusNeverStarted, false},
		{PRPreviewStatusRunning, false},
		{PRPreviewStatusStopped, false},
		{PRPreviewStatusMerged, false},
		{PRPreviewStatusClosed, false},
		{"bogus", true},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			t.Parallel()
			if err := tt.status.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPreviewProfileName_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    PreviewProfileName
		wantErr bool
	}{
		{PreviewProfileBootstrap, false},
		{PreviewProfileStagingLike, false},
		{"bogus", true},
	}
	for _, tt := range tests {
		t.Run(string(tt.name), func(t *testing.T) {
			t.Parallel()
			if err := tt.name.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPreviewReadiness_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		readiness PreviewReadiness
		wantErr   bool
	}{
		{PreviewReadinessReady, false},
		{PreviewReadinessAdminSetupRequired, false},
		{PreviewReadinessNotSupported, false},
		{"bogus", true},
	}
	for _, tt := range tests {
		t.Run(string(tt.readiness), func(t *testing.T) {
			t.Parallel()
			if err := tt.readiness.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
