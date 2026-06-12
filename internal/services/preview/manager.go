package preview

import (
	"bytes"
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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/assembledhq/143/internal/services/agent"
)

// =============================================================================
// Concurrency caps (MVP defaults)
// =============================================================================

const (
	DefaultMaxPreviewsPerUser   = models.DefaultPreviewMaxPreviewsPerUser
	DefaultMaxPreviewsPerOrg    = 5
	DefaultMaxPreviewsPerWorker = 3

	DefaultIdleTimeout = 15 * time.Minute
	DefaultHardTTL     = 30 * time.Minute
	DefaultMaxTTL      = 2 * time.Hour
	MinLifetimeTTL     = 1 * time.Minute

	// ProviderDocker is the provider identifier for Docker-based previews.
	ProviderDocker = "docker"
)

const (
	// PreviewCapacityCode is the stable API error code for preview capacity
	// failures.
	PreviewCapacityCode = "PREVIEW_CAPACITY_REACHED"
	// PreviewCapacityMessage is the user-facing message for transient preview
	// capacity failures. Keep lower-level live/reserved counts in logs.
	PreviewCapacityMessage = "Preview capacity is full. Try again shortly; if this keeps happening, stop another active preview or session and retry."
	// PreviewCapacityRetryExhaustedMessage is persisted when the durable
	// start_preview job gives up after retrying capacity admission.
	PreviewCapacityRetryExhaustedMessage = "Preview capacity stayed full while retrying. Stop another active preview or session, then retry the preview."
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
	orgSettings  OrgSettingsStore
	provider     PreviewCapableProvider
	// sandboxProvider is used to destroy the underlying sandbox container
	// when the last holder (preview or turn) releases its hold. Optional —
	// when nil, the manager will skip container destroy and only clear its
	// own hold; the startup reconciler will mop up orphans.
	sandboxProvider agent.SandboxProvider
	logger          zerolog.Logger

	workerNodeID           string // identity of this worker for routing
	previewInternalBaseURL string // worker endpoint app/gateway nodes use for runtime routing

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

	secretResolver *PreviewSecretResolver
	auditEmitter   *db.AuditEmitter

	// pollStopChs tracks stop channels for pollSupportServiceStatus goroutines,
	// keyed by preview ID. Closing the channel stops the poll goroutine,
	// preventing it from overwriting a "stopped" status with "ready".
	pollStopMu  sync.Mutex
	pollStopChs map[uuid.UUID]chan struct{}

	// Caps. maxPerUser is the process fallback; org settings can override it.
	maxPerUser   int
	maxPerOrg    int
	maxPerWorker int
}

type OrgSettingsStore interface {
	GetByID(ctx context.Context, id uuid.UUID) (models.Organization, error)
}

// ManagerConfig holds initialization options for the preview Manager.
type ManagerConfig struct {
	Store                  *db.PreviewStore
	SessionStore           *db.SessionStore
	OrgSettingsStore       OrgSettingsStore
	Provider               PreviewCapableProvider
	SandboxProvider        agent.SandboxProvider // used for destroy on final hold release
	Inspector              PreviewInspector
	SnapshotCache          *SnapshotCache
	SecretResolver         *PreviewSecretResolver
	AuditEmitter           *db.AuditEmitter
	HMRWatcher             *HMRWatcher // optional; enables HMR screenshot capture
	Logger                 zerolog.Logger
	WorkerNodeID           string
	PreviewInternalBaseURL string

	// MaxPerUser / MaxPerOrg / MaxPerWorker cap concurrent active previews.
	// Zero (the default) is NOT "unlimited" — it means "fall back to the
	// compile-time default" (DefaultMaxPreviewsPerUser / PerOrg / PerWorker
	// above). Any value > 0 is applied verbatim. To effectively disable a
	// cap, set it to a very large number.
	MaxPerUser   int
	MaxPerOrg    int
	MaxPerWorker int

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
		store:                  cfg.Store,
		sessionStore:           cfg.SessionStore,
		orgSettings:            cfg.OrgSettingsStore,
		provider:               cfg.Provider,
		sandboxProvider:        cfg.SandboxProvider,
		inspector:              cfg.Inspector,
		snapshotCache:          cfg.SnapshotCache,
		secretResolver:         cfg.SecretResolver,
		auditEmitter:           cfg.AuditEmitter,
		hmrWatcher:             cfg.HMRWatcher,
		logger:                 cfg.Logger,
		workerNodeID:           cfg.WorkerNodeID,
		previewInternalBaseURL: strings.TrimRight(cfg.PreviewInternalBaseURL, "/"),
		previewOriginTemplate:  cfg.PreviewOriginTemplate,
		pollStopChs:            make(map[uuid.UUID]chan struct{}),
		maxPerUser:             cfg.MaxPerUser,
		maxPerOrg:              cfg.MaxPerOrg,
		maxPerWorker:           cfg.MaxPerWorker,
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
	SessionID                  uuid.UUID
	PreviewTargetID            uuid.UUID
	OrgID                      uuid.UUID
	UserID                     uuid.UUID // for per-user concurrency cap
	Sandbox                    *agent.Sandbox
	Config                     *models.PreviewConfig
	RepositoryID               uuid.UUID
	BaseCommitSHA              string
	ProfileName                string
	ExpiresAt                  time.Time
	RequestID                  string
	MetricsSource              string
	MetricsRepositoryFullName  string
	WorkspaceRevision          int64
	WorkspaceRevisionUpdatedAt time.Time
}

// =============================================================================
// Start / Reserve / Launch / Abort
// =============================================================================

// StartPreview is a convenience wrapper that reserves, launches, and aborts on
// failure in a single call. Callers that need to interleave sandbox
// acquisition between capacity checks and service startup (the preview HTTP
// handler is the canonical case) should use ReservePreview / LaunchPreview /
// AbortReservation directly so the hold is in place before a new container is
// hydrated.
func (m *Manager) StartPreview(ctx context.Context, input StartPreviewInput) (*models.PreviewInstance, error) {
	instance, err := m.ReservePreview(ctx, input)
	if err != nil {
		return nil, err
	}
	launched, err := m.LaunchPreview(ctx, instance, input)
	if err != nil {
		m.AbortReservation(ctx, instance, "", fmt.Sprintf("launch failed: %v", err))
		return nil, err
	}
	return launched, nil
}

// ReservePreview performs the pre-hydrate phase of preview startup: it
// validates the config, rejects an existing active preview, enforces
// concurrency caps, inserts the preview row (status=starting), and acquires
// preview_holding_container.
//
// Reserving BEFORE sandbox hydration is load-bearing for two reasons:
//  1. Capacity/existing-preview failures short-circuit before we touch docker,
//     so a 503 never leaves a hydrated container behind.
//  2. preview_holding_container=TRUE exists before the caller publishes a
//     hydrated container_id, so a concurrent turn's FinalizeContainerDestroy
//     sees our hold and leaves the freshly-hydrated container alone.
//
// The returned instance is "half-built": services/infrastructure rows have
// not been created and the provider has not started yet. The caller must
// follow up with LaunchPreview (success path) or AbortReservation (failure
// path) — otherwise the preview row lingers in 'starting' with an active hold.
func (m *Manager) ReservePreview(ctx context.Context, input StartPreviewInput) (*models.PreviewInstance, error) {
	return m.reservePreview(ctx, m.store, input, m.workerNodeID, m.previewInternalBaseURL, true)
}

// ReservePreviewForWorkerInTx reserves a visible starting preview row for a
// selected worker inside the caller's transaction. It deliberately does not
// require a local preview provider, so API-only nodes can pair the reservation
// atomically with enqueueing the durable start_preview job.
func (m *Manager) ReservePreviewForWorkerInTx(ctx context.Context, tx pgx.Tx, input StartPreviewInput, workerNodeID, workerEndpointURL string) (*models.PreviewInstance, error) {
	if tx == nil {
		return nil, fmt.Errorf("transaction is required")
	}
	return m.reservePreview(ctx, m.store.WithTx(tx), input, workerNodeID, workerEndpointURL, false)
}

// ReserveBranchPreviewForWorkerInTx reserves a standalone branch preview row
// for a selected worker. Unlike session previews it does not acquire a session
// sandbox hold; the branch runner creates and owns a dedicated sandbox.
func (m *Manager) ReserveBranchPreviewForWorkerInTx(ctx context.Context, tx pgx.Tx, input StartPreviewInput, workerNodeID, workerEndpointURL string) (*models.PreviewInstance, error) {
	if tx == nil {
		return nil, fmt.Errorf("transaction is required")
	}
	return m.reserveBranchPreview(ctx, m.store.WithTx(tx), input, workerNodeID, workerEndpointURL)
}

