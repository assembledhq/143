package worker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	ghservice "github.com/assembledhq/143/internal/services/github"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

type runCodeReviewPayload struct {
	OrgID                   uuid.UUID                           `json:"org_id"`
	SessionID               uuid.UUID                           `json:"session_id"`
	MetadataID              uuid.UUID                           `json:"metadata_id"`
	RepositoryID            uuid.UUID                           `json:"repository_id"`
	PullRequestID           uuid.UUID                           `json:"pull_request_id"`
	PolicyID                uuid.UUID                           `json:"policy_id"`
	PolicyVersion           int                                 `json:"policy_version"`
	HeadSHA                 string                              `json:"head_sha"`
	FromFork                bool                                `json:"from_fork"`
	PullRequestAuthor       string                              `json:"pull_request_author,omitempty"`
	PullRequestAuthorTeams  []string                            `json:"-"`
	OutputKey               string                              `json:"review_output_key"`
	RequestedReviewerLogin  string                              `json:"requested_reviewer_login,omitempty"`
	RequestedTeamSlug       string                              `json:"requested_team_slug,omitempty"`
	RequestContext          *codereviewsvc.ReviewRequestContext `json:"request_context,omitempty"`
	PreviousOutputKey       string                              `json:"previous_review_output_key,omitempty"`
	PreviousReviewDecision  *models.CodeReviewDecision          `json:"previous_review_decision,omitempty"`
	PreviousReviewDecidedAt *time.Time                          `json:"previous_review_decided_at,omitempty"`
	PreviousReviewBody      *string                             `json:"previous_review_body,omitempty"`
	ExistingGitHubReviewID  *int64                              `json:"existing_github_review_id,omitempty"`
	ExistingGitHubReviewURL *string                             `json:"existing_github_review_url,omitempty"`
	TriggeringDisputeID     *uuid.UUID                          `json:"triggering_dispute_id,omitempty"`
}

const codeReviewRawOutputInlineLimit = 32 * 1024
const codeReviewOrchestratorSynthesisRepairLimit = 1
const codeReviewOrchestratorFindingLimit = 50
const codeReviewOrchestratorHumanReviewReasonLimit = 10
const codeReviewOrchestratorFindingSummaryLimit = 500
const codeReviewOrchestratorFindingBodyLimit = 2_000
const codeReviewOrchestratorHumanReviewSummaryLimit = 500

type codeReviewDescriptionEvaluation struct {
	Passed               bool
	RequirementSummaries []string
}

type codeReviewDescriptionAssessmentStatus string

const (
	codeReviewDescriptionAssessmentSatisfied     codeReviewDescriptionAssessmentStatus = "satisfied"
	codeReviewDescriptionAssessmentNotApplicable codeReviewDescriptionAssessmentStatus = "not_applicable"
	codeReviewDescriptionAssessmentMissing       codeReviewDescriptionAssessmentStatus = "missing"
)

type codeReviewDescriptionAssessment struct {
	Key           string                                    `json:"key"`
	Status        codeReviewDescriptionAssessmentStatus     `json:"status"`
	EvidenceBasis models.CodeReviewDescriptionEvidenceBasis `json:"evidence_basis"`
	EvidenceIDs   []string                                  `json:"evidence_ids"`
	Reason        string                                    `json:"reason"`
}

type codeReviewOrchestratorFinding struct {
	Severity   models.CodeReviewFindingSeverity   `json:"severity"`
	Confidence models.CodeReviewFindingConfidence `json:"confidence"`
	Path       *string                            `json:"path"`
	StartLine  *int                               `json:"start_line"`
	EndLine    *int                               `json:"end_line"`
	Summary    string                             `json:"summary"`
	Body       string                             `json:"body"`
}

type codeReviewOrchestratorHumanReviewReason struct {
	Code    models.CodeReviewHumanReviewReasonCode `json:"code"`
	Summary string                                 `json:"summary"`
}

type codeReviewOrchestratorSynthesis struct {
	// ApprovalRecommended is retained as a model self-check and prose guard.
	// The backend derives the actual decision from the explicit fields below.
	ApprovalRecommended     bool                                      `json:"approval_recommended"`
	DescriptionAssessments  []codeReviewDescriptionAssessment         `json:"description_assessments"`
	Findings                []codeReviewOrchestratorFinding           `json:"findings"`
	HumanReviewReasons      []codeReviewOrchestratorHumanReviewReason `json:"human_review_reasons"`
	Summary                 string                                    `json:"summary,omitempty"`
	ReviewSummary           string                                    `json:"review_summary,omitempty"`
	RiskNotes               []string                                  `json:"risk_notes,omitempty"`
	ScopeMismatch           bool                                      `json:"scope_mismatch,omitempty"`
	UnresolvedUncertainty   bool                                      `json:"unresolved_uncertainty,omitempty"`
	ReviewerDisagreement    bool                                      `json:"reviewer_disagreement,omitempty"`
	PromptInjectionDetected bool                                      `json:"prompt_injection_detected,omitempty"`
	DescriptionInputHash    string                                    `json:"-"`
}

func newRunCodeReviewHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, _ string, payload json.RawMessage) (handlerErr error) {
		if stores == nil || stores.CodeReviews == nil {
			return fmt.Errorf("code review store unavailable")
		}
		var job runCodeReviewPayload
		if err := json.Unmarshal(payload, &job); err != nil {
			return fmt.Errorf("decode code review job payload: %w", err)
		}
		if job.OrgID == uuid.Nil || job.SessionID == uuid.Nil {
			return fmt.Errorf("org_id and session_id are required")
		}
		registerCodeReviewDeadLetterReconciliation(ctx, stores, services, logger, job)
		defer func() {
			recordCodeReviewAutomaticWait(ctx, stores.CodeReviews, logger, job, handlerErr)
		}()
		metadata, err := stores.CodeReviews.MarkRunning(ctx, job.OrgID, job.SessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				existing, getErr := stores.CodeReviews.GetBySessionID(ctx, job.OrgID, job.SessionID)
				if getErr == nil && codeReviewMetadataTerminal(existing.Status) {
					logger.Info().
						Str("org_id", job.OrgID.String()).
						Str("session_id", job.SessionID.String()).
						Str("status", string(existing.Status)).
						Msg("skipping terminal code review job")
					switch existing.Status {
					case models.CodeReviewSessionStatusCompleted:
						reconcileCodeReviewSessionSuccess(ctx, stores, logger, job)
					case models.CodeReviewSessionStatusStale:
						if cancelErr := cancelActiveCodeReviewThreads(ctx, stores, services, logger, job); cancelErr != nil {
							return cancelErr
						}
						reconcileCodeReviewSessionStale(ctx, stores, logger, job)
					case models.CodeReviewSessionStatusFailed:
						reason := strings.TrimSpace(stringPtrValue(existing.FailureReason))
						if reason == "" {
							reason = "code review failed without usable reviewer output"
						}
						if reconcileErr := reconcileCodeReviewSessionFailure(ctx, stores, job, reason); reconcileErr != nil {
							return reconcileErr
						}
					}
					enqueueCodeReviewStatusCommentSync(ctx, stores, services, logger, job, "terminal")
					return nil
				}
			}
			return fmt.Errorf("mark code review running: %w", err)
		}
		policy, err := stores.CodeReviews.GetPolicyByID(ctx, job.OrgID, job.PolicyID)
		if err != nil {
			return fmt.Errorf("load captured code review policy: %w", err)
		}
		pr, err := stores.PullRequests.GetByID(ctx, job.OrgID, job.PullRequestID)
		if err != nil {
			return fmt.Errorf("load code review pull request: %w", err)
		}
		if cancelled, err := stopCodeReviewIfParentSessionCancelled(ctx, stores, services, logger, job, pr); cancelled || err != nil {
			return err
		}
		// Reviewer and orchestrator waits requeue this job every few seconds. Use
		// their durable result/thread state as a phase checkpoint so polling does
		// not repeat the expensive GitHub preflight. Terminal or inconsistent
		// state falls through, preserving the live refresh before every phase
		// transition and final decision.
		if codeReviewCanRunReviewerThreads(stores) {
			phase, phaseErr := codeReviewInFlightAgentPhase(ctx, stores, job, pr, policy.Config(), metadata)
			if phaseErr != nil {
				return phaseErr
			}
			if phase != codeReviewAgentPhaseNone {
				logger.Debug().
					Str("org_id", job.OrgID.String()).
					Str("session_id", job.SessionID.String()).
					Str("agent_phase", string(phase)).
					Msg("deferring code review without refreshing GitHub while agent work remains in flight")
			}
			switch phase {
			case codeReviewAgentPhaseReviewers:
				if _, err := stores.CodeReviews.SetOperationalPhase(ctx, job.OrgID, job.SessionID, models.CodeReviewPhaseReviewing); err != nil {
					return fmt.Errorf("set code review reviewer phase: %w", err)
				}
				return codeReviewWaitingForReviewers(policy.Config())
			case codeReviewAgentPhaseOrchestrator:
				if _, err := stores.CodeReviews.SetOperationalPhase(ctx, job.OrgID, job.SessionID, models.CodeReviewPhaseSynthesizing); err != nil {
					return fmt.Errorf("set code review synthesis phase: %w", err)
				}
				return codeReviewWaitingForOrchestrator(policy.Config())
			}
		}
		if syncErr := syncCodeReviewPullRequestState(ctx, services, logger, job); syncErr != nil {
			return syncErr
		}
		pr, err = stores.PullRequests.GetByID(ctx, job.OrgID, job.PullRequestID)
		if err != nil {
			return fmt.Errorf("reload code review pull request after sync: %w", err)
		}
		health, err := loadStoredCodeReviewHealth(ctx, stores, job, pr)
		if err != nil {
			return fmt.Errorf("load code review health: %w", err)
		}
		agentResults, err := stores.CodeReviews.ListAgentResults(ctx, job.OrgID, job.SessionID)
		if err != nil {
			return fmt.Errorf("list code review agent results: %w", err)
		}
		findings, err := stores.CodeReviews.ListFindings(ctx, job.OrgID, job.SessionID, false)
		if err != nil {
			return fmt.Errorf("list code review findings: %w", err)
		}
		if codeReviewHeadChanged(job.HeadSHA, pr, health) {
			return supersedeCodeReviewForChangedHead(ctx, stores, services, logger, job, pr, health, "PR head changed after review started")
		}
		changedFiles, changedFilesAvailable, err := loadCodeReviewChangedFiles(ctx, stores, services, job, pr)
		if err != nil {
			return fmt.Errorf("load code review changed files: %w", err)
		}
		visualEvidence, err := captureCodeReviewVisualEvidence(ctx, services, job, pr)
		if err != nil {
			return err
		}
		stableRisk := codeReviewStableDeterministicRisk(policy.Config(), job, pr, changedFiles, changedFilesAvailable)
		if !stableRisk.Acceptable && metadata.FinalReviewBody == nil {
			provisionalBody := models.BuildCodeReviewProvisionalBody(models.CodeReviewFinalReviewInput{
				RiskReasons:       stableRisk.ReasonDetails,
				SessionURL:        codeReviewSessionURL(services.FrontendURL, job.SessionID),
				PolicySettingsURL: codeReviewPolicySettingsURL(services.FrontendURL),
				HeadSHA:           job.HeadSHA,
				AssessedAt:        time.Now().UTC(),
			})
			// A concurrent supersede, stale mark, or cancellation moves the session
			// out of queued/running and makes this update match no rows. Returning
			// the error hands the run back to MarkRunning on retry, which owns the
			// terminal-state reconciliation, rather than letting a session that no
			// longer owns this pull request keep publishing.
			if _, err := stores.CodeReviews.SetProvisionalReviewBody(ctx, job.OrgID, job.SessionID, provisionalBody); err != nil {
				return fmt.Errorf("persist provisional deterministic blockers: %w", err)
			}
			enqueueCodeReviewStatusCommentSync(ctx, stores, services, logger, job, "deterministic")
		}
		stopAfterDeterministicFailure := policy.Config().RiskPolicy.StopAfterDeterministicFailure && codeReviewCanStopBeforeAgentFanout(agentResults)
		if !stableRisk.Acceptable && stopAfterDeterministicFailure && metadata.TriggerSource != models.CodeReviewTriggerSourceAutoPolicy {
			priorEarlyStop, err := stores.CodeReviews.HasPriorDeterministicEarlyStop(ctx, job.OrgID, job.PullRequestID, job.SessionID, job.HeadSHA)
			if err != nil {
				return err
			}
			if priorEarlyStop {
				stopAfterDeterministicFailure = false
				logger.Info().Str("org_id", job.OrgID.String()).Str("session_id", job.SessionID.String()).
					Msg("continuing full code review after explicit same-head request following deterministic early stop")
			}
		}
		if !stableRisk.Acceptable && stopAfterDeterministicFailure {
			if syncErr := syncCodeReviewPullRequestState(ctx, services, logger, job); syncErr != nil {
				return syncErr
			}
			pr, err = stores.PullRequests.GetByID(ctx, job.OrgID, job.PullRequestID)
			if err != nil {
				return fmt.Errorf("reload pull request before deterministic early stop: %w", err)
			}
			health, err = loadStoredCodeReviewHealth(ctx, stores, job, pr)
			if err != nil {
				return fmt.Errorf("reload pull request health before deterministic early stop: %w", err)
			}
			if codeReviewHeadChanged(job.HeadSHA, pr, health) {
				return supersedeCodeReviewForChangedHead(ctx, stores, services, logger, job, pr, health, "PR head changed before deterministic early-stop decision")
			}
			return completeCodeReviewAfterStableDeterministicFailure(ctx, stores, services, logger, job, metadata, policy.Config(), pr, changedFiles, stableRisk)
		}
		if codeReviewCanRunReviewerThreads(stores) {
			if _, err := stores.CodeReviews.SetOperationalPhase(ctx, job.OrgID, job.SessionID, models.CodeReviewPhaseReviewing); err != nil {
				return fmt.Errorf("set code review reviewer phase: %w", err)
			}
			if err := ensureCodeReviewReviewerThreads(ctx, stores, services, logger, job, pr, policy, metadata, changedFiles, visualEvidence); err != nil {
				return err
			}
			if err := harvestCodeReviewReviewerResults(ctx, stores, services, logger, job, policy, metadata, changedFiles); err != nil {
				return err
			}
			agentResults, err = stores.CodeReviews.ListAgentResults(ctx, job.OrgID, job.SessionID)
			if err != nil {
				return fmt.Errorf("list harvested code review agent results: %w", err)
			}
			findings, err = stores.CodeReviews.ListFindings(ctx, job.OrgID, job.SessionID, false)
			if err != nil {
				return fmt.Errorf("list harvested code review findings: %w", err)
			}
			if !codeReviewReviewerRosterTerminal(policy.Config(), agentResults) {
				return codeReviewWaitingForReviewers(policy.Config())
			}
			if cancelled, err := stopCodeReviewIfParentSessionCancelled(ctx, stores, services, logger, job, pr); cancelled || err != nil {
				return err
			}
			if codeReviewReviewerExecutionFailed(policy.Config(), agentResults) {
				return failCodeReviewWithoutReviewerOutput(ctx, stores, services, logger, job, pr, agentResults)
			}
			if _, err := stores.CodeReviews.SetOperationalPhase(ctx, job.OrgID, job.SessionID, models.CodeReviewPhaseSynthesizing); err != nil {
				return fmt.Errorf("set code review synthesis phase: %w", err)
			}
			if err := ensureCodeReviewOrchestratorThread(ctx, stores, services, logger, job, pr, health, policy, metadata, changedFiles, agentResults, findings, visualEvidence); err != nil {
				return err
			}
			if err := harvestCodeReviewOrchestratorResult(ctx, stores, services, logger, job, policy, changedFiles, visualEvidence); err != nil {
				return err
			}
			agentResults, err = stores.CodeReviews.ListAgentResults(ctx, job.OrgID, job.SessionID)
			if err != nil {
				return fmt.Errorf("list synthesized code review agent results: %w", err)
			}
			if !codeReviewOrchestratorTerminal(agentResults) {
				return codeReviewWaitingForOrchestrator(policy.Config())
			}
			findings, err = stores.CodeReviews.ListFindings(ctx, job.OrgID, job.SessionID, false)
			if err != nil {
				return fmt.Errorf("list orchestrator code review findings: %w", err)
			}
		} else {
			if cancelled, err := stopCodeReviewIfParentSessionCancelled(ctx, stores, services, logger, job, pr); cancelled || err != nil {
				return err
			}
			if !codeReviewHasUsableReviewerOutput(agentResults) {
				return failCodeReviewWithoutReviewerOutput(ctx, stores, services, logger, job, pr, agentResults)
			}
		}
		if cancelled, err := stopCodeReviewIfParentSessionCancelled(ctx, stores, services, logger, job, pr); cancelled || err != nil {
			return err
		}
		// Re-read all mutable PR gates immediately before deciding. A description,
		// code, or check event can arrive while reviewer agents are still running;
		// stale coding-agent context and deterministic safeguards are checked below.
		if _, err := stores.CodeReviews.SetOperationalPhase(ctx, job.OrgID, job.SessionID, models.CodeReviewPhaseSyncingGitHub); err != nil {
			return fmt.Errorf("set code review GitHub sync phase: %w", err)
		}
		if syncErr := syncCodeReviewPullRequestState(ctx, services, logger, job); syncErr != nil {
			return syncErr
		}
		pr, err = stores.PullRequests.GetByID(ctx, job.OrgID, job.PullRequestID)
		if err != nil {
			return fmt.Errorf("reload code review pull request before decision: %w", err)
		}
		health, err = loadStoredCodeReviewHealth(ctx, stores, job, pr)
		if err != nil {
			return fmt.Errorf("reload code review health before decision: %w", err)
		}
		if codeReviewHeadChanged(job.HeadSHA, pr, health) {
			return supersedeCodeReviewForChangedHead(ctx, stores, services, logger, job, pr, health, "PR head changed before final recommendation")
		}
		changedFiles, changedFilesAvailable, err = loadCodeReviewChangedFiles(ctx, stores, services, job, pr)
		if err != nil {
			return fmt.Errorf("reload code review changed files before decision: %w", err)
		}
		// Team membership can change while reviewer agents run. Recheck it
		// immediately before the final decision instead of treating the
		// synthesis-time lookup as captured, immutable policy evidence.
		job.PullRequestAuthorTeams, err = resolveCodeReviewAuthorTeams(ctx, stores, services, policy.Config(), job, pr)
		if err != nil {
			return err
		}
		decision, body := evaluateLiveCodeReviewOutcome(liveCodeReviewOutcomeInput{
			Policy:                policy.Config(),
			Job:                   job,
			SessionURL:            codeReviewSessionURL(services.FrontendURL, job.SessionID),
			PolicySettingsURL:     codeReviewPolicySettingsURL(services.FrontendURL),
			PullRequest:           pr,
			Health:                health,
			AgentResults:          agentResults,
			Findings:              findings,
			ChangedFiles:          changedFiles,
			ChangedFilesAvailable: changedFilesAvailable,
			OrchestratorSynthesis: codeReviewOrchestratorSynthesisFromResults(agentResults),
			VisualEvidence:        visualEvidence,
			AssessedAt:            time.Now().UTC(),
		})
		if err := ensureCodeReviewInlineSelection(ctx, stores.CodeReviews, job, findings, changedFiles, policy.Config().InlineCommentLimit); err != nil {
			return fmt.Errorf("select code review inline findings: %w", err)
		}
		if cancelled, err := stopCodeReviewIfParentSessionCancelled(ctx, stores, services, logger, job, pr); cancelled || err != nil {
			return err
		}
		if _, err := stores.CodeReviews.SetOperationalPhase(ctx, job.OrgID, job.SessionID, models.CodeReviewPhasePublishing); err != nil {
			return fmt.Errorf("set code review publishing phase: %w", err)
		}
		submission, submitted, err := submitCodeReviewToGitHub(ctx, stores, services, job, metadata, decision.Decision, body)
		if err != nil {
			return err
		}
		finalReviewBody := body
		if strings.TrimSpace(submission.FinalReviewBody) != "" {
			finalReviewBody = submission.FinalReviewBody
		}
		var additions, deletions *int
		if changedFilesAvailable {
			additionCount, deletionCount := codeReviewLineChanges(changedFiles)
			additions = &additionCount
			deletions = &deletionCount
		}
		removeCodeReviewRequestedReviewer(ctx, stores, services, logger, job, pr)
		if _, err := stores.CodeReviews.CompleteReview(ctx, job.OrgID, db.CompleteCodeReviewParams{
			SessionID:         job.SessionID,
			Decision:          decision.Decision,
			Acceptable:        decision.Acceptable,
			GitHubReviewID:    submission.GitHubReviewID,
			GitHubReviewURL:   submission.GitHubReviewURL,
			FinalReviewBody:   finalReviewBody,
			Additions:         additions,
			Deletions:         deletions,
			RiskReasonDetails: decision.RiskReasonDetails,
		}); err != nil {
			return fmt.Errorf("complete code review: %w", err)
		}
		event := logger.Info().
			Str("org_id", job.OrgID.String()).
			Str("session_id", job.SessionID.String()).
			Bool("github_submitted", submitted)
		if submission.GitHubReviewID != nil {
			event = event.Int64("github_review_id", *submission.GitHubReviewID)
		}
		event.Str("decision", string(decision.Decision)).Msg("completed code review")
		reconcileCodeReviewSessionSuccess(ctx, stores, logger, job)
		enqueueCodeReviewStatusCommentSync(ctx, stores, services, logger, job, "terminal")
		return nil
	}
}

func stopCodeReviewIfParentSessionCancelled(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, job runCodeReviewPayload, pr models.PullRequest) (bool, error) {
	if stores == nil || stores.Sessions == nil || stores.CodeReviews == nil {
		return false, nil
	}
	session, err := stores.Sessions.GetByID(ctx, job.OrgID, job.SessionID)
	if err != nil {
		return false, fmt.Errorf("load code review parent session for cancellation: %w", err)
	}
	if session.Status != models.SessionStatusCancelled {
		return false, nil
	}
	reason := "parent code review session was cancelled"
	if detail := strings.TrimSpace(stringPtrValue(session.FailureExplanation)); detail != "" {
		reason += ": " + detail
	}
	if _, err := stores.CodeReviews.CancelReview(ctx, job.OrgID, job.SessionID, reason); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return false, fmt.Errorf("cancel code review after parent cancellation: %w", err)
		}
	}
	removeCodeReviewRequestedReviewer(ctx, stores, services, logger, job, pr)
	enqueueCodeReviewStatusCommentSync(ctx, stores, services, logger, job, "terminal")
	logger.Info().
		Str("session_id", job.SessionID.String()).
		Msg("stopped code review because parent session was cancelled")
	return true, nil
}

type codeReviewThreadCanceller struct {
	orchestrator orchestratorService
}

func (c codeReviewThreadCanceller) CancelThread(threadID uuid.UUID) bool {
	return c.orchestrator != nil && c.orchestrator.CancelThreadByID(threadID)
}

func cancelActiveCodeReviewThreads(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, job runCodeReviewPayload) error {
	if stores == nil || stores.SessionThreads == nil || stores.Sessions == nil || stores.Jobs == nil {
		return nil
	}
	threads := threadsvc.NewService(stores.SessionThreads, stores.Sessions, stores.SessionMessages, stores.SessionLogs, stores.Jobs, logger)
	if services != nil && services.Orchestrator != nil {
		threads.SetCanceller(codeReviewThreadCanceller{orchestrator: services.Orchestrator})
	}
	cancelled, err := threads.CancelActiveThreads(ctx, job.OrgID, []uuid.UUID{job.SessionID})
	if err != nil {
		return fmt.Errorf("cancel active threads for stale code review: %w", err)
	}
	logger.Info().
		Str("org_id", job.OrgID.String()).
		Str("session_id", job.SessionID.String()).
		Int("threads_cancelled", cancelled).
		Msg("cancelled active threads for stale code review")
	return nil
}

func failCodeReviewWithoutReviewerOutput(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, job runCodeReviewPayload, pr models.PullRequest, results []models.CodeReviewAgentResult) error {
	reason := codeReviewNoUsableReviewerOutputReason(results)
	if _, err := stores.CodeReviews.FailReviewWithStatus(ctx, job.OrgID, db.FailCodeReviewParams{
		SessionID: job.SessionID,
		Reason:    reason,
		Code:      models.CodeReviewStatusCodeReviewerFailed,
		Message:   "Reviewer agents did not produce usable output. Retry the review to start a fresh attempt.",
		Retryable: true,
	}); err != nil {
		return fmt.Errorf("fail code review without usable reviewer output: %w", err)
	}
	removeCodeReviewRequestedReviewer(ctx, stores, services, logger, job, pr)
	if err := reconcileCodeReviewSessionFailure(ctx, stores, job, reason); err != nil {
		return err
	}
	enqueueCodeReviewStatusCommentSync(ctx, stores, services, logger, job, "terminal")
	logger.Warn().
		Str("session_id", job.SessionID.String()).
		Str("reason", reason).
		Msg("failed code review because no reviewer produced usable output")
	return nil
}

