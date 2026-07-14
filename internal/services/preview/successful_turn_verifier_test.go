package preview

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestVerificationOutcomeError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		status    models.PreviewVerificationStatus
		failure   string
		expectErr bool
	}{
		{name: "allows passed", status: models.PreviewVerificationStatusPassed},
		{name: "allows skipped", status: models.PreviewVerificationStatusSkipped},
		{name: "blocks failed", status: models.PreviewVerificationStatusFailed, failure: "console error", expectErr: true},
		{name: "blocks human intervention", status: models.PreviewVerificationStatusHumanInterventionRequired, failure: "login required", expectErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := verificationOutcomeError(models.PreviewVerificationRun{Status: tt.status, FailureReason: tt.failure}, nil)
			if tt.expectErr {
				require.Error(t, err, "terminal verification failure should block completion")
			} else {
				require.NoError(t, err, "non-failure verification outcome should allow completion")
			}
		})
	}
}

func TestBrowserVerificationObserverUsesSessionPolicyAndLease(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		path        string
		control     models.PreviewBrowserControlState
		expectedErr error
	}{
		{name: "rejects disallowed path", path: "/blocked", control: models.PreviewBrowserControlAgent, expectedErr: ErrNavigationNotAllowed},
		{name: "respects human control", path: "/safe", control: models.PreviewBrowserControlHuman, expectedErr: ErrBrowserControlHeld},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, sessionID, previewID := uuid.New(), uuid.New(), uuid.New()
			store := &fakeBrowserSessionStore{record: &models.PreviewBrowserSession{
				ID: uuid.New(), OrgID: orgID, SessionID: sessionID, PreviewInstanceID: &previewID,
				ContextKey: "session:" + sessionID.String(), ControlState: tt.control,
			}}
			observer := browserVerificationObserver{
				browser: NewBrowserSessionService(store, &fakeSessionBrowserInspector{}),
				orgID:   orgID, sessionID: sessionID, previewID: previewID,
				policy: BrowserSessionPolicy{PersistSession: true, AllowedPaths: []string{"/safe"}},
			}

			_, err := observer.Observe(context.Background(), tt.path, models.ViewportSpec{Width: 1440, Height: 900})

			require.ErrorIs(t, err, tt.expectedErr, "automatic verification should honor browser policy and control ownership")
		})
	}
}

func TestVerificationOutcomeErrorPropagatesPersistenceFailure(t *testing.T) {
	t.Parallel()
	expected := errors.New("database unavailable")
	require.ErrorIs(t, verificationOutcomeError(models.PreviewVerificationRun{}, expected), expected, "evidence persistence failures should block completion")
}
