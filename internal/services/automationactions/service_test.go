package automationactions

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"slices"
	"strings"
	"testing"
	"time"
)

type testStore struct {
	scope                           models.AutomationActionScope
	actions                         []models.AutomationAction
	resolveErr, claimErr, finishErr error
	claims                          int
}

func (s *testStore) Resolve(context.Context, uuid.UUID, models.AutomationActionActor) (models.AutomationActionScope, error) {
	return s.scope, s.resolveErr
}
func (s *testStore) ResolveStatus(context.Context, uuid.UUID, models.AutomationActionActor) (models.AutomationActionScope, error) {
	return s.scope, nil
}
func (s *testStore) List(_ context.Context, _ uuid.UUID, _ models.AutomationActionScope, op string) ([]models.AutomationAction, error) {
	out := []models.AutomationAction{}
	for _, a := range s.actions {
		if a.OperationKey == op {
			out = append(out, a)
		}
	}
	return out, nil
}
func (s *testStore) Expire(context.Context, uuid.UUID, models.AutomationActionScope, string) error {
	return nil
}
func (s *testStore) Reserve(_ context.Context, _ uuid.UUID, a models.AutomationActionActor, r models.AutomationActionRequest, digest string, c models.AutomationActionConfig, p json.RawMessage) (models.AutomationAction, error) {
	dest, err := json.Marshal(c)
	if err != nil {
		return models.AutomationAction{}, err
	}
	out := models.AutomationAction{ID: uuid.New(), OperationKey: r.OperationKey, ActionKey: r.ActionKey, Kind: r.Kind, HeadSHA: r.HeadSHA, RequestDigest: digest, Destination: dest, Payload: p, Status: models.AutomationActionPending, CreatedRunID: a.RunID}
	s.actions = append(s.actions, out)
	return out, nil
}
func (s *testStore) Claim(_ context.Context, _ uuid.UUID, _ models.AutomationActionActor, id uuid.UUID) (models.AutomationAction, error) {
	s.claims++
	if s.claimErr != nil {
		return models.AutomationAction{}, s.claimErr
	}
	for i, a := range s.actions {
		if a.ID == id {
			token := uuid.New()
			deadline := time.Now().Add(8 * time.Second)
			a.Status = models.AutomationActionSending
			a.SendToken = &token
			a.SendDeadlineAt = &deadline
			s.actions[i] = a
			return a, nil
		}
	}
	return models.AutomationAction{}, db.ErrAutomationActionBusy
}
func (s *testStore) Finish(_ context.Context, _ uuid.UUID, id, token uuid.UUID, status models.AutomationActionStatus, providerID, url, code string) error {
	if s.finishErr != nil {
		return s.finishErr
	}
	for i, a := range s.actions {
		if a.ID == id {
			a.Status = status
			a.ProviderObjectID = &providerID
			a.ProviderURL = &url
			a.LastErrorCode = &code
			s.actions[i] = a
			return nil
		}
	}
	return errors.New("missing action")
}

type testProvider struct {
	inspections, preflights, sends    int
	head                              string
	closed                            bool
	inspectErr, preflightErr, sendErr error
	sent                              Payload
}

