package integration

import (
	"fmt"
	"sync"

	"github.com/assembledhq/143/internal/models"
)

// Registry holds all configured integration providers, organized by category.
// It is safe for concurrent use.
//
// The orchestrator builds one Registry at startup from org credentials, then
// passes it to:
//   - PM context gatherer (for enriched issue analysis)
//   - MCP servers (for runtime tool access inside sandboxes)
//   - Static context writers (for pre-populating sandbox files)
type Registry struct {
	mu                 sync.RWMutex
	errorTrackers      map[string]ErrorTracker
	taskManagers       map[string]TaskManager
	documentStores     map[string]DocumentStore
	messageSources     map[string]MessageSource
	codeReviewSources  map[string]CodeReviewSource
	issueCreators      map[string]IssueCreator
	prCreators         map[string]PullRequestCreator
	sessionTabManagers map[string]SessionTabManager
	projectProposers   map[string]ProjectProposer
	ciTestInsights     map[string]CITestInsights
	logProviders       map[string]LogProvider
}

// NewRegistry creates an empty integration registry.
func NewRegistry() *Registry {
	return &Registry{
		errorTrackers:      make(map[string]ErrorTracker),
		taskManagers:       make(map[string]TaskManager),
		documentStores:     make(map[string]DocumentStore),
		messageSources:     make(map[string]MessageSource),
		codeReviewSources:  make(map[string]CodeReviewSource),
		issueCreators:      make(map[string]IssueCreator),
		prCreators:         make(map[string]PullRequestCreator),
		sessionTabManagers: make(map[string]SessionTabManager),
		projectProposers:   make(map[string]ProjectProposer),
		ciTestInsights:     make(map[string]CITestInsights),
		logProviders:       make(map[string]LogProvider),
	}
}

func (r *Registry) RegisterSessionTabManager(provider SessionTabManager) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessionTabManagers[provider.Name()] = provider
}

func (r *Registry) SessionTabManagers() []SessionTabManager {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]SessionTabManager, 0, len(r.sessionTabManagers))
	for _, p := range r.sessionTabManagers {
		result = append(result, p)
	}
	return result
}

// RegisterErrorTracker adds an error tracker (e.g. Sentry, Datadog).
func (r *Registry) RegisterErrorTracker(provider ErrorTracker) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errorTrackers[provider.Name()] = provider
}

// RegisterTaskManager adds a task manager (e.g. Linear, Jira).
func (r *Registry) RegisterTaskManager(provider TaskManager) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.taskManagers[provider.Name()] = provider
}

// RegisterDocumentStore adds a document store (e.g. Notion, Confluence).
func (r *Registry) RegisterDocumentStore(provider DocumentStore) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.documentStores[provider.Name()] = provider
}

// RegisterMessageSource adds a message source (e.g. Slack, Discord).
func (r *Registry) RegisterMessageSource(provider MessageSource) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messageSources[provider.Name()] = provider
}

// ErrorTrackers returns all registered error trackers.
func (r *Registry) ErrorTrackers() []ErrorTracker {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]ErrorTracker, 0, len(r.errorTrackers))
	for _, et := range r.errorTrackers {
		result = append(result, et)
	}
	return result
}

// ErrorTracker returns a specific error tracker by name, or an error if not found.
func (r *Registry) ErrorTracker(name string) (ErrorTracker, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	et, ok := r.errorTrackers[name]
	if !ok {
		return nil, fmt.Errorf("error tracker %q not registered", name)
	}
	return et, nil
}

// TaskManagers returns all registered task managers.
func (r *Registry) TaskManagers() []TaskManager {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]TaskManager, 0, len(r.taskManagers))
	for _, tm := range r.taskManagers {
		result = append(result, tm)
	}
	return result
}

// TaskManager returns a specific task manager by name, or an error if not found.
func (r *Registry) TaskManager(name string) (TaskManager, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tm, ok := r.taskManagers[name]
	if !ok {
		return nil, fmt.Errorf("task manager %q not registered", name)
	}
	return tm, nil
}

