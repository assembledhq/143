package preview

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakeBrowserSessionStore struct {
	mu     sync.Mutex
	record *models.PreviewBrowserSession
	saves  int
}

func (s *fakeBrowserSessionStore) GetBySession(context.Context, uuid.UUID, uuid.UUID) (*models.PreviewBrowserSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.record, nil
}
func (s *fakeBrowserSessionStore) Ensure(_ context.Context, orgID, sessionID, previewID uuid.UUID, contextKey string, viewport models.ViewportSpec) (*models.PreviewBrowserSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record == nil {
		raw, _ := json.Marshal(viewport)
		s.record = &models.PreviewBrowserSession{ID: uuid.New(), OrgID: orgID, SessionID: sessionID, PreviewInstanceID: &previewID, ContextKey: contextKey, ControlState: models.PreviewBrowserControlAgent, Viewport: raw, StorageState: json.RawMessage(`{}`)}
	} else {
		s.record.PreviewInstanceID = &previewID
	}
	copy := *s.record
	return &copy, nil
}
func (s *fakeBrowserSessionStore) GetControl(context.Context, uuid.UUID, uuid.UUID) (*models.PreviewBrowserSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.record, nil
}
func (s *fakeBrowserSessionStore) RequestHandoff(_ context.Context, _, _ uuid.UUID, reason string) (*models.PreviewBrowserSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record.ControlState, s.record.HandoffReason = models.PreviewBrowserControlWaiting, reason
	return s.record, nil
}
func (s *fakeBrowserSessionStore) AcquireHumanControl(_ context.Context, _, _, userID uuid.UUID, duration time.Duration) (*models.PreviewBrowserSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	expires := time.Now().Add(duration)
	s.record.ControlState, s.record.ControlLeaseOwnerID, s.record.ControlLeaseExpiresAt = models.PreviewBrowserControlHuman, &userID, &expires
	return s.record, nil
}
func (s *fakeBrowserSessionStore) ReturnAgentControl(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*models.PreviewBrowserSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record.ControlState, s.record.ControlLeaseOwnerID, s.record.ControlLeaseExpiresAt = models.PreviewBrowserControlAgent, nil, nil
	return s.record, nil
}
func (s *fakeBrowserSessionStore) BeginAgentAction(_ context.Context, _, _ uuid.UUID, token uuid.UUID, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record == nil || s.record.ControlState == "" || s.record.ControlState == models.PreviewBrowserControlAgent {
		return true, nil
	}
	return false, nil
}
func (s *fakeBrowserSessionStore) EndAgentAction(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
	return nil
}
func (s *fakeBrowserSessionStore) BeginHumanAction(_ context.Context, _, _, userID, _ uuid.UUID, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.record != nil && s.record.ControlState == models.PreviewBrowserControlHuman && s.record.ControlLeaseOwnerID != nil && *s.record.ControlLeaseOwnerID == userID, nil
}
func (s *fakeBrowserSessionStore) SaveState(_ context.Context, _, _ uuid.UUID, currentURL string, viewport models.ViewportSpec, storage json.RawMessage, cursor int64, observedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	s.record.CurrentURL = currentURL
	s.record.Viewport, _ = json.Marshal(viewport)
	s.record.StorageState = storage
	s.record.ConsoleCursor = cursor
	s.record.LastObservedAt = &observedAt
	return nil
}

func TestBrowserSessionService_PersistsViewportAction(t *testing.T) {
	t.Parallel()
	store := &fakeBrowserSessionStore{}
	service := NewBrowserSessionService(store, &fakeSessionBrowserInspector{hasContext: true})
	_, err := service.Act(context.Background(), uuid.New(), uuid.New(), uuid.New(), BrowserSessionPolicy{PersistSession: true}, []models.InteractionStep{{Action: "viewport", Value: "390x844"}}, models.PreviewObservationOpts{})
	require.NoError(t, err, "viewport action should succeed")
	var viewport models.ViewportSpec
	require.NoError(t, json.Unmarshal(store.record.Viewport, &viewport), "persisted viewport should decode")
	require.Equal(t, models.ViewportSpec{Width: 390, Height: 844}, viewport, "viewport action should persist the final browser size")
}