func codeReviewMetadataTerminal(status models.CodeReviewSessionStatus) bool {
	switch status {
	case models.CodeReviewSessionStatusCompleted, models.CodeReviewSessionStatusFailed, models.CodeReviewSessionStatusStale, models.CodeReviewSessionStatusCancelled:
		return true
	default:
		return false
	}
}

// reconcileCodeReviewSessionSuccess drives the parent session to completed
// once the review itself finishes successfully. The run_code_review job — not the
// per-thread runtime — owns the lifecycle of an origin=code_review session, so
// when the handler reaches a terminal outcome it must stop leaving the session
// in whatever transient state (e.g. a 'pending' parked by a sibling reviewer's
// sandbox-node retry) the thread machinery left behind. Without this, a fully
// completed review can strand its session in 'pending' until the reaper sweeps
// it and stamps the misleading "unable to start within the expected time"
// failure on an already-successful review. Best-effort: a reconciliation
// failure is logged, not surfaced, so it can never undo a posted review.
func reconcileCodeReviewSessionSuccess(ctx context.Context, stores *Stores, logger zerolog.Logger, job runCodeReviewPayload) {
	reconcileCodeReviewSessionCompletion(ctx, stores, logger, job, true)
}

// reconcileCodeReviewSessionStale finishes a non-terminal parent without
// converting a prior reviewer failure into a successful session.
func reconcileCodeReviewSessionStale(ctx context.Context, stores *Stores, logger zerolog.Logger, job runCodeReviewPayload) {
	reconcileCodeReviewSessionCompletion(ctx, stores, logger, job, false)
}

func reconcileCodeReviewSessionCompletion(ctx context.Context, stores *Stores, logger zerolog.Logger, job runCodeReviewPayload, recoverFailed bool) {
	if stores == nil || stores.Sessions == nil {
		return
	}
	session, err := stores.Sessions.GetByID(ctx, job.OrgID, job.SessionID)
	if err != nil {
		logger.Warn().Err(err).Str("session_id", job.SessionID.String()).Msg("failed to load session for code review reconciliation")
		return
	}
	if session.Status == models.SessionStatusCancelled {
		return
	}
	if session.Status.IsTerminal() && !(recoverFailed && session.Status == models.SessionStatusFailed && session.Origin == models.SessionOriginCodeReview) {
		return
	}
	if err := stores.Sessions.UpdateStatus(ctx, job.OrgID, job.SessionID, models.SessionStatusCompleted); err != nil {
		logger.Warn().Err(err).Str("session_id", job.SessionID.String()).Msg("failed to reconcile code review session to completed")
		return
	}
	logger.Info().
		Str("session_id", job.SessionID.String()).
		Str("prev_status", string(session.Status)).
		Msg("reconciled code review session to completed")
}

func reconcileCodeReviewSessionFailure(ctx context.Context, stores *Stores, job runCodeReviewPayload, reason string) error {
	return reconcileCodeReviewSessionFailureWithDetails(ctx, stores, job, reason,
		"code_review_no_reviewer_output",
		[]string{"Configure at least one reviewer credential and request the review again."})
}

func reconcileCodeReviewSessionJobFailure(ctx context.Context, stores *Stores, job runCodeReviewPayload, reason string) error {
	return reconcileCodeReviewSessionFailureWithDetails(ctx, stores, job, reason,
		"code_review_job_failed",
		[]string{"Request the code reviewer again to start a fresh attempt."})
}

func reconcileCodeReviewSessionFailureWithDetails(ctx context.Context, stores *Stores, job runCodeReviewPayload, reason, category string, nextSteps []string) error {
	if stores == nil || stores.Sessions == nil {
		return nil
	}
	session, err := stores.Sessions.GetByID(ctx, job.OrgID, job.SessionID)
	if err != nil {
		return fmt.Errorf("load parent session for code review failure reconciliation: %w", err)
	}
	if session.Status.IsTerminal() && session.Status != models.SessionStatusFailed {
		return nil
	}
	if session.Status == models.SessionStatusFailed && session.Origin != models.SessionOriginCodeReview {
		return nil
	}
	if session.Status != models.SessionStatusFailed {
		if err := stores.Sessions.UpdateStatus(ctx, job.OrgID, job.SessionID, models.SessionStatusFailed); err != nil {
			return fmt.Errorf("reconcile code review parent session to failed: %w", err)
		}
	}
	if err := stores.Sessions.UpdateFailure(ctx, job.OrgID, job.SessionID, reason, category, nextSteps, true); err != nil {
		return fmt.Errorf("record code review parent session failure details: %w", err)
	}
	return nil
}

func registerCodeReviewDeadLetterReconciliation(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, job runCodeReviewPayload) {
	jobctx.RegisterDeadLetterHook(ctx, func(hookCtx context.Context, deadLetterErr error) {
		reason := codeReviewDeadLetterReason(deadLetterErr)
		if stores != nil && stores.CodeReviews != nil {
			code, message, retryable := codeReviewTerminalFailureStatus(deadLetterErr)
			if _, err := stores.CodeReviews.FailReviewWithStatus(hookCtx, job.OrgID, db.FailCodeReviewParams{
				SessionID: job.SessionID,
				Reason:    reason,
				Code:      code,
				Message:   message,
				Retryable: retryable,
			}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				logger.Warn().Err(err).
					Str("session_id", job.SessionID.String()).
					Msg("failed to reconcile dead-lettered code review metadata")
			}
		}
		if err := reconcileCodeReviewSessionJobFailure(hookCtx, stores, job, reason); err != nil {
			logger.Warn().Err(err).
				Str("session_id", job.SessionID.String()).
				Msg("failed to reconcile dead-lettered code review session")
		}
		removeCodeReviewRequestedReviewerAfterDeadLetter(hookCtx, stores, services, logger, job)
		enqueueCodeReviewStatusCommentSync(hookCtx, stores, services, logger, job, "terminal")
	})
}

func recordCodeReviewAutomaticWait(ctx context.Context, store *db.CodeReviewStore, logger zerolog.Logger, job runCodeReviewPayload, handlerErr error) {
	if store == nil || handlerErr == nil {
		return
	}
	classification := ghservice.ClassifyRetry(handlerErr, time.Now())
	if !classification.RateLimited {
		return
	}
	delay := classification.RetryAfter
	var retryable *RetryableError
	if errors.As(handlerErr, &retryable) && retryable.RetryAfter != nil {
		delay = retryable.RetryAfter
	}
	if delay == nil {
		fallback := githubRateLimitMinimumRetryAfter
		delay = &fallback
	}
	retryAt := time.Now().Add(*delay).UTC()
	if _, err := store.SetWaitingForGitHub(ctx, job.OrgID, job.SessionID, retryAt,
		"GitHub is rate-limited. The review will resume automatically when the limit resets."); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		logger.Warn().Err(err).
			Str("session_id", job.SessionID.String()).
			Msg("failed to persist code review GitHub rate-limit wait")
	}
}

func codeReviewTerminalFailureStatus(err error) (models.CodeReviewStatusCode, string, bool) {
	classification := ghservice.ClassifyRetry(err, time.Now())
	if classification.RateLimited {
		return models.CodeReviewStatusCodeGitHubRateLimited,
			"GitHub remained rate-limited until automatic retries expired. Retry the review to start a fresh attempt.", true
	}
	if classification.Retryable {
		return models.CodeReviewStatusCodeGitHubUnavailable,
			"GitHub remained unavailable until automatic retries expired. Retry the review to start a fresh attempt.", true
	}
	var retryable *RetryableError
	if errors.As(err, &retryable) {
		return models.CodeReviewStatusCodeWorkerFailed,
			"The review could not recover automatically. Retry the review to start a fresh attempt.", true
	}
	return models.CodeReviewStatusCodeWorkerFailed,
		"The review stopped because of a non-retryable error. Check the failure details before trying again.", false
}

func removeCodeReviewRequestedReviewerAfterDeadLetter(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, job runCodeReviewPayload) {
	if strings.TrimSpace(job.RequestedReviewerLogin) == "" && strings.TrimSpace(job.RequestedTeamSlug) == "" {
		return
	}
	if services == nil || services.CodeReviews == nil {
		return
	}
	if _, ok := services.CodeReviews.(codeReviewRequestedReviewerRemover); !ok {
		return
	}
	if stores == nil || stores.PullRequests == nil {
		logger.Warn().Str("session_id", job.SessionID.String()).Msg("skipping dead-lettered requested reviewer cleanup: pull request store unavailable")
		return
	}
	pr, err := stores.PullRequests.GetByID(ctx, job.OrgID, job.PullRequestID)
	if err != nil {
		logger.Warn().Err(err).
			Str("session_id", job.SessionID.String()).
			Str("pull_request_id", job.PullRequestID.String()).
			Msg("failed to load pull request for dead-lettered requested reviewer cleanup")
		return
	}
	removeCodeReviewRequestedReviewer(ctx, stores, services, logger, job, pr)
}

func codeReviewDeadLetterReason(err error) string {
	const maxRunes = 2000

	detail := "unknown worker failure"
	var apiErr *ghservice.GitHubAPIError
	if errors.As(err, &apiErr) {
		detail = fmt.Sprintf("GitHub API %s %s returned %d", apiErr.Method, apiErr.Path, apiErr.StatusCode)
	} else if err != nil && strings.TrimSpace(err.Error()) != "" {
		detail = strings.TrimSpace(err.Error())
	}
	reason := "code review job exhausted retries: " + detail
	runes := []rune(reason)
	if len(runes) > maxRunes {
		reason = string(runes[:maxRunes-1]) + "…"
	}
	return reason
}

func syncCodeReviewPullRequestState(ctx context.Context, services *Services, logger zerolog.Logger, job runCodeReviewPayload) error {
	if services == nil || services.PR == nil {
		return nil
	}
	syncCtx := ghservice.WithPullRequestSyncReason(ctx, ghservice.PullRequestSyncReasonCodeReview)
	if err := services.PR.SyncPullRequestState(syncCtx, job.OrgID, job.PullRequestID); err != nil {
		if errors.Is(err, ghservice.ErrPullRequestMergeabilityPending) {
			delay := 5 * time.Second
			return &RetryableError{Err: err, RetryAfter: &delay, BypassMaxRetryDuration: true}
		}
		if errors.Is(err, ghservice.ErrPullRequestRepositoryDisconnected) {
			logger.Info().
				Str("org_id", job.OrgID.String()).
				Str("pull_request_id", job.PullRequestID.String()).
				Msg("skipping code review PR state sync for disconnected repository")
			return nil
		}
		wrapped := fmt.Errorf("sync code review pull request state: %w", err)
		return classifyGitHubJobError(wrapped, job.SessionID.String())
	}
	return nil
}

// supersedeCodeReviewForChangedHead preserves commit-level approval integrity
// without turning an in-flight push into a dead end. Webhooks normally queue
// the replacement assessment first, but the worker also queues it here after
// its live GitHub refresh so a delayed or missed webhook cannot strand the PR
// with only a stale result.
func supersedeCodeReviewForChangedHead(
	ctx context.Context,
	stores *Stores,
	services *Services,
	logger zerolog.Logger,
	job runCodeReviewPayload,
	pr models.PullRequest,
	health *models.PullRequestHealthResponse,
	reason string,
) error {
	if err := queueCodeReviewReplacementForChangedHead(ctx, services, logger, job, pr, health); err != nil {
		return err
	}

	if _, err := stores.CodeReviews.MarkStale(ctx, job.OrgID, job.SessionID, reason); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("mark changed-head code review superseded: %w", err)
	}
	reconcileCodeReviewSessionStale(ctx, stores, logger, job)
	enqueueCodeReviewStatusCommentSync(ctx, stores, services, logger, job, "terminal")
	return nil
}

func queueCodeReviewReplacementForChangedHead(
	ctx context.Context,
	services *Services,
	logger zerolog.Logger,
	job runCodeReviewPayload,
	pr models.PullRequest,
	health *models.PullRequestHealthResponse,
) error {
	if services == nil || services.CodeReviewLifecycle == nil {
		return fmt.Errorf("queue replacement code review: lifecycle service unavailable")
	}
	latestHead, baseSHA := codeReviewCurrentRevision(pr, health)
	if latestHead == "" {
		return fmt.Errorf("queue replacement code review: current PR head is missing")
	}
	changeKey, err := codereviewsvc.MaterialChangeKey(latestHead)
	if err != nil {
		return fmt.Errorf("build replacement code review change key: %w", err)
	}
	queued, err := services.CodeReviewLifecycle.QueueReviewChanged(ctx, codereviewsvc.ReviewChangedInput{
		OrgID:             job.OrgID,
		RepositoryID:      job.RepositoryID,
		PullRequestID:     job.PullRequestID,
		PriorSessionID:    job.SessionID,
		GitHubRepo:        pr.GitHubRepo,
		GitHubPRNumber:    pr.GitHubPRNumber,
		GitHubPRURL:       pr.GitHubPRURL,
		PullRequestTitle:  pr.Title,
		PullRequestAuthor: codeReviewAuthor(job, pr),
		BaseSHA:           baseSHA,
		HeadSHA:           latestHead,
		FromFork:          job.FromFork,
		ChangeKey:         changeKey,
		ChangeReason:      "code_review.live_head_changed",
	})
	if err != nil {
		return fmt.Errorf("queue replacement code review for latest PR head: %w", err)
	}
	logger.Info().
		Str("org_id", job.OrgID.String()).
		Str("session_id", job.SessionID.String()).
		Str("reviewed_head", job.HeadSHA).
		Str("latest_head", latestHead).
		Bool("replacement_queued", queued.Processed).
		Bool("replacement_reused", queued.Reused).
		Str("ignored_reason", queued.IgnoredReason).
		Msg("ensured a fresh code review after PR head changed")
	return nil
}

func codeReviewCurrentRevision(pr models.PullRequest, health *models.PullRequestHealthResponse) (string, string) {
	if headSHA := strings.TrimSpace(stringPtrValue(pr.HeadSHA)); headSHA != "" {
		return headSHA, strings.TrimSpace(stringPtrValue(pr.BaseSHA))
	}
	if health != nil {
		return strings.TrimSpace(health.HeadSHA), strings.TrimSpace(health.BaseSHA)
	}
	return "", ""
}

func codeReviewCanRunReviewerThreads(stores *Stores) bool {
	return stores != nil &&
		stores.Sessions != nil &&
		stores.SessionThreads != nil &&
		stores.SessionMessages != nil &&
		stores.SessionLogs != nil &&
		stores.Jobs != nil
}

type codeReviewReviewerStructuredResult struct {
	ReviewerKey       string  `json:"reviewer_key"`
	ReviewerIndex     int     `json:"reviewer_index"`
	ThreadID          string  `json:"thread_id"`
	PromptRecordKey   string  `json:"prompt_record_key,omitempty"`
	FindingCount      int     `json:"finding_count,omitempty"`
	CostCents         float64 `json:"cost_cents,omitempty"`
	RawRecordKey      string  `json:"raw_record_key,omitempty"`
	NativeReview      bool    `json:"native_review,omitempty"`
	ReadOnly          bool    `json:"read_only,omitempty"`
	ReadOnlyViolation bool    `json:"read_only_violation,omitempty"`
	Reverted          bool    `json:"reverted,omitempty"`
	Unavailable       bool    `json:"unavailable,omitempty"`
	Error             string  `json:"error,omitempty"`
	CompletedAt       string  `json:"completed_at,omitempty"`
}

// MarshalJSON/UnmarshalJSON keep the pre-rename prompt and raw output keys
// readable and writable. Harvest decodes a persisted structured result,
// mutates it, and writes it back, so without the compatibility keys a row
// created by a draining worker generation would silently lose its prompt and
// raw-output references on the first harvest by the other generation.
func (r codeReviewReviewerStructuredResult) MarshalJSON() ([]byte, error) {
	type reviewerStructuredResultAlias codeReviewReviewerStructuredResult
	return json.Marshal(struct {
		reviewerStructuredResultAlias
		LegacyPromptRecordKey string `json:"prompt_artifact_key,omitempty"`
		LegacyRawRecordKey    string `json:"raw_artifact_key,omitempty"`
	}{
		reviewerStructuredResultAlias: reviewerStructuredResultAlias(r),
		LegacyPromptRecordKey:         r.PromptRecordKey,
		LegacyRawRecordKey:            r.RawRecordKey,
	})
}

func (r *codeReviewReviewerStructuredResult) UnmarshalJSON(data []byte) error {
	type reviewerStructuredResultAlias codeReviewReviewerStructuredResult
	decoded := struct {
		*reviewerStructuredResultAlias
		LegacyPromptRecordKey string `json:"prompt_artifact_key"`
		LegacyRawRecordKey    string `json:"raw_artifact_key"`
	}{reviewerStructuredResultAlias: (*reviewerStructuredResultAlias)(r)}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if r.PromptRecordKey == "" {
		r.PromptRecordKey = decoded.LegacyPromptRecordKey
	}
	if r.RawRecordKey == "" {
		r.RawRecordKey = decoded.LegacyRawRecordKey
	}
	return nil
}

type codeReviewOrchestratorStructuredResult struct {
	ThreadID                string                          `json:"thread_id,omitempty"`
	PromptRecordKey         string                          `json:"prompt_record_key,omitempty"`
	DescriptionInputHash    string                          `json:"description_input_hash,omitempty"`
	FindingCount            int                             `json:"finding_count,omitempty"`
	CostCents               float64                         `json:"cost_cents,omitempty"`
	RawRecordKey            string                          `json:"raw_record_key,omitempty"`
	Synthesis               codeReviewOrchestratorSynthesis `json:"synthesis,omitempty"`
	SynthesisValidated      bool                            `json:"synthesis_validated,omitempty"`
	SynthesisRepairCount    int                             `json:"synthesis_repair_count,omitempty"`
	SynthesisRepairPending  bool                            `json:"synthesis_repair_pending,omitempty"`
	SynthesisRepairBaseTurn int                             `json:"synthesis_repair_base_turn,omitempty"`
	ReadOnly                bool                            `json:"read_only,omitempty"`
	ReadOnlyViolation       bool                            `json:"read_only_violation,omitempty"`
	Reverted                bool                            `json:"reverted,omitempty"`
	Error                   string                          `json:"error,omitempty"`
	CompletedAt             string                          `json:"completed_at,omitempty"`
}

func (r codeReviewOrchestratorStructuredResult) MarshalJSON() ([]byte, error) {
	type orchestratorStructuredResultAlias codeReviewOrchestratorStructuredResult
	return json.Marshal(struct {
		orchestratorStructuredResultAlias
		LegacyPromptRecordKey string `json:"prompt_artifact_key,omitempty"`
		LegacyRawRecordKey    string `json:"raw_artifact_key,omitempty"`
	}{
		orchestratorStructuredResultAlias: orchestratorStructuredResultAlias(r),
		LegacyPromptRecordKey:             r.PromptRecordKey,
		LegacyRawRecordKey:                r.RawRecordKey,
	})
}

func (r *codeReviewOrchestratorStructuredResult) UnmarshalJSON(data []byte) error {
	type orchestratorStructuredResultAlias codeReviewOrchestratorStructuredResult
	decoded := struct {
		*orchestratorStructuredResultAlias
		LegacyPromptRecordKey string `json:"prompt_artifact_key"`
		LegacyRawRecordKey    string `json:"raw_artifact_key"`
	}{orchestratorStructuredResultAlias: (*orchestratorStructuredResultAlias)(r)}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if r.PromptRecordKey == "" {
		r.PromptRecordKey = decoded.LegacyPromptRecordKey
	}
	if r.RawRecordKey == "" {
		r.RawRecordKey = decoded.LegacyRawRecordKey
	}
	return nil
}

func ensureCodeReviewReviewerThreads(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, job runCodeReviewPayload, pr models.PullRequest, policy models.CodeReviewPolicyRecord, metadata models.CodeReviewSessionMetadata, changedFiles []codereviewsvc.PullRequestFile, visualEvidence models.CodeReviewVisualEvidenceSnapshot) error {
	results, err := stores.CodeReviews.ListAgentResults(ctx, job.OrgID, job.SessionID)
	if err != nil {
		return fmt.Errorf("list code review reviewer results: %w", err)
	}
	existing := codeReviewReviewerResultsByKey(results)
	cfg := policy.Config()
	rootKey := codeReviewPromptRecordRoot(metadata, job)
	if metadata.PromptRecordKey == nil || strings.TrimSpace(*metadata.PromptRecordKey) == "" {
		if _, err := stores.CodeReviews.SetPromptRecordKey(ctx, job.OrgID, job.SessionID, rootKey); err != nil {
			return fmt.Errorf("set code review prompt record key: %w", err)
		}
	}
	threads := threadsvc.NewService(stores.SessionThreads, stores.Sessions, stores.SessionMessages, stores.SessionLogs, stores.Jobs, logger)
	fileScope := codeReviewChangedPaths(changedFiles)
	timedOutBeforeStart := codeReviewReviewTimedOut(cfg, metadata)
	selections, err := resolveCodeReviewReviewerAvailability(ctx, services, job.OrgID, cfg)
	if err != nil {
		return err
	}
	for _, selection := range selections {
		idx := selection.Index
		agentType := selection.AgentType
		agentModel := codeReviewReviewerAgentModel(cfg, idx, agentType)
		key := codeReviewReviewerKey(idx, agentType)
		if _, ok := existing[key]; ok {
			continue
		}
		if !selection.Available {
			result := unavailableCodeReviewReviewerResult(job, idx, agentType, agentModel)
			if err := stores.CodeReviews.CreateAgentResult(ctx, result); err != nil {
				return fmt.Errorf("create unavailable code review reviewer result: %w", err)
			}
			logger.Info().
				Str("session_id", job.SessionID.String()).
				Str("reviewer", string(agentType)).
				Msg("skipped unavailable code review reviewer")
			continue
		}
		if timedOutBeforeStart {
			raw := "reviewer timed out before the worker could start the reviewer thread"
			result := &models.CodeReviewAgentResult{
				OrgID:         job.OrgID,
				SessionID:     job.SessionID,
				AgentProvider: string(agentType),
				AgentModel:    agentModel,
				Role:          models.CodeReviewAgentRoleReviewer,
				Status:        models.CodeReviewAgentResultStatusTimedOut,
				RawOutput:     &raw,
				StructuredResult: marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{
					ReviewerKey:   key,
					ReviewerIndex: idx,
					Error:         raw,
					CompletedAt:   time.Now().UTC().Format(time.RFC3339),
				}),
			}
			if err := stores.CodeReviews.CreateAgentResult(ctx, result); err != nil {
				return fmt.Errorf("create timed-out code review reviewer result: %w", err)
			}
			continue
		}
		promptText := codeReviewReviewerPrompt(job, pr, cfg, policy.Version, metadata.BaseSHA, changedFiles, visualEvidence)
		recordKey := fmt.Sprintf("%s/reviewer-%02d-%s", rootKey, idx+1, agentType)
		recordMetadata, err := json.Marshal(map[string]any{
			"reviewer_key":         key,
			"agent_type":           agentType,
			"agent_model":          stringPtrValue(agentModel),
			"head_sha":             job.HeadSHA,
			"visual_evidence_hash": visualEvidence.CanonicalHash(),
		})
		if err != nil {
			return fmt.Errorf("marshal reviewer prompt record metadata: %w", err)
		}
		record := &models.CodeReviewPromptRecord{
			OrgID:         job.OrgID,
			SessionID:     job.SessionID,
			RecordKey:     recordKey,
			Role:          string(models.CodeReviewAgentRoleReviewer),
			AgentProvider: string(agentType),
			Content:       promptText,
			Metadata:      recordMetadata,
		}
		if err := stores.CodeReviews.CreatePromptRecord(ctx, record); err != nil {
			return fmt.Errorf("create reviewer prompt record: %w", err)
		}
		thread, err := threads.CreateThread(ctx, threadsvc.CreateThreadInput{
			SessionID:       job.SessionID,
			OrgID:           job.OrgID,
			AgentType:       string(agentType),
			Model:           stringPtrValue(agentModel),
			ReasoningEffort: reasoningEffortPtr(cfg.AgentRoster.ReviewerReasoningEffort(idx)),
			Label:           codeReviewReviewerThreadLabel(agentType),
			FileScope:       fileScope,
			ExecutionMode:   models.ThreadExecutionModeReview,
			FilesystemMode:  models.ThreadFilesystemModeReadOnly,
			CreatedBySource: models.ThreadCreatedBySourceSystem,
		})
		if err != nil {
			return fmt.Errorf("create code review reviewer thread: %w", err)
		}
		structured := marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{
			ReviewerKey:     key,
			ReviewerIndex:   idx,
			ThreadID:        thread.ID.String(),
			PromptRecordKey: recordKey,
			NativeReview:    codeReviewAgentHasBuiltinReviewCommand(agentType),
			ReadOnly:        true,
		})
		result := &models.CodeReviewAgentResult{
			OrgID:            job.OrgID,
			SessionID:        job.SessionID,
			AgentProvider:    string(agentType),
			AgentModel:       agentModel,
			Role:             models.CodeReviewAgentRoleReviewer,
			Status:           models.CodeReviewAgentResultStatusQueued,
			StructuredResult: structured,
		}
		if err := stores.CodeReviews.CreateAgentResult(ctx, result); err != nil {
			return fmt.Errorf("create code review reviewer result: %w", err)
		}
		if _, err := threads.SendMessage(ctx, codeReviewAgentMessageInput(
			job,
			thread.ID,
			codeReviewReviewerMessage(agentType, promptText),
			codeReviewNativeReviewCommands(agentType, promptText),
			visualEvidence,
		)); err != nil {
			raw := err.Error()
			if _, updateErr := stores.CodeReviews.UpdateAgentResultOutcome(ctx, job.OrgID, result.ID, models.CodeReviewAgentResultStatusFailed, &raw, marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{
				ReviewerKey:     key,
				ReviewerIndex:   idx,
				ThreadID:        thread.ID.String(),
				PromptRecordKey: recordKey,
				NativeReview:    codeReviewAgentHasBuiltinReviewCommand(agentType),
				ReadOnly:        true,
				Error:           raw,
				CompletedAt:     time.Now().UTC().Format(time.RFC3339),
			})); updateErr != nil {
				logger.Warn().Err(updateErr).
					Str("session_id", job.SessionID.String()).
					Str("thread_id", thread.ID.String()).
					Str("reviewer", string(agentType)).
					Msg("failed to record failed code review reviewer result")
			}
			logger.Warn().Err(err).
				Str("session_id", job.SessionID.String()).
				Str("thread_id", thread.ID.String()).
				Str("reviewer", string(agentType)).
				Msg("failed to start code review reviewer thread")
			continue
		}
		if _, err := stores.CodeReviews.UpdateAgentResultOutcome(ctx, job.OrgID, result.ID, models.CodeReviewAgentResultStatusRunning, nil, structured); err != nil {
			return fmt.Errorf("mark code review reviewer running: %w", err)
		}
	}
	return nil
}

