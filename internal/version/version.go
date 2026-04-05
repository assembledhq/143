// Package version exposes build metadata injected at compile time via ldflags.
//
// The server deploy SHA pins all embedded prompt templates (since they're
// identical across orgs and change only on deploy). Given a SHA, the exact
// template text can be reconstructed: git show <sha>:internal/prompts/templates/<name>.template
package version

// BuildSHA is the git commit SHA of the server binary. Set at build time via:
//
//	go build -ldflags "-X github.com/assembledhq/143/internal/version.BuildSHA=$(git rev-parse HEAD)"
//
// Defaults to "dev" for local development. IsDev() returns true if ldflags
// injection was skipped, which callers can use to log a warning at startup.
var BuildSHA string = "dev"

// IsDev returns true if BuildSHA was not injected at build time.
func IsDev() bool {
	return BuildSHA == "dev"
}
