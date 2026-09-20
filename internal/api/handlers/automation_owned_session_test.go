package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type stubOwnershipGuard struct {
	owner *models.SessionAutomationOwner
	err   error
	calls int
}

func (g *stubOwnershipGuard) RejectIfAutomationOwned(_ context.Context, _, _ uuid.UUID) error {
	g.calls++
	if g.err != nil {
		return g.err
	}
	if g.owner == nil {
		return nil
	}
	return &models.SessionAutomationOwnedError{Owner: *g.owner}
}

// TestRejectAutomationOwnedSession proves the shared guard: an owned
// session is refused with 409 SESSION_AUTOMATION_OWNED and the details a
// person needs to take the session back, an ordinary session passes
// through, a lookup failure is a 500 rather than a silent pass, and an
// unwired guard is a no-op.
func TestRejectAutomationOwnedSession(t *testing.T) {
	t.Parallel()
	automationID := uuid.New()
	targetID := uuid.New()
	owner := models.SessionAutomationOwner{
		AutomationID:   automationID,
		TargetID:       targetID,
		GenerationID:   uuid.New(),
		ResetURL:       models.SessionAutomationResetURL(automationID, targetID),
		ReleasePending: true,
	}

	tests := []struct {
		name       string
		guard      *stubOwnershipGuard
		unwired    bool
		wantHandle bool
		wantStatus int
		wantCode   string
	}{
		{name: "an owned session is refused with the target and its reset link", guard: &stubOwnershipGuard{owner: &owner}, wantHandle: true, wantStatus: http.StatusConflict, wantCode: "SESSION_AUTOMATION_OWNED"},
		{name: "an ordinary session passes through", guard: &stubOwnershipGuard{}},
		{name: "a lookup failure refuses rather than passing", guard: &stubOwnershipGuard{err: errors.New("db down")}, wantHandle: true, wantStatus: http.StatusInternalServerError, wantCode: "SESSION_LOOKUP_FAILED"},
		{name: "an unwired guard is a no-op", unwired: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/x/messages", nil)
			var handled bool
			if tt.unwired {
				handled = rejectAutomationOwnedSession(rec, req, nil, uuid.New(), uuid.New())
			} else {
				handled = rejectAutomationOwnedSession(rec, req, tt.guard, uuid.New(), uuid.New())
			}
			require.Equal(t, tt.wantHandle, handled, "whether the guard answered")
			if !tt.wantHandle {
				require.Equal(t, http.StatusOK, rec.Code, "nothing was written")
				return
			}
			require.Equal(t, tt.wantStatus, rec.Code, "status")
			var body models.ErrorResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), "decode body")
			require.Equal(t, tt.wantCode, body.Error.Code, "error code")
			if tt.wantCode != "SESSION_AUTOMATION_OWNED" {
				return
			}
			details, err := json.Marshal(body.Error.Details)
			require.NoError(t, err, "re-encode details")
			var got models.SessionAutomationOwner
			require.NoError(t, json.Unmarshal(details, &got), "decode details")
			require.Equal(t, owner.AutomationID, got.AutomationID, "the automation is named")
			require.Equal(t, owner.TargetID, got.TargetID, "the target is named")
			require.Equal(t, owner.ResetURL, got.ResetURL, "the reset link points at the target's reset action")
			require.True(t, got.ReleasePending, "a pending release is reported, so the UI can say the session is about to come back")
		})
	}
}

// TestWriteAutomationOwnedError proves a service's owned-session error is
// answered with the same 409, and that other errors fall through to the
// handler's own mapping.
func TestWriteAutomationOwnedError(t *testing.T) {
	t.Parallel()
	t.Run("an owned-session error is answered", func(t *testing.T) {
		t.Parallel()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/x/threads", nil)
		owner := models.SessionAutomationOwner{AutomationID: uuid.New(), TargetID: uuid.New()}
		handled := writeAutomationOwnedError(rec, req, &models.SessionAutomationOwnedError{Owner: owner})
		require.True(t, handled, "the error is answered")
		require.Equal(t, http.StatusConflict, rec.Code, "409")
	})
	t.Run("other errors fall through", func(t *testing.T) {
		t.Parallel()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/x/threads", nil)
		require.False(t, writeAutomationOwnedError(rec, req, errors.New("boom")), "an unrelated error is left to the caller")
		require.Equal(t, http.StatusOK, rec.Code, "nothing was written")
	})
	t.Run("the sentinel matches the typed error", func(t *testing.T) {
		t.Parallel()
		err := error(&models.SessionAutomationOwnedError{})
		require.ErrorIs(t, err, models.ErrSessionAutomationOwned, "callers can match the sentinel")
	})
}
