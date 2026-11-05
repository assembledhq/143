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

const codeReviewHistoryInputsChangedMessage = "review history inputs changed before dispatch"

var errCodeReviewHistoryNotRead = errors.New("requested review history was not consulted")

type codeReviewHistoryPromptStore interface {
	codeReviewPromptCheckpointStore
	ListPriorRounds(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, *uuid.UUID, int) ([]db.CodeReviewHistoryRound, error)
}

type codeReviewHistoryPromptMetadata struct {
	ThreadID             uuid.UUID                            `json:"review_history_thread_id"`
	History              *prompts.CodeReviewHistoryPromptData `json:"review_history"`
	AgentModel           string                               `json:"agent_model"`
	DescriptionInputHash string                               `json:"input_hash"`
}

type codeReviewHistoryPromptChangedError struct {
	Record models.CodeReviewPromptRecord
	Field  string
}

func (e *codeReviewHistoryPromptChangedError) Error() string {
	return fmt.Sprintf("%s (%s); request a new full review", codeReviewHistoryInputsChangedMessage, e.Field)
}

func codeReviewOrchestratorPromptKey(root string, attempt int, provider models.AgentType) string {
	if attempt > 0 {
		return fmt.Sprintf("%s/orchestrator-fallback-%02d-%s", root, attempt, provider)
	}
	return fmt.Sprintf("%s/orchestrator-%s", root, provider)
}

// Recover a dispatched turn before resolving live model availability or inputs.
// The result write can have failed after SendMessage succeeded. Its saved
// identity and description hash must win; no new prompt or attachments are sent.
func recoverCodeReviewHistoryOrchestrator(ctx context.Context, stores *Stores, job runCodeReviewPayload, root string, attempt int) (*models.CodeReviewPromptRecord, bool, error) {
	records, err := stores.CodeReviews.ListPromptRecords(ctx, job.OrgID, job.SessionID)
	if err != nil {
		return nil, false, fmt.Errorf("load review history dispatch checkpoints: %w", err)
	}
	var checkpoint *models.CodeReviewPromptRecord
	for _, record := range records {
		if record.Role != string(models.CodeReviewAgentRoleOrchestrator) || record.RecordKey != codeReviewOrchestratorPromptKey(root, attempt, models.AgentType(record.AgentProvider)) {
			continue
		}
		if checkpoint != nil {
			return nil, false, errors.New("multiple review history checkpoints for one orchestration attempt")
		}
		checkpoint = &record
	}
	if checkpoint == nil {
		return nil, false, nil
	}
	var metadata codeReviewHistoryPromptMetadata
	if err := json.Unmarshal(checkpoint.Metadata, &metadata); err != nil {
		return nil, false, fmt.Errorf("decode review history dispatch checkpoint: %w", err)
	}
	if _, err := validateCodeReviewHistoryPrompt(*checkpoint, job, checkpoint.RecordKey, metadata.ThreadID); err != nil {
		return nil, false, err
	}
	thread, err := stores.SessionThreads.GetByID(ctx, job.OrgID, metadata.ThreadID)
	if err != nil {
		return nil, false, fmt.Errorf("load bound review history thread: %w", err)
	}
	if thread.OrgID != job.OrgID || thread.SessionID != job.SessionID || metadata.DescriptionInputHash == "" {
		return nil, false, errors.New("review history dispatch checkpoint does not match its bound thread")
	}
	if thread.Status == models.ThreadStatusIdle && thread.CurrentTurn == 0 {
		return checkpoint, false, nil
	}
	if string(thread.AgentType) != checkpoint.AgentProvider || strings.TrimSpace(stringPtrValue(thread.ModelOverride)) != strings.TrimSpace(metadata.AgentModel) {
		return nil, false, errors.New("dispatched review history thread does not match its saved model")
	}
	result, err := codeReviewHistoryCheckpointResult(job, *checkpoint, models.CodeReviewAgentResultStatusRunning, "")
	if err != nil {
		return nil, false, err
	}
	result.StructuredResult = marshalCodeReviewOrchestratorStructuredResult(codeReviewOrchestratorStructuredResult{
		ThreadID: metadata.ThreadID.String(), PromptRecordKey: checkpoint.RecordKey, DescriptionInputHash: metadata.DescriptionInputHash,
		ReadOnly: thread.ExecutionMode == models.ThreadExecutionModeReview && thread.FilesystemMode == models.ThreadFilesystemModeReadOnly,
	})
	if err := stores.CodeReviews.CreateAgentResult(ctx, result); err != nil {
		return nil, false, fmt.Errorf("recover dispatched review history orchestrator: %w", err)
	}
	return checkpoint, true, nil
}