type codeReviewReviewerSelection struct {
	Index     int
	AgentType models.AgentType
	Available bool
}

type codeReviewOrchestratorSelection struct {
	AgentType       models.AgentType
	AgentModel      *string
	ReasoningEffort *models.ReasoningEffort
	Available       bool
}

func resolveCodeReviewReviewerAvailability(ctx context.Context, services *Services, orgID uuid.UUID, cfg models.CodeReviewPolicyConfig) ([]codeReviewReviewerSelection, error) {
	reviewers := cfg.AgentRoster.Reviewers
	selections := make([]codeReviewReviewerSelection, 0, len(reviewers))
	for idx, agentType := range reviewers {
		available := true
		if services != nil && services.CodingAgents != nil {
			var err error
			available, err = services.CodingAgents.IsAgentAvailable(ctx, orgID, nil, agentType, stringPtrValue(codeReviewReviewerAgentModel(cfg, idx, agentType)))
			if err != nil {
				return nil, fmt.Errorf("resolve code review reviewer %s availability: %w", agentType, err)
			}
		}
		selections = append(selections, codeReviewReviewerSelection{
			Index:     idx,
			AgentType: agentType,
			Available: available,
		})
	}
	return selections, nil
}

func resolveCodeReviewOrchestratorAvailability(ctx context.Context, services *Services, orgID uuid.UUID, cfg models.CodeReviewPolicyConfig) (codeReviewOrchestratorSelection, error) {
	configured := codeReviewOrchestratorSelection{
		AgentType:       cfg.AgentRoster.Orchestrator,
		AgentModel:      codeReviewOrchestratorAgentModel(cfg),
		ReasoningEffort: reasoningEffortPtr(cfg.AgentRoster.ReasoningEffort),
		Available:       true,
	}
	if services == nil || services.CodingAgents == nil {
		return configured, nil
	}

	available, err := services.CodingAgents.IsAgentAvailable(ctx, orgID, nil, configured.AgentType, stringPtrValue(configured.AgentModel))
	if err != nil {
		return codeReviewOrchestratorSelection{}, fmt.Errorf("resolve code review orchestrator %s availability: %w", configured.AgentType, err)
	}
	if available {
		return configured, nil
	}

	for idx, agentType := range cfg.AgentRoster.Reviewers {
		agentModel := codeReviewReviewerAgentModel(cfg, idx, agentType)
		available, err := services.CodingAgents.IsAgentAvailable(ctx, orgID, nil, agentType, stringPtrValue(agentModel))
		if err != nil {
			return codeReviewOrchestratorSelection{}, fmt.Errorf("resolve code review orchestrator fallback %s availability: %w", agentType, err)
		}
		if available {
			return codeReviewOrchestratorSelection{
				AgentType:       agentType,
				AgentModel:      agentModel,
				ReasoningEffort: reasoningEffortPtr(cfg.AgentRoster.ReviewerReasoningEffort(idx)),
				Available:       true,
			}, nil
		}
	}

	configured.Available = false
	return configured, nil
}

func unavailableCodeReviewReviewerResult(job runCodeReviewPayload, index int, agentType models.AgentType, agentModel *string) *models.CodeReviewAgentResult {
	raw := fmt.Sprintf("reviewer skipped because %s authentication is not configured", agentType)
	return &models.CodeReviewAgentResult{
		OrgID:         job.OrgID,
		SessionID:     job.SessionID,
		AgentProvider: string(agentType),
		AgentModel:    agentModel,
		Role:          models.CodeReviewAgentRoleReviewer,
		Status:        models.CodeReviewAgentResultStatusFailed,
		RawOutput:     &raw,
		StructuredResult: marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{
			ReviewerKey:   codeReviewReviewerKey(index, agentType),
			ReviewerIndex: index,
			Unavailable:   true,
			Error:         raw,
			CompletedAt:   time.Now().UTC().Format(time.RFC3339),
		}),
	}
}

func harvestCodeReviewReviewerResults(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, job runCodeReviewPayload, policy models.CodeReviewPolicyRecord, metadata models.CodeReviewSessionMetadata, changedFiles []codereviewsvc.PullRequestFile) error {
	results, err := stores.CodeReviews.ListAgentResults(ctx, job.OrgID, job.SessionID)
	if err != nil {
		return fmt.Errorf("list code review reviewer results for harvest: %w", err)
	}
	deadline := codeReviewReviewDeadline(policy.Config(), metadata)
	timedOut := time.Now().After(deadline)
	changedPaths := codeReviewChangedPaths(changedFiles)
	for _, result := range results {
		if result.Role != models.CodeReviewAgentRoleReviewer || codeReviewReviewerResultTerminal(result.Status) {
			continue
		}
		state, ok := parseCodeReviewReviewerStructuredResult(result.StructuredResult)
		if !ok {
			raw := "reviewer result has a malformed structured result"
			if _, err := stores.CodeReviews.UpdateAgentResultOutcome(ctx, job.OrgID, result.ID, models.CodeReviewAgentResultStatusFailed, &raw, result.StructuredResult); err != nil {
				return fmt.Errorf("mark malformed reviewer result failed: %w", err)
			}
			continue
		}
		if strings.TrimSpace(state.ThreadID) == "" {
			raw := "reviewer result is missing its thread id"
			state.Error = raw
			state.CompletedAt = time.Now().UTC().Format(time.RFC3339)
			if _, err := stores.CodeReviews.UpdateAgentResultOutcome(ctx, job.OrgID, result.ID, models.CodeReviewAgentResultStatusFailed, &raw, marshalCodeReviewReviewerStructuredResult(state)); err != nil {
				return fmt.Errorf("mark malformed reviewer result failed: %w", err)
			}
			continue
		}
		threadID, err := uuid.Parse(state.ThreadID)
		if err != nil {
			raw := "reviewer result has an invalid thread id: " + err.Error()
			state.Error = raw
			state.CompletedAt = time.Now().UTC().Format(time.RFC3339)
			if _, err := stores.CodeReviews.UpdateAgentResultOutcome(ctx, job.OrgID, result.ID, models.CodeReviewAgentResultStatusFailed, &raw, marshalCodeReviewReviewerStructuredResult(state)); err != nil {
				return fmt.Errorf("mark invalid reviewer result failed: %w", err)
			}
			continue
		}
		thread, err := stores.SessionThreads.GetByID(ctx, job.OrgID, threadID)
		if err != nil {
			return fmt.Errorf("load code review reviewer thread: %w", err)
		}
		state.CostCents = thread.CostCents
		// The worker may resume after the deadline even though the thread
		// completed on time. Preserve terminal output and apply the timeout only
		// when the persisted completion time does not prove it finished in time.
		if timedOut && !codeReviewThreadCompletedByDeadline(thread, deadline) {
			raw := "reviewer did not produce a completed turn before the review deadline"
			state.Error = raw
			completedAt := codeReviewThreadCompletionTime(thread)
			if codeReviewThreadStillRunning(thread.Status) {
				if cancelledThread, cancelErr := cancelCodeReviewThread(ctx, stores, logger, job, threadID); cancelErr == nil {
					state.CostCents = cancelledThread.CostCents
					completedAt = codeReviewThreadCompletionTime(cancelledThread)
				} else {
					logger.Warn().Err(cancelErr).
						Str("session_id", job.SessionID.String()).
						Str("thread_id", threadID.String()).
						Msg("failed to cancel timed-out code review reviewer thread")
				}
			}
			state.CompletedAt = completedAt.Format(time.RFC3339)
			if _, err := stores.CodeReviews.UpdateAgentResultOutcome(ctx, job.OrgID, result.ID, models.CodeReviewAgentResultStatusTimedOut, &raw, marshalCodeReviewReviewerStructuredResult(state)); err != nil {
				return fmt.Errorf("mark reviewer timed out: %w", err)
			}
			continue
		}
		if codeReviewThreadStillRunning(thread.Status) {
			continue
		}
		readOnlyViolation := codeReviewThreadReadOnlyViolation(thread)
		if readOnlyViolation {
			state.ReadOnly = true
			state.ReadOnlyViolation = true
			logger.Warn().
				Str("session_id", job.SessionID.String()).
				Str("thread_id", thread.ID.String()).
				Str("reviewer", result.AgentProvider).
				Msg("code review reviewer thread produced workspace changes; continuing")
		}
		raw, ok, err := latestAssistantMessageForThread(ctx, stores, job.OrgID, threadID)
		if err != nil {
			return err
		}
		threadFailed := thread.Status == models.ThreadStatusFailed || thread.Status == models.ThreadStatusCancelled
		if threadFailed && !codeReviewFailedReviewerThreadOutputUsable(thread, raw, ok) {
			failure := strings.TrimSpace(stringPtrValue(thread.FailureExplanation))
			if !ok {
				raw = failure
				if raw == "" {
					raw = "reviewer thread did not complete successfully"
				}
			}
			if failure == "" {
				failure = raw
			}
			state.Error = failure
			state.CompletedAt = codeReviewThreadCompletionTime(thread).Format(time.RFC3339)
			rawOutput, rawRecordKey, err := codeReviewRawOutputForStorage(ctx, stores, job, result.ID, models.CodeReviewAgentRoleReviewer, result.AgentProvider, raw)
			if err != nil {
				return err
			}
			state.RawRecordKey = rawRecordKey
			if _, err := stores.CodeReviews.UpdateAgentResultOutcome(ctx, job.OrgID, result.ID, models.CodeReviewAgentResultStatusFailed, rawOutput, marshalCodeReviewReviewerStructuredResult(state)); err != nil {
				return fmt.Errorf("mark reviewer failed: %w", err)
			}
			continue
		}
		if threadFailed {
			logger.Warn().
				Str("session_id", job.SessionID.String()).
				Str("thread_id", thread.ID.String()).
				Str("reviewer", result.AgentProvider).
				Msg("using persisted reviewer output from a subsequently failed thread")
		}
		if !ok {
			if readOnlyViolation {
				raw = strings.TrimSpace(stringPtrValue(thread.FailureExplanation))
				if raw == "" {
					raw = "reviewer thread produced workspace changes without persisted assistant output"
				}
				state.Error = raw
				state.CompletedAt = codeReviewThreadCompletionTime(thread).Format(time.RFC3339)
				rawOutput, rawRecordKey, err := codeReviewRawOutputForStorage(ctx, stores, job, result.ID, models.CodeReviewAgentRoleReviewer, result.AgentProvider, raw)
				if err != nil {
					return err
				}
				state.RawRecordKey = rawRecordKey
				if _, err := stores.CodeReviews.UpdateAgentResultOutcome(ctx, job.OrgID, result.ID, models.CodeReviewAgentResultStatusCompleted, rawOutput, marshalCodeReviewReviewerStructuredResult(state)); err != nil {
					return fmt.Errorf("mark read-only-violating reviewer completed: %w", err)
				}
			}
			continue
		}
		findings := parseCodeReviewFindings(raw, changedPaths)
		for i := range findings {
			findings[i].OrgID = job.OrgID
			findings[i].SessionID = job.SessionID
			findings[i].AgentResultID = &result.ID
			if err := stores.CodeReviews.CreateFinding(ctx, &findings[i]); err != nil {
				return fmt.Errorf("create harvested code review finding: %w", err)
			}
		}
		state.FindingCount = len(findings)
		state.CompletedAt = codeReviewThreadCompletionTime(thread).Format(time.RFC3339)
		rawOutput, rawRecordKey, err := codeReviewRawOutputForStorage(ctx, stores, job, result.ID, models.CodeReviewAgentRoleReviewer, result.AgentProvider, raw)
		if err != nil {
			return err
		}
		state.RawRecordKey = rawRecordKey
		if _, err := stores.CodeReviews.UpdateAgentResultOutcome(ctx, job.OrgID, result.ID, models.CodeReviewAgentResultStatusCompleted, rawOutput, marshalCodeReviewReviewerStructuredResult(state)); err != nil {
			return fmt.Errorf("mark reviewer completed: %w", err)
		}
	}
	return nil
}

func codeReviewFailedReviewerThreadOutputUsable(thread models.SessionThread, raw string, ok bool) bool {
	if !ok {
		return false
	}
	output := strings.TrimSpace(raw)
	if output == "" {
		return false
	}
	failure := strings.TrimSpace(stringPtrValue(thread.FailureExplanation))
	if failure != "" && strings.EqualFold(output, failure) {
		return false
	}
	category := strings.ToLower(strings.TrimSpace(stringPtrValue(thread.FailureCategory)))
	return category == "turn_persistence_failed"
}

func codeReviewReviewerResultsByKey(results []models.CodeReviewAgentResult) map[string]models.CodeReviewAgentResult {
	out := make(map[string]models.CodeReviewAgentResult)
	for _, result := range results {
		if result.Role != models.CodeReviewAgentRoleReviewer {
			continue
		}
		state, ok := parseCodeReviewReviewerStructuredResult(result.StructuredResult)
		if !ok || strings.TrimSpace(state.ReviewerKey) == "" {
			continue
		}
		out[state.ReviewerKey] = result
	}
	return out
}

func codeReviewReviewerKey(index int, agentType models.AgentType) string {
	return fmt.Sprintf("%02d:%s", index, agentType)
}

func codeReviewReviewerThreadLabel(agentType models.AgentType) string {
	label := strings.TrimSpace(string(agentType))
	if label == "" {
		label = "reviewer"
	}
	return "Code review: " + label
}

func codeReviewAgentHasBuiltinReviewCommand(agentType models.AgentType) bool {
	for _, command := range models.SlashCommandsForAgent(agentType) {
		if command.Name == "review" {
			return true
		}
	}
	return false
}

func codeReviewNativeReviewCommands(agentType models.AgentType, promptText string) models.SessionInputCommands {
	if !codeReviewAgentHasBuiltinReviewCommand(agentType) {
		return nil
	}
	arguments := strings.TrimSpace(strings.TrimPrefix(strings.TrimLeft(promptText, " \t\r\n"), "/review"))
	return models.SessionInputCommands{{
		Kind:      "command",
		AgentType: agentType,
		Name:      "review",
		Token:     "/review",
		Display:   "/review",
		Arguments: arguments,
		Source:    models.SessionInputCommandSourceBuiltin,
	}}
}

func codeReviewReviewerMessage(agentType models.AgentType, promptText string) string {
	promptText = strings.TrimSpace(promptText)
	if !codeReviewAgentHasBuiltinReviewCommand(agentType) || strings.HasPrefix(strings.TrimLeft(promptText, " \t\r\n"), "/review") {
		return promptText
	}
	if promptText == "" {
		return "/review"
	}
	return "/review " + promptText
}

func codeReviewAgentMessageInput(job runCodeReviewPayload, threadID uuid.UUID, message string, commands models.SessionInputCommands, visualEvidence models.CodeReviewVisualEvidenceSnapshot) threadsvc.SendMessageInput {
	return threadsvc.SendMessageInput{
		SessionID:     job.SessionID,
		OrgID:         job.OrgID,
		ThreadID:      threadID,
		Message:       message,
		Images:        codeReviewVisualEvidenceImages(visualEvidence),
		Commands:      commands,
		MessageSource: models.SessionMessageSourceAgentTool,
	}
}

func codeReviewPromptRecordRoot(metadata models.CodeReviewSessionMetadata, job runCodeReviewPayload) string {
	if metadata.PromptRecordKey != nil && strings.TrimSpace(*metadata.PromptRecordKey) != "" {
		return strings.TrimSpace(*metadata.PromptRecordKey)
	}
	return fmt.Sprintf("code-review-prompts/%s/%s", job.SessionID, job.HeadSHA)
}

func codeReviewDefaultAgentModel(agentType models.AgentType) *string {
	var model string
	switch agentType {
	case models.AgentTypeCodex:
		model = models.DefaultCodexModel
	case models.AgentTypeClaudeCode:
		model = models.DefaultClaudeCodeModel
	case models.AgentTypeAmp:
		model = models.AmpModeSmart
	case models.AgentTypePi:
		model = models.PiModelClaudeOpus48
	case models.AgentTypeOpenCode:
		model = models.OpenCodeModelGPT55
	default:
		return nil
	}
	return &model
}

func codeReviewReviewerAgentModel(cfg models.CodeReviewPolicyConfig, idx int, agentType models.AgentType) *string {
	if idx >= 0 && idx < len(cfg.AgentRoster.ReviewerModels) {
		if model := strings.TrimSpace(cfg.AgentRoster.ReviewerModels[idx]); model != "" {
			return &model
		}
	}
	return codeReviewDefaultAgentModel(agentType)
}

func reasoningEffortPtr(value models.ReasoningEffort) *models.ReasoningEffort {
	return &value
}

func codeReviewOrchestratorAgentModel(cfg models.CodeReviewPolicyConfig) *string {
	if cfg.AgentRoster.OrchestratorModel != nil {
		if model := strings.TrimSpace(*cfg.AgentRoster.OrchestratorModel); model != "" {
			return &model
		}
	}
	return codeReviewDefaultAgentModel(cfg.AgentRoster.Orchestrator)
}

func storeCodeReviewPromptRecord(ctx context.Context, stores *Stores, record models.CodeReviewPromptRecord) error {
	if stores == nil || stores.CodeReviews == nil {
		return nil
	}
	if err := stores.CodeReviews.CreatePromptRecord(ctx, &record); err != nil {
		return fmt.Errorf("store code review prompt record: %w", err)
	}
	return nil
}

func mustMarshalCodeReviewJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

