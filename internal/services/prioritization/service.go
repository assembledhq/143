package prioritization

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	llmpkg "github.com/assembledhq/143/internal/llm"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
)

// issueStore is the subset of db.IssueStore used by the service.
type issueStore interface {
	GetByID(ctx context.Context, orgID, issueID uuid.UUID) (models.Issue, error)
}

// priorityScoreStore is the subset of db.PriorityScoreStore used by the service.
type priorityScoreStore interface {
	Upsert(ctx context.Context, score *models.PriorityScore) error
}

// complexityEstimateStore is the subset of db.ComplexityEstimateStore used by the service.
type complexityEstimateStore interface {
	Upsert(ctx context.Context, est *models.ComplexityEstimate) error
}

// sessionStore is the subset of db.SessionStore used by the service.
type sessionStore interface {
	CountRunningByOrg(ctx context.Context, orgID uuid.UUID) (int, error)
	Create(ctx context.Context, run *models.Session) error
}

// orgStore is the subset of db.OrganizationStore used by the service.
type orgStore interface {
	GetByID(ctx context.Context, id uuid.UUID) (models.Organization, error)
}

// jobStore is the subset of db.JobStore used by the service.
type jobStore interface {
	Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
}

// OrgSettings holds the parsed org settings relevant to prioritization.
// NOTE: This duplicates fields from models.OrgSettings. The two structs use
// identical JSON tags and must be kept in sync when adding new fields. A full
// unification is not done because the nested struct shapes differ slightly
// (e.g. ConfidenceThresholds, PriorityWeights use inline structs here).
type OrgSettings struct {
	AutonomyLevel        string `json:"autonomy_level"`
	Aggressiveness       int    `json:"execution_aggressiveness"`
	MaxConcurrentRuns    int    `json:"max_concurrent_runs"`
	AgentAutonomy        string `json:"agent_autonomy"`
	ConfidenceThresholds struct {
		AutoProceed float64 `json:"auto_proceed"`
		HumanReview float64 `json:"human_review"`
	} `json:"confidence_thresholds"`
	PriorityWeights struct {
		CustomerImpact float64 `json:"customer_impact"`
		Severity       float64 `json:"severity"`
		Recency        float64 `json:"recency"`
		RevenueRisk    float64 `json:"revenue_risk"`
	} `json:"priority_weights"`
	MinPriorityThreshold float64 `json:"min_priority_threshold"`
	ProductDirection     string  `json:"product_direction"`
	DefaultAgentType     string  `json:"default_agent_type"`
}

// Default weight values.
const (
	defaultWeightCustomerImpact = 0.35
	defaultWeightSeverity       = 0.25
	defaultWeightRecency        = 0.20
	defaultWeightRevenueRisk    = 0.20
	defaultMaxConcurrentRuns    = 10
	defaultMinPriorityThreshold = 30.0

	// recencyHalfLifeHours is the half-life in hours for recency decay (1 week).
	recencyHalfLifeHours = 168.0
)

// Service computes priority scores and complexity estimates for issues,
// and optionally auto-triggers agent runs based on org settings.
type Service struct {
	issues     issueStore
	priorities priorityScoreStore
	complexity complexityEstimateStore
	sessions   sessionStore
	orgs       orgStore
	jobs       jobStore
	llm        llmpkg.Client // can be nil
	logger     zerolog.Logger
}

// NewService creates a new prioritization service.
func NewService(
	issues issueStore,
	priorities priorityScoreStore,
	complexity complexityEstimateStore,
	sessions sessionStore,
	orgs orgStore,
	jobs jobStore,
	llmClient llmpkg.Client,
	logger zerolog.Logger,
) *Service {
	return &Service{
		issues:     issues,
		priorities: priorities,
		complexity: complexity,
		sessions:   sessions,
		orgs:       orgs,
		jobs:       jobs,
		llm:        llmClient,
		logger:     logger,
	}
}

