package sandboxauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/models"
)

const (
	remoteBrokerTokenTTL            = 30 * time.Second
	remoteBrokerHTTPTimeout         = 30 * time.Second
	remoteBrokerReleaseDelay        = 5 * time.Second
	remoteBrokerAcquireAttempts     = 5
	remoteBrokerAcquireInitialDelay = 100 * time.Millisecond
	remoteBrokerAcquireMaxDelay     = time.Second
)

type RemoteBrokerClientConfig struct {
	BaseURL             string
	NodeID              string
	HolderID            uuid.UUID
	Keyring             auth.PreviewTokenKeyring
	HTTPClient          *http.Client
	AcquireRetryBackoff func(attempt int) time.Duration
	Logger              zerolog.Logger
}

// RemoteBrokerClient is used inside durable session executor containers. It
// never opens a local Unix listener; it acquires/releases a holder lease from
// the long-lived worker process that owns the host socket directory.
type RemoteBrokerClient struct {
	baseURL    string
	nodeID     string
	holderID   uuid.UUID
	keyring    auth.PreviewTokenKeyring
	httpClient *http.Client
	logger     zerolog.Logger
	backoff    func(attempt int) time.Duration

	mu       sync.Mutex
	acquired map[uuid.UUID]remoteBrokerAcquiredSession
}

type remoteBrokerAcquiredSession struct {
	orgID uuid.UUID
	count int
}

func NewRemoteBrokerClient(cfg RemoteBrokerClientConfig) *RemoteBrokerClient {
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: remoteBrokerHTTPTimeout}
	}
	holderID := cfg.HolderID
	if holderID == uuid.Nil {
		holderID = uuid.New()
	}
	backoff := cfg.AcquireRetryBackoff
	if backoff == nil {
		backoff = defaultRemoteBrokerAcquireBackoff
	}
	return &RemoteBrokerClient{
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		nodeID:     cfg.NodeID,
		holderID:   holderID,
		keyring:    cfg.Keyring,
		httpClient: httpClient,
		logger:     cfg.Logger,
		backoff:    backoff,
		acquired:   make(map[uuid.UUID]remoteBrokerAcquiredSession),
	}
}

func (c *RemoteBrokerClient) Listen(
	ctx context.Context,
	sessionID uuid.UUID,
	run *models.Session,
	_ *models.Repository,
	_ models.OrgSettings,
) (string, error) {
	if c == nil {
		return "", fmt.Errorf("sandboxauth remote broker client is nil")
	}
	if c.baseURL == "" {
		return "", fmt.Errorf("sandboxauth remote broker client base url is empty")
	}
	if c.nodeID == "" {
		return "", fmt.Errorf("sandboxauth remote broker client node id is empty")
	}
	if !c.keyring.Configured() {
		return "", fmt.Errorf("sandboxauth remote broker client keyring is not configured")
	}
	if run == nil {
		return "", fmt.Errorf("sandboxauth remote broker client requires session")
	}
	if sessionID == uuid.Nil || run.ID != sessionID {
		return "", fmt.Errorf("sandboxauth remote broker client session mismatch: run=%s requested=%s", run.ID, sessionID)
	}
	if run.OrgID == uuid.Nil {
		return "", fmt.Errorf("sandboxauth remote broker client requires org id")
	}

	body := BrokerAcquireRequest{
		OrgID:     run.OrgID,
		SessionID: sessionID,
		HolderID:  c.holderID,
	}
	resp, err := c.doAcquire(ctx, run.OrgID, sessionID, body)
	if err != nil {
		return "", err
	}
	var payload models.SingleResponse[BrokerAcquireResponse]
	if err := json.Unmarshal(resp, &payload); err != nil {
		return "", fmt.Errorf("sandboxauth remote broker acquire: decode response: %w", err)
	}
	if payload.Data.SocketPath == "" {
		return "", fmt.Errorf("sandboxauth remote broker acquire: worker returned empty socket path")
	}
	c.mu.Lock()
	current := c.acquired[sessionID]
	current.orgID = run.OrgID
	current.count++
	c.acquired[sessionID] = current
	c.mu.Unlock()
	return payload.Data.SocketPath, nil
}

