package preview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// =============================================================================
// Constants
// =============================================================================

const (
	// hmrStabilizationDelay is the debounce window after the last HMR message
	// before a screenshot is captured. If another HMR message arrives within
	// this window, the timer resets.
	hmrStabilizationDelay = 2 * time.Second

	// maxSnapshotsPerPreview is the hard cap on stored snapshots. When
	// exceeded, the oldest snapshots are evicted.
	maxSnapshotsPerPreview = 50

	// baselineDelay is the short delay before capturing the initial baseline
	// screenshot, giving the page time to finish its first paint.
	baselineDelay = 3 * time.Second

	// screenshotCaptureTimeout bounds how long a single screenshot capture
	// can take before being abandoned.
	screenshotCaptureTimeout = 15 * time.Second
)

// =============================================================================
// HMR pattern detection
// =============================================================================

// hmrPatternSets lists co-occurring substrings that identify HMR messages from
// common dev servers. Each entry requires ALL patterns in the set to match,
// reducing false positives from non-HMR messages that happen to contain a
// single matching substring.
var hmrPatternSets = [][]byte{
	// Vite: messages always include both "type" and "updates" array
	[]byte(`"type":"update"`), []byte(`"updates":`),
	// sentinel: end of Vite update pattern set
	nil,
	// Vite full-reload: includes "type" and "path"
	[]byte(`"type":"full-reload"`), []byte(`"path":`),
	nil,
	// webpack (webpack-dev-server): "action":"built" co-occurs with "hash"
	[]byte(`"action":"built"`), []byte(`"hash":"`),
	nil,
	// webpack hash message: "type":"hash" co-occurs with "data"
	[]byte(`"type":"hash"`), []byte(`"data":"`),
	nil,
	// Next.js: these action values are specific enough on their own
	[]byte(`"action":"serverComponentChanges"`),
	nil,
	[]byte(`"action":"devPagesManifestUpdate"`),
	nil,
}

// isHMRMessage returns true if data contains a known HMR update pattern set.
// All patterns within a set must match for the message to be considered HMR.
// Uses bytes.Contains directly to avoid allocating a string copy of the data.
func isHMRMessage(data []byte) bool {
	allMatch := true
	for _, p := range hmrPatternSets {
		if p == nil {
			// End of a pattern set. If all patterns matched, it's HMR.
			if allMatch {
				return true
			}
			allMatch = true
			continue
		}
		if allMatch && !bytes.Contains(data, p) {
			allMatch = false
		}
	}
	return false
}

// =============================================================================
// Per-preview watcher state
// =============================================================================

// previewWatcher holds the debounce timer and cancellation for a single
// preview's HMR monitoring.
type previewWatcher struct {
	previewID uuid.UUID
	orgID     uuid.UUID
	ctx       context.Context
	cancel    context.CancelFunc

	mu    sync.Mutex
	timer *time.Timer
}

// resetTimer cancels any pending screenshot and starts a new stabilization
// timer. When the timer fires, onFire is called.
func (pw *previewWatcher) resetTimer(onFire func()) {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	if pw.timer != nil {
		pw.timer.Stop()
	}
	pw.timer = time.AfterFunc(hmrStabilizationDelay, onFire)
}

// stopTimer cancels any pending screenshot timer.
func (pw *previewWatcher) stopTimer() {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	if pw.timer != nil {
		pw.timer.Stop()
		pw.timer = nil
	}
}

// =============================================================================
// HMRWatcher
// =============================================================================

// HMRWatcherConfig holds initialization options for HMRWatcher.
type HMRWatcherConfig struct {
	Inspector      PreviewInspector
	Store          *db.PreviewStore
	RuntimeStamper PreviewRuntimeRevisionStamper
	Logger         zerolog.Logger
	// BlobDir is the directory where screenshot PNGs are stored on disk.
	// Subdirectories are created per preview ID.
	BlobDir string
}

// HMRWatcher monitors WebSocket traffic for HMR update messages and
// automatically captures screenshots when the preview content changes.
//
// One HMRWatcher instance is shared across all active previews on a worker
// node. Each preview gets its own goroutine context and debounce timer.
type HMRWatcher struct {
	inspector      PreviewInspector
	store          *db.PreviewStore
	runtimeStamper PreviewRuntimeRevisionStamper
	logger         zerolog.Logger
	blobDir        string

	mu       sync.RWMutex
	watchers map[uuid.UUID]*previewWatcher // keyed by preview ID
	closed   bool
}

// NewHMRWatcher creates a new HMR watcher.
func NewHMRWatcher(cfg HMRWatcherConfig) (*HMRWatcher, error) {
	if cfg.Inspector == nil {
		return nil, fmt.Errorf("hmr watcher: inspector must not be nil")
	}
	if cfg.Store == nil {
		return nil, fmt.Errorf("hmr watcher: store must not be nil")
	}
	if cfg.BlobDir == "" {
		return nil, fmt.Errorf("hmr watcher: blob directory must be specified")
	}

	if err := os.MkdirAll(cfg.BlobDir, 0o750); err != nil {
		return nil, fmt.Errorf("hmr watcher: create blob dir: %w", err)
	}

	return &HMRWatcher{
		inspector:      cfg.Inspector,
		store:          cfg.Store,
		runtimeStamper: cfg.RuntimeStamper,
		logger:         cfg.Logger.With().Str("component", "hmr_watcher").Logger(),
		blobDir:        cfg.BlobDir,
		watchers:       make(map[uuid.UUID]*previewWatcher),
	}, nil
}