func codeReviewHistoryCheckpointResult(job runCodeReviewPayload, record models.CodeReviewPromptRecord, status models.CodeReviewAgentResultStatus, failure string) (*models.CodeReviewAgentResult, error) {
	var metadata codeReviewHistoryPromptMetadata
	if err := json.Unmarshal(record.Metadata, &metadata); err != nil {
		return nil, fmt.Errorf("decode review history result checkpoint: %w", err)
	}
	if _, err := validateCodeReviewHistoryPrompt(record, job, record.RecordKey, metadata.ThreadID); err != nil {
		return nil, err
	}
	state := codeReviewOrchestratorStructuredResult{ThreadID: metadata.ThreadID.String(), PromptRecordKey: record.RecordKey, DescriptionInputHash: metadata.DescriptionInputHash}
	var raw *string
	if failure != "" {
		state.Error, state.CompletedAt = failure, time.Now().UTC().Format(time.RFC3339)
		raw = &failure
	}
	var model *string
	if metadata.AgentModel != "" {
		model = &metadata.AgentModel
	}
	return &models.CodeReviewAgentResult{OrgID: job.OrgID, SessionID: job.SessionID, AgentProvider: record.AgentProvider,
		AgentModel: model, Role: models.CodeReviewAgentRoleOrchestrator, Status: status, RawOutput: raw,
		StructuredResult: marshalCodeReviewOrchestratorStructuredResult(state)}, nil
}

func failCodeReviewHistoryDispatch(ctx context.Context, stores *Stores, job runCodeReviewPayload, record models.CodeReviewPromptRecord, cause error) error {
	result, err := codeReviewHistoryCheckpointResult(job, record, models.CodeReviewAgentResultStatusFailed, cause.Error())
	if err != nil {
		return err
	}
	if err := stores.CodeReviews.CreateAgentResult(ctx, result); err != nil {
		return fmt.Errorf("record changed review history dispatch inputs: %w", err)
	}
	return nil
}

// prepareCodeReviewHistoryPrompt binds permission and references atomically to
// the exact synthesis thread before it can execute. Retries reuse the original
// prompt rather than changing what a possibly already-dispatched agent saw.
func prepareCodeReviewHistoryPrompt(ctx context.Context, records codeReviewHistoryPromptStore, job runCodeReviewPayload, threadID uuid.UUID, record models.CodeReviewPromptRecord, render func(*prompts.CodeReviewHistoryPromptData) string) (string, error) {
	inputRecord := record
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
	if err := validateCodeReviewHistoryDispatchInputs(saved, inputRecord); err != nil {
		return "", err
	}
	return saved.Content, nil
}

func validateCodeReviewHistoryDispatchInputs(saved, record models.CodeReviewPromptRecord) error {
	if saved.RecordKey != record.RecordKey || saved.AgentProvider != record.AgentProvider {
		return &codeReviewHistoryPromptChangedError{Record: saved, Field: "agent_provider"}
	}
	var expected, actual map[string]json.RawMessage
	if err := json.Unmarshal(record.Metadata, &expected); err != nil {
		return fmt.Errorf("decode current review history inputs: %w", err)
	}
	if err := json.Unmarshal(saved.Metadata, &actual); err != nil {
		return fmt.Errorf("decode saved review history inputs: %w", err)
	}
	for key, value := range expected {
		if !reflect.DeepEqual(value, actual[key]) {
			return &codeReviewHistoryPromptChangedError{Record: saved, Field: key}
		}
	}
	return nil
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
