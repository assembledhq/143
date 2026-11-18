package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type priorReviewHistoryStore interface {
	AuthorizeSource(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) error
	ListPriorRounds(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, *uuid.UUID, int) ([]db.CodeReviewHistoryRound, error)
	GetPriorRound(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) (db.CodeReviewHistoryReport, error)
	ListPriorFindings(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID, time.Time, int) ([]db.CodeReviewHistoryFinding, error)
	ListPriorThreads(context.Context, uuid.UUID, uuid.UUID, time.Time, int) ([]db.CodeReviewHistoryThread, error)
	ListPriorAgentResults(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID, time.Time, int) ([]db.CodeReviewHistoryAgentResult, error)
	HasPriorThread(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, time.Time) (bool, error)
	ValidMessageCursor(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, time.Time, int64, int) (bool, error)
	ListPriorMessages(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, time.Time, int64, int, int) ([]db.CodeReviewHistoryMessage, error)
	GetReceipt(context.Context, uuid.UUID, uuid.UUID, string) (json.RawMessage, error)
	SaveReceipt(context.Context, uuid.UUID, uuid.UUID, string, json.RawMessage, json.RawMessage) (json.RawMessage, error)
}

// InternalCodeReviewHistoryHandler grants only the bound orchestrator access to
// prior rounds of its own PR. Generic history capabilities are never granted.
type InternalCodeReviewHistoryHandler struct {
	history       priorReviewHistoryStore
	sessions      internalSessionGetter
	signingSecret string
}

func NewInternalCodeReviewHistoryHandler(history priorReviewHistoryStore, sessions internalSessionGetter, signingSecret string) *InternalCodeReviewHistoryHandler {
	return &InternalCodeReviewHistoryHandler{history: history, sessions: sessions, signingSecret: signingSecret}
}

func (h *InternalCodeReviewHistoryHandler) authorize(w http.ResponseWriter, r *http.Request) (*auth.InternalTokenClaims, bool) {
	claims, session, ok := authorizeInternalSession(w, r, h.signingSecret, h.sessions)
	if !ok {
		return nil, false
	}
	var revision struct {
		RequestContext struct {
			UseReviewHistory bool `json:"use_review_history"`
		} `json:"request_context"`
	}
	if session.OrgID != claims.OrgID || session.ID != *claims.SessionID || session.Origin != models.SessionOriginCodeReview ||
		claims.ThreadID == nil || *claims.ThreadID == uuid.Nil || json.Unmarshal(session.RevisionContext, &revision) != nil || !revision.RequestContext.UseReviewHistory {
		writeError(w, r, http.StatusForbidden, "REVIEW_HISTORY_DENIED", "prior review history is only available to an opted-in review orchestrator")
		return nil, false
	}
	if err := h.history.AuthorizeSource(r.Context(), claims.OrgID, claims.RepoID, *claims.SessionID, *claims.ThreadID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusForbidden, "REVIEW_HISTORY_DENIED", "this thread does not own an active review history grant")
		} else {
			writeError(w, r, http.StatusInternalServerError, "REVIEW_HISTORY_FAILED", "failed to authorize prior review history", err)
		}
		return nil, false
	}
	return claims, true
}

// List returns compact references only; content is retrieved deliberately.
func (h *InternalCodeReviewHistoryHandler) List(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authorize(w, r)
	if !ok {
		return
	}
	limit, ok := priorReviewLimit(w, r, 10, 50)
	if !ok {
		return
	}
	var cursor *uuid.UUID
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "cursor must be a prior session ID")
			return
		}
		cursor = &id
		if _, err := h.history.GetPriorRound(r.Context(), claims.OrgID, claims.RepoID, *claims.SessionID, id); err != nil {
			h.readError(w, r, err)
			return
		}
	}
	items, err := h.history.ListPriorRounds(r.Context(), claims.OrgID, claims.RepoID, *claims.SessionID, cursor, limit+1)
	if err != nil {
		h.readError(w, r, err)
		return
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = items[len(items)-1].SessionID.String()
	}
	writeJSON(w, http.StatusOK, models.ListResponse[db.CodeReviewHistoryRound]{Data: items, Meta: models.PaginationMeta{NextCursor: next}})
}

