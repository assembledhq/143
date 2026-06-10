package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

func newCLIDistTestRouter(t *testing.T, distDir string) http.Handler {
	t.Helper()
	h := NewCLIDistributionHandler("https://143.example.com", distDir, "1.0.0")
	r := chi.NewRouter()
	r.Get("/install.sh", h.InstallScript)
	r.Get("/install/{join_token}", h.InstallScript)
	r.Get("/download/143-tools/checksums.txt", h.Checksums)
	r.Get("/download/143-tools/{os}/{arch}", h.DownloadBinary)
	r.Get("/api/v1/cli/version", h.Version)
	return r
}

func writeCLIDistFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// sha256("hello\n")
	const helloSum = "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"
	for _, name := range []string{"143-tools-darwin-arm64", "143-tools-linux-amd64"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("hello\n"), 0o755))
	}
	checksums := helloSum + "  143-tools-darwin-arm64\n" + helloSum + "  143-tools-linux-amd64\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(checksums), 0o644))
	return dir
}

func TestInstallScriptTemplatesServerURL(t *testing.T) {
	t.Parallel()
	router := newCLIDistTestRouter(t, t.TempDir())

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/install.sh", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, `SERVER_URL='https://143.example.com'`)
	require.Contains(t, body, `JOIN_TOKEN=''`, "tokenless install should template an empty join token")
	// The dropped-connection guard: nothing executes unless the final line arrived.
	require.True(t, strings.HasSuffix(strings.TrimSpace(body), `main "$@"`),
		"script body must be wrapped in a function invoked on the last line")
}

func TestInstallScriptTemplatesValidJoinToken(t *testing.T) {
	t.Parallel()
	router := newCLIDistTestRouter(t, t.TempDir())

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/install/143j_Ab3x9kQ2mP4r", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `JOIN_TOKEN='143j_Ab3x9kQ2mP4r'`)
}

func TestInstallScriptRejectsMalformedJoinTokens(t *testing.T) {
	t.Parallel()
	router := newCLIDistTestRouter(t, t.TempDir())

	// Each of these is untrusted input that would be templated into a script
	// piped to sh — anything outside the strict charset must 404.
	malformed := []string{
		"/install/notatoken",
		"/install/143j_short",                      // under 12 chars after the prefix
		"/install/143j_has'quote12",                // quote breaks out of single-quoting
		"/install/143j_has%24dollar12",             // $ (encoded)
		"/install/143j_" + strings.Repeat("a", 65), // over 64 chars
		"/install/143u_AbcdefGhijkl",               // wrong prefix (CLI token, not join token)
	}
	for _, path := range malformed {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusNotFound, rec.Code, "expected 404 for %s", path)
	}
}

func TestDownloadBinaryServesWhitelistedPlatformsWithChecksumHeader(t *testing.T) {
	t.Parallel()
	router := newCLIDistTestRouter(t, writeCLIDistFixture(t))

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/download/143-tools/darwin/arm64", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "hello\n", rec.Body.String())
	require.Equal(t,
		"5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03",
		rec.Header().Get("X-Checksum-Sha256"))

	for _, path := range []string{
		"/download/143-tools/windows/amd64",
		"/download/143-tools/darwin/mips",
		"/download/143-tools/darwin/..", // traversal shapes must not reach the filesystem
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusNotFound, rec.Code, "expected 404 for %s", path)
	}
}

func TestDownloadBinaryMissingDistDir404s(t *testing.T) {
	t.Parallel()
	router := newCLIDistTestRouter(t, filepath.Join(t.TempDir(), "does-not-exist"))

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/download/143-tools/linux/amd64", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/download/143-tools/checksums.txt", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestCLIVersionEndpoint(t *testing.T) {
	t.Parallel()
	router := newCLIDistTestRouter(t, t.TempDir())

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/cli/version", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"min_supported":"1.0.0"`)
	require.Contains(t, rec.Body.String(), `"version"`)
}