func TestBrowserSessionService_ControlHandoffLifecycle(t *testing.T) {
	t.Parallel()
	store := &fakeBrowserSessionStore{}
	orgID, sessionID, previewID, userID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	service := NewBrowserSessionService(store, &fakeSessionBrowserInspector{hasContext: true})
	_, err := service.EnsureIdentity(context.Background(), orgID, sessionID, previewID, BrowserSessionPolicy{})
	require.NoError(t, err, "browser identity should be created before control transitions")
	waiting, err := service.RequestHandoff(context.Background(), orgID, sessionID, "MFA is required")
	require.NoError(t, err, "agent should request a human handoff")
	require.Equal(t, models.PreviewBrowserControlWaiting, waiting.State, "handoff should pause agent control")
	human, err := service.AcquireHumanControl(context.Background(), orgID, sessionID, userID, time.Minute)
	require.NoError(t, err, "human should acquire the requested handoff")
	require.Equal(t, models.PreviewBrowserControlHuman, human.State, "human lease should become authoritative")
	agent, err := service.ReturnAgentControl(context.Background(), orgID, sessionID, userID)
	require.NoError(t, err, "lease owner should return control")
	require.Equal(t, models.PreviewBrowserControlAgent, agent.State, "return should restore agent control")
}

func TestBrowserSessionService_AgentActionFailsWhileHumanControls(t *testing.T) {
	t.Parallel()
	store := &fakeBrowserSessionStore{record: &models.PreviewBrowserSession{ControlState: models.PreviewBrowserControlHuman}}
	service := NewBrowserSessionService(store, &fakeSessionBrowserInspector{hasContext: true})
	_, err := service.Act(context.Background(), uuid.New(), uuid.New(), uuid.New(), BrowserSessionPolicy{}, []models.InteractionStep{{Action: "press", Value: "Enter"}}, models.PreviewObservationOpts{})
	require.ErrorIs(t, err, ErrBrowserControlHeld, "agent actions should fail while a human lease is active")
}

func TestBrowserSessionService_HumanActionRequiresExactLeaseOwner(t *testing.T) {
	t.Parallel()
	ownerID := uuid.New()
	store := &fakeBrowserSessionStore{record: &models.PreviewBrowserSession{ControlState: models.PreviewBrowserControlHuman, ControlLeaseOwnerID: &ownerID}}
	service := NewBrowserSessionService(store, &fakeSessionBrowserInspector{hasContext: true})
	_, err := service.ActAsHuman(context.Background(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), BrowserSessionPolicy{}, []models.InteractionStep{{Action: "press", Value: "Enter"}}, models.PreviewObservationOpts{})
	require.ErrorIs(t, err, ErrBrowserControlHeld, "a different human must not use another user's browser lease")
}

func TestBrowserSessionService_AgentResumesSameContextAfterHumanHandoff(t *testing.T) {
	t.Parallel()
	store := &fakeBrowserSessionStore{}
	inspector := &fakeSessionBrowserInspector{hasContext: true}
	service := NewBrowserSessionService(store, inspector)
	orgID, sessionID, previewID, userID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	_, err := service.EnsureIdentity(context.Background(), orgID, sessionID, previewID, BrowserSessionPolicy{PersistSession: true})
	require.NoError(t, err, "shared browser identity should initialize")
	_, err = service.AcquireHumanControl(context.Background(), orgID, sessionID, userID, time.Minute)
	require.NoError(t, err, "human should acquire the shared context")
	_, err = service.ActAsHuman(context.Background(), orgID, sessionID, previewID, userID, BrowserSessionPolicy{PersistSession: true}, []models.InteractionStep{{Action: "navigate", Value: "/login"}}, models.PreviewObservationOpts{})
	require.NoError(t, err, "human interaction should execute in the session context")
	_, err = service.ReturnAgentControl(context.Background(), orgID, sessionID, userID)
	require.NoError(t, err, "human should return the shared context")
	result, err := service.Act(context.Background(), orgID, sessionID, previewID, BrowserSessionPolicy{PersistSession: true}, []models.InteractionStep{{Action: "press", Value: "Enter"}}, models.PreviewObservationOpts{})
	require.NoError(t, err, "agent should resume after handoff")
	require.Equal(t, models.PreviewBrowserRestorationPreserved, result.Observation.Context.Restoration, "agent should reuse the live context rather than restore a second browser")
	require.Equal(t, 2, inspector.acts, "human and agent should act through the same inspector context")
}

