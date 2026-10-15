package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func checkpointProgressFixture() realReviewProfileCheckpoint {
	return realReviewProfileCheckpoint{
		Sequence: 1, ElapsedUS: 100, InventoryUS: 60, InventoryResidualUS: 20,
		HashUS: 40, ReceiptUS: 100, ReceiptOutsideInventoryUS: 40,
		InventoryYielded: 3, HashStarted: 3, HashCompleted: 1,
		ReceiptStarted: 2, ReceiptCompleted: 1,
		OpenInventory: 2, ActiveInventory: 2, ActiveHash: 2, ActiveReceipt: 1,
	}
}

func checkpointProgressJSON(t *testing.T, checkpoint realReviewProfileCheckpoint, change func(map[string]any)) []byte {
	t.Helper()
	raw, err := json.Marshal(realReviewProfileRecord{RealReviewProfileCheckpoint: &checkpoint, Version: 1, Op: 4})
	require.NoError(t, err, "the complete checkpoint fixture should encode")
	if change == nil {
		return raw
	}
	var fields map[string]any
	require.NoError(t, json.Unmarshal(raw, &fields), "checkpoint fields should be available for one contract mutation")
	change(fields)
	raw, err = json.Marshal(fields)
	require.NoError(t, err, "the mutated checkpoint fixture should encode")
	return raw
}

func TestRealReviewProfileCheckpointRetainsPartialProgress(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		checkpoint func() realReviewProfileCheckpoint
	}{
		{name: "operations remain in flight", checkpoint: checkpointProgressFixture},
		{name: "one microsecond rounding residue is allowed", checkpoint: func() realReviewProfileCheckpoint {
			checkpoint := checkpointProgressFixture()
			checkpoint.InventoryUS++
			return checkpoint
		}},
		{name: "operation counts may complete before the phase exits", checkpoint: func() realReviewProfileCheckpoint {
			checkpoint := checkpointProgressFixture()
			checkpoint.HashCompleted, checkpoint.HashFailed = 3, 1
			checkpoint.ReceiptCompleted = 2
			checkpoint.OpenInventory, checkpoint.ActiveInventory, checkpoint.ActiveHash, checkpoint.ActiveReceipt = 0, 0, 0, 0
			return checkpoint
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			profile := &realReviewSourceProfile{}
			profile.consume([]byte(`{"version":1,"op":1,"phase":8,"category":1}`))
			profile.consume([]byte(`{"version":1,"op":1,"phase":9,"category":1}`))
			checkpoint := tt.checkpoint()
			profile.consume(checkpointProgressJSON(t, checkpoint, nil))
			result := profile.result()
			require.NotNil(t, result.LastCheckpoint, "valid partial progress should decode: %s", result.InvalidReason)
			require.Equal(t, checkpoint, *result.LastCheckpoint, "a killed or unfinished helper should retain its last exact progress checkpoint")
			require.Equal(t, 1, result.CheckpointCount, "partial progress should count one checkpoint")
			require.Equal(t, 1, result.CheckpointInvocation, "default consumption should belong to one helper invocation")
			require.Equal(t, 3, result.Records, "phase entries and the checkpoint should each count once")
			require.Equal(t, "incomplete_phase_exits", result.InvalidReason, "a checkpoint must not turn missing phase exits into a successful profile")
			require.True(t, result.Invalid, "unfinished phase exits should remain an invalid diagnostic")
			require.True(t, result.DiagnosticOnly, "progress must never count as acceptance evidence")
			require.Equal(t, map[string]realReviewProfileAggregate{
				"inventory/all": {Entered: 1, Active: 1, MaxActive: 1},
				"receipt/all":   {Entered: 1, Active: 1, MaxActive: 1},
			}, result.Phases, "partial operation totals must not manufacture phase completion or duration")
			require.Contains(t, result.CheckpointScope, "cumulative lower-bound partial progress", "the result should describe in-flight lower-bound progress")
			require.Contains(t, result.CheckpointScope, "not phase completion or allocation/OOM attribution", "the result should keep the diagnostic attribution boundary visible")
			result.LastCheckpoint.ElapsedUS++
			require.Equal(t, checkpoint, *profile.result().LastCheckpoint, "mutating a returned checkpoint must not alter retained evidence")
		})
	}
}