func (c *RemoteBrokerClient) Close(sessionID uuid.UUID) {
	if c == nil {
		return
	}
	orgID, ok := c.consumeAcquire(sessionID)
	if !ok {
		c.logger.Debug().Str("session_id", sessionID.String()).Msg("sandboxauth remote broker close ignored; session was not acquired")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), remoteBrokerReleaseDelay)
	defer cancel()
	body := BrokerReleaseRequest{
		OrgID:     orgID,
		SessionID: sessionID,
		HolderID:  c.holderID,
	}
	if _, err := c.do(ctx, http.MethodPost, "/internal/sandbox-auth/release", BrokerActionRelease, orgID, sessionID, body); err != nil {
		c.logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("sandboxauth remote broker release failed")
	}
}

func (c *RemoteBrokerClient) consumeAcquire(sessionID uuid.UUID) (uuid.UUID, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	current := c.acquired[sessionID]
	if current.count <= 0 {
		return uuid.Nil, false
	}
	current.count--
	if current.count == 0 {
		delete(c.acquired, sessionID)
	} else {
		c.acquired[sessionID] = current
	}
	return current.orgID, true
}

func (c *RemoteBrokerClient) doAcquire(ctx context.Context, orgID, sessionID uuid.UUID, body BrokerAcquireRequest) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= remoteBrokerAcquireAttempts; attempt++ {
		resp, err := c.do(ctx, http.MethodPost, "/internal/sandbox-auth/acquire", BrokerActionAcquire, orgID, sessionID, body)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if attempt == remoteBrokerAcquireAttempts || !retryableRemoteBrokerAcquireError(err) {
			break
		}
		delay := c.backoff(attempt)
		if delay <= 0 {
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
}

func (c *RemoteBrokerClient) do(
	ctx context.Context,
	method string,
	path string,
	action string,
	orgID uuid.UUID,
	sessionID uuid.UUID,
	body any,
) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("sandboxauth remote broker %s: marshal body: %w", action, err)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("sandboxauth remote broker %s: build request: %w", action, err)
	}
	req.Header.Set("Content-Type", "application/json")
	token, err := c.keyring.Generate(auth.PreviewTokenClaims{
		OrgID:        orgID,
		TargetNodeID: c.nodeID,
		SessionID:    &sessionID,
		Action:       action,
		ExpiresAt:    time.Now().Add(remoteBrokerTokenTTL),
	})
	if err != nil {
		return nil, fmt.Errorf("sandboxauth remote broker %s: sign token: %w", action, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sandboxauth remote broker %s: request worker: %w", action, err)
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if readErr != nil {
		return nil, fmt.Errorf("sandboxauth remote broker %s: read response: %w", action, readErr)
	}
	if resp.StatusCode >= 400 {
		var errResp models.ErrorResponse
		if err := json.Unmarshal(data, &errResp); err == nil && errResp.Error.Code != "" {
			return nil, &RemoteBrokerRequestError{
				Action:     action,
				StatusCode: resp.StatusCode,
				Code:       errResp.Error.Code,
				Message:    errResp.Error.Message,
			}
		}
		return nil, &RemoteBrokerRequestError{
			Action:     action,
			StatusCode: resp.StatusCode,
			Message:    strings.TrimSpace(string(data)),
		}
	}
	return data, nil
}

type RemoteBrokerRequestError struct {
	Action     string
	StatusCode int
	Code       string
	Message    string
}

func (e *RemoteBrokerRequestError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("sandboxauth remote broker %s failed with %d (%s): %s", e.Action, e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("sandboxauth remote broker %s failed with %d: %s", e.Action, e.StatusCode, e.Message)
}

func retryableRemoteBrokerAcquireError(err error) bool {
	var reqErr *RemoteBrokerRequestError
	if errors.As(err, &reqErr) {
		return reqErr.StatusCode == http.StatusTooManyRequests || reqErr.StatusCode >= 500
	}
	return true
}

func defaultRemoteBrokerAcquireBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		return remoteBrokerAcquireInitialDelay
	}
	delay := remoteBrokerAcquireInitialDelay << (attempt - 1)
	if delay > remoteBrokerAcquireMaxDelay {
		return remoteBrokerAcquireMaxDelay
	}
	return delay
}
