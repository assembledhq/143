package models

import "fmt"

type RuntimeProgressStrength string

const (
	RuntimeProgressStrengthNone   RuntimeProgressStrength = ""
	RuntimeProgressStrengthWeak   RuntimeProgressStrength = "weak"
	RuntimeProgressStrengthStrong RuntimeProgressStrength = "strong"
)

func (s RuntimeProgressStrength) Validate() error {
	switch s {
	case RuntimeProgressStrengthNone, RuntimeProgressStrengthWeak, RuntimeProgressStrengthStrong:
		return nil
	default:
		return fmt.Errorf("invalid RuntimeProgressStrength: %q", s)
	}
}

type RuntimeProgressType string

const (
	RuntimeProgressTypeNone            RuntimeProgressType = ""
	RuntimeProgressTypeAssistantOutput RuntimeProgressType = "assistant_output"
	RuntimeProgressTypeAssistantReason RuntimeProgressType = "assistant_reasoning"
	RuntimeProgressTypeToolUse         RuntimeProgressType = "tool_use"
	RuntimeProgressTypeToolResult      RuntimeProgressType = "tool_result"
	RuntimeProgressTypeDiffChanged     RuntimeProgressType = "diff_changed"
	RuntimeProgressTypeCheckpoint      RuntimeProgressType = "checkpoint_written"
	RuntimeProgressTypeQuestionBlocked RuntimeProgressType = "question_blocked"
)

func (t RuntimeProgressType) Validate() error {
	switch t {
	case RuntimeProgressTypeNone,
		RuntimeProgressTypeAssistantOutput,
		RuntimeProgressTypeAssistantReason,
		RuntimeProgressTypeToolUse,
		RuntimeProgressTypeToolResult,
		RuntimeProgressTypeDiffChanged,
		RuntimeProgressTypeCheckpoint,
		RuntimeProgressTypeQuestionBlocked:
		return nil
	default:
		return fmt.Errorf("invalid RuntimeProgressType: %q", t)
	}
}

type RuntimeStopReason string

const (
	RuntimeStopReasonNone                RuntimeStopReason = ""
	RuntimeStopReasonUserCancel          RuntimeStopReason = "user_cancel"
	RuntimeStopReasonSoftBudget          RuntimeStopReason = "soft_budget"
	RuntimeStopReasonNoProgress          RuntimeStopReason = "no_progress"
	RuntimeStopReasonAbsoluteCeiling     RuntimeStopReason = "absolute_ceiling"
	RuntimeStopReasonForceKill           RuntimeStopReason = "force_kill"
	RuntimeStopReasonWorkerRecovery      RuntimeStopReason = "worker_recovery"
	RuntimeStopReasonWorkerDrain         RuntimeStopReason = "worker_drain"
	RuntimeStopReasonDeployBudgetExpired RuntimeStopReason = "deploy_budget_expired"
)

func (r RuntimeStopReason) Validate() error {
	switch r {
	case RuntimeStopReasonNone,
		RuntimeStopReasonUserCancel,
		RuntimeStopReasonSoftBudget,
		RuntimeStopReasonNoProgress,
		RuntimeStopReasonAbsoluteCeiling,
		RuntimeStopReasonForceKill,
		RuntimeStopReasonWorkerRecovery,
		RuntimeStopReasonWorkerDrain,
		RuntimeStopReasonDeployBudgetExpired:
		return nil
	default:
		return fmt.Errorf("invalid RuntimeStopReason: %q", r)
	}
}

type CheckpointCapability string

const (
	CheckpointCapabilityNone           CheckpointCapability = ""
	CheckpointCapabilityFullResume     CheckpointCapability = "full_resume"
	CheckpointCapabilityFilesystemOnly CheckpointCapability = "filesystem_resume"
	CheckpointCapabilityNoDurable      CheckpointCapability = "no_durable_resume"
)

func (c CheckpointCapability) Validate() error {
	switch c {
	case CheckpointCapabilityNone,
		CheckpointCapabilityFullResume,
		CheckpointCapabilityFilesystemOnly,
		CheckpointCapabilityNoDurable:
		return nil
	default:
		return fmt.Errorf("invalid CheckpointCapability: %q", c)
	}
}

type CheckpointKind string

const (
	CheckpointKindNone         CheckpointKind = ""
	CheckpointKindBootstrap    CheckpointKind = "bootstrap"
	CheckpointKindTurnComplete CheckpointKind = "turn_complete"
	CheckpointKindGracefulStop CheckpointKind = "graceful_stop"
)

func (k CheckpointKind) Validate() error {
	switch k {
	case CheckpointKindNone, CheckpointKindBootstrap, CheckpointKindTurnComplete, CheckpointKindGracefulStop:
		return nil
	default:
		return fmt.Errorf("invalid CheckpointKind: %q", k)
	}
}

type RecoveryState string

const (
	RecoveryStateNone        RecoveryState = ""
	RecoveryStateQueued      RecoveryState = "queued"
	RecoveryStateRecovering  RecoveryState = "recovering"
	RecoveryStateUnavailable RecoveryState = "unavailable"
)

func (s RecoveryState) Validate() error {
	switch s {
	case RecoveryStateNone, RecoveryStateQueued, RecoveryStateRecovering, RecoveryStateUnavailable:
		return nil
	default:
		return fmt.Errorf("invalid RecoveryState: %q", s)
	}
}

type RuntimeBudgetSettings struct {
	NoProgressTimeoutSeconds            int `json:"no_progress_timeout_seconds,omitempty"`
	GracefulShutdownWindowSeconds       int `json:"graceful_shutdown_window_seconds,omitempty"`
	CheckpointFinalizationWindowSeconds int `json:"checkpoint_finalization_window_seconds,omitempty"`
	AutomaticExtensionSeconds           int `json:"automatic_extension_seconds,omitempty"`
	MaxAutomaticExtensionSeconds        int `json:"max_automatic_extension_seconds,omitempty"`
	AbsoluteRuntimeCeilingSeconds       int `json:"absolute_runtime_ceiling_seconds,omitempty"`
}
