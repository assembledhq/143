package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var errFullAssessmentInputsChanged = errors.New("full assessment inputs changed before publication")

// Full reviews may finish after CI changes. Their panel still covers the same
// analysis inputs; live publication safety is checked separately below.
func captureFreshFullAssessment(ctx context.Context, services *Services, job runCodeReviewPayload, assessment models.CodeReviewAssessment) (codereviewsvc.AssessmentInputCaptureResult, error) {
	if services == nil || services.CodeReviewInputCapture == nil {
		return codereviewsvc.AssessmentInputCaptureResult{}, fmt.Errorf("assessment freshness capture unavailable")
	}
	fresh, err := services.CodeReviewInputCapture.CaptureAssessmentInputs(ctx, codereviewsvc.AssessmentInputCaptureRequest{OrgID: job.OrgID, RepositoryID: job.RepositoryID, PullRequestID: job.PullRequestID, SessionID: job.SessionID, AssessmentID: assessment.ID, RequestContext: job.RequestContext, Fresh: true})
	if err != nil {
		return fresh, err
	}
	var baseline codereviewsvc.ReviewInputManifest
	if json.Unmarshal(assessment.InputManifest, &baseline) != nil || baseline.InputDigest != assessment.InputDigest ||
		!fullAssessmentAnalysisUnchanged(baseline, fresh.Manifest) || fresh.Manifest.Code.HeadSHA != assessment.HeadSHA || fresh.Manifest.Code.BaseSHA != assessment.BaseSHA || fresh.Manifest.Code.BaseRef != assessment.BaseRef || fresh.Policy.ID != assessment.PolicyID {
		return fresh, errFullAssessmentInputsChanged
	}
	return fresh, nil
}

func verifyFullAssessmentFreshness(ctx context.Context, services *Services, job runCodeReviewPayload, assessment models.CodeReviewAssessment) error {
	_, err := captureFreshFullAssessment(ctx, services, job, assessment)
	return err
}

func fullAssessmentAnalysisUnchanged(before, after codereviewsvc.ReviewInputManifest) bool {
	if before.InputVersion != codereviewsvc.ReviewInputManifestVersion || before.InputVersion != after.InputVersion || before.ReuseEligible != after.ReuseEligible ||
		before.CodeDigest == "" || before.CodeDigest != after.CodeDigest || before.ContractDigest == "" || before.ContractDigest != after.ContractDigest ||
		before.IntentDigest == "" || before.IntentDigest != after.IntentDigest || before.VisualDigest == "" || before.VisualDigest != after.VisualDigest ||
		before.RequestDigest == "" || before.RequestDigest != after.RequestDigest {
		return false
	}
	withoutChecks := func(input codereviewsvc.ReviewTextInput) codereviewsvc.ReviewTextInput {
		items := make([]codereviewsvc.ReviewTextEvidence, 0, len(input.Items))
		for _, item := range input.Items {
			if item.Surface != "check_status" {
				items = append(items, item)
			}
		}
		input.Items = items
		return input
	}
	return reflect.DeepEqual(withoutChecks(before.TextEvidence), withoutChecks(after.TextEvidence))
}

// A staged approval must still pass the backend decision rules at the moment
// of publication, even when CI, merge eligibility, or team membership changed.
func verifyFullAssessmentApproval(ctx context.Context, stores *Stores, services *Services, job runCodeReviewPayload, fresh codereviewsvc.AssessmentInputCaptureResult) error {
	if fresh.Health == nil {
		return errors.New("fresh full assessment health unavailable")
	}
	results, err := stores.CodeReviews.ListAgentResults(ctx, job.OrgID, job.SessionID)
	if err != nil {
		return err
	}
	findings, err := stores.CodeReviews.ListFindings(ctx, job.OrgID, job.SessionID, false)
	if err != nil {
		return err
	}
	job.PullRequestAuthorTeams, err = resolveCodeReviewAuthorTeams(ctx, stores, services, fresh.Policy.Config(), job, fresh.PullRequest)
	if err != nil {
		return err
	}
	decision, _ := evaluateLiveCodeReviewOutcome(liveCodeReviewOutcomeInput{
		Policy: fresh.Policy.Config(), Job: job, PullRequest: fresh.PullRequest, Health: fresh.Health,
		AgentResults: results, Findings: findings, ChangedFiles: fresh.Files, ChangedFilesAvailable: true,
		OrchestratorSynthesis: codeReviewOrchestratorSynthesisFromResults(results), VisualEvidence: fresh.VisualEvidence, AssessedAt: time.Now().UTC(),
	})
	if decision.Decision != models.CodeReviewDecisionApproved {
		return errFullAssessmentInputsChanged
	}
	return nil
}

