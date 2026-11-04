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
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const codeReviewHistoryIndexLimit = 10

var errCodeReviewHistoryNotRead = errors.New("requested review history was not consulted")

type codeReviewHistoryPromptStore interface {
	codeReviewGuidanceRecordStore
	ListPriorRounds(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, *uuid.UUID, int) ([]db.CodeReviewHistoryRound, error)
}

type codeReviewHistoryPromptMetadata struct {
	ThreadID uuid.UUID                            `json:"review_history_thread_id"`
	History  *prompts.CodeReviewHistoryPromptData `json:"review_history"`
}

// prepareCodeReviewHistoryPrompt binds permission and references atomically to
// the exact synthesis thread before it can execute. Retries reuse the original
// prompt rather than changing what a possibly already-dispatched agent saw.
func prepareCodeReviewHistoryPrompt(ctx context.Context, records codeReviewHistoryPromptStore, job runCodeReviewPayload, threadID uuid.UUID, record models.CodeReviewPromptRecord, render func(*prompts.CodeReviewHistoryPromptData) string) (string, error) {
	expectedMetadata := record.Metadata
	saved, err := records.GetPromptRecordByKey(ctx, job.OrgID, record.RecordKey)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("load review history prompt: %w", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		rounds, err := records.ListPriorRounds(ctx, job.OrgID, job.RepositoryID, job.SessionID, nil, codeReviewHistoryIndexLimit+1)
		if err != nil {
			return "", fmt.Errorf("list prior review references: %w", err)
		}
		history := &prompts.CodeReviewHistoryPromptData{SessionID: job.SessionID.String(), HasMore: len(rounds) > codeReviewHistoryIndexLimit}
		if history.HasMore {
			rounds = rounds[:codeReviewHistoryIndexLimit]
		}
		for _, round := range rounds {
			assessmentID := ""
			if round.AssessmentID != nil {
				assessmentID = round.AssessmentID.String()
			}
			history.Rounds = append(history.Rounds, prompts.CodeReviewHistoryRoundReference{
				SessionID: round.SessionID.String(), AssessmentID: assessmentID, BaseSHA: round.BaseSHA, HeadSHA: round.HeadSHA,
				CreatedAt: round.CreatedAt.UTC().Format(time.RFC3339),
			})
		}
		var metadata map[string]any
		if err := json.Unmarshal(record.Metadata, &metadata); err != nil {
			return "", fmt.Errorf("decode orchestrator prompt metadata: %w", err)
		}
		if metadata == nil {
			metadata = make(map[string]any)
		}
		metadata["review_history_thread_id"] = threadID
		metadata["review_history"] = history
		record.Metadata, err = json.Marshal(metadata)
		if err != nil {
			return "", fmt.Errorf("encode review history prompt metadata: %w", err)
		}
		record.Content = render(history)
		if _, err := records.CreatePromptRecordIfAbsent(ctx, &record); err != nil {
			return "", fmt.Errorf("save review history prompt: %w", err)
		}
		saved = record
	}
	if _, err := validateCodeReviewHistoryPrompt(saved, job, record.RecordKey, threadID); err != nil {
		return "", err
	}
	// Attachments and the result's description hash are supplied by the caller.
	// They must still describe the inputs used by the immutable saved prompt.
	var expected, actual map[string]json.RawMessage
	if err := json.Unmarshal(expectedMetadata, &expected); err != nil {
		return "", fmt.Errorf("decode current review history inputs: %w", err)
	}
	if err := json.Unmarshal(saved.Metadata, &actual); err != nil {
		return "", fmt.Errorf("decode saved review history inputs: %w", err)
	}
	for key, value := range expected {
		if !reflect.DeepEqual(value, actual[key]) {
			return "", fmt.Errorf("review history prompt input %s changed; a fresh review is required", key)
		}
	}
	return saved.Content, nil
}

func validateCodeReviewHistoryPrompt(record models.CodeReviewPromptRecord, job runCodeReviewPayload, key string, threadID uuid.UUID) (*prompts.CodeReviewHistoryPromptData, error) {
	if record.OrgID != job.OrgID || record.SessionID != job.SessionID || record.RecordKey != key ||
		record.Role != string(models.CodeReviewAgentRoleOrchestrator) || strings.TrimSpace(record.Content) == "" || threadID == uuid.Nil {
		return nil, errors.New("review history prompt identity mismatch")
	}
	var metadata codeReviewHistoryPromptMetadata
	if err := json.Unmarshal(record.Metadata, &metadata); err != nil {
		return nil, fmt.Errorf("decode review history prompt: %w", err)
	}
	if metadata.ThreadID != threadID || metadata.History == nil || metadata.History.SessionID != job.SessionID.String() {
		return nil, errors.New("review history prompt thread binding mismatch")
	}
	return metadata.History, nil
}

// validateCodeReviewHistoryReads verifies access, not the quality of the model's
// reconciliation. Receipts belong to the exact thread, including after fallback.
func validateCodeReviewHistoryReads(records []models.CodeReviewPromptRecord, job runCodeReviewPayload, key string, threadID uuid.UUID) error {
	var history *prompts.CodeReviewHistoryPromptData
	for _, record := range records {
		if record.RecordKey == key {
			var err error
			history, err = validateCodeReviewHistoryPrompt(record, job, key, threadID)
			if err != nil {
				return err
			}
			break
		}
	}
	if history == nil {
		return errors.New("review history prompt checkpoint is missing")
	}
	if len(history.Rounds) == 0 && !history.HasMore {
		return nil
	}
	if len(history.Rounds) == 0 {
		return errors.New("review history prompt has an incomplete reference index")
	}
	latestSessionID, err := uuid.Parse(history.Rounds[0].SessionID)
	if err != nil || latestSessionID == uuid.Nil || latestSessionID == job.SessionID {
		return errors.New("review history prompt has an invalid source reference")
	}
	for _, record := range records {
		if record.OrgID != job.OrgID || record.SessionID != job.SessionID || record.Role != "review_history" {
			continue
		}
		var receipt struct {
			ThreadID        uuid.UUID `json:"review_history_thread_id"`
			SourceSessionID uuid.UUID `json:"source_session_id"`
			Kind            string    `json:"kind"`
		}
		if err := json.Unmarshal(record.Metadata, &receipt); err != nil {
			return fmt.Errorf("decode review history receipt: %w", err)
		}
		// An empty transcript page or a bare index is not a report read. Require
		// the latest referenced round, as the prompt does, before optional dives
		// into older rounds or their conversation excerpts.
		var response struct {
			Data struct {
				SessionID uuid.UUID `json:"session_id"`
			} `json:"data"`
		}
		if receipt.ThreadID == threadID && receipt.SourceSessionID == latestSessionID && receipt.Kind == "prior_round" &&
			json.Unmarshal([]byte(record.Content), &response) == nil && response.Data.SessionID == latestSessionID {
			return nil
		}
	}
	return fmt.Errorf("%w: no successful read of the latest referenced round was recorded for this orchestrator", errCodeReviewHistoryNotRead)
}

func codeReviewHistoryProvenance(input liveCodeReviewOutcomeInput) string {
	if input.Job.RequestContext == nil || !input.Job.RequestContext.UseReviewHistory {
		return input.ReviewProvenance
	}
	return strings.TrimSpace(input.ReviewProvenance + "\nEarlier review history retrieval was enabled for this review (experimental).")
}
