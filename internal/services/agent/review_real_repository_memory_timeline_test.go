package agent_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const realReviewMemoryTimelineLimit = 64
const realReviewMemoryTimelineScope = "same sequential kernel sampling pass with the latest fully validated helper checkpoint observed asynchronously by the host; checkpoint age is host receipt age, not helper publication or kernel age; not an atomic snapshot, phase completion, causal allocation attribution, or proof of physical disk traffic"
const realReviewMemoryTimelineRetention = "whole-lifetime power-of-two sample-ordinal grid; retain first and latest samples; double stride and compact when over 64 points; coarser history can omit brief transitions and must not be treated as continuous coverage"

type realReviewMemoryTimelinePoint struct {
	SampleNumber                  int    `json:"sample_number"`
	HostElapsedUS                 int64  `json:"host_elapsed_us"`
	MemoryCurrentBytes            *int64 `json:"memory_current_bytes"`
	AnonBytes                     *int64 `json:"anon_bytes"`
	FileMappedBytes               *int64 `json:"file_mapped_bytes"`
	CheckpointInvocation          *int   `json:"checkpoint_invocation"`
	CheckpointSequence            *int64 `json:"checkpoint_sequence"`
	CheckpointHelperElapsedUS     *int64 `json:"checkpoint_helper_elapsed_us"`
	CheckpointCompletedHashes     *int64 `json:"checkpoint_completed_hashes"`
	CheckpointSuccessfulReadBytes *int64 `json:"checkpoint_successful_read_bytes"`
	CheckpointHostAgeUS           *int64 `json:"checkpoint_host_age_us"`
	ProfileInvalid                bool   `json:"profile_invalid"`
}

type realReviewMemoryTimeline struct {
	MaxPoints             int                             `json:"max_points"`
	SampleStride          int                             `json:"sample_stride"`
	NominalGridIntervalMS int64                           `json:"nominal_grid_interval_ms"`
	DiscardedSamples      int                             `json:"discarded_samples"`
	RetentionPolicy       string                          `json:"retention_policy"`
	ObservationScope      string                          `json:"observation_scope"`
	Points                []realReviewMemoryTimelinePoint `json:"points"`
}

type realReviewMemoryProgress struct {
	checkpoint *realReviewProfileCheckpoint
	invocation int
	observedAt time.Time
	invalid    bool
}

// Called under the monitor lock, after the existing profile observation. No
// additional kernel reads or goroutines are used for this diagnostic timeline.
func (m *realReviewMemoryMonitor) recordJointTimeline(sample realReviewMemorySample, progress realReviewMemoryProgress, observedAt time.Time) {
	if m.started.IsZero() {
		m.started = observedAt
	}
	point := realReviewMemoryTimelinePoint{SampleNumber: m.retained.Samples, HostElapsedUS: observedAt.Sub(m.started).Microseconds(),
		MemoryCurrentBytes: retainRealReviewMemoryMaximum(nil, sample.MemoryCurrentBytes), ProfileInvalid: progress.invalid}
	for _, field := range []struct {
		name   string
		target **int64
	}{{"anon", &point.AnonBytes}, {"file_mapped", &point.FileMappedBytes}} {
		if value, present := sample.MemoryStat[field.name]; present {
			*field.target = &value
		}
	}
	if progress.checkpoint != nil {
		checkpoint := progress.checkpoint
		point.CheckpointInvocation = &progress.invocation
		point.CheckpointSequence, point.CheckpointHelperElapsedUS = &checkpoint.Sequence, &checkpoint.ElapsedUS
		point.CheckpointCompletedHashes, point.CheckpointSuccessfulReadBytes = &checkpoint.HashCompleted, &checkpoint.HashCompletedBytes
		if !progress.observedAt.IsZero() && !observedAt.Before(progress.observedAt) {
			age := observedAt.Sub(progress.observedAt).Microseconds()
			point.CheckpointHostAgeUS = &age
		}
	}
	if m.retained.JointTimeline == nil {
		m.retained.JointTimeline = &realReviewMemoryTimeline{MaxPoints: realReviewMemoryTimelineLimit, SampleStride: 1,
			RetentionPolicy: realReviewMemoryTimelineRetention, ObservationScope: realReviewMemoryTimelineScope}
	}
	timeline := m.retained.JointTimeline
	// The previous latest sample is expendable only when it is off the grid.
	if n := len(timeline.Points); n > 0 && (timeline.Points[n-1].SampleNumber-1)%timeline.SampleStride != 0 {
		timeline.Points = timeline.Points[:n-1]
	}
	timeline.Points = append(timeline.Points, point)
	if len(timeline.Points) > realReviewMemoryTimelineLimit {
		timeline.SampleStride *= 2
		kept := timeline.Points[:0]
		for _, candidate := range timeline.Points {
			if (candidate.SampleNumber-1)%timeline.SampleStride == 0 || candidate.SampleNumber == point.SampleNumber {
				kept = append(kept, candidate)
			}
		}
		clear(timeline.Points[len(kept):])
		timeline.Points = kept
	}
	timeline.NominalGridIntervalMS = m.retained.IntervalMS * int64(timeline.SampleStride)
	timeline.DiscardedSamples = m.retained.Samples - len(timeline.Points)
}