// StartWatching begins HMR monitoring for a preview. It captures an initial
// baseline screenshot after a short delay to allow the page to render.
//
// Calling StartWatching on an already-watched preview is a no-op.
func (hw *HMRWatcher) StartWatching(previewID, orgID uuid.UUID) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	if hw.closed {
		return
	}
	if _, exists := hw.watchers[previewID]; exists {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	pw := &previewWatcher{
		previewID: previewID,
		orgID:     orgID,
		ctx:       ctx,
		cancel:    cancel,
	}
	hw.watchers[previewID] = pw

	hw.logger.Info().
		Str("preview_id", previewID.String()).
		Msg("started HMR watching")

	// Capture baseline screenshot after a short delay.
	go hw.captureBaseline(ctx, pw)
}

// OnWebSocketMessage is called by the preview gateway proxy each time a
// WebSocket frame is received from the dev server. If the frame contains a
// known HMR update pattern, a screenshot capture is scheduled after the
// stabilization delay (debounced).
func (hw *HMRWatcher) OnWebSocketMessage(previewID uuid.UUID, data []byte) {
	if !isHMRMessage(data) {
		return
	}

	hw.mu.RLock()
	pw, exists := hw.watchers[previewID]
	closed := hw.closed
	hw.mu.RUnlock()

	if !exists || closed {
		return
	}

	hw.logger.Debug().
		Str("preview_id", previewID.String()).
		Int("frame_size", len(data)).
		Msg("HMR update detected, scheduling screenshot")

	// Debounce: reset the timer so rapid bursts of HMR messages result in
	// only a single screenshot after the activity settles.
	pw.resetTimer(func() {
		// Re-check that the watcher is still active. StopWatching may have
		// deleted it between the timer being set and firing.
		hw.mu.RLock()
		_, stillActive := hw.watchers[previewID]
		hw.mu.RUnlock()
		if !stillActive {
			return
		}
		hw.captureAgentChange(pw)
	})
}

// StopWatching stops HMR monitoring for a preview, cancelling any pending
// screenshot capture. Safe to call multiple times or for unknown preview IDs.
func (hw *HMRWatcher) StopWatching(previewID uuid.UUID) {
	hw.mu.Lock()
	pw, exists := hw.watchers[previewID]
	if exists {
		delete(hw.watchers, previewID)
	}
	hw.mu.Unlock()

	if !exists {
		return
	}

	pw.stopTimer()
	pw.cancel()

	hw.logger.Info().
		Str("preview_id", previewID.String()).
		Msg("stopped HMR watching")
}

// Close stops all watchers and releases resources. After Close returns,
// StartWatching and OnWebSocketMessage are no-ops.
func (hw *HMRWatcher) Close() {
	hw.mu.Lock()
	hw.closed = true
	watchers := make([]*previewWatcher, 0, len(hw.watchers))
	for _, pw := range hw.watchers {
		watchers = append(watchers, pw)
	}
	hw.watchers = make(map[uuid.UUID]*previewWatcher)
	hw.mu.Unlock()

	for _, pw := range watchers {
		pw.stopTimer()
		pw.cancel()
	}

	hw.logger.Info().Msg("HMR watcher closed")
}

// =============================================================================
// Screenshot capture
// =============================================================================

// captureBaseline waits for the baseline delay and then takes an initial
// screenshot with trigger="baseline".
func (hw *HMRWatcher) captureBaseline(ctx context.Context, pw *previewWatcher) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(baselineDelay):
	}

	hw.captureAndStore(ctx, pw, models.PreviewSnapshotTriggerBaseline)
}

// captureAgentChange captures a screenshot triggered by an HMR update.
func (hw *HMRWatcher) captureAgentChange(pw *previewWatcher) {
	// Build a context that respects both the per-preview cancellation and
	// the screenshot timeout.
	ctx, cancel := context.WithTimeout(pw.ctx, 30*time.Second)
	defer cancel()

	// Check if the watcher is still active.
	hw.mu.RLock()
	_, active := hw.watchers[pw.previewID]
	hw.mu.RUnlock()
	if !active {
		return
	}

	if hw.runtimeStamper != nil {
		if err := hw.runtimeStamper.StampPreviewRuntimeRevision(ctx, pw.orgID, pw.previewID, models.PreviewRuntimeRevisionSourceHMR); err != nil {
			hw.logger.Warn().Err(err).Str("preview_id", pw.previewID.String()).Msg("failed to stamp HMR runtime workspace revision")
		}
	}

	hw.captureAndStore(ctx, pw, models.PreviewSnapshotTriggerAgentChange)
}

