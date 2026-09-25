package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

type codeReviewRecheckJob struct {
	OrgID        uuid.UUID `json:"org_id"`
	AssessmentID uuid.UUID `json:"assessment_id"`
}

type codeReviewAssessmentFallback interface {
	FallbackAssessmentToFull(context.Context, uuid.UUID, uuid.UUID, string) error
}

type codeReviewEvidenceAssessmentRefresher interface {
	RefreshUnsentEvidenceAssessment(context.Context, uuid.UUID, uuid.UUID, string) error
}

func refreshUnsentCodeReviewEvidence(ctx context.Context, services *Services, a models.CodeReviewAssessment, reason string) error {
	refresher, ok := services.CodeReviewLifecycle.(codeReviewEvidenceAssessmentRefresher)
	if !ok {
		return errors.New("code review evidence refresh unavailable")
	}
	return refresher.RefreshUnsentEvidenceAssessment(ctx, a.OrgID, a.ID, reason)
}

var errCodeReviewUnsentInputsChanged = errors.New("assessment inputs changed before publication send")
var errCodeReviewPublicationSuperseded = errors.New("assessment publication superseded and full fallback queued")

// This supervisor reuses only immutable full-review evidence. The durable
// dispatch store owns sends and exact-turn receipts; it never harvests the
// latest assistant message or the cumulative cost of a reused thread.
func newRunCodeReviewRecheckHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, _ string, raw json.RawMessage) error {
		if stores == nil || stores.CodeReviewAssessments == nil || stores.CodeReviewRechecks == nil || services == nil || services.CodeReviewInputCapture == nil {
			return errors.New("code review recheck dependencies unavailable")
		}
		var job codeReviewRecheckJob
		if err := json.Unmarshal(raw, &job); err != nil {
			return err
		}
		if job.OrgID == uuid.Nil || job.AssessmentID == uuid.Nil {
			return errors.New("org_id and assessment_id are required")
		}
		a, err := stores.CodeReviewAssessments.GetByID(ctx, job.OrgID, job.AssessmentID)
		if err != nil {
			return err
		}
		jobctx.RegisterDeadLetterHook(ctx, func(hookCtx context.Context, deadLetterErr error) {
			current, loadErr := stores.CodeReviewAssessments.GetByID(hookCtx, a.OrgID, a.ID)
			if loadErr != nil {
				logger.Warn().Err(loadErr).Str("assessment_id", a.ID.String()).Msg("load dead-lettered assessment failed")
				return
			}
			if current.Status != models.CodeReviewAssessmentRunning && current.Status != models.CodeReviewAssessmentReserved {
				return
			}
			if failErr := stores.CodeReviewAssessments.Fail(hookCtx, current.OrgID, current.ID, current.Generation, current.InputDigest, codeReviewDeadLetterReason(deadLetterErr)); failErr != nil {
				logger.Warn().Err(failErr).Str("assessment_id", a.ID.String()).Msg("reconcile dead-lettered assessment failed")
				return
			}
			if settleErr := settleCodeReviewAssessment(hookCtx, stores, current); settleErr != nil {
				logger.Warn().Err(settleErr).Str("assessment_id", a.ID.String()).Msg("settle dead-lettered assessment failed")
			}
			stores.CodeReviews.PublishAssessmentUpdated(hookCtx, current)
		})
		if a.ReviewScope != models.CodeReviewScopeEvidenceOnly || a.SourceAssessmentID == nil {
			return errors.New("recheck requires an evidence-only assessment and full baseline")
		}
		if a.Status == models.CodeReviewAssessmentSuperseded && a.FailureDetail != nil && strings.HasPrefix(*a.FailureDetail, "full_review:") {
			if err := settleCodeReviewAssessment(ctx, stores, a); err != nil {
				return err
			}
			return queueCodeReviewRecheckFallback(ctx, services, a, strings.TrimPrefix(*a.FailureDetail, "full_review:"))
		}
		if a.Status == models.CodeReviewAssessmentSuperseded && a.FailureDetail != nil && strings.HasPrefix(*a.FailureDetail, "evidence_recheck:") {
			return refreshUnsentCodeReviewEvidence(ctx, services, a, strings.TrimPrefix(*a.FailureDetail, "evidence_recheck:"))
		}
		if a.Status == models.CodeReviewAssessmentCompleted || a.Status == models.CodeReviewAssessmentCancelled || a.Status == models.CodeReviewAssessmentSuperseded {
			return settleCodeReviewAssessment(ctx, stores, a)
		}
		if a.Status == models.CodeReviewAssessmentFailed {
			if err := settleCodeReviewAssessment(ctx, stores, a); err != nil {
				return err
			}
			if a.FailureDetail != nil && strings.HasPrefix(*a.FailureDetail, "full_review:") {
				return queueCodeReviewRecheckFallback(ctx, services, a, strings.TrimPrefix(*a.FailureDetail, "full_review:"))
			}
			return nil
		}
		if a.Status == models.CodeReviewAssessmentPublishing || a.ResultOrigin != nil {
			if paused, pauseErr := pauseExpiredCodeReviewPublication(ctx, stores, a); paused || pauseErr != nil {
				return pauseErr
			}
			return resumeCodeReviewRecheckPublication(ctx, stores, services, a)
		}
		if a.Status == models.CodeReviewAssessmentReserved {
			if err = stores.CodeReviewAssessments.MarkRunning(ctx, a.OrgID, a.ID, a.Generation, a.InputDigest); err != nil {
				return err
			}
			a.Status = models.CodeReviewAssessmentRunning
		}
		// A running continuation can take minutes. Poll its exact queue receipt
		// without recapturing provider evidence or downloading images every wake.
		dispatch, dispatchErr := stores.CodeReviewRechecks.Get(ctx, a.OrgID, a.ID)
		if dispatchErr != nil && !errors.Is(dispatchErr, pgx.ErrNoRows) {
			return dispatchErr
		}
		if dispatchErr == nil {
			switch dispatch.Status {
			case models.CodeReviewRecheckDispatchPending, models.CodeReviewRecheckDispatchRunning:
				terminal, reconcileErr := stores.CodeReviewRechecks.FailTerminalJob(ctx, a.OrgID, a.ID, "bound continuation job ended without an exact turn receipt")
				if reconcileErr != nil {
					return reconcileErr
				}
				if terminal {
					terminalDispatch, loadErr := stores.CodeReviewRechecks.Get(ctx, a.OrgID, a.ID)
					if loadErr != nil {
						return loadErr
					}
					if terminalDispatch.Status == models.CodeReviewRecheckDispatchCancelled {
						return settleCodeReviewAssessment(ctx, stores, a)
					}
					return failCodeReviewRecheck(ctx, stores, services, a, "bound continuation job ended without an exact turn receipt", false)
				}
				policy, policyErr := stores.CodeReviews.GetPolicyByID(ctx, a.OrgID, a.PolicyID)
				if policyErr != nil {
					return policyErr
				}
				if time.Now().After(codeReviewAgentDeadline(policy.Config(), dispatch.CreatedAt)) {
					return failCodeReviewRecheck(ctx, stores, services, a, "orchestrator continuation exceeded review deadline", false)
				}
				return codeReviewWaitingForOrchestrator(policy.Config())
			case models.CodeReviewRecheckDispatchFailed:
				return failCodeReviewRecheck(ctx, stores, services, a, "orchestrator continuation failed", false)
			case models.CodeReviewRecheckDispatchCancelled:
				return settleCodeReviewAssessment(ctx, stores, a)
			}
		}
		baseline, err := stores.CodeReviewAssessments.GetByID(ctx, a.OrgID, *a.SourceAssessmentID)
		if err != nil {
			return err
		}
		if baseline.Status != models.CodeReviewAssessmentCompleted || baseline.ReviewScope != models.CodeReviewScopeFull || !baseline.CoverageComplete || baseline.SessionID != a.SessionID || baseline.PullRequestID != a.PullRequestID || baseline.RepositoryID != a.RepositoryID {
			return failCodeReviewRecheck(ctx, stores, services, a, "incomplete baseline", true)
		}
		var baselineManifest codereviewsvc.ReviewInputManifest
		if err = json.Unmarshal(baseline.InputManifest, &baselineManifest); err != nil {
			return failCodeReviewRecheck(ctx, stores, services, a, "baseline inputs unavailable", true)
		}
		if err = codereviewsvc.ValidateReviewInputManifest(baselineManifest); err != nil {
			return failCodeReviewRecheck(ctx, stores, services, a, "baseline inputs invalid", true)
		}
		results, err := stores.CodeReviewAssessments.ListAgentResults(ctx, a.OrgID, baseline.ID)
		if err != nil {
			return err
		}
		findings, err := stores.CodeReviewAssessments.ListFindings(ctx, a.OrgID, baseline.ID)
		if err != nil {
			return err
		}
		synthesis, threadID, err := recheckBaselineSynthesis(results)
		if err != nil {
			return failCodeReviewRecheck(ctx, stores, services, a, err.Error(), true)
		}
		var manifest codereviewsvc.ReviewInputManifest
		if err = json.Unmarshal(a.InputManifest, &manifest); err != nil {
			return err
		}
		if err = codereviewsvc.ValidateReviewInputManifest(manifest); err != nil {
			return failCodeReviewRecheck(ctx, stores, services, a, err.Error(), false)
		}
		requestContext := &codereviewsvc.ReviewRequestContext{Body: manifest.Request.SubstantiveText}
		capture, err := services.CodeReviewInputCapture.CaptureAssessmentInputs(ctx, codereviewsvc.AssessmentInputCaptureRequest{OrgID: a.OrgID, RepositoryID: a.RepositoryID, PullRequestID: a.PullRequestID, SessionID: a.SessionID, AssessmentID: a.ID, Fresh: true, RequestContext: requestContext})
		if err != nil {
			if errors.Is(err, codereviewsvc.ErrAssessmentReuseUnavailable) {
				return failCodeReviewRecheck(ctx, stores, services, a, "assessment inputs cannot establish reusable coverage", false)
			}
			return err
		}
		if capture.Manifest.InputDigest != a.InputDigest {
			return refreshUnsentCodeReviewEvidence(ctx, services, a, "inputs changed before evidence assessment")
		}
		if codereviewsvc.RecheckBaselineChange(capture.Manifest, baselineManifest) != "" {
			return failCodeReviewRecheck(ctx, stores, services, a, "baseline code or review policy changed", true)
		}
		requirements, err := recheckRequirements(capture.Policy.Config(), capture.Files, synthesis)
		if err != nil {
			return failCodeReviewRecheck(ctx, stores, services, a, err.Error(), true)
		}
		if errors.Is(dispatchErr, pgx.ErrNoRows) {
			if !services.CodeReviewRechecksEnabled || !capture.Policy.Config().ContinuationPolicy.Enabled {
				return failCodeReviewRecheck(ctx, stores, services, a, "continuation disabled", false)
			}
			thread, loadErr := stores.SessionThreads.GetByID(ctx, a.OrgID, threadID)
			if loadErr != nil {
				return loadErr
			}
			if thread.SessionID != a.SessionID {
				return failCodeReviewRecheck(ctx, stores, services, a, "orchestrator thread does not belong to baseline", true)
			}
			records, loadErr := stores.CodeReviewAssessments.ListPromptRecords(ctx, a.OrgID, baseline.ID)
			if loadErr != nil {
				return loadErr
			}
			completeResults, hydrateErr := recheckHydrateBaselineResults(baseline, results, records)
			if hydrateErr != nil {
				return failCodeReviewRecheck(ctx, stores, services, a, hydrateErr.Error(), true)
			}
			baselineContext, encodeErr := json.Marshal(struct {
				Synthesis         codeReviewOrchestratorSynthesis
				Results           []models.CodeReviewAgentResult
				Findings          []models.CodeReviewFinding
				PriorTextEvidence codereviewsvc.ReviewTextInput
				PriorImages       []codereviewsvc.ReviewVisualImage
			}{synthesis, completeResults, findings, baselineManifest.TextEvidence, baselineManifest.Visual.Images})
			if encodeErr != nil {
				return encodeErr
			}
			requirementJSON, encodeErr := json.Marshal(requirements)
			if encodeErr != nil {
				return encodeErr
			}
			currentContextJSON, encodeErr := json.Marshal(struct {
				Title   string                           `json:"title"`
				Request codereviewsvc.ReviewRequestInput `json:"request"`
			}{capture.Manifest.Title, capture.Manifest.Request})
			if encodeErr != nil {
				return encodeErr
			}
			textEvidenceJSON, encodeErr := json.Marshal(capture.Manifest.TextEvidence)
			if encodeErr != nil {
				return encodeErr
			}
			prompt := prompts.CodeReviewRecheckPrompt(prompts.CodeReviewRecheckPromptData{BaselineID: baseline.ID.String(), InputDigest: a.InputDigest, Baseline: string(baselineContext), Requirements: string(requirementJSON), CurrentContext: string(currentContextJSON), TextEvidence: string(textEvidenceJSON), VisualEvidence: codeReviewVisualEvidenceForPrompt(capture.VisualEvidence)})
			if len(prompt) > 128*1024 {
				return failCodeReviewRecheck(ctx, stores, services, a, "baseline exceeds recheck context budget", false)
			}
			dispatch, _, err = stores.CodeReviewRechecks.Dispatch(ctx, models.CodeReviewRecheckDispatchInput{OrgID: a.OrgID, RepositoryID: a.RepositoryID, PullRequestID: a.PullRequestID, AssessmentID: a.ID, SessionID: a.SessionID, ThreadID: threadID, ExpectedTurn: thread.CurrentTurn + 1, Prompt: prompt, ImageURLs: codeReviewVisualEvidenceImages(capture.VisualEvidence)})
			if err != nil {
				return err
			}
		}
		if dispatch.Status != "completed" || dispatch.ResultMessageID == nil {
			return codeReviewWaitingForOrchestrator(capture.Policy.Config())
		}
		message, err := stores.SessionMessages.GetByID(ctx, a.OrgID, *dispatch.ResultMessageID)
		if err != nil {
			return err
		}
		if message.ThreadID == nil || *message.ThreadID != dispatch.ThreadID || message.SessionID != a.SessionID || message.TurnNumber != dispatch.ExpectedTurn || message.Role != models.MessageRoleAssistant {
			return failCodeReviewRecheck(ctx, stores, services, a, "recheck result does not match dispatched turn", false)
		}
		validated, err := validateCodeReviewRecheckResponse(codeReviewRecheckValidationInput{Raw: message.Content, BaselineID: baseline.ID, InputDigest: a.InputDigest, Requirements: requirements, BaselineSynthesis: synthesis, BaselineFindings: findings, BaselineManifest: baselineManifest, CurrentManifest: capture.Manifest, VisualEvidence: capture.VisualEvidence})
		if err != nil {
			return failCodeReviewRecheck(ctx, stores, services, a, "invalid evidence response: "+err.Error(), false)
		}
		if validated.EscalationReason != "" {
			return failCodeReviewRecheck(ctx, stores, services, a, "evidence recheck needs human review: "+validated.EscalationReason, false)
		}
		// Fresh capture rediscovers sources and downloads bytes without restoring
		// or replacing this assessment's immutable evidence checkpoint.
		fresh, err := services.CodeReviewInputCapture.CaptureAssessmentInputs(ctx, codereviewsvc.AssessmentInputCaptureRequest{OrgID: a.OrgID, RepositoryID: a.RepositoryID, PullRequestID: a.PullRequestID, SessionID: a.SessionID, AssessmentID: a.ID, Fresh: true, RequestContext: requestContext})
		if err != nil {
			if errors.Is(err, codereviewsvc.ErrAssessmentReuseUnavailable) {
				return failCodeReviewRecheck(ctx, stores, services, a, "assessment inputs cannot establish reusable coverage", false)
			}
			return err
		}
		if fresh.Manifest.InputDigest != a.InputDigest {
			return refreshUnsentCodeReviewEvidence(ctx, services, a, "inputs changed during evidence assessment")
		}
		payload := runCodeReviewPayload{OrgID: a.OrgID, SessionID: a.SessionID, RepositoryID: a.RepositoryID, PullRequestID: a.PullRequestID, PolicyID: a.PolicyID, HeadSHA: a.HeadSHA, OutputKey: a.PublicationKey, FromFork: fresh.Snapshot.FromFork, PullRequestAuthor: fresh.Snapshot.AuthorLogin, RequestContext: requestContext}
		health, err := loadStoredCodeReviewHealth(ctx, stores, payload, fresh.PullRequest)
		if err != nil {
			return err
		}
		payload.PullRequestAuthorTeams, err = resolveCodeReviewAuthorTeams(ctx, stores, services, fresh.Policy.Config(), payload, fresh.PullRequest)
		if err != nil {
			return err
		}
		if _, err = codeReviewDescriptionEvaluationFromSynthesis(fresh.Policy.Config(), fresh.Files, validated.Synthesis, fresh.VisualEvidence); err != nil {
			return failCodeReviewRecheck(ctx, stores, services, a, "invalid merged evidence assessment: "+err.Error(), false)
		}
		decision, body := evaluateLiveCodeReviewOutcome(liveCodeReviewOutcomeInput{
			Policy:                fresh.Policy.Config(),
			Job:                   payload,
			PullRequest:           fresh.PullRequest,
			Health:                health,
			AgentResults:          results,
			Findings:              validated.EffectiveFindings,
			ChangedFiles:          fresh.Files,
			ChangedFilesAvailable: true,
			OrchestratorSynthesis: validated.Synthesis,
			VisualEvidence:        fresh.VisualEvidence,
			AssessedAt:            time.Now().UTC(),
			SessionURL:            codeReviewAssessmentURL(services.FrontendURL, a.ID),
			PolicySettingsURL:     codeReviewPolicySettingsURL(services.FrontendURL),
			EvidenceRecheckURL:    codeReviewEvidenceRecheckURL(services, fresh.Policy.Config(), a.ID, true),
			ReviewProvenance:      "Code review reused from assessment `" + baseline.ID.String() + "`. Updated evidence was checked in this assessment.",
		})
		outcome, err := json.Marshal(struct {
			Synthesis                codeReviewOrchestratorSynthesis            `json:"synthesis"`
			SourceAssessmentID       uuid.UUID                                  `json:"source_assessment_id"`
			Dispatch                 models.CodeReviewRecheckDispatch           `json:"execution"`
			RequirementReassessments []models.CodeReviewRequirementReassessment `json:"requirement_reassessments"`
			FindingReassessments     []models.CodeReviewFindingReassessment     `json:"finding_reassessments"`
		}{validated.Synthesis, baseline.ID, dispatch, validated.RequirementReassessments, validated.FindingReassessments})
		if err != nil {
			return err
		}
		reasons, err := json.Marshal(decision.RiskReasonDetails)
		if err != nil {
			return err
		}
		if err = publishCodeReviewRecheck(ctx, stores, services, a, fresh.PullRequest, decision.Decision, body, models.CodeReviewAssessmentCompletion{ResultOrigin: models.CodeReviewResultEvidenceOnly, CoverageComplete: true, Decision: decision.Decision, Acceptable: decision.Acceptable, RiskReasonDetails: reasons, StructuredOutcome: outcome, RenderedBody: body}); err != nil {
			if errors.Is(err, errCodeReviewPublicationSuperseded) {
				return nil
			}
			return err
		}
		a.Decision = &decision.Decision
		stores.CodeReviews.PublishAssessmentUpdated(ctx, a)
		logger.Info().Str("assessment_id", a.ID.String()).Str("source_assessment_id", baseline.ID.String()).Str("decision", string(decision.Decision)).Msg("completed evidence-only code review")
		return nil
	}
}

