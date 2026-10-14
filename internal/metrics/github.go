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
	githubActivityOnce        sync.Once
	githubActivity            otelmetric.Int64Counter
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

func RecordGitHubActivity(ctx context.Context, caller, resource, outcome, kind string) {
	githubActivityOnce.Do(func() {
		counter, err := otel.Meter("github.com/assembledhq/143/github").Int64Counter("github.activity", otelmetric.WithDescription("GitHub remote requests and observed local cache hits and deferrals"), otelmetric.WithUnit("{event}"))
		if err != nil {
			otel.Handle(err)
			return
		}
		githubActivity = counter
	})
	recordGitHubActivity(ctx, githubActivity, caller, resource, outcome, kind)
}

func recordGitHubActivity(ctx context.Context, counter otelmetric.Int64Counter, caller, resource, outcome, kind string) {
	if counter == nil {
		return
	}
	counter.Add(ctx, 1, otelmetric.WithAttributes(
		attrString("caller", githubActivityDimension(caller, "github_service", "pr_service", "pr_health", "code_review", "code_review_snapshot", "code_review_text_evidence", "code_review_visual_evidence", "code_review_review", "automation_head", "sandbox_gh", "sandbox_tools", "sandbox_token", "installation_token", "org_members", "org_membership", "team_membership")),
		attrString("resource", githubActivityDimension(resource, "core", "graphql", "search", "code_search", "integration_manifest")),
		attrString("outcome", githubActivityDimension(outcome, "success", "client_error", "server_error", "rate_limited", "transport_error", "hit", "deferred")),
		attrString("kind", githubActivityDimension(kind, "remote", "cache_hit", "deferral")),
	))
}

func githubActivityDimension(value string, allowed ...string) string {
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return "other"
}
