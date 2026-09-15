package cache

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// GitHubRateLimitState is an infrastructure DTO for one installation-wide
// cooldown record. It contains no tenant data or credentials.
type GitHubRateLimitState struct {
	Generation      int64
	EpisodeCount    int64
	BlockedUntil    time.Time
	Kind            string
	Resource        string
	Reason          string
	ProbeOwner      string
	ProbeToken      string
	ProbeGeneration int64
	ProbeUntil      time.Time
}

type GitHubRateLimitObservation struct {
	Now              time.Time
	BlockedUntil     time.Time
	Kind             string
	Resource         string
	Reason           string
	ProbeGeneration  int64
	ProbeToken       string
	TTL              time.Duration
	ProviderDeadline bool
}

// Read all retained authority inside the mutation script, including rejected
// outcomes: a second read could fail after Redis has already fenced a response.
const githubRateLimitStateResult = `
local function state_result(...)
  return {redis.call('HMGET', KEYS[1], 'generation', 'episode_count', 'blocked_until', 'probe_until',
    'probe_owner', 'probe_token', 'probe_generation', 'kind', 'resource', 'reason'), ...}
end
`

var githubRateLimitObserveScript = redis.NewScript(githubRateLimitStateResult + `
local now = tonumber(ARGV[1])
local deadline = tonumber(ARGV[2])
local observed_generation = tonumber(ARGV[6])
local generation = tonumber(redis.call('HGET', KEYS[1], 'generation') or '0')
local episodes = tonumber(redis.call('HGET', KEYS[1], 'episode_count') or '0')
local blocked = tonumber(redis.call('HGET', KEYS[1], 'blocked_until') or '0')
local probe_generation = tonumber(redis.call('HGET', KEYS[1], 'probe_generation') or '0')
local probe_token = redis.call('HGET', KEYS[1], 'probe_token') or ''
local increment = 0

local fenced = observed_generation > 0 and (observed_generation ~= generation or probe_generation ~= observed_generation or probe_token ~= ARGV[7])
if fenced then
  -- Synthetic deadlines and completed episodes grant no stale-token authority.
  if ARGV[9] ~= '1' or blocked == 0 or deadline <= blocked or deadline <= now then return state_result(0, 0) end
  -- New provider evidence fences the current lease without opening an episode.
  redis.call('HDEL', KEYS[1], 'probe_owner', 'probe_token', 'probe_generation', 'probe_until')
elseif observed_generation > 0 or blocked <= now then
  generation = generation + 1
  episodes = episodes + 1
  increment = 1
  redis.call('HDEL', KEYS[1], 'probe_owner', 'probe_token', 'probe_generation', 'probe_until')
end

-- Keep the attribution of the observation that supplied the effective
-- deadline. Equal or shorter evidence must not relabel the retained cooldown.
if deadline > blocked then
  blocked = deadline
  redis.call('HSET', KEYS[1], 'kind', ARGV[3], 'resource', ARGV[4], 'reason', ARGV[5])
end
redis.call('HSET', KEYS[1],
  'generation', generation,
  'episode_count', episodes,
  'blocked_until', blocked)
local ttl = tonumber(ARGV[8])
local minimum_ttl = blocked - now + 60000
if ttl < minimum_ttl then ttl = minimum_ttl end
local existing_ttl = redis.call('PTTL', KEYS[1])
if existing_ttl > ttl then ttl = existing_ttl end
redis.call('PEXPIRE', KEYS[1], ttl)
return state_result(increment, 1)
`)

var githubRateLimitClaimProbeScript = redis.NewScript(githubRateLimitStateResult + `
local now = tonumber(ARGV[1])
local generation = tonumber(redis.call('HGET', KEYS[1], 'generation') or '0')
local blocked = tonumber(redis.call('HGET', KEYS[1], 'blocked_until') or '0')
local probe_until = tonumber(redis.call('HGET', KEYS[1], 'probe_until') or '0')
if generation == 0 or blocked == 0 or blocked > now or probe_until > now then return state_result(0) end
local lease_until = tonumber(ARGV[4])
redis.call('HSET', KEYS[1], 'probe_owner', ARGV[2], 'probe_token', ARGV[3], 'probe_generation', generation, 'probe_until', lease_until)
redis.call('PEXPIRE', KEYS[1], tonumber(ARGV[5]))
return state_result(1)
`)