func safeCodeReviewRecordSegment(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && builder.Len() > 0 {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(builder.String(), "-")
	if out == "" {
		return "record"
	}
	return out
}

func codeReviewRawOutputForStorage(ctx context.Context, stores *Stores, job runCodeReviewPayload, resultID uuid.UUID, role models.CodeReviewAgentRole, provider, raw string) (*string, string, error) {
	if len(raw) <= codeReviewRawOutputInlineLimit {
		return &raw, "", nil
	}
	if stores == nil || stores.CodeReviews == nil {
		truncated := raw[:codeReviewRawOutputInlineLimit] + "\n\n[truncated: prompt record store unavailable]"
		return &truncated, "", nil
	}
	recordRole := string(role) + "_output"
	recordKey := fmt.Sprintf("code-review-prompts/%s/%s-output-%s", job.SessionID, safeCodeReviewRecordSegment(string(role)), resultID)
	if strings.TrimSpace(job.OutputKey) != "" {
		recordKey = fmt.Sprintf("%s/%s-output-%s", strings.TrimSpace(job.OutputKey), safeCodeReviewRecordSegment(string(role)), resultID)
	}
	if err := storeCodeReviewPromptRecord(ctx, stores, models.CodeReviewPromptRecord{
		OrgID:         job.OrgID,
		SessionID:     job.SessionID,
		RecordKey:     recordKey,
		Role:          recordRole,
		AgentProvider: provider,
		Content:       raw,
		Metadata: mustMarshalCodeReviewJSON(map[string]any{
			"result_id":      resultID,
			"role":           role,
			"provider":       provider,
			"head_sha":       job.HeadSHA,
			"raw_bytes":      len(raw),
			"stored_because": "raw_output_exceeded_inline_limit",
		}),
	}); err != nil {
		return nil, "", err
	}
	summary := fmt.Sprintf("Raw output stored in prompt record %s (%d bytes).", recordKey, len(raw))
	return &summary, recordKey, nil
}

func cancelCodeReviewThread(ctx context.Context, stores *Stores, logger zerolog.Logger, job runCodeReviewPayload, threadID uuid.UUID) (models.SessionThread, error) {
	if stores == nil || stores.SessionThreads == nil {
		return models.SessionThread{}, fmt.Errorf("session thread store is required")
	}
	threads := threadsvc.NewService(stores.SessionThreads, stores.Sessions, stores.SessionMessages, stores.SessionLogs, stores.Jobs, logger)
	return threads.CancelThread(ctx, job.OrgID, job.SessionID, threadID)
}

func codeReviewThreadReadOnlyViolation(thread models.SessionThread) bool {
	return thread.ExecutionMode == models.ThreadExecutionModeReview &&
		thread.FilesystemMode == models.ThreadFilesystemModeReadOnly &&
		thread.Diff != nil &&
		strings.TrimSpace(*thread.Diff) != ""
}

func revertCodeReviewReadOnlyThread(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, job runCodeReviewPayload, thread models.SessionThread) bool {
	if stores == nil || stores.Sessions == nil || services == nil || services.Orchestrator == nil {
		return false
	}
	session, err := stores.Sessions.GetByID(ctx, job.OrgID, job.SessionID)
	if err != nil {
		logger.Warn().Err(err).Str("session_id", job.SessionID.String()).Msg("failed to load code review session for read-only revert")
		return false
	}
	if err := services.Orchestrator.RevertThread(ctx, &session, &thread); err != nil {
		logger.Warn().Err(err).
			Str("session_id", job.SessionID.String()).
			Str("thread_id", thread.ID.String()).
			Msg("failed to revert read-only code review thread")
		return false
	}
	return true
}

func codeReviewReviewerPrompt(job runCodeReviewPayload, pr models.PullRequest, cfg models.CodeReviewPolicyConfig, policyVersion int, baseSHA string, changedFiles []codereviewsvc.PullRequestFile, visualEvidence models.CodeReviewVisualEvidenceSnapshot) string {
	cfg = models.ResolveCodeReviewPolicyConfig(&cfg)
	return strings.TrimSpace(prompts.CodeReviewReviewerPrompt(prompts.CodeReviewReviewerPromptData{
		ReviewInstructions:    cfg.ReviewInstructions,
		Repository:            pr.GitHubRepo,
		PullNumber:            pr.GitHubPRNumber,
		PullRequestURL:        pr.GitHubPRURL,
		BaseSHA:               firstNonEmpty(baseSHA, stringPtrValue(pr.BaseSHA)),
		HeadSHA:               job.HeadSHA,
		ChangedFiles:          codeReviewChangedPaths(changedFiles),
		VisualEvidence:        codeReviewVisualEvidenceForPrompt(visualEvidence),
		VisualEvidenceOmitted: visualEvidence.OmittedSourceCount,
	}))
}

func codeReviewOrchestratorPrompt(job runCodeReviewPayload, pr models.PullRequest, health *models.PullRequestHealthResponse, cfg models.CodeReviewPolicyConfig, policyVersion int, baseSHA string, changedFiles []codereviewsvc.PullRequestFile, agentResults []models.CodeReviewAgentResult, findings []models.CodeReviewFinding, visualEvidence models.CodeReviewVisualEvidenceSnapshot) string {
	return prompts.CodeReviewOrchestratorPrompt(prompts.CodeReviewOrchestratorPromptData{
		Repository:                 pr.GitHubRepo,
		PullNumber:                 pr.GitHubPRNumber,
		PullRequestURL:             pr.GitHubPRURL,
		Title:                      pr.Title,
		PRBody:                     stringPtrValue(pr.Body),
		Author:                     codeReviewAuthor(job, pr),
		BaseSHA:                    firstNonEmpty(baseSHA, stringPtrValue(pr.BaseSHA)),
		HeadSHA:                    job.HeadSHA,
		PolicyVersion:              policyVersion,
		ApprovalMode:               cfg.ApprovalMode,
		RequiredReviewerQuorum:     codeReviewRequiredReviewerQuorum(cfg, agentResults),
		InlineCommentLimit:         cfg.InlineCommentLimit,
		DescriptionRequirements:    codeReviewDescriptionRequirementsForPrompt(cfg, changedFiles),
		RiskReasons:                models.CodeReviewRiskReasonMessages(codeReviewPromptRiskReasons(job, pr, health, cfg, changedFiles, agentResults, findings, visualEvidence)),
		ReviewerOutputs:            codeReviewReviewerOutputsForPrompt(agentResults),
		Findings:                   codeReviewFindingsForPrompt(findings),
		ChangedFiles:               codeReviewChangedPaths(changedFiles),
		RequestContextAuthor:       codeReviewRequestContextAuthor(job.RequestContext),
		RequestContextBody:         codeReviewRequestContextBody(job.RequestContext),
		RequestContextURL:          codeReviewRequestContextURL(job.RequestContext),
		ReviewInstructions:         cfg.ReviewInstructions,
		AutomatedApprovalPolicy:    cfg.AutomatedApprovalPolicy,
		UseAutomatedApprovalPolicy: cfg.ApprovalMode == models.CodeReviewApprovalModeApproveAcceptable,
		VisualEvidence:             codeReviewVisualEvidenceForPrompt(visualEvidence),
		VisualEvidenceOmitted:      visualEvidence.OmittedSourceCount,
	})
}

func codeReviewPromptRiskReasons(job runCodeReviewPayload, pr models.PullRequest, health *models.PullRequestHealthResponse, cfg models.CodeReviewPolicyConfig, changedFiles []codereviewsvc.PullRequestFile, agentResults []models.CodeReviewAgentResult, findings []models.CodeReviewFinding, visualEvidence models.CodeReviewVisualEvidenceSnapshot) []models.CodeReviewRiskReason {
	reviewerQuorum, _ := codeReviewReviewerEvidence(agentResults)
	risk := models.EvaluateCodeReviewRisk(cfg, models.CodeReviewRiskInput{
		FilesChanged:          len(changedFiles),
		LinesChanged:          codeReviewLinesChanged(changedFiles),
		ChangedPaths:          codeReviewChangedPaths(changedFiles),
		ChecksPassing:         codeReviewChecksPassing(cfg, health),
		RequiredChecksPassing: codeReviewRequiredChecksPassing(cfg, health),
		// The coding-agent orchestrator owns description-policy assessment.
		// Pre-synthesis risk reasons contain deterministic safeguards only.
		DescriptionPassed:    true,
		UpToDate:             codeReviewUpToDate(health),
		Author:               codeReviewAuthor(job, pr),
		AuthorClass:          codeReviewAuthorClass(pr),
		AuthorTeams:          job.PullRequestAuthorTeams,
		FromFork:             job.FromFork,
		ContextFetchFailed:   health == nil,
		HeadSHAChanged:       codeReviewHeadChanged(job.HeadSHA, pr, health),
		BlockingFindings:     codeReviewBlockingFindings(findings),
		ReviewerDisagreement: false,
		PromptInjectionFound: codeReviewPromptInjectionLikely(stringPtrValue(pr.Body), job.RequestContext) || codeReviewVisualEvidencePromptInjectionLikely(visualEvidence),
	})
	requiredReviewerQuorum := codeReviewRequiredReviewerQuorum(cfg, agentResults)
	if reviewerQuorum < requiredReviewerQuorum {
		risk.AddReason(models.CodeReviewRiskReason{Code: models.CodeReviewRiskReasonReviewerQuorum, Actual: reviewerQuorum, Limit: requiredReviewerQuorum})
	}
	return risk.ReasonDetails
}

func codeReviewReviewerOutputsForPrompt(results []models.CodeReviewAgentResult) []string {
	out := make([]string, 0)
	for _, result := range results {
		if result.Role != models.CodeReviewAgentRoleReviewer {
			continue
		}
		provider := strings.TrimSpace(result.AgentProvider)
		if provider == "" {
			provider = "reviewer"
		}
		raw := strings.TrimSpace(stringPtrValue(result.RawOutput))
		if raw == "" {
			raw = string(result.StructuredResult)
		}
		if raw == "" {
			raw = string(result.Status)
		}
		out = append(out, fmt.Sprintf("Reviewer %s (%s):\n%s", provider, result.Status, raw))
	}
	return out
}

func codeReviewFindingsForPrompt(findings []models.CodeReviewFinding) []string {
	out := make([]string, 0, len(findings))
	for _, finding := range findings {
		location := ""
		if finding.Path != nil {
			location = *finding.Path
			if finding.StartLine != nil {
				location = fmt.Sprintf("%s:%d", location, *finding.StartLine)
			}
		}
		if location != "" {
			out = append(out, fmt.Sprintf("%s %s - %s", finding.Severity, location, finding.Summary))
		} else {
			out = append(out, fmt.Sprintf("%s - %s", finding.Severity, finding.Summary))
		}
	}
	return out
}

func codeReviewDescriptionInputHash(pr models.PullRequest, visualEvidence models.CodeReviewVisualEvidenceSnapshot) string {
	body := ""
	if pr.Body != nil {
		body = *pr.Body
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(pr.Title) + "\n" + strings.TrimSpace(body) + "\n" + visualEvidence.CanonicalHash()))
	return fmt.Sprintf("%x", sum[:])
}

func codeReviewVisualEvidenceImages(snapshot models.CodeReviewVisualEvidenceSnapshot) []string {
	images, _ := codeReviewVisualEvidenceAttachments(snapshot)
	return images
}

func codeReviewVisualEvidenceAttachments(snapshot models.CodeReviewVisualEvidenceSnapshot) ([]string, []int) {
	images := make([]string, 0, len(snapshot.Evidence))
	attachmentIndexes := make([]int, len(snapshot.Evidence))
	indexesByContent := make(map[string]int, len(snapshot.Evidence))
	for index, evidence := range snapshot.Evidence {
		storedURL := strings.TrimSpace(evidence.StoredURL)
		if evidence.Status != models.CodeReviewVisualEvidenceFetchStatusAvailable || storedURL == "" {
			continue
		}
		contentKey := strings.ToLower(strings.TrimSpace(evidence.ContentSHA256))
		if contentKey == "" {
			// Valid persisted snapshots always carry a content hash. Retain a
			// deterministic fallback for legacy fixtures and draining binaries.
			contentKey = "stored-url:" + storedURL
		} else {
			contentKey = "sha256:" + contentKey
		}
		if attachmentIndex, exists := indexesByContent[contentKey]; exists {
			attachmentIndexes[index] = attachmentIndex
			continue
		}
		images = append(images, storedURL)
		attachmentIndex := len(images)
		indexesByContent[contentKey] = attachmentIndex
		attachmentIndexes[index] = attachmentIndex
	}
	return images, attachmentIndexes
}

func codeReviewVisualEvidenceForPrompt(snapshot models.CodeReviewVisualEvidenceSnapshot) []prompts.CodeReviewVisualEvidencePromptData {
	entries := make([]prompts.CodeReviewVisualEvidencePromptData, 0, len(snapshot.Evidence))
	_, attachmentIndexes := codeReviewVisualEvidenceAttachments(snapshot)
	for index, evidence := range snapshot.Evidence {
		observedAt := ""
		if evidence.Source.CreatedAt != nil {
			observedAt = evidence.Source.CreatedAt.UTC().Format(time.RFC3339)
		} else if evidence.Source.UpdatedAt != nil {
			observedAt = evidence.Source.UpdatedAt.UTC().Format(time.RFC3339)
		}
		entries = append(entries, prompts.CodeReviewVisualEvidencePromptData{
			EvidenceID:            evidence.EvidenceID,
			Surface:               string(evidence.Source.Surface),
			SourceURL:             evidence.Source.SourceURL,
			Author:                evidence.Source.AuthorLogin,
			ObservedAt:            observedAt,
			AttachmentIndex:       attachmentIndexes[index],
			AltText:               evidence.Source.AltText,
			ContextText:           evidence.Source.ContextText,
			Status:                string(evidence.Status),
			DuplicateOfEvidenceID: evidence.DuplicateOfEvidenceID,
			FailureReason:         evidence.FailureReason,
		})
	}
	return entries
}

func codeReviewAvailableVisualEvidenceForPrompt(snapshot models.CodeReviewVisualEvidenceSnapshot) []prompts.CodeReviewVisualEvidencePromptData {
	manifest := codeReviewVisualEvidenceForPrompt(snapshot)
	available := make([]prompts.CodeReviewVisualEvidencePromptData, 0, len(manifest))
	for _, evidence := range manifest {
		if evidence.AttachmentIndex > 0 {
			available = append(available, evidence)
		}
	}
	return available
}

func codeReviewPromptInjectionLikely(prBody string, requestContext *codereviewsvc.ReviewRequestContext) bool {
	patterns := []string{
		"ignore previous instructions",
		"ignore all previous instructions",
		"disregard previous instructions",
		"override your instructions",
		"system prompt",
		"developer message",
		"approval policy does not apply",
	}
	return containsAnyFold(prBody, patterns) ||
		containsAnyFold(codeReviewRequestContextBody(requestContext), patterns)
}

func codeReviewVisualEvidencePromptInjectionLikely(snapshot models.CodeReviewVisualEvidenceSnapshot) bool {
	for _, evidence := range snapshot.Evidence {
		if codeReviewPromptInjectionLikely(evidence.Source.AltText+"\n"+evidence.Source.ContextText, nil) {
			return true
		}
	}
	return false
}

func marshalCodeReviewReviewerStructuredResult(state codeReviewReviewerStructuredResult) json.RawMessage {
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil
	}
	return encoded
}

func parseCodeReviewReviewerStructuredResult(raw json.RawMessage) (codeReviewReviewerStructuredResult, bool) {
	if len(raw) == 0 {
		return codeReviewReviewerStructuredResult{}, false
	}
	var state codeReviewReviewerStructuredResult
	if err := json.Unmarshal(raw, &state); err != nil {
		return codeReviewReviewerStructuredResult{}, false
	}
	return state, true
}

func marshalCodeReviewOrchestratorStructuredResult(state codeReviewOrchestratorStructuredResult) json.RawMessage {
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil
	}
	return encoded
}

func parseCodeReviewOrchestratorStructuredResult(raw json.RawMessage) (codeReviewOrchestratorStructuredResult, bool) {
	if len(raw) == 0 {
		return codeReviewOrchestratorStructuredResult{}, false
	}
	var state codeReviewOrchestratorStructuredResult
	if err := json.Unmarshal(raw, &state); err != nil {
		return codeReviewOrchestratorStructuredResult{}, false
	}
	return state, true
}

func parseCodeReviewOrchestratorSynthesis(raw string) (codeReviewOrchestratorSynthesis, error) {
	var payload struct {
		ApprovalRecommended     *bool                                      `json:"approval_recommended"`
		DescriptionAssessments  *[]codeReviewDescriptionAssessment         `json:"description_assessments"`
		Findings                *[]codeReviewOrchestratorFinding           `json:"findings"`
		HumanReviewReasons      *[]codeReviewOrchestratorHumanReviewReason `json:"human_review_reasons"`
		Summary                 *string                                    `json:"summary"`
		ReviewSummary           *string                                    `json:"review_summary"`
		RiskNotes               *[]string                                  `json:"risk_notes"`
		ScopeMismatch           *bool                                      `json:"scope_mismatch"`
		UnresolvedUncertainty   *bool                                      `json:"unresolved_uncertainty"`
		ReviewerDisagreement    *bool                                      `json:"reviewer_disagreement"`
		PromptInjectionDetected *bool                                      `json:"prompt_injection_detected"`
	}
	if err := json.Unmarshal([]byte(extractCodeReviewJSON(raw)), &payload); err != nil {
		return codeReviewOrchestratorSynthesis{}, fmt.Errorf("parse orchestrator synthesis: %w", err)
	}
	if payload.ApprovalRecommended == nil ||
		payload.DescriptionAssessments == nil ||
		payload.Findings == nil ||
		payload.HumanReviewReasons == nil ||
		payload.Summary == nil || strings.TrimSpace(*payload.Summary) == "" ||
		payload.ReviewSummary == nil || strings.TrimSpace(*payload.ReviewSummary) == "" ||
		payload.RiskNotes == nil ||
		payload.ScopeMismatch == nil ||
		payload.UnresolvedUncertainty == nil ||
		payload.ReviewerDisagreement == nil ||
		payload.PromptInjectionDetected == nil {
		return codeReviewOrchestratorSynthesis{}, errors.New("orchestrator synthesis is missing required fields")
	}
	for assessmentIndex := range *payload.DescriptionAssessments {
		assessment := &(*payload.DescriptionAssessments)[assessmentIndex]
		assessment.Key = strings.TrimSpace(assessment.Key)
		assessment.Reason = strings.TrimSpace(assessment.Reason)
		if assessment.Key == "" || assessment.Reason == "" || assessment.EvidenceIDs == nil {
			return codeReviewOrchestratorSynthesis{}, errors.New("orchestrator description assessment is missing required fields")
		}
		if err := assessment.EvidenceBasis.Validate(); err != nil {
			return codeReviewOrchestratorSynthesis{}, fmt.Errorf("orchestrator description assessment: %w", err)
		}
		switch assessment.Status {
		case codeReviewDescriptionAssessmentSatisfied:
			switch assessment.EvidenceBasis {
			case models.CodeReviewDescriptionEvidenceBasisImage:
				if len(assessment.EvidenceIDs) == 0 {
					return codeReviewOrchestratorSynthesis{}, errors.New("image-backed description assessment must cite visual evidence")
				}
			case models.CodeReviewDescriptionEvidenceBasisPreviewLink,
				models.CodeReviewDescriptionEvidenceBasisRepository,
				models.CodeReviewDescriptionEvidenceBasisPullRequestDescription,
				models.CodeReviewDescriptionEvidenceBasisDiff:
				if len(assessment.EvidenceIDs) != 0 {
					return codeReviewOrchestratorSynthesis{}, errors.New("non-image description assessment must not cite visual evidence")
				}
			default:
				return codeReviewOrchestratorSynthesis{}, errors.New("satisfied description assessment has an incompatible evidence basis")
			}
		case codeReviewDescriptionAssessmentNotApplicable:
			if assessment.EvidenceBasis != models.CodeReviewDescriptionEvidenceBasisNotApplicable || len(assessment.EvidenceIDs) != 0 {
				return codeReviewOrchestratorSynthesis{}, errors.New("not-applicable description assessment has an incompatible evidence basis")
			}
		case codeReviewDescriptionAssessmentMissing:
			if assessment.EvidenceBasis != models.CodeReviewDescriptionEvidenceBasisMissing || len(assessment.EvidenceIDs) != 0 {
				return codeReviewOrchestratorSynthesis{}, errors.New("missing description assessment has an incompatible evidence basis")
			}
		default:
			return codeReviewOrchestratorSynthesis{}, fmt.Errorf("orchestrator description assessment has invalid status %q", assessment.Status)
		}
		seenEvidenceIDs := make(map[string]struct{}, len(assessment.EvidenceIDs))
		for evidenceIndex, rawEvidenceID := range assessment.EvidenceIDs {
			evidenceID := strings.TrimSpace(rawEvidenceID)
			if evidenceID == "" {
				return codeReviewOrchestratorSynthesis{}, errors.New("orchestrator description assessment contains an empty evidence ID")
			}
			if _, duplicate := seenEvidenceIDs[evidenceID]; duplicate {
				return codeReviewOrchestratorSynthesis{}, fmt.Errorf("orchestrator description assessment cites evidence %q more than once", evidenceID)
			}
			seenEvidenceIDs[evidenceID] = struct{}{}
			assessment.EvidenceIDs[evidenceIndex] = evidenceID
		}
	}
	findings, err := normalizeCodeReviewOrchestratorFindings(*payload.Findings)
	if err != nil {
		return codeReviewOrchestratorSynthesis{}, err
	}
	humanReviewReasons, err := normalizeCodeReviewOrchestratorHumanReviewReasons(*payload.HumanReviewReasons)
	if err != nil {
		return codeReviewOrchestratorSynthesis{}, err
	}
	return codeReviewOrchestratorSynthesis{
		ApprovalRecommended:     *payload.ApprovalRecommended,
		DescriptionAssessments:  append([]codeReviewDescriptionAssessment{}, (*payload.DescriptionAssessments)...),
		Findings:                findings,
		HumanReviewReasons:      humanReviewReasons,
		Summary:                 *payload.Summary,
		ReviewSummary:           *payload.ReviewSummary,
		RiskNotes:               *payload.RiskNotes,
		ScopeMismatch:           *payload.ScopeMismatch,
		UnresolvedUncertainty:   *payload.UnresolvedUncertainty,
		ReviewerDisagreement:    *payload.ReviewerDisagreement,
		PromptInjectionDetected: *payload.PromptInjectionDetected,
	}, nil
}

func codeReviewOrchestratorSynthesisUsable(synthesis codeReviewOrchestratorSynthesis) bool {
	return strings.TrimSpace(synthesis.Summary) != "" &&
		strings.TrimSpace(synthesis.ReviewSummary) != "" &&
		synthesis.DescriptionAssessments != nil &&
		synthesis.Findings != nil &&
		synthesis.HumanReviewReasons != nil
}

func normalizeCodeReviewOrchestratorFindings(findings []codeReviewOrchestratorFinding) ([]codeReviewOrchestratorFinding, error) {
	if len(findings) > codeReviewOrchestratorFindingLimit {
		return nil, fmt.Errorf("orchestrator synthesis has %d findings; limit is %d", len(findings), codeReviewOrchestratorFindingLimit)
	}
	normalized := make([]codeReviewOrchestratorFinding, 0, len(findings))
	for _, finding := range findings {
		if err := finding.Severity.Validate(); err != nil {
			return nil, fmt.Errorf("orchestrator finding severity: %w", err)
		}
		if finding.Severity == models.CodeReviewFindingSeverityInfo {
			return nil, errors.New("orchestrator finding severity must map to P0, P1, P2, or P3")
		}
		if err := finding.Confidence.Validate(); err != nil {
			return nil, fmt.Errorf("orchestrator finding confidence: %w", err)
		}
		finding.Summary = strings.TrimSpace(finding.Summary)
		finding.Body = strings.TrimSpace(finding.Body)
		if finding.Summary == "" || finding.Body == "" {
			return nil, errors.New("orchestrator finding is missing summary or body")
		}
		if utf8.RuneCountInString(finding.Summary) > codeReviewOrchestratorFindingSummaryLimit {
			return nil, fmt.Errorf("orchestrator finding summary exceeds %d characters", codeReviewOrchestratorFindingSummaryLimit)
		}
		if utf8.RuneCountInString(finding.Body) > codeReviewOrchestratorFindingBodyLimit {
			return nil, fmt.Errorf("orchestrator finding body exceeds %d characters", codeReviewOrchestratorFindingBodyLimit)
		}
		if finding.Path != nil {
			path := strings.TrimSpace(*finding.Path)
			if path == "" {
				finding.Path = nil
			} else {
				finding.Path = &path
			}
		}
		if finding.StartLine != nil && *finding.StartLine <= 0 {
			return nil, errors.New("orchestrator finding start_line must be positive")
		}
		if finding.EndLine != nil && *finding.EndLine <= 0 {
			return nil, errors.New("orchestrator finding end_line must be positive")
		}
		if finding.StartLine != nil && finding.Path == nil {
			return nil, errors.New("orchestrator finding with start_line must include path")
		}
		if finding.EndLine != nil && finding.StartLine == nil {
			return nil, errors.New("orchestrator finding with end_line must include start_line")
		}
		if finding.StartLine != nil && finding.EndLine == nil {
			finding.EndLine = finding.StartLine
		}
		if finding.StartLine != nil && finding.EndLine != nil && *finding.EndLine < *finding.StartLine {
			return nil, errors.New("orchestrator finding end_line must not precede start_line")
		}
		normalized = append(normalized, finding)
	}
	return normalized, nil
}

func normalizeCodeReviewOrchestratorHumanReviewReasons(reasons []codeReviewOrchestratorHumanReviewReason) ([]codeReviewOrchestratorHumanReviewReason, error) {
	if len(reasons) > codeReviewOrchestratorHumanReviewReasonLimit {
		return nil, fmt.Errorf("orchestrator synthesis has %d human review reasons; limit is %d", len(reasons), codeReviewOrchestratorHumanReviewReasonLimit)
	}
	normalized := make([]codeReviewOrchestratorHumanReviewReason, 0, len(reasons))
	for _, reason := range reasons {
		if err := reason.Code.Validate(); err != nil {
			return nil, fmt.Errorf("orchestrator human review reason: %w", err)
		}
		reason.Summary = strings.TrimSpace(reason.Summary)
		if reason.Summary == "" {
			return nil, errors.New("orchestrator human review reason is missing summary")
		}
		if utf8.RuneCountInString(reason.Summary) > codeReviewOrchestratorHumanReviewSummaryLimit {
			return nil, fmt.Errorf("orchestrator human review reason summary exceeds %d characters", codeReviewOrchestratorHumanReviewSummaryLimit)
		}
		normalized = append(normalized, reason)
	}
	return normalized, nil
}

func codeReviewOrchestratorSynthesisFromResults(results []models.CodeReviewAgentResult) codeReviewOrchestratorSynthesis {
	for _, result := range results {
		if result.Role != models.CodeReviewAgentRoleOrchestrator || result.Status != models.CodeReviewAgentResultStatusCompleted {
			continue
		}
		state, ok := parseCodeReviewOrchestratorStructuredResult(result.StructuredResult)
		if !ok || !state.SynthesisValidated || !codeReviewOrchestratorSynthesisUsable(state.Synthesis) {
			continue
		}
		synthesis := state.Synthesis
		synthesis.DescriptionInputHash = state.DescriptionInputHash
		return synthesis
	}
	return codeReviewOrchestratorSynthesis{}
}

func codeReviewOrchestratorEvidence(results []models.CodeReviewAgentResult) (present, usable bool) {
	for _, result := range results {
		if result.Role != models.CodeReviewAgentRoleOrchestrator {
			continue
		}
		present = true
		if result.Status != models.CodeReviewAgentResultStatusCompleted {
			continue
		}
		state, ok := parseCodeReviewOrchestratorStructuredResult(result.StructuredResult)
		if ok && state.SynthesisValidated && codeReviewOrchestratorSynthesisUsable(state.Synthesis) {
			usable = true
		}
	}
	return present, usable
}

func codeReviewOrchestratorOperationalSummary(results []models.CodeReviewAgentResult, reasons []models.CodeReviewRiskReason) string {
	if !codeReviewRiskReasonsContain(reasons, models.CodeReviewRiskReasonOrchestratorSynthesisInvalid) {
		return ""
	}

	for _, result := range results {
		if result.Role != models.CodeReviewAgentRoleOrchestrator {
			continue
		}
		switch result.Status {
		case models.CodeReviewAgentResultStatusTimedOut:
			return "143 could not complete the final synthesis because the orchestration step timed out. The automated review is incomplete; this is not a code-quality finding."
		case models.CodeReviewAgentResultStatusFailed:
			state, _ := parseCodeReviewOrchestratorStructuredResult(result.StructuredResult)
			detail := strings.ToLower(strings.TrimSpace(state.Error))
			if detail == "" && result.RawOutput != nil {
				detail = strings.ToLower(strings.TrimSpace(*result.RawOutput))
			}
			if strings.Contains(detail, "no authenticated coding agent") {
				return "143 could not run the final synthesis because no authenticated orchestrator was available. The automated review is incomplete; this is a configuration issue, not a code-quality finding."
			}
			if strings.Contains(detail, "synthesis") || strings.Contains(detail, "required field") || strings.Contains(detail, "valid json") {
				return "143 received reviewer output, but the final synthesis did not match the required response format. The automated review is incomplete; this is not a code-quality finding."
			}
			return "143 could not complete the final synthesis because the orchestration step failed. The automated review is incomplete; this is not a code-quality finding."
		case models.CodeReviewAgentResultStatusCompleted:
			return "143 received reviewer output, but the final synthesis did not match the required response format. The automated review is incomplete; this is not a code-quality finding."
		default:
			return "143 could not complete the final synthesis because the orchestration step did not finish. The automated review is incomplete; this is not a code-quality finding."
		}
	}

	return "143 could not complete the final synthesis because the orchestration step did not return a usable result. The automated review is incomplete; this is not a code-quality finding."
}