func recheckBaselineSynthesis(results []models.CodeReviewAgentResult) (codeReviewOrchestratorSynthesis, uuid.UUID, error) {
	for _, result := range results {
		if result.Role != models.CodeReviewAgentRoleOrchestrator || result.Status != models.CodeReviewAgentResultStatusCompleted {
			continue
		}
		state, ok := parseCodeReviewOrchestratorStructuredResult(result.StructuredResult)
		if !ok || !state.SynthesisValidated || !state.ReadOnly || state.ReadOnlyViolation || state.Reverted || state.Error != "" || !codeReviewOrchestratorSynthesisUsable(state.Synthesis) {
			continue
		}
		id, err := uuid.Parse(state.ThreadID)
		if err != nil || id == uuid.Nil {
			continue
		}
		return state.Synthesis, id, nil
	}
	return codeReviewOrchestratorSynthesis{}, uuid.Nil, errors.New("missing validated full-review orchestrator evidence")
}

func recheckRequirements(policy models.CodeReviewPolicyConfig, files []codereviewsvc.PullRequestFile, baseline codeReviewOrchestratorSynthesis) ([]models.CodeReviewDescriptionRequirement, error) {
	byKey := make(map[string]codeReviewDescriptionAssessment, len(baseline.DescriptionAssessments))
	for _, a := range baseline.DescriptionAssessments {
		if a.Key == "" || byKey[a.Key].Key != "" {
			return nil, errors.New("ambiguous baseline description assessment")
		}
		byKey[a.Key] = a
	}
	var requirements []models.CodeReviewDescriptionRequirement
	for _, req := range codeReviewApplicableDescriptionRequirements(policy, files) {
		a, ok := byKey[req.Key]
		if !ok {
			return nil, errors.New("incomplete baseline description assessment")
		}
		if a.Status != codeReviewDescriptionAssessmentNotApplicable {
			requirements = append(requirements, req)
		}
	}
	return requirements, nil
}

