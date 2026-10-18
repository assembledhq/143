package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// This pins the legacy command boundary before extraction. In particular, a
// successful dedupe no-op is currently acceptance, not proof of a consumer bound
// to this message. Request-specific readiness is a later compatibility change.
func TestSessionLifecycleCharacterization_SendMessageAcceptance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		status         models.SessionStatus
		deduplicated   bool
		enqueueFails   bool
		expectedStatus int
		expectedTurn   int
	}{
		{name: "running append needs no checkpoint or answer transaction", status: models.SessionStatusRunning, expectedStatus: http.StatusCreated, expectedTurn: 2},
		{name: "idle admission uses the claimed turn", status: models.SessionStatusIdle, expectedStatus: http.StatusCreated, expectedTurn: 9},
		{name: "idle dedupe no-op still accepts the message", status: models.SessionStatusIdle, deduplicated: true, expectedStatus: http.StatusCreated, expectedTurn: 9},
		{name: "idle enqueue error rolls back acceptance", status: models.SessionStatusIdle, enqueueFails: true, expectedStatus: http.StatusInternalServerError, expectedTurn: 9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "each command should own an isolated database fixture")
			defer mock.Close()
			orgID, sessionID, userID := uuid.New(), uuid.New(), uuid.New()
			now := time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)
			handler := newSessionHandler(t, mock)
			// Configure the answer plumbing even for running sends: the absence of
			// an answer must follow the entry-state branch, not a nil dependency.
			handler.SetHumanInputRequestStore(db.NewSessionHumanInputRequestStore(mock))
			expectUnownedCodeReviewSession(mock)
			mock.ExpectQuery("SELECT .+ FROM sessions WHERE").WithArgs(sessionID, orgID).
				WillReturnRows(pgxmock.NewRows(sessionColumns).AddRow(retrySessionRow(sessionID, orgID, tt.status, nil, nil, models.SandboxStateRunning, nil, now)...))
			if tt.status == models.SessionStatusIdle {
				mock.ExpectBegin()
				claimedRow := retrySessionRow(sessionID, orgID, models.SessionStatusRunning, nil, nil, models.SandboxStateRunning, nil, now)
				for i, column := range sessionColumns {
					if column == "current_turn" {
						claimedRow[i] = 8
					}
				}
				mock.ExpectQuery("UPDATE sessions SET status").WithArgs(sessionID, orgID).
					WillReturnRows(pgxmock.NewRows(sessionColumns).AddRow(claimedRow...))
			}
			mock.ExpectQuery("INSERT INTO session_messages").
				WithArgs(sessionID, orgID, (*uuid.UUID)(nil), &userID, tt.expectedTurn, models.MessageRoleUser, "Follow up", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(41), now))
			if tt.status == models.SessionStatusIdle {
				payload, err := json.Marshal(map[string]string{"session_id": sessionID.String(), "org_id": orgID.String()})
				require.NoError(t, err, "the expected continuation payload should be serializable")
				dedupeKey := db.ContinueSessionDedupeKey(sessionID)
				enqueue := mock.ExpectQuery("INSERT INTO jobs").WithArgs(orgID, "agent", "continue_session", payload, 5, &dedupeKey)
				switch {
				case tt.enqueueFails:
					enqueue.WillReturnError(errors.New("queue unavailable"))
					mock.ExpectRollback()
				case tt.deduplicated:
					enqueue.WillReturnRows(pgxmock.NewRows([]string{"id"}))
					mock.ExpectCommit()
				default:
					enqueue.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
					mock.ExpectCommit()
				}
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/messages", strings.NewReader(`{"message":"  Follow up  "}`))
			route := chi.NewRouteContext()
			route.URLParams.Add("id", sessionID.String())
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, route)
			ctx = middleware.WithOrgID(ctx, orgID)
			ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: uuid.New()})
			recorder := httptest.NewRecorder()
			handler.SendMessage(recorder, req.WithContext(ctx))
			require.Equal(t, tt.expectedStatus, recorder.Code, "the legacy command should report acceptance only after its required writes")
			if tt.enqueueFails {
				var actual models.ErrorResponse
				require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &actual), "a rejected admission should return the standard error response")
				require.Equal(t, models.ErrorResponse{Error: models.ErrorDetail{Code: "ENQUEUE_FAILED", Message: "failed to enqueue continue_session job"}}, actual, "enqueue rejection should expose no accepted message or internal error")
			} else {
				var actual models.SingleResponse[models.SessionMessage]
				require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &actual), "an accepted message should decode as the existing response contract")
				expected := models.SingleResponse[models.SessionMessage]{Data: models.SessionMessage{ID: 41, SessionID: sessionID, OrgID: orgID, UserID: &userID, TurnNumber: tt.expectedTurn, Role: models.MessageRoleUser, Content: "Follow up", CreatedAt: now}}
				require.Equal(t, expected, actual, "acceptance should preserve the authenticated tenant, normalized text, and authoritative turn")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "running sends must not answer or enqueue and idle failures must roll back the transaction")
		})
	}
}

