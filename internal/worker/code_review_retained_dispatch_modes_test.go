package worker

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRetainedReviewerEntryCannotClaimPending(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		ineligible    bool
		becameActive  bool
		newerWriter   bool
		invalidMode   bool
		expectedUnits int
	}{
		{name: "eligible pending waits for existing parent gates", expectedUnits: 1},
		{name: "ineligible pending is cancelled without a claim", ineligible: true, expectedUnits: 2},
		{name: "fresh eligible owner defeats stale cancellation hint", ineligible: true, becameActive: true, expectedUnits: 2},
		{name: "newer tuple defeats stale cancellation hint", ineligible: true, newerWriter: true, expectedUnits: 2},
		{name: "unsupported mode refuses before reading", invalidMode: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newRetainedDispatchCPUFixture(t)
			f.eligible = !tt.ineligible
			mock, runner, sender := newRetainedDispatchCPUHarness(t)
			mode := codeReviewObserveRetainedOnly
			if tt.invalidMode {
				mode = codeReviewReviewerDispatchMode(255)
			} else {
				expectRetainedDispatchCPURead(mock, f)
				if tt.ineligible {
					current := f
					current.eligible = tt.becameActive
					if tt.newerWriter {
						current.writer++
					}
					expectRetainedDispatchCPULocks(mock, current)
					if !tt.becameActive && !tt.newerWriter {
						expectRetainedDispatchCPULocks(mock, current)
						expectRetainedDispatchCPUUpdate(mock, current, "cancel", "owner_ineligible", 0, uuid.Nil)
						current.state, current.writer, current.due = db.ReviewerDispatchCancelled, current.writer+1, false
						expectRetainedDispatchCPUIntent(mock, current)
					}
				}
			}
			err := advanceRetainedCodeReviewReviewer(context.Background(), runner, sender, f.job, f.resultID, time.Now().Add(time.Hour), mode)
			if tt.invalidMode {
				require.ErrorIs(t, err, db.ErrReviewerDispatchInput, "an unsupported phase cannot obtain a private default send permission")
			} else {
				require.NoError(t, err, "early reconciliation may observe or cancel only freshly proven ineligible pending work")
			}
			require.Equal(t, tt.expectedUnits, runner.calls, "entry mode cannot acquire a claim or send settlement unit")
			require.Empty(t, sender.inputs, "neither eligible pending, stale cancellation evidence nor unsupported phase can release a send at handler entry")
			require.NoError(t, mock.ExpectationsWereMet(), "observe-only cancellation must recheck exact metadata and result after ordered locks and never issue claim SQL")
		})
	}
}
