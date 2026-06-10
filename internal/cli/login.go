package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// loginWait bounds how long `143-tools login` waits for the browser flow.
const loginWait = 5 * time.Minute

type loginOptions struct {
	server    string
	join      string
	noBrowser bool
}

// callbackResult is what the loopback listener receives from the browser
// redirect: a one-time code on success, or an error code + message.
type callbackResult struct {
	code    string
	errCode string
	errMsg  string
}

func runLogin(args []string, stdout, stderr io.Writer) int {
	opts := loginOptions{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--server":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "error: --server requires a value")
				return 1
			}
			i++
			opts.server = args[i]
		case "--join":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "error: --join requires a value")
				return 1
			}
			i++
			opts.join = args[i]
		case "--no-browser":
			opts.noBrowser = true
		case "--help", "-h":
			fmt.Fprintln(stdout, "Usage: 143-tools login [--server URL] [--join TOKEN] [--no-browser]")
			return 0
		default:
			fmt.Fprintf(stderr, "error: unknown flag %q\n", args[i])
			return 1
		}
	}

	cfg, err := LoadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	if opts.server != "" {
		cfg.ServerURL = strings.TrimRight(opts.server, "/")
	}
	if cfg.ServerURL == "" {
		fmt.Fprintln(stderr, "error: no server configured — run `143-tools login --server https://your-143-server`")
		return 1
	}
	join := opts.join
	if join == "" {
		join = cfg.PendingJoinToken
	}

	// PKCE-style binding: the challenge rides through the browser, the
	// verifier never leaves this process. Only the holder of the verifier
	// can redeem the one-time code.
	var verifierRaw [32]byte
	if _, err := rand.Read(verifierRaw[:]); err != nil {
		fmt.Fprintf(stderr, "error: generate verifier: %s\n", err)
		return 1
	}
	verifier := hex.EncodeToString(verifierRaw[:])
	challengeSum := sha256.Sum256([]byte(verifier))
	challenge := hex.EncodeToString(challengeSum[:])

	// Loopback listener on a random port. 127.0.0.1 hardcoded — never
	// "localhost", which can resolve unexpectedly.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(stderr, "error: start loopback listener: %s\n", err)
		return 1
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	device, _ := os.Hostname()

	startURL := buildStartURL(cfg.ServerURL, port, challenge, device, join)
	if opts.noBrowser {
		fmt.Fprintf(stdout, "Open this URL in your browser to log in:\n\n  %s\n\n", startURL)
	} else {
		fmt.Fprintln(stdout, "Opening your browser to complete login ...")
		if err := openBrowser(startURL); err != nil {
			fmt.Fprintf(stdout, "Could not open a browser automatically. Open this URL:\n\n  %s\n\n", startURL)
		}
	}

	result, err := waitForCallback(listener, loginWait)
	if err != nil {
		fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	if result.errCode != "" {
		msg := result.errMsg
		if msg == "" {
			msg = result.errCode
		}
		fmt.Fprintf(stderr, "login failed: %s\n", msg)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	exchange, err := exchangeCode(ctx, cfg.ServerURL, result.code, verifier)
	if err != nil {
		fmt.Fprintf(stderr, "error: token exchange failed: %s\n", err)
		return 1
	}

	cfg.Token = exchange.Token
	cfg.PendingJoinToken = ""
	if exchange.Org != nil {
		cfg.OrgID = exchange.Org.ID
	}
	if err := SaveConfig(cfg); err != nil {
		fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}

	// The new token is confirmed working (the exchange minted it and the
	// config write succeeded) — now retire older tokens for this device so
	// the "CLI sessions" list stays one row per device.
	revokeStaleDeviceTokens(ctx, cfg, exchange.TokenID, device, stderr)

	identity := exchange.User.Name
	if exchange.User.GitHubLogin != nil && *exchange.User.GitHubLogin != "" {
		identity = "@" + *exchange.User.GitHubLogin
	}
	if exchange.Org != nil {
		fmt.Fprintf(stdout, "Logged in as %s (%s)\n", identity, exchange.Org.Name)
	} else {
		fmt.Fprintf(stdout, "Logged in as %s\n", identity)
	}
	return 0
}

func buildStartURL(server string, port int, challenge, device, join string) string {
	params := url.Values{
		"port":      {fmt.Sprintf("%d", port)},
		"challenge": {challenge},
	}
	if device != "" {
		params.Set("device", device)
	}
	if join != "" {
		params.Set("join", join)
	}
	return server + "/api/v1/auth/cli/start?" + params.Encode()
}

// waitForCallback serves exactly one /callback request on the loopback
// listener and returns its parameters. The handler responds with a tiny
// "you can close this tab" page so the browser side ends cleanly.
func waitForCallback(listener net.Listener, timeout time.Duration) (callbackResult, error) {
	resultCh := make(chan callbackResult, 1)

	server := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/callback" {
				http.NotFound(w, r)
				return
			}
			q := r.URL.Query()
			res := callbackResult{
				code:    q.Get("code"),
				errCode: q.Get("error"),
				errMsg:  q.Get("message"),
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if res.errCode != "" {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, loginPage("Login failed", "Return to your terminal for details."))
			} else {
				fmt.Fprint(w, loginPage("You're in", "You can close this tab and return to your terminal."))
			}
			select {
			case resultCh <- res:
			default: // a second hit on /callback after the first resolves — drop it
			}
		}),
	}
	go func() { _ = server.Serve(listener) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	select {
	case res := <-resultCh:
		return res, nil
	case <-time.After(timeout):
		return callbackResult{}, errors.New("timed out waiting for browser login (5 minutes) — re-run `143-tools login`")
	}
}

