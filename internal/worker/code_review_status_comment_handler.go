package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

const codeReviewStatusCommentJobMaxAttempts = 3

func newSyncCodeReviewStatusCommentHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, _ string, payload json.RawMessage) error {
		if stores == nil || stores.CodeReviews == nil {
			return fmt.Errorf("code review store is unavailable")
		}
		if services == nil || services.CodeReviews == nil {
			return nil
		}
		updater, ok := services.CodeReviews.(codeReviewStatusCommentUpdater)
		if !ok {
			return nil
		}
		var job codereviewsvc.SyncReviewStatusCommentJobPayload
		if err := json.Unmarshal(payload, &job); err != nil {
			return fmt.Errorf("decode code review status comment job payload: %w", err)
		}
		if job.OrgID == uuid.Nil || job.SessionID == uuid.Nil {
			return fmt.Errorf("org_id and session_id are required")
		}
		metadata, err := stores.CodeReviews.GetBySessionID(ctx, job.OrgID, job.SessionID)
		if err != nil {
			return fmt.Errorf("load code review for status comment: %w", err)
		}
		if job.TriggeringDisputeID == nil && metadata.TriggerSource == models.CodeReviewTriggerSourceDisputeReassessment {
			job.TriggeringDisputeID, err = stores.CodeReviews.GetTriggeringDisputeID(ctx, job.OrgID, job.SessionID)
			if err != nil {
				return fmt.Errorf("load triggering dispute for status comment: %w", err)
			}
		}
		if job.TriggeringDisputeID != nil && codeReviewMetadataTerminal(metadata.Status) && services.CodeReviewDisputes != nil {
			if err := services.CodeReviewDisputes.EnqueueReply(ctx, job.OrgID, *job.TriggeringDisputeID, "status_sync_terminal"); err != nil {
				return fmt.Errorf("enqueue terminal dispute reconciliation: %w", err)
			}
		}
		latest, err := stores.CodeReviews.GetLatestByPullRequest(ctx, job.OrgID, metadata.PullRequestID)
		if err != nil {
			return fmt.Errorf("load latest code review for status comment: %w", err)
		}
		if latest.SessionID != metadata.SessionID {
			logger.Info().
				Str("session_id", metadata.SessionID.String()).
				Str("latest_session_id", latest.SessionID.String()).
				Msg("skipping superseded code review status comment sync")
			return nil
		}
		if stores.Repositories == nil || stores.PullRequests == nil {
			return fmt.Errorf("code review status comment repository stores are unavailable")
		}
		repository, err := stores.Repositories.GetByID(ctx, job.OrgID, metadata.RepositoryID)
		if err != nil {
			return fmt.Errorf("load repository for code review status comment: %w", err)
		}
		if repository.InstallationID == 0 {
			return fmt.Errorf("repository %s has no GitHub installation id", repository.ID)
		}
		pullRequest, err := stores.PullRequests.GetByID(ctx, job.OrgID, metadata.PullRequestID)
		if err != nil {
			return fmt.Errorf("load pull request for code review status comment: %w", err)
		}
		repositoryName := strings.TrimSpace(pullRequest.GitHubRepo)
		if repositoryName == "" {
			repositoryName = strings.TrimSpace(repository.FullName)
		}
		var (
			commentID int64
			status    models.CodeReviewSessionStatus
			updated   bool
			reviewID  int64
		)
		err = stores.CodeReviews.RunWithGitHubPublicationLock(ctx, job.OrgID, metadata.PullRequestID, func(lockCtx context.Context, lockDB db.DBTX) error {
			lockedCodeReviews := db.NewCodeReviewStore(lockDB)
			lockedPullRequests := db.NewPullRequestStore(lockDB)
			lockedLatest, latestErr := lockedCodeReviews.GetLatestByPullRequest(lockCtx, job.OrgID, metadata.PullRequestID)
			if latestErr != nil {
				return fmt.Errorf("recheck latest code review under status comment lock: %w", latestErr)
			}
			if lockedLatest.SessionID != metadata.SessionID {
				logger.Info().
					Str("session_id", metadata.SessionID.String()).
					Str("latest_session_id", lockedLatest.SessionID.String()).
					Msg("skipping superseded code review status comment sync under lock")
				return nil
			}
			var previousCompleted *models.CodeReviewSessionMetadata
			if !codeReviewMetadataTerminal(lockedLatest.Status) {
				previous, previousErr := lockedCodeReviews.GetLatestCompletedByPullRequest(lockCtx, job.OrgID, metadata.PullRequestID)
				if previousErr == nil {
					previousCompleted = &previous
				} else if !errors.Is(previousErr, pgx.ErrNoRows) {
					return fmt.Errorf("load previous completed code review for status comment: %w", previousErr)
				}
			}
			existingCommentID, existingErr := lockedPullRequests.GetCodeReviewStatusCommentID(lockCtx, job.OrgID, metadata.PullRequestID)
			if existingErr != nil {
				return fmt.Errorf("load durable code review status comment id: %w", existingErr)
			}
			body := codeReviewStatusCommentBody(lockedLatest, previousCompleted, codeReviewSessionURL(services.FrontendURL, lockedLatest.SessionID))
			var updateErr error
			commentID, updateErr = updater.UpsertReviewStatusComment(lockCtx, codereviewsvc.UpsertReviewStatusCommentRequest{
				InstallationID:    repository.InstallationID,
				Repository:        repositoryName,
				PullNumber:        pullRequest.GitHubPRNumber,
				Body:              body,
				ExistingCommentID: existingCommentID,
			})
			if updateErr == nil {
				updateErr = lockedPullRequests.SetCodeReviewStatusCommentID(lockCtx, job.OrgID, metadata.PullRequestID, commentID)
			}
			status = lockedLatest.Status
			if updateErr == nil &&
				codeReviewMetadataTerminal(lockedLatest.Status) &&
				lockedLatest.GitHubReviewID != nil {
				reviewID = *lockedLatest.GitHubReviewID
				updateErr = updater.HideReviewSummary(lockCtx, codereviewsvc.HideReviewSummaryRequest{
					InstallationID: repository.InstallationID,
					Repository:     repositoryName,
					PullNumber:     pullRequest.GitHubPRNumber,
					ReviewID:       reviewID,
					OutputKey:      lockedLatest.ReviewOutputKey,
				})
				if updateErr != nil {
					return fmt.Errorf("hide code review fallback summary: %w", updateErr)
				}
			}
			updated = updateErr == nil
			return updateErr
		})
		if err != nil {
			return classifyGitHubJobError(fmt.Errorf("synchronize code review GitHub publication: %w", err), metadata.SessionID.String())
		}
		if !updated {
			return nil
		}
		logger.Info().
			Str("org_id", job.OrgID.String()).
			Str("session_id", job.SessionID.String()).
			Int64("github_comment_id", commentID).
			Bool("github_review_summary_hidden", reviewID > 0).
			Str("review_status", string(status)).
			Msg("synchronized code review status comment")
		return nil
	}
}

