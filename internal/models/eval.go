package models

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// EvalTaskSource indicates how an eval task was created.
type EvalTaskSource string

const (
	EvalTaskSourceManual         EvalTaskSource = "manual"
	EvalTaskSourcePRBootstrap    EvalTaskSource = "pr_bootstrap"
	EvalTaskSourceFailureDerived EvalTaskSource = "failure_derived"
)

func (s EvalTaskSource) Validate() error {
	switch s {
	case EvalTaskSourceManual, EvalTaskSourcePRBootstrap, EvalTaskSourceFailureDerived:
		return nil
	default:
		return fmt.Errorf("invalid EvalTaskSource: %q", s)
	}
}

// EvalComplexity classifies the difficulty of an eval task.
type EvalComplexity string

const (
	EvalComplexityTrivial  EvalComplexity = "trivial"
	EvalComplexitySimple   EvalComplexity = "simple"
	EvalComplexityModerate EvalComplexity = "moderate"
	EvalComplexityComplex  EvalComplexity = "complex"
)

func (c EvalComplexity) Validate() error {
	switch c {
	case EvalComplexityTrivial, EvalComplexitySimple, EvalComplexityModerate, EvalComplexityComplex:
		return nil
	default:
		return fmt.Errorf("invalid EvalComplexity: %q", c)
	}
}

// GraderType identifies the type of scoring criterion grader.
type GraderType string

const (
	GraderTypeCodeCheck GraderType = "code_check"
	GraderTypeLLMJudge  GraderType = "llm_judge"
)

func (g GraderType) Validate() error {
	switch g {
	case GraderTypeCodeCheck, GraderTypeLLMJudge:
		return nil
	default:
		return fmt.Errorf("invalid GraderType: %q", g)
	}
}

// ScoringCriterion defines one dimension of evaluation for an eval task.
type ScoringCriterion struct {
	Name         string          `json:"name"`
	Notes        string          `json:"notes"`
	GraderType   GraderType      `json:"grader_type"`
	GraderConfig json.RawMessage `json:"grader_config,omitempty"`
	Weight       float64         `json:"weight"`
	Required     bool            `json:"required"`
}