func codeReviewRiskReasonsContain(reasons []models.CodeReviewRiskReason, expected models.CodeReviewRiskReasonCode) bool {
	for _, reason := range reasons {
		if reason.Code == expected {
			return true
		}
	}
	return false
}

func codeReviewOrchestratorReviewSummary(synthesis codeReviewOrchestratorSynthesis) string {
	if summary := strings.TrimSpace(synthesis.ReviewSummary); summary != "" {
		return summary
	}
	return strings.TrimSpace(synthesis.Summary)
}

func codeReviewOrchestratorRepairPrompt(validationErr error, policy models.CodeReviewPolicyConfig, changedFiles []codereviewsvc.PullRequestFile, visualEvidence models.CodeReviewVisualEvidenceSnapshot) string {
	return strings.TrimSpace(prompts.CodeReviewOrchestratorRepairPrompt(prompts.CodeReviewOrchestratorRepairPromptData{
		ValidationError:         validationErr.Error(),
		DescriptionRequirements: codeReviewDescriptionRequirementsForPrompt(policy, changedFiles),
		VisualEvidence:          codeReviewAvailableVisualEvidenceForPrompt(visualEvidence),
	}))
}

func codeReviewOrchestratorCombinedOutput(previous *string, current string, repairCount int) string {
	current = strings.TrimSpace(current)
	if repairCount <= 0 || previous == nil || strings.TrimSpace(*previous) == "" {
		return current
	}
	return strings.TrimSpace(*previous) + "\n\n--- synthesis repair response ---\n\n" + current
}

func codeReviewOrchestratorObserveRepairCompletion(state codeReviewOrchestratorStructuredResult, currentTurn int) codeReviewOrchestratorStructuredResult {
	if !state.SynthesisRepairPending || currentTurn <= state.SynthesisRepairBaseTurn {
		return state
	}
	nextState := state
	nextState.SynthesisRepairPending = false
	if nextState.SynthesisRepairCount < codeReviewOrchestratorSynthesisRepairLimit {
		nextState.SynthesisRepairCount++
	}
	return nextState
}

func extractCodeReviewJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}"
	}
	if start := strings.LastIndex(raw, "```json"); start >= 0 {
		rest := raw[start+len("```json"):]
		if end := strings.Index(rest, "```"); end >= 0 {
			return strings.TrimSpace(rest[:end])
		}
	}
	start := strings.LastIndex(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start >= 0 && end > start {
		return strings.TrimSpace(raw[start : end+1])
	}
	return raw
}

type codeReviewOrchestratorRepairSender interface {
	SendMessage(context.Context, threadsvc.SendMessageInput) (*threadsvc.SendMessageResult, error)
}

func persistCodeReviewOrchestratorFindings(
	ctx context.Context,
	stores *Stores,
	job runCodeReviewPayload,
	resultID uuid.UUID,
	synthesis *codeReviewOrchestratorSynthesis,
	raw string,
	changedPaths []string,
) (int, error) {
	findings := make([]models.CodeReviewFinding, 0)
	if synthesis != nil {
		findings = codeReviewFindingsFromSynthesis(*synthesis, changedPaths)
	}
	seen := make(map[string]struct{}, len(findings))
	for _, finding := range findings {
		seen[finding.DedupeKey] = struct{}{}
	}
	for _, legacyFinding := range parseCodeReviewFindings(raw, changedPaths) {
		if _, ok := seen[legacyFinding.DedupeKey]; ok {
			continue
		}
		seen[legacyFinding.DedupeKey] = struct{}{}
		findings = append(findings, legacyFinding)
	}
	for i := range findings {
		findings[i].OrgID = job.OrgID
		findings[i].SessionID = job.SessionID
		findings[i].AgentResultID = &resultID
		if err := stores.CodeReviews.ReplaceFinding(ctx, &findings[i]); err != nil {
			return 0, fmt.Errorf("create harvested orchestrator code review finding: %w", err)
		}
	}
	return len(findings), nil
}

func codeReviewFindingsFromSynthesis(synthesis codeReviewOrchestratorSynthesis, changedPaths []string) []models.CodeReviewFinding {
	findings := make([]models.CodeReviewFinding, 0, len(synthesis.Findings))
	for _, structured := range synthesis.Findings {
		path := ""
		if structured.Path != nil {
			path = codeReviewNormalizeFindingPath(*structured.Path, changedPaths)
		}
		var pathPtr *string
		if path != "" {
			pathPtr = &path
		}
		startLine := 0
		if structured.StartLine != nil {
			startLine = *structured.StartLine
		}
		endLine := startLine
		if structured.EndLine != nil {
			endLine = *structured.EndLine
		}
		findings = append(findings, models.CodeReviewFinding{
			DedupeKey:  codeReviewFindingDedupeKey(path, startLine, endLine, structured.Summary),
			Severity:   structured.Severity,
			Confidence: structured.Confidence,
			Path:       pathPtr,
			StartLine:  structured.StartLine,
			EndLine:    structured.EndLine,
			Summary:    structured.Summary,
			Body:       structured.Body,
		})
	}
	return findings
}

func requestCodeReviewOrchestratorSynthesisRepair(
	ctx context.Context,
	stores *Stores,
	sender codeReviewOrchestratorRepairSender,
	logger zerolog.Logger,
	job runCodeReviewPayload,
	policy models.CodeReviewPolicyConfig,
	changedFiles []codereviewsvc.PullRequestFile,
	result models.CodeReviewAgentResult,
	state codeReviewOrchestratorStructuredResult,
	threadCurrentTurn int,
	raw string,
	validationErr error,
	visualEvidence models.CodeReviewVisualEvidenceSnapshot,
) (bool, bool, error) {
	if state.SynthesisRepairCount >= codeReviewOrchestratorSynthesisRepairLimit {
		return false, false, nil
	}
	threadID, err := uuid.Parse(strings.TrimSpace(state.ThreadID))
	if err != nil {
		return false, false, fmt.Errorf("parse orchestrator thread id for synthesis repair: %w", err)
	}

	findingCount, err := persistCodeReviewOrchestratorFindings(ctx, stores, job, result.ID, nil, raw, codeReviewChangedPaths(changedFiles))
	if err != nil {
		return false, false, err
	}
	nextState := state
	nextState.Synthesis = codeReviewOrchestratorSynthesis{}
	nextState.SynthesisValidated = false
	nextState.SynthesisRepairPending = true
	if !state.SynthesisRepairPending {
		nextState.SynthesisRepairBaseTurn = threadCurrentTurn
	}
	if findingCount > nextState.FindingCount {
		nextState.FindingCount = findingCount
	}
	nextState.Error = "repairing invalid orchestrator synthesis: " + validationErr.Error()
	nextState.CompletedAt = ""

	rawOutput, rawRecordKey, err := codeReviewRawOutputForStorage(ctx, stores, job, result.ID, models.CodeReviewAgentRoleOrchestrator, result.AgentProvider, raw)
	if err != nil {
		return false, false, err
	}
	nextState.RawRecordKey = rawRecordKey
	if _, err := stores.CodeReviews.UpdateAgentResultOutcome(
		ctx,
		job.OrgID,
		result.ID,
		models.CodeReviewAgentResultStatusRunning,
		rawOutput,
		marshalCodeReviewOrchestratorStructuredResult(nextState),
	); err != nil {
		return false, false, fmt.Errorf("record orchestrator synthesis repair: %w", err)
	}

	if _, err := sender.SendMessage(ctx, threadsvc.SendMessageInput{
		SessionID:     job.SessionID,
		OrgID:         job.OrgID,
		ThreadID:      threadID,
		Message:       codeReviewOrchestratorRepairPrompt(validationErr, policy, changedFiles, visualEvidence),
		MessageSource: models.SessionMessageSourceAgentTool,
	}); err != nil {
		logger.Warn().Err(err).
			Str("session_id", job.SessionID.String()).
			Str("thread_id", nextState.ThreadID).
			Msg("failed to request orchestrator synthesis repair")
		return true, false, fmt.Errorf("request orchestrator synthesis repair: %w", err)
	}

	logger.Info().
		Str("session_id", job.SessionID.String()).
		Str("thread_id", nextState.ThreadID).
		Int("repair_count", nextState.SynthesisRepairCount).
		Bool("repair_pending", nextState.SynthesisRepairPending).
		Msg("requested orchestrator synthesis repair")
	return true, true, nil
}

func codeReviewReviewerResultTerminal(status models.CodeReviewAgentResultStatus) bool {
	switch status {
	case models.CodeReviewAgentResultStatusCompleted, models.CodeReviewAgentResultStatusFailed, models.CodeReviewAgentResultStatusTimedOut:
		return true
	default:
		return false
	}
}

type codeReviewAgentPhase string

const (
	codeReviewAgentPhaseNone         codeReviewAgentPhase = ""
	codeReviewAgentPhaseReviewers    codeReviewAgentPhase = "reviewers"
	codeReviewAgentPhaseOrchestrator codeReviewAgentPhase = "orchestrator"
)

func codeReviewInFlightAgentPhase(ctx context.Context, stores *Stores, job runCodeReviewPayload, pr models.PullRequest, policy models.CodeReviewPolicyConfig, metadata models.CodeReviewSessionMetadata) (codeReviewAgentPhase, error) {
	if stores == nil || stores.CodeReviews == nil || stores.SessionThreads == nil {
		return codeReviewAgentPhaseNone, nil
	}
	if codeReviewHeadChanged(job.HeadSHA, pr, nil) {
		return codeReviewAgentPhaseNone, nil
	}
	results, err := stores.CodeReviews.ListAgentResults(ctx, job.OrgID, job.SessionID)
	if err != nil {
		return codeReviewAgentPhaseNone, fmt.Errorf("list code review agent results for in-flight check: %w", err)
	}
	hasNonterminalAgent := false
	for _, result := range results {
		if (result.Role == models.CodeReviewAgentRoleReviewer || result.Role == models.CodeReviewAgentRoleOrchestrator) && !codeReviewReviewerResultTerminal(result.Status) {
			hasNonterminalAgent = true
			break
		}
	}
	if !hasNonterminalAgent {
		return codeReviewAgentPhaseNone, nil
	}
	threads, err := stores.SessionThreads.ListBySession(ctx, job.OrgID, job.SessionID)
	if err != nil {
		return codeReviewAgentPhaseNone, fmt.Errorf("list code review threads for in-flight check: %w", err)
	}
	phase := codeReviewInFlightAgentPhaseFromState(policy, results, threads)
	if codeReviewInFlightAgentPhaseTimedOut(time.Now(), policy, metadata, results, phase) {
		return codeReviewAgentPhaseNone, nil
	}
	return phase, nil
}

func codeReviewInFlightAgentPhaseTimedOut(now time.Time, policy models.CodeReviewPolicyConfig, metadata models.CodeReviewSessionMetadata, results []models.CodeReviewAgentResult, phase codeReviewAgentPhase) bool {
	switch phase {
	case codeReviewAgentPhaseReviewers:
		return now.After(codeReviewReviewDeadline(policy, metadata))
	case codeReviewAgentPhaseOrchestrator:
		for _, result := range results {
			if result.Role == models.CodeReviewAgentRoleOrchestrator && !codeReviewReviewerResultTerminal(result.Status) {
				return now.After(codeReviewOrchestratorResultDeadline(policy, result))
			}
		}
		return true
	}
	return false
}

func codeReviewInFlightAgentPhaseFromState(policy models.CodeReviewPolicyConfig, results []models.CodeReviewAgentResult, threads []models.SessionThread) codeReviewAgentPhase {
	threadsByID := make(map[uuid.UUID]models.SessionThread, len(threads))
	for _, thread := range threads {
		threadsByID[thread.ID] = thread
	}

	orchestratorResults := make([]models.CodeReviewAgentResult, 0, 1)
	for _, result := range results {
		if result.Role != models.CodeReviewAgentRoleOrchestrator {
			continue
		}
		if codeReviewReviewerResultTerminal(result.Status) {
			return codeReviewAgentPhaseNone
		}
		orchestratorResults = append(orchestratorResults, result)
	}
	if len(orchestratorResults) > 0 {
		for _, result := range orchestratorResults {
			state, ok := parseCodeReviewOrchestratorStructuredResult(result.StructuredResult)
			threadID, parseErr := uuid.Parse(strings.TrimSpace(state.ThreadID))
			thread, found := threadsByID[threadID]
			if !ok || parseErr != nil || !found || !codeReviewThreadStillRunning(thread.Status) {
				return codeReviewAgentPhaseNone
			}
		}
		return codeReviewAgentPhaseOrchestrator
	}

	byKey := codeReviewReviewerResultsByKey(results)
	if len(policy.AgentRoster.Reviewers) == 0 {
		return codeReviewAgentPhaseNone
	}
	waiting := false
	for idx, agentType := range policy.AgentRoster.Reviewers {
		result, found := byKey[codeReviewReviewerKey(idx, agentType)]
		if !found {
			return codeReviewAgentPhaseNone
		}
		if codeReviewReviewerResultTerminal(result.Status) {
			continue
		}
		state, ok := parseCodeReviewReviewerStructuredResult(result.StructuredResult)
		threadID, parseErr := uuid.Parse(strings.TrimSpace(state.ThreadID))
		thread, found := threadsByID[threadID]
		if !ok || parseErr != nil || !found || !codeReviewThreadStillRunning(thread.Status) {
			return codeReviewAgentPhaseNone
		}
		waiting = true
	}
	if waiting {
		return codeReviewAgentPhaseReviewers
	}
	return codeReviewAgentPhaseNone
}

func codeReviewReviewTimedOut(policy models.CodeReviewPolicyConfig, metadata models.CodeReviewSessionMetadata) bool {
	return time.Now().After(codeReviewReviewDeadline(policy, metadata))
}

func codeReviewReviewDeadline(policy models.CodeReviewPolicyConfig, metadata models.CodeReviewSessionMetadata) time.Time {
	return codeReviewAgentDeadline(policy, metadata.CreatedAt)
}

func codeReviewOrchestratorDispatchDeadline(policy models.CodeReviewPolicyConfig, metadata models.CodeReviewSessionMetadata, results []models.CodeReviewAgentResult) time.Time {
	startedAt := metadata.CreatedAt
	for _, result := range results {
		if result.Role != models.CodeReviewAgentRoleReviewer || !codeReviewReviewerResultTerminal(result.Status) {
			continue
		}
		state, ok := parseCodeReviewReviewerStructuredResult(result.StructuredResult)
		if !ok {
			continue
		}
		completedAt, err := time.Parse(time.RFC3339, state.CompletedAt)
		if err == nil && completedAt.After(startedAt) {
			startedAt = completedAt
		}
	}
	return codeReviewAgentDeadline(policy, startedAt)
}

func codeReviewOrchestratorResultDeadline(policy models.CodeReviewPolicyConfig, result models.CodeReviewAgentResult) time.Time {
	return codeReviewAgentDeadline(policy, result.CreatedAt)
}

func codeReviewAgentDeadline(policy models.CodeReviewPolicyConfig, startedAt time.Time) time.Time {
	timeout := time.Duration(policy.AgentRoster.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	return startedAt.Add(timeout)
}

func codeReviewThreadCompletedByDeadline(thread models.SessionThread, deadline time.Time) bool {
	return !codeReviewThreadStillRunning(thread.Status) &&
		thread.CompletedAt != nil &&
		!thread.CompletedAt.After(deadline)
}

func codeReviewThreadCompletionTime(thread models.SessionThread) time.Time {
	if !codeReviewThreadStillRunning(thread.Status) && thread.CompletedAt != nil {
		return thread.CompletedAt.UTC()
	}
	return time.Now().UTC()
}

func codeReviewThreadStillRunning(status models.ThreadStatus) bool {
	return status == models.ThreadStatusPending || status == models.ThreadStatusRunning || status == models.ThreadStatusAwaitingInput
}

func latestAssistantMessageForThread(ctx context.Context, stores *Stores, orgID, threadID uuid.UUID) (string, bool, error) {
	messages, err := stores.SessionMessages.ListByThread(ctx, orgID, threadID)
	if err != nil {
		return "", false, fmt.Errorf("list reviewer thread messages: %w", err)
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != models.MessageRoleAssistant {
			continue
		}
		content := strings.TrimSpace(messages[i].Content)
		if content == "" {
			continue
		}
		return content, true, nil
	}
	return "", false, nil
}

func codeReviewReviewerRosterTerminal(policy models.CodeReviewPolicyConfig, results []models.CodeReviewAgentResult) bool {
	byKey := codeReviewReviewerResultsByKey(results)
	for idx, agentType := range policy.AgentRoster.Reviewers {
		result, ok := byKey[codeReviewReviewerKey(idx, agentType)]
		if !ok || !codeReviewReviewerResultTerminal(result.Status) {
			return false
		}
	}
	return true
}

func codeReviewReviewerExecutionFailed(policy models.CodeReviewPolicyConfig, results []models.CodeReviewAgentResult) bool {
	if !codeReviewReviewerRosterTerminal(policy, results) {
		return false
	}
	return !codeReviewHasUsableReviewerOutput(results)
}

func codeReviewHasUsableReviewerOutput(results []models.CodeReviewAgentResult) bool {
	quorum, _ := codeReviewReviewerEvidence(results)
	return quorum > 0
}

func codeReviewNoUsableReviewerOutputReason(results []models.CodeReviewAgentResult) string {
	summaries := codeReviewAgentSummaries(results, nil)
	if len(summaries) == 0 {
		return "no code review reviewer agents were able to run"
	}
	return "no code review reviewer produced usable output: " + strings.Join(summaries, ", ")
}

func codeReviewWaitingForReviewers(policy models.CodeReviewPolicyConfig) error {
	delay := 10 * time.Second
	if policy.AgentRoster.TimeoutSeconds <= 120 {
		delay = 5 * time.Second
	}
	return &RetryableError{
		Err:                    errors.New("waiting for code review reviewer agents"),
		RetryAfter:             &delay,
		BypassMaxRetryDuration: true,
	}
}

func ensureCodeReviewOrchestratorThread(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, job runCodeReviewPayload, pr models.PullRequest, health *models.PullRequestHealthResponse, policy models.CodeReviewPolicyRecord, metadata models.CodeReviewSessionMetadata, changedFiles []codereviewsvc.PullRequestFile, agentResults []models.CodeReviewAgentResult, findings []models.CodeReviewFinding, visualEvidence models.CodeReviewVisualEvidenceSnapshot) error {
	for _, result := range agentResults {
		if result.Role == models.CodeReviewAgentRoleOrchestrator {
			return nil
		}
	}
	cfg := policy.Config()
	selection, err := resolveCodeReviewOrchestratorAvailability(ctx, services, job.OrgID, cfg)
	if err != nil {
		return err
	}
	agentType := selection.AgentType
	agentModel := selection.AgentModel
	reasoningEffort := selection.ReasoningEffort
	if !selection.Available {
		raw := "orchestrator skipped because no authenticated coding agent is configured"
		result := &models.CodeReviewAgentResult{
			OrgID:         job.OrgID,
			SessionID:     job.SessionID,
			AgentProvider: string(agentType),
			AgentModel:    agentModel,
			Role:          models.CodeReviewAgentRoleOrchestrator,
			Status:        models.CodeReviewAgentResultStatusFailed,
			RawOutput:     &raw,
			StructuredResult: marshalCodeReviewOrchestratorStructuredResult(codeReviewOrchestratorStructuredResult{
				Error:       raw,
				CompletedAt: time.Now().UTC().Format(time.RFC3339),
			}),
		}
		if err := stores.CodeReviews.CreateAgentResult(ctx, result); err != nil {
			return fmt.Errorf("create unavailable code review orchestrator result: %w", err)
		}
		logger.Info().
			Str("session_id", job.SessionID.String()).
			Msg("skipped unavailable code review orchestrator")
		return nil
	}
	if time.Now().After(codeReviewOrchestratorDispatchDeadline(cfg, metadata, agentResults)) {
		raw := "orchestrator timed out before the worker could start the orchestrator thread"
		result := &models.CodeReviewAgentResult{
			OrgID:         job.OrgID,
			SessionID:     job.SessionID,
			AgentProvider: string(agentType),
			AgentModel:    agentModel,
			Role:          models.CodeReviewAgentRoleOrchestrator,
			Status:        models.CodeReviewAgentResultStatusTimedOut,
			RawOutput:     &raw,
			StructuredResult: marshalCodeReviewOrchestratorStructuredResult(codeReviewOrchestratorStructuredResult{
				Error:       raw,
				CompletedAt: time.Now().UTC().Format(time.RFC3339),
			}),
		}
		return stores.CodeReviews.CreateAgentResult(ctx, result)
	}
	job.PullRequestAuthorTeams, err = resolveCodeReviewAuthorTeams(ctx, stores, services, cfg, job, pr)
	if err != nil {
		return err
	}
	rootKey := codeReviewPromptRecordRoot(metadata, job)
	recordKey := fmt.Sprintf("%s/orchestrator-%s", rootKey, agentType)
	promptText := codeReviewOrchestratorPrompt(job, pr, health, cfg, policy.Version, metadata.BaseSHA, changedFiles, agentResults, findings, visualEvidence)
	descriptionInputHash := codeReviewDescriptionInputHash(pr, visualEvidence)
	if err := storeCodeReviewPromptRecord(ctx, stores, models.CodeReviewPromptRecord{
		OrgID:         job.OrgID,
		SessionID:     job.SessionID,
		RecordKey:     recordKey,
		Role:          string(models.CodeReviewAgentRoleOrchestrator),
		AgentProvider: string(agentType),
		Content:       promptText,
		Metadata: mustMarshalCodeReviewJSON(map[string]any{
			"head_sha":             job.HeadSHA,
			"policy_version":       policy.Version,
			"agent_model":          stringPtrValue(agentModel),
			"input_hash":           descriptionInputHash,
			"visual_evidence_hash": visualEvidence.CanonicalHash(),
		}),
	}); err != nil {
		return err
	}
	threads := threadsvc.NewService(stores.SessionThreads, stores.Sessions, stores.SessionMessages, stores.SessionLogs, stores.Jobs, logger)
	// Run the orchestrator on the session's primary ("Main") thread rather than
	// spinning up a dedicated tab. The primary thread starts with the policy's
	// configured orchestrator and is retargeted below when only a reviewer agent
	// is authenticated. The reviewers keep their own read-only tabs; only the
	// final synthesis is folded back onto the main thread.
	session, err := stores.Sessions.GetByID(ctx, job.OrgID, job.SessionID)
	if err != nil {
		return fmt.Errorf("load code review session for orchestrator: %w", err)
	}

	// A sibling reviewer's sandbox-node retry can park the parent session in a
	// non-claimable 'pending' state. Older workers could also mark the shared
	// code-review parent failed when one reviewer failed, even though this
	// controller still had quorum and needed to dispatch synthesis. Every
	// reviewer thread is terminal by this point, so normalize either poisoned
	// state back to idle. Cancelled/completed parents remain terminal and are
	// handled before this phase.
	if codeReviewSessionNeedsOrchestratorNormalization(session) {
		previousStatus := session.Status
		if resetErr := stores.Sessions.UpdateStatus(ctx, job.OrgID, job.SessionID, models.SessionStatusIdle); resetErr != nil {
			logger.Warn().Err(resetErr).
				Str("session_id", job.SessionID.String()).
				Str("previous_status", string(previousStatus)).
				Msg("failed to normalize code review session before orchestrator dispatch")
		} else {
			session.Status = models.SessionStatusIdle
			logger.Warn().
				Str("session_id", job.SessionID.String()).
				Str("previous_status", string(previousStatus)).
				Msg("normalized code review session to idle before orchestrator dispatch")
		}
	}

	threadID, err := primaryThreadIDForSession(ctx, stores, session)
	if err != nil {
		return fmt.Errorf("resolve code review primary thread for orchestrator: %w", err)
	}
	primaryThread, err := stores.SessionThreads.GetByID(ctx, job.OrgID, threadID)
	if err != nil {
		return fmt.Errorf("load code review primary thread for orchestrator: %w", err)
	}
	if primaryThread.AgentType != agentType ||
		!codeReviewAgentModelsEqual(primaryThread.ModelOverride, agentModel) ||
		!codeReviewReasoningEffortsEqual(primaryThread.ReasoningEffort, reasoningEffort) {
		model := ""
		if agentModel != nil {
			model = *agentModel
		}
		_, updateErr := threads.UpdateThread(ctx, threadsvc.UpdateThreadInput{
			SessionID:       job.SessionID,
			OrgID:           job.OrgID,
			ThreadID:        threadID,
			AgentType:       string(agentType),
			Model:           &model,
			ReasoningEffort: reasoningEffort,
			Label:           primaryThread.Label,
		})
		if updateErr != nil {
			return fmt.Errorf("retarget code review primary thread to available orchestrator %s: %w", agentType, updateErr)
		}
		logger.Info().
			Str("session_id", job.SessionID.String()).
			Str("thread_id", threadID.String()).
			Str("orchestrator", string(agentType)).
			Msg("retargeted code review primary thread to available orchestrator")
	}
	structured := marshalCodeReviewOrchestratorStructuredResult(codeReviewOrchestratorStructuredResult{
		ThreadID:             threadID.String(),
		PromptRecordKey:      recordKey,
		DescriptionInputHash: descriptionInputHash,
		ReadOnly:             false,
	})
	// The orchestrator agent result is created only once the thread is actually
	// dispatched. A transient claim race leaves no result behind, so the next
	// run_code_review poll re-enters this function cleanly and retries.
	if _, err := threads.SendMessage(ctx, codeReviewAgentMessageInput(job, threadID, promptText, nil, visualEvidence)); err != nil {
		// Transient: the session was momentarily non-resumable despite the reset
		// above (e.g. re-parked by a sibling's sandbox-node retry between the
		// reset and the claim). Don't record a permanent orchestrator failure —
		// let run_code_review re-poll so synthesis dispatches once the session
		// settles. The orchestrator runs on the Main thread, so there is no
		// transient tab to clean up, and no agent result exists yet, so the next
		// pass re-enters this function cleanly.
		if errors.Is(err, threadsvc.ErrSessionNotResumable) {
			logger.Warn().Err(err).Str("thread_id", threadID.String()).Msg("code review session was not resumable for orchestrator dispatch; retrying")
			return codeReviewWaitingForOrchestrator(cfg)
		}
		// Permanent failure: record a terminal orchestrator result so the review
		// can finish in a degraded state instead of looping forever.
		raw := err.Error()
		failed := &models.CodeReviewAgentResult{
			OrgID:         job.OrgID,
			SessionID:     job.SessionID,
			AgentProvider: string(agentType),
			AgentModel:    agentModel,
			Role:          models.CodeReviewAgentRoleOrchestrator,
			Status:        models.CodeReviewAgentResultStatusFailed,
			RawOutput:     &raw,
			StructuredResult: marshalCodeReviewOrchestratorStructuredResult(codeReviewOrchestratorStructuredResult{
				ThreadID:             threadID.String(),
				PromptRecordKey:      recordKey,
				DescriptionInputHash: descriptionInputHash,
				Error:                raw,
				CompletedAt:          time.Now().UTC().Format(time.RFC3339),
			}),
		}
		if createErr := stores.CodeReviews.CreateAgentResult(ctx, failed); createErr != nil {
			return fmt.Errorf("create failed code review orchestrator result: %w", createErr)
		}
		logger.Warn().Err(err).Str("thread_id", threadID.String()).Msg("failed to start code review orchestrator thread")
		return nil
	}
	result := &models.CodeReviewAgentResult{
		OrgID:            job.OrgID,
		SessionID:        job.SessionID,
		AgentProvider:    string(agentType),
		AgentModel:       agentModel,
		Role:             models.CodeReviewAgentRoleOrchestrator,
		Status:           models.CodeReviewAgentResultStatusRunning,
		StructuredResult: structured,
	}
	if err := stores.CodeReviews.CreateAgentResult(ctx, result); err != nil {
		return fmt.Errorf("create code review orchestrator result: %w", err)
	}
	return nil
}

func codeReviewSessionNeedsOrchestratorNormalization(session models.Session) bool {
	return session.Status == models.SessionStatusPending ||
		(session.Status == models.SessionStatusFailed && session.Origin == models.SessionOriginCodeReview)
}

func codeReviewAgentModelsEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return strings.TrimSpace(*left) == strings.TrimSpace(*right)
}