func submitFullReviewWithAssessment(ctx context.Context, stores *Stores, services *Services, job runCodeReviewPayload, metadata models.CodeReviewSessionMetadata, assessment *models.CodeReviewAssessment, decision models.CodeReviewDecision, body string) (codeReviewSubmission, bool, error) {
	if assessment != nil {
		job.OutputKey = assessment.PublicationKey
	}
	if assessment != nil && assessment.PublicationState == models.CodeReviewPublicationConfirmed && assessment.GitHubReviewID != nil {
		return codeReviewSubmission{GitHubReviewID: assessment.GitHubReviewID, GitHubReviewURL: assessment.GitHubReviewURL, FinalReviewBody: stringPtrValue(metadata.FinalReviewBody)}, false, nil
	}
	if assessment != nil && assessment.PreviousPublishedAssessmentID != nil {
		previous, err := stores.CodeReviewAssessments.GetByID(ctx, assessment.OrgID, *assessment.PreviousPublishedAssessmentID)
		if err != nil {
			return codeReviewSubmission{}, false, err
		}
		if previous.PublicationState != models.CodeReviewPublicationConfirmed || previous.GitHubReviewID == nil {
			return codeReviewSubmission{}, false, db.ErrCodeReviewAssessmentState
		}
		job.PreviousOutputKey = previous.PublicationKey
		job.ExistingGitHubReviewID = previous.GitHubReviewID
		job.ExistingGitHubReviewURL = previous.GitHubReviewURL
		job.PreviousReviewBody = previous.RenderedBody
		job.PreviousReviewDecision = previous.Decision
		job.PreviousReviewDecidedAt = previous.CompletedAt
	}
	if assessment != nil && services != nil && services.CodeReviews != nil && assessment.PublicationState == models.CodeReviewPublicationNotStarted {
		if err := reserveFullAssessmentPublication(ctx, stores.ThreadSendTx, *assessment, job.HeadSHA); err != nil {
			return codeReviewSubmission{}, false, err
		}
		assessment.Status = models.CodeReviewAssessmentPublishing
		assessment.PublicationState = models.CodeReviewPublicationReserved
	}
	var preSubmit func(context.Context, db.DBTX, codereviewsvc.SubmitReviewRequest) (codereviewsvc.SubmitReviewResult, bool, error)
	if assessment != nil {
		preSubmit = func(lockCtx context.Context, lockDB db.DBTX, request codereviewsvc.SubmitReviewRequest) (codereviewsvc.SubmitReviewResult, bool, error) {
			jobID, hasJob := jobctx.JobIDFromContext(ctx)
			token, hasToken := jobctx.LockTokenFromContext(ctx)
			if !hasJob || !hasToken {
				return codereviewsvc.SubmitReviewResult{}, false, errors.New("full assessment publication requires an active job lease")
			}
			if err := db.NewCodeReviewStore(lockDB).LockAssessmentPublicationJob(lockCtx, assessment.OrgID, jobID, token); err != nil {
				return codereviewsvc.SubmitReviewResult{}, false, err
			}
			current, err := db.NewCodeReviewAssessmentStore(lockDB).GetByID(lockCtx, assessment.OrgID, assessment.ID)
			if err != nil {
				return codereviewsvc.SubmitReviewResult{}, false, err
			}
			if current.Status != models.CodeReviewAssessmentPublishing || current.Generation != assessment.Generation || current.InputDigest != assessment.InputDigest || current.PublicationState != models.CodeReviewPublicationReserved && current.PublicationState != models.CodeReviewPublicationUncertain {
				return codereviewsvc.SubmitReviewResult{}, false, db.ErrCodeReviewAssessmentState
			}
			fresh, freshnessErr := captureFreshFullAssessment(lockCtx, services, job, current)
			if freshnessErr == nil && decision == models.CodeReviewDecisionApproved {
				freshnessErr = verifyFullAssessmentApproval(lockCtx, stores, services, job, fresh)
			}
			if err := freshnessErr; err != nil {
				if !errors.Is(err, errFullAssessmentInputsChanged) && !errors.Is(err, codereviewsvc.ErrAssessmentReuseUnavailable) {
					return codereviewsvc.SubmitReviewResult{}, false, err
				}
				if current.PublicationState == models.CodeReviewPublicationReserved {
					return codereviewsvc.SubmitReviewResult{}, false, errFullAssessmentInputsChanged
				}
				reconciler, ok := services.CodeReviews.(interface {
					ReconcileAssessmentPublication(context.Context, codereviewsvc.SubmitReviewRequest) (codereviewsvc.SubmitReviewResult, bool, error)
				})
				if !ok {
					return codereviewsvc.SubmitReviewResult{}, false, errors.New("uncertain full assessment requires read-only publication reconciliation")
				}
				result, found, reconcileErr := reconciler.ReconcileAssessmentPublication(lockCtx, request)
				if reconcileErr != nil {
					return codereviewsvc.SubmitReviewResult{}, false, reconcileErr
				}
				if !found {
					if noteErr := stores.CodeReviewAssessments.NoteUncertainPublication(lockCtx, current.OrgID, current.ID, current.Generation, current.InputDigest, "review marker not found; pending reconciliation"); noteErr != nil {
						return codereviewsvc.SubmitReviewResult{}, false, noteErr
					}
					return codereviewsvc.SubmitReviewResult{}, false, errors.New("full assessment publication uncertain: review marker not found; pending reconciliation")
				}
				return result, true, nil
			}
			if current.PublicationState == models.CodeReviewPublicationReserved {
				// Use the pool-backed store so this state commits before the
				// external send, independently of the publication lock txn.
				if err := stores.CodeReviewAssessments.MarkPublicationAttemptUncertain(lockCtx, current.OrgID, current.ID, current.Generation, current.InputDigest); err != nil {
					return codereviewsvc.SubmitReviewResult{}, false, err
				}
			}
			return codereviewsvc.SubmitReviewResult{}, false, nil
		}
	}
	submission, submitted, err := submitCodeReviewToGitHubWithOptions(ctx, stores, services, job, metadata, decision, body, preSubmit, assessment != nil)
	if err != nil {
		if assessment != nil && (errors.Is(err, errFullAssessmentInputsChanged) || errors.Is(err, codereviewsvc.ErrAssessmentReuseUnavailable)) {
			if supersedeErr := supersedeUnsentFullPublication(ctx, stores, services, *assessment); supersedeErr != nil {
				return submission, submitted, supersedeErr
			}
			return submission, submitted, errCodeReviewPublicationSuperseded
		}
		return submission, submitted, err
	}
	if assessment != nil && submission.GitHubReviewID != nil && assessment.PublicationState != models.CodeReviewPublicationConfirmed {
		receipt, err := fullAssessmentPublicationReceipt(assessment.ID, assessment.InputDigest, job.HeadSHA, submission.GitHubReviewID, submission.GitHubReviewURL, submission.FormalApprovalID, submission.FormalApprovalURL)
		if err != nil {
			return submission, submitted, err
		}
		if err := recordFullAssessmentPublication(ctx, stores.ThreadSendTx, *assessment, receipt, submission.GitHubReviewID, submission.GitHubReviewURL); err != nil {
			return submission, submitted, err
		}
		assessment.PublicationState = models.CodeReviewPublicationConfirmed
	}
	return submission, submitted, nil
}

