package models

import (
	"os"
	"regexp"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

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
		{PreviewStatusUnavailable, false},
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
		{PreviewStatusUnavailable, false},
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
		{PreviewStatusUnavailable, true},
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

func TestPreviewAutoMode_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mode    PreviewAutoMode
		wantErr bool
	}{
		{name: "off is valid", mode: PreviewAutoModeOff},
		{name: "warm is valid", mode: PreviewAutoModeWarm},
		{name: "on is valid", mode: PreviewAutoModeOn},
		{name: "empty is invalid", mode: "", wantErr: true},
		{name: "bogus is invalid", mode: "bogus", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.mode.Validate()
			if tt.wantErr {
				require.Error(t, err, "PreviewAutoMode should reject invalid values")
				return
			}
			require.NoError(t, err, "PreviewAutoMode should accept known policy modes")
		})
	}
}

func TestPreviewStoppedReason_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		reason  PreviewStoppedReason
		wantErr bool
	}{
		{name: "none is valid", reason: PreviewStoppedReasonNone},
		{name: "user is valid", reason: PreviewStoppedReasonUser},
		{name: "expired is valid", reason: PreviewStoppedReasonExpired},
		{name: "warm policy is valid", reason: PreviewStoppedReasonWarmPolicy},
		{name: "pr closed is valid", reason: PreviewStoppedReasonPRClosed},
		{name: "drain is valid", reason: PreviewStoppedReasonDrain},
		{name: "error is valid", reason: PreviewStoppedReasonError},
		{name: "bogus is invalid", reason: "bogus", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.reason.Validate()
			if tt.wantErr {
				require.Error(t, err, "PreviewStoppedReason should reject invalid values")
				return
			}
			require.NoError(t, err, "PreviewStoppedReason should accept migration check values")
		})
	}
}

func TestPreviewPolicyEnumsMatchMigrationChecks(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../migrations/000180_preview_policies.up.sql")
	require.NoError(t, err, "migration file should be readable")

	tests := []struct {
		name       string
		constraint string
		expected   []string
	}{
		{
			name:       "auto mode",
			constraint: "repository_preview_policies_auto_mode_check",
			expected: []string{
				string(PreviewAutoModeOff),
				string(PreviewAutoModeWarm),
				string(PreviewAutoModeOn),
			},
		},
		{
			name:       "stopped reason",
			constraint: "preview_instances_stopped_reason_check",
			expected: []string{
				string(PreviewStoppedReasonNone),
				string(PreviewStoppedReasonUser),
				string(PreviewStoppedReasonExpired),
				string(PreviewStoppedReasonWarmPolicy),
				string(PreviewStoppedReasonPRClosed),
				string(PreviewStoppedReasonDrain),
				string(PreviewStoppedReasonError),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			values := migrationCheckValues(t, string(body), tt.constraint)
			sort.Strings(values)
			sort.Strings(tt.expected)
			require.Equal(t, tt.expected, values, "migration CHECK values should match Go enum constants")
		})
	}
}

func migrationCheckValues(t *testing.T, sql, constraint string) []string {
	t.Helper()

	re := regexp.MustCompile(regexp.QuoteMeta(constraint) + `(?s).*?CHECK\s*\([^)]*IN\s*\(([^)]*)\)`)
	match := re.FindStringSubmatch(sql)
	require.Len(t, match, 2, "migration should define the expected CHECK constraint")

	valueRe := regexp.MustCompile(`'([^']*)'`)
	matches := valueRe.FindAllStringSubmatch(match[1], -1)
	values := make([]string, 0, len(matches))
	for _, m := range matches {
		values = append(values, m[1])
	}
	return values
}

func TestPreviewFreshnessState_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		state   PreviewFreshnessState
		wantErr bool
	}{
		{name: "current is valid", state: PreviewFreshnessCurrent},
		{name: "live updated is valid", state: PreviewFreshnessLiveUpdated},
		{name: "restart required is valid", state: PreviewFreshnessRestartRequired},
		{name: "out of date is valid", state: PreviewFreshnessOutOfDate},
		{name: "updating is valid", state: PreviewFreshnessUpdating},
		{name: "unknown is valid", state: PreviewFreshnessUnknown},
		{name: "bogus is invalid", state: "bogus", wantErr: true},
		{name: "empty is invalid", state: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.state.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPreviewRuntimeRevisionSource_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		source  PreviewRuntimeRevisionSource
		wantErr bool
	}{
		{name: "none is valid", source: PreviewRuntimeRevisionSourceNone},
		{name: "launch is valid", source: PreviewRuntimeRevisionSourceLaunch},
		{name: "recycle is valid", source: PreviewRuntimeRevisionSourceRecycle},
		{name: "hmr is valid", source: PreviewRuntimeRevisionSourceHMR},
		{name: "file event is valid", source: PreviewRuntimeRevisionSourceFileEvent},
		{name: "bogus is invalid", source: "bogus", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.source.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPreviewRestartReasonKind_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		kind    PreviewRestartReasonKind
		wantErr bool
	}{
		{name: "dependency changed is valid", kind: PreviewRestartReasonDependencyChanged},
		{name: "preview config changed is valid", kind: PreviewRestartReasonPreviewConfigChanged},
		{name: "build config changed is valid", kind: PreviewRestartReasonBuildConfigChanged},
		{name: "environment config changed is valid", kind: PreviewRestartReasonEnvironmentConfigChanged},
		{name: "database schema changed is valid", kind: PreviewRestartReasonDatabaseSchemaChanged},
		{name: "bogus is invalid", kind: "bogus", wantErr: true},
		{name: "empty is invalid", kind: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.kind.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPreviewRuntimeStatus_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status  PreviewRuntimeStatus
		wantErr bool
	}{
		{PreviewRuntimeStatusStarting, false},
		{PreviewRuntimeStatusReady, false},
		{PreviewRuntimeStatusDraining, false},
		{PreviewRuntimeStatusLost, false},
		{PreviewRuntimeStatusStopped, false},
		{PreviewRuntimeStatusFailed, false},
		{"bogus", true},
		{"", true},
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

func TestPreviewRuntimeStatus_IsActive(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status PreviewRuntimeStatus
		want   bool
	}{
		{PreviewRuntimeStatusStarting, true},
		{PreviewRuntimeStatusReady, true},
		{PreviewRuntimeStatusDraining, true},
		{PreviewRuntimeStatusLost, false},
		{PreviewRuntimeStatusStopped, false},
		{PreviewRuntimeStatusFailed, false},
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