func (m *Manager) reserveBranchPreview(ctx context.Context, store *db.PreviewStore, input StartPreviewInput, workerNodeID, workerEndpointURL string) (*models.PreviewInstance, error) {
	if store == nil {
		return nil, fmt.Errorf("preview store is not configured")
	}
	if input.PreviewTargetID == uuid.Nil {
		return nil, fmt.Errorf("preview target id is required")
	}
	resourcePolicy, err := m.resourcePolicy(ctx, input.OrgID)
	if err != nil {
		return nil, err
	}
	if errs := ValidateConfigWithResourcePolicy(input.Config, resourcePolicy); len(errs) > 0 {
		return nil, fmt.Errorf("invalid preview config: %s", strings.Join(errs, "; "))
	}
	existing, err := store.GetActivePreviewForTarget(ctx, input.OrgID, input.PreviewTargetID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("check existing target preview: %w", err)
	} else if err == nil && existing != nil {
		return nil, fmt.Errorf("target already has an active preview (id=%s)", existing.ID)
	}
	if err := m.checkStandaloneConcurrencyCapsWithStore(ctx, store, input.OrgID, input.UserID, workerNodeID); err != nil {
		return nil, err
	}

	limits := ResolveResourceLimitsWithPolicy(input.Config, resourcePolicy)
	configDigest := computeConfigDigest(input.Config)
	profileName := input.ProfileName
	if profileName == "" {
		profileName = string(models.PreviewProfileBootstrap)
	}
	targetID := input.PreviewTargetID
	instance := &models.PreviewInstance{
		PreviewTargetID: &targetID,
		OrgID:           input.OrgID,
		UserID:          input.UserID,
		ProfileName:     profileName,
		Name:            input.Config.Name,
		Status:          models.PreviewStatusStarting,
		CurrentPhase:    "reserved",
		RequestID:       nilIfEmpty(input.RequestID),
		Provider:        ProviderDocker,
		WorkerNodeID:    workerNodeID,
		PrimaryService:  input.Config.Primary,
		ConfigDigest:    configDigest,
		BaseCommitSHA:   input.BaseCommitSHA,
		ExpiresAt:       resolvePreviewExpiresAt(input.ExpiresAt),
		LastPath:        "/",
		MemoryLimitMB:   limits.MemoryMiB,
		CPULimitMillis:  limits.CPUMillis,
		DiskLimitMB:     limits.DiskMiB,
	}
	if err := store.CreateBranchPreviewInstance(ctx, instance); err != nil {
		return nil, fmt.Errorf("create branch preview instance: %w", err)
	}
	if workerEndpointURL != "" {
		if err := store.CreatePreviewRuntime(ctx, newStartingRuntime(input.OrgID, instance.ID, workerNodeID, workerEndpointURL)); err != nil {
			return nil, fmt.Errorf("create preview runtime: %w", err)
		}
	}
	m.logger.Info().
		Str("preview_id", instance.ID.String()).
		Str("preview_target_id", targetID.String()).
		Str("name", input.Config.Name).
		Msg("branch preview reserved")
	return instance, nil
}

func resolvePreviewExpiresAt(requested time.Time) time.Time {
	if requested.IsZero() {
		return time.Now().Add(DefaultHardTTL)
	}
	return requested
}

func newStartingRuntime(orgID, previewID uuid.UUID, workerNodeID, endpointURL string) *models.PreviewRuntime {
	now := time.Now()
	return &models.PreviewRuntime{
		OrgID:             orgID,
		PreviewInstanceID: previewID,
		RuntimeEpoch:      1,
		WorkerNodeID:      workerNodeID,
		EndpointURL:       strings.TrimRight(endpointURL, "/"),
		Status:            models.PreviewRuntimeStatusStarting,
		LeaseExpiresAt:    now.Add(90 * time.Second),
	}
}

func (m *Manager) reservePreview(ctx context.Context, store *db.PreviewStore, input StartPreviewInput, workerNodeID, workerEndpointURL string, requireProvider bool) (*models.PreviewInstance, error) {
	if requireProvider && m.provider == nil {
		return nil, fmt.Errorf("preview provider is not configured")
	}
	if store == nil {
		return nil, fmt.Errorf("preview store is not configured")
	}
	resourcePolicy, err := m.resourcePolicy(ctx, input.OrgID)
	if err != nil {
		return nil, err
	}
	if errs := ValidateConfigWithResourcePolicy(input.Config, resourcePolicy); len(errs) > 0 {
		return nil, fmt.Errorf("%w: validate %s: %s", ErrInvalidConfig, repoconfig.ConfigPath, strings.Join(errs, "; "))
	}
	if err := m.resolvePreviewSecrets(ctx, input); err != nil {
		return nil, err
	}

	existing, err := store.GetActivePreviewForSession(ctx, input.OrgID, input.SessionID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("check existing preview: %w", err)
	} else if err == nil && existing != nil {
		return nil, fmt.Errorf("session already has an active preview (id=%s)", existing.ID)
	}

	if err := m.checkConcurrencyCapsWithStore(ctx, store, input.OrgID, input.UserID, workerNodeID); err != nil {
		return nil, err
	}

	limits := ResolveResourceLimitsWithPolicy(input.Config, resourcePolicy)
	configDigest := computeConfigDigest(input.Config)
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
		CurrentPhase:   "reserved",
		RequestID:      nilIfEmpty(input.RequestID),
		Provider:       ProviderDocker,
		WorkerNodeID:   workerNodeID,
		PrimaryService: input.Config.Primary,
		ConfigDigest:   configDigest,
		BaseCommitSHA:  input.BaseCommitSHA,
		ExpiresAt:      resolvePreviewExpiresAt(input.ExpiresAt),
		LastPath:       "/",
		MemoryLimitMB:  limits.MemoryMiB,
		CPULimitMillis: limits.CPUMillis,
		DiskLimitMB:    limits.DiskMiB,
	}
	if !input.WorkspaceRevisionUpdatedAt.IsZero() {
		revision := input.WorkspaceRevision
		updatedAt := input.WorkspaceRevisionUpdatedAt
		instance.SourceWorkspaceRevision = &revision
		instance.SourceWorkspaceRevisionUpdatedAt = &updatedAt
	}
	// Only store recycle bytes if we already have a sandbox at reservation
	// time. The handler flow reserves before hydrate, so Sandbox is typically
	// nil here; LaunchPreview populates recycle bytes once the sandbox is known.
	if input.Sandbox != nil {
		if err := storeRecycleInput(instance, input); err != nil {
			return nil, fmt.Errorf("store recycle input: %w", err)
		}
	}

	if err := store.CreatePreviewInstance(ctx, instance); err != nil {
		return nil, fmt.Errorf("create preview instance: %w", err)
	}

	// Acquire the preview's half of the sandbox refcount. Retry once, but
	// only for transient I/O errors: a permanent PostgreSQL error or a
	// not-found response means the row is gone and retrying will not help.
	var holdErr error
	for attempt := 0; attempt < 2; attempt++ {
		if _, holdErr = store.AcquirePreviewHold(ctx, input.OrgID, instance.ID); holdErr == nil {
			break
		}
		if !isTransientPreviewDBError(holdErr) {
			break
		}
		m.logger.Warn().Err(holdErr).
			Str("preview_id", instance.ID.String()).
			Int("attempt", attempt+1).
			Msg("acquire preview hold failed; retrying")
	}
	if holdErr != nil {
		if statusErr := store.UpdatePreviewStatus(ctx, input.OrgID, instance.ID, models.PreviewStatusFailed,
			fmt.Sprintf("acquire preview hold: %v", holdErr)); statusErr != nil {
			m.logger.Warn().Err(statusErr).Str("preview_id", instance.ID.String()).Msg("failed to mark preview failed after hold error")
		}
		return nil, fmt.Errorf("acquire preview hold: %w", holdErr)
	}
	// Reflect the acquired hold in the in-memory struct so AbortReservation can
	// use this field as an explicit guard instead of relying on SessionID == Nil
	// as an indirect proxy for "was the hold acquired?".
	instance.PreviewHoldingContainer = true
	if workerEndpointURL != "" {
		if err := store.CreatePreviewRuntime(ctx, newStartingRuntime(input.OrgID, instance.ID, workerNodeID, workerEndpointURL)); err != nil {
			m.AbortReservation(ctx, instance, "", fmt.Sprintf("create preview runtime: %v", err))
			return nil, fmt.Errorf("create preview runtime: %w", err)
		}
	}

	m.logger.Info().
		Str("preview_id", instance.ID.String()).
		Str("session_id", input.SessionID.String()).
		Str("name", input.Config.Name).
		Msg("preview reserved")

	return instance, nil
}