// DocumentStores returns all registered document stores.
func (r *Registry) DocumentStores() []DocumentStore {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]DocumentStore, 0, len(r.documentStores))
	for _, ds := range r.documentStores {
		result = append(result, ds)
	}
	return result
}

// DocumentStore returns a specific document store by name, or an error if not found.
func (r *Registry) DocumentStore(name string) (DocumentStore, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ds, ok := r.documentStores[name]
	if !ok {
		return nil, fmt.Errorf("document store %q not registered", name)
	}
	return ds, nil
}

// MessageSources returns all registered message sources.
func (r *Registry) MessageSources() []MessageSource {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]MessageSource, 0, len(r.messageSources))
	for _, ms := range r.messageSources {
		result = append(result, ms)
	}
	return result
}

// MessageSource returns a specific message source by name, or an error if not found.
func (r *Registry) MessageSource(name string) (MessageSource, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ms, ok := r.messageSources[name]
	if !ok {
		return nil, fmt.Errorf("message source %q not registered", name)
	}
	return ms, nil
}

// RegisterCodeReviewSource adds a code review source (e.g. GitHub, GitLab).
func (r *Registry) RegisterCodeReviewSource(provider CodeReviewSource) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.codeReviewSources[provider.Name()] = provider
}

// CodeReviewSources returns all registered code review sources.
func (r *Registry) CodeReviewSources() []CodeReviewSource {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]CodeReviewSource, 0, len(r.codeReviewSources))
	for _, cr := range r.codeReviewSources {
		result = append(result, cr)
	}
	return result
}

// CodeReviewSource returns a specific code review source by name, or an error if not found.
func (r *Registry) CodeReviewSource(name string) (CodeReviewSource, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cr, ok := r.codeReviewSources[name]
	if !ok {
		return nil, fmt.Errorf("code review source %q not registered", name)
	}
	return cr, nil
}

// RegisterIssueCreator adds an issue creator (e.g. internal 143 API).
func (r *Registry) RegisterIssueCreator(provider IssueCreator) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.issueCreators[provider.Name()] = provider
}

// IssueCreators returns all registered issue creators.
func (r *Registry) IssueCreators() []IssueCreator {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]IssueCreator, 0, len(r.issueCreators))
	for _, ic := range r.issueCreators {
		result = append(result, ic)
	}
	return result
}

// IssueCreator returns a specific issue creator by name, or an error if not found.
func (r *Registry) IssueCreator(name string) (IssueCreator, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ic, ok := r.issueCreators[name]
	if !ok {
		return nil, fmt.Errorf("issue creator %q not registered", name)
	}
	return ic, nil
}

// RegisterPullRequestCreator adds a PR creator (e.g. internal 143 API).
func (r *Registry) RegisterPullRequestCreator(provider PullRequestCreator) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prCreators[provider.Name()] = provider
}

// PullRequestCreators returns all registered PR creators.
func (r *Registry) PullRequestCreators() []PullRequestCreator {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]PullRequestCreator, 0, len(r.prCreators))
	for _, pc := range r.prCreators {
		result = append(result, pc)
	}
	return result
}

// PullRequestCreator returns a specific PR creator by name, or an error if not found.
func (r *Registry) PullRequestCreator(name string) (PullRequestCreator, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	pc, ok := r.prCreators[name]
	if !ok {
		return nil, fmt.Errorf("pull request creator %q not registered", name)
	}
	return pc, nil
}

// RegisterProjectProposer adds a project proposer (e.g. internal 143 API).
func (r *Registry) RegisterProjectProposer(provider ProjectProposer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.projectProposers[provider.Name()] = provider
}

