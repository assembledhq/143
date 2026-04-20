package preview

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

// =============================================================================
// Concurrency caps (MVP defaults)
// =============================================================================

const (
	DefaultMaxPreviewsPerUser   = 2
	DefaultMaxPreviewsPerOrg    = 5
	DefaultMaxPreviewsPerWorker = 3

	DefaultIdleTimeout = 15 * time.Minute
	DefaultHardTTL     = 30 * time.Minute
	DefaultMaxTTL      = 2 * time.Hour

	// ProviderDocker is the provider identifier for Docker-based previews.
	ProviderDocker = "docker"
)

// =============================================================================
// Manager
// =============================================================================

// Manager owns the preview lifecycle. It coordinates between the store
// (persistence), the provider (sandbox/container management), and the
// config parser (validation).
//
// It is separate from HTTP handlers so the lifecycle logic does not leak
// into routers.
type Manager struct {
	store        *db.PreviewStore
	sessionStore *db.SessionStore
	provider     PreviewCapableProvider
	logger       zerolog.Logger

	workerNodeID string // identity of this worker for routing

	// previewOriginTemplate is used to compute PREVIEW_ORIGIN for each
	// preview instance. "{id}" is replaced with the instance UUID. When
	// empty, PREVIEW_ORIGIN is not injected.
	previewOriginTemplate string

	// hmrWatcher captures screenshots on HMR updates. May be nil.
	hmrWatcher *HMRWatcher

	// Inspector is the headless browser used for screenshot capture, DOM
	// inspection, and interaction replay. It may be nil if the headless
	// browser has not been configured on this worker node.
	inspectorMu sync.RWMutex
	inspector   PreviewInspector

	// snapshotCache handles filesystem snapshot caching for fast startup.
	snapshotCache *SnapshotCache

	// pollStopChs tracks stop channels for pollSupportServiceStatus goroutines,
	// keyed by preview ID. Closing the channel stops the poll goroutine,
	// preventing it from overwriting a "stopped" status with "ready".
	pollStopMu  sync.Mutex
	pollStopChs map[uuid.UUID]chan struct{}

	// Caps (configurable per org in future; hardcoded for MVP).
	maxPerUser   int
	maxPerOrg    int
	maxPerWorker int
}

// ManagerConfig holds initialization options for the preview Manager.
type ManagerConfig struct {
	Store         *db.PreviewStore
	SessionStore  *db.SessionStore
	Provider      PreviewCapableProvider
	Inspector     PreviewInspector
	SnapshotCache *SnapshotCache
	HMRWatcher    *HMRWatcher // optional; enables HMR screenshot capture
	Logger        zerolog.Logger
	WorkerNodeID  string
	MaxPerUser    int
	MaxPerOrg     int
	MaxPerWorker  int

	// PreviewOriginTemplate is the URL template used to compute the public
	// origin each preview is served from, with "{id}" replaced by the preview
	// instance UUID. It is passed through to each service as PREVIEW_ORIGIN so
	// backends can generate absolute URLs that round-trip through the gateway.
	// When empty (e.g. in tests), PREVIEW_ORIGIN is not injected.
	PreviewOriginTemplate string
}

// NewManager creates a new preview Manager. If cfg.Provider is nil, the
// manager is created but any operation that requires the provider (StartPreview,
// StopPreview, DialPreview, etc.) will return an error.
func NewManager(cfg ManagerConfig) *Manager {
	if cfg.Provider == nil {
		cfg.Logger.Warn().Msg("preview.NewManager: Provider is nil — preview operations will fail until a provider is set")
	}
	m := &Manager{
		store:                 cfg.Store,
		sessionStore:          cfg.SessionStore,
		provider:              cfg.Provider,
		inspector:             cfg.Inspector,
		snapshotCache:         cfg.SnapshotCache,
		hmrWatcher:            cfg.HMRWatcher,
		logger:                cfg.Logger,
		workerNodeID:          cfg.WorkerNodeID,
		previewOriginTemplate: cfg.PreviewOriginTemplate,
		pollStopChs:           make(map[uuid.UUID]chan struct{}),
		maxPerUser:            cfg.MaxPerUser,
		maxPerOrg:             cfg.MaxPerOrg,
		maxPerWorker:          cfg.MaxPerWorker,
	}
	if m.maxPerUser <= 0 {
		m.maxPerUser = DefaultMaxPreviewsPerUser
	}
	if m.maxPerOrg <= 0 {
		m.maxPerOrg = DefaultMaxPreviewsPerOrg
	}
	if m.maxPerWorker <= 0 {
		m.maxPerWorker = DefaultMaxPreviewsPerWorker
	}
	return m
}

// =============================================================================
// StartPreviewInput
// =============================================================================

// StartPreviewInput contains everything needed to start a new preview.
type StartPreviewInput struct {
	SessionID     uuid.UUID
	OrgID         uuid.UUID
	UserID        uuid.UUID // for per-user concurrency cap
	Sandbox       *agent.Sandbox
	Config        *models.PreviewConfig
	BaseCommitSHA string
	ProfileName   string
}

// =============================================================================
// StartPreview
// =============================================================================

