package mcp

import (
	"fmt"
	"io"
	"os"

	"github.com/assembledhq/143/internal/services/integration"
	"github.com/assembledhq/143/internal/services/sandboxauth"
)

// BuildRegistryFromEnv creates an integration registry from environment variables.
// Each integration is optional — only configured integrations are registered.
// Diagnostic messages are written to logger (typically stderr).
func BuildRegistryFromEnv(logger io.Writer) *integration.Registry {
	reg := integration.NewRegistry()

	if token := os.Getenv("SENTRY_AUTH_TOKEN"); token != "" {
		orgSlug := os.Getenv("SENTRY_ORG_SLUG")
		baseURL := os.Getenv("SENTRY_BASE_URL")

		tracker := integration.NewSentryErrorTracker(integration.SentryTrackerConfig{
			AuthToken: token,
			OrgSlug:   orgSlug,
			BaseURL:   baseURL,
		})
		reg.RegisterErrorTracker(tracker)
		fmt.Fprintf(logger, "143-tools: registered sentry (org=%s)\n", orgSlug)
	}

	if token := os.Getenv("LINEAR_ACCESS_TOKEN"); token != "" {
		apiURL := os.Getenv("LINEAR_API_URL")

		manager := integration.NewLinearTaskManager(integration.LinearManagerConfig{
			AuthToken: token,
			APIURL:    apiURL,
		})
		reg.RegisterTaskManager(manager)
		fmt.Fprintln(logger, "143-tools: registered linear")
	}

	if token := os.Getenv("PAGERDUTY_ACCESS_TOKEN"); token != "" {
		provider := integration.NewPagerDutyIncidentProvider(integration.PagerDutyProviderConfig{
			AccessToken:      token,
			BaseURL:          os.Getenv("PAGERDUTY_API_URL"),
			WritebackEnabled: os.Getenv("PAGERDUTY_WRITEBACK_ENABLED") == "true",
		})
		reg.RegisterIncidentProvider(provider)
		fmt.Fprintln(logger, "143-tools: registered pagerduty")
	}

	if token := os.Getenv("NOTION_ACCESS_TOKEN"); token != "" {
		store := integration.NewNotionDocumentStore(integration.NotionDocumentStoreConfig{
			AuthToken: token,
		})
		reg.RegisterDocumentStore(store)
		fmt.Fprintln(logger, "143-tools: registered notion")
	}

	if token := os.Getenv("INTERNAL_API_TOKEN"); token != "" {
		apiURL := os.Getenv("INTERNAL_API_URL")
		if apiURL != "" {
			goalImprovementToolsOnly := os.Getenv("AUTOMATION_GOAL_IMPROVEMENT_TOOLS_ENABLED") == "true"
			if !goalImprovementToolsOnly {
				creator := integration.NewInternalIssueCreator(token, apiURL)
				reg.RegisterIssueCreator(creator)
				fmt.Fprintln(logger, "143-tools: registered issue creator")

				prCreator := integration.NewInternalPullRequestCreator(token, apiURL)
				reg.RegisterPullRequestCreator(prCreator)
				fmt.Fprintln(logger, "143-tools: registered PR creator")
			}

			tabManager := integration.NewInternalSessionTabManager(token, apiURL)
			reg.RegisterSessionTabManager(tabManager)
			fmt.Fprintln(logger, "143-tools: registered session tab manager")

			if !goalImprovementToolsOnly {
				proposer := integration.NewInternalProjectProposer(token, apiURL)
				reg.RegisterProjectProposer(proposer)
				fmt.Fprintln(logger, "143-tools: registered project proposer")
			}

			if os.Getenv("EVAL_BOOTSTRAP_TOOLS_ENABLED") == "true" {
				reporter := integration.NewInternalEvalCandidateReporter(token, apiURL, os.Getenv("EVAL_BOOTSTRAP_RUN_ID"))
				reg.RegisterEvalCandidateReporter(reporter)
				fmt.Fprintln(logger, "143-tools: registered eval candidate reporter")
			}

			if goalImprovementToolsOnly {
				completer := integration.NewInternalAutomationGoalImprovementCompleter(token, apiURL)
				reg.RegisterAutomationGoalImprovementCompleter(completer)
				fmt.Fprintln(logger, "143-tools: registered automation goal improvement completer")
			}
		}
	}

	if token := os.Getenv("CIRCLECI_TOKEN"); token != "" {
		slug := os.Getenv("CIRCLECI_PROJECT_SLUG")
		baseURL := os.Getenv("CIRCLECI_BASE_URL")
		if slug == "" {
			fmt.Fprintln(logger, "143-tools: CIRCLECI_TOKEN set but CIRCLECI_PROJECT_SLUG is empty — skipping circleci")
		} else {
			provider := integration.NewCircleCITestInsights(integration.CircleCIConfig{
				AuthToken:   token,
				ProjectSlug: slug,
				BaseURL:     baseURL,
			})
			reg.RegisterCITestInsights(provider)
			fmt.Fprintf(logger, "143-tools: registered circleci (slug=%s)\n", slug)
		}
	}

	if queryURL := os.Getenv("VICTORIALOGS_URL"); queryURL != "" {
		provider := integration.NewVictoriaLogsProvider(integration.VictoriaLogsConfig{
			QueryURL:          queryURL,
			FieldNamesURL:     os.Getenv("VICTORIALOGS_FIELDS_URL"),
			AuthToken:         os.Getenv("VICTORIALOGS_TOKEN"),
			SharedOrgID:       os.Getenv("VICTORIALOGS_ORG_ID"),
			MultiTenantShared: os.Getenv("VICTORIALOGS_SHARED") == "true",
		})
		reg.RegisterLogProvider(provider)
		fmt.Fprintln(logger, "143-tools: registered victorialogs")
	}

	if token := os.Getenv("MEZMO_API_KEY"); token != "" {
		provider := integration.NewMezmoProvider(integration.MezmoConfig{
			APIKey:  token,
			BaseURL: os.Getenv("MEZMO_BASE_URL"),
			Dataset: os.Getenv("MEZMO_DATASET"),
		})
		reg.RegisterLogProvider(provider)
		fmt.Fprintln(logger, "143-tools: registered mezmo")
	}

	owner := os.Getenv("GITHUB_REPO_OWNER")
	repo := os.Getenv("GITHUB_REPO_NAME")
	if owner != "" && repo != "" {
		// Two credential paths, mutually exclusive in practice (the orchestrator
		// only injects one — see prepareSandboxGitHubAuth):
		//   1. GITHUB_TOKEN env var: legacy fallback or PAT-based setups. Token
		//      sits in the env for the container's lifetime.
		//   2. _143_AUTH_SOCK: GitHub App identity flow. The socket vends fresh,
		//      short-lived tokens per request. We resolve once per `143-tools`
		//      invocation and cache inside the source for the life of that
		//      single CLI process.
		if token := os.Getenv("GITHUB_TOKEN"); token != "" {
			source := integration.NewGitHubCodeReviewSource(integration.GitHubCodeReviewConfig{
				Token: token,
				Owner: owner,
				Repo:  repo,
			})
			reg.RegisterCodeReviewSource(source)
			fmt.Fprintf(logger, "143-tools: registered github (%s/%s)\n", owner, repo)
		} else if sockPath := os.Getenv(sandboxauth.SocketEnvVar); sockPath != "" {
			source := integration.NewGitHubCodeReviewSource(integration.GitHubCodeReviewConfig{
				TokenFunc: sandboxauth.NewClient(sockPath).GetAPIToken,
				Owner:     owner,
				Repo:      repo,
			})
			reg.RegisterCodeReviewSource(source)
			fmt.Fprintf(logger, "143-tools: registered github (%s/%s) via host auth socket\n", owner, repo)
		}
	}

	return reg
}
