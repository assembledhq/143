package cache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type GitHubQuotaObservation struct {
	Now       time.Time
	ResetAt   time.Time
	Limit     int64
	Remaining int64
}

type GitHubBackgroundAdmission struct {
	Known    bool
	Low      bool
	Admitted bool
	Overflow bool
	RetryAt  time.Time
}

const (
	// Keep provider reset families bounded even if an installation sees unusual
	// headers. Excess families share a conservative minimum until their latest
	// reset, so reaching the cap never discards lower remaining quota.
	githubQuotaMaxFamilies        = 16
	githubQuotaMaxResetAge        = 2 * time.Hour
	githubQuotaMaxCandidates      = 1024
	githubQuotaMaxInterval        = 15 * time.Minute
	githubQuotaMaxCandidateExpiry = 3 * githubQuotaMaxInterval
)

// The legacy hash remains readable and writable by older binaries, including
// its candidate FIFO. Families and the new-client admission lane live in
// separate hashes so an older writer's shorter PEXPIRE cannot erase their state.
// All three keys share the caller's installation hash tag and are accessed
// atomically on Redis Cluster. Old binaries retain their original admission
// behavior; enable the reserve only after upgrading all writers, and disable it
// before rollback.
const githubQuotaFamiliesScript = `
local now = tonumber(ARGV[1])
local max_age = tonumber(ARGV[6])
local max_families = tonumber(ARGV[7])
local family_count = 0
local latest_reset = 0
local lowest_remaining = nil
local families = {}
local overflow_reset = tonumber(redis.call('HGET', KEYS[2], 'overflow_reset') or '0')
local overflow_remaining = tonumber(redis.call('HGET', KEYS[2], 'overflow_remaining') or '-1')
if overflow_reset <= now or overflow_remaining < 0 then
  redis.call('HDEL', KEYS[2], 'overflow_reset', 'overflow_remaining')
  overflow_reset = 0
  overflow_remaining = nil
else
  latest_reset = overflow_reset
  lowest_remaining = overflow_remaining
end
local entries = redis.call('HGETALL', KEYS[2])
for i = 1, #entries, 2 do
  local reset = tonumber(string.match(entries[i], '^reset:(%d+)$') or '')
  if reset then
    local remaining = tonumber(entries[i+1])
    if reset <= now or not remaining or remaining < 0 then
      redis.call('HDEL', KEYS[2], entries[i])
    else
      families[entries[i]] = remaining
      family_count = family_count + 1
      latest_reset = math.max(latest_reset, reset)
      lowest_remaining = lowest_remaining and math.min(lowest_remaining, remaining) or remaining
    end
  end
end
local function merge_family(reset, remaining)
  if reset <= now or reset > now + max_age or remaining < 0 then return false end
  local field = 'reset:' .. string.format('%.0f', reset)
  local prior = families[field]
  if prior then
    if remaining > prior then return false end
  elseif family_count >= max_families then
    -- The overflow summary can retain a low value longer than its individual
    -- family, but never past the latest bounded provider reset in the summary.
    overflow_reset = math.max(overflow_reset, reset)
    overflow_remaining = overflow_remaining and math.min(overflow_remaining, remaining) or remaining
    redis.call('HSET', KEYS[2], 'overflow_reset', overflow_reset, 'overflow_remaining', overflow_remaining)
    latest_reset = math.max(latest_reset, overflow_reset)
    lowest_remaining = lowest_remaining and math.min(lowest_remaining, overflow_remaining) or overflow_remaining
    return true
  else
    family_count = family_count + 1
  end
  families[field] = remaining
  redis.call('HSET', KEYS[2], field, remaining)
  latest_reset = math.max(latest_reset, reset)
  lowest_remaining = lowest_remaining and math.min(lowest_remaining, remaining) or remaining
  return true
end
local legacy_reset = tonumber(redis.call('HGET', KEYS[1], 'reset') or '0')
local legacy_limit = tonumber(redis.call('HGET', KEYS[1], 'limit') or '0')
local legacy_remaining = tonumber(redis.call('HGET', KEYS[1], 'remaining') or '-1')
if legacy_limit > 0 and legacy_remaining <= legacy_limit then
  merge_family(legacy_reset, legacy_remaining)
end
local function retain_families()
  if latest_reset > now then
    redis.call('PEXPIRE', KEYS[2], latest_reset - now)
    if redis.call('EXISTS', KEYS[3]) == 1 then redis.call('PEXPIRE', KEYS[3], latest_reset - now) end
  else
    redis.call('DEL', KEYS[2], KEYS[3])
  end
end
`