func (m *Manager) resolvePreviewSecrets(ctx context.Context, input StartPreviewInput) error {
	if input.Config == nil {
		return nil
	}
	refs := SecretBundleRefs(input.Config)
	if len(refs) == 0 {
		return nil
	}
	if m.secretResolver == nil {
		err := fmt.Errorf("preview secret resolver is not configured")
		m.emitPreviewSecretResolveAudit(ctx, input, refs, models.AuditActionPreviewSecretBundleFailed, err)
		return err
	}
	if err := m.secretResolver.Resolve(ctx, input.OrgID, input.RepositoryID, input.Config); err != nil {
		m.emitPreviewSecretResolveAudit(ctx, input, refs, models.AuditActionPreviewSecretBundleFailed, err)
		return err
	}
	m.emitPreviewSecretResolveAudit(ctx, input, refs, models.AuditActionPreviewSecretBundleResolved, nil)
	return nil
}

func (m *Manager) emitPreviewSecretResolveAudit(ctx context.Context, input StartPreviewInput, refs []models.PreviewSecretBundleRef, action models.AuditAction, resolveErr error) {
	if m.auditEmitter == nil {
		return
	}
	for _, ref := range refs {
		resourceID := ref.Bundle
		details := map[string]any{
			"repository_id": input.RepositoryID.String(),
			"bundle":        ref.Bundle,
			"services":      ref.Services,
			"env":           ref.Env,
			"files":         ref.Files,
		}
		if resolveErr != nil {
			details["error"] = resolveErr.Error()
		}
		rawDetails, err := json.Marshal(details)
		if err != nil {
			m.logger.Warn().Err(err).Str("bundle", ref.Bundle).Msg("marshal preview secret resolve audit details")
			rawDetails = nil
		}
		m.auditEmitter.EmitSystemAction(ctx, db.SystemActionParams{
			OrgID:        input.OrgID,
			ActorID:      "preview-secret-resolver",
			Action:       action,
			ResourceType: models.AuditResourcePreviewSecretBundle,
			ResourceID:   &resourceID,
			Details:      rawDetails,
			SessionID:    &input.SessionID,
		})
	}
}

// LaunchPreview takes a reserved preview and completes startup: it updates
// the row if the caller resolved a different config after reservation (e.g.
// workspace autodetect), creates service/infra rows, invokes the provider,
// persists the handle, and transitions to ready.
//
// On failure, LaunchPreview cleans up provider-side state it created (calling
// StopPreview if the handle was acquired) and returns the error without
// touching the preview hold or the sandbox container — the caller is
// responsible for AbortReservation to complete teardown.
func (m *Manager) LaunchPreview(ctx context.Context, instance *models.PreviewInstance, input StartPreviewInput) (*models.PreviewInstance, error) {
	if m.provider == nil {
		return nil, fmt.Errorf("preview provider is not configured")
	}
	if input.Sandbox == nil {
		return nil, fmt.Errorf("sandbox must not be nil")
	}
	resourcePolicy, err := m.resourcePolicy(ctx, input.OrgID)
	if err != nil {
		return nil, err
	}
	if errs := ValidateConfigWithResourcePolicy(input.Config, resourcePolicy); len(errs) > 0 {
		return nil, fmt.Errorf("%w: validate %s: %s", ErrInvalidConfig, repoconfig.ConfigPath, strings.Join(errs, "; "))
	}
	if err := m.resolvePreviewSecrets(ctx, input); err != nil {
		return nil, err
	}

	// If the caller resolved a different config after reservation (autodetect
	// from the sandbox workspace), or the reservation didn't persist recycle
	// bytes because the sandbox wasn't known yet, overwrite the row now.
	newDigest := computeConfigDigest(input.Config)
	needsUpdate := newDigest != instance.ConfigDigest || len(instance.RecycleSandbox) == 0
	if needsUpdate {
		limits := ResolveResourceLimitsWithPolicy(input.Config, resourcePolicy)
		scratch := &models.PreviewInstance{}
		if err := storeRecycleInput(scratch, input); err != nil {
			return nil, fmt.Errorf("marshal recycle input: %w", err)
		}
		ok, err := m.store.UpdatePreviewReservationConfig(
			ctx, input.OrgID, instance.ID,
			input.Config.Name, input.Config.Primary, newDigest,
			limits.MemoryMiB, limits.CPUMillis, limits.DiskMiB,
			scratch.RecycleConfig, scratch.RecycleSandbox,
		)
		if err != nil {
			return nil, fmt.Errorf("update reserved preview config: %w", err)
		}
		if !ok {
			// Status was flipped from 'starting' (e.g. a concurrent StopPreview
			// or recycle). The caller's LaunchPreview is racing against that;
			// bail out and let the caller tear down the hold via AbortReservation.
			return nil, fmt.Errorf("preview reservation is no longer pending")
		}
		instance.Name = input.Config.Name
		instance.PrimaryService = input.Config.Primary
		instance.ConfigDigest = newDigest
		instance.MemoryLimitMB = limits.MemoryMiB
		instance.CPULimitMillis = limits.CPUMillis
		instance.DiskLimitMB = limits.DiskMiB
		instance.RecycleConfig = scratch.RecycleConfig
		instance.RecycleSandbox = scratch.RecycleSandbox
	}

	m.logger.Info().
		Str("preview_id", instance.ID.String()).
		Str("session_id", input.SessionID.String()).
		Str("name", input.Config.Name).
		Msg("launching reserved preview")

	// Create service records.
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
			return nil, fmt.Errorf("create service record %q: %w", name, err)
		}
	}

	// Create infrastructure records.
	for name, infraCfg := range input.Config.Infrastructure {
		infra := &models.PreviewInfrastructure{
			PreviewInstanceID: instance.ID,
			InfraName:         name,
			Template:          infraCfg.Template,
			Status:            models.PreviewInfraStatusProvisioning,
		}
		if err := m.store.CreatePreviewInfrastructure(ctx, infra); err != nil {
			return nil, fmt.Errorf("create infrastructure record %q: %w", name, err)
		}
	}

	// Start the preview via the provider. The observer streams per-service
	// Ready/Failed transitions into the DB as they happen, so the frontend's
	// startup checklist sees progress instead of "all starting" until the
	// whole launch returns. It also writes a preview_logs row with the tail
	// of stdout/stderr when a service fails, so the user sees why.
	observer := m.newServiceObserver(input.OrgID, instance.ID, input.MetricsSource, input.MetricsRepositoryFullName)
	defer observer.Close()
	handle, err := m.provider.StartPreview(ctx, input.Sandbox, input.Config, StartPreviewOptions{
		OrgID:        input.OrgID,
		RepositoryID: input.RepositoryID,
		SessionID:    input.SessionID,
		ConfigDigest: computeConfigDigest(input.Config),
		ExtraEnv:     m.platformEnv(instance.ID),
	}, observer)
	if err != nil {
		return nil, fmt.Errorf("provider start preview: %w", err)
	}

	instance.PreviewHandle = handle.Handle
	instance.Port = handle.PrimaryPort

	// Persist the handle. If this fails, the DB row has no route info and
	// subsequent proxy/status calls would break — stop the provider and
	// return so the caller aborts.
	if m.previewInternalBaseURL != "" {
		if runtime, runtimeErr := m.store.GetActivePreviewRuntime(ctx, input.OrgID, instance.ID); runtimeErr == nil {
			err = m.store.MarkPreviewRuntimeReady(ctx, input.OrgID, runtime.ID, handle.Handle, handle.PrimaryPort)
		} else {
			err = m.store.UpdatePreviewHandle(ctx, input.OrgID, instance.ID, handle.Handle, handle.PrimaryPort)
		}
	} else {
		err = m.store.UpdatePreviewHandle(ctx, input.OrgID, instance.ID, handle.Handle, handle.PrimaryPort)
	}
	if err != nil {
		m.logger.Error().Err(err).Str("preview_id", instance.ID.String()).Msg("failed to update handle in DB, stopping provider")
		_ = m.provider.StopPreview(ctx, handle.Handle)
		return nil, fmt.Errorf("persist preview handle: %w", err)
	}

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

	// Refresh infrastructure/service rows with the provider's initial snapshot.
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

	if m.hmrWatcher != nil {
		m.hmrWatcher.StartWatching(instance.ID, input.OrgID)
	}

	return instance, nil
}

