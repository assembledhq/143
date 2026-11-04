package agent

import (
	"bytes"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

const (
	reviewProfileMaxRecords = 64
	reviewProfileMaxMillis  = 600_000
	reviewProfileMaxCount   = 100_000_000
)

// Preparation stdout can also contain repository or provider output. This
// writer never retains more than one short line and only logs typed metrics.
type reviewPreparationProfileKey struct{}

type reviewPreparationProfile struct {
	log     zerolog.Logger
	parent  string
	mu      sync.Mutex
	line    [96]byte
	length  int
	dropped bool
	records int
}

func (p *reviewPreparationProfile) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for remaining := data; len(remaining) > 0 && p.records < reviewProfileMaxRecords; {
		before, after, complete := bytes.Cut(remaining, []byte{'\n'})
		if !p.dropped && len(before) <= len(p.line)-p.length {
			p.length += copy(p.line[p.length:], before)
		} else {
			p.dropped = true
		}
		if complete {
			if !p.dropped {
				p.record(p.line[:p.length])
			}
			p.length = 0
			p.dropped = false
		}
		remaining = after
	}
	return len(data), nil
}

func (p *reviewPreparationProfile) record(line []byte) {
	fields := bytes.Split(line, []byte{' '})
	if len(fields) != 4 || string(fields[0]) != "143-prep-v1" {
		return
	}
	stage := string(fields[1])
	switch stage {
	case "pre_integrity", "post_integrity", "authenticated_clone", "authenticated_prepare", "local_prepare",
		"commit_check", "commit_fetch", "merge_base", "history_fetch", "missing_enumeration", "object_fetch",
		"patch_generation", "patch_hashing", "captured_object_hashing":
	default:
		return
	}
	elapsed, elapsedOK := reviewProfileNumber(fields[2], reviewProfileMaxMillis)
	count, countOK := reviewProfileNumber(fields[3], reviewProfileMaxCount)
	if !elapsedOK || !countOK {
		return
	}
	p.records++
	p.event(stage).Uint64("elapsed_ms", elapsed).Str("profile_source", "sandbox_reported").Uint64("count", count).Msg("review preparation profile")
}

func reviewProfileNumber(value []byte, limit uint64) (uint64, bool) {
	if len(value) == 0 || len(value) > 9 {
		return 0, false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return 0, false
		}
	}
	number, err := strconv.ParseUint(string(value), 10, 32)
	return number, err == nil && number <= limit
}

func (p *reviewPreparationProfile) event(stage string) *zerolog.Event {
	return p.log.Info().Str("parent_stage", p.parent).Str("profile_stage", stage)
}

func (p *reviewPreparationProfile) executor(start time.Time, outcome string) {
	millis := time.Since(start).Milliseconds()
	if millis < 0 {
		millis = 0
	}
	if millis > reviewProfileMaxMillis {
		millis = reviewProfileMaxMillis
	}
	p.event("executor").Int64("elapsed_ms", millis).Str("profile_source", "orchestrator").Str("outcome", outcome).Msg("review preparation profile")
}

const reviewPreparationProfilePython = `
profile_enabled = False

def profile(stage, start, count=1):
    if not profile_enabled:
        return
    elapsed_ms = min(600000, int((time.monotonic() - start) * 1000))
    try:
        os.write(1, ("143-prep-v1 %s %d %d\n" % (stage, elapsed_ms, min(count, 100000000))).encode("ascii"))
    except OSError:
        pass

def measured(stage, operation, *args, **kwargs):
    if not profile_enabled:
        return operation(*args, **kwargs)
    start = time.monotonic()
    try:
        return operation(*args, **kwargs)
    finally:
        profile(stage, start)
`
