package preview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const maxBrowserStorageStateBytes = 1024 * 1024

var (
	ErrBrowserUnavailable   = errors.New("preview browser unavailable")
	ErrNavigationNotAllowed = errors.New("preview navigation not allowed")
	ErrBrowserControlHeld   = errors.New("preview browser control is held by another actor")
	ErrBrowserControlBusy   = errors.New("preview browser action is already in progress")
)

type BrowserSessionStore interface {
	GetBySession(ctx context.Context, orgID, sessionID uuid.UUID) (*models.PreviewBrowserSession, error)
	Ensure(ctx context.Context, orgID, sessionID, previewID uuid.UUID, contextKey string, viewport models.ViewportSpec) (*models.PreviewBrowserSession, error)
	SaveState(ctx context.Context, orgID, sessionID uuid.UUID, currentURL string, viewport models.ViewportSpec, storageState json.RawMessage, consoleCursor int64, observedAt time.Time) error
	GetControl(ctx context.Context, orgID, sessionID uuid.UUID) (*models.PreviewBrowserSession, error)
	RequestHandoff(ctx context.Context, orgID, sessionID uuid.UUID, reason string) (*models.PreviewBrowserSession, error)
	AcquireHumanControl(ctx context.Context, orgID, sessionID, userID uuid.UUID, duration time.Duration) (*models.PreviewBrowserSession, error)
	ReturnAgentControl(ctx context.Context, orgID, sessionID, userID uuid.UUID) (*models.PreviewBrowserSession, error)
	BeginAgentAction(ctx context.Context, orgID, sessionID, token uuid.UUID, duration time.Duration) (bool, error)
	EndAgentAction(ctx context.Context, orgID, sessionID, token uuid.UUID) error
	BeginHumanAction(ctx context.Context, orgID, sessionID, userID, token uuid.UUID, duration time.Duration) (bool, error)
}

func controlStatus(record *models.PreviewBrowserSession) *models.PreviewBrowserControlStatus {
	if record == nil {
		return nil
	}
	return &models.PreviewBrowserControlStatus{State: record.ControlState, LeaseOwnerID: record.ControlLeaseOwnerID, LeaseExpiresAt: record.ControlLeaseExpiresAt, HandoffReason: record.HandoffReason}
}

func (s *BrowserSessionService) GetControl(ctx context.Context, orgID, sessionID uuid.UUID) (*models.PreviewBrowserControlStatus, error) {
	record, err := s.store.GetControl(ctx, orgID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("get browser control: %w", err)
	}
	if record == nil {
		return nil, ErrBrowserUnavailable
	}
	return controlStatus(record), nil
}

func (s *BrowserSessionService) RequestHandoff(ctx context.Context, orgID, sessionID uuid.UUID, reason string) (*models.PreviewBrowserControlStatus, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, fmt.Errorf("handoff reason is required")
	}
	if len(reason) > 500 {
		return nil, fmt.Errorf("handoff reason must be at most 500 characters")
	}
	record, err := s.store.RequestHandoff(ctx, orgID, sessionID, reason)
	if err != nil {
		return nil, fmt.Errorf("request browser handoff: %w", err)
	}
	return controlStatus(record), nil
}

func (s *BrowserSessionService) AcquireHumanControl(ctx context.Context, orgID, sessionID, userID uuid.UUID, duration time.Duration) (*models.PreviewBrowserControlStatus, error) {
	if userID == uuid.Nil {
		return nil, fmt.Errorf("control lease owner is required")
	}
	if duration <= 0 {
		duration = 5 * time.Minute
	}
	if duration > 15*time.Minute {
		duration = 15 * time.Minute
	}
	record, err := s.store.AcquireHumanControl(ctx, orgID, sessionID, userID, duration)
	if errors.Is(err, pgx.ErrNoRows) || strings.Contains(fmt.Sprint(err), pgx.ErrNoRows.Error()) {
		return nil, ErrBrowserControlBusy
	}
	if err != nil {
		return nil, fmt.Errorf("acquire human browser control: %w", err)
	}
	return controlStatus(record), nil
}

func (s *BrowserSessionService) ReturnAgentControl(ctx context.Context, orgID, sessionID, userID uuid.UUID) (*models.PreviewBrowserControlStatus, error) {
	record, err := s.store.ReturnAgentControl(ctx, orgID, sessionID, userID)
	if errors.Is(err, pgx.ErrNoRows) || strings.Contains(fmt.Sprint(err), pgx.ErrNoRows.Error()) {
		return nil, ErrBrowserControlHeld
	}
	if err != nil {
		return nil, fmt.Errorf("return agent browser control: %w", err)
	}
	return controlStatus(record), nil
}

