package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

const (
	codeReviewRiskTierPromptRole      = "risk_tier_signal"
	codeReviewRiskTierLiveRetryDelay  = 30 * time.Second
	codeReviewRiskTierLiveFailureNote = "live risk-tier lookup failed before the final decision"
)

type codeReviewRiskTierCheckRunLister interface {
	ListCodeReviewCheckRuns(ctx context.Context, orgID, repositoryID uuid.UUID, headSHA, checkName string) ([]models.CodeReviewCheckRun, error)
}

type codeReviewRiskTierCheckpointStore interface {
	GetPromptRecordByKey(ctx context.Context, orgID uuid.UUID, recordKey string) (models.CodeReviewPromptRecord, error)
	CreatePromptRecordIfAbsent(ctx context.Context, record *models.CodeReviewPromptRecord) (bool, error)
}

func resolveCodeReviewRiskTierSignal(ctx context.Context, stores *Stores, services *Services, job runCodeReviewPayload, policy models.CodeReviewPolicyConfig) (*models.CodeReviewRiskTierSignal, error) {
	var checkpoints codeReviewRiskTierCheckpointStore
	if stores != nil && stores.CodeReviews != nil {
		checkpoints = stores.CodeReviews
	}
	return resolveCodeReviewRiskTierSignalWith(ctx, checkpoints, codeReviewRiskTierChecks(services), job, policy)
}

func codeReviewRiskTierChecks(services *Services) codeReviewRiskTierCheckRunLister {
	if services == nil || services.CodeReviewRiskTier == nil {
		return nil
	}
	return services.CodeReviewRiskTier
}

func resolveCodeReviewRiskTierSignalWith(ctx context.Context, checkpoints codeReviewRiskTierCheckpointStore, checks codeReviewRiskTierCheckRunLister, job runCodeReviewPayload, policy models.CodeReviewPolicyConfig) (*models.CodeReviewRiskTierSignal, error) {
	setting := policy.RiskPolicy.RiskTierCheck
	if !setting.Active() {
		return &models.CodeReviewRiskTierSignal{}, nil
	}
	if checkpoints == nil || checks == nil {
		return nil, errors.New("risk-tier check is enabled but its checkpoint store or check run reader is not configured")
	}
	key := codeReviewRiskTierRecordKey(job.SessionID, job.HeadSHA, *setting)
	record, err := checkpoints.GetPromptRecordByKey(ctx, job.OrgID, key)
	if err == nil {
		return restoreCodeReviewRiskTierSignal(record, job, key, *setting)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("load risk-tier checkpoint: %w", err)
	}
	runs, err := checks.ListCodeReviewCheckRuns(ctx, job.OrgID, job.RepositoryID, job.HeadSHA, setting.EffectiveCheckName())
	if err != nil {
		return nil, fmt.Errorf("list risk-tier check runs: %w", err)
	}
	signal := models.ResolveCodeReviewRiskTierSignal(setting, job.HeadSHA, runs)
	metadata, err := json.Marshal(signal)
	if err != nil {
		return nil, fmt.Errorf("encode risk-tier checkpoint: %w", err)
	}
	record = models.CodeReviewPromptRecord{
		OrgID: job.OrgID, SessionID: job.SessionID, RecordKey: key, Role: codeReviewRiskTierPromptRole,
		Content: codeReviewRiskTierSummary(signal), Metadata: metadata,
	}
	if _, err := checkpoints.CreatePromptRecordIfAbsent(ctx, &record); err != nil {
		return nil, fmt.Errorf("persist risk-tier checkpoint: %w", err)
	}
	return restoreCodeReviewRiskTierSignal(record, job, key, *setting)
}

func refreshCodeReviewRiskTierSignal(ctx context.Context, services *Services, job runCodeReviewPayload, policy models.CodeReviewPolicyConfig) (*models.CodeReviewRiskTierSignal, error) {
	return refreshCodeReviewRiskTierSignalWith(ctx, codeReviewRiskTierChecks(services), job, policy)
}