var githubRateLimitCompleteProbeScript = redis.NewScript(`
local generation = tonumber(redis.call('HGET', KEYS[1], 'generation') or '0')
local probe_generation = tonumber(redis.call('HGET', KEYS[1], 'probe_generation') or '0')
local probe_owner = redis.call('HGET', KEYS[1], 'probe_owner') or ''
local probe_token = redis.call('HGET', KEYS[1], 'probe_token') or ''
if generation ~= tonumber(ARGV[1]) or probe_generation ~= tonumber(ARGV[1]) or probe_owner ~= ARGV[2] or probe_token ~= ARGV[3] then
  return 0
end
if ARGV[4] == 'success' then
  redis.call('HSET', KEYS[1], 'blocked_until', 0)
  redis.call('HDEL', KEYS[1], 'kind', 'resource', 'reason')
else
  redis.call('HINCRBY', KEYS[1], 'generation', 1)
end
redis.call('HDEL', KEYS[1], 'probe_owner', 'probe_token', 'probe_generation', 'probe_until')
redis.call('PEXPIRE', KEYS[1], tonumber(ARGV[5]))
return 1
`)

var githubRateLimitReconcileScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local local_generation = tonumber(ARGV[2])
local generation = tonumber(redis.call('HGET', KEYS[1], 'generation') or '0')
local blocked = tonumber(redis.call('HGET', KEYS[1], 'blocked_until') or '0')
local local_blocked = tonumber(ARGV[4])
local probe_token = redis.call('HGET', KEYS[1], 'probe_token') or ''
local probe_until = tonumber(redis.call('HGET', KEYS[1], 'probe_until') or '0')
local local_probe_token = ARGV[9]
local changed = 0
local deadline_fenced_probe = 0

if local_generation < generation then
  -- Generation fences outcomes and leases, but it is not sufficient evidence
  -- to discard a later unexpired provider deadline retained by another
  -- controller after Redis rollback. Keep the remote generation while merging
  -- only stronger deadline evidence. A zero blocked_until is an authoritative
  -- completion tombstone and must not be resurrected.
  if blocked == 0 or local_blocked <= blocked or local_blocked <= now then return 0 end
  blocked = local_blocked
  redis.call('HSET', KEYS[1], 'blocked_until', blocked, 'kind', ARGV[5], 'resource', ARGV[6], 'reason', ARGV[7])
  redis.call('HDEL', KEYS[1], 'probe_owner', 'probe_token', 'probe_generation', 'probe_until')
  probe_token = ''
  probe_until = 0
  changed = 1
elseif local_generation == generation then
  if generation == 0 or blocked == 0 then return 0 end
  if local_blocked > blocked then
    blocked = local_blocked
    redis.call('HSET', KEYS[1], 'blocked_until', blocked, 'kind', ARGV[5], 'resource', ARGV[6], 'reason', ARGV[7])
	if blocked > now then
	  -- A probe admitted against the shorter deadline cannot prove recovery
	  -- after stronger evidence is restored. Clear its authority while the
	  -- restored cooldown itself continues to exclude competing probes.
	  redis.call('HDEL', KEYS[1], 'probe_owner', 'probe_token', 'probe_generation', 'probe_until')
	  probe_token = ''
	  probe_until = 0
	  deadline_fenced_probe = 1
	end
    changed = 1
  end
  local episodes = tonumber(redis.call('HGET', KEYS[1], 'episode_count') or '0')
  if tonumber(ARGV[3]) > episodes then
    redis.call('HSET', KEYS[1], 'episode_count', ARGV[3])
    changed = 1
  end
	if deadline_fenced_probe == 0 and probe_token == '' and local_probe_token ~= '' and tonumber(ARGV[10]) == generation then
    redis.call('HSET', KEYS[1], 'probe_owner', ARGV[8], 'probe_token', local_probe_token, 'probe_generation', ARGV[10], 'probe_until', ARGV[11])
    changed = 1
  end
