package models

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// AutomationSessionContinuity controls whether each run of an automation
// starts a fresh session (per_run, the historical behaviour) or whether all
// runs for the same pull request continue one automation-owned session
// (per_target). See design doc 125.
type AutomationSessionContinuity string

const (
	AutomationSessionContinuityPerRun    AutomationSessionContinuity = "per_run"
	AutomationSessionContinuityPerTarget AutomationSessionContinuity = "per_target"
)

func (c AutomationSessionContinuity) Validate() error {
	switch c {
	case AutomationSessionContinuityPerRun, AutomationSessionContinuityPerTarget:
		return nil
	default:
		return fmt.Errorf("invalid session_continuity: %q (must be per_run or per_target)", c)
	}
}

func (c AutomationSessionContinuity) OrDefault() AutomationSessionContinuity {
	if c == "" {
		return AutomationSessionContinuityPerRun
	}
	return c
}

// AutomationSessionContinuityError is returned when an automation's
// continuity setting is incompatible with its other settings. Field names
// the offending request field so the API can surface it in details.field.
type AutomationSessionContinuityError struct {
	Field   string
	Message string
}

func (e *AutomationSessionContinuityError) Error() string {
	return e.Message
}

// ValidateAutomationSessionContinuity enforces the cross-field rules for
// per-target continuity: it needs at least one pull-request-scoped GitHub
// event trigger (every supported GitHub event is delivered per pull request,
// including check events, which PRService fans out per PR reference) and a
// publish policy of none, because a per-target session is a review
// conversation that never opens pull requests.
func ValidateAutomationSessionContinuity(continuity AutomationSessionContinuity, triggers []AutomationGitHubEvent, publishPolicy AutomationPublishPolicy) error {
	if err := continuity.OrDefault().Validate(); err != nil {
		return &AutomationSessionContinuityError{Field: "session_continuity", Message: err.Error()}
	}
	if continuity.OrDefault() != AutomationSessionContinuityPerTarget {
		return nil
	}
	if len(triggers) == 0 {
		return &AutomationSessionContinuityError{
			Field:   "github_event_triggers",
			Message: "session_continuity=per_target requires at least one pull request GitHub event trigger",
		}
	}
	if publishPolicy.OrDefault() != AutomationPublishPolicyNone {
		return &AutomationSessionContinuityError{
			Field:   "publish_policy",
			Message: "session_continuity=per_target requires publish_policy=none",
		}
	}
	return nil
}

// AutomationTargetKind identifies what a target row points at. Only pull
// requests are supported in this version.
type AutomationTargetKind string

const (
	AutomationTargetKindGitHubPullRequest AutomationTargetKind = "github_pull_request"
)

func (k AutomationTargetKind) Validate() error {
	switch k {
	case AutomationTargetKindGitHubPullRequest:
		return nil
	default:
		return fmt.Errorf("invalid automation target kind: %q", k)
	}
}

// AutomationTargetLifecycleState mirrors the pull request's open/closed/merged
// state as observed through webhooks and dispatch-time revalidation.
type AutomationTargetLifecycleState string

const (
	AutomationTargetLifecycleOpen   AutomationTargetLifecycleState = "open"
	AutomationTargetLifecycleClosed AutomationTargetLifecycleState = "closed"
	AutomationTargetLifecycleMerged AutomationTargetLifecycleState = "merged"
)

// AllowsEvent reports whether a run for event may execute on a target in
// this lifecycle state: open targets run everything; a merged target runs
// only its merged event; a closed target runs nothing.
func (s AutomationTargetLifecycleState) AllowsEvent(event AutomationGitHubEvent) bool {
	switch s {
	case AutomationTargetLifecycleOpen:
		return true
	case AutomationTargetLifecycleMerged:
		return event == AutomationGitHubEventPullRequestMerged
	default:
		return false
	}
}

func (s AutomationTargetLifecycleState) Validate() error {
	switch s {
	case AutomationTargetLifecycleOpen, AutomationTargetLifecycleClosed, AutomationTargetLifecycleMerged:
		return nil
	default:
		return fmt.Errorf("invalid automation target lifecycle state: %q", s)
	}
}

