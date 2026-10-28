package handlers

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/thread"
	"github.com/google/uuid"
)

// threadMessageAcceptedResponse projects only authorized receipt data. It must
// run before postcommit adapter hooks: a malformed result is never acceptance,
// and an exact replay must not repeat those hooks.
func threadMessageAcceptedResponse(result *thread.SendMessageResult, orgID, sessionID, threadID uuid.UUID) (models.SendThreadMessageResponse, int, bool, error) {
	if result == nil || result.Message == nil {
		return models.SendThreadMessageResponse{}, 0, false, errors.New("thread message result has no message")
	}
	response := models.SendThreadMessageResponse{Message: *result.Message, InboxEntry: result.InboxEntry, ThreadStatus: result.ThreadStatus, DeliveryState: result.DeliveryState}
	if result.Admission == nil {
		return response, http.StatusCreated, false, nil
	}
	admission := *result.Admission
	if err := admission.Validate(); err != nil {
		return models.SendThreadMessageResponse{}, 0, false, err
	}
	laneID, threaded := admission.Fence().ThreadID()
	if admission.OrgID() != orgID || admission.Fence().SessionID() != sessionID || !threaded || laneID != threadID {
		return models.SendThreadMessageResponse{}, 0, false, errors.New("thread message receipt has a different scope")
	}
	receipt, status, err := threadMessageReceipt(admission.Disposition(), admission.OriginalDisposition(), admission.DispatchID(), admission.Ingress())
	if err != nil {
		return models.SendThreadMessageResponse{}, 0, false, err
	}
	// Canonical acceptance returns the retained receipt envelope, not mutable
	// policy/status observations or historical audit work on the service result.
	message, inbox := admission.Message(), admission.InboxEntry()
	response.Message, response.InboxEntry, response.ThreadStatus = message, &inbox, admission.ThreadStatus()
	response.DeliveryState = inbox.DeliveryState
	response.Admission = &receipt
	return response, status, receipt.Replayed, nil
}

func threadMessageReceipt(disposition, original db.MessageAdmissionDisposition, dispatchID uuid.UUID, ingress db.LifecycleIngress) (models.SessionMessageAdmissionReceipt, int, error) {
	receipt := models.SessionMessageAdmissionReceipt{DispatchID: dispatchID, IngressKind: models.SessionMessageIngressKind(ingress.Kind()), IngressKey: ingress.Key(), Replayed: disposition == db.MessageAdmissionReplayed}
	if err := disposition.Validate(); err != nil {
		return models.SessionMessageAdmissionReceipt{}, 0, err
	}
	if disposition != db.MessageAdmissionReplayed && disposition != original {
		return models.SessionMessageAdmissionReceipt{}, 0, errors.New("thread message receipt has inconsistent acceptance")
	}
	status := http.StatusAccepted
	switch original {
	case db.MessageAdmissionNewJob:
		receipt.State, status = models.LifecycleAdmissionBound, http.StatusCreated
	case db.MessageAdmissionQueued, db.MessageAdmissionWaiting:
		receipt.State = models.LifecycleAdmissionWaiting
	default:
		return models.SessionMessageAdmissionReceipt{}, 0, errors.New("thread message receipt has no original accepted disposition")
	}
	if err := ingress.Validate(); err != nil {
		return models.SessionMessageAdmissionReceipt{}, 0, err
	}
	if err := receipt.Validate(); err != nil {
		return models.SessionMessageAdmissionReceipt{}, 0, err
	}
	return receipt, status, nil
}

