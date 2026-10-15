package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
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
		expectedCount  int
		expectedReason string
	}{
		{name: "each helper command resets cumulative counters", separateWriter: true, expectedCount: 2},
		{name: "one command cannot reset cumulative counters", expectedCount: 1, expectedReason: "regressing_checkpoint"},
		{name: "default consumption cannot reset cumulative counters", defaultStream: true, expectedCount: 1, expectedReason: "regressing_checkpoint"},
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
				require.NoError(t, err, "the first helper checkpoint should drain")
				if tt.separateWriter {
					decoder = &realReviewProfileDecoder{profile: profile}
				}
				_, err = decoder.Write(append(append([]byte(realReviewProfilePrefix), secondRaw...), '\n'))
				require.NoError(t, err, "counter validation must not interrupt the command output writer")
				decoder.finish()
			}
			result := profile.result()
			require.Equal(t, tt.expectedReason, result.InvalidReason, "counter resets should be judged within their helper command only")
			require.Equal(t, tt.expectedCount, result.CheckpointCount, "only valid per-command checkpoints should be retained")
			require.Equal(t, tt.expectedCount, result.CheckpointInvocation, "the latest valid helper command should own the displayed checkpoint")
			expected := first
			if tt.separateWriter {
				expected = second
			}
			require.NotNil(t, result.LastCheckpoint, "the initial valid checkpoint should survive: %s", result.InvalidReason)
			require.Equal(t, expected, *result.LastCheckpoint, "the latest accepted command should supply its own cumulative totals")
		})
	}
}

func TestRealReviewProfileCheckpointLatestCommandWins(t *testing.T) {
	t.Parallel()
	profile := &realReviewSourceProfile{}
	first, second := &realReviewProfileDecoder{profile: profile}, &realReviewProfileDecoder{profile: profile}
	initial := checkpointProgressFixture()
	latest := realReviewProfileCheckpoint{Sequence: 1, ElapsedUS: 1}
	olderUpdate := initial
	olderUpdate.Sequence, olderUpdate.ElapsedUS = 2, 200
	for _, record := range []struct {
		decoder    *realReviewProfileDecoder
		checkpoint realReviewProfileCheckpoint
	}{{first, initial}, {second, latest}, {first, olderUpdate}} {
		raw := append(append([]byte(realReviewProfilePrefix), checkpointProgressJSON(t, record.checkpoint, nil)...), '\n')
		written, err := record.decoder.Write(raw)
		require.NoError(t, err, "interleaved helper output should drain")
		require.Equal(t, len(raw), written, "interleaved helper output should be consumed completely")
	}
	result := profile.result()
	require.False(t, result.Invalid, "independent helper streams should preserve their own monotonic sequences")
	require.Equal(t, 3, result.CheckpointCount, "all three valid checkpoints should contribute to the diagnostic count")
	require.Equal(t, 2, result.CheckpointInvocation, "a delayed earlier command must not replace the latest command identity")
	require.NotNil(t, result.LastCheckpoint, "valid command streams should retain a checkpoint")
	require.Equal(t, latest, *result.LastCheckpoint, "a delayed earlier command must not replace the latest command totals")
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
		{name: "cumulative totals cannot fall", count: 2, changeLast: func(c *realReviewProfileCheckpoint) { c.ElapsedUS = 0 }, wantCount: 1, wantReason: "regressing_checkpoint"},
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
	command := exec.CommandContext(ctx, "python3", "-B", "-m", "unittest", "discover", "-s", "testdata", "-p", "test_review_source_profile.py")
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "REVIEW_SOURCE_PROFILE_REAL_HELPER=") {
			command.Env = append(command.Env, value)
		}
	}
	output, err := command.CombinedOutput()
	require.NoError(t, err, "the public observer Python contract suite should pass without a private helper fixture: %s", output)
	require.NotContains(t, string(output), "Ran 0 tests", "ordinary Go CI must select the observer Python contracts")
	t.Logf("observer Python contracts: %s", output)
}