func loginPage(title, body string) string {
	return fmt.Sprintf(`<!doctype html><meta charset="utf-8"><title>143 CLI login</title>
<body style="font-family: system-ui, sans-serif; max-width: 40rem; margin: 4rem auto;">
<h1 style="font-size:1.2rem">%s</h1><p>%s</p></body>`, title, body)
}

type exchangeUser struct {
	ID          string  `json:"id"`
	Email       string  `json:"email"`
	Name        string  `json:"name"`
	GitHubLogin *string `json:"github_login"`
}

type exchangeOrg struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type exchangeResponse struct {
	Token       string       `json:"token"`
	TokenID     string       `json:"token_id"`
	TokenPrefix string       `json:"token_prefix"`
	ExpiresAt   time.Time    `json:"expires_at"`
	User        exchangeUser `json:"user"`
	Org         *exchangeOrg `json:"org"`
}

func exchangeCode(ctx context.Context, server, code, verifier string) (*exchangeResponse, error) {
	client := NewClient(Config{ServerURL: server})
	var resp struct {
		Data exchangeResponse `json:"data"`
	}
	err := client.Do(ctx, http.MethodPost, "/api/v1/auth/cli/exchange",
		map[string]string{"code": code, "verifier": verifier}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Data.Token == "" {
		return nil, errors.New("server returned no token")
	}
	return &resp.Data, nil
}

// revokeStaleDeviceTokens lists the user's CLI tokens and revokes the ones
// sharing this device name, keeping the freshly-minted one. Best-effort:
// failures leave harmless extra rows in the sessions list, never a broken
// login.
func revokeStaleDeviceTokens(ctx context.Context, cfg Config, keepTokenID, device string, stderr io.Writer) {
	if device == "" {
		return
	}
	client := NewClient(cfg)
	var resp struct {
		Data []struct {
			ID         string `json:"id"`
			DeviceName string `json:"device_name"`
		} `json:"data"`
	}
	if err := client.Do(ctx, http.MethodGet, "/api/v1/auth/cli-tokens", nil, &resp); err != nil {
		fmt.Fprintf(stderr, "note: could not list older CLI sessions: %s\n", err)
		return
	}
	for _, t := range resp.Data {
		if t.DeviceName != device || t.ID == keepTokenID {
			continue
		}
		if err := client.Do(ctx, http.MethodDelete, "/api/v1/auth/cli-tokens/"+t.ID, nil, nil); err != nil {
			fmt.Fprintf(stderr, "note: could not revoke older CLI session %s: %s\n", t.ID, err)
		}
	}
}

// openBrowser launches the platform's default browser at url.
func openBrowser(targetURL string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", targetURL).Start()
	case "linux":
		return exec.Command("xdg-open", targetURL).Start()
	default:
		return fmt.Errorf("unsupported platform %s", runtime.GOOS)
	}
}