func TestRealReviewProfileCheckpointStreams(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		separateWriter bool
		defaultStream  bool
	}{
		{name: "separate execs each start a helper invocation", separateWriter: true},
		{name: "prepare then verify in one exec starts a new helper invocation"},
		{name: "default consumption also recognizes a new helper invocation", defaultStream: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			profile := &realReviewSourceProfile{}
			first := checkpointProgressFixture()
			second := realReviewProfileCheckpoint{Sequence: 1, ElapsedUS: 1}
			firstRaw, secondRaw := checkpointProgressJSON(t, first, nil), checkpointProgressJSON(t, second, nil)
			if tt.defaultStream {
				profile.consume(firstRaw)
				profile.consume(secondRaw)
			} else {
				decoder := &realReviewProfileDecoder{profile: profile}
				_, err := decoder.Write(append(append([]byte(realReviewProfilePrefix), firstRaw...), '\n'))
				require.NoError(t, err, "the prepare helper checkpoint should drain")
				if tt.separateWriter {
					decoder = &realReviewProfileDecoder{profile: profile}
				}
				_, err = decoder.Write(append(append([]byte(realReviewProfilePrefix), secondRaw...), '\n'))
				require.NoError(t, err, "the verify helper checkpoint should drain in either exec topology")
				decoder.finish()
			}
			result := profile.result()
			require.False(t, result.Invalid, "a fresh helper invocation should reset cumulative counters at its trusted sequence-one boundary")
			require.Equal(t, 2, result.CheckpointCount, "both helper invocations should retain their valid checkpoints")
			require.Equal(t, 2, result.CheckpointInvocation, "the verify helper invocation should own the displayed progress")
			require.NotNil(t, result.LastCheckpoint, "the verify helper checkpoint should survive: %s", result.InvalidReason)
			require.Equal(t, second, *result.LastCheckpoint, "the verify helper should supply its own cumulative totals rather than inheriting prepare totals")
		})
	}
}

func TestRealReviewProfilePrepareThenVerifyInOneExec(t *testing.T) {
	t.Parallel()
	profile := &realReviewSourceProfile{}
	decoder := &realReviewProfileDecoder{profile: profile}
	prepare := realReviewProfileCheckpoint{Sequence: 1, ElapsedUS: 100, InventoryUS: 60, InventoryResidualUS: 20, HashUS: 40, ReceiptUS: 100, ReceiptOutsideInventoryUS: 40, InventoryYielded: 3, HashStarted: 3, HashCompleted: 3, ReceiptStarted: 1, ReceiptCompleted: 1}
	verify := realReviewProfileCheckpoint{Sequence: 1, ElapsedUS: 1, InventoryUS: 1, HashUS: 1, HashStarted: 1, OpenInventory: 1, ActiveInventory: 1, ActiveHash: 1}
	verified := realReviewProfileCheckpoint{Sequence: 2, ElapsedUS: 70, InventoryUS: 70, InventoryResidualUS: 20, HashUS: 50, InventoryYielded: 3, HashStarted: 3, HashCompleted: 3}
	records := []string{
		`{"version":1,"op":1,"phase":6,"category":1}`,
		`{"version":1,"op":2,"phase":6,"category":1,"duration_us":2,"failed":0}`,
		`{"version":1,"op":1,"phase":9,"category":1}`,
		`{"version":1,"op":1,"phase":8,"category":1}`,
		`{"version":1,"op":2,"phase":8,"category":1,"duration_us":60,"failed":0,"items":3}`,
		`{"version":1,"op":2,"phase":9,"category":1,"duration_us":100,"failed":0,"items":3}`,
		string(checkpointProgressJSON(t, prepare, nil)),
		`{"version":1,"op":1,"phase":6,"category":1}`,
		`{"version":1,"op":2,"phase":6,"category":1,"duration_us":3,"failed":0}`,
		`{"version":1,"op":1,"phase":8,"category":1}`,
		string(checkpointProgressJSON(t, verify, nil)),
		string(checkpointProgressJSON(t, verified, nil)),
		`{"version":1,"op":2,"phase":8,"category":1,"duration_us":70,"failed":0,"items":3}`,
	}
	for _, record := range records {
		raw := realReviewProfilePrefix + record + "\n"
		written, err := io.WriteString(decoder, raw)
		require.NoError(t, err, "both helper launches should drain through the single cold exec decoder")
		require.Equal(t, len(raw), written, "all prepare and verify output should remain available to downstream consumers")
	}
	decoder.finish()
	result := profile.result()
	require.False(t, result.Invalid, "the complete cold prepare and verify lifecycle should keep its diagnostic valid")
	require.Equal(t, len(records), result.Records, "the sequence-one boundary must neither discard records nor fabricate phase completions")
	require.Equal(t, 3, result.CheckpointCount, "all prepare and verify checkpoints should contribute to the total")
	require.Equal(t, 2, result.CheckpointInvocation, "verify should have a distinct first-observed invocation ordinal within the same exec")
	require.NotNil(t, result.LastCheckpoint, "the completed verify rehash should remain available")
	require.Equal(t, verified, *result.LastCheckpoint, "verify totals should be scoped to its fresh helper and should not include prepare hashing or receipt work")
	items := int64(6)
	require.Equal(t, map[string]realReviewProfileAggregate{
		"input_binding/all": {Entered: 2, Completed: 2, MaxActive: 1, DurationUS: 5},
		"inventory/all":     {Entered: 2, Completed: 2, MaxActive: 1, DurationUS: 130, Items: &items},
		"receipt/all":       {Entered: 1, Completed: 1, MaxActive: 1, DurationUS: 100, Items: &prepare.InventoryYielded},
	}, result.Phases, "ordinary phase records from both helpers must retain their actual operation counts and durations")
}