// AutomationTarget is the identity, lifecycle, lock, and wake-outbox row for
// one (automation, repository, pull request). It exists whether or not a
// session has been created yet; ActiveGeneration = 0 means no session.
type AutomationTarget struct {
	ID                       uuid.UUID                      `db:"id" json:"id"`
	OrgID                    uuid.UUID                      `db:"org_id" json:"org_id"`
	AutomationID             uuid.UUID                      `db:"automation_id" json:"automation_id"`
	RepositoryID             uuid.UUID                      `db:"repository_id" json:"repository_id"`
	TargetKind               AutomationTargetKind           `db:"target_kind" json:"target_kind"`
	TargetKey                string                         `db:"target_key" json:"target_key"`
	ActiveGeneration         int                            `db:"active_generation" json:"active_generation"`
	LifecycleState           AutomationTargetLifecycleState `db:"lifecycle_state" json:"lifecycle_state"`
	LifecycleUpdatedAt       *time.Time                     `db:"lifecycle_updated_at" json:"lifecycle_updated_at,omitempty"`
	ObservedHeadSHA          *string                        `db:"observed_head_sha" json:"observed_head_sha,omitempty"`
	ObservedHeadUpdatedAt    *time.Time                     `db:"observed_head_updated_at" json:"observed_head_updated_at,omitempty"`
	HeadEpoch                int                            `db:"head_epoch" json:"head_epoch"`
	HeadResolutionPending    bool                           `db:"head_resolution_pending" json:"head_resolution_pending"`
	HeadResolutionDeadlineAt *time.Time                     `db:"head_resolution_deadline_at" json:"head_resolution_deadline_at,omitempty"`
	WakeRequestedAt          *time.Time                     `db:"wake_requested_at" json:"wake_requested_at,omitempty"`
	CreatedAt                time.Time                      `db:"created_at" json:"created_at"`
	UpdatedAt                time.Time                      `db:"updated_at" json:"updated_at"`
}

// AutomationTargetSessionStatus is the state of one generation row.
type AutomationTargetSessionStatus string

const (
	AutomationTargetSessionStatusActive  AutomationTargetSessionStatus = "active"
	AutomationTargetSessionStatusRetired AutomationTargetSessionStatus = "retired"
)

func (s AutomationTargetSessionStatus) Validate() error {
	switch s {
	case AutomationTargetSessionStatusActive, AutomationTargetSessionStatusRetired:
		return nil
	default:
		return fmt.Errorf("invalid automation target session status: %q", s)
	}
}

// AutomationTargetRetiredReason records why a generation stopped being the
// active session for its target. Retirement never deletes anything.
type AutomationTargetRetiredReason string

const (
	AutomationTargetRetiredManualReset          AutomationTargetRetiredReason = "manual_reset"
	AutomationTargetRetiredPRClosed             AutomationTargetRetiredReason = "pr_closed"
	AutomationTargetRetiredPRMerged             AutomationTargetRetiredReason = "pr_merged"
	AutomationTargetRetiredSessionUnavailable   AutomationTargetRetiredReason = "session_unavailable"
	AutomationTargetRetiredNotResumable         AutomationTargetRetiredReason = "not_resumable"
	AutomationTargetRetiredAgentConfigChanged   AutomationTargetRetiredReason = "agent_config_changed"
	AutomationTargetRetiredIdentityChanged      AutomationTargetRetiredReason = "identity_changed"
	AutomationTargetRetiredBaseRetargeted       AutomationTargetRetiredReason = "base_retargeted"
	AutomationTargetRetiredTurnLimit            AutomationTargetRetiredReason = "turn_limit"
	AutomationTargetRetiredSnapshotTooLarge     AutomationTargetRetiredReason = "snapshot_too_large"
	AutomationTargetRetiredUnsupportedWorkspace AutomationTargetRetiredReason = "unsupported_workspace"
	AutomationTargetRetiredAwaitingInput        AutomationTargetRetiredReason = "awaiting_input"
	AutomationTargetRetiredContinuityDisabled   AutomationTargetRetiredReason = "continuity_disabled"
)