func refreshCodeReviewRiskTierSignalWith(ctx context.Context, checks codeReviewRiskTierCheckRunLister, job runCodeReviewPayload, policy models.CodeReviewPolicyConfig) (*models.CodeReviewRiskTierSignal, error) {
	checkpoint := job.RiskTierSignal
	setting := policy.RiskPolicy.RiskTierCheck
	if !checkpoint.Active() || !setting.Active() {
		return checkpoint, nil
	}
	live := models.CodeReviewRiskTierLookupFailedSignal(setting, job.HeadSHA)
	var lookupErr error
	if checks == nil {
		lookupErr = errors.New("risk-tier check is enabled but its check run reader is not configured")
	} else if runs, err := checks.ListCodeReviewCheckRuns(ctx, job.OrgID, job.RepositoryID, job.HeadSHA, setting.EffectiveCheckName()); err != nil {
		lookupErr = fmt.Errorf("list live risk-tier check runs: %w", err)
	} else {
		live = models.ResolveCodeReviewRiskTierSignal(setting, job.HeadSHA, runs)
	}
	combined := checkpoint.WithLive(live)
	return &combined, lookupErr
}

func refreshCodeReviewRiskTierSignalForDecision(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, job runCodeReviewPayload, policy models.CodeReviewPolicyConfig) (*models.CodeReviewRiskTierSignal, error) {
	var checkpoints codeReviewRiskTierCheckpointStore
	if stores != nil && stores.CodeReviews != nil {
		checkpoints = stores.CodeReviews
	}
	return refreshCodeReviewRiskTierSignalForDecisionWith(ctx, checkpoints, codeReviewRiskTierChecks(services), logger, job, policy, time.Now)
}

func refreshCodeReviewRiskTierSignalForDecisionWith(ctx context.Context, checkpoints codeReviewRiskTierCheckpointStore, checks codeReviewRiskTierCheckRunLister, logger zerolog.Logger, job runCodeReviewPayload, policy models.CodeReviewPolicyConfig, clock func() time.Time) (*models.CodeReviewRiskTierSignal, error) {
	combined, lookupErr := refreshCodeReviewRiskTierSignalWith(ctx, checks, job, policy)
	if lookupErr == nil {
		return combined, nil
	}
	if checkpoints == nil {
		return nil, lookupErr
	}
	record := models.CodeReviewPromptRecord{
		OrgID: job.OrgID, SessionID: job.SessionID, RecordKey: codeReviewRiskTierRecordKey(job.SessionID, job.HeadSHA, *policy.RiskPolicy.RiskTierCheck) + "/live-lookup-failure",
		Role: codeReviewRiskTierPromptRole, Content: codeReviewRiskTierLiveFailureNote, Metadata: json.RawMessage(`{}`),
	}
	if _, err := checkpoints.CreatePromptRecordIfAbsent(ctx, &record); err != nil {
		return nil, fmt.Errorf("persist risk-tier lookup failure window: %w", err)
	}
	now := clock()
	startedAt := record.CreatedAt
	if startedAt.IsZero() {
		startedAt = now
	}
	delay := codeReviewRiskTierLiveRetryDelay
	if now.Sub(startedAt)+delay <= maxRetryableDuration {
		return nil, &RetryableError{Err: lookupErr, RetryAfter: &delay, RetryWindowStartedAt: &startedAt, BypassMaxRetryDuration: true}
	}
	logger.Warn().Err(lookupErr).Str("session_id", job.SessionID.String()).Dur("retry_window_age", now.Sub(startedAt)).
		Msg("live risk-tier lookup kept failing before the final decision; treating it as non-authoritative so the size limits apply")
	return combined, nil
}

func codeReviewRecheckRiskTierSupportsApproval(ctx context.Context, checkpoints codeReviewRiskTierCheckpointStore, checks codeReviewRiskTierCheckRunLister, logger zerolog.Logger, a models.CodeReviewAssessment, policy models.CodeReviewPolicyConfig, completion models.CodeReviewAssessmentCompletion) (bool, string, error) {
	var reasons []models.CodeReviewRiskReason
	if len(completion.RiskReasonDetails) > 0 {
		if err := json.Unmarshal(completion.RiskReasonDetails, &reasons); err != nil {
			return false, "", fmt.Errorf("decode staged risk reasons: %w", err)
		}
	}
	job := runCodeReviewPayload{OrgID: a.OrgID, SessionID: a.SessionID, RepositoryID: a.RepositoryID, HeadSHA: a.HeadSHA}
	checkpoint, err := resolveCodeReviewRiskTierSignalWith(ctx, checkpoints, checks, job, policy)
	if err != nil {
		logger.Warn().Err(err).Str("assessment_id", a.ID.String()).Msg("risk-tier checkpoint unavailable at publication; treating it as non-authoritative")
		failed := models.CodeReviewRiskTierLookupFailedSignal(policy.RiskPolicy.RiskTierCheck, a.HeadSHA)
		checkpoint = &failed
	}
	job.RiskTierSignal = checkpoint
	current, lookupErr := refreshCodeReviewRiskTierSignalWith(ctx, checks, job, policy)
	if lookupErr != nil {
		logger.Warn().Err(lookupErr).Str("assessment_id", a.ID.String()).Msg("live risk-tier lookup failed at publication; treating the live result as non-authoritative")
	}
	return codeReviewRiskTierApprovalHolds(reasons, current)
}

