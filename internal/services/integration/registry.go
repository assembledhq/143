package integration

import (
	"fmt"
	"sync"
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
	mu             sync.RWMutex
	errorTrackers  map[string]ErrorTracker
	taskManagers   map[string]TaskManager
	documentStores map[string]DocumentStore
	messageSources map[string]MessageSource
}

// NewRegistry creates an empty integration registry.
func NewRegistry() *Registry {
	return &Registry{
		errorTrackers:  make(map[string]ErrorTracker),
		taskManagers:   make(map[string]TaskManager),
		documentStores: make(map[string]DocumentStore),
		messageSources: make(map[string]MessageSource),
	}
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

// HasAny returns true if at least one provider is registered.
func (r *Registry) HasAny() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.errorTrackers) > 0 ||
		len(r.taskManagers) > 0 ||
		len(r.documentStores) > 0 ||
		len(r.messageSources) > 0
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
	return m
}
