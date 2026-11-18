package deploy_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResolveCaddyServiceIPs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		nslookup   string
		getent     string
		noNSLookup bool
		noGetent   bool
		lookupExit int
		want       string
		wantTool   string
		wantError  bool
	}{
		{
			name:     "prefers complete DNS answers over a single getent address",
			nslookup: "Server:\t192.0.2.53\nAddress:\t192.0.2.53:53\n\nNon-authoritative answer:\n\nNon-authoritative answer:\nName:\tapi\nAddress: 192.0.2.20\nName:\tapi\nAddress: 192.0.2.10\n",
			getent:   "192.0.2.10 api\n",
			want:     "192.0.2.10\n192.0.2.20\n", wantTool: "nslookup",
		},
		{
			name:     "numbered BusyBox answers exclude resolver address and IPv6",
			nslookup: "Server: dns.example\nAddress 1: 192.0.2.53 dns.example\n\nName: api\nAddress 1: 192.0.2.20 api\nAddress 2: 2001:db8::20 api\nAddress 3: 192.0.2.10 api\nAddress 4: 192.0.2.20 api\n",
			want:     "192.0.2.10\n192.0.2.20\n", wantTool: "nslookup",
		},
		{
			name:     "unnumbered resolver address is not an answer",
			nslookup: "Server: dns.example\nAddress: 192.0.2.53\n\nName: api\nAddress: 192.0.2.20\n",
			want:     "192.0.2.20\n", wantTool: "nslookup",
		},
		{
			name:     "a later resolver header resets the answer section",
			nslookup: "Name: api\nAddress: 192.0.2.20\nServer: dns.example\nAddress: 192.0.2.53\n",
			want:     "192.0.2.20\n", wantTool: "nslookup",
		},
		{
			name:     "no answers never falls back to getent",
			nslookup: "Server: dns.example\nAddress: 192.0.2.53\n",
			getent:   "192.0.2.20 api\n", wantTool: "nslookup",
		},
		{
			name:     "lookup failure discards partial answers without fallback",
			nslookup: "Name: api\nAddress: 192.0.2.20\n", lookupExit: 1,
			getent: "192.0.2.20 api\n", wantTool: "nslookup", wantError: true,
		},
		{
			name:       "getent remains available only when nslookup is absent",
			noNSLookup: true, getent: "2001:db8::20 api\n192.0.2.20 api\n192.0.2.10 api\n192.0.2.20 api\n",
			want: "192.0.2.10\n192.0.2.20\n", wantTool: "getent",
		},
		{
			name:       "missing resolver tools fail closed",
			noNSLookup: true, noGetent: true, wantError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			script, err := os.ReadFile("scripts/deploy.sh")
			require.NoError(t, err, "should read the deployed DNS discovery implementation")
			function := extractShellFunction(t, string(script), "resolve_caddy_service_ips", "wait_caddy_upstream_discovery")
			dir := t.TempDir()
			innerBin := filepath.Join(dir, "inner-bin")
			require.NoError(t, os.Mkdir(innerBin, 0o755), "resolver tools should be isolated from the machine PATH")
			trace := filepath.Join(dir, "trace")
			require.NoError(t, os.WriteFile(trace, nil, 0o600), "should initialize the resolver invocation trace")
			for _, tool := range []struct {
				name   string
				output string
				absent bool
				exit   int
			}{
				{name: "nslookup", output: tt.nslookup, absent: tt.noNSLookup, exit: tt.lookupExit},
				{name: "getent", output: tt.getent, absent: tt.noGetent},
			} {
				if tool.absent {
					continue
				}
				stub := "#!/bin/sh\nprintf '%s\\n' " + shellQuote(tool.name) + " >>\"$DNS_TRACE\"\nprintf '%s' " + shellQuote(tool.output) + fmt.Sprintf("\nexit %d\n", tool.exit)
				require.NoError(t, os.WriteFile(filepath.Join(innerBin, tool.name), []byte(stub), 0o755), "should create the isolated resolver fixture")
			}
			// Execute the actual sh -c payload rather than returning fake parsed
			// addresses from Docker: tool selection and parsing must both run.
			docker := `#!/bin/sh
[ "$1" = exec ] && [ "$2" = caddy-test ] && [ "$3" = sh ] || exit 99
shift 3
PATH="$DNS_INNER_BIN" exec /bin/sh "$@"
`
			require.NoError(t, os.WriteFile(filepath.Join(dir, "docker"), []byte(docker), 0o755), "should create the Docker exec fixture")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "bash", "-c", "set -euo pipefail\n"+function+"\nresolve_caddy_service_ips caddy-test api\n")
			command.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "DNS_INNER_BIN="+innerBin, "DNS_TRACE="+trace)
			output, err := command.CombinedOutput()
			require.NoError(t, ctx.Err(), "DNS discovery should finish within its test deadline")
			if tt.wantError {
				require.Error(t, err, "unavailable DNS evidence should fail discovery")
			} else {
				require.NoError(t, err, "available resolver output should be parsed successfully: %s", output)
			}
			require.Equal(t, tt.want, string(output), "discovery should return exactly the unique service IPv4 answers")
			calls, err := os.ReadFile(trace)
			require.NoError(t, err, "should read the resolver invocation trace")
			require.Equal(t, tt.wantTool, strings.TrimSpace(string(calls)), "only the appropriate resolver should run")
		})
	}
}

func TestCaddyUpstreamDiscoveryRequiresDNSAndHealth(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		addresses  string
		healthExit int
		wantError  bool
		wantProbe  string
	}{
		{name: "healthy candidate in DNS", addresses: "192.0.2.10\n192.0.2.20\n", wantProbe: "health\n"},
		{name: "candidate absent from DNS", addresses: "192.0.2.10\n", wantError: true},
		{name: "candidate in DNS but unhealthy", addresses: "192.0.2.10\n192.0.2.20\n", healthExit: 1, wantError: true, wantProbe: "health\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			script, err := os.ReadFile("scripts/deploy.sh")
			require.NoError(t, err, "should read the deployed upstream acceptance gate")
			function := extractShellFunction(t, string(script), "wait_caddy_upstream_discovery", "preview_rpc_auth_preflight")
			trace := filepath.Join(t.TempDir(), "trace")
			require.NoError(t, os.WriteFile(trace, nil, 0o600), "should initialize the health probe trace")
			output, err := runWorkerShell(t, function, `
COMPOSE_FILE=app.yml
CADDY_UPSTREAM_DISCOVERY_ATTEMPTS=1
sleep() { :; }
resolve_caddy_service_ips() { printf '%s' `+shellQuote(tt.addresses)+`; }
docker() {
  case "$1" in
    compose) printf 'caddy-test\n' ;;
    inspect) printf '192.0.2.20\n' ;;
    exec) printf 'health\n' >>`+shellQuote(trace)+`; return `+fmt.Sprint(tt.healthExit)+` ;;
    *) return 99 ;;
  esac
}
wait_caddy_upstream_discovery api candidate-test
`)
			if tt.wantError {
				require.Error(t, err, "missing DNS or health evidence must block old-container retirement: %s", output)
			} else {
				require.NoError(t, err, "a candidate with both DNS and health evidence should pass discovery: %s", output)
			}
			probes, err := os.ReadFile(trace)
			require.NoError(t, err, "should read the health probe trace")
			require.Equal(t, tt.wantProbe, string(probes), "health should be probed only after the candidate appears in DNS")
		})
	}
}