func TestBrowserSessionService_EnsureIdentityWithoutLocalInspector(t *testing.T) {
	t.Parallel()
	store := &fakeBrowserSessionStore{}
	sessionID := uuid.New()
	service := NewBrowserSessionService(store, nil)
	status, err := service.EnsureIdentity(context.Background(), uuid.New(), sessionID, uuid.New(), BrowserSessionPolicy{PersistSession: true})
	require.NoError(t, err, "API nodes should persist browser identity without a local inspector")
	require.Equal(t, "session:"+sessionID.String(), status.ContextKey, "ensure should create the stable session context key")
	require.False(t, status.Reused, "identity creation without an inspector should not claim a live context")
}

func TestBrowserSessionService_RejectsOversizedStorageState(t *testing.T) {
	t.Parallel()
	inspector := &fakeSessionBrowserInspector{hasContext: true, storage: json.RawMessage(bytes.Repeat([]byte("x"), maxBrowserStorageStateBytes+1))}
	service := NewBrowserSessionService(&fakeBrowserSessionStore{}, inspector)
	_, err := service.Observe(context.Background(), uuid.New(), uuid.New(), uuid.New(), BrowserSessionPolicy{PersistSession: true}, models.PreviewObservationOpts{ScreenshotOpts: models.ScreenshotOpts{Path: "/"}})
	require.Error(t, err, "oversized browser state should not be persisted")
	require.Contains(t, err.Error(), "exceeds", "oversized state error should explain the configured bound")
}

func TestBrowserSessionService_ReadOnlyObservationAvoidsPersistenceWrites(t *testing.T) {
	t.Parallel()
	store := &fakeBrowserSessionStore{}
	service := NewBrowserSessionService(store, &fakeSessionBrowserInspector{hasContext: true})
	_, err := service.Observe(context.Background(), uuid.New(), uuid.New(), uuid.New(), BrowserSessionPolicy{PersistSession: true}, models.PreviewObservationOpts{ReadOnly: true, SkipSemantic: true})
	require.NoError(t, err, "read-only watch observation should succeed")
	require.Equal(t, 0, store.saves, "high-frequency preview-panel observations should not write browser state")
}

type fakeSessionBrowserInspector struct {
	mu         sync.Mutex
	hasContext bool
	restores   int
	acts       int
	active     int
	maxActive  int
	delay      time.Duration
	storage    json.RawMessage
	restoreErr error
}

