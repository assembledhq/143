package connector

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	ErrActionSignature    = errors.New("connector action signature invalid")
	ErrActionUnauthorized = errors.New("connector action unauthorized")
	ErrActionExpired      = errors.New("connector action expired")
	ErrActionClockSkew    = errors.New("connector action clock skew")
	ErrActionReplay       = errors.New("connector action replay")
	ErrConfigPushStale    = errors.New("connector config push stale")
)

type ActionRequest struct {
	OrgID       uuid.UUID       `json:"org_id"`
	ConnectorID uuid.UUID       `json:"connector_id"`
	ResourceID  uuid.UUID       `json:"resource_id"`
	Capability  string          `json:"capability"`
	RequestID   uuid.UUID       `json:"request_id"`
	IssuedAt    time.Time       `json:"issued_at"`
	ExpiresAt   time.Time       `json:"expires_at"`
	Params      json.RawMessage `json:"params,omitempty"`
}

type VerifyOptions struct {
	OrgID       uuid.UUID
	ConnectorID uuid.UUID
	ResourceIDs map[uuid.UUID]struct{}
	Now         func() time.Time
	NonceCache  *NonceCache
	ClockSkew   time.Duration
}

type SessionAuthPayload struct {
	InstanceID uuid.UUID `json:"instance_id"`
	Nonce      uuid.UUID `json:"nonce"`
	IssuedAt   time.Time `json:"issued_at"`
}

type SessionAuthVerifyOptions struct {
	InstanceID uuid.UUID
	Now        func() time.Time
	NonceCache *NonceCache
	ClockSkew  time.Duration
}

type ConfigPushVerifyOptions struct {
	OrgID       uuid.UUID
	ConnectorID uuid.UUID
	MinVersion  int64
	Now         func() time.Time
	ClockSkew   time.Duration
}

func SignActionRequest(privateKey ed25519.PrivateKey, req ActionRequest) (string, error) {
	payload, err := canonicalActionRequest(req)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload)), nil
}

func VerifyActionRequest(publicKey ed25519.PublicKey, req ActionRequest, signature string, opts VerifyOptions) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return ErrActionSignature
	}
	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return ErrActionSignature
	}
	payload, err := canonicalActionRequest(req)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, payload, sig) {
		return ErrActionSignature
	}
	if req.OrgID != opts.OrgID || req.ConnectorID != opts.ConnectorID {
		return ErrActionUnauthorized
	}
	if _, ok := opts.ResourceIDs[req.ResourceID]; !ok {
		return ErrActionUnauthorized
	}
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now().UTC()
	}
	skew := opts.ClockSkew
	if skew == 0 {
		skew = 10 * time.Second
	}
	if now.Add(skew).Before(req.IssuedAt.UTC()) {
		return ErrActionClockSkew
	}
	if !req.ExpiresAt.IsZero() && now.After(req.ExpiresAt.UTC().Add(skew)) {
		return ErrActionExpired
	}
	if opts.NonceCache != nil && !opts.NonceCache.Add(req.RequestID, now) {
		return ErrActionReplay
	}
	return nil
}

func SignSessionAuth(privateKey ed25519.PrivateKey, payload SessionAuthPayload) (string, error) {
	canonical, err := canonicalSessionAuth(payload)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, canonical)), nil
}

func VerifySessionAuth(publicKey ed25519.PublicKey, payload SessionAuthPayload, signature string, opts SessionAuthVerifyOptions) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return ErrActionSignature
	}
	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return ErrActionSignature
	}
	canonical, err := canonicalSessionAuth(payload)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, canonical, sig) {
		return ErrActionSignature
	}
	if payload.InstanceID != opts.InstanceID {
		return ErrActionUnauthorized
	}
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now().UTC()
	}
	skew := opts.ClockSkew
	if skew == 0 {
		skew = 10 * time.Second
	}
	if now.Add(skew).Before(payload.IssuedAt.UTC()) {
		return ErrActionClockSkew
	}
	if now.After(payload.IssuedAt.UTC().Add(30 * time.Second).Add(skew)) {
		return ErrActionExpired
	}
	if opts.NonceCache != nil && !opts.NonceCache.Add(payload.Nonce, now) {
		return ErrActionReplay
	}
	return nil
}