func (p *testProvider) Inspect(context.Context, models.AutomationActionScope) (PullRequest, error) {
	p.inspections++
	return PullRequest{Head: p.head, Open: !p.closed}, p.inspectErr
}
func (p *testProvider) Preflight(context.Context, models.AutomationActionScope, models.AutomationActionRequest) error {
	p.preflights++
	return p.preflightErr
}
func (p *testProvider) Send(_ context.Context, _ models.AutomationActionScope, _ models.AutomationAction, payload Payload) (Receipt, error) {
	p.sends++
	p.sent = payload
	return Receipt{ID: "receipt"}, p.sendErr
}
func actionFixture() (*testStore, *testProvider, models.AutomationActionRequest) {
	return &testStore{scope: models.AutomationActionScope{RepositoryName: "owner/repo", Config: models.AutomationActionConfig{Actions: models.AutomationActionKinds(), Repository: "owner/repo", SlackChannelID: "C0123456789"}}}, &testProvider{head: strings.Repeat("a", 40)}, models.AutomationActionRequest{AutomationActionKey: models.AutomationActionKey{OperationKey: "daily:2026-09-22", ActionKey: "notify"}, Kind: models.AutomationActionSlack, Text: "Daily summary"}
}
func TestIndependentActionReplayAndResume(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		firstError error
		wantStatus models.AutomationActionStatus
		wantSends  int
	}{
		{"success reused", nil, models.AutomationActionSucceeded, 1},
		{"definite failure retried", &SendFailure{Code: "DENIED", Definitive: true}, models.AutomationActionSucceeded, 2},
		{"uncertainty preserved", context.DeadlineExceeded, models.AutomationActionUnknown, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, provider, r := actionFixture()
			provider.sendErr = tt.firstError
			svc := New(store, provider, zerolog.Nop())
			actor := models.AutomationActionActor{OrgID: uuid.New(), RunID: uuid.New()}
			_, err := svc.Execute(context.Background(), actor, r.AutomationActionKey, &r)
			require.NoError(t, err, "first attempt returns recorded state")
			provider.sendErr = nil
			actor.RunID = uuid.New()
			result, err := svc.Execute(context.Background(), actor, r.AutomationActionKey, nil)
			require.NoError(t, err, "later run resumes the same frozen step")
			require.Equal(t, tt.wantStatus, result.Actions[0].Status, "apply certainty-based retry rule")
			require.Equal(t, tt.wantSends, provider.sends, "never replay success or uncertainty")
			require.Equal(t, tt.firstError == nil, result.Reused, "identify a receipt reused without a new send")
			require.Equal(t, 0, provider.inspections, "Slack-only runs do not depend on GitHub")
			require.Equal(t, r, provider.sent.Request, "retries preserve frozen content")
			// Another step of the same kind is independent even if the first is unknown.
			other := r
			other.ActionKey = "notify-second"
			other.Text = "Another explicitly requested step"
			result, err = svc.Execute(context.Background(), actor, other.AutomationActionKey, &other)
			require.NoError(t, err, "independent same-kind step can run")
			require.Equal(t, models.AutomationActionDelivered, result.Status, "result describes the selected step")
			require.Equal(t, tt.wantSends+1, provider.sends, "second key reserves a distinct action")
			status, err := svc.Status(context.Background(), actor, r.OperationKey)
			require.NoError(t, err, "operation status spans both steps")
			require.Equal(t, []string{"notify", "notify-second"}, []string{status.Actions[0].ActionKey, status.Actions[1].ActionKey}, "status includes both durable identities")
		})
	}
}
func TestActionAdmissionAndFencing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(*testStore, *testProvider, *models.AutomationActionRequest)
		want  error
	}{
		{"revoked", func(s *testStore, _ *testProvider, _ *models.AutomationActionRequest) {
			s.resolveErr = db.ErrAutomationActionUnauthorized
		}, db.ErrAutomationActionUnauthorized},
		{"unconfigured kind", func(s *testStore, _ *testProvider, _ *models.AutomationActionRequest) {
			s.scope.Config.Actions = []models.AutomationActionKind{models.AutomationActionLabel}
		}, ErrInvalidRequest},
		{"wrong configured repo", func(s *testStore, _ *testProvider, r *models.AutomationActionRequest) {
			s.scope.Config.Repository = "other/repo"
			r.Kind = models.AutomationActionComment
			r.PRNumber = 42
			r.HeadSHA = strings.Repeat("a", 40)
		}, db.ErrAutomationActionUnauthorized},
		{"wrong target", func(s *testStore, _ *testProvider, r *models.AutomationActionRequest) {
			s.scope.PRNumber = 42
			r.PRNumber = 43
			r.HeadSHA = strings.Repeat("a", 40)
			s.scope.HeadSHA = strings.Repeat("a", 40)
		}, ErrStaleHead},
		{"stale live head", func(_ *testStore, p *testProvider, r *models.AutomationActionRequest) {
			r.PRNumber = 42
			r.HeadSHA = strings.Repeat("b", 40)
		}, ErrStaleHead},
		{"closed PR", func(_ *testStore, p *testProvider, r *models.AutomationActionRequest) {
			r.PRNumber = 42
			r.HeadSHA = p.head
			p.closed = true
		}, ErrStaleHead},
		{"missing provider", func(_ *testStore, p *testProvider, _ *models.AutomationActionRequest) {
			p.preflightErr = errors.New("not connected")
		}, ErrIntegrationNotReady},
		{"revoked at claim", func(s *testStore, _ *testProvider, _ *models.AutomationActionRequest) {
			s.claimErr = db.ErrAutomationActionUnauthorized
		}, db.ErrAutomationActionUnauthorized},
		{"invalid key", func(_ *testStore, _ *testProvider, r *models.AutomationActionRequest) { r.ActionKey = "" }, ErrInvalidRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, p, r := actionFixture()
			tt.setup(s, p, &r)
			_, err := New(s, p, zerolog.Nop()).Execute(context.Background(), models.AutomationActionActor{}, r.AutomationActionKey, &r)
			require.ErrorIs(t, err, tt.want, "invalid authority or precondition must fail closed")
			require.Equal(t, 0, p.sends, "rejected action cannot reach provider")
		})
	}
}
func TestActionConflictAndInterruptedSend(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*testStore, *models.AutomationActionRequest)
		want   error
	}{
		{"changed text", func(_ *testStore, r *models.AutomationActionRequest) { r.Text = "changed" }, db.ErrAutomationActionConflict},
		{"changed destination", func(s *testStore, _ *models.AutomationActionRequest) { s.scope.Config.SlackChannelID = "C9999999999" }, db.ErrAutomationActionConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, p, r := actionFixture()
			svc := New(s, p, zerolog.Nop())
			_, err := svc.Execute(context.Background(), models.AutomationActionActor{}, r.AutomationActionKey, &r)
			require.NoError(t, err, "initial action succeeds")
			tt.change(s, &r)
			result, err := svc.Execute(context.Background(), models.AutomationActionActor{}, r.AutomationActionKey, &r)
			require.ErrorIs(t, err, tt.want, "same keys cannot replace content")
			require.Equal(t, models.AutomationActionDelivered, result.Status, "conflict preserves receipt")
			require.Equal(t, 1, p.sends, "conflict does not create another effect")
		})
	}
}
func TestActionBudgetAndReceiptFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		budget        bool
		finishFailure bool
		wantSends     int
		wantStatus    models.AutomationActionStatus
	}{
		{"insufficient budget leaves pending", true, false, 0, models.AutomationActionPending},
		{"lost receipt leaves fenced sending", false, true, 1, models.AutomationActionSending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, p, r := actionFixture()
			if tt.finishFailure {
				s.finishErr = errors.New("db disconnected")
			}
			ctx := context.Background()
			if tt.budget {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Second)
				defer cancel()
			}
			result, err := New(s, p, zerolog.Nop()).Execute(ctx, models.AutomationActionActor{}, r.AutomationActionKey, &r)
			require.Error(t, err, "report receipt persistence failure or insufficient request budget")
			if tt.budget {
				require.ErrorIs(t, err, ErrBudgetExhausted, "explain the retryable budget interruption")
				require.Equal(t, 0, p.preflights, "do not spend provider budget when sending cannot start")
			}
			require.Equal(t, tt.wantStatus, result.Actions[0].Status, "retain crash-safe state")
			require.Equal(t, tt.wantSends, p.sends, "budget governs send admission")
		})
	}
}

