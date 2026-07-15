package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/db"
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
	OrgID                  uuid.UUID `json:"org_id"`
	SessionID              uuid.UUID `json:"session_id"`
	MetadataID             uuid.UUID `json:"metadata_id"`
	RepositoryID           uuid.UUID `json:"repository_id"`
	PullRequestID          uuid.UUID `json:"pull_request_id"`
	PolicyID               uuid.UUID `json:"policy_id"`
	PolicyVersion          int       `json:"policy_version"`
	HeadSHA                string    `json:"head_sha"`
	FromFork               bool      `json:"from_fork"`
	PullRequestAuthor      string    `json:"pull_request_author,omitempty"`
	OutputKey              string    `json:"review_output_key"`
	RequestedReviewerLogin string    `json:"requested_reviewer_login,omitempty"`
	RequestedTeamSlug      string    `json:"requested_team_slug,omitempty"`
}

const codeReviewRawOutputInlineLimit = 32 * 1024

type codeReviewDescriptionEvaluation struct {
	Passed                 bool
	PromptInjectionFound   bool
	RequirementSummaries   []string
	FailedRequirementCount int
}

type codeReviewOrchestratorSynthesis struct {
	Summary                 string   `json:"summary,omitempty"`
	RiskNotes               []string `json:"risk_notes,omitempty"`
	ScopeMismatch           bool     `json:"scope_mismatch,omitempty"`
	UnresolvedUncertainty   bool     `json:"unresolved_uncertainty,omitempty"`
	ReviewerDisagreement    bool     `json:"reviewer_disagreement,omitempty"`
	PromptInjectionDetected bool     `json:"prompt_injection_detected,omitempty"`
}

func newRunCodeReviewHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, _ string, payload json.RawMessage) error {
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
					return nil
				}
			}
			return fmt.Errorf("mark code review running: %w", err)
		}
		policy, err := stores.CodeReviews.GetPolicyByID(ctx, job.OrgID, job.PolicyID)
		if err != nil {
			return fmt.Errorf("load captured code review policy: %w", err)
		}
		if syncErr := syncCodeReviewPullRequestState(ctx, services, logger, job); syncErr != nil {
			return syncErr
		}
		pr, err := stores.PullRequests.GetByID(ctx, job.OrgID, job.PullRequestID)
		if err != nil {
			return fmt.Errorf("load code review pull request: %w", err)
		}
		if cancelled, err := stopCodeReviewIfParentSessionCancelled(ctx, stores, services, logger, job, pr); cancelled || err != nil {
			return err
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
		changedFiles, changedFilesAvailable, err := loadCodeReviewChangedFiles(ctx, stores, services, job, pr)
		if err != nil {
			return fmt.Errorf("load code review changed files: %w", err)
		}
		if codeReviewHeadChanged(job.HeadSHA, pr, health) {
			if _, staleErr := stores.CodeReviews.MarkStale(ctx, job.OrgID, job.SessionID, "PR head changed after review started"); staleErr != nil {
				return fmt.Errorf("mark code review stale: %w", staleErr)
			}
			logger.Info().
				Str("org_id", job.OrgID.String()).
				Str("session_id", job.SessionID.String()).
				Str("reviewed_head", job.HeadSHA).
				Msg("marked code review stale after PR head changed")
			reconcileCodeReviewSessionStale(ctx, stores, logger, job)
			return nil
		}
		descriptionEvaluation, err := evaluateCodeReviewDescriptionPolicy(ctx, stores, services, logger, job, pr, policy, metadata, changedFiles)
		if err != nil {
			return err
		}
		reviewContext, reviewContextChecked, reviewContextAvailable, err := loadCodeReviewReviewContext(ctx, stores, services, job, pr)
		if err != nil {
			return err
		}
		if codeReviewCanRunReviewerThreads(stores) {
			if err := ensureCodeReviewReviewerThreads(ctx, stores, services, logger, job, pr, policy, metadata, changedFiles); err != nil {
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
			if err := ensureCodeReviewOrchestratorThread(ctx, stores, services, logger, job, pr, health, policy, metadata, changedFiles, descriptionEvaluation, reviewContext, reviewContextAvailable, agentResults, findings); err != nil {
				return err
			}
			if err := harvestCodeReviewOrchestratorResult(ctx, stores, services, logger, job, policy, metadata, changedFiles); err != nil {
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
		decision, body := evaluateLiveCodeReviewOutcome(liveCodeReviewOutcomeInput{
			Policy:                 policy.Config(),
			Job:                    job,
			SessionURL:             codeReviewSessionURL(services.FrontendURL, job.SessionID),
			PullRequest:            pr,
			Health:                 health,
			AgentResults:           agentResults,
			Findings:               findings,
			ChangedFiles:           changedFiles,
			ChangedFilesAvailable:  changedFilesAvailable,
			DescriptionEvaluation:  descriptionEvaluation,
			ReviewContext:          reviewContext,
			ReviewContextChecked:   reviewContextChecked,
			ReviewContextAvailable: reviewContextAvailable,
			OrchestratorSynthesis:  codeReviewOrchestratorSynthesisFromResults(agentResults),
		})
		if err := ensureCodeReviewInlineSelection(ctx, stores.CodeReviews, job, findings, changedFiles, policy.Config().InlineCommentLimit); err != nil {
			return fmt.Errorf("select code review inline findings: %w", err)
		}
		if cancelled, err := stopCodeReviewIfParentSessionCancelled(ctx, stores, services, logger, job, pr); cancelled || err != nil {
			return err
		}
		submission, submitted, err := submitCodeReviewToGitHub(ctx, stores, services, job, metadata, decision.Decision, body)
		if err != nil {
			return err
		}
		removeCodeReviewRequestedReviewer(ctx, stores, services, logger, job, pr)
		if _, err := stores.CodeReviews.CompleteReview(ctx, job.OrgID, db.CompleteCodeReviewParams{
			SessionID:       job.SessionID,
			Decision:        decision.Decision,
			Acceptable:      decision.Acceptable,
			GitHubReviewID:  submission.GitHubReviewID,
			GitHubReviewURL: submission.GitHubReviewURL,
			FinalReviewBody: body,
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
	logger.Info().
		Str("session_id", job.SessionID.String()).
		Msg("stopped code review because parent session was cancelled")
	return true, nil
}

func failCodeReviewWithoutReviewerOutput(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, job runCodeReviewPayload, pr models.PullRequest, results []models.CodeReviewAgentResult) error {
	reason := codeReviewNoUsableReviewerOutputReason(results)
	if _, err := stores.CodeReviews.FailReview(ctx, job.OrgID, job.SessionID, reason); err != nil {
		return fmt.Errorf("fail code review without usable reviewer output: %w", err)
	}
	removeCodeReviewRequestedReviewer(ctx, stores, services, logger, job, pr)
	if err := reconcileCodeReviewSessionFailure(ctx, stores, job, reason); err != nil {
		return err
	}
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
	if err := stores.Sessions.UpdateFailure(ctx, job.OrgID, job.SessionID, reason, "code_review_no_reviewer_output", []string{"Configure at least one reviewer credential and request the review again."}, true); err != nil {
		return fmt.Errorf("record code review parent session failure details: %w", err)
	}
	return nil
}

func syncCodeReviewPullRequestState(ctx context.Context, services *Services, logger zerolog.Logger, job runCodeReviewPayload) error {
	if services == nil || services.PR == nil {
		return nil
	}
	if err := services.PR.SyncPullRequestState(ctx, job.OrgID, job.PullRequestID); err != nil {
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
		return fmt.Errorf("sync code review pull request state: %w", err)
	}
	return nil
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
	PromptArtifactKey string  `json:"prompt_artifact_key,omitempty"`
	FindingCount      int     `json:"finding_count,omitempty"`
	CostCents         float64 `json:"cost_cents,omitempty"`
	RawArtifactKey    string  `json:"raw_artifact_key,omitempty"`
	NativeReview      bool    `json:"native_review,omitempty"`
	ReadOnly          bool    `json:"read_only,omitempty"`
	ReadOnlyViolation bool    `json:"read_only_violation,omitempty"`
	Reverted          bool    `json:"reverted,omitempty"`
	Unavailable       bool    `json:"unavailable,omitempty"`
	Error             string  `json:"error,omitempty"`
	CompletedAt       string  `json:"completed_at,omitempty"`
}

type codeReviewOrchestratorStructuredResult struct {
	ThreadID          string                          `json:"thread_id,omitempty"`
	PromptArtifactKey string                          `json:"prompt_artifact_key,omitempty"`
	FindingCount      int                             `json:"finding_count,omitempty"`
	CostCents         float64                         `json:"cost_cents,omitempty"`
	RawArtifactKey    string                          `json:"raw_artifact_key,omitempty"`
	Synthesis         codeReviewOrchestratorSynthesis `json:"synthesis,omitempty"`
	ReadOnly          bool                            `json:"read_only,omitempty"`
	ReadOnlyViolation bool                            `json:"read_only_violation,omitempty"`
	Reverted          bool                            `json:"reverted,omitempty"`
	Error             string                          `json:"error,omitempty"`
	CompletedAt       string                          `json:"completed_at,omitempty"`
}

func ensureCodeReviewReviewerThreads(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, job runCodeReviewPayload, pr models.PullRequest, policy models.CodeReviewPolicyRecord, metadata models.CodeReviewSessionMetadata, changedFiles []codereviewsvc.PullRequestFile) error {
	results, err := stores.CodeReviews.ListAgentResults(ctx, job.OrgID, job.SessionID)
	if err != nil {
		return fmt.Errorf("list code review reviewer results: %w", err)
	}
	existing := codeReviewReviewerResultsByKey(results)
	cfg := policy.Config()
	rootKey := codeReviewPromptArtifactRoot(metadata, job)
	if metadata.PromptArtifactKey == nil || strings.TrimSpace(*metadata.PromptArtifactKey) == "" {
		if _, err := stores.CodeReviews.SetPromptArtifactKey(ctx, job.OrgID, job.SessionID, rootKey); err != nil {
			return fmt.Errorf("set code review prompt artifact key: %w", err)
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
		promptText := codeReviewReviewerPrompt(job, pr, cfg, policy.Version, metadata.BaseSHA, changedFiles)
		artifactKey := fmt.Sprintf("%s/reviewer-%02d-%s", rootKey, idx+1, agentType)
		artifactMetadata, err := json.Marshal(map[string]any{
			"reviewer_key": key,
			"agent_type":   agentType,
			"agent_model":  stringPtrValue(agentModel),
			"head_sha":     job.HeadSHA,
		})
		if err != nil {
			return fmt.Errorf("marshal reviewer prompt artifact metadata: %w", err)
		}
		artifact := &models.CodeReviewPromptArtifact{
			OrgID:         job.OrgID,
			SessionID:     job.SessionID,
			ArtifactKey:   artifactKey,
			Role:          string(models.CodeReviewAgentRoleReviewer),
			AgentProvider: string(agentType),
			Content:       promptText,
			Metadata:      artifactMetadata,
		}
		if err := stores.CodeReviews.CreatePromptArtifact(ctx, artifact); err != nil {
			return fmt.Errorf("create reviewer prompt artifact: %w", err)
		}
		thread, err := threads.CreateThread(ctx, threadsvc.CreateThreadInput{
			SessionID:       job.SessionID,
			OrgID:           job.OrgID,
			AgentType:       string(agentType),
			Model:           stringPtrValue(agentModel),
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
			ReviewerKey:       key,
			ReviewerIndex:     idx,
			ThreadID:          thread.ID.String(),
			PromptArtifactKey: artifactKey,
			NativeReview:      codeReviewAgentHasBuiltinReviewCommand(agentType),
			ReadOnly:          true,
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
		if _, err := threads.SendMessage(ctx, threadsvc.SendMessageInput{
			SessionID:     job.SessionID,
			OrgID:         job.OrgID,
			ThreadID:      thread.ID,
			Message:       codeReviewReviewerMessage(agentType, promptText),
			Commands:      codeReviewNativeReviewCommands(agentType, promptText),
			MessageSource: models.SessionMessageSourceAgentTool,
		}); err != nil {
			raw := err.Error()
			if _, updateErr := stores.CodeReviews.UpdateAgentResultOutcome(ctx, job.OrgID, result.ID, models.CodeReviewAgentResultStatusFailed, &raw, marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{
				ReviewerKey:       key,
				ReviewerIndex:     idx,
				ThreadID:          thread.ID.String(),
				PromptArtifactKey: artifactKey,
				Error:             raw,
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
	AgentType  models.AgentType
	AgentModel *string
	Available  bool
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
		AgentType:  cfg.AgentRoster.Orchestrator,
		AgentModel: codeReviewOrchestratorAgentModel(cfg),
		Available:  true,
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
				AgentType:  agentType,
				AgentModel: agentModel,
				Available:  true,
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
	timedOut := codeReviewReviewTimedOut(policy.Config(), metadata)
	changedPaths := codeReviewChangedPaths(changedFiles)
	for _, result := range results {
		if result.Role != models.CodeReviewAgentRoleReviewer || codeReviewReviewerResultTerminal(result.Status) {
			continue
		}
		state, ok := parseCodeReviewReviewerStructuredResult(result.StructuredResult)
		if !ok || strings.TrimSpace(state.ThreadID) == "" {
			raw := "reviewer result is missing its thread id"
			if _, err := stores.CodeReviews.UpdateAgentResultOutcome(ctx, job.OrgID, result.ID, models.CodeReviewAgentResultStatusFailed, &raw, result.StructuredResult); err != nil {
				return fmt.Errorf("mark malformed reviewer result failed: %w", err)
			}
			continue
		}
		threadID, err := uuid.Parse(state.ThreadID)
		if err != nil {
			raw := "reviewer result has an invalid thread id: " + err.Error()
			if _, err := stores.CodeReviews.UpdateAgentResultOutcome(ctx, job.OrgID, result.ID, models.CodeReviewAgentResultStatusFailed, &raw, result.StructuredResult); err != nil {
				return fmt.Errorf("mark invalid reviewer result failed: %w", err)
			}
			continue
		}
		if timedOut {
			raw := "reviewer timed out before producing a completed turn"
			state.Error = raw
			if thread, cancelErr := cancelCodeReviewThread(ctx, stores, logger, job, threadID); cancelErr == nil {
				state.CostCents = thread.CostCents
			} else {
				logger.Warn().Err(cancelErr).
					Str("session_id", job.SessionID.String()).
					Str("thread_id", threadID.String()).
					Msg("failed to cancel timed-out code review reviewer thread")
			}
			if _, err := stores.CodeReviews.UpdateAgentResultOutcome(ctx, job.OrgID, result.ID, models.CodeReviewAgentResultStatusTimedOut, &raw, marshalCodeReviewReviewerStructuredResult(state)); err != nil {
				return fmt.Errorf("mark reviewer timed out: %w", err)
			}
			continue
		}
		thread, err := stores.SessionThreads.GetByID(ctx, job.OrgID, threadID)
		if err != nil {
			return fmt.Errorf("load code review reviewer thread: %w", err)
		}
		state.CostCents = thread.CostCents
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
			state.CompletedAt = time.Now().UTC().Format(time.RFC3339)
			rawOutput, rawArtifactKey, err := codeReviewRawOutputForStorage(ctx, stores, job, result.ID, models.CodeReviewAgentRoleReviewer, result.AgentProvider, raw)
			if err != nil {
				return err
			}
			state.RawArtifactKey = rawArtifactKey
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
				state.CompletedAt = time.Now().UTC().Format(time.RFC3339)
				rawOutput, rawArtifactKey, err := codeReviewRawOutputForStorage(ctx, stores, job, result.ID, models.CodeReviewAgentRoleReviewer, result.AgentProvider, raw)
				if err != nil {
					return err
				}
				state.RawArtifactKey = rawArtifactKey
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
		state.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		rawOutput, rawArtifactKey, err := codeReviewRawOutputForStorage(ctx, stores, job, result.ID, models.CodeReviewAgentRoleReviewer, result.AgentProvider, raw)
		if err != nil {
			return err
		}
		state.RawArtifactKey = rawArtifactKey
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

func codeReviewPromptArtifactRoot(metadata models.CodeReviewSessionMetadata, job runCodeReviewPayload) string {
	if metadata.PromptArtifactKey != nil && strings.TrimSpace(*metadata.PromptArtifactKey) != "" {
		return strings.TrimSpace(*metadata.PromptArtifactKey)
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

func codeReviewOrchestratorAgentModel(cfg models.CodeReviewPolicyConfig) *string {
	if cfg.AgentRoster.OrchestratorModel != nil {
		if model := strings.TrimSpace(*cfg.AgentRoster.OrchestratorModel); model != "" {
			return &model
		}
	}
	return codeReviewDefaultAgentModel(cfg.AgentRoster.Orchestrator)
}

func storeCodeReviewPromptArtifact(ctx context.Context, stores *Stores, artifact models.CodeReviewPromptArtifact) error {
	if stores == nil || stores.CodeReviews == nil {
		return nil
	}
	if err := stores.CodeReviews.CreatePromptArtifact(ctx, &artifact); err != nil {
		return fmt.Errorf("store code review prompt artifact: %w", err)
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

func safeCodeReviewArtifactSegment(value string) string {
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
		return "artifact"
	}
	return out
}

func codeReviewRawOutputForStorage(ctx context.Context, stores *Stores, job runCodeReviewPayload, resultID uuid.UUID, role models.CodeReviewAgentRole, provider, raw string) (*string, string, error) {
	if len(raw) <= codeReviewRawOutputInlineLimit {
		return &raw, "", nil
	}
	if stores == nil || stores.CodeReviews == nil {
		truncated := raw[:codeReviewRawOutputInlineLimit] + "\n\n[truncated: prompt artifact store unavailable]"
		return &truncated, "", nil
	}
	artifactRole := string(role) + "_output"
	artifactKey := fmt.Sprintf("code-review-prompts/%s/%s-output-%s", job.SessionID, safeCodeReviewArtifactSegment(string(role)), resultID)
	if strings.TrimSpace(job.OutputKey) != "" {
		artifactKey = fmt.Sprintf("%s/%s-output-%s", strings.TrimSpace(job.OutputKey), safeCodeReviewArtifactSegment(string(role)), resultID)
	}
	if err := storeCodeReviewPromptArtifact(ctx, stores, models.CodeReviewPromptArtifact{
		OrgID:         job.OrgID,
		SessionID:     job.SessionID,
		ArtifactKey:   artifactKey,
		Role:          artifactRole,
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
	summary := fmt.Sprintf("Raw output stored in prompt artifact %s (%d bytes).", artifactKey, len(raw))
	return &summary, artifactKey, nil
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

func codeReviewReviewerPrompt(job runCodeReviewPayload, pr models.PullRequest, cfg models.CodeReviewPolicyConfig, policyVersion int, baseSHA string, changedFiles []codereviewsvc.PullRequestFile) string {
	return strings.TrimSpace(prompts.CodeReviewReviewerPrompt(prompts.CodeReviewReviewerPromptData{}))
}

func codeReviewOrchestratorPrompt(job runCodeReviewPayload, pr models.PullRequest, health *models.PullRequestHealthResponse, cfg models.CodeReviewPolicyConfig, policyVersion int, baseSHA string, changedFiles []codereviewsvc.PullRequestFile, description codeReviewDescriptionEvaluation, reviewContext *codereviewsvc.ReviewContext, reviewContextAvailable bool, agentResults []models.CodeReviewAgentResult, findings []models.CodeReviewFinding) string {
	reviewContextSummary := "GitHub review context unavailable"
	if reviewContextAvailable && reviewContext != nil {
		reviewContextSummary = fmt.Sprintf("Unresolved human threads: %d; blocking human reviews: %d", reviewContext.UnresolvedHumanThreads, reviewContext.BlockingHumanReviews)
	}
	return prompts.CodeReviewOrchestratorPrompt(prompts.CodeReviewOrchestratorPromptData{
		Repository:             pr.GitHubRepo,
		PullNumber:             pr.GitHubPRNumber,
		PullRequestURL:         pr.GitHubPRURL,
		Title:                  pr.Title,
		Author:                 codeReviewAuthor(job, pr),
		BaseSHA:                firstNonEmpty(baseSHA, stringPtrValue(pr.BaseSHA)),
		HeadSHA:                job.HeadSHA,
		PolicyVersion:          policyVersion,
		ApprovalMode:           cfg.ApprovalMode,
		RequiredReviewerQuorum: cfg.AgentRoster.RequireReviewerQuorum,
		InlineCommentLimit:     cfg.InlineCommentLimit,
		DescriptionResults:     append([]string(nil), description.RequirementSummaries...),
		RiskReasons:            codeReviewPromptRiskReasons(job, pr, health, cfg, changedFiles, description, reviewContext, reviewContextAvailable, agentResults, findings),
		ReviewerOutputs:        codeReviewReviewerOutputsForPrompt(agentResults),
		Findings:               codeReviewFindingsForPrompt(findings),
		ChangedFiles:           codeReviewChangedPaths(changedFiles),
		Checklist:              []string{reviewContextSummary},
	})
}

func codeReviewPromptRiskReasons(job runCodeReviewPayload, pr models.PullRequest, health *models.PullRequestHealthResponse, cfg models.CodeReviewPolicyConfig, changedFiles []codereviewsvc.PullRequestFile, description codeReviewDescriptionEvaluation, reviewContext *codereviewsvc.ReviewContext, reviewContextAvailable bool, agentResults []models.CodeReviewAgentResult, findings []models.CodeReviewFinding) []string {
	reviewerQuorum, _ := codeReviewReviewerEvidence(agentResults)
	descriptionPassed := description.Passed
	if len(description.RequirementSummaries) == 0 {
		descriptionPassed = codeReviewDescriptionPassed(cfg, pr, changedFiles)
	}
	unresolvedHumanThreads := codeReviewUnresolvedHumanThreads(pr)
	if reviewContext != nil {
		unresolvedHumanThreads += reviewContext.UnresolvedHumanThreads + reviewContext.BlockingHumanReviews
	}
	risk := models.EvaluateCodeReviewRisk(cfg, models.CodeReviewRiskInput{
		FilesChanged:           len(changedFiles),
		LinesChanged:           codeReviewLinesChanged(changedFiles),
		ChangedPaths:           codeReviewChangedPaths(changedFiles),
		Categories:             codeReviewChangedCategories(changedFiles),
		ChecksPassing:          codeReviewChecksPassing(cfg, health),
		RequiredChecksPassing:  codeReviewRequiredChecksPassing(cfg, health),
		DescriptionPassed:      descriptionPassed,
		UpToDate:               codeReviewUpToDate(health),
		Author:                 codeReviewAuthor(job, pr),
		AuthorClass:            codeReviewAuthorClass(pr),
		FromFork:               job.FromFork,
		ContextFetchFailed:     health == nil || !reviewContextAvailable,
		HeadSHAChanged:         codeReviewHeadChanged(job.HeadSHA, pr, health),
		BlockingFindings:       codeReviewBlockingFindings(findings),
		ReviewerDisagreement:   false,
		UnresolvedHumanThreads: unresolvedHumanThreads,
		PromptInjectionFound:   description.PromptInjectionFound,
	})
	if reviewerQuorum < cfg.AgentRoster.RequireReviewerQuorum && !codeReviewLowRiskQuorumWaived(cfg, changedFiles) {
		risk.Reasons = append(risk.Reasons, fmt.Sprintf("reviewer quorum %d is below policy requirement %d", reviewerQuorum, cfg.AgentRoster.RequireReviewerQuorum))
	}
	return risk.Reasons
}

// codeReviewLowRiskQuorumWaived reports whether the resolved policy's low-risk
// lane waives the reviewer-quorum requirement for this change. It lets a clean
// low-risk change (e.g. docs-only) approve on the heuristic gates even when the
// review agents fail or time out, which is otherwise the dominant blocker.
func codeReviewLowRiskQuorumWaived(policy models.CodeReviewPolicyConfig, changedFiles []codereviewsvc.PullRequestFile) bool {
	lane := models.ResolveCodeReviewPolicyConfig(&policy).RiskPolicy.LowRiskLane
	if !lane.WaiveReviewerQuorum {
		return false
	}
	return models.CodeReviewLowRiskLaneApplies(lane, codeReviewChangedCategories(changedFiles))
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

type codeReviewDescriptionLLMResponse struct {
	Passed                  bool   `json:"passed"`
	Reason                  string `json:"reason"`
	PromptInjectionDetected bool   `json:"prompt_injection_detected"`
}

type codeReviewDescriptionArtifactMetadata struct {
	RequirementKey          string `json:"requirement_key"`
	RequirementTitle        string `json:"requirement_title"`
	Passed                  *bool  `json:"passed"`
	Reason                  string `json:"reason"`
	PromptInjectionDetected bool   `json:"prompt_injection_detected"`
	PolicyVersion           int    `json:"policy_version"`
	HeadSHA                 string `json:"head_sha"`
}

func evaluateCodeReviewDescriptionPolicy(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, job runCodeReviewPayload, pr models.PullRequest, policy models.CodeReviewPolicyRecord, metadata models.CodeReviewSessionMetadata, changedFiles []codereviewsvc.PullRequestFile) (codeReviewDescriptionEvaluation, error) {
	cfg := policy.Config()
	body := ""
	if pr.Body != nil {
		body = strings.TrimSpace(*pr.Body)
	}
	evaluation := codeReviewDescriptionEvaluation{Passed: true}
	rootKey := codeReviewPromptArtifactRoot(metadata, job)
	cachedResults, err := loadCodeReviewDescriptionArtifactResults(ctx, stores, job, rootKey)
	if err != nil {
		return codeReviewDescriptionEvaluation{}, err
	}
	for idx, requirement := range cfg.DescriptionPolicy.Requirements {
		if !requirement.Required || strings.TrimSpace(requirement.Key) == "" || !codeReviewDescriptionRequirementApplies(requirement, changedFiles) {
			continue
		}
		artifactKey := fmt.Sprintf("%s/description-%02d-%s", rootKey, idx+1, safeCodeReviewArtifactSegment(requirement.Key))
		result, ok := cachedResults[artifactKey]
		if !ok {
			userPrompt := prompts.CodeReviewDescriptionCheckUserPrompt(prompts.CodeReviewDescriptionCheckUserPromptData{
				Title:        requirement.Title,
				Requirement:  requirement.Prompt,
				PRTitle:      pr.Title,
				PRBody:       body,
				ChangedFiles: codeReviewChangedPaths(changedFiles),
			})
			var evalErr error
			result, evalErr = evaluateCodeReviewDescriptionRequirement(ctx, services, requirement, body, userPrompt)
			if evalErr != nil {
				logger.Warn().Err(evalErr).
					Str("session_id", job.SessionID.String()).
					Str("requirement", requirement.Key).
					Msg("description requirement evaluation failed")
				result = codeReviewDescriptionLLMResponse{
					Passed: false,
					Reason: "description requirement could not be evaluated",
				}
			}
			if err := storeCodeReviewPromptArtifact(ctx, stores, models.CodeReviewPromptArtifact{
				OrgID:         job.OrgID,
				SessionID:     job.SessionID,
				ArtifactKey:   artifactKey,
				Role:          "description_policy",
				AgentProvider: codeReviewDescriptionEvaluatorProvider(services),
				Content:       prompts.CodeReviewDescriptionCheckSystemPrompt() + "\n\n" + userPrompt,
				Metadata: mustMarshalCodeReviewJSON(map[string]any{
					"requirement_key":           requirement.Key,
					"requirement_title":         requirement.Title,
					"passed":                    result.Passed,
					"reason":                    result.Reason,
					"prompt_injection_detected": result.PromptInjectionDetected,
					"policy_version":            policy.Version,
					"head_sha":                  job.HeadSHA,
				}),
			}); err != nil {
				return codeReviewDescriptionEvaluation{}, err
			}
		}
		summary := requirement.Title + ": passed"
		if !result.Passed {
			evaluation.Passed = false
			evaluation.FailedRequirementCount++
			summary = requirement.Title + ": failed"
			if strings.TrimSpace(result.Reason) != "" {
				summary += " (" + strings.TrimSpace(result.Reason) + ")"
			}
		}
		if result.PromptInjectionDetected {
			evaluation.Passed = false
			evaluation.PromptInjectionFound = true
			summary += " [prompt injection detected]"
		}
		evaluation.RequirementSummaries = append(evaluation.RequirementSummaries, summary)
	}
	return evaluation, nil
}

func loadCodeReviewDescriptionArtifactResults(ctx context.Context, stores *Stores, job runCodeReviewPayload, rootKey string) (map[string]codeReviewDescriptionLLMResponse, error) {
	if stores == nil || stores.CodeReviews == nil {
		return nil, nil
	}
	artifacts, err := stores.CodeReviews.ListPromptArtifacts(ctx, job.OrgID, job.SessionID)
	if err != nil {
		return nil, fmt.Errorf("list cached code review description artifacts: %w", err)
	}
	results := make(map[string]codeReviewDescriptionLLMResponse)
	for _, artifact := range artifacts {
		if artifact.Role != "description_policy" || !strings.HasPrefix(artifact.ArtifactKey, rootKey+"/description-") {
			continue
		}
		result, ok := codeReviewDescriptionResultFromArtifact(job, artifact)
		if ok {
			results[artifact.ArtifactKey] = result
		}
	}
	return results, nil
}

func codeReviewDescriptionResultFromArtifact(job runCodeReviewPayload, artifact models.CodeReviewPromptArtifact) (codeReviewDescriptionLLMResponse, bool) {
	if len(artifact.Metadata) == 0 {
		return codeReviewDescriptionLLMResponse{}, false
	}
	var metadata codeReviewDescriptionArtifactMetadata
	if err := json.Unmarshal(artifact.Metadata, &metadata); err != nil || metadata.Passed == nil {
		return codeReviewDescriptionLLMResponse{}, false
	}
	if strings.TrimSpace(metadata.HeadSHA) != "" && strings.TrimSpace(metadata.HeadSHA) != strings.TrimSpace(job.HeadSHA) {
		return codeReviewDescriptionLLMResponse{}, false
	}
	if metadata.PolicyVersion != 0 && metadata.PolicyVersion != job.PolicyVersion {
		return codeReviewDescriptionLLMResponse{}, false
	}
	reason := strings.TrimSpace(metadata.Reason)
	if reason == "" {
		if *metadata.Passed {
			reason = "requirement satisfied"
		} else {
			reason = "requirement not satisfied"
		}
	}
	return codeReviewDescriptionLLMResponse{
		Passed:                  *metadata.Passed,
		Reason:                  reason,
		PromptInjectionDetected: metadata.PromptInjectionDetected,
	}, true
}

func evaluateCodeReviewDescriptionRequirement(ctx context.Context, services *Services, requirement models.CodeReviewDescriptionRequirement, body, userPrompt string) (codeReviewDescriptionLLMResponse, error) {
	if codeReviewDescriptionPromptInjectionLikely(body) {
		return codeReviewDescriptionLLMResponse{Passed: false, Reason: "description contains instruction-override language", PromptInjectionDetected: true}, nil
	}
	if services != nil && services.LLM != nil {
		raw, err := services.LLM.Complete(ctx, prompts.CodeReviewDescriptionCheckSystemPrompt(), userPrompt)
		if err != nil {
			return codeReviewDescriptionLLMResponse{}, err
		}
		parsed, err := parseCodeReviewDescriptionLLMResponse(raw)
		if err != nil {
			return codeReviewDescriptionLLMResponse{}, err
		}
		if strings.TrimSpace(parsed.Reason) == "" {
			if parsed.Passed {
				parsed.Reason = "requirement satisfied"
			} else {
				parsed.Reason = "requirement not satisfied"
			}
		}
		return parsed, nil
	}
	if codeReviewDescriptionRequirementKnownBuiltIn(requirement) {
		passed := codeReviewDescriptionRequirementPassed(requirement, body)
		reason := "requirement satisfied"
		if !passed {
			reason = "required evidence missing"
		}
		return codeReviewDescriptionLLMResponse{Passed: passed, Reason: reason}, nil
	}
	return codeReviewDescriptionLLMResponse{Passed: false, Reason: "prompt-only requirement needs LLM evaluation"}, nil
}

func parseCodeReviewDescriptionLLMResponse(raw string) (codeReviewDescriptionLLMResponse, error) {
	raw = strings.TrimSpace(extractCodeReviewJSON(raw))
	var parsed codeReviewDescriptionLLMResponse
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return codeReviewDescriptionLLMResponse{}, fmt.Errorf("parse description check response: %w", err)
	}
	return parsed, nil
}

func codeReviewDescriptionRequirementKnownBuiltIn(requirement models.CodeReviewDescriptionRequirement) bool {
	switch strings.ToLower(strings.TrimSpace(requirement.Key)) {
	case "description", "summary", "intent", "testing", "tests", "validation", "ui_evidence", "screenshots", "screenshot", "preview":
		return true
	default:
		return false
	}
}

func codeReviewDescriptionPromptInjectionLikely(body string) bool {
	return containsAnyFold(body, []string{
		"ignore previous instructions",
		"ignore all previous instructions",
		"disregard previous instructions",
		"override your instructions",
		"system prompt",
		"developer message",
		"approval policy does not apply",
	})
}

func codeReviewDescriptionEvaluatorProvider(services *Services) string {
	if services != nil && services.LLM != nil {
		return "platform_llm"
	}
	return "deterministic"
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

func parseCodeReviewOrchestratorSynthesis(raw string) codeReviewOrchestratorSynthesis {
	var parsed codeReviewOrchestratorSynthesis
	if err := json.Unmarshal([]byte(extractCodeReviewJSON(raw)), &parsed); err != nil {
		return codeReviewOrchestratorSynthesis{}
	}
	return parsed
}

func codeReviewOrchestratorSynthesisFromResults(results []models.CodeReviewAgentResult) codeReviewOrchestratorSynthesis {
	for _, result := range results {
		if result.Role != models.CodeReviewAgentRoleOrchestrator {
			continue
		}
		state, ok := parseCodeReviewOrchestratorStructuredResult(result.StructuredResult)
		if !ok {
			continue
		}
		return state.Synthesis
	}
	return codeReviewOrchestratorSynthesis{}
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

func codeReviewReviewerResultTerminal(status models.CodeReviewAgentResultStatus) bool {
	switch status {
	case models.CodeReviewAgentResultStatusCompleted, models.CodeReviewAgentResultStatusFailed, models.CodeReviewAgentResultStatusTimedOut:
		return true
	default:
		return false
	}
}

func codeReviewReviewTimedOut(policy models.CodeReviewPolicyConfig, metadata models.CodeReviewSessionMetadata) bool {
	timeout := time.Duration(policy.AgentRoster.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	return time.Since(metadata.CreatedAt) > timeout
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

func ensureCodeReviewOrchestratorThread(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, job runCodeReviewPayload, pr models.PullRequest, health *models.PullRequestHealthResponse, policy models.CodeReviewPolicyRecord, metadata models.CodeReviewSessionMetadata, changedFiles []codereviewsvc.PullRequestFile, description codeReviewDescriptionEvaluation, reviewContext *codereviewsvc.ReviewContext, reviewContextAvailable bool, agentResults []models.CodeReviewAgentResult, findings []models.CodeReviewFinding) error {
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
	if codeReviewReviewTimedOut(cfg, metadata) {
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
	rootKey := codeReviewPromptArtifactRoot(metadata, job)
	artifactKey := fmt.Sprintf("%s/orchestrator-%s", rootKey, agentType)
	promptText := codeReviewOrchestratorPrompt(job, pr, health, cfg, policy.Version, metadata.BaseSHA, changedFiles, description, reviewContext, reviewContextAvailable, agentResults, findings)
	if err := storeCodeReviewPromptArtifact(ctx, stores, models.CodeReviewPromptArtifact{
		OrgID:         job.OrgID,
		SessionID:     job.SessionID,
		ArtifactKey:   artifactKey,
		Role:          string(models.CodeReviewAgentRoleOrchestrator),
		AgentProvider: string(agentType),
		Content:       promptText,
		Metadata: mustMarshalCodeReviewJSON(map[string]any{
			"head_sha":       job.HeadSHA,
			"policy_version": policy.Version,
			"agent_model":    stringPtrValue(agentModel),
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
	// non-claimable 'pending' state. Every reviewer thread is terminal by the
	// time we dispatch the orchestrator, so reset a stranded 'pending' session
	// back to idle so the SendMessage below can claim it instead of failing with
	// ErrSessionNotResumable — which would degrade the review and strand the
	// session for the reaper to sweep as "unable to start within the expected
	// time".
	if session.Status == models.SessionStatusPending {
		if resetErr := stores.Sessions.UpdateStatus(ctx, job.OrgID, job.SessionID, models.SessionStatusIdle); resetErr != nil {
			logger.Warn().Err(resetErr).Str("session_id", job.SessionID.String()).Msg("failed to reset stranded code review session before orchestrator dispatch")
		} else {
			session.Status = models.SessionStatusIdle
			logger.Warn().Str("session_id", job.SessionID.String()).Msg("reset stranded pending code review session to idle before orchestrator dispatch")
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
	if primaryThread.AgentType != agentType || !codeReviewAgentModelsEqual(primaryThread.ModelOverride, agentModel) {
		model := ""
		if agentModel != nil {
			model = *agentModel
		}
		_, updateErr := threads.UpdateThread(ctx, threadsvc.UpdateThreadInput{
			SessionID: job.SessionID,
			OrgID:     job.OrgID,
			ThreadID:  threadID,
			AgentType: string(agentType),
			Model:     &model,
			Label:     primaryThread.Label,
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
		ThreadID:          threadID.String(),
		PromptArtifactKey: artifactKey,
		ReadOnly:          false,
	})
	// The orchestrator agent result is created only once the thread is actually
	// dispatched. A transient claim race leaves no result behind, so the next
	// run_code_review poll re-enters this function cleanly and retries.
	if _, err := threads.SendMessage(ctx, threadsvc.SendMessageInput{
		SessionID:     job.SessionID,
		OrgID:         job.OrgID,
		ThreadID:      threadID,
		Message:       promptText,
		MessageSource: models.SessionMessageSourceAgentTool,
	}); err != nil {
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
				ThreadID:          threadID.String(),
				PromptArtifactKey: artifactKey,
				Error:             raw,
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

func codeReviewAgentModelsEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return strings.TrimSpace(*left) == strings.TrimSpace(*right)
}

func harvestCodeReviewOrchestratorResult(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, job runCodeReviewPayload, policy models.CodeReviewPolicyRecord, metadata models.CodeReviewSessionMetadata, changedFiles []codereviewsvc.PullRequestFile) error {
	results, err := stores.CodeReviews.ListAgentResults(ctx, job.OrgID, job.SessionID)
	if err != nil {
		return fmt.Errorf("list code review orchestrator results for harvest: %w", err)
	}
	timedOut := codeReviewReviewTimedOut(policy.Config(), metadata)
	changedPaths := codeReviewChangedPaths(changedFiles)
	for _, result := range results {
		if result.Role != models.CodeReviewAgentRoleOrchestrator || codeReviewReviewerResultTerminal(result.Status) {
			continue
		}
		state, ok := parseCodeReviewOrchestratorStructuredResult(result.StructuredResult)
		if !ok || strings.TrimSpace(state.ThreadID) == "" {
			raw := "orchestrator result is missing its thread id"
			if _, err := stores.CodeReviews.UpdateAgentResultOutcome(ctx, job.OrgID, result.ID, models.CodeReviewAgentResultStatusFailed, &raw, result.StructuredResult); err != nil {
				return fmt.Errorf("mark malformed orchestrator result failed: %w", err)
			}
			continue
		}
		threadID, err := uuid.Parse(state.ThreadID)
		if err != nil {
			raw := "orchestrator result has an invalid thread id: " + err.Error()
			if _, err := stores.CodeReviews.UpdateAgentResultOutcome(ctx, job.OrgID, result.ID, models.CodeReviewAgentResultStatusFailed, &raw, result.StructuredResult); err != nil {
				return fmt.Errorf("mark invalid orchestrator result failed: %w", err)
			}
			continue
		}
		if timedOut {
			raw := "orchestrator timed out before producing a completed turn"
			state.Error = raw
			if thread, cancelErr := cancelCodeReviewThread(ctx, stores, logger, job, threadID); cancelErr == nil {
				state.CostCents = thread.CostCents
			} else {
				logger.Warn().Err(cancelErr).Str("thread_id", threadID.String()).Msg("failed to cancel timed-out code review orchestrator thread")
			}
			if _, err := stores.CodeReviews.UpdateAgentResultOutcome(ctx, job.OrgID, result.ID, models.CodeReviewAgentResultStatusTimedOut, &raw, marshalCodeReviewOrchestratorStructuredResult(state)); err != nil {
				return fmt.Errorf("mark orchestrator timed out: %w", err)
			}
			continue
		}
		thread, err := stores.SessionThreads.GetByID(ctx, job.OrgID, threadID)
		if err != nil {
			return fmt.Errorf("load code review orchestrator thread: %w", err)
		}
		state.CostCents = thread.CostCents
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
				rawOutput, rawArtifactKey, err := codeReviewRawOutputForStorage(ctx, stores, job, result.ID, models.CodeReviewAgentRoleOrchestrator, result.AgentProvider, raw)
				if err != nil {
					return err
				}
				state.RawArtifactKey = rawArtifactKey
				if _, err := stores.CodeReviews.UpdateAgentResultOutcome(ctx, job.OrgID, result.ID, models.CodeReviewAgentResultStatusFailed, rawOutput, marshalCodeReviewOrchestratorStructuredResult(state)); err != nil {
					return fmt.Errorf("mark orchestrator failed: %w", err)
				}
			}
			continue
		}
		synthesis := parseCodeReviewOrchestratorSynthesis(raw)
		findings := parseCodeReviewFindings(raw, changedPaths)
		for i := range findings {
			findings[i].OrgID = job.OrgID
			findings[i].SessionID = job.SessionID
			findings[i].AgentResultID = &result.ID
			if err := stores.CodeReviews.ReplaceFinding(ctx, &findings[i]); err != nil {
				return fmt.Errorf("create harvested orchestrator code review finding: %w", err)
			}
		}
		state.Synthesis = synthesis
		state.FindingCount = len(findings)
		state.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		rawOutput, rawArtifactKey, err := codeReviewRawOutputForStorage(ctx, stores, job, result.ID, models.CodeReviewAgentRoleOrchestrator, result.AgentProvider, raw)
		if err != nil {
			return err
		}
		state.RawArtifactKey = rawArtifactKey
		if _, err := stores.CodeReviews.UpdateAgentResultOutcome(ctx, job.OrgID, result.ID, models.CodeReviewAgentResultStatusCompleted, rawOutput, marshalCodeReviewOrchestratorStructuredResult(state)); err != nil {
			return fmt.Errorf("mark orchestrator completed: %w", err)
		}
	}
	return nil
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
}

type codeReviewRequestedReviewerRemover interface {
	RemoveRequestedReviewers(ctx context.Context, req codereviewsvc.RequestedReviewersRequest) error
}

type liveCodeReviewOutcomeInput struct {
	Policy                 models.CodeReviewPolicyConfig
	Job                    runCodeReviewPayload
	SessionURL             string
	PullRequest            models.PullRequest
	Health                 *models.PullRequestHealthResponse
	AgentResults           []models.CodeReviewAgentResult
	Findings               []models.CodeReviewFinding
	ChangedFiles           []codereviewsvc.PullRequestFile
	ChangedFilesAvailable  bool
	DescriptionEvaluation  codeReviewDescriptionEvaluation
	ReviewContext          *codereviewsvc.ReviewContext
	ReviewContextChecked   bool
	ReviewContextAvailable bool
	OrchestratorSynthesis  codeReviewOrchestratorSynthesis
}

func evaluateLiveCodeReviewOutcome(input liveCodeReviewOutcomeInput) (models.CodeReviewDecisionEvaluation, string) {
	policy := models.ResolveCodeReviewPolicyConfig(&input.Policy)
	reviewerQuorum, _ := codeReviewReviewerEvidence(input.AgentResults)
	reviewerQuorumWaived := reviewerQuorum < policy.AgentRoster.RequireReviewerQuorum && codeReviewLowRiskQuorumWaived(policy, input.ChangedFiles)
	blockingFindings := codeReviewBlockingFindings(input.Findings)
	descriptionPassed := input.DescriptionEvaluation.Passed
	if len(input.DescriptionEvaluation.RequirementSummaries) == 0 {
		descriptionPassed = codeReviewDescriptionPassed(policy, input.PullRequest, input.ChangedFiles)
	}
	reviewContextFetchFailed := input.ReviewContextChecked && !input.ReviewContextAvailable
	unresolvedHumanThreads := codeReviewUnresolvedHumanThreads(input.PullRequest)
	if input.ReviewContext != nil {
		unresolvedHumanThreads += input.ReviewContext.UnresolvedHumanThreads + input.ReviewContext.BlockingHumanReviews
	}
	risk := models.EvaluateCodeReviewRisk(policy, models.CodeReviewRiskInput{
		FilesChanged:           len(input.ChangedFiles),
		LinesChanged:           codeReviewLinesChanged(input.ChangedFiles),
		ChangedPaths:           codeReviewChangedPaths(input.ChangedFiles),
		Categories:             codeReviewChangedCategories(input.ChangedFiles),
		ChecksPassing:          codeReviewChecksPassing(policy, input.Health),
		RequiredChecksPassing:  codeReviewRequiredChecksPassing(policy, input.Health),
		DescriptionPassed:      descriptionPassed,
		UpToDate:               codeReviewUpToDate(input.Health),
		Author:                 codeReviewAuthor(input.Job, input.PullRequest),
		AuthorClass:            codeReviewAuthorClass(input.PullRequest),
		FromFork:               input.Job.FromFork,
		ContextFetchFailed:     input.Health == nil || !input.ChangedFilesAvailable || reviewContextFetchFailed,
		HeadSHAChanged:         codeReviewHeadChanged(input.Job.HeadSHA, input.PullRequest, input.Health),
		BlockingFindings:       blockingFindings,
		ReviewerDisagreement:   input.OrchestratorSynthesis.ReviewerDisagreement,
		UnresolvedHumanThreads: unresolvedHumanThreads,
		ScopeMismatch:          input.OrchestratorSynthesis.ScopeMismatch,
		UnresolvedUncertainty:  input.OrchestratorSynthesis.UnresolvedUncertainty,
		PromptInjectionFound:   input.DescriptionEvaluation.PromptInjectionFound || input.OrchestratorSynthesis.PromptInjectionDetected,
	})
	if reviewerQuorum < policy.AgentRoster.RequireReviewerQuorum && !reviewerQuorumWaived {
		risk.Acceptable = false
		risk.Reasons = append(risk.Reasons, fmt.Sprintf("reviewer quorum %d is below policy requirement %d", reviewerQuorum, policy.AgentRoster.RequireReviewerQuorum))
	}
	decision := models.EvaluateCodeReviewDecision(policy, risk)
	body := models.BuildCodeReviewFinalReviewBody(models.CodeReviewFinalReviewInput{
		Decision:                  decision.Decision,
		Acceptable:                decision.Acceptable,
		RiskReasons:               decision.RiskReasons,
		SessionURL:                input.SessionURL,
		DescriptionPassed:         &descriptionPassed,
		DescriptionIssues:         codeReviewFailedDescriptionRequirements(input.DescriptionEvaluation.RequirementSummaries),
		AgentSummaries:            codeReviewAgentSummaries(input.AgentResults, input.Findings),
		Findings:                  input.Findings,
		RecommendedHumanReviewers: codeReviewRecommendedHumanReviewers(decision.RiskReasons, input.ChangedFiles),
		ChangeStatsAvailable:      input.ChangedFilesAvailable,
		FilesChanged:              len(input.ChangedFiles),
		LinesChanged:              codeReviewLinesChanged(input.ChangedFiles),
		ChecksRequired:            policy.RiskPolicy.RequirePassingChecks || len(policy.RiskPolicy.RequiredChecks) > 0,
		ReviewerQuorum:            reviewerQuorum,
		RequiredReviewerQuorum:    policy.AgentRoster.RequireReviewerQuorum,
		ReviewerQuorumWaived:      reviewerQuorumWaived,
	})
	return decision, body
}

type codeReviewFileLister interface {
	ListPullRequestFiles(ctx context.Context, req codereviewsvc.PullRequestFilesRequest) ([]codereviewsvc.PullRequestFile, error)
}

type codeReviewContextLister interface {
	ListReviewContext(ctx context.Context, req codereviewsvc.ReviewContextRequest) (codereviewsvc.ReviewContext, error)
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
		return nil, false, err
	}
	return files, true, nil
}

func loadCodeReviewReviewContext(ctx context.Context, stores *Stores, services *Services, job runCodeReviewPayload, pr models.PullRequest) (*codereviewsvc.ReviewContext, bool, bool, error) {
	if services == nil || services.CodeReviews == nil {
		return nil, false, false, nil
	}
	lister, ok := services.CodeReviews.(codeReviewContextLister)
	if !ok {
		return nil, false, false, nil
	}
	if stores == nil || stores.Repositories == nil {
		return nil, true, false, fmt.Errorf("repository store is required")
	}
	repo, err := stores.Repositories.GetByID(ctx, job.OrgID, job.RepositoryID)
	if err != nil {
		return nil, true, false, fmt.Errorf("load code review repository: %w", err)
	}
	if repo.InstallationID == 0 {
		return nil, true, false, fmt.Errorf("repository %s has no GitHub installation id", repo.ID)
	}
	repository := strings.TrimSpace(pr.GitHubRepo)
	if repository == "" {
		repository = strings.TrimSpace(repo.FullName)
	}
	context, err := lister.ListReviewContext(ctx, codereviewsvc.ReviewContextRequest{
		InstallationID: repo.InstallationID,
		Repository:     repository,
		PullNumber:     pr.GitHubPRNumber,
		BotLogins:      codeReviewBotLogins(job),
	})
	if err != nil {
		return nil, true, false, err
	}
	return &context, true, true, nil
}

func codeReviewBotLogins(job runCodeReviewPayload) []string {
	logins := []string{"143", "143-code-reviewer", "143 Code Reviewer", "github-actions[bot]"}
	if reviewer := strings.TrimSpace(job.RequestedReviewerLogin); reviewer != "" {
		logins = append(logins, reviewer)
	}
	return compactCodeReviewStrings(logins)
}

func compactCodeReviewStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{})
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
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
		switch finding.Severity {
		case models.CodeReviewFindingSeverityHigh, models.CodeReviewFindingSeverityCritical:
			count++
		}
	}
	return count
}

func codeReviewLinesChanged(files []codereviewsvc.PullRequestFile) int {
	lines := 0
	for _, file := range files {
		lines += file.Additions + file.Deletions
	}
	return lines
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

func codeReviewChangedCategories(files []codereviewsvc.PullRequestFile) []string {
	seen := make(map[string]struct{})
	categories := make([]string, 0)
	for _, file := range files {
		for _, category := range codeReviewPathCategories(file.Filename) {
			if _, ok := seen[category]; ok {
				continue
			}
			seen[category] = struct{}{}
			categories = append(categories, category)
		}
	}
	return categories
}

func codeReviewPathCategories(path string) []string {
	normalized := strings.ToLower(strings.TrimSpace(path))
	categories := make([]string, 0, 2)
	if codeReviewDocsPath(normalized) {
		// Documentation is prose, not code. Returning only "docs" keeps the
		// substring-based code-risk heuristics below (auth/crypto/permissions/
		// etc.) from misclassifying a doc whose filename merely contains a
		// sensitive word, e.g. "111-session-changesets.md" reading as "auth".
		return []string{"docs"}
	}
	if strings.Contains(normalized, "/test/") ||
		strings.Contains(normalized, "/tests/") ||
		strings.Contains(normalized, "/__tests__/") ||
		strings.HasSuffix(normalized, "_test.go") ||
		strings.Contains(normalized, ".test.") ||
		strings.Contains(normalized, ".spec.") {
		categories = append(categories, "tests")
	}
	if codeReviewFrontendPath(normalized) {
		categories = append(categories, "frontend")
	}
	if strings.HasSuffix(normalized, ".go") ||
		strings.Contains(normalized, "internal/") ||
		strings.Contains(normalized, "cmd/") ||
		strings.Contains(normalized, "pkg/") {
		categories = append(categories, "backend")
	}
	if strings.Contains(normalized, "generated") ||
		strings.HasSuffix(normalized, ".pb.go") ||
		strings.HasSuffix(normalized, ".gen.go") ||
		strings.HasSuffix(normalized, ".generated.ts") {
		categories = append(categories, "generated")
	}
	switch {
	case normalized == "go.mod" || normalized == "go.sum" ||
		strings.HasSuffix(normalized, "package-lock.json") ||
		strings.HasSuffix(normalized, "pnpm-lock.yaml") ||
		strings.HasSuffix(normalized, "yarn.lock") ||
		strings.HasSuffix(normalized, "cargo.lock") ||
		strings.HasSuffix(normalized, "poetry.lock") ||
		strings.Contains(normalized, "requirements.txt"):
		categories = append(categories, "dependencies")
	}
	if strings.Contains(normalized, "migration") || strings.Contains(normalized, "/migrations/") {
		categories = append(categories, "migrations")
	}
	if strings.Contains(normalized, "auth") || strings.Contains(normalized, "session") {
		categories = append(categories, "auth")
	}
	if strings.Contains(normalized, "billing") || strings.Contains(normalized, "invoice") || strings.Contains(normalized, "payment") {
		categories = append(categories, "billing")
	}
	if strings.Contains(normalized, "permission") || strings.Contains(normalized, "role") || strings.Contains(normalized, "rbac") {
		categories = append(categories, "permissions")
	}
	if strings.Contains(normalized, "crypto") || strings.Contains(normalized, "secret") || strings.Contains(normalized, "token") {
		categories = append(categories, "crypto")
	}
	if strings.Contains(normalized, ".github/workflows/") || strings.Contains(normalized, "terraform") || strings.Contains(normalized, "deploy") || strings.Contains(normalized, "infra") {
		categories = append(categories, "infra")
	}
	return categories
}

func codeReviewDocsPath(normalized string) bool {
	normalized = strings.ToLower(strings.TrimSpace(normalized))
	return strings.HasPrefix(normalized, "docs/") ||
		strings.HasSuffix(normalized, ".md") ||
		strings.HasSuffix(normalized, ".mdx") ||
		strings.HasSuffix(normalized, ".rst") ||
		strings.HasSuffix(normalized, ".adoc")
}

func codeReviewDescriptionPassed(policy models.CodeReviewPolicyConfig, pr models.PullRequest, changedFiles []codereviewsvc.PullRequestFile) bool {
	body := ""
	if pr.Body != nil {
		body = strings.TrimSpace(*pr.Body)
	}
	for _, requirement := range policy.DescriptionPolicy.Requirements {
		if !requirement.Required {
			continue
		}
		if strings.TrimSpace(requirement.Key) == "" {
			continue
		}
		if !codeReviewDescriptionRequirementApplies(requirement, changedFiles) {
			continue
		}
		if !codeReviewDescriptionRequirementPassed(requirement, body) {
			return false
		}
	}
	return true
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
	case "frontend_or_ui_visible", "frontend", "ui":
		for _, file := range changedFiles {
			path := strings.ToLower(file.Filename)
			if strings.Contains(path, "frontend/") ||
				strings.Contains(path, "app/") ||
				strings.Contains(path, "components/") ||
				strings.Contains(path, "pages/") ||
				strings.Contains(path, ".tsx") ||
				strings.Contains(path, ".jsx") ||
				strings.Contains(path, ".css") {
				return true
			}
		}
		return false
	default:
		return true
	}
}

func codeReviewDescriptionApplicabilityApplies(applicability models.CodeReviewDescriptionApplicability, changedFiles []codereviewsvc.PullRequestFile) bool {
	linesChanged := codeReviewLinesChanged(changedFiles)
	changedPaths := codeReviewChangedPaths(changedFiles)
	changedCategories := codeReviewChangedCategories(changedFiles)
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
	if len(applicability.Categories) > 0 {
		for _, category := range changedCategories {
			if stringInCodeReviewSlice(category, applicability.Categories) {
				return true
			}
		}
	}
	if applicability.RequireTestFilesChanged && codeReviewAnyTestFileChanged(changedPaths) {
		return true
	}
	switch applicability.Kind {
	case "", models.CodeReviewDescriptionApplicabilityAll:
		return true
	case models.CodeReviewDescriptionApplicabilityNontrivial:
		return len(changedFiles) > 1 || linesChanged > 30
	case models.CodeReviewDescriptionApplicabilityFrontend:
		for _, path := range changedPaths {
			if codeReviewFrontendPath(path) {
				return true
			}
		}
		return false
	case models.CodeReviewDescriptionApplicabilityPaths:
		return len(applicability.PathPatterns) == 0
	case models.CodeReviewDescriptionApplicabilityCategories:
		return len(applicability.Categories) == 0
	case models.CodeReviewDescriptionApplicabilityTests:
		return codeReviewAnyTestFileChanged(changedPaths)
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

func stringInCodeReviewSlice(needle string, values []string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(needle), strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

func codeReviewAnyTestFileChanged(paths []string) bool {
	for _, path := range paths {
		normalized := strings.ToLower(filepath.ToSlash(path))
		if strings.Contains(normalized, "/test/") ||
			strings.Contains(normalized, "/tests/") ||
			strings.Contains(normalized, "/__tests__/") ||
			strings.Contains(normalized, "/fixtures/") ||
			strings.Contains(normalized, "/testdata/") ||
			strings.HasSuffix(normalized, "_test.go") ||
			strings.Contains(normalized, ".test.") ||
			strings.Contains(normalized, ".spec.") {
			return true
		}
	}
	return false
}

func codeReviewFrontendPath(path string) bool {
	path = strings.ToLower(filepath.ToSlash(path))
	return strings.Contains(path, "frontend/") ||
		strings.Contains(path, "apps/web/") ||
		strings.Contains(path, "/app/") ||
		strings.Contains(path, "/components/") ||
		strings.Contains(path, "/pages/") ||
		strings.HasSuffix(path, ".tsx") ||
		strings.HasSuffix(path, ".jsx") ||
		strings.HasSuffix(path, ".css")
}

func codeReviewDescriptionRequirementPassed(requirement models.CodeReviewDescriptionRequirement, body string) bool {
	if strings.TrimSpace(body) == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(requirement.Key)) {
	case "description", "summary", "intent":
		return len([]rune(body)) >= 20
	case "testing", "tests", "validation":
		if containsAnyFold(body, []string{"not run", "not tested", "tests not run", "did not run", "not applicable", "n/a"}) {
			return false
		}
		return containsAnyFold(body, []string{"test", "tested", "testing", "go test", "npm test", "vitest", "verified", "validation"})
	case "ui_evidence", "screenshots", "screenshot", "preview":
		return containsAnyFold(body, []string{"screenshot", "screen shot", "preview", "image", "video", "loom", "recording", "http://", "https://", "![", ".png", ".jpg", ".gif"})
	default:
		return true
	}
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

func codeReviewUnresolvedHumanThreads(pr models.PullRequest) int {
	if pr.ReviewStatus == models.PullRequestReviewStatusChangesRequested {
		return 1
	}
	return 0
}

func ensureCodeReviewInlineSelection(ctx context.Context, store *db.CodeReviewStore, job runCodeReviewPayload, findings []models.CodeReviewFinding, changedFiles []codereviewsvc.PullRequestFile, limit int) error {
	if store == nil || len(findings) == 0 {
		return nil
	}
	for _, finding := range findings {
		if finding.SelectedForInline {
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
	codeReviewDirectivePattern = regexp.MustCompile(`::code-comment\{([^}]*)\}`)
	codeReviewAttributePattern = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)=("(?:\\.|[^"\\])*"|[^\s}]+)`)
	codeReviewPriorityPattern  = regexp.MustCompile(`(?i)\[P([0-3])\]`)
	codeReviewDiffHunkPattern  = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
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

func codeReviewRecommendedHumanReviewers(reasons []string, changedFiles []codereviewsvc.PullRequestFile) []string {
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
		lower := strings.ToLower(reason)
		switch {
		case strings.Contains(lower, "auth") || strings.Contains(lower, "permission") || strings.Contains(lower, "secret") || strings.Contains(lower, "crypto"):
			add("security/platform")
		case strings.Contains(lower, "billing") || strings.Contains(lower, "invoice") || strings.Contains(lower, "payment"):
			add("billing")
		case strings.Contains(lower, "infra") || strings.Contains(lower, "workflow") || strings.Contains(lower, "deploy"):
			add("platform")
		case strings.Contains(lower, "migration") || strings.Contains(lower, "database"):
			add("backend/platform")
		}
	}
	for _, category := range codeReviewChangedCategories(changedFiles) {
		switch category {
		case "auth", "permissions", "crypto":
			add("security/platform")
		case "billing":
			add("billing")
		case "infra":
			add("platform")
		case "migrations":
			add("backend/platform")
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
	result, err := services.CodeReviews.SubmitReview(ctx, codereviewsvc.SubmitReviewRequest{
		InstallationID: repo.InstallationID,
		Repository:     repository,
		PullNumber:     pr.GitHubPRNumber,
		HeadSHA:        job.HeadSHA,
		OutputKey:      job.OutputKey,
		Decision:       codeReviewSubmitDecision(decision),
		Body:           body,
		Comments:       comments,
	})
	if err != nil {
		return codeReviewSubmission{}, false, fmt.Errorf("submit code review to GitHub: %w", err)
	}
	if _, err := stores.CodeReviews.RecordGitHubReview(ctx, job.OrgID, job.SessionID, result.ID, result.URL, body); err != nil {
		return codeReviewSubmission{}, true, fmt.Errorf("record submitted code review: %w", err)
	}
	markPostedCodeReviewFindings(ctx, stores.CodeReviews, job.OrgID, findings, result.Comments)
	return codeReviewSubmission{
		GitHubReviewID:  &result.ID,
		GitHubReviewURL: &result.URL,
	}, true, nil
}

func codeReviewSubmitDecision(decision models.CodeReviewDecision) codereviewsvc.SubmitReviewDecision {
	if decision == models.CodeReviewDecisionApproved {
		return codereviewsvc.SubmitReviewDecisionApproved
	}
	return codereviewsvc.SubmitReviewDecisionCommentOnly
}

func codeReviewInlineComments(findings []models.CodeReviewFinding) []codereviewsvc.SubmitReviewComment {
	comments := make([]codereviewsvc.SubmitReviewComment, 0, len(findings))
	for _, finding := range findings {
		if finding.GitHubCommentID != nil {
			continue
		}
		if finding.Path == nil || strings.TrimSpace(*finding.Path) == "" || finding.StartLine == nil || *finding.StartLine <= 0 {
			continue
		}
		body := strings.TrimSpace(finding.Body)
		if body == "" {
			body = strings.TrimSpace(finding.Summary)
		}
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

func markPostedCodeReviewFindings(ctx context.Context, store *db.CodeReviewStore, orgID uuid.UUID, findings []models.CodeReviewFinding, posted []codereviewsvc.SubmitReviewPostedComment) {
	if store == nil || len(findings) == 0 || len(posted) == 0 {
		return
	}
	used := make(map[int]struct{})
	for _, finding := range findings {
		if finding.ID == uuid.Nil || finding.GitHubCommentID != nil || finding.Path == nil || finding.StartLine == nil {
			continue
		}
		body := strings.TrimSpace(finding.Body)
		if body == "" {
			body = strings.TrimSpace(finding.Summary)
		}
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
