package codereview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/llm"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

const (
	codeReviewDisputeEvaluatorVersion = "phase1a-v1"
	codeReviewDisputeConfidenceFloor  = 0.72
)

var (
	ErrCodeReviewDisputeNotReady       = errors.New("code review decision is not ready to dispute")
	ErrCodeReviewDisputeNotConfigured  = errors.New("code review dispute service is not configured")
	ErrCodeReviewDisputeNotEscalatable = errors.New("code review dispute is not eligible for escalation")
	ErrCodeReviewDisputeInvalidBody    = errors.New("invalid code review dispute body")
)

type DisputeStore interface {
	CreateAndEnqueueTriage(ctx context.Context, dispute *models.CodeReviewDispute) (bool, error)
	GetByID(ctx context.Context, orgID, disputeID uuid.UUID) (models.CodeReviewDispute, error)
	ListBySession(ctx context.Context, orgID, sessionID uuid.UUID, cursor *uuid.UUID, limit int) (models.CodeReviewDisputePage, error)
	ListQueue(ctx context.Context, orgID uuid.UUID, filters models.CodeReviewDisputeListFilters) (models.CodeReviewDisputePage, error)
	ListRecentKinds(ctx context.Context, orgID uuid.UUID, limit int) ([]string, error)
	SetTriage(ctx context.Context, orgID, disputeID uuid.UUID, result models.CodeReviewDisputeTriageResult, adjudicationEligible bool, detail string) (models.CodeReviewDispute, error)
	FailTriage(ctx context.Context, orgID, disputeID uuid.UUID, detail string) error
	RecordAuthorization(ctx context.Context, authorization models.CodeReviewDisputeAuthorization) error
	AdmitAndEnqueueReassessment(ctx context.Context, dispute models.CodeReviewDispute, userID *uuid.UUID, semanticHash string, cooldown time.Duration, maxActive int, payload any) (bool, error)
	CompleteReassessment(ctx context.Context, orgID, disputeID, sessionID uuid.UUID, status models.CodeReviewSessionStatus, decision *models.CodeReviewDecision, detail string) error
	MarkHeadChanged(ctx context.Context, orgID, disputeID uuid.UUID, detail string) error
	MarkReassessmentFailed(ctx context.Context, orgID, disputeID uuid.UUID, detail string) error
	Escalate(ctx context.Context, orgID, disputeID, userID uuid.UUID, note string) (models.CodeReviewDispute, error)
	Adjudicate(ctx context.Context, orgID, disputeID, userID uuid.UUID, update models.CodeReviewDisputeAdjudicationUpdate) (models.CodeReviewDispute, error)
}

type DisputeReviewStore interface {
	GetBySessionID(ctx context.Context, orgID, sessionID uuid.UUID) (models.CodeReviewSessionMetadata, error)
	GetLatestCompletedByPullRequest(ctx context.Context, orgID, pullRequestID uuid.UUID) (models.CodeReviewSessionMetadata, error)
	GetByGitHubFindingComment(ctx context.Context, orgID uuid.UUID, githubCommentID int64) (models.CodeReviewSessionMetadata, error)
	GetListItemBySessionID(ctx context.Context, orgID, sessionID uuid.UUID) (models.CodeReviewListItem, error)
	GetRiskReasonCodesBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.CodeReviewRiskReasonCode, error)
	ListFindings(ctx context.Context, orgID, sessionID uuid.UUID, selectedOnly bool) ([]models.CodeReviewFinding, error)
}

type disputePolicyStore interface {
	GetPolicyByID(ctx context.Context, orgID, policyID uuid.UUID) (models.CodeReviewPolicyRecord, error)
}

type disputeRiskReasonStore interface {
	GetRiskReasonsBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.CodeReviewRiskReason, error)
}

type DisputeJobStore interface {
	EnqueueWithOpts(ctx context.Context, orgID uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error)
}

type DisputeService struct {
	disputes     DisputeStore
	reviews      DisputeReviewStore
	pullRequests PullRequestStore
	jobs         DisputeJobStore
	llm          llm.Client
	logger       zerolog.Logger
	audit        *db.AuditEmitter
	frontendURL  string
	config       DisputeConfig
}

// SetAuditEmitter enables best-effort audit records for webhook-filed disputes.
func (s *DisputeService) SetAuditEmitter(audit *db.AuditEmitter) {
	s.audit = audit
}

type DisputeConfig struct {
	ReassessmentsEnabled   bool
	MaxActiveReassessments int
}

func NewDisputeService(disputes DisputeStore, reviews DisputeReviewStore, pullRequests PullRequestStore, jobs DisputeJobStore, llmClient llm.Client, frontendURL string, logger zerolog.Logger, configs ...DisputeConfig) *DisputeService {
	config := DisputeConfig{ReassessmentsEnabled: true, MaxActiveReassessments: 1000}
	if len(configs) > 0 {
		config = configs[0]
	}
	return &DisputeService{disputes: disputes, reviews: reviews, pullRequests: pullRequests, jobs: jobs, llm: llmClient, frontendURL: strings.TrimRight(strings.TrimSpace(frontendURL), "/"), logger: logger, config: config}
}

type FileCodeReviewDisputeInput struct {
	OrgID                uuid.UUID
	SessionID            uuid.UUID
	FiledByUserID        *uuid.UUID
	FiledByLogin         string
	AuthorAssociation    string
	RepositoryVisibility string
	Body                 string
	ContestedReasonCodes []models.CodeReviewRiskReasonCode
	Source               models.CodeReviewDisputeSource
	GitHubCommentID      *int64
	GitHubThreadRootID   *int64
	SourceVersion        int64
}

