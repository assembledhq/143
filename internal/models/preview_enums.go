package models

import "fmt"

// PreviewStatus captures the lifecycle of a preview instance.
type PreviewStatus string

const (
	PreviewStatusStarting       PreviewStatus = "starting"
	PreviewStatusReady          PreviewStatus = "ready"
	PreviewStatusPartiallyReady PreviewStatus = "partially_ready"
	PreviewStatusUnhealthy      PreviewStatus = "unhealthy"
	PreviewStatusStopped        PreviewStatus = "stopped"
	PreviewStatusFailed         PreviewStatus = "failed"
	PreviewStatusExpired        PreviewStatus = "expired"
	PreviewStatusUnavailable    PreviewStatus = "unavailable"
)

func (s PreviewStatus) Validate() error {
	switch s {
	case PreviewStatusStarting,
		PreviewStatusReady,
		PreviewStatusPartiallyReady,
		PreviewStatusUnhealthy,
		PreviewStatusStopped,
		PreviewStatusFailed,
		PreviewStatusExpired,
		PreviewStatusUnavailable:
		return nil
	default:
		return fmt.Errorf("invalid PreviewStatus: %q", s)
	}
}

// IsActive returns true for statuses where the preview is consuming resources.
func (s PreviewStatus) IsActive() bool {
	switch s {
	case PreviewStatusStarting, PreviewStatusReady, PreviewStatusPartiallyReady, PreviewStatusUnhealthy:
		return true
	default:
		return false
	}
}

// IsTerminal returns true for statuses where the preview has stopped.
func (s PreviewStatus) IsTerminal() bool {
	switch s {
	case PreviewStatusStopped, PreviewStatusFailed, PreviewStatusExpired, PreviewStatusUnavailable:
		return true
	default:
		return false
	}
}

// PreviewAutoMode controls repository-level automatic PR preview behavior.
type PreviewAutoMode string

const (
	PreviewAutoModeOff  PreviewAutoMode = "off"
	PreviewAutoModeWarm PreviewAutoMode = "warm"
	PreviewAutoModeOn   PreviewAutoMode = "on"
)

func (m PreviewAutoMode) Validate() error {
	switch m {
	case PreviewAutoModeOff, PreviewAutoModeWarm, PreviewAutoModeOn:
		return nil
	default:
		return fmt.Errorf("invalid PreviewAutoMode: %q", m)
	}
}

// PreviewStoppedReason records why a preview instance entered a terminal stop.
type PreviewStoppedReason string

const (
	PreviewStoppedReasonNone       PreviewStoppedReason = ""
	PreviewStoppedReasonUser       PreviewStoppedReason = "user"
	PreviewStoppedReasonExpired    PreviewStoppedReason = "expired"
	PreviewStoppedReasonWarmPolicy PreviewStoppedReason = "warm_policy"
	PreviewStoppedReasonPRClosed   PreviewStoppedReason = "pr_closed"
	PreviewStoppedReasonDrain      PreviewStoppedReason = "drain"
	PreviewStoppedReasonError      PreviewStoppedReason = "error"
)

func (r PreviewStoppedReason) Validate() error {
	switch r {
	case PreviewStoppedReasonNone,
		PreviewStoppedReasonUser,
		PreviewStoppedReasonExpired,
		PreviewStoppedReasonWarmPolicy,
		PreviewStoppedReasonPRClosed,
		PreviewStoppedReasonDrain,
		PreviewStoppedReasonError:
		return nil
	default:
		return fmt.Errorf("invalid PreviewStoppedReason: %q", r)
	}
}

// PreviewFreshnessState describes whether a session preview was launched from
// the latest durable workspace revision.
type PreviewFreshnessState string

const (
	PreviewFreshnessCurrent   PreviewFreshnessState = "current"
	PreviewFreshnessOutOfDate PreviewFreshnessState = "out_of_date"
	PreviewFreshnessUpdating  PreviewFreshnessState = "updating"
	PreviewFreshnessUnknown   PreviewFreshnessState = "unknown"
)

func (s PreviewFreshnessState) Validate() error {
	switch s {
	case PreviewFreshnessCurrent,
		PreviewFreshnessOutOfDate,
		PreviewFreshnessUpdating,
		PreviewFreshnessUnknown:
		return nil
	default:
		return fmt.Errorf("invalid PreviewFreshnessState: %q", s)
	}
}

// PreviewRuntimeStatus captures the lifecycle of a worker-owned live preview runtime.
type PreviewRuntimeStatus string