func TestRealReviewProfileCheckpointLatestCommandWins(t *testing.T) {
	t.Parallel()
	profile := &realReviewSourceProfile{}
	first, second := &realReviewProfileDecoder{profile: profile}, &realReviewProfileDecoder{profile: profile}
	initial := checkpointProgressFixture()
	latest := realReviewProfileCheckpoint{Sequence: 1, ElapsedUS: 1}
	olderUpdate := initial
	olderUpdate.Sequence, olderUpdate.ElapsedUS = 2, 200
	latestUpdate := latest
	latestUpdate.Sequence, latestUpdate.ElapsedUS = 2, 2
	third := realReviewProfileCheckpoint{Sequence: 1, ElapsedUS: 1}
	thirdUpdate := third
	thirdUpdate.Sequence, thirdUpdate.ElapsedUS = 2, 3
	for _, record := range []struct {
		decoder    *realReviewProfileDecoder
		checkpoint realReviewProfileCheckpoint
	}{{first, initial}, {second, latest}, {first, olderUpdate}, {first, third}, {second, latestUpdate}, {first, thirdUpdate}} {
		raw := append(append([]byte(realReviewProfilePrefix), checkpointProgressJSON(t, record.checkpoint, nil)...), '\n')
		written, err := record.decoder.Write(raw)
		require.NoError(t, err, "interleaved helper output should drain")
		require.Equal(t, len(raw), written, "interleaved helper output should be consumed completely")
	}
	result := profile.result()
	require.False(t, result.Invalid, "independent helper streams should preserve their own monotonic sequences")
	require.Equal(t, 6, result.CheckpointCount, "all valid checkpoints from three helper invocations should contribute to the diagnostic count")
	require.Equal(t, 3, result.CheckpointInvocation, "a delayed earlier invocation must not replace a new helper within an older exec stream")
	require.NotNil(t, result.LastCheckpoint, "valid command streams should retain a checkpoint")
	require.Equal(t, thirdUpdate, *result.LastCheckpoint, "a delayed earlier invocation must not replace the latest helper totals")
}

