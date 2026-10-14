package sandboxauth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/assembledhq/143/internal/services/github/telemetry"
)

func runGH(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if os.Getenv(SocketEnvVar) == "" || os.Getenv("GH_TOKEN") != "" || os.Getenv("GITHUB_TOKEN") != "" || os.Getenv("GH_HOST") != "" && os.Getenv("GH_HOST") != "github.com" {
		return executeGH(context.Background(), ghBinaryPath, args, os.Environ(), stdin, stdout, stderr)
	}
	client := NewClient(os.Getenv(SocketEnvVar))
	observedCommand := shouldObserveGH(args, os.Environ())
	var response *Response
	var err error
	if observedCommand {
		response, err = client.GetObservedAPIToken(context.Background())
	} else {
		response, err = client.Get(context.Background(), ActionAPI)
	}
	if err != nil || response.Error != "" || response.Token == "" {
		fmt.Fprintln(stderr, "143-tools gh: GitHub credential unavailable")
		return 1
	}
	if response.ObservationID == "" {
		if observedCommand {
			fmt.Fprintln(stderr, "143-tools gh: GitHub request attribution unavailable")
		}
		return executeGH(context.Background(), ghBinaryPath, args, replaceGHEnv(os.Environ(), map[string]string{"GH_TOKEN": response.Token}), stdin, stdout, stderr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	report := make(chan HTTPObservation, 128)
	complete := make(chan bool, 1)
	go func() {
		failed := false
		for event := range report {
			callCtx, stop := context.WithTimeout(ctx, time.Second)
			err := client.Observe(callCtx, response.ObservationID, []HTTPObservation{event}, false)
			stop()
			if err != nil {
				failed = true
			}
		}
		callCtx, stop := context.WithTimeout(ctx, time.Second)
		err := client.Observe(callCtx, response.ObservationID, nil, true)
		stop()
		complete <- failed || err != nil
	}()
	observed := func(event HTTPObservation) {
		select {
		case report <- event:
		default:
			fmt.Fprintln(stderr, "143-tools gh: GitHub observation queue full")
		}
	}
	code := runObservedGH(ctx, ghBinaryPath, args, os.Environ(), response.Token, stdin, stdout, stderr, observed)
	close(report)
	select {
	case failed := <-complete:
		if failed {
			fmt.Fprintln(stderr, "143-tools gh: GitHub request attribution incomplete")
		}
	case <-time.After(2 * time.Second):
		fmt.Fprintln(stderr, "143-tools gh: GitHub request attribution timed out")
	}
	return code
}

func runObservedGH(ctx context.Context, binary string, args, env []string, token string, stdin io.Reader, stdout, stderr io.Writer, observe func(HTTPObservation), baseTransports ...http.RoundTripper) int {
	dir, err := os.MkdirTemp("/tmp", "143-gh-")
	if err != nil {
		fmt.Fprintln(stderr, "143-tools gh: create transport directory failed")
		return 1
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintln(stderr, "143-tools gh: transport cleanup failed")
		}
	}()
	socketPath := filepath.Join(dir, "api.sock")
	if err := prepareGHConfig(dir, socketPath, env); err != nil {
		fmt.Fprintln(stderr, "143-tools gh: prepare configuration failed")
		return 1
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		fmt.Fprintln(stderr, "143-tools gh: start transport failed")
		return 1
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		listener.Close()
		fmt.Fprintln(stderr, "143-tools gh: secure transport failed")
		return 1
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	var handlers sync.WaitGroup
	var handlerMu sync.Mutex
	closing := false
	var base http.RoundTripper = transport
	if len(baseTransports) > 0 {
		base = baseTransports[0]
	}
	proxy := ghProxy(token, base, observe)
	forwardCtx, stopForwarding := context.WithCancel(ctx)
	server := &http.Server{BaseContext: func(net.Listener) context.Context { return forwardCtx }, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerMu.Lock()
		if closing {
			handlerMu.Unlock()
			http.Error(w, "GitHub transport closing", http.StatusServiceUnavailable)
			return
		}
		handlers.Add(1)
		handlerMu.Unlock()
		defer handlers.Done()
		proxy.ServeHTTP(w, r)
	}), ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 1 << 20}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(stderr, "143-tools gh: transport stopped unexpectedly")
		}
	}()
	defer func() {
		handlerMu.Lock()
		closing = true
		handlerMu.Unlock()
		stopForwarding()
		if err := server.Close(); err != nil {
			fmt.Fprintln(stderr, "143-tools gh: close transport failed")
		}
		handlers.Wait()
	}()
	env = replaceGHEnv(env, map[string]string{"GH_TOKEN": token, "GH_CONFIG_DIR": dir, "GH_DEBUG": ""})
	return executeGH(ctx, binary, args, env, stdin, stdout, stderr)
}