// StartPreview validates caps, resolves config, starts the preview via the
// provider, and persists the result.
func (m *Manager) StartPreview(ctx context.Context, input StartPreviewInput) (*models.PreviewInstance, error) {
	// 0. Validate required pointers.
	if m.provider == nil {
		return nil, fmt.Errorf("preview provider is not configured")
	}
	if input.Sandbox == nil {
		return nil, fmt.Errorf("sandbox must not be nil")
	}

	// 1. Validate the config.
	if errs := ValidateConfig(input.Config); len(errs) > 0 {
		return nil, fmt.Errorf("invalid preview config: %s", strings.Join(errs, "; "))
	}

	// 2. Check for existing active preview on this session.
	existing, err := m.store.GetActivePreviewForSession(ctx, input.OrgID, input.SessionID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("check existing preview: %w", err)
	} else if err == nil && existing != nil {
		return nil, fmt.Errorf("session already has an active preview (id=%s)", existing.ID)
	}

	// 3. Enforce concurrency caps.
	if err := m.checkConcurrencyCaps(ctx, input.OrgID, input.UserID); err != nil {
		return nil, err
	}

	// 4. Resolve resource limits.
	limits := ResolveResourceLimits(input.Config)

	// 5. Compute config digest.
	configDigest := computeConfigDigest(input.Config)

	// 6. Create the preview instance record (status=starting).
	profileName := input.ProfileName
	if profileName == "" {
		profileName = string(models.PreviewProfileBootstrap)
	}
	instance := &models.PreviewInstance{
		SessionID:      input.SessionID,
		OrgID:          input.OrgID,
		UserID:         input.UserID,
		ProfileName:    profileName,
		Name:           input.Config.Name,
		Status:         models.PreviewStatusStarting,
		Provider:       ProviderDocker,
		WorkerNodeID:   m.workerNodeID,
		PrimaryService: input.Config.Primary,
		ConfigDigest:   configDigest,
		BaseCommitSHA:  input.BaseCommitSHA,
		ExpiresAt:      time.Now().Add(DefaultHardTTL),
		LastPath:       "/",
		MemoryLimitMB:  limits.MemoryMB,
		CPULimitMillis: limits.CPUMillis,
	}
	if err := storeRecycleInput(instance, input); err != nil {
		return nil, fmt.Errorf("store recycle input: %w", err)
	}

	if err := m.store.CreatePreviewInstance(ctx, instance); err != nil {
		return nil, fmt.Errorf("create preview instance: %w", err)
	}

	m.logger.Info().
		Str("preview_id", instance.ID.String()).
		Str("session_id", input.SessionID.String()).
		Str("name", input.Config.Name).
		Msg("starting preview")

	// 7. Create service records.
	for name, svcCfg := range input.Config.Services {
		role := models.PreviewServiceRoleSupport
		if name == input.Config.Primary {
			role = models.PreviewServiceRolePrimary
		}
		svc := &models.PreviewService{
			PreviewInstanceID: instance.ID,
			ServiceName:       name,
			Role:              role,
			Status:            models.PreviewServiceStatusStarting,
			Command:           svcCfg.Command,
			Cwd:               svcCfg.Cwd,
			Port:              svcCfg.Port,
		}
		if err := m.store.CreatePreviewService(ctx, svc); err != nil {
			if statusErr := m.store.UpdatePreviewStatus(ctx, input.OrgID, instance.ID, models.PreviewStatusFailed,
				fmt.Sprintf("failed to create service record for %q: %v", name, err)); statusErr != nil {
				m.logger.Warn().Err(statusErr).Str("preview_id", instance.ID.String()).Msg("failed to update preview status to failed")
			}
			return nil, fmt.Errorf("create service record %q: %w", name, err)
		}
	}

	// 8. Create infrastructure records.
	for name, infraCfg := range input.Config.Infrastructure {
		infra := &models.PreviewInfrastructure{
			PreviewInstanceID: instance.ID,
			InfraName:         name,
			Template:          infraCfg.Template,
			Status:            models.PreviewInfraStatusProvisioning,
		}
		if err := m.store.CreatePreviewInfrastructure(ctx, infra); err != nil {
			if statusErr := m.store.UpdatePreviewStatus(ctx, input.OrgID, instance.ID, models.PreviewStatusFailed,
				fmt.Sprintf("failed to create infrastructure record for %q: %v", name, err)); statusErr != nil {
				m.logger.Warn().Err(statusErr).Str("preview_id", instance.ID.String()).Msg("failed to update preview status to failed")
			}
			return nil, fmt.Errorf("create infrastructure record %q: %w", name, err)
		}
	}

	// 9. Start the preview via the provider (async-friendly).
	handle, err := m.provider.StartPreview(ctx, input.Sandbox, input.Config, m.platformEnv(instance.ID))
	if err != nil {
		if statusErr := m.store.UpdatePreviewStatus(ctx, input.OrgID, instance.ID, models.PreviewStatusFailed, err.Error()); statusErr != nil {
			m.logger.Warn().Err(statusErr).Str("preview_id", instance.ID.String()).Msg("failed to update preview status to failed")
		}
		return nil, fmt.Errorf("provider start preview: %w", err)
	}

	// 10. Update instance with handle and port.
	instance.PreviewHandle = handle.Handle
	instance.Port = handle.PrimaryPort

	// Persist the handle first — if this fails, the DB row has no route info
	// and subsequent proxy/status calls will break. Stop the provider and fail.
	if err := m.store.UpdatePreviewHandle(ctx, input.OrgID, instance.ID, handle.Handle, handle.PrimaryPort); err != nil {
		m.logger.Error().Err(err).Str("preview_id", instance.ID.String()).Msg("failed to update handle in DB, stopping provider")
		_ = m.provider.StopPreview(ctx, handle.Handle)
		if statusErr := m.store.UpdatePreviewStatus(ctx, input.OrgID, instance.ID, models.PreviewStatusFailed, "failed to persist preview handle"); statusErr != nil {
			m.logger.Warn().Err(statusErr).Str("preview_id", instance.ID.String()).Msg("failed to update preview status to failed after handle error")
		}
		return nil, fmt.Errorf("persist preview handle: %w", err)
	}

	// Set status based on progressive preview support. Use conditional update
	// so that a concurrent StopPreview cannot be overwritten.
	nextStatus := models.PreviewStatusReady
	if handle.PartiallyReady {
		nextStatus = models.PreviewStatusPartiallyReady
	}
	updated, err := m.store.UpdatePreviewStatusIfActive(ctx, input.OrgID, instance.ID, nextStatus, "")
	if err != nil {
		m.logger.Error().Err(err).Str("preview_id", instance.ID.String()).Msg("failed to update preview status after start")
	}
	if !updated {
		// Preview was stopped concurrently — clean up the provider.
		m.logger.Warn().Str("preview_id", instance.ID.String()).Msg("preview was stopped during startup, cleaning up provider")
		_ = m.provider.StopPreview(ctx, handle.Handle)
		return nil, fmt.Errorf("preview was stopped concurrently during startup")
	}
	instance.Status = nextStatus

	// 11. Update infrastructure records with container details.
	if statusSnap, err := m.provider.PreviewStatus(ctx, handle.Handle); err == nil {
		for _, infraSnap := range statusSnap.Infrastructure {
			if err := m.store.UpdateInfraStatus(ctx, input.OrgID, instance.ID, infraSnap.Name, models.PreviewInfraStatusHealthy, ""); err != nil {
				m.logger.Warn().Err(err).Str("preview_id", instance.ID.String()).Str("infra", infraSnap.Name).Msg("failed to update infra status after start")
			}
		}
		for _, svcSnap := range statusSnap.Services {
			if err := m.store.UpdateServiceStatus(ctx, input.OrgID, instance.ID, svcSnap.Name, svcSnap.Status, svcSnap.Error); err != nil {
				m.logger.Warn().Err(err).Str("preview_id", instance.ID.String()).Str("service", svcSnap.Name).Msg("failed to update service status after start")
			}
			if svcSnap.PID > 0 {
				if err := m.store.UpdateServicePID(ctx, input.OrgID, instance.ID, svcSnap.Name, svcSnap.PID); err != nil {
					m.logger.Warn().Err(err).Str("preview_id", instance.ID.String()).Str("service", svcSnap.Name).Msg("failed to update service PID after start")
				}
			}
		}
	}

	m.logger.Info().
		Str("preview_id", instance.ID.String()).
		Str("handle", handle.Handle).
		Int("primary_port", handle.PrimaryPort).
		Msg("preview ready")

	// When the preview started with progressive readiness, support services
	// are still being probed in the background. Poll the provider until all
	// services leave "starting" status, then persist the final statuses to
	// the database so the API returns up-to-date information.
	if handle.PartiallyReady {
		stopCh := make(chan struct{})
		m.pollStopMu.Lock()
		m.pollStopChs[instance.ID] = stopCh
		m.pollStopMu.Unlock()
		go func() {
			m.pollSupportServiceStatus(stopCh, input.OrgID, instance.ID, handle.Handle)
			m.pollStopMu.Lock()
			delete(m.pollStopChs, instance.ID)
			m.pollStopMu.Unlock()
		}()
	}

	// Start HMR watching now that the preview is up so the gateway can
	// detect live-reload messages and capture screenshots.
	if m.hmrWatcher != nil {
		m.hmrWatcher.StartWatching(instance.ID, input.OrgID)
	}

	return instance, nil
}