type FileGitHubCodeReviewDisputeInput struct {
	OrgID              uuid.UUID
	PullRequestID      uuid.UUID
	InlineThreadRootID *int64
	AuthorLogin        string
	AuthorType         models.PRFeedbackAuthorType
	AuthorAssociation  string
	RepositoryPrivate  bool
	OwnAppLogin        string
	Body               string
	GitHubCommentID    int64
	SourceVersion      int64
}

func (s *DisputeService) FileInApp(ctx context.Context, input FileCodeReviewDisputeInput) (models.CodeReviewDispute, error) {
	if input.Source == "" {
		input.Source = models.CodeReviewDisputeSourceAppUI
	}
	if input.AuthorAssociation == "" {
		input.AuthorAssociation = "MEMBER"
	}
	if input.RepositoryVisibility == "" {
		input.RepositoryVisibility = "unknown"
	}
	return s.fileAgainstSession(ctx, input)
}

func (s *DisputeService) FileFromGitHub(ctx context.Context, input FileGitHubCodeReviewDisputeInput) (models.CodeReviewDispute, bool, error) {
	if s == nil || s.disputes == nil || s.reviews == nil || s.pullRequests == nil {
		return models.CodeReviewDispute{}, false, ErrCodeReviewDisputeNotConfigured
	}
	provenance := ghservice.EvaluatePRFeedbackProvenance(ghservice.PRFeedbackProvenanceInput{
		PrivateRepo: input.RepositoryPrivate, AuthorLogin: input.AuthorLogin, AuthorType: input.AuthorType,
		Association: strings.ToUpper(strings.TrimSpace(input.AuthorAssociation)), OwnAppLogin: input.OwnAppLogin, Body: input.Body,
	})
	if !provenance.Recordable {
		return models.CodeReviewDispute{}, false, nil
	}
	var review models.CodeReviewSessionMetadata
	var err error
	if input.InlineThreadRootID != nil {
		review, err = s.reviews.GetByGitHubFindingComment(ctx, input.OrgID, *input.InlineThreadRootID)
	} else {
		review, err = s.reviews.GetLatestCompletedByPullRequest(ctx, input.OrgID, input.PullRequestID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return models.CodeReviewDispute{}, false, nil
	}
	if err != nil {
		return models.CodeReviewDispute{}, false, err
	}
	visibility := "public"
	if input.RepositoryPrivate {
		visibility = "private"
	}
	commentID := input.GitHubCommentID
	dispute, created, err := s.fileAgainstSessionResult(ctx, FileCodeReviewDisputeInput{
		OrgID: input.OrgID, SessionID: review.SessionID, FiledByLogin: input.AuthorLogin,
		AuthorAssociation: input.AuthorAssociation, RepositoryVisibility: visibility,
		Body: input.Body, Source: models.CodeReviewDisputeSourceGitHubComment,
		GitHubCommentID: &commentID, GitHubThreadRootID: input.InlineThreadRootID,
		SourceVersion: input.SourceVersion,
	})
	if err == nil && created && s.audit != nil {
		resourceID := dispute.ID.String()
		details, marshalErr := json.Marshal(map[string]any{
			"github_comment_id": input.GitHubCommentID,
			"source_version":    input.SourceVersion,
		})
		if marshalErr != nil {
			s.logger.Warn().Err(marshalErr).Str("dispute_id", resourceID).Msg("failed to marshal GitHub dispute audit details")
		} else {
			s.audit.EmitWebhookAction(ctx, db.WebhookActionParams{
				OrgID: input.OrgID, ProviderName: "github",
				Action: models.AuditActionCodeReviewDisputeFiled, ResourceType: models.AuditResourceCodeReviewDispute,
				ResourceID: &resourceID, Details: details, SessionID: &dispute.SessionID,
			})
		}
	}
	return dispute, err == nil, err
}

func (s *DisputeService) fileAgainstSession(ctx context.Context, input FileCodeReviewDisputeInput) (models.CodeReviewDispute, error) {
	dispute, _, err := s.fileAgainstSessionResult(ctx, input)
	return dispute, err
}

func (s *DisputeService) fileAgainstSessionResult(ctx context.Context, input FileCodeReviewDisputeInput) (models.CodeReviewDispute, bool, error) {
	if s == nil || s.disputes == nil || s.reviews == nil || s.pullRequests == nil {
		return models.CodeReviewDispute{}, false, ErrCodeReviewDisputeNotConfigured
	}
	body := strings.TrimSpace(input.Body)
	if body == "" || !utf8.ValidString(body) || utf8.RuneCountInString(body) > models.CodeReviewDisputeBodyMaxRunes {
		return models.CodeReviewDispute{}, false, fmt.Errorf("%w: body must be valid UTF-8 with 1-%d characters", ErrCodeReviewDisputeInvalidBody, models.CodeReviewDisputeBodyMaxRunes)
	}
	if err := input.Source.Validate(); err != nil {
		return models.CodeReviewDispute{}, false, err
	}
	review, err := s.reviews.GetBySessionID(ctx, input.OrgID, input.SessionID)
	if err != nil {
		return models.CodeReviewDispute{}, false, err
	}
	if review.Status != models.CodeReviewSessionStatusCompleted || review.Decision == nil {
		return models.CodeReviewDispute{}, false, ErrCodeReviewDisputeNotReady
	}
	item, err := s.reviews.GetListItemBySessionID(ctx, input.OrgID, input.SessionID)
	if err != nil {
		return models.CodeReviewDispute{}, false, err
	}
	pullRequest, err := s.pullRequests.GetByID(ctx, input.OrgID, review.PullRequestID)
	if err != nil {
		return models.CodeReviewDispute{}, false, err
	}
	reasonCodes, err := s.reviews.GetRiskReasonCodesBySession(ctx, input.OrgID, input.SessionID)
	if err != nil {
		return models.CodeReviewDispute{}, false, err
	}
	contested := validContestedReasonCodes(input.ContestedReasonCodes, reasonCodes)
	version := input.SourceVersion
	if version <= 0 {
		version = 1
	}
	bodyHash := sha256.Sum256([]byte(body))
	semanticHash := codeReviewDisputeSemanticHash(review.HeadSHA, review.PolicyID, body, pullRequest.Title, stringPtr(pullRequest.Body))
	evidence, err := json.Marshal(map[string]any{
		"author_association":    strings.ToUpper(strings.TrimSpace(input.AuthorAssociation)),
		"repository_visibility": input.RepositoryVisibility,
		"filed_by_user_id":      input.FiledByUserID,
	})
	if err != nil {
		return models.CodeReviewDispute{}, false, fmt.Errorf("marshal dispute membership evidence: %w", err)
	}
	direction := directionForDecision(*review.Decision)
	replyStatus := models.CodeReviewDisputeReplyPending
	if input.Source != models.CodeReviewDisputeSourceGitHubComment && direction != models.CodeReviewDisputeDirectionShouldNotHaveApproved {
		replyStatus = models.CodeReviewDisputeReplyNotApplicable
	}
	dispute := models.CodeReviewDispute{
		OrgID: input.OrgID, SessionID: review.SessionID, PullRequestID: review.PullRequestID,
		RepositoryID: review.RepositoryID, PolicyID: review.PolicyID, ReviewedHeadSHA: review.HeadSHA,
		Decision: *review.Decision, FiledByUserID: input.FiledByUserID, FiledByLogin: strings.TrimSpace(input.FiledByLogin),
		Direction:            &direction,
		AuthorAssociation:    strings.ToUpper(strings.TrimSpace(input.AuthorAssociation)),
		AuthorIsPRAuthor:     strings.EqualFold(strings.TrimSpace(input.FiledByLogin), strings.TrimSpace(item.PullRequestAuthor)),
		RepositoryVisibility: models.CodeReviewRepositoryVisibility(input.RepositoryVisibility), MembershipEvidence: evidence, Source: input.Source,
		GitHubCommentID: input.GitHubCommentID, GitHubThreadRootCommentID: input.GitHubThreadRootID,
		SourceBodyHash: hex.EncodeToString(bodyHash[:]), SourceVersion: version, Body: body,
		ContestedReasonCodes: contested, SemanticInputHashAtFiling: semanticHash, ReplyStatus: replyStatus,
	}
	trustedAtFiling, _ := dispute.CurrentTrust()
	queueSignals, err := json.Marshal(map[string]any{
		"trusted_at_filing":   trustedAtFiling,
		"filer_is_pr_author":  dispute.AuthorIsPRAuthor,
		"pull_request_author": strings.TrimSpace(item.PullRequestAuthor),
		"pull_request_title":  strings.TrimSpace(item.PullRequestTitle),
		"github_pr_number":    item.GitHubPRNumber,
		"github_pr_url":       strings.TrimSpace(item.GitHubPRURL),
		"github_repository":   strings.TrimSpace(item.GitHubRepo),
		"source":              dispute.Source,
		"reason_codes":        dispute.ContestedReasonCodes,
	})
	if err != nil {
		return models.CodeReviewDispute{}, false, fmt.Errorf("marshal code review dispute queue signals: %w", err)
	}
	dispute.QueueSignals = queueSignals
	created, err := s.disputes.CreateAndEnqueueTriage(ctx, &dispute)
	if err != nil {
		return models.CodeReviewDispute{}, false, err
	}
	return enrichCodeReviewDisputeTrust(dispute), created, nil
}

func validContestedReasonCodes(requested, available []models.CodeReviewRiskReasonCode) []models.CodeReviewRiskReasonCode {
	availableSet := make(map[models.CodeReviewRiskReasonCode]struct{}, len(available))
	for _, code := range available {
		availableSet[code] = struct{}{}
	}
	seen := make(map[models.CodeReviewRiskReasonCode]struct{}, len(requested))
	result := make([]models.CodeReviewRiskReasonCode, 0, len(requested))
	for _, code := range requested {
		if code.Validate() != nil {
			continue
		}
		if _, ok := availableSet[code]; !ok {
			continue
		}
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		result = append(result, code)
	}
	return result
}

func (s *DisputeService) Triage(ctx context.Context, orgID, disputeID uuid.UUID) error {
	dispute, err := s.disputes.GetByID(ctx, orgID, disputeID)
	if err != nil {
		return err
	}
	if dispute.IntakeStatus != models.CodeReviewDisputeIntakePending {
		return s.completeTriagedWorkflow(ctx, dispute)
	}
	reasons, err := s.reviews.GetRiskReasonCodesBySession(ctx, orgID, dispute.SessionID)
	if err != nil {
		return err
	}
	reviewContext, err := s.reviews.GetListItemBySessionID(ctx, orgID, dispute.SessionID)
	if err != nil {
		return err
	}
	existingKinds, err := s.disputes.ListRecentKinds(ctx, orgID, 50)
	if err != nil {
		return err
	}
	var findings []models.CodeReviewFinding
	if dispute.Source == models.CodeReviewDisputeSourceGitHubComment {
		findings, err = s.reviews.ListFindings(ctx, orgID, dispute.SessionID, false)
		if err != nil {
			return err
		}
	}
	result, err := s.triageResult(ctx, dispute, reasons, reviewContext, findings, existingKinds)
	if err != nil {
		return err
	}
	result.Direction = directionForDecision(dispute.Decision)
	result.ContestedReasonCodes = validContestedReasonCodes(result.ContestedReasonCodes, reasons)
	if len(result.ContestedReasonCodes) == 0 {
		result.ContestedReasonCodes = append([]models.CodeReviewRiskReasonCode(nil), dispute.ContestedReasonCodes...)
	}
	trusted, trustReason := dispute.CurrentTrust()
	if result.Direction == models.CodeReviewDisputeDirectionShouldNotHaveApproved {
		result.Routing = models.CodeReviewDisputeRoutingPolicySignalOnly
	}
	if result.Confidence < codeReviewDisputeConfidenceFloor && result.Routing == models.CodeReviewDisputeRoutingReassess {
		result.Routing = models.CodeReviewDisputeRoutingPolicySignalOnly
		result.Reply = "We weren't sure what part of the decision you were disputing. File explicitly in 143 to request reconsideration."
	}
	if result.Routing == models.CodeReviewDisputeRoutingReassess && (!trusted || !dispute.AuthorIsPRAuthor) {
		result.Routing = models.CodeReviewDisputeRoutingPolicySignalOnly
		if !trusted {
			result.Reply = "External contributors can't trigger a re-review. Ask a maintainer to re-request one."
		} else {
			result.Reply = "Only the pull request author can trigger an automatic re-review right now. The objection was recorded for a policy owner."
		}
	}
	if result.Routing == models.CodeReviewDisputeRoutingReassess {
		switch {
		case !s.config.ReassessmentsEnabled:
			result.Routing = models.CodeReviewDisputeRoutingPolicySignalOnly
			result.Reply = "The objection was recorded, but automatic reassessment is temporarily unavailable."
		}
	}
	if result.Routing != models.CodeReviewDisputeRoutingAnswerOnly && result.Routing != models.CodeReviewDisputeRoutingNotADispute && onlyDeterministicReasons(result.ContestedReasonCodes) {
		result.Routing = models.CodeReviewDisputeRoutingPolicySignalOnly
		var details []models.CodeReviewRiskReason
		if riskReasons, ok := s.reviews.(disputeRiskReasonStore); ok {
			details, err = riskReasons.GetRiskReasonsBySession(ctx, orgID, dispute.SessionID)
			if err != nil {
				return err
			}
		}
		result.Reply = deterministicPolicySignalReply(result.ContestedReasonCodes, details)
	}
	adjudicationEligible := trusted && result.Routing != models.CodeReviewDisputeRoutingAnswerOnly && result.Routing != models.CodeReviewDisputeRoutingNotADispute
	detail := boundedGeneratedReply(result.Reply)
	updated, err := s.disputes.SetTriage(ctx, orgID, disputeID, result, adjudicationEligible, detail)
	if err != nil {
		return err
	}
	return s.completeTriagedWorkflowWithTrust(ctx, updated, trustReason)
}

func (s *DisputeService) completeTriagedWorkflow(ctx context.Context, dispute models.CodeReviewDispute) error {
	_, trustReason := dispute.CurrentTrust()
	return s.completeTriagedWorkflowWithTrust(ctx, dispute, trustReason)
}

func (s *DisputeService) completeTriagedWorkflowWithTrust(ctx context.Context, dispute models.CodeReviewDispute, trustReason string) error {
	if dispute.IntakeStatus == models.CodeReviewDisputeIntakeDiscarded {
		return s.EnqueueReply(ctx, dispute.OrgID, dispute.ID, "triaged")
	}
	if dispute.IntakeStatus != models.CodeReviewDisputeIntakeTriaged || dispute.Routing == nil {
		return nil
	}
	adjudicationEligible := dispute.AdjudicationStatus != nil
	if err := s.recordAuthorization(ctx, dispute, models.CodeReviewDisputeAuthorizationQueueInfluence, adjudicationEligible, trustReason); err != nil {
		return err
	}
	if *dispute.Routing == models.CodeReviewDisputeRoutingReassess {
		if err := s.recordAuthorization(ctx, dispute, models.CodeReviewDisputeAuthorizationRerun, true, trustReason); err != nil {
			return err
		}
		if dispute.ReassessmentStatus == models.CodeReviewDisputeReassessmentNotRequested {
			if err := s.queueReassessment(ctx, dispute); err != nil {
				return err
			}
		}
	}
	return s.EnqueueReply(ctx, dispute.OrgID, dispute.ID, "triaged")
}

func (s *DisputeService) FailTriage(ctx context.Context, orgID, disputeID uuid.UUID, detail string) error {
	dispute, err := s.disputes.GetByID(ctx, orgID, disputeID)
	if err != nil {
		return err
	}
	detail = boundedGeneratedReply(detail)
	if dispute.IntakeStatus == models.CodeReviewDisputeIntakePending {
		err = s.disputes.FailTriage(ctx, orgID, disputeID, detail)
	} else if dispute.IntakeStatus == models.CodeReviewDisputeIntakeTriaged && dispute.Routing != nil && *dispute.Routing == models.CodeReviewDisputeRoutingReassess {
		err = s.disputes.MarkReassessmentFailed(ctx, orgID, disputeID, detail)
	}
	if err != nil {
		return err
	}
	return s.EnqueueReply(ctx, orgID, disputeID, "triage_failed")
}

func (s *DisputeService) triageResult(ctx context.Context, dispute models.CodeReviewDispute, reasons []models.CodeReviewRiskReasonCode, review models.CodeReviewListItem, findings []models.CodeReviewFinding, existingKinds []string) (models.CodeReviewDisputeTriageResult, error) {
	direction := directionForDecision(dispute.Decision)
	if dispute.Source == models.CodeReviewDisputeSourceAppUI || dispute.Source == models.CodeReviewDisputeSourceAPI {
		routing := models.CodeReviewDisputeRoutingReassess
		if direction == models.CodeReviewDisputeDirectionShouldNotHaveApproved || onlyDeterministicReasons(dispute.ContestedReasonCodes) {
			routing = models.CodeReviewDisputeRoutingPolicySignalOnly
		}
		return models.CodeReviewDisputeTriageResult{Direction: direction, ContestedReasonCodes: dispute.ContestedReasonCodes, DisputeKind: "explicit_reconsideration", AssertsNewInformation: true, Routing: routing, Confidence: 1}, nil
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(dispute.Body), " "))
	trimmed := strings.Trim(normalized, " .,!?:;")
	if trimmed == "thanks" || trimmed == "thank you" || trimmed == "lgtm" || trimmed == "looks good" || trimmed == "noted" {
		return models.CodeReviewDisputeTriageResult{Direction: direction, DisputeKind: "acknowledgement", Routing: models.CodeReviewDisputeRoutingNotADispute, Confidence: 1, Reply: "Noted. If you meant to challenge this decision, use the reconsideration action in 143."}, nil
	}
	deterministicRouteHint := ""
	if strings.Contains(dispute.Body, "?") && !containsDisagreementLanguage(normalized) {
		deterministicRouteHint = string(models.CodeReviewDisputeRoutingAnswerOnly)
	}
	if deterministicRouteHint == string(models.CodeReviewDisputeRoutingAnswerOnly) && s.llm == nil {
		return models.CodeReviewDisputeTriageResult{Direction: direction, DisputeKind: "explanation_question", Routing: models.CodeReviewDisputeRoutingAnswerOnly, Confidence: 0.95, Reply: "The review evidence and policy blockers are linked from the latest 143 review comment."}, nil
	}
	if s.llm == nil {
		return models.CodeReviewDisputeTriageResult{}, fmt.Errorf("LLM is unavailable for code review dispute triage")
	}
	findingContext := codeReviewDisputeFindingContext(dispute.GitHubThreadRootCommentID, findings)
	payload, err := json.Marshal(map[string]any{
		"decision": dispute.Decision, "reviewed_head_sha": dispute.ReviewedHeadSHA,
		"available_reason_codes": reasons, "selected_reason_codes": dispute.ContestedReasonCodes,
		"author_is_pr_author":        dispute.AuthorIsPRAuthor,
		"pull_request_title":         review.PullRequestTitle,
		"pull_request_author":        review.PullRequestAuthor,
		"review_summary":             stringPtr(review.FinalReviewBody),
		"review_findings":            findingContext,
		"existing_dispute_kinds":     existingKinds,
		"deterministic_route_hint":   deterministicRouteHint,
		"untrusted_dispute_evidence": map[string]any{"body": dispute.Body, "filed_by_login": dispute.FiledByLogin},
	})
	if err != nil {
		return models.CodeReviewDisputeTriageResult{}, fmt.Errorf("marshal code review dispute triage input: %w", err)
	}
	raw, err := s.llm.Complete(ctx, prompts.CodeReviewDisputeTriagePrompt(), string(payload))
	if err != nil {
		return models.CodeReviewDisputeTriageResult{}, fmt.Errorf("triage code review dispute: %w", err)
	}
	var result models.CodeReviewDisputeTriageResult
	if err := json.Unmarshal([]byte(extractJSONObject(raw)), &result); err != nil {
		return result, fmt.Errorf("decode code review dispute triage: %w", err)
	}
	if err := result.Validate(); err != nil {
		return result, fmt.Errorf("validate code review dispute triage: %w", err)
	}
	return result, nil
}

