package codereview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var errRecheckAdmissionChanged = errors.New("review inputs changed during admission")
var errRecheckNeedsFull = errors.New("review requires a full assessment")

func (s *Service) requestAssessmentReview(ctx context.Context, req ScheduleRequestInput, fromWake ...bool) (ScheduleRequestResult, error) {
	if s.scheduling == nil || !s.scheduling.rechecksEnabled || s.scheduling.assessmentCapture == nil {
		return ScheduleRequestResult{}, fmt.Errorf("code review rechecks unavailable")
	}
	recovering := len(fromWake) > 0 && fromWake[0]
	if req.Mode == models.CodeReviewForceFresh && (strings.TrimSpace(req.Reason) == "" || utf8.RuneCountInString(req.Reason) > 2000) {
		return ScheduleRequestResult{}, fmt.Errorf("force_fresh requires a reason of at most 2000 characters")
	}
	state, err := s.GetSchedule(ctx, req.OrgID, req.PullRequestID)
	if errors.Is(err, pgx.ErrNoRows) {
		latest, loadErr := s.metadata.GetLatestByPullRequest(ctx, req.OrgID, req.PullRequestID)
		if loadErr != nil {
			return ScheduleRequestResult{}, loadErr
		}
		state.RepositoryID = latest.RepositoryID
	} else if err != nil {
		return ScheduleRequestResult{}, err
	}
	requestContext := assessmentRequestContext(req)
	ordinaryHash, err := reviewRequestHash(req.PullRequestID, req.Mode, requestContext, false)
	if err != nil {
		return ScheduleRequestResult{}, err
	}
	forcedHash, err := reviewRequestHash(req.PullRequestID, req.Mode, requestContext, true)
	if err != nil {
		return ScheduleRequestResult{}, err
	}
	kind := "github"
	if req.RequesterID != nil {
		kind = "ui"
	}
	if !recovering {
		existing, lookupErr := s.scheduling.store.GetRequestByIdentity(ctx, req.OrgID, kind, req.RequestID.String())
		if lookupErr == nil {
			if existing.Mode != req.Mode || (existing.InputHash != ordinaryHash && existing.InputHash != forcedHash) {
				return ScheduleRequestResult{}, db.ErrCodeReviewRequestConflict
			}
			return s.existingAssessmentRequestResult(ctx, req, existing)
		}
		if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return ScheduleRequestResult{}, lookupErr
		}
	}
	if req.Mode == models.CodeReviewForceFresh {
		return s.requestFullAssessmentReview(ctx, req, state.RepositoryID, requestContext, true, recovering)
	}
	forcedPending, err := pendingForcedFull(state.PendingInput)
	if err != nil {
		return ScheduleRequestResult{}, err
	}
	if forcedPending {
		return s.queuePendingAssessmentCapture(ctx, req, state.RepositoryID, requestContext, ordinaryHash, kind)
	}
	baseline, baselineErr := s.scheduling.store.GetLatestFullBaseline(ctx, req.OrgID, req.PullRequestID)
	if errors.Is(baselineErr, pgx.ErrNoRows) {
		return s.requestFullAssessmentReview(ctx, req, state.RepositoryID, requestContext, true, recovering)
	}
	if baselineErr != nil {
		return ScheduleRequestResult{}, baselineErr
	}
	// Capture network and image bytes outside the PR transaction. A fresh UUID
	// keeps each attempted snapshot immutable even when a prior request failed.
	captured, captureErr := s.scheduling.assessmentCapture.CaptureAssessmentInputs(ctx, AssessmentInputCaptureRequest{
		OrgID: req.OrgID, RepositoryID: state.RepositoryID, PullRequestID: req.PullRequestID,
		SessionID: baseline.SessionID, AssessmentID: uuid.New(), RequestContext: requestContext,
	})
	if errors.Is(captureErr, ErrReviewIneligible) {
		return ScheduleRequestResult{}, captureErr
	}
	if errors.Is(captureErr, ErrAssessmentReuseUnavailable) {
		return s.requestFullAssessmentReview(ctx, req, state.RepositoryID, requestContext, true, recovering)
	}
	if captureErr != nil {
		return s.queuePendingAssessmentCapture(ctx, req, state.RepositoryID, requestContext, ordinaryHash, kind)
	}
	latest, err := s.scheduling.store.GetLatestAssessment(ctx, req.OrgID, req.PullRequestID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ScheduleRequestResult{}, err
	}
	var previous *RecheckPrevious
	if err == nil {
		manifest, decodeErr := decodeAssessmentManifest(latest.InputManifest)
		if decodeErr == nil {
			previous = &RecheckPrevious{Inputs: manifest, Completed: latest.Status == models.CodeReviewAssessmentCompleted,
				EvidenceValidated: latest.Status == models.CodeReviewAssessmentCompleted && len(latest.StructuredOutcome) > 0}
		}
		if latest.ReviewScope == models.CodeReviewScopeEvidenceOnly && latest.Status == models.CodeReviewAssessmentFailed {
			previous = &RecheckPrevious{Completed: true, EvidenceValidated: false}
		}
	}
	policy := captured.Policy.Config()
	if !policy.ContinuationPolicy.Effective().Enabled {
		return s.requestFullAssessmentReview(ctx, req, state.RepositoryID, requestContext, true, recovering)
	}
	baselineFacts := baselineForPlanning(baseline, policy, captured.Files)
	plan := PlanReviewRecheck(RecheckPlanInput{Current: &captured.Manifest, Baseline: baselineFacts, Previous: previous})
	if plan.Route == RecheckRouteWait {
		return s.queuePendingAssessmentCapture(ctx, req, state.RepositoryID, requestContext, ordinaryHash, kind)
	}
	if plan.Route == RecheckRouteFull {
		return s.requestFullAssessmentReview(ctx, req, state.RepositoryID, requestContext, true, recovering)
	}
	result, err := s.admitCapturedAssessment(ctx, req, captured, baseline, latest, plan, ordinaryHash, kind, requestContext)
	if errors.Is(err, errRecheckNeedsFull) {
		return s.requestFullAssessmentReview(ctx, req, state.RepositoryID, requestContext, true, recovering)
	}
	if errors.Is(err, errRecheckAdmissionChanged) {
		return s.queuePendingAssessmentCapture(ctx, req, state.RepositoryID, requestContext, ordinaryHash, kind)
	}
	return result, err
}