const (
	PreviewRuntimeStatusStarting PreviewRuntimeStatus = "starting"
	PreviewRuntimeStatusReady    PreviewRuntimeStatus = "ready"
	PreviewRuntimeStatusDraining PreviewRuntimeStatus = "draining"
	PreviewRuntimeStatusLost     PreviewRuntimeStatus = "lost"
	PreviewRuntimeStatusStopped  PreviewRuntimeStatus = "stopped"
	PreviewRuntimeStatusFailed   PreviewRuntimeStatus = "failed"
)

func (s PreviewRuntimeStatus) Validate() error {
	switch s {
	case PreviewRuntimeStatusStarting,
		PreviewRuntimeStatusReady,
		PreviewRuntimeStatusDraining,
		PreviewRuntimeStatusLost,
		PreviewRuntimeStatusStopped,
		PreviewRuntimeStatusFailed:
		return nil
	default:
		return fmt.Errorf("invalid PreviewRuntimeStatus: %q", s)
	}
}

// IsActive returns true when a runtime can still own or serve preview traffic.
func (s PreviewRuntimeStatus) IsActive() bool {
	switch s {
	case PreviewRuntimeStatusStarting, PreviewRuntimeStatusReady, PreviewRuntimeStatusDraining:
		return true
	default:
		return false
	}
}

// PreviewSourceType records who or what requested a branch preview target.
type PreviewSourceType string

const (
	PreviewSourceTypeSession     PreviewSourceType = "session"
	PreviewSourceTypePullRequest PreviewSourceType = "pull_request"
	PreviewSourceTypeAPI         PreviewSourceType = "api"
	PreviewSourceTypeManual      PreviewSourceType = "manual"
	PreviewSourceTypeAutomation  PreviewSourceType = "automation"
)

func (s PreviewSourceType) Validate() error {
	switch s {
	case PreviewSourceTypeSession,
		PreviewSourceTypePullRequest,
		PreviewSourceTypeAPI,
		PreviewSourceTypeManual,
		PreviewSourceTypeAutomation:
		return nil
	default:
		return fmt.Errorf("invalid PreviewSourceType: %q", s)
	}
}

// PreviewLinkType identifies the stable link namespace a preview link occupies.
type PreviewLinkType string

const (
	PreviewLinkTypeTarget      PreviewLinkType = "target"
	PreviewLinkTypePullRequest PreviewLinkType = "pull_request"
)

func (t PreviewLinkType) Validate() error {
	switch t {
	case PreviewLinkTypeTarget, PreviewLinkTypePullRequest:
		return nil
	default:
		return fmt.Errorf("invalid PreviewLinkType: %q", t)
	}
}

// PreviewServiceRole identifies a service as primary or support.
type PreviewServiceRole string

const (
	PreviewServiceRolePrimary PreviewServiceRole = "primary"
	PreviewServiceRoleSupport PreviewServiceRole = "support"
)

func (r PreviewServiceRole) Validate() error {
	switch r {
	case PreviewServiceRolePrimary, PreviewServiceRoleSupport:
		return nil
	default:
		return fmt.Errorf("invalid PreviewServiceRole: %q", r)
	}
}

// PreviewServiceStatus captures the lifecycle of a single service within a preview.
type PreviewServiceStatus string

const (
	PreviewServiceStatusStarting PreviewServiceStatus = "starting"
	PreviewServiceStatusReady    PreviewServiceStatus = "ready"
	PreviewServiceStatusStopped  PreviewServiceStatus = "stopped"
	PreviewServiceStatusFailed   PreviewServiceStatus = "failed"
)

func (s PreviewServiceStatus) Validate() error {
	switch s {
	case PreviewServiceStatusStarting,
		PreviewServiceStatusReady,
		PreviewServiceStatusStopped,
		PreviewServiceStatusFailed:
		return nil
	default:
		return fmt.Errorf("invalid PreviewServiceStatus: %q", s)
	}
}

// PreviewInfraStatus captures the lifecycle of a platform infrastructure container.
type PreviewInfraStatus string

const (
	PreviewInfraStatusProvisioning PreviewInfraStatus = "provisioning"
	PreviewInfraStatusHealthy      PreviewInfraStatus = "healthy"
	PreviewInfraStatusUnhealthy    PreviewInfraStatus = "unhealthy"
	PreviewInfraStatusStopped      PreviewInfraStatus = "stopped"
	PreviewInfraStatusFailed       PreviewInfraStatus = "failed"
)

func (s PreviewInfraStatus) Validate() error {
	switch s {
	case PreviewInfraStatusProvisioning,
		PreviewInfraStatusHealthy,
		PreviewInfraStatusUnhealthy,
		PreviewInfraStatusStopped,
		PreviewInfraStatusFailed:
		return nil
	default:
		return fmt.Errorf("invalid PreviewInfraStatus: %q", s)
	}
}