var githubQuotaObserveScript = redis.NewScript(githubQuotaFamiliesScript + `
local reset = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local remaining = tonumber(ARGV[4])
local applied = merge_family(reset, remaining)
-- Preserve the old scalar contract during a rolling deploy or rollback. Old
-- readers retain their existing single-family behavior; they cannot overwrite
-- or erase the separate family hash used by new readers.
if reset > now and not (reset < legacy_reset or (reset > legacy_reset and legacy_reset > now) or (reset == legacy_reset and remaining > legacy_remaining)) then
  redis.call('HSET', KEYS[1], 'reset', reset, 'limit', limit, 'remaining', remaining)
  redis.call('PEXPIRE', KEYS[1], reset - now)
end
retain_families()
return applied and 1 or 0
`)

var githubBackgroundAdmissionScript = redis.NewScript(githubQuotaFamiliesScript + `
local function clear_legacy_lane()
  local legacy_entries = redis.call('HGETALL', KEYS[1])
  for i = 1, #legacy_entries, 2 do
    if string.sub(legacy_entries[i], 1, 10) == 'candidate:' then
      redis.call('HDEL', KEYS[1], legacy_entries[i])
    end
  end
  redis.call('HDEL', KEYS[1], 'sequence', 'next_admission')
end
local function reset_lane()
  redis.call('DEL', KEYS[3])
  clear_legacy_lane()
  -- A healthy admission serves work outside the periodic lane. Its old queue
  -- and slot are obsolete, and must not be reimported if quota becomes low.
  if latest_reset > now then redis.call('HSET', KEYS[3], 'migrated', 1) end
  retain_families()
end
if not lowest_remaining then
  reset_lane()
  return {0, 0, 1, 0, 0}
end
if lowest_remaining > tonumber(ARGV[2]) then
  reset_lane()
  return {1, 0, 1, 0, 0}
end
local max_interval = tonumber(ARGV[8])
local max_candidate_expiry = tonumber(ARGV[9])
local max_candidates = tonumber(ARGV[10])
if redis.call('HGET', KEYS[3], 'migrated') ~= '1' then
  -- One-time migration preserves valid existing positions and the consumed
  -- periodic slot. Later old-client writes cannot modify this isolated lane.
  redis.call('DEL', KEYS[3])
  redis.call('HSET', KEYS[3], 'migrated', 1)
  local legacy_entries = redis.call('HGETALL', KEYS[1])
  local highest_sequence = 0
  local imported = 0
  for i = 1, #legacy_entries, 2 do
    local field = legacy_entries[i]
    if string.sub(field, 1, 10) == 'candidate:' and #field > 10 and #field <= 138 then
      local deadline, sequence = string.match(legacy_entries[i+1], '^(%d+):(%d+)$')
      deadline = tonumber(deadline)
      sequence = tonumber(sequence)
      if deadline and sequence and deadline > now and deadline <= now + max_candidate_expiry and sequence > 0 and sequence < 9007199254740991 and imported < max_candidates then
        redis.call('HSET', KEYS[3], field, legacy_entries[i+1])
        imported = imported + 1
        highest_sequence = math.max(highest_sequence, sequence)
      end
    end
  end
  redis.call('HSET', KEYS[3], 'sequence', highest_sequence)
  local legacy_next = tonumber(redis.call('HGET', KEYS[1], 'next_admission') or '0')
  if legacy_next > now and legacy_next <= now + max_interval then
    redis.call('HSET', KEYS[3], 'next_admission', legacy_next)
  end
end
retain_families()
local candidate = 'candidate:' .. ARGV[3]
local interval = tonumber(ARGV[4])
local expiry = tonumber(ARGV[5])
local entries = redis.call('HGETALL', KEYS[3])
local oldest = nil
local oldest_sequence = nil
local count = 0
for i = 1, #entries, 2 do
  if string.sub(entries[i], 1, 10) == 'candidate:' then
    local deadline, sequence = string.match(entries[i+1], '^(%d+):(%d+)$')
    deadline = tonumber(deadline)
    sequence = tonumber(sequence)
    if not deadline or not sequence or deadline <= now then
      redis.call('HDEL', KEYS[3], entries[i])
    else
      count = count + 1
      if not oldest_sequence or sequence < oldest_sequence then
        oldest = entries[i]
        oldest_sequence = sequence
      end
    end
  end
end
local current = redis.call('HGET', KEYS[3], candidate)
local sequence = current and tonumber(string.match(current, ':(%d+)$'))
if not sequence then
  if count >= max_candidates then return {1, 1, 1, 1, 0} end
  sequence = redis.call('HINCRBY', KEYS[3], 'sequence', 1)
end
redis.call('HSET', KEYS[3], candidate, string.format('%.0f:%.0f', now + expiry, sequence))
if not oldest then oldest = candidate end
local next_admission = tonumber(redis.call('HGET', KEYS[3], 'next_admission') or '0')
if next_admission <= now and oldest == candidate then
  redis.call('HDEL', KEYS[3], candidate)
  redis.call('HSET', KEYS[3], 'next_admission', now + interval)
  return {1, 1, 1, 0, now + interval}
end
if next_admission <= now then next_admission = now + interval end
return {1, 1, 0, 0, next_admission}
`)