func assessmentRequestContext(req ScheduleRequestInput) *ReviewRequestContext {
	if req.RequestContext != nil {
		return normalizeReviewRequestContext(req.RequestContext)
	}
	if strings.TrimSpace(req.Reason) == "" {
		return nil
	}
	return &ReviewRequestContext{Source: "ui", Body: strings.TrimSpace(req.Reason)}
}

func decodeAssessmentManifest(raw json.RawMessage) (ReviewInputManifest, error) {
	var m ReviewInputManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, err
	}
	return m, ValidateReviewInputManifest(m)
}

func pendingForcedFull(raw json.RawMessage) (bool, error) {
	if len(raw) == 0 {
		return false, nil
	}
	var pending scheduledReviewIntent
	if err := json.Unmarshal(raw, &pending); err != nil {
		return false, err
	}
	return pending.Mode == models.CodeReviewForceFresh || pending.Force, nil
}

func baselineForPlanning(a models.CodeReviewAssessment, policy models.CodeReviewPolicyConfig, files []PullRequestFile) *RecheckBaseline {
	m, err := decodeAssessmentManifest(a.InputManifest)
	if err != nil {
		return nil
	}
	b := &RecheckBaseline{Inputs: m, CompletedFull: a.Status == models.CodeReviewAssessmentCompleted && a.ReviewScope == models.CodeReviewScopeFull, CoverageComplete: a.CoverageComplete}
	if !b.CompletedFull || !b.CoverageComplete {
		return b
	}
	var outcome struct {
		DescriptionAssessments []struct {
			Key    string `json:"key"`
			Status string `json:"status"`
		} `json:"description_assessments"`
		RiskReasons      []models.CodeReviewRiskReason `json:"risk_reasons"`
		CoverageComplete bool                          `json:"coverage_complete"`
	}
	requirements := ApplicableDescriptionRequirements(policy, files)
	if err := json.Unmarshal(a.StructuredOutcome, &outcome); err != nil || !outcome.CoverageComplete || len(outcome.DescriptionAssessments) == 0 || len(outcome.DescriptionAssessments) != len(requirements) {
		b.CoverageComplete = false
		return b
	}
	var storedReasons []models.CodeReviewRiskReason
	if err := json.Unmarshal(a.RiskReasonDetails, &storedReasons); err != nil || len(storedReasons) != len(outcome.RiskReasons) {
		b.CoverageComplete = false
		return b
	}
	for i, r := range outcome.RiskReasons {
		if r.Code != storedReasons[i].Code {
			b.CoverageComplete = false
			return b
		}
		b.RiskReasons = append(b.RiskReasons, r.Code)
	}
	byKey := make(map[string]models.CodeReviewDescriptionRequirement, len(requirements))
	for _, r := range requirements {
		if r.Key == "" {
			b.CoverageComplete = false
			return b
		}
		byKey[r.Key] = r
	}
	seen := make(map[string]bool, len(byKey))
	for _, r := range outcome.DescriptionAssessments {
		requirement, ok := byKey[r.Key]
		if !ok || seen[r.Key] {
			b.CoverageComplete = false
			return b
		}
		seen[r.Key] = true
		switch r.Status {
		case "missing":
			if requirement.Required {
				b.MissingRequirements = append(b.MissingRequirements, RecheckMissingRequirement{ID: r.Key, EvidenceKind: string(requirement.EvidenceKind)})
			}
		case "satisfied", "not_applicable":
		default:
			b.CoverageComplete = false
			return b
		}
	}
	return b
}

