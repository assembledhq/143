package handlers

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type codeReviewDisputeQueueCursor struct {
	SnapshotID uuid.UUID `json:"snapshot_id"`
	Position   int64     `json:"position"`
	Scope      [32]byte  `json:"scope"`
}

type codeReviewDisputeQueueCursorScope struct {
	OrgID              uuid.UUID                                   `json:"org_id"`
	AdjudicationStatus *models.CodeReviewDisputeAdjudicationStatus `json:"adjudication_status,omitempty"`
	RepositoryID       *uuid.UUID                                  `json:"repository_id,omitempty"`
	Direction          *models.CodeReviewDisputeDirection          `json:"direction,omitempty"`
}

func (h *CodeReviewHandler) CreateDispute(w http.ResponseWriter, r *http.Request) {
	if h.disputes == nil {
		writeError(w, r, http.StatusServiceUnavailable, "CODE_REVIEW_DISPUTES_UNAVAILABLE", "code review disputes are unavailable")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SESSION_ID", "invalid code review session ID")
		return
	}
	var req struct {
		Body                 string                            `json:"body"`
		ContestedReasonCodes []models.CodeReviewRiskReasonCode `json:"contested_reason_codes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	req.Body = strings.TrimSpace(req.Body)
	if req.Body == "" || !utf8.ValidString(req.Body) || utf8.RuneCountInString(req.Body) > models.CodeReviewDisputeBodyMaxRunes {
		writeError(w, r, http.StatusUnprocessableEntity, "INVALID_DISPUTE_BODY", "body must contain between 1 and 8000 valid UTF-8 characters")
		return
	}
	for _, code := range req.ContestedReasonCodes {
		if err := code.Validate(); err != nil {
			writeError(w, r, http.StatusUnprocessableEntity, "INVALID_REASON_CODE", "contested_reason_codes contains an invalid code", err)
			return
		}
	}
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user is required")
		return
	}
	login := user.Name
	if user.GitHubLogin != nil {
		login = *user.GitHubLogin
	}
	dispute, err := h.disputes.FileInApp(r.Context(), codereviewsvc.FileCodeReviewDisputeInput{
		OrgID: orgID, SessionID: sessionID, FiledByUserID: &user.ID, FiledByLogin: login,
		AuthorAssociation: "MEMBER", RepositoryVisibility: "unknown", Body: req.Body,
		ContestedReasonCodes: req.ContestedReasonCodes, Source: models.CodeReviewDisputeSourceAppUI,
	})
	if err != nil {
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeError(w, r, http.StatusNotFound, "CODE_REVIEW_NOT_FOUND", "code review not found")
		case errors.Is(err, codereviewsvc.ErrCodeReviewDisputeNotReady):
			writeError(w, r, http.StatusConflict, "CODE_REVIEW_NOT_DISPUTABLE", "the code review does not have a completed decision yet")
		case errors.Is(err, codereviewsvc.ErrCodeReviewDisputeInvalidBody):
			writeError(w, r, http.StatusUnprocessableEntity, "INVALID_DISPUTE_BODY", "body must contain between 1 and 8000 valid UTF-8 characters")
		default:
			writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_DISPUTE_CREATE_FAILED", "failed to file code review dispute", err)
		}
		return
	}
	writeJSON(w, http.StatusCreated, models.SingleResponse[models.CodeReviewDispute]{Data: dispute})
	resourceID := dispute.ID.String()
	emitUserAuditWithSession(h.audit, r, models.AuditActionCodeReviewDisputeFiled, models.AuditResourceCodeReviewDispute, &resourceID, &sessionID, nil, nil)
}

func (h *CodeReviewHandler) ListSessionDisputes(w http.ResponseWriter, r *http.Request) {
	if h.disputes == nil {
		writeError(w, r, http.StatusServiceUnavailable, "CODE_REVIEW_DISPUTES_UNAVAILABLE", "code review disputes are unavailable")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SESSION_ID", "invalid code review session ID")
		return
	}
	cursor, ok := parseOptionalDisputeCursor(w, r)
	if !ok {
		return
	}
	page, err := h.disputes.ListBySession(r.Context(), orgID, sessionID, cursor, 50)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "CODE_REVIEW_NOT_FOUND", "code review not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_DISPUTES_LIST_FAILED", "failed to list code review disputes", err)
		return
	}
	writeJSON(w, http.StatusOK, disputeListResponse(page))
}

func (h *CodeReviewHandler) EscalateDispute(w http.ResponseWriter, r *http.Request) {
	if h.disputes == nil {
		writeError(w, r, http.StatusServiceUnavailable, "CODE_REVIEW_DISPUTES_UNAVAILABLE", "code review disputes are unavailable")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	disputeID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_DISPUTE_ID", "invalid dispute ID")
		return
	}
	var req struct {
		Note string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user is required")
		return
	}
	dispute, err := h.disputes.Escalate(r.Context(), orgID, disputeID, user.ID, req.Note)
	if err != nil {
		switch {
		case errors.Is(err, codereviewsvc.ErrCodeReviewDisputeNotEscalatable), errors.Is(err, pgx.ErrNoRows):
			writeError(w, r, http.StatusConflict, "DISPUTE_NOT_ESCALATABLE", "this dispute cannot be sent to a policy owner")
		default:
			writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_DISPUTE_ESCALATE_FAILED", "failed to send dispute to a policy owner", err)
		}
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.CodeReviewDispute]{Data: dispute})
}

func (h *CodeReviewHandler) ListDisputeQueue(w http.ResponseWriter, r *http.Request) {
	if h.disputes == nil {
		writeError(w, r, http.StatusServiceUnavailable, "CODE_REVIEW_DISPUTES_UNAVAILABLE", "code review disputes are unavailable")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	filters := models.CodeReviewDisputeListFilters{Limit: 50}
	if raw := strings.TrimSpace(r.URL.Query().Get("adjudication_status")); raw != "" {
		value := models.CodeReviewDisputeAdjudicationStatus(raw)
		if err := value.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_ADJUDICATION_STATUS", "invalid adjudication_status", err)
			return
		}
		filters.AdjudicationStatus = &value
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("repository_id")); raw != "" {
		value, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY_ID", "invalid repository_id")
			return
		}
		filters.RepositoryID = &value
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("direction")); raw != "" {
		value := models.CodeReviewDisputeDirection(raw)
		if err := value.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_DIRECTION", "invalid direction", err)
			return
		}
		filters.Direction = &value
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		cursor, err := decodeCodeReviewDisputeQueueCursor(raw, orgID, filters)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "invalid cursor")
			return
		}
		filters.Cursor = &cursor
	}
	page, err := h.disputes.ListQueue(r.Context(), orgID, filters)
	if err != nil {
		if errors.Is(err, db.ErrCodeReviewDisputeQueueCursorExpired) {
			writeError(w, r, http.StatusGone, "CODE_REVIEW_DISPUTE_QUEUE_CURSOR_EXPIRED", "the queue changed; refresh to continue")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_DISPUTE_QUEUE_FAILED", "failed to list the code review dispute queue", err)
		return
	}
	response, err := disputeQueueListResponse(orgID, filters, page)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_DISPUTE_QUEUE_FAILED", "failed to encode the code review dispute queue cursor", err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *CodeReviewHandler) UpdateDispute(w http.ResponseWriter, r *http.Request) {
	if h.disputes == nil {
		writeError(w, r, http.StatusServiceUnavailable, "CODE_REVIEW_DISPUTES_UNAVAILABLE", "code review disputes are unavailable")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	disputeID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_DISPUTE_ID", "invalid dispute ID")
		return
	}
	var req struct {
		ExpectedVersion          int                                         `json:"expected_version"`
		AdjudicationStatus       *models.CodeReviewDisputeAdjudicationStatus `json:"adjudication_status"`
		AdjudicationNote         *string                                     `json:"adjudication_note"`
		PolicyOwnerActiveSeconds *int                                        `json:"policy_owner_active_seconds"`
		TrustOverride            json.RawMessage                             `json:"trust_override"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	if req.AdjudicationStatus != nil && !validCodeReviewDisputeAdjudicationUpdate(*req.AdjudicationStatus) {
		writeError(w, r, http.StatusUnprocessableEntity, "INVALID_ADJUDICATION_STATUS", "adjudication_status must be upheld, rejected, or needs_context")
		return
	}
	trustOverridePresent := req.TrustOverride != nil
	var trustOverride *bool
	if trustOverridePresent && string(req.TrustOverride) != "null" {
		var value bool
		if err := json.Unmarshal(req.TrustOverride, &value); err != nil {
			writeError(w, r, http.StatusUnprocessableEntity, "INVALID_TRUST_OVERRIDE", "trust_override must be true, false, or null")
			return
		}
		trustOverride = &value
	}
	if req.ExpectedVersion <= 0 || (req.AdjudicationStatus == nil && !trustOverridePresent) {
		writeError(w, r, http.StatusUnprocessableEntity, "INVALID_DISPUTE_UPDATE", "expected_version and at least one update are required")
		return
	}
	if req.PolicyOwnerActiveSeconds != nil && (*req.PolicyOwnerActiveSeconds < 0 || *req.PolicyOwnerActiveSeconds > 3600) {
		writeError(w, r, http.StatusUnprocessableEntity, "INVALID_POLICY_OWNER_ACTIVE_SECONDS", "policy_owner_active_seconds must be between 0 and 3600")
		return
	}
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user is required")
		return
	}
	dispute, err := h.disputes.Adjudicate(r.Context(), orgID, disputeID, user.ID, models.CodeReviewDisputeAdjudicationUpdate{
		ExpectedVersion: req.ExpectedVersion, AdjudicationStatus: req.AdjudicationStatus,
		AdjudicationNote: req.AdjudicationNote, TrustOverride: trustOverride,
		PolicyOwnerActiveSeconds: req.PolicyOwnerActiveSeconds,
		TrustOverridePresent:     trustOverridePresent,
	})
	if err != nil {
		switch {
		// A dispute that does not exist also matches no rows, so the store
		// reports it as a version conflict rather than a separate not-found.
		case errors.Is(err, db.ErrCodeReviewDisputeVersionConflict):
			writeError(w, r, http.StatusConflict, "CODE_REVIEW_DISPUTE_VERSION_CONFLICT", "the dispute changed; refresh and try again")
		case errors.Is(err, codereviewsvc.ErrCodeReviewDisputeInvalidUpdate):
			writeError(w, r, http.StatusUnprocessableEntity, "CODE_REVIEW_DISPUTE_UPDATE_FAILED", "failed to update code review dispute", err)
		default:
			// Anything else is ours, not the caller's: reporting a database or
			// enqueue failure as 4xx hides it from error budgets and alerting.
			writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_DISPUTE_UPDATE_FAILED", "failed to update code review dispute", err)
		}
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.CodeReviewDispute]{Data: dispute})
	resourceID := dispute.ID.String()
	emitUserAudit(h.audit, r, models.AuditActionCodeReviewDisputeAdjudicated, models.AuditResourceCodeReviewDispute, &resourceID, nil)
}