// PreviewSnapshotTrigger identifies what caused a screenshot capture.
type PreviewSnapshotTrigger string

const (
	PreviewSnapshotTriggerBaseline      PreviewSnapshotTrigger = "baseline"
	PreviewSnapshotTriggerAgentChange   PreviewSnapshotTrigger = "agent_change"
	PreviewSnapshotTriggerAgentExplicit PreviewSnapshotTrigger = "agent_explicit"
	PreviewSnapshotTriggerUserRequest   PreviewSnapshotTrigger = "user_request"
	PreviewSnapshotTriggerDesignMode    PreviewSnapshotTrigger = "design_mode"
)

func (t PreviewSnapshotTrigger) Validate() error {
	switch t {
	case PreviewSnapshotTriggerBaseline,
		PreviewSnapshotTriggerAgentChange,
		PreviewSnapshotTriggerAgentExplicit,
		PreviewSnapshotTriggerUserRequest,
		PreviewSnapshotTriggerDesignMode:
		return nil
	default:
		return fmt.Errorf("invalid PreviewSnapshotTrigger: %q", t)
	}
}

// PreviewLogStep identifies which preview lifecycle phase a log belongs to.
type PreviewLogStep string

const (
	PreviewLogStepBuild          PreviewLogStep = "build"
	PreviewLogStepInstall        PreviewLogStep = "install"
	PreviewLogStepInit           PreviewLogStep = "init"
	PreviewLogStepStart          PreviewLogStep = "start"
	PreviewLogStepProxy          PreviewLogStep = "proxy"
	PreviewLogStepCleanup        PreviewLogStep = "cleanup"
	PreviewLogStepDesignFeedback PreviewLogStep = "design_feedback"
)

func (s PreviewLogStep) Validate() error {
	switch s {
	case PreviewLogStepBuild,
		PreviewLogStepInstall,
		PreviewLogStepInit,
		PreviewLogStepStart,
		PreviewLogStepProxy,
		PreviewLogStepCleanup,
		PreviewLogStepDesignFeedback:
		return nil
	default:
		return fmt.Errorf("invalid PreviewLogStep: %q", s)
	}
}

// PreviewTrustTier controls the credential and egress policy for a preview.
type PreviewTrustTier string

const (
	PreviewTrustTierRestricted      PreviewTrustTier = "restricted"
	PreviewTrustTierTrustedInternal PreviewTrustTier = "trusted_internal"
)

func (t PreviewTrustTier) Validate() error {
	switch t {
	case PreviewTrustTierRestricted, PreviewTrustTierTrustedInternal:
		return nil
	default:
		return fmt.Errorf("invalid PreviewTrustTier: %q", t)
	}
}

// PreviewProfileName identifies the preview security profile.
type PreviewProfileName string

const (
	PreviewProfileBootstrap   PreviewProfileName = "bootstrap"
	PreviewProfileStagingLike PreviewProfileName = "staging_like"
)

func (p PreviewProfileName) Validate() error {
	switch p {
	case PreviewProfileBootstrap, PreviewProfileStagingLike:
		return nil
	default:
		return fmt.Errorf("invalid PreviewProfileName: %q", p)
	}
}

// PRPreviewStatus tracks the PR comment lifecycle.
type PRPreviewStatus string

const (
	PRPreviewStatusNeverStarted PRPreviewStatus = "never_started"
	PRPreviewStatusRunning      PRPreviewStatus = "running"
	PRPreviewStatusStopped      PRPreviewStatus = "stopped"
	PRPreviewStatusMerged       PRPreviewStatus = "merged"
	PRPreviewStatusClosed       PRPreviewStatus = "closed"
)

func (s PRPreviewStatus) Validate() error {
	switch s {
	case PRPreviewStatusNeverStarted,
		PRPreviewStatusRunning,
		PRPreviewStatusStopped,
		PRPreviewStatusMerged,
		PRPreviewStatusClosed:
		return nil
	default:
		return fmt.Errorf("invalid PRPreviewStatus: %q", s)
	}
}

// PreviewReadiness is the result of repo preview detection.
type PreviewReadiness string

const (
	PreviewReadinessReady              PreviewReadiness = "ready"
	PreviewReadinessAdminSetupRequired PreviewReadiness = "admin_setup_required"
	PreviewReadinessNotSupported       PreviewReadiness = "not_supported"
)

func (r PreviewReadiness) Validate() error {
	switch r {
	case PreviewReadinessReady,
		PreviewReadinessAdminSetupRequired,
		PreviewReadinessNotSupported:
		return nil
	default:
		return fmt.Errorf("invalid PreviewReadiness: %q", r)
	}
}