func codeReviewDisputeFindingContext(threadRootCommentID *int64, findings []models.CodeReviewFinding) []map[string]any {
	const maxFindings = 20
	result := make([]map[string]any, 0, min(len(findings), maxFindings))
	for _, finding := range findings {
		if len(result) >= maxFindings {
			break
		}
		result = append(result, map[string]any{
			"severity": finding.Severity, "confidence": finding.Confidence,
			"path": finding.Path, "start_line": finding.StartLine, "end_line": finding.EndLine,
			"summary":           boundedDisputePromptContext(finding.Summary, 400),
			"body":              boundedDisputePromptContext(finding.Body, 1200),
			"is_replied_thread": threadRootCommentID != nil && finding.GitHubCommentID != nil && *threadRootCommentID == *finding.GitHubCommentID,
		})
	}
	return result
}

func boundedDisputePromptContext(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "…"
}

func extractJSONObject(value string) string {
	start := strings.IndexByte(value, '{')
	end := strings.LastIndexByte(value, '}')
	if start >= 0 && end >= start {
		return value[start : end+1]
	}
	return strings.TrimSpace(value)
}

func containsDisagreementLanguage(value string) bool {
	for _, phrase := range []string{"disagree", "should have", "shouldn't", "should not", "wrong", "unsafe", "reconsider", "mistake", "incorrect"} {
		if strings.Contains(value, phrase) {
			return true
		}
	}
	return false
}