func supersedeUnsentFullPublication(ctx context.Context, stores *Stores, services *Services, assessment models.CodeReviewAssessment) error {
	const reason = "inputs changed before publication"
	const detail = "full_review:" + reason
	current, err := stores.CodeReviewAssessments.GetByID(ctx, assessment.OrgID, assessment.ID)
	if err != nil {
		return err
	}
	if current.Status == models.CodeReviewAssessmentPublishing && current.PublicationState == models.CodeReviewPublicationReserved {
		if err := stores.CodeReviewAssessments.SupersedeUnsentPublication(ctx, current.OrgID, current.ID, current.Generation, current.InputDigest, detail); err != nil {
			return err
		}
	} else if (current.Status == models.CodeReviewAssessmentRunning || current.Status == models.CodeReviewAssessmentReserved) && current.PublicationState == models.CodeReviewPublicationNotStarted {
		if err := stores.CodeReviewAssessments.Supersede(ctx, current.OrgID, current.ID, current.Generation, current.InputDigest, detail); err != nil {
			return err
		}
	} else if current.Status != models.CodeReviewAssessmentSuperseded || current.FailureDetail == nil || *current.FailureDetail != detail {
		return db.ErrCodeReviewAssessmentState
	}
	if _, err := stores.CodeReviews.MarkStale(ctx, current.OrgID, current.SessionID, reason); err != nil {
		return err
	}
	if err := settleFullAssessmentScheduler(ctx, stores.ThreadSendTx, current); err != nil {
		return err
	}
	if services == nil || services.CodeReviewLifecycle == nil {
		return errors.New("full assessment replacement scheduler unavailable")
	}
	fallback, ok := services.CodeReviewLifecycle.(codeReviewAssessmentFallback)
	if !ok {
		return errors.New("full assessment replacement scheduler unavailable")
	}
	return fallback.FallbackAssessmentToFull(ctx, current.OrgID, current.ID, reason)
}