// pollSupportServiceStatus polls the provider until all support services leave
// "starting" status, then persists the final statuses to the database. This
// runs in a background goroutine after a progressive preview start.
//
// The stopCh is closed by StopPreview to interrupt the poll early and prevent
// the goroutine from overwriting the "stopped" status with "ready".
func (m *Manager) pollSupportServiceStatus(stopCh <-chan struct{}, orgID, previewID uuid.UUID, handle string) {
	const (
		pollInterval = 3 * time.Second
		maxPollTime  = 5 * time.Minute
	)

	ctx, cancel := context.WithTimeout(context.Background(), maxPollTime)
	defer cancel()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stopCh:
			m.logger.Info().
				Str("preview_id", previewID.String()).
				Msg("poll stopped: preview is shutting down")
			return
		case <-ctx.Done():
			m.logger.Warn().
				Str("preview_id", previewID.String()).
				Msg("timed out polling support service status")
			return
		case <-ticker.C:
			// Check if the preview is still active before writing status.
			instance, err := m.store.GetPreviewInstance(ctx, orgID, previewID)
			if err != nil || instance.Status.IsTerminal() {
				m.logger.Info().
					Str("preview_id", previewID.String()).
					Msg("poll stopped: preview is no longer active")
				return
			}

			snap, err := m.provider.PreviewStatus(ctx, handle)
			if err != nil {
				m.logger.Debug().Err(err).
					Str("preview_id", previewID.String()).
					Msg("poll support service status: provider error")
				continue
			}

			allSettled := true
			for _, svc := range snap.Services {
				if svc.Status == models.PreviewServiceStatusStarting {
					allSettled = false
					continue
				}
				// Persist non-starting statuses as they become available.
				if err := m.store.UpdateServiceStatus(ctx, orgID, previewID, svc.Name, svc.Status, svc.Error); err != nil {
					m.logger.Warn().Err(err).
						Str("preview_id", previewID.String()).
						Str("service", svc.Name).
						Msg("failed to persist support service status")
				}
			}

			if allSettled {
				// All services have settled — check if the overall preview
				// should be promoted from partially_ready to ready.
				var failedServices []string
				allReady := true
				for _, svc := range snap.Services {
					if svc.Status == models.PreviewServiceStatusFailed {
						failedServices = append(failedServices, svc.Name)
						allReady = false
					} else if svc.Status != models.PreviewServiceStatusReady {
						allReady = false
					}
				}
				if allReady {
					if err := m.store.UpdatePreviewStatus(ctx, orgID, previewID, models.PreviewStatusReady, ""); err != nil {
						m.logger.Warn().Err(err).
							Str("preview_id", previewID.String()).
							Msg("failed to promote preview to ready")
					}
				} else if len(failedServices) > 0 {
					// Primary is serving but support services failed — promote
					// to ready with an error noting the degraded services.
					errMsg := fmt.Sprintf("support services failed: %s", strings.Join(failedServices, ", "))
					if err := m.store.UpdatePreviewStatus(ctx, orgID, previewID, models.PreviewStatusReady, errMsg); err != nil {
						m.logger.Warn().Err(err).
							Str("preview_id", previewID.String()).
							Msg("failed to promote preview to ready (degraded)")
					}
					m.logger.Warn().
						Str("preview_id", previewID.String()).
						Strs("failed_services", failedServices).
						Msg("preview promoted to ready with failed support services")
				}
				m.logger.Info().
					Str("preview_id", previewID.String()).
					Msg("all support services settled")
				return
			}
		}
	}
}

