package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/thread"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type admissionOutcomeThreadService struct {
	ThreadService
	send func(context.Context, thread.SendMessageInput) (*thread.SendMessageResult, error)
}

func (s admissionOutcomeThreadService) SendMessage(ctx context.Context, input thread.SendMessageInput) (*thread.SendMessageResult, error) {
	return s.send(ctx, input)
}

func admissionOutcomeRequest(orgID, sessionID, threadID uuid.UUID, body string) *http.Request {
	return threadRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/threads/"+threadID.String()+"/messages", body, orgID, map[string]string{"id": sessionID.String(), "tid": threadID.String()})
}

func TestThreadMessageReceiptPreservesOriginalAcceptance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		disposition db.MessageAdmissionDisposition
		original    db.MessageAdmissionDisposition
		state       models.LifecycleAdmissionState
		status      int
		replayed    bool
	}{
		{name: "new bound job", disposition: db.MessageAdmissionNewJob, original: db.MessageAdmissionNewJob, state: models.LifecycleAdmissionBound, status: http.StatusCreated},
		{name: "queued input is not delivered", disposition: db.MessageAdmissionQueued, original: db.MessageAdmissionQueued, state: models.LifecycleAdmissionWaiting, status: http.StatusAccepted},
		{name: "waiting input is not launched", disposition: db.MessageAdmissionWaiting, original: db.MessageAdmissionWaiting, state: models.LifecycleAdmissionWaiting, status: http.StatusAccepted},
		{name: "bound replay retains created", disposition: db.MessageAdmissionReplayed, original: db.MessageAdmissionNewJob, state: models.LifecycleAdmissionBound, status: http.StatusCreated, replayed: true},
		{name: "queued replay retains accepted", disposition: db.MessageAdmissionReplayed, original: db.MessageAdmissionQueued, state: models.LifecycleAdmissionWaiting, status: http.StatusAccepted, replayed: true},
		{name: "waiting replay retains accepted", disposition: db.MessageAdmissionReplayed, original: db.MessageAdmissionWaiting, state: models.LifecycleAdmissionWaiting, status: http.StatusAccepted, replayed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dispatch := uuid.New()
			ingress, err := db.NewLifecycleIngress("thread_client_message", "synthetic-opaque-key", db.LifecycleDigest{1})
			require.NoError(t, err, "prepare exact synthetic ingress")
			receipt, status, err := threadMessageReceipt(tt.disposition, tt.original, dispatch, ingress)
			require.NoError(t, err, "accepted disposition should project a safe receipt")
			require.Equal(t, tt.status, status, "HTTP status should describe original input admission rather than current eligibility")
			require.Equal(t, models.SessionMessageAdmissionReceipt{DispatchID: dispatch, State: tt.state, IngressKind: models.SessionMessageIngressClient, IngressKey: "synthetic-opaque-key", Replayed: tt.replayed}, receipt, "transport receipt should contain only exact input identity and original acceptance")
		})
	}
}

func TestThreadMessageReceiptRejectsMalformedAcceptance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		disposition db.MessageAdmissionDisposition
		original    db.MessageAdmissionDisposition
	}{
		{name: "zero receipt"},
		{name: "replay cannot be original", disposition: db.MessageAdmissionReplayed, original: db.MessageAdmissionReplayed},
		{name: "nonreplay cannot change outcome", disposition: db.MessageAdmissionWaiting, original: db.MessageAdmissionNewJob},
		{name: "unknown disposition", disposition: "delivered", original: db.MessageAdmissionNewJob},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ingress, err := db.NewLifecycleIngress("thread_client_message", "synthetic", db.LifecycleDigest{1})
			require.NoError(t, err, "prepare exact synthetic ingress")
			receipt, status, err := threadMessageReceipt(tt.disposition, tt.original, uuid.New(), ingress)
			require.Error(t, err, "malformed disposition must not become an accepted response")
			require.Equal(t, models.SessionMessageAdmissionReceipt{}, receipt, "failure should discard public receipt data")
			require.Zero(t, status, "failure should not manufacture a success status")
		})
	}
}

func TestThreadMessageReceiptRejectsInvalidPublicIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, kind, key           string
		zeroDispatch, zeroIngress bool
	}{
		{name: "zero dispatch", kind: "thread_client_message", key: "synthetic-key", zeroDispatch: true},
		{name: "zero ingress", zeroIngress: true},
		{name: "unknown ingress kind", kind: "provider_launch", key: "synthetic-key"},
		{name: "invalid UTF8 key", kind: "thread_client_message", key: string([]byte{0xff})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dispatchID := uuid.New()
			if tt.zeroDispatch {
				dispatchID = uuid.Nil
			}
			var ingress db.LifecycleIngress
			if !tt.zeroIngress {
				var err error
				ingress, err = db.NewLifecycleIngress(tt.kind, tt.key, db.LifecycleDigest{1})
				require.NoError(t, err, "prepare shape-only internal identity for public projection validation")
			}
			receipt, status, err := threadMessageReceipt(db.MessageAdmissionNewJob, db.MessageAdmissionNewJob, dispatchID, ingress)
			require.Error(t, err, "invalid public identity must not be projected as an accepted receipt")
			require.Equal(t, models.SessionMessageAdmissionReceipt{}, receipt, "invalid identity should discard the complete projected receipt")
			require.Zero(t, status, "invalid identity must not manufacture a success status")
		})
	}
}

func TestSendThreadMessageAdmissionErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                      string
		cause                     error
		status                    int
		code, message, retryAfter string
	}{
		{name: "normalizing", cause: thread.ErrSendNormalizing, status: http.StatusConflict, code: "NORMALIZATION_PENDING", message: "this session is preparing its message protocol; retry with the same command key", retryAfter: "1"},
		{name: "contention", cause: db.ErrLifecycleContention, status: http.StatusConflict, code: "LIFECYCLE_CONTENDED", message: "message admission is busy; retry with the same command key", retryAfter: "1"},
		{name: "mode changed", cause: db.ErrThreadSendModeChanged, status: http.StatusConflict, code: "LIFECYCLE_CONTENDED", message: "message admission is busy; retry with the same command key", retryAfter: "1"},
		{name: "concurrent exact receipt requires observation", cause: db.ErrMessageAdmissionReplayObservationNeeded, status: http.StatusConflict, code: "LIFECYCLE_CONTENDED", message: "message admission is busy; retry with the same command key", retryAfter: "1"},
		{name: "semantic key conflict precedes generic stale", cause: errors.Join(db.ErrMessageAdmissionConflict, db.ErrMessageAdmissionIdempotencyConflict), status: http.StatusConflict, code: "IDEMPOTENCY_CONFLICT", message: "this command key was already used for different message intent"},
		{name: "epoch advanced", cause: db.ErrMessageAdmissionConflict, status: http.StatusConflict, code: "STALE_ADMISSION", message: "the message admission scope has changed"},
		{name: "retained specialist owner precedes generic authorization", cause: errors.Join(db.ErrMessageAdmissionNotAllowed, db.ErrMessageAdmissionSpecialistControlRequired), status: http.StatusConflict, code: "SPECIALIST_CONTROL_REQUIRED", message: "this session requires its specialist control action"},
		{name: "current actor forbidden", cause: errors.Join(db.ErrMessageAdmissionNotAllowed, db.ErrMessageAdmissionActorForbidden), status: http.StatusForbidden, code: "FORBIDDEN", message: "message admission is not permitted"},
		{name: "standalone actor forbidden", cause: db.ErrMessageAdmissionActorForbidden, status: http.StatusForbidden, code: "FORBIDDEN", message: "message admission is not permitted"},
		{name: "unsupported ordinary source", cause: db.ErrMessageAdmissionNotAllowed, status: http.StatusForbidden, code: "FORBIDDEN", message: "message admission is not permitted"},
		{name: "awaiting input refuses without answering", cause: db.ErrMessageAdmissionAwaitingInput, status: http.StatusConflict, code: "ANSWER_ADMISSION_REQUIRED", message: "use the input request answer action"},
		{name: "absent hidden or foreign scope", cause: pgx.ErrNoRows, status: http.StatusNotFound, code: "NOT_FOUND", message: "thread not found"},
		{name: "legacy resume refusal retains domain outcome over claim miss", cause: errors.Join(thread.ErrSessionNotResumable, pgx.ErrNoRows), status: http.StatusConflict, code: "NOT_RESUMABLE", message: "session must be idle, running, awaiting input, need guidance, or otherwise resumable to send a message"},
		{name: "legacy enqueue failure retains domain outcome over no rows", cause: errors.Join(thread.ErrEnqueueFailed, pgx.ErrNoRows), status: http.StatusInternalServerError, code: "ENQUEUE_FAILED", message: "failed to enqueue continue_session job"},
		{name: "invalid retained provenance", cause: db.ErrLifecycleGuardProvenanceRequired, status: http.StatusServiceUnavailable, code: "LIFECYCLE_UNAVAILABLE", message: "message admission cannot be safely verified"},
		{name: "consumer reconciliation required", cause: db.ErrLifecycleConsumerReconciliation, status: http.StatusServiceUnavailable, code: "LIFECYCLE_UNAVAILABLE", message: "message admission cannot be safely verified"},
		{name: "consumer discovery incomplete", cause: db.ErrLifecycleConsumerDiscoveryIncomplete, status: http.StatusServiceUnavailable, code: "LIFECYCLE_UNAVAILABLE", message: "message admission cannot be safely verified"},
		{name: "legacy target configuration unavailable", cause: thread.ErrSendChangesetsNotConfigured, status: http.StatusNotImplemented, code: "CHANGESETS_NOT_CONFIGURED", message: "pull request targeting is not configured"},
		{name: "legacy target missing or read failed", cause: thread.ErrSendChangesetNotFound, status: http.StatusNotFound, code: "CHANGESET_NOT_FOUND", message: "pull request target not found"},
		{name: "legacy target not materialized", cause: thread.ErrSendChangesetNotMaterialized, status: http.StatusConflict, code: "CHANGESET_NOT_MATERIALIZED", message: "this pull request must be materialized before the agent can edit it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handler := NewSessionThreadHandler(admissionOutcomeThreadService{send: func(context.Context, thread.SendMessageInput) (*thread.SendMessageResult, error) {
				return nil, fmt.Errorf("synthetic private diagnostic: %w", tt.cause)
			}})
			orgID, sessionID, laneID := uuid.New(), uuid.New(), uuid.New()
			recorder := httptest.NewRecorder()
			handler.SendThreadMessage(recorder, admissionOutcomeRequest(orgID, sessionID, laneID, `{"message":"continue","client_message_id":"synthetic-key"}`))
			require.Equal(t, tt.status, recorder.Code, "admission error should use the specified HTTP outcome")
			require.Equal(t, "application/json", recorder.Header().Get("Content-Type"), "admission failures should retain the standard JSON response content type")
			require.Equal(t, tt.retryAfter, recorder.Header().Get("Retry-After"), "only retryable protocol contention should carry the explicit retry delay")
			var actual models.ErrorResponse
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &actual), "error response should be valid JSON")
			require.Equal(t, models.ErrorResponse{Error: models.ErrorDetail{Code: tt.code, Message: tt.message}}, actual, "domain failure should expose only a stable safe code and message")
			require.NotContains(t, recorder.Body.String(), "synthetic private", "transport should never expose wrapped dependency diagnostics")
		})
	}
}