func cloneRealReviewMemoryTimeline(timeline *realReviewMemoryTimeline) *realReviewMemoryTimeline {
	if timeline == nil {
		return nil
	}
	result := *timeline
	result.Points = make([]realReviewMemoryTimelinePoint, len(timeline.Points))
	for index, point := range timeline.Points {
		for _, field := range []**int64{&point.MemoryCurrentBytes, &point.AnonBytes, &point.FileMappedBytes, &point.CheckpointSequence,
			&point.CheckpointHelperElapsedUS, &point.CheckpointCompletedHashes, &point.CheckpointSuccessfulReadBytes, &point.CheckpointHostAgeUS} {
			*field = retainRealReviewMemoryMaximum(nil, *field)
		}
		if point.CheckpointInvocation != nil {
			invocation := *point.CheckpointInvocation
			point.CheckpointInvocation = &invocation
		}
		result.Points[index] = point
	}
	return &result
}

func realReviewTimelineInt64(value int64) *int64 { return &value }
func realReviewTimelineInt(value int) *int       { return &value }

func TestRealReviewDiagnosticTimelineExactPointAvailability(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		sample     realReviewMemorySample
		checkpoint bool
		expected   realReviewMemoryTimelinePoint
	}{
		{name: "absent metrics and checkpoint stay null", sample: realReviewMemorySample{UnavailableFields: []string{"memory.current", "memory.stat"}}, expected: realReviewMemoryTimelinePoint{SampleNumber: 1, HostElapsedUS: 3000000}},
		{name: "available zero metrics are distinct from absence", sample: realReviewMemorySample{MemoryCurrentBytes: realReviewTimelineInt64(0), MemoryStat: map[string]int64{"anon": 0, "file_mapped": 0}}, expected: realReviewMemoryTimelinePoint{SampleNumber: 1, HostElapsedUS: 3000000, MemoryCurrentBytes: realReviewTimelineInt64(0), AnonBytes: realReviewTimelineInt64(0), FileMappedBytes: realReviewTimelineInt64(0)}},
		{name: "validated checkpoint has host receipt age", sample: realReviewMemorySample{MemoryCurrentBytes: realReviewTimelineInt64(300), MemoryStat: map[string]int64{"anon": 100, "file_mapped": 150}}, checkpoint: true, expected: realReviewMemoryTimelinePoint{SampleNumber: 1, HostElapsedUS: 3000000, MemoryCurrentBytes: realReviewTimelineInt64(300), AnonBytes: realReviewTimelineInt64(100), FileMappedBytes: realReviewTimelineInt64(150), CheckpointInvocation: realReviewTimelineInt(1), CheckpointSequence: realReviewTimelineInt64(1), CheckpointHelperElapsedUS: realReviewTimelineInt64(100), CheckpointCompletedHashes: realReviewTimelineInt64(1), CheckpointSuccessfulReadBytes: realReviewTimelineInt64(1024), CheckpointHostAgeUS: realReviewTimelineInt64(2000000)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			start := time.Now()
			profile := &realReviewSourceProfile{now: func() time.Time { return start.Add(time.Second) }}
			if tt.checkpoint {
				profile.consume(checkpointProgressJSON(t, checkpointProgressFixture(), nil))
			}
			monitor := &realReviewMemoryMonitor{profile: profile, started: start, now: func() time.Time { return start.Add(3 * time.Second) }}
			monitor.record(tt.sample)
			expected := &realReviewMemoryTimeline{MaxPoints: 64, SampleStride: 1, RetentionPolicy: realReviewMemoryTimelineRetention, ObservationScope: realReviewMemoryTimelineScope, Points: []realReviewMemoryTimelinePoint{tt.expected}}
			require.Equal(t, expected, monitor.retained.JointTimeline, "each diagnostic point must preserve exact nullable metrics and the validated checkpoint's host age")
			encoded, err := json.Marshal(monitor.retained.JointTimeline)
			require.NoError(t, err, "fixed allowlisted joint points should serialize")
			if !tt.checkpoint {
				require.Contains(t, string(encoded), `"checkpoint_sequence":null`, "missing progress must remain explicitly null")
			}
		})
	}
}