// =============================================================================
// StopPreview
// =============================================================================

// StopPreview stops a preview and revokes all access sessions.
func (m *Manager) StopPreview(ctx context.Context, orgID, previewID uuid.UUID) error {
	instance, err := m.store.GetPreviewInstance(ctx, orgID, previewID)
	if err != nil {
		return fmt.Errorf("get preview instance: %w", err)
	}

	if instance.Status.IsTerminal() {
		return nil // already stopped — idempotent
	}

	// Stop any running poll goroutine before changing status, so it cannot
	// race and overwrite "stopped" with "ready".
	m.pollStopMu.Lock()
	if ch, ok := m.pollStopChs[previewID]; ok {
		close(ch)
		delete(m.pollStopChs, previewID)
	}
	m.pollStopMu.Unlock()

	// Stop via provider.
	if instance.PreviewHandle != "" && m.provider != nil {
		if err := m.provider.StopPreview(ctx, instance.PreviewHandle); err != nil {
			m.logger.Error().Err(err).
				Str("preview_id", previewID.String()).
				Str("handle", instance.PreviewHandle).
				Msg("provider stop failed")
		}
	}

	// Atomically stop + revoke access sessions.
	if err := m.store.StopPreviewWithRevocation(ctx, orgID, previewID); err != nil {
		return fmt.Errorf("stop preview: %w", err)
	}

	// Stop HMR watching for this preview.
	if m.hmrWatcher != nil {
		m.hmrWatcher.StopWatching(previewID)
	}

	m.logger.Info().Str("preview_id", previewID.String()).Msg("preview stopped")
	return nil
}

// =============================================================================
// GetStatus
// =============================================================================

// GetStatus returns the full preview status including services and infrastructure.
func (m *Manager) GetStatus(ctx context.Context, orgID, previewID uuid.UUID) (*models.PreviewStatusResponse, error) {
	instance, err := m.store.GetPreviewInstance(ctx, orgID, previewID)
	if err != nil {
		return nil, fmt.Errorf("get preview instance: %w", err)
	}

	services, err := m.store.ListServicesByPreview(ctx, orgID, previewID)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}

	infra, err := m.store.ListInfraByPreview(ctx, orgID, previewID)
	if err != nil {
		return nil, fmt.Errorf("list infrastructure: %w", err)
	}

	return &models.PreviewStatusResponse{
		Instance:       instance,
		Services:       services,
		Infrastructure: infra,
	}, nil
}

// =============================================================================
// MintBootstrapToken
// =============================================================================