func fullAssessmentCoverage(policy models.CodeReviewPolicyConfig, results []models.CodeReviewAgentResult, manifest codereviewsvc.ReviewInputManifest) bool {
	if !manifest.ReuseEligible || policy.AgentRoster.EffectiveReviewerCount() < 1 {
		return false
	}
	allowedReviewers := make(map[string]struct{}, len(policy.AgentRoster.Reviewers))
	for index, provider := range policy.AgentRoster.Reviewers {
		allowedReviewers[codeReviewReviewerKey(index, provider)] = struct{}{}
	}
	completedKeys := make(map[string]struct{}, policy.AgentRoster.EffectiveReviewerCount())
	completedReviewers := 0
	selectedOrchestrator := false
	safeSelectedOrchestrator := false
	for _, result := range results {
		switch result.Role {
		case models.CodeReviewAgentRoleReviewer:
			state, ok := parseCodeReviewReviewerStructuredResult(result.StructuredResult)
			if ok && state.Unavailable {
				continue
			}
			if result.Status == models.CodeReviewAgentResultStatusFailed {
				// Ranked rosters can fall through to another reviewer. A failed
				// candidate does not count as coverage, but any write violation
				// still makes the source conversation unsafe to reuse.
				if ok && (state.ReadOnlyViolation || state.Reverted) {
					return false
				}
				continue
			}
			if result.Status != models.CodeReviewAgentResultStatusCompleted || !ok || !state.ReadOnly || state.ReadOnlyViolation || state.Reverted || state.Error != "" || !codeReviewReviewerResultHasUsableOutput(result) {
				return false
			}
			if _, allowed := allowedReviewers[state.ReviewerKey]; !allowed {
				return false
			}
			if _, duplicate := completedKeys[state.ReviewerKey]; duplicate {
				return false
			}
			completedKeys[state.ReviewerKey] = struct{}{}
			completedReviewers++
		case models.CodeReviewAgentRoleOrchestrator:
			state, ok := parseCodeReviewOrchestratorStructuredResult(result.StructuredResult)
			if result.Status == models.CodeReviewAgentResultStatusCompleted && ok && state.SynthesisValidated && codeReviewOrchestratorSynthesisUsable(state.Synthesis) && !selectedOrchestrator {
				// The full decision renderer chooses the first validated
				// synthesis. A later valid result cannot silently become the
				// recheck source for a different set of requirements.
				selectedOrchestrator = true
				_, threadErr := uuid.Parse(state.ThreadID)
				safeSelectedOrchestrator = threadErr == nil && state.ReadOnly && !state.ReadOnlyViolation && !state.Reverted && state.Error == ""
			}
		}
	}
	return safeSelectedOrchestrator && completedReviewers >= policy.AgentRoster.EffectiveReviewerCount() && completedReviewers >= policy.AgentRoster.RequireReviewerQuorum
}

func fullAssessmentOutcome(results []models.CodeReviewAgentResult, reasons []models.CodeReviewRiskReason, coverage bool) (json.RawMessage, error) {
	synthesis := codeReviewOrchestratorSynthesisFromResults(results)
	return json.Marshal(struct {
		DescriptionAssessments []codeReviewDescriptionAssessment `json:"description_assessments"`
		RiskReasons            []models.CodeReviewRiskReason     `json:"risk_reasons"`
		CoverageComplete       bool                              `json:"coverage_complete"`
	}{append([]codeReviewDescriptionAssessment{}, synthesis.DescriptionAssessments...), reasons, coverage})
}