func (i *fakeSessionBrowserInspector) HasContext(models.BrowserTarget) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.hasContext
}
func (i *fakeSessionBrowserInspector) Observe(_ context.Context, target models.BrowserTarget, opts models.PreviewObservationOpts) (*models.PreviewObservation, error) {
	i.mu.Lock()
	i.hasContext = true
	i.mu.Unlock()
	now := time.Now()
	return &models.PreviewObservation{URL: "https://preview.test/app", Title: "App", Viewport: models.ViewportSpec{Width: opts.ViewportW, Height: opts.ViewportH}, CapturedAt: now, ConsoleCursor: 4, Ready: true, Context: models.PreviewBrowserContextStatus{ContextKey: target.ContextKey}}, nil
}
func (i *fakeSessionBrowserInspector) Act(ctx context.Context, target models.BrowserTarget, steps []models.InteractionStep, opts models.PreviewObservationOpts) (*models.PreviewActResult, error) {
	i.mu.Lock()
	i.acts++
	i.active++
	if i.active > i.maxActive {
		i.maxActive = i.active
	}
	i.mu.Unlock()
	if i.delay > 0 {
		time.Sleep(i.delay)
	}
	observation, err := i.Observe(ctx, target, opts)
	i.mu.Lock()
	i.active--
	i.mu.Unlock()
	return &models.PreviewActResult{Interaction: &models.InteractionResult{Steps: []models.StepResult{{StepIndex: 0, Action: steps[0].Action, Success: true}}}, Observation: observation}, err
}
func (i *fakeSessionBrowserInspector) ExportStorage(context.Context, models.BrowserTarget) (json.RawMessage, error) {
	if len(i.storage) == 0 {
		return json.RawMessage(`{"cookies":[]}`), nil
	}
	return i.storage, nil
}
func (i *fakeSessionBrowserInspector) RestoreStorage(context.Context, models.BrowserTarget, json.RawMessage) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.restores++
	i.hasContext = true
	return i.restoreErr
}

func TestBrowserSessionService_ObserveLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		existingContext bool
		storage         json.RawMessage
		persist         bool
		expected        models.PreviewBrowserRestorationStatus
		restores        int
	}{
		{name: "new context starts reset", persist: true, expected: models.PreviewBrowserRestorationReset},
		{name: "live context is preserved", existingContext: true, persist: true, expected: models.PreviewBrowserRestorationPreserved},
		{name: "stored context is restored", storage: json.RawMessage(`{"cookies":[]}`), persist: true, expected: models.PreviewBrowserRestorationRestored, restores: 1},
		{name: "persistence disabled resets", storage: json.RawMessage(`{"cookies":[]}`), expected: models.PreviewBrowserRestorationReset},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, sessionID, previewID := uuid.New(), uuid.New(), uuid.New()
			store := &fakeBrowserSessionStore{}
			if len(tt.storage) > 0 {
				raw, _ := json.Marshal(models.ViewportSpec{Width: 1440, Height: 900})
				store.record = &models.PreviewBrowserSession{ID: uuid.New(), OrgID: orgID, SessionID: sessionID, ContextKey: "session:" + sessionID.String(), Viewport: raw, StorageState: tt.storage}
			}
			inspector := &fakeSessionBrowserInspector{hasContext: tt.existingContext}
			service := NewBrowserSessionService(store, inspector)
			result, err := service.Observe(context.Background(), orgID, sessionID, previewID, BrowserSessionPolicy{PersistSession: tt.persist, AllowedPaths: []string{"/app/**"}}, models.PreviewObservationOpts{})
			require.NoError(t, err, "observe should complete")
			require.Equal(t, tt.expected, result.Context.Restoration, "observe should report the browser restoration outcome")
			require.Equal(t, tt.restores, inspector.restores, "observe should restore stored state only when needed")
			require.Equal(t, 1, store.saves, "observe should persist the latest browser state")
		})
	}
}

func TestBrowserSessionService_ReportsRestoreFailure(t *testing.T) {
	t.Parallel()
	orgID, sessionID, previewID := uuid.New(), uuid.New(), uuid.New()
	viewport, _ := json.Marshal(models.ViewportSpec{Width: 1440, Height: 900})
	store := &fakeBrowserSessionStore{record: &models.PreviewBrowserSession{ID: uuid.New(), OrgID: orgID, SessionID: sessionID, ContextKey: "session:" + sessionID.String(), Viewport: viewport, StorageState: json.RawMessage(`{"cookies":[]}`)}}
	service := NewBrowserSessionService(store, &fakeSessionBrowserInspector{restoreErr: context.DeadlineExceeded})
	observation, err := service.Observe(context.Background(), orgID, sessionID, previewID, BrowserSessionPolicy{PersistSession: true}, models.PreviewObservationOpts{ScreenshotOpts: models.ScreenshotOpts{Path: "/"}})
	require.NoError(t, err, "restore failure should reset to a fresh browser instead of failing observation")
	require.Equal(t, models.PreviewBrowserRestorationReset, observation.Context.Restoration, "restore failure should be reported as a reset")
	require.Contains(t, observation.Context.StatusDetail, "could not be restored", "restore failure should have an actionable status detail")
}