func ghProxy(token string, transport http.RoundTripper, observe func(HTTPObservation)) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.Out.URL.Scheme = "https"
			request.Out.URL.Host = request.In.Host
			request.Out.Host = request.In.Host
		},
		Transport: ghObservationTransport{base: transport, observe: observe},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "GitHub transport failed", http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Host != "api.github.com" && request.Host != "uploads.github.com" || request.Method == "CONNECT" {
			http.Error(w, "unsupported GitHub transport host", http.StatusForbidden)
			return
		}
		auth := request.Header.Get("Authorization")
		unknown := subtle.ConstantTimeCompare([]byte(auth), []byte("token "+token)) != 1 && subtle.ConstantTimeCompare([]byte(auth), []byte("Bearer "+token)) != 1
		request = request.WithContext(context.WithValue(request.Context(), unknownGHPrincipal{}, unknown))
		proxy.ServeHTTP(w, request)
	})
}

type unknownGHPrincipal struct{}

type ghObservationTransport struct {
	base    http.RoundTripper
	observe func(HTTPObservation)
}

func (t ghObservationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	started := time.Now()
	response, err := t.base.RoundTrip(request)
	unknown, _ := request.Context().Value(unknownGHPrincipal{}).(bool)
	event := HTTPObservation{UnknownPrincipal: unknown, Method: request.Method, Route: telemetry.NormalizeExternalRoute(request.URL.Path), DurationMS: time.Since(started).Milliseconds()}
	if response != nil {
		event.Status = response.StatusCode
		event.Header = make(map[string]string)
		for _, key := range []string{"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Used", "X-RateLimit-Reset", "X-RateLimit-Resource", "Retry-After"} {
			if value := response.Header.Get(key); value != "" {
				if value := observationHeaders(map[string]string{key: value}).Get(key); value != "" {
					event.Header[key] = value
				}
			}
		}
	}
	t.observe(event)
	return response, err
}

func executeGH(ctx context.Context, binary string, args, env []string, stdin io.Reader, stdout, stderr io.Writer) int {
	command := exec.CommandContext(ctx, binary, args...)
	command.Env, command.Stdin, command.Stdout, command.Stderr = env, stdin, stdout, stderr
	if err := command.Start(); err != nil {
		fmt.Fprintln(stderr, "143-tools gh: start GitHub CLI failed")
		return 1
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	defer close(done)
	defer signal.Stop(signals)
	go func() {
		for {
			select {
			case sig := <-signals:
				if err := command.Process.Signal(sig); err != nil && !errors.Is(err, os.ErrProcessDone) {
					fmt.Fprintln(stderr, "143-tools gh: forward signal failed")
				}
			case <-done:
				return
			}
		}
	}()
	if err := command.Wait(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				return 128 + int(status.Signal())
			}
			return exit.ExitCode()
		}
		fmt.Fprintln(stderr, "143-tools gh: wait for GitHub CLI failed")
		return 1
	}
	return 0
}

func prepareGHConfig(dir, socketPath string, env []string) error {
	source, err := filepath.Abs(ghConfigSource(env))
	if err != nil {
		return err
	}
	config := make(map[string]any)
	if body, err := os.ReadFile(filepath.Join(source, "config.yml")); err == nil {
		if err := yaml.Unmarshal(body, &config); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if config == nil {
		config = make(map[string]any)
	}
	config["http_unix_socket"] = socketPath
	body, err := yaml.Marshal(config)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), body, 0o600); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == "config.yml" {
			continue
		}
		if err := os.Symlink(filepath.Join(source, entry.Name()), filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func replaceGHEnv(env []string, replacements map[string]string) []string {
	result := make([]string, 0, len(env)+len(replacements))
	for _, value := range env {
		key, _, _ := strings.Cut(value, "=")
		if _, replaced := replacements[key]; !replaced {
			result = append(result, value)
		}
	}
	for key, value := range replacements {
		result = append(result, key+"="+value)
	}
	return result
}

func ghConfigSource(env []string) string {
	values := make(map[string]string)
	for _, entry := range env {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[key] = value
		}
	}
	source := values["GH_CONFIG_DIR"]
	if source == "" {
		configHome := values["XDG_CONFIG_HOME"]
		if configHome == "" {
			configHome = filepath.Join(values["HOME"], ".config")
		}
		source = filepath.Join(configHome, "gh")
	}
	return source
}

func shouldObserveGH(args, env []string) bool {
	for i, arg := range args {
		if arg == "--hostname" && i+1 < len(args) && args[i+1] != "github.com" || strings.HasPrefix(arg, "--hostname=") && arg != "--hostname=github.com" {
			return false
		}
		if len(args) > 1 && args[0] == "api" && i == 1 && (strings.HasPrefix(arg, "https://") || strings.HasPrefix(arg, "http://")) {
			parsed, err := url.Parse(arg)
			if err != nil || parsed.Hostname() != "api.github.com" && parsed.Hostname() != "uploads.github.com" {
				return false
			}
		}
	}
	if len(args) > 0 {
		switch args[0] {
		case "config", "auth", "alias", "extension", "release", "run":
			return false
		}
	}
	body, err := os.ReadFile(filepath.Join(ghConfigSource(env), "config.yml"))
	if err != nil {
		return true
	}
	var config struct {
		Socket string `yaml:"http_unix_socket"`
	}
	if yaml.Unmarshal(body, &config) != nil {
		return true
	}
	return config.Socket == ""
}