func (r AutomationTargetRetiredReason) Validate() error {
	switch r {
	case AutomationTargetRetiredManualReset, AutomationTargetRetiredPRClosed, AutomationTargetRetiredPRMerged,
		AutomationTargetRetiredSessionUnavailable, AutomationTargetRetiredNotResumable,
		AutomationTargetRetiredAgentConfigChanged, AutomationTargetRetiredIdentityChanged,
		AutomationTargetRetiredBaseRetargeted, AutomationTargetRetiredTurnLimit,
		AutomationTargetRetiredSnapshotTooLarge, AutomationTargetRetiredUnsupportedWorkspace,
		AutomationTargetRetiredAwaitingInput, AutomationTargetRetiredContinuityDisabled:
		return nil
	default:
		return fmt.Errorf("invalid automation target retired reason: %q", r)
	}
}

// AutomationTargetSession is one generation of a target's session. The
// checkpoint_* fields describe the provenance of the session's current
// snapshot_key and are written only by PublishCheckpointWithProvenance;
// LastReviewedHeadSHA advances only on a completed review.
type AutomationTargetSession struct {
	ID                              uuid.UUID                      `db:"id" json:"id"`
	OrgID                           uuid.UUID                      `db:"org_id" json:"org_id"`
	TargetID                        uuid.UUID                      `db:"target_id" json:"target_id"`
	Generation                      int                            `db:"generation" json:"generation"`
	SessionID                       uuid.UUID                      `db:"session_id" json:"session_id"`
	Status                          AutomationTargetSessionStatus  `db:"status" json:"status"`
	RetiredReason                   *AutomationTargetRetiredReason `db:"retired_reason" json:"retired_reason,omitempty"`
	RetiredAt                       *time.Time                     `db:"retired_at" json:"retired_at,omitempty"`
	TurnCount                       int                            `db:"turn_count" json:"turn_count"`
	OwnershipReleasePending         bool                           `db:"ownership_release_pending" json:"ownership_release_pending"`
	LastAttemptedHeadSHA            *string                        `db:"last_attempted_head_sha" json:"last_attempted_head_sha,omitempty"`
	LastReviewedHeadSHA             *string                        `db:"last_reviewed_head_sha" json:"last_reviewed_head_sha,omitempty"`
	LastReviewedEpoch               int                            `db:"last_reviewed_epoch" json:"last_reviewed_epoch"`
	CheckpointSnapshotKey           *string                        `db:"checkpoint_snapshot_key" json:"-"`
	CheckpointHeadSHA               *string                        `db:"checkpoint_head_sha" json:"checkpoint_head_sha,omitempty"`
	CheckpointDependencyFingerprint *string                        `db:"checkpoint_dependency_fingerprint" json:"-"`
	CheckpointReviewComplete        *bool                          `db:"checkpoint_review_complete" json:"checkpoint_review_complete,omitempty"`
	LastBaseRef                     *string                        `db:"last_base_ref" json:"last_base_ref,omitempty"`
	LastRunID                       *uuid.UUID                     `db:"last_run_id" json:"last_run_id,omitempty"`
	LastTurnAt                      *time.Time                     `db:"last_turn_at" json:"last_turn_at,omitempty"`
	CreatedAt                       time.Time                      `db:"created_at" json:"created_at"`
	UpdatedAt                       time.Time                      `db:"updated_at" json:"updated_at"`
}

// AutomationRunHeadResolution records how a run's head was ordered against
// the target's observed head at arrival or dispatch.
type AutomationRunHeadResolution string

const (
	AutomationRunHeadAuthoritative AutomationRunHeadResolution = "authoritative"
	AutomationRunHeadAmbiguous     AutomationRunHeadResolution = "ambiguous"
	AutomationRunHeadUnresolved    AutomationRunHeadResolution = "unresolved"
)

func (r AutomationRunHeadResolution) Validate() error {
	switch r {
	case AutomationRunHeadAuthoritative, AutomationRunHeadAmbiguous, AutomationRunHeadUnresolved:
		return nil
	default:
		return fmt.Errorf("invalid automation run head resolution: %q", r)
	}
}

// AutomationRunContinuationMode is how a per-target run obtained its
// workspace and context.
type AutomationRunContinuationMode string

const (
	AutomationRunContinuationFresh         AutomationRunContinuationMode = "fresh"
	AutomationRunContinuationContinued     AutomationRunContinuationMode = "continued"
	AutomationRunContinuationReconstructed AutomationRunContinuationMode = "reconstructed"
)

func (m AutomationRunContinuationMode) Validate() error {
	switch m {
	case AutomationRunContinuationFresh, AutomationRunContinuationContinued, AutomationRunContinuationReconstructed:
		return nil
	default:
		return fmt.Errorf("invalid automation run continuation mode: %q", m)
	}
}