// IsLikelyDisputeMention separates plain-language objections and questions
// from bare team mentions that are requesting a normal code review. Inline
// replies do not need this heuristic because their thread root supplies the
// dispute context.
func IsLikelyDisputeMention(body string) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(body), " "))
	return strings.Contains(body, "?") || containsDisagreementLanguage(normalized)
}

func directionForDecision(decision models.CodeReviewDecision) models.CodeReviewDisputeDirection {
	if decision == models.CodeReviewDecisionApproved {
		return models.CodeReviewDisputeDirectionShouldNotHaveApproved
	}
	return models.CodeReviewDisputeDirectionShouldHaveApproved
}

func onlyDeterministicReasons(codes []models.CodeReviewRiskReasonCode) bool {
	if len(codes) == 0 {
		return false
	}
	for _, code := range codes {
		switch code {
		case models.CodeReviewRiskReasonFilesLimitExceeded, models.CodeReviewRiskReasonLinesLimitExceeded,
			models.CodeReviewRiskReasonForkIneligible, models.CodeReviewRiskReasonAuthorIneligible,
			models.CodeReviewRiskReasonSensitivePath, models.CodeReviewRiskReasonPathOutsideScope,
			models.CodeReviewRiskReasonBlockedPath, models.CodeReviewRiskReasonPolicyPathChanged,
			models.CodeReviewRiskReasonExcludedCategory:
		default:
			return false
		}
	}
	return true
}