func codeReviewReasoningEffortsEqual(left, right *models.ReasoningEffort) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func harvestCodeReviewOrchestratorResult(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, job runCodeReviewPayload, policy models.CodeReviewPolicyRecord, changedFiles []codereviewsvc.PullRequestFile, visualEvidence models.CodeReviewVisualEvidenceSnapshot) error {
	results, err := stores.CodeReviews.ListAgentResults(ctx, job.OrgID, job.SessionID)
	if err != nil {
		return fmt.Errorf("list code review orchestrator results for harvest: %w", err)
	}
	changedPaths := codeReviewChangedPaths(changedFiles)
	for _, result := range results {
		if result.Role != models.CodeReviewAgentRoleOrchestrator || codeReviewReviewerResultTerminal(result.Status) {
			continue
		}
		state, ok := parseCodeReviewOrchestratorStructuredResult(result.StructuredResult)
		if !ok {
			raw := "orchestrator result has a malformed structured result"
			if _, err := stores.CodeReviews.UpdateAgentResultOutcome(ctx, job.OrgID, result.ID, models.CodeReviewAgentResultStatusFailed, &raw, result.StructuredResult); err != nil {
				return fmt.Errorf("mark malformed orchestrator result failed: %w", err)
			}
			continue
		}
		if strings.TrimSpace(state.ThreadID) == "" {
			raw := "orchestrator result is missing its thread id"
			state.Error = raw
			state.CompletedAt = time.Now().UTC().Format(time.RFC3339)
			if _, err := stores.CodeReviews.UpdateAgentResultOutcome(ctx, job.OrgID, result.ID, models.CodeReviewAgentResultStatusFailed, &raw, marshalCodeReviewOrchestratorStructuredResult(state)); err != nil {
				return fmt.Errorf("mark malformed orchestrator result failed: %w", err)
			}
			continue
		}
		threadID, err := uuid.Parse(state.ThreadID)
		if err != nil {
			raw := "orchestrator result has an invalid thread id: " + err.Error()
			state.Error = raw
			state.CompletedAt = time.Now().UTC().Format(time.RFC3339)
			if _, err := stores.CodeReviews.UpdateAgentResultOutcome(ctx, job.OrgID, result.ID, models.CodeReviewAgentResultStatusFailed, &raw, marshalCodeReviewOrchestratorStructuredResult(state)); err != nil {
				return fmt.Errorf("mark invalid orchestrator result failed: %w", err)
			}
			continue
		}
		thread, err := stores.SessionThreads.GetByID(ctx, job.OrgID, threadID)
		if err != nil {
			return fmt.Errorf("load code review orchestrator thread: %w", err)
		}
		deadline := codeReviewOrchestratorResultDeadline(policy.Config(), result)
		timedOut := time.Now().After(deadline)
		state.CostCents = thread.CostCents
		state = codeReviewOrchestratorObserveRepairCompletion(state, thread.CurrentTurn)
		// A terminal synthesis is useful evidence even when a delayed job resume
		// observes it after the deadline, but only if it completed in time.
		if timedOut && !codeReviewThreadCompletedByDeadline(thread, deadline) {
			raw := "orchestrator did not produce a completed turn before the review deadline"
			state.Error = raw
			completedAt := codeReviewThreadCompletionTime(thread)
			if codeReviewThreadStillRunning(thread.Status) {
				if cancelledThread, cancelErr := cancelCodeReviewThread(ctx, stores, logger, job, threadID); cancelErr == nil {
					state.CostCents = cancelledThread.CostCents
					completedAt = codeReviewThreadCompletionTime(cancelledThread)
				} else {
					logger.Warn().Err(cancelErr).Str("thread_id", threadID.String()).Msg("failed to cancel timed-out code review orchestrator thread")
				}
			}
			state.CompletedAt = completedAt.Format(time.RFC3339)
			if _, err := stores.CodeReviews.UpdateAgentResultOutcome(ctx, job.OrgID, result.ID, models.CodeReviewAgentResultStatusTimedOut, &raw, marshalCodeReviewOrchestratorStructuredResult(state)); err != nil {
				return fmt.Errorf("mark orchestrator timed out: %w", err)
			}
			continue
		}
		if codeReviewThreadStillRunning(thread.Status) {
			continue
		}
		if codeReviewThreadReadOnlyViolation(thread) {
			state.ReadOnly = true
			state.ReadOnlyViolation = true
			if reverted := revertCodeReviewReadOnlyThread(ctx, stores, services, logger, job, thread); reverted {
				state.Reverted = true
			}
			logger.Warn().
				Str("session_id", job.SessionID.String()).
				Str("thread_id", thread.ID.String()).
				Str("orchestrator", result.AgentProvider).
				Bool("reverted", state.Reverted).
				Msg("code review orchestrator thread produced workspace changes; ignoring for review validity")
		}
		raw, ok, err := latestAssistantMessageForThread(ctx, stores, job.OrgID, threadID)
		if err != nil {
			return err
		}
		if !ok {
			if thread.Status == models.ThreadStatusFailed || thread.Status == models.ThreadStatusCancelled {
				raw = strings.TrimSpace(stringPtrValue(thread.FailureExplanation))
				if raw == "" {
					raw = "orchestrator thread did not complete successfully"
				}
				state.Error = raw
				state.CompletedAt = codeReviewThreadCompletionTime(thread).Format(time.RFC3339)
				rawOutput, rawRecordKey, err := codeReviewRawOutputForStorage(ctx, stores, job, result.ID, models.CodeReviewAgentRoleOrchestrator, result.AgentProvider, raw)
				if err != nil {
					return err
				}
				state.RawRecordKey = rawRecordKey
				if _, err := stores.CodeReviews.UpdateAgentResultOutcome(ctx, job.OrgID, result.ID, models.CodeReviewAgentResultStatusFailed, rawOutput, marshalCodeReviewOrchestratorStructuredResult(state)); err != nil {
					return fmt.Errorf("mark orchestrator failed: %w", err)
				}
			}
			continue
		}
		combinedRaw := codeReviewOrchestratorCombinedOutput(result.RawOutput, raw, state.SynthesisRepairCount)
		synthesis, synthesisErr := parseCodeReviewOrchestratorSynthesis(raw)
		if synthesisErr == nil {
			_, synthesisErr = codeReviewDescriptionEvaluationFromSynthesis(policy.Config(), changedFiles, synthesis, visualEvidence)
		}
		if synthesisErr != nil {
			threads := threadsvc.NewService(stores.SessionThreads, stores.Sessions, stores.SessionMessages, stores.SessionLogs, stores.Jobs, logger)
			repairHandled, repairStarted, repairErr := requestCodeReviewOrchestratorSynthesisRepair(
				ctx,
				stores,
				threads,
				logger,
				job,
				policy.Config(),
				changedFiles,
				result,
				state,
				thread.CurrentTurn,
				combinedRaw,
				synthesisErr,
				visualEvidence,
			)
			if repairErr != nil {
				return repairErr
			}
			if repairStarted {
				return codeReviewWaitingForOrchestrator(policy.Config())
			}
			if repairHandled {
				continue
			}

			state.Synthesis = codeReviewOrchestratorSynthesis{}
			state.SynthesisValidated = false
			state.Error = "invalid orchestrator synthesis: " + synthesisErr.Error()
			state.CompletedAt = codeReviewThreadCompletionTime(thread).Format(time.RFC3339)
			rawOutput, rawRecordKey, err := codeReviewRawOutputForStorage(ctx, stores, job, result.ID, models.CodeReviewAgentRoleOrchestrator, result.AgentProvider, combinedRaw)
			if err != nil {
				return err
			}
			if rawRecordKey != "" {
				state.RawRecordKey = rawRecordKey
			}
			if _, err := stores.CodeReviews.UpdateAgentResultOutcome(ctx, job.OrgID, result.ID, models.CodeReviewAgentResultStatusFailed, rawOutput, marshalCodeReviewOrchestratorStructuredResult(state)); err != nil {
				return fmt.Errorf("mark malformed orchestrator synthesis failed: %w", err)
			}
			continue
		}
		findingCount, err := persistCodeReviewOrchestratorFindings(ctx, stores, job, result.ID, &synthesis, combinedRaw, changedPaths)
		if err != nil {
			return err
		}
		state.Synthesis = synthesis
		state.SynthesisValidated = true
		if findingCount > state.FindingCount {
			state.FindingCount = findingCount
		}
		state.CompletedAt = codeReviewThreadCompletionTime(thread).Format(time.RFC3339)
		state.Error = ""
		rawOutput, rawRecordKey, err := codeReviewRawOutputForStorage(ctx, stores, job, result.ID, models.CodeReviewAgentRoleOrchestrator, result.AgentProvider, combinedRaw)
		if err != nil {
			return err
		}
		if rawRecordKey != "" {
			state.RawRecordKey = rawRecordKey
		}
		if _, err := stores.CodeReviews.UpdateAgentResultOutcome(ctx, job.OrgID, result.ID, models.CodeReviewAgentResultStatusCompleted, rawOutput, marshalCodeReviewOrchestratorStructuredResult(state)); err != nil {
			return fmt.Errorf("mark orchestrator completed: %w", err)
		}
		for _, satisfaction := range codeReviewVisualEvidenceSatisfactions(synthesis, visualEvidence) {
			metrics.RecordCodeReviewVisualEvidenceSatisfaction(ctx, string(satisfaction.Basis), satisfaction.Surface)
		}
	}
	return nil
}

type codeReviewVisualEvidenceSatisfaction struct {
	Basis   models.CodeReviewDescriptionEvidenceBasis
	Surface string
}

func codeReviewVisualEvidenceSatisfactions(synthesis codeReviewOrchestratorSynthesis, visualEvidence models.CodeReviewVisualEvidenceSnapshot) []codeReviewVisualEvidenceSatisfaction {
	byID := make(map[string]models.CodeReviewVisualEvidence, len(visualEvidence.Evidence))
	for _, evidence := range visualEvidence.Evidence {
		byID[evidence.EvidenceID] = evidence
	}
	satisfactions := make([]codeReviewVisualEvidenceSatisfaction, 0)
	for _, assessment := range synthesis.DescriptionAssessments {
		if assessment.Status != codeReviewDescriptionAssessmentSatisfied {
			continue
		}
		switch assessment.EvidenceBasis {
		case models.CodeReviewDescriptionEvidenceBasisImage:
			for _, evidenceID := range assessment.EvidenceIDs {
				if evidence, exists := byID[strings.TrimSpace(evidenceID)]; exists {
					satisfactions = append(satisfactions, codeReviewVisualEvidenceSatisfaction{Basis: assessment.EvidenceBasis, Surface: string(evidence.Source.Surface)})
				}
			}
		case models.CodeReviewDescriptionEvidenceBasisPreviewLink,
			models.CodeReviewDescriptionEvidenceBasisRepository:
			satisfactions = append(satisfactions, codeReviewVisualEvidenceSatisfaction{Basis: assessment.EvidenceBasis, Surface: "none"})
		}
	}
	return satisfactions
}

func codeReviewOrchestratorTerminal(results []models.CodeReviewAgentResult) bool {
	for _, result := range results {
		if result.Role == models.CodeReviewAgentRoleOrchestrator && codeReviewReviewerResultTerminal(result.Status) {
			return true
		}
	}
	return false
}

func codeReviewWaitingForOrchestrator(policy models.CodeReviewPolicyConfig) error {
	delay := 10 * time.Second
	if policy.AgentRoster.TimeoutSeconds <= 120 {
		delay = 5 * time.Second
	}
	return &RetryableError{
		Err:                    errors.New("waiting for code review orchestrator agent"),
		RetryAfter:             &delay,
		BypassMaxRetryDuration: true,
	}
}

type codeReviewSubmission struct {
	GitHubReviewID  *int64
	GitHubReviewURL *string
	FinalReviewBody string
}

type codeReviewRequestedReviewerRemover interface {
	RemoveRequestedReviewers(ctx context.Context, req codereviewsvc.RequestedReviewersRequest) error
}

type codeReviewAuthorTeamMembershipChecker interface {
	IsActiveTeamMember(ctx context.Context, installationID int64, orgLogin, teamSlug, username string) (bool, error)
}

type liveCodeReviewOutcomeInput struct {
	Policy                models.CodeReviewPolicyConfig
	Job                   runCodeReviewPayload
	SessionURL            string
	PolicySettingsURL     string
	PullRequest           models.PullRequest
	Health                *models.PullRequestHealthResponse
	AgentResults          []models.CodeReviewAgentResult
	Findings              []models.CodeReviewFinding
	ChangedFiles          []codereviewsvc.PullRequestFile
	ChangedFilesAvailable bool
	OrchestratorSynthesis codeReviewOrchestratorSynthesis
	VisualEvidence        models.CodeReviewVisualEvidenceSnapshot
	AssessedAt            time.Time
}

func codeReviewStableDeterministicRisk(policy models.CodeReviewPolicyConfig, job runCodeReviewPayload, pr models.PullRequest, changedFiles []codereviewsvc.PullRequestFile, changedFilesAvailable bool) models.CodeReviewRiskEvaluation {
	if !changedFilesAvailable {
		return models.CodeReviewRiskEvaluation{Acceptable: true}
	}
	requiredChecksPassing := make(map[string]bool, len(policy.RiskPolicy.RequiredChecks))
	for _, check := range policy.RiskPolicy.RequiredChecks {
		requiredChecksPassing[check] = true
	}
	evaluated := models.EvaluateCodeReviewRisk(policy, models.CodeReviewRiskInput{
		FilesChanged:          len(changedFiles),
		LinesChanged:          codeReviewLinesChanged(changedFiles),
		ChangedPaths:          codeReviewChangedPaths(changedFiles),
		ChecksPassing:         true,
		RequiredChecksPassing: requiredChecksPassing,
		DescriptionPassed:     true,
		UpToDate:              true,
		Author:                codeReviewAuthor(job, pr),
		AuthorClass:           codeReviewAuthorClass(pr),
		AuthorTeams:           job.PullRequestAuthorTeams,
		FromFork:              job.FromFork,
	})
	stable := models.CodeReviewRiskEvaluation{Acceptable: true}
	for _, reason := range evaluated.ReasonDetails {
		// Explicit usernames are stable for the captured policy, but GitHub team
		// membership is live provider state and is rechecked before approval.
		if reason.Code == models.CodeReviewRiskReasonAuthorIneligible && len(policy.RiskPolicy.EligibleAuthorTeams) > 0 {
			continue
		}
		if models.IsCodeReviewStableDeterministicRiskReason(reason.Code) {
			stable.AddReason(reason)
		}
	}
	return stable
}

func codeReviewCanStopBeforeAgentFanout(agentResults []models.CodeReviewAgentResult) bool {
	// Durable results are created before a reviewer message is dispatched. Their
	// presence therefore means a retry must preserve that work, including when a
	// rolling deployment resumes a session started by an older worker.
	return len(agentResults) == 0
}

func completeCodeReviewAfterStableDeterministicFailure(
	ctx context.Context,
	stores *Stores,
	services *Services,
	logger zerolog.Logger,
	job runCodeReviewPayload,
	metadata models.CodeReviewSessionMetadata,
	policy models.CodeReviewPolicyConfig,
	pr models.PullRequest,
	changedFiles []codereviewsvc.PullRequestFile,
	risk models.CodeReviewRiskEvaluation,
) error {
	if cancelled, err := stopCodeReviewIfParentSessionCancelled(ctx, stores, services, logger, job, pr); cancelled || err != nil {
		return err
	}
	decision := models.EvaluateCodeReviewDecision(policy, risk)
	body := models.BuildCodeReviewFinalReviewBody(models.CodeReviewFinalReviewInput{
		Decision:             decision.Decision,
		Acceptable:           decision.Acceptable,
		RiskReasons:          decision.RiskReasonDetails,
		SessionURL:           codeReviewSessionURL(services.FrontendURL, job.SessionID),
		PolicySettingsURL:    codeReviewPolicySettingsURL(services.FrontendURL),
		ChangeStatsAvailable: true,
		FilesChanged:         len(changedFiles),
		LinesChanged:         codeReviewLinesChanged(changedFiles),
		HeadSHA:              job.HeadSHA,
		AssessedAt:           time.Now().UTC(),
	})
	if _, err := stores.CodeReviews.SetOperationalPhase(ctx, job.OrgID, job.SessionID, models.CodeReviewPhasePublishing); err != nil {
		return fmt.Errorf("set deterministic early-stop publishing phase: %w", err)
	}
	submission, submitted, err := submitCodeReviewToGitHub(ctx, stores, services, job, metadata, decision.Decision, body)
	if err != nil {
		return err
	}
	finalReviewBody := body
	if strings.TrimSpace(submission.FinalReviewBody) != "" {
		finalReviewBody = submission.FinalReviewBody
	}
	additions, deletions := codeReviewLineChanges(changedFiles)
	removeCodeReviewRequestedReviewer(ctx, stores, services, logger, job, pr)
	if _, err := stores.CodeReviews.CompleteReview(ctx, job.OrgID, db.CompleteCodeReviewParams{
		SessionID:         job.SessionID,
		Decision:          decision.Decision,
		Acceptable:        decision.Acceptable,
		GitHubReviewID:    submission.GitHubReviewID,
		GitHubReviewURL:   submission.GitHubReviewURL,
		FinalReviewBody:   finalReviewBody,
		Additions:         &additions,
		Deletions:         &deletions,
		RiskReasonDetails: decision.RiskReasonDetails,
	}); err != nil {
		return fmt.Errorf("complete deterministic early-stop code review: %w", err)
	}
	logger.Info().
		Str("org_id", job.OrgID.String()).
		Str("session_id", job.SessionID.String()).
		Bool("github_submitted", submitted).
		Int("reviewer_runs_avoided", len(policy.AgentRoster.Reviewers)+1).
		Msg("completed code review after stable deterministic failure")
	reconcileCodeReviewSessionSuccess(ctx, stores, logger, job)
	enqueueCodeReviewStatusCommentSync(ctx, stores, services, logger, job, "terminal")
	return nil
}