// AutomationRunContinuationReason explains a fresh or reconstructed
// continuation mode. The retire reasons are shared with
// AutomationTargetRetiredReason by value.
type AutomationRunContinuationReason string

const (
	AutomationRunContinuationReasonNoGeneration         AutomationRunContinuationReason = "no_generation"
	AutomationRunContinuationReasonKillSwitch           AutomationRunContinuationReason = "kill_switch"
	AutomationRunContinuationReasonSessionUnavailable   AutomationRunContinuationReason = "session_unavailable"
	AutomationRunContinuationReasonNotResumable         AutomationRunContinuationReason = "not_resumable"
	AutomationRunContinuationReasonAgentConfigChanged   AutomationRunContinuationReason = "agent_config_changed"
	AutomationRunContinuationReasonIdentityChanged      AutomationRunContinuationReason = "identity_changed"
	AutomationRunContinuationReasonBaseRetargeted       AutomationRunContinuationReason = "base_retargeted"
	AutomationRunContinuationReasonTurnLimit            AutomationRunContinuationReason = "turn_limit"
	AutomationRunContinuationReasonSnapshotTooLarge     AutomationRunContinuationReason = "snapshot_too_large"
	AutomationRunContinuationReasonUnsupportedWorkspace AutomationRunContinuationReason = "unsupported_workspace"
	AutomationRunContinuationReasonAwaitingInput        AutomationRunContinuationReason = "awaiting_input"
	AutomationRunContinuationReasonSnapshotMissing      AutomationRunContinuationReason = "snapshot_missing"
	AutomationRunContinuationReasonRestoreFailed        AutomationRunContinuationReason = "restore_failed"
	AutomationRunContinuationReasonSandboxDestroyed     AutomationRunContinuationReason = "sandbox_destroyed"
)

func (r AutomationRunContinuationReason) Validate() error {
	switch r {
	case AutomationRunContinuationReasonNoGeneration, AutomationRunContinuationReasonKillSwitch,
		AutomationRunContinuationReasonSessionUnavailable, AutomationRunContinuationReasonNotResumable,
		AutomationRunContinuationReasonAgentConfigChanged, AutomationRunContinuationReasonIdentityChanged,
		AutomationRunContinuationReasonBaseRetargeted, AutomationRunContinuationReasonTurnLimit,
		AutomationRunContinuationReasonSnapshotTooLarge, AutomationRunContinuationReasonUnsupportedWorkspace,
		AutomationRunContinuationReasonAwaitingInput, AutomationRunContinuationReasonSnapshotMissing,
		AutomationRunContinuationReasonRestoreFailed, AutomationRunContinuationReasonSandboxDestroyed:
		return nil
	default:
		return fmt.Errorf("invalid automation run continuation reason: %q", r)
	}
}

// ContinuationReasonForRetirement maps a retire reason onto the run's
// continuation reason for the fresh generation that follows it.
func ContinuationReasonForRetirement(reason AutomationTargetRetiredReason) AutomationRunContinuationReason {
	return AutomationRunContinuationReason(reason)
}

// AutomationRunDispatchState is the per-target dispatch state of a run. Nil
// on per-run rows and on rows created before continuity existed.
type AutomationRunDispatchState string

const (
	AutomationRunDispatchWaiting   AutomationRunDispatchState = "waiting"
	AutomationRunDispatchExecuting AutomationRunDispatchState = "executing"
	AutomationRunDispatchDone      AutomationRunDispatchState = "done"
)

func (s AutomationRunDispatchState) Validate() error {
	switch s {
	case AutomationRunDispatchWaiting, AutomationRunDispatchExecuting, AutomationRunDispatchDone:
		return nil
	default:
		return fmt.Errorf("invalid automation run dispatch state: %q", s)
	}
}

// AutomationRunWaitReason explains why a run is waiting.
type AutomationRunWaitReason string

const (
	AutomationRunWaitTargetBusy AutomationRunWaitReason = "target_busy"
)

func (r AutomationRunWaitReason) Validate() error {
	switch r {
	case AutomationRunWaitTargetBusy:
		return nil
	default:
		return fmt.Errorf("invalid automation run wait reason: %q", r)
	}
}