func deterministicPolicySignalReply(codes []models.CodeReviewRiskReasonCode, details []models.CodeReviewRiskReason) string {
	byCode := make(map[models.CodeReviewRiskReasonCode]models.CodeReviewRiskReason, len(details))
	for _, detail := range details {
		byCode[detail.Code] = detail
	}
	labels := make([]string, 0, len(codes))
	for _, code := range codes {
		detail := byCode[code]
		switch code {
		case models.CodeReviewRiskReasonFilesLimitExceeded:
			labels = append(labels, fmt.Sprintf("Files changed limit is %d (observed %d)", detail.Limit, detail.Actual))
		case models.CodeReviewRiskReasonLinesLimitExceeded:
			labels = append(labels, fmt.Sprintf("Lines changed limit is %d (observed %d)", detail.Limit, detail.Actual))
		case models.CodeReviewRiskReasonForkIneligible:
			labels = append(labels, "Allow forks is off")
		case models.CodeReviewRiskReasonAuthorIneligible:
			labels = append(labels, "Eligible authors excludes this pull request author")
		case models.CodeReviewRiskReasonSensitivePath:
			labels = append(labels, deterministicPathPolicyLabel("Sensitive paths includes", detail.Subject))
		case models.CodeReviewRiskReasonPathOutsideScope:
			labels = append(labels, deterministicPathPolicyLabel("Allowed paths excludes", detail.Subject))
		case models.CodeReviewRiskReasonBlockedPath:
			labels = append(labels, deterministicPathPolicyLabel("Blocked paths includes", detail.Subject))
		case models.CodeReviewRiskReasonPolicyPathChanged:
			labels = append(labels, deterministicPathPolicyLabel("Policy/config path protection includes", detail.Subject))
		case models.CodeReviewRiskReasonExcludedCategory:
			labels = append(labels, deterministicPathPolicyLabel("Excluded categories includes", detail.Subject))
		default:
			labels = append(labels, strings.ReplaceAll(string(code), "_", " "))
		}
	}
	return "This objection concerns deterministic policy: " + strings.Join(labels, "; ") + ". Reassessment would apply the same rule, so it was recorded for a policy owner instead."
}