func SignConfigPush(privateKey ed25519.PrivateKey, frame ConfigPushFrame) (string, error) {
	payload, err := canonicalConfigPush(frame)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload)), nil
}

func VerifyConfigPush(publicKey ed25519.PublicKey, frame ConfigPushFrame, signature string, opts ConfigPushVerifyOptions) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return ErrActionSignature
	}
	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return ErrActionSignature
	}
	payload, err := canonicalConfigPush(frame)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, payload, sig) {
		return ErrActionSignature
	}
	if frame.OrgID != opts.OrgID || frame.ConnectorID != opts.ConnectorID {
		return ErrActionUnauthorized
	}
	if opts.MinVersion > 0 && frame.Version < opts.MinVersion {
		return ErrConfigPushStale
	}
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now().UTC()
	}
	skew := opts.ClockSkew
	if skew == 0 {
		skew = 10 * time.Second
	}
	if now.Add(skew).Before(frame.IssuedAt.UTC()) {
		return ErrActionClockSkew
	}
	if !frame.ExpiresAt.IsZero() && now.After(frame.ExpiresAt.UTC().Add(skew)) {
		return ErrActionExpired
	}
	return nil
}

func canonicalActionRequest(req ActionRequest) ([]byte, error) {
	if req.OrgID == uuid.Nil || req.ConnectorID == uuid.Nil || req.ResourceID == uuid.Nil || req.RequestID == uuid.Nil {
		return nil, fmt.Errorf("%w: missing scoped identifier", ErrActionUnauthorized)
	}
	if req.Capability == "" {
		return nil, fmt.Errorf("%w: capability is required", ErrActionUnauthorized)
	}
	req.IssuedAt = req.IssuedAt.UTC()
	req.ExpiresAt = req.ExpiresAt.UTC()
	if len(req.Params) == 0 {
		req.Params = nil
	}
	return json.Marshal(req)
}

func canonicalSessionAuth(payload SessionAuthPayload) ([]byte, error) {
	if payload.InstanceID == uuid.Nil || payload.Nonce == uuid.Nil {
		return nil, fmt.Errorf("%w: missing session auth identifier", ErrActionUnauthorized)
	}
	payload.IssuedAt = payload.IssuedAt.UTC()
	return json.Marshal(payload)
}

func canonicalConfigPush(frame ConfigPushFrame) ([]byte, error) {
	if frame.OrgID == uuid.Nil || frame.ConnectorID == uuid.Nil {
		return nil, fmt.Errorf("%w: missing config push scope", ErrActionUnauthorized)
	}
	if frame.Version <= 0 {
		return nil, fmt.Errorf("%w: config push version is required", ErrActionUnauthorized)
	}
	frame.IssuedAt = frame.IssuedAt.UTC()
	frame.ExpiresAt = frame.ExpiresAt.UTC()
	resources := append([]ConfigPushResource(nil), frame.Resources...)
	sort.Slice(resources, func(i, j int) bool {
		return resources[i].ID.String() < resources[j].ID.String()
	})
	for i := range resources {
		if resources[i].ID == uuid.Nil {
			return nil, fmt.Errorf("%w: config push resource id is required", ErrActionUnauthorized)
		}
		if len(resources[i].Config) == 0 {
			resources[i].Config = nil
		}
	}
	frame.Resources = resources
	return json.Marshal(frame)
}

type NonceCache struct {
	mu     sync.Mutex
	ttl    time.Duration
	nonces map[uuid.UUID]time.Time
}

func NewNonceCache(ttl time.Duration) *NonceCache {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &NonceCache{ttl: ttl, nonces: make(map[uuid.UUID]time.Time)}
}

func (c *NonceCache) Add(id uuid.UUID, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for nonce, expiresAt := range c.nonces {
		if !expiresAt.After(now) {
			delete(c.nonces, nonce)
		}
	}
	if _, ok := c.nonces[id]; ok {
		return false
	}
	c.nonces[id] = now.Add(c.ttl)
	return true
}