// MintBootstrapToken creates a one-time, short-lived bootstrap token for
// establishing preview access from the iframe.
func (m *Manager) MintBootstrapToken(ctx context.Context, orgID, userID, previewID uuid.UUID) (string, error) {
	instance, err := m.store.GetPreviewInstance(ctx, orgID, previewID)
	if err != nil {
		return "", fmt.Errorf("get preview instance: %w", err)
	}

	if !instance.Status.IsActive() {
		return "", fmt.Errorf("preview is not active (status=%s)", instance.Status)
	}

	// Generate a random token.
	token, err := generateToken()
	if err != nil {
		return "", fmt.Errorf("generate bootstrap token: %w", err)
	}
	tokenHash := hashToken(token)

	sess := &models.PreviewAccessSession{
		OrgID:             orgID,
		UserID:            userID,
		PreviewInstanceID: previewID,
		SessionTokenHash:  tokenHash,
		ExpiresAt:         time.Now().Add(5 * time.Minute),
	}

	if err := m.store.CreateAccessSession(ctx, sess); err != nil {
		return "", fmt.Errorf("create access session: %w", err)
	}

	return token, nil
}

// =============================================================================
// ValidateBootstrapToken
// =============================================================================

// ValidateBootstrapToken exchanges a bootstrap token for a preview access
// session. Returns the session if the token is valid and not expired.
func (m *Manager) ValidateBootstrapToken(ctx context.Context, orgID uuid.UUID, token string) (*models.PreviewAccessSession, error) {
	tokenHash := hashToken(token)
	sess, err := m.store.GetAccessSessionByToken(ctx, orgID, tokenHash)
	if err != nil {
		return nil, fmt.Errorf("invalid bootstrap token")
	}
	return m.validateSession(ctx, sess)
}

// =============================================================================
// ValidateBootstrapTokenUnscoped
// =============================================================================

// ValidateBootstrapTokenUnscoped exchanges a bootstrap token without requiring
// an org_id. This is used by the preview gateway which does not have session
// middleware. The token hash is 32 random bytes, making unscoped lookup safe.
func (m *Manager) ValidateBootstrapTokenUnscoped(ctx context.Context, token string) (*models.PreviewAccessSession, error) {
	tokenHash := hashToken(token)
	sess, err := m.store.GetAccessSessionByTokenUnscoped(ctx, tokenHash)
	if err != nil {
		return nil, fmt.Errorf("invalid bootstrap token")
	}
	return m.validateSession(ctx, sess)
}

// validateSession checks revocation, expiration, and records activity for a
// bootstrap session. Shared by scoped and unscoped token validation.
func (m *Manager) validateSession(ctx context.Context, sess *models.PreviewAccessSession) (*models.PreviewAccessSession, error) {
	if sess.RevokedAt != nil {
		return nil, fmt.Errorf("bootstrap token has been revoked")
	}
	if time.Now().After(sess.ExpiresAt) {
		return nil, fmt.Errorf("bootstrap token has expired")
	}
	// Mark as used by updating activity.
	if err := m.store.UpdateAccessSessionActivity(ctx, sess.OrgID, sess.ID); err != nil {
		m.logger.Warn().Err(err).Str("session_id", sess.ID.String()).Msg("failed to update access session activity")
	}
	return sess, nil
}

// =============================================================================
// ExtendTTL
// =============================================================================

// ExtendTTL extends the preview's hard TTL by DefaultHardTTL from now, capped
// at DefaultMaxTTL after the original creation time. Callers may invoke this
// any number of times, but the effective expiry will never exceed
// CreatedAt + DefaultMaxTTL, so repeated calls cannot extend a preview
// indefinitely. The background recycler's DefaultMaxUptime bounds total
// process uptime independently.
func (m *Manager) ExtendTTL(ctx context.Context, orgID, previewID uuid.UUID) error {
	instance, err := m.store.GetPreviewInstance(ctx, orgID, previewID)
	if err != nil {
		return fmt.Errorf("get preview instance: %w", err)
	}

	maxExpiry := instance.CreatedAt.Add(DefaultMaxTTL)
	if !time.Now().Before(maxExpiry) {
		return fmt.Errorf("preview has reached its maximum lifetime and cannot be extended further")
	}
	newExpiry := time.Now().Add(DefaultHardTTL)
	if newExpiry.After(maxExpiry) {
		newExpiry = maxExpiry
	}

	return m.store.UpdatePreviewExpiry(ctx, orgID, previewID, newExpiry)
}

// =============================================================================
// RecordAccess
// =============================================================================

// RecordAccess updates the last_accessed_at timestamp for activity-aware timeouts.
func (m *Manager) RecordAccess(ctx context.Context, orgID, previewID uuid.UUID) error {
	return m.store.UpdatePreviewAccess(ctx, orgID, previewID)
}

// =============================================================================
// RecordLastPath
// =============================================================================

// RecordLastPath stores the last proxied request path for navigation restore.
func (m *Manager) RecordLastPath(ctx context.Context, orgID, previewID uuid.UUID, path string) error {
	return m.store.UpdateLastPath(ctx, orgID, previewID, path)
}

// =============================================================================
// DialPreview
// =============================================================================

// DialPreview opens a transport stream to the primary service.
func (m *Manager) DialPreview(ctx context.Context, orgID, previewID uuid.UUID) (PreviewStream, error) {
	instance, err := m.store.GetPreviewInstance(ctx, orgID, previewID)
	if err != nil {
		return nil, fmt.Errorf("get preview instance: %w", err)
	}

	if !instance.Status.IsActive() {
		return nil, fmt.Errorf("preview is not active (status=%s)", instance.Status)
	}

	return m.provider.DialPreview(ctx, instance.PreviewHandle)
}