// AutomationRunOutcomeReason is the terminal result of a per-target run,
// stored separately from the continuation reason.
type AutomationRunOutcomeReason string

const (
	AutomationRunOutcomeTurnCompleted         AutomationRunOutcomeReason = "turn_completed"
	AutomationRunOutcomeHeadLookupDegraded    AutomationRunOutcomeReason = "head_lookup_degraded"
	AutomationRunOutcomeAgentFailed           AutomationRunOutcomeReason = "agent_failed"
	AutomationRunOutcomeCancelled             AutomationRunOutcomeReason = "cancelled"
	AutomationRunOutcomeAwaitingInput         AutomationRunOutcomeReason = "awaiting_input"
	AutomationRunOutcomeRetriesExhausted      AutomationRunOutcomeReason = "retries_exhausted"
	AutomationRunOutcomeStaleHead             AutomationRunOutcomeReason = "stale_head"
	AutomationRunOutcomeDuplicateHead         AutomationRunOutcomeReason = "duplicate_head"
	AutomationRunOutcomeSuperseded            AutomationRunOutcomeReason = "superseded"
	AutomationRunOutcomeWaitTimeout           AutomationRunOutcomeReason = "wait_timeout"
	AutomationRunOutcomeWaitOverflow          AutomationRunOutcomeReason = "wait_overflow"
	AutomationRunOutcomePRClosed              AutomationRunOutcomeReason = "pr_closed"
	AutomationRunOutcomeRepositoryUnavailable AutomationRunOutcomeReason = "repository_unavailable"
)

func (r AutomationRunOutcomeReason) Validate() error {
	switch r {
	case AutomationRunOutcomeTurnCompleted, AutomationRunOutcomeHeadLookupDegraded,
		AutomationRunOutcomeAgentFailed, AutomationRunOutcomeCancelled, AutomationRunOutcomeAwaitingInput,
		AutomationRunOutcomeRetriesExhausted, AutomationRunOutcomeStaleHead, AutomationRunOutcomeDuplicateHead,
		AutomationRunOutcomeSuperseded, AutomationRunOutcomeWaitTimeout, AutomationRunOutcomeWaitOverflow,
		AutomationRunOutcomePRClosed, AutomationRunOutcomeRepositoryUnavailable:
		return nil
	default:
		return fmt.Errorf("invalid automation run outcome reason: %q", r)
	}
}

// RunStatus is the single status mapping shared by the store, the API, and
// the tests: skipped for head and lifecycle outcomes, failed for timeouts,
// overflow, and every agent-side failure, completed for a finished turn.
// head_lookup_degraded is a flag rather than an outcome; a run that carries
// it as its outcome is treated as completed.
func (r AutomationRunOutcomeReason) RunStatus() AutomationRunStatus {
	switch r {
	case AutomationRunOutcomeTurnCompleted, AutomationRunOutcomeHeadLookupDegraded:
		return AutomationRunStatusCompleted
	case AutomationRunOutcomeSuperseded, AutomationRunOutcomeDuplicateHead,
		AutomationRunOutcomeStaleHead, AutomationRunOutcomePRClosed:
		return AutomationRunStatusSkipped
	default:
		return AutomationRunStatusFailed
	}
}

// AutomationRunResultOutcome is the attempt-end outcome recorded by the
// orchestrator's result marker.
type AutomationRunResultOutcome string

const (
	AutomationRunResultTurnCompleted AutomationRunResultOutcome = "turn_completed"
	AutomationRunResultAgentFailed   AutomationRunResultOutcome = "agent_failed"
	AutomationRunResultCancelled     AutomationRunResultOutcome = "cancelled"
	AutomationRunResultAwaitingInput AutomationRunResultOutcome = "awaiting_input"
)

func (o AutomationRunResultOutcome) Validate() error {
	switch o {
	case AutomationRunResultTurnCompleted, AutomationRunResultAgentFailed,
		AutomationRunResultCancelled, AutomationRunResultAwaitingInput:
		return nil
	default:
		return fmt.Errorf("invalid automation run result outcome: %q", o)
	}
}

// OutcomeReason maps a marker outcome onto the run's terminal outcome reason.
func (o AutomationRunResultOutcome) OutcomeReason() AutomationRunOutcomeReason {
	return AutomationRunOutcomeReason(o)
}