else
	local episodes = tonumber(ARGV[3])
	local remote_episodes = tonumber(redis.call('HGET', KEYS[1], 'episode_count') or '0')
	if remote_episodes > episodes then episodes = remote_episodes end
	local effective_kind = ARGV[5]
	local effective_resource = ARGV[6]
	local effective_reason = ARGV[7]
	if blocked > local_blocked then
		local_blocked = blocked
		effective_kind = redis.call('HGET', KEYS[1], 'kind') or ''
		effective_resource = redis.call('HGET', KEYS[1], 'resource') or ''
		effective_reason = redis.call('HGET', KEYS[1], 'reason') or ''
	end
  redis.call('HSET', KEYS[1],
    'generation', local_generation,
	'episode_count', episodes,
    'blocked_until', local_blocked,
	'kind', effective_kind,
	'resource', effective_resource,
	'reason', effective_reason)
	if probe_token ~= '' and probe_until > now then
		-- Preserve a live post-rollback claim. Raising the generation fences its
		-- eventual result, while probe_until still prevents a second probe.
	elseif local_probe_token ~= '' and tonumber(ARGV[10]) == local_generation then
    redis.call('HSET', KEYS[1], 'probe_owner', ARGV[8], 'probe_token', local_probe_token, 'probe_generation', ARGV[10], 'probe_until', ARGV[11])
  else
    redis.call('HDEL', KEYS[1], 'probe_owner', 'probe_token', 'probe_generation', 'probe_until')
  end
  blocked = local_blocked
  changed = 1
end

if changed == 1 then
  local ttl = tonumber(ARGV[12])
  local retained_until = blocked
  if probe_until > retained_until then retained_until = probe_until end
  local minimum_ttl = retained_until - now + 60000
  if ttl < minimum_ttl then ttl = minimum_ttl end
  local existing_ttl = redis.call('PTTL', KEYS[1])
  if existing_ttl > ttl then ttl = existing_ttl end
  redis.call('PEXPIRE', KEYS[1], ttl)