func (s *Service) existingAssessmentRequestResult(ctx context.Context, req ScheduleRequestInput, record db.CodeReviewRequestRecord) (ScheduleRequestResult, error) {
	state, err := s.GetSchedule(ctx, req.OrgID, req.PullRequestID)
	if err != nil {
		return ScheduleRequestResult{}, err
	}
	disposition := models.CodeReviewRequestQueued
	switch record.Status {
	case "satisfied":
		disposition = models.CodeReviewRequestReused
	case "joined":
		disposition = models.CodeReviewRequestJoined
	case "cancelled", "superseded", "failed":
		disposition = models.CodeReviewRequestCancelled
	}
	return ScheduleRequestResult{RequestID: req.RequestID, SessionID: record.SessionID, AssessmentID: record.AssessmentID, Disposition: disposition, Schedule: state}, nil
}

func (s *Service) requestFullAssessmentReview(ctx context.Context, req ScheduleRequestInput, repoID uuid.UUID, context *ReviewRequestContext, force, recovering bool) (ScheduleRequestResult, error) {
	source := req.TriggerSource
	if source == "" {
		source = models.CodeReviewTriggerSourceSlashCommand
	}
	input := ReviewChangedInput{OrgID: req.OrgID, RepositoryID: repoID, PullRequestID: req.PullRequestID, ExplicitRequest: !recovering, GitHubDeliveryID: req.RequestID.String(), RequestContext: context, ChangeReason: "assessment.full_fallback", TriggerSource: source}
	result, err := s.scheduleReview(ctx, input, req.Mode, force, req.RequesterID)
	if err != nil {
		return ScheduleRequestResult{}, err
	}
	state, err := s.GetSchedule(ctx, req.OrgID, req.PullRequestID)
	if err != nil {
		return ScheduleRequestResult{}, err
	}
	disposition := models.CodeReviewRequestQueued
	if result.Reused {
		disposition = models.CodeReviewRequestJoined
		if !result.Deferred {
			disposition = models.CodeReviewRequestReused
		}
	}
	if result.IgnoredReason == "cancelled" {
		disposition = models.CodeReviewRequestCancelled
	}
	var sessionID *uuid.UUID
	if result.SessionID != uuid.Nil {
		sessionID = &result.SessionID
	}
	return ScheduleRequestResult{RequestID: req.RequestID, SessionID: sessionID, Disposition: disposition, Schedule: state}, nil
}

