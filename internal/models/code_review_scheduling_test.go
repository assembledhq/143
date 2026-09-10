package models

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestCodeReviewSchedulingPresence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, raw string
		expected  CodeReviewSchedulingSettings
		invalid   bool
	}{
		{"absent", "null", CodeReviewSchedulingSettings{true, 60, 0}, false},
		{"historical empty", "{}", CodeReviewSchedulingSettings{true, 60, 0}, false},
		{"explicit disabled and zero", `{"automatic_re_review":false,"quiet_period_seconds":0,"minimum_interval_seconds":0}`, CodeReviewSchedulingSettings{false, 0, 0}, false},
		{"recommended", `{"quiet_period_seconds":300,"minimum_interval_seconds":900}`, CodeReviewSchedulingSettings{true, 300, 900}, false},
		{"negative", `{"quiet_period_seconds":-1}`, CodeReviewSchedulingSettings{true, -1, 0}, true},
		{"too long", `{"minimum_interval_seconds":86401}`, CodeReviewSchedulingSettings{true, 60, 86401}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var policy *CodeReviewSchedulingPolicy
			require.NoError(t, json.Unmarshal([]byte(tt.raw), &policy), "decode presence-aware settings")
			require.Equal(t, tt.expected, policy.Effective(), "effective settings retain explicit zero and false")
			if tt.invalid {
				require.Error(t, policy.Validate(), "invalid duration is rejected")
			} else {
				require.NoError(t, policy.Validate(), "valid settings are accepted")
			}
		})
	}
}
func TestCodeReviewSchedulingDeadline(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		change   int
		last     *time.Time
		expected int
	}{
		{"first review", 3, nil, 8}, {"cadence dominates", 3, &start, 15}, {"quiet dominates", 14, &start, 19},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			settings := CodeReviewSchedulingSettings{true, 300, 900}
			require.Equal(t, start.Add(time.Duration(tt.expected)*time.Minute), settings.EligibleAt(start.Add(time.Duration(tt.change)*time.Minute), tt.last), "deadline satisfies both quiet period and actual-start cadence")
		})
	}
}
func TestCodeReviewPolicySchedulingPatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, patch string
		expected    CodeReviewSchedulingSettings
		invalid     bool
	}{
		{"unrelated edit", `{"inline_comment_limit":3}`, CodeReviewSchedulingSettings{false, 300, 900}, false},
		{"partial nested edit", `{"scheduling_policy":{"quiet_period_seconds":0}}`, CodeReviewSchedulingSettings{false, 0, 900}, false},
		{"field reset", `{"scheduling_policy":{"quiet_period_seconds":null}}`, CodeReviewSchedulingSettings{false, 60, 900}, false},
		{"section reset", `{"scheduling_policy":null}`, CodeReviewSchedulingSettings{true, 60, 0}, false},
		{"unknown field", `{"scheduling_policy":{"quiet_seconds":100}}`, CodeReviewSchedulingSettings{}, true},
		{"fractional duration", `{"scheduling_policy":{"quiet_period_seconds":1.5}}`, CodeReviewSchedulingSettings{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := DefaultCodeReviewPolicyConfig()
			enabled, quiet, interval := false, 300, 900
			base.SchedulingPolicy = &CodeReviewSchedulingPolicy{&enabled, &quiet, &interval}
			updated, err := ApplyCodeReviewPolicyMergePatch(base, []byte(tt.patch))
			if tt.invalid {
				require.Error(t, err, "invalid patch fails without saving")
				return
			}
			require.NoError(t, err, "merge patch validates")
			require.Equal(t, tt.expected, updated.SchedulingPolicy.Effective(), "patch preserves unrelated fields and applies null defaults")
			require.Equal(t, CodeReviewSchedulingSettings{false, 300, 900}, base.SchedulingPolicy.Effective(), "patch does not mutate its source")
		})
	}
}
func TestCodeReviewSchedulingEnums(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		err   func() error
		valid bool
	}{
		{"queued disposition", func() error { return CodeReviewRequestQueued.Validate() }, true},
		{"joined disposition", func() error { return CodeReviewRequestJoined.Validate() }, true},
		{"reused disposition", func() error { return CodeReviewRequestReused.Validate() }, true},
		{"cancelled disposition", func() error { return CodeReviewRequestCancelled.Validate() }, true},
		{"unknown disposition", func() error { return CodeReviewRequestDisposition("other").Validate() }, false},
		{"waiting", func() error { return CodeReviewScheduleWaiting.Validate() }, true},
		{"unknown state", func() error { return CodeReviewScheduleState("other").Validate() }, false},
		{"quiet", func() error { return CodeReviewWaitQuiet.Validate() }, true},
		{"unknown reason", func() error { return CodeReviewWaitReason("other").Validate() }, false},
		{"review now", func() error { return CodeReviewReviewNow.Validate() }, true},
		{"force unavailable", func() error { return CodeReviewRequestMode("force_fresh").Validate() }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.valid {
				require.NoError(t, tt.err(), "supported enum value validates")
			} else {
				require.Error(t, tt.err(), "unsupported enum value is rejected")
			}
		})
	}
}