func TestSendThreadMessageUnknownPrecedesEveryDomainError(t *testing.T) {
	t.Parallel()
	causes := []struct {
		name string
		err  error
	}{
		{name: "comment resolver not configured", err: thread.ErrReviewCommentsNotConfigured},
		{name: "foreign review comment IDs", err: &db.ErrReviewCommentsNotInSession{Missing: []uuid.UUID{uuid.New()}}},
		{name: "automation ownership", err: &models.SessionAutomationOwnedError{Owner: models.SessionAutomationOwner{AutomationID: uuid.New(), TargetID: uuid.New()}}},
		{name: "review ownership", err: &models.SessionCodeReviewOwnedError{PullRequestID: uuid.New()}},
		{name: "not found", err: thread.ErrThreadNotFound},
		{name: "pgx no rows", err: pgx.ErrNoRows},
		{name: "backpressure", err: thread.ErrThreadInboxBackpressure},
		{name: "busy", err: thread.ErrThreadNotIdle},
		{name: "running limit", err: thread.ErrRunningLimitReached},
		{name: "active sibling", err: thread.ErrActiveThreadExists},
		{name: "snapshot expired", err: thread.ErrSessionSnapshotExpired},
		{name: "not resumable", err: thread.ErrSessionNotResumable},
		{name: "enqueue failure", err: thread.ErrEnqueueFailed},
		{name: "normalization pending", err: thread.ErrSendNormalizing},
		{name: "contention", err: db.ErrLifecycleContention},
		{name: "mode changed", err: db.ErrThreadSendModeChanged},
		{name: "idempotency conflict", err: db.ErrMessageAdmissionIdempotencyConflict},
		{name: "epoch changed", err: db.ErrMessageAdmissionConflict},
		{name: "actor forbidden", err: db.ErrMessageAdmissionNotAllowed},
		{name: "specialist control", err: db.ErrMessageAdmissionSpecialistControlRequired},
		{name: "answer required", err: db.ErrMessageAdmissionAwaitingInput},
		{name: "positive receipt observation missing", err: db.ErrMessageAdmissionReplayObservationNeeded},
		{name: "provenance missing", err: db.ErrLifecycleGuardProvenanceRequired},
		{name: "reconciliation required", err: db.ErrLifecycleConsumerReconciliation},
		{name: "generic dependency text", err: errors.New("synthetic-secret-do-not-publish")},
		{name: "legacy target unconfigured", err: thread.ErrSendChangesetsNotConfigured},
		{name: "legacy target missing", err: thread.ErrSendChangesetNotFound},
		{name: "legacy target materialization missing", err: thread.ErrSendChangesetNotMaterialized},
	}
	for _, cause := range causes {
		t.Run(cause.name, func(t *testing.T) {
			t.Parallel()
			wrappers := []struct {
				name string
				wrap func(error) error
			}{
				{name: "wrapped unknown retains cause", wrap: func(err error) error { return fmt.Errorf("outer synthetic diagnostic: %w", err) }},
				{name: "joined domain error before unknown", wrap: func(err error) error { return errors.Join(cause.err, err) }},
			}
			for _, wrapper := range wrappers {
				t.Run(wrapper.name, func(t *testing.T) {
					t.Parallel()
					orgID, sessionID, laneID := uuid.New(), uuid.New(), uuid.New()
					key := orgID.String() + ":" + sessionID.String() + ":" + laneID.String() + ":synthetic-key"
					ingress, err := db.NewLifecycleIngress("thread_client_message", key, db.LifecycleDigest{1})
					require.NoError(t, err, "prepare original exact command identity")
					unknown := &thread.CommandOutcomeUnknownError{Ingress: ingress, DiagnosticID: uuid.New(), ClientMessageID: "synthetic-key", Cause: cause.err}
					handler := NewSessionThreadHandler(admissionOutcomeThreadService{send: func(context.Context, thread.SendMessageInput) (*thread.SendMessageResult, error) {
						return nil, wrapper.wrap(unknown)
					}})
					recorder := httptest.NewRecorder()
					handler.SendThreadMessage(recorder, admissionOutcomeRequest(orgID, sessionID, laneID, `{"message":"continue","client_message_id":"synthetic-key"}`))
					require.Equal(t, http.StatusServiceUnavailable, recorder.Code, "unknown commit must precede every domain and legacy renderer")
					require.Equal(t, "application/json", recorder.Header().Get("Content-Type"), "uncertain admission should retain the standard JSON content type")
					require.Empty(t, recorder.Header().Get("Retry-After"), "uncertain acceptance cannot announce an automatic blind retry")
					expected := models.ErrorResponse{Error: models.ErrorDetail{Code: "COMMAND_OUTCOME_UNKNOWN", Message: "message acceptance is pending confirmation; retain the same command key", Details: sessionMessageUnknownDetails{IngressKind: models.SessionMessageIngressClient, IngressKey: key}}}
					encoded, err := json.Marshal(expected)
					require.NoError(t, err, "encode exact safe unknown envelope")
					require.JSONEq(t, string(encoded), recorder.Body.String(), "unknown response should return the same command key and no inner cause or owner identifiers")
				})
			}
		})
	}
}

