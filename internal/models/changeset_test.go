package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeriveChangesetStackState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		statuses             []ChangesetStatus
		confirmationRequired bool
		expected             ChangesetStackState
	}{
		{name: "one pull request", statuses: []ChangesetStatus{ChangesetStatusPROpen}, expected: ChangesetStackStateOnePR},
		{name: "draft stack", statuses: []ChangesetStatus{ChangesetStatusPlanned, ChangesetStatusPlanned}, expected: ChangesetStackStateDraft},
		{name: "stale descendant", statuses: []ChangesetStatus{ChangesetStatusPROpen, ChangesetStatusNeedsRestack}, expected: ChangesetStackStateNeedsRestack},
		{name: "conflict blocks stack", statuses: []ChangesetStatus{ChangesetStatusPROpen, ChangesetStatusRestackConflict}, expected: ChangesetStackStateBlocked},
		{name: "semantic delta blocks stack", statuses: []ChangesetStatus{ChangesetStatusPROpen, ChangesetStatusReady}, confirmationRequired: true, expected: ChangesetStackStateBlocked},
		{name: "partial merge", statuses: []ChangesetStatus{ChangesetStatusMerged, ChangesetStatusPROpen}, expected: ChangesetStackStatePartiallyMerged},
		{name: "fully merged", statuses: []ChangesetStatus{ChangesetStatusMerged, ChangesetStatusMerged}, expected: ChangesetStackStateMerged},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			changesets := make([]ChangesetSummary, 0, len(tt.statuses))
			for _, status := range tt.statuses {
				changesets = append(changesets, ChangesetSummary{Status: status})
			}
			if tt.confirmationRequired {
				changesets[len(changesets)-1].RestackConfirmationRequired = true
			}
			require.Equal(t, tt.expected, DeriveChangesetStackState(changesets), "stack health should be derived from changeset statuses")
		})
	}
}

func TestChangesetStatusValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		status    ChangesetStatus
		expectErr bool
	}{
		{name: "planned", status: ChangesetStatusPlanned},
		{name: "needs restack", status: ChangesetStatusNeedsRestack},
		{name: "external update", status: ChangesetStatusExternalUpdateDetected},
		{name: "invalid", status: ChangesetStatus("unknown"), expectErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.status.Validate()
			if tt.expectErr {
				require.Error(t, err, "invalid changeset status should be rejected")
				return
			}
			require.NoError(t, err, "known changeset status should be accepted")
		})
	}
}

func TestChangesetRestackDeltaKindValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		kind      ChangesetRestackDeltaKind
		expectErr bool
	}{
		{name: "clean replay", kind: ChangesetRestackDeltaCleanReplay},
		{name: "mechanical fallout", kind: ChangesetRestackDeltaMechanicalFallout},
		{name: "semantic change", kind: ChangesetRestackDeltaSemanticChange},
		{name: "invalid", kind: ChangesetRestackDeltaKind("unknown"), expectErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.kind.Validate()
			if tt.expectErr {
				require.Error(t, err, "unknown restack delta kinds should be rejected")
				return
			}
			require.NoError(t, err, "known restack delta kinds should be accepted")
		})
	}
}