func TestRealReviewProfileInvocationBoundaryRetainsGuards(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		seed         int
		intermediate func(*testing.T) []byte
		changeReset  func(map[string]any)
		wantReason   string
	}{
		{name: "reset cannot bypass the shared record cap", seed: 19999, wantReason: "record_count_limit"},
		{name: "reset must contain every required field", changeReset: func(fields map[string]any) { delete(fields, "hash_completed") }, wantReason: "invalid_checkpoint"},
		{name: "reset cannot revive a prior regression", intermediate: func(t *testing.T) []byte {
			checkpoint := checkpointProgressFixture()
			checkpoint.Sequence, checkpoint.ElapsedUS = 2, 0
			return checkpointProgressJSON(t, checkpoint, nil)
		}, wantReason: "regressing_checkpoint"},
		{name: "reset does not fabricate an unfinished phase exit", intermediate: func(t *testing.T) []byte {
			return []byte(`{"version":1,"op":1,"phase":8,"category":1}`)
		}, wantReason: "incomplete_phase_exits"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			profile := &realReviewSourceProfile{records: tt.seed}
			first := checkpointProgressFixture()
			profile.consume(checkpointProgressJSON(t, first, nil))
			if tt.intermediate != nil {
				profile.consume(tt.intermediate(t))
			}
			reset := realReviewProfileCheckpoint{Sequence: 1, ElapsedUS: 1}
			profile.consume(checkpointProgressJSON(t, reset, tt.changeReset))
			result := profile.result()
			require.Equal(t, tt.wantReason, result.InvalidReason, "a helper boundary must retain prior invalidity, record caps and real phase lifecycle requirements")
			require.NotNil(t, result.LastCheckpoint, "the last accepted checkpoint should survive later rejection")
			if tt.wantReason == "incomplete_phase_exits" {
				require.Equal(t, 2, result.CheckpointInvocation, "a valid reset should start a helper without changing the earlier phase state")
				require.Equal(t, reset, *result.LastCheckpoint, "the new helper may report partial progress while a missing phase exit remains invalid")
				require.Equal(t, realReviewProfileAggregate{Entered: 1, Active: 1, MaxActive: 1}, result.Phases["inventory/all"], "accepting a helper reset must never close an earlier open phase")
			} else {
				require.Equal(t, 1, result.CheckpointCount, "rejected reset evidence must not advance the retained checkpoint count")
				require.Equal(t, 1, result.CheckpointInvocation, "rejected reset evidence must not allocate a new displayed invocation ordinal")
				require.Equal(t, first, *result.LastCheckpoint, "rejected reset evidence must preserve the previous exact lower bound")
			}
		})
	}
}