func evaluateLiveCodeReviewOutcome(input liveCodeReviewOutcomeInput) (models.CodeReviewDecisionEvaluation, string) {
	policy := models.ResolveCodeReviewPolicyConfig(&input.Policy)
	reviewerQuorum, _ := codeReviewReviewerEvidence(input.AgentResults)
	requiredReviewerQuorum := codeReviewRequiredReviewerQuorum(policy, input.AgentResults)
	blockingFindings := codeReviewBlockingFindings(input.Findings)
	orchestratorPresent, orchestratorEvidenceUsable := codeReviewOrchestratorEvidence(input.AgentResults)
	orchestratorSynthesisUsable := codeReviewOrchestratorSynthesisUsable(input.OrchestratorSynthesis)
	descriptionEvaluation := codeReviewDescriptionEvaluation{Passed: true}
	descriptionEvaluationValid := false
	if orchestratorSynthesisUsable {
		var descriptionErr error
		descriptionEvaluation, descriptionErr = codeReviewDescriptionEvaluationFromSynthesis(policy, input.ChangedFiles, input.OrchestratorSynthesis, input.VisualEvidence)
		descriptionEvaluationValid = descriptionErr == nil
	}
	risk := models.EvaluateCodeReviewRisk(policy, models.CodeReviewRiskInput{
		FilesChanged:          len(input.ChangedFiles),
		LinesChanged:          codeReviewLinesChanged(input.ChangedFiles),
		ChangedPaths:          codeReviewChangedPaths(input.ChangedFiles),
		ChecksPassing:         codeReviewChecksPassing(policy, input.Health),
		RequiredChecksPassing: codeReviewRequiredChecksPassing(policy, input.Health),
		DescriptionPassed:     !orchestratorSynthesisUsable || (descriptionEvaluationValid && descriptionEvaluation.Passed),
		UpToDate:              codeReviewUpToDate(input.Health),
		Author:                codeReviewAuthor(input.Job, input.PullRequest),
		AuthorClass:           codeReviewAuthorClass(input.PullRequest),
		AuthorTeams:           input.Job.PullRequestAuthorTeams,
		FromFork:              input.Job.FromFork,
		ContextFetchFailed:    input.Health == nil || !input.ChangedFilesAvailable,
		HeadSHAChanged:        codeReviewHeadChanged(input.Job.HeadSHA, input.PullRequest, input.Health),
		BlockingFindings:      blockingFindings,
		ReviewerDisagreement:  input.OrchestratorSynthesis.ReviewerDisagreement,
		ScopeMismatch:         input.OrchestratorSynthesis.ScopeMismatch,
		UnresolvedUncertainty: input.OrchestratorSynthesis.UnresolvedUncertainty,
		PromptInjectionFound:  codeReviewPromptInjectionLikely(stringPtrValue(input.PullRequest.Body), input.Job.RequestContext) || codeReviewVisualEvidencePromptInjectionLikely(input.VisualEvidence) || input.OrchestratorSynthesis.PromptInjectionDetected,
	})
	if reviewerQuorum < requiredReviewerQuorum {
		risk.AddReason(models.CodeReviewRiskReason{Code: models.CodeReviewRiskReasonReviewerQuorum, Actual: reviewerQuorum, Limit: requiredReviewerQuorum})
	}
	if policy.ApprovalMode == models.CodeReviewApprovalModeApproveAcceptable && orchestratorSynthesisUsable {
		for _, reason := range input.OrchestratorSynthesis.HumanReviewReasons {
			risk.AddReason(models.CodeReviewRiskReason{
				Code:    reason.Code.RiskReasonCode(),
				Subject: reason.Summary,
			})
		}
	}
	if policy.ApprovalMode == models.CodeReviewApprovalModeApproveAcceptable && (!orchestratorPresent || !orchestratorEvidenceUsable || !orchestratorSynthesisUsable) {
		risk.AddReason(models.CodeReviewRiskReason{Code: models.CodeReviewRiskReasonOrchestratorSynthesisInvalid})
	} else if orchestratorSynthesisUsable && !descriptionEvaluationValid {
		risk.AddReason(models.CodeReviewRiskReason{Code: models.CodeReviewRiskReasonOrchestratorSynthesisInvalid})
	} else if orchestratorPresent && !orchestratorEvidenceUsable {
		risk.AddReason(models.CodeReviewRiskReason{Code: models.CodeReviewRiskReasonOrchestratorSynthesisInvalid})
	}
	var descriptionPassed *bool
	if descriptionEvaluationValid {
		descriptionPassed = &descriptionEvaluation.Passed
	}
	decision := models.EvaluateCodeReviewDecision(policy, risk)
	generatedSummary := ""
	// Never let generated prose bypass the structured output contract. A model
	// may restate advisory finding details in its summary, so only publish that
	// prose for a clean approval with no findings at all.
	if decision.Decision == models.CodeReviewDecisionApproved &&
		input.OrchestratorSynthesis.ApprovalRecommended &&
		len(input.Findings) == 0 {
		generatedSummary = codeReviewOrchestratorReviewSummary(input.OrchestratorSynthesis)
	}
	body := models.BuildCodeReviewFinalReviewBody(models.CodeReviewFinalReviewInput{
		Decision:                  decision.Decision,
		Acceptable:                decision.Acceptable,
		RiskReasons:               decision.RiskReasonDetails,
		GeneratedSummary:          generatedSummary,
		ChangeSummary:             input.OrchestratorSynthesis.Summary,
		OperationalSummary:        codeReviewOrchestratorOperationalSummary(input.AgentResults, decision.RiskReasonDetails),
		SessionURL:                input.SessionURL,
		PolicySettingsURL:         input.PolicySettingsURL,
		DescriptionPassed:         descriptionPassed,
		DescriptionIssues:         codeReviewFailedDescriptionRequirements(descriptionEvaluation.RequirementSummaries),
		AgentSummaries:            codeReviewAgentSummaries(input.AgentResults, input.Findings),
		Findings:                  input.Findings,
		RecommendedHumanReviewers: codeReviewRecommendedHumanReviewers(decision.RiskReasonDetails),
		ChangeStatsAvailable:      input.ChangedFilesAvailable,
		FilesChanged:              len(input.ChangedFiles),
		LinesChanged:              codeReviewLinesChanged(input.ChangedFiles),
		ChecksRequired:            policy.RiskPolicy.RequirePassingChecks || len(policy.RiskPolicy.RequiredChecks) > 0,
		ReviewerQuorum:            reviewerQuorum,
		RequiredReviewerQuorum:    requiredReviewerQuorum,
		HeadSHA:                   input.Job.HeadSHA,
		AssessedAt:                input.AssessedAt,
	})
	return decision, body
}

type codeReviewFileLister interface {
	ListPullRequestFiles(ctx context.Context, req codereviewsvc.PullRequestFilesRequest) ([]codereviewsvc.PullRequestFile, error)
}

func removeCodeReviewRequestedReviewer(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, job runCodeReviewPayload, pr models.PullRequest) {
	reviewer := strings.TrimSpace(job.RequestedReviewerLogin)
	team := strings.TrimSpace(job.RequestedTeamSlug)
	if reviewer == "" && team == "" {
		return
	}
	if services == nil || services.CodeReviews == nil {
		return
	}
	remover, ok := services.CodeReviews.(codeReviewRequestedReviewerRemover)
	if !ok {
		return
	}
	if stores == nil || stores.Repositories == nil {
		logger.Warn().Str("session_id", job.SessionID.String()).Msg("skipping requested reviewer cleanup: repository store unavailable")
		return
	}
	repo, err := stores.Repositories.GetByID(ctx, job.OrgID, job.RepositoryID)
	if err != nil {
		logger.Warn().Err(err).Str("session_id", job.SessionID.String()).Msg("failed to load repository for requested reviewer cleanup")
		return
	}
	if repo.InstallationID == 0 {
		logger.Warn().Str("repository_id", repo.ID.String()).Str("session_id", job.SessionID.String()).Msg("skipping requested reviewer cleanup: repository has no GitHub installation id")
		return
	}
	repository := strings.TrimSpace(pr.GitHubRepo)
	if repository == "" {
		repository = strings.TrimSpace(repo.FullName)
	}
	req := codereviewsvc.RequestedReviewersRequest{
		InstallationID: repo.InstallationID,
		Repository:     repository,
		PullNumber:     pr.GitHubPRNumber,
	}
	if reviewer != "" {
		req.Reviewers = []string{reviewer}
	}
	if team != "" {
		req.TeamReviewers = []string{team}
	}
	if err := remover.RemoveRequestedReviewers(ctx, req); err != nil {
		logger.Warn().Err(err).Str("session_id", job.SessionID.String()).Msg("failed to remove stale code review requested reviewer")
	}
}

func codeReviewSessionURL(frontendURL string, sessionID uuid.UUID) string {
	base := strings.TrimRight(strings.TrimSpace(frontendURL), "/")
	if base == "" || sessionID == uuid.Nil {
		return ""
	}
	return base + "/sessions/" + sessionID.String()
}

func codeReviewPolicySettingsURL(frontendURL string) string {
	base := strings.TrimRight(strings.TrimSpace(frontendURL), "/")
	if base == "" {
		return ""
	}
	return base + "/code-reviews?tab=policy"
}

func loadCodeReviewChangedFiles(ctx context.Context, stores *Stores, services *Services, job runCodeReviewPayload, pr models.PullRequest) ([]codereviewsvc.PullRequestFile, bool, error) {
	if services == nil || services.CodeReviews == nil {
		return nil, false, nil
	}
	lister, ok := services.CodeReviews.(codeReviewFileLister)
	if !ok {
		return nil, false, nil
	}
	if stores == nil || stores.Repositories == nil {
		return nil, false, fmt.Errorf("repository store is required")
	}
	repo, err := stores.Repositories.GetByID(ctx, job.OrgID, job.RepositoryID)
	if err != nil {
		return nil, false, fmt.Errorf("load code review repository: %w", err)
	}
	if repo.InstallationID == 0 {
		return nil, false, fmt.Errorf("repository %s has no GitHub installation id", repo.ID)
	}
	repository := strings.TrimSpace(pr.GitHubRepo)
	if repository == "" {
		repository = strings.TrimSpace(repo.FullName)
	}
	files, err := lister.ListPullRequestFiles(ctx, codereviewsvc.PullRequestFilesRequest{
		InstallationID: repo.InstallationID,
		Repository:     repository,
		PullNumber:     pr.GitHubPRNumber,
	})
	if err != nil {
		return nil, false, classifyGitHubJobError(fmt.Errorf("list GitHub pull request files: %w", err), job.SessionID.String())
	}
	return files, true, nil
}

func captureCodeReviewVisualEvidence(ctx context.Context, services *Services, job runCodeReviewPayload, pr models.PullRequest) (models.CodeReviewVisualEvidenceSnapshot, error) {
	if services == nil || services.CodeReviewVisualEvidence == nil {
		return models.CodeReviewVisualEvidenceSnapshot{}, errors.New("code review visual evidence provider is not configured")
	}
	snapshot, err := services.CodeReviewVisualEvidence.Capture(ctx, codereviewsvc.CaptureVisualEvidenceInput{
		OrgID:             job.OrgID,
		SessionID:         job.SessionID,
		RepositoryID:      job.RepositoryID,
		PullRequestNumber: pr.GitHubPRNumber,
		HeadSHA:           job.HeadSHA,
	})
	if err != nil {
		return models.CodeReviewVisualEvidenceSnapshot{}, fmt.Errorf("capture code review visual evidence: %w", err)
	}
	if snapshot.Version != 1 || !snapshot.Complete || snapshot.RepositoryID != job.RepositoryID ||
		snapshot.PullRequestNumber != pr.GitHubPRNumber || !strings.EqualFold(strings.TrimSpace(snapshot.HeadSHA), strings.TrimSpace(job.HeadSHA)) {
		return models.CodeReviewVisualEvidenceSnapshot{}, errors.New("captured code review visual evidence is incomplete or does not match the assessment")
	}
	return snapshot, nil
}

func loadStoredCodeReviewHealth(ctx context.Context, stores *Stores, job runCodeReviewPayload, pr models.PullRequest) (*models.PullRequestHealthResponse, error) {
	if stores == nil || stores.PullRequests == nil {
		return nil, nil
	}
	resp := &models.PullRequestHealthResponse{
		PullRequestID:     pr.ID,
		PullRequestNumber: pr.GitHubPRNumber,
		Repository:        pr.GitHubRepo,
		URL:               pr.GitHubPRURL,
		Status:            pr.Status,
		MergeState:        pr.MergeState,
		HasConflicts:      pr.HasConflicts,
		FailingTestCount:  pr.FailingTestCount,
		HealthVersion:     pr.HealthVersion,
		CanMerge:          pr.Status == models.PullRequestStatusOpen && pr.MergeState == models.PullRequestMergeStateClean && !pr.HasConflicts && pr.FailingTestCount == 0,
	}
	if pr.HeadSHA != nil {
		resp.HeadSHA = *pr.HeadSHA
	}
	if pr.BaseSHA != nil {
		resp.BaseSHA = *pr.BaseSHA
	}
	current, err := stores.PullRequests.GetHealthCurrent(ctx, job.OrgID, job.PullRequestID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return resp, nil
	}
	var summary models.PullRequestHealthSummary
	if err := json.Unmarshal(current.SummaryJSON, &summary); err != nil {
		return nil, fmt.Errorf("decode code review health summary: %w", err)
	}
	resp.HeadSHA = current.HeadSHA
	resp.BaseSHA = current.BaseSHA
	resp.MergeState = summary.MergeState
	resp.HasConflicts = summary.HasConflicts
	resp.FailingTestCount = summary.FailingTestCount
	resp.Checks = summary.Checks
	resp.ChecksConfirmed = summary.ChecksConfirmed || len(summary.Checks) > 0
	resp.CanMerge = pr.Status == models.PullRequestStatusOpen &&
		summary.MergeState == models.PullRequestMergeStateClean &&
		!summary.HasConflicts &&
		codeReviewAllChecksPassing(summary.ChecksConfirmed, summary.Checks)
	return resp, nil
}

func codeReviewReviewerEvidence(results []models.CodeReviewAgentResult) (quorum int, failures int) {
	for _, result := range results {
		if result.Role != models.CodeReviewAgentRoleReviewer {
			continue
		}
		switch result.Status {
		case models.CodeReviewAgentResultStatusCompleted:
			if codeReviewReviewerResultHasUsableOutput(result) {
				quorum++
			}
		case models.CodeReviewAgentResultStatusFailed, models.CodeReviewAgentResultStatusTimedOut:
			failures++
		}
	}
	return quorum, failures
}

// codeReviewRequiredReviewerQuorum returns the reviewer quorum this run is
// held to: the configured requirement clamped to the number of roster
// reviewers that could actually run. Reviewers skipped because their
// credential is unavailable can never produce a report, so they shrink the
// requirement instead of making approval impossible; reviewers that ran and
// failed or timed out still count against the configured quorum. The
// requirement never drops below one reviewer.
func codeReviewRequiredReviewerQuorum(cfg models.CodeReviewPolicyConfig, results []models.CodeReviewAgentResult) int {
	required := cfg.AgentRoster.RequireReviewerQuorum
	available := len(cfg.AgentRoster.Reviewers)
	for _, result := range results {
		if result.Role != models.CodeReviewAgentRoleReviewer {
			continue
		}
		if state, ok := parseCodeReviewReviewerStructuredResult(result.StructuredResult); ok && state.Unavailable {
			available--
		}
	}
	if available < 1 {
		available = 1
	}
	if required > available {
		return available
	}
	return required
}

func codeReviewReviewerResultHasUsableOutput(result models.CodeReviewAgentResult) bool {
	if result.Status != models.CodeReviewAgentResultStatusCompleted {
		return false
	}
	state, ok := parseCodeReviewReviewerStructuredResult(result.StructuredResult)
	if !ok {
		return true
	}
	return !codeReviewReviewerStateHasNoUsableOutput(state)
}

func codeReviewReviewerStateHasNoUsableOutput(state codeReviewReviewerStructuredResult) bool {
	return state.ReadOnlyViolation && strings.TrimSpace(state.Error) != ""
}

func codeReviewBlockingFindings(findings []models.CodeReviewFinding) int {
	count := 0
	for _, finding := range findings {
		if finding.Severity.IsBlocking() {
			count++
		}
	}
	return count
}

func codeReviewLinesChanged(files []codereviewsvc.PullRequestFile) int {
	additions, deletions := codeReviewLineChanges(files)
	return additions + deletions
}

func codeReviewLineChanges(files []codereviewsvc.PullRequestFile) (int, int) {
	additions := 0
	deletions := 0
	for _, file := range files {
		additions += file.Additions
		deletions += file.Deletions
	}
	return additions, deletions
}

func codeReviewChangedPaths(files []codereviewsvc.PullRequestFile) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		if strings.TrimSpace(file.Filename) == "" {
			continue
		}
		paths = append(paths, file.Filename)
	}
	return paths
}

func codeReviewDescriptionRequirementApplies(requirement models.CodeReviewDescriptionRequirement, changedFiles []codereviewsvc.PullRequestFile) bool {
	if !requirement.AppliesWhen.Empty() {
		return codeReviewDescriptionApplicabilityApplies(requirement.AppliesWhen, changedFiles)
	}
	switch strings.ToLower(strings.TrimSpace(requirement.Applicability)) {
	case "", "all", "always":
		return true
	case "nontrivial":
		return len(changedFiles) > 1 || codeReviewLinesChanged(changedFiles) > 30
	default:
		return true
	}
}

func codeReviewApplicableDescriptionRequirements(policy models.CodeReviewPolicyConfig, changedFiles []codereviewsvc.PullRequestFile) []models.CodeReviewDescriptionRequirement {
	policy = models.ResolveCodeReviewPolicyConfig(&policy)
	requirements := make([]models.CodeReviewDescriptionRequirement, 0, len(policy.DescriptionPolicy.Requirements))
	for _, requirement := range policy.DescriptionPolicy.Requirements {
		if !requirement.Required || strings.TrimSpace(requirement.Key) == "" {
			continue
		}
		if !codeReviewDescriptionRequirementApplies(requirement, changedFiles) {
			continue
		}
		requirements = append(requirements, requirement)
	}
	return requirements
}

func codeReviewDescriptionRequirementsForPrompt(policy models.CodeReviewPolicyConfig, changedFiles []codereviewsvc.PullRequestFile) []prompts.CodeReviewDescriptionRequirementPromptData {
	requirements := codeReviewApplicableDescriptionRequirements(policy, changedFiles)
	rendered := make([]prompts.CodeReviewDescriptionRequirementPromptData, 0, len(requirements))
	for _, requirement := range requirements {
		applicability := strings.TrimSpace(requirement.Applicability)
		if applicability == "" {
			applicability = string(requirement.AppliesWhen.Kind)
		}
		rendered = append(rendered, prompts.CodeReviewDescriptionRequirementPromptData{
			Key:           strings.TrimSpace(requirement.Key),
			Title:         strings.TrimSpace(requirement.Title),
			Prompt:        strings.TrimSpace(requirement.Prompt),
			Applicability: applicability,
			EvidenceKind:  string(requirement.EvidenceKind),
		})
	}
	return rendered
}

func codeReviewDescriptionEvaluationFromSynthesis(policy models.CodeReviewPolicyConfig, changedFiles []codereviewsvc.PullRequestFile, synthesis codeReviewOrchestratorSynthesis, visualEvidence models.CodeReviewVisualEvidenceSnapshot) (codeReviewDescriptionEvaluation, error) {
	requirements := codeReviewApplicableDescriptionRequirements(policy, changedFiles)
	expected := make(map[string]models.CodeReviewDescriptionRequirement, len(requirements))
	for _, requirement := range requirements {
		expected[strings.TrimSpace(requirement.Key)] = requirement
	}

	assessments := make(map[string]codeReviewDescriptionAssessment, len(synthesis.DescriptionAssessments))
	for _, assessment := range synthesis.DescriptionAssessments {
		key := strings.TrimSpace(assessment.Key)
		if _, ok := expected[key]; !ok {
			return codeReviewDescriptionEvaluation{}, fmt.Errorf("orchestrator assessed unknown description requirement %q", key)
		}
		if _, duplicate := assessments[key]; duplicate {
			return codeReviewDescriptionEvaluation{}, fmt.Errorf("orchestrator assessed description requirement %q more than once", key)
		}
		if err := validateCodeReviewDescriptionAssessmentEvidence(assessment, visualEvidence); err != nil {
			return codeReviewDescriptionEvaluation{}, fmt.Errorf("orchestrator description requirement %q: %w", key, err)
		}
		assessments[key] = assessment
	}

	evaluation := codeReviewDescriptionEvaluation{Passed: true}
	for _, requirement := range requirements {
		key := strings.TrimSpace(requirement.Key)
		assessment, ok := assessments[key]
		if !ok {
			return codeReviewDescriptionEvaluation{}, fmt.Errorf("orchestrator did not assess description requirement %q", key)
		}
		title := strings.TrimSpace(requirement.Title)
		if title == "" {
			title = key
		}
		reason := strings.TrimSpace(assessment.Reason)
		switch assessment.Status {
		case codeReviewDescriptionAssessmentSatisfied:
			if codeReviewDescriptionRequirementNeedsVisualBasis(requirement) {
				switch assessment.EvidenceBasis {
				case models.CodeReviewDescriptionEvidenceBasisImage,
					models.CodeReviewDescriptionEvidenceBasisPreviewLink,
					models.CodeReviewDescriptionEvidenceBasisRepository:
				default:
					return codeReviewDescriptionEvaluation{}, fmt.Errorf("orchestrator description requirement %q must use image, preview-link, or repository visual evidence", key)
				}
			}
			evaluation.RequirementSummaries = append(evaluation.RequirementSummaries, title+": passed ("+reason+")")
		case codeReviewDescriptionAssessmentNotApplicable:
			evaluation.RequirementSummaries = append(evaluation.RequirementSummaries, title+": passed (not applicable: "+reason+")")
		case codeReviewDescriptionAssessmentMissing:
			evaluation.Passed = false
			evaluation.RequirementSummaries = append(evaluation.RequirementSummaries, title+": failed ("+reason+")")
		default:
			return codeReviewDescriptionEvaluation{}, fmt.Errorf("orchestrator description requirement %q has invalid status %q", key, assessment.Status)
		}
	}
	return evaluation, nil
}

func codeReviewDescriptionRequirementNeedsVisualBasis(requirement models.CodeReviewDescriptionRequirement) bool {
	return requirement.EvidenceKind == models.CodeReviewDescriptionEvidenceKindVisual
}

func validateCodeReviewDescriptionAssessmentEvidence(assessment codeReviewDescriptionAssessment, visualEvidence models.CodeReviewVisualEvidenceSnapshot) error {
	if err := assessment.EvidenceBasis.Validate(); err != nil {
		return err
	}
	seenIDs := make(map[string]struct{}, len(assessment.EvidenceIDs))
	for _, rawID := range assessment.EvidenceIDs {
		evidenceID := strings.TrimSpace(rawID)
		if evidenceID == "" {
			return errors.New("evidence IDs must not be empty")
		}
		if _, duplicate := seenIDs[evidenceID]; duplicate {
			return fmt.Errorf("evidence ID %q is cited more than once", evidenceID)
		}
		seenIDs[evidenceID] = struct{}{}
	}
	switch assessment.Status {
	case codeReviewDescriptionAssessmentSatisfied:
		if assessment.EvidenceBasis == models.CodeReviewDescriptionEvidenceBasisImage {
			if len(seenIDs) == 0 {
				return errors.New("image-backed satisfaction must cite at least one evidence ID")
			}
			if !visualEvidence.Complete {
				return errors.New("image-backed satisfaction requires a complete visual evidence snapshot")
			}
			available := make(map[string]models.CodeReviewVisualEvidence, len(visualEvidence.Evidence))
			for _, evidence := range visualEvidence.Evidence {
				available[evidence.EvidenceID] = evidence
			}
			for evidenceID := range seenIDs {
				evidence, exists := available[evidenceID]
				if !exists {
					return fmt.Errorf("unknown visual evidence ID %q", evidenceID)
				}
				if evidence.Status != models.CodeReviewVisualEvidenceFetchStatusAvailable || strings.TrimSpace(evidence.StoredURL) == "" {
					return fmt.Errorf("visual evidence ID %q is not available", evidenceID)
				}
			}
			return nil
		}
		if len(seenIDs) != 0 {
			return errors.New("non-image satisfaction must not cite visual evidence IDs")
		}
		switch assessment.EvidenceBasis {
		case models.CodeReviewDescriptionEvidenceBasisPreviewLink,
			models.CodeReviewDescriptionEvidenceBasisRepository,
			models.CodeReviewDescriptionEvidenceBasisPullRequestDescription,
			models.CodeReviewDescriptionEvidenceBasisDiff:
			return nil
		default:
			return errors.New("satisfied status has an incompatible evidence basis")
		}
	case codeReviewDescriptionAssessmentNotApplicable:
		if assessment.EvidenceBasis != models.CodeReviewDescriptionEvidenceBasisNotApplicable || len(seenIDs) != 0 {
			return errors.New("not-applicable status requires the not_applicable basis and no evidence IDs")
		}
		return nil
	case codeReviewDescriptionAssessmentMissing:
		if assessment.EvidenceBasis != models.CodeReviewDescriptionEvidenceBasisMissing || len(seenIDs) != 0 {
			return errors.New("missing status requires the missing basis and no evidence IDs")
		}
		return nil
	default:
		return fmt.Errorf("invalid status %q", assessment.Status)
	}
}

func codeReviewDescriptionApplicabilityApplies(applicability models.CodeReviewDescriptionApplicability, changedFiles []codereviewsvc.PullRequestFile) bool {
	linesChanged := codeReviewLinesChanged(changedFiles)
	changedPaths := codeReviewChangedPaths(changedFiles)
	if applicability.MinFilesChanged > 0 && len(changedFiles) >= applicability.MinFilesChanged {
		return true
	}
	if applicability.MinLinesChanged > 0 && linesChanged >= applicability.MinLinesChanged {
		return true
	}
	if len(applicability.PathPatterns) > 0 {
		for _, path := range changedPaths {
			if codeReviewPathMatchesAny(path, applicability.PathPatterns) {
				return true
			}
		}
	}
	switch applicability.Kind {
	case "", models.CodeReviewDescriptionApplicabilityAll:
		return true
	case models.CodeReviewDescriptionApplicabilityNontrivial:
		return len(changedFiles) > 1 || linesChanged > 30
	case models.CodeReviewDescriptionApplicabilityPaths:
		return len(applicability.PathPatterns) == 0
	default:
		return true
	}
}

func codeReviewPathMatchesAny(path string, patterns []string) bool {
	path = filepath.ToSlash(strings.ToLower(strings.TrimSpace(path)))
	for _, pattern := range patterns {
		pattern = filepath.ToSlash(strings.ToLower(strings.TrimSpace(pattern)))
		if pattern == "" {
			continue
		}
		if ok, err := filepath.Match(pattern, path); err == nil && ok {
			return true
		}
		if strings.Contains(pattern, "**") {
			regexPattern := regexp.QuoteMeta(pattern)
			regexPattern = strings.ReplaceAll(regexPattern, `\*\*`, `.*`)
			regexPattern = strings.ReplaceAll(regexPattern, `\*`, `[^/]*`)
			if ok, err := regexp.MatchString("^"+regexPattern+"$", path); err == nil && ok {
				return true
			}
		}
		trimmedTree := strings.TrimSuffix(pattern, "/**")
		if trimmedTree != pattern && (path == trimmedTree || strings.HasPrefix(path, trimmedTree+"/")) {
			return true
		}
		if path == pattern || strings.HasPrefix(path, pattern+"/") || strings.HasPrefix(path, pattern) {
			return true
		}
	}
	return false
}