// ComputeScore computes and upserts a priority score for the given issue.
func (s *Service) ComputeScore(ctx context.Context, orgID, issueID uuid.UUID) (*models.PriorityScore, error) {
	issue, err := s.issues.GetByID(ctx, orgID, issueID)
	if err != nil {
		return nil, fmt.Errorf("fetch issue: %w", err)
	}

	org, err := s.orgs.GetByID(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("fetch org: %w", err)
	}

	settings := parseOrgSettings(s.logger, org.Settings)

	// Compute sub-scores.
	customerImpact := computeCustomerImpact(issue.AffectedCustomerCount, issue.OccurrenceCount)
	severity := computeSeverity(string(issue.Severity))
	recency := computeRecency(issue.LastSeenAt)
	revenueRisk := 0.0 // placeholder

	// Get weights (use defaults if zero).
	wCI := defaultOrValue(settings.PriorityWeights.CustomerImpact, defaultWeightCustomerImpact)
	wSev := defaultOrValue(settings.PriorityWeights.Severity, defaultWeightSeverity)
	wRec := defaultOrValue(settings.PriorityWeights.Recency, defaultWeightRecency)
	wRev := defaultOrValue(settings.PriorityWeights.RevenueRisk, defaultWeightRevenueRisk)

	score := wCI*customerImpact + wSev*severity + wRec*recency + wRev*revenueRisk

	// Direction alignment via LLM.
	directionAlignment := 0.0
	if s.llm != nil && settings.ProductDirection != "" {
		alignment, err := s.computeDirectionAlignment(ctx, &issue, settings.ProductDirection)
		if err != nil {
			s.logger.Warn().Err(err).Str("issue_id", issueID.String()).Msg("direction alignment failed, using 0")
		} else {
			directionAlignment = alignment
		}
	}

	// Apply direction modifier: finalScore = score * (1 + 0.3 * directionAlignment)
	finalScore := score * (1 + 0.3*directionAlignment)

	// Determine eligibility.
	threshold := defaultOrValue(settings.MinPriorityThreshold, defaultMinPriorityThreshold)
	eligible := directionAlignment > -0.5 &&
		(issue.Status == "open" || issue.Status == "triaged") &&
		finalScore > threshold

	factors, err := json.Marshal(map[string]any{
		"customer_impact_raw": customerImpact,
		"severity_raw":        severity,
		"recency_raw":         recency,
		"revenue_risk_raw":    revenueRisk,
		"direction_alignment": directionAlignment,
		"weights": map[string]float64{
			"customer_impact": wCI,
			"severity":        wSev,
			"recency":         wRec,
			"revenue_risk":    wRev,
		},
	})
	if err != nil {
		s.logger.Warn().Err(err).Msg("failed to marshal priority score factors")
		factors = nil
	}

	ps := &models.PriorityScore{
		IssueID:             issueID,
		OrgID:               orgID,
		Score:               finalScore,
		CustomerImpactScore: customerImpact,
		SeverityScore:       severity,
		RecencyScore:        recency,
		RevenueRiskScore:    revenueRisk,
		DirectionAlignment:  directionAlignment,
		Factors:             factors,
		EligibleForAgent:    eligible,
		ComputedAt:          time.Now(),
	}

	if err := s.priorities.Upsert(ctx, ps); err != nil {
		return nil, fmt.Errorf("upsert priority score: %w", err)
	}

	s.logger.Info().
		Str("issue_id", issueID.String()).
		Float64("score", finalScore).
		Bool("eligible", eligible).
		Msg("priority score computed")

	return ps, nil
}

// EstimateComplexity estimates the complexity tier for an issue.
func (s *Service) EstimateComplexity(ctx context.Context, orgID, issueID uuid.UUID, issue *models.Issue) (*models.ComplexityEstimate, error) {
	if issue == nil {
		fetched, err := s.issues.GetByID(ctx, orgID, issueID)
		if err != nil {
			return nil, fmt.Errorf("fetch issue: %w", err)
		}
		issue = &fetched
	}

	var tier int
	var label string
	var confidence float64
	var reasoning string
	var modelUsed *string

	if s.llm != nil {
		t, l, c, r, err := s.estimateComplexityViaLLM(ctx, issue)
		if err != nil {
			s.logger.Warn().Err(err).Str("issue_id", issueID.String()).Msg("LLM complexity estimation failed, using heuristic")
			tier, label, confidence, reasoning = heuristicComplexity(issue)
		} else {
			tier, label, confidence, reasoning = t, l, c, r
			m := "llm"
			modelUsed = &m
		}
	} else {
		tier, label, confidence, reasoning = heuristicComplexity(issue)
	}

	est := &models.ComplexityEstimate{
		IssueID:    issueID,
		OrgID:      orgID,
		Tier:       tier,
		Label:      label,
		Confidence: confidence,
		Reasoning:  &reasoning,
		ModelUsed:  modelUsed,
		ComputedAt: time.Now(),
	}

	if err := s.complexity.Upsert(ctx, est); err != nil {
		return nil, fmt.Errorf("upsert complexity estimate: %w", err)
	}

	s.logger.Info().
		Str("issue_id", issueID.String()).
		Int("tier", tier).
		Str("label", label).
		Msg("complexity estimated")

	return est, nil
}

