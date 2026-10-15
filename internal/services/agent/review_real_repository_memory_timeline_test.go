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
const realReviewMemoryTimelineGridLimit = 40
const realReviewMemoryTimelineRecentLimit = 24
const realReviewMemoryTimelineScope = "same sequential kernel sampling pass with the latest fully validated helper checkpoint observed asynchronously by the host; checkpoint age is host receipt age, not helper publication or kernel age; not an atomic snapshot, phase completion, causal allocation attribution, or proof of physical disk traffic; memory.stat fields overlap and must not be summed"
const realReviewMemoryTimelineRetention = "union of at most 40 whole-lifetime power-of-two sample-ordinal grid points and the most recent 24 native-interval samples; double grid stride when its point count exceeds 40; retain first and latest samples once in chronological order; coarser grid history can omit brief transitions and must not be treated as continuous coverage"

type realReviewMemoryTimelinePoint struct {
	SampleNumber                  int    `json:"sample_number"`
	HostElapsedUS                 int64  `json:"host_elapsed_us"`
	MemoryCurrentBytes            *int64 `json:"memory_current_bytes"`
	AnonBytes                     *int64 `json:"anon_bytes"`
	FileBytes                     *int64 `json:"file_bytes"`
	ShmemBytes                    *int64 `json:"shmem_bytes"`
	FileMappedBytes               *int64 `json:"file_mapped_bytes"`
	FileDirtyBytes                *int64 `json:"file_dirty_bytes"`
	FileWritebackBytes            *int64 `json:"file_writeback_bytes"`
	SlabBytes                     *int64 `json:"slab_bytes"`
	KernelBytes                   *int64 `json:"kernel_bytes"`
	CheckpointInvocation          *int   `json:"checkpoint_invocation"`
	CheckpointSequence            *int64 `json:"checkpoint_sequence"`
	CheckpointHelperElapsedUS     *int64 `json:"checkpoint_helper_elapsed_us"`
	CheckpointCompletedHashes     *int64 `json:"checkpoint_completed_hashes"`
	CheckpointSuccessfulReadBytes *int64 `json:"checkpoint_successful_read_bytes"`
	CheckpointHostAgeUS           *int64 `json:"checkpoint_host_age_us"`
	ProfileInvalid                bool   `json:"profile_invalid"`
}