func TestSendThreadMessageLegacyResponseUnchanged(t *testing.T) {
	t.Parallel()
	orgID, sessionID, laneID := uuid.New(), uuid.New(), uuid.New()
	message := models.SessionMessage{ID: 42, OrgID: orgID, SessionID: sessionID, ThreadID: &laneID, Role: models.MessageRoleUser, Content: "continue", TurnNumber: 3, CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	handler := NewSessionThreadHandler(admissionOutcomeThreadService{send: func(context.Context, thread.SendMessageInput) (*thread.SendMessageResult, error) {
		return &thread.SendMessageResult{Message: &message, ThreadStatus: models.ThreadStatusRunning}, nil
	}})
	recorder := httptest.NewRecorder()
	handler.SendThreadMessage(recorder, admissionOutcomeRequest(orgID, sessionID, laneID, `{"message":"continue"}`))
	expected := models.SingleResponse[models.SendThreadMessageResponse]{Data: models.SendThreadMessageResponse{Message: message, ThreadStatus: models.ThreadStatusRunning}}
	encoded, err := json.Marshal(expected)
	require.NoError(t, err, "encode unchanged legacy message response")
	require.Equal(t, http.StatusCreated, recorder.Code, "legacy queue or new message acceptance should remain 201")
	require.Equal(t, "application/json", recorder.Header().Get("Content-Type"), "legacy message acceptance should retain its standard content type")
	require.Equal(t, string(encoded)+"\n", recorder.Body.String(), "legacy envelope should remain byte-for-byte unchanged without an admission field")
	require.NotContains(t, recorder.Body.String(), "admission", "nil canonical receipt should not create a legacy response field")
}

func TestSendThreadMessageForwardsTargetWithoutPreflight(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                            string
		configured, noTarget, malformed bool
		policy                          thread.LegacyChangesetPolicy
	}{
		{name: "configured HTTP adapter supplies compatibility policy only", configured: true, policy: thread.LegacyChangesetRequireMaterialized},
		{name: "unconfigured HTTP adapter still reaches router", policy: thread.LegacyChangesetUnconfigured},
		{name: "untargeted request retains default", configured: true, noTarget: true, policy: thread.LegacyChangesetUnchanged},
		{name: "malformed target refuses before router", malformed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, sessionID, laneID, targetID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			calls := 0
			handler := NewSessionThreadHandler(admissionOutcomeThreadService{send: func(_ context.Context, input thread.SendMessageInput) (*thread.SendMessageResult, error) {
				calls++
				var expectedTarget *uuid.UUID
				if !tt.noTarget {
					expectedTarget = &targetID
				}
				require.Equal(t, thread.SendMessageInput{OrgID: orgID, SessionID: sessionID, ThreadID: laneID, ChangesetID: expectedTarget, LegacyChangesetPolicy: tt.policy, Message: "continue"}, input, "HTTP adapter should forward original target identity and only its legacy policy without materialization reads")
				message := &models.SessionMessage{ID: 42, OrgID: orgID, SessionID: sessionID, ThreadID: &laneID, Role: models.MessageRoleUser, Content: input.Message}
				return &thread.SendMessageResult{Message: message}, nil
			}})
			if tt.configured {
				// A read through this inert handle would panic. The adapter must
				// retain configuration only and leave routing to the service.
				handler.SetChangesetStore(db.NewSessionChangesetStore(nil))
			}
			body := map[string]string{"message": "continue"}
			if !tt.noTarget {
				body["changeset_id"] = targetID.String()
			}
			if tt.malformed {
				body["changeset_id"] = "not-a-uuid"
			}
			encoded, err := json.Marshal(body)
			require.NoError(t, err, "encode target request fixture")
			recorder := httptest.NewRecorder()
			handler.SendThreadMessage(recorder, admissionOutcomeRequest(orgID, sessionID, laneID, string(encoded)))
			if tt.malformed {
				require.Zero(t, calls, "malformed target identity should not reach the service router")
				require.Equal(t, http.StatusBadRequest, recorder.Code, "malformed target should retain its old HTTP status")
				require.JSONEq(t, `{"error":{"code":"INVALID_CHANGESET_ID","message":"invalid pull request target"}}`, recorder.Body.String(), "malformed identity should expose only the stable safe error")
			} else {
				require.Equal(t, 1, calls, "target configuration/materialization must not veto the request before service routing")
				require.Equal(t, http.StatusCreated, recorder.Code, "adapter should render the service's existing accepted envelope")
			}
		})
	}
}