// CheckAutoTrigger checks whether an agent run should be auto-triggered
// based on org settings, priority score, and complexity estimate.
func (s *Service) CheckAutoTrigger(ctx context.Context, orgID uuid.UUID, score *models.PriorityScore, estimate *models.ComplexityEstimate, issue *models.Issue) error {
	org, err := s.orgs.GetByID(ctx, orgID)
	if err != nil {
		return fmt.Errorf("fetch org: %w", err)
	}
	settings := parseOrgSettings(s.logger, org.Settings)

	// Gate 1: autonomy level.
	if settings.AutonomyLevel == string(models.AutonomyLevelManual) {
		s.logger.Debug().Str("org_id", orgID.String()).Msg("auto-trigger skipped: manual mode")
		return nil
	}

	// Gate 2: auto_simple mode — only trigger for high severity + high score.
	if settings.AutonomyLevel == string(models.AutonomyLevelAutoSimple) {
		if !isHighSeverity(string(issue.Severity)) || score.Score < 60 {
			s.logger.Debug().
				Str("org_id", orgID.String()).
				Str("severity", string(issue.Severity)).
				Float64("score", score.Score).
				Msg("auto-trigger skipped: auto_simple gate not met")
			return nil
		}
	}

	// Gate 3: aggressiveness-based tier limit.
	maxTier := aggressivenessMaxTier(settings.Aggressiveness)
	if estimate.Tier > maxTier {
		s.logger.Debug().
			Str("org_id", orgID.String()).
			Int("tier", estimate.Tier).
			Int("max_tier", maxTier).
			Msg("auto-trigger skipped: complexity tier exceeds aggressiveness limit")
		return nil
	}

	// Gate 4: concurrent run limit.
	maxConcurrent := settings.MaxConcurrentRuns
	if maxConcurrent <= 0 {
		maxConcurrent = defaultMaxConcurrentRuns
	}
	running, err := s.sessions.CountRunningByOrg(ctx, orgID)
	if err != nil {
		return fmt.Errorf("count running agent runs: %w", err)
	}
	if running >= maxConcurrent {
		s.logger.Debug().
			Str("org_id", orgID.String()).
			Int("running", running).
			Int("max", maxConcurrent).
			Msg("auto-trigger skipped: concurrent run limit reached")
		return nil
	}

	// All gates passed — create agent run and enqueue job.
	agentType := models.AgentType(settings.DefaultAgentType)
	if agentType == "" {
		agentType = models.DefaultDefaultAgentType
	}
	run := &models.Session{
		PrimaryIssueID: &issue.ID,
		OrgID:          orgID,
		AgentType:      agentType,
		Status:         models.SessionStatusPending,
		AutonomyLevel:  models.SessionAutonomy(settings.AutonomyLevel),
		TokenMode:      models.SessionTokenModeLow,
		ComplexityTier: &estimate.Tier,
		RepositoryID:   issue.RepositoryID,
	}
	if err := s.sessions.Create(ctx, run); err != nil {
		return fmt.Errorf("create agent run: %w", err)
	}

	payload := db.RunAgentPayload(run)
	dedupeKey := db.RunAgentDedupeKey(run.ID)
	if _, err := s.jobs.Enqueue(ctx, orgID, "agent", "run_agent", payload, 5, &dedupeKey); err != nil {
		return fmt.Errorf("enqueue run_agent job: %w", err)
	}

	s.logger.Info().
		Str("issue_id", issue.ID.String()).
		Str("session_id", run.ID.String()).
		Float64("score", score.Score).
		Int("tier", estimate.Tier).
		Msg("auto-triggered agent run")

	return nil
}

// computeCustomerImpact computes the customer impact sub-score.
func computeCustomerImpact(customers, occurrences int) float64 {
	score := math.Log2(float64(customers+1))*10 + math.Log2(float64(occurrences+1))*5
	return math.Min(score, 100)
}

// computeSeverity maps severity string to a numeric score.
func computeSeverity(severity string) float64 {
	switch strings.ToLower(severity) {
	case "critical":
		return 100
	case "high":
		return 75
	case "medium":
		return 50
	case "low":
		return 25
	default:
		return 25
	}
}

// computeRecency computes a time-decay score based on when the issue was last seen.
func computeRecency(lastSeenAt time.Time) float64 {
	hours := time.Since(lastSeenAt).Hours()
	if hours < 0 {
		hours = 0
	}
	return 100 * math.Exp(-hours/recencyHalfLifeHours)
}

