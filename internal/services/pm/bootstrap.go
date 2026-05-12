package pm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/services/agent/adapters"
)

const bootstrapOutputPath = "/workspace/.pm-context/CONTEXT.md"
const bootstrapInputPath = "/workspace/.bootstrap-input.json"

// sandboxRunParams captures the inputs needed to run an agent in a sandbox.
// Shared by RunBootstrap and RunRefresh to avoid duplicating setup logic.
type sandboxRunParams struct {
	orgID   uuid.UUID
	prompt  *agent.AgentPrompt
	logName string // e.g. "bootstrap-agent", "refresh-agent"

	// preExec is called after clone + seed write but before agent execution.
	// Use it to inject additional files into the sandbox (e.g. existing context doc).
	preExec func(ctx context.Context, sb *agent.Sandbox) error
}

// runAgentInSandbox handles the common sandbox lifecycle: create, clone, seed,
// execute agent, and return. Callers provide the prompt and any pre-exec setup.
// The returned cleanup function destroys the sandbox — callers must defer it.
func (s *Service) runAgentInSandbox(ctx context.Context, params sandboxRunParams) (*agent.Sandbox, func(), error) {
	noop := func() {}

	repo, err := s.selectRepo(ctx, params.orgID, nil)
	if err != nil {
		return nil, noop, fmt.Errorf("select repo: %w", err)
	}

	var ghToken string
	if s.github != nil {
		t, err := s.github.GetInstallationToken(ctx, repo.InstallationID)
		if err != nil {
			return nil, noop, fmt.Errorf("get installation token: %w", err)
		}
		ghToken = t
	}

	creds := s.fetchIntegrationCredentials(ctx, params.orgID)
	creds.github = ghToken != ""

	org, err := s.orgs.GetByID(ctx, params.orgID)
	if err != nil {
		return nil, noop, fmt.Errorf("get org: %w", err)
	}
	settings, err := models.ParseOrgSettings(org.Settings)
	if err != nil {
		return nil, noop, fmt.Errorf("parse org settings: %w", err)
	}
	agentType := resolveAgentType(settings, nil)
	adapter, err := s.pickAdapter(agentType)
	if err != nil {
		return nil, noop, fmt.Errorf("pick adapter: %w", err)
	}

	sbCfg := bootstrapSandboxConfig()
	sbCfg.Env = s.env.Resolve(ctx, params.orgID, agentType, nil)
	if sbCfg.Env == nil {
		sbCfg.Env = make(map[string]string)
	}
	applyGitHubEnv(sbCfg.Env, ghToken, &repo)
	if err := s.finalizeSandboxEnv(agentType, sbCfg.Env); err != nil {
		return nil, noop, fmt.Errorf("agent auth preflight: %w", err)
	}
	sb, err := s.sandbox.Create(ctx, sbCfg)
	if err != nil {
		return nil, noop, fmt.Errorf("create sandbox: %w", err)
	}
	exitReason := "completed"
	containerStartedAt := time.Now()
	var usageEventID uuid.UUID
	if s.usageTracker != nil {
		usageEventID = s.usageTracker.ContainerStarted(ctx, params.orgID, uuid.Nil, sb, sbCfg, containerStartedAt)
	}
	cleanup := func() {
		if s.usageTracker != nil {
			s.usageTracker.ContainerStopped(ctx, params.orgID, uuid.Nil, usageEventID, sb.ID, containerStartedAt, exitReason)
		}
		if destroyErr := s.sandbox.Destroy(ctx, sb); destroyErr != nil {
			s.logger.Warn().Err(destroyErr).Str("source", params.logName).Msg("failed to destroy sandbox")
		}
	}
	if _, err := s.injectRequiredAgentAuth(ctx, params.orgID, agentType, sb, sbCfg.Env); err != nil {
		exitReason = containerExitReason(ctx, err)
		cleanup()
		return nil, noop, fmt.Errorf("inject codex auth: %w", err)
	}

	if err := s.sandbox.CloneRepo(ctx, sb, repo.CloneURL, repo.DefaultBranch, ghToken); err != nil {
		exitReason = containerExitReason(ctx, err)
		cleanup()
		return nil, noop, fmt.Errorf("clone repo: %w", err)
	}

	// Write seed context so the agent doesn't start cold.
	seedJSON, err := s.buildBootstrapSeed(ctx, params.orgID, &repo, &creds)
	if err != nil {
		s.logger.Warn().Err(err).Msg("failed to build seed — continuing without seed")
		seedJSON = []byte("{}")
	}
	if err := s.sandbox.WriteFile(ctx, sb, bootstrapInputPath, seedJSON); err != nil {
		exitReason = containerExitReason(ctx, err)
		cleanup()
		return nil, noop, fmt.Errorf("write seed: %w", err)
	}

	// Inject seed as user prompt if not already set.
	if params.prompt.UserPrompt == "" {
		params.prompt.UserPrompt = string(seedJSON)
	}

	// Run any pre-execution setup (e.g. writing existing context doc).
	if params.preExec != nil {
		if err := params.preExec(ctx, sb); err != nil {
			exitReason = containerExitReason(ctx, err)
			cleanup()
			return nil, noop, err
		}
	}

	logCh := make(chan agent.LogEntry, 100)
	var logWg sync.WaitGroup
	logWg.Add(1)
	go func() {
		defer logWg.Done()
		for entry := range logCh {
			s.logger.Debug().Str("source", params.logName).Str("level", entry.Level).Msg(entry.Message)
		}
	}()

	execCtx := adapters.WithSandboxProvider(ctx, s.sandbox)
	if _, err := adapter.Execute(execCtx, sb, params.prompt, logCh); err != nil {
		exitReason = containerExitReason(ctx, err)
		close(logCh)
		logWg.Wait()
		cleanup()
		return nil, noop, fmt.Errorf("%s execution: %w", params.logName, err)
	}
	close(logCh)
	logWg.Wait()

	return sb, cleanup, nil
}

