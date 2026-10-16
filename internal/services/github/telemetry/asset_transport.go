package telemetry

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

const CallerVisualAsset = "visual_asset"

// NewGitHubAssetTransport observes authenticated GitHub attachment exchanges
// without changing the base transport's routing, admission or body handling.
// Callers must supply the installation downloader's safe base; nil is rejected.
// The api event kind identifies the existing native HTTP telemetry stream; it
// does not establish that an attachment consumes any particular quota resource.
func NewGitHubAssetTransport(base http.RoundTripper, logger zerolog.Logger) http.RoundTripper {
	return &assetTransport{base: base, observer: &transport{
		logger: logger, now: time.Now, credentials: DefaultCredentialRegistry(), caller: CallerVisualAsset,
	}}
}

type assetTransport struct {
	base     http.RoundTripper
	observer *transport
}

func (t *assetTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.base == nil {
		return nil, errors.New("GitHub asset observer requires a base transport")
	}
	if !authenticatedGitHubAssetRequest(req) {
		return t.base.RoundTrip(req)
	}
	metadata, _ := RequestMetadataFromContext(req.Context())
	metadata.Kind = RequestKindAPI
	metadata.AuthType = AuthTypeAppInstallation
	metadata.Caller = CallerVisualAsset
	metadata.SyncReason = boundedDimension(metadata.SyncReason, 64)
	metadata.PrincipalUnresolved = metadata.InstallationID <= 0
	req = req.WithContext(WithRequestMetadata(req.Context(), metadata))
	identity, _ := t.observer.credentials.credentialFromAuthorization(req.Header)
	observation := requestObservation{
		credential: identity, correlationStatus: credentialCorrelationStatus(identity, metadata, req.URL),
		timingSource: "host_transport", writes: &writeObservation{},
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{WroteRequest: observation.writes.wroteRequest}))
	observation.startedAt = t.observer.now()
	response, err := t.base.RoundTrip(req)
	observation.headersAt = t.observer.now()
	if response != nil {
		if providerDate, parseErr := http.ParseTime(response.Header.Get("Date")); parseErr == nil {
			observation.providerDate = providerDate
		}
		observation.requestID = boundedDimension(response.Header.Get("X-GitHub-Request-Id"), 128)
	}
	finish := func(observationErr error) {
		t.observer.logRequest(req, response, observationErr, nil, nil, false, observation, t.observer.now())
	}
	if err != nil || response == nil {
		// Transport errors can contain the full signed URL. Preserve the actual
		// error for the caller while logging only a fixed diagnostic category.
		finish(errors.New("GitHub asset transport failed"))
		return response, err
	}
	if response.Body == nil {
		finish(nil)
		return response, err
	}
	response.Body = &assetResponseBody{ReadCloser: response.Body, finish: finish}
	return response, err
}

func (t *assetTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func authenticatedGitHubAssetRequest(req *http.Request) bool {
	if req == nil || req.URL == nil || req.URL.User != nil || req.Method != http.MethodGet ||
		!strings.EqualFold(req.URL.Scheme, "https") || !strings.EqualFold(req.URL.Hostname(), "github.com") ||
		!strings.HasPrefix(req.URL.EscapedPath(), "/user-attachments/assets/") {
		return false
	}
	var values []string
	for name, candidate := range req.Header {
		if strings.EqualFold(name, "Authorization") {
			values = append(values, candidate...)
		}
	}
	if len(values) != 1 {
		return false
	}
	parts := strings.Fields(values[0])
	return len(parts) == 2 && strings.EqualFold(parts[0], "bearer")
}

// assetResponseBody never captures or drains bytes. In particular, closing a
// redirect or error response must not trigger an additional read. Completion is
// observed at EOF, a read failure, or Close; later activity cannot revise that
// summary's transport-write cutoff.
type assetResponseBody struct {
	io.ReadCloser
	finish      func(error)
	observeOnce sync.Once
}

func (b *assetResponseBody) Read(data []byte) (int, error) {
	n, err := b.ReadCloser.Read(data)
	if err != nil {
		var observationErr error
		if err != io.EOF {
			observationErr = errors.New("GitHub asset response read failed")
		}
		b.observeOnce.Do(func() { b.finish(observationErr) })
	}
	return n, err
}

func (b *assetResponseBody) Close() error {
	err := b.ReadCloser.Close()
	var observationErr error
	if err != nil {
		observationErr = errors.New("GitHub asset response close failed")
	}
	b.observeOnce.Do(func() { b.finish(observationErr) })
	return err
}
