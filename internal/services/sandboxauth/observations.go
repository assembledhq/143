package sandboxauth

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/github/identity"
	"github.com/assembledhq/143/internal/services/github/telemetry"
)

type observationBinding struct {
	metadata  telemetry.RequestMetadata
	expires   time.Time
	reusable  bool
	tokenHash [32]byte
}

type observationScope struct {
	metadata telemetry.RequestMetadata
	mu       sync.Mutex
	bindings map[string]observationBinding
}

func newObservationScope(sessionID uuid.UUID, run *models.Session, repo *models.Repository) *observationScope {
	scope := &observationScope{metadata: telemetry.RequestMetadata{Kind: telemetry.RequestKindAPI, Caller: "sandbox_token", SessionID: sessionID.String()}, bindings: make(map[string]observationBinding)}
	if run != nil {
		scope.metadata.OrgID = run.OrgID.String()
	}
	if repo != nil {
		scope.metadata.Repository = repo.FullName
	}
	return scope
}

func (s *observationScope) bind(res *identity.Resolution, callers ...string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, binding := range s.bindings {
		if !binding.expires.After(now) {
			delete(s.bindings, id)
		}
	}
	metadata := s.metadata
	metadata.Caller = "sandbox_gh"
	reusable := len(callers) > 0 && callers[0] == "sandbox_tools"
	if reusable {
		metadata.Caller = "sandbox_tools"
	}
	metadata.AuthType = telemetry.AuthTypeUser
	if res.Source == identity.SourceApp {
		metadata.AuthType = telemetry.AuthTypeAppInstallation
		metadata.InstallationID = res.InstallationID
	}
	expires := now.Add(time.Hour)
	if !res.ExpiresAt.IsZero() && res.ExpiresAt.Before(expires) {
		expires = res.ExpiresAt
	}
	tokenHash := sha256.Sum256([]byte(res.Token))
	if reusable {
		for id, binding := range s.bindings {
			if binding.reusable && binding.tokenHash == tokenHash && binding.metadata == metadata {
				binding.expires = expires
				s.bindings[id] = binding
				return id
			}
		}
	}
	if len(s.bindings) >= 256 {
		return ""
	}
	id := uuid.NewString()
	s.bindings[id] = observationBinding{metadata: metadata, expires: expires, reusable: reusable, tokenHash: tokenHash}
	return id
}

func (s *observationScope) record(ctx context.Context, logger zerolog.Logger, req Request) error {
	if len(req.Events) > 32 {
		return errors.New("too many GitHub observations")
	}
	headers := make([]http.Header, len(req.Events))
	for i, event := range req.Events {
		if event.Status != 0 && (event.Status < 100 || event.Status > 599) || event.DurationMS < 0 || event.DurationMS > 3600000 || len(event.Route) > 512 || len(event.Header) > 6 {
			return errors.New("invalid GitHub observation")
		}
		switch event.Method {
		case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		default:
			return errors.New("invalid GitHub method")
		}
		headers[i] = observationHeaders(event.Header)
	}
	s.mu.Lock()
	binding, ok := s.bindings[req.ObservationID]
	if req.Finish {
		delete(s.bindings, req.ObservationID)
	}
	s.mu.Unlock()
	if !ok || !binding.expires.After(time.Now()) {
		return errors.New("unknown GitHub observation binding")
	}
	for i, event := range req.Events {
		metadata := binding.metadata
		if event.UnknownPrincipal {
			metadata.AuthType = telemetry.AuthTypeUnknown
			metadata.InstallationID = 0
			metadata.PrincipalUnresolved = true
		}
		telemetry.RecordExternalRequest(ctx, logger, metadata, event.Method, event.Route, event.Status, headers[i], time.Duration(event.DurationMS)*time.Millisecond)
	}
	return nil
}

func observationHeaders(values map[string]string) http.Header {
	headers := make(http.Header)
	for key, value := range values {
		switch key {
		case "X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Used", "X-RateLimit-Reset":
			n, err := strconv.ParseInt(value, 10, 64)
			if err == nil && n >= 0 && n <= 1e12 {
				headers.Set(key, strconv.FormatInt(n, 10))
			}
		case "X-RateLimit-Resource":
			switch value {
			case "core", "graphql", "search", "code_search", "integration_manifest":
				headers.Set(key, value)
			}
		case "Retry-After":
			n, err := strconv.ParseInt(value, 10, 32)
			if err == nil && n >= 0 {
				headers.Set(key, strconv.FormatInt(n, 10))
			}
		}
	}
	return headers
}
