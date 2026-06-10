package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/assembledhq/143/internal/version"
)

// runUpdate re-downloads the binary for this platform from the configured
// server and atomically replaces the running executable. The server sets
// X-Checksum-Sha256 on the download; the replacement is verified against it
// before the swap.
func runUpdate(_ []string, stdout, stderr io.Writer) int {
	cfg, err := LoadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	if cfg.ServerURL == "" {
		fmt.Fprintln(stderr, "error: no server configured — run `143-tools login --server <url>` first")
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Version skew check: skip the download when already current.
	client := NewClient(cfg)
	var versionResp struct {
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := client.Do(ctx, http.MethodGet, "/api/v1/cli/version", nil, &versionResp); err == nil {
		if versionResp.Data.Version != "" && versionResp.Data.Version == version.BuildSHA {
			fmt.Fprintf(stdout, "Already up to date (%s).\n", version.BuildSHA)
			return 0
		}
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "error: locate current binary: %s\n", err)
		return 1
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		fmt.Fprintf(stderr, "error: resolve current binary: %s\n", err)
		return 1
	}

	downloadURL := fmt.Sprintf("%s/download/143-tools/%s/%s", cfg.ServerURL, runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(stdout, "Downloading %s ...\n", downloadURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	req.Header.Set("User-Agent", "143-tools/"+version.BuildSHA)
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req) // #nosec G704 -- server URL comes from the user's own config
	if err != nil {
		fmt.Fprintf(stderr, "error: download failed: %s\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "error: download failed: HTTP %d\n", resp.StatusCode)
		return 1
	}

	// Stage next to the target so the final rename is same-filesystem atomic.
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".143-tools-update-*")
	if err != nil {
		fmt.Fprintf(stderr, "error: stage update: %s\n", err)
		return 1
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hasher), resp.Body); err != nil {
		tmp.Close()
		fmt.Fprintf(stderr, "error: download failed: %s\n", err)
		return 1
	}
	if err := tmp.Close(); err != nil {
		fmt.Fprintf(stderr, "error: stage update: %s\n", err)
		return 1
	}

	if expected := resp.Header.Get("X-Checksum-Sha256"); expected != "" {
		if actual := hex.EncodeToString(hasher.Sum(nil)); actual != expected {
			fmt.Fprintf(stderr, "error: checksum mismatch (expected %s, got %s) — aborting update\n", expected, actual)
			return 1
		}
	} else {
		fmt.Fprintln(stderr, "warning: server sent no checksum header; installing unverified")
	}

	if err := os.Chmod(tmpPath, 0o755); err != nil { // #nosec G302 -- executable binary
		fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	if err := os.Rename(tmpPath, exe); err != nil {
		fmt.Fprintf(stderr, "error: replace binary: %s\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Updated %s\n", exe)
	return 0
}