func TestRealReviewDiagnosticTimelineWholeLifetimeRetention(t *testing.T) {
	t.Parallel()
	tests := []struct {
		samples, stride int
	}{
		{samples: 64, stride: 1},
		{samples: 65, stride: 2},
		{samples: 128, stride: 4},
		{samples: 5402, stride: 128},
	}
	for _, tt := range tests {
		t.Run("samples-"+strconv.Itoa(tt.samples), func(t *testing.T) {
			t.Parallel()
			start := time.Now()
			observed := start
			monitor := &realReviewMemoryMonitor{profile: &realReviewSourceProfile{}, started: start, now: func() time.Time { return observed }, retained: realReviewMemoryTelemetry{IntervalMS: 500}}
			for sample := 1; sample <= tt.samples; sample++ {
				observed = start.Add(time.Duration(sample-1) * 500 * time.Millisecond)
				monitor.record(realReviewMemorySample{MemoryCurrentBytes: realReviewTimelineInt64(int64(sample))})
				require.LessOrEqual(t, len(monitor.retained.JointTimeline.Points), 64, "retained points must never exceed the fixed diagnostic cap")
			}
			expected := []realReviewMemoryTimelinePoint{}
			for sample := 1; sample <= tt.samples; sample += tt.stride {
				expected = append(expected, realReviewMemoryTimelinePoint{SampleNumber: sample, HostElapsedUS: int64(sample-1) * 500000, MemoryCurrentBytes: realReviewTimelineInt64(int64(sample))})
			}
			if expected[len(expected)-1].SampleNumber != tt.samples {
				expected = append(expected, realReviewMemoryTimelinePoint{SampleNumber: tt.samples, HostElapsedUS: int64(tt.samples-1) * 500000, MemoryCurrentBytes: realReviewTimelineInt64(int64(tt.samples))})
			}
			timeline := monitor.retained.JointTimeline
			require.Equal(t, expected, timeline.Points, "deterministic thinning must cover the entire lifetime in order and preserve first and latest")
			require.Equal(t, tt.stride, timeline.SampleStride, "overflow must double the sample-ordinal stride")
			require.Equal(t, int64(tt.stride)*500, timeline.NominalGridIntervalMS, "metadata must disclose the coarser nominal historical grid")
			require.Equal(t, tt.samples-len(expected), timeline.DiscardedSamples, "metadata must disclose every sample omitted by retention")
		})
	}
}

func TestRealReviewDiagnosticTimelineCheckpointResetsAndInvalidity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		invalid bool
	}{{name: "interleaved invocation updates preserve newest helper"}, {name: "sticky invalidity never advances validated progress", invalid: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			start := time.Now()
			observed := start
			profile := &realReviewSourceProfile{now: func() time.Time { return observed }}
			monitor := &realReviewMemoryMonitor{profile: profile, started: start, now: func() time.Time { return observed }}
			first, second := &realReviewProfileStream{}, &realReviewProfileStream{}
			checkpoint := checkpointProgressFixture()
			profile.consumeStream(checkpointProgressJSON(t, checkpoint, nil), first)
			monitor.record(realReviewMemorySample{})
			observed = start.Add(time.Second)
			reset := realReviewProfileCheckpoint{Sequence: 1, ElapsedUS: 1}
			profile.consumeStream(checkpointProgressJSON(t, reset, nil), second)
			monitor.record(realReviewMemorySample{})
			observed = start.Add(2 * time.Second)
			checkpoint.Sequence, checkpoint.ElapsedUS = 2, 200
			profile.consumeStream(checkpointProgressJSON(t, checkpoint, nil), first)
			if tt.invalid {
				profile.consume([]byte(`{"version":1,"op":4,"phase":0,"category":0}`))
			}
			observed = start.Add(3 * time.Second)
			reset.Sequence, reset.ElapsedUS = 2, 20
			profile.consumeStream(checkpointProgressJSON(t, reset, nil), second)
			monitor.record(realReviewMemorySample{})
			last := monitor.retained.JointTimeline.Points[2]
			expected := realReviewMemoryTimelinePoint{SampleNumber: 3, HostElapsedUS: 3000000, CheckpointInvocation: realReviewTimelineInt(2), CheckpointSequence: realReviewTimelineInt64(2), CheckpointHelperElapsedUS: realReviewTimelineInt64(20), CheckpointCompletedHashes: realReviewTimelineInt64(0), CheckpointSuccessfulReadBytes: realReviewTimelineInt64(0), CheckpointHostAgeUS: realReviewTimelineInt64(0)}
			if tt.invalid {
				expected.CheckpointSequence, expected.CheckpointHelperElapsedUS, expected.CheckpointHostAgeUS, expected.ProfileInvalid = realReviewTimelineInt64(1), realReviewTimelineInt64(1), realReviewTimelineInt64(2000000), true
			}
			require.Equal(t, expected, last, "valid resets and interleaving must preserve invocation identity while invalid records freeze progress and age")
			require.Equal(t, tt.invalid, profile.result().Invalid, "timeline observation must not revive invalid profile state")
		})
	}
}