type BrowserSessionPolicy struct {
	PersistSession  bool
	DefaultViewport models.ViewportSpec
	AllowedPaths    []string
}

type BrowserSessionService struct {
	store     BrowserSessionStore
	inspector SessionBrowserInspector
	locksMu   sync.Mutex
	locks     map[uuid.UUID]*browserSessionLock
}

type browserSessionLock struct {
	mu   sync.Mutex
	refs int
}

func NewBrowserSessionService(store BrowserSessionStore, inspector SessionBrowserInspector) *BrowserSessionService {
	return &BrowserSessionService{store: store, inspector: inspector, locks: make(map[uuid.UUID]*browserSessionLock)}
}

func (s *BrowserSessionService) EnsureIdentity(ctx context.Context, orgID, sessionID, previewID uuid.UUID, policy BrowserSessionPolicy) (*models.PreviewBrowserContextStatus, error) {
	if s == nil || s.store == nil {
		return nil, ErrBrowserUnavailable
	}
	policy = normalizeBrowserPolicy(policy)
	contextKey := "session:" + sessionID.String()
	if _, err := s.store.Ensure(ctx, orgID, sessionID, previewID, contextKey, policy.DefaultViewport); err != nil {
		return nil, fmt.Errorf("ensure browser identity: %w", err)
	}
	reused := s.inspector != nil && s.inspector.HasContext(models.BrowserTarget{PreviewID: previewID.String(), SessionID: sessionID.String(), ContextKey: contextKey})
	return &models.PreviewBrowserContextStatus{ContextKey: contextKey, Reused: reused, Persisted: policy.PersistSession, Restoration: models.PreviewBrowserRestorationUnavailable, StatusDetail: "browser identity is ready; live context starts on first observation"}, nil
}

func (s *BrowserSessionService) acquireSession(sessionID uuid.UUID) func() {
	s.locksMu.Lock()
	entry := s.locks[sessionID]
	if entry == nil {
		entry = &browserSessionLock{}
		s.locks[sessionID] = entry
	}
	entry.refs++
	s.locksMu.Unlock()
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		s.locksMu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(s.locks, sessionID)
		}
		s.locksMu.Unlock()
	}
}

func normalizeBrowserPolicy(policy BrowserSessionPolicy) BrowserSessionPolicy {
	if policy.DefaultViewport.Width == 0 {
		policy.DefaultViewport.Width = 1440
	}
	if policy.DefaultViewport.Height == 0 {
		policy.DefaultViewport.Height = 900
	}
	if len(policy.AllowedPaths) == 0 {
		policy.AllowedPaths = []string{"/**"}
	}
	return policy
}

func (s *BrowserSessionService) prepare(ctx context.Context, orgID, sessionID, previewID uuid.UUID, policy BrowserSessionPolicy) (models.BrowserTarget, *models.PreviewBrowserSession, models.PreviewBrowserRestorationStatus, string, error) {
	if s == nil || s.store == nil || s.inspector == nil {
		return models.BrowserTarget{}, nil, models.PreviewBrowserRestorationUnavailable, "browser service is not configured", ErrBrowserUnavailable
	}
	policy = normalizeBrowserPolicy(policy)
	target := models.BrowserTarget{PreviewID: previewID.String(), SessionID: sessionID.String(), ContextKey: "session:" + sessionID.String()}
	record, err := s.store.Ensure(ctx, orgID, sessionID, previewID, target.ContextKey, policy.DefaultViewport)
	if err != nil {
		return target, nil, models.PreviewBrowserRestorationUnavailable, "browser session persistence failed", fmt.Errorf("ensure browser session: %w", err)
	}
	if s.inspector.HasContext(target) {
		return target, record, models.PreviewBrowserRestorationPreserved, "live browser context reused", nil
	}
	if !policy.PersistSession || len(record.StorageState) == 0 || string(record.StorageState) == "{}" {
		return target, record, models.PreviewBrowserRestorationReset, "no restorable browser state was available", nil
	}
	if err := s.inspector.RestoreStorage(ctx, target, record.StorageState); err != nil {
		return target, record, models.PreviewBrowserRestorationReset, "stored browser state was incompatible or could not be restored", nil
	}
	return target, record, models.PreviewBrowserRestorationRestored, "stored browser state restored", nil
}