// AbortReservation tears down a reservation created by ReservePreview.
//
// It releases the preview hold, finalize-destroys the hydrated container (if
// any) only when the CAS confirms no other holder is keeping the session's
// container alive, and marks the preview row failed so the partial unique
// index releases for a retry.
//
// hydratedContainerID is the container id the caller published via
// PublishHydratedContainerID. Pass "" when the sandbox was reused from a
// turn (not hydrated) — AbortReservation must NOT destroy a reused container
// because the turn still owns it.
//
// Uses a detached context with its own timeout so a shutdown mid-abort still
// completes the hold release and container destroy.
func (m *Manager) AbortReservation(parentCtx context.Context, instance *models.PreviewInstance, hydratedContainerID, reason string) {
	if instance == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parentCtx), 60*time.Second)
	defer cancel()

	// Flip the row to failed first so the partial unique index on
	// (session_id) where status in active lets a retry through.
	if err := m.store.UpdatePreviewStatus(ctx, instance.OrgID, instance.ID, models.PreviewStatusFailed, reason); err != nil {
		m.logger.Warn().Err(err).
			Str("preview_id", instance.ID.String()).
			Msg("abort reservation: failed to mark preview failed")
	}

	if instance.SessionID == uuid.Nil {
		// Standalone branch preview: owns a dedicated sandbox, no session
		// container lifecycle to coordinate. Destroy the sandbox directly.
		if hydratedContainerID != "" && m.sandboxProvider != nil {
			sb := &agent.Sandbox{ID: hydratedContainerID, Provider: ProviderDocker}
			if err := m.sandboxProvider.Destroy(ctx, sb); err != nil {
				m.logger.Error().Err(err).
					Str("preview_id", instance.ID.String()).
					Str("container_id", hydratedContainerID).
					Msg("abort branch reservation: destroy failed; container orphaned on host")
			}
		}
		return
	}

	// Only release the hold when we know it was acquired. The in-memory struct
	// field is set to true by reservePreview after AcquirePreviewHold succeeds,
	// so a false value here means the hold was never taken (e.g. this instance
	// was fetched from the DB after reservation already aborted, or a code path
	// called AbortReservation before hold acquisition).
	if !instance.PreviewHoldingContainer {
		return
	}

	// Release the preview hold; learn sibling (turn) state.
	destroyNow, _, sessionContainerID, err := m.store.ReleasePreviewHold(ctx, instance.OrgID, instance.ID)
	if err != nil {
		m.logger.Warn().Err(err).
			Str("preview_id", instance.ID.String()).
			Msg("abort reservation: failed to release preview hold; row will be mopped up by reconciler")
		return
	}

	// Only destroy when (a) we hydrated a container the caller vouches for,
	// (b) no turn still holds it, and (c) the session's current container_id
	// still matches the hydrated id. If the caller didn't hydrate (reuse
	// path) or a new holder has acquired, leave the container alone.
	if hydratedContainerID == "" || !destroyNow || m.sandboxProvider == nil || m.sessionStore == nil {
		return
	}
	if sessionContainerID != "" && sessionContainerID != hydratedContainerID {
		m.logger.Info().
			Str("preview_id", instance.ID.String()).
			Str("hydrated_container_id", hydratedContainerID).
			Str("session_container_id", sessionContainerID).
			Msg("abort reservation: session now tracks a different container; leaving hydrated id alone")
		return
	}

	cleared, err := m.sessionStore.FinalizeContainerDestroy(ctx, instance.OrgID, instance.SessionID, hydratedContainerID)
	if err != nil {
		m.logger.Warn().Err(err).
			Str("preview_id", instance.ID.String()).
			Str("container_id", hydratedContainerID).
			Msg("abort reservation: FinalizeContainerDestroy failed; container may be orphaned")
		return
	}
	if !cleared {
		// Another holder acquired between our release and now; the container
		// is someone else's responsibility.
		return
	}
	sb := &agent.Sandbox{ID: hydratedContainerID, Provider: ProviderDocker}
	if err := m.sandboxProvider.Destroy(ctx, sb); err != nil {
		m.logger.Error().Err(err).
			Str("preview_id", instance.ID.String()).
			Str("container_id", hydratedContainerID).
			Msg("abort reservation: destroy failed; container orphaned on host")
	}
}

// newServiceObserver returns a preview.ServiceObserver that pumps per-service
// Ready/Failed transitions into the DB as they happen during StartPreview,
// and writes a preview_logs row with the tail of stdout/stderr when a service
// fails. It uses fresh background contexts for each DB write so observer
// callbacks fired after StartPreview returns (progressive support services,
// the startService goroutine catching a non-zero exit) still land in the DB
// even if the request context has already been canceled.
func (m *Manager) newServiceObserver(orgID, previewID uuid.UUID, metricsSource, metricsRepo string) *managerServiceObserver {
	observer := &managerServiceObserver{
		manager:       m,
		orgID:         orgID,
		previewID:     previewID,
		source:        strings.TrimSpace(metricsSource),
		repository:    strings.TrimSpace(metricsRepo),
		phaseStarts:   make(map[string]time.Time),
		outputCh:      make(chan previewServiceOutput, serviceOutputBufferSize),
		outputDone:    make(chan struct{}),
		lifecycleCh:   make(chan previewLifecycleLog, lifecycleLogBufferSize),
		lifecycleDone: make(chan struct{}),
	}
	go observer.runServiceOutputWriter()
	go observer.runLifecycleLogWriter()
	return observer
}

type managerServiceObserver struct {
	manager       *Manager
	orgID         uuid.UUID
	previewID     uuid.UUID
	source        string
	repository    string
	phaseMu       sync.Mutex
	phaseStarts   map[string]time.Time
	outputCh      chan previewServiceOutput
	outputDone    chan struct{}
	lifecycleCh   chan previewLifecycleLog
	lifecycleDone chan struct{}
	outputMu      sync.Mutex
	outputClosed  bool
	closeOnce     sync.Once
}

const observerWriteTimeout = 5 * time.Second
const maxPersistedServiceOutputRunes = 4000
const serviceOutputBufferSize = 512
const serviceOutputFlushInterval = 250 * time.Millisecond
const serviceOutputBatchLines = 50
const lifecycleLogBufferSize = 256

type previewServiceOutput struct {
	name string
	line string
	step models.PreviewLogStep
}

type previewLifecycleLog struct {
	level    string
	step     models.PreviewLogStep
	message  string
	metadata json.RawMessage
}

func previewServiceOutputMessage(name, line string) string {
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return ""
	}
	runes := []rune(line)
	if len(runes) > maxPersistedServiceOutputRunes {
		line = string(runes[:maxPersistedServiceOutputRunes]) + "..."
	}
	return fmt.Sprintf("[%s] %s", name, line)
}

func (o *managerServiceObserver) OnServiceOutput(name, line string) {
	o.outputMu.Lock()
	defer o.outputMu.Unlock()
	if o.outputClosed {
		return
	}
	o.enqueueOutputLocked(previewServiceOutput{name: name, line: line, step: models.PreviewLogStepStart})
}

func (o *managerServiceObserver) OnInstallOutput(line string) {
	o.outputMu.Lock()
	defer o.outputMu.Unlock()
	if o.outputClosed {
		return
	}
	o.enqueueOutputLocked(previewServiceOutput{name: "install", line: line, step: models.PreviewLogStepInstall})
}

func (o *managerServiceObserver) enqueueOutputLocked(output previewServiceOutput) {
	select {
	case o.outputCh <- output:
	default:
		o.manager.logger.Warn().
			Str("preview_id", o.previewID.String()).
			Str("source", output.name).
			Msg("observer: dropping preview output because the buffer is full")
	}
}

func (o *managerServiceObserver) Close() {
	o.closeOnce.Do(func() {
		o.outputMu.Lock()
		o.outputClosed = true
		close(o.outputCh)
		close(o.lifecycleCh)
		o.outputMu.Unlock()
		<-o.outputDone
		<-o.lifecycleDone
	})
}