func failCodeReviewRecheck(ctx context.Context, stores *Stores, services *Services, a models.CodeReviewAssessment, reason string, full bool) error {
	detail := reason
	if full {
		detail = "full_review:" + reason
	}
	if err := stores.CodeReviewAssessments.Fail(ctx, a.OrgID, a.ID, a.Generation, a.InputDigest, detail); err != nil {
		return err
	}
	if err := settleCodeReviewAssessment(ctx, stores, a); err != nil {
		return err
	}
	stores.CodeReviews.PublishAssessmentUpdated(ctx, a)
	if full {
		return queueCodeReviewRecheckFallback(ctx, services, a, reason)
	}
	return nil
}

func queueCodeReviewRecheckFallback(ctx context.Context, services *Services, a models.CodeReviewAssessment, reason string) error {
	s, ok := services.CodeReviewLifecycle.(codeReviewAssessmentFallback)
	if !ok {
		return errors.New("code review full fallback unavailable")
	}
	return s.FallbackAssessmentToFull(ctx, a.OrgID, a.ID, reason)
}

func codeReviewAssessmentURL(base string, id uuid.UUID) string {
	return strings.TrimRight(base, "/") + "/code-reviews?assessment=" + id.String()
}

func settleCodeReviewAssessment(ctx context.Context, stores *Stores, a models.CodeReviewAssessment) error {
	if stores.ThreadSendTx == nil {
		return errors.New("assessment scheduler settlement requires transactions")
	}
	return db.NewCodeReviewScheduleStore(stores.ThreadSendTx).SettleAssessment(ctx, a.OrgID, a.ID)
}

