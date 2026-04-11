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
	store    *db.PreviewStore
	provider PreviewCapableProvider
	logger   zerolog.Logger

	workerNodeID string // identity of this worker for routing

	// Inspector is the headless browser used for screenshot capture, DOM
	// inspection, and interaction replay. It may be nil if the headless
	// browser has not been configured on this worker node.
	inspector PreviewInspector

	// snapshotCache handles filesystem snapshot caching for fast startup.
	snapshotCache *SnapshotCache

	// Caps (configurable per org in future; hardcoded for MVP).
	maxPerUser   int
	maxPerOrg    int
	maxPerWorker int
}

// ManagerConfig holds initialization options for the preview Manager.
type ManagerConfig struct {
	Store         *db.PreviewStore
	Provider      PreviewCapableProvider
	Inspector     PreviewInspector
	SnapshotCache *SnapshotCache
	Logger        zerolog.Logger
	WorkerNodeID  string
	MaxPerUser    int
	MaxPerOrg     int
	MaxPerWorker  int
}

// NewManager creates a new preview Manager. If cfg.Provider is nil, the
// manager is created but any operation that requires the provider (StartPreview,
// StopPreview, DialPreview, etc.) will return an error.
func NewManager(cfg ManagerConfig) *Manager {
	if cfg.Provider == nil {
		cfg.Logger.Warn().Msg("preview.NewManager: Provider is nil — preview operations will fail until a provider is set")
	}
	m := &Manager{
		store:         cfg.Store,
		provider:      cfg.Provider,
		inspector:     cfg.Inspector,
		snapshotCache: cfg.SnapshotCache,
		logger:        cfg.Logger,
		workerNodeID:  cfg.WorkerNodeID,
		maxPerUser:    cfg.MaxPerUser,
		maxPerOrg:     cfg.MaxPerOrg,
		maxPerWorker:  cfg.MaxPerWorker,
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
	handle, err := m.provider.StartPreview(ctx, input.Sandbox, input.Config)
	if err != nil {
		if statusErr := m.store.UpdatePreviewStatus(ctx, input.OrgID, instance.ID, models.PreviewStatusFailed, err.Error()); statusErr != nil {
			m.logger.Warn().Err(statusErr).Str("preview_id", instance.ID.String()).Msg("failed to update preview status to failed")
		}
		return nil, fmt.Errorf("provider start preview: %w", err)
	}

	// 10. Update instance with handle and port.
	instance.PreviewHandle = handle.Handle
	instance.Port = handle.PrimaryPort

	// Set status based on progressive preview support.
	if handle.PartiallyReady {
		instance.Status = models.PreviewStatusPartiallyReady
		if err := m.store.UpdatePreviewStatus(ctx, input.OrgID, instance.ID, models.PreviewStatusPartiallyReady, ""); err != nil {
			m.logger.Error().Err(err).Str("preview_id", instance.ID.String()).Msg("failed to update preview status to partially_ready")
		}
	} else {
		instance.Status = models.PreviewStatusReady
		if err := m.store.UpdatePreviewStatus(ctx, input.OrgID, instance.ID, models.PreviewStatusReady, ""); err != nil {
			m.logger.Error().Err(err).Str("preview_id", instance.ID.String()).Msg("failed to update preview status to ready")
		}
	}

	// Update handle in DB for recycle support.
	if err := m.store.UpdatePreviewHandle(ctx, input.OrgID, instance.ID, handle.Handle, handle.PrimaryPort); err != nil {
		m.logger.Warn().Err(err).Str("preview_id", instance.ID.String()).Msg("failed to update handle in DB")
	}

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

	return instance, nil
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

	// Stop via provider.
	if instance.PreviewHandle != "" {
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
		PreviewInstance: *instance,
		Services:        services,
		Infrastructure:  infra,
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
	_ = m.store.UpdateAccessSessionActivity(ctx, sess.OrgID, sess.ID)
	return sess, nil
}

// =============================================================================
// ExtendTTL
// =============================================================================

// ExtendTTL extends the preview's hard TTL, up to DefaultMaxTTL from creation.
func (m *Manager) ExtendTTL(ctx context.Context, orgID, previewID uuid.UUID) error {
	instance, err := m.store.GetPreviewInstance(ctx, orgID, previewID)
	if err != nil {
		return fmt.Errorf("get preview instance: %w", err)
	}

	maxExpiry := instance.CreatedAt.Add(DefaultMaxTTL)
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
	return m.inspector
}

// SetInspector sets the headless browser inspector (useful for late binding).
func (m *Manager) SetInspector(inspector PreviewInspector) {
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

	input, err := loadRecycleInput(instance)
	if err != nil {
		return fmt.Errorf("load recycle input: %w", err)
	}

	m.logger.Info().Str("preview_id", previewID.String()).Msg("recycling preview")

	// Transition to starting.
	if err := m.store.UpdatePreviewStatus(ctx, orgID, previewID, models.PreviewStatusStarting, ""); err != nil {
		return fmt.Errorf("set starting status: %w", err)
	}

	// Stop current processes via provider.
	if instance.PreviewHandle != "" {
		if err := m.provider.StopPreview(ctx, instance.PreviewHandle); err != nil {
			m.logger.Warn().Err(err).Str("preview_id", previewID.String()).Msg("recycle: provider stop failed")
		}
	}

	// Restart via provider with same sandbox and config.
	handle, err := m.provider.StartPreview(ctx, input.Sandbox, input.Config)
	if err != nil {
		if statusErr := m.store.UpdatePreviewStatus(ctx, orgID, previewID, models.PreviewStatusFailed, err.Error()); statusErr != nil {
			m.logger.Warn().Err(statusErr).Msg("recycle: failed to set failed status")
		}
		return fmt.Errorf("recycle start: %w", err)
	}

	// Update instance with new handle.
	if err := m.store.UpdatePreviewHandle(ctx, orgID, previewID, handle.Handle, handle.PrimaryPort); err != nil {
		m.logger.Error().Err(err).Msg("recycle: failed to update handle")
	}
	nextStatus := models.PreviewStatusReady
	if handle.PartiallyReady {
		nextStatus = models.PreviewStatusPartiallyReady
	}
	if err := m.store.UpdatePreviewStatus(ctx, orgID, previewID, nextStatus, ""); err != nil {
		m.logger.Error().Err(err).Str("status", string(nextStatus)).Msg("recycle: failed to set preview status")
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

func loadRecycleInput(instance *models.PreviewInstance) (StartPreviewInput, error) {
	if len(instance.RecycleConfig) == 0 {
		return StartPreviewInput{}, fmt.Errorf("preview %s is missing stored recycle config", instance.ID)
	}
	if len(instance.RecycleSandbox) == 0 {
		return StartPreviewInput{}, fmt.Errorf("preview %s is missing stored recycle sandbox", instance.ID)
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