func (o *managerServiceObserver) runServiceOutputWriter() {
	defer close(o.outputDone)
	ticker := time.NewTicker(serviceOutputFlushInterval)
	defer ticker.Stop()

	batches := make(map[models.PreviewLogStep][]string)
	flush := func() {
		for step, batch := range batches {
			if len(batch) == 0 {
				continue
			}
			o.writeOutputLog(step, strings.Join(batch, "\n"))
			delete(batches, step)
		}
	}

	for {
		select {
		case output, ok := <-o.outputCh:
			if !ok {
				flush()
				return
			}
			msg := previewServiceOutputMessage(output.name, output.line)
			if msg == "" {
				continue
			}
			step := output.step
			if step == "" {
				step = models.PreviewLogStepStart
			}
			batches[step] = append(batches[step], msg)
			if len(batches[step]) >= serviceOutputBatchLines {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (o *managerServiceObserver) runLifecycleLogWriter() {
	defer close(o.lifecycleDone)
	for entry := range o.lifecycleCh {
		o.persistLifecycleLog(entry)
	}
}

func (o *managerServiceObserver) writeOutputLog(step models.PreviewLogStep, msg string) {
	ctx, cancel := context.WithTimeout(context.Background(), observerWriteTimeout)
	defer cancel()
	logEntry := &models.PreviewLog{
		PreviewInstanceID: o.previewID,
		OrgID:             o.orgID,
		Level:             "info",
		Step:              step,
		Message:           msg,
		Metadata:          json.RawMessage(`{"batched":true}`),
	}
	if err := o.manager.store.CreatePreviewLog(ctx, logEntry); err != nil {
		o.manager.logger.Warn().Err(err).
			Str("preview_id", o.previewID.String()).
			Msg("observer: failed to write preview service output log")
	}
}

func (o *managerServiceObserver) OnPhaseStart(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	o.phaseMu.Lock()
	o.phaseStarts[name] = time.Now()
	o.phaseMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), observerWriteTimeout)
	defer cancel()
	if err := o.manager.store.UpdatePreviewPhase(ctx, o.orgID, o.previewID, name); err != nil {
		o.manager.logger.Warn().Err(err).
			Str("preview_id", o.previewID.String()).
			Str("phase", name).
			Msg("observer: failed to update preview phase")
	}
	o.enqueueLifecycleLog("info", previewLogStepForPhase(name), fmt.Sprintf("preview phase started: %s", name), map[string]any{
		"phase":  name,
		"status": "started",
	})
}

func (o *managerServiceObserver) OnPhaseEnd(name string, phaseErr error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}

	o.phaseMu.Lock()
	started, ok := o.phaseStarts[name]
	if ok {
		delete(o.phaseStarts, name)
	}
	o.phaseMu.Unlock()
	metadata := map[string]any{
		"phase":  name,
		"status": "completed",
	}
	if ok {
		metadata["duration_ms"] = time.Since(started).Milliseconds()
	}
	level := "info"
	message := fmt.Sprintf("preview phase completed: %s", name)
	if phaseErr != nil {
		level = "warn"
		message = fmt.Sprintf("preview phase failed: %s: %v", name, phaseErr)
		metadata["status"] = "failed"
		metadata["error"] = phaseErr.Error()
	}
	o.enqueueLifecycleLog(level, previewLogStepForPhase(name), message, metadata)
	if ok && o.source != "" && o.repository != "" {
		metrics.RecordBranchPreviewPhaseDuration(context.Background(), o.orgID.String(), o.source, o.repository, name, time.Since(started))
	}
}

func (o *managerServiceObserver) OnServiceReady(name string, port, pid int) {
	ctx, cancel := context.WithTimeout(context.Background(), observerWriteTimeout)
	defer cancel()
	if err := o.manager.store.UpdateServiceStatus(ctx, o.orgID, o.previewID, name, models.PreviewServiceStatusReady, ""); err != nil {
		o.manager.logger.Warn().Err(err).
			Str("preview_id", o.previewID.String()).
			Str("service", name).
			Msg("observer: failed to mark service ready")
	}
	if pid > 0 {
		if err := o.manager.store.UpdateServicePID(ctx, o.orgID, o.previewID, name, pid); err != nil {
			o.manager.logger.Warn().Err(err).
				Str("preview_id", o.previewID.String()).
				Str("service", name).
				Msg("observer: failed to record service PID")
		}
	}
}

func (o *managerServiceObserver) OnServiceFailed(name, errMsg string, tail []string) {
	ctx, cancel := context.WithTimeout(context.Background(), observerWriteTimeout)
	defer cancel()
	if err := o.manager.store.UpdateServiceStatus(ctx, o.orgID, o.previewID, name, models.PreviewServiceStatusFailed, errMsg); err != nil {
		o.manager.logger.Warn().Err(err).
			Str("preview_id", o.previewID.String()).
			Str("service", name).
			Msg("observer: failed to mark service failed")
	}
	msg := fmt.Sprintf("service %q failed: %s", name, errMsg)
	if len(tail) > 0 {
		msg += "\n--- last output ---\n" + strings.Join(tail, "\n")
	}
	logEntry := &models.PreviewLog{
		PreviewInstanceID: o.previewID,
		OrgID:             o.orgID,
		Level:             "error",
		Step:              models.PreviewLogStepStart,
		Message:           msg,
	}
	if err := o.manager.store.CreatePreviewLog(ctx, logEntry); err != nil {
		o.manager.logger.Warn().Err(err).
			Str("preview_id", o.previewID.String()).
			Str("service", name).
			Msg("observer: failed to write preview log")
	}
}

func (o *managerServiceObserver) OnInstallFailed(errMsg string, tail []string) {
	ctx, cancel := context.WithTimeout(context.Background(), observerWriteTimeout)
	defer cancel()

	msg := fmt.Sprintf("preview install failed: %s", errMsg)
	if len(tail) > 0 {
		msg += "\n--- last output ---\n" + strings.Join(tail, "\n")
	}
	logEntry := &models.PreviewLog{
		PreviewInstanceID: o.previewID,
		OrgID:             o.orgID,
		Level:             "error",
		Step:              models.PreviewLogStepInstall,
		Message:           msg,
	}
	if err := o.manager.store.CreatePreviewLog(ctx, logEntry); err != nil {
		o.manager.logger.Warn().Err(err).
			Str("preview_id", o.previewID.String()).
			Msg("observer: failed to write preview install log")
	}
}

func (o *managerServiceObserver) OnDependencyCacheRestore(status string, cacheKey string, sizeBytes int64, err error) {
	switch status {
	case "disabled", "miss", "restore_failed", "restored", "restored_satisfied_install", "skipped_marker_missing":
	default:
		return
	}
	level := "info"
	msg := fmt.Sprintf("preview dependency cache %s", status)
	if err != nil {
		level = "warn"
		msg = fmt.Sprintf("preview dependency cache restore failed: %v", err)
	}
	o.writeDependencyCacheLog(level, msg, cacheKey, sizeBytes)
}

func (o *managerServiceObserver) OnDependencyCacheSave(status string, cacheKey string, sizeBytes int64, err error) {
	switch status {
	case "saved", "skipped", "skipped_fresh_restore", "skipped_no_paths", "unchanged", "save_failed":
	default:
		return
	}
	level := "info"
	msg := fmt.Sprintf("preview dependency cache save %s", status)
	if err != nil || status == "save_failed" {
		level = "warn"
		msg = fmt.Sprintf("preview dependency cache save failed: %v", err)
	}
	o.writeDependencyCacheLog(level, msg, cacheKey, sizeBytes)
}

func (o *managerServiceObserver) OnPackageManagerCacheRestore(status string, cacheKey string, sizeBytes int64, err error) {
	switch status {
	case "disabled", "miss", "restore_failed", "restored", "key_failed", "skipped_no_paths":
	default:
		return
	}
	level := "info"
	msg := fmt.Sprintf("preview package-manager cache %s", status)
	if err != nil {
		level = "warn"
		msg = fmt.Sprintf("preview package-manager cache restore failed: %v", err)
	}
	o.writeDependencyCacheLog(level, msg, cacheKey, sizeBytes)
}

func (o *managerServiceObserver) OnPackageManagerCacheSave(status string, cacheKey string, sizeBytes int64, err error) {
	switch status {
	case "saved", "skipped", "skipped_no_paths", "unchanged", "save_failed":
	default:
		return
	}
	level := "info"
	msg := fmt.Sprintf("preview package-manager cache save %s", status)
	if err != nil || status == "save_failed" {
		level = "warn"
		msg = fmt.Sprintf("preview package-manager cache save failed: %v", err)
	}
	o.writeDependencyCacheLog(level, msg, cacheKey, sizeBytes)
}

func (o *managerServiceObserver) OnBuildCacheRestore(status string, cacheKey string, sizeBytes int64, err error) {
	switch status {
	case "disabled", "miss", "restore_failed", "restored", "key_failed":
	default:
		return
	}
	level := "info"
	msg := fmt.Sprintf("preview build cache %s", status)
	if err != nil {
		level = "warn"
		msg = fmt.Sprintf("preview build cache restore failed: %v", err)
	}
	o.writeDependencyCacheLog(level, msg, cacheKey, sizeBytes)
}

func (o *managerServiceObserver) OnBuildCacheSave(status string, cacheKey string, sizeBytes int64, err error) {
	switch status {
	case "saved", "skipped", "skipped_no_paths", "unchanged", "save_failed":
	default:
		return
	}
	level := "info"
	msg := fmt.Sprintf("preview build cache save %s", status)
	if err != nil || status == "save_failed" {
		level = "warn"
		msg = fmt.Sprintf("preview build cache save failed: %v", err)
	}
	o.writeDependencyCacheLog(level, msg, cacheKey, sizeBytes)
}

func (o *managerServiceObserver) writeDependencyCacheLog(level, msg, cacheKey string, sizeBytes int64) {
	ctx, cancel := context.WithTimeout(context.Background(), observerWriteTimeout)
	defer cancel()
	metadata, _ := json.Marshal(map[string]any{
		"cache_key":  cacheKey,
		"size_bytes": sizeBytes,
	})
	step := models.PreviewLogStepInstall
	if strings.Contains(msg, "build cache") {
		step = models.PreviewLogStepBuild
	}
	logEntry := &models.PreviewLog{
		PreviewInstanceID: o.previewID,
		OrgID:             o.orgID,
		Level:             level,
		Step:              step,
		Message:           msg,
		Metadata:          metadata,
	}
	if err := o.manager.store.CreatePreviewLog(ctx, logEntry); err != nil {
		o.manager.logger.Warn().Err(err).Str("preview_id", o.previewID.String()).Msg("observer: failed to write preview dependency cache log")
	}
}

func (o *managerServiceObserver) enqueueLifecycleLog(level string, step models.PreviewLogStep, msg string, metadata map[string]any) {
	rawMetadata, err := json.Marshal(metadata)
	if err != nil {
		o.manager.logger.Warn().Err(err).Str("preview_id", o.previewID.String()).Msg("observer: failed to marshal preview lifecycle log metadata")
		rawMetadata = json.RawMessage(`{}`)
	}
	o.outputMu.Lock()
	defer o.outputMu.Unlock()
	if o.outputClosed {
		return
	}
	entry := previewLifecycleLog{
		level:    level,
		step:     step,
		message:  msg,
		metadata: rawMetadata,
	}
	select {
	case o.lifecycleCh <- entry:
	default:
		o.manager.logger.Warn().
			Str("preview_id", o.previewID.String()).
			Str("phase", fmt.Sprint(metadata["phase"])).
			Msg("observer: dropping preview lifecycle log because the buffer is full")
	}
}

func (o *managerServiceObserver) persistLifecycleLog(entry previewLifecycleLog) {
	ctx, cancel := context.WithTimeout(context.Background(), observerWriteTimeout)
	defer cancel()
	logEntry := &models.PreviewLog{
		PreviewInstanceID: o.previewID,
		OrgID:             o.orgID,
		Level:             entry.level,
		Step:              entry.step,
		Message:           entry.message,
		Metadata:          entry.metadata,
	}
	if err := o.manager.store.CreatePreviewLog(ctx, logEntry); err != nil {
		o.manager.logger.Warn().Err(err).Str("preview_id", o.previewID.String()).Msg("observer: failed to write preview lifecycle log")
	}
}

func previewLogStepForPhase(phase string) models.PreviewLogStep {
	switch {
	case strings.Contains(phase, "install"),
		strings.Contains(phase, "dependency_cache"),
		strings.Contains(phase, "package_manager_cache"):
		return models.PreviewLogStepInstall
	case strings.Contains(phase, "build_cache"):
		return models.PreviewLogStepBuild
	case strings.Contains(phase, "infra"),
		strings.Contains(phase, "init"):
		return models.PreviewLogStepInit
	case strings.Contains(phase, "service"),
		strings.Contains(phase, "readiness"):
		return models.PreviewLogStepStart
	default:
		return models.PreviewLogStepBuild
	}
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
	return m.StopPreviewWithReason(ctx, orgID, previewID, models.PreviewStoppedReasonNone)
}

// StopPreviewWithReason stops a preview, records a stop cause when supplied,
// and revokes all access sessions.
func (m *Manager) StopPreviewWithReason(ctx context.Context, orgID, previewID uuid.UUID, reason models.PreviewStoppedReason) error {
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
	if err := m.store.StopPreviewWithRevocationAndReason(ctx, orgID, previewID, reason); err != nil {
		return fmt.Errorf("stop preview: %w", err)
	}
	if instance.PreviewTargetID != nil {
		if target, targetErr := m.store.GetPreviewTarget(ctx, orgID, *instance.PreviewTargetID); targetErr == nil {
			metrics.RecordBranchPreviewMinutes(ctx, orgID.String(), string(target.SourceType), target.RepositoryID.String(), time.Since(instance.CreatedAt))
		}
	}

	// Stop HMR watching for this preview.
	if m.hmrWatcher != nil {
		m.hmrWatcher.StopWatching(previewID)
	}

	// Release the preview hold. The store returns the sibling state so we
	// know whether an agent turn is still using the container — if so, leave
	// it running; otherwise destroy it here. This is the inverse of the
	// orchestrator's ReleaseTurnHold in the common case where the user stops
	// a preview on an idle session.
	if instance.SessionID == uuid.Nil {
		if m.sandboxProvider != nil && len(instance.RecycleSandbox) > 2 {
			var sb agent.Sandbox
			if err := json.Unmarshal(instance.RecycleSandbox, &sb); err != nil {
				m.logger.Warn().Err(err).Str("preview_id", previewID.String()).Msg("failed to parse branch preview sandbox for destroy")
			} else if sb.ID != "" {
				destroyCtx, destroyCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer destroyCancel()
				if destroyErr := m.sandboxProvider.Destroy(destroyCtx, &sb); destroyErr != nil {
					m.logger.Error().Err(destroyErr).
						Str("preview_id", previewID.String()).
						Str("container_id", sb.ID).
						Msg("failed to destroy branch preview sandbox")
				}
			}
		}
		return nil
	}

	destroyNow, _, containerID, releaseErr := m.store.ReleasePreviewHold(ctx, orgID, previewID)
	if releaseErr != nil {
		// If the hold release fails we don't have clean signal about sibling
		// state; play it safe and leave the container alone. The reconciler
		// will eventually clean up orphans.
		m.logger.Warn().Err(releaseErr).
			Str("preview_id", previewID.String()).
			Msg("failed to release preview hold; leaving container for reconciler")
	} else if destroyNow && containerID != "" && m.sandboxProvider != nil {
		// Detached context so destroy completes even if the HTTP ctx was
		// cancelled while we were tearing down services above.
		destroyCtx, destroyCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer destroyCancel()
		// FinalizeContainerDestroy atomically clears container_id and marks
		// sandbox_state='snapshotted' only if no holder has come back in the
		// gap between ReleasePreviewHold and here. If it returns false we
		// must leave the container alone — a new holder owns it now.
		// Order: clear container_id FIRST via the CAS, THEN destroy the
		// container, so a concurrent reuse-path reader sees the cleared row
		// and takes the hydrate branch instead of attaching to a dying ID.
		if m.sessionStore != nil {
			cleared, finalizeErr := m.sessionStore.FinalizeContainerDestroy(destroyCtx, orgID, instance.SessionID, containerID)
			if finalizeErr != nil {
				m.logger.Warn().Err(finalizeErr).
					Str("preview_id", previewID.String()).
					Str("container_id", containerID).
					Msg("failed to finalize container destroy; leaving container for reconciler")
			} else if !cleared {
				m.logger.Info().
					Str("preview_id", previewID.String()).
					Str("container_id", containerID).
					Msg("another holder acquired between preview release and destroy; leaving container alive")
			} else {
				sb := &agent.Sandbox{ID: containerID, Provider: ProviderDocker}
				if destroyErr := m.sandboxProvider.Destroy(destroyCtx, sb); destroyErr != nil {
					m.logger.Error().Err(destroyErr).
						Str("preview_id", previewID.String()).
						Str("container_id", containerID).
						Msg("failed to destroy sandbox after final hold release")
				}
			}
		}
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
		PreviewOrigin:  m.previewOrigin(previewID),
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
		// Deliberately short: this initial window only needs to cover token
		// redemption. The gateway extends the session to its full sliding
		// lifetime (accessSessionTTL) on the first proxied request.
		ExpiresAt: time.Now().Add(5 * time.Minute),
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

// SetLifetime sets the preview expiry to a bounded duration from now. It is
// intentionally capped to DefaultHardTTL per adjustment so the UI can offer
// short, explicit choices without creating an always-on environment. Total
// lifetime remains capped by CreatedAt + DefaultMaxTTL.
func (m *Manager) SetLifetime(ctx context.Context, orgID, previewID uuid.UUID, duration time.Duration) (time.Time, error) {
	if duration < MinLifetimeTTL {
		return time.Time{}, fmt.Errorf("preview lifetime must be at least %s", MinLifetimeTTL)
	}
	if duration > DefaultHardTTL {
		return time.Time{}, fmt.Errorf("preview lifetime cannot exceed %s per adjustment", DefaultHardTTL)
	}

	instance, err := m.store.GetPreviewInstance(ctx, orgID, previewID)
	if err != nil {
		return time.Time{}, fmt.Errorf("get preview instance: %w", err)
	}

	now := time.Now()
	maxExpiry := instance.CreatedAt.Add(DefaultMaxTTL)
	if !now.Before(maxExpiry) {
		return time.Time{}, fmt.Errorf("preview has reached its maximum lifetime and cannot be extended further")
	}

	newExpiry := now.Add(duration)
	if newExpiry.After(maxExpiry) {
		newExpiry = maxExpiry
	}

	if err := m.store.UpdatePreviewExpiry(ctx, orgID, previewID, newExpiry); err != nil {
		return time.Time{}, err
	}
	return newExpiry, nil
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
// StopActivePreviewForSession
// =============================================================================

// StopActivePreviewForSession stops the active preview for the given session,
// if one exists. Returns (true, nil) when a preview was actually stopped,
// (false, nil) when the session had no active preview, or (false, err) on a
// real failure.
//
// Used by the reaper before it expires a snapshot: if a preview is holding
// the sandbox container, StopPreview goes through the hold-aware destroy
// path and tears down the container + clears container_id, so the reaper's
// follow-up sandbox_state='destroyed' transition is clean.
func (m *Manager) StopActivePreviewForSession(ctx context.Context, orgID, sessionID uuid.UUID) (bool, error) {
	preview, err := m.store.GetActivePreviewForSession(ctx, orgID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("lookup active preview for session: %w", err)
	}
	if err := m.StopPreview(ctx, orgID, preview.ID); err != nil {
		return false, fmt.Errorf("stop active preview %s: %w", preview.ID, err)
	}
	return true, nil
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

// HMRWatcher returns the worker-local HMR watcher, if configured.
func (m *Manager) HMRWatcher() *HMRWatcher {
	return m.hmrWatcher
}

// =============================================================================
// RecyclePreview
// =============================================================================

// RecyclePreview restarts a preview in place. It stops the existing processes,
// re-provisions infrastructure, re-runs init scripts, and restarts services.
// The preview instance ID and last_path are preserved.
func (m *Manager) RecyclePreview(ctx context.Context, orgID, previewID uuid.UUID) error {
	return m.recyclePreview(ctx, orgID, previewID, nil, nil)
}

// RecyclePreviewWithConfig restarts an active preview in place using a freshly
// resolved config. The config is validated before any provider process is
// stopped so invalid workspace edits do not take down the currently running
// preview.
func (m *Manager) RecyclePreviewWithConfig(ctx context.Context, orgID, previewID uuid.UUID, cfg *models.PreviewConfig) error {
	return m.recyclePreview(ctx, orgID, previewID, cfg, nil)
}

func (m *Manager) RecyclePreviewWithConfigAndRevision(ctx context.Context, orgID, previewID uuid.UUID, cfg *models.PreviewConfig, revision int64, revisionUpdatedAt time.Time) error {
	return m.recyclePreview(ctx, orgID, previewID, cfg, &workspaceRevisionStamp{
		revision:  revision,
		updatedAt: revisionUpdatedAt,
	})
}

type workspaceRevisionStamp struct {
	revision  int64
	updatedAt time.Time
}

func (m *Manager) recyclePreview(ctx context.Context, orgID, previewID uuid.UUID, refreshedConfig *models.PreviewConfig, revisionStamp *workspaceRevisionStamp) error {
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
	if refreshedConfig != nil {
		resourcePolicy, policyErr := m.resourcePolicy(ctx, orgID)
		if policyErr != nil {
			return policyErr
		}
		if errs := ValidateConfigWithResourcePolicy(refreshedConfig, resourcePolicy); len(errs) > 0 {
			return fmt.Errorf("%w: validate %s: %s", ErrInvalidConfig, repoconfig.ConfigPath, strings.Join(errs, "; "))
		}
		input.Config = refreshedConfig
		limits := ResolveResourceLimitsWithPolicy(refreshedConfig, resourcePolicy)
		configJSON, err := json.Marshal(refreshedConfig)
		if err != nil {
			return fmt.Errorf("marshal refreshed recycle config: %w", err)
		}
		if err := m.store.UpdatePreviewRecycleConfig(
			ctx,
			orgID,
			previewID,
			refreshedConfig.Name,
			refreshedConfig.Primary,
			computeConfigDigest(refreshedConfig),
			limits.MemoryMiB,
			limits.CPUMillis,
			limits.DiskMiB,
			configJSON,
		); err != nil {
			return err
		}
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
	if revisionStamp != nil && !revisionStamp.updatedAt.IsZero() {
		if err := m.store.UpdatePreviewSourceWorkspaceRevision(ctx, orgID, previewID, revisionStamp.revision, revisionStamp.updatedAt); err != nil {
			return err
		}
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
	observer := m.newServiceObserver(orgID, previewID, "", "")
	defer observer.Close()
	if err := m.resolvePreviewSecrets(ctx, input); err != nil {
		if statusErr := m.store.UpdatePreviewStatus(ctx, orgID, previewID, models.PreviewStatusFailed, err.Error()); statusErr != nil {
			m.logger.Warn().Err(statusErr).Msg("recycle: failed to set failed status after secret resolution error")
		}
		m.releasePreviewHoldAfterRecycleFailure(ctx, instance)
		return err
	}
	var recycleRuntime *models.PreviewRuntime
	if m.previewInternalBaseURL != "" {
		recycleRuntime, err = m.store.CreateNextPreviewRuntime(ctx, orgID, previewID, m.workerNodeID, m.previewInternalBaseURL)
		if err != nil {
			if statusErr := m.store.UpdatePreviewStatus(ctx, orgID, previewID, models.PreviewStatusFailed, "recycle failed: could not create runtime epoch"); statusErr != nil {
				m.logger.Warn().Err(statusErr).Msg("recycle: failed to set failed status after runtime epoch error")
			}
			m.releasePreviewHoldAfterRecycleFailure(ctx, instance)
			return fmt.Errorf("recycle: create runtime epoch: %w", err)
		}
	}
	handle, err := m.provider.StartPreview(ctx, input.Sandbox, input.Config, StartPreviewOptions{
		OrgID:        input.OrgID,
		RepositoryID: input.RepositoryID,
		SessionID:    input.SessionID,
		ConfigDigest: computeConfigDigest(input.Config),
		ExtraEnv:     m.platformEnv(previewID),
	}, observer)
	if err != nil {
		if recycleRuntime != nil {
			if runtimeErr := m.store.MarkPreviewRuntimeFailed(ctx, orgID, recycleRuntime.ID, err.Error()); runtimeErr != nil {
				m.logger.Warn().Err(runtimeErr).Msg("recycle: failed to mark runtime failed after start error")
			}
		}
		if statusErr := m.store.UpdatePreviewStatus(ctx, orgID, previewID, models.PreviewStatusFailed, err.Error()); statusErr != nil {
			m.logger.Warn().Err(statusErr).Msg("recycle: failed to set failed status")
		}
		m.releasePreviewHoldAfterRecycleFailure(ctx, instance)
		return fmt.Errorf("recycle start: %w", err)
	}

	// Update instance with new handle. This is critical — if it fails, the DB
	// still points to the old (dead) handle and all subsequent proxy/status
	// operations will break. Stop the new preview and fail the recycle.
	if recycleRuntime != nil {
		err = m.store.MarkPreviewRuntimeReady(ctx, orgID, recycleRuntime.ID, handle.Handle, handle.PrimaryPort)
	} else {
		err = m.store.UpdatePreviewHandle(ctx, orgID, previewID, handle.Handle, handle.PrimaryPort)
	}
	if err != nil {
		m.logger.Error().Err(err).Msg("recycle: failed to update handle, stopping new preview")
		_ = m.provider.StopPreview(ctx, handle.Handle)
		if statusErr := m.store.UpdatePreviewStatus(ctx, orgID, previewID, models.PreviewStatusFailed, "recycle failed: could not persist new handle"); statusErr != nil {
			m.logger.Warn().Err(statusErr).Msg("recycle: failed to set failed status after handle update error")
		}
		m.releasePreviewHoldAfterRecycleFailure(ctx, instance)
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

func (m *Manager) releasePreviewHoldAfterRecycleFailure(parentCtx context.Context, instance *models.PreviewInstance) {
	if instance == nil || instance.SessionID == uuid.Nil {
		return
	}
	if !instance.PreviewHoldingContainer {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parentCtx), 60*time.Second)
	defer cancel()

	destroyNow, _, containerID, err := m.store.ReleasePreviewHold(ctx, instance.OrgID, instance.ID)
	if err != nil {
		m.logger.Warn().Err(err).
			Str("preview_id", instance.ID.String()).
			Msg("recycle: failed to release preview hold after restart failure")
		return
	}
	if !destroyNow || containerID == "" || m.sessionStore == nil || m.sandboxProvider == nil {
		return
	}

	cleared, err := m.sessionStore.FinalizeContainerDestroy(ctx, instance.OrgID, instance.SessionID, containerID)
	if err != nil {
		m.logger.Warn().Err(err).
			Str("preview_id", instance.ID.String()).
			Str("container_id", containerID).
			Msg("recycle: failed to finalize container destroy after restart failure")
		return
	}
	if !cleared {
		return
	}
	if err := m.sandboxProvider.Destroy(ctx, &agent.Sandbox{ID: containerID, Provider: ProviderDocker}); err != nil {
		m.logger.Error().Err(err).
			Str("preview_id", instance.ID.String()).
			Str("container_id", containerID).
			Msg("recycle: failed to destroy sandbox after restart failure")
	}
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

func (m *Manager) previewOrigin(previewID uuid.UUID) string {
	env := m.platformEnv(previewID)
	if env == nil {
		return ""
	}
	return env["PREVIEW_ORIGIN"]
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
	return m.checkConcurrencyCapsWithStore(ctx, m.store, orgID, userID, m.workerNodeID)
}

func (m *Manager) checkConcurrencyCapsWithStore(ctx context.Context, store *db.PreviewStore, orgID, userID uuid.UUID, workerNodeID string) error {
	maxPerUser, err := m.maxPreviewsPerUser(ctx, orgID)
	if err != nil {
		return err
	}

	// Per-user cap.
	userCount, err := store.CountActivePreviewsByUser(ctx, orgID, userID)
	if err != nil {
		return fmt.Errorf("count user previews: %w", err)
	}
	if userCount >= maxPerUser {
		return &CapacityError{Scope: CapacityScopeUser, Active: userCount, Limit: maxPerUser}
	}

	// Per-org cap.
	orgCount, err := store.CountActivePreviewsByOrg(ctx, orgID)
	if err != nil {
		return fmt.Errorf("count org previews: %w", err)
	}
	if orgCount >= m.maxPerOrg {
		return fmt.Errorf("%w: your team has %d active previews (limit %d) — ask a teammate to stop one", ErrPreviewCapacity, orgCount, m.maxPerOrg)
	}

	// Per-worker cap. This is the capacity guardrail: when the host is
	// saturated, a fresh StartPreview would risk OOM-killing peers.
	workerCount, err := store.CountActivePreviewsByWorker(ctx, workerNodeID)
	if err != nil {
		return fmt.Errorf("count worker previews: %w", err)
	}
	if workerCount >= m.maxPerWorker {
		return fmt.Errorf("%w: all preview slots are in use (%d/%d) — try again in a few minutes", ErrPreviewCapacity, workerCount, m.maxPerWorker)
	}

	return nil
}

// CheckPreviewCapacity performs a non-transactional quota pre-check for branch
// previews. It returns ErrPreviewCapacity (check with errors.Is) when any cap
// would be exceeded. This is a best-effort early check; the authoritative cap
// enforcement runs atomically inside ReserveBranchPreviewForWorkerInTx.
func (m *Manager) CheckPreviewCapacity(ctx context.Context, orgID, userID uuid.UUID, workerNodeID string) error {
	return m.checkStandaloneConcurrencyCapsWithStore(ctx, m.store, orgID, userID, workerNodeID)
}

func (m *Manager) checkStandaloneConcurrencyCapsWithStore(ctx context.Context, store *db.PreviewStore, orgID, userID uuid.UUID, workerNodeID string) error {
	maxPerUser, err := m.maxPreviewsPerUser(ctx, orgID)
	if err != nil {
		return err
	}
	counts, err := store.CheckStandaloneCapacityCounts(ctx, orgID, userID, workerNodeID)
	if err != nil {
		return fmt.Errorf("check preview capacity: %w", err)
	}
	if counts.UserStandalone >= maxPerUser {
		return fmt.Errorf("%w: you already have %d active branch previews (limit %d) — stop one before starting another", ErrPreviewCapacity, counts.UserStandalone, maxPerUser)
	}
	if counts.OrgStandalone >= m.maxPerOrg {
		return fmt.Errorf("%w: your team has %d active branch previews (limit %d) — ask a teammate to stop one", ErrPreviewCapacity, counts.OrgStandalone, m.maxPerOrg)
	}
	if counts.WorkerTotal >= m.maxPerWorker {
		return fmt.Errorf("%w: all preview slots are in use (%d/%d) — try again in a few minutes", ErrPreviewCapacity, counts.WorkerTotal, m.maxPerWorker)
	}
	return nil
}

func (m *Manager) maxPreviewsPerUser(ctx context.Context, orgID uuid.UUID) (int, error) {
	if m.orgSettings == nil {
		return m.maxPerUser, nil
	}

	org, err := m.orgSettings.GetByID(ctx, orgID)
	if err != nil {
		return 0, fmt.Errorf("load org preview settings: %w", err)
	}
	hasOrgSetting, err := hasPreviewMaxPreviewsPerUserSetting(org.Settings)
	if err != nil {
		return 0, fmt.Errorf("parse org preview settings: %w", err)
	}
	if !hasOrgSetting {
		return m.maxPerUser, nil
	}
	settings, err := models.ParseOrgSettings(org.Settings)
	if err != nil {
		return 0, fmt.Errorf("parse org preview settings: %w", err)
	}
	if settings.PreviewMaxPreviewsPerUser > 0 {
		return settings.PreviewMaxPreviewsPerUser, nil
	}
	return m.maxPerUser, nil
}

func (m *Manager) resourcePolicy(ctx context.Context, orgID uuid.UUID) (ResourcePolicy, error) {
	if m.orgSettings == nil {
		return defaultResourcePolicy(), nil
	}
	org, err := m.orgSettings.GetByID(ctx, orgID)
	if err != nil {
		return ResourcePolicy{}, fmt.Errorf("load org preview resource settings: %w", err)
	}
	settings, err := models.ParseOrgSettings(org.Settings)
	if err != nil {
		return ResourcePolicy{}, fmt.Errorf("parse org preview resource settings: %w", err)
	}
	return ResourcePolicyFromOrgSettings(settings), nil
}

func hasPreviewMaxPreviewsPerUserSetting(raw json.RawMessage) (bool, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return false, nil
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(raw, &settings); err != nil {
		return false, err
	}
	if settings == nil {
		return false, nil
	}
	_, ok := settings["preview_max_previews_per_user"]
	return ok, nil
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
		SessionID:       instance.SessionID,
		PreviewTargetID: previewTargetIDValue(instance.PreviewTargetID),
		OrgID:           instance.OrgID,
		UserID:          instance.UserID,
		Sandbox:         &sandbox,
		Config:          &cfg,
		BaseCommitSHA:   instance.BaseCommitSHA,
		ProfileName:     instance.ProfileName,
	}, nil
}

func previewTargetIDValue(id *uuid.UUID) uuid.UUID {
	return uuidPointerValue(id)
}

func uuidPointerValue(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
}

func (m *Manager) loadRecycleInput(ctx context.Context, instance *models.PreviewInstance) (StartPreviewInput, error) {
	input, err := loadRecycleInput(instance)
	if err == nil {
		input.RepositoryID = m.recycleRepositoryID(ctx, instance)
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

func (m *Manager) recycleRepositoryID(ctx context.Context, instance *models.PreviewInstance) uuid.UUID {
	if instance.PreviewTargetID != nil && *instance.PreviewTargetID != uuid.Nil {
		target, err := m.store.GetPreviewTarget(ctx, instance.OrgID, *instance.PreviewTargetID)
		if err == nil {
			return target.RepositoryID
		}
		m.logger.Warn().Err(err).Str("preview_id", instance.ID.String()).Msg("failed to load preview target repository for recycle")
	}
	if instance.SessionID != uuid.Nil && m.sessionStore != nil {
		session, err := m.sessionStore.GetByID(ctx, instance.OrgID, instance.SessionID)
		if err == nil {
			return uuidPointerValue(session.RepositoryID)
		}
		m.logger.Warn().Err(err).Str("preview_id", instance.ID.String()).Msg("failed to load session repository for recycle")
	}
	return uuid.Nil
}

func (m *Manager) rebuildLegacyRecycleInput(ctx context.Context, instance *models.PreviewInstance) (StartPreviewInput, error) {
	if instance.SessionID == uuid.Nil {
		return StartPreviewInput{}, fmt.Errorf("branch preview has no legacy session recycle input")
	}
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
		RepositoryID:  uuidPointerValue(session.RepositoryID),
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

// nilIfEmpty returns a pointer to s, or nil when s is the empty string.
// Used when mapping optional string fields to nullable DB columns.
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// derefStringPtr dereferences a nullable string pointer, returning "" for nil.
func derefStringPtr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// isTransientPreviewDBError returns true only for errors that are worth
// retrying: network/connection failures where the operation may not have
// reached the server. PostgreSQL errors (pgconn.PgError), not-found rows, and
// cancelled contexts are all permanent and must not be retried.
func isTransientPreviewDBError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	var pgErr *pgconn.PgError
	return !errors.As(err, &pgErr)
}