func TestThreadMessageUnknownDetailsRejectsUntrustedIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                    string
		kind, suffix, callerKey string
		foreign, invalidDigest  bool
	}{
		{name: "different tenant", kind: "thread_client_message", suffix: "synthetic-key", callerKey: "synthetic-key", foreign: true},
		{name: "changed client key", kind: "thread_client_message", suffix: "other-synthetic-key", callerKey: "synthetic-key"},
		{name: "invalid server key", kind: "thread_server_command", suffix: "not-a-uuid"},
		{name: "unrecognized kind", kind: "provider_launch", suffix: "synthetic-key", callerKey: "synthetic-key"},
		{name: "zero ingress", invalidDigest: true},
		{name: "malformed caller key", kind: "thread_client_message", suffix: "synthetic\x00key", callerKey: "synthetic\x00key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, sessionID, laneID, diagnosticID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			keyOrg := orgID
			if tt.foreign {
				keyOrg = uuid.New()
			}
			key := keyOrg.String() + ":" + sessionID.String() + ":" + laneID.String() + ":" + tt.suffix
			var ingress db.LifecycleIngress
			if !tt.invalidDigest {
				var err error
				ingress, err = db.NewLifecycleIngress(tt.kind, key, db.LifecycleDigest{1})
				require.NoError(t, err, "prepare deliberately untrusted shape-only ingress for disclosure validation")
			}
			unknown := &thread.CommandOutcomeUnknownError{Ingress: ingress, DiagnosticID: diagnosticID, ClientMessageID: "not-the-caller-key", Cause: errors.New("synthetic private failure")}
			actual := threadMessageUnknownDetails(unknown, orgID, sessionID, laneID, tt.callerKey)
			expected := sessionMessageUnknownDetails{DiagnosticID: &diagnosticID}
			if validThreadMessageReceiptText(tt.callerKey) {
				expected.ClientMessageID = tt.callerKey
			}
			require.Equal(t, expected, actual, "unknown fallback must retain only a diagnostic identity and independently known caller key, never a foreign or malformed ingress")
		})
	}
}

func TestThreadMessageUnknownDetailsRetainsIndependentIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		server, noKey bool
	}{
		{name: "valid keyless server command", server: true},
		{name: "sentinel without diagnostic retains caller key"},
		{name: "sentinel without identity invents nothing", noKey: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, sessionID, laneID := uuid.New(), uuid.New(), uuid.New()
			var err error = thread.ErrCommandOutcomeUnknown
			callerKey := "synthetic-key"
			var expected any = sessionMessageUnknownDetails{ClientMessageID: callerKey}
			if tt.noKey {
				callerKey, expected = "", nil
			}
			if tt.server {
				callerKey = ""
				key := orgID.String() + ":" + sessionID.String() + ":" + laneID.String() + ":" + uuid.NewString()
				ingress, createErr := db.NewLifecycleIngress("thread_server_command", key, db.LifecycleDigest{1})
				require.NoError(t, createErr, "prepare the same original keyless server identity")
				err = &thread.CommandOutcomeUnknownError{Ingress: ingress}
				expected = sessionMessageUnknownDetails{IngressKind: models.SessionMessageIngressServer, IngressKey: key}
			}
			require.Equal(t, expected, threadMessageUnknownDetails(err, orgID, sessionID, laneID, callerKey), "unknown must retain only an independently known original key and never mint a replacement identity")
		})
	}
}

func TestSendThreadMessageUnverifiedSuccessIsUnknown(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		key    string
		result func(models.SessionMessage) *thread.SendMessageResult
	}{
		{name: "nil result retains caller key", key: "synthetic-key", result: func(models.SessionMessage) *thread.SendMessageResult { return nil }},
		{name: "nil message has no invented key", result: func(models.SessionMessage) *thread.SendMessageResult { return &thread.SendMessageResult{} }},
		{name: "uninitialized canonical receipt", key: "synthetic-key", result: func(message models.SessionMessage) *thread.SendMessageResult {
			return &thread.SendMessageResult{Message: &message, Admission: &db.Admission{}, AnsweredQuestion: &models.SessionQuestion{ID: uuid.New()}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, sessionID, laneID, actorID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			result := tt.result(models.SessionMessage{ID: 42, OrgID: orgID, SessionID: sessionID, ThreadID: &laneID, Role: models.MessageRoleUser, Content: "continue"})
			handler := NewSessionThreadHandler(admissionOutcomeThreadService{send: func(context.Context, thread.SendMessageInput) (*thread.SendMessageResult, error) { return result, nil }})
			auditDB, err := pgxmock.NewPool()
			require.NoError(t, err, "construct isolated unexpected-effect detector")
			defer auditDB.Close()
			var auditLog bytes.Buffer
			logger := zerolog.New(&auditLog)
			handler.SetAuditEmitter(db.NewAuditEmitter(db.NewAuditLogStore(auditDB), logger))
			handler.SetLogger(logger)
			body, err := json.Marshal(map[string]string{"message": "continue", "client_message_id": tt.key})
			require.NoError(t, err, "encode exact caller key")
			req := admissionOutcomeRequest(orgID, sessionID, laneID, string(body))
			req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: actorID}))
			recorder := httptest.NewRecorder()
			handler.SendThreadMessage(recorder, req)
			var details any
			if tt.key != "" {
				details = sessionMessageUnknownDetails{ClientMessageID: tt.key}
			}
			expected := models.ErrorResponse{Error: models.ErrorDetail{Code: "COMMAND_OUTCOME_UNKNOWN", Message: "message acceptance is pending confirmation; retain the same command key", Details: details}}
			encoded, err := json.Marshal(expected)
			require.NoError(t, err, "encode truthful unresolved-success response")
			require.Equal(t, http.StatusServiceUnavailable, recorder.Code, "unverified service success must not pretend rejection or acceptance")
			require.Equal(t, string(encoded)+"\n", recorder.Body.String(), "unverified success should return only the original independently known caller key")
			require.Empty(t, auditLog.String(), "unverified success must not emit postcommit audit work")
		})
	}
}