func (c *Client) ObserveGitHubQuota(ctx context.Context, key string, observation GitHubQuotaObservation) (bool, error) {
	if c == nil || c.rdb == nil {
		return false, errors.New("redis unavailable")
	}
	if observation.Limit <= 0 || observation.Remaining < 0 || observation.Remaining > observation.Limit || !observation.ResetAt.After(observation.Now) || observation.ResetAt.Sub(observation.Now) > githubQuotaMaxResetAge {
		return false, nil
	}
	var applied int
	err := c.doCommand(ctx, "github_quota_observe", func() error {
		var err error
		applied, err = githubQuotaObserveScript.Run(ctx, c.rdb, []string{key, githubQuotaFamiliesKey(key), githubQuotaAdmissionKey(key)}, observation.Now.UnixMilli(), observation.ResetAt.UnixMilli(), observation.Limit, observation.Remaining, 0, githubQuotaMaxResetAge.Milliseconds(), githubQuotaMaxFamilies).Int()
		return err
	})
	return applied == 1, err
}

func (c *Client) AdmitGitHubBackground(ctx context.Context, key, candidate string, now time.Time, reserve int64, interval time.Duration) (GitHubBackgroundAdmission, error) {
	if c == nil || c.rdb == nil {
		return GitHubBackgroundAdmission{}, errors.New("redis unavailable")
	}
	if reserve <= 0 {
		return GitHubBackgroundAdmission{Admitted: true}, nil
	}
	if candidate == "" || len(candidate) > 128 || interval < time.Second || interval > githubQuotaMaxInterval {
		return GitHubBackgroundAdmission{}, errors.New("invalid GitHub background quota admission")
	}
	expiry := max(3*interval, 30*time.Minute)
	var values []any
	err := c.doCommand(ctx, "github_background_admit", func() error {
		var err error
		values, err = githubBackgroundAdmissionScript.Run(ctx, c.rdb, []string{key, githubQuotaFamiliesKey(key), githubQuotaAdmissionKey(key)}, now.UnixMilli(), reserve, candidate, interval.Milliseconds(), expiry.Milliseconds(), githubQuotaMaxResetAge.Milliseconds(), githubQuotaMaxFamilies, githubQuotaMaxInterval.Milliseconds(), githubQuotaMaxCandidateExpiry.Milliseconds(), githubQuotaMaxCandidates).Slice()
		return err
	})
	if err != nil {
		return GitHubBackgroundAdmission{}, err
	}
	if len(values) != 5 {
		return GitHubBackgroundAdmission{}, fmt.Errorf("invalid GitHub background admission result length %d", len(values))
	}
	ints := make([]int64, len(values))
	for i, value := range values {
		parsed, ok := value.(int64)
		if !ok {
			return GitHubBackgroundAdmission{}, errors.New("invalid GitHub background admission result")
		}
		ints[i] = parsed
	}
	result := GitHubBackgroundAdmission{Known: ints[0] == 1, Low: ints[1] == 1, Admitted: ints[2] == 1, Overflow: ints[3] == 1}
	if ints[4] > 0 {
		result.RetryAt = time.UnixMilli(ints[4]).UTC()
	}
	return result, nil
}

func githubQuotaFamiliesKey(key string) string {
	return key + ":families:v2"
}

func githubQuotaAdmissionKey(key string) string {
	return key + ":admission:v2"
}
