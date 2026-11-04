package cache

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

var githubCheckRefreshAdmissionScript = redis.NewScript(githubQuotaFamiliesScript + `
retain_families()
if lowest_remaining and lowest_remaining <= tonumber(ARGV[2]) then return 0 end
local admitted = redis.call('SET', KEYS[4], '1', 'PX', ARGV[4], 'NX')
return admitted and 1 or 0
`)

// AdmitGitHubCheckRefresh reserves the next slot if every observed core quota
// window has enough headroom. The slot expires even if the worker exits.
func (c *Client) AdmitGitHubCheckRefresh(ctx context.Context, key string, now time.Time, reserve int64, interval time.Duration) (bool, error) {
	keys := []string{key, githubQuotaFamiliesKey(key), githubQuotaAdmissionKey(key), key + ":check-refresh"}
	var admitted int
	err := c.doCommand(ctx, "github_check_refresh_admit", func() error {
		var err error
		admitted, err = githubCheckRefreshAdmissionScript.Run(ctx, c.rdb, keys,
			now.UnixMilli(), reserve, "", interval.Milliseconds(), 0, githubQuotaMaxResetAge.Milliseconds(), githubQuotaMaxFamilies).Int()
		return err
	})
	return admitted == 1 && err == nil, err
}
