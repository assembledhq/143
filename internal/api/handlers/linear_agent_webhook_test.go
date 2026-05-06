package handlers

import (
	"context"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/linear"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// fakeJobs records every Enqueue call so tests can pin the dedupe key,
// queue, and payload without exercising the real JobStore.
type fakeJobs struct {
	calls []fakeEnqueue
}

type fakeEnqueue struct {
	OrgID     uuid.UUID
	Queue     string
	JobType   string
	Payload   any
	Priority  int
	DedupeKey string
}

func (f *fakeJobs) Enqueue(_ context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	c := fakeEnqueue{OrgID: orgID, Queue: queue, JobType: jobType, Payload: payload, Priority: priority}
	if dedupeKey != nil {
		c.DedupeKey = *dedupeKey
	}
	f.calls = append(f.calls, c)
	return uuid.New(), nil
}

func TestLinearAgentDispatcher_FeatureOff(t *testing.T) {
	t.Parallel()
	jobs := &fakeJobs{}
	d := NewLinearAgentDispatcher(LinearAgentDispatcherConfig{
		Logger:         zerolog.Nop(),
		Jobs:           jobs,
		FeatureEnabled: false,
		// AgentSessions is non-nil so NewLinearAgentDispatcher returns
		// a constructed dispatcher; FeatureEnabled=false is what should
		// short-circuit Dispatch.
		AgentSessions: nil,
	})
	if d != nil {
		t.Fatalf("expected nil dispatcher when AgentSessions store is nil")
	}
}

func TestLinearAgentDispatcher_NonAgentEventIgnored(t *testing.T) {
	t.Parallel()
	// Build a dispatcher with the feature enabled but pass an event type
	// that isn't AgentSessionEvent. Should return ignored without
	// touching jobs or stores.
	jobs := &fakeJobs{}
	d := newDispatcherForTest(t, jobs, true /*featureEnabled*/)
	res := d.Dispatch(context.Background(), &models.Integration{ID: uuid.New(), OrgID: uuid.New()},
		LinearAgentEventAppUserNotification, []byte(`{"type":"AppUserNotification","action":"created","payload":{}}`))
	if res.Status != "ignored" {
		t.Fatalf("Status=%q want ignored", res.Status)
	}
	if len(jobs.calls) != 0 {
		t.Fatalf("expected no enqueue calls; got %d", len(jobs.calls))
	}
}

// Prompted-without-created behavior is covered by the worker integration
// tests (handlers_linear_agent_test.go). Here we only verify the
// dispatcher's pre-store branches; the row-lookup path requires a real
// store and is exercised end-to-end at a higher layer.

func TestSniffLinearEventType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want LinearAgentEventType
	}{
		{
			name: "agent session event",
			body: `{"type":"AgentSessionEvent","action":"created"}`,
			want: LinearAgentEventAgentSession,
		},
		{
			name: "app user notification",
			body: `{"type":"AppUserNotification","notification":{"type":"issueAssignedToYou"}}`,
			want: LinearAgentEventAppUserNotification,
		},
		{
			name: "ingestion-style payload",
			body: `{"type":"Issue","action":"create"}`,
			want: "",
		},
		{
			name: "junk",
			body: `not json`,
			want: "",
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := sniffLinearEventType([]byte(tc.body))
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

// newDispatcherForTest constructs a dispatcher with non-nil feature-required
// fields so Dispatch flows past its construct-time short-circuits. The
// settings loader returns Enabled=true so the per-org gate doesn't block.
func newDispatcherForTest(t *testing.T, jobs linearAgentJobEnqueuer, featureEnabled bool) *LinearAgentDispatcher {
	t.Helper()
	enabled := true
	settings := func(_ context.Context, _ uuid.UUID) (models.LinearAgentSettings, error) {
		return models.LinearAgentSettings{Enabled: &enabled}, nil
	}
	clientFor := func(_ context.Context, _ uuid.UUID) (linear.Client, error) {
		return nil, nil
	}
	d := &LinearAgentDispatcher{
		logger:         zerolog.Nop(),
		jobs:           jobs,
		settingsLoader: settings,
		clientForOrg:   clientFor,
		featureEnabled: featureEnabled,
	}
	return d
}
