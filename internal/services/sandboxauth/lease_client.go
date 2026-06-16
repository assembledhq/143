package sandboxauth

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

// LeaseClient adapts the broker's holder-scoped API to the older
// agent.SandboxAuthServer shape used by the orchestrator and PR service.
// Each Listen call gets a unique holder ID; Close releases one holder for
// that session. This preserves refcount semantics even when an old-style
// caller opens the same session socket more than once concurrently.
type LeaseClient struct {
	broker *Broker
	owner  string
	logger zerolog.Logger

	mu      sync.Mutex
	holders map[uuid.UUID][]uuid.UUID
}

func NewLeaseClient(broker *Broker, owner string, logger zerolog.Logger) *LeaseClient {
	if owner == "" {
		owner = "local"
	}
	return &LeaseClient{
		broker:  broker,
		owner:   owner,
		logger:  logger,
		holders: make(map[uuid.UUID][]uuid.UUID),
	}
}

func (c *LeaseClient) Listen(
	ctx context.Context,
	sessionID uuid.UUID,
	run *models.Session,
	repo *models.Repository,
	orgSettings models.OrgSettings,
) (string, error) {
	if c == nil || c.broker == nil {
		return "", fmt.Errorf("sandboxauth lease client is not configured")
	}
	holderID := uuid.New()
	socketPath, err := c.broker.AcquirePrepared(ctx, sessionID, holderID, run, repo, orgSettings)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.holders[sessionID] = append(c.holders[sessionID], holderID)
	c.mu.Unlock()
	c.logger.Debug().
		Str("owner", c.owner).
		Str("session_id", sessionID.String()).
		Str("holder_id", holderID.String()).
		Msg("sandboxauth: local lease acquired")
	return socketPath, nil
}

func (c *LeaseClient) Close(sessionID uuid.UUID) {
	if c == nil || c.broker == nil {
		return
	}
	holderID, ok := c.popHolder(sessionID)
	if !ok {
		c.logger.Debug().
			Str("owner", c.owner).
			Str("session_id", sessionID.String()).
			Msg("sandboxauth: local lease close ignored; no holder recorded")
		return
	}
	if err := c.broker.ReleaseHolder(sessionID, holderID); err != nil {
		c.logger.Warn().
			Err(err).
			Str("owner", c.owner).
			Str("session_id", sessionID.String()).
			Str("holder_id", holderID.String()).
			Msg("sandboxauth: failed to release local lease")
		return
	}
	c.logger.Debug().
		Str("owner", c.owner).
		Str("session_id", sessionID.String()).
		Str("holder_id", holderID.String()).
		Msg("sandboxauth: local lease released")
}

func (c *LeaseClient) Rehydrate(
	ctx context.Context,
	sessionID uuid.UUID,
	run *models.Session,
	repo *models.Repository,
	orgSettings models.OrgSettings,
) (string, error) {
	if c == nil || c.broker == nil {
		return "", fmt.Errorf("sandboxauth lease client is not configured")
	}
	return c.broker.EnsurePrepared(ctx, sessionID, run, repo, orgSettings)
}

func (c *LeaseClient) popHolder(sessionID uuid.UUID) (uuid.UUID, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	holders := c.holders[sessionID]
	if len(holders) == 0 {
		return uuid.Nil, false
	}
	idx := len(holders) - 1
	holderID := holders[idx]
	holders = holders[:idx]
	if len(holders) == 0 {
		delete(c.holders, sessionID)
	} else {
		c.holders[sessionID] = holders
	}
	return holderID, true
}
