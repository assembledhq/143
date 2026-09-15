package metrics

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	otelmetric "go.opentelemetry.io/otel/metric"
)

var (
	githubPrincipalOnce       sync.Once
	githubPrincipalUnresolved otelmetric.Int64Counter
)

func getGitHubPrincipalUnresolved() otelmetric.Int64Counter {
	githubPrincipalOnce.Do(func() {
		meter := otel.Meter("github.com/assembledhq/143/github")
		counter, err := meter.Int64Counter(
			"github.principal.unresolved",
			otelmetric.WithDescription("GitHub HTTP requests whose authenticated principal was not resolved before dispatch"),
			otelmetric.WithUnit("{request}"),
		)
		if err != nil {
			otel.Handle(err)
			return
		}
		githubPrincipalUnresolved = counter
	})
	return githubPrincipalUnresolved
}

// RecordGitHubPrincipalUnresolved records only bounded rollout dimensions.
// Installation IDs, repositories, URLs, and credentials are deliberately not
// metric attributes.
func RecordGitHubPrincipalUnresolved(ctx context.Context, caller, mode string) {
	counter := getGitHubPrincipalUnresolved()
	if counter == nil {
		return
	}
	counter.Add(ctx, 1, otelmetric.WithAttributes(
		attrString("caller", caller),
		attrString("mode", mode),
	))
}