func TestPathAllowed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, value string
		patterns    []string
		expected    bool
	}{
		{name: "wildcard", value: "/anything", patterns: []string{"/**"}, expected: true},
		{name: "prefix", value: "/app/settings?tab=team", patterns: []string{"/app/**"}, expected: true},
		{name: "exact", value: "/login", patterns: []string{"/login"}, expected: true},
		{name: "outside prefix", value: "/admin", patterns: []string{"/app/**"}},
		{name: "absolute URL", value: "https://evil.test/app", patterns: []string{"/**"}},
		{name: "relative path", value: "app", patterns: []string{"/**"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, pathAllowed(tt.value, tt.patterns), "path policy should return the expected decision")
		})
	}
}

func TestBrowserSessionService_ActRejectsDisallowedNavigation(t *testing.T) {
	t.Parallel()
	store := &fakeBrowserSessionStore{}
	inspector := &fakeSessionBrowserInspector{}
	service := NewBrowserSessionService(store, inspector)
	_, err := service.Act(context.Background(), uuid.New(), uuid.New(), uuid.New(), BrowserSessionPolicy{AllowedPaths: []string{"/app/**"}}, []models.InteractionStep{{Action: "navigate", Value: "/admin"}}, models.PreviewObservationOpts{})
	require.ErrorIs(t, err, ErrNavigationNotAllowed, "act should reject navigation outside configured paths")
	require.Equal(t, 0, inspector.acts, "rejected navigation should not reach the browser")
}

func TestBrowserSessionService_SerializesActions(t *testing.T) {
	t.Parallel()
	store := &fakeBrowserSessionStore{}
	inspector := &fakeSessionBrowserInspector{hasContext: true, delay: 20 * time.Millisecond}
	service := NewBrowserSessionService(store, inspector)
	orgID, sessionID, previewID := uuid.New(), uuid.New(), uuid.New()
	start := make(chan struct{})
	done := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := service.Act(context.Background(), orgID, sessionID, previewID, BrowserSessionPolicy{PersistSession: true}, []models.InteractionStep{{Action: "click", Selector: "button"}}, models.PreviewObservationOpts{})
			done <- err
		}()
	}
	close(start)
	require.NoError(t, <-done, "first concurrent action should succeed")
	require.NoError(t, <-done, "second concurrent action should succeed")
	require.Equal(t, 1, inspector.maxActive, "one session should execute only one browser action at a time")
	require.Empty(t, service.locks, "completed actions should release their in-memory session lock")
}

func TestBrowserSessionService_IsolatesSessionContextKeys(t *testing.T) {
	t.Parallel()
	service := NewBrowserSessionService(&fakeBrowserSessionStore{}, &fakeSessionBrowserInspector{})
	orgID, previewID := uuid.New(), uuid.New()
	first, err := service.Observe(context.Background(), orgID, uuid.New(), previewID, BrowserSessionPolicy{PersistSession: true}, models.PreviewObservationOpts{ScreenshotOpts: models.ScreenshotOpts{Path: "/"}})
	require.NoError(t, err, "first session observation should succeed")
	second, err := service.Observe(context.Background(), orgID, uuid.New(), previewID, BrowserSessionPolicy{PersistSession: true}, models.PreviewObservationOpts{ScreenshotOpts: models.ScreenshotOpts{Path: "/"}})
	require.NoError(t, err, "second session observation should succeed")
	require.NotEqual(t, first.Context.ContextKey, second.Context.ContextKey, "different sessions must never share a browser context key")
}