func codeReviewStatusCommentBody(metadata models.CodeReviewSessionMetadata, previousCompleted *models.CodeReviewSessionMetadata, sessionURL string) string {
	var paragraphs []string
	switch metadata.Status {
	case models.CodeReviewSessionStatusCompleted:
		if body := strings.TrimSpace(stringPtrValue(metadata.FinalReviewBody)); body != "" {
			paragraphs = append(paragraphs, body)
		} else if metadata.Decision != nil && *metadata.Decision == models.CodeReviewDecisionApproved {
			paragraphs = append(paragraphs, "143 Code Reviewer approved this PR.")
		} else {
			paragraphs = append(paragraphs, "143 Code Reviewer completed its review.")
		}
	case models.CodeReviewSessionStatusFailed:
		paragraphs = append(paragraphs, "143 Code Reviewer could not complete this review.")
		if message := strings.TrimSpace(stringPtrValue(metadata.StatusMessage)); message != "" {
			paragraphs = append(paragraphs, message)
		}
	case models.CodeReviewSessionStatusStale:
		paragraphs = append(paragraphs, "143 Code Reviewer stopped this review because the pull request changed before the result was published.")
	case models.CodeReviewSessionStatusCancelled:
		paragraphs = append(paragraphs, "This 143 code review was cancelled.")
	default:
		previousBody := ""
		if previousCompleted != nil {
			previousBody = strings.TrimSpace(stringPtrValue(previousCompleted.FinalReviewBody))
			if previousBody == "" && previousCompleted.Decision != nil && *previousCompleted.Decision == models.CodeReviewDecisionApproved {
				previousBody = "143 Code Reviewer approved this PR."
			} else if previousBody == "" {
				previousBody = "143 Code Reviewer completed its previous review."
			}
		}
		if previousBody == "" {
			paragraphs = append(paragraphs, "143 Code Reviewer has started reviewing this pull request.")
			break
		}
		paragraphs = append(paragraphs, codereviewsvc.WithCodeReviewReassessmentHistory(
			previousBody,
			metadata.HeadSHA,
			metadata.CreatedAt,
			sessionURL,
		))
	}
	if sessionURL != "" && !strings.Contains(strings.Join(paragraphs, "\n\n"), sessionURL) {
		label := "Follow the review session"
		if codeReviewMetadataTerminal(metadata.Status) {
			label = "View the review session"
		}
		paragraphs = append(paragraphs, fmt.Sprintf("[%s](%s)", label, sessionURL))
	}
	return strings.Join(paragraphs, "\n\n")
}