// writeThreadMessageAdmissionError precedes every legacy/domain renderer. In
// particular, joined/wrapped uncertainty must never turn into an unaccepted
// comment, ownership or stale-admission rejection through its inner cause.
func writeThreadMessageAdmissionError(w http.ResponseWriter, r *http.Request, err error, orgID, sessionID, threadID uuid.UUID, clientKey string) bool {
	switch {
	case errors.Is(err, thread.ErrCommandOutcomeUnknown):
		writeErrorWithDetails(w, r, http.StatusServiceUnavailable, "COMMAND_OUTCOME_UNKNOWN", "message acceptance is pending confirmation; retain the same command key", threadMessageUnknownDetails(err, orgID, sessionID, threadID, clientKey))
	case errors.Is(err, thread.ErrSendNormalizing):
		w.Header().Set("Retry-After", "1")
		writeError(w, r, http.StatusConflict, "NORMALIZATION_PENDING", "this session is preparing its message protocol; retry with the same command key")
	case errors.Is(err, db.ErrLifecycleContention), errors.Is(err, db.ErrThreadSendModeChanged), errors.Is(err, db.ErrMessageAdmissionReplayObservationNeeded):
		w.Header().Set("Retry-After", "1")
		writeError(w, r, http.StatusConflict, "LIFECYCLE_CONTENDED", "message admission is busy; retry with the same command key")
	case errors.Is(err, db.ErrMessageAdmissionIdempotencyConflict):
		writeError(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "this command key was already used for different message intent")
	case errors.Is(err, db.ErrMessageAdmissionConflict):
		writeError(w, r, http.StatusConflict, "STALE_ADMISSION", "the message admission scope has changed")
	case errors.Is(err, db.ErrMessageAdmissionSpecialistControlRequired):
		writeError(w, r, http.StatusConflict, "SPECIALIST_CONTROL_REQUIRED", "this session requires its specialist control action")
	case errors.Is(err, db.ErrMessageAdmissionNotResumable):
		writeError(w, r, http.StatusConflict, "NOT_RESUMABLE", "session must be idle, running, awaiting input, need guidance, or otherwise resumable to send a message")
	case errors.Is(err, db.ErrMessageAdmissionNotIdle):
		writeError(w, r, http.StatusConflict, "NOT_IDLE", "thread must be idle to send a message")
	case errors.Is(err, db.ErrMessageAdmissionChangesetNotFound):
		writeError(w, r, http.StatusNotFound, "CHANGESET_NOT_FOUND", "pull request target not found")
	case errors.Is(err, db.ErrMessageAdmissionNotAllowed), errors.Is(err, db.ErrMessageAdmissionActorForbidden):
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "message admission is not permitted")
	case errors.Is(err, db.ErrMessageAdmissionAwaitingInput):
		writeError(w, r, http.StatusConflict, "ANSWER_ADMISSION_REQUIRED", "use the input request answer action")
	case errors.Is(err, db.ErrLifecycleGuardProvenanceRequired), errors.Is(err, db.ErrLifecycleConsumerReconciliation), errors.Is(err, db.ErrLifecycleConsumerDiscoveryIncomplete):
		writeError(w, r, http.StatusServiceUnavailable, "LIFECYCLE_UNAVAILABLE", "message admission cannot be safely verified")
	default:
		return false
	}
	return true
}

type sessionMessageUnknownDetails struct {
	IngressKind     models.SessionMessageIngressKind `json:"ingress_kind,omitempty"`
	IngressKey      string                           `json:"ingress_key,omitempty"`
	DiagnosticID    *uuid.UUID                       `json:"diagnostic_id,omitempty"`
	ClientMessageID string                           `json:"client_message_id,omitempty"`
}

func threadMessageUnknownDetails(err error, orgID, sessionID, threadID uuid.UUID, clientKey string) any {
	var unknown *thread.CommandOutcomeUnknownError
	if !errors.As(err, &unknown) || unknown == nil {
		if validThreadMessageReceiptText(clientKey) {
			return sessionMessageUnknownDetails{ClientMessageID: clientKey}
		}
		return nil
	}
	kind, key := models.SessionMessageIngressKind(unknown.Ingress.Kind()), unknown.Ingress.Key()
	prefix := orgID.String() + ":" + sessionID.String() + ":" + threadID.String() + ":"
	if unknown.Ingress.Validate() == nil && kind.Validate() == nil && strings.HasPrefix(key, prefix) && validThreadMessageReceiptText(key) {
		suffix := strings.TrimPrefix(key, prefix)
		validKey := kind == models.SessionMessageIngressClient && validThreadMessageReceiptText(clientKey) && suffix == clientKey
		if kind == models.SessionMessageIngressServer && clientKey == "" {
			id, parseErr := uuid.Parse(suffix)
			validKey = parseErr == nil && id != uuid.Nil && id.String() == suffix
		}
		if validKey {
			return sessionMessageUnknownDetails{IngressKind: kind, IngressKey: key}
		}
	}
	details := sessionMessageUnknownDetails{}
	if unknown.DiagnosticID != uuid.Nil {
		id := unknown.DiagnosticID
		details.DiagnosticID = &id
	}
	if validThreadMessageReceiptText(clientKey) {
		details.ClientMessageID = clientKey
	}
	if details.DiagnosticID == nil && details.ClientMessageID == "" {
		return nil
	}
	return details
}

func validThreadMessageReceiptText(value string) bool {
	return strings.TrimSpace(value) != "" && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}
