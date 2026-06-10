package handlers

import (
	"bufio"
	_ "embed"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"text/template"

	"github.com/go-chi/chi/v5"

	"github.com/assembledhq/143/internal/version"
)

//go:embed assets/install.sh.tmpl
var installScriptTemplate string

// joinTokenPathPattern is the syntactic gate for the /install/{join_token}
// path segment. This is untrusted input templated into a script the user
// pipes to `sh`, so anything outside this charset is treated as a potential
// shell-injection vector and 404s. Existence/validity of the token is
// deliberately NOT checked here — a revoked link should still install the
// binary; the join is validated at login.
var joinTokenPathPattern = regexp.MustCompile(`^143j_[A-Za-z0-9]{12,64}$`)

// cliPlatforms whitelists the {os}/{arch} pairs the download route serves.
// Must stay in sync with the `make build-cli` cross-compile matrix.
var cliPlatforms = map[string]bool{
	"darwin/amd64": true,
	"darwin/arm64": true,
	"linux/amd64":  true,
	"linux/arm64":  true,
}

// CLIDistributionHandler serves the 143-tools installer script, the
// cross-compiled binaries, and the CLI version endpoint. Binaries live as
// static files under distDir (baked into the server image at /opt/143/cli);
// the install script is a Go template so the server can inject its own base
// URL — and, on /install/{join_token}, the join token — making install +
// auth a single zero-config command.
type CLIDistributionHandler struct {
	baseURL      string
	distDir      string
	minSupported string
	tmpl         *template.Template

	// checksums caches the parsed checksums.txt (filename → sha256 hex).
	// Loaded once on first download: the dist dir is immutable for the
	// lifetime of the server image.
	checksumsOnce sync.Once
	checksums     map[string]string
}

func NewCLIDistributionHandler(baseURL, distDir, minSupportedVersion string) *CLIDistributionHandler {
	tmpl := template.Must(template.New("install.sh").Parse(installScriptTemplate))
	return &CLIDistributionHandler{
		baseURL:      strings.TrimRight(baseURL, "/"),
		distDir:      distDir,
		minSupported: minSupportedVersion,
		tmpl:         tmpl,
	}
}

// InstallScript serves the templated installer. With a {join_token} path
// segment the token is templated into the config-write step; the segment is
// syntactically validated (see joinTokenPathPattern) because it ends up
// inside a script piped to sh.
func (h *CLIDistributionHandler) InstallScript(w http.ResponseWriter, r *http.Request) {
	joinToken := chi.URLParam(r, "join_token")
	if joinToken != "" && !joinTokenPathPattern.MatchString(joinToken) {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = h.tmpl.Execute(w, map[string]string{
		"ServerURL": h.baseURL,
		"JoinToken": joinToken,
	})
}

// DownloadBinary streams the 143-tools binary for the requested platform and
// sets X-Checksum-Sha256 so clients (the `update` command) can verify without
// a second round-trip. Unknown platform pairs 404.
func (h *CLIDistributionHandler) DownloadBinary(w http.ResponseWriter, r *http.Request) {
	osName := chi.URLParam(r, "os")
	arch := chi.URLParam(r, "arch")
	if !cliPlatforms[osName+"/"+arch] {
		http.NotFound(w, r)
		return
	}

	name := "143-tools-" + osName + "-" + arch
	path := filepath.Join(h.distDir, name)
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	if sum := h.checksumFor(name); sum != "" {
		w.Header().Set("X-Checksum-Sha256", sum)
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="143-tools"`)
	http.ServeFile(w, r, path)
}

// Checksums serves the checksums.txt the installer verifies against.
func (h *CLIDistributionHandler) Checksums(w http.ResponseWriter, r *http.Request) {
	path := filepath.Join(h.distDir, "checksums.txt")
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	http.ServeFile(w, r, path)
}

// Version reports the server's build version and the minimum CLI version it
// supports. min_supported is enforced by middleware.CLIVersionGate, not just
// advisory — see the config field doc for the orderable-version caveat.
func (h *CLIDistributionHandler) Version(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]string{
			"version":       version.BuildSHA,
			"min_supported": h.minSupported,
		},
	})
}

func (h *CLIDistributionHandler) checksumFor(name string) string {
	h.checksumsOnce.Do(func() {
		h.checksums = parseChecksumsFile(filepath.Join(h.distDir, "checksums.txt"))
	})
	return h.checksums[name]
}

// parseChecksumsFile reads a sha256sum-format file ("<hex>  <name>" per
// line, with shasum's optional binary-mode "*" prefix on the name). Missing
// or malformed files yield an empty map — the download route then simply
// omits the checksum header.
func parseChecksumsFile(path string) map[string]string {
	sums := make(map[string]string)
	f, err := os.Open(path) // #nosec G304 -- path is server-config dist dir, not user input
	if err != nil {
		return sums
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || len(fields[0]) != 64 {
			continue
		}
		sums[strings.TrimPrefix(fields[1], "*")] = fields[0]
	}
	return sums
}