// computeDirectionAlignment calls the LLM to assess how well an issue aligns
// with the organization's product direction. Returns a value in [-1, 1].
func (s *Service) computeDirectionAlignment(ctx context.Context, issue *models.Issue, productDirection string) (float64, error) {
	systemPrompt := prompts.DirectionAlignmentPrompt()

	desc := ""
	if issue.Description != nil {
		desc = *issue.Description
	}

	userPrompt := prompts.DirectionAlignmentUserPrompt(prompts.DirectionAlignmentUserPromptData{
		ProductDirection: productDirection,
		Title:            issue.Title,
		Description:      desc,
		Severity:         string(issue.Severity),
		OccurrenceCount:  issue.OccurrenceCount,
	})

	response, err := s.llm.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		return 0, fmt.Errorf("LLM direction alignment: %w", err)
	}

	var result struct {
		Alignment float64 `json:"alignment"`
		Reasoning string  `json:"reasoning"`
	}
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		return 0, fmt.Errorf("parse LLM alignment response: %w", err)
	}

	// Clamp to [-1, 1].
	if result.Alignment > 1 {
		result.Alignment = 1
	}
	if result.Alignment < -1 {
		result.Alignment = -1
	}

	return result.Alignment, nil
}

// estimateComplexityViaLLM calls the LLM to estimate issue complexity.
func (s *Service) estimateComplexityViaLLM(ctx context.Context, issue *models.Issue) (int, string, float64, string, error) {
	systemPrompt := prompts.ComplexityEstimatePrompt()

	desc := ""
	if issue.Description != nil {
		desc = *issue.Description
	}

	userPrompt := prompts.ComplexityEstimateUserPrompt(prompts.ComplexityEstimateUserPromptData{
		Title:                 issue.Title,
		Description:           desc,
		Severity:              string(issue.Severity),
		OccurrenceCount:       issue.OccurrenceCount,
		AffectedCustomerCount: issue.AffectedCustomerCount,
	})

	response, err := s.llm.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		return 0, "", 0, "", fmt.Errorf("LLM complexity estimation: %w", err)
	}

	var result struct {
		Tier       int     `json:"tier"`
		Label      string  `json:"label"`
		Confidence float64 `json:"confidence"`
		Reasoning  string  `json:"reasoning"`
	}
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		return 0, "", 0, "", fmt.Errorf("parse LLM complexity response: %w", err)
	}

	// Clamp tier to valid range.
	if result.Tier < 1 {
		result.Tier = 1
	}
	if result.Tier > 5 {
		result.Tier = 5
	}

	return result.Tier, result.Label, result.Confidence, result.Reasoning, nil
}

// heuristicComplexity uses simple rules to estimate complexity without an LLM.
func heuristicComplexity(issue *models.Issue) (tier int, label string, confidence float64, reasoning string) {
	switch strings.ToLower(string(issue.Severity)) {
	case "critical":
		return 3, "moderate", 0.4, "critical severity issues typically require moderate effort"
	case "high":
		return 2, "simple", 0.5, "high severity issues are often straightforward to fix"
	case "medium":
		return 2, "simple", 0.4, "medium severity issues are often single-file fixes"
	default:
		return 1, "trivial", 0.5, "low severity issues are typically trivial fixes"
	}
}

// parseOrgSettings parses OrgSettings from raw JSON, returning defaults for missing values.
func parseOrgSettings(logger zerolog.Logger, raw json.RawMessage) OrgSettings {
	var settings OrgSettings
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &settings); err != nil {
			logger.Warn().Err(err).Msg("failed to parse org settings for prioritization, using defaults")
		}
	}
	return settings
}

// defaultOrValue returns val if it's non-zero, otherwise returns def.
func defaultOrValue(val, def float64) float64 {
	if val == 0 {
		return def
	}
	return val
}

// isHighSeverity returns true for critical or high severity.
func isHighSeverity(severity string) bool {
	s := strings.ToLower(severity)
	return s == "critical" || s == "high"
}

// aggressivenessMaxTier maps the aggressiveness setting to the maximum allowed complexity tier.
func aggressivenessMaxTier(aggressiveness int) int {
	switch aggressiveness {
	case 1: // conservative
		return 2
	case 2: // moderate
		return 3
	case 3: // aggressive
		return 4
	case 4: // maximum
		return 5
	default:
		// Default to moderate if not set.
		return 3
	}
}
