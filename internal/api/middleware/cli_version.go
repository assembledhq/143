package middleware

import (
	"net/http"
	"strconv"
	"strings"
)

// cliUserAgentPrefix is how the 143-tools CLI identifies itself. The version
// segment after the slash is the CLI's embedded build version.
const cliUserAgentPrefix = "143-tools/"

// CLIUpdateRequiredCode is the error code returned with 426 when a CLI below
// the server's minimum supported version makes an authenticated request.
const CLIUpdateRequiredCode = "CLI_UPDATE_REQUIRED"

// CLIVersionGate rejects requests from a 143-tools CLI older than
// minSupported with 426 CLI_UPDATE_REQUIRED, giving breaking API changes a
// clean failure mode that names the fix (`143-tools update`) instead of a
// confusing downstream error.
//
// Enforcement only engages when both the configured minimum and the CLI's
// advertised version are orderable (dotted numerics, e.g. "1.4.0"). Git-SHA
// and "dev" builds are never blocked — comparing them is meaningless — so
// enforcement is effectively opt-in for deployments that tag CLI releases.
// Non-CLI user agents pass through untouched.
func CLIVersionGate(minSupported string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if minSupported != "" {
				if v, ok := strings.CutPrefix(r.UserAgent(), cliUserAgentPrefix); ok {
					if cmp, comparable := compareCLIVersions(v, minSupported); comparable && cmp < 0 {
						writeError(w, http.StatusUpgradeRequired, CLIUpdateRequiredCode,
							"this CLI version is no longer supported — run `143-tools update`")
						return
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// compareCLIVersions compares two dotted-numeric versions ("1.4.0" style,
// with an optional leading "v"). Returns (-1|0|1, true) when both parse, or
// (0, false) when either side is not orderable (git SHA, "dev", empty).
func compareCLIVersions(a, b string) (int, bool) {
	av, ok := parseDottedVersion(a)
	if !ok {
		return 0, false
	}
	bv, ok := parseDottedVersion(b)
	if !ok {
		return 0, false
	}
	for i := 0; i < len(av) || i < len(bv); i++ {
		var x, y int
		if i < len(av) {
			x = av[i]
		}
		if i < len(bv) {
			y = bv[i]
		}
		if x != y {
			if x < y {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

func parseDottedVersion(s string) ([]int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if s == "" {
		return nil, false
	}
	parts := strings.Split(s, ".")
	nums := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		nums = append(nums, n)
	}
	return nums, true
}
