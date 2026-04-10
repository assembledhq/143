package models

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

func TestPMPlanStatusValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   PMPlanStatus
		wantErr bool
	}{
		{name: "valid executing", value: PMPlanStatusExecuting},
		{name: "valid completed", value: PMPlanStatusCompleted},
		{name: "valid failed", value: PMPlanStatusFailed},
		{name: "invalid empty", value: PMPlanStatus(""), wantErr: true},
		{name: "invalid unknown", value: PMPlanStatus("unknown"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.wantErr {
				require.Error(t, err, "Validate should return error for invalid status")
				return
			}
			require.NoError(t, err, "Validate should succeed for valid status")
		})
	}
}

func TestPMTaskStatusValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   PMTaskStatus
		wantErr bool
	}{
		{name: "valid pending", value: PMTaskStatusPending},
		{name: "valid delegated", value: PMTaskStatusDelegated},
		{name: "valid skipped capacity", value: PMTaskStatusSkippedCapacity},
		{name: "invalid empty", value: PMTaskStatus(""), wantErr: true},
		{name: "invalid unknown", value: PMTaskStatus("blocked"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.wantErr {
				require.Error(t, err, "Validate should return error for invalid status")
				return
			}
			require.NoError(t, err, "Validate should succeed for valid status")
		})
	}
}

func TestPMTaskComplexityValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   PMTaskComplexity
		wantErr bool
	}{
		{name: "valid trivial", value: PMTaskComplexityTrivial},
		{name: "valid simple", value: PMTaskComplexitySimple},
		{name: "valid moderate", value: PMTaskComplexityModerate},
		{name: "valid complex", value: PMTaskComplexityComplex},
		{name: "invalid empty", value: PMTaskComplexity(""), wantErr: true},
		{name: "invalid unknown", value: PMTaskComplexity("very_complex"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.wantErr {
				require.Error(t, err, "Validate should return error for invalid complexity")
				return
			}
			require.NoError(t, err, "Validate should succeed for valid complexity")
		})
	}
}

func TestPMTaskConfidenceValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   PMTaskConfidence
		wantErr bool
	}{
		{name: "valid high", value: PMTaskConfidenceHigh},
		{name: "valid medium", value: PMTaskConfidenceMedium},
		{name: "valid low", value: PMTaskConfidenceLow},
		{name: "invalid empty", value: PMTaskConfidence(""), wantErr: true},
		{name: "invalid unknown", value: PMTaskConfidence("sure"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.wantErr {
				require.Error(t, err, "Validate should return error for invalid confidence")
				return
			}
			require.NoError(t, err, "Validate should succeed for valid confidence")
		})
	}
}

func TestPMSkipReasonValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   PMSkipReason
		wantErr bool
	}{
		{name: "valid duplicate", value: PMSkipReasonDuplicate},
		{name: "valid needs human decision", value: PMSkipReasonNeedsHumanDecision},
		{name: "valid too complex", value: PMSkipReasonTooComplex},
		{name: "valid misaligned", value: PMSkipReasonMisaligned},
		{name: "valid avoid area", value: PMSkipReasonInAvoidArea},
		{name: "valid already in flight", value: PMSkipReasonAlreadyInFlight},
		{name: "invalid empty", value: PMSkipReason(""), wantErr: true},
		{name: "invalid unknown", value: PMSkipReason("other"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.wantErr {
				require.Error(t, err, "Validate should return error for invalid skip reason")
				return
			}
			require.NoError(t, err, "Validate should succeed for valid skip reason")
		})
	}
}

func TestPMDecisionTypeValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   PMDecisionType
		wantErr bool
	}{
		{name: "valid delegate", value: PMDecisionTypeDelegate},
		{name: "valid skip", value: PMDecisionTypeSkip},
		{name: "valid cluster", value: PMDecisionTypeCluster},
		{name: "invalid empty", value: PMDecisionType(""), wantErr: true},
		{name: "invalid unknown", value: PMDecisionType("defer"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.wantErr {
				require.Error(t, err, "Validate should return error for invalid decision type")
				return
			}
			require.NoError(t, err, "Validate should succeed for valid decision type")
		})
	}
}

func TestPMDecisionOutcomeValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   PMDecisionOutcome
		wantErr bool
	}{
		{name: "valid succeeded", value: PMDecisionOutcomeSucceeded},
		{name: "valid failed", value: PMDecisionOutcomeFailed},
		{name: "valid still open", value: PMDecisionOutcomeStillOpen},
		{name: "invalid empty", value: PMDecisionOutcome(""), wantErr: true},
		{name: "invalid unknown", value: PMDecisionOutcome("pending"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.wantErr {
				require.Error(t, err, "Validate should return error for invalid outcome")
				return
			}
			require.NoError(t, err, "Validate should succeed for valid outcome")
		})
	}
}

func TestPMPlanStatus_TextValueAndScanText(t *testing.T) {
	t.Parallel()

	status := PMPlanStatusExecuting
	tv, err := status.TextValue()
	require.NoError(t, err)
	require.True(t, tv.Valid)
	require.Equal(t, "executing", tv.String)

	var scanned PMPlanStatus
	require.NoError(t, scanned.ScanText(pgtype.Text{String: "completed", Valid: true}))
	require.Equal(t, PMPlanStatusCompleted, scanned)

	require.NoError(t, scanned.ScanText(pgtype.Text{Valid: false}))
	require.Equal(t, PMPlanStatus(""), scanned)
}

func TestPMDecisionType_TextValueAndScanText(t *testing.T) {
	t.Parallel()

	dt := PMDecisionTypeDelegate
	tv, err := dt.TextValue()
	require.NoError(t, err)
	require.True(t, tv.Valid)
	require.Equal(t, "delegate", tv.String)

	var scanned PMDecisionType
	require.NoError(t, scanned.ScanText(pgtype.Text{String: "skip", Valid: true}))
	require.Equal(t, PMDecisionTypeSkip, scanned)

	require.NoError(t, scanned.ScanText(pgtype.Text{Valid: false}))
	require.Equal(t, PMDecisionType(""), scanned)
}

func TestPMDecisionOutcome_TextValueAndScanText(t *testing.T) {
	t.Parallel()

	outcome := PMDecisionOutcomeSucceeded
	tv, err := outcome.TextValue()
	require.NoError(t, err)
	require.True(t, tv.Valid)
	require.Equal(t, "succeeded", tv.String)

	// Empty outcome returns invalid text.
	empty := PMDecisionOutcome("")
	tv, err = empty.TextValue()
	require.NoError(t, err)
	require.False(t, tv.Valid)

	var scanned PMDecisionOutcome
	require.NoError(t, scanned.ScanText(pgtype.Text{String: "failed", Valid: true}))
	require.Equal(t, PMDecisionOutcomeFailed, scanned)

	require.NoError(t, scanned.ScanText(pgtype.Text{Valid: false}))
	require.Equal(t, PMDecisionOutcome(""), scanned)
}

func TestPMTrigger_TextValueAndScanText(t *testing.T) {
	t.Parallel()

	trigger := PMTriggerCron
	tv, err := trigger.TextValue()
	require.NoError(t, err)
	require.True(t, tv.Valid)
	require.Equal(t, "cron", tv.String)

	var scanned PMTrigger
	require.NoError(t, scanned.ScanText(pgtype.Text{String: "manual", Valid: true}))
	require.Equal(t, PMTriggerManual, scanned)

	require.NoError(t, scanned.ScanText(pgtype.Text{Valid: false}))
	require.Equal(t, PMTrigger(""), scanned)
}

func TestPMTriggerValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   PMTrigger
		wantErr bool
	}{
		{name: "valid cron", value: PMTriggerCron},
		{name: "valid manual", value: PMTriggerManual},
		{name: "invalid empty", value: PMTrigger(""), wantErr: true},
		{name: "invalid unknown", value: PMTrigger("api"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.wantErr {
				require.Error(t, err, "Validate should return error for invalid trigger")
				return
			}
			require.NoError(t, err, "Validate should succeed for valid trigger")
		})
	}
}