// RunBootstrap creates a coding agent session that explores all connected
// integrations and the codebase, then writes a structured PM context file.
// The output is extracted from the sandbox and stored in pm_documents.
func (s *Service) RunBootstrap(ctx context.Context, orgID uuid.UUID) error {
	if s.sandbox == nil || s.env == nil {
		return fmt.Errorf("pm sandbox or env helper not configured")
	}
	if s.pmDocuments == nil {
		return fmt.Errorf("pm document store not configured")
	}

	skillsDoc := s.buildSkillsDoc(ctx, orgID)

	creds := s.fetchIntegrationCredentials(ctx, orgID)

	prompt := &agent.AgentPrompt{
		SystemPrompt: prompts.PMBootstrapPrompt(prompts.PMBootstrapPromptData{
			SkillsDoc: skillsDoc,
			HasNotion: creds.notion != nil,
			HasLinear: creds.linear != nil,
			HasSentry: creds.sentry != nil,
			HasGitHub: creds.github,
		}),
		MaxTokens: 80_000,
	}

	sb, cleanup, err := s.runAgentInSandbox(ctx, sandboxRunParams{
		orgID:   orgID,
		prompt:  prompt,
		logName: "bootstrap-agent",
	})
	if err != nil {
		return err
	}
	defer cleanup()

	content, err := s.sandbox.ReadFile(ctx, sb, bootstrapOutputPath)
	if err != nil {
		return fmt.Errorf("read CONTEXT.md from sandbox: %w", err)
	}
	if len(content) == 0 {
		return fmt.Errorf("bootstrap agent produced empty CONTEXT.md")
	}

	return s.upsertAutogeneratedDoc(ctx, orgID, string(content))
}

// buildSkillsDoc returns the integration skills doc for the org, or empty string.
func (s *Service) buildSkillsDoc(ctx context.Context, orgID uuid.UUID) string {
	if s.skills != nil {
		return s.skills.BuildIntegrationSkills(ctx, orgID)
	}
	return ""
}

// upsertAutogeneratedDoc creates or updates the autogenerated context document.
// There is exactly one autogenerated doc per org.
func (s *Service) upsertAutogeneratedDoc(ctx context.Context, orgID uuid.UUID, content string) error {
	now := time.Now()

	existing, err := s.pmDocuments.GetByOrgAndSourceType(ctx, orgID, models.PMDocSourceAutogenerated)
	if err == nil {
		existing.Content = content
		existing.LastSyncedAt = &now
		return s.pmDocuments.Update(ctx, &existing)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("check existing autogenerated doc: %w", err)
	}

	doc := &models.PMDocument{
		OrgID:        orgID,
		Title:        "PM Context — Autogenerated",
		Content:      content,
		DocType:      models.PMDocTypeContext,
		SourceType:   models.PMDocSourceAutogenerated,
		LastSyncedAt: &now,
		SortOrder:    -1, // sort before user-uploaded docs
	}
	return s.pmDocuments.Create(ctx, doc)
}