// The legacy session composer answers the latest threadless free-text request
// behind the compatibility-question dependency. Unlike structured Answer, it
// currently does not check readiness, and a job dedupe no-op still commits the
// answer. These are characterized migration gaps, not the intended 0a contract.
func TestSessionLifecycleCharacterization_AwaitingInputAnswerBinding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                 string
		deduplicated         bool
		missingCheckpoint    bool
		onlyThreadedInput    bool
		missingQuestionStore bool
	}{
		{name: "answered provider request identifies its continuation"},
		{name: "dedupe no-op commits the provider answer without inserting a job", deduplicated: true},
		{name: "threaded input is ignored by session autoanswer", onlyThreadedInput: true},
		{name: "missing checkpoint does not prevent composer autoanswer", missingCheckpoint: true},
		{name: "missing compatibility question store skips provider autoanswer", missingQuestionStore: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "each awaiting-input command should own its database expectations")
			defer mock.Close()
			orgID, sessionID, userID, requestID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			now := time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)
			snapshotKey := "existing-session-checkpoint"
			var snapshot *string
			if !tt.missingCheckpoint {
				snapshot = &snapshotKey
			}
			handler := newSessionHandler(t, mock)
			handler.SetHumanInputRequestStore(db.NewSessionHumanInputRequestStore(mock))
			if tt.missingQuestionStore {
				handler.questionStore = nil
			}
			expectUnownedCodeReviewSession(mock)
			mock.ExpectQuery("SELECT .+ FROM sessions WHERE").WithArgs(sessionID, orgID).
				WillReturnRows(pgxmock.NewRows(sessionColumns).AddRow(retrySessionRow(sessionID, orgID, models.SessionStatusAwaitingInput, snapshot, nil, models.SandboxStateRunning, nil, now)...))
			mock.ExpectBegin()
			mock.ExpectQuery("UPDATE sessions SET status").WithArgs(sessionID, orgID).
				WillReturnRows(pgxmock.NewRows(sessionColumns))
			mock.ExpectQuery("UPDATE sessions SET status").WithArgs(sessionID, orgID, pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(sessionColumns).AddRow(retrySessionRow(sessionID, orgID, models.SessionStatusRunning, snapshot, nil, models.SandboxStateRunning, nil, now)...))
			mock.ExpectQuery("INSERT INTO session_messages").
				WithArgs(sessionID, orgID, (*uuid.UUID)(nil), &userID, 2, models.MessageRoleUser, "Follow up", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(42), now))
			payload := map[string]string{"session_id": sessionID.String(), "org_id": orgID.String()}
			if !tt.missingQuestionStore {
				answerPayload, err := db.MarshalHumanInputAnswerPayload(models.HumanInputAnswerInput{AnswerText: stringPtr("Follow up")})
				require.NoError(t, err, "the composer should retain the structured free-text answer payload")
				columns := []string{"id", "org_id", "session_id", "thread_id", "turn_number", "agent_type", "provider_request_id", "request_kind", "status", "title", "body", "context", "blocks_phase", "assigned_user_id", "sensitivity", "preferred_channel", "choices", "response_schema", "provider_payload", "answer_text", "answer_payload", "answered_by", "answered_at", "expires_at", "created_at", "activity_phase_id"}
				rows := pgxmock.NewRows(columns)
				if !tt.onlyThreadedInput {
					providerRequestID, answerText := "provider-request", "Follow up"
					rows.AddRow(requestID, orgID, sessionID, (*uuid.UUID)(nil), 1, models.AgentTypeClaudeCode, &providerRequestID, models.HumanInputRequestKindFreeText, models.HumanInputRequestStatusAnswered, "Choose approach", "What should happen next?", (*string)(nil), (*string)(nil), (*uuid.UUID)(nil), models.HumanInputSensitivityTeam, models.HumanInputPreferredChannelSlackThread, []byte("[]"), json.RawMessage(nil), json.RawMessage(nil), &answerText, answerPayload, &userID, &now, (*time.Time)(nil), now, (*uuid.UUID)(nil))
					payload["human_input_request_id"] = requestID.String()
				}
				// This expectation pins the actual store selection performed by the
				// handler: thread_id IS NULL, free_text, latest created_at. A
				// threaded-only fixture yields no row and must not bind an answer.
				mock.ExpectQuery(`(?s)UPDATE session_human_input_requests.*thread_id IS NULL.*request_kind = 'free_text'.*ORDER BY created_at DESC`).
					WithArgs("Follow up", answerPayload, userID, orgID, sessionID).WillReturnRows(rows)
				mock.ExpectQuery("UPDATE session_questions").WithArgs("Follow up", userID, sessionID, orgID).
					WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "question_text", "options", "context", "blocks_phase", "answer_text", "answered_by", "answered_at", "status", "created_at"}))
			}
			encodedPayload, err := json.Marshal(payload)
			require.NoError(t, err, "the continuation should encode the exact autoanswered provider request or no request")
			dedupeKey := db.ContinueSessionDedupeKey(sessionID)
			enqueue := mock.ExpectQuery("INSERT INTO jobs").WithArgs(orgID, "agent", "continue_session", encodedPayload, 5, &dedupeKey)
			if tt.deduplicated {
				enqueue.WillReturnRows(pgxmock.NewRows([]string{"id"}))
			} else {
				enqueue.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
			}
			mock.ExpectCommit()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/messages", strings.NewReader(`{"message":"Follow up"}`))
			route := chi.NewRouteContext()
			route.URLParams.Add("id", sessionID.String())
			ctx := middleware.WithOrgID(req.Context(), orgID)
			ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: uuid.New()})
			recorder := httptest.NewRecorder()
			handler.SendMessage(recorder, req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, route)))
			require.Equal(t, http.StatusCreated, recorder.Code, "the baseline composer should commit its accepted message and selected answer even after a dedupe no-op")
			var actual models.SingleResponse[models.SessionMessage]
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &actual), "the legacy autoanswer response should retain the message envelope")
			expected := models.SingleResponse[models.SessionMessage]{Data: models.SessionMessage{ID: 42, SessionID: sessionID, OrgID: orgID, UserID: &userID, TurnNumber: 2, Role: models.MessageRoleUser, Content: "Follow up", CreatedAt: now}}
			require.Equal(t, expected, actual, "the composer response should preserve the exact message without inventing input or job response fields")
			require.NoError(t, mock.ExpectationsWereMet(), "the baseline should bind only a selected threadless request, skip answers without the question store, and commit after dedupe")
		})
	}
}