func deterministicPathPolicyLabel(prefix, subject string) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return prefix + " the changed path"
	}
	return prefix + " `" + boundedDisputePromptContext(subject, 200) + "`"
}

func (s *DisputeService) recordAuthorization(ctx context.Context, dispute models.CodeReviewDispute, action models.CodeReviewDisputeAuthorizationAction, trusted bool, reason string) error {
	return s.recordAuthorizationWithActor(ctx, dispute, action, trusted, reason, nil)
}

func (s *DisputeService) recordAuthorizationWithActor(ctx context.Context, dispute models.CodeReviewDispute, action models.CodeReviewDisputeAuthorizationAction, trusted bool, reason string, actorID *uuid.UUID) error {
	inputs, err := json.Marshal(map[string]any{
		"author_association": dispute.AuthorAssociation, "repository_visibility": dispute.RepositoryVisibility,
		"author_is_pr_author": dispute.AuthorIsPRAuthor, "membership_evidence": dispute.MembershipEvidence,
	})
	if err != nil {
		return fmt.Errorf("marshal code review dispute authorization inputs: %w", err)
	}
	return s.disputes.RecordAuthorization(ctx, models.CodeReviewDisputeAuthorization{
		OrgID: dispute.OrgID, DisputeID: dispute.ID, Action: action, Trusted: trusted,
		ObservedInputs: inputs, EvaluatorVersion: codeReviewDisputeEvaluatorVersion,
		OverrideValue: dispute.TrustOverride, OverrideByUserID: actorID, DecisionReason: reason,
	})
}