// This obtains a sealed receipt through the real DB reader using synthetic SQL
// rows. It does not forge Admission's private fields, activate a protocol or use
// PostgreSQL. The current scope is deliberately ended/awaiting input with a
// retained specialist marker and different epochs; exact replay must still
// return only the original accepted envelope after current access validation.
func threadMessageReplayFixture(t *testing.T, original db.MessageAdmissionDisposition) (db.Admission, pgxmock.PgxPoolIface, uuid.UUID) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "create isolated CPU receipt fixture")
	t.Cleanup(mock.Close)
	orgID, sessionID, laneID, actorID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	dispatchID, inboxID, ownerID := uuid.New(), uuid.New(), uuid.New()
	laneEpoch := int64(7)
	fence, err := db.NewAdmissionFence(sessionID, &laneID, 5, &laneEpoch)
	require.NoError(t, err, "construct original receipt scope")
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	input := db.MessageAdmissionInput{OrgID: orgID, Fence: fence, ActorID: &actorID, Message: "continue ACS-123", Dispatch: db.MessageAdmissionDispatchOptions{RunAt: now, MaxAttempts: 3}}
	comment := models.SessionReviewComment{ID: uuid.New(), OrgID: orgID, SessionID: sessionID, Resolved: true}
	comments := []models.SessionReviewComment{}
	if original == db.MessageAdmissionNewJob {
		input.ResolveReviewCommentIDs = []uuid.UUID{comment.ID}
		comments = []models.SessionReviewComment{comment}
	}
	input.Ingress, err = db.PrepareMessageAdmissionIngress(input, "synthetic-key")
	require.NoError(t, err, "prepare canonical original semantic identity")
	command, err := db.NewMessageAdmission(input)
	require.NoError(t, err, "construct original frozen command")
	threadStatus := models.ThreadStatusIdle
	if original == db.MessageAdmissionQueued {
		threadStatus = models.ThreadStatusRunning
	}
	jobID, claimToken := uuid.Nil, uuid.Nil
	claimTurn := 0
	if original == db.MessageAdmissionNewJob {
		jobID, claimToken, claimTurn, threadStatus = uuid.New(), uuid.New(), 3, models.ThreadStatusRunning
	}
	envelope, err := json.Marshal(map[string]any{"version": 1, "semantic_payload": string(command.SemanticPayload()), "session_epoch": 5, "thread_epoch": 7, "message_id": 42, "message_turn": 3, "run_at": now, "max_attempts": 3, "disposition": original, "thread_status": threadStatus, "inbox_id": inboxID, "job_id": jobID, "claim_token": claimToken, "claim_turn": claimTurn, "resolved_comments": comments, "target_node_id": "synthetic-private-target", "reason": "synthetic-internal-reason"})
	require.NoError(t, err, "encode immutable synthetic accepted envelope")
	digest, err := input.Ingress.Digest().Encode()
	require.NoError(t, err, "encode original exact digest")
	expectPolicy := func() {
		mock.ExpectQuery(`SELECT\s+current_setting\('transaction_isolation'\)`).WillReturnRows(pgxmock.NewRows([]string{"isolation", "marker", "lock", "statement"}).AddRow("read committed", "1", "250ms", "1500ms"))
	}
	expectDispatch := func() {
		parentEpoch, threadEpoch, messageID := int64(5), int64(7), int64(42)
		mock.ExpectQuery(`SELECT id,session_id,thread_id,session_epoch,thread_epoch,`).WithArgs(orgID, input.Ingress.Kind(), input.Ingress.Key()).WillReturnRows(pgxmock.NewRows([]string{"id", "session", "lane", "session_epoch", "thread_epoch", "message", "kind", "digest", "payload", "normalization"}).AddRow(dispatchID, sessionID, &laneID, &parentEpoch, &threadEpoch, &messageID, "message", digest, envelope, 1))
	}
	expectOwners := func() {
		mock.ExpectQuery(`SELECT s.code_review_owner_pr_id,s.automation_owner_generation_id,`).WithArgs(orgID, sessionID, laneID).WillReturnRows(pgxmock.NewRows([]string{"review", "generation", "target", "dispatch", "token", "job"}).AddRow(&ownerID, nil, nil, nil, nil, nil))
		mock.ExpectQuery(`SELECT id,pull_request_id FROM pull_request_repair_runs`).WithArgs(orgID, sessionID, pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows([]string{"id", "pr"}))
	}
	mock.ExpectBegin()
	expectPolicy()
	expectDispatch()
	expectPolicy()
	mock.ExpectQuery(`SELECT pg_try_advisory_xact_lock_shared`).WithArgs("session_lifecycle_cutover:" + orgID.String() + ":" + sessionID.String()).WillReturnRows(pgxmock.NewRows([]string{"locked"}).AddRow(true))
	normalization := json.RawMessage(`{"schema_version":1,"receipt_id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","floor_receipt_id":"ffffffff-1111-4222-8333-444444444444","cutoff_id":1,"complete":true}`)
	capability := json.RawMessage(`{"schema_version":1,"release_receipt_id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","tuples":[{"provider":"codex","cli_build":"synthetic-cli-v1","image_build":"synthetic-image-v1","resume_identity":"native_conversation","verification_protocol":"synthetic-verify-v1","conformance_receipt_id":"ffffffff-1111-4222-8333-444444444444"}]}`)
	version, cutoff, minimum, cutoverState := int64(9), int64(1), 1, models.LifecycleCutoverStateActive
	mock.ExpectQuery(`SELECT s.org_id, s.id, s.lifecycle_version,`).WithArgs(orgID, sessionID).WillReturnRows(pgxmock.NewRows([]string{"org", "session", "protocol", "cutover_org", "cutover_session", "version", "state", "cutoff", "normalization", "activated", "minimum", "capability", "provider", "lanes"}).AddRow(orgID, sessionID, int16(1), &orgID, &sessionID, &version, &cutoverState, &cutoff, normalization, &now, &minimum, capability, models.AgentTypeCodex, json.RawMessage(`[{"protocol_version":1,"provider":"codex"}]`)))
	expectOwners()
	mock.ExpectQuery(`SELECT bool_and\(pg_try_advisory_xact_lock`).WithArgs([]string{"code_review_pr:" + orgID.String() + ":" + ownerID.String()}).WillReturnRows(pgxmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectQuery(`SELECT id FROM session_input_dispatches WHERE org_id=\$1 AND id=ANY`).WithArgs(orgID, []uuid.UUID{dispatchID}).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(dispatchID))
	if jobID != uuid.Nil {
		mock.ExpectQuery(`SELECT id FROM jobs WHERE org_id=\$1 AND id=ANY`).WithArgs(orgID, []uuid.UUID{jobID}).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
	}
	expectPolicy()
	end := int64(999)
	mock.ExpectQuery(`SELECT id, org_id, status, deleted_at, archived_at,`).WithArgs(orgID, sessionID).WillReturnRows(pgxmock.NewRows([]string{"id", "org", "status", "deleted", "archived", "epoch", "end", "protocol"}).AddRow(sessionID, orgID, models.SessionStatusAwaitingInput, nil, nil, int64(999), &end, int16(1)))
	mock.ExpectQuery(`SELECT id FROM session_threads`).WithArgs(orgID, sessionID, laneID, pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(laneID))
	mock.ExpectQuery(`SELECT id, org_id, session_id, status, archived_at,`).WithArgs(orgID, sessionID, []uuid.UUID{laneID}).WillReturnRows(pgxmock.NewRows([]string{"id", "org", "session", "status", "archived", "epoch", "end", "protocol"}).AddRow(laneID, orgID, sessionID, models.ThreadStatusAwaitingInput, nil, int64(999), &end, int16(1)))
	expectOwners()
	expectDispatch()
	mock.ExpectQuery(`SELECT role FROM organization_memberships`).WithArgs(orgID, actorID).WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow(models.RoleMember))
	mock.ExpectQuery(`SELECT .* FROM session_messages WHERE org_id`).WithArgs(orgID, int64(42)).WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "user_id", "turn_number", "role", "content", "source", "created_at"}).AddRow(int64(42), sessionID, orgID, &laneID, &actorID, 3, models.MessageRoleUser, input.Message, models.SessionMessageSource(""), now))
	inboxPayload, err := json.Marshal(map[string]any{"message_id": 42, "turn_number": 3, "plan_mode": false, "content": input.Message})
	require.NoError(t, err, "encode exact inbox association")
	mock.ExpectQuery(`SELECT .* FROM thread_inbox_entries WHERE org_id=\$1 AND session_id=\$2 AND thread_id=\$3 AND id=\$4 AND message_id=\$5`).WithArgs(orgID, sessionID, laneID, inboxID, int64(42)).WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "session_id", "thread_id", "sequence_no", "message_id", "client_message_id", "entry_type", "payload", "delivery_state", "delivery_attempts", "last_error", "owner_node_id", "runtime_id", "accepted_at", "delivered_at", "acked_at", "applied_at", "created_at", "updated_at"}).AddRow(inboxID, orgID, sessionID, laneID, int64(1), int64(42), "synthetic-key", models.ThreadInboxEntryTypeUserMessage, inboxPayload, models.ThreadInboxDeliveryStatePending, 0, nil, nil, nil, now, nil, nil, nil, now, now))
	if jobID != uuid.Nil {
		mock.ExpectQuery(`SELECT queue='agent' AND job_type='continue_session'`).WithArgs(orgID, jobID, pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows([]string{"exact"}).AddRow(true))
	}
	mock.ExpectCommit()
	ctx, cancel := context.WithTimeout(context.Background(), db.LifecycleTransactionTimeout)
	defer cancel()
	tx, err := mock.Begin(ctx)
	require.NoError(t, err, "open isolated receipt fixture unit")
	admission, found, err := db.FindMessageAdmissionReplayInTx(ctx, tx, orgID, sessionID, laneID, input.Ingress)
	require.NoError(t, err, "real receipt reader should validate the original accepted identity despite changed current policy")
	require.True(t, found, "exact committed fixture identity should resolve positively")
	require.NoError(t, tx.Commit(ctx), "fixture owner should confirm observation completion before exposing receipt")
	require.NoError(t, mock.ExpectationsWereMet(), "only the real bounded exact receipt observation should execute")
	return admission, mock, actorID
}

