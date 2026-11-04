package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CodeReviewHistoryStore reads earlier rounds of exactly one pull request.
// The current session's creation time bounds both round and message discovery.
type CodeReviewHistoryStore struct{ db DBTX }

func NewCodeReviewHistoryStore(db DBTX) *CodeReviewHistoryStore {
	return &CodeReviewHistoryStore{db: db}
}

// ListPriorRounds shares the scoped index with the worker's compact prompt.
func (s *CodeReviewStore) ListPriorRounds(ctx context.Context, orgID, repositoryID, currentSessionID uuid.UUID, cursor *uuid.UUID, limit int) ([]CodeReviewHistoryRound, error) {
	return NewCodeReviewHistoryStore(s.db).ListPriorRounds(ctx, orgID, repositoryID, currentSessionID, cursor, limit)
}

type CodeReviewHistoryRound struct {
	AssessmentID *uuid.UUID                     `db:"assessment_id" json:"assessment_id,omitempty"`
	SessionID    uuid.UUID                      `db:"session_id" json:"session_id"`
	BaseSHA      string                         `db:"base_sha" json:"base_sha"`
	HeadSHA      string                         `db:"head_sha" json:"head_sha"`
	PolicyID     uuid.UUID                      `db:"policy_id" json:"policy_id"`
	Status       models.CodeReviewSessionStatus `db:"status" json:"status"`
	Decision     *models.CodeReviewDecision     `db:"decision" json:"decision,omitempty"`
	CreatedAt    time.Time                      `db:"created_at" json:"created_at"`
	CompletedAt  *time.Time                     `db:"completed_at" json:"completed_at,omitempty"`
}

type CodeReviewHistoryReport struct {
	EvidenceAssessmentID *uuid.UUID `db:"evidence_assessment_id" json:"source_assessment_id,omitempty"`
	CodeReviewHistoryRound
	FinalReviewBody      string    `db:"final_review_body" json:"final_review_body"`
	FinalReviewBodyRunes int       `db:"final_review_body_runes" json:"final_review_body_runes"`
	Cutoff               time.Time `db:"cutoff" json:"-"`
}

type CodeReviewHistoryFinding struct {
	ID           uuid.UUID                          `db:"id" json:"id"`
	Severity     models.CodeReviewFindingSeverity   `db:"severity" json:"severity"`
	Confidence   models.CodeReviewFindingConfidence `db:"confidence" json:"confidence"`
	Path         *string                            `db:"path" json:"path,omitempty"`
	StartLine    *int                               `db:"start_line" json:"start_line,omitempty"`
	EndLine      *int                               `db:"end_line" json:"end_line,omitempty"`
	Summary      string                             `db:"summary" json:"summary"`
	Body         string                             `db:"body" json:"body"`
	BodyRunes    int                                `db:"body_runes" json:"body_runes"`
	PostedInline bool                               `db:"posted_inline" json:"posted_inline"`
}

type CodeReviewHistoryThread struct {
	ID            uuid.UUID                   `db:"id" json:"thread_id"`
	Label         string                      `db:"label" json:"label"`
	AgentProvider models.AgentType            `db:"agent_type" json:"agent_provider"`
	Status        models.ThreadStatus         `db:"status" json:"status"`
	Role          *models.CodeReviewAgentRole `db:"role" json:"role,omitempty"`
}

type CodeReviewHistoryAgentResult struct {
	ID                uuid.UUID                          `db:"id" json:"id"`
	AgentProvider     string                             `db:"agent_provider" json:"agent_provider"`
	Role              models.CodeReviewAgentRole         `db:"role" json:"role"`
	Status            models.CodeReviewAgentResultStatus `db:"status" json:"status"`
	ThreadID          *string                            `db:"thread_id" json:"thread_id,omitempty"`
	Output            string                             `db:"output" json:"output"`
	OutputRunes       int                                `db:"output_runes" json:"output_runes"`
	OutputUnavailable bool                               `db:"output_unavailable" json:"output_unavailable"`
}

type CodeReviewHistoryMessage struct {
	ID           int64              `db:"id" json:"id"`
	Role         models.MessageRole `db:"role" json:"role"`
	TurnNumber   int                `db:"turn_number" json:"turn_number"`
	Content      string             `db:"content" json:"content"`
	ContentRunes int                `db:"content_runes" json:"content_runes"`
	CreatedAt    time.Time          `db:"created_at" json:"created_at"`
}