// =============================================================================
// Inspector
// =============================================================================

// Inspector returns the PreviewInspector, or nil if not configured.
func (m *Manager) Inspector() PreviewInspector {
	m.inspectorMu.RLock()
	defer m.inspectorMu.RUnlock()
	return m.inspector
}

// SetInspector sets the headless browser inspector (useful for late binding).
func (m *Manager) SetInspector(inspector PreviewInspector) {
	m.inspectorMu.Lock()
	defer m.inspectorMu.Unlock()
	m.inspector = inspector
}

// =============================================================================
// RecyclePreview
// =============================================================================

// RecyclePreview restarts a preview in place. It stops the existing processes,
// re-provisions infrastructure, re-runs init scripts, and restarts services.
// The preview instance ID and last_path are preserved.
func (m *Manager) RecyclePreview(ctx context.Context, orgID, previewID uuid.UUID) error {
	instance, err := m.store.GetPreviewInstance(ctx, orgID, previewID)
	if err != nil {
		return fmt.Errorf("get preview instance: %w", err)
	}
	if instance.Status.IsTerminal() {
		return fmt.Errorf("cannot recycle terminal preview (status=%s)", instance.Status)
	}

	if m.provider == nil {
		return fmt.Errorf("preview provider is not configured")
	}

	input, err := m.loadRecycleInput(ctx, instance)
	if err != nil {
		return fmt.Errorf("load recycle input: %w", err)
	}

	m.logger.Info().Str("preview_id", previewID.String()).Msg("recycling preview")

	// Stop any running poll goroutine before recycling, so it cannot race
	// and overwrite the recycled preview's status with stale values.
	m.pollStopMu.Lock()
	if ch, ok := m.pollStopChs[previewID]; ok {
		close(ch)
		delete(m.pollStopChs, previewID)
	}
	m.pollStopMu.Unlock()

	// Atomically transition to starting only if the preview is still active.
	// This eliminates the TOCTOU window where a concurrent stop could race
	// between our check above and the status update.
	updated, err := m.store.UpdatePreviewStatusIfActive(ctx, orgID, previewID, models.PreviewStatusStarting, "")
	if err != nil {
		return fmt.Errorf("set starting status: %w", err)
	}
	if !updated {
		return fmt.Errorf("preview was stopped concurrently before recycle could begin")
	}

	// Stop current processes via provider.
	if instance.PreviewHandle != "" && m.provider != nil {
		if err := m.provider.StopPreview(ctx, instance.PreviewHandle); err != nil {
			m.logger.Warn().Err(err).Str("preview_id", previewID.String()).Msg("recycle: provider stop failed")
		}
	}

	// Revoke all access sessions before restarting so stale cookies are
	// invalidated across the recycle boundary.
	if err := m.store.RevokeAllForPreview(ctx, orgID, previewID); err != nil {
		m.logger.Warn().Err(err).Str("preview_id", previewID.String()).Msg("recycle: failed to revoke access sessions")
	}

	// Restart via provider with same sandbox and config. Use the existing
	// instance ID so PREVIEW_ORIGIN stays stable across recycles.
	handle, err := m.provider.StartPreview(ctx, input.Sandbox, input.Config, m.platformEnv(previewID))
	if err != nil {
		if statusErr := m.store.UpdatePreviewStatus(ctx, orgID, previewID, models.PreviewStatusFailed, err.Error()); statusErr != nil {
			m.logger.Warn().Err(statusErr).Msg("recycle: failed to set failed status")
		}
		return fmt.Errorf("recycle start: %w", err)
	}

	// Update instance with new handle. This is critical — if it fails, the DB
	// still points to the old (dead) handle and all subsequent proxy/status
	// operations will break. Stop the new preview and fail the recycle.
	if err := m.store.UpdatePreviewHandle(ctx, orgID, previewID, handle.Handle, handle.PrimaryPort); err != nil {
		m.logger.Error().Err(err).Msg("recycle: failed to update handle, stopping new preview")
		_ = m.provider.StopPreview(ctx, handle.Handle)
		if statusErr := m.store.UpdatePreviewStatus(ctx, orgID, previewID, models.PreviewStatusFailed, "recycle failed: could not persist new handle"); statusErr != nil {
			m.logger.Warn().Err(statusErr).Msg("recycle: failed to set failed status after handle update error")
		}
		return fmt.Errorf("recycle: update handle: %w", err)
	}

	nextStatus := models.PreviewStatusReady
	if handle.PartiallyReady {
		nextStatus = models.PreviewStatusPartiallyReady
	}
	if err := m.store.UpdatePreviewStatus(ctx, orgID, previewID, nextStatus, ""); err != nil {
		m.logger.Error().Err(err).Str("status", string(nextStatus)).Msg("recycle: failed to set preview status")
	}

	// When the recycled preview started with progressive readiness, restart the
	// background poll so support services are tracked to completion.
	if handle.PartiallyReady {
		stopCh := make(chan struct{})
		m.pollStopMu.Lock()
		m.pollStopChs[previewID] = stopCh
		m.pollStopMu.Unlock()
		go func() {
			m.pollSupportServiceStatus(stopCh, orgID, previewID, handle.Handle)
			m.pollStopMu.Lock()
			delete(m.pollStopChs, previewID)
			m.pollStopMu.Unlock()
		}()
	}

	// Reset expiry without extending beyond the preview's hard max lifetime.
	newExpiry := time.Now().Add(DefaultHardTTL)
	maxExpiry := instance.CreatedAt.Add(DefaultMaxTTL)
	if newExpiry.After(maxExpiry) {
		newExpiry = maxExpiry
	}
	if err := m.store.UpdatePreviewExpiry(ctx, orgID, previewID, newExpiry); err != nil {
		m.logger.Warn().Err(err).Msg("recycle: failed to reset expiry")
	}

	// Re-register HMR watching so screenshot capture continues after recycle.
	if m.hmrWatcher != nil {
		m.hmrWatcher.StopWatching(previewID)
		m.hmrWatcher.StartWatching(previewID, orgID)
	}

	// Clear the grace-window marker so the UI stops showing the "recycling
	// soon" warning after the restart completes.
	if err := m.store.ClearRecycleSchedule(ctx, orgID, previewID); err != nil {
		m.logger.Warn().Err(err).Str("preview_id", previewID.String()).Msg("recycle: failed to clear recycle schedule marker")
	}

	m.logger.Info().Str("preview_id", previewID.String()).Str("handle", handle.Handle).Msg("preview recycled")
	return nil
}