func TestSendThreadMessageReplaySuppressesAllAdapterEffects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		original     db.MessageAdmissionDisposition
		status       int
		state        models.LifecycleAdmissionState
		foreignScope bool
	}{
		{name: "bound original is still created", original: db.MessageAdmissionNewJob, status: http.StatusCreated, state: models.LifecycleAdmissionBound},
		{name: "queued original is still accepted", original: db.MessageAdmissionQueued, status: http.StatusAccepted, state: models.LifecycleAdmissionWaiting},
		{name: "waiting original is still accepted", original: db.MessageAdmissionWaiting, status: http.StatusAccepted, state: models.LifecycleAdmissionWaiting},
		{name: "different receipt tenant cannot disclose acceptance", original: db.MessageAdmissionWaiting, status: http.StatusServiceUnavailable, foreignScope: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			admission, auditDB, actorID := threadMessageReplayFixture(t, tt.original)
			orgID, sessionID := admission.OrgID(), admission.Fence().SessionID()
			laneID, threaded := admission.Fence().ThreadID()
			require.True(t, threaded, "thread receipt should preserve an exact lane")
			message, inbox := admission.Message(), admission.InboxEntry()
			// Poison the adapter-work fields deliberately. Replay suppression must
			// be explicit, not an accidental consequence of today's nil fields.
			result := &thread.SendMessageResult{Admission: &admission, Message: &message, InboxEntry: &inbox, ThreadStatus: models.ThreadStatusAwaitingInput, DeliveryState: models.ThreadInboxDeliveryStatePending, ResolvedComments: []models.SessionReviewComment{{ID: uuid.New(), OrgID: orgID, SessionID: sessionID, Resolved: true}}, AnsweredQuestion: &models.SessionQuestion{ID: uuid.New(), SessionID: sessionID}, AnsweredHumanInput: &models.HumanInputRequest{ID: uuid.New(), SessionID: sessionID}}
			handler := NewSessionThreadHandler(admissionOutcomeThreadService{send: func(context.Context, thread.SendMessageInput) (*thread.SendMessageResult, error) { return result, nil }})
			var auditLog bytes.Buffer
			logger := zerolog.New(&auditLog)
			handler.SetAuditEmitter(db.NewAuditEmitter(db.NewAuditLogStore(auditDB), logger))
			handler.SetLogger(logger)
			linker := &fakeLinearLinker{}
			handler.SetLinearLinker(linker)
			requestOrg := orgID
			if tt.foreignScope {
				requestOrg = uuid.New()
			}
			req := admissionOutcomeRequest(requestOrg, sessionID, laneID, `{"message":"continue ACS-123","client_message_id":"synthetic-key"}`)
			req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: actorID}))
			recorder := httptest.NewRecorder()
			handler.SendThreadMessage(recorder, req)
			require.Equal(t, tt.status, recorder.Code, "replay must preserve original bound/wait acceptance rather than current awaiting-input policy")
			require.Equal(t, "application/json", recorder.Header().Get("Content-Type"), "accepted or unresolved replay should use the standard JSON envelope")
			require.Empty(t, recorder.Header().Get("Retry-After"), "an accepted or unknown replay should not manufacture an unaccepted retry hint")
			if tt.foreignScope {
				expected := models.ErrorResponse{Error: models.ErrorDetail{Code: "COMMAND_OUTCOME_UNKNOWN", Message: "message acceptance is pending confirmation; retain the same command key", Details: sessionMessageUnknownDetails{ClientMessageID: "synthetic-key"}}}
				encoded, err := json.Marshal(expected)
				require.NoError(t, err, "encode safe unresolved mismatched result")
				require.Equal(t, string(encoded)+"\n", recorder.Body.String(), "different tenant receipt must expose no message, dispatch or internal scope")
				require.Empty(t, auditLog.String(), "mismatched success must not repeat any adapter audit")
				calls, _ := linker.midSessionObservation()
				require.Zero(t, calls, "mismatched receipt must not trigger Linear linking")
				return
			}
			expectedReceipt := models.SessionMessageAdmissionReceipt{DispatchID: admission.DispatchID(), State: tt.state, IngressKind: models.SessionMessageIngressClient, IngressKey: admission.Ingress().Key(), Replayed: true}
			expected := models.SingleResponse[models.SendThreadMessageResponse]{Data: models.SendThreadMessageResponse{Message: message, InboxEntry: &inbox, ThreadStatus: admission.ThreadStatus(), DeliveryState: inbox.DeliveryState, Admission: &expectedReceipt}}
			encoded, err := json.Marshal(expected)
			require.NoError(t, err, "encode original safe accepted response")
			require.Equal(t, string(encoded)+"\n", recorder.Body.String(), "exact replay should serialize the safe retained message envelope only")
			require.Empty(t, auditLog.String(), "replay must not emit question, human-input or comment audits even if service historical fields are populated")
			calls, _ := linker.midSessionObservation()
			require.Zero(t, calls, "replay must not start another mid-session Linear link")
			require.NotContains(t, recorder.Body.String(), "synthetic-private-target", "receipt projection must omit internal target placement")
			require.NotContains(t, recorder.Body.String(), "synthetic-internal-reason", "receipt projection must omit private internal reason values")
			require.NotContains(t, recorder.Body.String(), "claim_token", "safe response must not serialize authority tokens")
		})
	}
}