func TestRealReviewDiagnosticTimelineDoesNotCompletePhases(t *testing.T) {
	t.Parallel()
	profile := &realReviewSourceProfile{}
	profile.consume([]byte(`{"version":1,"op":1,"phase":8,"category":1}`))
	profile.consume(checkpointProgressJSON(t, checkpointProgressFixture(), nil))
	monitor := &realReviewMemoryMonitor{profile: profile}
	monitor.record(realReviewMemorySample{})
	result := profile.result()
	require.True(t, result.Invalid, "partial diagnostic progress must not complete an unclosed phase")
	require.Equal(t, "incomplete_phase_exits", result.InvalidReason, "the original final completeness check must remain authoritative")
	require.NotNil(t, monitor.retained.JointTimeline.Points[0].CheckpointSequence, "validated partial progress should remain observable despite an incomplete final phase")
	require.False(t, monitor.retained.JointTimeline.Points[0].ProfileInvalid, "point validity describes parser state at sampling, while final completeness remains separately checked")
}

func TestRealReviewDiagnosticTimelineFinalReadAndIsolation(t *testing.T) {
	t.Parallel()
	initial := realReviewContractMemorySample(300, realReviewContractMemoryStat())
	var calls atomic.Int64
	profile := &realReviewSourceProfile{}
	profile.consume(checkpointProgressJSON(t, checkpointProgressFixture(), nil))
	monitor := newRealReviewMemoryMonitor(strings.Repeat("a", 64), func() realReviewMemorySample {
		if calls.Add(1) == 1 {
			return initial
		}
		return realReviewMemorySample{UnavailableFields: []string{"memory.events", "memory.peak", "memory.current", "memory.stat"}}
	}, time.Hour, time.Minute, profile)
	t.Cleanup(func() { monitor.stop() })
	original := monitor.stop()
	require.Equal(t, 2, len(original.JointTimeline.Points), "the joined final unavailable read must be retained without discarding prior evidence")
	require.Equal(t, int64(300), *original.JointTimeline.Points[0].MemoryCurrentBytes, "the earlier available point must survive cgroup disappearance")
	require.Nil(t, original.JointTimeline.Points[1].MemoryCurrentBytes, "final unavailable metrics must not borrow earlier kernel values")
	require.Equal(t, int64(2), calls.Load(), "timeline capture must not add kernel reads to initial and joined final sampling")
	*initial.MemoryCurrentBytes, initial.MemoryStat["anon"] = 999, 999
	changed := monitor.stop()
	for _, point := range changed.JointTimeline.Points {
		for _, value := range []*int64{point.MemoryCurrentBytes, point.AnonBytes, point.FileMappedBytes, point.CheckpointSequence, point.CheckpointHelperElapsedUS, point.CheckpointCompletedHashes, point.CheckpointSuccessfulReadBytes, point.CheckpointHostAgeUS} {
			if value != nil {
				*value = 999
			}
		}
		if point.CheckpointInvocation != nil {
			*point.CheckpointInvocation = 999
		}
	}
	changed.JointTimeline.SampleStride = 999
	changed.JointTimeline.Points[0].SampleNumber = 999
	require.Equal(t, original, monitor.stop(), "input samples and every returned point pointer must remain isolated from retained observations")
	*original.JointTimeline.Points[0].CheckpointSequence = 777
	require.Equal(t, int64(1), *monitor.stop().JointTimeline.Points[1].CheckpointSequence, "points for the same checkpoint must not alias each other's numeric fields")
}