func validCodeReviewDisputeAdjudicationUpdate(status models.CodeReviewDisputeAdjudicationStatus) bool {
	switch status {
	case models.CodeReviewDisputeAdjudicationUpheld,
		models.CodeReviewDisputeAdjudicationRejected,
		models.CodeReviewDisputeAdjudicationNeedsContext:
		return true
	default:
		return false
	}
}

func parseOptionalDisputeCursor(w http.ResponseWriter, r *http.Request) (*uuid.UUID, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if raw == "" {
		return nil, true
	}
	value, err := uuid.Parse(raw)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "invalid cursor")
		return nil, false
	}
	return &value, true
}

func codeReviewDisputeQueueCursorScopeHash(orgID uuid.UUID, filters models.CodeReviewDisputeListFilters) ([32]byte, error) {
	encoded, err := json.Marshal(codeReviewDisputeQueueCursorScope{
		OrgID: orgID, AdjudicationStatus: filters.AdjudicationStatus,
		RepositoryID: filters.RepositoryID, Direction: filters.Direction,
	})
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

func encodeCodeReviewDisputeQueueCursor(orgID uuid.UUID, filters models.CodeReviewDisputeListFilters, cursor models.CodeReviewDisputeQueueCursor) (string, error) {
	scope, err := codeReviewDisputeQueueCursorScopeHash(orgID, filters)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(codeReviewDisputeQueueCursor{
		SnapshotID: cursor.SnapshotID,
		Position:   cursor.Position,
		Scope:      scope,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeCodeReviewDisputeQueueCursor(raw string, orgID uuid.UUID, filters models.CodeReviewDisputeListFilters) (models.CodeReviewDisputeQueueCursor, error) {
	encoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return models.CodeReviewDisputeQueueCursor{}, err
	}
	var cursor codeReviewDisputeQueueCursor
	if err := json.Unmarshal(encoded, &cursor); err != nil {
		return models.CodeReviewDisputeQueueCursor{}, err
	}
	if cursor.SnapshotID == uuid.Nil || cursor.Position <= 0 {
		return models.CodeReviewDisputeQueueCursor{}, errors.New("cursor anchor is incomplete")
	}
	scope, err := codeReviewDisputeQueueCursorScopeHash(orgID, filters)
	if err != nil {
		return models.CodeReviewDisputeQueueCursor{}, err
	}
	if cursor.Scope != scope {
		return models.CodeReviewDisputeQueueCursor{}, errors.New("cursor does not match the active filters")
	}
	return models.CodeReviewDisputeQueueCursor{
		SnapshotID: cursor.SnapshotID,
		Position:   cursor.Position,
	}, nil
}

func disputeListResponse(page models.CodeReviewDisputePage) models.ListResponse[models.CodeReviewDispute] {
	meta := models.PaginationMeta{}
	if page.NextCursor != nil {
		meta.NextCursor = page.NextCursor.String()
	}
	return models.ListResponse[models.CodeReviewDispute]{Data: page.Items, Meta: meta}
}

func disputeQueueListResponse(orgID uuid.UUID, filters models.CodeReviewDisputeListFilters, page models.CodeReviewDisputePage) (models.ListResponse[models.CodeReviewDispute], error) {
	response := models.ListResponse[models.CodeReviewDispute]{Data: page.Items, Meta: models.PaginationMeta{}}
	if page.NextQueueCursor == nil {
		return response, nil
	}
	nextCursor, err := encodeCodeReviewDisputeQueueCursor(orgID, filters, *page.NextQueueCursor)
	if err != nil {
		return models.ListResponse[models.CodeReviewDispute]{}, err
	}
	response.Meta.NextCursor = nextCursor
	return response, nil
}