func TestContinuousActionResumePreconditions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		kind       models.AutomationActionKind
		bound      bool
		firstError error
	}{
		{"unbound pending Slack", models.AutomationActionSlack, false, nil},
		{"unbound rejected Slack", models.AutomationActionSlack, false, &SendFailure{Code: "DENIED", Definitive: true}},
		{"unbound pending Notion", models.AutomationActionNotion, false, nil},
		{"bound pending Slack", models.AutomationActionSlack, true, nil},
		{"bound rejected Notion", models.AutomationActionNotion, true, &SendFailure{Code: "DENIED", Definitive: true}},
		{"GitHub always bound", models.AutomationActionComment, true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, p, r := actionFixture()
			s.scope.PRNumber, s.scope.HeadSHA = 42, p.head
			r.Kind = tt.kind
			if tt.kind == models.AutomationActionNotion {
				r.Text = ""
				r.Properties = map[string]string{"Report": "Daily report"}
				s.scope.Config.NotionProperties = map[string]models.AutomationActionPropertyType{"Report": models.AutomationActionPropertyTitle}
			}
			if tt.bound {
				r.PRNumber, r.HeadSHA = 42, p.head
			}
			if tt.firstError == nil {
				p.preflightErr = errors.New("temporarily unavailable")
			} else {
				p.sendErr = tt.firstError
			}
			svc := New(s, p, zerolog.Nop())
			first, err := svc.Execute(context.Background(), models.AutomationActionActor{}, r.AutomationActionKey, &r)
			if tt.firstError == nil {
				require.ErrorIs(t, err, ErrIntegrationNotReady, "reserve without sending before interruption")
			} else {
				require.NoError(t, err, "record definitive failure")
			}
			require.Equal(t, models.AutomationActionPartial, first.Status, "interrupted step remains retryable")
			sends := p.sends
			s.scope.HeadSHA, p.head = strings.Repeat("b", 40), strings.Repeat("b", 40)
			p.sendErr, p.preflightErr = nil, nil
			resumed, err := svc.Execute(context.Background(), models.AutomationActionActor{}, r.AutomationActionKey, nil)
			if tt.bound {
				require.ErrorIs(t, err, ErrStaleHead, "explicit preconditions remain frozen on resume")
				require.Equal(t, sends, p.sends, "never send content tied to an obsolete commit")
			} else {
				require.NoError(t, err, "independent action resumes across target pushes")
				require.Equal(t, models.AutomationActionDelivered, resumed.Status, "finish pending provider work")
				require.Equal(t, sends+1, p.sends, "send unbound step exactly once after recovery")
				require.Equal(t, 0, p.inspections, "unbound actions never inspect GitHub")
			}
		})
	}
}

func TestActionConfigOrderDoesNotChangeDigest(t *testing.T) {
	t.Parallel()
	s, p, r := actionFixture()
	svc := New(s, p, zerolog.Nop())
	_, err := svc.Execute(context.Background(), models.AutomationActionActor{}, r.AutomationActionKey, &r)
	require.NoError(t, err, "record original receipt")
	slices.Reverse(s.scope.Config.Actions)
	result, err := svc.Execute(context.Background(), models.AutomationActionActor{}, r.AutomationActionKey, &r)
	require.NoError(t, err, "order-only edits preserve action identity")
	require.True(t, result.Reused, "return original receipt")
	require.Equal(t, 1, p.sends, "do not resend after a checkbox reorder")
}