func publishCodeReviewRecheck(ctx context.Context, stores *Stores, services *Services, a models.CodeReviewAssessment, pr models.PullRequest, decision models.CodeReviewDecision, body string, completion models.CodeReviewAssessmentCompletion) error {
	if services.CodeReviews == nil {
		return errors.New("code review publisher unavailable")
	}
	repo, err := stores.Repositories.GetByID(ctx, a.OrgID, a.RepositoryID)
	if err != nil {
		return err
	}
	if a.PublicationState == models.CodeReviewPublicationNotStarted {
		if err = stores.CodeReviewAssessments.StageOutcome(ctx, a.OrgID, a.ID, a.Generation, a.InputDigest, completion); err != nil {
			return err
		}
		if err = stores.CodeReviewAssessments.ReservePublication(ctx, a.OrgID, a.ID, a.Generation, a.InputDigest, a.HeadSHA); err != nil {
			return err
		}
	}
	err = stores.CodeReviews.RunWithGitHubPublicationLock(ctx, a.OrgID, a.PullRequestID, func(lockCtx context.Context, tx db.DBTX) error {
		// Hold the actual job row while performing the existing bounded GitHub
		// publication protocol. A reclaim cannot overlap this publication.
		jobID, ok := jobctx.JobIDFromContext(ctx)
		if !ok {
			return errors.New("recheck publication requires job identity")
		}
		token, ok := jobctx.LockTokenFromContext(ctx)
		if !ok {
			return errors.New("recheck publication requires job lease")
		}
		if err := db.NewCodeReviewStore(tx).LockAssessmentPublicationJob(lockCtx, a.OrgID, jobID, token); err != nil {
			return err
		}
		assessments := db.NewCodeReviewAssessmentStore(tx)
		complete := func() error {
			if err := assessments.Complete(lockCtx, a.OrgID, a.ID, a.Generation, a.InputDigest, completion); err != nil {
				return err
			}
			txStarter, ok := tx.(db.TxStarter)
			if !ok {
				return errors.New("assessment publication transaction cannot settle scheduler")
			}
			if err := db.NewCodeReviewScheduleStore(txStarter).SettleAssessment(lockCtx, a.OrgID, a.ID); err != nil {
				return err
			}
			if _, ok := services.CodeReviews.(codeReviewStatusCommentUpdater); !ok {
				return nil
			}
			key := "code_review_status_comment:assessment:" + a.ID.String()
			_, err := db.NewJobStore(tx).EnqueueWithOpts(lockCtx, a.OrgID, db.EnqueueOpts{Queue: "default", JobType: models.JobTypeSyncCodeReviewStatusComment, Payload: codereviewsvc.SyncReviewStatusCommentJobPayload{OrgID: a.OrgID, SessionID: a.SessionID, RepositoryID: a.RepositoryID, PullRequestID: a.PullRequestID}, Priority: 3, DedupeKey: &key, MaxAttempts: codeReviewStatusCommentJobMaxAttempts})
			return err
		}
		current, err := assessments.GetByID(lockCtx, a.OrgID, a.ID)
		if err != nil {
			return err
		}
		if current.Status == models.CodeReviewAssessmentCompleted {
			return nil
		}
		if current.Status != models.CodeReviewAssessmentPublishing || current.InputDigest != a.InputDigest {
			return db.ErrCodeReviewAssessmentState
		}
		if current.PublicationState == models.CodeReviewPublicationConfirmed {
			return complete()
		}
		request := codereviewsvc.SubmitReviewRequest{RequirePublicationReceipt: true, InstallationID: repo.InstallationID, Repository: repo.FullName, PullNumber: pr.GitHubPRNumber, HeadSHA: a.HeadSHA, OutputKey: a.PublicationKey, Decision: codeReviewSubmitDecision(decision), Body: body}
		if a.PreviousPublishedAssessmentID != nil {
			previous, err := assessments.GetByID(lockCtx, a.OrgID, *a.PreviousPublishedAssessmentID)
			if err != nil {
				return err
			}
			request.PreviousOutputKey = previous.PublicationKey
			request.ExistingReviewID = int64PtrValue(previous.GitHubReviewID)
			request.ExistingReviewURL = stringPtrValue(previous.GitHubReviewURL)
			request.PreviousBody = stringPtrValue(previous.RenderedBody)
			request.PreviousDecision = codeReviewSubmitDecisionPtr(previous.Decision)
			request.PreviousDecidedAt = timePtrValue(previous.CompletedAt)
		}
		var result codereviewsvc.SubmitReviewResult
		allowWrite := false
		inputsChanged := false
		{
			var manifest codereviewsvc.ReviewInputManifest
			if err = json.Unmarshal(current.InputManifest, &manifest); err != nil {
				return err
			}
			// The PR publication lock serializes this check with our other
			// publishers. Re-read after acquiring it: waiting for another
			// publisher must not preserve permission to send stale inputs.
			fresh, captureErr := services.CodeReviewInputCapture.CaptureAssessmentInputs(lockCtx, codereviewsvc.AssessmentInputCaptureRequest{OrgID: a.OrgID, RepositoryID: a.RepositoryID, PullRequestID: a.PullRequestID, SessionID: a.SessionID, AssessmentID: a.ID, Fresh: true, RequestContext: &codereviewsvc.ReviewRequestContext{Body: manifest.Request.SubstantiveText}})
			allowWrite = captureErr == nil && fresh.Manifest.InputDigest == a.InputDigest
			inputsChanged = captureErr == nil && !allowWrite || errors.Is(captureErr, codereviewsvc.ErrAssessmentReuseUnavailable)
			if inputsChanged && current.PublicationState == models.CodeReviewPublicationReserved {
				return errCodeReviewUnsentInputsChanged
			}
		}
		if allowWrite {
			if current.PublicationState == models.CodeReviewPublicationReserved {
				// Commit send intent outside this callback's transaction. A crash
				// after GitHub accepts must never roll the intent back to unsent.
				if err := stores.CodeReviewAssessments.MarkPublicationAttemptUncertain(lockCtx, a.OrgID, a.ID, a.Generation, a.InputDigest); err != nil {
					return err
				}
			}
			result, err = services.CodeReviews.SubmitReview(lockCtx, request)
		} else {
			reconciler, ok := services.CodeReviews.(interface {
				ReconcileAssessmentPublication(context.Context, codereviewsvc.SubmitReviewRequest) (codereviewsvc.SubmitReviewResult, bool, error)
			})
			if !ok {
				return errors.New("assessment publication reconciliation unavailable")
			}
			var found bool
			result, found, err = reconciler.ReconcileAssessmentPublication(lockCtx, request)
			if err == nil && !found {
				if inputsChanged && current.PublicationState == models.CodeReviewPublicationUncertain {
					if noteErr := stores.CodeReviewAssessments.NoteUncertainPublication(lockCtx, current.OrgID, current.ID, current.Generation, current.InputDigest, "review marker not found after input change; pending reconciliation"); noteErr != nil {
						return noteErr
					}
				}
				return codeReviewWaitingForOrchestrator(models.DefaultCodeReviewPolicyConfig())
			}
		}
		if err != nil {
			return err
		}
		receipt, err := json.Marshal(struct {
			OutputKey, CommitSHA, InputDigest string
			SummaryID                         int64
			SummaryURL                        string
			FormalApprovalID                  *int64
			FormalApprovalURL                 *string
		}{a.PublicationKey, a.HeadSHA, a.InputDigest, result.ID, result.URL, result.FormalApprovalID, result.FormalApprovalURL})
		if err != nil {
			return err
		}
		if err = assessments.RecordPublication(lockCtx, a.OrgID, a.ID, a.Generation, a.InputDigest, models.CodeReviewPublicationConfirmed, receipt, &result.ID, &result.URL); err != nil {
			return err
		}
		return complete()
	})
	if errors.Is(err, errCodeReviewUnsentInputsChanged) {
		reason := "inputs changed before publication"
		if refreshErr := refreshUnsentCodeReviewEvidence(ctx, services, a, reason); refreshErr != nil {
			return refreshErr
		}
		return errCodeReviewPublicationSuperseded
	}
	if errors.Is(err, db.ErrCodeReviewPublicationLockBusy) {
		return classifyGitHubJobError(err, a.SessionID.String())
	}
	return err
}