func TestRealReviewDiagnosticTimelineConcurrentSamplingAndSnapshots(t *testing.T) {
	t.Parallel()
	profile := &realReviewSourceProfile{}
	profile.consume(checkpointProgressJSON(t, realReviewProfileCheckpoint{Sequence: 1, ElapsedUS: 1}, nil))
	monitor := newRealReviewMemoryMonitor(strings.Repeat("a", 64), func() realReviewMemorySample {
		return realReviewContractMemorySample(300, realReviewContractMemoryStat())
	}, time.Millisecond, time.Minute, profile)
	t.Cleanup(func() { monitor.stop() })
	var writers sync.WaitGroup
	writers.Add(1)
	go func() {
		defer writers.Done()
		for sequence := int64(2); sequence <= 100; sequence++ {
			checkpoint := realReviewProfileCheckpoint{Sequence: sequence, ElapsedUS: sequence}
			raw, err := json.Marshal(realReviewProfileRecord{RealReviewProfileCheckpoint: &checkpoint, Version: 1, Op: 4})
			if err != nil {
				panic(err)
			}
			profile.consume(raw)
		}
	}()
	for range 8 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			result := monitor.stop()
			for _, point := range result.JointTimeline.Points {
				if point.CheckpointSequence != nil {
					*point.CheckpointSequence = 999
				}
			}
		}()
	}
	writers.Wait()
	actual := monitor.stop()
	require.LessOrEqual(t, len(actual.JointTimeline.Points), 64, "concurrent ingestion and private snapshots must retain the fixed bound")
	for _, point := range actual.JointTimeline.Points {
		if point.CheckpointSequence != nil {
			require.LessOrEqual(t, *point.CheckpointSequence, int64(100), "snapshot mutation must never change monitor-owned validated progress")
		}
	}
	require.False(t, profile.result().Invalid, "concurrent checkpoint parsing must remain valid while memory results are read")
}

func TestRealReviewNormalMemoryTimelineAbsent(t *testing.T) {
	t.Parallel()
	monitor := newRealReviewMemoryMonitor(strings.Repeat("a", 64), func() realReviewMemorySample { return realReviewMemorySample{} }, time.Hour, time.Minute)
	t.Cleanup(func() { monitor.stop() })
	actual := monitor.stop()
	require.Nil(t, actual.JointTimeline, "normal acceptance must not retain diagnostic timeline points")
	encoded, err := json.Marshal(actual)
	require.NoError(t, err, "normal memory aggregates must serialize")
	require.NotContains(t, string(encoded), "diagnostic_joint_memory_hash_timeline", "normal result artifacts must omit the diagnostic timeline entirely")
}

func TestRealReviewDiagnosticTimelineSameStreamInvocationReset(t *testing.T) {
	t.Parallel()
	start := time.Now()
	observed := start
	profile := &realReviewSourceProfile{now: func() time.Time { return observed }}
	monitor := &realReviewMemoryMonitor{profile: profile, started: start, now: func() time.Time { return observed }}
	profile.consume(checkpointProgressJSON(t, checkpointProgressFixture(), nil))
	monitor.record(realReviewMemorySample{})
	observed = start.Add(time.Second)
	profile.consume(checkpointProgressJSON(t, realReviewProfileCheckpoint{Sequence: 1, ElapsedUS: 1}, nil))
	observed = start.Add(3 * time.Second)
	monitor.record(realReviewMemorySample{})
	expected := realReviewMemoryTimelinePoint{SampleNumber: 2, HostElapsedUS: 3000000, CheckpointInvocation: realReviewTimelineInt(2), CheckpointSequence: realReviewTimelineInt64(1), CheckpointHelperElapsedUS: realReviewTimelineInt64(1), CheckpointCompletedHashes: realReviewTimelineInt64(0), CheckpointSuccessfulReadBytes: realReviewTimelineInt64(0), CheckpointHostAgeUS: realReviewTimelineInt64(2000000)}
	require.Equal(t, expected, monitor.retained.JointTimeline.Points[1], "sequence one in the same launcher stream must reset helper totals and host checkpoint age under a new invocation ordinal")
	require.False(t, profile.result().Invalid, "a fully validated helper reset must remain a valid profile")
}