// =============================================================================
// Store accessor
// =============================================================================

// Store returns the preview store (used by cleanup/recycle workers).
func (m *Manager) Store() *db.PreviewStore {
	return m.store
}

// WorkerNodeID returns this worker's identity string (used by recycle workers).
func (m *Manager) WorkerNodeID() string {
	return m.workerNodeID
}

// platformEnv returns environment variables the platform injects into every
// service of the given preview, overriding any user-declared value. Currently
// exposes PREVIEW_ORIGIN (computed from PreviewOriginTemplate with "{id}"
// replaced by the preview UUID). Returns nil when PreviewOriginTemplate is
// unset, leaving user-declared env untouched.
func (m *Manager) platformEnv(previewID uuid.UUID) map[string]string {
	if m.previewOriginTemplate == "" {
		return nil
	}
	origin := strings.ReplaceAll(m.previewOriginTemplate, "{id}", previewID.String())
	return map[string]string{"PREVIEW_ORIGIN": origin}
}

// =============================================================================
// SnapshotCache accessor
// =============================================================================

// SnapshotCache returns the filesystem snapshot cache, or nil if not configured.
func (m *Manager) SnapshotCache() *SnapshotCache {
	return m.snapshotCache
}

// =============================================================================
// Concurrency checks
// =============================================================================

// checkConcurrencyCaps enforces soft concurrency limits. These are checked
// non-atomically relative to CreatePreviewInstance, so under concurrent requests
// limits may briefly be exceeded. The database partial unique index on session_id
// prevents duplicates per session; these caps are best-effort guardrails.
func (m *Manager) checkConcurrencyCaps(ctx context.Context, orgID, userID uuid.UUID) error {
	// Per-user cap.
	userCount, err := m.store.CountActivePreviewsByUser(ctx, orgID, userID)
	if err != nil {
		return fmt.Errorf("count user previews: %w", err)
	}
	if userCount >= m.maxPerUser {
		return fmt.Errorf("you have reached your limit of %d concurrent previews — stop an existing preview to start a new one", m.maxPerUser)
	}

	// Per-org cap.
	orgCount, err := m.store.CountActivePreviewsByOrg(ctx, orgID)
	if err != nil {
		return fmt.Errorf("count org previews: %w", err)
	}
	if orgCount >= m.maxPerOrg {
		return fmt.Errorf("org has reached its limit of %d concurrent previews — stop an existing preview to start a new one", m.maxPerOrg)
	}

	// Per-worker cap.
	workerCount, err := m.store.CountActivePreviewsByWorker(ctx, m.workerNodeID)
	if err != nil {
		return fmt.Errorf("count worker previews: %w", err)
	}
	if workerCount >= m.maxPerWorker {
		return fmt.Errorf("worker node has reached its limit of %d concurrent previews", m.maxPerWorker)
	}

	return nil
}

// =============================================================================
// Helpers
// =============================================================================

func computeConfigDigest(cfg *models.PreviewConfig) string {
	// Use JSON serialization for a deterministic, collision-resistant digest.
	// json.Marshal sorts map keys, ensuring stable output regardless of Go
	// map iteration order.
	data, err := json.Marshal(cfg)
	if err != nil {
		// Fallback: hash the name + primary if serialization somehow fails.
		data = []byte(cfg.Name + "\x00" + cfg.Primary)
	}
	h := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(h[:])[:16]
}

func storeRecycleInput(instance *models.PreviewInstance, input StartPreviewInput) error {
	if input.Config == nil {
		return fmt.Errorf("preview config is required")
	}
	if input.Sandbox == nil {
		return fmt.Errorf("sandbox is required")
	}

	configBytes, err := json.Marshal(input.Config)
	if err != nil {
		return fmt.Errorf("marshal preview config: %w", err)
	}
	sandboxBytes, err := json.Marshal(input.Sandbox)
	if err != nil {
		return fmt.Errorf("marshal sandbox: %w", err)
	}

	instance.RecycleConfig = configBytes
	instance.RecycleSandbox = sandboxBytes
	return nil
}

