package linear

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// BuildDeps captures the shared infra (pool + already-constructed top-level
// stores) the Linear service needs. Pulling this out of router.go and
// cmd/server/main.go avoids two diverging copies of identical wiring.
type BuildDeps struct {
	Pool          *pgxpool.Pool
	Logger        zerolog.Logger
	Integrations  IntegrationReader
	Credentials   CredentialReader
	Issues        *db.IssueStore
	Sessions      *db.SessionStore
	IssueLinks    *db.SessionIssueLinkStore
	Orgs          *db.OrganizationStore
	Jobs          *db.JobStore
	JobQueueName  string
	JobPriority   int
	ClientFactory ClientFactory
}

// Build constructs the Linear service plus its three side-table stores from
// a shared dependency bundle. The returned *Service already has its job
// enqueuer wired onto the supplied JobStore — callers only need to add the
// SSE notifier (router) or PR-milestone enqueuer (PR service) themselves.
//
// JobQueueName defaults to "linear" and JobPriority to 5 when zero. The
// defaults match the design's worker conventions.
func Build(deps BuildDeps) *Service {
	queue := deps.JobQueueName
	if queue == "" {
		queue = "linear"
	}
	priority := deps.JobPriority
	if priority == 0 {
		priority = 5
	}

	clientFactory := deps.ClientFactory
	if clientFactory == nil {
		clientFactory = func(_ context.Context, token string) (Client, error) {
			return NewClient(token), nil
		}
	}

	loader := func(ctx context.Context, orgID uuid.UUID) (models.OrgSettings, error) {
		org, err := deps.Orgs.GetByID(ctx, orgID)
		if err != nil {
			return models.OrgSettings{}, err
		}
		return models.ParseOrgSettings(org.Settings)
	}

	svc := NewService(Config{
		Logger:            deps.Logger,
		Integrations:      deps.Integrations,
		Credentials:       deps.Credentials,
		Issues:            deps.Issues,
		Links:             deps.IssueLinks,
		TeamKeys:          db.NewLinearTeamKeyStore(deps.Pool),
		ProviderState:     db.NewLinearProviderStateStore(deps.Pool),
		StateEvents:       db.NewLinearStateEventStore(deps.Pool),
		Sessions:          deps.Sessions,
		ClientFactory:     clientFactory,
		OrgSettingsLoader: loader,
	})

	if deps.Jobs != nil {
		jobs := deps.Jobs
		svc.SetJobEnqueuer(func(ctx context.Context, orgID uuid.UUID, jobType string, payload any, dedupeKey *string) error {
			_, err := jobs.Enqueue(ctx, orgID, queue, jobType, payload, priority, dedupeKey)
			return err
		})
	}

	return svc
}

// MilestoneEnqueuerFor returns the closure that PRService.SetLinearMilestoneEnqueuer
// expects, sharing the same JobStore + queue convention as Build. Held as a
// helper so the router and main.go don't diverge on dedupe-key shape.
func MilestoneEnqueuerFor(jobs *db.JobStore, logger zerolog.Logger) func(ctx context.Context, orgID, sessionID uuid.UUID, event string, prNumber int) {
	return func(ctx context.Context, orgID, sessionID uuid.UUID, event string, prNumber int) {
		dedupe := "linear_milestone:" + sessionID.String() + ":" + event
		if _, err := jobs.Enqueue(ctx, orgID, "linear", "linear_milestone", map[string]any{
			"org_id":     orgID.String(),
			"session_id": sessionID.String(),
			"event":      event,
			"pr_number":  prNumber,
		}, 5, &dedupe); err != nil {
			logger.Warn().Err(err).Msg("failed to enqueue linear_milestone job")
		}
	}
}