// CodeCheckConfig is the grader config for code_check criteria.
type CodeCheckConfig struct {
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// LLMJudgeConfig is the grader config for llm_judge criteria.
type LLMJudgeConfig struct {
	Model  string `json:"model,omitempty"`
	Output string `json:"output,omitempty"` // "pass_fail" (default) or "score"
}

// CriterionResult captures the outcome of evaluating one scoring criterion.
type CriterionResult struct {
	Name       string     `json:"name"`
	GraderType GraderType `json:"grader_type,omitempty"`
	Score      float64    `json:"score"`
	Pass       bool       `json:"pass"`
	Details    string     `json:"details,omitempty"`
	Reasoning  string     `json:"reasoning,omitempty"`
}

// EvalTask is a reproducible challenge: given a codebase at a specific commit,
// a problem description, and scoring criteria, can the agent produce a solution?
type EvalTask struct {
	ID          uuid.UUID `db:"id" json:"id"`
	OrgID       uuid.UUID `db:"org_id" json:"org_id"`
	RepoID      uuid.UUID `db:"repo_id" json:"repo_id"`
	Name        string    `db:"name" json:"name"`
	Description string    `db:"description" json:"description"`

	// Codebase snapshot
	BaseCommitSHA     string  `db:"base_commit_sha" json:"base_commit_sha"`
	SolutionCommitSHA *string `db:"solution_commit_sha" json:"solution_commit_sha,omitempty"`
	SolutionDiff      *string `db:"solution_diff" json:"solution_diff,omitempty"`

	// Problem definition
	IssueDescription string          `db:"issue_description" json:"issue_description"`
	IssueContext     json.RawMessage `db:"issue_context" json:"issue_context"`

	// Input configuration (frozen references, see doc 43)
	ServerDeploySHA      *string         `db:"server_deploy_sha" json:"server_deploy_sha,omitempty"`
	PMDocumentSetPinID   *uuid.UUID      `db:"pm_document_set_pin_id" json:"pm_document_set_pin_id,omitempty"`
	OrgSettingsVersionID *uuid.UUID      `db:"org_settings_version_id" json:"org_settings_version_id,omitempty"`
	MemorySnapshot       json.RawMessage `db:"memory_snapshot" json:"memory_snapshot,omitempty"`
	SandboxImageDigest   *string         `db:"sandbox_image_digest" json:"sandbox_image_digest,omitempty"`
	ContextOverrides     json.RawMessage `db:"context_overrides" json:"context_overrides"`

	// Scoring
	ScoringCriteria json.RawMessage `db:"scoring_criteria" json:"scoring_criteria"`
	PassThreshold   float64         `db:"pass_threshold" json:"pass_threshold"`

	// Metadata
	Source         EvalTaskSource `db:"source" json:"source"`
	SourcePRNumber *int           `db:"source_pr_number" json:"source_pr_number,omitempty"`
	Complexity     EvalComplexity `db:"complexity" json:"complexity"`
	Tags           []string       `db:"tags" json:"tags"`
	SnapshotBroken bool           `db:"snapshot_broken" json:"snapshot_broken"`
	CreatedBy      *uuid.UUID     `db:"created_by" json:"created_by,omitempty"`
	CreatedAt      time.Time      `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time      `db:"updated_at" json:"updated_at"`
	ArchivedAt     *time.Time     `db:"archived_at" json:"archived_at,omitempty"`
}

// EvalRunStatus tracks the lifecycle of an eval run.
type EvalRunStatus string

const (
	EvalRunStatusPending   EvalRunStatus = "pending"
	EvalRunStatusRunning   EvalRunStatus = "running"
	EvalRunStatusGrading   EvalRunStatus = "grading"
	EvalRunStatusCompleted EvalRunStatus = "completed"
	EvalRunStatusFailed    EvalRunStatus = "failed"
)

// EvalRun represents a single execution of an eval task with specific configuration.
type EvalRun struct {
	ID        uuid.UUID  `db:"id" json:"id"`
	TaskID    uuid.UUID  `db:"task_id" json:"task_id"`
	OrgID     uuid.UUID  `db:"org_id" json:"org_id"`
	BatchID   *uuid.UUID `db:"batch_id" json:"batch_id,omitempty"`
	SessionID *uuid.UUID `db:"session_id" json:"session_id,omitempty"`
	ThreadID  *uuid.UUID `db:"thread_id" json:"thread_id,omitempty"`

	// Configuration used
	InputManifest      json.RawMessage `db:"input_manifest" json:"input_manifest,omitempty"`
	Model              string          `db:"model" json:"model"`
	ServerDeploySHA    *string         `db:"server_deploy_sha" json:"server_deploy_sha,omitempty"`
	PMDocumentSetPinID *uuid.UUID      `db:"pm_document_set_pin_id" json:"pm_document_set_pin_id,omitempty"`
	ConfigRef          *string         `db:"config_ref" json:"config_ref,omitempty"`
	ContextOverrides   json.RawMessage `db:"context_overrides" json:"context_overrides"`

	// Output
	AgentDiff  *string         `db:"agent_diff" json:"agent_diff,omitempty"`
	AgentTrace json.RawMessage `db:"agent_trace" json:"agent_trace,omitempty"`
	TokenUsage json.RawMessage `db:"token_usage" json:"token_usage,omitempty"`

	// Scoring
	CriterionResults json.RawMessage `db:"criterion_results" json:"criterion_results,omitempty"`
	FinalScore       *float64        `db:"final_score" json:"final_score,omitempty"`
	Passed           *bool           `db:"passed" json:"passed,omitempty"`

	// Metadata
	Status          EvalRunStatus `db:"status" json:"status"`
	DurationSeconds *int          `db:"duration_seconds" json:"duration_seconds,omitempty"`
	SandboxID       *string       `db:"sandbox_id" json:"sandbox_id,omitempty"`
	StartedAt       *time.Time    `db:"started_at" json:"started_at,omitempty"`
	CompletedAt     *time.Time    `db:"completed_at" json:"completed_at,omitempty"`
	ErrorMessage    *string       `db:"error_message" json:"error_message,omitempty"`
	CreatedAt       time.Time     `db:"created_at" json:"created_at"`
}

// EvalBatchStatus tracks the lifecycle of a batch of eval runs.
type EvalBatchStatus string

const (
	EvalBatchStatusPending   EvalBatchStatus = "pending"
	EvalBatchStatusRunning   EvalBatchStatus = "running"
	EvalBatchStatusCompleted EvalBatchStatus = "completed"
	EvalBatchStatusFailed    EvalBatchStatus = "failed"
)

// EvalBatch groups multiple eval runs for comparison across tasks × configs.
type EvalBatch struct {
	ID          uuid.UUID       `db:"id" json:"id"`
	OrgID       uuid.UUID       `db:"org_id" json:"org_id"`
	Name        string          `db:"name" json:"name"`
	Status      EvalBatchStatus `db:"status" json:"status"`
	TaskCount   int             `db:"task_count" json:"task_count"`
	RunCount    int             `db:"run_count" json:"run_count"`
	CreatedBy   *uuid.UUID      `db:"created_by" json:"created_by,omitempty"`
	CreatedAt   time.Time       `db:"created_at" json:"created_at"`
	CompletedAt *time.Time      `db:"completed_at" json:"completed_at,omitempty"`
}

// EvalBatchDetail enriches a batch with its runs for the comparison matrix.
type EvalBatchDetail struct {
	EvalBatch
	Runs          []EvalRun                 `json:"runs"`
	GateDecisions []EvalReleaseGateDecision `json:"gate_decisions,omitempty"`
}

// EvalRunResult holds the fields written back when an eval run completes.
type EvalRunResult struct {
	Status           EvalRunStatus   `json:"status"`
	AgentDiff        *string         `json:"agent_diff,omitempty"`
	AgentTrace       json.RawMessage `json:"agent_trace,omitempty"`
	TokenUsage       json.RawMessage `json:"token_usage,omitempty"`
	CriterionResults json.RawMessage `json:"criterion_results,omitempty"`
	FinalScore       *float64        `json:"final_score,omitempty"`
	Passed           *bool           `json:"passed,omitempty"`
	DurationSeconds  *int            `json:"duration_seconds,omitempty"`
	SandboxID        *string         `json:"sandbox_id,omitempty"`
	ErrorMessage     *string         `json:"error_message,omitempty"`
	InputManifest    json.RawMessage `json:"input_manifest,omitempty"`
}

// EvalTaskListFilters holds optional filters for listing eval tasks.
type EvalTaskListFilters struct {
	Source     *EvalTaskSource
	Complexity *EvalComplexity
	Archived   *bool // nil = non-archived, true = archived, false = non-archived
	Tags       []string
	Cursor     *time.Time
	Limit      int
}

// --- Bootstrap ---

// EvalBootstrapCandidate represents a proposed eval task from PR history scanning.
type EvalBootstrapCandidate struct {
	PRNumber           int                     `json:"pr_number"`
	PRTitle            string                  `json:"pr_title"`
	BaseCommitSHA      string                  `json:"base_commit_sha"`
	SolutionCommitSHA  string                  `json:"solution_commit_sha"`
	SolutionDiff       string                  `json:"solution_diff"`
	IssueDescription   string                  `json:"issue_description"`
	ScoringCriteria    []ScoringCriterion      `json:"scoring_criteria"`
	Complexity         EvalComplexity          `json:"complexity"`
	FitnessScore       float64                 `json:"fitness_score"`
	FitnessReasoning   string                  `json:"fitness_reasoning"`
	Evidence           json.RawMessage         `json:"evidence,omitempty"`
	Warnings           []string                `json:"warnings,omitempty"`
	ValidationWarnings []EvalValidationWarning `json:"validation_warnings,omitempty"`
}

type EvalValidationWarning struct {
	Code       string `json:"code"`
	Severity   string `json:"severity"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
	Blocking   bool   `json:"blocking"`
}

// EvalBootstrapCandidateStatus tracks review state for a normalized bootstrap candidate.
type EvalBootstrapCandidateStatus string

const (
	EvalBootstrapCandidateStatusProposed      EvalBootstrapCandidateStatus = "proposed"
	EvalBootstrapCandidateStatusAccepted      EvalBootstrapCandidateStatus = "accepted"
	EvalBootstrapCandidateStatusRejected      EvalBootstrapCandidateStatus = "rejected"
	EvalBootstrapCandidateStatusNeedsRevision EvalBootstrapCandidateStatus = "needs_revision"
)

func (s EvalBootstrapCandidateStatus) Validate() error {
	switch s {
	case EvalBootstrapCandidateStatusProposed, EvalBootstrapCandidateStatusAccepted, EvalBootstrapCandidateStatusRejected, EvalBootstrapCandidateStatusNeedsRevision:
		return nil
	default:
		return fmt.Errorf("invalid EvalBootstrapCandidateStatus: %q", s)
	}
}

// EvalBootstrapCandidateRow is the normalized persistence wrapper around a
// candidate payload proposed by an eval bootstrap session.
type EvalBootstrapCandidateRow struct {
	ID                uuid.UUID                    `db:"id" json:"id"`
	OrgID             uuid.UUID                    `db:"org_id" json:"org_id"`
	BootstrapRunID    uuid.UUID                    `db:"bootstrap_run_id" json:"bootstrap_run_id"`
	SessionID         uuid.UUID                    `db:"session_id" json:"session_id"`
	ThreadID          *uuid.UUID                   `db:"thread_id" json:"thread_id,omitempty"`
	RepoID            uuid.UUID                    `db:"repo_id" json:"repo_id"`
	CandidateIndex    int                          `db:"candidate_index" json:"candidate_index"`
	PRNumber          int                          `db:"pr_number" json:"pr_number"`
	PRTitle           string                       `db:"pr_title" json:"pr_title"`
	BaseCommitSHA     string                       `db:"base_commit_sha" json:"base_commit_sha"`
	SolutionCommitSHA string                       `db:"solution_commit_sha" json:"solution_commit_sha"`
	SolutionDiff      string                       `db:"solution_diff" json:"solution_diff"`
	IssueDescription  string                       `db:"issue_description" json:"issue_description"`
	ScoringCriteria   json.RawMessage              `db:"scoring_criteria" json:"scoring_criteria"`
	Complexity        EvalComplexity               `db:"complexity" json:"complexity"`
	FitnessScore      float64                      `db:"fitness_score" json:"fitness_score"`
	FitnessReasoning  string                       `db:"fitness_reasoning" json:"fitness_reasoning"`
	Evidence          json.RawMessage              `db:"evidence" json:"evidence,omitempty"`
	Warnings          []string                     `db:"warnings" json:"warnings,omitempty"`
	Payload           json.RawMessage              `db:"payload" json:"payload"`
	Status            EvalBootstrapCandidateStatus `db:"status" json:"status"`
	RejectionReason   *string                      `db:"rejection_reason" json:"rejection_reason,omitempty"`
	CreatedByTool     string                       `db:"created_by_tool" json:"created_by_tool"`
	ReviewedBy        *uuid.UUID                   `db:"reviewed_by" json:"reviewed_by,omitempty"`
	ReviewedAt        *time.Time                   `db:"reviewed_at" json:"reviewed_at,omitempty"`
	AcceptedTaskID    *uuid.UUID                   `db:"accepted_task_id" json:"accepted_task_id,omitempty"`
	CreatedAt         time.Time                    `db:"created_at" json:"created_at"`
}

func (r EvalBootstrapCandidateRow) Candidate() EvalBootstrapCandidate {
	var criteria []ScoringCriterion
	if len(r.ScoringCriteria) > 0 {
		_ = json.Unmarshal(r.ScoringCriteria, &criteria)
	}
	candidate := EvalBootstrapCandidate{
		PRNumber:          r.PRNumber,
		PRTitle:           r.PRTitle,
		BaseCommitSHA:     r.BaseCommitSHA,
		SolutionCommitSHA: r.SolutionCommitSHA,
		SolutionDiff:      r.SolutionDiff,
		IssueDescription:  r.IssueDescription,
		ScoringCriteria:   criteria,
		Complexity:        r.Complexity,
		FitnessScore:      r.FitnessScore,
		FitnessReasoning:  r.FitnessReasoning,
		Evidence:          r.Evidence,
		Warnings:          r.Warnings,
	}
	if len(r.Payload) > 0 {
		var payload EvalBootstrapCandidate
		if err := json.Unmarshal(r.Payload, &payload); err == nil {
			candidate.ValidationWarnings = payload.ValidationWarnings
		}
	}
	return candidate
}

// EvalBootstrapStatus tracks the lifecycle of a bootstrap scan.
type EvalBootstrapStatus string

const (
	EvalBootstrapStatusPending   EvalBootstrapStatus = "pending"
	EvalBootstrapStatusRunning   EvalBootstrapStatus = "running"
	EvalBootstrapStatusCompleted EvalBootstrapStatus = "completed"
	EvalBootstrapStatusFailed    EvalBootstrapStatus = "failed"
)

// EvalBootstrapRun represents a single PR history scanning session.
type EvalBootstrapRun struct {
	ID           uuid.UUID           `db:"id" json:"id"`
	OrgID        uuid.UUID           `db:"org_id" json:"org_id"`
	RepoID       uuid.UUID           `db:"repo_id" json:"repo_id"`
	Status       EvalBootstrapStatus `db:"status" json:"status"`
	Candidates   json.RawMessage     `db:"candidates" json:"candidates,omitempty"`
	SessionID    *uuid.UUID          `db:"session_id" json:"session_id,omitempty"`
	ThreadID     *uuid.UUID          `db:"thread_id" json:"thread_id,omitempty"`
	CreatedBy    *uuid.UUID          `db:"created_by" json:"created_by,omitempty"`
	CreatedAt    time.Time           `db:"created_at" json:"created_at"`
	CompletedAt  *time.Time          `db:"completed_at" json:"completed_at,omitempty"`
	ErrorMessage *string             `db:"error_message" json:"error_message,omitempty"`
}

type EvalDatasetType string

const (
	EvalDatasetTypeGolden      EvalDatasetType = "golden"
	EvalDatasetTypeShadow      EvalDatasetType = "shadow"
	EvalDatasetTypeAdversarial EvalDatasetType = "adversarial"
)

func (t EvalDatasetType) Validate() error {
	switch t {
	case EvalDatasetTypeGolden, EvalDatasetTypeShadow, EvalDatasetTypeAdversarial:
		return nil
	default:
		return fmt.Errorf("invalid EvalDatasetType: %q", t)
	}
}

type EvalDatasetStatus string

const (
	EvalDatasetStatusActive   EvalDatasetStatus = "active"
	EvalDatasetStatusArchived EvalDatasetStatus = "archived"
)

func (s EvalDatasetStatus) Validate() error {
	switch s {
	case EvalDatasetStatusActive, EvalDatasetStatusArchived:
		return nil
	default:
		return fmt.Errorf("invalid EvalDatasetStatus: %q", s)
	}
}

type EvalDataset struct {
	ID              uuid.UUID         `db:"id" json:"id"`
	OrgID           uuid.UUID         `db:"org_id" json:"org_id"`
	RepositoryID    *uuid.UUID        `db:"repository_id" json:"repository_id,omitempty"`
	Name            string            `db:"name" json:"name"`
	DatasetType     EvalDatasetType   `db:"dataset_type" json:"dataset_type"`
	Status          EvalDatasetStatus `db:"status" json:"status"`
	Description     string            `db:"description" json:"description"`
	SourceSummary   string            `db:"source_summary" json:"source_summary"`
	CreatedByUserID *uuid.UUID        `db:"created_by_user_id" json:"created_by_user_id,omitempty"`
	CreatedAt       time.Time         `db:"created_at" json:"created_at"`
	UpdatedAt       time.Time         `db:"updated_at" json:"updated_at"`
	TaskCount       int               `db:"task_count" json:"task_count"`
}

type EvalDatasetTask struct {
	ID        uuid.UUID `db:"id" json:"id"`
	OrgID     uuid.UUID `db:"org_id" json:"org_id"`
	DatasetID uuid.UUID `db:"dataset_id" json:"dataset_id"`
	TaskID    uuid.UUID `db:"task_id" json:"task_id"`
	SliceKey  string    `db:"slice_key" json:"slice_key"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

type EvalReleaseGate struct {
	ID                  uuid.UUID       `db:"id" json:"id"`
	OrgID               uuid.UUID       `db:"org_id" json:"org_id"`
	GateName            string          `db:"gate_name" json:"gate_name"`
	Enabled             bool            `db:"enabled" json:"enabled"`
	DatasetID           *uuid.UUID      `db:"dataset_id" json:"dataset_id,omitempty"`
	MinPassAt1          float64         `db:"min_pass_at_1" json:"min_pass_at_1"`
	MinPassAtK          float64         `db:"min_pass_at_k" json:"min_pass_at_k"`
	MaxPolicyViolations int             `db:"max_policy_violations" json:"max_policy_violations"`
	MaxRegressionDelta  float64         `db:"max_regression_delta" json:"max_regression_delta"`
	CanaryStages        json.RawMessage `db:"canary_stages" json:"canary_stages"`
	RollbackRules       json.RawMessage `db:"rollback_rules" json:"rollback_rules"`
	UpdatedByUserID     *uuid.UUID      `db:"updated_by_user_id" json:"updated_by_user_id,omitempty"`
	Active              bool            `db:"active" json:"active"`
	CreatedAt           time.Time       `db:"created_at" json:"created_at"`
}

type EvalReleaseGateDecisionStatus string

const (
	EvalReleaseGateDecisionPassed EvalReleaseGateDecisionStatus = "passed"
	EvalReleaseGateDecisionFailed EvalReleaseGateDecisionStatus = "failed"
	EvalReleaseGateDecisionNoData EvalReleaseGateDecisionStatus = "no_data"
)

func (s EvalReleaseGateDecisionStatus) Validate() error {
	switch s {
	case EvalReleaseGateDecisionPassed, EvalReleaseGateDecisionFailed, EvalReleaseGateDecisionNoData:
		return nil
	default:
		return fmt.Errorf("invalid EvalReleaseGateDecisionStatus: %q", s)
	}
}

type EvalReleaseGateDecision struct {
	ID        uuid.UUID                     `db:"id" json:"id"`
	OrgID     uuid.UUID                     `db:"org_id" json:"org_id"`
	BatchID   uuid.UUID                     `db:"batch_id" json:"batch_id"`
	GateID    uuid.UUID                     `db:"gate_id" json:"gate_id"`
	Status    EvalReleaseGateDecisionStatus `db:"status" json:"status"`
	Reason    string                        `db:"reason" json:"reason"`
	Metrics   json.RawMessage               `db:"metrics" json:"metrics"`
	CreatedAt time.Time                     `db:"created_at" json:"created_at"`
}

// EvalBatchUpdatedEvent is the lightweight pub/sub signal published whenever
// an eval batch's status or one of its runs' statuses changes. Consumers fetch
// the full EvalBatchDetail via the existing GET handler when they receive an
// event; the payload deliberately does not include the runs array to keep
// fanout cost bounded for large batches.
type EvalBatchUpdatedEvent struct {
	BatchID   uuid.UUID       `json:"batch_id"`
	OrgID     uuid.UUID       `json:"org_id"`
	Status    EvalBatchStatus `json:"status"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// EvalBootstrapUpdatedEvent is the pub/sub signal for bootstrap (PR-history
// scan) status transitions. Mirrors EvalBatchUpdatedEvent — clients fetch the
// full EvalBootstrapRun on receipt.
type EvalBootstrapUpdatedEvent struct {
	BootstrapRunID uuid.UUID           `json:"bootstrap_run_id"`
	OrgID          uuid.UUID           `json:"org_id"`
	Status         EvalBootstrapStatus `json:"status"`
	SessionID      *uuid.UUID          `json:"session_id,omitempty"`
	UpdatedAt      time.Time           `json:"updated_at"`
}