var errMissingRecycleInput = errors.New("preview recycle input is missing")

func loadRecycleInput(instance *models.PreviewInstance) (StartPreviewInput, error) {
	if len(instance.RecycleConfig) <= 2 {
		return StartPreviewInput{}, fmt.Errorf("%w: preview %s is missing stored recycle config", errMissingRecycleInput, instance.ID)
	}
	if len(instance.RecycleSandbox) <= 2 {
		return StartPreviewInput{}, fmt.Errorf("%w: preview %s is missing stored recycle sandbox", errMissingRecycleInput, instance.ID)
	}

	var cfg models.PreviewConfig
	if err := json.Unmarshal(instance.RecycleConfig, &cfg); err != nil {
		return StartPreviewInput{}, fmt.Errorf("unmarshal recycle config: %w", err)
	}
	var sandbox agent.Sandbox
	if err := json.Unmarshal(instance.RecycleSandbox, &sandbox); err != nil {
		return StartPreviewInput{}, fmt.Errorf("unmarshal recycle sandbox: %w", err)
	}

	return StartPreviewInput{
		SessionID:     instance.SessionID,
		OrgID:         instance.OrgID,
		UserID:        instance.UserID,
		Sandbox:       &sandbox,
		Config:        &cfg,
		BaseCommitSHA: instance.BaseCommitSHA,
		ProfileName:   instance.ProfileName,
	}, nil
}

func (m *Manager) loadRecycleInput(ctx context.Context, instance *models.PreviewInstance) (StartPreviewInput, error) {
	input, err := loadRecycleInput(instance)
	if err == nil {
		return input, nil
	}
	if !errors.Is(err, errMissingRecycleInput) {
		return StartPreviewInput{}, err
	}

	rebuilt, rebuildErr := m.rebuildLegacyRecycleInput(ctx, instance)
	if rebuildErr != nil {
		return StartPreviewInput{}, fmt.Errorf("%w; rebuild legacy restart input: %v", err, rebuildErr)
	}
	m.logger.Warn().
		Str("preview_id", instance.ID.String()).
		Msg("recycle input missing; rebuilding preview restart input from persisted session and service state")
	return rebuilt, nil
}

func (m *Manager) rebuildLegacyRecycleInput(ctx context.Context, instance *models.PreviewInstance) (StartPreviewInput, error) {
	if m.sessionStore == nil {
		return StartPreviewInput{}, fmt.Errorf("session store is not configured")
	}

	session, err := m.sessionStore.GetByID(ctx, instance.OrgID, instance.SessionID)
	if err != nil {
		return StartPreviewInput{}, fmt.Errorf("get session: %w", err)
	}
	if session.ContainerID == nil || *session.ContainerID == "" {
		return StartPreviewInput{}, fmt.Errorf("session has no active sandbox container")
	}

	services, err := m.store.ListServicesByPreview(ctx, instance.OrgID, instance.ID)
	if err != nil {
		return StartPreviewInput{}, fmt.Errorf("list preview services: %w", err)
	}
	if len(services) == 0 {
		return StartPreviewInput{}, fmt.Errorf("preview has no persisted services to rebuild from")
	}

	infra, err := m.store.ListInfraByPreview(ctx, instance.OrgID, instance.ID)
	if err != nil {
		return StartPreviewInput{}, fmt.Errorf("list preview infrastructure: %w", err)
	}

	cfg := &models.PreviewConfig{
		Version:        "3",
		Name:           instance.Name,
		Primary:        instance.PrimaryService,
		Services:       make(map[string]models.ServiceConfig, len(services)),
		Infrastructure: make(map[string]models.InfrastructureConfig, len(infra)),
		Credentials:    models.CredentialConfig{Mode: "none"},
		Network:        models.NetworkConfig{Mode: "restricted"},
	}
	for _, svc := range services {
		cfg.Services[svc.ServiceName] = models.ServiceConfig{
			Command: svc.Command,
			Cwd:     svc.Cwd,
			Port:    svc.Port,
			Ready:   models.ReadinessProbe{HTTPPath: "/"},
		}
	}
	if _, ok := cfg.Services[cfg.Primary]; !ok {
		return StartPreviewInput{}, fmt.Errorf("primary service %q is missing from persisted preview services", cfg.Primary)
	}
	for _, item := range infra {
		cfg.Infrastructure[item.InfraName] = models.InfrastructureConfig{
			Template: item.Template,
		}
	}

	return StartPreviewInput{
		SessionID:     instance.SessionID,
		OrgID:         instance.OrgID,
		UserID:        instance.UserID,
		Sandbox:       &agent.Sandbox{ID: *session.ContainerID, Provider: instance.Provider, WorkDir: "/workspace"},
		Config:        cfg,
		BaseCommitSHA: instance.BaseCommitSHA,
		ProfileName:   instance.ProfileName,
	}, nil
}

// RandomHex returns n random bytes encoded as a hex string.
func RandomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand.Read failed: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func generateToken() (string, error) {
	return RandomHex(32)
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
