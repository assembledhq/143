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
	Name      string  `json:"name"`
	Score     float64 `json:"score"`
	Pass      bool    `json:"pass"`
	Details   string  `json:"details,omitempty"`
	Reasoning string  `json:"reasoning,omitempty"`
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
	EvalRunStatusCompleted EvalRunStatus = "completed"
	EvalRunStatusFailed    EvalRunStatus = "failed"
)

// EvalRun represents a single execution of an eval task with specific configuration.
type EvalRun struct {
	ID      uuid.UUID  `db:"id" json:"id"`
	TaskID  uuid.UUID  `db:"task_id" json:"task_id"`
	OrgID   uuid.UUID  `db:"org_id" json:"org_id"`
	BatchID *uuid.UUID `db:"batch_id" json:"batch_id,omitempty"`

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
	Runs []EvalRun `json:"runs"`
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
	PRNumber          int                `json:"pr_number"`
	PRTitle           string             `json:"pr_title"`
	BaseCommitSHA     string             `json:"base_commit_sha"`
	SolutionCommitSHA string             `json:"solution_commit_sha"`
	SolutionDiff      string             `json:"solution_diff"`
	IssueDescription  string             `json:"issue_description"`
	ScoringCriteria   []ScoringCriterion `json:"scoring_criteria"`
	Complexity        EvalComplexity     `json:"complexity"`
	FitnessScore      float64            `json:"fitness_score"`
	FitnessReasoning  string             `json:"fitness_reasoning"`
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
	CreatedBy    *uuid.UUID          `db:"created_by" json:"created_by,omitempty"`
	CreatedAt    time.Time           `db:"created_at" json:"created_at"`
	CompletedAt  *time.Time          `db:"completed_at" json:"completed_at,omitempty"`
	ErrorMessage *string             `db:"error_message" json:"error_message,omitempty"`
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
