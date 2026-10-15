package telemetry

import (
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
)

// writeObservation counts trace callbacks, not provider executions or charged
// quota. A custom transport may emit no callbacks, and callbacks may arrive
// after the existing request summary. Never wait for them or change retries.
type writeObservation struct {
	mu       sync.Mutex
	closed   bool
	attempts uint64
	errors   uint64
}

func (o *writeObservation) wroteRequest(info httptrace.WroteRequestInfo) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}
	o.attempts++
	if info.Err != nil {
		o.errors++
	}
}

// freeze establishes the observation cutoff at request-summary emission. The
// lock makes each callback either wholly included or wholly excluded; late
// callbacks cannot revise an already emitted count.
func (o *writeObservation) freeze() (attempts, writeErrors uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closed = true
	return o.attempts, o.errors
}

func targetHostClass(requestURL *url.URL) string {
	if requestURL == nil || requestURL.Hostname() == "" {
		return "unknown"
	}
	switch strings.ToLower(requestURL.Hostname()) {
	case "api.github.com":
		return "api_github_com"
	case "github.com":
		return "github_com"
	default:
		return "other"
	}
}