type priorReviewRoundDetail struct {
	db.CodeReviewHistoryReport
	SourceFindings       []db.CodeReviewHistoryFinding          `json:"source_findings"`
	SourceAgentResults   []db.CodeReviewHistoryAgentResult      `json:"source_agent_results"`
	FindingReassessments []models.CodeReviewFindingReassessment `json:"finding_reassessments"`
	Threads              []db.CodeReviewHistoryThread           `json:"threads"`
	Truncated            bool                                   `json:"truncated"`
}

func (h *InternalCodeReviewHistoryHandler) Get(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authorize(w, r)
	if !ok {
		return
	}
	sourceID, ok := priorReviewID(w, r, "session_id")
	if !ok {
		return
	}
	h.withReceipt(w, r, claims, sourceID, "prior_round", priorReviewReceiptParameters{}, func() (any, error) {
		report, err := h.history.GetPriorRound(r.Context(), claims.OrgID, claims.RepoID, *claims.SessionID, sourceID)
		if err != nil {
			return nil, err
		}
		findings, err := h.history.ListPriorFindings(r.Context(), claims.OrgID, sourceID, report.EvidenceAssessmentID, report.Cutoff, 51)
		if err != nil {
			return nil, err
		}
		threads, err := h.history.ListPriorThreads(r.Context(), claims.OrgID, sourceID, report.Cutoff, 51)
		if err != nil {
			return nil, err
		}
		results, err := h.history.ListPriorAgentResults(r.Context(), claims.OrgID, sourceID, report.EvidenceAssessmentID, report.Cutoff, 21)
		if err != nil {
			return nil, err
		}
		reassessments, reassessmentsTruncated, err := priorReviewFindingReassessments(report, findings)
		if err != nil {
			return nil, err
		}
		detail := priorReviewRoundDetail{CodeReviewHistoryReport: report,
			SourceFindings: findings[:min(len(findings), 50)], Threads: threads[:min(len(threads), 50)], SourceAgentResults: results[:min(len(results), 20)], FindingReassessments: reassessments,
			Truncated: reassessmentsTruncated || report.FinalReviewBodyRunes > utf8.RuneCountInString(report.FinalReviewBody) || len(findings) > 50 || len(threads) > 50 || len(results) > 20}
		for _, finding := range detail.SourceFindings {
			detail.Truncated = detail.Truncated || finding.BodyRunes > utf8.RuneCountInString(finding.Body)
		}
		for _, result := range detail.SourceAgentResults {
			detail.Truncated = detail.Truncated || result.OutputUnavailable || result.OutputRunes > utf8.RuneCountInString(result.Output)
		}
		return models.SingleResponse[priorReviewRoundDetail]{Data: detail}, nil
	})
}