func (s *BrowserSessionService) Observe(ctx context.Context, orgID, sessionID, previewID uuid.UUID, policy BrowserSessionPolicy, opts models.PreviewObservationOpts) (*models.PreviewObservation, error) {
	if opts.Path == "" {
		return s.observe(ctx, orgID, sessionID, previewID, policy, opts)
	}
	token := uuid.New()
	started, err := s.store.BeginAgentAction(ctx, orgID, sessionID, token, 5*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("acquire agent browser navigation: %w", err)
	}
	if !started {
		return nil, ErrBrowserControlHeld
	}
	result, observeErr := s.observe(ctx, orgID, sessionID, previewID, policy, opts)
	endErr := s.store.EndAgentAction(context.WithoutCancel(ctx), orgID, sessionID, token)
	if endErr != nil {
		endErr = fmt.Errorf("release agent browser navigation: %w", endErr)
	}
	return result, errors.Join(observeErr, endErr)
}

func (s *BrowserSessionService) observe(ctx context.Context, orgID, sessionID, previewID uuid.UUID, policy BrowserSessionPolicy, opts models.PreviewObservationOpts) (*models.PreviewObservation, error) {
	policy = normalizeBrowserPolicy(policy)
	release := s.acquireSession(sessionID)
	defer release()
	target, record, restoration, restorationDetail, err := s.prepare(ctx, orgID, sessionID, previewID, policy)
	if err != nil {
		return nil, err
	}
	if opts.ConsoleCursor == 0 {
		opts.ConsoleCursor = record.ConsoleCursor
	}
	if opts.ViewportW == 0 || opts.ViewportH == 0 {
		var saved models.ViewportSpec
		if json.Unmarshal(record.Viewport, &saved) == nil {
			opts.ViewportW, opts.ViewportH = saved.Width, saved.Height
		}
	}
	if opts.Path == "" && restoration == models.PreviewBrowserRestorationReset {
		opts.Path = initialBrowserPath(record.CurrentURL, policy.AllowedPaths)
	}
	if opts.Path != "" && !pathAllowed(opts.Path, policy.AllowedPaths) {
		return nil, ErrNavigationNotAllowed
	}
	observation, err := s.inspector.Observe(ctx, target, opts)
	if err != nil {
		return nil, err
	}
	observation.Context.Restoration = restoration
	observation.Context.StatusDetail = restorationDetail
	observation.Context.Persisted = policy.PersistSession
	if !urlPathAllowed(observation.URL, policy.AllowedPaths) {
		return nil, ErrNavigationNotAllowed
	}
	if !opts.ReadOnly {
		persistedCursor := observation.ConsoleCursor
		if opts.PreserveConsoleCursor {
			persistedCursor = record.ConsoleCursor
		}
		if err := s.persist(ctx, orgID, sessionID, target, policy, observation, persistedCursor); err != nil {
			return nil, err
		}
	}
	return observation, nil
}

func (s *BrowserSessionService) Act(ctx context.Context, orgID, sessionID, previewID uuid.UUID, policy BrowserSessionPolicy, steps []models.InteractionStep, opts models.PreviewObservationOpts) (*models.PreviewActResult, error) {
	token := uuid.New()
	started, err := s.store.BeginAgentAction(ctx, orgID, sessionID, token, 5*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("acquire agent browser action: %w", err)
	}
	if !started {
		control, controlErr := s.GetControl(ctx, orgID, sessionID)
		if controlErr == nil && control.State != models.PreviewBrowserControlAgent {
			return nil, ErrBrowserControlHeld
		}
		return nil, ErrBrowserControlBusy
	}
	result, actErr := s.act(ctx, orgID, sessionID, previewID, policy, steps, opts)
	endErr := s.store.EndAgentAction(context.WithoutCancel(ctx), orgID, sessionID, token)
	if endErr != nil {
		endErr = fmt.Errorf("release agent browser action: %w", endErr)
	}
	return result, errors.Join(actErr, endErr)
}

func (s *BrowserSessionService) ActAsHuman(ctx context.Context, orgID, sessionID, previewID, userID uuid.UUID, policy BrowserSessionPolicy, steps []models.InteractionStep, opts models.PreviewObservationOpts) (*models.PreviewActResult, error) {
	token := uuid.New()
	allowed, err := s.store.BeginHumanAction(ctx, orgID, sessionID, userID, token, 5*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("acquire human browser action: %w", err)
	}
	if !allowed {
		return nil, ErrBrowserControlHeld
	}
	result, actErr := s.act(ctx, orgID, sessionID, previewID, policy, steps, opts)
	endErr := s.store.EndAgentAction(context.WithoutCancel(ctx), orgID, sessionID, token)
	if endErr != nil {
		endErr = fmt.Errorf("release human browser action: %w", endErr)
	}
	return result, errors.Join(actErr, endErr)
}

