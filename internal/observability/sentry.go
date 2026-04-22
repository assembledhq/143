package observability

import (
	"fmt"
	"net/http"
	"time"

	"github.com/assembledhq/143/internal/version"
	"github.com/getsentry/sentry-go"
)

type RequestErrorEvent struct {
	Method       string
	Path         string
	Route        string
	RequestID    string
	Status       int
	ErrorCode    string
	ErrorMessage string
	OrgID        string
	UserID       string
}

type Reporter interface {
	CaptureRequestError(r *http.Request, event RequestErrorEvent)
	CaptureRecoveredPanic(r *http.Request, recovered any, stack []byte)
}

type SentryConfig struct {
	DSN         string
	Environment string
	Release     string
}

type SentryReporter struct {
	enabled bool
}

func NewSentryReporter(cfg SentryConfig) (*SentryReporter, error) {
	if cfg.DSN == "" {
		return &SentryReporter{enabled: false}, nil
	}

	if cfg.Environment == "" {
		cfg.Environment = "development"
	}
	if cfg.Release == "" && !version.IsDev() {
		cfg.Release = version.BuildSHA
	}

	if err := sentry.Init(sentry.ClientOptions{
		Dsn:              cfg.DSN,
		Environment:      cfg.Environment,
		Release:          cfg.Release,
		AttachStacktrace: true,
		SampleRate:       1.0,
	}); err != nil {
		return nil, fmt.Errorf("init sentry: %w", err)
	}

	return &SentryReporter{enabled: true}, nil
}

func (r *SentryReporter) Enabled() bool {
	return r != nil && r.enabled
}

func (r *SentryReporter) Flush(timeout time.Duration) bool {
	if !r.Enabled() {
		return true
	}
	return sentry.Flush(timeout)
}

func (r *SentryReporter) CaptureRequestError(req *http.Request, event RequestErrorEvent) {
	if !r.Enabled() {
		return
	}

	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetLevel(sentry.LevelError)
		scope.SetRequest(sanitizedRequest(req))
		scope.SetTag("capture_type", "request_5xx")
		scope.SetTag("request_id", event.RequestID)
		scope.SetTag("method", event.Method)
		scope.SetTag("path", event.Path)
		scope.SetTag("route", event.Route)
		scope.SetTag("status", fmt.Sprintf("%d", event.Status))
		if event.ErrorCode != "" {
			scope.SetTag("error_code", event.ErrorCode)
		}
		if event.OrgID != "" {
			scope.SetTag("org_id", event.OrgID)
		}
		if event.UserID != "" {
			scope.SetTag("user_id", event.UserID)
		}
		scope.SetContext("http", sentry.Context{
			"method":        event.Method,
			"path":          event.Path,
			"route":         event.Route,
			"status":        event.Status,
			"request_id":    event.RequestID,
			"error_code":    event.ErrorCode,
			"error_message": event.ErrorMessage,
		})
		if event.Route != "" || event.ErrorCode != "" {
			scope.SetFingerprint([]string{"request-5xx", event.Route, event.ErrorCode})
		}
		sentry.CaptureException(fmt.Errorf("http %d %s: %s", event.Status, event.ErrorCode, event.ErrorMessage))
	})
}

func (r *SentryReporter) CaptureRecoveredPanic(req *http.Request, recovered any, stack []byte) {
	if !r.Enabled() {
		return
	}

	hub := sentry.CurrentHub().Clone()
	hub.Scope().SetRequest(sanitizedRequest(req))
	hub.Scope().SetLevel(sentry.LevelFatal)
	hub.Scope().SetTag("capture_type", "panic")
	hub.Scope().SetContext("panic", sentry.Context{
		"value": fmt.Sprintf("%v", recovered),
		"stack": string(stack),
	})
	hub.RecoverWithContext(req.Context(), recovered)
}

func sanitizedRequest(req *http.Request) *http.Request {
	if req == nil {
		return nil
	}

	cloned := req.Clone(req.Context())
	cloned.Header = req.Header.Clone()
	cloned.Header.Del("Authorization")
	cloned.Header.Del("Cookie")
	cloned.Header.Del("X-CSRF-Token")
	cloned.Body = http.NoBody
	cloned.ContentLength = 0
	return cloned
}