// Rechecks retain immutable source findings. Their selected assessment supplies
// the disposition; a missing or malformed disposition is not an active finding.
func priorReviewFindingReassessments(report db.CodeReviewHistoryReport, findings []db.CodeReviewHistoryFinding) ([]models.CodeReviewFindingReassessment, bool, error) {
	reassessments := make([]models.CodeReviewFindingReassessment, 0)
	if len(report.FindingReassessments) != 0 {
		if err := json.Unmarshal(report.FindingReassessments, &reassessments); err != nil {
			return nil, false, fmt.Errorf("decode prior finding reassessments: %w", err)
		}
	}
	isRecheck := report.ReviewScope != nil && *report.ReviewScope == models.CodeReviewScopeEvidenceOnly
	if isRecheck && (len(report.FindingReassessments) == 0 || reassessments == nil) {
		return nil, false, errors.New("prior evidence-only assessment is missing finding reassessments")
	}
	byFinding := make(map[uuid.UUID]models.CodeReviewFindingReassessment, len(reassessments))
	for _, reassessment := range reassessments {
		if err := reassessment.Validate(); err != nil {
			return nil, false, fmt.Errorf("invalid prior finding reassessment: %w", err)
		}
		if _, exists := byFinding[reassessment.FindingID]; exists {
			return nil, false, errors.New("prior assessment has duplicate finding reassessments")
		}
		byFinding[reassessment.FindingID] = reassessment
	}
	if isRecheck {
		for _, finding := range findings {
			if _, exists := byFinding[finding.ID]; !exists {
				return nil, false, errors.New("prior evidence-only assessment omitted a source finding disposition")
			}
		}
	}
	bounded := make([]models.CodeReviewFindingReassessment, 0, min(len(findings), 50))
	truncated := false
	// Match the displayed source findings, not the persisted array order. An
	// omitted finding must never displace a displayed finding's disposition.
	for _, finding := range findings[:min(len(findings), 50)] {
		reassessment, exists := byFinding[finding.ID]
		if !exists {
			continue
		}
		item := models.CodeReviewFindingReassessment{FindingID: reassessment.FindingID, Status: reassessment.Status}
		var omitted bool
		item.Reason, omitted = priorReviewBoundedText(reassessment.Reason, 2000)
		truncated = truncated || omitted || len(reassessment.EvidenceCitations) > 5
		item.EvidenceCitations = make([]models.CodeReviewEvidenceCitation, 0, min(len(reassessment.EvidenceCitations), 5))
		for _, citation := range reassessment.EvidenceCitations[:min(len(reassessment.EvidenceCitations), 5)] {
			id, idOmitted := priorReviewBoundedText(citation.EvidenceID, 200)
			quote, quoteOmitted := priorReviewBoundedText(citation.Quote, 500)
			item.EvidenceCitations = append(item.EvidenceCitations, models.CodeReviewEvidenceCitation{EvidenceID: id, Quote: quote})
			truncated = truncated || idOmitted || quoteOmitted
		}
		bounded = append(bounded, item)
	}
	return bounded, truncated || len(bounded) < len(reassessments), nil
}

func priorReviewBoundedText(value string, limit int) (string, bool) {
	runes := []rune(value)
	if len(runes) <= limit {
		return value, false
	}
	return string(runes[:limit]), true
}

type priorReviewMessageCursor struct {
	ID     int64 `json:"id"`
	Offset int   `json:"offset"`
}

// Only validated, effective retrieval parameters participate in the receipt
// identity. URL spelling, ignored fields and equivalent defaults must not
// create another copy of evidence that has already been shown.
type priorReviewReceiptParameters struct {
	ThreadID uuid.UUID                `json:"thread_id"`
	Limit    int                      `json:"limit"`
	Cursor   priorReviewMessageCursor `json:"cursor"`
}

type priorReviewMessageChunk struct {
	db.CodeReviewHistoryMessage
	Offset    int  `json:"offset"`
	Truncated bool `json:"truncated"`
}