// ProjectProposers returns all registered project proposers.
func (r *Registry) ProjectProposers() []ProjectProposer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]ProjectProposer, 0, len(r.projectProposers))
	for _, pp := range r.projectProposers {
		result = append(result, pp)
	}
	return result
}

// ProjectProposer returns a specific project proposer by name, or an error if not found.
func (r *Registry) ProjectProposer(name string) (ProjectProposer, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	pp, ok := r.projectProposers[name]
	if !ok {
		return nil, fmt.Errorf("project proposer %q not registered", name)
	}
	return pp, nil
}

// RegisterCITestInsights adds a CI test insights provider (e.g. CircleCI).
func (r *Registry) RegisterCITestInsights(provider CITestInsights) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ciTestInsights[provider.Name()] = provider
}

// RegisterLogProvider adds a read-only log provider (e.g. VictoriaLogs, Mezmo).
func (r *Registry) RegisterLogProvider(provider LogProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logProviders[string(provider.Name())] = provider
}

// CITestInsightsProviders returns all registered CI test insights providers.
func (r *Registry) CITestInsightsProviders() []CITestInsights {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]CITestInsights, 0, len(r.ciTestInsights))
	for _, p := range r.ciTestInsights {
		result = append(result, p)
	}
	return result
}

// CITestInsightsProvider returns a specific CI test insights provider by name.
func (r *Registry) CITestInsightsProvider(name string) (CITestInsights, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.ciTestInsights[name]
	if !ok {
		return nil, fmt.Errorf("ci test insights %q not registered", name)
	}
	return p, nil
}

// LogProviders returns all registered log providers.
func (r *Registry) LogProviders() []LogProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]LogProvider, 0, len(r.logProviders))
	for _, p := range r.logProviders {
		result = append(result, p)
	}
	return result
}

// LogProvider returns a specific log provider by provider name.
func (r *Registry) LogProvider(name models.ProviderName) (LogProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	key := string(name)
	p, ok := r.logProviders[key]
	if !ok {
		return nil, fmt.Errorf("log provider %q not registered", key)
	}
	return p, nil
}

// HasAny returns true if at least one provider is registered.
func (r *Registry) HasAny() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.errorTrackers) > 0 ||
		len(r.taskManagers) > 0 ||
		len(r.documentStores) > 0 ||
		len(r.messageSources) > 0 ||
		len(r.codeReviewSources) > 0 ||
		len(r.issueCreators) > 0 ||
		len(r.prCreators) > 0 ||
		len(r.sessionTabManagers) > 0 ||
		len(r.projectProposers) > 0 ||
		len(r.ciTestInsights) > 0 ||
		len(r.logProviders) > 0
}

// Summary returns a human-readable summary of registered providers.
func (r *Registry) Summary() map[string][]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m := make(map[string][]string)
	for name := range r.errorTrackers {
		m["error_trackers"] = append(m["error_trackers"], name)
	}
	for name := range r.taskManagers {
		m["task_managers"] = append(m["task_managers"], name)
	}
	for name := range r.documentStores {
		m["document_stores"] = append(m["document_stores"], name)
	}
	for name := range r.messageSources {
		m["message_sources"] = append(m["message_sources"], name)
	}
	for name := range r.codeReviewSources {
		m["code_review_sources"] = append(m["code_review_sources"], name)
	}
	for name := range r.issueCreators {
		m["issue_creators"] = append(m["issue_creators"], name)
	}
	for name := range r.prCreators {
		m["pull_request_creators"] = append(m["pull_request_creators"], name)
	}
	for name := range r.sessionTabManagers {
		m["session_tab_managers"] = append(m["session_tab_managers"], name)
	}
	for name := range r.projectProposers {
		m["project_proposers"] = append(m["project_proposers"], name)
	}
	for name := range r.ciTestInsights {
		m["ci_test_insights"] = append(m["ci_test_insights"], name)
	}
	for name := range r.logProviders {
		m["log_providers"] = append(m["log_providers"], name)
	}
	return m
}