// buildBootstrapSeed gathers existing context as a JSON seed for the bootstrap agent.
func (s *Service) buildBootstrapSeed(ctx context.Context, orgID uuid.UUID, repo *models.Repository, creds *integrationCredentials) ([]byte, error) {
	org, err := s.orgs.GetByID(ctx, orgID)
	if err != nil {
		return nil, err
	}
	settings, err := models.ParseOrgSettings(org.Settings)
	if err != nil {
		return nil, err
	}

	seed := map[string]any{}
	if settings.ProductContext != nil {
		seed["product_context"] = settings.ProductContext
	}
	seed["connected_integrations"] = creds.connectedProviderNames()

	// Include summaries of existing PM documents so the agent knows what's already there.
	// Exclude pending refresh docs — they're transient suggestions, not confirmed context.
	if s.pmDocuments != nil {
		docs, err := s.pmDocuments.ListByOrgExcludeSourceType(ctx, orgID, models.PMDocSourceRefresh, 100)
		if err == nil && len(docs) > 0 {
			summaries := make([]map[string]string, 0, len(docs))
			for _, doc := range docs {
				summaries = append(summaries, map[string]string{
					"title":       doc.Title,
					"doc_type":    doc.DocType,
					"source_type": doc.SourceType,
				})
			}
			seed["existing_pm_documents"] = summaries
		}
	}

	return json.Marshal(seed)
}

// integrationCredentials holds fetched credentials for all integration providers.
// Fetched once per bootstrap/refresh run to avoid redundant DB lookups.
type integrationCredentials struct {
	github bool // true if GitHub App is configured (token is fetched separately)
	sentry *models.DecryptedCredential
	linear *models.DecryptedCredential
	notion *models.DecryptedCredential
}

// fetchIntegrationCredentials fetches all integration credentials for an org in one pass.
func (s *Service) fetchIntegrationCredentials(ctx context.Context, orgID uuid.UUID) integrationCredentials {
	if s.credentials == nil {
		return integrationCredentials{}
	}
	var creds integrationCredentials
	if c, err := s.credentials.Get(ctx, orgID, models.ProviderSentry); err == nil {
		creds.sentry = c
	}
	if c, err := s.credentials.Get(ctx, orgID, models.ProviderLinear); err == nil {
		creds.linear = c
	}
	if c, err := s.credentials.Get(ctx, orgID, models.ProviderNotion); err == nil {
		creds.notion = c
	}
	return creds
}

// connectedProviderNames returns the names of connected integration providers.
func (creds *integrationCredentials) connectedProviderNames() []string {
	var names []string
	if creds.github {
		names = append(names, "github")
	}
	if creds.sentry != nil {
		names = append(names, string(models.ProviderSentry))
	}
	if creds.linear != nil {
		names = append(names, string(models.ProviderLinear))
	}
	if creds.notion != nil {
		names = append(names, string(models.ProviderNotion))
	}
	return names
}

func bootstrapSandboxConfig() agent.SandboxConfig {
	// Inherit SANDBOX_CPU_LIMIT / SANDBOX_MEMORY_LIMIT_MB from
	// DefaultSandboxConfig so capacity-planning math (deploy/scripts/
	// worker_buckets.sh) doesn't have to special-case bootstraps. Bootstrap
	// only overrides the wall-clock timeout (repo clone + analysis is the
	// long pole) and the network policy.
	cfg := agent.DefaultSandboxConfig()
	cfg.Timeout = 30 * time.Minute
	cfg.NetworkPolicy = "restricted"
	return cfg
}

// applyGitHubEnv layers repo-scoped GitHub env vars (GITHUB_TOKEN and the
// owner/name pair used by code-review skills) onto an env map already populated
// by AgentEnv.Resolve. Repo context is not an AgentEnv concern — it's specific
// to bootstrap/refresh runs that clone a single repo — so it lives here.
func applyGitHubEnv(env map[string]string, ghToken string, repo *models.Repository) {
	if env == nil {
		return
	}
	if ghToken != "" {
		env["GITHUB_TOKEN"] = ghToken
	}
	if repo != nil && repo.FullName != "" {
		parts := strings.SplitN(repo.FullName, "/", 2)
		if len(parts) == 2 {
			env["GITHUB_REPO_OWNER"] = parts[0]
			env["GITHUB_REPO_NAME"] = parts[1]
		}
	}
}
