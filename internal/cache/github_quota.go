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

var githubQuotaObserveScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local reset = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local remaining = tonumber(ARGV[4])
local prior_reset = tonumber(redis.call('HGET', KEYS[1], 'reset') or '0')
local prior_remaining = tonumber(redis.call('HGET', KEYS[1], 'remaining') or '0')
if reset <= now or reset < prior_reset or (reset == prior_reset and remaining > prior_remaining) then return 0 end
redis.call('HSET', KEYS[1], 'reset', reset, 'limit', limit, 'remaining', remaining)
redis.call('PEXPIRE', KEYS[1], reset - now)
return 1
`)

var githubBackgroundAdmissionScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local reset = tonumber(redis.call('HGET', KEYS[1], 'reset') or '0')
local remaining = tonumber(redis.call('HGET', KEYS[1], 'remaining') or '-1')
if reset <= now or remaining < 0 then return {0, 0, 1, 0, 0} end
if remaining > tonumber(ARGV[2]) then return {1, 0, 1, 0, 0} end
local candidate = 'candidate:' .. ARGV[3]
local interval = tonumber(ARGV[4])
local expiry = tonumber(ARGV[5])
local entries = redis.call('HGETALL', KEYS[1])
local oldest = nil
local oldest_sequence = nil
local count = 0
for i = 1, #entries, 2 do
  if string.sub(entries[i], 1, 10) == 'candidate:' then
    local deadline, sequence = string.match(entries[i+1], '^(%d+):(%d+)$')
    deadline = tonumber(deadline)
    sequence = tonumber(sequence)
    if not deadline or not sequence or deadline <= now then
      redis.call('HDEL', KEYS[1], entries[i])
    else
      count = count + 1
      if not oldest_sequence or sequence < oldest_sequence then
        oldest = entries[i]
        oldest_sequence = sequence
      end
    end
  end
end
local current = redis.call('HGET', KEYS[1], candidate)
local sequence = current and tonumber(string.match(current, ':(%d+)$'))
if not sequence then
  if count >= 1024 then return {1, 1, 1, 1, 0} end
  sequence = redis.call('HINCRBY', KEYS[1], 'sequence', 1)
end
redis.call('HSET', KEYS[1], candidate, string.format('%.0f:%.0f', now + expiry, sequence))
if not oldest then oldest = candidate end
local next_admission = tonumber(redis.call('HGET', KEYS[1], 'next_admission') or '0')
if next_admission <= now and oldest == candidate then
  redis.call('HDEL', KEYS[1], candidate)
  redis.call('HSET', KEYS[1], 'next_admission', now + interval)
  return {1, 1, 1, 0, now + interval}
end
if next_admission <= now then next_admission = now + interval end
return {1, 1, 0, 0, next_admission}
`)

func (c *Client) ObserveGitHubQuota(ctx context.Context, key string, observation GitHubQuotaObservation) (bool, error) {
	if c == nil || c.rdb == nil {
		return false, errors.New("redis unavailable")
	}
	if observation.Limit <= 0 || observation.Remaining < 0 || observation.Remaining > observation.Limit || !observation.ResetAt.After(observation.Now) {
		return false, nil
	}
	var applied int
	err := c.doCommand(ctx, "github_quota_observe", func() error {
		var err error
		applied, err = githubQuotaObserveScript.Run(ctx, c.rdb, []string{key}, observation.Now.UnixMilli(), observation.ResetAt.UnixMilli(), observation.Limit, observation.Remaining).Int()
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
	if candidate == "" || len(candidate) > 128 || interval < time.Second || interval > 15*time.Minute {
		return GitHubBackgroundAdmission{}, errors.New("invalid GitHub background quota admission")
	}
	expiry := max(3*interval, 30*time.Minute)
	var values []any
	err := c.doCommand(ctx, "github_background_admit", func() error {
		var err error
		values, err = githubBackgroundAdmissionScript.Run(ctx, c.rdb, []string{key}, now.UnixMilli(), reserve, candidate, interval.Milliseconds(), expiry.Milliseconds()).Slice()
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
