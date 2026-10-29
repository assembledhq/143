package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type codeReviewGuidanceRecordStore interface {
	GetPromptRecordByKey(context.Context, uuid.UUID, string) (models.CodeReviewPromptRecord, error)
	CreatePromptRecordIfAbsent(context.Context, *models.CodeReviewPromptRecord) (bool, error)
}

func loadCodeReviewRepositoryGuidance(ctx context.Context, stores *Stores, services *Services, job runCodeReviewPayload, assessment *models.CodeReviewAssessment, reviewersStarted bool) (*models.CodeReviewRepositoryGuidance, error) {
	if assessment != nil {
		var manifest codereviewsvc.ReviewInputManifest
		if err := json.Unmarshal(assessment.InputManifest, &manifest); err != nil {
			return nil, fmt.Errorf("decode captured review guidance: %w", err)
		}
		guidance := manifest.Contract.RepositoryGuidance
		if guidance != nil {
			if err := guidance.Validate(); err != nil {
				return nil, err
			}
		}
		// Pre-upgrade assessments keep their original inputs. A subsequent
		// recheck sees nil versus captured guidance and requires a full review.
		return guidance, nil
	}
	if services == nil || services.CodeReviewRepositoryGuidance == nil {
		return nil, nil
	}
	return captureCodeReviewGuidanceCheckpoint(ctx, stores.CodeReviews, services.CodeReviewRepositoryGuidance, job, reviewersStarted)
}

// Non-reusable full reviews still need one immutable capture shared by every
// reviewer, fallback reviewer, and synthesis attempt across controller retries.
func captureCodeReviewGuidanceCheckpoint(ctx context.Context, records codeReviewGuidanceRecordStore, capturer codereviewsvc.RepositoryGuidanceCapturer, job runCodeReviewPayload, reviewersStarted bool) (*models.CodeReviewRepositoryGuidance, error) {
	key := codeReviewPromptRecordRoot(models.CodeReviewSessionMetadata{}, job) + "/repository-guidance"
	record, err := records.GetPromptRecordByKey(ctx, job.OrgID, key)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// An older worker already dispatched reviewers without a guidance capture.
		// Preserve those inputs rather than changing instructions mid-review.
		if reviewersStarted {
			return nil, nil
		}
		guidance, err := capturer.CaptureCodeReviewRepositoryGuidance(ctx, job.OrgID, job.RepositoryID)
		if err != nil {
			return nil, classifyGitHubJobError(err, job.SessionID.String())
		}
		if err := guidance.Validate(); err != nil {
			return nil, err
		}
		content, err := json.Marshal(guidance)
		if err != nil {
			return nil, err
		}
		record = models.CodeReviewPromptRecord{OrgID: job.OrgID, SessionID: job.SessionID, RecordKey: key, Role: string(models.CodeReviewAgentRoleReviewer), Content: string(content)}
		if _, err := records.CreatePromptRecordIfAbsent(ctx, &record); err != nil {
			return nil, err
		}
	}
	if record.OrgID != job.OrgID || record.SessionID != job.SessionID || record.RecordKey != key {
		return nil, fmt.Errorf("repository review guidance checkpoint identity mismatch")
	}
	var guidance models.CodeReviewRepositoryGuidance
	if err := json.Unmarshal([]byte(record.Content), &guidance); err != nil {
		return nil, fmt.Errorf("decode repository review guidance checkpoint: %w", err)
	}
	return &guidance, guidance.Validate()
}
