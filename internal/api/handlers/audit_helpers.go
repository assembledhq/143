package handlers

import (
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

// emitUserAudit is a fire-and-forget helper for emitting audit log entries
// from HTTP handlers. It extracts request context (IP, user-agent, request ID)
// automatically. If the emitter is nil, it's a no-op.
func emitUserAudit(emitter *db.AuditEmitter, r *http.Request, action models.AuditAction, resourceType models.AuditResourceType, resourceID *string, details json.RawMessage) {
	if emitter == nil {
		return
	}
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		emitAPIAudit(emitter, r, action, resourceType, resourceID, nil, nil, details)
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())

	params := db.UserActionParams{
		OrgID:        orgID,
		UserID:       user.ID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Details:      details,
	}

	if reqID := chiMiddleware.GetReqID(r.Context()); reqID != "" {
		params.RequestID = &reqID
	}
	if ip := parseClientIP(r); ip != nil {
		params.IPAddress = ip
	}
	if ua := r.UserAgent(); ua != "" {
		params.UserAgent = &ua
	}

	emitter.EmitUserAction(r.Context(), params)
}

// emitUserAuditWithSession is like emitUserAudit but also sets the session correlation ID.
func emitUserAuditWithSession(emitter *db.AuditEmitter, r *http.Request, action models.AuditAction, resourceType models.AuditResourceType, resourceID *string, sessionID *uuid.UUID, projectID *uuid.UUID, details json.RawMessage) {
	if emitter == nil {
		return
	}
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		emitAPIAudit(emitter, r, action, resourceType, resourceID, sessionID, projectID, details)
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())

	params := db.UserActionParams{
		OrgID:        orgID,
		UserID:       user.ID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Details:      details,
		SessionID:    sessionID,
		ProjectID:    projectID,
	}

	if reqID := chiMiddleware.GetReqID(r.Context()); reqID != "" {
		params.RequestID = &reqID
	}
	if ip := parseClientIP(r); ip != nil {
		params.IPAddress = ip
	}
	if ua := r.UserAgent(); ua != "" {
		params.UserAgent = &ua
	}

	emitter.EmitUserAction(r.Context(), params)
}

// userAuditEntry is a per-row payload for emitUserAuditsWithSession. The
// shared per-request fields (orgID, userID, IP, request ID, user agent) are
// pulled once at dispatch time, so callers only have to supply the bits that
// vary across audit rows.
type userAuditEntry struct {
	Action       models.AuditAction
	ResourceType models.AuditResourceType
	ResourceID   *string
	SessionID    *uuid.UUID
	ProjectID    *uuid.UUID
	Details      json.RawMessage
}

// emitUserAuditsWithSession is the batched form of emitUserAuditWithSession:
// it extracts request context once and writes all entries in a single DB
// round-trip via AuditEmitter.EmitUserActions. No-op when emitter or user
// context is missing, mirroring the single-emit helpers.
func emitUserAuditsWithSession(emitter *db.AuditEmitter, r *http.Request, entries []userAuditEntry) {
	if emitter == nil || len(entries) == 0 {
		return
	}
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		entriesForAPI := make([]apiAuditEntry, 0, len(entries))
		for _, e := range entries {
			entriesForAPI = append(entriesForAPI, apiAuditEntry(e))
		}
		emitAPIAudits(emitter, r, entriesForAPI)
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())

	var requestID *string
	if reqID := chiMiddleware.GetReqID(r.Context()); reqID != "" {
		requestID = &reqID
	}
	ipAddress := parseClientIP(r)
	var userAgent *string
	if ua := r.UserAgent(); ua != "" {
		userAgent = &ua
	}

	paramsList := make([]db.UserActionParams, 0, len(entries))
	for _, e := range entries {
		paramsList = append(paramsList, db.UserActionParams{
			OrgID:        orgID,
			UserID:       user.ID,
			Action:       e.Action,
			ResourceType: e.ResourceType,
			ResourceID:   e.ResourceID,
			Details:      e.Details,
			RequestID:    requestID,
			IPAddress:    ipAddress,
			UserAgent:    userAgent,
			SessionID:    e.SessionID,
			ProjectID:    e.ProjectID,
		})
	}
	emitter.EmitUserActions(r.Context(), paramsList)
}

type apiAuditEntry struct {
	Action       models.AuditAction
	ResourceType models.AuditResourceType
	ResourceID   *string
	SessionID    *uuid.UUID
	ProjectID    *uuid.UUID
	Details      json.RawMessage
}

func emitAPIAudit(emitter *db.AuditEmitter, r *http.Request, action models.AuditAction, resourceType models.AuditResourceType, resourceID *string, sessionID *uuid.UUID, projectID *uuid.UUID, details json.RawMessage) {
	emitAPIAudits(emitter, r, []apiAuditEntry{{
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		SessionID:    sessionID,
		ProjectID:    projectID,
		Details:      details,
	}})
}

func emitAPIAudits(emitter *db.AuditEmitter, r *http.Request, entries []apiAuditEntry) {
	if emitter == nil || len(entries) == 0 {
		return
	}
	client := middleware.APIClientFromContext(r.Context())
	token := middleware.APITokenFromContext(r.Context())
	if client == nil {
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	var tokenID *uuid.UUID
	if token != nil {
		tokenID = &token.ID
	}
	var requestID *string
	if reqID := chiMiddleware.GetReqID(r.Context()); reqID != "" {
		requestID = &reqID
	}
	ipAddress := parseClientIP(r)
	var userAgent *string
	if ua := r.UserAgent(); ua != "" {
		userAgent = &ua
	}
	for _, e := range entries {
		emitter.EmitAPIAction(r.Context(), db.APIActionParams{
			OrgID:        orgID,
			APIClientID:  client.ID,
			APITokenID:   tokenID,
			Action:       e.Action,
			ResourceType: e.ResourceType,
			ResourceID:   e.ResourceID,
			Details:      e.Details,
			RequestID:    requestID,
			IPAddress:    ipAddress,
			UserAgent:    userAgent,
			SessionID:    e.SessionID,
			ProjectID:    e.ProjectID,
		})
	}
}

// parseClientIP extracts the client IP from the request as a netip.Prefix
// suitable for PostgreSQL inet storage.
func parseClientIP(r *http.Request) *netip.Prefix {
	// Try X-Forwarded-For first (reverse proxy), then RemoteAddr.
	// X-Forwarded-For may contain a comma-separated list; use the first (client) IP.
	ip := r.Header.Get("X-Forwarded-For")
	if ip != "" {
		ip = strings.TrimSpace(strings.SplitN(ip, ",", 2)[0])
	} else {
		ip = r.RemoteAddr
	}
	// Strip port if present.
	if host, err := netip.ParseAddrPort(ip); err == nil {
		prefix := netip.PrefixFrom(host.Addr(), host.Addr().BitLen())
		return &prefix
	}
	if addr, err := netip.ParseAddr(ip); err == nil {
		prefix := netip.PrefixFrom(addr, addr.BitLen())
		return &prefix
	}
	return nil
}