// AuthorizeSource checks the pre-dispatch binding, rather than agent results:
// the latter can be persisted after the orchestrator starts making tool calls.
func (s *CodeReviewHistoryStore) AuthorizeSource(ctx context.Context, orgID, repositoryID, sessionID, threadID uuid.UUID) error {
	var allowed bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM code_review_session_metadata m
		JOIN session_threads t ON t.org_id = m.org_id AND t.session_id = m.session_id
		JOIN code_review_prompt_records p ON p.org_id = m.org_id AND p.session_id = m.session_id
		WHERE m.org_id = @org_id AND m.repository_id = @repository_id AND m.session_id = @session_id
		  AND m.status IN ('queued', 'running') AND m.stale = false AND m.superseded_by_session_id IS NULL
		  AND t.id = @thread_id AND t.archived_at IS NULL
		  AND p.role = 'orchestrator' AND p.metadata->>'review_history_thread_id' = @thread_id_text
	)`, pgx.NamedArgs{"org_id": orgID, "repository_id": repositoryID, "session_id": sessionID,
		"thread_id": threadID, "thread_id_text": threadID.String()}).Scan(&allowed)
	if err != nil {
		return fmt.Errorf("authorize prior review history: %w", err)
	}
	if !allowed {
		return pgx.ErrNoRows
	}
	return nil
}

const priorRoundColumns = `prior.session_id, assessment.id AS assessment_id,
 COALESCE(assessment.base_sha, prior.base_sha) AS base_sha,
 COALESCE(assessment.head_sha, prior.head_sha) AS head_sha,
 COALESCE(assessment.policy_id, prior.policy_id) AS policy_id,
 COALESCE(assessment.status, prior.status) AS status,
 COALESCE(assessment.decision, prior.decision) AS decision, prior.created_at,
 COALESCE(assessment.completed_at, prior.completed_at) AS completed_at`

const priorRoundScope = `
	FROM code_review_session_metadata current
	JOIN sessions source ON source.org_id = current.org_id AND source.id = current.session_id
	JOIN code_review_session_metadata prior ON prior.org_id = current.org_id
	  AND prior.repository_id = current.repository_id AND prior.pull_request_id = current.pull_request_id
 LEFT JOIN LATERAL (
  SELECT a.id, a.source_assessment_id, a.base_sha, a.head_sha, a.policy_id, a.status, a.decision, a.completed_at, a.rendered_body
  FROM code_review_revision_assessments a
  WHERE a.org_id = prior.org_id AND a.session_id = prior.session_id
    AND a.repository_id = prior.repository_id AND a.pull_request_id = prior.pull_request_id
    AND a.status = 'completed' AND a.completed_at < source.created_at
  ORDER BY a.generation DESC LIMIT 1
 ) assessment ON true
 WHERE current.org_id = @org_id AND current.repository_id = @repository_id AND current.session_id = @current_session_id
	  AND prior.session_id <> current.session_id AND prior.created_at < source.created_at
	  AND (assessment.id IS NOT NULL OR (prior.status = 'completed' AND prior.completed_at < source.created_at AND NOT EXISTS (
  SELECT 1 FROM code_review_revision_assessments any_assessment
  WHERE any_assessment.org_id = prior.org_id AND any_assessment.session_id = prior.session_id
  )))`

func (s *CodeReviewHistoryStore) ListPriorRounds(ctx context.Context, orgID, repositoryID, currentSessionID uuid.UUID, cursor *uuid.UUID, limit int) ([]CodeReviewHistoryRound, error) {
	rows, err := s.db.Query(ctx, `SELECT `+priorRoundColumns+priorRoundScope+`
	  AND (@cursor::uuid IS NULL OR (prior.created_at, prior.session_id) < (
		SELECT c.created_at, c.session_id FROM code_review_session_metadata c
		WHERE c.org_id = current.org_id AND c.repository_id = current.repository_id
		  AND c.pull_request_id = current.pull_request_id AND c.session_id = @cursor
		  AND c.created_at < source.created_at
	  ))
	ORDER BY prior.created_at DESC, prior.session_id DESC LIMIT @limit`, pgx.NamedArgs{
		"org_id": orgID, "repository_id": repositoryID, "current_session_id": currentSessionID, "cursor": cursor, "limit": limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list prior review rounds: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[CodeReviewHistoryRound])
}

func (s *CodeReviewHistoryStore) GetPriorRound(ctx context.Context, orgID, repositoryID, currentSessionID, priorSessionID uuid.UUID) (CodeReviewHistoryReport, error) {
	rows, err := s.db.Query(ctx, `SELECT `+priorRoundColumns+`,
		left(CASE WHEN assessment.id IS NOT NULL THEN COALESCE(assessment.rendered_body, '') ELSE COALESCE(prior.final_review_body, '') END, 16000) AS final_review_body,
		char_length(CASE WHEN assessment.id IS NOT NULL THEN COALESCE(assessment.rendered_body, '') ELSE COALESCE(prior.final_review_body, '') END) AS final_review_body_runes,
		source.created_at AS cutoff, COALESCE(assessment.source_assessment_id, assessment.id) AS evidence_assessment_id`+priorRoundScope+` AND prior.session_id = @prior_session_id`, pgx.NamedArgs{
		"org_id": orgID, "repository_id": repositoryID, "current_session_id": currentSessionID, "prior_session_id": priorSessionID,
	})
	if err != nil {
		return CodeReviewHistoryReport{}, fmt.Errorf("get prior review round: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[CodeReviewHistoryReport])
}

func (s *CodeReviewHistoryStore) ListPriorFindings(ctx context.Context, orgID, sessionID uuid.UUID, assessmentID *uuid.UUID, cutoff time.Time, limit int) ([]CodeReviewHistoryFinding, error) {
	rows, err := s.db.Query(ctx, `SELECT id, severity, confidence, left(path, 1000) AS path, start_line, end_line,
		left(summary, 500) AS summary, left(body, 2000) AS body, char_length(body) AS body_runes,
		github_comment_id IS NOT NULL AS posted_inline
		FROM code_review_findings WHERE org_id = @org_id AND session_id = @session_id AND created_at < @cutoff
		AND assessment_id IS NOT DISTINCT FROM @assessment_id::uuid
		ORDER BY created_at, id LIMIT @limit`, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID, "assessment_id": assessmentID, "cutoff": cutoff, "limit": limit})
	if err != nil {
		return nil, fmt.Errorf("list prior review findings: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[CodeReviewHistoryFinding])
}

func (s *CodeReviewHistoryStore) ListPriorThreads(ctx context.Context, orgID, sessionID uuid.UUID, cutoff time.Time, limit int) ([]CodeReviewHistoryThread, error) {
	rows, err := s.db.Query(ctx, `SELECT t.id, left(t.label, 200) AS label, t.agent_type, t.status,
		(SELECT r.role FROM code_review_agent_results r WHERE r.org_id = t.org_id AND r.session_id = t.session_id
		 AND r.structured_result->>'thread_id' = t.id::text AND r.created_at < @cutoff ORDER BY r.created_at DESC, r.id DESC LIMIT 1) AS role
		FROM session_threads t WHERE t.org_id = @org_id AND t.session_id = @session_id AND t.created_at < @cutoff
		ORDER BY t.created_at, t.id LIMIT @limit`, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID, "cutoff": cutoff, "limit": limit})
	if err != nil {
		return nil, fmt.Errorf("list prior review threads: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[CodeReviewHistoryThread])
}

func (s *CodeReviewHistoryStore) ListPriorAgentResults(ctx context.Context, orgID, sessionID uuid.UUID, assessmentID *uuid.UUID, cutoff time.Time, limit int) ([]CodeReviewHistoryAgentResult, error) {
	rows, err := s.db.Query(ctx, `SELECT r.id, r.agent_provider, r.role, r.status, r.structured_result->>'thread_id' AS thread_id,
		left(COALESCE(output.content, r.raw_output, ''), 4000) AS output,
		char_length(COALESCE(output.content, r.raw_output, '')) AS output_runes,
		(COALESCE(r.structured_result->>'raw_record_key', r.structured_result->>'raw_artifact_key', '') <> '' AND output.id IS NULL) AS output_unavailable
		FROM code_review_agent_results r
		LEFT JOIN code_review_prompt_records output ON output.org_id = r.org_id AND output.session_id = r.session_id
		  AND output.record_key = COALESCE(r.structured_result->>'raw_record_key', r.structured_result->>'raw_artifact_key')
		  AND output.role IN ('reviewer_output', 'orchestrator_output') AND output.created_at < @cutoff
		WHERE r.org_id = @org_id AND r.session_id = @session_id AND r.created_at < @cutoff
		  AND r.assessment_id IS NOT DISTINCT FROM @assessment_id::uuid
		ORDER BY r.created_at, r.id LIMIT @limit`, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID, "assessment_id": assessmentID, "cutoff": cutoff, "limit": limit})
	if err != nil {
		return nil, fmt.Errorf("list prior reviewer results: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[CodeReviewHistoryAgentResult])
}

func (s *CodeReviewHistoryStore) GetReceipt(ctx context.Context, orgID, sessionID uuid.UUID, key string) (json.RawMessage, error) {
	record, err := NewCodeReviewStore(s.db).GetPromptRecordByKey(ctx, orgID, key)
	if err != nil {
		return nil, err
	}
	if record.SessionID != sessionID || record.Role != "review_history" || !json.Valid([]byte(record.Content)) {
		return nil, fmt.Errorf("invalid prior review retrieval receipt")
	}
	return json.RawMessage(record.Content), nil
}

func (s *CodeReviewHistoryStore) SaveReceipt(ctx context.Context, orgID, sessionID uuid.UUID, key string, content, metadata json.RawMessage) (json.RawMessage, error) {
	record := &models.CodeReviewPromptRecord{
		OrgID: orgID, SessionID: sessionID, RecordKey: key, Role: "review_history", Content: string(content), Metadata: metadata,
	}
	if _, err := NewCodeReviewStore(s.db).CreatePromptRecordIfAbsent(ctx, record); err != nil {
		return nil, err
	}
	if record.SessionID != sessionID || record.Role != "review_history" || !json.Valid([]byte(record.Content)) {
		return nil, fmt.Errorf("invalid saved prior review retrieval receipt")
	}
	return json.RawMessage(record.Content), nil
}

func (s *CodeReviewHistoryStore) HasPriorThread(ctx context.Context, orgID, sessionID, threadID uuid.UUID, cutoff time.Time) (bool, error) {
	var found bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM session_threads
		WHERE org_id = @org_id AND session_id = @session_id AND id = @thread_id AND created_at < @cutoff)`,
		pgx.NamedArgs{"org_id": orgID, "session_id": sessionID, "thread_id": threadID, "cutoff": cutoff}).Scan(&found)
	return found, err
}

