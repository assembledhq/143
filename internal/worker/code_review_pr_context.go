package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	agentsvc "github.com/assembledhq/143/internal/services/agent"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/jackc/pgx/v5"
)

type codeReviewPullRequestContext struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Unavailable bool   `json:"unavailable,omitempty"`
}

// Freeze the coordinator's PR metadata before the first reviewer dispatch so
// a same-head edit cannot change the inputs for a retry or fallback reviewer.
func loadCodeReviewPullRequestContext(ctx context.Context, records codeReviewPromptCheckpointStore, job runCodeReviewPayload, target agentsvc.CodeReviewInputTarget, snapshot ghservice.CodeReviewPullRequestSnapshot, assessment *models.CodeReviewAssessment, results []models.CodeReviewAgentResult) (codeReviewPullRequestContext, error) {
	key := codeReviewPromptRecordRoot(models.CodeReviewSessionMetadata{}, job) + "/pull-request-context"
	record, err := records.GetPromptRecordByKey(ctx, job.OrgID, key)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return codeReviewPullRequestContext{}, err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		for _, result := range results {
			if result.Role != models.CodeReviewAgentRoleReviewer {
				continue
			}
			state, ok := parseCodeReviewReviewerStructuredResult(result.StructuredResult)
			if !ok || !state.Unavailable {
				// An older worker started this review without the context block.
				return codeReviewPullRequestContext{}, nil
			}
		}
		captured, err := codeReviewCapturedPullRequestContext(job, target, snapshot, assessment)
		if err != nil {
			return codeReviewPullRequestContext{}, err
		}
		content, err := json.Marshal(captured)
		if err != nil {
			return codeReviewPullRequestContext{}, err
		}
		record = models.CodeReviewPromptRecord{OrgID: job.OrgID, SessionID: job.SessionID, RecordKey: key, Role: string(models.CodeReviewAgentRoleReviewer), Content: string(content)}
		if _, err := records.CreatePromptRecordIfAbsent(ctx, &record); err != nil {
			return codeReviewPullRequestContext{}, err
		}
	}
	if record.OrgID != job.OrgID || record.SessionID != job.SessionID || record.RecordKey != key {
		return codeReviewPullRequestContext{}, errors.New("reviewer pull request context checkpoint identity mismatch")
	}
	var captured codeReviewPullRequestContext
	if err := json.Unmarshal([]byte(record.Content), &captured); err != nil {
		return codeReviewPullRequestContext{}, fmt.Errorf("decode reviewer pull request context checkpoint: %w", err)
	}
	return captured, nil
}

func codeReviewCapturedPullRequestContext(job runCodeReviewPayload, target agentsvc.CodeReviewInputTarget, snapshot ghservice.CodeReviewPullRequestSnapshot, assessment *models.CodeReviewAssessment) (codeReviewPullRequestContext, error) {
	if assessment != nil {
		var manifest codereviewsvc.ReviewInputManifest
		if err := json.Unmarshal(assessment.InputManifest, &manifest); err != nil {
			return codeReviewPullRequestContext{}, fmt.Errorf("decode assessment pull request context: %w", err)
		}
		if manifest.Code.OrgID != job.OrgID || manifest.Code.RepositoryID != job.RepositoryID || manifest.Code.PullRequestID != job.PullRequestID ||
			manifest.Code.BaseSHA != target.BaseSHA || manifest.Code.HeadSHA != target.HeadSHA || manifest.InputDigest != assessment.InputDigest {
			return codeReviewPullRequestContext{}, errors.New("assessment pull request context does not match the captured review target")
		}
		return codeReviewPullRequestContext{Title: manifest.Title, Description: manifest.Description}, nil
	}
	if snapshot.Number != target.PullRequestNumber || snapshot.HeadSHA != target.HeadSHA || snapshot.BaseSHA != target.BaseSHA {
		return codeReviewPullRequestContext{Unavailable: true}, nil
	}
	return codeReviewPullRequestContext{Title: snapshot.Title, Description: snapshot.Body}, nil
}