func enqueueCodeReviewStatusCommentSync(ctx context.Context, stores *Stores, services *Services, logger zerolog.Logger, job runCodeReviewPayload, stage string) {
	if strings.TrimSpace(stage) == "terminal" && job.TriggeringDisputeID != nil &&
		stores != nil && stores.CodeReviews != nil && stores.CodeReviewDisputes != nil &&
		services != nil && services.CodeReviewDisputes != nil {
		metadata, err := stores.CodeReviews.GetBySessionID(ctx, job.OrgID, job.SessionID)
		if err != nil {
			logger.Warn().Err(err).
				Str("session_id", job.SessionID.String()).
				Str("dispute_id", job.TriggeringDisputeID.String()).
				Msg("failed to load terminal reassessment for dispute reconciliation")
		} else {
			detail := strings.TrimSpace(stringPtrValue(metadata.StatusMessage))
			transitioned, completeErr := stores.CodeReviewDisputes.CompleteReassessmentOnce(ctx, job.OrgID, *job.TriggeringDisputeID, job.SessionID, metadata.Status, metadata.Decision, detail)
			if completeErr != nil {
				logger.Warn().Err(completeErr).
					Str("session_id", job.SessionID.String()).
					Str("dispute_id", job.TriggeringDisputeID.String()).
					Msg("failed to reconcile terminal reassessment with dispute")
			}
			if err := services.CodeReviewDisputes.EnqueueReply(ctx, job.OrgID, *job.TriggeringDisputeID, "terminal"); err != nil {
				logger.Warn().Err(err).
					Str("session_id", job.SessionID.String()).
					Str("dispute_id", job.TriggeringDisputeID.String()).
					Msg("failed to enqueue terminal dispute reply")
			} else if completeErr == nil && transitioned && stores.AuditLogs != nil {
				resourceID := job.TriggeringDisputeID.String()
				details, marshalErr := json.Marshal(map[string]any{
					"reassessment_session_id": job.SessionID,
					"status":                  metadata.Status,
					"decision":                metadata.Decision,
				})
				if marshalErr != nil {
					logger.Warn().Err(marshalErr).Str("dispute_id", resourceID).Msg("failed to marshal code review dispute reassessment audit")
				} else {
					db.NewAuditEmitter(stores.AuditLogs, logger).EmitSystemAction(ctx, db.SystemActionParams{
						OrgID: job.OrgID, ActorID: "code_review_dispute_reassessment",
						Action: models.AuditActionCodeReviewDisputeReassessed, ResourceType: models.AuditResourceCodeReviewDispute,
						ResourceID: &resourceID, Details: details, SessionID: &job.SessionID,
					})
				}
			}
		}
	}
	if strings.TrimSpace(stage) == "terminal" && stores != nil && stores.Jobs != nil &&
		services != nil && services.CodeReviewInsights != nil {
		dedupeKey := fmt.Sprintf("code_review_outcome:%s", job.SessionID)
		if _, err := stores.Jobs.EnqueueWithOpts(ctx, job.OrgID, db.EnqueueOpts{
			Queue: "feedback", JobType: models.JobTypeReconcileCodeReviewOutcomes,
			Payload:  codeReviewInsightJobPayload{OrgID: job.OrgID, SessionID: &job.SessionID},
			Priority: 3, DedupeKey: &dedupeKey,
		}); err != nil {
			logger.Warn().Err(err).Str("session_id", job.SessionID.String()).
				Msg("failed to enqueue code review decision outcome projection")
		}
	}
	if stores == nil || stores.Jobs == nil || services == nil || services.CodeReviews == nil {
		return
	}
	if _, ok := services.CodeReviews.(codeReviewStatusCommentUpdater); !ok {
		return
	}
	dedupeKey := fmt.Sprintf("code_review_status_comment:%s:%s", job.SessionID, strings.TrimSpace(stage))
	if _, err := stores.Jobs.EnqueueWithOpts(ctx, job.OrgID, db.EnqueueOpts{
		Queue:   "default",
		JobType: models.JobTypeSyncCodeReviewStatusComment,
		Payload: codereviewsvc.SyncReviewStatusCommentJobPayload{
			OrgID:               job.OrgID,
			SessionID:           job.SessionID,
			RepositoryID:        job.RepositoryID,
			PullRequestID:       job.PullRequestID,
			TriggeringDisputeID: job.TriggeringDisputeID,
		},
		Priority:    3,
		DedupeKey:   &dedupeKey,
		MaxAttempts: codeReviewStatusCommentJobMaxAttempts,
	}); err != nil {
		logger.Warn().Err(err).
			Str("session_id", job.SessionID.String()).
			Str("stage", stage).
			Msg("failed to enqueue best-effort code review status comment sync")
	}
}
