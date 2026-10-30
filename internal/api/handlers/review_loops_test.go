package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	reviewloopsvc "github.com/assembledhq/143/internal/services/reviewloop"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
)

type fakeReviewLoopService struct {
	recoverFn func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*models.SessionReviewLoop, error)
	startFn   func(ctx context.Context, orgID, sessionID uuid.UUID, req reviewloopsvc.StartReviewLoopRequest) (*models.SessionReviewLoop, error)
}

func (f *fakeReviewLoopService) RecoverStart(ctx context.Context, orgID, sessionID, startID uuid.UUID) (*models.SessionReviewLoop, error) {
	return f.recoverFn(ctx, orgID, sessionID, startID)
}

func (f *fakeReviewLoopService) Start(ctx context.Context, orgID, sessionID uuid.UUID, req reviewloopsvc.StartReviewLoopRequest) (*models.SessionReviewLoop, error) {
	return f.startFn(ctx, orgID, sessionID, req)
}

func TestReviewLoopHandler_Start_OwnershipErrors(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	automationID := uuid.New()
	targetID := uuid.New()
	generationID := uuid.New()
	prID := uuid.New()
	loopID := uuid.New()

	tests := []struct {
		name          string
		startErr      error
		expectedCode  int
		expectedError string
		// expectedDetails is compared whole so a dropped field fails the test.
		expectedDetails map[string]any
	}{
		{
			name:          "visible thread limit is a known conflict",
			startErr:      db.ErrThreadLimitReached,
			expectedCode:  http.StatusConflict,
			expectedError: "THREAD_LIMIT",
		},
		{
			name:          "terminal session is a known conflict",
			startErr:      threadsvc.ErrSessionTerminal,
			expectedCode:  http.StatusConflict,
			expectedError: "SESSION_TERMINAL",
		},
		{
			name:          "invalid creation agent is a known bad request",
			startErr:      threadsvc.ErrInvalidAgentType,
			expectedCode:  http.StatusBadRequest,
			expectedError: "INVALID_AGENT_TYPE",
		},
		{
			name:          "invalid creation model is a known bad request",
			startErr:      threadsvc.ErrInvalidModel,
			expectedCode:  http.StatusBadRequest,
			expectedError: "INVALID_MODEL",
		},
		{
			name:            "pending creation keeps safe original correlation",
			startErr:        fmt.Errorf("synthetic private storage detail: %w", &reviewloopsvc.StartPendingError{LoopID: loopID}),
			expectedCode:    http.StatusServiceUnavailable,
			expectedError:   "REVIEW_LOOP_START_PENDING_CONFIRMATION",
			expectedDetails: map[string]any{"loop_id": loopID.String()},
		},
		{
			name:          "known retained refusal",
			startErr:      reviewloopsvc.ErrReviewLoopStartRefused,
			expectedCode:  http.StatusConflict,
			expectedError: "REVIEW_LOOP_START_REFUSED",
		},
		{
			name: "automation owned session",
			startErr: fmt.Errorf("create review thread: %w", &models.SessionAutomationOwnedError{Owner: models.SessionAutomationOwner{
				AutomationID:   automationID,
				TargetID:       targetID,
				GenerationID:   generationID,
				ResetURL:       models.SessionAutomationResetURL(automationID, targetID),
				ReleasePending: true,
			}}),
			expectedCode:  http.StatusConflict,
			expectedError: "SESSION_AUTOMATION_OWNED",
			expectedDetails: map[string]any{
				"automation_id":   automationID.String(),
				"target_id":       targetID.String(),
				"generation_id":   generationID.String(),
				"reset_url":       models.SessionAutomationResetURL(automationID, targetID),
				"release_pending": true,
			},
		},
		{
			name:          "code review owned session",
			startErr:      fmt.Errorf("create review thread: %w", &models.SessionCodeReviewOwnedError{PullRequestID: prID}),
			expectedCode:  http.StatusConflict,
			expectedError: "SESSION_CODE_REVIEW_OWNED",
			expectedDetails: map[string]any{
				"pull_request_id": prID.String(),
				"release_pending": false,
			},
		},
		{
			name:          "unexpected failure",
			startErr:      errors.New("database unavailable"),
			expectedCode:  http.StatusInternalServerError,
			expectedError: "START_REVIEW_FAILED",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := &fakeReviewLoopService{
				startFn: func(_ context.Context, gotOrgID, gotSessionID uuid.UUID, _ reviewloopsvc.StartReviewLoopRequest) (*models.SessionReviewLoop, error) {
					require.Equal(t, orgID, gotOrgID, "Start should receive the request org")
					require.Equal(t, sessionID, gotSessionID, "Start should receive the session from the URL")
					return nil, tt.startErr
				},
			}
			handler := NewReviewLoopHandler(svc, nil)

			req := threadRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/review-loops", `{"agent_type":"claude_code","max_passes":3}`, orgID, map[string]string{
				"id": sessionID.String(),
			})
			w := httptest.NewRecorder()

			handler.Start(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "Start should return the expected status code")

			var errResp struct {
				Error struct {
					Code    string         `json:"code"`
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp), "response body should be valid JSON")
			require.Equal(t, tt.expectedError, errResp.Error.Code, "Start should return the expected error code")
			require.Equal(t, tt.expectedDetails, errResp.Error.Details, "known refusals expose only their existing safe details and never pending-start correlation")
		})
	}
}
