package preview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

const (
	previewResourceSampleInterval = 5 * time.Second
	previewResourceSampleTimeout  = 5 * time.Second
	previewProcessSnapshotLimit   = 12
)

type previewResourceSampleStore interface {
	RecordPreviewResourceSample(ctx context.Context, orgID uuid.UUID, sample *models.PreviewResourceSample) error
}

type previewResourceSampler struct {
	store           previewResourceSampleStore
	statsProvider   agent.RuntimeStatsProvider
	sandboxProvider agent.SandboxProvider
	sandbox         *agent.Sandbox
	orgID           uuid.UUID
	previewID       uuid.UUID
	workerNodeID    string
	cpuLimitMillis  int
	phase           func() string
	logger          zerolog.Logger
	interval        time.Duration

	stopOnce sync.Once
	cancel   context.CancelFunc
	stopCh   chan struct{}
	doneCh   chan struct{}
}

func newPreviewResourceSampler(
	store previewResourceSampleStore,
	statsProvider agent.RuntimeStatsProvider,
	sandboxProvider agent.SandboxProvider,
	sb *agent.Sandbox,
	orgID uuid.UUID,
	previewID uuid.UUID,
	workerNodeID string,
	cpuLimitMillis int,
	phase func() string,
	logger zerolog.Logger,
) *previewResourceSampler {
	if store == nil || statsProvider == nil || sandboxProvider == nil || sb == nil || sb.ID == "" || orgID == uuid.Nil || previewID == uuid.Nil {
		return nil
	}
	if phase == nil {
		phase = func() string { return "" }
	}
	return &previewResourceSampler{
		store:           store,
		statsProvider:   statsProvider,
		sandboxProvider: sandboxProvider,
		sandbox:         sb,
		orgID:           orgID,
		previewID:       previewID,
		workerNodeID:    workerNodeID,
		cpuLimitMillis:  cpuLimitMillis,
		phase:           phase,
		logger:          logger.With().Str("component", "preview_resource_sampler").Logger(),
		interval:        previewResourceSampleInterval,
		stopCh:          make(chan struct{}),
		doneCh:          make(chan struct{}),
	}
}

func (s *previewResourceSampler) Start() func() {
	if s == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go s.run(ctx)
	return s.Stop
}

func (s *previewResourceSampler) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		close(s.stopCh)
		<-s.doneCh
	})
}

func (s *previewResourceSampler) run(ctx context.Context) {
	defer close(s.doneCh)
	if keepGoing := s.sampleOnce(ctx); !keepGoing {
		return
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			if keepGoing := s.sampleOnce(ctx); !keepGoing {
				return
			}
		}
	}
}

func (s *previewResourceSampler) sampleOnce(ctx context.Context) bool {
	if s == nil {
		return false
	}
	sampleCtx, cancel := context.WithTimeout(ctx, previewResourceSampleTimeout)
	defer cancel()
	stats, err := s.statsProvider.Stats(sampleCtx, s.sandbox)
	if err != nil {
		if errors.Is(err, agent.ErrSandboxNotFound) {
			s.logger.Debug().
				Str("preview_id", s.previewID.String()).
				Str("sandbox_id", s.sandbox.ID).
				Msg("preview resource sampler stopped because sandbox disappeared")
			return false
		}
		s.logger.Debug().Err(err).
			Str("preview_id", s.previewID.String()).
			Str("sandbox_id", s.sandbox.ID).
			Msg("preview resource sample failed")
		return true
	}
	processes := s.processSnapshot(ctx)
	sampledAt := time.Now()
	sample := &models.PreviewResourceSample{
		OrgID:             s.orgID,
		PreviewInstanceID: s.previewID,
		WorkerNodeID:      s.workerNodeID,
		Phase:             strings.TrimSpace(s.phase()),
		MemoryBytes:       uint64ToInt64(stats.MemoryBytes),
		MemoryLimitBytes:  uint64ToInt64(stats.MemoryLimitBytes),
		CPUCores:          stats.CPUCores,
		CPULimitMillis:    s.cpuLimitMillis,
		Processes:         processes,
		SampledAt:         sampledAt,
	}
	if err := s.store.RecordPreviewResourceSample(ctx, s.orgID, sample); err != nil {
		s.logger.Warn().Err(err).
			Str("preview_id", s.previewID.String()).
			Str("sandbox_id", s.sandbox.ID).
			Msg("failed to persist preview resource sample")
	}
	return true
}

func (s *previewResourceSampler) processSnapshot(ctx context.Context) json.RawMessage {
	cmd := "(ps -eo pid=,ppid=,rss=,comm=,args= --sort=-rss || ps -eo pid=,ppid=,rss=,comm=,args=) 2>/dev/null | head -n " + strconv.Itoa(previewProcessSnapshotLimit)
	sampleCtx, cancel := context.WithTimeout(ctx, previewResourceSampleTimeout)
	defer cancel()
	var stdout bytes.Buffer
	exitCode, err := s.sandboxProvider.Exec(sampleCtx, s.sandbox, cmd, &stdout, io.Discard)
	if err != nil || exitCode != 0 {
		if err != nil {
			s.logger.Debug().Err(err).
				Str("preview_id", s.previewID.String()).
				Str("sandbox_id", s.sandbox.ID).
				Msg("preview process snapshot failed")
		}
		return json.RawMessage(`[]`)
	}
	return parsePreviewProcessSnapshot(stdout.String())
}

type previewProcessSample struct {
	PID     int    `json:"pid"`
	PPID    int    `json:"ppid"`
	RSSKiB  int64  `json:"rss_kib"`
	Command string `json:"command"`
	Args    string `json:"args,omitempty"`
}

func parsePreviewProcessSnapshot(output string) json.RawMessage {
	lines := strings.Split(output, "\n")
	processes := make([]previewProcessSample, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		rssKiB, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			continue
		}
		process := previewProcessSample{
			PID:     pid,
			PPID:    ppid,
			RSSKiB:  rssKiB,
			Command: fields[3],
		}
		if len(fields) > 4 {
			process.Args = strings.Join(fields[4:], " ")
		}
		processes = append(processes, process)
	}
	raw, err := json.Marshal(processes)
	if err != nil {
		return json.RawMessage(`[]`)
	}
	return raw
}

func uint64ToInt64(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}