func (s *CodeReviewHistoryStore) ValidMessageCursor(ctx context.Context, orgID, sessionID, threadID uuid.UUID, cutoff time.Time, messageID int64, offset int) (bool, error) {
	var found bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM session_messages
		WHERE org_id = @org_id AND session_id = @session_id AND thread_id = @thread_id
		  AND id = @message_id AND created_at < @cutoff AND @offset <= char_length(content))`,
		pgx.NamedArgs{"org_id": orgID, "session_id": sessionID, "thread_id": threadID, "cutoff": cutoff,
			"message_id": messageID, "offset": offset}).Scan(&found)
	return found, err
}

// ListPriorMessages returns at most one bounded chunk per message. Offset is
// applied only to the cursor message; callers stop at a partial chunk and use
// the returned offset to continue without silently discarding its tail.
func (s *CodeReviewHistoryStore) ListPriorMessages(ctx context.Context, orgID, sessionID, threadID uuid.UUID, cutoff time.Time, cursorID int64, offset, limit int) ([]CodeReviewHistoryMessage, error) {
	rows, err := s.db.Query(ctx, `SELECT m.id, m.role, m.turn_number,
		substring(m.content FROM CASE WHEN m.id = @cursor_id THEN @offset + 1 ELSE 1 END FOR 4000) AS content,
		char_length(m.content) AS content_runes, m.created_at
		FROM session_messages m JOIN session_threads t ON t.org_id = m.org_id AND t.id = m.thread_id AND t.session_id = m.session_id
		WHERE m.org_id = @org_id AND m.session_id = @session_id AND m.thread_id = @thread_id
		  AND m.created_at < @cutoff AND t.created_at < @cutoff
		  AND (m.id > @cursor_id OR (m.id = @cursor_id AND @offset > 0))
		ORDER BY m.id LIMIT @limit`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "thread_id": threadID, "cutoff": cutoff,
		"cursor_id": cursorID, "offset": offset, "limit": limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list prior review messages: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[CodeReviewHistoryMessage])
}