func (s *DisputeService) queueReassessment(ctx context.Context, dispute models.CodeReviewDispute) error {
	pr, err := s.pullRequests.GetByID(ctx, dispute.OrgID, dispute.PullRequestID)
	if err != nil {
		return err
	}
	liveHead := strings.TrimSpace(stringPtr(pr.HeadSHA))
	if liveHead == "" || liveHead != strings.TrimSpace(dispute.ReviewedHeadSHA) {
		detail := fmt.Sprintf("The pull request changed after this objection was filed; the current review covers %s.", shortCodeReviewSHA(liveHead))
		if err := s.disputes.MarkHeadChanged(ctx, dispute.OrgID, dispute.ID, detail); err != nil {
			return err
		}
		return nil
	}
	semanticHash := codeReviewDisputeSemanticHash(liveHead, dispute.PolicyID, dispute.Body, pr.Title, stringPtr(pr.Body))
	changeKey := "dispute:" + dispute.ID.String()
	payload := ReviewChangedInput{
		OrgID: dispute.OrgID, RepositoryID: dispute.RepositoryID, PullRequestID: dispute.PullRequestID,
		GitHubRepo: pr.GitHubRepo, GitHubPRNumber: pr.GitHubPRNumber, GitHubPRURL: pr.GitHubPRURL,
		PullRequestTitle: pr.Title, BaseSHA: stringPtr(pr.BaseSHA), HeadSHA: liveHead,
		ChangeKey: changeKey, ChangeReason: "code_review_dispute.reassessment", ExplicitRequest: true,
		TriggerSource: models.CodeReviewTriggerSourceDisputeReassessment, TriggeringDisputeID: &dispute.ID,
		RequestContext: &ReviewRequestContext{Source: "code_review_dispute", AuthorLogin: dispute.FiledByLogin, Body: dispute.Body},
	}
	cooldown := time.Duration(models.DefaultCodeReviewSemanticDedupeCooldownSeconds) * time.Second
	if policies, ok := s.reviews.(disputePolicyStore); ok {
		policy, policyErr := policies.GetPolicyByID(ctx, dispute.OrgID, dispute.PolicyID)
		if policyErr != nil {
			return policyErr
		}
		cooldown = time.Duration(policy.Config().RiskPolicy.SemanticDedupeCooldownSeconds) * time.Second
	}
	_, err = s.disputes.AdmitAndEnqueueReassessment(ctx, dispute, dispute.FiledByUserID, semanticHash, cooldown, s.config.MaxActiveReassessments, payload)
	return err
}

func codeReviewDisputeSemanticHash(head string, policyID uuid.UUID, evidence, title, body string) string {
	normalizeText := func(value string) string {
		return strings.ToLower(strings.Join(strings.Fields(value), " "))
	}
	normalized := []string{strings.ToLower(strings.TrimSpace(head)), policyID.String(), normalizeText(evidence), normalizeText(title), normalizeText(body)}
	sum := sha256.Sum256([]byte(strings.Join(normalized, "\x00")))
	return hex.EncodeToString(sum[:])
}

func stringPtr(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func shortCodeReviewSHA(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 7 {
		return value[:7]
	}
	if value == "" {
		return "the latest commit"
	}
	return value
}

func boundedGeneratedReply(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "<!--", ""))
	runes := []rune(value)
	if len(runes) > 600 {
		value = string(runes[:600])
	}
	return value
}

func (s *DisputeService) EnqueueReply(ctx context.Context, orgID, disputeID uuid.UUID, stage string) error {
	if s.jobs == nil {
		return nil
	}
	dedupeKey := "reply_code_review_dispute:" + disputeID.String() + ":" + strings.TrimSpace(stage)
	_, err := s.jobs.EnqueueWithOpts(ctx, orgID, db.EnqueueOpts{
		Queue: "feedback", JobType: models.JobTypeReplyCodeReviewDispute,
		Payload:  map[string]any{"org_id": orgID, "dispute_id": disputeID},
		Priority: 4, DedupeKey: &dedupeKey, MaxAttempts: 6,
	})
	if err != nil {
		return fmt.Errorf("enqueue code review dispute reply: %w", err)
	}
	return nil
}

func (s *DisputeService) BuildReply(ctx context.Context, orgID, disputeID uuid.UUID) (models.CodeReviewDispute, string, error) {
	dispute, err := s.disputes.GetByID(ctx, orgID, disputeID)
	if err != nil {
		return dispute, "", err
	}
	if dispute.ReassessmentSessionID != nil &&
		(dispute.ReassessmentStatus == models.CodeReviewDisputeReassessmentQueued || dispute.ReassessmentStatus == models.CodeReviewDisputeReassessmentRunning) {
		metadata, metadataErr := s.reviews.GetBySessionID(ctx, orgID, *dispute.ReassessmentSessionID)
		if metadataErr != nil {
			return dispute, "", fmt.Errorf("load reassessment while building dispute reply: %w", metadataErr)
		}
		if codeReviewDisputeReviewTerminal(metadata.Status) {
			detail := strings.TrimSpace(stringPtr(metadata.StatusMessage))
			if completeErr := s.disputes.CompleteReassessment(ctx, orgID, disputeID, *dispute.ReassessmentSessionID, metadata.Status, metadata.Decision, detail); completeErr != nil {
				return dispute, "", completeErr
			}
			dispute, err = s.disputes.GetByID(ctx, orgID, disputeID)
			if err != nil {
				return dispute, "", err
			}
		}
	}
	return enrichCodeReviewDisputeTrust(dispute), buildCodeReviewDisputeReply(dispute, s.frontendURL), nil
}

func codeReviewDisputeReviewTerminal(status models.CodeReviewSessionStatus) bool {
	switch status {
	case models.CodeReviewSessionStatusCompleted, models.CodeReviewSessionStatusFailed,
		models.CodeReviewSessionStatusStale, models.CodeReviewSessionStatusCancelled:
		return true
	default:
		return false
	}
}

