package integration

import (
	"context"
	"testing"
	"time"
)

// --- mock implementations for testing ---

type mockErrorTracker struct {
	name   string
	errors []ErrorSummary
}

func (m *mockErrorTracker) Name() string { return m.name }
func (m *mockErrorTracker) ListErrors(_ context.Context, _ ErrorFilter) ([]ErrorSummary, error) {
	return m.errors, nil
}
func (m *mockErrorTracker) GetError(_ context.Context, id string) (*ErrorDetail, error) {
	for _, e := range m.errors {
		if e.ID == id {
			return &ErrorDetail{ErrorSummary: e}, nil
		}
	}
	return nil, nil
}
func (m *mockErrorTracker) GetTrend(_ context.Context, _ string, _ time.Duration) (*ErrorTrend, error) {
	return &ErrorTrend{Direction: "stable"}, nil
}
func (m *mockErrorTracker) FindRelated(_ context.Context, _ string) ([]ErrorSummary, error) {
	return nil, nil
}

type mockTaskManager struct {
	name  string
	tasks []TaskSummary
}

func (m *mockTaskManager) Name() string { return m.name }
func (m *mockTaskManager) ListTasks(_ context.Context, _ TaskFilter) ([]TaskSummary, error) {
	return m.tasks, nil
}
func (m *mockTaskManager) GetTask(_ context.Context, id string) (*TaskDetail, error) {
	for _, t := range m.tasks {
		if t.ID == id {
			return &TaskDetail{TaskSummary: t}, nil
		}
	}
	return nil, nil
}
func (m *mockTaskManager) FindRelated(_ context.Context, _ string) ([]TaskSummary, error) {
	return nil, nil
}
func (m *mockTaskManager) UpdateTask(_ context.Context, _ string, _ TaskUpdate) error {
	return nil
}
func (m *mockTaskManager) CreateTask(_ context.Context, spec TaskCreateSpec) (*TaskSummary, error) {
	ts := &TaskSummary{ID: "new-1", Title: spec.Title}
	return ts, nil
}

type mockDocStore struct{ name string }

func (m *mockDocStore) Name() string { return m.name }
func (m *mockDocStore) SearchDocuments(_ context.Context, _ string, _ DocFilter) ([]DocSummary, error) {
	return nil, nil
}
func (m *mockDocStore) GetDocument(_ context.Context, _ string) (*Document, error) {
	return nil, nil
}

type mockMsgSource struct{ name string }

func (m *mockMsgSource) Name() string { return m.name }
func (m *mockMsgSource) SearchMessages(_ context.Context, _ string, _ MessageFilter) ([]MessageSummary, error) {
	return nil, nil
}
func (m *mockMsgSource) GetThread(_ context.Context, _ string) (*Thread, error) { return nil, nil }

// --- registry tests ---

func TestRegistry_RegisterAndRetrieve(t *testing.T) {
	r := NewRegistry()

	// Register one of each type.
	r.RegisterErrorTracker(&mockErrorTracker{name: "sentry"})
	r.RegisterTaskManager(&mockTaskManager{name: "linear"})
	r.RegisterDocumentStore(&mockDocStore{name: "notion"})
	r.RegisterMessageSource(&mockMsgSource{name: "slack"})

	if !r.HasAny() {
		t.Fatal("expected HasAny to be true")
	}

	// Retrieve by name.
	et, err := r.ErrorTracker("sentry")
	if err != nil {
		t.Fatalf("ErrorTracker: %v", err)
	}
	if et.Name() != "sentry" {
		t.Errorf("expected sentry, got %s", et.Name())
	}

	tm, err := r.TaskManager("linear")
	if err != nil {
		t.Fatalf("TaskManager: %v", err)
	}
	if tm.Name() != "linear" {
		t.Errorf("expected linear, got %s", tm.Name())
	}

	ds, err := r.DocumentStore("notion")
	if err != nil {
		t.Fatalf("DocumentStore: %v", err)
	}
	if ds.Name() != "notion" {
		t.Errorf("expected notion, got %s", ds.Name())
	}

	ms, err := r.MessageSource("slack")
	if err != nil {
		t.Fatalf("MessageSource: %v", err)
	}
	if ms.Name() != "slack" {
		t.Errorf("expected slack, got %s", ms.Name())
	}
}

func TestRegistry_NotFound(t *testing.T) {
	r := NewRegistry()

	_, err := r.ErrorTracker("sentry")
	if err == nil {
		t.Fatal("expected error for missing tracker")
	}

	_, err = r.TaskManager("linear")
	if err == nil {
		t.Fatal("expected error for missing task manager")
	}

	_, err = r.DocumentStore("notion")
	if err == nil {
		t.Fatal("expected error for missing doc store")
	}

	_, err = r.MessageSource("slack")
	if err == nil {
		t.Fatal("expected error for missing msg source")
	}
}

func TestRegistry_EmptyHasAny(t *testing.T) {
	r := NewRegistry()
	if r.HasAny() {
		t.Fatal("expected HasAny to be false on empty registry")
	}
}

func TestRegistry_ListAll(t *testing.T) {
	r := NewRegistry()
	r.RegisterErrorTracker(&mockErrorTracker{name: "sentry"})
	r.RegisterErrorTracker(&mockErrorTracker{name: "datadog"})
	r.RegisterTaskManager(&mockTaskManager{name: "linear"})

	ets := r.ErrorTrackers()
	if len(ets) != 2 {
		t.Fatalf("expected 2 error trackers, got %d", len(ets))
	}

	tms := r.TaskManagers()
	if len(tms) != 1 {
		t.Fatalf("expected 1 task manager, got %d", len(tms))
	}

	ds := r.DocumentStores()
	if len(ds) != 0 {
		t.Fatalf("expected 0 doc stores, got %d", len(ds))
	}
}

func TestRegistry_Summary(t *testing.T) {
	r := NewRegistry()
	r.RegisterErrorTracker(&mockErrorTracker{name: "sentry"})
	r.RegisterTaskManager(&mockTaskManager{name: "linear"})

	summary := r.Summary()
	if len(summary["error_trackers"]) != 1 {
		t.Errorf("expected 1 error tracker in summary, got %d", len(summary["error_trackers"]))
	}
	if len(summary["task_managers"]) != 1 {
		t.Errorf("expected 1 task manager in summary, got %d", len(summary["task_managers"]))
	}
}

func TestRegistry_OverwriteSameName(t *testing.T) {
	r := NewRegistry()

	et1 := &mockErrorTracker{name: "sentry", errors: []ErrorSummary{{ID: "1"}}}
	et2 := &mockErrorTracker{name: "sentry", errors: []ErrorSummary{{ID: "2"}}}

	r.RegisterErrorTracker(et1)
	r.RegisterErrorTracker(et2) // should overwrite

	et, _ := r.ErrorTracker("sentry")
	errors, _ := et.ListErrors(context.Background(), ErrorFilter{})
	if len(errors) != 1 || errors[0].ID != "2" {
		t.Error("expected second registration to overwrite first")
	}
}
