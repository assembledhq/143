package main

import (
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/services/agent"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/assembledhq/143/internal/services/publicationintent"
	"github.com/assembledhq/143/internal/worker"
)

type publicationExecutionPolicySetter interface {
	SetPublicationExecutionPolicy(ghservice.PublicationExecutionPolicy)
}

type publicationIntentCoordinatorSetter interface {
	SetPublicationIntentCoordinator(publicationintent.PublicationIntentCoordinator)
}

var (
	_ publicationExecutionPolicySetter   = (*ghservice.PRService)(nil)
	_ publicationIntentCoordinatorSetter = (*agent.Orchestrator)(nil)
)

// wirePublicationCoordinator gives workers and isolated session executors the
// same publication coordinator and runtime switches. Both runtimes complete
// automation turns that can request a PR and execute publication jobs.
func wirePublicationCoordinator(cfg *config.Config, services *worker.Services, stores *worker.Stores, logger zerolog.Logger) {
	if services == nil || stores == nil {
		return
	}

	coordinator := publicationintent.NewCoordinator(
		stores.Sessions,
		stores.SessionChangesets,
		stores.PullRequests,
		stores.Organizations,
		stores.Users,
		stores.SessionPublications,
		stores.Jobs,
		logger,
	)
	coordinator.SetRepositoryStore(stores.Repositories)
	coordinator.SetAutomationPRIntentStores(stores.AutomationRuns, stores.SessionPRIntents)
	coordinator.SetPublicationEnabled(cfg.AgentPRPublicationEnabled)
	coordinator.SetReviewEnabled(cfg.PrePRReviewEnabled)
	services.PublicationIntents = coordinator
	if pr, ok := services.PR.(publicationExecutionPolicySetter); ok {
		pr.SetPublicationExecutionPolicy(coordinator)
	}
	if pr, ok := services.PR.(interface {
		SetTeamReviewRequestStore(ghservice.TeamReviewRequestStore)
	}); ok && stores.TeamReviewRequests != nil {
		pr.SetTeamReviewRequestStore(stores.TeamReviewRequests)
	}
	if orchestrator, ok := services.Orchestrator.(publicationIntentCoordinatorSetter); ok {
		orchestrator.SetPublicationIntentCoordinator(coordinator)
	}
}