func buildCodeReviewDisputeReply(dispute models.CodeReviewDispute, frontendURL string) string {
	message := strings.TrimSpace(stringPtr(dispute.StatusDetail))
	switch dispute.ReassessmentStatus {
	case models.CodeReviewDisputeReassessmentQueued, models.CodeReviewDisputeReassessmentRunning:
		message = "I recorded the objection and started a reassessment of the same pull request head."
	case models.CodeReviewDisputeReassessmentDeduped:
		message = "I recorded the objection. An equivalent reassessment was already requested for this pull request."
	case models.CodeReviewDisputeReassessmentHeadChanged:
		if message == "" {
			message = "I recorded the objection, but the pull request changed before reassessment could start."
		}
	case models.CodeReviewDisputeReassessmentCompleted:
		if dispute.ReassessmentDecision != nil && dispute.ReassessmentFlipped != nil && *dispute.ReassessmentFlipped {
			message = "The reassessment completed and the decision changed to " + strings.ReplaceAll(string(*dispute.ReassessmentDecision), "_", " ") + "."
		} else {
			message = "The reassessment completed and the decision did not change. The objection remains available to a policy owner."
		}
	case models.CodeReviewDisputeReassessmentFailed:
		message = "The reassessment could not complete. The objection remains recorded and can be retried from 143."
	}
	if dispute.EscalatedAt != nil {
		message = "This objection was sent to a policy owner for review."
	}
	if dispute.AdjudicationStatus != nil {
		switch *dispute.AdjudicationStatus {
		case models.CodeReviewDisputeAdjudicationUpheld:
			message = "A policy owner upheld this objection. The decision is retained as feedback for policy tuning."
		case models.CodeReviewDisputeAdjudicationRejected:
			message = "A policy owner reviewed and rejected this objection."
		case models.CodeReviewDisputeAdjudicationNeedsContext:
			message = "A policy owner reviewed this objection and requested more context."
		}
	}
	if message == "" {
		message = "I recorded this objection for review."
	}
	if dispute.Direction != nil && *dispute.Direction == models.CodeReviewDisputeDirectionShouldNotHaveApproved {
		var signals struct {
			PullRequestAuthor string `json:"pull_request_author"`
		}
		if err := json.Unmarshal(dispute.QueueSignals, &signals); err == nil && strings.TrimSpace(signals.PullRequestAuthor) != "" {
			message = "@" + strings.TrimSpace(signals.PullRequestAuthor) + " " + message
		}
	}
	if frontendURL != "" {
		message += " [View the dispute](" + frontendURL + "/code-reviews?evidence=" + dispute.SessionID.String() + ")"
	}
	message += "\n\n" + ghservice.PRFeedbackHiddenMarker("code-review-dispute:"+dispute.ID.String())
	return message
}

func (s *DisputeService) ListBySession(ctx context.Context, orgID, sessionID uuid.UUID, cursor *uuid.UUID, limit int) (models.CodeReviewDisputePage, error) {
	if _, err := s.reviews.GetBySessionID(ctx, orgID, sessionID); err != nil {
		return models.CodeReviewDisputePage{}, err
	}
	page, err := s.disputes.ListBySession(ctx, orgID, sessionID, cursor, limit)
	return enrichCodeReviewDisputePage(page), err
}

func (s *DisputeService) ListQueue(ctx context.Context, orgID uuid.UUID, filters models.CodeReviewDisputeListFilters) (models.CodeReviewDisputePage, error) {
	page, err := s.disputes.ListQueue(ctx, orgID, filters)
	return enrichCodeReviewDisputePage(page), err
}

func enrichCodeReviewDisputePage(page models.CodeReviewDisputePage) models.CodeReviewDisputePage {
	items := make([]models.CodeReviewDispute, len(page.Items))
	for index, dispute := range page.Items {
		items[index] = enrichCodeReviewDisputeTrust(dispute)
	}
	page.Items = items
	return page
}

func enrichCodeReviewDisputeTrust(dispute models.CodeReviewDispute) models.CodeReviewDispute {
	dispute.Trusted, dispute.CurrentAuthorizationReason = dispute.CurrentTrust()
	return dispute
}

func (s *DisputeService) Escalate(ctx context.Context, orgID, disputeID, userID uuid.UUID, note string) (models.CodeReviewDispute, error) {
	result, err := s.disputes.Escalate(ctx, orgID, disputeID, userID, boundedGeneratedReply(note))
	if errors.Is(err, pgx.ErrNoRows) {
		return result, ErrCodeReviewDisputeNotEscalatable
	}
	if err != nil {
		return result, err
	}
	if err := s.EnqueueReply(ctx, orgID, disputeID, "escalated"); err != nil {
		return models.CodeReviewDispute{}, err
	}
	return enrichCodeReviewDisputeTrust(result), nil
}

func (s *DisputeService) Adjudicate(ctx context.Context, orgID, disputeID, userID uuid.UUID, update models.CodeReviewDisputeAdjudicationUpdate) (models.CodeReviewDispute, error) {
	if update.AdjudicationStatus != nil {
		if err := update.AdjudicationStatus.Validate(); err != nil {
			return models.CodeReviewDispute{}, err
		}
		if *update.AdjudicationStatus == models.CodeReviewDisputeAdjudicationPending || *update.AdjudicationStatus == models.CodeReviewDisputeAdjudicationExpired {
			return models.CodeReviewDispute{}, fmt.Errorf("adjudication_status must be upheld, rejected, or needs_context")
		}
	}
	result, err := s.disputes.Adjudicate(ctx, orgID, disputeID, userID, update)
	if err != nil {
		return result, err
	}
	if update.TrustOverridePresent {
		trusted, reason := result.CurrentTrust()
		if authErr := s.recordAuthorizationWithActor(ctx, result, models.CodeReviewDisputeAuthorizationAdminPromotion, trusted, reason, &userID); authErr != nil {
			return models.CodeReviewDispute{}, authErr
		}
	}
	if err := s.EnqueueReply(ctx, orgID, disputeID, "adjudicated"); err != nil {
		return models.CodeReviewDispute{}, err
	}
	return enrichCodeReviewDisputeTrust(result), nil
}