func containsAnyFold(haystack string, needles []string) bool {
	haystack = strings.ToLower(haystack)
	for _, needle := range needles {
		if strings.Contains(haystack, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

func codeReviewChecksPassing(policy models.CodeReviewPolicyConfig, health *models.PullRequestHealthResponse) bool {
	if !policy.RiskPolicy.RequirePassingChecks {
		return true
	}
	if health == nil {
		return false
	}
	return codeReviewAllChecksPassing(health.ChecksConfirmed, codeReviewExternalChecks(health.Checks))
}

// codeReviewExternalChecks drops 143's own non-CI status contexts before the
// checks-passing gate is evaluated. Historical "143 Code Reviewer" statuses
// and current "preview/143" statuses are not CI signals and must not affect
// the reviewer's approval decision.
func codeReviewExternalChecks(checks []models.PullRequestCheckSummary) []models.PullRequestCheckSummary {
	filtered := make([]models.PullRequestCheckSummary, 0, len(checks))
	for _, check := range checks {
		if codeReviewSelfReportedCheck(check.Name) {
			continue
		}
		filtered = append(filtered, check)
	}
	return filtered
}

func codeReviewSelfReportedCheck(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == "" {
		return false
	}
	return strings.Contains(normalized, "143 code reviewer") || strings.HasPrefix(normalized, "preview/143")
}

func codeReviewRequiredChecksPassing(policy models.CodeReviewPolicyConfig, health *models.PullRequestHealthResponse) map[string]bool {
	statuses := make(map[string]bool, len(policy.RiskPolicy.RequiredChecks))
	if health == nil {
		return statuses
	}
	for _, required := range policy.RiskPolicy.RequiredChecks {
		required = strings.TrimSpace(required)
		if required == "" {
			continue
		}
		for _, check := range health.Checks {
			if strings.EqualFold(strings.TrimSpace(check.Name), required) && check.Status == models.PullRequestCheckStatusPassed {
				statuses[required] = true
				break
			}
		}
	}
	return statuses
}

func codeReviewAllChecksPassing(confirmed bool, checks []models.PullRequestCheckSummary) bool {
	if len(checks) == 0 {
		return confirmed
	}
	for _, check := range checks {
		if check.Status != models.PullRequestCheckStatusPassed {
			return false
		}
	}
	return true
}

func codeReviewUpToDate(health *models.PullRequestHealthResponse) bool {
	return health != nil && health.MergeState != models.PullRequestMergeStateBehind
}

func codeReviewHeadChanged(reviewedHead string, pr models.PullRequest, health *models.PullRequestHealthResponse) bool {
	if reviewedHead == "" {
		return true
	}
	if pr.HeadSHA != nil && strings.TrimSpace(*pr.HeadSHA) != "" && *pr.HeadSHA != reviewedHead {
		return true
	}
	if health != nil && strings.TrimSpace(health.HeadSHA) != "" && health.HeadSHA != reviewedHead {
		return true
	}
	return false
}

func codeReviewAuthor(job runCodeReviewPayload, pr models.PullRequest) string {
	if author := strings.TrimSpace(job.PullRequestAuthor); author != "" {
		return author
	}
	return string(pr.AuthoredBy)
}

func codeReviewRequestContextAuthor(request *codereviewsvc.ReviewRequestContext) string {
	if request == nil {
		return ""
	}
	return request.AuthorLogin
}

func codeReviewRequestContextBody(request *codereviewsvc.ReviewRequestContext) string {
	if request == nil {
		return ""
	}
	return request.Body
}

func codeReviewRequestContextURL(request *codereviewsvc.ReviewRequestContext) string {
	if request == nil {
		return ""
	}
	return request.URL
}

func codeReviewAuthorClass(pr models.PullRequest) string {
	switch pr.AuthoredBy {
	case models.GitIdentitySourceApp:
		return "143"
	case models.GitIdentitySourceUser:
		return "human"
	default:
		return ""
	}
}

func loadCodeReviewAuthorTeams(
	ctx context.Context,
	stores *Stores,
	services *Services,
	policy models.CodeReviewPolicyConfig,
	job runCodeReviewPayload,
	pr models.PullRequest,
) ([]string, error) {
	policy = models.ResolveCodeReviewPolicyConfig(&policy)
	if len(policy.RiskPolicy.EligibleAuthorTeams) == 0 {
		return nil, nil
	}
	author := codeReviewAuthor(job, pr)
	if author == "" || models.CodeReviewAuthorAllowed(author, codeReviewAuthorClass(pr), policy.RiskPolicy.EligibleAuthors) {
		return nil, nil
	}
	if stores == nil || stores.Repositories == nil {
		return nil, fmt.Errorf("repository store unavailable")
	}
	repository, err := stores.Repositories.GetByID(ctx, job.OrgID, job.RepositoryID)
	if err != nil {
		return nil, fmt.Errorf("load repository for author team lookup: %w", err)
	}
	owner, _, found := strings.Cut(strings.TrimSpace(repository.FullName), "/")
	if !found || strings.TrimSpace(owner) == "" {
		return nil, fmt.Errorf("repository %q has no GitHub organization owner", repository.FullName)
	}
	if repository.InstallationID <= 0 {
		return nil, fmt.Errorf("repository %q has no GitHub installation", repository.FullName)
	}

	relevantTeams := make([]string, 0, len(policy.RiskPolicy.EligibleAuthorTeams))
	for _, teamRef := range policy.RiskPolicy.EligibleAuthorTeams {
		organization, teamSlug, ok := strings.Cut(strings.TrimSpace(teamRef), "/")
		if !ok || strings.TrimSpace(teamSlug) == "" || !strings.EqualFold(strings.TrimSpace(organization), owner) {
			continue
		}
		relevantTeams = append(relevantTeams, strings.TrimSpace(teamRef))
	}
	if len(relevantTeams) == 0 {
		return nil, nil
	}
	if services == nil || services.GitHub == nil {
		return nil, fmt.Errorf("GitHub service unavailable")
	}
	checker, ok := services.GitHub.(codeReviewAuthorTeamMembershipChecker)
	if !ok {
		return nil, fmt.Errorf("GitHub team membership lookup unavailable")
	}
	for _, teamRef := range relevantTeams {
		organization, teamSlug, _ := strings.Cut(teamRef, "/")
		active, err := checker.IsActiveTeamMember(ctx, repository.InstallationID, organization, teamSlug, author)
		if err != nil {
			return nil, fmt.Errorf("check @%s membership for %s: %w", teamRef, author, err)
		}
		if active {
			return []string{teamRef}, nil
		}
	}
	return nil, nil
}

func resolveCodeReviewAuthorTeams(
	ctx context.Context,
	stores *Stores,
	services *Services,
	policy models.CodeReviewPolicyConfig,
	job runCodeReviewPayload,
	pr models.PullRequest,
) ([]string, error) {
	teams, err := loadCodeReviewAuthorTeams(ctx, stores, services, policy, job, pr)
	if err != nil {
		return nil, classifyGitHubJobError(fmt.Errorf("verify code review author team eligibility: %w", err), job.SessionID.String())
	}
	return teams, nil
}

func ensureCodeReviewInlineSelection(ctx context.Context, store *db.CodeReviewStore, job runCodeReviewPayload, findings []models.CodeReviewFinding, changedFiles []codereviewsvc.PullRequestFile, limit int) error {
	if store == nil || len(findings) == 0 {
		return nil
	}
	for _, finding := range findings {
		if finding.SelectedForInline && finding.Severity.IsBlocking() {
			return nil
		}
	}
	inlineable := codeReviewFindingsOnChangedLines(findings, changedFiles)
	selected := models.SelectCodeReviewInlineFindings(inlineable, limit)
	if len(selected) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(selected))
	for _, finding := range selected {
		if finding.ID == uuid.Nil {
			continue
		}
		ids = append(ids, finding.ID)
	}
	_, err := store.MarkFindingsSelectedForInline(ctx, job.OrgID, job.SessionID, ids)
	return err
}

func codeReviewFindingsOnChangedLines(findings []models.CodeReviewFinding, changedFiles []codereviewsvc.PullRequestFile) []models.CodeReviewFinding {
	changedLines := codeReviewChangedLineSet(changedFiles)
	if len(changedLines) == 0 {
		return nil
	}
	out := make([]models.CodeReviewFinding, 0, len(findings))
	for _, finding := range findings {
		if codeReviewFindingOnChangedLine(finding, changedLines) {
			out = append(out, finding)
		}
	}
	return out
}

func codeReviewFindingOnChangedLine(finding models.CodeReviewFinding, changedLines map[string]map[int]struct{}) bool {
	if finding.Path == nil || finding.StartLine == nil || *finding.StartLine <= 0 {
		return false
	}
	lines, ok := changedLines[filepath.ToSlash(strings.TrimSpace(*finding.Path))]
	if !ok || len(lines) == 0 {
		return false
	}
	start := *finding.StartLine
	end := start
	if finding.EndLine != nil && *finding.EndLine >= start {
		end = *finding.EndLine
	}
	for line := start; line <= end; line++ {
		if _, ok := lines[line]; ok {
			return true
		}
	}
	return false
}

func codeReviewChangedLineSet(files []codereviewsvc.PullRequestFile) map[string]map[int]struct{} {
	changed := make(map[string]map[int]struct{})
	for _, file := range files {
		path := filepath.ToSlash(strings.TrimSpace(file.Filename))
		patch := strings.TrimSpace(file.Patch)
		if path == "" || patch == "" {
			continue
		}
		lines := make(map[int]struct{})
		newLine := 0
		for _, diffLine := range strings.Split(patch, "\n") {
			if match := codeReviewDiffHunkPattern.FindStringSubmatch(diffLine); len(match) == 2 {
				parsed, err := strconv.Atoi(match[1])
				if err == nil {
					newLine = parsed
				}
				continue
			}
			if newLine <= 0 || strings.HasPrefix(diffLine, `\`) {
				continue
			}
			if strings.HasPrefix(diffLine, "+++") {
				continue
			}
			if strings.HasPrefix(diffLine, "---") {
				continue
			}
			if strings.HasPrefix(diffLine, "+") {
				lines[newLine] = struct{}{}
				newLine++
				continue
			}
			if strings.HasPrefix(diffLine, "-") {
				continue
			}
			newLine++
		}
		if len(lines) > 0 {
			changed[path] = lines
		}
	}
	return changed
}

var (
	codeReviewDirectivePattern       = regexp.MustCompile(`::code-comment\{([^}]*)\}`)
	codeReviewAttributePattern       = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)=("(?:\\.|[^"\\])*"|[^\s}]+)`)
	codeReviewPriorityPattern        = regexp.MustCompile(`(?i)\[P([0-3])\]`)
	codeReviewLeadingPriorityPattern = regexp.MustCompile(`(?i)^\[P[0-3]\]\s*`)
	codeReviewDiffHunkPattern        = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
)

func parseCodeReviewFindings(output string, changedPaths []string) []models.CodeReviewFinding {
	matches := codeReviewDirectivePattern.FindAllStringSubmatch(output, -1)
	findings := make([]models.CodeReviewFinding, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		attrs := parseCodeReviewDirectiveAttributes(match[1])
		title := strings.TrimSpace(attrs["title"])
		body := strings.TrimSpace(attrs["body"])
		file := codeReviewNormalizeFindingPath(attrs["file"], changedPaths)
		if title == "" || body == "" || file == "" {
			continue
		}
		startLine := parsePositiveInt(attrs["start"])
		if startLine == nil {
			startLine = parsePositiveInt(attrs["line"])
		}
		if startLine == nil {
			continue
		}
		endLine := parsePositiveInt(attrs["end"])
		if endLine == nil {
			endLine = startLine
		}
		summary := strings.TrimSpace(codeReviewPriorityPattern.ReplaceAllString(title, ""))
		if summary == "" {
			summary = title
		}
		severity := codeReviewSeverityFromDirective(title, attrs["priority"])
		confidence := models.CodeReviewFindingConfidenceHigh
		if severity == models.CodeReviewFindingSeverityLow || severity == models.CodeReviewFindingSeverityInfo {
			confidence = models.CodeReviewFindingConfidenceMedium
		}
		path := file
		findings = append(findings, models.CodeReviewFinding{
			DedupeKey:  codeReviewFindingDedupeKey(path, *startLine, *endLine, summary),
			Severity:   severity,
			Confidence: confidence,
			Path:       &path,
			StartLine:  startLine,
			EndLine:    endLine,
			Summary:    summary,
			Body:       body,
		})
	}
	return findings
}

func parseCodeReviewDirectiveAttributes(raw string) map[string]string {
	attrs := make(map[string]string)
	for _, match := range codeReviewAttributePattern.FindAllStringSubmatch(raw, -1) {
		if len(match) < 3 {
			continue
		}
		value := strings.TrimSpace(match[2])
		if strings.HasPrefix(value, `"`) {
			if unquoted, err := strconv.Unquote(value); err == nil {
				value = unquoted
			}
		}
		attrs[strings.ToLower(match[1])] = value
	}
	return attrs
}

func parsePositiveInt(raw string) *int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return nil
	}
	return &value
}

func codeReviewSeverityFromDirective(title, priorityRaw string) models.CodeReviewFindingSeverity {
	priority := strings.TrimSpace(priorityRaw)
	if priority == "" {
		if match := codeReviewPriorityPattern.FindStringSubmatch(title); len(match) == 2 {
			priority = match[1]
		}
	}
	switch priority {
	case "0":
		return models.CodeReviewFindingSeverityCritical
	case "1":
		return models.CodeReviewFindingSeverityHigh
	case "2":
		return models.CodeReviewFindingSeverityMedium
	case "3":
		return models.CodeReviewFindingSeverityLow
	default:
		return models.CodeReviewFindingSeverityMedium
	}
}

func codeReviewNormalizeFindingPath(raw string, changedPaths []string) string {
	path := filepath.ToSlash(strings.TrimSpace(raw))
	path = strings.TrimPrefix(path, "file://")
	if path == "" {
		return ""
	}
	for _, changed := range changedPaths {
		changed = filepath.ToSlash(strings.TrimSpace(changed))
		if changed == "" {
			continue
		}
		if path == changed || strings.HasSuffix(path, "/"+changed) {
			return changed
		}
	}
	if strings.HasPrefix(path, "/") {
		return strings.TrimLeft(path, "/")
	}
	return path
}

func codeReviewFindingDedupeKey(path string, startLine, endLine int, summary string) string {
	return fmt.Sprintf("%s:%d:%d:%s", path, startLine, endLine, strings.ToLower(strings.TrimSpace(summary)))
}

func codeReviewAgentSummaries(results []models.CodeReviewAgentResult, findings []models.CodeReviewFinding) []string {
	findingCounts := make(map[uuid.UUID]int)
	for _, finding := range findings {
		if finding.AgentResultID != nil {
			findingCounts[*finding.AgentResultID]++
		}
	}
	summaries := make([]string, 0)
	for _, result := range results {
		if result.Role != models.CodeReviewAgentRoleReviewer {
			continue
		}
		name := codeReviewAgentDisplayName(result.AgentProvider)
		switch result.Status {
		case models.CodeReviewAgentResultStatusCompleted:
			if !codeReviewReviewerResultHasUsableOutput(result) {
				summaries = append(summaries, name+" produced no usable review output")
				continue
			}
			if findingCounts[result.ID] == 0 {
				summaries = append(summaries, name+" found no blocking issues")
			} else {
				count := findingCounts[result.ID]
				label := "findings"
				if count == 1 {
					label = "finding"
				}
				summaries = append(summaries, fmt.Sprintf("%s reported %d %s", name, count, label))
			}
		case models.CodeReviewAgentResultStatusFailed:
			state, ok := parseCodeReviewReviewerStructuredResult(result.StructuredResult)
			if ok && state.Unavailable {
				summaries = append(summaries, name+" unavailable")
			} else {
				summaries = append(summaries, name+" failed")
			}
		case models.CodeReviewAgentResultStatusTimedOut:
			summaries = append(summaries, name+" timed out")
		default:
			summaries = append(summaries, name+" pending")
		}
	}
	return summaries
}

func codeReviewFailedDescriptionRequirements(summaries []string) []string {
	issues := make([]string, 0)
	for _, summary := range summaries {
		summary = strings.TrimSpace(summary)
		if !strings.Contains(summary, ": failed") {
			continue
		}
		issues = append(issues, strings.TrimSpace(strings.Replace(summary, ": failed", "", 1)))
	}
	return issues
}

func codeReviewAgentDisplayName(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch provider {
	case "codex":
		return "Codex"
	case "claude", "claude_code":
		return "Claude Code"
	case "opencode", "open_code":
		return "OpenCode"
	case "gemini":
		return "Gemini"
	case "":
		return "Review agent"
	}

	words := strings.FieldsFunc(provider, func(r rune) bool {
		return r == '_' || r == '-' || r == ' '
	})
	for i, word := range words {
		if word == "" {
			continue
		}
		words[i] = strings.ToUpper(word[:1]) + word[1:]
	}
	if len(words) == 0 {
		return "Review agent"
	}
	return strings.Join(words, " ")
}

func codeReviewRecommendedHumanReviewers(reasons []models.CodeReviewRiskReason) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	add := func(value string) {
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	for _, reason := range reasons {
		switch reason.Code {
		case models.CodeReviewRiskReasonPromptInjection:
			add("security/platform")
		}
	}
	return out
}

func submitCodeReviewToGitHub(ctx context.Context, stores *Stores, services *Services, job runCodeReviewPayload, metadata models.CodeReviewSessionMetadata, decision models.CodeReviewDecision, body string) (codeReviewSubmission, bool, error) {
	if services == nil || services.CodeReviews == nil {
		return codeReviewSubmission{}, false, nil
	}
	if stores.Repositories == nil || stores.PullRequests == nil {
		return codeReviewSubmission{}, false, fmt.Errorf("submit code review: repository and pull request stores are required")
	}
	if metadata.GitHubReviewID != nil {
		return codeReviewSubmission{
			GitHubReviewID:  metadata.GitHubReviewID,
			GitHubReviewURL: metadata.GitHubReviewURL,
			FinalReviewBody: stringPtrValue(metadata.FinalReviewBody),
		}, false, nil
	}

	repo, err := stores.Repositories.GetByID(ctx, job.OrgID, job.RepositoryID)
	if err != nil {
		return codeReviewSubmission{}, false, fmt.Errorf("load code review repository: %w", err)
	}
	if repo.InstallationID == 0 {
		return codeReviewSubmission{}, false, fmt.Errorf("submit code review: repository %s has no GitHub installation id", repo.ID)
	}
	pr, err := stores.PullRequests.GetByID(ctx, job.OrgID, job.PullRequestID)
	if err != nil {
		return codeReviewSubmission{}, false, fmt.Errorf("load code review pull request: %w", err)
	}

	repository := strings.TrimSpace(pr.GitHubRepo)
	if repository == "" {
		repository = strings.TrimSpace(repo.FullName)
	}
	findings, err := stores.CodeReviews.ListFindings(ctx, job.OrgID, job.SessionID, true)
	if err != nil {
		return codeReviewSubmission{}, false, fmt.Errorf("list selected code review findings: %w", err)
	}
	comments := codeReviewInlineComments(findings)
	submitRequest := codereviewsvc.SubmitReviewRequest{
		InstallationID:    repo.InstallationID,
		Repository:        repository,
		PullNumber:        pr.GitHubPRNumber,
		HeadSHA:           job.HeadSHA,
		OutputKey:         job.OutputKey,
		PreviousOutputKey: job.PreviousOutputKey,
		ExistingReviewID:  int64PtrValue(job.ExistingGitHubReviewID),
		ExistingReviewURL: stringPtrValue(job.ExistingGitHubReviewURL),
		Decision:          codeReviewSubmitDecision(decision),
		PreviousDecision:  codeReviewSubmitDecisionPtr(job.PreviousReviewDecision),
		PreviousDecidedAt: timePtrValue(job.PreviousReviewDecidedAt),
		PreviousBody:      stringPtrValue(job.PreviousReviewBody),
		Body:              body,
		Comments:          comments,
	}
	var result codereviewsvc.SubmitReviewResult
	err = stores.CodeReviews.RunWithGitHubPublicationLock(ctx, job.OrgID, job.PullRequestID, func(lockCtx context.Context, _ db.DBTX) error {
		var submitErr error
		result, submitErr = services.CodeReviews.SubmitReview(lockCtx, submitRequest)
		return submitErr
	})
	if err != nil {
		return codeReviewSubmission{}, false, classifyGitHubJobError(fmt.Errorf("submit code review to GitHub: %w", err), job.SessionID.String())
	}
	finalReviewBody := strings.TrimSpace(result.Body)
	if finalReviewBody == "" {
		finalReviewBody = body
	}
	if _, err := stores.CodeReviews.RecordGitHubReview(ctx, job.OrgID, job.SessionID, result.ID, result.URL, finalReviewBody); err != nil {
		return codeReviewSubmission{}, true, fmt.Errorf("record submitted code review: %w", err)
	}
	markPostedCodeReviewFindings(ctx, stores.CodeReviews, job.OrgID, findings, result.Comments)
	return codeReviewSubmission{
		GitHubReviewID:  &result.ID,
		GitHubReviewURL: &result.URL,
		FinalReviewBody: finalReviewBody,
	}, true, nil
}

func int64PtrValue(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func codeReviewSubmitDecision(decision models.CodeReviewDecision) codereviewsvc.SubmitReviewDecision {
	return codereviewsvc.SubmitReviewDecision(decision)
}

func codeReviewSubmitDecisionPtr(decision *models.CodeReviewDecision) codereviewsvc.SubmitReviewDecision {
	if decision == nil {
		return ""
	}
	return codeReviewSubmitDecision(*decision)
}

func timePtrValue(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

func codeReviewInlineComments(findings []models.CodeReviewFinding) []codereviewsvc.SubmitReviewComment {
	comments := make([]codereviewsvc.SubmitReviewComment, 0, len(findings))
	for _, finding := range findings {
		if !finding.Severity.IsBlocking() {
			continue
		}
		if finding.GitHubCommentID != nil {
			continue
		}
		if finding.Path == nil || strings.TrimSpace(*finding.Path) == "" || finding.StartLine == nil || *finding.StartLine <= 0 {
			continue
		}
		body := codeReviewInlineCommentBody(finding)
		if body == "" {
			continue
		}
		comments = append(comments, codereviewsvc.SubmitReviewComment{
			Path:      *finding.Path,
			Line:      *finding.StartLine,
			Body:      body,
			DedupeKey: finding.DedupeKey,
		})
	}
	return comments
}

func codeReviewInlineCommentBody(finding models.CodeReviewFinding) string {
	body := strings.TrimSpace(finding.Body)
	if body == "" {
		body = strings.TrimSpace(finding.Summary)
	}
	if body == "" {
		return ""
	}
	return codeReviewPriorityPrefix(finding.Severity) + " " + codeReviewLeadingPriorityPattern.ReplaceAllString(body, "")
}

func codeReviewPriorityPrefix(severity models.CodeReviewFindingSeverity) string {
	switch severity {
	case models.CodeReviewFindingSeverityCritical:
		return "[P0]"
	case models.CodeReviewFindingSeverityHigh:
		return "[P1]"
	case models.CodeReviewFindingSeverityMedium:
		return "[P2]"
	case models.CodeReviewFindingSeverityLow, models.CodeReviewFindingSeverityInfo:
		return "[P3]"
	default:
		return "[P2]"
	}
}

func markPostedCodeReviewFindings(ctx context.Context, store *db.CodeReviewStore, orgID uuid.UUID, findings []models.CodeReviewFinding, posted []codereviewsvc.SubmitReviewPostedComment) {
	if store == nil || len(findings) == 0 || len(posted) == 0 {
		return
	}
	used := make(map[int]struct{})
	for _, finding := range findings {
		if finding.ID == uuid.Nil || finding.GitHubCommentID != nil || finding.Path == nil || finding.StartLine == nil {
			continue
		}
		body := codeReviewInlineCommentBody(finding)
		for idx, comment := range posted {
			if _, ok := used[idx]; ok {
				continue
			}
			if comment.ID == 0 ||
				comment.Line != *finding.StartLine ||
				!strings.EqualFold(strings.TrimSpace(comment.Path), strings.TrimSpace(*finding.Path)) ||
				!codeReviewPostedCommentMatchesFinding(comment, finding, body) {
				continue
			}
			if _, err := store.MarkFindingPosted(ctx, orgID, finding.ID, comment.ID); err == nil {
				used[idx] = struct{}{}
			}
			break
		}
	}
}

func codeReviewPostedCommentMatchesFinding(comment codereviewsvc.SubmitReviewPostedComment, finding models.CodeReviewFinding, body string) bool {
	if strings.TrimSpace(comment.DedupeKey) != "" && strings.TrimSpace(comment.DedupeKey) == strings.TrimSpace(finding.DedupeKey) {
		return true
	}
	posted := strings.TrimSpace(comment.Body)
	body = strings.TrimSpace(body)
	return posted == body || strings.HasPrefix(posted, body+"\n")
}
