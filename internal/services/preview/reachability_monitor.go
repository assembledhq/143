package preview

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type ReachabilityRuntimeStore interface {
	ListActivePreviewRuntimesForReachability(ctx context.Context, limit int) ([]models.PreviewRuntime, error)
	MarkPreviewRuntimeUnreachable(ctx context.Context, orgID, previewID, runtimeID uuid.UUID, reason string) (bool, error)
}

type ReachabilityDialFunc func(ctx context.Context, network, address string) error

type ReachabilityMonitorConfig struct {
	Store       ReachabilityRuntimeStore
	Logger      zerolog.Logger
	Interval    time.Duration
	Timeout     time.Duration
	Limit       int
	DialContext ReachabilityDialFunc
}

type ReachabilityMonitor struct {
	store       ReachabilityRuntimeStore
	logger      zerolog.Logger
	interval    time.Duration
	timeout     time.Duration
	limit       int
	dialContext ReachabilityDialFunc
}

func NewReachabilityMonitor(cfg ReachabilityMonitorConfig) *ReachabilityMonitor {
	interval := cfg.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	limit := cfg.Limit
	if limit <= 0 {
		limit = 500
	}
	dialContext := cfg.DialContext
	if dialContext == nil {
		dialer := &net.Dialer{Timeout: timeout}
		dialContext = func(ctx context.Context, network, address string) error {
			conn, err := dialer.DialContext(ctx, network, address)
			if err != nil {
				return err
			}
			return conn.Close()
		}
	}
	return &ReachabilityMonitor{
		store:       cfg.Store,
		logger:      cfg.Logger,
		interval:    interval,
		timeout:     timeout,
		limit:       limit,
		dialContext: dialContext,
	}
}

func (m *ReachabilityMonitor) Start(ctx context.Context) {
	if m == nil || m.store == nil {
		return
	}
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.probeOnce(ctx)
		}
	}
}

func (m *ReachabilityMonitor) probeOnce(ctx context.Context) {
	runtimes, err := m.store.ListActivePreviewRuntimesForReachability(ctx, m.limit)
	if err != nil {
		m.logger.Warn().Err(err).Msg("preview reachability monitor failed to list runtimes")
		return
	}
	for _, runtime := range runtimes {
		address, err := previewEndpointAddress(runtime.EndpointURL)
		if err != nil {
			m.markUnreachable(ctx, runtime, fmt.Errorf("invalid endpoint URL: %w", err))
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, m.timeout)
		err = m.dialContext(probeCtx, "tcp", address)
		cancel()
		if err != nil {
			m.markUnreachable(ctx, runtime, err)
		}
	}
}

func (m *ReachabilityMonitor) markUnreachable(ctx context.Context, runtime models.PreviewRuntime, cause error) {
	reason := "preview reachability probe failed"
	if cause != nil {
		reason += ": " + cause.Error()
	}
	updated, err := m.store.MarkPreviewRuntimeUnreachable(ctx, runtime.OrgID, runtime.PreviewInstanceID, runtime.ID, reason)
	if err != nil {
		m.logger.Warn().Err(err).
			Str("preview_id", runtime.PreviewInstanceID.String()).
			Str("runtime_id", runtime.ID.String()).
			Str("endpoint_url", runtime.EndpointURL).
			Msg("preview reachability monitor failed to mark runtime unreachable")
		return
	}
	if updated {
		m.logger.Warn().
			Str("preview_id", runtime.PreviewInstanceID.String()).
			Str("runtime_id", runtime.ID.String()).
			Str("endpoint_url", runtime.EndpointURL).
			Msg("preview reachability monitor marked runtime unreachable")
	}
}

func previewEndpointAddress(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("missing host")
	}
	return parsed.Host, nil
}