func (s *Service) queuePendingAssessmentCapture(ctx context.Context, req ScheduleRequestInput, repoID uuid.UUID, context *ReviewRequestContext, hash, kind string) (ScheduleRequestResult, error) {
	var result ScheduleRequestResult
	err := s.scheduling.store.WithLockedPR(ctx, req.OrgID, repoID, req.PullRequestID, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
		id, duplicate, err := db.RecordCodeReviewRequest(ctx, tx, req.OrgID, repoID, req.PullRequestID, kind, req.RequestID.String(), req.Mode, hash, state.Generation+1, req.RequesterID)
		if err != nil {
			return err
		}
		if duplicate {
			var status string
			var sessionID, assessmentID *uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT status,session_id,assessment_id FROM code_review_requests WHERE org_id=$1 AND id=$2`, req.OrgID, id).Scan(&status, &sessionID, &assessmentID); err != nil {
				return err
			}
			if status != "pending" {
				result.Disposition = models.CodeReviewRequestJoined
				if status == "satisfied" {
					result.Disposition = models.CodeReviewRequestReused
				}
				result.SessionID = sessionID
				result.AssessmentID = assessmentID
				return nil
			}
			if state.PendingRequestID != nil && *state.PendingRequestID == id {
				result.Disposition = models.CodeReviewRequestQueued
				return nil
			}
		}
		forcePending, err := pendingForcedFull(state.PendingInput)
		if err != nil {
			return err
		}
		if forcePending {
			_, err = tx.Exec(ctx, `UPDATE code_review_requests SET status='superseded' WHERE org_id=$1 AND id=$2 AND status='pending'`, req.OrgID, id)
			result.Disposition = models.CodeReviewRequestCancelled
			return err
		}
		if state.PendingRequestID != nil && *state.PendingRequestID != id {
			if _, err := tx.Exec(ctx, `UPDATE code_review_requests SET status='superseded' WHERE org_id=$1 AND id=$2 AND status='pending'`, req.OrgID, *state.PendingRequestID); err != nil {
				return err
			}
		}
		if state.Generation == 0 {
			state.Generation = 1
		}
		source := req.TriggerSource
		if source == "" {
			source = models.CodeReviewTriggerSourceSlashCommand
		}
		input := ReviewChangedInput{OrgID: req.OrgID, RepositoryID: repoID, PullRequestID: req.PullRequestID, ExplicitRequest: true, GitHubDeliveryID: req.RequestID.String(), RequestContext: context, TriggerSource: source}
		state.PendingInput, err = db.EncodeCodeReviewScheduleInput(scheduledReviewIntent{Input: input, Mode: models.CodeReviewRecheck, RequesterID: req.RequesterID})
		if err != nil {
			return err
		}
		state.PendingRequestID = &id
		now := s.scheduling.now()
		state.FirstPendingAt = &now
		state.State = models.CodeReviewScheduleWaiting
		state.WaitReason = models.CodeReviewWaitContext
		retry := now.Add(15 * time.Second)
		state.RetryAt = &retry
		result.Disposition = models.CodeReviewRequestQueued
		return db.UpsertCodeReviewWake(ctx, tx, req.OrgID, req.PullRequestID, retry)
	})
	if err != nil {
		return result, err
	}
	state, err := s.GetSchedule(ctx, req.OrgID, req.PullRequestID)
	if err != nil {
		return result, err
	}
	result.RequestID = req.RequestID
	result.Schedule = state
	return result, nil
}

func (s *Service) admitCapturedAssessment(ctx context.Context, req ScheduleRequestInput, captured AssessmentInputCaptureResult, baseline, previous models.CodeReviewAssessment, plan RecheckPlan, hash, kind string, requestContext *ReviewRequestContext) (ScheduleRequestResult, error) {
	var result ScheduleRequestResult
	err := s.scheduling.store.WithLockedPR(ctx, req.OrgID, captured.Manifest.Code.RepositoryID, req.PullRequestID, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
		assessments := db.NewCodeReviewAssessmentStore(tx)
		current, err := assessments.GetLatestForPR(ctx, req.OrgID, req.PullRequestID)
		if err != nil {
			return err
		}
		if current.ID != previous.ID {
			return errRecheckAdmissionChanged
		}
		currentBaseline, err := assessments.GetByID(ctx, req.OrgID, baseline.ID)
		if err != nil || currentBaseline.Status != models.CodeReviewAssessmentCompleted {
			return errRecheckAdmissionChanged
		}
		resolved, err := db.NewCodeReviewStore(tx).ResolvePolicy(ctx, req.OrgID)
		if err != nil {
			return err
		}
		if resolved.Policy == nil || resolved.Policy.ID != captured.Policy.ID || resolved.Policy.Version != captured.Policy.Version || !resolved.Config.ContinuationPolicy.Effective().Enabled {
			return errRecheckNeedsFull
		}
		if state.RepositoryID != captured.Manifest.Code.RepositoryID || captured.Manifest.Code.HeadSHA != captured.Snapshot.HeadSHA || captured.Manifest.Code.BaseSHA != captured.Snapshot.BaseSHA || captured.Manifest.Code.BaseRef != captured.Snapshot.BaseRef {
			return errRecheckAdmissionChanged
		}
		// The pull request store was refreshed by capture; the admission lock
		// verifies it still names the same immutable target.
		pr, err := db.NewPullRequestStore(tx).GetByID(ctx, req.OrgID, req.PullRequestID)
		if err != nil {
			return err
		}
		if pr.HeadSHA == nil || *pr.HeadSHA != captured.Manifest.Code.HeadSHA || pr.BaseSHA == nil || *pr.BaseSHA != captured.Manifest.Code.BaseSHA {
			return errRecheckAdmissionChanged
		}
		requestID, duplicate, err := db.RecordCodeReviewRequest(ctx, tx, req.OrgID, state.RepositoryID, req.PullRequestID, kind, req.RequestID.String(), req.Mode, hash, state.Generation+1, req.RequesterID)
		if err != nil {
			return err
		}
		if duplicate {
			var status string
			var existingAssessment, sessionID *uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT status,assessment_id,session_id FROM code_review_requests WHERE org_id=$1 AND id=$2`, req.OrgID, requestID).Scan(&status, &existingAssessment, &sessionID); err != nil {
				return err
			}
			if status != "pending" {
				result = ScheduleRequestResult{RequestID: req.RequestID, AssessmentID: existingAssessment, SessionID: sessionID, Disposition: models.CodeReviewRequestJoined}
				return nil
			}
		}
		forcePending, err := pendingForcedFull(state.PendingInput)
		if err != nil {
			return err
		}
		if forcePending {
			_, err = tx.Exec(ctx, `UPDATE code_review_requests SET status='superseded' WHERE org_id=$1 AND id=$2 AND status='pending'`, req.OrgID, requestID)
			result = ScheduleRequestResult{RequestID: req.RequestID, Disposition: models.CodeReviewRequestCancelled}
			return err
		}
		if current.Status == models.CodeReviewAssessmentReserved || current.Status == models.CodeReviewAssessmentRunning || current.Status == models.CodeReviewAssessmentPublishing {
			if current.InputDigest == captured.Manifest.InputDigest {
				_, err = tx.Exec(ctx, `UPDATE code_review_requests SET status='joined',assessment_id=$3,session_id=$4 WHERE org_id=$1 AND id=$2 AND status='pending'`, req.OrgID, requestID, current.ID, current.SessionID)
				result = ScheduleRequestResult{RequestID: req.RequestID, AssessmentID: &current.ID, SessionID: &current.SessionID, Disposition: models.CodeReviewRequestJoined}
				return err
			}
			result = ScheduleRequestResult{RequestID: req.RequestID, Disposition: models.CodeReviewRequestQueued}
			return queuePendingAssessmentInTx(ctx, s, tx, state, req, requestContext, requestID)
		}
		active, err := db.HasActiveCodeReview(ctx, tx, req.OrgID, req.PullRequestID, codeReviewJobEnqueueGracePeriod)
		if err != nil {
			return err
		}
		if active {
			result = ScheduleRequestResult{RequestID: req.RequestID, Disposition: models.CodeReviewRequestQueued}
			return queuePendingAssessmentInTx(ctx, s, tx, state, req, requestContext, requestID)
		}
		facts := baselineForPlanning(currentBaseline, resolved.Config, captured.Files)
		manifest, decodeErr := decodeAssessmentManifest(current.InputManifest)
		var predecessor *RecheckPrevious
		if decodeErr == nil {
			predecessor = &RecheckPrevious{Inputs: manifest, Completed: current.Status == models.CodeReviewAssessmentCompleted, EvidenceValidated: current.Status == models.CodeReviewAssessmentCompleted && len(current.StructuredOutcome) > 0}
		}
		lockedPlan := PlanReviewRecheck(RecheckPlanInput{Current: &captured.Manifest, Baseline: facts, Previous: predecessor})
		if lockedPlan != plan {
			return errRecheckAdmissionChanged
		}
		if lockedPlan.Route == RecheckRouteFull {
			return errRecheckNeedsFull
		}
		if lockedPlan.Route == RecheckRouteReuse {
			_, err = tx.Exec(ctx, `UPDATE code_review_requests SET status='satisfied',assessment_id=$3,session_id=$4 WHERE org_id=$1 AND id=$2 AND status='pending'`, req.OrgID, requestID, current.ID, current.SessionID)
			if err != nil {
				return err
			}
			state.CurrentAssessmentID = &current.ID
			result = ScheduleRequestResult{RequestID: req.RequestID, AssessmentID: &current.ID, SessionID: &current.SessionID, Disposition: models.CodeReviewRequestReused}
			return nil
		}
		if lockedPlan.Route != RecheckRouteEvidenceOnly {
			return errRecheckAdmissionChanged
		}
		if captured.VisualEvidence.AssessmentID == nil || *captured.VisualEvidence.AssessmentID == uuid.Nil {
			return errRecheckAdmissionChanged
		}
		assessmentID := *captured.VisualEvidence.AssessmentID
		manifestRaw, err := json.Marshal(captured.Manifest)
		if err != nil {
			return err
		}
		generation := current.Generation + 1
		var priorPublished *uuid.UUID
		published, err := assessments.GetLatestPublishedForPR(ctx, req.OrgID, req.PullRequestID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			priorPublished = &published.ID
		}
		capture := models.CodeReviewAssessmentCapture{ID: assessmentID, OrgID: req.OrgID, RepositoryID: state.RepositoryID, PullRequestID: req.PullRequestID, PolicyID: captured.Policy.ID, SessionID: currentBaseline.SessionID, Generation: generation, SourceAssessmentID: &currentBaseline.ID, PreviousAssessmentID: &current.ID, PreviousPublishedAssessmentID: priorPublished,
			BaseSHA: captured.Manifest.Code.BaseSHA, BaseRef: captured.Manifest.Code.BaseRef, HeadSHA: captured.Manifest.Code.HeadSHA, InputVersion: captured.Manifest.InputVersion,
			CodeDigest: captured.Manifest.CodeDigest, ContractDigest: captured.Manifest.ContractDigest, IntentDigest: captured.Manifest.IntentDigest, VisualDigest: captured.Manifest.VisualDigest, RequestDigest: captured.Manifest.RequestDigest, GateDigest: captured.Manifest.GateDigest, InputDigest: captured.Manifest.InputDigest, InputManifest: manifestRaw,
			ReviewScope: models.CodeReviewScopeEvidenceOnly, RouteReason: models.CodeReviewRouteVisualChanged, PublicationKey: "code-review-assessment:" + assessmentID.String()}
		_, _, err = assessments.Create(ctx, capture)
		if err != nil {
			return err
		}
		if err = assessments.LinkVisualEvidence(ctx, req.OrgID, assessmentID); err != nil {
			return err
		}
		_, err = db.NewJobStore(tx).EnqueueWithOpts(ctx, req.OrgID, db.EnqueueOpts{Queue: "agent", JobType: models.JobTypeRunCodeReviewRecheck, Payload: struct {
			OrgID        uuid.UUID `json:"org_id"`
			AssessmentID uuid.UUID `json:"assessment_id"`
		}{req.OrgID, assessmentID}, DedupeKey: recheckDedupePtr("code_review_recheck:" + assessmentID.String()), Priority: 5, MaxAttempts: 8})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE code_review_requests SET assessment_id=$3,session_id=$4,target_generation=$5 WHERE org_id=$1 AND id=$2`, req.OrgID, requestID, assessmentID, currentBaseline.SessionID, generation)
		if err != nil {
			return err
		}
		state.ActiveAssessmentID = &assessmentID
		state.CurrentAssessmentID = &assessmentID
		state.ActiveSessionID = &currentBaseline.SessionID
		state.Generation = generation
		state.State = models.CodeReviewScheduleRunning
		state.WaitReason = models.CodeReviewWaitNone
		state.PendingInput = nil
		state.PendingRequestID = nil
		state.FirstPendingAt = nil
		state.EligibleAt = nil
		state.RetryAt = nil
		result = ScheduleRequestResult{RequestID: req.RequestID, AssessmentID: &assessmentID, SourceAssessmentID: &currentBaseline.ID, SessionID: &currentBaseline.SessionID, Disposition: models.CodeReviewRequestQueued}
		return nil
	})
	if err != nil {
		return ScheduleRequestResult{}, err
	}
	state, err := s.GetSchedule(ctx, req.OrgID, req.PullRequestID)
	if err != nil {
		return ScheduleRequestResult{}, err
	}
	result.Schedule = state
	return result, nil
}

func recheckDedupePtr(v string) *string { return &v }

func queuePendingAssessmentInTx(ctx context.Context, s *Service, tx pgx.Tx, state *models.CodeReviewPRState, req ScheduleRequestInput, requestContext *ReviewRequestContext, requestID uuid.UUID) error {
	if state.PendingRequestID != nil && *state.PendingRequestID != requestID {
		if _, err := tx.Exec(ctx, `UPDATE code_review_requests SET status='superseded' WHERE org_id=$1 AND id=$2 AND status='pending'`, req.OrgID, *state.PendingRequestID); err != nil {
			return err
		}
	}
	source := req.TriggerSource
	if source == "" {
		source = models.CodeReviewTriggerSourceSlashCommand
	}
	input := ReviewChangedInput{OrgID: req.OrgID, RepositoryID: state.RepositoryID, PullRequestID: req.PullRequestID, ExplicitRequest: true, GitHubDeliveryID: req.RequestID.String(), RequestContext: requestContext, TriggerSource: source}
	var err error
	state.PendingInput, err = db.EncodeCodeReviewScheduleInput(scheduledReviewIntent{Input: input, Mode: models.CodeReviewRecheck, RequesterID: req.RequesterID})
	if err != nil {
		return err
	}
	state.PendingRequestID = &requestID
	now := s.scheduling.now()
	state.FirstPendingAt = &now
	state.State = models.CodeReviewScheduleWaiting
	state.WaitReason = models.CodeReviewWaitActive
	retry := now.Add(15 * time.Second)
	state.RetryAt = &retry
	return db.UpsertCodeReviewWake(ctx, tx, req.OrgID, req.PullRequestID, retry)
}

// FallbackAssessmentToFull is the supervisor's idempotent recovery path after
// it has terminally failed or superseded an assessment. The caller must
// record the terminal outcome before invoking this method.
func (s *Service) FallbackAssessmentToFull(ctx context.Context, orgID, assessmentID uuid.UUID, reason string) error {
	if strings.TrimSpace(reason) == "" || utf8.RuneCountInString(reason) > 2000 {
		return errors.New("fallback reason must be 1 to 2000 characters")
	}
	if !s.SchedulingEnabled() {
		return errors.New("review scheduling unavailable")
	}
	a, err := s.scheduling.store.GetAssessmentByID(ctx, orgID, assessmentID)
	if err != nil {
		return err
	}
	if (a.ReviewScope != models.CodeReviewScopeEvidenceOnly && a.ReviewScope != models.CodeReviewScopeFull) || (a.Status != models.CodeReviewAssessmentFailed && a.Status != models.CodeReviewAssessmentCompleted && a.Status != models.CodeReviewAssessmentSuperseded) {
		return errors.New("assessment must be terminal before a full fallback")
	}
	requestID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("code-review-full-fallback:"+assessmentID.String()))
	_, err = s.requestFullAssessmentReview(ctx, ScheduleRequestInput{OrgID: orgID, PullRequestID: a.PullRequestID, RequestID: requestID, Mode: models.CodeReviewForceFresh, Reason: reason}, a.RepositoryID, &ReviewRequestContext{Source: "assessment_fallback", Body: reason}, true, false)
	if err != nil {
		return err
	}
	return s.scheduling.store.MarkFallbackQueued(ctx, orgID, assessmentID)
}