// captureAndStore is the shared logic for taking a screenshot, writing the
// PNG to disk, and persisting the snapshot record.
func (hw *HMRWatcher) captureAndStore(ctx context.Context, pw *previewWatcher, trigger models.PreviewSnapshotTrigger) {
	log := hw.logger.With().
		Str("preview_id", pw.previewID.String()).
		Str("trigger", string(trigger)).
		Logger()

	// Apply a timeout so a hung headless browser does not block forever.
	captureCtx, captureCancel := context.WithTimeout(ctx, screenshotCaptureTimeout)
	defer captureCancel()

	opts := models.DefaultScreenshotOpts()
	result, err := hw.inspector.CaptureScreenshot(captureCtx, pw.previewID.String(), opts)
	if err != nil {
		log.Warn().Err(err).Msg("failed to capture screenshot")
		return
	}

	if len(result.PNG) == 0 {
		log.Warn().Msg("screenshot capture returned empty PNG, skipping")
		return
	}

	// Write PNG to disk.
	blobRef, err := hw.writePNG(pw.previewID, result.PNG)
	if err != nil {
		log.Error().Err(err).Msg("failed to write screenshot blob")
		return
	}

	// Marshal console errors to JSON for storage.
	var consoleErrors json.RawMessage
	if len(result.ConsoleErrors) > 0 {
		ce, err := json.Marshal(result.ConsoleErrors)
		if err != nil {
			log.Warn().Err(err).Msg("failed to marshal console errors")
			consoleErrors = json.RawMessage("[]")
		} else {
			consoleErrors = ce
		}
	} else {
		consoleErrors = json.RawMessage("[]")
	}

	snap := &models.PreviewSnapshot{
		PreviewInstanceID: pw.previewID,
		Trigger:           trigger,
		URLPath:           opts.Path,
		BlobRef:           blobRef,
		ViewportWidth:     opts.ViewportW,
		ViewportHeight:    opts.ViewportH,
		ConsoleErrors:     consoleErrors,
		// FileChanges is nullable; omit when not available.
	}

	if err := hw.store.CreateSnapshot(ctx, snap); err != nil {
		log.Error().Err(err).Msg("failed to persist snapshot")
		return
	}

	log.Info().
		Str("blob_ref", blobRef).
		Str("snapshot_id", snap.ID.String()).
		Msg("screenshot captured and stored")

	// Enforce the per-preview snapshot cap by evicting oldest entries.
	hw.enforceSnapshotLimit(ctx, pw)
}

// enforceSnapshotLimit checks whether the preview has exceeded the maximum
// number of snapshots and evicts the oldest if necessary.
func (hw *HMRWatcher) enforceSnapshotLimit(ctx context.Context, pw *previewWatcher) {
	count, err := hw.store.CountSnapshotsByPreview(ctx, pw.orgID, pw.previewID)
	if err != nil {
		hw.logger.Warn().
			Err(err).
			Str("preview_id", pw.previewID.String()).
			Msg("failed to count snapshots for limit enforcement")
		return
	}

	if count <= maxSnapshotsPerPreview {
		return
	}

	blobRefs, err := hw.store.DeleteOldestSnapshots(ctx, pw.orgID, pw.previewID, maxSnapshotsPerPreview)
	if err != nil {
		hw.logger.Warn().
			Err(err).
			Str("preview_id", pw.previewID.String()).
			Int("count", count).
			Msg("failed to evict oldest snapshots")
		return
	}

	// Clean up the corresponding PNG files on disk.
	for _, ref := range blobRefs {
		// Validate that the blob ref is under blobDir to prevent deletion
		// of arbitrary files if the DB contained a bad path.
		absRef, absErr := filepath.Abs(ref)
		absDir, dirErr := filepath.Abs(hw.blobDir)
		if absErr != nil || dirErr != nil || !strings.HasPrefix(absRef, absDir+string(filepath.Separator)) {
			hw.logger.Warn().Str("blob_ref", ref).Msg("skipping blob removal: path is not under blobDir")
			continue
		}
		if err := os.Remove(ref); err != nil && !os.IsNotExist(err) {
			hw.logger.Warn().Err(err).Str("blob_ref", ref).Msg("failed to remove evicted snapshot blob")
		}
	}

	hw.logger.Info().
		Str("preview_id", pw.previewID.String()).
		Int("evicted", count-maxSnapshotsPerPreview).
		Msg("evicted oldest snapshots to stay within limit")
}

// =============================================================================
// Blob storage
// =============================================================================

// writePNG writes the screenshot PNG to a file under blobDir and returns the
// blob reference (local file path). Files are organized by preview ID to
// avoid a flat directory with thousands of files.
func (hw *HMRWatcher) writePNG(previewID uuid.UUID, png []byte) (string, error) {
	dir := filepath.Join(hw.blobDir, previewID.String())
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create blob subdir: %w", err)
	}

	filename := fmt.Sprintf("%d_%s.png", time.Now().UnixMilli(), uuid.New().String()[:8])
	path := filepath.Join(dir, filename)

	if err := os.WriteFile(path, png, 0o600); err != nil {
		return "", fmt.Errorf("write png: %w", err)
	}

	return path, nil
}