end
return changed
`)

func (c *Client) GetGitHubRateLimitState(ctx context.Context, key string) (GitHubRateLimitState, error) {
	if c == nil || c.rdb == nil {
		return GitHubRateLimitState{}, errors.New("redis unavailable")
	}
	var values map[string]string
	err := c.doCommand(ctx, "github_rate_limit_get", func() error {
		var err error
		values, err = c.rdb.HGetAll(ctx, key).Result()
		return err
	})
	if err != nil {
		return GitHubRateLimitState{}, err
	}
	return decodeGitHubRateLimitState(values), nil
}

func (c *Client) ObserveGitHubRateLimit(ctx context.Context, key string, observation GitHubRateLimitObservation) (GitHubRateLimitState, bool, bool, error) {
	if c == nil || c.rdb == nil {
		return GitHubRateLimitState{}, false, false, errors.New("redis unavailable")
	}
	ttl := observation.TTL
	minimumTTL := observation.BlockedUntil.Sub(observation.Now) + time.Minute
	if ttl < minimumTTL {
		ttl = minimumTTL
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	var result []any
	err := c.doCommand(ctx, "github_rate_limit_observe", func() error {
		var err error
		result, err = githubRateLimitObserveScript.Run(ctx, c.rdb, []string{key},
			observation.Now.UnixMilli(), observation.BlockedUntil.UnixMilli(), observation.Kind,
			observation.Resource, observation.Reason, observation.ProbeGeneration, observation.ProbeToken, ttl.Milliseconds(), observation.ProviderDeadline).Slice()
		return err
	})
	if err != nil {
		return GitHubRateLimitState{}, false, false, err
	}
	if len(result) != 3 {
		return GitHubRateLimitState{}, false, false, errors.New("invalid github rate-limit observe result")
	}
	state, err := decodeGitHubRateLimitResult(result[0])
	applied := numberResult(result[2]) == 1
	return state, applied && numberResult(result[1]) == 1, applied, err
}

func (c *Client) ClaimGitHubRateLimitProbe(ctx context.Context, key, owner, token string, now, leaseUntil time.Time, ttl time.Duration) (GitHubRateLimitState, bool, error) {
	if c == nil || c.rdb == nil {
		return GitHubRateLimitState{}, false, errors.New("redis unavailable")
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	var result []any
	err := c.doCommand(ctx, "github_rate_limit_claim_probe", func() error {
		var err error
		result, err = githubRateLimitClaimProbeScript.Run(ctx, c.rdb, []string{key}, now.UnixMilli(), owner, token, leaseUntil.UnixMilli(), ttl.Milliseconds()).Slice()
		return err
	})
	if err != nil {
		return GitHubRateLimitState{}, false, err
	}
	if len(result) != 2 {
		return GitHubRateLimitState{}, false, errors.New("invalid github rate-limit probe result")
	}
	state, err := decodeGitHubRateLimitResult(result[0])
	return state, numberResult(result[1]) == 1, err
}

// ReconcileGitHubRateLimitState restores missing/older process-local state and
// merges a stronger retained provider deadline without rolling back the shared
// generation. A current-generation tombstone remains authoritative, and any
// probe admitted against weaker deadline evidence is fenced atomically.
func (c *Client) ReconcileGitHubRateLimitState(ctx context.Context, key string, state GitHubRateLimitState, now time.Time, ttl time.Duration) (GitHubRateLimitState, bool, error) {
	if c == nil || c.rdb == nil {
		return GitHubRateLimitState{}, false, errors.New("redis unavailable")
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	var applied int64
	err := c.doCommand(ctx, "github_rate_limit_reconcile", func() error {
		var err error
		applied, err = githubRateLimitReconcileScript.Run(ctx, c.rdb, []string{key},
			now.UnixMilli(), state.Generation, state.EpisodeCount, state.BlockedUntil.UnixMilli(),
			state.Kind, state.Resource, state.Reason, state.ProbeOwner, state.ProbeToken,
			state.ProbeGeneration, state.ProbeUntil.UnixMilli(), ttl.Milliseconds()).Int64()
		return err
	})
	if err != nil {
		return GitHubRateLimitState{}, false, err
	}
	reconciled, err := c.GetGitHubRateLimitState(ctx, key)
	return reconciled, applied == 1, err
}

func (c *Client) CompleteGitHubRateLimitProbe(ctx context.Context, key, owner, token string, generation int64, success bool, ttl time.Duration) (bool, error) {
	if c == nil || c.rdb == nil {
		return false, errors.New("redis unavailable")
	}
	result := "release"
	if success {
		result = "success"
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	var completed int64
	err := c.doCommand(ctx, "github_rate_limit_complete_probe", func() error {
		var err error
		completed, err = githubRateLimitCompleteProbeScript.Run(ctx, c.rdb, []string{key}, generation, owner, token, result, ttl.Milliseconds()).Int64()
		return err
	})
	return completed == 1, err
}

func decodeGitHubRateLimitResult(value any) (GitHubRateLimitState, error) {
	fields, ok := value.([]any)
	if !ok || len(fields) != 10 {
		return GitHubRateLimitState{}, errors.New("invalid github rate-limit state result")
	}
	return GitHubRateLimitState{
		Generation: numberResult(fields[0]), EpisodeCount: numberResult(fields[1]),
		BlockedUntil: millisResult(fields[2]), ProbeUntil: millisResult(fields[3]),
		ProbeOwner: stringResult(fields[4]), ProbeToken: stringResult(fields[5]), ProbeGeneration: numberResult(fields[6]),
		Kind: stringResult(fields[7]), Resource: stringResult(fields[8]), Reason: stringResult(fields[9]),
	}, nil
}

func decodeGitHubRateLimitState(values map[string]string) GitHubRateLimitState {
	return GitHubRateLimitState{
		Generation: parseInt64(values["generation"]), EpisodeCount: parseInt64(values["episode_count"]),
		BlockedUntil: millisResult(values["blocked_until"]), Kind: values["kind"], Resource: values["resource"], Reason: values["reason"],
		ProbeOwner: values["probe_owner"], ProbeToken: values["probe_token"], ProbeGeneration: parseInt64(values["probe_generation"]), ProbeUntil: millisResult(values["probe_until"]),
	}
}

func parseInt64(value string) int64 {
	parsed, _ := strconv.ParseInt(value, 10, 64)
	return parsed
}

func numberResult(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case string:
		return parseInt64(typed)
	case []byte:
		return parseInt64(string(typed))
	default:
		return 0
	}
}

func stringResult(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return ""
	}
}

func millisResult(value any) time.Time {
	millis := numberResult(value)
	if millis <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(millis).UTC()
}