func (s *BrowserSessionService) act(ctx context.Context, orgID, sessionID, previewID uuid.UUID, policy BrowserSessionPolicy, steps []models.InteractionStep, opts models.PreviewObservationOpts) (*models.PreviewActResult, error) {
	policy = normalizeBrowserPolicy(policy)
	release := s.acquireSession(sessionID)
	defer release()
	for _, step := range steps {
		if step.Action == "navigate" && !pathAllowed(step.Value, policy.AllowedPaths) {
			return nil, ErrNavigationNotAllowed
		}
	}
	target, record, restoration, restorationDetail, err := s.prepare(ctx, orgID, sessionID, previewID, policy)
	if err != nil {
		return nil, err
	}
	if opts.ConsoleCursor == 0 {
		opts.ConsoleCursor = record.ConsoleCursor
	}
	if opts.ViewportW == 0 || opts.ViewportH == 0 {
		var saved models.ViewportSpec
		if json.Unmarshal(record.Viewport, &saved) == nil {
			opts.ViewportW, opts.ViewportH = saved.Width, saved.Height
		}
	}
	for _, step := range steps {
		if step.Action != "viewport" {
			continue
		}
		if width, height, viewportErr := parseViewportValue(step.Value); viewportErr == nil {
			opts.ViewportW, opts.ViewportH = width, height
		}
	}
	if restoration == models.PreviewBrowserRestorationReset {
		initialOpts := opts
		initialOpts.Path = initialBrowserPath(record.CurrentURL, policy.AllowedPaths)
		if _, initErr := s.inspector.Observe(ctx, target, initialOpts); initErr != nil {
			return nil, fmt.Errorf("initialize browser page: %w", initErr)
		}
	}
	result, err := s.inspector.Act(ctx, target, steps, opts)
	if err != nil {
		return nil, err
	}
	if result.Observation != nil {
		result.Observation.Context.Restoration = restoration
		result.Observation.Context.StatusDetail = restorationDetail
		result.Observation.Context.Persisted = policy.PersistSession
		if !urlPathAllowed(result.Observation.URL, policy.AllowedPaths) {
			return nil, ErrNavigationNotAllowed
		}
		if err := s.persist(ctx, orgID, sessionID, target, policy, result.Observation, result.Observation.ConsoleCursor); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *BrowserSessionService) persist(ctx context.Context, orgID, sessionID uuid.UUID, target models.BrowserTarget, policy BrowserSessionPolicy, observation *models.PreviewObservation, consoleCursor int64) error {
	storage := json.RawMessage(`{}`)
	if policy.PersistSession {
		exported, err := s.inspector.ExportStorage(ctx, target)
		if err != nil {
			return fmt.Errorf("export browser state: %w", err)
		}
		if len(exported) > maxBrowserStorageStateBytes {
			return fmt.Errorf("browser storage state exceeds %d bytes", maxBrowserStorageStateBytes)
		}
		storage = exported
	}
	if err := s.store.SaveState(ctx, orgID, sessionID, observation.URL, observation.Viewport, storage, consoleCursor, observation.CapturedAt); err != nil {
		return fmt.Errorf("persist browser state: %w", err)
	}
	return nil
}

func pathAllowed(raw string, patterns []string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.IsAbs() || !strings.HasPrefix(parsed.Path, "/") {
		return false
	}
	clean := path.Clean(parsed.Path)
	for _, pattern := range patterns {
		if pattern == "/**" {
			return true
		}
		if strings.HasSuffix(pattern, "/**") && (clean == strings.TrimSuffix(pattern, "/**") || strings.HasPrefix(clean, strings.TrimSuffix(pattern, "**"))) {
			return true
		}
		if clean == pattern {
			return true
		}
	}
	return false
}

func urlPathAllowed(raw string, patterns []string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return pathAllowed(parsed.RequestURI(), patterns)
}

func initialBrowserPath(currentURL string, patterns []string) string {
	if parsed, err := url.Parse(currentURL); err == nil && pathAllowed(parsed.RequestURI(), patterns) {
		return parsed.RequestURI()
	}
	for _, pattern := range patterns {
		if pattern == "/**" {
			return "/"
		}
		if strings.HasSuffix(pattern, "/**") {
			return strings.TrimSuffix(pattern, "**")
		}
		if pathAllowed(pattern, patterns) {
			return pattern
		}
	}
	return "/"
}