func codeReviewRiskTierApprovalHolds(staged []models.CodeReviewRiskReason, current *models.CodeReviewRiskTierSignal) (bool, string, error) {
	for _, reason := range staged {
		record := reason.SizeSafeguard
		if reason.Code != models.CodeReviewRiskReasonSizeSafeguard || record == nil || record.Outcome != models.CodeReviewSizeSafeguardWaived {
			continue
		}
		if !(record.Files.Exceeded || record.Additions.Exceeded || record.Deletions.Exceeded) || current.WaivesSizeSafeguard() {
			continue
		}
		return false, fmt.Sprintf("staged size waiver from %s tier %q no longer holds; current risk tier is %s", record.Source, record.Tier, codeReviewRiskTierDescription(current)), nil
	}
	return true, "", nil
}

func codeReviewRiskTierDescription(signal *models.CodeReviewRiskTierSignal) string {
	if !signal.Active() {
		return "disabled"
	}
	observations := make([]string, 0, len(signal.Observations))
	for _, observation := range signal.Observations {
		origin := "checkpoint"
		if observation.Live {
			origin = "live"
		}
		observations = append(observations, fmt.Sprintf("%s %s/%s/%s", origin, observation.Source, observation.Tier, observation.UIRenderChange))
	}
	return fmt.Sprintf("%s tier %q render %q (%s)", signal.Source, signal.Tier, signal.UIRenderChange, strings.Join(observations, ", "))
}

func codeReviewRiskTierRecordKey(sessionID uuid.UUID, headSHA string, setting models.CodeReviewRiskTierCheckPolicy) string {
	binding := sha256.Sum256([]byte(strconv.FormatInt(setting.AppID, 10) + "\x00" + setting.EffectiveCheckName()))
	return fmt.Sprintf("code-review-prompts/%s/%s/risk-tier-v2/%s", sessionID, strings.ToLower(strings.TrimSpace(headSHA)), hex.EncodeToString(binding[:8]))
}

func restoreCodeReviewRiskTierSignal(record models.CodeReviewPromptRecord, job runCodeReviewPayload, key string, setting models.CodeReviewRiskTierCheckPolicy) (*models.CodeReviewRiskTierSignal, error) {
	if record.OrgID != job.OrgID || record.SessionID != job.SessionID || record.RecordKey != key || record.Role != codeReviewRiskTierPromptRole {
		return nil, errors.New("stored risk-tier checkpoint identity is invalid")
	}
	var signal models.CodeReviewRiskTierSignal
	if err := json.Unmarshal(record.Metadata, &signal); err != nil {
		return nil, fmt.Errorf("decode risk-tier checkpoint: %w", err)
	}
	if !signal.Enabled || signal.HeadSHA != strings.ToLower(strings.TrimSpace(job.HeadSHA)) || signal.AppID != setting.AppID || signal.CheckName != setting.EffectiveCheckName() || signal.Source.Validate() != nil || len(signal.Observations) == 0 {
		return nil, errors.New("stored risk-tier checkpoint does not match the assessed head and configured check")
	}
	return &signal, nil
}

func codeReviewRiskTierSummary(signal models.CodeReviewRiskTierSignal) string {
	if signal.Authoritative() {
		return fmt.Sprintf("%s check from app %d: tier %s, ui_render_change %s across %d run(s)", signal.CheckName, signal.AppID, signal.Tier, signal.UIRenderChange, len(signal.Observations))
	}
	return fmt.Sprintf("%s check from app %d is not authoritative for this head: %s", signal.CheckName, signal.AppID, signal.Source)
}