func failFullAssessmentForSession(ctx context.Context, stores *Stores, job runCodeReviewPayload, detail string) error {
	if stores == nil || stores.CodeReviewAssessments == nil {
		return nil
	}
	assessment, err := stores.CodeReviewAssessments.GetBySessionID(ctx, job.OrgID, job.SessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if assessment.Status == models.CodeReviewAssessmentFailed || assessment.Status == models.CodeReviewAssessmentCompleted || assessment.Status == models.CodeReviewAssessmentSuperseded || assessment.Status == models.CodeReviewAssessmentCancelled {
		return settleFullAssessmentScheduler(ctx, stores.ThreadSendTx, assessment)
	}
	if err := stores.CodeReviewAssessments.Fail(ctx, job.OrgID, assessment.ID, assessment.Generation, assessment.InputDigest, detail); err != nil {
		return err
	}
	return settleFullAssessmentScheduler(ctx, stores.ThreadSendTx, assessment)
}

func supersedeFullAssessmentForSession(ctx context.Context, stores *Stores, job runCodeReviewPayload, detail string) error {
	if stores == nil || stores.CodeReviewAssessments == nil {
		return nil
	}
	assessment, err := stores.CodeReviewAssessments.GetBySessionID(ctx, job.OrgID, job.SessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if assessment.Status == models.CodeReviewAssessmentSuperseded || assessment.Status == models.CodeReviewAssessmentCompleted {
		return settleFullAssessmentScheduler(ctx, stores.ThreadSendTx, assessment)
	}
	if err := stores.CodeReviewAssessments.Supersede(ctx, job.OrgID, assessment.ID, assessment.Generation, assessment.InputDigest, detail); err != nil {
		return err
	}
	return settleFullAssessmentScheduler(ctx, stores.ThreadSendTx, assessment)
}

func settleFullAssessmentScheduler(ctx context.Context, txStarter db.TxStarter, assessment models.CodeReviewAssessment) error {
	if txStarter == nil {
		return fmt.Errorf("assessment settlement requires transaction support")
	}
	return db.NewCodeReviewScheduleStore(txStarter).SettleAssessment(ctx, assessment.OrgID, assessment.ID)
}

// reconcileCompletedFullAssessment repairs a crash after legacy completion.
// The immutable staged result is authoritative; a later poll must not rerun
// synthesis or substitute current PR evidence into this assessment.
func reconcileCompletedFullAssessment(ctx context.Context, stores *Stores, metadata models.CodeReviewSessionMetadata) error {
	if stores == nil || stores.CodeReviewAssessments == nil {
		return nil
	}
	assessment, err := stores.CodeReviewAssessments.GetBySessionID(ctx, metadata.OrgID, metadata.SessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if assessment.Status == models.CodeReviewAssessmentCompleted {
		return nil
	}
	if assessment.ResultOrigin == nil || assessment.Decision == nil || assessment.Acceptable == nil || assessment.RenderedBody == nil || len(assessment.StructuredOutcome) == 0 {
		return db.ErrCodeReviewAssessmentState
	}
	if assessment.PublicationState == models.CodeReviewPublicationNotStarted {
		if metadata.GitHubReviewID == nil {
			if err := stores.CodeReviewAssessments.MarkPublicationNotRequired(ctx, assessment.OrgID, assessment.ID, assessment.Generation, assessment.InputDigest); err != nil {
				return err
			}
			assessment.PublicationState = models.CodeReviewPublicationNotRequired
		} else {
			if err := stores.CodeReviewAssessments.ReservePublication(ctx, assessment.OrgID, assessment.ID, assessment.Generation, assessment.InputDigest, assessment.HeadSHA); err != nil {
				return err
			}
			assessment.Status = models.CodeReviewAssessmentPublishing
			assessment.PublicationState = models.CodeReviewPublicationReserved
		}
	}
	if assessment.PublicationState == models.CodeReviewPublicationReserved || assessment.PublicationState == models.CodeReviewPublicationUncertain {
		if metadata.GitHubReviewID == nil {
			return db.ErrCodeReviewAssessmentState
		}
		receipt, err := fullAssessmentPublicationReceipt(assessment.ID, assessment.InputDigest, assessment.HeadSHA, metadata.GitHubReviewID, metadata.GitHubReviewURL, nil, nil)
		if err != nil {
			return err
		}
		if err := stores.CodeReviewAssessments.RecordPublication(ctx, assessment.OrgID, assessment.ID, assessment.Generation, assessment.InputDigest, models.CodeReviewPublicationConfirmed, receipt, metadata.GitHubReviewID, metadata.GitHubReviewURL); err != nil {
			return err
		}
		assessment.PublicationState = models.CodeReviewPublicationConfirmed
	}
	if assessment.PublicationState != models.CodeReviewPublicationConfirmed && assessment.PublicationState != models.CodeReviewPublicationNotRequired {
		return db.ErrCodeReviewAssessmentState
	}
	if stores.ThreadSendTx == nil {
		return fmt.Errorf("assessment reconciliation requires transaction")
	}
	tx, err := stores.ThreadSendTx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	store := db.NewCodeReviewAssessmentStore(tx)
	if err := store.LinkFullReviewEvidence(ctx, assessment.OrgID, assessment.ID, assessment.SessionID); err != nil {
		return err
	}
	if assessment.CoverageComplete {
		claimed, err := db.NewCodeReviewRecheckStore(tx).ClaimFullAssessmentOwner(ctx, tx, assessment.OrgID, assessment.SessionID, assessment.PullRequestID)
		if err != nil {
			return err
		}
		if !claimed {
			return fmt.Errorf("full assessment conversation is not yet drained: %w", db.ErrCodeReviewAssessmentState)
		}
	}
	if err := store.Complete(ctx, assessment.OrgID, assessment.ID, assessment.Generation, assessment.InputDigest, models.CodeReviewAssessmentCompletion{ResultOrigin: *assessment.ResultOrigin, CoverageComplete: assessment.CoverageComplete, Decision: *assessment.Decision, Acceptable: *assessment.Acceptable, RiskReasonDetails: assessment.RiskReasonDetails, StructuredOutcome: assessment.StructuredOutcome, RenderedBody: *assessment.RenderedBody}); err != nil {
		return err
	}
	if err := db.NewCodeReviewScheduleStore(tx).SettleAssessment(ctx, assessment.OrgID, assessment.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// resumeStagedFullAssessment consumes the already committed decision after a
// worker crash. It never re-renders a body with a new timestamp or asks the
// model for another synthesis to repair publication state.
func resumeStagedFullAssessment(ctx context.Context, stores *Stores, services *Services, job runCodeReviewPayload, metadata models.CodeReviewSessionMetadata, assessment models.CodeReviewAssessment, changedFiles []codereviewsvc.PullRequestFile) error {
	if assessment.Status == models.CodeReviewAssessmentSuperseded && assessment.FailureDetail != nil {
		if strings.HasPrefix(*assessment.FailureDetail, "full_review_queued:") {
			return errCodeReviewPublicationSuperseded
		}
		if strings.HasPrefix(*assessment.FailureDetail, "full_review:") {
			if err := supersedeUnsentFullPublication(ctx, stores, services, assessment); err != nil {
				return err
			}
			return errCodeReviewPublicationSuperseded
		}
	}
	if assessment.ResultOrigin == nil || assessment.Decision == nil || assessment.Acceptable == nil || assessment.RenderedBody == nil || len(assessment.StructuredOutcome) == 0 {
		return db.ErrCodeReviewAssessmentState
	}
	if assessment.PublicationState == models.CodeReviewPublicationNotStarted {
		if err := verifyFullAssessmentFreshness(ctx, services, job, assessment); err != nil {
			if errors.Is(err, errFullAssessmentInputsChanged) || errors.Is(err, codereviewsvc.ErrAssessmentReuseUnavailable) {
				if recoveryErr := supersedeUnsentFullPublication(ctx, stores, services, assessment); recoveryErr != nil {
					return recoveryErr
				}
				return errCodeReviewPublicationSuperseded
			}
			return err
		}
	}
	submission, _, err := submitFullReviewWithAssessment(ctx, stores, services, job, metadata, &assessment, *assessment.Decision, *assessment.RenderedBody)
	if err != nil {
		return err
	}
	finalBody := *assessment.RenderedBody
	if submission.FinalReviewBody != "" {
		finalBody = submission.FinalReviewBody
	}
	var reasons []models.CodeReviewRiskReason
	if len(assessment.RiskReasonDetails) > 0 {
		if err := json.Unmarshal(assessment.RiskReasonDetails, &reasons); err != nil {
			return fmt.Errorf("decode staged risk reasons: %w", err)
		}
	}
	additions, deletions := codeReviewLineChanges(changedFiles)
	legacy := db.CompleteCodeReviewParams{SessionID: job.SessionID, Decision: *assessment.Decision, Acceptable: *assessment.Acceptable, GitHubReviewID: submission.GitHubReviewID, GitHubReviewURL: submission.GitHubReviewURL, FinalReviewBody: finalBody, Additions: &additions, Deletions: &deletions, RiskReasonDetails: reasons}
	if err := completeFullAssessment(ctx, stores.ThreadSendTx, assessment, legacy, assessment.StructuredOutcome, assessment.CoverageComplete, *assessment.RenderedBody); err != nil {
		return err
	}
	return nil
}

// prepareFullAssessment captures source inputs before any reviewer fanout. A
// stable ID lets a worker retry restore the same visual snapshot after a crash.
// Legacy jobs and disabled deployments continue without an assessment.
func prepareFullAssessment(ctx context.Context, stores *Stores, services *Services, job runCodeReviewPayload, metadata models.CodeReviewSessionMetadata) (*models.CodeReviewAssessment, *codereviewsvc.AssessmentInputCaptureResult, error) {
	if stores == nil || services == nil || stores.CodeReviewAssessments == nil || !services.CodeReviewAssessmentsEnabled || services.CodeReviewInputCapture == nil {
		return nil, nil, nil
	}
	assessmentID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("code-review-full:"+job.SessionID.String()))
	existing, err := stores.CodeReviewAssessments.GetBySessionID(ctx, job.OrgID, job.SessionID)
	if err == nil {
		return &existing, nil, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, err
	}
	captured, err := services.CodeReviewInputCapture.CaptureAssessmentInputs(ctx, codereviewsvc.AssessmentInputCaptureRequest{OrgID: job.OrgID, RepositoryID: job.RepositoryID, PullRequestID: job.PullRequestID, SessionID: job.SessionID, AssessmentID: assessmentID, RequestContext: job.RequestContext})
	if err != nil {
		if errors.Is(err, codereviewsvc.ErrAssessmentReuseUnavailable) {
			// A complete reusable fingerprint is unavailable, but the normal
			// full-review controller can still assess and publish this PR.
			return nil, nil, nil
		}
		return nil, nil, err
	}
	if captured.Policy.ID != job.PolicyID || captured.Policy.Version != job.PolicyVersion || captured.Snapshot.HeadSHA != job.HeadSHA || captured.Snapshot.BaseSHA != metadata.BaseSHA {
		return nil, nil, fmt.Errorf("full review captured source differs from queued review identity")
	}
	generation := int64(1)
	var previousID, previousPublishedID *uuid.UUID
	previous, err := stores.CodeReviewAssessments.GetLatestForPR(ctx, job.OrgID, job.PullRequestID)
	if err == nil {
		generation = previous.Generation + 1
		previousID = &previous.ID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, err
	}
	previousPublished, err := stores.CodeReviewAssessments.GetLatestPublishedForPR(ctx, job.OrgID, job.PullRequestID)
	if err == nil {
		previousPublishedID = &previousPublished.ID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, err
	}
	capture, err := codereviewsvc.AssessmentCaptureFromManifest(models.CodeReviewAssessmentCapture{ID: assessmentID, OrgID: job.OrgID, RepositoryID: job.RepositoryID, PullRequestID: job.PullRequestID, PolicyID: job.PolicyID, SessionID: job.SessionID, Generation: generation, PreviousAssessmentID: previousID, PreviousPublishedAssessmentID: previousPublishedID, ReviewScope: models.CodeReviewScopeFull, RouteReason: models.CodeReviewRouteInitialFull, PublicationKey: job.OutputKey}, captured.Manifest)
	if err != nil {
		return nil, nil, err
	}
	created, _, err := stores.CodeReviewAssessments.Create(ctx, capture)
	if err != nil {
		return nil, nil, err
	}
	if created.Status == models.CodeReviewAssessmentReserved {
		if err := stores.CodeReviewAssessments.MarkRunning(ctx, created.OrgID, created.ID, created.Generation, created.InputDigest); err != nil {
			return nil, nil, err
		}
		created.Status = models.CodeReviewAssessmentRunning
	}
	return &created, &captured, nil
}

// completeFullAssessment commits the legacy full-review record, evidence
// attribution, and immutable assessment outcome in one transaction. The caller
// has already reconciled any external GitHub write against this assessment's
// publication key; absence of a GitHub publisher is explicitly not_required.
func completeFullAssessment(ctx context.Context, txStarter db.TxStarter, assessment models.CodeReviewAssessment, legacy db.CompleteCodeReviewParams, outcome json.RawMessage, coverageComplete bool, assessmentBody string) error {
	if txStarter == nil {
		return fmt.Errorf("assessment completion requires a transaction")
	}
	if assessment.ReviewScope != models.CodeReviewScopeFull || assessment.SessionID != legacy.SessionID || assessment.Status != models.CodeReviewAssessmentRunning && assessment.Status != models.CodeReviewAssessmentPublishing {
		return db.ErrCodeReviewAssessmentState
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	assessments := db.NewCodeReviewAssessmentStore(tx)
	if err := assessments.LinkFullReviewEvidence(ctx, assessment.OrgID, assessment.ID, assessment.SessionID); err != nil {
		return err
	}
	if assessment.PublicationState == models.CodeReviewPublicationNotStarted {
		if err := assessments.MarkPublicationNotRequired(ctx, assessment.OrgID, assessment.ID, assessment.Generation, assessment.InputDigest); err != nil {
			return err
		}
	}
	if _, err := db.NewCodeReviewStore(tx).CompleteReview(ctx, assessment.OrgID, legacy); err != nil {
		return err
	}
	if coverageComplete {
		claimed, err := db.NewCodeReviewRecheckStore(tx).ClaimFullAssessmentOwner(ctx, tx, assessment.OrgID, assessment.SessionID, assessment.PullRequestID)
		if err != nil {
			return err
		}
		if !claimed {
			return fmt.Errorf("full assessment conversation is not yet drained: %w", db.ErrCodeReviewAssessmentState)
		}
	}
	reasons, err := json.Marshal(legacy.RiskReasonDetails)
	if err != nil {
		return fmt.Errorf("encode assessment risk reasons: %w", err)
	}
	if err := assessments.Complete(ctx, assessment.OrgID, assessment.ID, assessment.Generation, assessment.InputDigest, models.CodeReviewAssessmentCompletion{
		ResultOrigin:      models.CodeReviewResultExecuted,
		CoverageComplete:  coverageComplete,
		Decision:          legacy.Decision,
		Acceptable:        legacy.Acceptable,
		RiskReasonDetails: reasons,
		StructuredOutcome: outcome,
		RenderedBody:      assessmentBody,
	}); err != nil {
		return err
	}
	if assessment.PreviousAssessmentID != nil {
		previous, err := assessments.GetByID(ctx, assessment.OrgID, *assessment.PreviousAssessmentID)
		if err != nil {
			return err
		}
		if previous.SessionID != assessment.SessionID {
			if _, err := db.NewCodeReviewRecheckStore(tx).RetireOwnerForCompletedReplacement(ctx, tx, assessment.OrgID, previous.SessionID, assessment.PullRequestID, assessment.ID); err != nil {
				return err
			}
		}
	}
	if err := db.NewCodeReviewScheduleStore(tx).SettleAssessment(ctx, assessment.OrgID, assessment.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// reserveFullAssessmentPublication fences the exact input and commit before a
// GitHub submission. The caller must reconcile a prior reserved/uncertain
// marker rather than allocating a new publication key after a retry.
func reserveFullAssessmentPublication(ctx context.Context, tx db.DBTX, assessment models.CodeReviewAssessment, commitSHA string) error {
	if assessment.Status != models.CodeReviewAssessmentRunning {
		return db.ErrCodeReviewAssessmentState
	}
	return db.NewCodeReviewAssessmentStore(tx).ReservePublication(ctx, assessment.OrgID, assessment.ID, assessment.Generation, assessment.InputDigest, commitSHA)
}

func recordFullAssessmentPublication(ctx context.Context, tx db.DBTX, assessment models.CodeReviewAssessment, receipt json.RawMessage, reviewID *int64, reviewURL *string) error {
	return db.NewCodeReviewAssessmentStore(tx).RecordPublication(ctx, assessment.OrgID, assessment.ID, assessment.Generation, assessment.InputDigest, models.CodeReviewPublicationConfirmed, receipt, reviewID, reviewURL)
}

func fullAssessmentPublicationReceipt(assessmentID uuid.UUID, inputDigest, commitSHA string, reviewID *int64, reviewURL *string, formalApprovalID *int64, formalApprovalURL *string) (json.RawMessage, error) {
	return json.Marshal(struct {
		AssessmentID       uuid.UUID `json:"assessment_id"`
		InputDigest        string    `json:"input_digest"`
		SubmittedCommitSHA string    `json:"submitted_commit_sha"`
		GitHubReviewID     *int64    `json:"github_review_id,omitempty"`
		GitHubReviewURL    *string   `json:"github_review_url,omitempty"`
		FormalApprovalID   *int64    `json:"formal_approval_id,omitempty"`
		FormalApprovalURL  *string   `json:"formal_approval_url,omitempty"`
	}{assessmentID, inputDigest, commitSHA, reviewID, reviewURL, formalApprovalID, formalApprovalURL})
}