type realReviewMemoryTimeline struct {
	MaxPoints               int                             `json:"max_points"`
	GridPointLimit          int                             `json:"grid_point_limit"`
	RecentPointLimit        int                             `json:"recent_point_limit"`
	NominalNativeIntervalMS int64                           `json:"nominal_native_interval_ms"`
	SampleStride            int                             `json:"sample_stride"`
	NominalGridIntervalMS   int64                           `json:"nominal_grid_interval_ms"`
	DiscardedSamples        int                             `json:"discarded_samples"`
	RetentionPolicy         string                          `json:"retention_policy"`
	ObservationScope        string                          `json:"observation_scope"`
	Points                  []realReviewMemoryTimelinePoint `json:"points"`
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
	}{{"anon", &point.AnonBytes}, {"file", &point.FileBytes}, {"shmem", &point.ShmemBytes}, {"file_mapped", &point.FileMappedBytes},
		{"file_dirty", &point.FileDirtyBytes}, {"file_writeback", &point.FileWritebackBytes}, {"slab", &point.SlabBytes}, {"kernel", &point.KernelBytes}} {
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
		m.retained.JointTimeline = &realReviewMemoryTimeline{MaxPoints: realReviewMemoryTimelineLimit, GridPointLimit: realReviewMemoryTimelineGridLimit, RecentPointLimit: realReviewMemoryTimelineRecentLimit,
			NominalNativeIntervalMS: m.retained.IntervalMS, SampleStride: 1,
			RetentionPolicy: realReviewMemoryTimelineRetention, ObservationScope: realReviewMemoryTimelineScope}
	}
	timeline := m.retained.JointTimeline
	// Every old grid point survives until a stride doubling; the recent tail
	// preserves short transitions immediately before completion or failure.
	timeline.Points = append(timeline.Points, point)
	gridPoints := 0
	for _, candidate := range timeline.Points {
		if (candidate.SampleNumber-1)%timeline.SampleStride == 0 {
			gridPoints++
		}
	}
	if gridPoints > realReviewMemoryTimelineGridLimit {
		timeline.SampleStride *= 2
	}
	kept := timeline.Points[:0]
	for _, candidate := range timeline.Points {
		if (candidate.SampleNumber-1)%timeline.SampleStride == 0 || candidate.SampleNumber > point.SampleNumber-realReviewMemoryTimelineRecentLimit {
			kept = append(kept, candidate)
		}
	}
	clear(timeline.Points[len(kept):])
	timeline.Points = kept
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
		for _, field := range []**int64{&point.MemoryCurrentBytes, &point.AnonBytes, &point.FileBytes, &point.ShmemBytes, &point.FileMappedBytes, &point.FileDirtyBytes, &point.FileWritebackBytes, &point.SlabBytes, &point.KernelBytes, &point.CheckpointSequence,
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
		{name: "available zero metrics are distinct from absence", sample: realReviewMemorySample{MemoryCurrentBytes: realReviewTimelineInt64(0), MemoryStat: map[string]int64{"anon": 0, "file": 0, "shmem": 0, "file_mapped": 0, "file_dirty": 0, "file_writeback": 0, "slab": 0, "kernel": 0}}, expected: realReviewMemoryTimelinePoint{SampleNumber: 1, HostElapsedUS: 3000000, MemoryCurrentBytes: realReviewTimelineInt64(0), AnonBytes: realReviewTimelineInt64(0), FileBytes: realReviewTimelineInt64(0), ShmemBytes: realReviewTimelineInt64(0), FileMappedBytes: realReviewTimelineInt64(0), FileDirtyBytes: realReviewTimelineInt64(0), FileWritebackBytes: realReviewTimelineInt64(0), SlabBytes: realReviewTimelineInt64(0), KernelBytes: realReviewTimelineInt64(0)}},
		{name: "partial metrics do not borrow absent values", sample: realReviewMemorySample{MemoryStat: map[string]int64{"anon": 0, "shmem": 15, "file_writeback": 0}}, expected: realReviewMemoryTimelinePoint{SampleNumber: 1, HostElapsedUS: 3000000, AnonBytes: realReviewTimelineInt64(0), ShmemBytes: realReviewTimelineInt64(15), FileWritebackBytes: realReviewTimelineInt64(0)}},
		{name: "validated checkpoint has host receipt age", sample: realReviewMemorySample{MemoryCurrentBytes: realReviewTimelineInt64(300), MemoryStat: realReviewContractMemoryStat()}, checkpoint: true, expected: realReviewMemoryTimelinePoint{SampleNumber: 1, HostElapsedUS: 3000000, MemoryCurrentBytes: realReviewTimelineInt64(300), AnonBytes: realReviewTimelineInt64(10), FileBytes: realReviewTimelineInt64(20), ShmemBytes: realReviewTimelineInt64(15), FileMappedBytes: realReviewTimelineInt64(4), FileDirtyBytes: realReviewTimelineInt64(3), FileWritebackBytes: realReviewTimelineInt64(0), SlabBytes: realReviewTimelineInt64(2), KernelBytes: realReviewTimelineInt64(5), CheckpointInvocation: realReviewTimelineInt(1), CheckpointSequence: realReviewTimelineInt64(1), CheckpointHelperElapsedUS: realReviewTimelineInt64(100), CheckpointCompletedHashes: realReviewTimelineInt64(1), CheckpointSuccessfulReadBytes: realReviewTimelineInt64(1024), CheckpointHostAgeUS: realReviewTimelineInt64(2000000)}},
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
			expected := &realReviewMemoryTimeline{MaxPoints: 64, GridPointLimit: 40, RecentPointLimit: 24, SampleStride: 1, RetentionPolicy: realReviewMemoryTimelineRetention, ObservationScope: realReviewMemoryTimelineScope, Points: []realReviewMemoryTimelinePoint{tt.expected}}
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
		{samples: 64, stride: 2},
		{samples: 65, stride: 2},
		{samples: 128, stride: 4},
		{samples: 939, stride: 32},
		{samples: 5402, stride: 256},
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
				timeline := monitor.retained.JointTimeline
				require.LessOrEqual(t, len(timeline.Points), 64, "retained points must never exceed the fixed diagnostic cap")
				ordinals, recent := []int{}, []int{}
				gridCount := 0
				for _, point := range timeline.Points {
					ordinals = append(ordinals, point.SampleNumber)
					if (point.SampleNumber-1)%timeline.SampleStride == 0 {
						gridCount++
					}
					if point.SampleNumber > sample-24 {
						recent = append(recent, point.SampleNumber)
					}
				}
				expectedOrdinals, expectedRecent := []int{}, []int{}
				for ordinal := 1; ordinal <= sample; ordinal++ {
					if (ordinal-1)%timeline.SampleStride == 0 || ordinal > sample-24 {
						expectedOrdinals = append(expectedOrdinals, ordinal)
					}
					if ordinal > sample-24 {
						expectedRecent = append(expectedRecent, ordinal)
					}
				}
				require.Equal(t, expectedOrdinals, ordinals, "every retained union must stay sorted, deduplicated, and contain every lifetime grid and recent sample")
				require.Equal(t, expectedRecent, recent, "the latest 24 available samples must retain native-interval resolution after every observation")
				require.LessOrEqual(t, gridCount, 40, "the whole-lifetime grid must remain within its reserved point budget")
			}
			expected := []realReviewMemoryTimelinePoint{}
			for sample := 1; sample <= tt.samples; sample++ {
				if (sample-1)%tt.stride == 0 || sample > tt.samples-24 {
					expected = append(expected, realReviewMemoryTimelinePoint{SampleNumber: sample, HostElapsedUS: int64(sample-1) * 500000, MemoryCurrentBytes: realReviewTimelineInt64(int64(sample))})
				}
			}
			timeline := monitor.retained.JointTimeline
			require.Equal(t, expected, timeline.Points, "deterministic retention must cover the lifetime grid and full native-resolution recent tail exactly once")
			require.Equal(t, tt.stride, timeline.SampleStride, "whole-lifetime grid overflow must double its sample-ordinal stride")
			require.Equal(t, 40, timeline.GridPointLimit, "metadata must state the whole-lifetime grid point budget")
			require.Equal(t, 24, timeline.RecentPointLimit, "metadata must state the native-resolution recent sample budget")
			require.Equal(t, int64(500), timeline.NominalNativeIntervalMS, "metadata must retain the native sampling interval independently of the grid")
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
	*initial.MemoryCurrentBytes = 999
	for name := range initial.MemoryStat {
		initial.MemoryStat[name] = 999
	}
	changed := monitor.stop()
	for _, point := range changed.JointTimeline.Points {
		for _, value := range []*int64{point.MemoryCurrentBytes, point.AnonBytes, point.FileBytes, point.ShmemBytes, point.FileMappedBytes, point.FileDirtyBytes, point.FileWritebackBytes, point.SlabBytes, point.KernelBytes, point.CheckpointSequence, point.CheckpointHelperElapsedUS, point.CheckpointCompletedHashes, point.CheckpointSuccessfulReadBytes, point.CheckpointHostAgeUS} {
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

func TestRealReviewDiagnosticTimelineAvailablePointIsolation(t *testing.T) {
	t.Parallel()
	start := time.Now()
	observed := start
	monitor := &realReviewMemoryMonitor{profile: &realReviewSourceProfile{}, started: start, now: func() time.Time { return observed }}
	sample := realReviewContractMemorySample(300, realReviewContractMemoryStat())
	monitor.record(sample)
	observed = start.Add(500 * time.Millisecond)
	monitor.record(sample)
	expectedFirst := realReviewMemoryTimelinePoint{SampleNumber: 1, HostElapsedUS: 0, MemoryCurrentBytes: realReviewTimelineInt64(300),
		AnonBytes: realReviewTimelineInt64(10), FileBytes: realReviewTimelineInt64(20), ShmemBytes: realReviewTimelineInt64(15), FileMappedBytes: realReviewTimelineInt64(4),
		FileDirtyBytes: realReviewTimelineInt64(3), FileWritebackBytes: realReviewTimelineInt64(0), SlabBytes: realReviewTimelineInt64(2), KernelBytes: realReviewTimelineInt64(5)}
	expectedSecond := expectedFirst
	expectedSecond.SampleNumber, expectedSecond.HostElapsedUS = 2, 500000
	require.Equal(t, []realReviewMemoryTimelinePoint{expectedFirst, expectedSecond}, monitor.retained.JointTimeline.Points, "every allowlisted metric must retain exact per-sample values")
	changed := cloneRealReviewMemoryTimeline(monitor.retained.JointTimeline)
	first := &changed.Points[0]
	for _, value := range []*int64{first.MemoryCurrentBytes, first.AnonBytes, first.FileBytes, first.ShmemBytes, first.FileMappedBytes, first.FileDirtyBytes, first.FileWritebackBytes, first.SlabBytes, first.KernelBytes} {
		*value = 999
	}
	require.Equal(t, expectedSecond, changed.Points[1], "available points must not share numeric pointers even when both read the same sample map")
	require.Equal(t, []realReviewMemoryTimelinePoint{expectedFirst, expectedSecond}, monitor.retained.JointTimeline.Points, "cloned point mutation must not alter retained timeline fields")
}