func TestRealReviewProfileCheckpointRejectsInvalidEvidence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(map[string]any)
		raw    func([]byte) []byte
		reason string
	}{
		{name: "unknown numeric field", change: func(f map[string]any) { f["unexpected"] = 1 }, reason: "invalid_record"},
		{name: "duplicate key", raw: func(raw []byte) []byte { return []byte(strings.TrimSuffix(string(raw), "}") + `,"sequence":1}`) }, reason: "invalid_record"},
		{name: "null required field", change: func(f map[string]any) { f["hash_us"] = nil }, reason: "invalid_record"},
		{name: "fractional counter", change: func(f map[string]any) { f["hash_started"] = 3.5 }, reason: "invalid_record"},
		{name: "quoted numeric counter", change: func(f map[string]any) { f["hash_started"] = "3" }, reason: "invalid_record"},
		{name: "overflowing integer", raw: func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"elapsed_us":100`), []byte(`"elapsed_us":9223372036854775808`), 1)
		}, reason: "invalid_record"},
		{name: "missing required field", change: func(f map[string]any) { delete(f, "receipt_failed") }, reason: "invalid_checkpoint"},
		{name: "trailing object", raw: func(raw []byte) []byte { return append(raw, []byte(` {}`)...) }, reason: "invalid_record"},
		{name: "wrong version", change: func(f map[string]any) { f["version"] = 2 }, reason: "invalid_record"},
		{name: "phase attribution is forbidden", change: func(f map[string]any) { f["phase"] = 8 }, reason: "invalid_checkpoint"},
		{name: "sequence starts at one", change: func(f map[string]any) { f["sequence"] = 2 }, reason: "checkpoint_sequence"},
		{name: "zero sequence", change: func(f map[string]any) { f["sequence"] = 0 }, reason: "invalid_checkpoint"},
		{name: "checkpoint cap", change: func(f map[string]any) { f["sequence"] = 513 }, reason: "invalid_checkpoint"},
		{name: "negative cumulative time", change: func(f map[string]any) { f["elapsed_us"] = -1 }, reason: "invalid_checkpoint"},
		{name: "elapsed budget", change: func(f map[string]any) { f["elapsed_us"] = 480000001 }, reason: "invalid_checkpoint"},
		{name: "parallel duration bound", change: func(f map[string]any) { f["receipt_us"] = int64(64*480000000 + 1) }, reason: "invalid_checkpoint"},
		{name: "operation count bound", change: func(f map[string]any) { f["inventory_yielded"] = 1000001 }, reason: "invalid_checkpoint"},
		{name: "active concurrency bound", change: func(f map[string]any) { f["open_inventory"] = 65 }, reason: "invalid_checkpoint"},
		{name: "residual cannot exceed inventory minus hashing", change: func(f map[string]any) { f["inventory_residual_us"] = 21 }, reason: "invalid_checkpoint"},
		{name: "rounding cannot conceal two microseconds", change: func(f map[string]any) { f["inventory_us"] = 62 }, reason: "invalid_checkpoint"},
		{name: "receipt residual cannot exceed receipt time", change: func(f map[string]any) { f["receipt_outside_inventory_us"] = 101 }, reason: "invalid_checkpoint"},
		{name: "completions require starts", change: func(f map[string]any) { f["hash_completed"] = 4 }, reason: "invalid_checkpoint"},
		{name: "failures require completions", change: func(f map[string]any) { f["receipt_failed"] = 2 }, reason: "invalid_checkpoint"},
		{name: "active hashes require unfinished starts", change: func(f map[string]any) { f["active_hash"] = 1 }, reason: "invalid_checkpoint"},
		{name: "hashes require active inventory", change: func(f map[string]any) { f["active_inventory"] = 1 }, reason: "invalid_checkpoint"},
		{name: "active inventory requires open inventory", change: func(f map[string]any) { f["open_inventory"] = 1 }, reason: "invalid_checkpoint"},
		{name: "active receipts require unfinished starts", change: func(f map[string]any) { f["active_receipt"] = 0 }, reason: "invalid_checkpoint"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw := checkpointProgressJSON(t, checkpointProgressFixture(), tt.change)
			if tt.raw != nil {
				raw = tt.raw(raw)
			}
			profile := &realReviewSourceProfile{}
			profile.consume(raw)
			result := profile.result()
			require.Equal(t, tt.reason, result.InvalidReason, "malformed or inconsistent progress should fail closed with its finite reason")
			require.True(t, result.Invalid, "invalid checkpoint evidence should invalidate only the diagnostic")
			require.Equal(t, 0, result.CheckpointCount, "rejected evidence must not advance the checkpoint count")
			require.Nil(t, result.LastCheckpoint, "rejected evidence must not become displayed progress")
		})
	}
}

func TestRealReviewProfileCheckpointBoundsAndProgression(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		seed       int
		count      int64
		changeLast func(*realReviewProfileCheckpoint)
		wantCount  int
		wantReason string
	}{
		{name: "all 512 consecutive checkpoints are accepted", count: 512, wantCount: 512},
		{name: "a sequence gap cannot hide discarded progress", count: 2, changeLast: func(c *realReviewProfileCheckpoint) { c.Sequence = 3 }, wantCount: 1, wantReason: "checkpoint_sequence"},
		{name: "duplicate non-one sequence cannot reset an invocation", count: 3, changeLast: func(c *realReviewProfileCheckpoint) { c.Sequence = 2 }, wantCount: 2, wantReason: "checkpoint_sequence"},
		{name: "mid invocation non-one cumulative totals cannot fall", count: 2, changeLast: func(c *realReviewProfileCheckpoint) { c.ElapsedUS = 0 }, wantCount: 1, wantReason: "regressing_checkpoint"},
		{name: "active operations may finish while totals rise", count: 2, changeLast: func(c *realReviewProfileCheckpoint) {
			c.HashCompleted, c.HashFailed, c.ReceiptCompleted = c.HashStarted, 1, c.ReceiptStarted
			c.ActiveHash, c.ActiveReceipt, c.ActiveInventory, c.OpenInventory = 0, 0, 0, 0
		}, wantCount: 2},
		{name: "checkpoints share the global record cap", seed: 19999, count: 2, wantCount: 1, wantReason: "record_count_limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			profile := &realReviewSourceProfile{records: tt.seed}
			var last realReviewProfileCheckpoint
			for sequence := int64(1); sequence <= tt.count; sequence++ {
				checkpoint := checkpointProgressFixture()
				checkpoint.Sequence, checkpoint.ElapsedUS = sequence, sequence*100
				if sequence == tt.count && tt.changeLast != nil {
					tt.changeLast(&checkpoint)
				}
				profile.consume(checkpointProgressJSON(t, checkpoint, nil))
				if sequence <= int64(tt.wantCount) {
					last = checkpoint
				}
			}
			result := profile.result()
			require.Equal(t, tt.wantReason, result.InvalidReason, "stream bounds should reject gaps and regressions without rejecting finished operations")
			require.Equal(t, tt.wantCount, result.CheckpointCount, "the checkpoint count should stop at the last valid record")
			require.NotNil(t, result.LastCheckpoint, "the initial valid checkpoint should survive: %s", result.InvalidReason)
			require.Equal(t, last, *result.LastCheckpoint, "the exact last valid lower-bound progress should survive a later rejection")
		})
	}
}

func TestRealReviewProfileInvalidCheckpointStillDrainsOutput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		line   string
		reason string
	}{
		{name: "malformed numeric record", line: realReviewProfilePrefix + `{"version":1,"op":4,"phase":0,"category":0,"secret":"private-command"}` + "\n", reason: "invalid_record"},
		{name: "oversized record", line: realReviewProfilePrefix + strings.Repeat("7", 1100) + "\n", reason: "record_size_limit"},
		{name: "truncated command output", line: realReviewProfilePrefix + `{"version":1,"op":4`, reason: "truncated_record"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			profile := &realReviewSourceProfile{}
			decoder := &realReviewProfileDecoder{profile: profile}
			var downstream bytes.Buffer
			writer := io.MultiWriter(decoder, &downstream)
			chunks := []string{"ordinary output\n", tt.line[:7], tt.line[7:]}
			if strings.HasSuffix(tt.line, "\n") {
				chunks = append(chunks, "output after invalid diagnostic\n", realReviewProfilePrefix+string(checkpointProgressJSON(t, checkpointProgressFixture(), nil))+"\n")
			}
			for _, chunk := range chunks {
				written, err := io.WriteString(writer, chunk)
				require.NoError(t, err, "invalid optional diagnostics must not interrupt original output or credential scanning")
				require.Equal(t, len(chunk), written, "every original output byte should continue downstream")
			}
			decoder.finish()
			require.Equal(t, strings.Join(chunks, ""), downstream.String(), "the original writer should receive all bytes before and after diagnostic rejection")
			result := profile.result()
			require.Equal(t, tt.reason, result.InvalidReason, "the first invalid diagnostic should retain its finite error reason")
			require.Equal(t, 0, result.CheckpointCount, "later valid records must not revive an already invalid diagnostic")
			require.Nil(t, result.LastCheckpoint, "invalid output must not become retained progress")
			require.Empty(t, decoder.line, "finished command output must release the bounded decoder buffer")
			raw, err := json.Marshal(result)
			require.NoError(t, err, "finite diagnostic results should encode")
			require.NotContains(t, string(raw), "private-command", "raw invalid record contents must not leak into retained diagnostic evidence")
		})
	}
}

// Keep the observer's Python contracts selected by the ordinary Go CI suite.
func TestRealReviewProfilePythonContracts(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "python3", "-B", "-m", "unittest", "discover", "-v", "-s", "testdata", "-p", "test_review_source_profile.py")
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "REVIEW_SOURCE_PROFILE_REAL_HELPER=") {
			command.Env = append(command.Env, value)
		}
	}
	output, err := command.CombinedOutput()
	require.NoError(t, err, "the public observer Python contract suite should pass without a private helper fixture: %s", output)
	require.NoError(t, validateRealReviewProfilePythonContracts(string(output)), "ordinary Go CI must execute observer contracts and skip only the optional external helper fixture: %s", output)
	t.Logf("observer Python contracts: %s", output)
}

// Verbose unittest output identifies the optional external fixture by test ID;
// the summary alone would also accept a discovered suite whose tests all skip.
func validateRealReviewProfilePythonContracts(output string) error {
	runs := regexp.MustCompile(`(?m)^Ran ([0-9]+) tests? in [^\r\n]+$`).FindAllStringSubmatch(output, -1)
	results := regexp.MustCompile(`(?m)^OK(?: \(skipped=([0-9]+)\))?$`).FindAllStringSubmatch(output, -1)
	if len(runs) != 1 || len(results) != 1 {
		return fmt.Errorf("expected one successful unittest run summary")
	}
	total, err := strconv.Atoi(runs[0][1])
	if err != nil {
		return fmt.Errorf("invalid unittest test count: %w", err)
	}
	skipped := 0
	if results[0][1] != "" {
		skipped, err = strconv.Atoi(results[0][1])
		if err != nil {
			return fmt.Errorf("invalid unittest skip count: %w", err)
		}
	}
	if skipped > 1 || total <= skipped {
		return fmt.Errorf("expected nonzero executed contracts and at most one optional fixture skip")
	}
	observedSkips := 0
	const optional = "test_actual_pinned_helper_receipt_bytes_tamper_links_and_fifo_are_preserved (test_review_source_profile.ReviewSourceProfileTests.test_actual_pinned_helper_receipt_bytes_tamper_links_and_fifo_are_preserved) ... skipped "
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, " ... skipped ") {
			if !strings.HasPrefix(line, optional) {
				return fmt.Errorf("unexpected skipped Python contract")
			}
			observedSkips++
		}
	}
	if observedSkips != skipped {
		return fmt.Errorf("unittest skip summary does not match the optional fixture record")
	}
	return nil
}

func TestRealReviewProfilePythonContractSummary(t *testing.T) {
	t.Parallel()
	const optional = "test_actual_pinned_helper_receipt_bytes_tamper_links_and_fifo_are_preserved (test_review_source_profile.ReviewSourceProfileTests.test_actual_pinned_helper_receipt_bytes_tamper_links_and_fifo_are_preserved) ... skipped 'external helper not supplied'\n"
	tests := []struct {
		name      string
		output    string
		wantError bool
	}{
		{name: "all contracts execute", output: "Ran 22 tests in 1.5s\n\nOK\n"},
		{name: "only the identified optional fixture skips", output: optional + "Ran 22 tests in 1.5s\n\nOK (skipped=1)\n"},
		{name: "no contracts discovered", output: "Ran 0 tests in 0.0s\n\nOK\n", wantError: true},
		{name: "only the optional fixture discovered and skipped", output: optional + "Ran 1 test in 0.0s\n\nOK (skipped=1)\n", wantError: true},
		{name: "all discovered contracts skip", output: "Ran 22 tests in 0.0s\n\nOK (skipped=22)\n", wantError: true},
		{name: "an unrelated contract skips", output: "test_other (test_review_source_profile.ReviewSourceProfileTests.test_other) ... skipped 'unavailable'\nRan 22 tests in 1.5s\n\nOK (skipped=1)\n", wantError: true},
		{name: "skip count lacks an identified fixture", output: "Ran 22 tests in 1.5s\n\nOK (skipped=1)\n", wantError: true},
		{name: "identified skip lacks a matching count", output: optional + "Ran 22 tests in 1.5s\n\nOK\n", wantError: true},
		{name: "multiple suites cannot obscure selected tests", output: "Ran 22 tests in 1.5s\n\nOK\nRan 0 tests in 0.0s\n\nOK\n", wantError: true},
		{name: "missing summary", output: "fixture output only\n", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateRealReviewProfilePythonContracts(tt.output)
			if tt.wantError {
				require.Error(t, err, "a vacuous or unexpected Python contract result must fail the Go bridge")
			} else {
				require.NoError(t, err, "executed Python contracts with only the known optional fixture skip should pass")
			}
		})
	}
}
