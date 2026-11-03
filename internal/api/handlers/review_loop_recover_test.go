package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/assembledhq/143/internal/models"
	reviewloopsvc "github.com/assembledhq/143/internal/services/reviewloop"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestReviewLoopHandlerRecover(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		state   models.ReviewLoopAdmissionState
		err     error
		foreign bool
		status  int
		code    string
	}{
		{name: "known creation pending admission", state: models.ReviewLoopAdmissionPending, status: http.StatusOK},
		{name: "known creation attempting", state: models.ReviewLoopAdmissionAttempting, status: http.StatusOK},
		{name: "known creation unknown outcome", state: models.ReviewLoopAdmissionUnknown, status: http.StatusOK},
		{name: "known creation accepted", state: models.ReviewLoopAdmissionAccepted, status: http.StatusOK},
		{name: "known creation refused admission", state: models.ReviewLoopAdmissionRefused, status: http.StatusOK},
		{name: "known creation attention", state: models.ReviewLoopAdmissionAttention, status: http.StatusOK},
		{name: "known creation cancelled admission", state: models.ReviewLoopAdmissionCancelled, status: http.StatusOK},
		{name: "creation unconfirmed", err: &reviewloopsvc.StartPendingError{LoopID: uuid.New()}, status: http.StatusServiceUnavailable, code: "REVIEW_LOOP_START_PENDING_CONFIRMATION"},
		{name: "known refusal", err: reviewloopsvc.ErrReviewLoopStartRefused, status: http.StatusConflict, code: "REVIEW_LOOP_START_REFUSED"},
		{name: "private failure", err: errors.New("synthetic private envelope and prompt"), status: http.StatusInternalServerError, code: "RECOVER_REVIEW_LOOP_FAILED"},
		{name: "scope mismatch", foreign: true, status: http.StatusNotFound, code: "NOT_FOUND"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org, session, id := uuid.New(), uuid.New(), uuid.New()
			loop := models.SessionReviewLoop{ID: id, OrgID: org, SessionID: session, AdmissionState: &tt.state}
			if tt.foreign {
				loop.SessionID = uuid.New()
			}
			svc := &fakeReviewLoopService{recoverFn: func(_ context.Context, gotOrg, gotSession, gotID uuid.UUID) (*models.SessionReviewLoop, error) {
				require.Equal(t, [3]uuid.UUID{org, session, id}, [3]uuid.UUID{gotOrg, gotSession, gotID}, "follow-up must recover the exact route identity within the request tenant")
				return &loop, tt.err
			}}
			request := threadRequest(http.MethodPost, "/recover", "", org, map[string]string{"id": session.String(), "loop_id": id.String()})
			response := httptest.NewRecorder()
			NewReviewLoopHandler(svc, nil).Recover(response, request)
			require.Equal(t, tt.status, response.Code, "recovery must distinguish known creation from uncertain creation and refusal")
			if tt.status == http.StatusOK {
				var body models.SingleResponse[models.SessionReviewLoop]
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body), "recovery returns the public loop projection")
				require.Equal(t, loop, body.Data, "follow-up retains the complete original public loop and admission state")
				return
			}
			var body struct {
				Error struct {
					Code    string         `json:"code"`
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body), "recovery error is valid JSON")
			require.Equal(t, tt.code, body.Error.Code, "recovery uses the stable error contract")
			var details map[string]any
			if tt.status == http.StatusServiceUnavailable {
				details = map[string]any{"loop_id": id.String()}
			}
			require.Equal(t, details, body.Error.Details, "only the requested correlation may be exposed in pending details")
			require.NotContains(t, response.Body.String(), "synthetic private", "storage error text and frozen contents stay private")
		})
	}
}

func TestReviewLoopHandlerRecoverInvalidIDs(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, session, loop string }{
		{name: "session", session: "invalid", loop: uuid.NewString()},
		{name: "loop", session: uuid.NewString(), loop: "invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			response := httptest.NewRecorder()
			NewReviewLoopHandler(nil, nil).Recover(response, threadRequest(http.MethodPost, "/recover", "", uuid.New(), map[string]string{"id": tt.session, "loop_id": tt.loop}))
			require.Equal(t, http.StatusBadRequest, response.Code, "malformed route identity must be rejected before any service call")
		})
	}
}
