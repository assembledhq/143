package reviewloop

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
	"github.com/stretchr/testify/require"
)

func TestRetainedCreationRefusalClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"capacity", db.ErrThreadLimitReached, "creation_thread_capacity"},
		{"terminal", threadsvc.ErrSessionTerminal, "creation_session_terminal"},
		{"code review owned", &models.SessionCodeReviewOwnedError{}, "creation_code_review_owned"},
		{"automation owned", &models.SessionAutomationOwnedError{}, "creation_automation_owned"},
		{"model", threadsvc.ErrInvalidModel, "creation_invalid_model"},
		{"agent", threadsvc.ErrInvalidAgentType, "creation_invalid_agent"},
		{"identity", db.ErrThreadCreationIdentityConflict, "thread_identity_conflict"},
		{"transport", errors.New("synthetic connection lost"), ""},
		{"success", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.err
			if err != nil {
				err = fmt.Errorf("creation: %w", err)
			}
			require.Equal(t, tt.want, retainedCreationRefusalDetail(err), "only existing typed pre-row E1 refusals select bounded durable outcomes")
		})
	}
}

func TestRetainedRuntimeAdapterRequiresPreflight(t *testing.T) {
	t.Parallel()
	adapter := RuntimeAdapter{Threads: &fakeThreadService{}}
	err := adapter.PreflightCreateThread(context.Background(), threadsvc.CreateThreadInput{})
	require.EqualError(t, err, "retained thread creation preflight unavailable", "a production adapter cannot silently skip required retained creation checks")
}
