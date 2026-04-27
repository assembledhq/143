// Package sandboxauth defines the wire protocol that sandboxed coding agents
// use to obtain a fresh GitHub credential from the host. The host opens a
// per-session Unix domain socket; the in-sandbox `143-tools` binary dials it
// over a bind-mount and exchanges newline-delimited JSON.
//
// Protocol shape (one request per connection):
//
//	→ {"op":"get","action":"push"}\n
//	← {"token":"...","username":"x-access-token","identity":"user","login":"alice","expires_at":"..."}\n
//
// The socket path is the auth boundary: anything that can open it is trusted
// to ask for the session's resolved GitHub identity. Inside the sandbox, the
// bind-mount is the only way to reach the socket; outside, filesystem
// permissions on the socket directory keep other tenants out.
//
// Why newline-delimited JSON: simpler than length-prefixed framing, plays
// nicely with the git credential-helper protocol's own line-oriented format,
// and lets us debug a session's auth path with `socat - UNIX-CONNECT:...`
// from a privileged shell on the host.
package sandboxauth

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// SocketEnvVar is the environment variable that points the in-sandbox helper
// at the auth socket. Bound by the orchestrator at container-create time and
// read by `143-tools git-credential` / `143-tools auth-token`.
const SocketEnvVar = "_143_AUTH_SOCK"

// SandboxSocketDir is the canonical in-sandbox directory the orchestrator
// bind-mounts the per-session host socket directory onto. We mount a
// directory rather than the socket file directly because Linux file bind-
// mounts pin the source inode at mount time: if the host socket is
// recreated mid-session (e.g. across turn boundaries when a preview holds
// the container open), an in-container connect via a file bind-mount would
// keep dialing the original, now-orphaned inode. Directory bind-mounts
// resolve filenames at lookup time, so the agent always sees the live
// socket inside the mounted dir.
//
// Lives under /run rather than /tmp so it stays out of noexec/exec scratch
// tmpfses and is unambiguously sandbox-internal infrastructure (not user
// data).
const SandboxSocketDir = "/run/143-auth"

// SandboxSocketPath is the canonical in-sandbox path the orchestrator binds
// the per-session host socket to. The file lives inside SandboxSocketDir
// so the directory mount above carries it through without inode pinning.
const SandboxSocketPath = SandboxSocketDir + "/sock"

// SocketFileName is the constant filename of the AF_UNIX socket inside the
// per-session directory. Exported so the host server, the in-sandbox
// helpers, and tests stay in lockstep on the path scheme.
const SocketFileName = "sock"

// Action discriminates what kind of GitHub call the in-sandbox tool is about
// to make. Today the host-side handler logs it for audit and the resolver
// is action-agnostic — both push and api get the same token. Kept on the
// wire so we can later differentiate (e.g. issue scoped tokens for push
// vs read-only tokens for api) without a protocol break.
type Action string

const (
	ActionPush Action = "push" // git push / fetch / clone via HTTPS
	ActionAPI  Action = "api"  // gh / direct GitHub REST API calls
)

// Op identifies what the client is asking the host to do.
type Op string

const (
	// OpGet asks the host to resolve and return a fresh credential.
	OpGet Op = "get"
)

// Identity classifies whose credential the host returned. Mirrors
// identity.Source — duplicated here so this package has no dependency on
// the github subtree, which keeps the wire format reusable from
// cmd/tools/main.go without dragging the whole import graph.
type Identity string

const (
	IdentityUser Identity = "user"
	IdentityApp  Identity = "app"
)

// Request is the in-sandbox client's call.
type Request struct {
	Op     Op     `json:"op"`
	Action Action `json:"action"`
}

// Response is the host handler's reply. On success Token is non-empty and
// Error is "". On failure Error carries a human-readable message and the
// caller should treat the response as if no credential were available.
type Response struct {
	Token     string    `json:"token,omitempty"`
	Username  string    `json:"username,omitempty"`
	Identity  Identity  `json:"identity,omitempty"`
	Login     string    `json:"login,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// DefaultUsername is the username git uses when authenticating to GitHub
// with a token-style password. Constant per GitHub's docs; centralized here
// so the credential helper, push script, and tests stay in lockstep.
const DefaultUsername = "x-access-token"

// DialTimeout bounds how long an in-sandbox call will wait to reach the host
// socket. Generous enough for a cold container's first credential request,
// short enough that a misconfigured socket fails quickly.
const DialTimeout = 5 * time.Second

// CallTimeout bounds the entire round-trip (dial + write + read). The host
// resolver may make outbound HTTP calls (refresh, repo-access probe), so
// allow more headroom than DialTimeout.
const CallTimeout = 30 * time.Second

// Client dials the per-session socket and runs one request/response. It
// opens a fresh connection per call — git's credential helper invokes us
// once per push, so connection reuse buys nothing and complicates the
// host-side handler (which today does one Resolve per Accept).
type Client struct {
	SocketPath string
}

// NewClient constructs a Client that talks to socketPath. socketPath is
// usually $_143_AUTH_SOCK as set by the orchestrator.
func NewClient(socketPath string) *Client {
	return &Client{SocketPath: socketPath}
}

// Get asks the host to resolve a fresh GitHub credential for the given
// action. Returns the response payload (which may carry a non-empty Error
// field) and any transport error.
func (c *Client) Get(ctx context.Context, action Action) (*Response, error) {
	if c.SocketPath == "" {
		return nil, errors.New("sandboxauth: socket path is empty (set " + SocketEnvVar + ")")
	}
	dialer := net.Dialer{Timeout: DialTimeout}
	conn, err := dialer.DialContext(ctx, "unix", c.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("sandboxauth: dial %s: %w", c.SocketPath, err)
	}
	defer conn.Close()

	// Bound the entire exchange. Without this a hung host could block git
	// indefinitely and the user would only see "git push hangs forever".
	deadline := time.Now().Add(CallTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)

	req := Request{Op: OpGet, Action: action}
	if err := json.NewEncoder(conn).Encode(&req); err != nil {
		return nil, fmt.Errorf("sandboxauth: write request: %w", err)
	}

	// Newline-delimited; read one line so we don't wait for EOF on the
	// half-close (the host may keep its end open momentarily for logging).
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("sandboxauth: read response: %w", err)
	}
	if len(line) == 0 {
		return nil, fmt.Errorf("sandboxauth: empty response")
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("sandboxauth: decode response: %w", err)
	}
	return &resp, nil
}