// A reserved publication always consumes its staged result. Mutable evidence
// can revoke permission to retry a write, but cannot rewrite that result.
func resumeCodeReviewRecheckPublication(ctx context.Context, stores *Stores, services *Services, a models.CodeReviewAssessment) error {
	if a.Decision == nil || a.Acceptable == nil || a.RenderedBody == nil || len(a.StructuredOutcome) == 0 {
		return errors.New("reserved publication has no staged outcome")
	}
	completion := models.CodeReviewAssessmentCompletion{ResultOrigin: models.CodeReviewResultEvidenceOnly, CoverageComplete: a.CoverageComplete, Decision: *a.Decision, Acceptable: *a.Acceptable, RiskReasonDetails: a.RiskReasonDetails, StructuredOutcome: a.StructuredOutcome, RenderedBody: *a.RenderedBody}
	pr, err := stores.PullRequests.GetByID(ctx, a.OrgID, a.PullRequestID)
	if err != nil {
		return err
	}
	if err := publishCodeReviewRecheck(ctx, stores, services, a, pr, *a.Decision, *a.RenderedBody, completion); err != nil {
		if errors.Is(err, errCodeReviewPublicationSuperseded) {
			return nil
		}
		return err
	}
	stores.CodeReviews.PublishAssessmentUpdated(ctx, a)
	return nil
}