// AutomationRunResult is the run-keyed result marker written by the
// orchestrator at every attempt end, in the same transaction as the session
// status write for that end. One row per run; replaced only by a newer
// attempt's write.
type AutomationRunResult struct {
	RunID                 uuid.UUID                  `db:"run_id" json:"run_id"`
	OrgID                 uuid.UUID                  `db:"org_id" json:"org_id"`
	Attempt               int                        `db:"attempt" json:"attempt"`
	AttemptLockToken      uuid.UUID                  `db:"attempt_lock_token" json:"-"`
	ThreadID              uuid.UUID                  `db:"thread_id" json:"thread_id"`
	TurnNumber            int                        `db:"turn_number" json:"turn_number"`
	Outcome               AutomationRunResultOutcome `db:"outcome" json:"outcome"`
	ReviewComplete        bool                       `db:"review_complete" json:"review_complete"`
	CheckpointKey         *string                    `db:"checkpoint_key" json:"-"`
	CheckpointPublished   bool                       `db:"checkpoint_published" json:"checkpoint_published"`
	CheckpointHeadSHA     *string                    `db:"checkpoint_head_sha" json:"checkpoint_head_sha,omitempty"`
	NativeContext         bool                       `db:"native_context" json:"native_context"`
	DependencyFingerprint *string                    `db:"dependency_fingerprint" json:"-"`
	AgentSessionID        *string                    `db:"agent_session_id" json:"-"`
	RecordedAt            time.Time                  `db:"recorded_at" json:"recorded_at"`
}

// CheckpointProvenance is what PublishCheckpointWithProvenance records on
// the owning generation together with the checkpoint it installs on the
// session (design doc 125, "Checkpoint coherence"): the key, the head the
// workspace was at, the dependency input fingerprint that applies to that
// workspace, and whether the turn that produced it completed its review.
type CheckpointProvenance struct {
	GenerationID          uuid.UUID
	HeadSHA               string
	DependencyFingerprint *string
	ReviewComplete        bool
}

// AutomationTurnWorkspace is what workspace preparation records on a run:
// the merge-base the delta falls back to, the node the turn ran on, the
// snapshot size when the turn restored a checkpoint, and the time from the
// attempt's start to a ready workspace on every path (snapshot restore, or
// clone and checkout for a rebuilt workspace).
type AutomationTurnWorkspace struct {
	BaseSHA              string
	WorkerNodeID         string
	RestoreSnapshotBytes *int64
	RestoreDurationMS    *int
}

// AutomationTurnSummary is a completed run's review summary, embedded as
// data in a later turn's prompt.
type AutomationTurnSummary struct {
	RunID       uuid.UUID
	HeadSHA     string
	TurnNumber  int
	Summary     string
	CompletedAt string
}

// SessionAutomationOwner describes the generation that owns a session
// (design doc 125, "Automation-owned sessions"). A session with an owner
// accepts no human turn: its thread belongs to the automation's next turn,
// and a message sent into it would either be lost or race that turn. The
// person's way out is the target's Reset action, which retires the
// generation and hands the session back.
type SessionAutomationOwner struct {
	AutomationID   uuid.UUID `json:"automation_id"`
	TargetID       uuid.UUID `json:"target_id"`
	GenerationID   uuid.UUID `json:"generation_id"`
	ResetURL       string    `json:"reset_url"`
	ReleasePending bool      `json:"release_pending"`
}

// ErrSessionAutomationOwned is what every human-entry path on an owned
// session returns; handlers map it to 409 SESSION_AUTOMATION_OWNED with the
// owner as details.
var ErrSessionAutomationOwned = errors.New("session is owned by an automation target")

// SessionAutomationOwnedError carries the owner alongside the sentinel, so
// a handler can answer with the target and its reset link.
type SessionAutomationOwnedError struct {
	Owner SessionAutomationOwner
}

func (e *SessionAutomationOwnedError) Error() string {
	return ErrSessionAutomationOwned.Error()
}

func (e *SessionAutomationOwnedError) Unwrap() error { return ErrSessionAutomationOwned }

// SessionAutomationResetURL is the path that hands an owned session back.
func SessionAutomationResetURL(automationID, targetID uuid.UUID) string {
	return fmt.Sprintf("/api/v1/automations/%s/targets/%s/reset", automationID, targetID)
}
