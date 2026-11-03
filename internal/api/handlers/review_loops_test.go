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

	"github.com/assembledhq/143/internal/models"
	reviewloopsvc "github.com/assembledhq/143/internal/services/reviewloop"
)

type fakeReviewLoopService struct {
	startFn func(ctx context.Context, orgID, sessionID uuid.UUID, req reviewloopsvc.StartReviewLoopRequest) (*models.SessionReviewLoop, error)
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

	tests := []struct {
		name          string
		startErr      error
		expectedCode  int
		expectedError string
		// expectedDetails is compared whole so a dropped field fails the test.
		expectedDetails map[string]any
	}{
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
			if tt.expectedDetails != nil {
				require.Equal(t, tt.expectedDetails, errResp.Error.Details, "ownership conflict should return exactly the owner as details")
			}
		})
	}
}