func (h *InternalCodeReviewHistoryHandler) Messages(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authorize(w, r)
	if !ok {
		return
	}
	sourceID, ok := priorReviewID(w, r, "session_id")
	if !ok {
		return
	}
	threadID, ok := priorReviewID(w, r, "thread_id")
	if !ok {
		return
	}
	limit, ok := priorReviewLimit(w, r, 10, 20)
	if !ok {
		return
	}
	cursor := priorReviewMessageCursor{}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		encoded, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || len(raw) > 128 || json.Unmarshal(encoded, &cursor) != nil || cursor.ID <= 0 || cursor.Offset < 0 {
			writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "invalid prior message cursor")
			return
		}
	}
	parameters := priorReviewReceiptParameters{ThreadID: threadID, Limit: limit, Cursor: cursor}
	h.withReceipt(w, r, claims, sourceID, "prior_messages", parameters, func() (any, error) {
		report, err := h.history.GetPriorRound(r.Context(), claims.OrgID, claims.RepoID, *claims.SessionID, sourceID)
		if err != nil {
			return nil, err
		}
		found, err := h.history.HasPriorThread(r.Context(), claims.OrgID, sourceID, threadID, report.Cutoff)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, pgx.ErrNoRows
		}
		if cursor.ID != 0 {
			valid, err := h.history.ValidMessageCursor(r.Context(), claims.OrgID, sourceID, threadID, report.Cutoff, cursor.ID, cursor.Offset)
			if err != nil {
				return nil, err
			}
			if !valid {
				return nil, pgx.ErrNoRows
			}
		}
		messages, err := h.history.ListPriorMessages(r.Context(), claims.OrgID, sourceID, threadID, report.Cutoff, cursor.ID, cursor.Offset, limit+1)
		if err != nil {
			return nil, err
		}
		chunks := make([]priorReviewMessageChunk, 0, min(len(messages), limit))
		next := ""
		for i, message := range messages {
			if i == limit {
				break
			}
			offset := 0
			if message.ID == cursor.ID {
				offset = cursor.Offset
			}
			end := offset + utf8.RuneCountInString(message.Content)
			partial := end < message.ContentRunes
			chunks = append(chunks, priorReviewMessageChunk{CodeReviewHistoryMessage: message, Offset: offset, Truncated: partial})
			if partial || i == limit-1 && len(messages) > limit {
				nextCursor := priorReviewMessageCursor{ID: message.ID}
				if partial {
					nextCursor.Offset = end
				}
				encoded, err := json.Marshal(nextCursor)
				if err != nil {
					return nil, err
				}
				next = base64.RawURLEncoding.EncodeToString(encoded)
				break
			}
		}
		return models.ListResponse[priorReviewMessageChunk]{Data: chunks, Meta: models.PaginationMeta{NextCursor: next}}, nil
	})
}

// withReceipt records exactly what the orchestrator received. Repeated reads
// reuse the immutable response even if an older session is subsequently edited.
func (h *InternalCodeReviewHistoryHandler) withReceipt(w http.ResponseWriter, r *http.Request, claims *auth.InternalTokenClaims, sourceID uuid.UUID, kind string, parameters priorReviewReceiptParameters, load func() (any, error)) {
	identity, err := json.Marshal(struct {
		SourceSessionID uuid.UUID                    `json:"source_session_id"`
		Kind            string                       `json:"kind"`
		Parameters      priorReviewReceiptParameters `json:"parameters"`
	}{SourceSessionID: sourceID, Kind: kind, Parameters: parameters})
	if err != nil {
		h.readError(w, r, err)
		return
	}
	digest := sha256.Sum256(identity)
	key := fmt.Sprintf("%s/history/%s/%x", claims.SessionID, claims.ThreadID, digest)
	content, err := h.history.GetReceipt(r.Context(), claims.OrgID, *claims.SessionID, key)
	if errors.Is(err, pgx.ErrNoRows) {
		var value any
		value, err = load()
		if err == nil {
			content, err = json.Marshal(value)
		}
		if err == nil {
			metadata, marshalErr := json.Marshal(map[string]any{"review_history_thread_id": claims.ThreadID, "source_session_id": sourceID, "kind": kind})
			if marshalErr != nil {
				err = marshalErr
			} else {
				content, err = h.history.SaveReceipt(r.Context(), claims.OrgID, *claims.SessionID, key, content, metadata)
			}
		}
	}
	if err != nil {
		h.readError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, content)
}

func (h *InternalCodeReviewHistoryHandler) readError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "prior review evidence not found")
		return
	}
	writeError(w, r, http.StatusInternalServerError, "REVIEW_HISTORY_FAILED", "failed to retrieve prior review evidence", err)
}

func priorReviewID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil || id == uuid.Nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid "+name)
		return uuid.Nil, false
	}
	return id, true
}

func priorReviewLimit(w http.ResponseWriter, r *http.Request, fallback, maximum int) (int, bool) {
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 {
			writeError(w, r, http.StatusBadRequest, "INVALID_LIMIT", "limit must be a positive integer")
			return 0, false
		}
		return min(limit, maximum), true
	}
	return fallback, true
}
